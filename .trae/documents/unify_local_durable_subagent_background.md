# Local / Durable Background Task: Current Architecture and API Audit

## 1. Scope and Baseline

This document describes the current `feat/durabletask` branch state. It replaces
the pre-implementation proposal with an as-built architecture and a ground-up API
audit.

The model-facing compatibility baseline is commit
`12c3d1b9ce23e8f8678b49e938dd5f8682ba7034`, the last commit before durable
background execution was introduced.

The feature has four distinct audiences:

1. The model, through `agent`, `execute`, `task_output`, and `task_stop`.
2. An application embedding Eino, through `backgroundtask.Manager`.
3. A durable worker/provider, through `Store`, `Executor`, Runner, and notification SPIs.
4. The prebuilt Deep Agent, which wires the first three layers together.

## 2. Executive Verdict

### 2.1 Is model-facing compatibility complete?

**No, not exactly.**

The important execution semantics are compatible:

- Local subagent, durable subagent, and managed shell use the same task lifecycle coordinator.
- Local and durable subagent tools expose the same JSON schema.
- Foreground success returns the same final subagent text.
- Explicit background, foreground timeout, auto-background, caller cancellation, and
  output-file fail-soft behavior are aligned.
- `task_output` remains human-readable and supports blocking/non-blocking queries.
- Shell streaming still provides foreground chunks and a bounded explicit-background
  startup preview.
- Local cancellation retains its original terminal semantics without exposing the
  Durable-only `canceling` state.

However, exact model-visible compatibility with `12c3d1b9` is not complete:

| Difference | Current behavior | Baseline behavior | Assessment |
|---|---|---|---|
| Local `task_stop` success text | `Successfully stopped task: <id>` | `Successfully stopped task: <id>` | Exact compatibility preserved. |
| Durable `task_stop` text | `canceling`: `Stop requested ...`; `canceled`: `Successfully stopped ...` | No Durable baseline | Additive state-aware behavior. |
| Control prompt notification promise | Unconditionally says tasks will notify | Same promise | Exact text compatibility restored; host must fulfill the contract. |
| Shell prompt notification promise | Promises completion notification | Same promise | Exact compatibility restored. |
| Subagent launch text/schema description | Promises `You will be notified` | Same promise | Exact compatibility restored. |
| Subagent failure/cancel text | Includes `(<description>)` | Same text | Exact compatibility restored. |
| Default subagent JSONL record | Contains `agent_name` and `message` without a synthetic type field | Same record shape | Exact compatibility restored. |
| Reliable transcript label | Kind-specific `Event transcript (JSONL)` / `Command output transcript` | Generic `Output file` | Clearer semantics, but exact text drift. |
| Default task IDs | 128-bit random `subagent_...` / `bash_...` / `task_...` | Older generator/layout | Intentional security break; task IDs are opaque. |
| Durable-only states | Durable tasks may return `pending`, `waiting_input`, `suspended`, or `canceling` | States did not exist | Necessary additive behavior; Local tasks transition directly from `running` to `canceled`. |

The correct claim is therefore:

> The feature is behaviorally compatible for normal foreground/background execution,
> with intentional durable and security extensions, but it is not byte-for-byte or
> text-for-text model-facing compatible with the baseline.

### 2.2 Overall API verdict

The architecture is sound but the exported API is not yet minimal enough to stabilize.
The model-facing surface is small and coherent. The application and provider surfaces
currently overlap, and notification provider contracts enlarge the core package.

Highest-priority API issues:

1. `Manager` exposes duplicate legacy and context-aware operations:
   `Get`/`GetTask`, `Wait`/`WaitTask`, and `Cancel`/`RequestCancel`.
2. `TaskPayload` is exported while documented as executor-private, and
   `RegisterAgent` duplicates the more complete `Register`.
3. Completion notification is promised consistently, but delivery remains a host integration
   obligation rather than a construction-time guarantee.
4. `Manager.Subscribe` and `Manager.List` are process-local views whose names imply a
   complete durable view.
5. Equivalent `RunOptionsFactory` configuration across workers remains a deployment
   invariant; task payloads currently identify only the registered sub-agent name.

Resolved during this audit:

- The public `RegisterObserver` / `DeactivateObserver` registry was removed.
- Foreground parent-event forwarding and streaming presentation now use an
  `adk/internal/agenttool` context value preserved by `RunSubmitted`.
