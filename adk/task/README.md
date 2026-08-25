# Task 模块

## 概述

Task 模块解决的是一个简单但容易被混淆的问题：

> 一段工作开始后，即使最初发起它的调用已经返回、进程发生切换，或者它正在等待新的输入，我们仍然需要知道“这件事是谁、现在由谁负责、后续消息发给谁”。

在这里，`Task` 不是 goroutine，也不等同于后台任务记录。它表示一次有明确起点和终点的逻辑执行。

理解整个模块只需要先记住四个概念：

| 概念 | 大白话解释 |
|---|---|
| `TaskID` | 这一次工作的编号。一次执行结束后，这个编号也结束。 |
| `ChildSessionID` | Sub-agent 的长期对话编号。一个对话可以先后产生多个 Task。 |
| `Mailbox` | Task 的持久收件箱。新输入先写入这里，再由当前执行者消费。 |
| owner | 当前负责推进 Task 生命周期的一方，可以是 parent，也可以是后台 `Manager`。 |

它们的关系是：

```text
一个 ChildSession
    ├── Task A（已经完成）
    ├── Task B（已经完成）
    └── Task C（当前活跃）
            └── Mailbox（属于 Task C）
```

一个 `ChildSessionID` 同时最多有一个未结束的 Task。Task 结束后，可以在同一个 ChildSession 中创建新的 Task，继续使用原来的对话历史。

***

## 模块结构

| 包 | 职责 |
|---|---|
| `adk/task` | 定义通用 Task 身份、Handle、输入、Mailbox 和嵌套执行 authority。 |
| `adk/task/background` | 管理可持久化、可恢复的后台生命周期。 |
| `adk/task/subagent` | 用 TurnLoop 执行可持续对话的 Sub-agent Task。 |
| `adk/task/local` | 把当前进程中的闭包包装成可前台运行、可转后台观察的 Task。 |
| `adk/task/tool` | 把外部工具适配为支持恢复、等待输入和 task-first projection 的 Task。 |
| `adk/task/shell` | 把可恢复 shell 实现适配为 managed tool。 |
| `adk/middlewares/task` | 向 Agent 注入 `task_output` 和 `task_stop`。 |

整体调用关系：

```text
用户 / Agent
    |
    +-- task.Handle ------------------------+
    |                                      |
    +-- subagent.Controller                |
    +-- local.Runner                       |
    +-- tool.NewManagedTool                |
                                           v
                                  durable Mailbox
                                           |
                     +---------------------+---------------------+
                     |                                           |
              parent-owned execution                    background.Manager
                     |                                           |
                     +--------------- handoff -------------------+
```

***

## 生命周期

### Foreground Task

Foreground 表示当前 parent 调用仍然负责等待和推进执行：

```text
Register mailbox
      |
      v
Run in parent process
      |
      +-- complete ------> seal mailbox
      |
      +-- fail/cancel ---> abandon mailbox
      |
      +-- wait longer ---> handoff to Manager
```

Foreground 阶段不会提前创建后台生命周期记录。这样 parent 和 `Manager` 不会同时认为自己拥有同一个 Task。

### Background Task

后台生命周期由 `background.Manager` 和 `LifecycleStore` 管理：

```text
Pending -> Running -> Completed
                   -> Failed
                   -> Canceled
                   -> WaitingInput -- new input --> Pending
                   -> Suspended ---- release/new input --> Pending
                   -> Yield ---------------------> Pending
```

`WaitingInput` 和 `Suspended` 都必须保存 checkpoint。两者的区别是：

- `WaitingInput`：缺少业务输入，收到输入后自动重新调度。
- `Suspended`：主动让出执行权，可由 `ReleaseSuspension` 或新输入重新激活。
- `Yield`：当前 worker 放弃 attempt，但逻辑操作仍存在，后续 worker 可重新 claim。

### Handoff

Handoff 是 owner 转移，不是创建新 Task：

```text
TaskID 不变
Mailbox 不变
ChildSessionID 不变
owner: parent -> Manager
Generation: n -> n + 1
```

`Generation` 用于阻止旧 owner 在 handoff 后继续写入。

### Task-first foreground projection

允许自动后台化的 Local/Managed Tool 不使用上述 owner transfer。它们从开始就由
`background.Manager` 执行，foreground 只是调用方观察同一个 Task 的临时窗口：

```text
Manager-owned Task execution
      |
      +-- terminal before timeout -> return ordinary foreground result
      |
      +-- timeout/caller abort ----> publish Task and close projection
                                      execution continues unchanged
```

这里必须区分：

- foreground/background 是调用方是否仍在观察；
- Parent/Manager 是谁拥有 execution lifecycle；
- Deferred/OnCreate/OnBackground 是 Task 何时对 parent 可见。

因此，支持自动后台化不等于先由 Parent 执行再 handoff。Local、Managed Tool 以及
未来的 auto-background Sub-agent 都应使用 task-first projection。

***

## `adk/task` API

### 概述

普通调用者通常只需要：

- `Handle`
- `Input`
- `Outcome`

Mailbox 和 `ExecutionContext` 主要面向 runtime 或存储实现者。

### 核心类型

#### `Handle`

`Handle` 是与当前 owner 无关的 Task 操作入口。

```go
type Handle interface {
	ID() string
	SendInput(context.Context, *Input) error
	Wait(context.Context) (*Outcome, error)
	Cancel(context.Context, string) error
}
```

**行为：**

- handoff 前后使用同一个 `TaskID`。
- `Wait` 返回业务结果；context 超时、存储失败等基础设施错误通过 `error` 返回。
- `Cancel` 请求取消整个逻辑 Task，不只是停止本次等待。

#### `StartMode`

