# ADK AgentHandler 重构设计文档

## 1. 背景与问题

### 1.1 原有设计

`adk` 包中的 `AgentMiddleware` 是一个结构体，用于扩展 Agent 的行为：

```go
type AgentMiddleware struct {
    AdditionalInstruction string
    AdditionalTools       []tool.BaseTool
    BeforeChatModel       func(context.Context, *ChatModelAgentState) error
    AfterChatModel        func(context.Context, *ChatModelAgentState) error
    WrapToolCall          compose.ToolMiddleware
}
```

### 1.2 存在的问题

#### 问题 1：AgentMiddleware 是 struct 而非 interface

这是最核心的问题。作为 struct，`AgentMiddleware` 存在以下限制：

```go
// 1. 无法携带自定义状态
// 如果想实现一个带缓存的 middleware，struct 字段是固定的，无法扩展
type CachingMiddleware struct {
    cache map[string]string  // 无法添加到 AgentMiddleware
}

// 只能用闭包变通，但状态管理变得隐晦
var cache = make(map[string]string)
middleware := AgentMiddleware{
    BeforeChatModel: func(ctx context.Context, state *ChatModelAgentState) error {
        // 通过闭包访问外部 cache，不够直观
        if cached, ok := cache[key]; ok { ... }
        return nil
    },
}

// 2. 违反开闭原则 (Open-Closed Principle, OCP)
// 通俗解释：当需要添加新功能时，应该可以通过"添加新代码"来实现，而不是"修改现有代码"。
//
// 当前 `AgentMiddleware` 是一个字段固定的结构体。如果未来需要增加新的 Hook 点（例如 `AfterToolCall`），就必须修改结构体定义，这会导致所有依赖该结构体的代码（包括 adk 内部和用户侧）都可能受到影响，无法做到对修改封闭。

// 3. 违反接口隔离原则 (Interface Segregation Principle, ISP)
// 通俗解释：一个接口（或结构体）应该尽量小且专注，不要把无关的功能塞在一起，导致使用者被迫面对一堆他不需要的东西。
//
// 当前 `AgentMiddleware` 将指令修改、工具修改、消息处理、工具拦截等所有功能耦合在一个结构体中。绝大多数用户可能只需要修改指令，却被迫面对并初始化一个包含大量无关字段的庞大结构体，造成认知负担和代码冗余。
middleware := AgentMiddleware{
    AdditionalInstruction: "extra",
    // 用户心理活动："这些 AdditionalTools, WrapToolCall 是什么？我需要填吗？填 nil 会报错吗？"
}
```

如果是 interface，上述问题都可以解决：

```go
// 自定义类型可以携带任意状态
type CachingHandler struct {
    cache map[string]string
}

// 只需实现需要的接口
func (h *CachingHandler) ModifyInstruction(ctx context.Context, instruction string) (string, error) {
    // 直接访问 h.cache
}

// 不同类型可以统一放入 []AgentHandler
handlers := []AgentHandler{
    &CachingHandler{cache: make(map[string]string)},
    &LoggingHandler{logger: log},
    &MetricsHandler{collector: metrics},
}
```

#### 问题 2：Instruction 只能追加，无法修改

```go
// 当前只能追加
middleware := AgentMiddleware{
    AdditionalInstruction: "额外指令",  // 只会追加到原 instruction 后面
}

// 无法实现以下场景：
// - 在原 instruction 前面插入内容
// - 根据条件动态替换 instruction
// - 基于上下文修改 instruction
```

#### 问题 3：Tool 只能添加，无法移除或修改

```go
// 当前只能添加 tool
middleware := AgentMiddleware{
    AdditionalTools: []tool.BaseTool{newTool},  // 只能添加
}

// 无法实现以下场景：
// - 移除某个不安全的 tool
// - 修改 tool 的 ReturnDirectly 属性
// - 根据上下文动态调整 tool 集
```

#### 问题 4：BeforeChatModel/AfterChatModel 引入了额外概念

```go
// 当前：通过 ChatModelAgentState 指针修改状态
BeforeChatModel: func(ctx context.Context, state *ChatModelAgentState) error {
    state.Messages = append(state.Messages, newMsg)
    return nil
}

// 问题：
// - ChatModelAgentState 是仅此处暴露的新概念，增加了理解成本
// - 非函数式风格：通过指针修改而非返回新值
```