- Durable execution options are reconstructed from worker `AgentRegistration` on every
  attempt. Invocation-scoped `AgentRunOption` values are rejected instead of being applied
  only to the first same-process foreground attempt.

## 3. Current Architecture

### 3.1 Authority boundaries

- `Store` is authoritative for task intent, lifecycle, result, checkpoint envelope,
  output reliability, CAS version, and attempt fencing.
- Session and checkpoint stores are authoritative for resumable agent history.
- `OutputFile` is an optional incremental transcript, not the terminal result authority.
- Process-local closures and foreground event projection are ephemeral sidecars carried
  only through internal execution context.
- A task ID is an opaque bearer capability. The default ID contains 128 bits of
  `crypto/rand` entropy and is not used in default output paths.
- UUID v4 is also a valid default: it provides 122 random bits in a standard
  36-character representation, while the current raw URL-safe encoding carries 128
  random bits in 22 characters. `Config.IDGen` already permits either choice. UUID v7
  is a worse bearer-capability default because it exposes creation time.

### 3.2 Execution backends

| Backend | Persistence | Restart | Drain | Waiting input | Model contract |
|---|---|---|---|---|---|
| Local subagent | Task state persisted; work closure process-local | No | No | No durable resume | `agent` + common controls |
| Durable subagent | Task, child identity, checkpoint, session persisted | Yes | Yes | Yes, host resumes | Same `agent` + common controls |
| Managed shell | Task state persisted; shell execution process-local | No | No | No | `execute` + common controls |

### 3.3 Shared lifecycle

Canonical statuses are:

`pending -> running -> completed | failed | canceled`

Durable pause/cancel paths add:

- `running -> waiting_input -> pending`
- `running -> suspended -> pending`
- Durable cancellation: `running -> canceling -> canceled`
- Local cancellation: `running -> canceled`
- expired `retry` lease -> `pending`
- expired `fail` lease -> `failed`

`Run`, `RunStream`, and `RunSubmitted` share the same foreground/background policy:

- Explicit background detaches before execution is released.
- Foreground timeout invokes `ShouldAutoBackground`.
- Rejected auto-background becomes deterministic failure:
  `timed out after <n>ms`.
- Foreground caller cancellation stops Local work directly and records cancellation
  intent for Durable work.
- Explicit-background caller cancellation does not cancel the task.

### 3.4 Durable subagent execution

Durable submission persists versioned JSON v1 containing:

- `subagent_name`
- `prompt`
- `<taskID>/session`
- `<taskID>/checkpoint`
- resume mode and empty-resume policy

The worker provides deployment dependencies through an equivalent
`TypedRunnerConfig`, then calls:

```go
runner.ExecuteBackgroundTask(ctx, manager, taskID)
```

This binds the Runner environment before `Manager.Execute`. Missing session/checkpoint
dependencies are rejected before `Store.Start`, leaving the task pending.

The serialized `subagent_name` also selects a worker-local `AgentRegistration`.
`RunOptionsFactory`, when configured for that registration, reconstructs fresh
deployment-owned `AgentRunOption` values for every initial or resumed attempt. Factories
are not serialized; every worker serving the same name must configure a semantically
equivalent factory. The factory receives no execution context, preventing hidden
dependence on launching-process values. Invocation-scoped options are rejected because
they cannot be reconstructed after worker reassignment.

For same-process foreground execution only, middleware attaches parent-event receivers
and streaming presentation to an internal context value. `RunSubmitted` preserves context
values while detaching cancellation, so the executor can project events until the task
backgrounds. Cross-process and explicitly backgrounded execution have no such projection.

### 3.5 Output and notification behavior

- Local and durable subagents use the same default JSONL event formatter.
- Durable resume appends to the existing transcript.
- Shell output is a command transcript; subagent output is an event transcript.
- Open/encode/write/close failure records `OutputFileErr` and execution continues.
- Failure to record `OutputFileErr` is an infrastructure failure and the attempt cannot
  commit a misleading successful/reliable result.
- Durable subagent tasks target `session_inbox`, but delivery requires the host to run
  `Dispatcher` with an outbox, sink registry, inbox, and session activator.
