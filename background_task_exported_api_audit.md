# Background Task Exported API Audit

## Basis

Fresh audit of the current `feat/durabletask` worktree after resolving the
stabilization findings. Reviewed:

- `adk/backgroundtask` and all subpackages
- background-task, filesystem, and sub-agent middleware integrations
- `adk/prebuilt/deep`, `adk.RunnerSessionID`, and model-facing contracts
- declarations, implementations, call sites, tests, and `go doc`

Verdicts:

| Verdict | Meaning |
|---|---|
| **Keep** | Necessary, coherent, and adequately documented for alpha |
| **Provisional** | Shape is reasonable, but an identified stabilization requirement remains |
| **Rework** | Public shape or ownership should change before stabilization |
| **Compatibility** | Retained only for an established non-background API |
| **Remove** | No sufficient reason to remain public |

## Findings

No unresolved API rework or documentation findings remain in this audit.
External providers must run the exported `backgroundtask/storetest`
conformance suites; passing those suites is the executable stabilization gate,
not an assumption made from interface shape alone.

## Resolved

- Removed the speculative polling `worker` package.
- Split filesystem background configuration into exactly one local or
  recoverable mode.
- Removed recoverable-shell aliases from filesystem middleware.
- Removed the generic progress-reader fallback; selection is executor-keyed.
- Replaced the sub-agent progress callback with a concrete reader.
- Standardized public task category vocabulary on `Kind`.
- Renamed the managed-tool NDJSON presentation envelope to
  `ManagedToolResponseEvent` and typed and validated its response variants.
- Validated legal `ExecutionResult` and managed-tool `Outcome` combinations.
- Sorted executor-registry keys and constrained generated ID kinds.
- Scoped inbox deduplication by session and notification ID.
- Completed ownership, pagination, callback, shutdown, drain, resume,
  transcript, notification, and stream-lifetime documentation.
- Added reusable conformance suites for lifecycle stores, event stores,
  notification outboxes, and session inboxes.
- Made Manager construction reject missing event-store capability.
- Moved creation time from caller `Spec` to provider-owned `Task.CreatedAt`.
- Reduced session activation to success or failure.
- Made Dispatcher and managed-tool progress-reader dependencies immutable after
  validated construction.
- Removed alpha background configuration from the deprecated filesystem
  constructor.

## 1. Core Package

### Lifecycle, identity, and errors

| Exported API | Verdict | Audit |
|---|---|---|
| `Status`; all seven `Status*` constants | **Keep** | Complete durable lifecycle. Suspended work explicitly requires release. |
| `LeaseExpiryPolicy`; `LeaseExpiryRetry`, `LeaseExpiryFail` | **Keep** | Correct immutable recovery policy. |
| `ControlKind`; `ControlStop`, `ControlDrain`, `ControlTimeout`; `ControlRequest{Kind, Reason}` | **Keep** | Separates stop, graceful relinquishment, and timeout; reason semantics are explicit. |
| `ExecutionDirective`; `ExecutionDirectiveYield` | **Keep** | Represents continuing external work without pretending it is suspended. |
| `AllocateTaskIDRequest{Kind}`; `IDGenerator`; `Manager.AllocateTaskID` | **Keep** | Full-ID customization is centralized; default kind is a bounded safe segment. |
| `ErrNotFound`, `ErrAlreadyExists`, `ErrVersionConflict`, `ErrLeaseLost`, `ErrIllegalTransition`, `ErrInvalidExecutionResult`, `ErrAlreadyTerminal` | **Keep** | Required stable error classification. |
| `ErrDrainCheckpointUnavailable`, `ErrCloseDeadlineRequired`, `ErrUnsupportedExecutorPayloadVersion`, `ErrTaskEventIDConflict`, `ErrInvalidCursor` | **Keep** | Each identifies a distinct recovery or contract failure. |

### Task intent and snapshot