`StartMode` 只决定 Task 刚启动时由谁负责。

```go
type StartMode uint8

const (
	StartModeForeground StartMode = iota
	StartModeBackground
)
```

它不是 Task 的永久属性。Foreground Task 可以 handoff 给后台 Manager。

#### `Owner`

`Owner` 表示执行当前代码时真正持有 lifecycle authority 的一方。

```go
type Owner uint8

const (
	OwnerParent Owner = iota
	OwnerManager
)
```

不要用 `StartMode` 推断当前 owner；发生 handoff 后两者可能不同。

#### `Outcome`

`Outcome` 是跨领域统一的等待结果。

```go
type Outcome struct {
	Status OutcomeStatus
	Data   []byte
	Error  string
}
```

#### `OutcomeStatus`

```go
type OutcomeStatus uint8

const (
	OutcomeUnknown OutcomeStatus = iota
	OutcomeCompleted
	OutcomeInterrupted
	OutcomeFailed
	OutcomeCanceled
)
```

**状态说明：**

- `OutcomeCompleted`：成功，结果位于 `Data`。
- `OutcomeInterrupted`：当前需要外部输入，不是终态。
- `OutcomeFailed`：业务执行失败，原因位于 `Error`。
- `OutcomeCanceled`：Task 被取消，原因可以位于 `Error`。
- `OutcomeUnknown`：无效零值，不应由合法 Handle 返回。

#### `Input`

`Input` 是调用方准备发送的内容，不包含存储生成的信息。

```go
type Input struct {
	EventID  string
	Kind     string
	Data     []byte
	Delivery InputDelivery
}
```

- `EventID`：Task 内的幂等键。
- `Kind`：由具体领域解释的输入类型。
- `Data`：领域数据。
- `Delivery`：排队或抢占意图。

#### `InputRecord`

`InputRecord` 是 `Input` 写入 Mailbox 后的持久化记录。

```go
type InputRecord struct {
	TaskID   string
	Sequence int64
	Input
	CreatedAt time.Time
}
```

调用方发送 `Input`，Store 返回 `InputRecord`。调用方不应自行构造 `TaskID`、`Sequence` 或 `CreatedAt`。

#### `InputDelivery`

```go
type InputDelivery uint8

const (
	InputQueued InputDelivery = iota
	InputPreempt
)
```

- `InputQueued`：在下一个 turn boundary 消费。
- `InputPreempt`：请求在 runtime 认可的安全点抢占当前 turn。

`InputPreempt` 是意图，不保证立刻中断。最终安全点由具体 runtime 决定。

#### `ExecutionContext`

`ExecutionContext` 是创建嵌套 Task 时继承的 authority。

```go
type ExecutionContext struct {
	TaskID        string
	Owner         Owner
	Generation    int64
	Attempt       int64
	RootSessionID string
}
```

- `TaskID`：直接父 Task。
- `Owner`：当前执行由 parent 还是 Manager 持有。
- `Generation`：Mailbox owner fence。
- `Attempt`：后台 worker attempt；parent owner 时为 `0`。
- `RootSessionID`：整棵 Task 树所属的根 Session。

#### `Mailbox`

Mailbox 只负责通信和路由，不保存执行结果或生命周期状态。

```go
type Mailbox struct {
	TaskID         string
	InvocationID   string
	Identity       []byte
	ParentTaskID   string
	RootSessionID  string
	ChildSessionID string
	State          MailboxState
	Generation     int64
	LatestSequence int64
	ConsumedCursor int64
}
```

#### `MailboxState`

```go
const (
	MailboxForeground MailboxState = "foreground"
	MailboxBackground MailboxState = "background"
	MailboxSealed     MailboxState = "sealed"
)
```

`MailboxState` 表示谁可以消费输入，或者 Mailbox 是否已经关闭；它不是后台 Task 的 lifecycle status。

### 函数

#### `WithExecutionContext`

把当前 Task authority 放入 `context.Context`，供嵌套 Task 创建时继承。

```go
func WithExecutionContext(
	ctx context.Context,
	execution ExecutionContext,
) context.Context
```

#### `ExecutionContextFromContext`

读取当前 Task authority。

```go
func ExecutionContextFromContext(
	ctx context.Context,
) (ExecutionContext, bool)
```

### 方法

#### `InputClient.SendInput`

只有 TaskID、没有 Handle 时，通过窄接口发送持久输入。

```go
func (c *InputClient) SendInput(
	ctx context.Context,
	taskID string,
	input *Input,
) (*SendInputResult, error)
```

### Mailbox SPI

#### `InputSender`

最小发送能力，适合只需要投递输入的组件。

```go
type InputSender interface {
	SendInput(context.Context, *SendInputRequest) (*SendInputResult, error)
}
```

#### `MailboxStore`

完整的持久通信 SPI。

```go
type MailboxStore interface {
	InputSender
	Register(context.Context, *RegisterMailboxRequest) (*RegisterMailboxResult, error)
	GetMailbox(context.Context, string) (*Mailbox, error)
	GetActiveMailboxBySession(context.Context, string) (*Mailbox, error)
	ListInputs(context.Context, *ListInputsRequest) (*ListInputsResult, error)
	WaitInputs(context.Context, *WaitInputsRequest) (*ListInputsResult, error)
	AdvanceCursor(context.Context, *AdvanceCursorRequest) error
	SealIfIdle(context.Context, *SealMailboxRequest) (*Mailbox, error)
	Abandon(context.Context, *AbandonMailboxRequest) (*Mailbox, error)
	ListChildren(context.Context, *ListChildrenRequest) (*ListChildrenResult, error)
}
```

这是存储实现接口，不是普通业务调用入口。

#### `RegisterMailboxRequest`

