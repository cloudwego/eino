# Cancel 与 Resume 功能设计方案 V2

## 1. 需求概述

### 1.1 背景

TurnLoop 通过 Runner 调用 Agent 来实现 Agent Turn 功能。本次需求是为了支持 TurnLoop 的外部取消与恢复功能。

### 1.2 核心需求

| 功能 | 描述 | 触发场景 |
|------|------|----------|
| Cancel | 取消正在执行的 Agent | 用户取消、新消息插入、Pod 迁移等 |
| Resume | 从中断点恢复执行 | Cancel 后恢复、Interrupt 后恢复 |

### 1.3 设计决策

| 项目 | 决策 | 说明 |
|------|------|------|
| API 模式 | `RunWithCancel` 模式 | Run 直接返回 iterator 和 cancelFn |
| Checkpoint 策略 | Cancel 不绑定 Checkpoint | 是否保存取决于 Store + CheckpointID 配置 |
| 语义统一 | Cancel 统一表达外部停止 | `ExternalInterrupt = Cancel + Checkpoint 存储` |
| CancellableResume | 需要实现 | Resume 过程中也可以被 Cancel |

---

## 2. CancelMode 语义定义

### 2.1 三种取消模式

| CancelMode | 触发时机 | 说明 |
|------------|----------|------|
| `CancelImmediate` | 在任意检查点立刻退出 | 等同于 `CancelAfterChatModel \| CancelAfterToolCall` |
| `CancelAfterChatModel` | ChatModel **响应后**立刻退出 | 在 ChatModel 的 PostHandler 中检查 |
| `CancelAfterToolCall` | ToolsNode **完成后**立刻退出 | 在 ToolsNode 的 PostHandler 中检查 |

### 2.2 Timeout 语义

`Timeout` 表示**无论哪种 CancelMode，最多等待的时间长度**。超过该时间后，强制调用 `context.Cancel()` 退出，不管当前的运行状态。

---

## 3. 信号传递链路

### 3.1 Cancel 信号触发链路

```
TurnLoop (用户层)
    │
    ▼ MessageSource.Receive 收到 WithPreemptive 等 option
    │
Runner.RunWithCancel()
    │ 返回 (iter, cancelFn, err)
    │
    ▼ 内部调用 flowAgent.RunWithCancel()
          │ 返回 (iter, cancelFn)
          │
          ▼ 内部调用 Agent.RunWithCancel() (如 ChatModelAgent)
                │ 返回 (iter, cancelFn)
                │
                ▼ Cancel 信号通过 cancelContext 传递到内部执行
                    │
                    ▼ 在 PostHandler 检查点位检查信号
                        - CancelAfterChatModel: ChatModel 响应后检查
                        - CancelAfterToolCall: ToolsNode 完成后检查
                        - CancelImmediate: 在任意检查点检查
```

### 3.2 Resume 链路

```
TurnLoop / 用户代码
    │
    ▼ 携带 checkpointID
    │
Runner.ResumeWithCancel()
    │ 返回 (iter, cancelFn, err)
    │
    ▼ 内部加载 Checkpoint，调用 flowAgent.ResumeWithCancel()
          │
          ▼ 根据 ResumeInfo 定位到中断点
          │
          ▼ 调用 Agent.ResumeWithCancel()
                │
                ▼ 从中断状态恢复执行
```

---

## 4. 接口设计

### 4.1 核心接口

```go
// CancelMode 定义取消的时机点位
type CancelMode int

const (
    CancelImmediate      CancelMode = 0         // 立即取消（在任意检查点）
    CancelAfterChatModel CancelMode = 1 << iota // ChatModel 响应后取消
    CancelAfterToolCall                          // ToolsNode 完成后取消
)

// CancelFunc 取消函数签名
type CancelFunc func(context.Context, ...CancelOption) error

// CancellableRun 支持取消的 Run 接口
type CancellableRun interface {
    RunWithCancel(ctx context.Context, input *AgentInput, 
        options ...AgentRunOption) (*AsyncIterator[*AgentEvent], CancelFunc)
}

// CancellableResume 支持取消的 Resume 接口
type CancellableResume interface {
    ResumeWithCancel(ctx context.Context, info *ResumeInfo, 
        opts ...AgentRunOption) (*AsyncIterator[*AgentEvent], CancelFunc)
}
```

### 4.2 CancelOption

```go
func WithCancelMode(mode CancelMode) CancelOption
func WithCancelTimeout(timeout time.Duration) CancelOption
```
---

## 5. 关键设计要点

| 要点 | 说明 |
|------|------|
| 复用 compose.Graph 机制 | 通过 `compose.Interrupt` 触发 Graph 的 Checkpoint&Resume 机制 |
| CancelImmediate | 在任意检查点触发，等同于 `CancelAfterChatModel \| CancelAfterToolCall` |
| Timeout 语义 | 无论哪种模式，最多等待 timeout 时间，超时后强制 `context.Cancel()` |

---

## 7. 使用示例

### 7.1 基本 Cancel 使用

```go
runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})
iter, cancelFn, err := runner.RunWithCancel(ctx, messages)

go func() {
    <-userClickedStop
    cancelFn(ctx) // 立即取消（默认）
}()
```

### 7.2 定点取消

```go
// 等待当前 Tool 完成后取消，最多等待 10 秒
cancelFn(ctx, 
    adk.WithCancelMode(adk.CancelAfterToolCall),
    adk.WithCancelTimeout(10 * time.Second),
)
```

### 7.3 Cancel + Checkpoint（Pod 迁移场景）

```go
runner := adk.NewRunner(ctx, adk.RunnerConfig{
    Agent:           agent,
    CheckPointStore: store,
})

iter, cancelFn, _ := runner.RunWithCancel(ctx, messages, adk.WithCheckPointID("session-123"))

go func() {
    <-podMigrationSignal
    cancelFn(ctx, adk.WithCancelTimeout(30 * time.Second))
}()

// --- 新 Pod 上恢复 ---
iter, cancelFn, _ := runner.ResumeWithCancel(ctx, "session-123")
```

### 7.4 TurnLoop 中使用

```go
func (s *mySource) Receive(ctx context.Context, cfg adk.ReceiveConfig) (
    context.Context, Message, []adk.ConsumeOption, error) {
    
    msg := <-s.msgChan
    
    return ctx, msg, []adk.ConsumeOption{
        adk.WithPreemptive(),
        adk.WithCancelOptions(adk.WithCancelMode(adk.CancelAfterToolCall)),
    }, nil
}
```

---