#### 问题 5：ToolMiddleware 配置存在重复

`ChatModelAgentConfig` 中有两处可以配置 `ToolMiddleware`：

```go
type ChatModelAgentConfig struct {
    // 位置 1：在 ToolsConfig 中
    ToolsConfig ToolsConfig  // 内含 ToolCallMiddlewares []compose.ToolMiddleware
    
    // 位置 2：在 Middlewares 中
    Middlewares []AgentMiddleware  // 每个 AgentMiddleware 都有 WrapToolCall 字段
}

// 使用时容易混淆：
config := &ChatModelAgentConfig{
    ToolsConfig: ToolsConfig{
        ToolCallMiddlewares: []compose.ToolMiddleware{mw1},  // 这里配置了
    },
    Middlewares: []AgentMiddleware{
        {WrapToolCall: mw2},  // 这里也配置了，两者关系不清晰
    },
}
```

#### 问题 6：compose.ToolMiddleware 是底层概念，需要在 ADK 层封装

`compose.ToolMiddleware` 是 `compose` 包的概念，直接暴露给 ADK 用户不够友好：

```go
// compose 层的 ToolMiddleware 定义
type ToolMiddleware struct {
    Invokable  InvokableToolMiddleware
    Streamable StreamableToolMiddleware
}

type InvokableToolMiddleware func(InvokableToolEndpoint) InvokableToolEndpoint
type InvokableToolEndpoint func(context.Context, *ToolInput) (*ToolOutput, error)

// ADK 用户需要理解 ToolInput/ToolOutput/Endpoint 等概念
// 应该在 ADK 层提供更简洁的封装
```

## 2. 设计目标

解决上述问题，同时保持 100% 向后兼容：

1. 支持对 instruction 进行任意修改（追加、前置、替换、条件逻辑）
2. 支持对 tool 进行增删改，包括 `ReturnDirectly` 属性
3. 使用函数式风格处理消息状态（输入 → 输出），直接操作 `[]Message` 而非 `ChatModelAgentState`
4. 统一 ToolMiddleware 的配置入口，在 ADK 层提供简洁封装
5. 现有使用 `AgentMiddleware` 的代码无需任何修改

## 3. 核心设计

### 3.1 AgentHandler 接口

定义一个统一的 `AgentHandler` 接口，包含所有扩展点：

```go
type AgentHandler interface {
    Name() string
    
    BeforeAgent(ctx context.Context, config *AgentConfig) (context.Context, error)
    
    BeforeModelRewriteHistory(ctx context.Context, messages []Message) (context.Context, []Message, error)
    AfterModelRewriteHistory(ctx context.Context, messages []Message) (context.Context, []Message, error)
    
    WrapInvokableToolCall(
        ctx context.Context,
        input *ToolCallInput,
        next func(context.Context, *ToolCallInput) (*ToolCallResult, error),
    ) (*ToolCallResult, error)
    
    WrapStreamableToolCall(
		ctx context.Context,
		input *ToolCallInput,
		next func(context.Context, *ToolCallInput) (*StreamToolCallResult, error),
	) (*StreamToolCallResult, error)
}
```

### 3.2 AgentConfig 结构体

`BeforeAgent` 方法接收 `AgentConfig` 指针，handler 可以直接修改其字段：

```go
type AgentConfig struct {
    Instruction string
    Tools       []ToolMeta
    Input       *AgentInput
    RunOptions  *AgentRunOptions
}

type ToolMeta struct {
    Tool           tool.BaseTool
    ReturnDirectly bool
}
```

### 3.3 BaseAgentHandler

提供默认实现，自定义 handler 可嵌入它，只需实现关心的方法：