```go
type RegisterMailboxRequest struct {
	CandidateTaskID string
	InvocationID    string
	Identity        []byte
	RootSessionID   string
	ChildSessionID  string
	ParentExecution *ExecutionContext
}
```

**Parent scope 规则：**

- root Task：设置 `RootSessionID`，`ParentExecution` 留空。
- nested Task：只设置 `ParentExecution`。
- nested Task 的 `ParentTaskID` 和 `RootSessionID` 由 Store 从父 Mailbox 派生。
- 同时设置 `RootSessionID` 和 `ParentExecution` 是非法请求。

#### Mailbox 请求与结果

| 类型 | 用途 |
|---|---|
| `RegisterMailboxResult` | 返回规范化后的 Mailbox，并说明是否首次创建。 |
| `SendInputRequest` | 指定目标 TaskID 和待发送的 `Input`。 |
| `SendInputResult` | 返回持久化 `InputRecord` 和幂等插入结果。 |
| `ListInputsRequest` / `ListInputsResult` | 按 sequence 分页读取输入。 |
| `WaitInputsRequest` | 等待指定 sequence 之后出现输入。 |
| `AdvanceCursorRequest` | 在 generation/attempt fence 下推进消费位置。 |
| `SealMailboxRequest` | 仅在没有待处理输入时封存 foreground Mailbox。 |
| `AbandonMailboxRequest` | foreground 失败或取消时封存并丢弃剩余输入。 |
| `ListChildrenRequest` / `ListChildrenResult` | 分页列出直接子 Task。 |

### 哨兵错误

| 错误 | 含义 |
|---|---|
| `ErrMailboxStoreRequired` | 缺少 Mailbox 存储能力。 |
| `ErrMailboxNotFound` | TaskID 或 ChildSessionID 没有对应 Mailbox。 |
| `ErrMailboxIdentityConflict` | 同一 invocation 被不同身份数据重放。 |
| `ErrMailboxSealed` | 向已结束 Task 发送输入。 |
| `ErrInputRequired` | 输入为空。 |
| `ErrInputConflict` | 同一 EventID 被不同内容重复使用。 |
| `ErrInputsPending` | 完成或暂停时出现了尚未消费的新输入。 |
| `ErrCursorConflict` | 消费者使用了过期 cursor。 |
| `ErrOwnershipLost` | owner generation 或 attempt 已失效。 |
| `ErrSessionBusy` | 同一个 ChildSession 已有活跃 Task。 |

***

## `adk/task/foreground` API

该包只定义宿主配置 foreground projection 时需要公开引用的类型：

```go
type ShouldAutoBackground func(
	context.Context,
	*CandidateInfo,
) bool

type ShouldCancelOnCallerAbort func(
	context.Context,
	*CallerAbortInfo,
) bool
```

`ShouldCancelOnCallerAbort` 为空或返回 false 时，调用断开只会 detach
foreground projection，Task 继续运行；返回 true 时才请求 Task cancellation。

Task 启动、timer、terminal watcher 和 publish 竞态由
`adk/internal/taskfirst` 实现，不属于公开 API。

***

## `adk/task/background` API

### 概述

`background.Manager` 是后台 owner。它负责：

1. 把可序列化的 `Spec` 交给 Store。
2. 根据 `ExecutorKey` 找到本机 Executor。
3. claim Task 并建立 attempt/lease。
4. 将 Executor 返回的 action 原子提交到 Store。
5. 在 worker 丢失后重新调度可恢复 Task。

### 核心类型

#### `Manager`

```go
type Manager struct {
	// unexported fields
}

func New(context.Context, *Config) (*Manager, error)
```

一个 Manager 可以承载 Sub-agent、shell、managed tool 等多种 Task，它们共享同一 TaskID 空间。

#### `Config`

```go
type Config struct {
	Tasks                LifecycleStore
	TaskEvents           TaskEventStore
	SendTaskCreatedEvent func(context.Context, *TaskSnapshot) error
	IDGen                IDGenerator
	ContextSnapshotter   ContextSnapshotter
}
```

- `Tasks`：生命周期、Mailbox 和通知原子性的权威 Store。
- `TaskEvents`：追加式进度事件 Store。
- `SendTaskCreatedEvent`：向活跃 parent Runner 发送低延迟创建事件。
- `IDGen`：自定义完整 TaskID。
- `ContextSnapshotter`：保存跨 worker 恢复所需的 context 值。

#### `Spec`

```go
type Spec struct {
	ID            string
	ExecutorKey   string
	Kind          string
	Payload       []byte
	Description   string
	OutputFile    string
	ParentTaskID  string
	RootSessionID string
	NotifySession bool
}
```

`Spec` 是不可变执行意图。`Payload` 由 Executor 解释，Manager 不理解其中内容。

#### `TaskSnapshot`

`TaskSnapshot` 是后台生命周期记录的独立快照，包含：

- `Spec`
- `Publication`
- `Status`
- `Checkpoint`
- `ResultData` / `ResultError`
- `Version` / `Attempt`
- cancel intent
- 创建、更新时间和完成时间

修改返回的切片或时间指针不得影响 Store 中的数据。

#### `Publication`

```go
const (
	PublicationDeferred     Publication = "deferred"
	PublicationOnCreate     Publication = "on_create"
	PublicationOnBackground Publication = "on_background"
)
```

- `Deferred`：Task 已存在且可执行，但尚未对 parent 发布 lifecycle notification。
- `OnCreate`：显式后台 Task 在创建时发布。
- `OnBackground`：foreground projection detach 时通过 `Manager.Publish` 发布。

Publication 不推进 lifecycle `Version`，因此不会让正在运行的 attempt 失去 CAS
authority。

