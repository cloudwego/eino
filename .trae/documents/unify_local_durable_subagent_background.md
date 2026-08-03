# Local and Durable Background Tasks: Implemented Architecture and API Audit

## 1. Purpose and Status

This document audits the background-task implementation currently present in the
repository. It covers:

- Local and Durable execution;
- model-facing compatibility;
- lifecycle, lease, cancellation, resume, and shutdown semantics;
- output, transcript, and notification authority;
- the Go API and its ownership boundaries;
- remaining operational risks.

All sections describe the implemented status quo. Section 14 records the completed
Manager surface narrowing and its verified constraints.

The implementation is Alpha. Source compatibility is secondary to a coherent API, but
model-facing Local behavior should remain compatible unless Durable semantics require a
difference.

## 2. Current Architecture

The implementation separates the following concerns:

```text
backgroundtask.Manager
    Store-backed lifecycle plus current worker coordination

backgroundtask/local.Runner
    process-local closure execution and ephemeral string streaming

adk/internal/foreground
    foreground waiting, timeout, auto-background, and projection detachment

Store output feed
    ordered incremental output for task kinds that use it

Child SessionEventStore
    durable subagent conversation and progress
```

The main architectural decisions are:

- Manager does not expose Local execution helpers such as `Run` or `RunStream`.
- Local closures belong to `backgroundtask/local.Runner`.
- Durable work is submitted as serialized intent and reconstructed by an `Executor`.
- Foreground projection is distinct from task durability.
- Local and shell output can be persisted independently of optional filesystem mirrors.
- Durable subagent progress comes from its child SessionEventStore rather than a duplicate
  task-output stream.
- Lease-expiry policy is selected by the registered Executor, not by application input.
- Cancellation intent is durable without introducing a public `canceling` status.
- Model-facing middleware validates notification delivery during construction.

This removes the former false association between streaming and Local durability.
Streaming is an observation choice; durability is a reconstruction and persistence
choice.

## 3. Model-Facing Compatibility

### 3.1 Preserved Local behavior

Local subagent and shell tools preserve:

- `run_in_background` behavior;
- foreground waiting and per-call timeout;
- startup preview for streaming shell commands moved to the background;
- auto-background policy;
- `task_stop` success text;
- completion notifications;
- failure and cancellation descriptions;
- terminal `ResultData`;
- optional output-file mirrors;
- subagent JSONL framing.

### 3.2 Presentation clarification

`task_output` uses task-kind-specific labels:

- subagent: `Event transcript (JSONL)`;
- shell: `Command output transcript`.

The labels prevent an event transcript from being mistaken for a plain final answer.
This is a presentation clarification shared by Local and Durable execution.

### 3.3 Durable-only behavior

Durable work can:

- remain pending until claimed by an eligible worker;
- survive process loss;
- retry after an expired recoverable attempt;
- checkpoint into `waiting_input` or `suspended`;
- resume on another worker;
- persist cancellation intent before the active worker acknowledges it.

These differences do not change pure Local task semantics.

## 4. Lifecycle and State Authority

The canonical lifecycle is:

```text
pending -> running
running -> completed
running -> failed
running -> canceled
running -> waiting_input -> pending
running -> suspended -> pending

running + expired lease + retry policy -> pending
running + expired lease + fail policy  -> failed
running + CancelRequestedAt + expired lease -> canceled
```

The `suspended -> pending` transition is worker-runtime recovery after planned drain. It
is not application resume.

There is no `canceling` status. Cancellation is represented by:

```text
running + CancelRequestedAt == nil     executing normally
running + CancelRequestedAt != nil     cancellation requested
canceled                               terminal acknowledgement
```

After cancellation intent is persisted, Store fencing rejects completion, failure,
checkpoint, output append, and heartbeat from the canceled attempt. Only cancellation
acknowledgement, or lease-expiry cancellation resolution, may complete the transition.

## 5. Submission, Claim, and Lease Expiry

`Executor.LeaseExpiryPolicy()` defines whether work can be reconstructed:

- `LeaseExpiryRetry`: another worker may reconstruct the task after lease expiry;
- `LeaseExpiryFail`: required process-local state is unavailable after worker loss.

`Manager.Submit`:

1. resolves the Executor by `Spec.ExecutorKey`;
2. validates the serialized Spec;
3. reads the Executor-owned lease-expiry policy;
4. persists the task as `pending`.

Application-supplied Spec cannot select the recovery policy.

The current execution flow is:

1. A worker discovers candidates through executor-key-scoped `ListPending`.
2. `Execute` validates worker-local dependencies.
3. `Store.Start` atomically claims the task, increments `Attempt`, and creates a lease.
4. Manager heartbeats extend the lease.
5. The Executor reconstructs and runs the attempt.
6. Store validates the attempt and lifecycle transition when the result is committed.

`InMemoryStore` resolves expiry lazily during Store operations. A persistent Store may use
transactional access or a sweeper, but must preserve equivalent compare-and-swap, lease,
cancellation, and attempt-fencing semantics.

## 6. Cancellation and Attempt Control

Cancellation and attempt-local control are different contracts.

`Manager.RequestCancel` is the canonical cancellation operation. It:

- persists cancellation intent for recoverable Durable work;
- makes cancellation visible across workers;
- fences the active attempt;
- delivers `ControlStop` when the active attempt is local;
- resolves non-running tasks directly to `canceled`;
- handles the process-local non-recoverable path without exposing a separate API.

Attempt-local controls are Executor coordination:

- `ControlStop`: stop and return a canceled result;
- `ControlDrain`: checkpoint and suspend if supported;
- `ControlTimeout`: fail with a deterministic foreground-timeout reason.

The public Manager does not accept `ControlRequest` values. Foreground timeout is carried
through an execution-scoped controller in `adk/internal/taskcontrol`; Manager binds that
controller to the task runtime during `Execute`. Stop remains owned by `RequestCancel`,
and drain remains owned by Manager shutdown.

Timeout is not cancellation. A timeout produces `failed` and does not set
`CancelRequestedAt`.

## 7. Output and Progress Authority

Terminal result and incremental progress have separate authorities:

- `Task.ResultData`: authoritative successful terminal result;
- `Task.ResultError`: authoritative terminal failure or cancellation reason;
- Store output feed: ordered incremental output for task kinds that use it;
- child SessionEventStore: authoritative Durable subagent conversation and progress;
- `Spec.OutputFile`: optional Local and shell compatibility mirror.

### 7.1 Store output feed

Each `OutputRecord` contains:

```text
TaskID
Attempt
Sequence
Data
CreatedAt
```

`Sequence` is monotonic across attempts of one task. `Attempt` identifies the producing
attempt. `ReadOutput(AfterSequence)` therefore supports ordered replay without presenting
a process-local live subscription as durable.

`AppendOutput` requires:

- `running` status;
- matching attempt;
- active lease;
- no persisted cancellation request.

Local streaming shell chunks and Local subagent transcript records use this feed. Durable
subagents do not append a second copy of child-session events.

There is deliberately no public live-subscription API. Foreground projection and durable
replay remain separate contracts.

### 7.2 Durable subagent progress

The child session `<taskID>/session` is the sole durable source of incremental subagent
progress. The model-facing `task_output` tool receives an optional `ReadTaskProgress`
callback. The built-in Durable callback privately projects the child session instead of
adding a public Session reader to Manager.

The projection:

- excludes the first persisted message because it is the submitted query;
- reads a bounded recent page of messages and incomplete-stream events;
- restores chronological order after reverse pagination;
- formats messages using the root `subagent_name`;
- reads the latest interrupt separately for `waiting_input`.

Transferred or nested emitter names are represented as the root subagent name. This
preserves presentation compatibility without adding persistence metadata solely for
formatting.

Local and Durable paths share:

```go
type TranscriptFormat[M adk.MessageType] func(
    context.Context,
    agentName string,
    message M,
) (string, error)
```