- Deep Agent constructs tools and Manager wiring; it does not itself run the durable
  notification dispatcher.
- `sessionnotify.MemoryInbox` deep-copies notification target metadata as well as task
  snapshots, so caller- or reader-owned maps cannot mutate pending routing state.

## 4. Complete Capability Inventory and Audit

### 4.1 Model-facing tools

| Capability | Exposed API | Local | Durable | Design assessment |
|---|---|---:|---:|---|
| Launch subagent | `agent{subagent_type,prompt,description,run_in_background}` | Yes | Yes | Good. One schema and one conceptual operation. |
| Run shell | `execute{command,run_in_background,timeout}` | Yes | Shell remains local only | Good. `timeout` is foreground occupancy, but its schema text should say policy may move or stop the task. |
| Query task | `task_output{task_id,block?,timeout?}` | Yes | Yes | Good and minimal. Blocking default and bounded timeout are intuitive. |
| Stop task | `task_stop{task_id}` | Yes | Yes | Good. Local preserves its success text; Durable distinguishes accepted cancellation from terminal cancellation. |
| Resume task | None | N/A | Host-only | Coherent boundary for now, but `waiting_input` text should not imply the model can resolve it. |
| Read transcript | Filesystem `read_file`/backend-dependent path | Optional | Optional | Useful but not self-contained. Hosts that configure output files must expose a compatible reader. |

Model-facing uniqueness is strong: launch, inspect, stop, and read each have one role.
The remaining notification concern is operational enforcement, not contradictory wording.

### 4.2 Subagent middleware configuration

| API | Capability | Assessment |
|---|---|---|
| `subagent.New` / `NewTyped` | Construct subagent middleware | Good typed/default pair. |
| `TypedConfig.SubAgents` | Fixed agent catalog | Good; duplicate names rejected. |
| `ToolName`, `ToolDescriptionGenerator`, `SystemPrompt` | Presentation customization | Good, established middleware pattern. |
| `TypedBackgroundConfig{Local,Durable}` | Strict backend choice | Good concept. Pointer union is idiomatic enough and validated. |
| `TypedLocalBackgroundConfig` | Manager + optional transcript policy | Good. |
| `TypedDurableBackgroundConfig` | Same plus cross-worker formatter constraint | Good, but structurally duplicates Local config. |
| `RunOptionsFactories` | Reconstruct deployment-owned options by serialized sub-agent name | Good boundary. Semantic equivalence across workers is an operational invariant. |
| `AgentEventFormat` | Custom transcript framing | Useful expert extension. Compatibility responsibility is correctly documented. |
| `NameFromTask` | Decode host policy input | Good, narrow domain helper. |

The Local/Durable union is conceptually coherent. The two child configs currently have
identical fields, but separate types prevent accidental mode ambiguity and leave room for
mode-specific evolution. That trade-off is acceptable.

### 4.3 Filesystem and Deep Agent configuration

| API | Capability | Assessment |
|---|---|---|
| `filesystem.BackgroundConfig` | Enable process-local managed shell | Good. It correctly does not pretend shell is durable. |
| `filesystem.CommandFromTask` | Decode shell command for host policy | Good, symmetric with `NameFromTask`. |
| `filesystem.ExecuteTaskType` | Stable task kind | Good. |
| `deep.TypedBackgroundConfig{Local,Durable}` | Top-level mode selection | Useful facade, but duplicates subagent configuration. |
| `deep.TypedLocalBackgroundConfig` | Shared Manager + output dir | Adequate. Generic parameter is unused. |
| `deep.TypedDurableBackgroundConfig` | Shared Manager, output dir, and run-option factories | Adequate. It forwards worker reconstruction configuration; generic parameter is unused. |

Recommended cleanup: make Deep’s two leaf background configs non-generic, or define one
non-generic leaf config used by the strict Local/Durable wrapper.

### 4.4 Application lifecycle API