#### `Status`

```go
const (
	StatusPending      Status = "pending"
	StatusRunning      Status = "running"
	StatusWaitingInput Status = "waiting_input"
	StatusSuspended    Status = "suspended"
	StatusCompleted    Status = "completed"
	StatusFailed       Status = "failed"
	StatusCanceled     Status = "canceled"
)
```

#### `Executor`

Executor 是具体领域在后台 worker 中的恢复和执行实现。

```go
type Executor interface {
	Key() string
	LeaseExpiryPolicy() LeaseExpiryPolicy
	ValidateSpec(Spec) error
	ValidateExecution(context.Context, *TaskSnapshot) error
	SupportsDrain() bool
	Execute(context.Context, *TaskSnapshot, ExecutionRuntime) (*ExecutionResult, error)
}
```

#### `ExecutionResult`

```go
type ExecutionResult struct {
	Action      ExecutionAction
	Checkpoint  []byte
	Data        []byte
	Error       string
	InputCursor int64
}
```

`Action` 是唯一判别字段：

| Action | 合法载荷 |
|---|---|
| `ExecutionActionComplete` | `Data`、`InputCursor` |
| `ExecutionActionFail` | `Error` |
| `ExecutionActionCancel` | 可选 `Error` |
| `ExecutionActionWaitInput` | `Checkpoint`、`InputCursor` |
| `ExecutionActionSuspend` | `Checkpoint`、`InputCursor` |
| `ExecutionActionYield` | 可选 `Checkpoint` |

不要组合多个 action 的字段。

#### `ExecutionRuntime`

```go
type ExecutionRuntime interface {
	Controls() <-chan ControlRequest
	NewTaskEventWriter(string) (TaskEventScope, TaskEventWriter)
	ReportTranscriptFailure(context.Context, error) error
	ListInputs(context.Context, int64, int) (*task.ListInputsResult, error)
	WaitInputs(context.Context, int64) (*task.ListInputsResult, error)
	AdvanceInputCursor(context.Context, int64, int64) error
	CommitInput(context.Context, int64, int64, []byte) error
	CommitStart(context.Context, []byte) error
}
```

它是 attempt-scoped 能力。Executor 不应自行拼接 version、generation 或 lease token。
`NewTaskEventWriter` 生成或绑定 logical EventID，返回的 writer 在每次 part 写入时
重新校验 attempt authority。

原始事件和 stream 不在这里提前变成 `[]byte`。Executor 使用泛型 persister：

```go
type TaskEventEnvelope[E, Chunk any] struct {
	Event  E
	Stream *schema.StreamReader[Chunk]
}

type TaskEventPersister[E, Chunk any] interface {
	Persist(
		context.Context,
		TaskEventScope,
		*TaskEventEnvelope[E, Chunk],
		TaskEventWriter,
	) ([]*AppendTaskEventResult, error)
}
```

`Stream` 必须是 persistence-owned copy。Persister 自行序列化 Event、消费并关闭
stream，再通过 `TaskEventWriter.Append` 写入一个或多个 durable part。live
projection 必须使用另一份 stream copy。

### Manager 方法

#### `Manager.Submit`

```go
func (m *Manager) Submit(
	ctx context.Context,
	req *SubmitRequest,
) (*TaskSnapshot, error)
```

持久化一个 Pending Task。`SubmitRequest.Publication` 默认为
`PublicationOnCreate`；设置为 `PublicationDeferred` 时 Task 可以执行，但在
`Publish` 前不会发送 lifecycle notification。嵌套调用会从
`ExecutionContext` 自动继承 parent 和 root scope。

#### `Manager.Publish`

```go
func (m *Manager) Publish(
	ctx context.Context,
	taskID string,
) (*TaskSnapshot, error)
```

原子执行唯一合法的 publication transition：
`PublicationDeferred -> PublicationOnBackground`。目标状态由方法语义固定，不由
调用方传入。若 terminal commit 先完成，返回 `ErrAlreadyTerminal`，Task 保持
Deferred。

#### `Manager.Execute`

```go
func (m *Manager) Execute(ctx context.Context, taskID string) error
```

claim 并执行一次 pending attempt。它不是“创建 Task”的接口。

#### `Manager.Handle`

```go
func (m *Manager) Handle(taskID string) (*Handle, error)
```

按 TaskID 创建通用 Handle。该 Handle 适合已经存在后台记录的 Task。

#### `Manager.Get`

```go
func (m *Manager) Get(
	ctx context.Context,
	taskID string,
) (*TaskSnapshot, error)
```

读取后台生命周期的权威快照。

#### `Manager.RequestCancel`

```go
func (m *Manager) RequestCancel(
	ctx context.Context,
	taskID string,
	options ...RequestCancelOption,
) (*TaskSnapshot, error)
```

持久化取消意图，并通知当前进程中的活跃 attempt。

#### `Manager.ReleaseSuspension`

```go
func (m *Manager) ReleaseSuspension(
	ctx context.Context,
	taskID string,
) (*TaskSnapshot, error)
```

把 `Suspended` Task 放回 `Pending`。

#### `Manager.Close`

```go
func (m *Manager) Close(
	ctx context.Context,
	options ...CloseOption,
) error
```

有活跃 attempt 时必须传入带 deadline 的 context。

#### 查询和 runtime 方法

