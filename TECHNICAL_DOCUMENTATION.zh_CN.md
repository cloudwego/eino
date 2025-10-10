# Eino human-in-the-loop框架：技术架构指南

## 概述

本文档提供 Eino 的human-in-the-loop (Human-in-the-Loop, HITL) 框架架构的技术细节，重点介绍中断/恢复机制和底层的寻址系统。

## Alpha 版本发布公告

> **注意**：本文档中描述的human-in-the-loop框架是一个 **Alpha 功能**。

- **发布标签**：`v0.6.0-alpha1`
- **稳定性**：在正式发布前，API 和功能可能会发生变化。
- **Alpha 阶段**：Alpha 阶段预计将在 2025 年 11 月底前结束。

我们欢迎在此阶段提供反馈和贡献，以帮助我们改进该框架。

## human-in-the-loop的需求

下图说明了在中断/恢复过程中，每个组件必须回答的关键问题。理解这些需求是掌握架构设计背后原因的关键。

```mermaid
graph TD
    subgraph P1 [中断阶段]
        direction LR
        subgraph Dev1 [开发者]
            direction TB
            D1[我现在应该中断吗？<br/>我之前被中断过吗？]
            D2[用户应该看到关于此中断的<br/>什么信息？]
            D3[我应该保留什么状态<br/>以便后续恢复？]
            D1 --> D2 --> D3
        end
        
        subgraph Fw1 [框架]
            direction TB
            F1[中断发生在执行层级的<br/>哪个位置？]
            F2[如何将状态与<br/>中断位置关联？]
            F3[如何持久化中断<br/>上下文和状态？]
            F4[用户需要什么信息<br/>来理解中断？]
            F1 --> F2 --> F3 --> F4
        end
        
        Dev1 --> Fw1
    end
    
    subgraph P2 [用户决策阶段]
        direction TB
        subgraph "最终用户"
            direction TB
            U1[中断发生在流程的<br/>哪个环节？]
            U2[开发者提供了<br/>什么类型的信息？]
            U3[我应该恢复这个<br/>中断吗？]
            U4[我应该为恢复<br/>提供数据吗？]
            U5[我应该提供什么类型的<br/>恢复数据？]
            U1 --> U2 --> U3 --> U4 --> U5
        end
    end
    
    
    subgraph P3 [恢复阶段]
        direction LR   
        subgraph Fw2 [框架]
            direction TB
            FR1[哪个实体正在中断<br/>以及如何重新运行它？]
            FR2[如何为被中断的实体<br/>恢复上下文？]
            FR3[如何将用户数据<br/>路由到中断实体？]
            FR1 --> FR2 --> FR3
        end
        
        subgraph Dev2 [开发者]
            direction TB
            DR1[我是显式的<br/>恢复目标吗？]
            DR2[如果不是目标，我应该<br/>重新中断以继续吗？]
            DR3[中断时我保留了<br/>什么状态？]
            DR4[如果提供了用户恢复数据，<br/>该如何处理？]
            DR1 --> DR2 --> DR3 --> DR4
        end
        
        Fw2 --> Dev2
    end
    
    P1 --> P2 --> P3
    
    classDef dev fill:#e1f5fe
    classDef fw fill:#f3e5f5
    classDef user fill:#e8f5e8
    
    class D1,D2,D3,DR1,DR2,DR3,DR4 dev
    class F1,F2,F3,F4,FR1,FR2,FR3 fw
    class U1,U2,U3,U4,U5 user
```

因此，我们的目标是：
1. 帮助开发者尽可能轻松地回答上述问题。
2. 帮助最终用户尽可能轻松地回答上述问题。
3. 使框架能够自动并开箱即用地回答上述问题。

## 架构概述

以下流程图说明了高层次的中断/恢复流程：

```mermaid
flowchart TD
    U[最终用户]
    
    subgraph R [Runner]
        Run
        Resume
    end
    
    U -->|初始输入| Run
    U -->|恢复数据| Resume
    
    subgraph E [（任意嵌套的）实体]
        Agent
        Tool
        ...
    end
    
    subgraph C [运行上下文]
        Address
        InterruptState
        ResumeData
    end
    
    Run -->|任意数量的 transfer / call| E
    R <-->|存储/恢复| C
    Resume -->|重放 transfer / call| E
    C -->|自动分配给| E
```

以下序列图显示了三个主要参与者之间按时间顺序的交互流程：