| Exported API | Verdict | Audit |
|---|---|---|
| `Spec{ID, ExecutorKey, Kind, Payload, Description, OutputFile, SessionID, NotifySession}` | **Keep** | Pure caller intent; `OutputFile` is correctly documented as derived. |
| `Task{Spec, LeaseExpiryPolicy, Status, Checkpoint, ResultData, ResultError, OutputFileErr, PendingResume, Version, Attempt, CancelRequestedAt, CancelReason, CreatedAt, UpdatedAt, DoneAt}` | **Keep** | Necessary authoritative snapshot. Timestamps are provider-owned and mutable ownership is documented. |
| `Config{Tasks, TaskEvents, Executors, IDGen}` | **Keep** | Explicit lifecycle/event/implementation authorities. Misconfiguration handling belongs to `New`. |
| `New(context.Context, *Config) (*Manager, error)` | **Keep** | Rejects a supplied lifecycle store without a matching event-store capability at construction. |
| `Manager` | **Keep** | Correct heterogeneous lifecycle coordinator. |

### Store request and result types

All exported fields are included below.

| Exported API | Verdict | Audit |
|---|---|---|
| `CreateTaskRequest{Spec, LeaseExpiryPolicy}` | **Keep** | Persists intent and immutable recovery policy. |
| `StartTaskRequest{TaskID, ExpectedVersion}`, `HeartbeatRequest{TaskID, ExpectedVersion}` | **Keep** | Minimal claim and renewal CAS inputs. |
| `CompleteTaskRequest{TaskID, ExpectedVersion, Data}`, `FailTaskRequest{TaskID, ExpectedVersion, Error}` | **Keep** | Explicit terminal transitions. |
| `WaitInputTaskRequest`, `SuspendTaskRequest`, `YieldTaskRequest` with `TaskID`, `ExpectedVersion`, `Checkpoint` | **Keep** | Distinct semantics; yield's empty-checkpoint retention rule is explicit. |
| `AckCancelRequest{TaskID, ExpectedVersion, Reason}`, `RequestCancelRequest{TaskID, ExpectedVersion, Reason}` | **Keep** | Correctly separates durable intent from active-attempt acknowledgement. |
| `ResumeRequest{TaskID, ExpectedVersion, Data}` | **Keep** | Opaque one-shot input; executor owns schema validation. |
| `ReleaseSuspensionRequest{TaskID, ExpectedVersion}` | **Keep** | Minimal release CAS. |
| `WaitForTaskVersionRequest{TaskID, AfterVersion}` | **Keep** | Exact lifecycle wait predicate; events do not wake it. |
| `ReportTranscriptFailureRequest{TaskID, ExpectedVersion, Error}` | **Keep** | First-error, non-terminal derived-output failure. |
| `ListPendingRequest`, `ListSuspendedRequest` with `ExecutorKeys`, `Cursor`, `Limit` | **Keep** | Shape and defaults are clear and covered by provider conformance. |
| `ListPendingResult`, `ListSuspendedResult` with `Tasks`, `NextCursor` | **Keep** | Snapshot ownership and exhaustion are clear. |

### Progress events

| Exported API | Verdict | Audit |
|---|---|---|
| `TaskEvent{EventID, TaskID, Data, CreatedAt}` | **Keep** | Minimal immutable event; no public sequence or attempt. |
| `AppendTaskEventRequest{TaskID, Attempt, EventID, Data}` | **Keep** | Attempt is write authorization, not event data. |
| `AppendTaskEventResult{Event, Inserted}` | **Keep** | Distinguishes insertion from byte-identical replay. |
| `ListTaskEventsRequest{TaskID, Cursor, Limit, NewestFirst}` | **Keep** | Supports complete replay and efficient recent reads with opaque cursors. |
| `ListTaskEventsResult{Events, NextCursor}` | **Keep** | Minimal snapshot-page result. |

### Storage interfaces

| Exported API | Verdict | Audit |
|---|---|---|
| `TaskStore` and all 18 methods | **Keep** | Cohesive explicit state machine with reusable lifecycle, lease, CAS, list, and cancellation conformance. |
| `TaskEventStore.AppendTaskEvent`, `ListTaskEvents` | **Keep** | Fencing, replay, retention, order, cursor, and snapshot contracts have reusable conformance. |
| `NotificationOutbox.Receive`, `Ack` | **Keep** | Opaque receipt lease with reusable exclusion, expiry, redelivery, and acknowledgement conformance. |

