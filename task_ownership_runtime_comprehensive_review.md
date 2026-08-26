# Comprehensive Review Summary: Task Ownership Runtime

## 本轮全量复审 / Pre-flight

本节只建立复审基线，不复用下文既有结论，也不包含代码审查或修复。

### 范围与 baseline

- Diff：`origin/alpha/10...HEAD`（three-dot）
- Merge base / base commit：`46b3c87c597d11902276a97737b16ab4e837ec8d`
- HEAD：`02a7a31250a205a304c49b3afbead964c2d7487b`
- 规模：93 files changed，16,643 insertions，6,834 deletions
- 分类口径：删除条目优先计入 removed；其余按目标路径分类，`*_test.go`
  为 test，Markdown 为 docs，其他为 production。rename 作为一个变更条目，
  并按目标路径分类。
- 分类计数：production 44，test 38，docs 4，removed 7；合计 93。
- Baseline：`go test ./...` 通过；无失败 package。主要非缓存耗时包括
  `adk` 48.897s、`adk/task/tool` 9.650s、`adk/task/background` 5.642s、
  `adk/task/local` 5.491s、`adk/task/subagent` 5.339s。

### 全部变更文件

#### Production（44）

```text
M    adk/filesystem/backend.go
M    adk/handler.go
A    adk/internal/taskfirst/coordinator.go
M    adk/middlewares/filesystem/bash_run.go
M    adk/middlewares/filesystem/filesystem.go
M    adk/middlewares/filesystem/prompt.go
M    adk/middlewares/subagent/agent_tool.go
M    adk/middlewares/subagent/middleware.go
M    adk/middlewares/subagent/task_progress.go
R087 adk/middlewares/backgroundtask/middleware.go -> adk/middlewares/task/middleware.go
R082 adk/middlewares/backgroundtask/prompt.go -> adk/middlewares/task/prompt.go
M    adk/prebuilt/deep/deep.go
R054 adk/backgroundtask/executor.go -> adk/task/background/executor.go
A    adk/task/background/handle.go
R098 adk/backgroundtask/id.go -> adk/task/background/id.go
R069 adk/backgroundtask/in_memory_store.go -> adk/task/background/in_memory_store.go
A    adk/task/background/mailbox.go
A    adk/task/background/mailbox_manager.go
R080 adk/backgroundtask/manager.go -> adk/task/background/manager.go
R085 adk/backgroundtask/session_event.go -> adk/task/background/session_event.go
R052 adk/backgroundtask/store.go -> adk/task/background/store.go
R076 adk/backgroundtask/types.go -> adk/task/background/types.go
A    adk/task/context.go
A    adk/task/foreground/policy.go
A    adk/task/local/local.go
R071 adk/backgroundtask/local/stream.go -> adk/task/local/stream.go
A    adk/task/mailbox.go
R076 adk/backgroundtask/shell/shell.go -> adk/task/shell/shell.go
R056 adk/backgroundtask/storetest/conformance.go -> adk/task/storetest/conformance.go
A    adk/task/storetest/mailbox.go
R086 adk/backgroundtask/subagent/progress.go -> adk/task/subagent/progress.go
A    adk/task/subagent/runtime_types.go
A    adk/task/subagent/subagent.go
A    adk/task/subagent/turn_loop.go
A    adk/task/task.go
R055 adk/backgroundtask/tool/executor.go -> adk/task/tool/executor.go
R062 adk/backgroundtask/tool/managed_tool.go -> adk/task/tool/managed_tool.go
R100 adk/backgroundtask/tool/materializer.go -> adk/task/tool/materializer.go
R088 adk/backgroundtask/tool/progress.go -> adk/task/tool/progress.go
R097 adk/backgroundtask/tool/projection.go -> adk/task/tool/projection.go
R067 adk/backgroundtask/tool/registry.go -> adk/task/tool/registry.go
R070 adk/backgroundtask/tool/submit.go -> adk/task/tool/submit.go
R073 adk/backgroundtask/tool/tooltest/recovery_conformance.go -> adk/task/tool/tooltest/recovery_conformance.go
R079 adk/backgroundtask/tool/types.go -> adk/task/tool/types.go
```

#### Test（38）

```text
A    adk/internal/taskfirst/coordinator_test.go
M    adk/middlewares/filesystem/bash_run_test.go
M    adk/middlewares/filesystem/recoverable_shell_test.go
M    adk/middlewares/subagent/middleware_test.go
M    adk/middlewares/subagent/task_progress_test.go
R094 adk/middlewares/backgroundtask/middleware_test.go -> adk/middlewares/task/middleware_test.go
M    adk/prebuilt/deep/deep_test.go
R076 adk/backgroundtask/conformance_test.go -> adk/task/background/conformance_test.go
R087 adk/backgroundtask/durable_store_test.go -> adk/task/background/durable_store_test.go
A    adk/task/background/event_persister_test.go
A    adk/task/background/handle_test.go
R099 adk/backgroundtask/id_test.go -> adk/task/background/id_test.go
A    adk/task/background/mailbox_test.go
R079 adk/backgroundtask/manager_test.go -> adk/task/background/manager_test.go
A    adk/task/background/notification_attack_test.go
R085 adk/backgroundtask/notification_test.go -> adk/task/background/notification_test.go
R077 adk/backgroundtask/notify_parent_test.go -> adk/task/background/notify_parent_test.go
A    adk/task/background/publication_test.go
R088 adk/backgroundtask/session_event_test.go -> adk/task/background/session_event_test.go
R063 adk/backgroundtask/local/local_test.go -> adk/task/local/local_test.go
R087 adk/backgroundtask/shell/example_test.go -> adk/task/shell/example_test.go
R079 adk/backgroundtask/shell/shell_test.go -> adk/task/shell/shell_test.go
R060 adk/backgroundtask/storetest/in_memory_test.go -> adk/task/storetest/in_memory_test.go
A    adk/task/subagent/integration_test.go
A    adk/task/subagent/progress_test.go
A    adk/task/subagent/runtime_validation_test.go
A    adk/task/subagent/turn_loop_test.go
A    adk/task/task_test.go
R074 adk/backgroundtask/tool/attack_test.go -> adk/task/tool/attack_test.go
R079 adk/backgroundtask/tool/example_test.go -> adk/task/tool/example_test.go
R075 adk/backgroundtask/tool/managed_tool_test.go -> adk/task/tool/managed_tool_test.go
R077 adk/backgroundtask/tool/progress_test.go -> adk/task/tool/progress_test.go
R100 adk/backgroundtask/tool/session_context_test.go -> adk/task/tool/session_context_test.go
R087 adk/backgroundtask/tool/submit_attack_test.go -> adk/task/tool/submit_attack_test.go
R079 adk/backgroundtask/tool/submit_test.go -> adk/task/tool/submit_test.go
R094 adk/backgroundtask/tool/tooltest/recovery_conformance_test.go -> adk/task/tool/tooltest/recovery_conformance_test.go
R068 adk/backgroundtask/tool/validation_test.go -> adk/task/tool/validation_test.go
M    adk/turn_loop_test.go
```

#### Docs（4）

```text
M .trae/documents/design-durable-background-tool-execution.md
A adk/task/README.md
A task_ownership_runtime_comprehensive_review.md
A task_runtime_integration_test_audit.md
```

#### Removed（7）

```text
D adk/backgroundtask/local/local.go
D adk/backgroundtask/notification_attack_test.go
D adk/backgroundtask/subagent/subagent.go
D adk/backgroundtask/subagent/subagent_test.go
D adk/backgroundtask/subagent/typed_submit_test.go
D adk/internal/foreground/coordinator.go
D foreground_background_notification_comprehensive_review.md
```