```mermaid
sequenceDiagram
    participant D as 开发者
    participant F as 框架
    participant U as 最终用户
    
   
    Note over D,F: 1. 中断阶段
    D->>F: 调用 StatefulInterrupt()<br>指定信息和状态
    F->>F: 持久化 InterruptID->{address, state}
    
   
    Note over F,U: 2. 用户决策阶段
    F->>U: 抛出 InterruptID->{address, info}
    U->>U: 决定 InterruptID->{resume data}
    U->>F: 调用 TargetedResume()<br>提供 InterruptID->{resume data}
    
   
    Note over D,F: 3. 恢复阶段
    F->>F: 路由到中断实体
    F->>D: 提供状态和恢复数据
    D->>D: 处理恢复
```


## ADK 包 API

ADK 包提供了用于构建具有human-in-the-loop能力的可中断 agent 的高级抽象。

### 1. 用于中断的 API

#### `Interrupt`
创建一个基础的中断动作。当 agent 需要暂停执行以请求外部输入或干预，但不需要保存任何内部状态以供恢复时使用。

```go
func Interrupt(ctx context.Context, info any) *AgentEvent
```

**参数:**
- `ctx`: 正在运行组件的上下文。
- `info`: 描述中断原因的面向用户的数据。

**返回:** 带有中断动作的 `*AgentEvent`。

**示例:**
```go
// 在 agent 的 Run 方法内部：

// 创建一个简单的中断以请求澄清。
return adk.Interrupt(ctx, "用户查询不明确，请澄清。")
```

---

#### `StatefulInterrupt`
创建一个中断动作，同时保存 agent 的内部状态。当 agent 具有必须恢复才能正确继续的内部状态时使用。

```go
func StatefulInterrupt(ctx context.Context, info any, state any) *AgentEvent
```

**参数:**
- `ctx`: 正在运行组件的上下文。
- `info`: 描述中断的面向用户的数据。
- `state`: agent 的内部状态对象，它将被序列化并存储。

**返回:** 带有中断动作的 `*AgentEvent`。

**示例:**
```go
// 在 agent 的 Run 方法内部：

// 定义要保存的状态。
type MyAgentState struct {
    ProcessedItems int
    CurrentTopic   string
}

currentState := &MyAgentState{
    ProcessedItems: 42,
    CurrentTopic:   "HITL",
}

// 中断并保存当前状态。
return adk.StatefulInterrupt(ctx, "在继续前需要用户反馈", currentState)
```

---

#### `CompositeInterrupt`
为协调多个子组件的组件创建一个中断动作。它将一个或多个子 agent 的中断组合成一个单一、内聚的中断。任何包含子 agent 的 agent（例如，自定义的 `Sequential` 或 `Parallel` agent）都使用此功能来传播其子级的中断。

```go
func CompositeInterrupt(ctx context.Context, info any, state any, 
    subInterruptSignals ...*InterruptSignal) *AgentEvent
```

**参数:**
- `ctx`: 正在运行组件的上下文。
- `info`: 描述协调器自身中断原因的面向用户的数据。
- `state`: 协调器 agent 自身的状态（例如，被中断的子 agent 的索引）。
- `subInterruptSignals`: 来自被中断子 agent 的 `InterruptSignal` 对象的变长列表。

**返回:** 带有中断动作的 `*AgentEvent`。

**示例:**
```go
// 在一个运行两个子 agent 的自定义顺序 agent 中...
subAgent1 := &myInterruptingAgent{}
subAgent2 := &myOtherAgent{}

// 如果 subAgent1 返回一个中断事件...
subInterruptEvent := subAgent1.Run(ctx, input)

// 父 agent 必须捕获它并将其包装在 CompositeInterrupt 中。
if subInterruptEvent.Action.Interrupted != nil {
    // 父 agent 可以添加自己的状态，比如哪个子 agent 被中断了。
    parentState := map[string]int{"interrupted_child_index": 0}
    
    //向上冒泡中断。
    return adk.CompositeInterrupt(ctx, 
        "一个子 agent 需要注意", 
        parentState, 
        subInterruptEvent.Action.Interrupted.internalInterrupted,
    )
}
```

### 2. 用于获取中断信息的 API

#### `InterruptInfo` 和 `InterruptCtx`
当 agent 执行被中断时，`AgentEvent` 包含结构化的中断信息。`InterruptInfo` 结构体包含一个 `InterruptCtx` 对象列表，每个对象代表层级中的一个中断点。

`InterruptCtx` 为单个可恢复的中断点提供了一个完整的、面向用户的上下文。