### Execution boundary

| Exported API | Verdict | Audit |
|---|---|---|
| `ExecutionResult{Directive, Status, Checkpoint, Data, Error}` | **Keep** | Legal variants are documented and rejected when mixed. |
| `ProgressEmission{EventID, FirstEmission}` | **Keep** | Semantic replay result without leaking store envelopes. |
| `ExecutionRuntime.Controls`, `EmitProgress`, `ReportTranscriptFailure` | **Keep** | Clean attempt-scoped semantic API; storage fencing stays private. |
| `Executor` and its six methods | **Keep** | Minimal generic reconstruction boundary; domain resume logic stays concrete. |
| `ExecutorRegistry`; constructor; `Register`, `LoadOrRegister`, `Resolve`, `Keys` | **Keep** | Atomic installation and deterministic key listing are appropriate. |

### Manager methods and options

| Exported API | Verdict | Audit |
|---|---|---|
| `Submit`, `Get`, `ListPending`, `ListSuspended`, `ListTaskEvents`, `WaitForTaskVersion` | **Keep** | Appropriate validated persistence and read boundaries. |
| `Execute` | **Keep** | Single generic claim/run entry point; no polling policy is embedded. |
| `RequestCancel`; `RequestCancelOption`; `WithCancellationReason` | **Keep** | Durable first-write reason plus best-effort local signaling. |
| `Resume`, `ReleaseSuspension` | **Keep** | Manager owns generic persistence/CAS while executor owns domain validation. |
| `Close`; `CloseOption`; `WithDrainReason` | **Keep** | Bounded graceful shutdown and advisory drain reason are explicit. |

### Reference store

| Exported API | Verdict | Audit |
|---|---|---|
| `InMemoryStoreConfig{ActiveAttemptTimeout, MaxValueBytes}` | **Keep** | Defaults and bounded values are documented. |
| `InMemoryStore`; `NewInMemoryStore` | **Keep** | Useful non-durable reference provider. |
| All concrete `TaskStore`, `TaskEventStore`, and `NotificationOutbox` methods | **Keep** | Fully documented interface implementations and useful direct test surface. |

## 2. Notifications

| Exported API | Verdict | Audit |
|---|---|---|
| `NotificationKind`; four `Notification*` constants | **Keep** | Exactly the notification-producing lifecycle transitions. |
| `Notification{ID, TaskID, Version, Kind, CreatedAt, Task}` | **Keep** | Pointer-only outbox and enriched inbox phases are documented. |
| `NotificationReceipt`; `ReceiveNotificationsRequest{Limit, LeaseDuration}`; `NotificationDelivery{Record, Receipt}`; `ReceiveNotificationsResult{Deliveries}` | **Keep** | Correct opaque leased-delivery model with reusable provider conformance. |
| `EnqueueSessionNotificationRequest{SessionID, Notification}` | **Keep** | Single supported notification destination. |
| `SessionInboxItem{ItemID, ItemVersion, SessionID, Notification, CreatedAt}` | **Keep** | Coherent durable inbox record. |
| `ListSessionNotificationsRequest{SessionID, Limit}`, `AckSessionNotificationRequest{SessionID, ItemID, ExpectedVersion}` | **Keep** | Ordering, limits, and CAS are explicit. |
| `SessionNotificationInbox` | **Keep** | Deduplication, order, limits, and CAS have reusable conformance. |
| `SessionActivationRequest{SessionID}`, `SessionActivator.RequestTurn(...) error` | **Keep** | Host scheduling remains separate from persistence and exposes only consumed semantics. |
| `DispatcherConfig{Outbox, Tasks, Inbox, Activator, BatchSize, LeaseDuration}`; `NewDispatcher`; `Dispatcher.DispatchOnce` | **Keep** | Validated construction freezes dependencies and policy; dispatch retains the correct at-least-once sequence. |

## 3. `backgroundtask/local`

