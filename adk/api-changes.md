# API Changes — AgenticMessage Support

本文档列出 PR #920 引入的所有新增和变更的 **exported API**。

> 设计原则：所有既有 `*schema.Message` API 保持完全向后兼容（通过 type alias）。新增 `Typed*` 泛型版本支持 `*schema.AgenticMessage`。

---

## 1. `components/model` 包

### 新增

| 符号 | 签名 | 说明 |
|------|------|------|
| `BaseModel[M]` | `interface { Generate(...); Stream(...) }` | 泛型基础模型接口，`M` 可为任意类型 |

### 变更（type alias，向后兼容）

| 原符号 | 现在是 | 说明 |
|--------|--------|------|
| `BaseChatModel` | `= BaseModel[*schema.Message]` | 原为独立 interface，现为 alias |
| `AgenticModel` | `= BaseModel[*schema.AgenticMessage]` | 原为独立 interface（含 `WithTools`），现为 alias（不含 `WithTools`） |

---

## 2. `schema` 包

### 新增

| 符号 | 说明 |
|------|------|
| `(*MCPListToolsItem).GobEncode() ([]byte, error)` | 支持 gob 序列化（用于 checkpoint） |
| `(*MCPListToolsItem).GobDecode([]byte) error` | 支持 gob 反序列化 |

---

## 3. `adk` 包 — 核心类型约束

### 新增

| 符号 | 说明 |
|------|------|
| `MessageType` | 类型约束 `*schema.Message \| *schema.AgenticMessage` |
| `ComponentOfAgenticAgent` | 常量 `components.Component("AgenticAgent")`，用于回调过滤 |

---

## 4. `adk` 包 — Agent 接口 & Runner

### 新增

| 符号 | 签名 | 说明 |
|------|------|------|
| `TypedAgent[M]` | `interface { Name; Description; Run(...) }` | 泛型 Agent 接口 |
| `TypedResumableAgent[M]` | `interface { TypedAgent[M]; Resume(...) }` | 泛型可恢复 Agent |
| `TypedOnSubAgents[M]` | `interface { OnSetSubAgents; OnSetAsSubAgent; ... }` | 泛型子 Agent 管理接口 |
| `TypedAgentInput[M]` | `struct { Messages []M; EnableStreaming bool }` | 泛型 Agent 输入 |
| `TypedAgentEvent[M]` | `struct { AgentName; RunPath; Output; Action; Err }` | 泛型 Agent 事件 |
| `TypedAgentOutput[M]` | `struct { MessageOutput; CustomizedOutput }` | 泛型 Agent 输出 |
| `TypedMessageVariant[M]` | `struct { IsStreaming; Message M; MessageStream; ... }` | 泛型消息变体 |
| `TypedRunner[M]` | `struct` | 泛型 Runner |
| `TypedRunnerConfig[M]` | `struct` | 泛型 Runner 配置 |
| `NewTypedRunner[M]` | `func(TypedRunnerConfig[M]) *TypedRunner[M]` | 创建泛型 Runner |
| `NewRunnerFrom[M]` | `func(TypedAgent[M]) *TypedRunner[M]` | 便捷构造器，从 Agent 创建 Runner |
| `TypedEventFromMessage[M]` | `func(M, stream, role, toolName) *TypedAgentEvent[M]` | 创建泛型事件 |
| `TypedGetMessage[M]` | `func(*TypedAgentEvent[M]) (M, *TypedAgentEvent[M], error)` | 从事件提取消息 |

### 变更（type alias，向后兼容）

| 原符号 | 现在是 |
|--------|--------|
| `Agent` | `= TypedAgent[*schema.Message]` |
| `ResumableAgent` | `= TypedResumableAgent[*schema.Message]` |
| `OnSubAgents` | `= TypedOnSubAgents[*schema.Message]` |
| `AgentInput` | `= TypedAgentInput[*schema.Message]` |
| `AgentEvent` | `= TypedAgentEvent[*schema.Message]` |
| `AgentOutput` | `= TypedAgentOutput[*schema.Message]` |
| `MessageVariant` | `= TypedMessageVariant[*schema.Message]` |
| `Runner` | `= TypedRunner[*schema.Message]` |
| `RunnerConfig` | `= TypedRunnerConfig[*schema.Message]` |

---

## 5. `adk` 包 — ChatModelAgent

### 新增

| 符号 | 签名 | 说明 |
|------|------|------|
| `TypedChatModelAgent[M]` | `struct` | 泛型 ChatModel Agent |
| `TypedChatModelAgentConfig[M]` | `struct` | 泛型 ChatModel Agent 配置 |
| `TypedChatModelAgentState[M]` | `struct { Messages []M; ... }` | 泛型 Agent 状态 |
| `TypedGenModelInput[M]` | `func(ctx, instruction, *TypedAgentInput[M]) ([]M, error)` | 泛型模型输入生成函数 |
| `NewTypedChatModelAgent[M]` | `func(ctx, *TypedChatModelAgentConfig[M]) (*TypedChatModelAgent[M], error)` | 创建泛型 ChatModel Agent |
| `NewChatModelAgentFrom[M]` | `func(ctx, BaseModel[M], *ChatModelAgentConfig) (*TypedChatModelAgent[M], error)` | 便捷构造器，从 model 创建 Agent |