```go
type InterruptCtx struct {
    // ID 是中断点的唯一、完全限定地址，用于定向恢复。
    // 例如："agent:A;node:graph_a;tool:tool_call_123"
    ID string

    // Address 是导致中断点的 AddressSegment 段的结构化序列。
    Address Address

    // Info 是与中断关联的面向用户的信息，由触发它的组件提供。
    Info any

    // IsRootCause 指示中断点是否是中断的确切根本原因。
    IsRootCause bool

    // Parent 指向中断链中父组件的上下文（对于顶级中断为 nil）。
    Parent *InterruptCtx
}
```

以下示例展示了如何访问此信息：

```go
// 在应用层，中断后：
if event.Action != nil && event.Action.Interrupted != nil {
    interruptInfo := event.Action.Interrupted
    
    // 获取所有中断点的扁平列表
    interruptPoints := interruptInfo.InterruptContexts 
    
    for _, point := range interruptPoints {
        // 每个点都包含一个唯一的 ID、面向用户的信息及其层级地址
        fmt.Printf("Interrupt ID: %s, Address: %s, Info: %v\n", point.ID, point.Address.String(), point.Info)
    }
}
```

### 3. 用于最终用户恢复的 API

#### `(*Runner).TargetedResume`
使用“显式定向恢复”策略从检查点继续中断的执行。这是最常见和最强大的恢复方式，允许您定位特定的中断点并为其提供数据。

使用此方法时：
- 地址在 `targets` 映射中的组件将是显式目标。
- 地址不在 `targets` 映射中的被中断组件必须重新中断自己以保留其状态。

```go
func (r *Runner) TargetedResume(ctx context.Context, checkPointID string, 
    targets map[string]any, opts ...AgentRunOption) (*AsyncIterator[*AgentEvent], error)
```

**参数:**
- `ctx`: 用于恢复的上下文。
- `checkPointID`: 要从中恢复的检查点的标识符。
- `targets`: 中断 ID 到恢复数据的映射。这些 ID 可以指向整个执行图中的任何可中断组件。
- `opts`: 额外的运行选项。

**返回:** agent 事件的异步迭代器。

**示例:**
```go
// 收到中断事件后...
interruptID := interruptEvent.Action.Interrupted.InterruptContexts[0].ID

// 为特定中断点准备数据。
resumeData := map[string]any{
    interruptID: "这是您请求的澄清。",
}

// 使用目标数据恢复执行。
resumeIterator, err := runner.TargetedResume(ctx, "my-checkpoint-id", resumeData)
if err != nil {
    // 处理错误
}

// 处理来自恢复迭代器的事件
for event := range resumeIterator.Events() {
    if event.Err != nil {
        // 处理事件错误
        break
    }
    // 处理 agent 事件
    fmt.Printf("Event: %+v\n", event)
}
```

### 4. 用于开发者恢复的 API

#### `ResumeInfo` 结构体
`ResumeInfo` 持有恢复中断的 agent 执行所需的所有信息。它由框架创建并传递给 agent 的 `Resume` 方法。

```go
type ResumeInfo struct {
    // WasInterrupted 指示此 agent 是否是中断的直接来源。
    WasInterrupted bool

    // InterruptState 持有通过 StatefulInterrupt 或 CompositeInterrupt 保存的状态。
    InterruptState any

    // IsResumeTarget 指示此 agent 是否是 TargetedResume 的特定目标。
    IsResumeTarget bool

    // ResumeData 持有用户为此 agent 提供的数据。
    ResumeData any

    // ... 其他字段
}
```

**示例:**
```go
import (
    "context"
    "errors"
    "fmt"
    
    "github.com/cloudwego/eino/adk"
)

// 在 agent 的 Resume 方法内部：
func (a *myAgent) Resume(ctx context.Context, info *adk.ResumeInfo, opts ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
    if !info.WasInterrupted {
        // 在恢复流程中不应发生。
        return adk.NewAsyncIterator([]*adk.AgentEvent{{Err: errors.New("not an interrupt")}}, nil)
    }

    if !info.IsResumeTarget {
        // 此 agent 不是特定目标，因此必须重新中断以保留其状态。
        return adk.StatefulInterrupt(ctx, "等待工作流的另一部分被恢复", info.InterruptState)
    }

    // 此 agent 是目标。处理恢复数据。
    if info.ResumeData != nil {
        userInput, ok := info.ResumeData.(string)
        if ok {
            // 处理用户输入并继续执行
            fmt.Printf("收到用户输入: %s\n", userInput)
            // 根据用户输入更新 agent 状态
            a.currentState.LastUserInput = userInput
        }
    }
    
    // 继续正常执行逻辑
    return a.Run(ctx, &adk.AgentInput{Input: "resumed execution"})
}
```