| 方法 | 用途 |
|---|---|
| `AllocateTaskID` | 在持久化前分配稳定 TaskID。 |
| `ListPending` | worker 获取可 claim 的 Task。 |
| `ListSuspended` | 运维或调度器查看暂停 Task。 |
| `WaitForTaskVersion` | 等待生命周期版本变化。 |
| `Publish` | 把 Deferred Task 原子发布为 OnBackground。 |
| `ListTaskEvents` | 查询追加式进度。 |
| `RegisterMailbox` | 创建或幂等重放 foreground Mailbox。 |
| `GetMailbox` | 读取 Mailbox。 |
| `GetActiveMailboxBySession` | 找到 ChildSession 当前活跃 Task。 |
| `SendInput` | 向任意 owner 发送输入。 |
| `ListInputs` / `WaitInputs` | runtime 消费输入。 |
| `AdvanceInputCursor` | 提交消费位置。 |
| `SealMailbox` / `AbandonMailbox` | 结束 foreground Mailbox。 |
| `ListChildren` | 查询直接子 Task。 |
| `AdoptForeground` | 把 foreground ownership 原子转给 Manager。 |

### Store SPI

#### `TaskStore`

持久化后台生命周期、publication、lease、attempt、checkpoint 和 terminal
result。`PublicationDeferred` 的 Task 可执行但不发送 lifecycle notification；
`Publish` 原子切换为 `PublicationOnBackground`。

#### `LifecycleStore`

```go
type LifecycleStore interface {
	TaskStore
	task.MailboxStore
	NotificationWriter
	AdoptForeground(context.Context, *AdoptForegroundStoreRequest) (*TaskSnapshot, error)
	CommitInput(context.Context, *CommitInputRequest) (*TaskSnapshot, error)
	WaitInputIfNoInputs(context.Context, *WaitInputIfNoInputsRequest) (*TaskSnapshot, error)
	SuspendIfNoInputs(context.Context, *SuspendIfNoInputsRequest) (*TaskSnapshot, error)
	CompleteIfNoInputs(context.Context, *CompleteIfNoInputsRequest) (*TaskSnapshot, error)
}
```

这些方法必须原子检查 Mailbox cursor，再提交 lifecycle 状态，避免输入和完成/暂停互相覆盖。

#### `TaskEventStore`

保存 append-only event parts：

- `EventID` 标识一个 logical event。
- `PartID` 标识该 event 中可幂等重放的一部分。
- `(TaskID, EventID, PartID)` 相同且内容相同是幂等 replay。
- 同 key 不同内容返回 `ErrTaskEventPartConflict`。
- `Final=true` 后拒绝该 EventID 的新 part。
- 每次 part append 都必须重新校验 active attempt，不能在整个 stream 期间持有
  一次性 authorization。

Store 只负责 durable bytes 和 fencing；typed event 序列化及 stream 处理由
executor-specific `TaskEventPersister` 完成。

#### `NotificationWriter`

由当前有效 attempt 向直接 parent Task 或根 Session 写入通知。

#### `NotificationOutbox`

为跨进程通知提供 lease、重试和 acknowledgement。

#### Store 请求类型

| 类型 | 作用 |
|---|---|
| `CreateTaskRequest` | 创建 Pending 记录。 |
| `StartTaskRequest` | claim 新 attempt。 |
| `HeartbeatRequest` | 续租当前 attempt。 |
| `CommitStartRequest` | 提交外部操作已经建立的边界。 |
| `CommitInputRequest` | 原子提交输入 cursor 和恢复 checkpoint。 |
| `CompleteIfNoInputsRequest` | Mailbox 空闲时完成。 |
| `WaitInputIfNoInputsRequest` | Mailbox 空闲时进入等待输入。 |
| `SuspendIfNoInputsRequest` | Mailbox 空闲时暂停。 |
| `YieldTaskRequest` | 放弃当前 attempt，回到 Pending。 |
| `FailTaskRequest` | 提交失败。 |
| `RequestCancelRequest` | 写入取消意图。 |
| `AckCancelRequest` | attempt 完成取消清理后确认终态。 |
| `ReleaseSuspensionRequest` | 释放暂停。 |
| `WaitForTaskVersionRequest` | 等待版本推进。 |

### Notification

```go
const (
	NotificationTaskCreated      NotificationKind = "task_created"
	NotificationTaskBackgrounded NotificationKind = "task_backgrounded"
	NotificationWaitingInput     NotificationKind = "waiting_input"
	NotificationCompleted        NotificationKind = "completed"
	NotificationFailed           NotificationKind = "failed"
	NotificationCanceled         NotificationKind = "canceled"
)
```

嵌套 Task 的通知进入直接 parent Mailbox；只有根 Task 的 lifecycle 通知进入 Session outbox。

`NotifyParent` 允许 Executor 发送自定义应用通知：

```go
func NotifyParent(
	ctx context.Context,
	req *NotifyParentRequest,
) error
```

该函数只能在 Manager 授权的 attempt context 中调用。

***

## `adk/task/subagent` API

### 概述

`Controller` 把 durable Mailbox、TurnLoop、SessionStore 和 background Manager 组合成统一的 Sub-agent runtime。

Foreground 与 Background 使用同一套执行逻辑，区别只有初始 owner：

```text
StartModeForeground -> parent owns -> complete or handoff
StartModeBackground -> Manager owns immediately
```

### 核心类型

#### `Controller`

```go
func NewController[M adk.MessageType](
	config *ControllerConfig[M],
) (*Controller[M], error)
```

一个 Manager 只能绑定一个 Sub-agent Controller executor。

#### `ControllerConfig`

```go
type ControllerConfig[M adk.MessageType] struct {
	Manager            *background.Manager
	Barrier            CompletionBarrier[M]
	InputsToAgentInput  InputsToAgentInput[M]
	CancellationHook   CancellationHook
	InputPreemptPolicy InputPreemptPolicy[M]
	SessionStore        adk.SessionEventStore[M]
	SessionStoreFactory RuntimeSessionStoreFactory[M]
	CheckPointStore     adk.CheckPointStore
	SessionConfig       *adk.SessionConfig[M]
	InputBatchSize      int
}
```