```go
type BaseAgentHandler struct {
    name string
}

func NewBaseAgentHandler(name string) BaseAgentHandler {
    return BaseAgentHandler{name: name}
}

func (b BaseAgentHandler) Name() string {
    return b.name
}

func (b BaseAgentHandler) BeforeAgent(ctx context.Context, config *AgentConfig) (context.Context, error) {
    return ctx, nil
}

func (b BaseAgentHandler) BeforeModelRewriteHistory(ctx context.Context, messages []Message) (context.Context, []Message, error) {
    return ctx, messages, nil
}

func (b BaseAgentHandler) AfterModelRewriteHistory(ctx context.Context, messages []Message) (context.Context, []Message, error) {
    return ctx, messages, nil
}

func (b BaseAgentHandler) WrapInvokableToolCall(
    ctx context.Context,
    input *ToolCallInput,
    next func(context.Context, *ToolCallInput) (*ToolCallResult, error),
) (*ToolCallResult, error) {
    return next(ctx, input)
}

func (b BaseAgentHandler) WrapStreamableToolCall(
	ctx context.Context,
	input *ToolCallInput,
	next func(context.Context, *ToolCallInput) (*StreamToolCallResult, error),
) (*StreamToolCallResult, error) {
	return next(ctx, input)
}
```

### 3.4 接口方法说明

| 方法 | 时机 | 用途 |
|------|------|------|
| `BeforeAgent` | Agent 执行前 | 修改 instruction、tools、input、options |
| `BeforeModelRewriteHistory` | 模型调用前 | 重写对话历史 |
| `AfterModelRewriteHistory` | 模型调用后 | 重写对话历史 |
| `WrapInvokableToolCall` | 同步 tool 调用时 | 包装 tool 调用（日志、缓存、拦截等） |
| `WrapStreamableToolCall` | 流式 tool 调用时 | 包装流式 tool 调用 |

所有方法都返回 `context.Context`（或通过 `next` 传递），允许 handler 修改 context 并传递给后续处理。

### 3.5 ADK 层类型定义

```go
type ToolCallInput = compose.ToolInput
type ToolCallResult = compose.ToolOutput
type StreamToolCallResult = compose.StreamToolOutput
```

`ToolCallInput` 包含以下字段：
- `Name`: tool 名称
- `Arguments`: 调用参数
- `CallID`: 调用唯一标识
- `CallOptions`: tool 选项

`ToolCallResult.Result` 是 `string`，`StreamToolCallResult.Result` 是 `*schema.StreamReader[string]`。

### 3.6 辅助函数

为常见场景提供便捷的 handler 创建函数：

```go
func WithInstruction(text string) AgentHandler
func WithInstructionFunc(fn func(ctx context.Context, instruction string) (context.Context, string, error)) AgentHandler
func WithTools(tools ...tool.BaseTool) AgentHandler
func WithToolsFunc(fn func(ctx context.Context, tools []ToolMeta) (context.Context, []ToolMeta, error)) AgentHandler
func WithBeforeAgent(fn func(ctx context.Context, config *AgentConfig) (context.Context, error)) AgentHandler
func WithBeforeModelRewriteHistory(fn func(ctx context.Context, messages []Message) (context.Context, []Message, error)) AgentHandler
func WithAfterModelRewriteHistory(fn func(ctx context.Context, messages []Message) (context.Context, []Message, error)) AgentHandler
func WithInvokableToolWrapper(fn func(ctx, input, next) (*ToolCallResult, error)) AgentHandler
func WithStreamableToolWrapper(fn func(ctx, input, next) (*StreamToolCallResult, error)) AgentHandler
```

## 4. 向后兼容策略

### 4.1 AgentMiddleware 实现 AgentHandler 接口

`AgentMiddleware` 结构体保持不变，但新增实现 `AgentHandler` 接口：