## Compose 包 API

`compose` 包提供了用于创建复杂、可中断工作流的低级构建块。

### 1. 用于中断的 API

#### `Interrupt`
创建一个特殊错误，该错误向执行引擎发出信号，以在组件的特定地址处中断当前运行并保存检查点。这是单个、非复合组件发出可恢复中断信号的标准方式。

```go
func Interrupt(ctx context.Context, info any) error
```

**参数:**
- `ctx`: 正在运行组件的上下文，用于检索当前执行地址。
- `info`: 关于中断的面向用户的信息。此信息不会被持久化，但会通过 `InterruptCtx` 暴露给调用应用程序。

---

#### `StatefulInterrupt`
与 `Interrupt` 类似，但也保存组件的内部状态。状态保存在检查点中，并在恢复时通过 `GetInterruptState` 提供回组件。

```go
func StatefulInterrupt(ctx context.Context, info any, state any) error
```

**参数:**
- `ctx`: 正在运行组件的上下文。
- `info`: 关于中断的面向用户的信息。
- `state`: 中断组件需要持久化的内部状态。

---

#### `CompositeInterrupt`
创建一个表示复合中断的特殊错误。它专为“复合”节点（如 `ToolsNode`）或任何协调多个独立的、可中断子流程的组件而设计。它将多个子中断错误捆绑成一个单一的错误，引擎可以将其解构为可恢复点的扁平列表。

```go
func CompositeInterrupt(ctx context.Context, info any, state any, errs ...error) error
```

**参数:**
- `ctx`: 正在运行的复合节点的上下文。
- `info`: 复合节点本身的面向用户的信息（可以为 `nil`）。
- `state`: 复合节点本身的状态（可以为 `nil`）。
- `errs`: 来自子流程的错误列表。这些可以是 `Interrupt`、`StatefulInterrupt` 或嵌套的 `CompositeInterrupt` 错误。

**示例:**
```go
// 一个并行运行多个进程的节点。
var errs []error
for _, process := range processes {
    subCtx := compose.AppendAddressSegment(ctx, "process", process.ID)
    _, err := process.Run(subCtx)
    if err != nil {
        errs = append(errs, err)
    }
}

// 如果任何子流程中断，则将它们捆绑起来。
if len(errs) > 0 {
    // 复合节点可以保存自己的状态，例如，哪些进程已经完成。
    return compose.CompositeInterrupt(ctx, "并行执行需要输入", parentState, errs...)
}
```

### 2. 用于获取中断信息的 API

#### `ExtractInterruptInfo`
从 `Runnable` 的 `Invoke` 或 `Stream` 方法返回的错误中提取结构化的 `InterruptInfo` 对象。这是应用程序在执行暂停后获取所有中断点列表的主要方式。

```go
composeInfo, ok := compose.ExtractInterruptInfo(err)
if ok {
    // 访问中断上下文
    interruptContexts := composeInfo.InterruptContexts
}
```

**示例:**
```go
// 在调用一个中断的图之后...
_, err := graph.Invoke(ctx, "initial input")

if err != nil {
    interruptInfo, isInterrupt := compose.ExtractInterruptInfo(err)
    if isInterrupt {
        fmt.Printf("执行被 %d 个中断点中断。\n", len(interruptInfo.InterruptContexts))
        // 现在你可以检查 interruptInfo.InterruptContexts 来决定如何恢复。
    }
}
```

### 3. 用于最终用户恢复的 API

#### `Resume`
通过不提供数据来定位一个或多个组件，为“显式定向恢复”操作准备上下文。当恢复行为本身就是信号时，这很有用。

```go
func Resume(ctx context.Context, interruptIDs ...string) context.Context
```

**示例:**
```go
// 中断后，我们得到两个中断 ID：id1 和 id2。
// 我们想在不提供特定数据的情况下恢复两者。
resumeCtx := compose.Resume(context.Background(), id1, id2)

// 将此上下文传递给下一个 Invoke/Stream 调用。
// 在对应于 id1 和 id2 的组件中，GetResumeContext 将返回 isResumeFlow = true。
```

---

#### `ResumeWithData`
准备一个上下文以使用数据恢复单个特定组件。它是 `BatchResumeWithData` 的便捷包装器。

```go
func ResumeWithData(ctx context.Context, interruptID string, data any) context.Context
```

**示例:**
```go
// 使用特定数据恢复单个中断点。
resumeCtx := compose.ResumeWithData(context.Background(), interruptID, "这是您请求的特定数据。")

// 将此上下文传递给下一个 Invoke/Stream 调用。
```