| Exported API | Verdict | Audit |
|---|---|---|
| `WorkFunc`, `StreamWorkFunc` | **Keep** | Appropriate process-local closure boundaries. |
| `Input{Description, Kind, Payload, OutputFile, SessionID, NotifySession, RunInBackground, BackgroundStartupPreviewMs, ForegroundTimeoutMs}` | **Keep** | Naming and timeout/preview precedence now match `Spec`. |
| `NoticeInfo{Task, AutoBackgrounded}` | **Keep** | Minimal formatter context. |
| `Config{Manager, Executors, ForegroundTimeoutMs, ShouldAutoBackground, BackgroundNotice}` | **Keep** | Explicit authorities and policy semantics. |
| `Runner`; `New`; `Manager`; `Run`; `RunStream` | **Keep** | Correctly owns non-serializable work; stream-close cancellation is explicit. |

## 4. `backgroundtask/sessionnotify`

| Exported API | Verdict | Audit |
|---|---|---|
| `MemoryInbox`; constructor; `Enqueue`, `ListPending`, `Ack` | **Keep** | Session-scoped deduplication, ordering, limits, tombstones, and CAS are documented. |
| `TurnLoopTarget{Loop, RunContext}` | **Keep** | Deployment lifetime and borrowed-target rules are explicit. |
| `TurnLoopActivator{Resolve, WakeItem}`; `RequestTurn` | **Keep** | Correct bridge returning only activation success or failure. |

## 5. Recoverable Shell

| Exported API | Verdict | Audit |
|---|---|---|
| `RecoverableShell` | **Keep** | Correctly distinct from process-local filesystem shells. |
| `StartCommandRequest{TaskID, Command, Attempt}`, `RecoverCommandRequest{TaskID, Command, Attempt}` | **Keep** | Minimal identity/attempt envelopes without checkpoints. |
| `RegistrationConfig{Info, Shell, Materializer}`; `NewRegistration` | **Keep** | Small adapter boundary that hides generic managed-tool plumbing. |

## 6. Durable Sub-Agent

| Exported API | Verdict | Audit |
|---|---|---|
| `ExecutorKey` | **Keep** | Stable persisted routing key. |
| `RunOptionsFactory`; `AgentRegistration{Agent, RunOptionsFactory}` | **Keep** | Worker-equivalence and concurrency requirements are explicit. |
| `ExecutorConfig{SessionStore, CheckPointStore, SessionConfig}`; `Executor`; `NewExecutor` | **Keep** | Durable dependencies are constructor-injected, not context-injected. |
| `Executor.SessionEventStore` | **Keep narrowly** | Needed to construct the matching progress reader from the same authority. |
| `Register`, `Key`, `LeaseExpiryPolicy`, `ValidateSpec`, `ValidateExecution`, `SupportsDrain`, `Execute` | **Keep** | Interface and registration surface is coherent; checkpoint logic stays sub-agent-specific. |
| `SubmitRequest{TaskID, SubAgentName, Query, Description, SessionID}`; `Submit` | **Keep** | Hides payload encoding and clearly identifies parent session. |

## 7. Managed Background Tools