The default remains one JSON object per line with `{agent_name, message}`. Local formats
live AgentEvents; Durable formats persisted SessionEvents at read time. Changing the
formatter changes the view of existing Durable history without rewriting stored events.

Progress read or formatting failure is displayed as transcript unavailability. It never
changes task lifecycle state. Terminal `ResultData` or `ResultError` remains authoritative.

## 8. Durable Subagent Reconstruction and Resume

Payload v2 stores only non-derivable serialized invocation data:

```text
version
subagent_name
query
```

`query` is the initial user input passed to `Runner.Query`, not the Agent system
instruction. The model-facing `prompt` field maps to `SubmitRequest.Query`.

Child identities are deterministic:

```text
child session = <taskID>/session
checkpoint    = <taskID>/checkpoint
```

The task checkpoint envelope stores only interrupted target IDs and a sequence number.

The child Session and Checkpoint Store have different authority:

- the child Session Store preserves durable conversation, lifecycle events, reconstructed
  history, and session-level concurrency;
- the Checkpoint Store preserves transient execution-continuation state.

On resume, Runner reopens the child session and restores the derived checkpoint.
Checkpoint deletion after completion does not remove child-session history.
`Spec.SessionID` identifies the parent notification destination; `<taskID>/session`
identifies the child execution history.

Resume has one meaning: continue from the derived checkpoint.

- Empty resume input calls `Runner.Resume`.
- Targeted input is validated against interrupted target IDs and passed to
  `Runner.ResumeWithParams`.
- Sending a new query to the child session is not a resume mode.

External-input resume and rollout recovery remain distinct:

- agent interrupt: `waiting_input`, followed by application `Resume`;
- graceful drain: `suspended`, followed by worker-runtime suspension release.

Version 1 payloads are rejected rather than silently reinterpreted. Hosts must drain or
discard version 1 tasks before deploying workers that only understand version 2.

Agents, functions, transcript formatters, and `AgentRunOption` values are not serialized.
Each eligible worker reconstructs them through:

```text
AgentRegistration
    Agent
    RunOptionsFactory
```

Workers for the same subagent name must remain semantically homogeneous for the lifetime
of resumable tasks. Incompatible changes require draining existing tasks or using a new
subagent name. No registration revision is persisted.

## 9. Notifications

Generic Manager use is notification-optional.

Model-facing subagent, filesystem, task-control, and DeepAgent middleware require a
`NotificationDeliveryRuntime`. Construction-time validation checks:

- the task Store supports `NotificationOutbox`;
- the target route exists;
- sink dependencies are complete;
- host readiness validation succeeds.

Runtime outages are handled by durable outbox retry and idempotent inbox delivery.
Construction-time validation establishes structural readiness, not permanent service
availability.

Middleware calls `Manager.ValidateNotificationDelivery`, which supplies only Store
capability facts and the target kind to the delivery runtime. Built-in composition cannot
recover a Store capable of bypassing Manager lifecycle validation.

## 10. Task IDs

The default generator uses:

```text
<kind-prefix> + Base64URL(16 random bytes)
```

The suffix contains 128 random bits and is opaque, URL-safe, and compact. `Config.IDGen`
supports host-defined IDs.

Task IDs are bearer capabilities. They must not be written to public paths, leaked in
logs, or exposed through unauthorised enumeration.

`AllocateTaskID` is separate from `Submit` because Local execution must reserve an ID
before registering a process-local closure, and executor-specific submit helpers may need
the same ordering.

## 11. Current API Inventory

### 11.1 Manager application lifecycle

| API | Capability | Current assessment |
|---|---|---|
| `New(Config)` | Construct Manager | Store, registry, and ID generator are explicit dependencies. |
| `Submit(Spec)` | Validate and persist task intent | Canonical creation operation. |
| `Get(taskID)` | Read authoritative snapshot | Clear lifecycle lookup. |
| `WaitUpdate(request)` | Wait for `Version > AfterVersion` | Name states the actual condition. |
| `ReadOutput(request)` | Replay output records | Durable and transport-neutral. |
| `RequestCancel(taskID)` | Persist cancellation intent | Correctly distinguishes request from terminal acknowledgement. |
| `Resume(request)` | Validate and persist resume input | Canonical external-input resume. |
| `Close(ctx)` | Bounded shutdown and drain | Owns aggregate runtime shutdown. |