---

#### `BatchResumeWithData`
这是准备恢复上下文的核心函数。它将恢复目标（中断 ID）及其相应数据的映射注入到上下文中。中断 ID 作为键存在的组件在调用 `GetResumeContext` 时将收到 `isResumeFlow = true`。

```go
func BatchResumeWithData(ctx context.Context, resumeData map[string]any) context.Context
```

**示例:**
```go
// 一次性恢复多个中断点，每个中断点使用不同的数据。
resumeData := map[string]any{
    "interrupt-id-1": "第一个点的数据。",
    "interrupt-id-2": 42, // 数据可以是任何类型。
    "interrupt-id-3": nil, // 等效于对此 ID 使用 Resume()。
}

resumeCtx := compose.BatchResumeWithData(context.Background(), resumeData)

// 将此上下文传递给下一个 Invoke/Stream 调用。
```

### 4. 用于开发者恢复的 API

#### `GetInterruptState`
提供一种类型安全的方式来检查和检索先前中断的持久化状态。这是组件用来了解其过去状态的主要函数。

```go
func GetInterruptState[T any](ctx context.Context) (wasInterrupted bool, hasState bool, state T)
```

**返回值:**
- `wasInterrupted`: 如果节点是先前中断的一部分，则为 `true`。
- `hasState`: 如果提供了状态并成功转换为类型 `T`，则为 `true`。
- `state`: 类型化的状态对象。

**示例:**
```go
// 在 lambda 或 tool 的执行逻辑内部：
wasInterrupted, hasState, state := compose.GetInterruptState[*MyState](ctx)

if wasInterrupted {
    fmt.Println("此组件在先前的运行中被中断。")
    if hasState {
        fmt.Printf("已恢复状态: %+v\n", state)
    } 
} else {
    // 这是此组件在此执行中第一次运行。
}
```

---

#### `GetResumeContext`
检查当前组件是否是恢复操作的目标，并检索用户提供的任何数据。这通常在 `GetInterruptState` 确认组件被中断后调用。

```go
func GetResumeContext[T any](ctx context.Context) (isResumeFlow bool, hasData bool, data T)
```

**返回值:**
- `isResumeFlow`: 如果组件被恢复调用明确指定为目标，则为 `true`。如果为 `false`，组件必须重新中断以保留其状态。
- `hasData`: 如果为此组件提供了数据，则为 `true`。
- `data`: 用户提供的类型化数据。

**示例:**
```go
// 在 lambda 或 tool 的执行逻辑内部，检查 GetInterruptState 之后：
wasInterrupted, _, oldState := compose.GetInterruptState[*MyState](ctx)

if wasInterrupted {
    isTarget, hasData, resumeData := compose.GetResumeContext[string](ctx)
    if isTarget {
        // 此组件是目标，继续执行逻辑。
        if hasData {
            fmt.Printf("使用用户数据恢复: %s\n", resumeData)
        }
        // 使用恢复的状态和恢复数据完成工作
        result := processWithStateAndData(state, resumeData)
        return result, nil
    } else {
        // 此组件不是目标，因此必须重新中断。
        return compose.StatefulInterrupt(ctx, "等待另一个组件被恢复", oldState)
    }
}
```

## 底层架构：寻址系统

### 对地址的需求

寻址系统旨在解决有效的human-in-the-loop交互中的三个基本需求：

1.  **状态附加**：要将本地状态附加到中断点，我们需要为每个中断点提供一个稳定、唯一的定位器。
2.  **定向恢复**：要为特定的中断点提供定向的恢复数据，我们需要一种精确识别每个点的方法。
3.  **中断定位**：要告诉最终用户中断在执行层级中的确切位置。

### 地址如何满足这些需求

地址系统通过三个关键属性满足这些需求：

- **稳定性**：地址在多次执行中保持一致，确保可以可靠地识别相同的中断点。
- **唯一性**：每个中断点都有一个唯一的地址，从而能够在恢复期间进行精确定位。
- **层级结构**：地址提供了一个清晰的层级路径，准确显示中断发生在执行流中的哪个位置。

### 地址结构和段类型

#### `Address` 结构
```go
type Address struct {
    Segments []AddressSegment
}

type AddressSegment struct {
    Type  AddressSegmentType
    ID    string
    SubID string
}
```

#### 地址结构图

以下图表从 ADK 和 Compose 两个层面说明了 `Address` 及其 `AddressSegment` 的层级结构：