### 新增或修改的 public symbols

口径为当前 Stage 1 工作树中声明新增、重命名、迁移、签名变化，或 public
类型字段发生变化的 exported symbol；仅注释或函数体变化不计。字段变化记录在
所属类型后。

- `adk/internal/taskfirst`（internal exported surface，新）：`Policy`、`StartRequest`、
  `Outcome`、`Execution`、`Start`、`Observe`、`ProjectionDetached`；
  `Execution.TaskID`、`Initial`、`Await`、`Boundary`、`Timeout`、
  `WaitBoundary`、`ResolveTimeout`、`ResolveCallerAbort`；
  `ForegroundMailboxFinalizer`、`NewForegroundMailboxFinalizer`、
  `SealIfIdle`、`Abandon`、`CombineForegroundErrors`。
- `adk/task`（新）：`StartMode`（`StartModeForeground`、
  `StartModeBackground`）、`Owner`（`OwnerParent`、`OwnerManager`）、
  `OutcomeStatus`（`OutcomeUnknown`、`OutcomeCompleted`、
  `OutcomeInterrupted`、`OutcomeFailed`、`OutcomeCanceled`）、`Outcome`、
  `Handle`、`InputClient`/`SendInput`、`ExecutionContext`、
  `WithExecutionContext`、`ExecutionContextFromContext`、`MailboxState`
  （`MailboxForeground`、`MailboxBackground`、`MailboxSealed`）、`Mailbox`、
  `InputDelivery`（`InputQueued`、`InputPreempt`）、`Input`、`InputRecord`、
  `RegisterMailboxRequest`、`RegisterMailboxResult`、`SendInputRequest`、
  `SendInputResult`、`ListInputsRequest`、`ListInputsResult`、
  `WaitInputsRequest`、`AdvanceCursorRequest`、`SealMailboxRequest`、
  `AbandonMailboxRequest`、`ListChildrenRequest`、`ListChildrenResult`、
  `InputSender`、`MailboxStore`，以及 `ErrMailboxStoreRequired`、
  `ErrMailboxNotFound`、`ErrMailboxIdentityConflict`、`ErrMailboxSealed`、
  `ErrInputRequired`、`ErrInputConflict`、`ErrInputsPending`、
  `ErrCursorConflict`、`ErrOwnershipLost`、`ErrSessionBusy`。
- `adk/task/foreground`（新）：`DefaultTimeoutMs`、`CandidateInfo`、
  `CallerAbortInfo`、`ShouldAutoBackground`、`ShouldCancelOnCallerAbort`。
- `adk/task/background`（新增/修改/由 `adk/backgroundtask` 迁移）：
  `TaskSnapshot`（原 `Task`，新增 `Publication`、
  `ParentNotificationError`）、`Publication`（`PublicationDeferred`、
  `PublicationOnCreate`、`PublicationOnBackground`）、`ExecutionAction`
  及六个 `ExecutionAction*` 常量、`ExecutionResult`、`ExecutionRuntime`、
  `CancellationAcknowledger`、`TaskEventEnvelope`、`TaskEventWriter`、
  `TaskEventPersister`、`TaskEventPersisterFunc`/`Persist`、
  `TaskEventPersistResult`（`Scope`、`Appends`）、`PersistTaskEvent`、
  `Handle` 及 `ID`、
  `SendInput`、`Wait`、`Cancel`。
- `adk/task/background` Manager surface：`Config`（`Tasks` 改为
  `LifecycleStore`，新增 `TaskEvents`）、`Manager.Submit`、`Publish`、`Get`、
  `WaitForTaskVersion`、`ReleaseSuspension`、`Handle`、
  `LoadOrRegisterExecutor`、`RegisterMailbox`、`GetMailbox`、
  `GetActiveMailboxBySession`、`SendInput`、`ListInputs`、`WaitInputs`、
  `AdvanceInputCursor`、`SealMailbox`、`AbandonMailbox`、`ListChildren`、
  `AdoptForeground`；`InMemoryStore` 上对应 lifecycle/mailbox 方法及
  `Publish`、`CommitInput`、`WaitInputIfNoInputs`、`SuspendIfNoInputs`、
  `CompleteIfNoInputs`。
- `adk/task/background` Store/SPI：`SubmitRequest`（新增
  `Publication`）、`PublishTaskRequest`、`AdoptForegroundRequest`、
  `AdoptForegroundStoreRequest`、`SuspendIfNoInputsRequest`、
  `WaitInputIfNoInputsRequest`、`CompleteIfNoInputsRequest`、
  `LifecycleStore`、`CommitInputRequest`、`TaskEventPartInput`、
  `TaskEventPart`、`TaskEventScope`；
  `TaskStore`、`TaskEventStore`、`NotificationWriter`、`NotificationOutbox`
  的 Task 参数统一为 `TaskSnapshot`，`CreateTaskRequest` 新增
  `Publication`/`ParentExecution`，`Notification` 和
  `NotifyParentRequest` 新增 `Delivery`，新增
  `NotificationTaskBackgrounded`；`TaskCreatedSessionEventSender` 参数改为
  `TaskSnapshot`。
- `adk/task/local`（迁移并修改）：`WorkFunc`、`StreamWorkFunc`、`Input`、
  `NoticeInfo`、`ProjectionDetached`、`Config`、`Runner`、`New`、
  `Runner.Manager`、`RunResult` 及 `ID`/`Foreground`/`Task`、`Run`、
  `RunStream`；`Config` 新增 caller-abort policy 和 `EventPersister`，
  buffered 返回类型改为 foreground/durable 二选一的 `RunResult`。
- `adk/task/subagent`（新 runtime surface）：`ResumeInputKind`、
  `StartRequest`、`Handle` 及 `ID`、`ChildSessionID`、`SendInput`、`Wait`、
  `Cancel`，`Result`、`StartOptions`、`ContinueRequest`、
  `CompletionAction` 及 `CompletionUnknown`/`CompletionComplete`/
  `CompletionSuspend`，
  `CompletionContext`、`CompletionBarrier`、
  `CancellationHook`、`InputPreemptPolicy`、`ChildSessionID`、
  `RuntimeSessionStoreAccessMode`、非法零值
  `RuntimeSessionStoreAccessUnknown`，以及
  `RuntimeSessionStoreAccessForegroundExecute`、
  `RuntimeSessionStoreAccessManagedExecute`、
  `RuntimeSessionStoreAccessReadProgress` 三个有效 mode、
  `RuntimeSessionStoreRequest`、
  `RuntimeSessionStoreFactory`、`RunOptionsFactory`、`AgentRegistration`、
  `InputsToAgentInput`、`ControllerConfig`、`Controller`、`NewController`，
  以及 `Controller.RegisterAgent`、`Manager`、`Handle`、`Start`、
  `Continue`、`SendInput`、`Wait`、`Cancel`、`ReadProgress`。
- `adk/task/tool`（迁移并修改）：`Tool`、
  `RecoverableTool`、`ResumableTool`、`InputPreparer`、`StartRequest`、
  `StartResult`、`RecoverRequest`、`ResumeRequest`、`Run`、`UpdateSource`、
  `InputRequest`、`Outcome`（统一使用 `task.OutcomeStatus`）、`Update`、
  `ResumeInputKind`、`ErrResumeInputRejected`、`ExecutorKey`、
  `RecoverableExecutorKey`、`ManagedToolResponseEventType` 及三个事件常量、
  `ManagedToolResponseEvent`、`Registration`（新增 `EventPersister`）、
  `Registry`、`NewRegistry`、`Registry.Register`、`ManagedToolConfig`
  （新增 caller-abort policy 和 `ForegroundTimeoutMsForInvocation`）、
  `NewManagedTool`、`SubmitRequest`、`Submit`、
  `ProgressReader`、`NewProgressReader`、`ReadInputRequest`。
