# Local / Durable Background Task: Current Architecture and API Audit

## 1. Scope

This document describes the implemented background-task architecture in the current
repository. It audits:

- model-facing compatibility with the former Local-only behavior;
- the complete Local and Durable Go API;
- lifecycle, cancellation, lease, notification, and output semantics;
- API coherence, minimality, naming, and ownership boundaries.

The implementation is still Alpha. Source compatibility is secondary to a coherent
surface, but model-facing Local behavior remains compatible unless Durable semantics
require a difference.

## 2. Executive Summary

The architecture now separates four concerns:

```text
backgroundtask.Manager
    Store-backed lifecycle and worker coordination

backgroundtask/local.Runner
    process-local closure execution and ephemeral string streaming

adk/internal/foreground
    foreground timeout, auto-background, and projection boundary

Store output feed
    durable ordered observation and replay
```

This removes the former false association between streaming and Local durability.
Streaming is an output-projection choice; durability is a task-reconstruction choice.

The current design is conceptually coherent:

- `Manager` no longer exposes `Run`, `RunStream`, or `RunSubmitted`.
- Local closures are owned by `backgroundtask/local.Runner`.
- Durable execution uses `Submit` plus worker `Execute`.
- Foreground projection is internal ADK coordination.
- Output records are persisted in Store independently of optional filesystem mirrors.
- Recovery policy is selected by the registered `Executor`, not by `Spec`.
- Cancellation intent is persisted without a public `canceling` status.
- Notification delivery is validated when model-facing middleware is constructed.

## 3. Model-Facing Compatibility

### 3.1 Preserved Local behavior

Local subagent and shell tools retain:

- the same `run_in_background` behavior;
- foreground waiting and per-call timeout behavior;
- explicit background startup preview for streaming shell commands;
- auto-background policy;
- original `task_stop` success text;
- completion notifications;
- task failure and cancellation descriptions;
- authoritative terminal `ResultData`;
- optional output-file mirrors;
- original subagent JSONL framing.

### 3.2 Intentional clarifications

`task_output` uses kind-specific labels:

- subagent: `Event transcript (JSONL)`;
- shell: `Command output transcript`.

This prevents the model from treating an event log as a plain final answer. It is a
presentation clarification shared by Local and Durable execution, not a durability
requirement.

### 3.3 Durable-only semantic differences

Durable execution can:

- remain pending until claimed by an eligible worker;
- survive process loss;
- retry an expired recoverable attempt;
- checkpoint into `waiting_input` or `suspended`;
- resume on another worker;
- persist cancellation intent before the active worker acknowledges it.

These differences do not alter pure Local task semantics.

## 4. Canonical Lifecycle

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

There is no `canceling` status.

Cancellation is represented as:

```text
running + CancelRequestedAt == nil     executing normally
running + CancelRequestedAt != nil     cancellation requested
canceled                               terminal acknowledgement
```

Store fencing prevents completion, failure, checkpoint, or heartbeat from winning after
cancellation intent has been persisted.

## 5. Lease Ownership and Expiry

`Executor.LeaseExpiryPolicy()` returns the recovery policy for that executor:

- `LeaseExpiryRetry`: the task is reconstructable after worker loss;
- `LeaseExpiryFail`: the task depends on unavailable process-local state.

`Manager.Submit` resolves the executor, validates `Spec`, reads its policy, and stamps the
policy into `CreateTaskRequest`. Application-supplied `Spec` cannot choose it.

The execution flow is:

1. `Submit` creates a `pending` task.
2. A worker discovers it through executor-key-scoped `ListPending`.
3. `Execute` calls `Store.Start`, producing `running`, incrementing `Attempt`, and
   creating an active lease.
4. Manager heartbeats extend that lease.
5. Store detects expiry atomically during access or provider-specific sweeping.
6. Store applies the persisted executor-owned policy.

`MemoryStore` performs lazy expiry resolution during Store operations. A production
persistent Store may use transactional queries or a sweeper, but must preserve identical
CAS, lease, cancellation, and attempt-fencing semantics.