| API | Capability | Assessment |
|---|---|---|
| `New`, `Config` | Construct Manager and policy | Good. Defaults are documented. |
| `Run` | Execute process-local buffered work | Good high-level API. |
| `RunStream` | Execute process-local streaming work | Powerful but necessarily complex; startup timing is well documented. |
| `RunSubmitted` | Start an already-submitted task with foreground projection | Necessary internally, but the name does not distinguish it from worker execution. |
| `Get` | Legacy contextless lookup | Redundant with `GetTask`. |
| `GetTask` | Context-aware authoritative lookup | Preferred API. |
| `Wait` | Legacy terminal-only wait with boolean result | Redundant and less expressive than `WaitTask`. |
| `WaitTask` | Context-aware version wait | Preferred primitive, though callers must understand version semantics. |
| `List` | List tasks submitted through this Manager instance | Misleading name; not a complete Store listing. |
| `ListPending` | Durable worker dispatch candidates | Good worker API, not an ordinary application list. |
| `Cancel` | Legacy contextless cancellation | Redundant with `RequestCancel`. |
| `RequestCancel` | Mode-aware cancellation: terminal Local stop or persisted Durable intent | Preferred API. The returned status is authoritative: Local returns `canceled`; active Durable work returns `canceling`. |
| `ResumeTask` | Validate and persist resume input | Good domain-neutral host API. |
| `Subscribe` | Process-local Manager lifecycle events | Useful but name overstates durability and completeness. |
| `Close` | Bounded drain/cancel shutdown | Good. Required deadline with active work is explicit and safe. |
| `Store` | Access provider SPI | Pragmatic, but enables bypass of Manager validation. |
| `Executors` | Access mutable registry | Needed by current middleware wiring, but leaks construction internals. |
| `AllocateTaskID` | Preallocate bearer capability | Useful expert operation, but no longer required by Durable subagent middleware. |
| `Submit` | Persist generic durable `Spec` | Good provider/application boundary. |
| `Execute` | Claim and execute a generic pending task | Good generic worker entry, but unsafe for Runner-dependent executors unless correctly wrapped. |

Recommended canonical application surface:

```text
Run / RunStream
GetTask / WaitTask
RequestCancel / ResumeTask
Close
```

Keep `AllocateTaskID`, `Submit`, `ListPending`, and `Execute` as an explicitly documented
worker/executor layer. Deprecate or remove `Get`, `Wait`, and `Cancel` before stabilization.
Rename `List` to `ListManagedTasks` or remove it until Store has a true general listing API.
Rename `Subscribe` to `SubscribeLocalEvents`.

### 4.5 Core task model and executor SPI

| API | Capability | Assessment |
|---|---|---|
| `Status` and status constants | Canonical lifecycle | Good. |
| `State` and `State*` aliases | Source compatibility | Redundant by design; acceptable only as temporary compatibility aliases. |
| `Spec` | Immutable serialized intent | Good separation from mutable `Task`. |
| `Task` | Canonical snapshot | Good; fields have distinct authority. |
| `LeaseExpiryPolicy` | Retry/fail process-loss behavior | Good and explicit. |
| `ControlKind`, `ControlRequest` | Stop/drain/timeout control | Coherent executor protocol. |
| `ExecutionResult` | Executor lifecycle output | Good, but valid field/status combinations are enforced only at runtime. |
| `ExecutionRuntime` | Attempt-scoped controls and output reporting | Good narrow capability interface. |
| `Executor` | Validation, resume, drain, execute SPI | Coherent but large; acceptable for an alpha provider SPI. |
| `ExecutorRegistry` | Keyed executor registration | Good. `Keys` ordering is unspecified and should be documented. |
| Error sentinels | Stable transition/fencing classification | Good. |

The most important positive design choice is explicit `ExecutionRuntime`; it prevents
attempt-scoped controls and output reliability from becoming ambient context side channels.

### 4.6 Store SPI

`Store` exposes these transition capabilities:

- Create: `Create`, `CreateAndStart`
- Read/dispatch: `Get`, `ListPending`, `Wait`
- Attempt lease: `Start`, `Heartbeat`
- Output reliability: `ReportOutputFailure`
- Terminal outcomes: `Complete`, `Fail`, `Cancel`
- Pause/resume: `WaitInput`, `Suspend`, `Resume`, `ReleaseSuspension`
- Cancellation intent: `RequestCancel`

Every transition uses a request struct with `TaskID` and, where applicable,
`ExpectedVersion`. This is repetitive but explicit and future-compatible.

Design assessment:

- Conceptually coherent as a lifecycle state-machine SPI.
- Correctly keeps Store authoritative and executor payload opaque.
- Large implementer burden: 16 methods plus `NotificationOutbox` for full durable delivery.
- `CreateAndStart` exists solely for non-reconstructable process-local work; this is a
  justified semantic operation, not merely a convenience alias.
- `Cancel` accepts an active attempt in either `running` or `canceling`: Local executors
  commit directly from `running`, while Durable executors acknowledge persisted intent
  from `canceling`.
- `ReleaseSuspension` is exposed only through Store, while `ResumeTask` is exposed through
  Manager. The host control layer is therefore asymmetric.
- The package explicitly marks these SPIs provisional; they should not be declared stable
  before at least one external multiprocess Store passes conformance.

### 4.7 Durable subagent executor API

| API | Capability | Assessment |
|---|---|---|
| `ExecutorKey` | Stable executor identity | Good. |
| `ResumeMode` | Native interrupt or next-turn resume | Good and explicit. |
| `TaskPayload` | Serialized wire payload | Bad exposure: exported but documented “executor-private.” |
| `EventFormat` | Transcript encoding | Good expert SPI. |
| `RunOptionsFactory` | Reconstruct run options on each worker attempt | Good deployment boundary; must return fresh options and remain semantically equivalent across workers. |
| `AgentRegistration` | Bind serialized name to worker dependencies and option factory | Good concept. |
| `Executor` | Durable subagent executor | Good. |
| `Register` | Register full worker dependencies | Good canonical operation. |
| `RegisterAgent` | Register agent only | Redundant convenience operation. |
| `SubmitRequest` / `Submit` | Persist a durable subagent task | Good application helper. |

Recommended shape:

- Unexport `TaskPayload`.
- Keep one registration method. Prefer
  `Register(name string, registration AgentRegistration[M])`.
- If payload inspection is a supported host capability, expose a read-only
  `DecodeTask(task) (TaskInfo, error)` rather than the wire struct.
- If per-task option profiles become necessary, persist a versioned profile selector
  and resolve it through registration; never serialize `AgentRunOption`.

### 4.8 Runner API

| API | Capability | Assessment |
|---|---|---|
| `TypedRunner.ExecuteBackgroundTask` | Correct worker entry for Runner-dependent tasks | Good and necessary. |
| `TypedRunnerEnvironment` getters | Immutable session/checkpoint providers | Coherent plumbing. |
| `TypedRunnerEnvironmentFromContext` | Read ambient Runner environment | Necessary across current package boundaries, but broadens public API for one implementation need. |

The worker entry is intuitive. The context accessor is less ideal: it exposes a framework
plumbing mechanism as general public API. Prefer an explicit exported environment interface
owned by the background executor contract, or move the durable subagent executor closer to
Runner so the accessor can become internal.

### 4.9 Notification and session activation API

Capabilities:

- Durable route: `NotificationTarget`, `Notification`, `NotificationKind`
- Outbox lease: `NotificationOutbox`, receive/ack request/result types
- Dispatch: `NotificationSink`, `RoutedNotificationSink`,
  `NotificationSinkRegistry`, `SinkRegistry`, `Dispatcher`
- Session inbox: `SessionNotificationInbox` and enqueue/list/ack types
- Session activation: `SessionActivator` and activation types
- Reference adapters: `sessionnotify.Sink`, `MemoryInbox`, `TurnLoopActivator`

Assessment:

- The outbox -> dispatcher -> inbox -> activator chain is conceptually correct and preserves
  at-least-once delivery.
- These are deployment/provider SPIs, not core task operations. Keeping all route, inbox, and
  activation types in `backgroundtask` significantly enlarges the primary package.
- `NotificationSink` plus optional `RoutedNotificationSink` uses a type assertion to express
  two delivery contracts. A single target-aware sink method would be simpler.
- `SessionActivationStarted` exists, but `TurnLoopActivator` always reports `Queued`; this is
  acceptable for an interface, though the reference adapter does not demonstrate both states.
- Move provider-specific notification contracts into a `backgroundtask/notify` subpackage
  before stabilization.

## 5. Design Review

### 5.1 Concept Coherence

**Strong:** `Spec` vs `Task`, Task Store vs session/checkpoint stores, and result vs transcript
authority are clearly separated.

**Concern:** model-facing text consistently promises completion notification, but the delivery
path remains a host integration obligation rather than a construction-time invariant.