- `adk/task/shell`（迁移并修改）：`RecoverableShell`、
  `StartCommandRequest`、`RecoverCommandRequest`（移除 checkpoint）、
  `RegistrationConfig`、`NewRegistration`。
- `adk/task/storetest`（迁移并修改）：`LifecycleStoreConfig`、
  `TaskEventStoreConfig`、`NotificationOutboxConfig`、
  `NotificationWriterConfig`、`MailboxStoreConfig`，
  `RunLifecycleStoreConformance`、`RunTaskEventStoreConformance`、
  `RunNotificationOutboxConformance`、`RunNotificationWriterConformance`、
  `RunMailboxStoreConformance`。
- `adk/middlewares/task`（由 `middlewares/backgroundtask` 迁移并修改）：
  `ToolConfig`、`Config`、`TypedConfig`、`ProgressReader`、
  `SystemPromptInput`、`New`、`NewTyped`。
- `adk/middlewares/subagent`（修改）：`TypedConfig.Tasks`（原
  `Background`）、`TaskConfig`、`TypedTaskConfig`、`LocalTaskConfig`、
  `TypedLocalTaskConfig`（新增 `EventPersister`）、`DurableTaskConfig`、
  `TypedDurableTaskConfig`（以 `Runtime` 替代 Manager/Executors/Executor
  组合）、`DurableProgressReader`、`NewDurableProgressReader`、
  `DurableProgressReader.ReadProgress`、`NameFromTask`。
- `adk/middlewares/filesystem`（修改）：`LocalBackgroundConfig.Manager`
  使用新 Manager；`RecoverableBackgroundConfig` 移除
  `Executors`，新增 `ShouldCancelOnCallerAbort`，policy 类型收敛；
  `CommandFromTask` 参数改为 `TaskSnapshot`。
- `adk/prebuilt/deep`（修改）：`TypedTaskConfig`/`TaskConfig`、
  `TypedConfig.Tasks`、`TypedDurableSubAgentConfig.Runtime`，并新增
  `TypedTaskConfig.ShouldAutoBackground`。

### 模块地图

| 模块 | 本轮基线职责 | 关键状态/边界 | 对应测试范围 |
|---|---|---|---|
| `adk/task` | owner-neutral Task identity、Handle、input、Mailbox authority | `StartMode`、`Owner`、`MailboxState`、`OutcomeStatus` | `task_test.go` |
| `adk/task/background` | durable lifecycle、publication、attempt fencing、mailbox 与 event persistence | Pending/Running/WaitingInput/Suspended/terminal；Deferred/OnCreate/OnBackground | conformance、durable store、mailbox、publication、notification、event persister、manager/handle |
| `adk/internal/taskfirst` | Manager-owned execution 与 foreground projection 协调 | timeout、caller abort、boundary resolution | `coordinator_test.go` |
| `adk/task/subagent` | persistent child session 与 TurnLoop 执行/恢复 | foreground/background owner、Continue 幂等、interrupt/resume、preempt | integration、turn loop、runtime validation、progress |
| `adk/task/local` | process-local closure 的 task-first 执行和 streaming projection | foreground、explicit/auto background、caller abort | local/stream tests |
| `adk/task/tool`、`adk/task/shell` | managed/recoverable/resumable external operation adapters | start boundary、checkpoint、input wait、event replay/materialization | attack、managed tool、submit、validation、recovery conformance、shell |
| `adk/middlewares/task` | `task_output`/`task_stop` control tools | lifecycle wait 与 executor-specific progress | middleware tests |
| `adk/middlewares/subagent` | Sub-agent tool wiring 与 transcript projection | local/durable selection、child-session continuation | middleware、progress tests |
| `adk/middlewares/filesystem` | shell execution wiring | local/recoverable mode、timeout/caller-abort policy | bash/recoverable-shell tests |
| `adk/prebuilt/deep` | shared Manager/Controller and middleware composition | capability validation、single runtime wiring | deep tests |
| docs | API/design/review/test-audit records | 与当前 Task-first contract 对齐 | 人工核对 |

## Overview

- Base: `origin/alpha/10`
- Branch: `feat/task-ownership-runtime`
- Scope: unified foreground/background Task runtime, durable mailbox,
  Sub-agent continuation, nested Task authority, managed tools, and middleware
- Current phase: Task 5 final full review complete; delivery pending
- Status: **local review approved**. Final code findings remaining: 0.
  Commit/push and PR checks remain pending.

## Stage 1: Design Review

### Iteration 1

| ID | Finding | Validate | Counter | Verdict | 修复文件与验证 |
|---|---|---|---|---|---|
| S1-I1-01 | 只配置 caller-abort policy 时，Local/Tool 仍走 direct foreground，policy 不生效。 | `Run`/`RunStream` 和 managed tool 原条件只检查 explicit/auto-background。 | 可把 callback 限定为 auto-background 的附属项，但公开配置没有表达该限制。 | **Fix** | `adk/task/local/{local,stream}.go`、`adk/task/tool/managed_tool.go`；buffered/streaming detach 与 cancel tests。 |
| S1-I1-02 | Local streaming 在 constructor 返回前不响应 foreground timeout/caller abort。 | `RunStream` 先阻塞等待 `ready`，timer 与 caller cancellation 尚未进入 projection select。 | 把 timeout 定义为首 chunk 后开始会缩小策略语义，且阻塞 constructor 可永久卡住调用。 | **Fix** | `adk/task/local/stream.go`；`TestRunnerStreamTaskFirstConstructionBoundaries`。 |
| S1-I1-03 | `Publish` 失败后的取消清理丢失错误且可能无限等待。 | 旧路径忽略 `cancelAndWait` 返回值，也没有独立 cleanup bound。 | 只返回 publish error 更简单，但无法判断 cancel intent/hook/terminal 是否完成。 | **Fix** | `adk/internal/taskfirst/coordinator.go`；发布错误聚合与 blocked executor bounded-cleanup tests。 |
| S1-I1-04 | `CancellationHook` 在 cancel intent 持久化前执行，失败时 Store 中没有 durable intent。 | `RequestCancel` 原先先调用 executor acknowledger，再写 Store。 | 提交前 hook 可避免记录“未清理”的取消，但进程崩溃会同时丢失 intent 与重试依据。 | **Fix** | `adk/task/background/executor.go`、`adk/task/subagent/turn_loop.go`；active/recovery/失败重试 tests。 |
| S1-I1-05 | 普通输入或 child notification 会自动唤醒计划内 `Suspended` Task。 | Store 对 `WaitingInput` 与 `Suspended` 使用同一 wake transition。 | 自动唤醒看似方便，但绕过 completion barrier 的显式 release 决策。 | **Fix** | `adk/task/background/{mailbox,in_memory_store}.go`、`adk/task/subagent/turn_loop.go`；LifecycleStore conformance 与 Continue tests。 |
| S1-I1-06 | 新 Sub-agent payload v1 复用 legacy key，可能把旧 v4 durable task 当成新协议解码。 | key 相同而 payload/恢复状态机不兼容。 | 增加双版本 decoder 会把旧 runtime 语义永久带入新 Controller。 | **Fix** | `adk/task/subagent/subagent.go`、`runtime_validation_test.go`；当时先隔离到中间 key `eino.dev/task-subagent`，legacy v4 由旧 worker 排空；最终 v2 key 见 R6-08。 |
| S1-I1-07 | Tool executor key 被改名会让升级后的 worker 无法领取已有 durable Tool task。 | Tool payload 协议仍兼容，没有更换 key 的必要。 | 与新 package 命名对齐不值得破坏 persisted routing contract。 | **Fix** | `adk/task/tool/types.go`、`validation_test.go`；恢复原有两个 Tool keys。 |
| S1-I1-08 | 旧 invocation timeout 字段名暗示外部操作超时，实际只覆盖 foreground observation timeout。 | explicit background start window 不使用该值，底层 operation 也不由它终止。 | 保留短名称会继续诱导调用方把它当业务超时。 | **Fix** | `adk/task/tool/managed_tool.go`、filesystem adapter/tests；使用 `ForegroundTimeoutMsForInvocation`。 |
| S1-I1-09 | `RuntimeSessionStoreFactory` 无法区分 TurnLoop 读写与 progress 只读访问。 | 旧布尔判别同时覆盖 detached execution 与 progress read。 | 工厂可从 `Task` 是否为空猜测，但该隐式协议脆弱且限制授权实现。 | **Fix** | `adk/task/subagent/{subagent,turn_loop,progress}.go`、`session_store_factory_test.go`；增加显式 `AccessMode`。 |
| S1-I1-10 | 是否为旧 alpha package 提供 source shim。 | package/type/field 已发生系统性重命名。 | shim 可减轻短期迁移，但会长期保留两套同义 API，并掩盖不兼容 durable protocol。 | **Won't Fix** | Alpha source package 允许 breaking migration；设计文档明确无 shim，调用方随版本迁移。 |
| S1-I1-11 | 后台 attempt 是否应保留任意 caller context values。 | 任意 value 不可序列化，worker handoff 后无法保证等价恢复。 | 首次本机 attempt 可以透传 values，但不能把偶然可用升级为 durable contract。 | **Won't Fix** | Manager-owned root context 是执行根；部署明确选择的值通过 `ContextSnapshotter` capture/restore。 |
| S1-I1-12 | Manager 与 `LifecycleStore` 看似同时暴露 lifecycle/mailbox API。 | Manager 方法委托 Store，进程内只维护 executor/attempt 协调状态。 | 删除 facade 会迫使 sibling runtime 直接依赖 Store SPI 和 fencing request。 | **Won't Fix** | `adk/task/background/{manager,store,mailbox_manager}.go` 补充 facade/authority 注释；Store 仍是唯一 durable truth。 |

