# Durable Background Tool And Shell Execution

## Status

Implemented on `feat/task-ownership-runtime`.

## Purpose

`adk/task/tool` lets explicitly capable tools launch external operations
under the shared Task lifecycle. `adk/task/shell` adapts remote
shell backends that can recover a logical command after an Eino Worker changes.

The integration does not parse arbitrary tool output and does not expose an
external service operation ID to the model. A published background operation
returns a framework-owned `launch_result` containing its Eino task ID. A
synchronous operation returns `foreground_result`, whether it ran directly or
completed as a deferred Task before publication.

## Capability Classes

Two persisted executor keys prevent runtime capability drift:

| Executor key | Lease expiry | Drain | Backend requirement |
| --- | --- | --- | --- |
| `eino.dev/background-tool` | fail | no | process may observe and stop the operation |
| `eino.dev/recoverable-background-tool` | retry | yield | durable task-ID-keyed recovery |

A plain `Tool` must not be retried after Worker loss. A `RecoverableTool` must
satisfy all of these requirements:

1. `Start(TaskID)` is idempotent for the full task lifetime.
2. A fresh adapter instance can locate the same logical operation by TaskID.
3. Status and stop address that same operation.
4. Recovery state is stored outside the Manager and is visible to eligible Workers.
5. Replayable updates retain stable source IDs and identical payload bytes.
6. Backend state is fenced when stale Eino attempts could write concurrently.
7. Terminal retention and cleanup are defined by the backend owner.

A short request-deduplication cache or an adapter mapping written after an unkeyed
side effect is insufficient. Backends that cannot meet these requirements must use
the plain executor.

### Durable Migration Matrix

Persisted executor keys are protocol identifiers, not implementation names:

| Domain | Persisted key | Payload | Migration rule |
| --- | --- | --- | --- |
| Plain Tool | `eino.dev/background-tool` | v1 | Key is retained; existing tasks continue on upgraded Workers. |
| Recoverable Tool | `eino.dev/recoverable-background-tool` | v1 | Key is retained; existing tasks continue on upgraded Workers. |
| Legacy Sub-agent | `eino.dev/subagent` | v4 | New Workers must not claim it. Old Workers must drain every non-terminal v4 task before retirement. |
| Task runtime Sub-agent | `eino.dev/task-subagent` | v1 | New tasks use this key; v1 is scoped to this key and is not a decoder for legacy v4. |

There is intentionally no mixed-version decoder for the two Sub-agent protocols.
Deployment must stop creation of legacy tasks, keep old Workers available until
their v4 backlog is empty, and only then remove those Workers.

## Launch Ordering

The managed wrapper performs:

```text
validate serialized arguments
  -> allocate Eino task ID
  -> reserve an optional deterministic derived output path
  -> persist pending Spec as OnCreate or Deferred
  -> task-first coordinator calls Manager.Execute
  -> Store claims and fences an attempt
  -> executor calls Start(TaskID)
  -> persist updates before live projection
  -> complete internally or publish at the foreground policy boundary
```

The persisted payload contains a version, tool name, tool-call correlation ID, and
serialized arguments. Clients, credentials, callbacks, and live run handles are not
serialized.

The wrapper may use a parent Runner session or an explicit `SessionID` as its
notification route. An empty route disables session-routed lifecycle
notifications. When enabled, terminal lifecycle notifications continue to use
the existing outbox and session inbox. Progress records never enqueue
notifications and never advance task lifecycle version.

## Foreground Projection

Invokable tools return one JSON record. Streamable tools return newline-delimited
records, one complete record per chunk:

```json
{"type":"update","update":{"source_id":"event-1","kind":"stdout","data":"aGVsbG8="}}
{"type":"launch_result","task_id":"task_abc","status":"running"}
```

The final record is `launch_result` only when the framework publishes a
background handle, and `foreground_result` for a synchronous wire result.
`type` and `status` are framework-owned. A `launch_result` contains `task_id`,
which is the model-facing handle for `task_output` and `task_stop`. A
`foreground_result` may come from direct execution or a deferred, Manager-owned
Task that completed inside the foreground observation window without being
published; it does not promise a model-facing `task_id` and must not be used as
a control handle. Update records also omit `task_id`. Registration-specific
completed output is nested under `output`. Concatenating stream chunks therefore
remains valid NDJSON and cannot merge raw stdout with lifecycle JSON.

Auto-backgroundable work is Manager-owned from the beginning. The foreground
caller observes a live projection of that task; it never starts an Attempt 0
operation and never transfers a running handle between owners.

The default policy waits up to the configured foreground timeout, then allows
automatic backgrounding. The task starts with `PublicationDeferred`. If it
finishes first, the caller receives an ordinary foreground result and no
lifecycle notification is emitted. If timeout wins, `Publish` atomically changes
publication to `PublicationOnBackground`, emits `TaskBackgrounded`, and closes
only the projection. Task status remains `running`, the attempt and lease are
unchanged, and update persistence continues.

Explicit background launch uses the same Manager execution path with
`PublicationOnCreate`; only the initial publication and projection behavior
differ.

