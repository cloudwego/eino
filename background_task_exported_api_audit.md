# Background Task Exported API Audit

## Scope

This document inventories and audits the exported Go and model-facing API of
the background-task capability on the current `feat/durabletask` branch.

Included:

- `adk/backgroundtask`
- `adk/backgroundtask/local`
- `adk/backgroundtask/sessionnotify`
- `adk/backgroundtask/shell`
- `adk/backgroundtask/subagent`
- `adk/backgroundtask/tool`
- `adk/backgroundtask/worker`
- `adk/middlewares/backgroundtask`
- background-task integration APIs in `adk`, `adk/middlewares/filesystem`,
  `adk/middlewares/subagent`, and `adk/prebuilt/deep`
- the model-facing `task_output`, `task_stop`, `run_in_background`, and managed
  tool stream contracts

Excluded:

- unexported implementation details
- test-only helpers other than exported production conformance APIs
- unrelated exports from mixed-purpose packages, such as normal filesystem
  read/write request types and non-background DeepAgent configuration

Each grouped row explicitly lists every declaration and exported field covered
by its verdict.

### Verdicts

| Verdict | Meaning |
|---|---|
| **Keep** | Coherent, necessary, and adequately shaped |
| **Keep; document** | API shape is sound, but its contract is underspecified |
| **Rework** | Public shape should change before stabilization |
| **Compatibility** | Acceptable only as an explicitly deprecated compatibility surface |
| **Remove** | Unnecessary public surface |

## Executive Findings

### Blockers Before Stabilization

1. **`filesystem.BackgroundConfig` has two Manager authorities.** `Runner`
   owns one Manager while `Manager` may independently specify another; runtime
   validation rejects disagreement, but the API permits it. Split local and
   recoverable configurations under one Manager, following DeepAgent's
   capability-based API.
2. **The task-store, task-event-store, and notification SPIs are public but
   explicitly provisional.**
   They expose a large implementation commitment without a durable-provider
   conformance package. Either move them under an experimental package or ship
   a reusable conformance suite before declaring stability.

### Important Non-Blockers

1. Replace `ToolStreamEvent.Type string` with a typed discriminator and exported
   constants. The current wire contract relies on undocumented string literals.
2. Document limit normalization for `ReadRecentTaskEvents`, `ListPending`,
   `ListSuspended`, inbox listing, and outbox receive APIs. Current concrete
   defaults are not part of the interface contract.
3. Mark legacy filesystem `Config` itself deprecated, not only
   `NewMiddleware`, because every new background field is otherwise duplicated
   across two public configuration types.

### Recently Resolved

1. Removed the unused `CreateAndStart` lifecycle transition; submission and attempt
   claiming now have one path through `Create` and `Start`.
2. Renamed active-attempt cancellation acknowledgement from `Cancel` to
   `AckCancel`, preserving the distinction from durable `RequestCancel` intent.
3. Renamed the lifecycle long poll to `WaitForTaskVersion` and documented its
   exact `Task.Version > AfterVersion` predicate.
4. Added suspended-task discovery and Manager-owned release, closing the
   operational gap that could leave planned suspensions stranded.
5. Removed the exported test-only clock hook from `InMemoryStoreConfig`.
6. Replaced DeepAgent's ambiguous `Local`/`Durable` background mode with one
   Manager and explicit durable-sub-agent, recoverable-shell, and local-shell
   capability blocks.
7. Removed `ValidateResume` from the generic `Executor` contract. Manager now
   persists opaque resume input, while the durable sub-agent executor privately
   validates target data and defensively returns invalid persisted input to
   `waiting_input`.
8. Moved atomic idempotent executor installation from Manager to
   `ExecutorRegistry.LoadOrRegister`; integration configs now receive the
   registry explicitly instead of using Manager as a registry service locator.
9. Removed `tool.ArgumentsFromTask`; it exposed decoding of a private versioned
   payload envelope without any production caller.
10. Split the monolithic `Store` SPI into authoritative lifecycle `TaskStore`
    and append-only progress `TaskEventStore` capabilities. Manager receives
    both explicitly and no compatibility alias remains.
11. Replaced Store-shaped executor runtime methods with `EmitProgress` and
    `ReportTranscriptFailure`. `ProgressEmission` exposes only resolved event
    identity and replay status; task ID, attempt, CAS version, and provider
    request/result envelopes remain inside the private runtime adapter.
12. Removed `Manager.ValidateNotificationDelivery`,
    `NotificationDeliveryRuntime`, `NotificationDeliveryValidation`, and the
    validation-only `sessionnotify.Runtime`. Notification routing is no longer
    repeated through integration configs; Manager directly requires
    `NotificationOutbox` when submitting a notification-bearing task.
13. Removed notification `ConsumerID` from receive and dispatcher APIs.
    Each delivery now carries an opaque receipt authorizing acknowledgement
    only during its current `LeaseDuration`; expired and superseded receipts
    cannot acknowledge a redelivered notification.
14. Collapsed notification delivery to its only supported destination: the
    parent session. `Spec.NotifySession` is the only notification intent and
    `Spec.SessionID` is the only destination identity. Generic targets, sink
    variants, sink registries, and the session sink adapter were removed;
    Dispatcher now enqueues and activates the session directly.

## 1. Core Package: `adk/backgroundtask`

Sources:
[`manager.go`](adk/backgroundtask/manager.go),
[`types.go`](adk/backgroundtask/types.go),
[`store.go`](adk/backgroundtask/store.go),
[`executor.go`](adk/backgroundtask/executor.go),
[`notification.go`](adk/backgroundtask/notification.go), and
[`in_memory_store.go`](adk/backgroundtask/in_memory_store.go).

### Lifecycle Names

| Exported API | Shape | Verdict | Audit |
|---|---|---|---|
| `Status` | `type Status string` | **Keep** | Appropriate canonical lifecycle type. |
| `StatusPending`, `StatusRunning`, `StatusWaitingInput`, `StatusSuspended`, `StatusCompleted`, `StatusFailed`, `StatusCanceled` | Typed constants | **Keep** | Complete closed set with meaningful names. Unknown values are rejected at TaskStore boundaries. |
| `LeaseExpiryPolicy` | `type LeaseExpiryPolicy string` | **Keep** | Correctly separates retryability from status. |
| `LeaseExpiryRetry`, `LeaseExpiryFail` | Typed constants | **Keep** | Minimal and exhaustive current policy set. |
| `ControlKind` | `type ControlKind string` | **Keep** | Appropriate attempt-local control discriminator. |
| `ControlStop`, `ControlDrain`, `ControlTimeout` | Typed constants | **Keep** | Distinguishes cancellation, graceful checkpointing, and deterministic timeout. |
| `ControlRequest` | Fields: `Kind`, `Reason` | **Keep** | Stop reason comes from durable cancellation intent, drain reason is advisory, and timeout reason is guaranteed non-empty. |
| `ExecutionDirective` | `type ExecutionDirective string` | **Keep** | Correctly separates non-lifecycle execution instructions from terminal status. |
| `ExecutionDirectiveYield` | Typed constant | **Keep** | Necessary for an externally continuing operation to relinquish a worker. |