### 5.2 API Usability and Intuitiveness

**Strong:** the model gets four simple operations; strict Local/Durable selection prevents mixed
configuration.

**Concern:** application users must choose among overlapping Manager methods and distinguish
`RunSubmitted`, `Execute`, and `ExecuteBackgroundTask` without a clear audience boundary.

### 5.3 Minimum API Surface

**Strong:** no generic metadata bag, resolver, version/digest identity, or model-facing resume tool
was added.

**Concern:** legacy Manager aliases, the exported payload, and notification SPIs make
the Go API materially larger than necessary.

### 5.4 Backward Compatibility

**Strong:** normal Local execution, streaming, timeout, cancellation, and human-readable output are
substantially restored.

**Concern:** exact compatibility is broken only by the clearer transcript labels, opaque ID
generation, and additive Durable states. Control prompts, launch text, Local stop text, failure
text, and default JSONL framing match the baseline.

### 5.5 Module Separation and Layering

**Strong:** durable execution is executor-driven; Manager does not understand subagent payloads.

**Concern:** session notification provider contracts still live in the core package.

### 5.6 Cohesion vs. Tension

**Strong:** one Manager can deliberately coordinate heterogeneous local and durable tasks.

**Concern:** Manager combines a legacy process-local facade, durable worker coordinator, registry,
event bus, and shutdown owner. The overlap shows in duplicate methods and partial local views.

### 5.7 Elegance vs. Complexity

**Strong:** shared coordinator and explicit runtime remove duplicated lifecycle logic.

**Concern:** `RunStream` and Durable foreground projection remain inherently complex. The hardest
sections are stream preview/background transitions, Durable cancellation reconciliation, and
Durable subagent control/checkpoint handling. Local cancellation no longer participates in the
persisted `canceling` protocol.

### 5.8 Naming

| Name | Assessment |
|---|---|
| `Status` | Canonical and clear. |
| `State` | Redundant compatibility alias. |
| `RunSubmitted` | Ambiguous; does more than “run” and differs from worker execution. |
| `ExecuteBackgroundTask` | Clear worker entry. |
| `GetTask`, `WaitTask`, `RequestCancel`, `ResumeTask` | Clear application operations. |
| `Get`, `Wait`, `Cancel` | Redundant legacy names. |
| `List` | Misleading because scope is Manager-submitted IDs, not all Store tasks. |
| `Subscribe` | Misleading because events are process-local and incomplete. |
| `TaskPayload` | Contradicts “executor-private” documentation. |
| `AgentRegistration` | Clear. |
| `RunOptionsFactory` | Clear: deployment-owned reconstruction rather than serialized options. |
| `ReleaseSuspension` | Clear Store transition, missing Manager-level counterpart. |

### 5.9 Readability

The three hardest sections are:

1. `Manager.coordinateSubmittedStream`: multiple timers, caller state, execution state, and drain
   behavior interact.
2. Durable `taskRuntime` cancellation/version reconciliation: lock ordering and CAS ownership are implicit.
3. Durable subagent `Execute`: Runner cancellation, control translation, foreground projection,
   output writing, interrupts, and checkpoint validation converge in one path.

Existing comments are good, but state-transition tables near these functions would reduce the cost
of future changes.

### 5.10 Duplication

- Local and durable subagent formatting is shared: good.
- Local and durable config structs duplicate fields: acceptable for strict mode typing.
- Deep duplicates the Local/Durable wrapper: moderate facade duplication.
- Manager legacy/context-aware methods are semantic duplication and should be removed.
- `NotificationSink`/`RoutedNotificationSink` duplicate delivery shape through optional capability.

### 5.11 Public API Documentation

Overall rating: **3/5**.

High-risk methods such as `Run`, `RunStream`, `Close`, `Executor`, and Store transitions are
documented well. Important gaps remain:

- `Manager.Store`, `Executors`, `AllocateTaskID`, `Submit`, `GetTask`, `WaitTask`,
  `RequestCancel`, `ResumeTask`, and `Execute` need audience and misuse guidance.
