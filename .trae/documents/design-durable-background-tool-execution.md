# Durable Background Tool And Shell Execution

## Status

Implemented on `feat/durabletask`.

## Purpose

`adk/backgroundtask/tool` lets explicitly capable tools launch external operations
under the existing durable task lifecycle. `adk/backgroundtask/shell` adapts remote
shell backends that can recover a logical command after an Eino Worker changes.

The integration does not parse arbitrary tool output and does not expose an external
service operation ID to the model. The framework allocates and persists the Eino task
before external work starts, and every successful model-facing result ends with a
framework-owned `launch_result` containing that task ID.

## Capability Classes

Two persisted executor keys prevent runtime capability drift:

| Executor key | Lease expiry | Drain | Backend requirement |
| --- | --- | --- | --- |
| `eino.dev/background-tool` | fail | no | process may observe and stop the operation |
| `eino.dev/recoverable-background-tool` | retry | yield | durable task-ID-keyed recovery |

A plain `BackgroundTool` must not be retried after Worker loss. A
`RecoverableBackgroundTool` must satisfy all of these requirements:

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

## Launch Ordering

The managed wrapper performs:

```text
validate serialized arguments
  -> allocate Eino task ID
  -> reserve an optional deterministic derived output path
  -> persist pending Spec
  -> foreground coordinator calls Manager.Execute
  -> Store claims and fences an attempt
  -> executor calls Start(TaskID)
  -> persist updates
  -> complete in foreground or detach at the foreground policy boundary
```

The persisted payload contains a version, tool name, tool-call correlation ID, and
serialized arguments. Clients, credentials, callbacks, and live run handles are not
serialized.

The wrapper requires a parent Runner session and a validated notification route.
Terminal lifecycle notifications continue to use the existing outbox and session
inbox. Progress records never enqueue notifications and never advance task lifecycle
version.

## Foreground Result

Invokable tools return one JSON record. Streamable tools return newline-delimited
records, one complete record per chunk:

```json
{"type":"update","update":{"source_id":"event-1","kind":"stdout","data":"aGVsbG8="}}
{"type":"launch_result","task_id":"task_abc","status":"running"}
```

The final record is always `launch_result`. `type`, `task_id`, and `status` are
framework-owned. Registration-specific completed output is nested under `output`.
Concatenating stream chunks therefore remains valid NDJSON and cannot merge raw
stdout with lifecycle JSON.

The default policy waits up to the configured foreground timeout, then allows
automatic backgrounding. This changes caller occupancy only: task status remains
`running`, the attempt and lease are unchanged, and update persistence continues.

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

`ExecutionDirectiveYield` is distinct from lifecycle status. It commits:

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

## Reference Worker

`adk/backgroundtask/worker` is a minimal host dispatcher. It:

- polls `Manager.ListPending` for configured executor keys;
- dispatches through `Manager.Execute`;
- bounds concurrent attempts;
- tolerates duplicate listings and claim races through Store authorization;
- delays only attempt-zero tasks by `InitialPickupDelay`;
- immediately considers yielded and lease-expired tasks.

Production schedulers may replace it, but a host must provide an equivalent pending
task dispatch path for recovery to operate.

## Incremental Output

`Update` is the managed integration envelope:

```go
type Update struct {
    SourceID string
    Kind     string
    Data     []byte
    Metadata map[string]string
}
```

Plain producers may publish unkeyed updates. Recoverable producers must provide a
non-empty lifetime-stable `SourceID`.

`AppendOutputOnce(TaskID, Attempt, SourceID, Data)` is task-wide across attempts:

- first append allocates a monotonic sequence and returns `Inserted=true`;
- byte-identical replay returns the original record and sequence;
- different bytes under the same source ID return `ErrOutputConflict`;
- attempt fencing and cancellation still apply;
- output writes do not advance lifecycle version.

Persistence precedes live projection. Replayed records are not projected twice.
Correctness does not depend on an exact backend resume cursor.

`ReadOutput` remains the forward replay API. `ReadRecentOutput` returns the newest
bounded records in chronological order for model-facing presentation.

## Derived Output Files

`OutputMaterializer` is optional. Reservation occurs after task ID allocation and
before task submission. The returned deterministic path is persisted in
`Spec.OutputFile`.

Every keyed record, including a replay, is sent to the materializer with TaskID,
SourceID, Store sequence, path, and data. The implementation must be idempotent by
`(TaskID, SourceID)` and preserve sequence order.

The Store output feed remains authoritative. On the first materializer error, the
executor records `OutputFileErr`, disables later writes for the attempt, and continues
the logical operation. Recovery never reads the derived file.

Existing process-local shell execution keeps its `AppendOpener` compatibility mirror.
Recoverable shell advertises an output file only when an `OutputMaterializer` is
configured.

## Model-Facing Progress

Background-task middleware resolves `TaskProgressReader` by persisted executor key.
Managed tools use `ProgressReader`, which reads a bounded recent Store view. Durable
sub-agents retain their child `SessionEventStore` projection through the compatibility
fallback.

The `task_output` input remains:

```text
task_id
block
timeout
```

No Store cursor or page limit is exposed to the model. Blocking waits on lifecycle
version only, so a progress append does not wake the model or parent session.

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
- Cancel intent remains durable. A later recovery attempt observes Manager control
  and stops the recovered logical operation.
- Completion/cancel races remain Store-version and active-attempt fenced.

`Run.Wait` context cancellation stops local observation only. Implementations must
not treat it as logical-operation cancellation.

## Compatibility

- Existing `Shell`, `StreamingShell`, process-local tasks, and durable sub-agents keep
  their existing executor paths.
- Existing unkeyed output records have an empty `SourceID`.
- Existing `ReadTaskProgress` remains the middleware fallback.
- Store SPI additions are alpha lifecycle extensions.
- Public code remains compatible with Go 1.18.

## Verification

The implementation is covered by Store conformance, Worker dispatch, managed-tool,
recoverable-shell, filesystem middleware, background-task middleware, race, full
repository, and Go 1.18 test runs. The reusable
`CheckRecoveryConformance` helper lets backend owners verify independent-adapter
start/recover identity and stable update replay against their durable service.