### Errors

| Exported API | Verdict | Audit |
|---|---|---|
| `ErrNotFound` | **Keep** | Stable sentinel needed by storage providers and Manager callers. |
| `ErrAlreadyExists` | **Keep** | Correct shared duplicate sentinel for task and registry creation. |
| `ErrVersionConflict` | **Keep** | Necessary CAS failure classification. |
| `ErrLeaseLost` | **Keep** | Necessary fencing failure classification. |
| `ErrIllegalTransition` | **Keep** | Appropriate lifecycle rejection sentinel. |
| `ErrInvalidExecutionResult` | **Keep** | Classifies executor results and TaskStore transition payloads that violate lifecycle result invariants. |
| `ErrAlreadyTerminal` | **Keep** | Useful distinction from a generic illegal transition. |
| `ErrDrainCheckpointUnavailable` | **Keep** | Precisely identifies planned drain failure; Manager stops lease renewal so recovery can use the last durable checkpoint. |
| `ErrCloseDeadlineRequired` | **Keep** | Makes bounded shutdown requirements machine-checkable and leaves Manager open. |
| `ErrUnsupportedExecutorPayloadVersion` | **Keep** | Precisely classifies a selected executor that cannot decode persisted `Spec.Payload`. |
| `ErrTaskEventIDConflict` | **Keep** | Distinguishes identical idempotent replay from same-EventID/different-bytes misuse. |

### Task Identity, Intent, and Snapshot

| Exported API | Shape | Verdict | Audit |
|---|---|---|---|
| `AllocateTaskIDRequest` | Field: `Kind` | **Keep; document** | `Kind` affects the default prefix but is not persisted as an independent task attribute. State that it must be a safe identifier segment. |
| `IDGenerator` | `func(context.Context, *AllocateTaskIDRequest) (string, error)` | **Keep** | Centralized generation is necessary and concurrency semantics are documented. |
| `Spec` | Fields: `ID`, `ExecutorKey`, `Kind`, `Payload`, `Description`, `OutputFile`, `SessionID`, `NotifySession`, `CreatedAt` | **Keep; document** | Coherent immutable envelope. `NotifySession` requires `SessionID`; no separate routing identity exists. Clarify which fields are caller supplied versus TaskStore assigned (`CreatedAt`), maximum sizes, and whether `OutputFile` is authoritative or derived. |
| `Task` | Fields: `Spec`, `LeaseExpiryPolicy`, `Status`, `Checkpoint`, `ResultData`, `ResultError`, `OutputFileErr`, `PendingResume`, `Version`, `Attempt`, `CancelRequestedAt`, `CancelReason`, `UpdatedAt`, `DoneAt` | **Keep; document** | Necessary snapshot. `CancelReason` durably carries first-write stop intent into replacement attempts and terminal `ResultError`. Add explicit copy ownership for byte slices and time pointers returned by providers. |
| `Config` | Fields: `Tasks`, `TaskEvents`, `Executors`, `IDGen` | **Keep; document** | The lifecycle/event boundary is explicit. Document that both providers must share a task namespace and that `TaskEvents` must fence appends against attempts authorized by `Tasks`; when omitted, it is inferred only if `Tasks` also implements `TaskEventStore`. |
| `New` | `func(context.Context, *Config) *Manager` | **Keep; document** | A constructor that cannot fail is convenient, but nil `Config` behavior and why `context.Context` is currently unused should be explicit. |

### TaskStore Requests

All fields listed below are part of the exported API.

| Exported API | Exported fields | Verdict | Audit |
|---|---|---|---|
| `CreateTaskRequest` | `Spec`, `LeaseExpiryPolicy` | **Keep** | Correctly persists immutable recovery policy with intent. |
| `StartTaskRequest` | `TaskID`, `ExpectedVersion` | **Keep** | Minimal claim/CAS input. |
| `HeartbeatRequest` | `TaskID`, `ExpectedVersion` | **Keep** | Minimal fenced liveness input. |
| `CompleteTaskRequest` | `TaskID`, `ExpectedVersion`, `Data` | **Keep** | Clear terminal transition. |
| `FailTaskRequest` | `TaskID`, `ExpectedVersion`, `Error` | **Keep** | Clear terminal transition; bounded error contract should be documented here. |
| `WaitInputTaskRequest` | `TaskID`, `ExpectedVersion`, `Checkpoint` | **Keep** | Correct atomic checkpoint plus waiting transition. |
| `SuspendTaskRequest` | `TaskID`, `ExpectedVersion`, `Checkpoint` | **Keep** | Correct atomic planned-suspension transition. |
| `YieldTaskRequest` | `TaskID`, `ExpectedVersion`, `Checkpoint` | **Keep** | The empty-checkpoint retention rule is documented and important. |
| `AckCancelRequest` | `TaskID`, `ExpectedVersion`, `Reason` | **Keep** | Precisely names active-attempt acknowledgement; a previously persisted cancellation reason remains authoritative. |
| `RequestCancelRequest` | `TaskID`, `ExpectedVersion`, `Reason` | **Keep** | Durable first-write intent transition, distinct from acknowledgement. |
| `ResumeRequest` | `TaskID`, `ExpectedVersion`, `Data` | **Keep** | Correct one-shot external input command. |
| `ReleaseSuspensionRequest` | `TaskID`, `ExpectedVersion` | **Keep** | Correct TaskStore-level CAS transition; Manager hides the version retry from application callers. |
| `WaitForTaskVersionRequest` | `TaskID`, `AfterVersion` | **Keep** | Precisely names the long-poll predicate; heartbeats and other snapshot mutations can satisfy it, while task events cannot. |
| `ReportTranscriptFailureRequest` | `TaskID`, `ExpectedVersion`, `Error` | **Keep; document** | Precisely identifies failure of the optional derived transcript. Document first-error-wins behavior and that the failure is non-terminal. |
| `ListPendingRequest` | `ExecutorKeys`, `Cursor`, `Limit` | **Keep; document** | Necessary dispatcher query. Cursor validity, ordering, unknown cursor behavior, and limit defaults must be SPI contract text. |
| `ListPendingResult` | `Tasks`, `NextCursor` | **Keep; document** | Good shape; define whether empty `NextCursor` is terminal and whether returned tasks are snapshots. |
| `ListSuspendedRequest` | `ExecutorKeys`, `Cursor`, `Limit` | **Keep; document** | Provides explicit discovery for planned suspensions using the same filtering and cursor model as pending dispatch. |
| `ListSuspendedResult` | `Tasks`, `NextCursor` | **Keep; document** | Returns authoritative suspended snapshots for operational release workflows. |