Foreground observation resolves at a boundary rather than only at a terminal
state. `Execution.Boundary()` closes for `WaitingInput`, `Completed`, `Failed`,
or `Canceled`, and `Execution.WaitBoundary()` returns the snapshot responsible
for that boundary. `WaitingInput` ends the current projection so the wrapper can
emit an interrupt/input request, but it remains a non-terminal durable state.
Both `Boundary()` and `Timeout()` expose stable single-shot channels.

If another Worker wins the initial claim, foreground coordination reloads the
authoritative running or terminal task and returns a canonical launch result without
pretending that a local live projection exists.

## Recovery And Yield

Recovery is Manager-orchestrated and executor-performed:

```text
Worker lists pending task
  -> Manager claims a new attempt
  -> managed-tool executor resolves ToolName
  -> executor calls Recover(TaskID, boundary checkpoint)
  -> implementation loads its own durable running state
```

`ExecutionActionYield` is distinct from lifecycle status. It commits:

```text
running -> pending
```

Yield retains an optional opaque checkpoint or durable-state reference, ends the
active attempt, emits no lifecycle notification, and permits a later Store-authorized
claim.

The checkpoint is an optional recovery hint, not the operation identity. `Recover`
may ignore it, use it, or reject it directly; no separate adapter-level checkpoint
validation capability is required.

During `Manager.Close`, a recoverable executor receives `ControlDrain`, cancels local
`Run.Wait` observation, optionally reads `Checkpointer.Checkpoint`, and yields. It
does not call `Run.Stop`; the external operation continues. Plain executors are not
drainable and use bounded completion/cancellation.

Unexpected process loss is resolved by Store lease expiry. Retry-capable tasks return
to pending, and a polling Worker dispatches the next recovery attempt. The parent
session is not involved.

## Worker Dispatch

A host scheduler polls `Manager.ListPending` for configured executor keys and
dispatches through `Manager.Execute`. Duplicate listings and claim races are
resolved by Store authorization. Yielded and lease-expired tasks become pending
and are eligible for a later attempt.

## Incremental Output

Typed events are serialized by executor-specific `TaskEventPersister`
implementations rather than by `ExecutionRuntime`. A persister receives the
original event plus an optional persistence-owned stream copy:

```go
type TaskEventEnvelope[E, Chunk any] struct {
    Event  E
    Stream *schema.StreamReader[Chunk]
}
```

The runtime creates a tracking `TaskEventWriter` bound to
`(TaskID, Attempt, EventID)`. The persister returns only an error; the framework
collects and validates every successful append and returns the persisted prefix
even when the persister later fails. A persister may serialize one event into
multiple durable parts:

```go
type TaskEventPartInput struct {
    PartID string
    Data   []byte
    Final  bool
}

type TaskEventPersistResult struct {
    Scope   TaskEventScope
    Appends []*AppendTaskEventResult
}

type TaskEventPart struct {
    TaskID    string
    EventID   string
    PartID    string
    Data      []byte
    Final     bool
    CreatedAt time.Time
}
```

`AppendTaskEventResult.Part` contains one persisted record, while
`ListTaskEventsResult.Parts` contains a page of persisted records.
`TaskEventWriter.Append` requires a non-empty `TaskEventPartInput.PartID`; only
the lower-level `AppendTaskEventRequest` accepts an empty `PartID` as the
single-part `"event"` shorthand.

- `EventID` identifies one logical event.
- `PartID` is stable across recoverable replay.
- identical `(TaskID, EventID, PartID)` replay returns `Inserted=false`.
- conflicting replay returns `ErrTaskEventPartConflict`.
- `Final` closes the logical event and later new parts are rejected.
- every part append revalidates attempt fencing and cancellation.
- event-part writes do not advance lifecycle version.

For a streaming AgentEvent, the framework copies the stream before invoking the
persister: one copy remains live and one belongs exclusively to persistence.
The persister consumes its copy and decides how to serialize the event and
chunks; `PersistTaskEvent` closes the copy after the persister returns. The
persister may record an incomplete final part when the source errors. No Store
lock or transaction spans the lifetime of the stream.

Local and managed Tool persisters are called once per event with `Stream=nil`.
Only a streaming Sub-agent event passes an independent persistence-owned stream
copy.

`ListTaskEvents` provides snapshot-stable forward or reverse pagination.

## Derived Output Files

`OutputMaterializer` is optional. Reservation occurs after task ID allocation and
before task submission. The returned deterministic path is persisted in
`Spec.OutputFile`.

Every keyed record, including a replay, is sent to the materializer with TaskID,
EventID, path, and data. The implementation must be idempotent by
`(TaskID, EventID)` and preserve event order.

The Store output feed remains authoritative. On the first materializer error, the
executor records `OutputFileErr`, disables later writes for the attempt, and continues
the logical operation. Recovery never reads the derived file.

Existing process-local shell execution keeps its `AppendOpener` compatibility mirror.
Recoverable shell advertises an output file only when an `OutputMaterializer` is
configured.

## Model-Facing Progress