## 6. Output Model

Terminal result and incremental output are different authorities:

- `Task.ResultData`: authoritative successful terminal result;
- `Task.ResultError`: authoritative terminal failure/cancellation reason;
- Store output feed: authoritative ordered incremental output;
- `Spec.OutputFile`: optional compatibility mirror for filesystem-based hosts.

### 6.1 Store output feed

Each `OutputRecord` contains:

```text
TaskID
Attempt
Sequence
Data
CreatedAt
```

`Sequence` is monotonic across all attempts of one task. `Attempt` identifies which
execution attempt produced the record. `ReadOutput(AfterSequence)` therefore supports
ordered replay after reconnection or worker migration without presenting a process-local
subscription as durable.

`AppendOutput` is attempt-fenced:

- task must be `running`;
- attempt must match;
- lease must still be active;
- cancellation must not have been requested.

Local streaming shell chunks and Local/Durable subagent event records are appended to
the feed. Durable resumed subagents continue the same sequence while recording the new
attempt number.

There is deliberately no public live-subscription API. Ephemeral foreground projection
and durable replay are separate contracts.

## 7. API Inventory and Audit

### 7.1 `backgroundtask.Manager`

Application lifecycle:

| API | Capability | Assessment |
|---|---|---|
| `New(Config)` | Construct lifecycle manager | Minimal: Store, executor registry, ID generator. |
| `Submit(Spec)` | Validate and persist task intent | Canonical creation operation. |
| `Get(taskID)` | Authoritative snapshot lookup | Clear; `Task` is implied by the receiver package. |
| `WaitUpdate(request)` | Wait for `Version > AfterVersion` | Name states the actual condition; not confused with terminal waiting. |
| `ReadOutput(request)` | Replay persisted output after a sequence | Durable and transport-neutral. |
| `RequestCancel(taskID)` | Persist cancellation intent | Correctly avoids implying immediate terminal cancellation. |
| `Resume(request)` | Validate and persist resume input | Canonical resume operation. |
| `Close(ctx)` | Bounded worker shutdown and drain | Coherent lifecycle ownership. |

Worker-runtime coordination:

| API | Capability | Assessment |
|---|---|---|
| `ListPending(request)` | Executor-key-scoped dispatch candidates | Worker-runtime API, not a general task list. |
| `Execute(taskID)` | Claim and execute one task attempt | Canonical worker operation. |
| `MarkBackgrounded(taskID)` | Close the attempt projection boundary | Narrow bridge used by internal foreground coordination. |
| `RequestControl(taskID, control)` | Send attempt-local timeout/drain/stop control | Worker-runtime bridge, not model API. |
| `AllocateTaskID(request)` | Allocate opaque typed task ID | Useful to executor-specific submit helpers. |
| `Store()` | Access Store integration | Required by host validation, but permits bypass of Manager validation. |
| `Executors()` | Access executor registry | Required by host/executor assembly. |

“Worker-runtime-facing” means used by the host infrastructure that registers executors,
polls pending work, dispatches workers, heartbeats attempts, and integrates a persistent
Store. It does not mean ordinary application code, and none of these methods is exposed
directly to the model.

### 7.2 `backgroundtask/local.Runner`

| API | Capability | Assessment |
|---|---|---|
| `New(Config)` | Bind a process-local closure registry to a Manager | Correct ownership boundary. Multiple runners reuse the same compatible registry. |
| `Run(Input, WorkFunc)` | Buffered process-local execution | Local-only semantics are explicit. |
| `RunStream(Input, StreamWorkFunc)` | Ephemeral string projection plus persisted chunks | Streaming is accurately scoped as a Local adapter, not Manager lifecycle. |
| `Manager()` | Recover shared lifecycle manager | Necessary for middleware notification validation and control wiring. |