### Task Progress Events

| Exported API | Shape | Verdict | Audit |
|---|---|---|---|
| `TaskEvent` | Fields: `EventID`, `TaskID`, `Data`, `CreatedAt` | **Keep** | Minimal immutable event. Correctly excludes attempt and public sequence. |
| `AppendTaskEventRequest` | Fields: `TaskID`, `Attempt`, `EventID`, `Data` | **Keep** | Correctly keeps attempt as write authorization rather than persisted event data. |
| `AppendTaskEventResult` | Fields: `Event`, `Inserted` | **Keep** | Required for resolved identity and replay-aware live projection. |
| `ReadRecentTaskEventsRequest` | Fields: `TaskID`, `Limit` | **Keep; document** | Correct bounded, cursor-free surface. Define default and maximum limit in the interface contract. |
| `ReadRecentTaskEventsResult` | Field: `Events` | **Keep** | Minimal result; chronological ordering is documented. |

### Storage SPIs

| Exported API | Methods | Verdict | Audit |
|---|---|---|---|
| `TaskStore` | `Create`, `Get`, `ListPending`, `ListSuspended`, `Start`, `Heartbeat`, `ReportTranscriptFailure`, `Complete`, `Fail`, `WaitInput`, `Suspend`, `Yield`, `AckCancel`, `RequestCancel`, `Resume`, `ReleaseSuspension`, `WaitForTaskVersion` | **Keep; provisional** | Cohesive authoritative snapshot/state-machine boundary. Explicit transitions preserve CAS, fencing, lease, and checkpoint invariants; a provider conformance suite is still required before stabilization. |
| `TaskEventStore` | `AppendTaskEvent`, `ReadRecentTaskEvents` | **Keep; provisional** | Correctly isolates append-only progress from lifecycle snapshots. Implementations must share task identity and attempt authorization with `TaskStore`; fencing must precede replay deduplication. |

Method-level audit:

| Method | Verdict | Audit |
|---|---|---|
| `Create` | **Keep** | Needed for pending durable submission. |
| `Get` | **Keep** | Authoritative snapshot read. |
| `ListPending` | **Keep; document** | Define stable ordering and cursor semantics. |
| `ListSuspended` | **Keep; document** | Required discovery boundary for planned suspensions; shares executor filtering and cursor semantics with `ListPending`. |
| `Start` | **Keep** | Authoritative attempt claim. |
| `Heartbeat` | **Keep** | Required lease renewal. |
| `TaskEventStore.AppendTaskEvent` | **Keep** | Fencing and idempotency contract is well designed. |
| `TaskEventStore.ReadRecentTaskEvents` | **Keep; document** | Define bounds uniformly across providers. |
| `ReportTranscriptFailure` | **Keep** | Preserves optional-transcript failure without corrupting lifecycle status. |
| `Complete`, `Fail`, `WaitInput`, `Yield`, `AckCancel` | **Keep** | Explicit semantic transitions are preferable to a generic update API. `AckCancel` clearly identifies executor acknowledgement after durable cancellation intent. |
| `Suspend`, `ReleaseSuspension` | **Keep** | Planned suspension is now coherent: TaskStore persists the checkpointed pause, Manager exposes discovery, and Manager release returns it to pending. |
| `RequestCancel` | **Keep** | Correctly separates durable intent from active acknowledgement. |
| `Resume` | **Keep** | Atomic one-shot input persistence. |
| `WaitForTaskVersion` | **Keep** | It precisely exposes the long-poll predicate `Task.Version > AfterVersion`, including heartbeat mutations but excluding task events. |

### Execution Boundary

| Exported API | Shape | Verdict | Audit |
|---|---|---|---|
| `ExecutionResult` | Fields: `Directive`, `Status`, `Checkpoint`, `Data`, `Error` | **Keep; document** | Coherent result union, but valid combinations are enforced only by code. Add a table documenting legal directive/status/field combinations. |
| `ProgressEmission` | Fields: `EventID`, `FirstEmission` | **Keep; document** | Minimal executor-facing replay result. It deliberately omits persisted `TaskEvent`, task identity, attempt, and provider insertion vocabulary. |
| `ExecutionRuntime` | Methods: `Controls`, `EmitProgress`, `ReportTranscriptFailure` | **Keep; document** | Clean attempt-scoped semantic boundary. Empty EventID requests framework generation; `FirstEmission` is false for an idempotent replay; task ID, attempt, and expected version remain private runtime state. |
| `Executor` | Methods: `Key`, `LeaseExpiryPolicy`, `ValidateSpec`, `ValidateExecution`, `SupportsDrain`, `Execute` | **Keep** | Minimal generic execution contract; resume-data interpretation is no longer imposed on unrelated executors. |
| `ExecutorRegistry` | Opaque registry | **Keep** | Necessary worker-local implementation registry. |
| `NewExecutorRegistry` | Constructor | **Keep** | Appropriate. |
| `ExecutorRegistry.Register` | `Register(Executor) error` | **Keep** | Correct duplicate rejection. |
| `ExecutorRegistry.LoadOrRegister` | `LoadOrRegister(Executor) (Executor, bool, error)` | **Keep** | Correct atomic idempotent installation boundary for independently constructed integrations. |
| `ExecutorRegistry.Resolve` | `Resolve(string) (Executor, bool)` | **Keep** | Conventional lookup API. |
| `ExecutorRegistry.Keys` | `Keys() []string` | **Keep; fix/document** | Useful worker filter discovery, but current map iteration is nondeterministic. Sort the result or explicitly disclaim ordering. |

### Manager