### Iteration 2

| ID | Finding | Validate | Counter | Verdict | 修复文件与验证 |
|---|---|---|---|---|---|
| S1-I2-01 | Sub-agent `StartRequest`/`StartOptions` 与 `TypedAgentInput` 重复表达 streaming。 | 两个布尔值通过 OR 合并，无法区分来源且扩大 API。 | convenience field 少写一层，但制造两个 authority。 | **Fix** | `adk/task/subagent/runtime_types.go`、调用方与 tests；只保留 `Input.EnableStreaming`。 |
| S1-I2-02 | Completion zero value 被当作 complete，`WaitInput` 名称又与 durable `WaitingInput` 混淆。 | `iota` 从 complete 开始会让未初始化 barrier 决策成功；实际 transition 是计划内 suspend。 | 零值 complete 省校验，但 fail-open 会结束 Task。 | **Fix** | `adk/task/subagent/{runtime_types,turn_loop}.go`；增加 `CompletionUnknown`，更名为 `CompletionSuspend` 并 fail closed。 |
| S1-I2-03 | Sub-agent 重复公开 task runtime context setter/getter。 | 通用 `task.ExecutionContext` 已携带 authoritative Task identity。 | 专用 helper 更短，但允许调用方伪造第二套 Task identity。 | **Fix** | `adk/task/subagent/{runtime_types,turn_loop}.go`；Task identity 统一走 `task.ExecutionContextFromContext`，仅保留 `ChildSessionID`。 |
| S1-I2-04 | Event API 用同一 part 类型表示写入意图和持久化记录，且 framework 信任 persister 自报 append results。 | persister 可漏报、伪造或在后续失败时丢掉已写 prefix。 | 让 persister返回 results 可少一层 wrapper，但破坏 framework-owned identity/fencing 边界。 | **Fix** | `adk/task/background/{executor,types,store,in_memory_store}.go` 及 Local/Tool/Sub-agent adapters；拆分 `TaskEventPartInput`/`TaskEventPart`，Persister error-only，tracking writer 校验并收集结果。 |
| S1-I2-05 | Streaming persister 已写 prefix 后报错时，derived transcript 丢失已持久化内容或 replay 重复写。 | 旧 receiver 只在 persister 全成功后 materialize 返回列表。 | 报错时完全放弃 transcript 更简单，但与 Store authoritative prefix 不一致。 | **Fix** | `adk/middlewares/subagent/agent_tool.go`、event persister tests；只 materialize `Inserted=true` 的 validated prefix。 |
| S1-I2-06 | Direct foreground Local 暴露伪造的 durable event writer authority。 | Attempt 0 没有 Store claim/fence，却返回非空 scope/writer。 | 内存 writer 可统一调用形态，但会让 persister误以为事件已 durable。 | **Fix** | `adk/task/local/local.go`、`local_test.go`；direct foreground 返回空 scope/nil writer，只有 Manager-owned stream 持久化。 |
| S1-I2-07 | Local/Tool 每个 event 调 persister 且 `Stream=nil` 是否应改成 stream-owned persister。 | producer 已按 chunk/update 划分 logical event；再传 stream 会引入第二种 event 边界和长生命周期。 | 统一成 Sub-agent stream copy 看似对称，但 Local/Tool 没有单个 event 内的 chunk stream。 | **Won't Fix** | 保持 per-event `Stream=nil`；在 code comments、README 和 durable design 中明确，仅 Sub-agent streaming event 传独立 copy。 |
| S1-I2-08 | 自定义 Sub-agent persister 的输出仍被标注为 JSONL。 | 自定义 serializer 不保证 JSONL，model-facing hint 与通用 `task_output` label 会撒谎。 | 统一标签更简单，但会错误约束扩展格式。 | **Fix** | `adk/middlewares/subagent/agent_tool.go`、`adk/middlewares/task/middleware.go`、tests；仅默认 persister+format 声明 JSONL。 |
| S1-I2-09 | Deep 把顶层 managed launch capability 传播给 generated general，能力范围过宽。 | child 可获得 managed shell/auto-background policy；用户 Sub-agent 传播边界也不清晰。 | 全量继承配置省装配，但会扩大可发起后台工作的主体。 | **Fix** | `adk/prebuilt/deep/{deep,deep_test}.go`；顶层持有 launch capability，generated general 仅在 durable Sub-agent 开启时获得共享 task controls，用户 Sub-agent 不注入。 |
| S1-I2-10 | Sub-agent middleware 直接依赖 internal coordinator 的 projection signal。 | 跨 package 使用 internal helper 泄漏 coordinator 细节。 | 直接调用少一层，但让 middleware 越过 Local ownership boundary。 | **Fix** | `adk/task/local/local.go`、`adk/middlewares/subagent/agent_tool.go`；由 Local 暴露窄 `ProjectionDetached` 查询。 |
| S1-I2-11 | README、durable design 和 review inventory 仍引用旧状态/API/key/保证。 | 文档与当前源码在 suspension、completion、streaming、event part、progress reader、migration 和 Deep scope 上不一致。 | 可等最终 review 一次更新，但会让 iteration 2 的设计判断失去可审计基线。 | **Fix** | `adk/task/README.md`、`.trae/documents/design-durable-background-tool-execution.md`、本文件；旧符号 `rg` 与 `git diff --check`。 |
| S1-I2-12 | Direct foreground Local 移除伪 durable writer 后，buffered managed shell 仍无条件持久化输出事件。 | 定向测试中两个 filesystem foreground 用例返回 `runtime returned an incomplete task event writer`；`bashWork` 经 `appendResult` 调用 event persistence，而 direct runtime 正确返回 nil writer。 | 恢复伪 writer 可让测试通过，但会重新引入 S1-I2-06 的 authority 问题。 | **Fix** | `adk/middlewares/filesystem/{bash_run.go,bash_run_test.go}`；仅 Manager-owned execution 持久化 event，direct foreground 不创建 durable Task，并验证 background 仍写入完整 event part。 |