| Exported API | Verdict | Audit |
|---|---|---|
| `ExecutorKey`, `RecoverableExecutorKey` | **Keep** | Separate persisted capability classes preserve old-task executability. |
| `BackgroundTool`; `RecoverableBackgroundTool` | **Keep** | Clean tiered capability model with task-ID-based recovery. |
| `StartRequest`, `RecoverRequest` with `TaskID`, `Arguments`, `Attempt` | **Keep** | Minimal reconstruction envelopes; attempt is external-operation fencing context. |
| `Run.Wait`, `Run.Stop`; `UpdateSource.Updates` | **Keep** | Stop idempotency, observation cancellation, replay, and reader ownership are documented. |
| `Outcome{Status, Data, Error}` | **Keep** | Legal terminal variants are documented and validated. |
| `Update{EventID, Kind, Data, Metadata}` | **Keep** | Bounds, replay identity, ordering, and generated-ID behavior are explicit. |
| `Registration{Info, Tool, Description, LaunchOutput, Materializer}` | **Keep** | Nil/default and callback concurrency contracts are explicit. |
| `Registry`; constructor; `Register`; `RegisterExecutors` | **Keep** | Capability-class separation and explicit executor installation are sound. |
| `ManagedToolConfig` and all nine fields; `NewManagedTool` | **Keep** | Dependencies, callback precedence, nil behavior, and timeouts are documented. |
| `OutputMaterializer`; `ReserveOutputRequest{TaskID}`; `MaterializeOutputRequest{TaskID, EventID, Path, Data}` | **Keep** | Strong idempotent derived-output contract. |
| `ProgressReader`; `NewProgressReader`; `ReadProgress` | **Keep** | Validated immutable Manager dependency and bounded newest-first retrieval with chronological rendering. |
| `ManagedToolResponseEventType`; `ManagedToolResponseEventUpdate`, `ManagedToolResponseEventLaunchResult` | **Keep** | Stable typed wire discriminators whose names identify the model-facing response boundary. |
| `ManagedToolResponseEvent{Type, TaskID, Status, Description, Output, Error, Update}` | **Keep** | Presentation-only NDJSON decode envelope combining progress and launch-result responses. It is not task state, persistence, recovery, or an executor API; framework encoding validates legal variants. |

### `tool/tooltest`

| Exported API | Verdict | Audit |
|---|---|---|
| `RecoverySnapshot{LogicalOperationID, Updates}` | **Keep** | Test-only role is clear from package placement. |
| `RecoveryConformanceConfig{TaskID, Arguments, NewTool, Snapshot}`; `CheckRecoveryConformance` | **Keep** | Valuable reusable backend recovery gate, outside production execution APIs. |

### `backgroundtask/storetest`

| Exported API | Verdict | Audit |
|---|---|---|
| `TaskStoreConfig{New, ExpireActiveAttempt}`; `RunTaskStoreConformance` | **Keep** | Reusable lifecycle, CAS, ownership, transition, listing, cancellation, and lease-expiry gate. |
| `TaskEventStoreConfig{New}`; `RunTaskEventStoreConformance` | **Keep** | Reusable fencing-before-deduplication, replay, ordering, cursor, and snapshot-pagination gate. |
| `NotificationOutboxConfig{New, ExpireLease}`; `RunNotificationOutboxConformance` | **Keep** | Reusable lease exclusion, expiry, redelivery, stale-receipt, and acknowledgement gate. |
| `SessionInboxConfig{New}`; `RunSessionInboxConformance` | **Keep** | Reusable session-scoped deduplication, ordering, limit, CAS, and tombstone gate. |

## 8. Control Middleware

| Exported API | Verdict | Audit |
|---|---|---|
| `TaskProgressReader.ReadProgress` | **Keep** | Executor-specific semantic projection capability. |
| `ToolConfig{Name, Desc, Disable}` | **Keep** | Conventional control-tool customization. |
| `TypedConfig{Manager, ProgressReadersByExecutorKey, TaskOutputToolConfig, TaskStopToolConfig}`; `Config` alias | **Keep** | One selection mechanism, keyed by durable executor identity. |
| `NewTyped`, `New` | **Keep** | Standard typed/default constructors. |

Model-facing contracts:

| Contract | Verdict | Audit |
|---|---|---|
| `task_output{task_id, block?, timeout?}` | **Keep** | Lifecycle-only blocking and limits are explicit. |
| `task_stop{task_id, reason?}` | **Keep** | Optional reason becomes durable first-write cancellation intent. |

## 9. Filesystem Integration