| Exported API | Verdict | Audit |
|---|---|---|
| `Manager` | **Keep** | Correct non-generic coordinator for heterogeneous task domains. |
| `Manager.AllocateTaskID` | **Keep** | Central identity generation prevents domain adapters from diverging. |
| `Manager.Submit` | **Keep** | Appropriate validated persistence boundary. |
| `Manager.Get` | **Keep** | Appropriate authoritative read. |
| `Manager.ListPending` | **Keep** | Correct read-only dispatch boundary. |
| `Manager.ListSuspended` | **Keep; document** | Provides the missing discovery boundary for checkpointed planned suspensions; ordering, cursor, and limit rules need an interface contract. |
| `Manager.Execute` | **Keep** | Necessary worker entry point. Concrete executors own their dependencies, so every worker uses this same path. |
| `Manager.WaitForTaskVersion` | **Keep** | Exposes the exact long-poll predicate without the ambiguous “update” vocabulary. |
| `Manager.ReadRecentTaskEvents` | **Keep** | Thin delegation is useful to avoid exposing TaskEventStore ownership to callers. |
| `Manager.RequestCancel` | **Keep** | Combines durable intent with best-effort local signaling appropriately. |
| `RequestCancelOption`, `WithCancellationReason` | **Keep** | Optional caller reason is durably first-write and becomes cancellation `ResultError`. |
| `Manager.ReleaseSuspension` | **Keep** | Owns the read/CAS-retry workflow and returns suspended work to pending without exposing TaskStore details. |
| `Manager.Resume` | **Keep; document** | Persists opaque one-shot input through TaskStore. Concrete executors own domain validation and must defensively validate persisted input before use. |
| `Manager.Close` | **Keep** | Strong bounded-shutdown contract and explicit deadline error. |
| `CloseOption`, `WithDrainReason` | **Keep** | Allows an optional advisory operational reason without treating drain as terminal failure. |

### In-Memory Reference Store

| Exported API | Shape | Verdict | Audit |
|---|---|---|---|
| `InMemoryStoreConfig` | Fields: `ActiveAttemptTimeout`, `MaxValueBytes` | **Keep; document** | Document defaults and which values count toward `MaxValueBytes`. |
| `InMemoryStore` | Implements `TaskStore`, `TaskEventStore`, and `NotificationOutbox` | **Keep** | Valuable reference state machine and test double; clearly documented as non-durable. |
| `NewInMemoryStore` | Constructor | **Keep** | Appropriate nil-default constructor. |
| `InMemoryStore.Create`, `Get`, `ListPending`, `ListSuspended`, `Start`, `Heartbeat`, `AppendTaskEvent`, `ReadRecentTaskEvents`, `ReportTranscriptFailure`, `Complete`, `Fail`, `WaitInput`, `Suspend`, `Yield`, `AckCancel`, `RequestCancel`, `Resume`, `ReleaseSuspension`, `WaitForTaskVersion`, `Receive`, `Ack` | Exported concrete methods | **Keep; document** | Required for interface satisfaction and direct reference-store use. Most lack method comments in `go doc`; document non-obvious defaults and avoid making implementation-specific behavior accidental SPI contract. |

### Notifications

| Exported API | Shape | Verdict | Audit |
|---|---|---|---|
| `NotificationKind` | `type NotificationKind string` | **Keep** | Appropriate lifecycle notification discriminator. |
| `NotificationWaitingInput`, `NotificationCompleted`, `NotificationFailed`, `NotificationCanceled` | Typed constants | **Keep** | Complete notification-producing lifecycle set. |
| `Notification` | Fields: `ID`, `TaskID`, `Version`, `Kind`, `CreatedAt`, `Task` | **Keep; document** | Minimal wake-up pointer plus optional enriched snapshot. Session identity remains authoritative only in `Task.Spec.SessionID`; `Task`'s nil/populated phases need stronger method-level guarantees. |
| `ReceiveNotificationsRequest` | Fields: `Limit`, `LeaseDuration` | **Keep; document** | Minimal lease request without dispatcher identity. Define limit defaults and maximums; `LeaseDuration` must be positive. |
| `NotificationReceipt` | Opaque `[]byte` | **Keep** | Correct provider-owned lease token. It authorizes acknowledgement only for the current unexpired lease; document caller copy/retention rules. |
| `NotificationDelivery` | Fields: `Record`, `Receipt` | **Keep** | Minimal leased delivery. |
| `ReceiveNotificationsResult` | Field: `Deliveries` | **Keep** | Minimal result. |
| `NotificationOutbox` | Methods: `Receive`, `Ack` | **Keep; document** | Clean SPI; needs reusable conformance tests for lease exclusion, expiry, redelivery, and stale-receipt rejection. |
| `Dispatcher` | Fields: `Outbox`, `Tasks`, `Inbox`, `Activator`, `BatchSize`, `LeaseDuration` | **Keep; document** | Implements the single supported handoff: load the authoritative task, enqueue its parent session, request activation, then acknowledge the outbox lease. Public mutable fields make post-construction invalid states possible; a validated constructor would be safer. |
| `Dispatcher.DispatchOnce` | One-batch dispatch | **Keep** | Good composable primitive. |

### Session Inbox and Activation

| Exported API | Shape | Verdict | Audit |
|---|---|---|---|
| `EnqueueSessionNotificationRequest` | Fields: `SessionID`, `Notification` | **Keep** | Correct durable enqueue input. |
| `ListSessionNotificationsRequest` | Fields: `SessionID`, `Limit` | **Keep; document** | Define ordering and limit defaults. |
| `AckSessionNotificationRequest` | Fields: `SessionID`, `ItemID`, `ExpectedVersion` | **Keep** | Correct versioned acknowledgement input. |
| `SessionInboxItem` | Fields: `ItemID`, `ItemVersion`, `SessionID`, `Notification`, `CreatedAt` | **Keep** | Coherent durable inbox record. |
| `SessionNotificationInbox` | Methods: `Enqueue`, `ListPending`, `Ack` | **Keep; document** | Good independent SPI; needs deduplication and ordering conformance tests. |
| `SessionActivationDisposition` | String type | **Keep** | Appropriate host scheduling result. |
| `SessionActivationStarted`, `SessionActivationQueued` | Typed constants | **Keep** | Minimal useful outcomes. |
| `SessionActivationRequest` | Field: `SessionID` | **Keep** | Minimal request. |
| `SessionActivationResult` | Field: `Disposition` | **Keep** | Minimal result. |
| `SessionActivator` | Method: `RequestTurn` | **Keep** | Correctly keeps host scheduling outside the task package. |

## 2. Process-Local Tasks: `adk/backgroundtask/local`

Source: [`local.go`](adk/backgroundtask/local/local.go) and
[`stream.go`](adk/backgroundtask/local/stream.go).