### 11.2 Manager worker and integration surface

| API | Capability | Current assessment |
|---|---|---|
| `ListPending(request)` | Find executor-key-scoped candidates | Valid worker capability, but not application lifecycle. |
| `Execute(taskID)` | Claim and execute one attempt | Canonical worker-host operation on aggregate Manager. |
| `AllocateTaskID(request)` | Allocate an opaque typed ID | Required before some executor-specific submissions. |
| `LoadOrRegisterExecutor(executor)` | Atomically resolve or install one Executor | Narrow composition operation; registry remains encapsulated. |
| `ValidateNotificationDelivery(runtime, kind)` | Validate the Manager-owned Store route | Narrow composition operation; Store remains encapsulated. |

The phrase “worker-runtime-facing” describes intended callers but does not create a Go
visibility boundary. Exported methods remain public API even when model tools do not
expose them. Manager intentionally remains the aggregate task runtime rather than adding
separate Worker or Attempt abstractions.

### 11.3 Local Runner

| API | Capability | Current assessment |
|---|---|---|
| `New(Config)` | Bind Local execution to a Manager | Correct ownership for process-local closures. |
| `Run(Input, WorkFunc)` | Execute buffered Local work | Local-only semantics are explicit. |
| `RunStream(Input, StreamWorkFunc)` | Stream foreground strings and persist chunks | Required by shell UX; not a durability claim. |
| `Manager()` | Return the shared Manager | Used by current middleware wiring. |

### 11.4 Executor SPI

| API | Capability |
|---|---|
| `Key()` | Stable worker-routing identity |
| `LeaseExpiryPolicy()` | Executor-owned recovery invariant |
| `ValidateSpec` | Validate serialized intent before persistence |
| `ValidateExecution` | Validate worker-local dependencies |
| `ValidateCheckpoint` | Validate checkpoint compatibility |
| `ValidateResume` | Validate and normalize external resume input |
| `SupportsDrain` | Declare planned-suspension support |
| `Execute` | Reconstruct and run one attempt |

`ExecutionRuntime` exposes attempt controls, output append, and output-file failure
reporting. Foreground projection detachment is internal ADK context state and is not part
of the Executor SPI. ExecutionRuntime does not expose general task lookup or arbitrary
Store mutation.

### 11.5 Store SPI

Store owns:

- creation and compare-and-swap lifecycle transitions;
- lease claim, heartbeat, and expiry;
- attempt fencing;
- cancellation request and acknowledgement;
- checkpoint, suspension, and resume persistence;
- output append and replay;
- notification outbox records.

Store is intentionally broader than the application API because it is a storage-provider
contract. Persistent implementations must preserve the semantics of the in-memory
reference implementation.

### 11.6 Middleware and DeepAgent composition

Current model-facing composition follows these rules:

- Local subagent and shell middleware receive `*backgroundtask/local.Runner`.
- Durable subagent middleware receives a shared Manager, foreground policy,
  `RunOptionsFactories`, and notification delivery runtime.
- One `TranscriptFormat` is shared by Local live formatting and Durable read-time
  projection.
- Task-control middleware receives one optional `ReadTaskProgress` callback rather than a
  public progress-reader registry.
- `adk/internal/foreground` attaches a private projection-lifetime signal to the detached
  execution context. Local and Durable event forwarding stop when that signal closes.
- `TypedRunner.ExecuteBackgroundTask` binds the Runner session and checkpoint environment
  before invoking Manager execution.
- DeepAgent forwards one shared Manager and constructs or reuses a Local Runner for shell
  work even when subagents use Durable execution.
- Durable subagents do not use `OutputFile` or `OutputStore`; Durable `OutputDir` remains
  shell-only.