**ADK 层视角** (简化的、以 Agent 为中心的视图):
```mermaid
graph TD
    A[Address] --> B[AddressSegment 1]
    A --> C[AddressSegment 2]
    A --> D[AddressSegment 3]
    
    B --> B1[Type: Agent]
    B --> B2[ID: A]
    
    C --> C1[Type: Agent]
    C --> C2[ID: B]
    
    D --> D1[Type: Tool]
    D --> D2[ID: search_tool]
    D --> D3[SubID: 1]
    
    style A fill:#e1f5fe
    style B fill:#f3e5f5
    style C fill:#f3e5f5
    style D fill:#f3e5f5
    style B1 fill:#e8f5e8
    style B2 fill:#e8f5e8
    style C1 fill:#e8f5e8
    style C2 fill:#e8f5e8
    style D1 fill:#e8f5e8
    style D2 fill:#e8f5e8
    style D3 fill:#e8f5e8
```

**Compose 层视角** (详细的、完整的层级视图):
```mermaid
graph TD
    A[Address] --> B[AddressSegment 1]
    A --> C[AddressSegment 2]
    A --> D[AddressSegment 3]
    A --> E[AddressSegment 4]
    
    B --> B1[Type: Runnable]
    B --> B2[ID: my_graph]
    
    C --> C1[Type: Node]
    C --> C2[ID: sub_graph]
    
    D --> D1[Type: Node]
    D --> D2[ID: tools_node]
    
    E --> E1[Type: Tool]
    E --> E2[ID: mcp_tool]
    E --> E3[SubID: 1]
    
    style A fill:#e1f5fe
    style B fill:#f3e5f5
    style C fill:#f3e5f5
    style D fill:#f3e5f5
    style E fill:#f3e5f5
    style B1 fill:#e8f5e8
    style B2 fill:#e8f5e8
    style C1 fill:#e8f5e8
    style C2 fill:#e8f5e8
    style D1 fill:#e8f5e8
    style D2 fill:#e8f5e8
    style E1 fill:#e8f5e8
    style E2 fill:#e8f5e8
    style E3 fill:#e8f5e8
```

### 特定层的地址段类型

#### ADK 层段类型
ADK 层提供了执行层级的简化、以 agent 为中心的抽象：

```go
type AddressSegmentType = core.AddressSegmentType

const (
    AddressSegmentAgent AddressSegmentType = "agent"
    AddressSegmentTool  AddressSegmentType = "tool"
)
```

**关键特性:**
- **Agent 段**: 表示 agent 级别的执行段（通常省略 `SubID`）。
- **Tool 段**: 表示 tool 级别的执行段（`SubID` 用于确保唯一性）。
- **简化视图**: 为 agent 开发者抽象掉底层复杂性。
- **示例路径**: `Agent:A → Agent:B → Tool:search_tool:1`

#### Compose 层段类型
`compose` 层对整个执行层级提供了细粒度的控制和可见性：

```go
type AddressSegmentType = core.AddressSegmentType

const (
    AddressSegmentRunnable AddressSegmentType = "runnable"  // Graph, Workflow, or Chain
    AddressSegmentNode     AddressSegmentType = "node"      // Individual graph nodes
    AddressSegmentTool     AddressSegmentType = "tool"      // Specific tool calls
)
```

**关键特性:**
- **Runnable 段**: 表示顶层可执行文件（Graph、Workflow、Chain）。
- **Node 段**: 表示执行图中的单个节点。
- **Tool 段**: 表示 `ToolsNode` 内的特定 tool 调用。
- **详细视图**: 提供对执行层级的完全可见性。
- **示例路径**: `Runnable:my_graph → Node:sub_graph → Node:tools_node → Tool:mcp_tool:1`

### 可扩展性与设计原则

地址段类型系统被设计为**可扩展的**。框架开发者可以添加新的段类型以支持额外的执行模式或自定义组件，同时保持向后兼容性。

**关键设计原则**：ADK 层提供简化的、以 agent 为中心的抽象，而 `compose` 层处理执行层级的全部复杂性。这种分层方法允许开发者在适合其需求的抽象级别上工作。

## 向后兼容性

human-in-the-loop框架保持与现有代码的完全向后兼容性。所有先前的中断和恢复模式将继续像以前一样工作，同时通过新的寻址系统提供增强的功能。

### 1. 图中断兼容性

在节点/工具中使用已弃用的 `NewInterruptAndRerunErr` 或 `InterruptAndRerun` 的先前图中断流程将继续被支持，但需要一个关键的额外步骤：**错误包装**。