| Exported API | Shape | Verdict | Audit |
|---|---|---|---|
| `Config` | Fields: `Manager`, `Executors`, `ForegroundTimeoutMs`, `ShouldAutoBackground`, `BackgroundNotice` | **Keep** | Coherent runner-wide policy. The explicit registry must be the one configured on Manager and prevents registration through Manager. |
| `Input` | Fields: `Description`, `Type`, `Payload`, `OutputFile`, `SessionID`, `NotifySession`, `RunInBackground`, `BackgroundStartupPreviewMs`, `ForegroundTimeoutMs` | **Rework** | Powerful but low level. Rename `Type` to `Kind` to match `Spec.Kind`; document timeout precedence and that startup preview applies only to explicitly backgrounded streams. |
| `NoticeInfo` | Fields: `Task`, `AutoBackgrounded` | **Keep** | Minimal formatter context. |
| `WorkFunc` | `func(context.Context, ExecutionRuntime) (string, error)` | **Keep** | Good buffered closure boundary. |
| `StreamWorkFunc` | `func(context.Context, ExecutionRuntime) (*schema.StreamReader[string], error)` | **Keep** | Good streaming equivalent. |
| `Runner` | Opaque process-local closure registry | **Keep** | Correctly owns non-serializable work outside persistent task providers. |
| `New` | `func(*Config) (*Runner, error)` | **Keep** | Validation can fail because executor registration may conflict. |
| `Runner.Manager` | Returns shared Manager | **Keep** | Necessary for wiring one task-ID space. |
| `Runner.Run` | Buffered managed execution | **Keep** | Clear entry point. |
| `Runner.RunStream` | Streaming managed execution | **Keep; document** | Good API; document caller stream close semantics and that task-event persistence continues after caller detachment. |

## 3. Session Notification Support: `adk/backgroundtask/sessionnotify`

Sources: [`sessionnotify.go`](adk/backgroundtask/sessionnotify/sessionnotify.go)
and [`turnloop.go`](adk/backgroundtask/sessionnotify/turnloop.go).

| Exported API | Shape | Verdict | Audit |
|---|---|---|---|
| `MemoryInbox` | Process-local inbox implementation | **Keep** | Useful reference/test implementation, clearly non-durable by name. |
| `NewMemoryInbox` | Constructor | **Keep** | Appropriate. |
| `MemoryInbox.Enqueue`, `ListPending`, `Ack` | Inbox methods | **Keep; document** | Correct behavior; method docs cover deduplication/order/CAS. Add limit default documentation. |
| `TurnLoopTarget[T,M]` | Fields: `Loop`, `RunContext` | **Keep; document** | Correct deployment-owned target. Explain lifetime/cancellation responsibility prominently. |
| `TurnLoopActivator[T,M]` | Fields: `Resolve`, `WakeItem` | **Keep** | Appropriate generic bridge without coupling core task APIs to TurnLoop input types. |
| `TurnLoopActivator.RequestTurn` | Activation method | **Keep** | Correct `SessionActivator` implementation. |

## 4. Recoverable Shell Adapter: `adk/backgroundtask/shell`

Source: [`shell.go`](adk/backgroundtask/shell/shell.go).

| Exported API | Shape | Verdict | Audit |
|---|---|---|---|
| `RecoverableShell` | Methods: `StartCommand`, `RecoverCommand` | **Keep** | Correctly separate from process-local filesystem shell contracts. |
| `StartCommandRequest` | Fields: `TaskID`, `Command`, `Attempt` | **Keep** | Minimal start envelope. |
| `RecoverCommandRequest` | Fields: `TaskID`, `Command`, `Attempt`, `Checkpoint` | **Keep** | Minimal recovery envelope with executor-owned checkpoint bytes. |
| `RegistrationConfig` | Fields: `Info`, `Shell`, `Materializer` | **Keep** | Small adapter configuration. |
| `NewRegistration` | Returns managed-tool registration | **Keep** | Good package boundary: shell users need not implement generic managed-tool plumbing. |

## 5. Durable Sub-Agent Executor: `adk/backgroundtask/subagent`

Source: [`subagent.go`](adk/backgroundtask/subagent/subagent.go).

| Exported API | Shape | Verdict | Audit |
|---|---|---|---|
| `ExecutorKey` | `"eino.dev/subagent"` | **Keep** | Stable serialized routing key is necessary. |
| `AgentRegistration[M]` | Fields: `Agent`, `RunOptionsFactory` | **Keep** | Correct worker-local dependency binding with strong lifetime documentation. |
| `RunOptionsFactory` | `func() ([]adk.AgentRunOption, error)` | **Keep** | Correctly reconstructs fresh attempt-local options. |
| `ExecutorConfig[M]` | Fields: `SessionStore`, `CheckPointStore`, `SessionConfig` | **Keep** | Correctly makes mandatory durable dependencies explicit at construction. |
| `Executor[M]` | Opaque executor | **Keep** | Appropriate durable adapter. |
| `NewExecutor` | Validated constructor | **Keep** | Prevents middleware and workers from depending on hidden Runner context values. |
| `Executor.SessionEventStore` | Returns configured child-session store | **Keep; narrow integration API** | Used to construct durable progress readers from the same authoritative dependency. |
| `Executor.Register` | Stable name to registration | **Keep** | Necessary for persisted-name reconstruction. |
| `Executor.Key` | Executor key | **Keep** | Interface implementation. |
| `Executor.LeaseExpiryPolicy` | Retry policy | **Keep** | Correct durable recovery policy. |
| `Executor.ValidateSpec` | Submission validation | **Keep** | Ensures payload and registration compatibility. |
| `Executor.ValidateExecution` | Runner-environment validation | **Keep** | Important side-effect-free pre-claim check. |
| `Executor.SupportsDrain` | Drain capability | **Keep; document** | Exported because of interface satisfaction; add a doc comment. |
| `Executor.Execute` | Durable run/resume | **Keep** | Correct executor boundary; checkpoint compatibility and resume-target validation are owned privately and invalid persisted input returns to `waiting_input`. |
| `SubmitRequest` | Fields: `TaskID`, `SubAgentName`, `Query`, `Description`, `SessionID` | **Keep; document** | Good domain-specific request. Document that empty `TaskID` allocates one and `SessionID` identifies the parent session to notify. |
| `Submit` | Domain submission helper | **Keep** | Avoids exposing serialized payload format. |

## 6. Managed Background Tools: `adk/backgroundtask/tool`

Sources:
[`types.go`](adk/backgroundtask/tool/types.go),
[`registry.go`](adk/backgroundtask/tool/registry.go),
[`managed_tool.go`](adk/backgroundtask/tool/managed_tool.go),
[`materializer.go`](adk/backgroundtask/tool/materializer.go),
[`progress.go`](adk/backgroundtask/tool/progress.go), and
[`recovery_conformance.go`](adk/backgroundtask/tool/recovery_conformance.go).

### Tool and Run Contracts