`SessionStore` 和 `SessionStoreFactory` 必须且只能配置一个。

#### `Handle`

Sub-agent Handle 除了实现 `task.Handle`，还提供：

```go
func (h *Handle) ChildSessionID() string
```

Handle 的字段是私有的。需要从持久化 TaskID 恢复操作能力时，应调用 `Controller.Handle`，不要序列化 Handle 本身。

#### `StartRequest`

```go
type StartRequest[M adk.MessageType] struct {
	InvocationID    string
	ParentSessionID string
	ChildSessionID  string
	AgentName       string
	Description     string
	Input           *adk.TypedAgentInput[M]
	StartMode       task.StartMode
	EnableStreaming bool
	OnEvent         func(*adk.TypedAgentEvent[M])
}
```

`ChildSessionID` 为空时，会根据 `InvocationID` 生成稳定 ID。

#### `ContinueRequest`

```go
type ContinueRequest[M adk.MessageType] struct {
	ChildSessionID string
	InvocationID   string
	Input          *adk.TypedAgentInput[M]
	Delivery       task.InputDelivery
	IfIdle         *StartOptions[M]
}
```

- ChildSession 有 active Task：输入发给该 Task。
- ChildSession 空闲且 `IfIdle != nil`：创建新 Task。
- ChildSession 空闲且没有 `IfIdle`：返回 `task.ErrMailboxNotFound`。

#### `CompletionAction`

```go
const (
	CompletionComplete CompletionAction = iota
	CompletionWaitInput
)
```

- `CompletionComplete`：本次 Task 结束并 seal Mailbox。
- `CompletionWaitInput`：保持 ChildSession 活跃；foreground 会 handoff 给 Manager。

#### `CompletionBarrier`

```go
type CompletionBarrier[M adk.MessageType] interface {
	Check(
		context.Context,
		*CompletionContext[M],
	) (CompletionAction, error)
}
```

#### `CancellationHook`

```go
type CancellationHook interface {
	OnCancel(
		ctx context.Context,
		taskID string,
		childSessionID string,
		reason string,
	) error
}
```

#### `InputsToAgentInput`

```go
type InputsToAgentInput[M adk.MessageType] func(
	context.Context,
	[]*task.InputRecord,
) (*adk.TypedAgentInput[M], error)
```

它负责把领域输入合并成下一轮 Agent input。Runner resume 输入由 Controller 单独处理，不会传给它。

#### `InputPreemptPolicy`

把 `InputPreempt` 映射为 TurnLoop safe-point 策略。返回 nil 会安全降级为 queued delivery。

### 方法

#### `Controller.Start`

创建一个新 Task，可以 foreground 或 background 启动。

```go
func (r *Controller[M]) Start(
	ctx context.Context,
	req *StartRequest[M],
) (*Handle, error)
```

#### `Controller.Continue`

按 `ChildSessionID` 继续当前 Task，或者在空闲时创建新 Task。

```go
func (r *Controller[M]) Continue(
	ctx context.Context,
	req *ContinueRequest[M],
) (*Handle, error)
```

#### `Controller.Handle`

```go
func (r *Controller[M]) Handle(
	ctx context.Context,
	taskID string,
) (*Handle, error)
```

从 durable Mailbox 恢复一个绑定当前 Controller 的 Handle。

#### `Controller.Wait`

```go
func (r *Controller[M]) Wait(
	ctx context.Context,
	taskID string,
) (*Result[M], error)
```

返回带类型的最终消息或 `adk.InterruptInfo`。只需要通用状态时使用 `Handle.Wait`。

#### 其他方法

| 方法 | 用途 |
|---|---|
| `RegisterAgent` | 注册稳定 AgentName 对应的 resumable Agent。 |
| `SendInput` | 按 TaskID 发送领域输入。 |
| `Cancel` | 按 TaskID 取消执行。 |
| `ReadProgress` | 从 ChildSession 投影当前 Task 的有限 transcript。 |
| `Manager` | 返回 Controller 绑定的共享 Manager。 |

### Runtime context

```go
func TaskID(ctx context.Context) (string, bool)
func ChildSessionID(ctx context.Context) (string, bool)
```

Sub-agent 内部启动 nested Task 时，可以读取当前 Task 和 ChildSession 身份。通常无需直接调用 `WithRuntimeContext`；Controller 会自动设置。

***

## `adk/task/local` API

### 概述

`local.Runner` 用于不能序列化、不能跨进程恢复的 Go 闭包。

```go
type WorkFunc func(
	ctx context.Context,
	runtime background.ExecutionRuntime,
) (string, error)

type StreamWorkFunc func(
	ctx context.Context,
	runtime background.ExecutionRuntime,
) (*schema.StreamReader[string], error)
```

`local.Config.EventPersister` 可以替换默认的 UTF-8 chunk 序列化。Persister
接收原始 string event；streaming work 的每个输出 chunk 是一个独立 logical
event。

#### `Runner.Run`

执行 buffered work。配置 auto-background 时，work 从开始就在 Manager attempt
context 中执行；如果 foreground timeout 或调用方断开，只改变观察和 publication。

#### `Runner.RunStream`

执行 streaming work。Foreground chunk 只是 Manager-owned Task 的临时投影；
detach 后调用方收到后台通知，底层 work 不重启，持久进度继续由 TaskEvent 管理。

`local` 不承诺跨进程恢复，因为闭包只存在于当前进程。

***

## `adk/task/tool` API

### 概述

该包用于把“外部长期操作”接入 Task runtime，例如远程命令、异步作业或需要人工确认的工具。