- `TypedRunnerEnvironment` getter methods lack comments.
- subagent exported errors and several exported leaf config types lack direct comments.
- `TaskPayload` documentation is internally contradictory.
- `Config.IDGen` says the default ID is base62, while `id.go` uses raw URL-safe base64.
- `Manager` documentation refers to a free `Run` function; only the method exists.
- subagent `New` documentation refers to the removed `Config.Manager` field.
- Deep’s background config comment says it holds durable identity/session dependencies, but those
  now come exclusively from `TypedRunnerConfig`.

### 5.12 Internal Comments

Internal comments are strongest around streaming and output fail-soft behavior. Add focused comments
for:

1. Why Durable `RequestCancel` takes the active runtime lock before Store CAS, while Local
   cancellation directly signals the in-process attempt and waits for its terminal commit.
2. Why `List` is intentionally Manager-local rather than Store-global.
3. Why foreground projection is internal context, why it stops at the background boundary,
   and why detach does not wait for in-flight receivers.

## 6. Scorecard

| Dimension | Rating | Notes |
|---|---:|---|
| Concept coherence | 4/5 | Authority boundaries are strong; notification delivery is not enforced by construction. |
| API usability | 3/5 | Model API is simple; Go API has audience ambiguity. |
| Minimum API surface | 3/5 | The observer leak is removed; Manager aliases and provider SPIs remain broad. |
| Backward compatibility | 4/5 | Baseline text and Local behavior are restored; transcript labels and IDs intentionally differ. |
| Module separation | 4/5 | Foreground projection is internal; notification provider contracts remain in core. |
| Cohesion | 3/5 | Shared Manager is useful but owns too many roles. |
| Elegance | 4/5 | Explicit runtime and shared coordinator are good solutions to inherent complexity. |
| Naming | 3/5 | Most names are clear; `List`, `Subscribe`, and `RunSubmitted` need correction. |
| Readability | 4/5 | Complex paths are documented and tested. |
| Duplication | 3/5 | Config duplication is acceptable; Manager duplication is not. |
| Public API docs | 3/5 | Core semantics are strong; provider audience guidance is incomplete. |
| Internal comments | 4/5 | Good overall, with several concurrency invariants still implicit. |

## 7. Prioritized Recommendations

### P0: Reduce the durable executor API before release

- Unexport `TaskPayload`, or replace it with a stable read-only decoder.
- Remove `RegisterAgent` or `Register`; retain one canonical registration API.
- Document `RunOptionsFactory` as deployment configuration and require equivalent
  registration for every worker serving a serialized sub-agent name.

### P0: Enforce the notification contract

The model-facing API consistently preserves the baseline promise that background completion will
notify the model. Document and validate the required host integration:

- Durable tasks require outbox dispatch, a session inbox, and a session activator.
- Process-local tasks require a `Manager.Subscribe` consumer that projects completion into the
  active model/session channel.
- Prebuilt wiring should either install these paths or require them explicitly when background
  execution is enabled.

### P1: Separate application and worker Manager surfaces

- Canonicalize on `GetTask`, `WaitTask`, `RequestCancel`, and `ResumeTask`.
- Deprecate/remove `Get`, `Wait`, and `Cancel`.
- Rename `List` and `Subscribe` to reveal their Manager-local scope.
- Document `AllocateTaskID`/`Submit`/`ListPending`/`Execute` as worker/executor APIs.
- Consider a narrow `Worker` facade instead of exposing mutable `Store()` and `Executors()`.

### P1: Resolve model-facing compatibility drift

- Decide whether the kind-specific transcript labels justify their small text incompatibility.
- Keep golden tests comparing Local and Durable tool schema, launch text, terminal text, prompts,
  and default JSONL framing against the chosen contract.
- Correct stale public comments for ID encoding, Manager launch APIs, subagent configuration, and
  Deep Runner dependency ownership.

### P2: Reduce provider surface before stabilization

- Move notification/outbox/session activation contracts into a subpackage.
- Replace dual `NotificationSink` interfaces with one target-aware method.
- Validate the Store SPI against a real external multiprocess implementation.
- Consider a Manager-level `ReleaseSuspension` operation for symmetry with `ResumeTask`.

## 8. Verification Status

Executed against the current repository:

```bash
git diff --check
go test ./adk/...
go test -race ./adk/internal/agenttool \
  ./adk/backgroundtask/subagent \
  ./adk/middlewares/subagent \
  ./adk/prebuilt/deep
```

All commands passed.