| Exported API | Shape | Verdict | Audit |
|---|---|---|---|
| `ExecutorKey` | Plain managed-tool key | **Keep** | Necessary serialized routing key. |
| `RecoverableExecutorKey` | Recoverable managed-tool key | **Keep** | Separate key preserves old task executability across capability migration. |
| `BackgroundTool` | Methods: `ValidateArguments`, `Start` | **Keep** | Minimal start capability; TaskID-before-side-effect rule is strong. |
| `RecoverableBackgroundTool` | Embeds `BackgroundTool`; adds `Recover` | **Keep** | Good tiered capability model with no checkpoint validator leakage. |
| `StartRequest` | Fields: `TaskID`, `Arguments`, `Attempt` | **Keep** | Minimal initial-attempt envelope. |
| `RecoverRequest` | Fields: `TaskID`, `Arguments`, `Attempt`, `Checkpoint` | **Keep** | Correctly delegates opaque checkpoint interpretation to the tool. |
| `Run` | Methods: `Wait`, `Stop` | **Keep; document** | Good attempt-local observation handle. Specify whether `Stop` must be idempotent and how repeated/concurrent calls behave. |
| `Checkpointer` | Method: `Checkpoint` | **Keep; document** | Good optional capability. Link `ErrDrainCheckpointUnavailable` and size/lifetime expectations. |
| `UpdateSource` | Method: `Updates` | **Keep; document** | Good optional capability. Document whether each call may be made once, stream closure ownership, and replay starting point. |
| `Outcome` | Fields: `Status`, `Data`, `Error` | **Keep; document** | Necessary logical terminal result; document valid status/field combinations. |
| `Update` | Fields: `EventID`, `Kind`, `Data`, `Metadata` with JSON tags | **Keep; document** | Strong stable-ID contract. Document size bounds, metadata key rules, and that framework-generated IDs are not written back into the original update object. |

### Registry and Wrapper

| Exported API | Shape | Verdict | Audit |
|---|---|---|---|
| `Registration` | Fields: `Info`, `Tool`, `Description`, `LaunchOutput`, `Materializer` | **Keep; document** | Cohesive registration. Callback nil/default behavior and concurrency requirements need field comments. |
| `Registry` | Separate plain/recoverable maps | **Keep** | Capability-class separation is an excellent compatibility property. |
| `NewRegistry` | Constructor | **Keep** | Appropriate. |
| `Registry.Register` | Registration method | **Keep** | Correct class-specific duplicate behavior. |
| `RegisterExecutors` | Executor-registry/tool-registry installation | **Keep** | Useful one-call adapter setup without leaking registry operations through Manager. |
| `ManagedToolConfig` | Fields: `Manager`, `Executors`, `Registry`, `ToolName`, `ForegroundTimeoutMs`, `ShouldAutoBackground`, `RunInBackground`, `InvocationTimeoutMs`, `SessionID` | **Keep; document** | Necessary wrapper policy. `Executors` must be the registry configured on Manager; document callback precedence, concurrency, nil semantics, and timeout units/defaults. |
| `NewManagedTool` | Model-facing wrapper constructor | **Keep** | Correctly owns task creation and foreground/background projection. |

### Progress and Materialization

| Exported API | Shape | Verdict | Audit |
|---|---|---|---|
| `ReserveOutputRequest` | Field: `TaskID` | **Keep** | Minimal reservation key. |
| `MaterializeOutputRequest` | Fields: `TaskID`, `EventID`, `Path`, `Data` | **Keep** | Correct stable idempotency key and explicit destination. |
| `OutputMaterializer` | Methods: `ReserveOutput`, `AppendOutput` | **Keep** | Strong ordering and durable deduplication contract. |
| `ProgressReader` | Fields: `Manager`, `Limit` | **Keep; document** | Useful middleware adapter. Public mutable fields allow invalid nil state but reads fail closed; constructor is optional, not necessary. Document default limit. |
| `ProgressReader.ReadProgress` | Bounded textual projection | **Keep** | Correctly keeps model presentation outside TaskEventStore. |

### Model-Facing Stream

| Exported API | Shape | Verdict | Audit |
|---|---|---|---|
| `ToolStreamEvent` | Fields: `Type`, `TaskID`, `Status`, `Description`, `Output`, `Error`, `Update` | **Rework** | Public NDJSON envelope is appropriate, but `Type` is a free string and no event-type constants are exported. Add `ToolStreamEventType` plus constants and document legal field combinations per type. |

### Recovery Conformance API

| Exported API | Shape | Verdict | Audit |
|---|---|---|---|
| `RecoverySnapshot` | Fields: `LogicalOperationID`, `Updates` | **Keep; document** | Useful backend-test view; explicitly test-only semantics should be in the package location or name. |
| `RecoveryConformanceConfig` | Fields: `TaskID`, `Arguments`, `NewTool`, `Snapshot` | **Keep** | Complete reusable fixture. |
| `CheckRecoveryConformance` | Runs duplicate-start/recover checks | **Keep; relocate later** | Valuable quality gate. Consider a `tooltest` package so production API users do not confuse it with runtime behavior. |

## 7. Polling Worker: `adk/backgroundtask/worker`

Source: [`worker.go`](adk/backgroundtask/worker/worker.go).

| Exported API | Shape | Verdict | Audit |
|---|---|---|---|
| `WorkerConfig` | Fields: `Manager`, `ExecutorKeys`, `PollInterval`, `InitialPickupDelay`, `MaxConcurrent` | **Keep** | Minimal polling policy. Executor-specific dependencies are captured by registered executor instances. |
| `Worker` | Polling dispatcher | **Keep** | Correctly remains generic by calling the common `Manager.Execute` path. |
| `NewWorker` | Validated constructor | **Keep** | Correct constructor pattern. |
| `Worker.Run` | Poll and dispatch loop | **Keep** | Concurrency and shutdown semantics are good; TaskStore authorization remains authoritative. |

## 8. Control Middleware: `adk/middlewares/backgroundtask`

Sources:
[`middleware.go`](adk/middlewares/backgroundtask/middleware.go) and
[`prompt.go`](adk/middlewares/backgroundtask/prompt.go).

| Exported API | Shape | Verdict | Audit |
|---|---|---|---|
| `TaskProgressReader` | Method: `ReadProgress` | **Keep** | Good executor-specific projection capability. |
| `ToolConfig` | Fields: `Name`, `Desc`, `Disable` | **Keep** | Small conventional control-tool customization. |
| `TypedConfig[M]` | Fields: `Manager`, `ReadTaskProgress`, `ProgressReaders`, `TaskOutputToolConfig`, `TaskStopToolConfig` | **Rework mildly** | Overall coherent. `ReadTaskProgress` plus `ProgressReaders` creates two selection mechanisms; deprecate the fallback once all domains register by executor key. |
| `Config` | Alias of `TypedConfig[*schema.Message]` | **Keep** | Standard typed/default specialization pattern. |
| `NewTyped` | Typed middleware constructor | **Keep** | Appropriate. |
| `New` | Standard-message constructor | **Keep** | Appropriate convenience specialization. |