### Final re-review findings

| ID | Finding | Validate | Counter | Verdict | 修复文件与验证 |
|---|---|---|---|---|---|
| S1-FR-01 | `TaskEventPersistResult.Parts` 实际返回 append 结果而非持久化 part records，名称不准确。 | 元素类型是 `AppendTaskEventResult`，并携带 `Inserted` replay 元数据；真正的 record 位于 `.Part`。 | `Parts` 较短，但会与 `ListTaskEventsResult.Parts` 的 record 集合混淆。 | **Fix** | 更名为 `Appends`，同步 Tool/Sub-agent 调用点、API shape test、event persister tests 和现行文档。 |
| S1-FR-02 | `TaskEventPartInput.PartID` 的 writer 契约未说明必填，容易与 Store request 的空值 shorthand 混淆。 | Tracking writer 和 attempt writer 都拒绝空 `PartID`，而 `AppendTaskEventRequest` 明确把空值归一为 `"event"`。 | 两层都允许 shorthand 会减少一处字段，但 writer 需要显式、稳定的 replay identity。 | **Fix** | `types.go` Go doc、README、durable design 和实施计划明确 writer 必填、底层 request 才接受 shorthand。 |
| S1-FR-03 | `CancellationHook` 文档只描述单个 active attempt 内去重，没有要求跨请求与恢复重试时幂等。 | durable cancel intent 可由后续 `RequestCancel` 或新 recovery attempt 再次触发 hook，且可能发生在另一 worker/process。 | Runtime 无法为业务 side effect 提供跨进程 exactly-once；隐藏重试只会制造错误保证。 | **Fix** | `runtime_types.go` Go doc 与 README 明确重试范围及实现必须幂等。 |
| S1-FR-04 | README 的 Local streaming 段仍使用已移除的 singular event type 名称。 | 当前公开模型只有 `TaskEventPartInput`、`TaskEventPart` 及相关 Store API。 | 将其理解为普通概念仍可能误导读者搜索不存在的类型。 | **Fix** | 改为 “durable Task event parts”，并用 `rg` 检查现行文档旧符号。 |
| S1-FR-05 | 是否应丢弃每次 `Manager.Execute` 调用 context 中的所有 values。 | 自动后台首次 attempt 会保留 submit 调用链 values；恢复 attempt 则从当前 worker dispatch context 开始，并叠加持久化 snapshot，任意 values 不具备跨 attempt 一致性。 | 改用 `context.Background()` 会同时丢失 worker 注入的 tracing、logging 和进程内依赖；框架也无法通用序列化任意 value。 | **Won't Fix** | 调用 context values 仅是 attempt-scoped、best-effort carrier，不构成 durable contract；需要跨请求/worker 恢复的部署选定值必须由 `ContextSnapshotter` capture/restore。该结论重申 S1-I1-11，不新增第二套 context API。 |

### Clearance A findings

| ID | Finding | Validate | Counter | Verdict | 修复文件与验证 |
|---|---|---|---|---|---|
| S1-CA-01 | Local direct foreground 伪造 `TaskSnapshot`，调用方无法判断结果是否来自 durable lifecycle。 | direct path 没有 `Manager.Submit`，但旧 `Runner.Run` 返回形状与 Manager-owned Task 相同；调用方可能把 mailbox-only ID 交给 `Manager.Get`/`task_output`。 | 统一返回 `TaskSnapshot` 表面简单，但会把不存在的 durability、attempt 和 Store authority 写进 API。 | **Fix** | `adk/task/local/local.go` 增加 `RunResult`，`Foreground()`/`Task()` 恰好一个成功，`ID()` 两侧可用；同步 filesystem、Sub-agent、task middleware 调用方和 invalid-shape tests。 |
| S1-CA-02 | coordinator 的 terminal signal 不能表达 `WaitingInput` 也会结束 foreground observation。 | Managed Tool 到达 `StatusWaitingInput` 后需要返回 interrupt；若只等待 terminal，buffered/streaming projection 会继续挂起或各自旁路判断。 | 可以保留 terminal watcher并增加第二个 waiting-input channel，但会制造两个竞态 authority。 | **Fix** | `adk/internal/taskfirst/coordinator.go` 收敛为稳定的 `Boundary`/`WaitBoundary`，统一覆盖 WaitingInput 与三种 terminal；Local/Tool consumers 和 tests 同步。 |
| S1-CA-03 | bounded cancellation 的成功/失败返回契约不完整，普通取消与 publication-failure fallback 混用了 partial outcome。 | blocked executor、hook failure 或 Store failure 下，调用可能无限等待、丢失 cleanup cause，或向普通取消调用方暴露 Running + cancel-requested snapshot。 | 一律返回 latest snapshot 有利于诊断，但会让正常 timeout/caller-abort cancellation 看起来已成功解决。 | **Fix** | `taskfirst.Policy.CleanupTimeout` 提供默认 5 秒上界；普通取消失败返回 nil Outcome；publication failure 始终保留 publish cause，并在 cleanup 失败时返回 combined error 与 latest snapshot。对应 cooperative、blocked、hook-failure tests。 |

### Clearance B findings

| ID | Finding | Validate | Counter | Verdict | 修复文件与验证 |
|---|---|---|---|---|---|
| S1-CB-01 | direct foreground Local/Managed Tool 的 Mailbox 在失败、取消、timeout、stream close 和 pending-input race 中缺少统一收尾契约。 | 部分路径只在成功时 seal，部分路径使用已取消 caller context cleanup，Store error 可能丢失；盲目 seal 又会覆盖 racing input。 | 每个 adapter 自行补 defer 可以减少一个 internal helper，但 buffered/streaming/start/resume 路径会继续分叉。 | **Fix** | 新增 `ForegroundMailboxFinalizer`：detached bounded context、generation/cursor fencing、at-most-once；完成 `SealIfIdle`，失败类路径 `Abandon`，`ErrInputsPending` 保留输入并返回。Local/Tool tests 覆盖 buffered、streaming、constructor、resume 与 Store error。 |
| S1-CB-02 | SessionStore factory 的执行访问仍不足以区分 caller-owned write、Manager attempt write 与 progress read。 | 只有通用 execute/read 两类时，factory 仍需从 snapshot presence 推断 foreground 与 managed authority；深层 nested progress 还可能误用 root session。 | factory 可以统一授予读写来简化实现，但这会扩大 progress 权限并失去 attempt-scoped authorization。 | **Fix** | `RuntimeSessionStoreAccessMode` 明确三个有效 mode：`ForegroundExecute`、`ManagedExecute`、`ReadProgress`；零值非法，Task nil/non-nil 组合受校验，`ParentSessionID` 使用直接 parent。增加 factory/validation/nested progress tests。 |
| S1-CB-03 | Managed Tool 文档把成功结果概括为总有 `task_id`，与 foreground wire contract 冲突。 | direct foreground 以及 deferred Task 在 foreground boundary 前完成时都输出 `foreground_result` 且 `task_id` 为空；只有 `launch_result` 可交给 control tools。 | 暴露所有内部 ID 似乎更统一，但会把 mailbox-only 或未发布 deferred identity 错当成 model-facing durable handle。 | **Fix** | `NewManagedTool` description、response docs、README 与 durable design 明确：`launch_result` 必有 `task_id`，`foreground_result`/`update` 不携带；增加 Info 与 fast-completion assertions。 |