`RunStream` is justified by streaming shell UX. It does not imply that Durable work
cannot stream. Durable subagents project typed events through internal ADK context while
foreground and persist those events to the output feed for replay.

### 7.3 Internal foreground coordinator

`adk/internal/foreground.Run`:

- starts an already-submitted pending task on the current worker;
- waits for the active attempt to become visible;
- handles caller cancellation;
- enforces foreground timeout;
- consults `ShouldAutoBackground`;
- marks the projection boundary when detached;
- sends deterministic timeout control otherwise.

It replaces public `RunSubmitted`. Event forwarding remains internal because ADK event
receivers and parent-event projection are Eino implementation details.

### 7.4 Executor SPI

| API | Capability | Assessment |
|---|---|---|
| `Key()` | Stable executor identity | Required for worker routing. |
| `LeaseExpiryPolicy()` | Recovery invariant | Correctly executor-owned. |
| `ValidateSpec` | Validate serialized intent | Required before persistence. |
| `ValidateExecution` | Validate worker-local dependencies | Required after reconstruction. |
| `ValidateCheckpoint` | Check stored checkpoint compatibility | Required for safe resume. |
| `ValidateResume` | Validate/normalize resume input | Keeps domain logic out of Manager. |
| `SupportsDrain` | Declare planned-suspension support | Necessary for bounded shutdown. |
| `Execute` | Reconstruct and run one attempt | Canonical provider operation. |

`ExecutionRuntime` exposes attempt-scoped controls, background projection signal,
output append, and output-file failure reporting. It does not expose general task lookup
or arbitrary Store mutation.

### 7.5 Store SPI

Store owns:

- task creation and CAS transitions;
- lease creation, heartbeat, and expiry resolution;
- attempt fencing;
- cancellation intent and acknowledgement;
- checkpoint, suspension, and resume persistence;
- append-only output records and replay;
- lifecycle notification outbox records.

The interface is intentionally storage-provider-facing and therefore broader than the
ordinary application API. Here, “storage provider” means the host implementation of the
durable Store contract, not application or model code. External implementations must
treat task IDs as bearer capabilities and must not leak them in logs or unauthorised
listing surfaces.

### 7.6 Middleware configuration

Local model-facing middleware receives `*backgroundtask/local.Runner`.

Durable subagent middleware receives:

- shared `Manager`;
- optional foreground timeout and auto-background policy;
- output mirror configuration;
- `RunOptionsFactories`;
- validated notification delivery runtime.

DeepAgent forwards one shared Manager and constructs/reuses a Local runner for shell work
even when subagents use Durable execution.

## 8. Durable Subagent Reconstruction

Task payload v2 stores only non-derivable, serializable invocation data:

```text
version
subagent_name
query
```

`query` is the initial user input passed to `Runner.Query`; it is not the registered
Agent's system `Instruction`. The model-facing tool retains its compatible `prompt`
field and maps it to `SubmitRequest.Query`.

Child identities are deterministic and therefore not persisted:

```text
child session = <taskID>/session
checkpoint    = <taskID>/checkpoint
```

The executor derives both from `Task.Spec.ID` on every worker. The background-task
checkpoint envelope stores only interrupted target IDs and a sequence number.

Resume has one Eino-native meaning: restore and continue from the derived checkpoint.
Empty resume input calls `Runner.Resume`, which performs implicit resume-all. Supplied
target data is validated against the interrupted target IDs and passed to
`Runner.ResumeWithParams`. Sending a new query to the existing child session is not a
resume mode and is not currently exposed.

This is an intentional Alpha wire break. Version 1 payloads are rejected as unsupported
rather than silently reinterpreted; hosts must drain or discard version 1 tasks before
deploying workers that only understand version 2.

It does not serialize agents, functions, output backends, event formatters, or
`AgentRunOption` values.

Every worker reconstructs those through `AgentRegistration`:

```text
Agent
OutputStore
EventFormat
RunOptionsFactory
```

The accepted deployment contract is full semantic homogeneity:

- every eligible worker must register an equivalent complete `AgentRegistration` for a
  given subagent name;
- equivalence must hold for the full lifetime of resumable tasks, including rolling
  deployments;
- incompatible changes require draining existing tasks or using a new subagent name.

No registration revision is persisted under this accepted contract.

## 9. Notification Guarantee

Generic Manager usage remains notification-optional.

Model-facing subagent, filesystem, task-control, and DeepAgent middleware require a
`NotificationDeliveryRuntime` during construction. Validation checks:

- Store implements `NotificationOutbox`;
- the session-inbox route exists;
- sink dependencies are complete;
- host readiness validation succeeds.

Runtime outages are handled by durable outbox retry and idempotent inbox delivery.
Construction-time validation guarantees structural readiness, not permanent external
service availability.

## 10. Task IDs

The sole default generator uses:

```text
<kind-prefix> + Base64URL(16 bytes from crypto/rand)
```

The suffix contains 128 random bits and is opaque, URL-safe, and compact. `Config.IDGen`
exists for host-defined typed IDs. Task IDs are bearer capabilities and must not appear in
logs, public paths, or unauthorised enumeration APIs.

## 11. Removed Surface

The following APIs were removed rather than retained as Alpha compatibility aliases:

- `Manager.Run`;
- `Manager.RunStream`;
- `Manager.RunSubmitted`;
- contextless `Manager.Get`;
- terminal-only `Manager.Wait`;
- result-discarding `Manager.Cancel`;
- `Manager.List`;
- `Manager.Subscribe`;
- `TaskEvent`;
- public observer registration;
- `RegisterAgent`;
- exported durable subagent `TaskPayload`;
- durable payload `prompt`, `child_session_id`, `checkpoint_id`, `resume_mode`, and
  `allow_empty_resume`;
- `ResumeMode`, `ResumeNextTurn`, and next-turn resume-marker machinery;
- public `canceling` state;
- caller-controlled `Spec.LeaseExpiryPolicy`.

The canonical names are now:

```text
Submit / Get / WaitUpdate
ReadOutput / RequestCancel / Resume
ListPending / Execute
```

## 12. Remaining Risks

### 12.1 External Store conformance

`MemoryStore` is a deterministic reference implementation, not a durable backend. A real
multi-process Store must be tested for:

- atomic lease claim and expiry;
- stale-attempt rejection;
- cancellation/completion races;
- output sequence assignment under concurrent workers;
- resume one-shot consumption;
- notification outbox atomicity.

### 12.2 Worker-control bridge visibility

`MarkBackgrounded` and `RequestControl` are exported because
`adk/internal/foreground` is a different Go package. They are documented as
worker-runtime coordination, but remain technically callable by applications. If the
public surface is frozen later, consider returning a restricted worker handle from
Manager rather than adding more direct coordination methods.

### 12.3 Output retention and pagination

The feed has bounded page reads and per-record size enforcement, but production Stores
must define retention, total-size quotas, archival, and deletion with the owning task.

## 13. Final Verdict

| Dimension | Score | Summary |
|---|---:|---|
| Conceptual coherence | 5/5 | Lifecycle, Local closures, projection, and durable output have distinct owners. |
| Minimality | 4/5 | Legacy and observer APIs are removed; two worker-control bridges remain exported. |
| Intuitiveness | 5/5 | Names describe task update waiting, cancellation intent, and executor ownership. |
| Functional uniqueness | 5/5 | No duplicate Get/Wait/Cancel or Local/Durable run coordinators remain. |
| Local compatibility | 5/5 | Existing model behavior is preserved except intentional transcript-label clarification. |
| Durable correctness | 4/5 | Protocol is coherent; production Store conformance remains the main external obligation. |
| Operational safety | 4/5 | Notification construction and registration homogeneity are explicit; output retention remains host-defined. |

The API is now suitable for Alpha stabilization. The next quality gate should focus on a
real persistent Store conformance suite rather than adding more lifecycle surface.