## 9. ADK Runner Integration: `adk`

Source: [`runner.go`](adk/runner.go). Only background-task-specific exports are
listed; the rest of Runner is outside this audit.

| Exported API | Shape | Verdict | Audit |
|---|---|---|---|
| `RunnerSessionID` | `func(context.Context) (string, bool)` | **Keep** | Narrow request-scoped identity lookup associating model-facing background work with its parent session. It does not expose stores or configuration through context. |

## 10. Filesystem Middleware Integration

Sources:
[`filesystem.go`](adk/middlewares/filesystem/filesystem.go) and
[`bash_run.go`](adk/middlewares/filesystem/bash_run.go).

Only background-related exports and constructors that expose them are listed.

| Exported API | Shape | Verdict | Audit |
|---|---|---|---|
| `ExecuteTaskType` | `"bash"` | **Keep** | Useful host policy discriminator for process-local shell tasks. |
| `CommandFromTask` | Extracts persisted command | **Keep** | Hides payload encoding from host policy. |
| `RecoverableShell` | Alias of `backgroundtask/shell.RecoverableShell` | **Compatibility/convenience** | Convenient discovery at the integration package, but duplicates the canonical API. Keep only if filesystem is the intended user entry point. |
| `StartCommandRequest` | Alias | **Compatibility/convenience** | Same trade-off as `RecoverableShell`. |
| `RecoverCommandRequest` | Alias | **Compatibility/convenience** | Same trade-off as `RecoverableShell`. |
| `BackgroundConfig` | Fields: `Runner`, `Manager`, `Executors`, `ToolRegistry`, `OutputMaterializer`, `ForegroundTimeoutMs`, `ShouldAutoBackground`, `OutputStore`, `OutputDir` | **Rework** | Conflates process-local and recoverable modes and permits two Manager authorities. `Executors` at least makes installation ownership explicit. Split into explicit local/recoverable variants. |
| `ExecuteToolConfig` | Embeds `ToolConfig` | **Keep** | Appropriate extension point even though managed input shape is selected by background configuration. |
| `Config.RecoverableShell`, `Config.Background`, `Config.ExecuteToolConfig` | Legacy middleware fields | **Compatibility** | Required while `NewMiddleware` remains, but `Config` should itself be marked deprecated. |
| `MiddlewareConfig.RecoverableShell`, `MiddlewareConfig.Background`, `MiddlewareConfig.ExecuteToolConfig` | Current middleware fields | **Keep after `BackgroundConfig` rework** | Correct placement in current constructor config. |
| `NewMiddleware` | Legacy constructor | **Compatibility** | Already deprecated; keep only for source compatibility. |
| `NewTyped` | Current typed constructor | **Keep** | Appropriate. |
| `New` | Current standard-message constructor | **Keep** | Appropriate. |

## 11. Sub-Agent Middleware Integration

Sources:
[`middleware.go`](adk/middlewares/subagent/middleware.go),
[`agent_tool.go`](adk/middlewares/subagent/agent_tool.go), and
[`task_progress.go`](adk/middlewares/subagent/task_progress.go).

| Exported API | Shape | Verdict | Audit |
|---|---|---|---|
| `TaskTypeSubagent` | `"subagent"` | **Keep** | Stable host policy discriminator. |
| `NameFromTask` | Extracts persisted routing name | **Keep** | Hides payload encoding. |
| `TranscriptFormat[M]` | `func(context.Context, string, M) (string, error)` | **Keep; document** | Good customization point. Name the string parameter (`agentName`) in docs/signature and state concurrency requirements. |
| `TypedLocalBackgroundConfig[M]` | Fields: `Runner`, `OutputStore`, `OutputDir` | **Keep** | Clear local mode. Generic parameter is phantom but preserves nesting consistency. |
| `LocalBackgroundConfig` | Standard-message alias | **Keep** | Consistent specialization. |
| `TypedDurableBackgroundConfig[M]` | Fields: `Manager`, `Executors`, `Executor`, `ForegroundTimeoutMs`, `ShouldAutoBackground`, `RunOptionsFactories` | **Keep** | Clear durable mode; the dependency-bearing executor and its installation registry are explicit. |
| `DurableBackgroundConfig` | Standard-message alias | **Keep** | Consistent specialization. |
| `TypedBackgroundConfig[M]` | Fields: `Local`, `Durable`, `TranscriptFormat` | **Keep** | Explicit exactly-one mode is better than filesystem's dual authority. A sum type is impossible in idiomatic Go; constructor validation is acceptable. |
| `BackgroundConfig` | Standard-message alias | **Keep** | Consistent specialization. |
| `TypedConfig[M]` | Fields: `SubAgents`, `ToolName`, `ToolDescriptionGenerator`, `SystemPrompt`, `Background` | **Keep** | Coherent middleware configuration; background is cleanly optional. |
| `Config` | Standard-message alias | **Keep** | Consistent specialization. |
| `NewDurableTaskProgressHook` | Accepts `SessionEventStore` and formatter; returns progress callback | **Rework mildly** | SessionEventStore injection is explicit. Returning a bare function still prevents capability discovery/config evolution; a `TaskProgressReader` implementation would align with control middleware. |
| `NewTyped` | Typed middleware constructor | **Keep** | Appropriate. |
| `New` | Standard-message constructor | **Keep** | Appropriate. |

## 12. DeepAgent Integration

Source: [`deep.go`](adk/prebuilt/deep/deep.go). Only background-related exports
and constructors that expose them are listed.

| Exported API | Shape | Verdict | Audit |
|---|---|---|---|
| `TypedDurableSubAgentConfig[M]` | Fields: `Executor`, `RunOptionsFactories` | **Keep** | Durability is scoped precisely to reconstructable sub-agent execution. |
| `DurableSubAgentConfig` | Standard-message alias | **Keep** | Consistent specialization. |
| `RecoverableShellConfig` | Fields: `Shell`, `OutputDir`, `OutputMaterializer` | **Keep** | Groups only cross-worker shell recovery dependencies and output projection. |
| `LocalShellConfig` | Fields: `Shell`, `StreamingShell`, `OutputDir` | **Keep** | Owns the process-local managed shell backend explicitly without claiming durability. |
| `TypedBackgroundConfig[M]` | Fields: `Manager`, `Executors`, `SubAgents`, `RecoverableShell`, `LocalShell`, `ForegroundTimeoutMs`, `ShouldAutoBackground`, `TranscriptFormat` | **Keep** | One Manager and one explicit executor-registry authority with independently enabled capability blocks; durability is no longer an ambiguous umbrella mode. |
| `BackgroundConfig` | Standard-message alias | **Keep** | Consistent specialization. |
| `TypedConfig.Background` | Optional background capability | **Keep** | Correct top-level ownership; intentionally not propagated to child agents. |
| `NewTyped`, `New` | DeepAgent constructors exposing background config | **Keep** | Appropriate integration entry points. |