能力是逐层增加的：

```text
Tool
  +-- RecoverableTool
        +-- ResumableTool
```

#### `Tool`

```go
type Tool interface {
	ValidateArguments(arguments string) error
	Start(context.Context, *StartRequest) (*StartResult, error)
}
```

#### `RecoverableTool`

Worker 丢失后通过 TaskID 和 checkpoint 恢复同一个外部操作。

#### `ResumableTool`

在 `OutcomeInterrupted` 后接收 durable input，并继续原来的逻辑操作。

`tool.Registration.EventPersister` 可以替换默认 JSON `Update` 序列化。自定义
persister 与对应的 `ProgressReader` 应使用同一 durable record 格式。

#### `Run`

```go
type Run interface {
	Wait(context.Context) (*Outcome, error)
	Stop(context.Context) error
}
```

#### `Outcome`

```go
type Outcome struct {
	Status       task.OutcomeStatus
	Data         []byte
	Error        string
	InputRequest *InputRequest
	Checkpoint   []byte
}
```

合法组合：

| Status | 字段 |
|---|---|
| `task.OutcomeCompleted` | `Data` |
| `task.OutcomeFailed` | 非空 `Error` |
| `task.OutcomeCanceled` | 可选 `Error` |
| `task.OutcomeInterrupted` | 非空 `InputRequest`，可选 `Checkpoint` |

#### `Registry`

保存 model-facing tool name 到实现的映射。`NewManagedTool` 和 `Submit` 会自动把所需 Executor 安装到 Manager。

#### `NewManagedTool`

创建支持 direct foreground、显式 background、task-first auto-background、
streaming projection 和 waiting input 的工具包装。

#### `Submit`

直接提交已注册工具为后台 Task，不经过 model-facing wrapper。

***

## `adk/task/shell` API

### 概述

`shell` 是 `task/tool` 的薄适配层。

```go
type RecoverableShell interface {
	StartCommand(context.Context, *StartCommandRequest) (tool.Run, error)
	RecoverCommand(context.Context, *RecoverCommandRequest) (tool.Run, error)
}
```

通过 `NewRegistration` 可将实现注册到 `tool.Registry`。远端 shell 必须使用 TaskID 保证启动幂等。

***

## Middleware API

### `adk/middlewares/task`

该 middleware 是 `task_output` 和 `task_stop` 的唯一注入方。

```go
type TypedConfig[M adk.MessageType] struct {
	Manager                      *background.Manager
	ProgressReadersByExecutorKey map[string]ProgressReader
	TaskOutputToolConfig         *ToolConfig
	TaskStopToolConfig           *ToolConfig
	CustomSystemPrompt           func(context.Context, *SystemPromptInput) string
}
```

Sub-agent、filesystem 等领域 middleware 只负责创建 Task，不应重复注入控制工具。

### `adk/middlewares/subagent`

```go
type TypedTaskConfig[M adk.MessageType] struct {
	Local            *TypedLocalTaskConfig[M]
	Durable          *TypedDurableTaskConfig[M]
	TranscriptFormat TranscriptFormat[M]
}

type TypedLocalTaskConfig[M adk.MessageType] struct {
	Runner         *local.Runner
	OutputStore    filesystem.AppendOpener
	OutputDir      string
	EventPersister background.TaskEventPersister[*adk.TypedAgentEvent[M], M]
}

type TypedDurableTaskConfig[M adk.MessageType] struct {
	Runtime             *tasksubagent.Controller[M]
	RunOptionsFactories map[string]tasksubagent.RunOptionsFactory
}
```

`Local` 和 `Durable` 必须且只能配置一个。

Local Sub-agent 的 `EventPersister` 会收到不包含 reader 的原始 AgentEvent 元数据，
以及单独的 persistence stream copy。它可以自行决定 event/chunk 的序列化与
part 划分；live AgentEvent 使用另一份 copy。

### Filesystem middleware

- `LocalBackgroundConfig`：使用 `local.Runner`，仅支持当前进程。
- `RecoverableBackgroundConfig`：使用 `background.Manager`、`tool.Registry` 和 `RecoverableShell`，支持跨 worker 恢复。

***

## Deep Agent 集成

`deep.TypedTaskConfig` 把 Sub-agent、shell 和 task control middleware 放进同一个 TaskID 空间：

```go
type TypedTaskConfig[M adk.MessageType] struct {
	Manager              *background.Manager
	SubAgents            *TypedDurableSubAgentConfig[M]
	RecoverableShell     *RecoverableShellConfig
	LocalShell           *LocalShellConfig
	ForegroundTimeoutMs  *int
	ShouldAutoBackground func(context.Context, *foreground.CandidateInfo) bool
	TranscriptFormat     subagent.TranscriptFormat[M]
}
```

如果配置了 `SubAgents.Runtime`，可以省略 `Manager`，Deep Agent 会使用 Controller 已绑定的 Manager。如果同时提供二者，它们必须是同一个实例。

***

## 使用示例

### 最小可运行后台 Task