## 12. Removed Surface

The following APIs and concepts were removed instead of retained as Alpha compatibility
aliases:

- `Manager.Run`;
- `Manager.RunStream`;
- `Manager.RunSubmitted`;
- contextless `Manager.Get`;
- terminal-only `Manager.Wait`;
- result-discarding `Manager.Cancel`;
- `Manager.List`;
- `Manager.Subscribe`;
- generic `Manager.RequestControl`;
- `Manager.RequestTimeout`;
- `Manager.MarkBackgrounded`;
- `Manager.Store`;
- `Manager.Executors`;
- `ExecutionRuntime.Backgrounded`;
- `TaskEvent`;
- public observer registration;
- `RegisterAgent`;
- exported Durable subagent `TaskPayload`;
- payload fields `prompt`, `child_session_id`, `checkpoint_id`, `resume_mode`, and
  `allow_empty_resume`;
- `ResumeMode`, `ResumeNextTurn`, and next-turn resume markers;
- public `canceling` state;
- caller-controlled `Spec.LeaseExpiryPolicy`.

The generic control method was removed because `ControlStop` overlapped with
`RequestCancel` at the API shape while bypassing its durable semantics. Timeout control is
now internal and execution-scoped rather than a task-ID-addressed Manager method.

## 13. Current Assessment

The implemented persistence, execution, and API ownership are coherent:

- lifecycle authority belongs to Store;
- recovery policy belongs to Executor;
- cancellation intent is durable and fenced;
- Local closures and Durable reconstruction are separate;
- progress authority is not duplicated;
- external-input resume and worker recovery are distinct;
- notification delivery has an explicit structural guarantee;
- raw Store and executor-registry getters are absent;
- foreground projection lifetime is internal rather than Executor-facing;
- generic control is absent while deterministic timeout remains available internally.

Manager intentionally owns both application lifecycle and worker coordination. This is
an aggregate runtime boundary, not a lifecycle-only facade. Actual Executor
implementations do not call Manager; Manager calls Executors with attempt-scoped
`ExecutionRuntime`.

The remaining exported methods have distinct audiences:

```text
Application:
    Submit, Get, WaitUpdate, ReadOutput, RequestCancel, Resume

Executor-specific submit helpers:
    AllocateTaskID, Submit

Worker hosts:
    ListPending, Execute

Composition:
    New, LoadOrRegisterExecutor, ValidateNotificationDelivery

Runtime owner:
    Close
```

## 14. Implemented Manager Surface Narrowing

### 14.1 No Worker or Attempt abstraction

The implementation deliberately retains one aggregate Manager. `ListPending` and
`Execute` remain on Manager because they are real worker-host operations, and moving them
to another exported type would improve categorization without creating an access-control
boundary.

No public Worker, Attempt, or foreground-controller type was added.

### 14.2 Foreground projection ownership

`adk/internal/foreground.Run` creates a private projection signal and carries it through
the detached execution context. The signal closes whenever foreground coordination
returns, including:

- explicit `run_in_background`;
- policy-driven auto-background;
- terminal completion;
- timeout completion;
- caller cancellation.

Local receiver transforms and Durable subagent event forwarding read this internal signal.
Projection detachment therefore no longer requires:

```text
Manager.MarkBackgrounded
ExecutionRuntime.Backgrounded
taskRuntime.backgrounded
```

Projection state is process-local presentation coordination. It is neither persisted nor
part of Executor reconstruction.

### 14.3 Internal timeout controller

`adk/internal/taskcontrol` carries timeout requests through the detached execution
context. `foreground.Run` creates one controller per `Manager.Execute` invocation.
Manager binds it after the active runtime is created and forwards accepted requests as
`ControlTimeout`.

The controller:

- is unavailable to downstream applications through Go's `internal` import boundary;
- carries no task ID;
- accepts only a non-empty deterministic timeout reason;
- buffers the Store-running/runtime-ready gap;
- synchronously acknowledges accepted requests;
- closes on every early or terminal `Execute` return;
- reports `taskcontrol.ErrClosed` when execution wins a timeout race.