由于这些遗留函数不是地址感知的，调用它们的组件有责任捕获错误，并使用 `WrapInterruptAndRerunIfNeeded` 辅助函数将地址信息包装进去。这通常在协调遗留组件的复合节点内部完成。

> **注意**：如果您选择**不**使用 `WrapInterruptAndRerunIfNeeded`，遗留行为将被保留。最终用户仍然可以像以前一样使用 `ExtractInterruptInfo` 从错误中获取信息。但是，由于产生的中断上下文将缺少正确的地址，因此将无法对该特定中断点使用新的定向恢复 API。要完全启用新的地址感知功能，必须进行包装。

```go
// 1. 一个使用已弃用中断的遗留工具
func myLegacyTool(ctx context.Context, input string) (string, error) {
    // ... tool 逻辑
    // 这个错误不是地址感知的。
    return "", compose.NewInterruptAndRerunErr("需要用户批准")
}

// 2. 一个调用遗留工具的复合节点
var legacyToolNode = compose.InvokableLambda(func(ctx context.Context, input string) (string, error) {
    out, err := myLegacyTool(ctx, input)
    if err != nil {
        // 关键：调用者必须包装错误以添加地址。
        // "tool:legacy_tool" 段将被附加到当前地址。
        segment := compose.AddressSegment{Type: "tool", ID: "legacy_tool"}
        return "", compose.WrapInterruptAndRerunIfNeeded(ctx, segment, err)
    }
    return out, nil
})

// 3. 最终用户代码现在可以看到完整地址。
_, err := graph.Invoke(ctx, input)
if err != nil {
    interruptInfo, exists := compose.ExtractInterruptInfo(err)
    if exists {
        // 中断上下文现在将拥有一个正确的、完全限定的地址。
        fmt.Printf("Interrupt Address: %s\n", interruptInfo.InterruptContexts[0].Address.String())
    }
}
```

**增强功能**：通过包装错误，`InterruptInfo` 将包含一个正确的 `[]*InterruptCtx`，其中包含完全限定的地址，从而允许遗留组件无缝集成到新的人机协同框架中。

### 2. 对编译时静态中断图的兼容性

通过 `WithInterruptBeforeNodes` 或 `WithInterruptAfterNodes` 添加的先前静态中断图继续有效，但状态处理的方式得到了显著改进。

当静态中断被触发时，会生成一个 `InterruptCtx`，其地址指向图本身。关键在于，`InterruptCtx.Info` 字段现在直接暴露了该图的状态。

这启用了一个更直接、更直观的工作流：
1.  最终用户收到 `InterruptCtx`，并可以通过 `.Info` 字段检查图的实时状态。
2.  他们可以直接修改这个状态对象。
3.  然后，他们可以通过 `ResumeWithData` 和 `InterruptCtx.ID` 将修改后的状态对象传回以恢复执行。

这种新模式通常不再需要使用旧的 `WithStateModifier` 选项，尽管为了完全的向后兼容性，该选项仍然可用。

```go
// 1. 定义一个拥有自己本地状态的图
type MyGraphState struct {
    SomeValue string
}

g := compose.NewGraph[string, string](compose.WithGenLocalState(func(ctx context.Context) *MyGraphState {
    return &MyGraphState{SomeValue: "initial"}
}))
// ... 向图中添加节点1和节点2 ...

// 2. 使用静态中断点编译图
// 这将在 "node_1" 节点完成后中断图本身。
graph, err := g.Compile(ctx, compose.WithInterruptAfterNodes([]string{"node_1"}))

// 3. 运行图，这将触发静态中断
_, err = graph.Invoke(ctx, "start")

// 4. 提取中断上下文和图的状态
interruptInfo, isInterrupt := compose.ExtractInterruptInfo(err)
if isInterrupt {
    interruptCtx := interruptInfo.InterruptContexts[0]

    // .Info 字段暴露了图的当前状态
    graphState, ok := interruptCtx.Info.(*MyGraphState)
    if ok {
        // 5. 直接修改状态
        fmt.Printf("Original state value: %s\n", graphState.SomeValue) // 打印 "initial"
        graphState.SomeValue = "a-new-value-from-user"

        // 6. 通过传回修改后的状态对象来恢复
        resumeCtx := compose.ResumeWithData(context.Background(), interruptCtx.ID, graphState)
        result, err := graph.Invoke(resumeCtx, "start")
        // ... 执行将继续，并且 node_2 现在将看到修改后的状态。
    }
}
```

### 3. Agent 中断兼容性