### Clearance C findings

| ID | Finding | Validate | Counter | Verdict | 修复文件与验证 |
|---|---|---|---|---|---|
| S1-CC-01 | Managed Tool 的剩余文档仍把 `launch_result` 解释为一般成功结果，并把 `foreground_result` 等同于未持久化的 direct execution。 | deferred Manager-owned Task 可在 publication 前同步完成；此时 Store 中已有 snapshot，但 wire result 仍是无 control handle 的 `foreground_result`。 | 可继续用“有无持久化 Task”解释 wire variant，但这会把内部 lifecycle identity 与 model-facing publication 混为一谈。 | **Fix** | `adk/task/tool/types.go`、`NewManagedTool` description、`adk/task/README.md` 与 durable design 统一按 publication 定义：`launch_result` 仅表示已发布 background handle；`foreground_result` 是同步 wire result，可来自 direct execution 或未发布 deferred Task，不承诺 model-facing `task_id`。Info test 同步更新，direct/deferred assertions 保留。 |
| S1-CC-02 | Local 与 Managed Tool 各自复制 foreground operation error 与 Mailbox finalization error 的组合实现。 | 两处 helper、error type、`Error`/`Unwrap`/`Is` 实现逐字同构，后续修改可能导致错误顺序或 `errors.Is` 行为分叉。 | 保留 package-local helper 可减少一个 internal symbol，但错误契约本来就是共享 finalizer 的一部分，重复实现没有独立语义。 | **Fix** | 在 `adk/internal/taskfirst` 增加唯一的 `CombineForegroundErrors`，Local/Managed Tool 全部复用并删除副本；helper tests 覆盖 nil/单错误/双错误、operation-first 文本与 unwrap 顺序，以及 operation/finalization 两侧 `errors.Is`。 |

### Stage 1 当前判定

- Iteration 1/2、final re-review 及 Clearance A/B/C findings 已完成
  `Validate`、`Counter` 和 `Fix`/`Won't Fix` 分类。
- 四项 `Won't Fix` 为：旧 alpha package shim、caller context values、
  Manager facade、Local/Tool per-event nil stream；S1-FR-05 补充记录了
  caller context values 的 attempt-scoped 与 durable 边界。
- 所有已确认 Fix 均已落入当前工作树；针对 final re-review 与 Clearance A/B/C
  修复的确认性复审尚未执行。
- **不得据此勾选 Task 2，也不得宣称 Stage 1 完成。**

## Stage 2: Attack Review

Stage 1 final approval 后，针对 Task ownership、publication、mailbox、
stream persistence、Sub-agent recovery/continuation 和 Deep wiring 新增
26 个攻击测试；仓库内 `TestAttack_` 总数为 167。新增测试分布在：

- `adk/middlewares/subagent/stage2_attack_test.go`
- `adk/prebuilt/deep/stage2_attack_test.go`
- `adk/task/background/stage2_partition_a_attack_test.go`
- `adk/task/local/stage2_attack_test.go`
- `adk/task/subagent/stage2_attack_test.go`

### Test hardening findings

| ID | Finding | Verdict | 处置 |
|---|---|---|---|
| W1 | Deep 配置测试名声称 hooks 已到达 durable runtime，但测试实际只验证 `deepSubagentBackground` 的配置映射。 | **Fix** | 更名为 `TestAttack_DeepMapsDurableSubAgentConfiguration`，注释同步限定为配置映射与 identity preservation。 |
| W2 | `TestAttack_StreamTimeoutStartsAfterReady` 使用 real-time timing，存在慢机抖动风险。 | **Won't Fix** | 当前 runtime 没有可注入 fake clock；测试用 1 秒 coordination bound，并让 constructor latency 达到 foreground timeout 的 2 倍，判定窗口足够宽。该用例在 `-race -count=20` 下稳定通过。 |
| W3 | Sub-agent stream persistence failure 测试只扫描并排除 assistant message；若没有任何 message event，会 vacuous pass。 | **Fix** | `LoadEvents` 限定 `SessionEventMessage`，扫描前断言恰好一个 user message，且内容为 `"stream"`。 |
| W4 | 5 个新增 Stage 2 attack 文件未统一执行格式化，background 文件存在两处复合字面量对齐差异。 | **Fix** | 对全部 5 个文件执行 `gofmt` 并以 `gofmt -d` 确认无剩余差异。 |

### Stage 2 verification

- 两个受影响 focused tests 通过；对应 package 的 `-race` focused tests 通过。
- 5 个 Stage 2 package 的全部 `TestAttack_` 通过。
- `TestAttack_StreamTimeoutStartsAfterReady` 在 `-race -count=20` 下通过。
- 全仓 broad `go test ./... -run '^TestAttack_'` 按要求中止，不作为本轮通过结论。
- `gofmt -d` 与 `git diff --check` 通过。

**Stage 2 attack-test hardening verdict: APPROVE.**

## Stage 3 Final Approval

Stage 3 从头审计 `origin/alpha/10...HEAD` 范围内的测试，并复查本轮新增、
修改和删除的测试。审计覆盖重复、断言强度、样板、逻辑分组、语义价值和
关键覆盖缺口六个维度。

### 六维审计

| 维度 | 最终结论 |
|---|---|
| 重复 | 合并或删除重复的 reader-error、slow timeout 和构造取消用例；保留 direct 与 task-first 等 ownership 不同的语义对照。 |
| 断言强度 | 将弱状态、错误和数量断言收紧为精确契约；JSON payload 使用 `JSONEq`，避免把格式差异当成行为差异。 |
| 样板 | 复用 bounded wait、Manager cleanup、stream drain 和 Controller fixture helper，减少重复 setup，且不隐藏状态转换。 |
| 逻辑分组 | 同一入口的边界条件使用 table/subtest 聚合；跨层 ownership、恢复和并发场景继续保留独立集成测试。 |
| 语义价值 | 新增测试均锁定 publication、mailbox、attempt fencing、stream error、interrupt/resume 或恢复不变量；未新增 getter-only 测试。 |
| 覆盖缺口 | 补齐 Manager admission/cancel、foreground adoption、Local stream、Managed Tool、Sub-agent runtime 与 middleware wiring 的关键分支。 |

### Findings