Foreground treats `ErrClosed` as an already-finishing execution and waits for the normal
`Execute` result. Timeout still produces `failed`, never sets `CancelRequestedAt`, and
cannot express stop or drain.

`RequestCancel` remains the only public cancellation operation. `ControlStop` is delivered
internally after cancellation handling, and `ControlDrain` remains shutdown-owned.

### 14.4 Encapsulated Store validation

`Manager.Store()` was removed. Model-facing middleware now calls:

```go
func (m *Manager) ValidateNotificationDelivery(
    ctx context.Context,
    runtime NotificationDeliveryRuntime,
    targetKind string,
) error
```

Manager evaluates whether its Store implements `NotificationOutbox` and passes only that
capability fact plus the target kind to the delivery runtime. The Store object itself
never crosses the validation boundary. Dispatcher hosts retain the Store they explicitly
inject.

### 14.5 Atomic executor composition

`Manager.Executors()` was removed. Local and Durable assembly now call:

```go
func (m *Manager) LoadOrRegisterExecutor(
    executor Executor,
) (actual Executor, loaded bool, err error)
```

Lookup and registration occur under one registry lock. Concurrent callers observe one
canonical Executor instance, and registration is serialized against Manager shutdown.
`Config.Executors` remains available for hosts that explicitly inject a registry.

### 14.6 Verification

The implemented surface is protected by tests for:

- exact exported Manager methods;
- atomic concurrent executor registration;
- registration rejection after shutdown;
- Manager-owned notification validation;
- idempotent projection-detachment signaling;
- timeout request acknowledgement, closure, and context cancellation;
- controller closure on early and terminal `Execute` returns;
- Durable foreground projection boundaries;
- timeout failure without cancellation intent;
- existing cancellation, shutdown, Local, and Durable behavior.

Verification completed successfully:

```bash
go test -race ./adk/backgroundtask/... ./adk/internal/foreground \
  ./adk/internal/taskcontrol \
  ./adk/middlewares/subagent ./adk/middlewares/filesystem
go test ./...
git diff --check
```

## 15. Remaining Risks

### 15.1 Persistent Store conformance

`InMemoryStore` is a deterministic reference implementation, not a durable backend. A
multi-process Store needs conformance coverage for:

- atomic claim and expiry;
- stale-attempt rejection;
- cancellation/completion races;
- output sequence assignment;
- resume one-shot consumption;
- notification outbox atomicity.

### 15.2 Output retention

The output feed enforces page and record bounds, but production Stores must define:

- retention duration;
- total-size quotas;
- archival;
- deletion with the owning task.

Durable subagent transcript retention follows child SessionEventStore policy.

### 15.3 Suspension release

`Store.ReleaseSuspension` supports `suspended -> pending`, but automatic release after
graceful drain is not wired in the repository. A production worker host must define
ownership, retry, and fencing so rollout recovery does not leave tasks suspended.

This transition must not use application `Resume`, because suspension does not represent
missing external input.

### 15.4 Deployment homogeneity

Durable reconstruction assumes equivalent Executor and Agent registrations across
eligible workers. Rolling deployments must either preserve compatibility, drain existing
tasks, or use new executor/subagent identities.

## 16. Conclusion

The implemented lifecycle, persistence, reconstruction, output, and cancellation
semantics form a coherent Alpha baseline. The strongest parts of the design are its
single authorities:

- Store for lifecycle;
- Executor for recovery policy and reconstruction;
- child SessionEventStore for Durable subagent progress;
- terminal Task fields for final result;
- output feed for incremental Local and shell output.

The Manager surface is intentionally aggregate: it combines application lifecycle and
worker coordination without exposing raw Store, raw registry, generic control, or
projection state. Narrow composition operations cover the built-in assembly requirements.

The remaining Alpha work is operational rather than another API split: persistent Store
conformance, suspension release, output retention, and deployment homogeneity.