### 变更（type alias，向后兼容）

| 原符号 | 现在是 |
|--------|--------|
| `ChatModelAgent` | `= TypedChatModelAgent[*schema.Message]` |
| `ChatModelAgentConfig` | `= TypedChatModelAgentConfig[*schema.Message]` |
| `ChatModelAgentState` | `= TypedChatModelAgentState[*schema.Message]` |
| `GenModelInput` | `= TypedGenModelInput[*schema.Message]` |

---

## 6. `adk` 包 — Middleware

### 新增

| 符号 | 签名 | 说明 |
|------|------|------|
| `TypedChatModelAgentMiddleware[M]` | `interface` | 泛型 ChatModel Middleware 接口 |
| `TypedBaseChatModelAgentMiddleware[M]` | `struct{}` | 泛型 Middleware 默认实现基类 |

### 变更（type alias，向后兼容）

| 原符号 | 现在是 |
|--------|--------|
| `ChatModelAgentMiddleware` | `= TypedChatModelAgentMiddleware[*schema.Message]` |
| `BaseChatModelAgentMiddleware` | `= TypedBaseChatModelAgentMiddleware[*schema.Message]` |

### 接口方法签名变更

`TypedChatModelAgentMiddleware[M]` 中以下方法签名涉及消息类型：

| 方法 | 变更 |
|------|------|
| `BeforeModelRewriteState` | `*ChatModelAgentState` → `*TypedChatModelAgentState[M]` |
| `AfterModelRewriteState` | `*ChatModelAgentState` → `*TypedChatModelAgentState[M]` |
| `AfterToolCallsRewriteState` | `*ChatModelAgentState` → `*TypedChatModelAgentState[M]` |
| `WrapModel` | `model.BaseChatModel` → `model.BaseModel[M]` |

---

## 7. `adk` 包 — State

### 新增

| 符号 | 签名 | 说明 |
|------|------|------|
| `TypedState[M]` | `struct { Messages []M; Extra map[string]any; ... }` | 泛型 Agent 运行时状态 |

### 变更（type alias，向后兼容）

| 原符号 | 现在是 |
|--------|--------|
| `State` | `= TypedState[*schema.Message]` |

---

## 8. `adk` 包 — 多 Agent 编排

### 新增

| 符号 | 签名 | 说明 |
|------|------|------|
| `TypedAgentOption[M]` | `func(*typedFlowAgent[M])` | 泛型 Agent 选项 |
| `TypedHistoryEntry[M]` | `struct` | 泛型历史记录条目 |
| `TypedHistoryRewriter[M]` | `func(ctx, []*TypedHistoryEntry[M]) ([]M, error)` | 泛型历史重写器 |
| `TypedDeterministicTransferConfig[M]` | `struct { Agent TypedAgent[M]; ToAgentNames []string }` | 泛型确定性转移配置 |
| `TypedSetSubAgents[M]` | `func(ctx, TypedAgent[M], []TypedAgent[M]) (TypedResumableAgent[M], error)` | 泛型设置子 Agent |
| `TypedWithDisallowTransferToParent[M]` | `func() TypedAgentOption[M]` | 泛型禁止转移到父 Agent |
| `TypedWithHistoryRewriter[M]` | `func(TypedHistoryRewriter[M]) TypedAgentOption[M]` | 泛型设置历史重写器 |
| `TypedAgentWithDeterministicTransferTo[M]` | `func(ctx, *TypedDeterministicTransferConfig[M]) TypedAgent[M]` | 泛型确定性转移包装 |

### 变更（type alias，向后兼容）

| 原符号 | 现在是 |
|--------|--------|
| `AgentOption` | `= TypedAgentOption[*schema.Message]` |
| `HistoryEntry` | `= TypedHistoryEntry[*schema.Message]` |
| `HistoryRewriter` | `= TypedHistoryRewriter[*schema.Message]` |
| `DeterministicTransferConfig` | `= TypedDeterministicTransferConfig[*schema.Message]` |

---

## 9. `adk` 包 — Workflow Agents

### 新增