Task middleware resolves a `ProgressReader` from
`ProgressReadersByExecutorKey` using the persisted executor key. Managed tools use
their bounded recent Store view; durable Sub-agents register a reader backed by
their child `SessionEventStore`.

The `task_output` input remains:

```text
task_id
block
timeout
```

No Store cursor or page limit is exposed to the model. Blocking waits on lifecycle
version only, so a progress append does not wake the model or parent session.

## Sub-Agent Session Store Access

`RuntimeSessionStoreFactory` receives one explicit access mode:

| Mode | Authority | Snapshot |
| --- | --- | --- |
| `RuntimeSessionStoreAccessForegroundExecute` | caller-owned TurnLoop read/write | `Task` must be nil |
| `RuntimeSessionStoreAccessManagedExecute` | Manager-owned attempt read/write | `Task` is the current attempt snapshot |
| `RuntimeSessionStoreAccessReadProgress` | transcript/progress read-only | `Task` is the projected snapshot |

`RuntimeSessionStoreAccessUnknown` is invalid. The mode is the sole authority
discriminator; implementations must not infer access from snapshot presence.
For nested tasks, `ParentSessionID` is the direct parent session rather than the
root session.

## Recoverable Shell

`RecoverableShell` is separate from `filesystem.Shell` and `StreamingShell`:

```go
type RecoverableShell interface {
    StartCommand(context.Context, *StartCommandRequest) (tool.Run, error)
    RecoverCommand(context.Context, *RecoverCommandRequest) (tool.Run, error)
}
```

Streaming is an optional property of the returned `Run`, not a different shell
interface. A run implementing `UpdateSource` publishes durable command updates and
projects newly inserted records while foreground.

Filesystem middleware requires a shared Manager and notifications, registers the
shell through the generic recoverable executor, preserves task IDs and control tools,
and keeps all three shell choices mutually exclusive.

## Cancellation And Timeout

- `ControlStop` calls `Run.Stop` and acknowledges `canceled`.
- `ControlTimeout` calls `Stop` best-effort and returns deterministic failure.
- `ControlDrain` never calls `Stop`; recoverable work yields.
- Caller cancellation or reader closure defaults to publishing and detaching
  the foreground projection. A host may explicitly configure it to request
  Task cancellation instead.
- A failed publication at a timeout or caller-abort boundary triggers bounded
  cancellation cleanup.
  Cancel intent is persisted before optional domain cleanup. The cleanup context
  is detached from caller cancellation and defaults to a five-second bound.
- Normal cancellation resolution, including rejected auto-background timeout or
  an explicit caller-abort cancellation policy, returns an `Outcome` only after
  a boundary is observed. Request, domain cleanup, Store, or boundary timeout
  failure returns a nil `Outcome` and the infrastructure error; a running
  cancel-requested partial outcome is never exposed on this path.
- Publication-failure fallback always preserves the original publication error.
  If cancellation reaches a boundary within the bound, the API also returns the
  latest authoritative snapshot. If cleanup fails or times out, the error
  matches both publication and cleanup causes and the API returns the latest
  available snapshot, which may still be running with durable cancel intent.
  Synchronous terminal cancellation is therefore not guaranteed.
- Cancel intent remains durable. A later recovery attempt observes Manager control
  and stops the recovered logical operation.
- Completion/cancel races remain Store-version and active-attempt fenced.

`Run.Wait` context cancellation stops local observation only. Implementations must
not treat it as logical-operation cancellation.

## Direct Foreground Mailbox Finalization

Direct Attempt 0 execution has a foreground Mailbox but no durable lifecycle
snapshot. Local and managed Tool adapters finalize that Mailbox through one
bounded, at-most-once helper:

- completed outcomes call `SealIfIdle` with the captured generation and cursor;
- failed/canceled outcomes, timeout, caller abort, start/construction failure,
  stream failure, and reader close call `Abandon`;
- waiting-input keeps the Mailbox open and persists generation/cursor in
  interrupt state for resume;
- `ErrInputsPending` leaves the Mailbox foreground and preserves every input;
- finalization uses a caller-independent bounded context, and Store errors are
  returned instead of being discarded.

## Compatibility

- Existing `run_in_background`, timeout, task_output, task_stop, and foreground
  result formats remain unchanged.
- Auto-background and explicit background now share one task-owned executor path.
- Direct foreground execution without auto-background or caller-abort policy may
  retain Attempt 0 and parent-owned cancellation.
- Plain producers may leave `Update.EventID` empty; the framework assigns the
  persisted event ID.
- Store SPI additions are alpha lifecycle extensions.
- The source packages are alpha and this branch intentionally makes breaking Go
  API changes, including package moves, renamed types and fields, and removed
  helpers. No compatibility shim for the old alpha package surface is provided;
  callers must migrate source code together with the upgrade.
- Public code remains compatible with Go 1.18.

## Verification

The implementation is covered by Store conformance, Worker dispatch, managed-tool,
recoverable-shell, filesystem middleware, background-task middleware, race, full
repository, and Go 1.18 test runs. The reusable
`CheckRecoveryConformance` helper lets backend owners verify independent-adapter
start/recover identity and stable update replay against their durable service.