```go
func (m AgentMiddleware) Name() string {
    return "legacy-middleware"
}

func (m AgentMiddleware) BeforeAgent(ctx context.Context, config *AgentConfig) (context.Context, error) {
    if m.AdditionalInstruction != "" {
        config.Instruction = config.Instruction + "\n" + m.AdditionalInstruction
    }
    for _, t := range m.AdditionalTools {
        config.Tools = append(config.Tools, ToolMeta{Tool: t, ReturnDirectly: false})
    }
    return ctx, nil
}

func (m AgentMiddleware) BeforeModelRewriteHistory(ctx context.Context, messages []Message) (context.Context, []Message, error) {
    if m.BeforeChatModel == nil {
        return ctx, messages, nil
    }
    state := &ChatModelAgentState{Messages: messages}
    if err := m.BeforeChatModel(ctx, state); err != nil {
        return ctx, nil, err
    }
    return ctx, state.Messages, nil
}

func (m AgentMiddleware) AfterModelRewriteHistory(ctx context.Context, messages []Message) (context.Context, []Message, error) {
    if m.AfterChatModel == nil {
        return ctx, messages, nil
    }
    state := &ChatModelAgentState{Messages: messages}
    if err := m.AfterChatModel(ctx, state); err != nil {
        return ctx, nil, err
    }
    return ctx, state.Messages, nil
}

func (m AgentMiddleware) WrapInvokableToolCall(
    ctx context.Context,
    input *ToolCallInput,
    next func(context.Context, *ToolCallInput) (*ToolCallResult, error),
) (*ToolCallResult, error) {
    if m.WrapToolCall.Invokable == nil {
        return next(ctx, input)
    }
    // 适配 compose.InvokableToolMiddleware
    wrappedNext := m.WrapToolCall.Invokable(func(c context.Context, i *ToolCallInput) (*ToolCallResult, error) {
        return next(c, i)
    })
    return wrappedNext(ctx, input)
}

func (m AgentMiddleware) WrapStreamableToolCall(
    ctx context.Context,
    input *ToolCallInput,
    next func(context.Context, *ToolCallInput) (*schema.StreamReader[*ToolCallResult], error),
) (*schema.StreamReader[*ToolCallResult], error) {
    if m.WrapToolCall.Streamable == nil {
        return next(ctx, input)
    }
    // 适配 compose.StreamableToolMiddleware
    wrappedNext := m.WrapToolCall.Streamable(func(c context.Context, i *ToolCallInput) (*schema.StreamReader[*ToolCallResult], error) {
        return next(c, i)
    })
    return wrappedNext(ctx, input)
}
```

### 4.2 统一处理流程

在 `NewChatModelAgent` 中，先处理 `Middlewares`，再处理 `Handlers`，两者合并到统一的 handler 列表：

```go
allHandlers := make([]AgentHandler, 0, len(config.Middlewares)+len(config.Handlers))

// 先添加 Middlewares（AgentMiddleware 已实现 AgentHandler 接口）
for _, m := range config.Middlewares {
    allHandlers = append(allHandlers, m)
}

// 再添加 Handlers
allHandlers = append(allHandlers, config.Handlers...)

// 按顺序遍历，调用各个方法
```

处理顺序：`Middlewares[0]` → `Middlewares[1]` → ... → `Handlers[0]` → `Handlers[1]` → ...

## 5. 使用示例

### 5.1 旧 API（仍然支持）

```go
agent, _ := NewChatModelAgent(ctx, &ChatModelAgentConfig{
    Name:        "MyAgent",
    Description: "A helpful agent",
    Model:       model,
    Middlewares: []AgentMiddleware{
        {
            AdditionalInstruction: "Be concise.",
            AdditionalTools:       []tool.BaseTool{myTool},
            BeforeChatModel: func(ctx context.Context, state *ChatModelAgentState) error {
                // 修改消息
                return nil
            },
        },
    },
})
```

### 5.2 新 API

```go
agent, _ := NewChatModelAgent(ctx, &ChatModelAgentConfig{
    Name:        "MyAgent",
    Description: "A helpful agent",
    Model:       model,
    Handlers: []AgentHandler{
        // 简单场景：使用辅助函数
        WithInstruction("Be concise."),
        WithTools(myTool),
        
        // 复杂场景：使用 BeforeAgent
        WithBeforeAgent(func(ctx context.Context, config *AgentConfig) (context.Context, error) {
            config.Instruction = fmt.Sprintf("%s\nCurrent time: %s", config.Instruction, time.Now())
            
            // 移除危险 tool
            filtered := make([]ToolMeta, 0, len(config.Tools))
            for _, t := range config.Tools {
                info, _ := t.Tool.Info(ctx)
                if info.Name != "dangerous_tool" {
                    filtered = append(filtered, t)
                }
            }
            config.Tools = filtered
            
            // 设置某个 tool 的 ReturnDirectly
            for i := range config.Tools {
                info, _ := config.Tools[i].Tool.Info(ctx)
                if info.Name == "final_answer" {
                    config.Tools[i].ReturnDirectly = true
                }
            }
            
            return ctx, nil
        }),
        
        // 消息历史重写
        WithBeforeModelRewriteHistory(func(ctx context.Context, messages []Message) (context.Context, []Message, error) {
            // 过滤或修改消息
            return ctx, messages, nil
        }),
        
        // Tool 调用包装
        WithInvokableToolWrapper(func(ctx context.Context, input *ToolCallInput, next func(context.Context, *ToolCallInput) (*ToolCallResult, error)) (*ToolCallResult, error) {
            log.Printf("Calling tool: %s (callID: %s)", input.Name, input.CallID)
            result, err := next(ctx, input)
            log.Printf("Tool result: %s", result.Result)
            return result, err
        }),
    },
})
```