| Exported API | Verdict | Audit |
|---|---|---|
| `ExecuteTaskKind`; `CommandFromTask` | **Keep** | Consistent host-policy vocabulary and private payload decoding. |
| `BackgroundConfig{Local, Recoverable}` | **Keep** | Exactly-one mode validated at construction. |
| `LocalBackgroundConfig{Runner, OutputStore, OutputDir}` | **Keep** | Process-local authority is owned by `Runner`. |
| `RecoverableBackgroundConfig{Shell, Manager, Executors, ToolRegistry, OutputMaterializer, ForegroundTimeoutMs, ShouldAutoBackground}` | **Keep** | Explicit cross-worker capability and authorities. |
| `ExecuteToolConfig` | **Keep** | Existing filesystem extension point. |
| `MiddlewareConfig.Background`; `NewTyped`; `New` | **Keep** | Current integration surface. |
| legacy `Config`; `NewMiddleware` | **Compatibility** | Pre-existing filesystem compatibility only; the alpha background capability is intentionally absent. |
| model `execute.run_in_background`, `execute.timeout` | **Keep** | Explicit detachment and timeout stop/detach policy are documented. |

## 10. Sub-Agent Middleware

| Exported API | Verdict | Audit |
|---|---|---|
| `TaskKindSubagent`; `NameFromTask` | **Keep** | Consistent policy discriminator and private payload decoding. |
| `TranscriptFormat` | **Keep** | Named parameters and concurrency contract are explicit. |
| `TypedLocalBackgroundConfig{Runner, OutputStore, OutputDir}` and alias | **Keep** | Clear process-local mode. |
| `TypedDurableBackgroundConfig{Manager, Executors, Executor, ForegroundTimeoutMs, ShouldAutoBackground, RunOptionsFactories}` and alias | **Keep** | Explicit reconstruction dependencies. |
| `TypedBackgroundConfig{Local, Durable, TranscriptFormat}` and alias | **Keep** | Exactly-one mode is constructor-validated. |
| `TypedConfig` background-related fields; `Config`; `NewTyped`; `New` | **Keep** | Coherent optional integration. |
| `DurableTaskProgressReader`; `NewDurableTaskProgressReader`; `ReadProgress` | **Keep** | Concrete capability with validated session-store injection. |
| model `agent.run_in_background` | **Keep** | Consistent explicit detachment control. |

## 11. DeepAgent and Runner Integration

| Exported API | Verdict | Audit |
|---|---|---|
| `TypedBackgroundConfig{Manager, Executors, SubAgents, RecoverableShell, LocalShell, ForegroundTimeoutMs, ShouldAutoBackground, TranscriptFormat}` and alias | **Keep** | One lifecycle/registry authority with explicit capabilities. |
| `TypedDurableSubAgentConfig{Executor, RunOptionsFactories}` and alias | **Keep** | Only reconstructable sub-agent concerns. |
| `RecoverableShellConfig{Shell, OutputMaterializer}` | **Keep** | Only cross-worker shell concerns. |
| `LocalShellConfig{Shell, StreamingShell, OutputDir}` | **Keep** | Explicit process-local shell capability. |
| `TypedConfig.Background`; `NewTyped`; `New` | **Keep** | Strict forwarding facade; does not invent separate semantics. |
| `adk.RunnerSessionID` | **Keep** | Narrow request identity only; no dependency injection through context. |

## Documentation Audit

Strong:

- lifecycle transition, fencing, cancellation, drain, and checkpoint contracts
- snapshot ownership and byte-slice copying
- event replay, retention, ordering, and cursor behavior
- notification lease and at-least-once delivery semantics
- callback concurrency and worker-equivalence requirements
- local versus recoverable capability boundaries
- model-facing timeout and stream variants
- constructor failure behavior and provider-owned lifecycle timestamps
- executable provider obligations through `backgroundtask/storetest`

## Scorecard

| Dimension | Rating | Notes |
|---|---:|---|
| Concept coherence | 5/5 | Lifecycle, progress, transcript, and session events are distinct. |
| API usability | 5/5 | Dependencies are explicit and invalid wiring fails at construction. |
| Minimum surface | 5/5 | Speculative, compatibility-only, and unconsumed response concepts are absent. |
| Module separation | 5/5 | Domain recovery and test conformance live in dedicated packages. |
| Naming | 5/5 | Names identify exact lifecycle, presentation, and persistence roles. |
| Distributed correctness | 5/5 | Contracts have reusable conformance gates for external providers. |
| Documentation | 5/5 | Exported contracts and operational defaults are explicit. |
| Stabilization readiness | 5/5 | No known API rework or documentation blockers remain. |