```go
package main

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/adk/task"
	"github.com/cloudwego/eino/adk/task/background"
)

type echoExecutor struct{}

func (echoExecutor) Key() string {
	return "example.echo"
}

func (echoExecutor) LeaseExpiryPolicy() background.LeaseExpiryPolicy {
	return background.LeaseExpiryFail
}

func (echoExecutor) ValidateSpec(background.Spec) error {
	return nil
}

func (echoExecutor) ValidateExecution(
	context.Context,
	*background.TaskSnapshot,
) error {
	return nil
}

func (echoExecutor) SupportsDrain() bool {
	return false
}

func (echoExecutor) Execute(
	_ context.Context,
	snapshot *background.TaskSnapshot,
	_ background.ExecutionRuntime,
) (*background.ExecutionResult, error) {
	return &background.ExecutionResult{
		Action: background.ExecutionActionComplete,
		Data:   snapshot.Spec.Payload,
	}, nil
}

func main() {
	ctx := context.Background()
	manager, err := background.New(ctx, nil)
	if err != nil {
		panic(err)
	}

	_, _, err = manager.LoadOrRegisterExecutor(echoExecutor{})
	if err != nil {
		panic(err)
	}
	taskID, err := manager.AllocateTaskID(
		ctx,
		&background.AllocateTaskIDRequest{Kind: "echo"},
	)
	if err != nil {
		panic(err)
	}
	snapshot, err := manager.Submit(ctx, &background.SubmitRequest{
		Spec: background.Spec{
			ID: taskID, ExecutorKey: "example.echo",
			Kind: "echo", Payload: []byte("hello"),
		},
	})
	if err != nil {
		panic(err)
	}
	go func() {
		_ = manager.Execute(ctx, snapshot.Spec.ID)
	}()

	handle, err := manager.Handle(snapshot.Spec.ID)
	if err != nil {
		panic(err)
	}
	outcome, err := handle.Wait(ctx)
	if err != nil {
		panic(err)
	}
	if outcome.Status != task.OutcomeCompleted {
		panic(outcome.Error)
	}
	fmt.Println(string(outcome.Data))
}
```

### 通过 Handle 发送输入

```go
handle, err := manager.Handle(taskID)
if err != nil {
	return err
}

err = handle.SendInput(ctx, &task.Input{
	EventID:  requestID,
	Kind:     "approval",
	Data:     []byte(`{"approved":true}`),
	Delivery: task.InputPreempt,
})
if err != nil {
	return err
}

outcome, err := handle.Wait(ctx)
```

### 启动并继续 Sub-agent

```go
handle, err := controller.Start(ctx, &subagent.StartRequest[*schema.Message]{
	InvocationID:    invocationID,
	ParentSessionID: parentSessionID,
	AgentName:       "researcher",
	Input: &adk.AgentInput{
		Messages: []*schema.Message{schema.UserMessage("research this topic")},
	},
	StartMode: task.StartModeForeground,
})
if err != nil {
	return err
}

next, err := controller.Continue(ctx, &subagent.ContinueRequest[*schema.Message]{
	ChildSessionID: handle.ChildSessionID(),
	InvocationID:   nextInvocationID,
	Input: &adk.AgentInput{
		Messages: []*schema.Message{schema.UserMessage("focus on reliability")},
	},
	Delivery: task.InputQueued,
	IfIdle: &subagent.StartOptions[*schema.Message]{
		ParentSessionID: parentSessionID,
		AgentName:       "researcher",
		StartMode:       task.StartModeForeground,
	},
})
```

### 编写可恢复 Tool

```go
func (r *remoteRun) Wait(ctx context.Context) (*tasktool.Outcome, error) {
	result, err := r.client.Wait(ctx, r.operationID)
	if err != nil {
		return nil, err
	}
	return &tasktool.Outcome{
		Status: task.OutcomeCompleted,
		Data:   result,
	}, nil
}
```

需要业务输入时：

```go
return &tasktool.Outcome{
	Status: task.OutcomeInterrupted,
	InputRequest: &tasktool.InputRequest{
		ID:   "approval",
		Data: json.RawMessage(`{"question":"approve deployment?"}`),
	},
	Checkpoint: checkpoint,
}, nil
```

***

## 最佳实践

1. **优先持有 Handle**：已有 Handle 时使用 `SendInput`、`Wait` 和 `Cancel`，不要重复拼装底层请求。
2. **区分 TaskID 和 ChildSessionID**：前者标识一次执行，后者标识可持续对话。
3. **不要缓存 owner**：Task 可以 handoff，owner 不是 Task 的永久属性。
4. **每次输入都提供稳定 EventID**：重试必须复用 EventID 和相同内容。
5. **把 InputPreempt 当作请求**：真正的抢占点由 runtime policy 决定。
6. **嵌套 Task 依赖 context**：不要自行传递或伪造 ParentTaskID、RootSessionID、Generation。
7. **只实现一个 LifecycleStore**：生命周期和 Mailbox transition 必须位于同一个原子事务边界。
8. **Executor 返回单一 Action**：不要组合其他 action 专属字段。
9. **外部操作按 TaskID 幂等**：worker 可能在外部操作启动后、checkpoint 提交前退出。
10. **不要序列化 Handle**：持久化 TaskID，需要时通过 Manager 或 Controller 恢复 Handle。
11. **共享一个 Manager**：Sub-agent、shell、managed tool 和 task middleware 必须使用同一个 Manager。
12. **只注入一次控制 middleware**：避免重复注册 `task_output` 和 `task_stop`。

***

## 选择指南

| 需求 | 推荐入口 |
|---|---|
| 已知 TaskID，发送输入或等待结果 | `task.Handle` |
| 启动持久 Sub-agent | `subagent.Controller.Start` |
| 继续某个 ChildSession | `subagent.Controller.Continue` |
| 当前进程内执行闭包 | `local.Runner` |
| 包装可恢复外部工具 | `tool.NewManagedTool` |
| 直接提交后台工具 | `tool.Submit` |
| 接入可恢复 shell | `shell.NewRegistration` |
| 给 Agent 增加 task 控制能力 | `middlewares/task` |
| 实现生产存储 | `background.LifecycleStore` + `storetest` |

最重要的判断标准是：**执行能否在当前进程消失后继续恢复**。如果不能，使用 `local.Runner`；如果能，就使用 `background.Manager` 和对应的可恢复 Executor。