### 5.3 自定义 AgentHandler 实现

```go
type MyCustomHandler struct {
    BaseAgentHandler
    cache map[string]*ToolCallResult
}

func NewMyCustomHandler() *MyCustomHandler {
    return &MyCustomHandler{
        BaseAgentHandler: NewBaseAgentHandler("my-custom-handler"),
        cache:            make(map[string]*ToolCallResult),
    }
}

func (h *MyCustomHandler) BeforeAgent(ctx context.Context, config *AgentConfig) (context.Context, error) {
    config.Instruction = config.Instruction + "\n" + h.getExtraInstruction()
    return ctx, nil
}

func (h *MyCustomHandler) WrapInvokableToolCall(
    ctx context.Context,
    input *ToolCallInput,
    next func(context.Context, *ToolCallInput) (*ToolCallResult, error),
) (*ToolCallResult, error) {
    cacheKey := input.Name + ":" + input.Arguments
    if cached, ok := h.cache[cacheKey]; ok {
        return cached, nil
    }
    result, err := next(ctx, input)
    if err == nil {
        h.cache[cacheKey] = result
    }
    return result, err
}
```

## 6. 执行顺序与行为规范

### 6.1 Handler 执行顺序

所有 Handler 按照**管道式（Pipeline）**顺序执行，前一个 Handler 的输出作为后一个 Handler 的输入：

```
Middlewares[0] → Middlewares[1] → ... → Handlers[0] → Handlers[1] → ...
```

**具体执行流程**：

1. **BeforeAgent 阶段**（Agent 初始化时，仅执行一次）：
   ```go
   agentConfig := &AgentConfig{Instruction: initialInstruction, Tools: initialTools}
   for _, h := range allHandlers {
       ctx, err = h.BeforeAgent(ctx, agentConfig)  // 每个 handler 可修改 agentConfig
   }
   // 最终的 agentConfig 用于 Agent 配置
   ```

2. **BeforeModelRewriteHistory 阶段**（每次模型调用前）：
   ```go
   messages := currentMessages
   for _, h := range allHandlers {
       ctx, messages, err = h.BeforeModelRewriteHistory(ctx, messages)
   }
   // 最终的 messages 传递给模型
   ```

3. **AfterModelRewriteHistory 阶段**（每次模型调用后）：
   ```go
   messages := messagesWithModelResponse
   for _, h := range allHandlers {
       ctx, messages, err = h.AfterModelRewriteHistory(ctx, messages)
   }
   // 最终的 messages 保存到状态
   ```

4. **Tool 调用包装**（洋葱模型，外层先进后出）：
   ```go
   // Handler[0].Wrap → Handler[1].Wrap → ... → 实际 Tool 调用 → ... → Handler[1] 返回 → Handler[0] 返回
   ```

### 6.2 冲突解决规则

当多个 Handler 修改同一配置时，遵循以下规则：

| 配置项 | 规则 | 说明 |
|--------|------|------|
| `Instruction` | **累积拼接** | 每个 Handler 可追加、前置或替换，最终结果是所有修改的累积 |
| `Tools` | **追加模式** | `WithTools` 追加工具；如需移除或修改，使用 `WithToolsFunc` |
| `ReturnDirectly` | **后来者优先** | 后执行的 Handler 设置的值会覆盖先前的设置 |
| `Messages` | **管道传递** | 每个 Handler 接收前一个的输出，可任意修改 |

**示例：工具冲突解决**