| ID | Finding | Validate | Counter | Verdict |
|---|---|---|---|---|
| S3-01 | 测试中的裸 channel receive 和忽略的 `WaitInputs` error 可能无限等待或误报通过。 | 多处同步只依赖 goroutine 最终推进，Store conformance 还丢弃了 wait error。 | 外层 context 能限制部分生产调用，但不能保证测试 goroutine 和断言本身及时结束。 | **Fix**：统一改为 bounded wait/select，并显式断言异步 error；`WaitInputs` 压测 `-count=100` 通过。 |
| S3-02 | reader-error 与 slow timeout 用例存在重复。 | 部分用例覆盖同一入口和同一结果，只改变了 fixture 或等待方式。 | direct 与 task-first 的 owner、持久化结果不同，不能全部合并。 | **Fix**：删除或表格化真正重复项，保留 ownership 或错误边界不同的用例。 |
| S3-03 | JSON 使用原始字符串相等断言，绑定了非契约格式。 | key 顺序或空白变化会导致语义相同的 payload 失败。 | 当前 encoder 输出稳定，但测试目标是 payload 语义，不是序列化排版。 | **Fix**：相关 checkpoint、interrupt 和 resume payload 改用 `require.JSONEq`。 |
| S3-04 | `TestTurnLoop_PushStrategy_DuringTurn` 的结束条件缺少第二轮 agent 启动 handshake。 | 第二次 `GenInput` 已执行不等于第二轮 agent 已进入，立即 Stop 存在调度竞态。 | 常规调度下两者接近，所以历史上大多通过，但不构成 happens-before。 | **Fix**：增加 `secondAgentStarted` 信号后再 Stop，消除 flake 窗口。 |
| S3-05 | 扩展后的 mailbox conformance 触发 `funlen`。 | 单函数承载完整 provider contract，长度超过 lint 阈值。 | 拆散会削弱 provider 复用入口和契约可发现性，且不会降低场景总复杂度。 | **Fix**：保留单一 suite，并增加局部 `nolint:funlen` 及理由。 |
| S3-06 | 关键运行时分支覆盖不足。 | adoption、sticky writer validation、stream construction race、runtime input 和 middleware wiring 存在未覆盖分支。 | 单纯追逐行覆盖会产生低价值测试。 | **Fix**：只补充可观察状态、错误传播和恢复语义测试；重新审计未发现剩余高优先级缺口。 |

### Coverage

- Raw diff coverage：`87.29%`。
- Semantic diff coverage：`85.50%`。
- 关键范围最低 coverage：`70.97%`。
- 达到 diff coverage `85%`、关键范围 `70%` 的门槛。

### Stage 3 verification

- 当前 toolchain：`go test ./...` 通过，并完成一次全仓重跑。
- Race：Task runtime、Sub-agent middleware、task middleware 与 Deep 核心跨包
  `-race` 通过。
- Go 1.18：`GOTOOLCHAIN=go1.18.10 go test ./...` 通过。
- 稳定性：`WaitInputs -count=100`、Managed Tool 新增用例 `-count=5`
  均通过。
- 格式与 diff：PR Go 文件 `gofmt -d` 无差异；`git diff --check` 通过。
- `golangci-lint run ./...` 与 `go vet ./...` 均通过，`0 issues`。

所有 Stage 3 findings 均已完成 Validate、Counter 和 Verdict 分类；确认项均已
Fix，无 Won't Fix、Defer 或剩余高优先级 finding。

**Stage 3 final verdict: APPROVE. Remaining findings: 0.**

## Verification

- Pre-flight baseline（修改前）：`go test ./...` 通过。
- Iteration 1/2 targeted packages：`adk/internal/taskfirst`、
  `adk/task/{background,local,subagent,tool}`、`adk/middlewares/{subagent,task}`、
  `adk/prebuilt/deep` 通过。
- S1-I2-12：两个 foreground managed-shell 回归用例、background event
  persistence 用例及 `adk/middlewares/filesystem` package tests 通过；
  package race 与 Go 1.18 compile 通过。
- Final re-review fixes：`adk/task/background` 的 event persistence/API shape
  聚焦测试通过；`adk/task/{background,tool,subagent}` 与
  `adk/middlewares/subagent` focused compile 通过。
- Clearance A/B contract sync：`go test ./adk/task/...` 通过；
  `go test ./adk/internal/taskfirst ./adk/middlewares/task
  ./adk/middlewares/subagent ./adk/middlewares/filesystem` 通过。
- README 相关代码示例使用临时 compile harness 验证 `Executor`、
  `RunResult`、三个 `RuntimeSessionStoreAccessMode` 和 managed-tool envelope；
  `go test ./adk/task` 及 `go test ./adk/task/tool -run '^Example' -count=1`
  通过，临时文件已删除。
- Documentation consistency：旧符号 `rg` 无陈旧现行 API 命中；
  `git diff --check` 通过。
- Clearance C fixes：`go test ./adk/internal/taskfirst ./adk/task/local
  ./adk/task/tool` 与对应 package `-race` 通过；当前 toolchain 及
  `GOTOOLCHAIN=go1.18.10` 的 `go test ./... -run '^$'` 全仓 compile 通过；
  `git diff --check` 通过。
- Stage 3 已完成当轮全仓、核心跨包 race、Go 1.18、lint 与 vet 验证。
- Task 5 Round 6 已完成最终 focused、全仓、race、Go 1.18、lint、vet、
  gofmt 与 diff verification；PR checks 尚未确认。

## Remaining Items

1. 提交并推送最终 review commit。
2. 确认 PR #1204 GitHub Actions、Codecov 与 CLA 全部通过。
3. 最终提交后 append progress，并清理本轮临时文件；本轮文档收尾不提前执行。

## Stage 1 Final Approval

本节记录 Stage 1 的最终确认性复审结论，并取代上述“Stage 1 当前判定”及
“Remaining Items”中的待确认状态。确认性复审重新检查了完整
`origin/alpha/10...HEAD` diff、全部新增或修改的 public API，以及
Iteration 1/2、Final re-review、Clearance A/B/C 的修复结果。所有确认的
design findings 均已 Fix 或有明确的 Won't Fix 理由，没有剩余 blocker 或
需要 Defer 的 finding。

**Stage 1 final verdict: APPROVE.**

### 12 维评分

| 维度 | 评分 | 最终结论 |
|---|---:|---|
| Concept Coherence | 5/5 | Task、ownership、publication、mailbox 与 event persistence 的概念边界一致，无重复 authority。 |
| API Usability and Intuitiveness | 5/5 | zero value、foreground/durable result、boundary 与 session-store access 均已收敛为可判定契约。 |
| Minimum API Surface | 5/5 | 删除重复 streaming/context API；保留的 Manager facade 与 per-event persister 均有独立职责。 |
| Backward Compatibility | 4/5 | Tool durable key 保持兼容；Sub-agent 通过新 key 隔离不兼容协议，alpha source break 已明确记录迁移要求。 |
| Module Separation and Layering | 5/5 | Store、Manager、coordinator、adapter 与 middleware 的 authority 和依赖方向清晰。 |
| Cohesion vs. Tension | 5/5 | foreground projection、durable execution 与 mailbox finalization 的共享契约已统一。 |
| Elegance vs. Complexity | 4/5 | lifecycle 与恢复本身复杂，但 accidental duplication 和双重 signal/authority 已移除。 |
| Naming | 5/5 | `Boundary`、`CompletionSuspend`、`Appends`、access modes 与 timeout 字段准确表达语义。 |
| Readability | 4/5 | 并发和恢复路径仍需较高上下文，但关键状态转换、错误优先级和 fencing 已有明确结构与文档。 |
| Duplication | 5/5 | foreground error composition 与 mailbox finalization 已集中复用，平行路径不再维护同构实现。 |
| Public API Documentation | 5/5 | public types、nil/zero-value、stream ownership、retry/idempotency 与 publication contract 已同步。 |
| Internal Comments | 5/5 | Store authority、attempt context、tracking writer 与 facade 边界均有维护者所需说明。 |

总分：`57/60`。低于满分的维度反映 alpha migration 与 durable concurrency
的固有成本，不构成 Stage 1 阻塞项。

### 最终验证结果

- `go test ./adk/task/...` 通过。
- `go test ./adk/internal/taskfirst ./adk/middlewares/task
  ./adk/middlewares/subagent ./adk/middlewares/filesystem` 通过。
- `go test ./adk/internal/taskfirst ./adk/task/local ./adk/task/tool` 及对应
  package `-race` 通过。
- 当前 toolchain 与 `GOTOOLCHAIN=go1.18.10` 的
  `go test ./... -run '^$'` 全仓 compile 通过。
- README 示例 compile harness、`go test ./adk/task` 与
  `go test ./adk/task/tool -run '^Example' -count=1` 通过。