| 符号 | 签名 | 说明 |
|------|------|------|
| `TypedSequentialAgentConfig[M]` | `struct` | 泛型顺序 Agent 配置 |
| `TypedParallelAgentConfig[M]` | `struct` | 泛型并行 Agent 配置 |
| `TypedLoopAgentConfig[M]` | `struct` | 泛型循环 Agent 配置 |
| `NewTypedSequentialAgent[M]` | `func(ctx, *TypedSequentialAgentConfig[M]) (TypedResumableAgent[M], error)` | 创建泛型顺序 Agent |
| `NewTypedParallelAgent[M]` | `func(ctx, *TypedParallelAgentConfig[M]) (TypedResumableAgent[M], error)` | 创建泛型并行 Agent |
| `NewTypedLoopAgent[M]` | `func(ctx, *TypedLoopAgentConfig[M]) (TypedResumableAgent[M], error)` | 创建泛型循环 Agent |
| `NewSequentialAgentFrom[M]` | `func(ctx, []TypedAgent[M], *SequentialAgentConfig) (TypedResumableAgent[M], error)` | 便捷构造器 |
| `NewParallelAgentFrom[M]` | `func(ctx, []TypedAgent[M], *ParallelAgentConfig) (TypedResumableAgent[M], error)` | 便捷构造器 |
| `NewLoopAgentFrom[M]` | `func(ctx, []TypedAgent[M], *LoopAgentConfig) (TypedResumableAgent[M], error)` | 便捷构造器 |

### 变更（type alias，向后兼容）

| 原符号 | 现在是 |
|--------|--------|
| `SequentialAgentConfig` | `= TypedSequentialAgentConfig[*schema.Message]` |
| `ParallelAgentConfig` | `= TypedParallelAgentConfig[*schema.Message]` |
| `LoopAgentConfig` | `= TypedLoopAgentConfig[*schema.Message]` |

---

## 10. `adk` 包 — Interrupt

### 新增

| 符号 | 签名 | 说明 |
|------|------|------|
| `TypedInterrupt[M]` | `func(ctx, info) *TypedAgentEvent[M]` | 泛型中断 |
| `TypedStatefulInterrupt[M]` | `func(ctx, info, state) *TypedAgentEvent[M]` | 泛型有状态中断 |
| `TypedCompositeInterrupt[M]` | `func(ctx, info, state, children) *TypedAgentEvent[M]` | 泛型组合中断 |

---

## 11. `adk` 包 — SendEvent

### 新增

| 符号 | 签名 | 说明 |
|------|------|------|
| `TypedSendEvent[M]` | `func(ctx, *TypedAgentEvent[M]) error` | 泛型事件发送（Middleware 内使用） |

---

## 12. `adk` 包 — TurnLoop（未发布 API，直接变更）

> TurnLoop 是未发布 API，不提供 alias 兼容，所有类型直接增加 `M MessageType` 参数。

### 变更（Breaking — 新增泛型参数 `M MessageType`）

| 符号 | 旧签名 | 新签名 |
|------|--------|--------|
| `TurnLoopConfig` | `[T any]` | `[T any, M MessageType]` |
| `GenInputResult` | `[T any]` | `[T any, M MessageType]` |
| `GenResumeResult` | `[T any]` | `[T any, M MessageType]` |
| `TurnContext` | `[T any]` | `[T any, M MessageType]` |
| `TurnLoopExitState` | `[T any]` | `[T any, M MessageType]` |
| `TurnLoop` | `[T any]` | `[T any, M MessageType]` |
| `PushOption` | `[T any]` | `[T any, M MessageType]` |
| `NewTurnLoop` | `[T any]` | `[T any, M MessageType]` |
| `WithPreempt` | `[T any]` | `[T any, M MessageType]` |
| `WithPreemptDelay` | `[T any]` | `[T any, M MessageType]` |
| `WithPushStrategy` | `[T any]` | `[T any, M MessageType]` |

### `TurnLoopConfig` 回调签名变更

| 字段 | 旧类型 | 新类型 |
|------|--------|--------|
| `GenInput` | `func(...) (*GenInputResult[T], error)` | `func(...) (*GenInputResult[T, M], error)` |
| `GenResume` | `func(...) (*GenResumeResult[T], error)` | `func(...) (*GenResumeResult[T, M], error)` |
| `OnAgentEvents` | `func(ctx, *TurnContext[T], *AsyncIterator[*AgentEvent]) error` | `func(ctx, *TurnContext[T, M], *AsyncIterator[*TypedAgentEvent[M]]) error` |
| `ShouldEndTurn` | `func(ctx, *TurnContext[T]) (bool, error)` | `func(ctx, *TurnContext[T, M]) (bool, error)` |

### `GenInputResult` 字段变更

| 字段 | 旧类型 | 新类型 |
|------|--------|--------|
| `Input` | `*AgentInput` | `*TypedAgentInput[M]` |

---

## 13. `adk` 包 — Agentic Callbacks

### 新增

| 符号 | 说明 |
|------|------|
| `AgenticAgentCallbackInput` | Agentic Agent OnStart 回调输入 |
| `AgenticAgentCallbackOutput` | Agentic Agent OnEnd 回调输出 |
| `ConvAgenticAgentCallbackInput` | 类型转换辅助函数 |
| `ConvAgenticAgentCallbackOutput` | 类型转换辅助函数 |

---

## 14. `utils/callbacks` 包

### 新增

| 符号 | 说明 |
|------|------|
| `AgenticAgentCallbackHandler` | Agentic Agent 回调处理器（含 `OnStart`、`OnEnd`） |
| `(*HandlerHelper).AgenticAgent(handler)` | 注册 Agentic Agent 回调的链式方法 |