```go
// Handler 1: 添加工具 A
WithTools(toolA),

// Handler 2: 移除工具 A（使用 WithToolsFunc 实现）
WithToolsFunc(func(ctx context.Context, tools []ToolMeta) (context.Context, []ToolMeta, error) {
    filtered := make([]ToolMeta, 0, len(tools))
    for _, t := range tools {
        info, _ := t.Tool.Info(ctx)
        if info.Name != "toolA" {
            filtered = append(filtered, t)
        }
    }
    return ctx, filtered, nil
}),

// 最终结果：工具 A 被移除（Handler 2 的效果覆盖 Handler 1）
```

### 6.3 并发安全约束

**重要**：Handler 的实现必须遵循以下并发安全规则：

1. **无状态 Handler（推荐）**：
   - 辅助函数创建的 Handler（如 `WithInstruction`、`WithTools`）都是无状态的，天然线程安全
   - 推荐优先使用无状态设计

2. **有状态 Handler**：
   - 如果 Handler 包含可变状态（如缓存），必须自行保证线程安全
   - 使用 `sync.Mutex` 或 `sync.RWMutex` 保护共享状态
   - 或者为每个 Agent 实例创建独立的 Handler 实例

**错误示例**（存在竞态条件）：

```go
// ❌ 错误：共享的有状态 Handler，没有锁保护
type UnsafeCachingHandler struct {
    BaseAgentHandler
    cache map[string]*ToolCallResult  // 多个 goroutine 并发访问会出问题
}

func (h *UnsafeCachingHandler) WrapInvokableToolCall(...) (*ToolCallResult, error) {
    if cached, ok := h.cache[key]; ok {  // 并发读
        return cached, nil
    }
    result, err := next(ctx, input)
    h.cache[key] = result  // 并发写，竞态条件！
    return result, err
}
```

**正确示例**：

```go
// ✅ 正确：使用互斥锁保护共享状态
type SafeCachingHandler struct {
    BaseAgentHandler
    cache map[string]*ToolCallResult
    mu    sync.RWMutex
}

func (h *SafeCachingHandler) WrapInvokableToolCall(...) (*ToolCallResult, error) {
    h.mu.RLock()
    if cached, ok := h.cache[key]; ok {
        h.mu.RUnlock()
        return cached, nil
    }
    h.mu.RUnlock()
    
    result, err := next(ctx, input)
    if err == nil {
        h.mu.Lock()
        h.cache[key] = result
        h.mu.Unlock()
    }
    return result, err
}
```

```go
// ✅ 正确：为每个 Agent 创建独立的 Handler 实例
func NewAgentWithCaching(ctx context.Context, config *ChatModelAgentConfig) (*ChatModelAgent, error) {
    config.Handlers = append(config.Handlers, &CachingHandler{
        BaseAgentHandler: NewBaseAgentHandler("caching"),
        cache:            make(map[string]*ToolCallResult),  // 每个 Agent 独立的缓存
    })
    return NewChatModelAgent(ctx, config)
}
```

### 6.4 错误处理

- 任何 Handler 方法返回错误时，执行链立即中断
- 错误会被传播到调用方，不会被静默吞掉
- 对于 `BeforeAgent`，错误会导致 `NewChatModelAgent` 返回错误
- 对于运行时方法，错误会通过 `AgentEvent.Err` 传递给用户

## 7. 总结

本次重构引入了 `AgentHandler` 接口，解决了 `AgentMiddleware` 的以下问题：

| 问题 | 解决方案 |
|------|----------|
| struct 无法携带自定义状态 | interface 允许任意自定义类型 |
| 违反开闭原则 | 新增能力通过新方法实现，无需修改现有代码 |
| 违反接口隔离原则 | BaseAgentHandler 提供默认实现，只需覆盖关心的方法 |
| Instruction 只能追加 | BeforeAgent 可任意修改 AgentConfig.Instruction |
| Tool 只能添加 | BeforeAgent 可任意修改 AgentConfig.Tools |
| ChatModelAgentState 额外概念 | 直接操作 []Message，函数式风格 |
| ToolMiddleware 配置重复 | 统一通过 AgentHandler 配置 |
| compose.ToolMiddleware 底层概念暴露 | ADK 层定义 ToolCallInput/ToolCallResult 别名 |

现有使用 `AgentMiddleware` 的代码无需修改，新代码可以使用 `Handlers` 字段获得更灵活的扩展能力。