与旧版 agent 的兼容性是在数据结构层面维护的，确保了旧的 agent 实现能在新框架内继续运作。其关键在于 `adk.InterruptInfo` 和 `adk.ResumeInfo` 结构体是如何被填充的。

**对最终用户（应用层）而言：**
当从 agent 收到一个中断时，`adk.InterruptInfo` 结构体中会同时填充以下两者：
- 新的、结构化的 `InterruptContexts` 字段。
- 遗留的 `Data` 字段，它将包含原始的中断信息（例如 `ChatModelAgentInterruptInfo` 或 `WorkflowInterruptInfo`）。

这使得最终用户可以逐步迁移他们的应用逻辑来使用更丰富的 `InterruptContexts`，同时在需要时仍然可以访问旧的 `Data` 字段。

**对 Agent 开发者而言：**
当一个旧版 agent 的 `Resume` 方法被调用时，它收到的 `adk.ResumeInfo` 结构体仍然包含现已弃用的嵌入式 `InterruptInfo` 字段。该字段被填充了相同的遗留数据结构，允许 agent 开发者维持其现有的恢复逻辑，而无需立即更新到新的地址感知 API。

```go
// --- 最终用户视角 ---

// 在 agent 运行后，你收到了一个中断事件。
if event.Action != nil && event.Action.Interrupted != nil {
    interruptInfo := event.Action.Interrupted

    // 1. 新方式：访问结构化的中断上下文
    if len(interruptInfo.InterruptContexts) > 0 {
        fmt.Printf("New structured context available: %+v\n", interruptInfo.InterruptContexts[0])
    }

    // 2. 旧方式（仍然有效）：访问遗留的 Data 字段
    if chatInterrupt, ok := interruptInfo.Data.(*adk.ChatModelAgentInterruptInfo); ok {
        fmt.Printf("Legacy ChatModelAgentInterruptInfo still accessible.\n")
        // ... 使用旧结构体的逻辑
    }
}


// --- Agent 开发者视角 ---

// 在一个旧版 agent 的 Resume 方法内部：
func (a *myLegacyAgent) Resume(ctx context.Context, info *adk.ResumeInfo) *adk.AsyncIterator[*adk.AgentEvent] {
    // 已弃用的嵌入式 InterruptInfo 字段仍然会被填充。
    // 这使得旧的恢复逻辑可以继续工作。
    if info.InterruptInfo != nil {
        if chatInterrupt, ok := info.InterruptInfo.Data.(*adk.ChatModelAgentInterruptInfo); ok {
            // ... 依赖于旧的 ChatModelAgentInterruptInfo 结构体的现有恢复逻辑
            fmt.Println("Resuming based on legacy InterruptInfo.Data field.")
        }
    }
    
    // ... 继续执行
    return a.Run(ctx, &adk.AgentInput{Input: "resumed execution"})
}
```

### 迁移优势

- **保留遗留行为**: 现有代码将继续按其原有方式运行。旧的中断模式不会导致程序崩溃，但它们也不会在不经修改的情况下自动获得新的地址感知能力。
- **渐进式采用**: 团队可以根据具体情况选择性地启用新功能。例如，你可以只在你需要定向恢复的工作流中，用 `WrapInterruptAndRerunIfNeeded` 来包装遗留的中断。
- **增强的功能**: 新的寻址系统为所有中断提供了更丰富的结构化上下文 (`InterruptCtx`)，同时旧的数据字段仍然会被填充以实现完全兼容。
- **灵活的状态管理**: 对于静态图中断，你可以选择通过 `.Info` 字段进行现代、直接的状态修改，或者继续使用旧的 `WithStateModifier` 选项。

这种向后兼容性模型确保了现有用户的平滑过渡，同时为采用强大的新的 human-in-the-loop 交互功能提供了清晰的路径。

## 实现示例

有关human-in-the-loop模式的完整、可工作的示例，请参阅 [eino-examples repository](https://github.com/cloudwego/eino-examples/pull/125)。该仓库包含四个作为独立示例实现的典型模式：

### 1. 审批模式
在关键工具调用之前的简单、显式批准。非常适合不可逆操作，如数据库修改或金融交易。

### 2. 审查与编辑模式  
高级模式，允许在执行前进行人工审查和原地编辑工具调用参数。非常适合纠正误解。

### 3. 反馈循环模式
迭代优化模式，其中 agent 生成内容，人类提供定性反馈以进行改进。

### 4. 追问模式
主动模式，其中 agent 识别出不充分的工具输出并请求澄清或下一步行动。

这些示例演示了中断/恢复机制的实际用法，并附有可重用的工具包装器和详细文档。