The DeepAgent configuration duplicates several fields from filesystem and
sub-agent middleware. This is acceptable for a prebuilt facade, provided it
remains a strict forwarding layer and does not develop independent semantics.

## 13. Model-Facing API

These are not exported Go declarations in every case, but they are externally
observable contracts of the capability.

| API | Shape | Verdict | Audit |
|---|---|---|---|
| `task_output` | Input: `task_id`, optional `block`, optional `timeout` | **Keep; document** | Good bounded control tool. Make lifecycle-only blocking explicit: progress events do not wake it. |
| `task_stop` | Input: `task_id`, optional `reason` | **Keep** | The reason is durably first-write and becomes cancellation `ResultError`. |
| Filesystem `execute.run_in_background` | Boolean | **Keep** | Intuitive explicit detachment control. |
| Filesystem `execute.timeout` | Milliseconds | **Keep; document** | Timeout is ignored for explicit background execution and policy decides stop versus auto-background; schema text covers most of this. |
| Sub-agent `agent.run_in_background` | Boolean | **Keep** | Consistent with filesystem execution. |
| Managed tool NDJSON | `ToolStreamEvent` | **Rework** | Needs typed event kinds and legal-variant documentation. |
| Live `Update.event_id` | Optional opaque string | **Keep; document** | Necessary to tool authors and replay, but model consumers should be told not to infer order or reuse it as a cursor. |

## Cross-Package Naming Audit

| Name family | Verdict | Audit |
|---|---|---|
| `Manager`, `TaskStore`, `TaskEventStore`, `Executor`, `Worker`, `Dispatcher` | **Keep** | Lifecycle authority and append-only progress now have distinct names and contracts. |
| `TaskEvent` | **Keep** | Correctly signals task progress without implying lifecycle authority. |
| `OutputFile`, `OutputFileErr`, `OutputMaterializer` | **Keep; clarify** | These mean derived transcript/file output, while `TaskEvent` means progress. `ReportTranscriptFailure` now names the failure semantics directly; package docs should state the remaining distinction once. |
| `Typed*` plus standard aliases | **Keep** | Consistent with the rest of ADK. |
| `Type` in `local.Input` | **Rename to `Kind`** | Inconsistent with the `Spec.Kind` it populates. |
| `RecoverableShell` aliases in filesystem | **Convenience only** | Canonical ownership is `adk/backgroundtask/shell`; duplication should be intentional. |

## Public Documentation Audit

### Strong

- `TaskStore` documents cancellation, fencing, and yield semantics.
- `TaskEventStore` documents attempt fencing and EventID replay semantics.
- `OutputMaterializer` documents durable idempotency and stable replay order.
- `RecoverableBackgroundTool` and `AgentRegistration` document cross-worker
  lifetime requirements.
- DeepAgent and sub-agent middleware document top-level ownership.

### Missing or Weak

- Concrete `InMemoryStore` methods mostly have no exported method comments.
- `Executor` methods have no per-method semantic contract.
- `ExecutionRuntime.EmitProgress` needs fuller empty-ID and replay documentation.
- Pagination/default-limit behavior is absent from SPI docs.
- `ToolStreamEvent` has no variant schema.
- Several callback fields omit concurrency and nil/default semantics.
- `SupportsDrain` lacks comments on concrete executors.

## Summary Scorecard

| Dimension | Rating | Notes |
|---|---:|---|
| Concept coherence | 4/5 | Snapshot lifecycle, progress events, and child session events remain correctly separate. |
| API usability | 4/5 | Executor dependencies are explicit; filesystem Manager selection remains a trap. |
| Minimum API surface | 4/5 | Compatibility aliases are gone and validation-only notification contracts were removed; duplicated filesystem configs still add avoidable surface. |
| Backward compatibility | 4/5 | Alpha source APIs intentionally changed without aliases; persisted task data and executor keys remain compatible. |
| Module separation | 4/5 | Lifecycle and progress persistence are separated; notification delivery ownership remains host-level without leaking proof objects through integrations. |
| Cohesion | 4/5 | Dedicated packages are cohesive; filesystem `BackgroundConfig` mixes capability modes. |
| Elegance | 3/5 | Core state transitions are explicit and strong, but provisional SPI and config plumbing are large. |
| Naming | 4/5 | DeepAgent now names durability per capability; local `Type` remains ambiguous. |
| Readability | 4/5 | Public concepts are understandable; valid union combinations need tables. |
| Duplication | 3/5 | Typed aliases are justified; filesystem legacy/current configs and facade forwarding duplicate more than ideal. |
| Public API documentation | 3/5 | Critical distributed-system contracts are good, operational defaults and callbacks are incomplete. |
| Internal comments | 4/5 | Complex recovery and projection paths are generally well explained. |

## Recommended API Changes

### 1. Split Filesystem Background Modes

```go
type BackgroundConfig struct {
    Manager       *backgroundtask.Manager
    Local         *LocalBackgroundConfig
    Recoverable   *RecoverableBackgroundConfig
}
```

`LocalBackgroundConfig` should own its process-local runner policy without
introducing another Manager, while `RecoverableBackgroundConfig` should own the
recoverable shell, registry, and materializer. This removes dual Manager
authority and follows the capability-based DeepAgent configuration.

### 2. Stabilize or Isolate Provider SPIs

Publish reusable conformance suites for:

- TaskStore lifecycle/CAS/fencing/lease behavior
- TaskEventStore fencing, ordering, replay, and conflict behavior
- pending- and suspended-list cursor behavior
- notification lease exclusion/expiry/redelivery/acknowledgement
- session inbox deduplication/order/CAS

Until then, keep provider SPIs explicitly experimental.

### 3. Type the Managed Tool Stream

```go
type ToolStreamEventType string

const (
    ToolStreamEventUpdate       ToolStreamEventType = "update"
    ToolStreamEventLaunchResult ToolStreamEventType = "launch_result"
)
```

Document the legal fields for each event type and retain the current wire
strings.