- 文档旧符号检查无陈旧现行 API 命中，`git diff --check` 通过。

Task 2 已完成；后续工作从 Stage 2 attack review 开始。

## Stage 2 Final Approval

Stage 2 最终确认性复审覆盖 Task ownership、publication、mailbox、stream
persistence、Sub-agent recovery/continuation 与 Deep wiring。新增 26 个
`TestAttack_`，仓库内共 167 个；5 个涉及 package 的全部 attack tests 及
对应 race 验证均通过。所有 finding 已完成 Validate、Counter-argue 和
Fix/Won't Fix 分类，未发现修复引入的新路径问题。

**Stage 2 final verdict: APPROVE.**

- New attack tests: 26
- Total attack tests: 167
- Race verification: PASS
- Remaining findings: 0
- Red tests: 0

Task 3 已完成；Stage 3 test audit 亦已在上文完成。

## Task 5 Final Full Review（Round 6）

Round 6 从头复查 `origin/alpha/10...HEAD` 的完整 PR diff，并复查当前工作树中
Task 5 的所有代码与测试改动。该轮将上游 #1215、#1217 的行为移植到
Task-first Sub-agent runtime，同时闭环 checkpoint durability、输入确认、
attached lifecycle 与 filesystem timeout wire contract。以下每项均完成
Validate、Counter 和 Verdict；最终代码 finding remaining：**0**。

### Port 与 checkpoint/timeout findings

| ID | Finding | Validate | Counter | Verdict |
|---|---|---|---|---|
| R6-01 | Port #1215：`ControlDrain` 的 graceful stop 可能无限等待 blocked model/tool。 | 零上界会耗尽 shutdown deadline，Task 来不及持久化 checkpoint 并 suspend。 | 强制 immediate cancel 会放弃正常 safe-point drain。 | **Fix**：新增 `DrainCancelTimeout`；正值超时后升级为 checkpointed immediate cancellation，零值保留 legacy unbounded graceful 行为。blocked model、model stream、tool 均验证可 suspend/resume。 |
| R6-02 | Port #1217：drain escalation 的 stream cancellation 可能被当作普通执行失败，丢失可恢复 checkpoint。 | `adk.ErrStreamCanceled` 及 output materialization error 若绕过 control resolution，会把 Task 终态化为 failed。 | 所有 stream cancel 都视为 drain 会掩盖没有 control request 的真实失败。 | **Fix**：仅在 cancellation-shaped error 后等待已在途 control；有 `ControlDrain` 时 suspend 并保留 checkpoint，无 control 时仍返回原 failure。 |
| R6-03 | TurnLoop checkpoint 与 lifecycle checkpoint 分开写入，崩溃可产生 cursor、ack 和 runner state 的 split-brain。 | 外部 `CheckPointStore.Set` 成功但 `CommitInput` 失败时，新 runner state 可被旧 lifecycle cursor 错误恢复。 | 保留两个 Store 写入点改动较小，但无法跨 Store 提供原子性。 | **Fix**：managed execution 使用 capture store，将 TurnLoop state inline 到 lifecycle checkpoint，并通过 `CommitInput` 原子提交 cursor、sparse ack 与 runner state；legacy v1 只作恢复迁移读取。 |
| R6-04 | JSON runtime checkpoint 会对 opaque TurnLoop bytes 做 base64 膨胀，接近 1 MiB lifecycle 限制时无法保存。 | 随机 runner state 在 JSON/base64 下超过限制，而原始 bytes 仍可容纳。 | 提高 Store 限制会把编码开销和兼容负担转嫁给 provider。 | **Fix**：runtime checkpoint v2 使用带 magic/version、长度边界和严格尾部校验的 binary codec；保留 legacy v1 JSON decode，malformed/truncated/overflow 输入 fail closed。 |
| R6-05 | preempt/恢复可乱序消费 mailbox input，只有 contiguous cursor 会重复执行已消费的高 sequence。 | sequence 3 先完成、cursor 仍为 1 时，恢复读取会再次投递 sequence 3。 | 只按最大 sequence 推进 cursor 会跳过尚未消费的 sequence 2。 | **Fix**：checkpoint 记录有界、排序且折叠后的 `SparseAcks`；恢复合并 checkpoint/mailbox identity，跳过已确认项并在缺口补齐后推进 contiguous cursor。上限 4096，非法、冲突或超限均 fail closed。 |
| R6-06 | TurnLoop capture 后若没有新的 `Set` 回调，最新 cursor/ack/final 可能未提交到 lifecycle Store。 | graceful stop、turn completion 和无新 runner-state 写入路径都可能留下 `pendingCommit`。 | 仅依赖 capture callback 更简单，但 callback 是否发生不是 lifecycle durability contract。 | **Fix**：TurnLoop 退出后执行 post-capture commit，把最终 captured state 与 pending cursor/ack 一并提交；terminal commit 失败时保留可恢复 checkpoint，成功进入 terminal 时原子清空 checkpoint。 |
| R6-07 | attached foreground terminal candidate 在 late input 竞争后可能被旧结果重新 seal；仅 Delete 对不支持 deleter 的 Store 无效。 | stale candidate cursor 落后于 mailbox cursor 时，进程重启仍可能读到旧 terminal result。 | 要求所有 `CheckPointStore` 实现 `CheckPointDeleter` 会破坏现有 SPI；忽略 Delete 失败会保留歧义状态。 | **Fix**：先持久化 v2 `invalidated` marker，再 best-effort delete；no-deleter Store 仍能稳定拒绝旧 candidate。cursor conflict 会重读 mailbox 后判定，decode failure 不产生 lifecycle side effect。 |
| R6-08 | 是否保留中间 Sub-agent `ExecutorKey` 以兼容 v1/v2 checkpoint。 | 中间 v1 `eino.dev/task-subagent` 与 v2 checkpoint protocol 不兼容，共用 key 会让 mixed rollout worker 领取错误协议。 | 注册旧 key 或双 decoder 可兼容已持久化 Task。 | **Won't Fix**：中间 v1/v2 均未发布，不存在生产 durable Task 迁移义务；使用新 key `eino.dev/task-subagent-durable-v2` 隔离协议，旧 worker 不注册新 key，新 worker 不领取旧 key，防止混部误领。 |
| R6-09 | managed filesystem `timeout` 以毫秒暴露给模型，单位不直观且 schema、prompt、执行转换容易分叉。 | model-facing schema/prompt 需要秒语义；已有 replay 仍可能携带 legacy millisecond `timeout`。 | 直接删除旧字段最简洁，但会破坏已持久化参数重放。 | **Fix**：公开 `timeout_seconds`，内部转换为毫秒；legacy `timeout` 继续按 ms 解码但不再出现在 schema，双字段冲突和 null/非整数 fail closed，正值上限统一 clamp 到 3 天。 |

### Final coverage

- Raw diff coverage：`89.32%`。
- Semantic diff coverage：`88.51%`。
- Changed lines：`5117`。
- Important-scope minimum coverage：`71.43%`。
- 满足 raw/semantic diff coverage `85%` 与关键范围 `70%` 门槛。

### Final verification

| Gate | Result |
|---|---|
| Focused tests | **PASS** |
| Full repository tests | **PASS** |
| Race tests | **PASS** |
| Go 1.18 compatibility | **PASS** |
| `golangci-lint` | **PASS** |
| `go vet` | **PASS** |
| `gofmt` | **PASS** |
| `git diff --check` | **PASS** |

**Task 5 final full review verdict: APPROVE. Final code findings remaining: 0.**

Task 5 的复查、汇总与本地全量门禁已完成。最终 commit/push、PR checks、
工作区清洁和 progress append 保持待办。
