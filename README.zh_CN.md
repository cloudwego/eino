# Eino

![coverage](https://raw.githubusercontent.com/cloudwego/eino/badges/.badges/main/coverage.svg)
[![Release](https://img.shields.io/github/v/release/cloudwego/eino)](https://github.com/cloudwego/eino/releases)
[![WebSite](https://img.shields.io/website?up_message=cloudwego&url=https%3A%2F%2Fwww.cloudwego.io%2F)](https://www.cloudwego.io/)
[![License](https://img.shields.io/github/license/cloudwego/eino)](https://github.com/cloudwego/eino/blob/main/LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/cloudwego/eino)](https://goreportcard.com/report/github.com/cloudwego/eino)
[![OpenIssue](https://img.shields.io/github/issues/cloudwego/eino)](https://github.com/cloudwego/kitex/eino)
[![ClosedIssue](https://img.shields.io/github/issues-closed/cloudwego/eino)](https://github.com/cloudwego/eino/issues?q=is%3Aissue+is%3Aclosed)
![Stars](https://img.shields.io/github/stars/cloudwego/eino)
![Forks](https://img.shields.io/github/forks/cloudwego/eino)

[English](README.md) | 中文

# 简介

**Eino['aino]** (近似音: i know，希望应用程序达到 "i know" 的愿景) 旨在提供 Golang 语言的 AI 应用开发框架。 Eino
参考了开源社区中诸多优秀的 AI 应用开发框架，例如 LangChain、LangGraph、LlamaIndex 等，提供了更符合 Golang 编程习惯的 AI
应用开发框架。

Eino 提供了丰富的辅助 AI 应用开发的原子组件、集成组件、组件编排、切面扩展等能力，可以帮助开发者更加简单便捷地开发出架构清晰、易维护、高可用的
AI 应用。

Eino 可在 AI 应用开发周期中的不同阶段，规范、简化和提效：

![](.github/static/img/eino/eino_framework.png)

# 主要特性

## 丰富的组件(Component)

- 将多场景普遍使用的能力，抽象成可独立、可编排使用的组件，开箱即用

    - 例如 ChatModel、PromptTemplate、Retriever、Loader 等原子组件
    - 每一种 Component 均有接口抽象及其对应的多种实现
        - 编排时，对应节点的输入输出与接口抽象保持一致

- 组件又可细分为：功能不可细拆的原子组件、由一到多中组件以某种范式组合而成的集成组件

    - 集成组件：React Agent、MultiQueryRetriver、Host MultiAgent 等

## 易用的图编排(Graph/Chain)

- 将各组件实例，作为图的节点，以图的点边关系连接，以边的方向逐步执行节点并传输数据流，将 AI 应用的逻辑以图的方式进行编排和执行。
- 图编排可极大简化 **并行、流式(异步)** 逻辑的开发，并优化其代码结构

![](.github/static/img/eino/graph.gif)

> 使用“数据处理工厂”来比喻应用程序的构建。图编排将应用定义为：独立“黑箱(节点)
> ”的网络，这些“黑箱”通过数据包在预定义的连接上进行通信。“黑箱”可以在不同场景被连接，形成不同的应用程序，而无需在内部进行更改。这里一个“黑箱”代表一种
> Component，因此 Graph 编排具有 Component 导向的特性。

**图编排**提供一套简洁的「绘画」设计语义，通过添加 **点(计算逻辑)** 和 **线(数据流)**，让开发者能够通过形象化的“作图”，将
Component 中提供的各个基础组件编排为草稿（Draft），进而可以选择按需复用或是编译为一个 LLM 应用。 简而言之：

> Nodes do the work, Edges tell what to do next.

**图编排**中所涉及的元素和概念可以用：点、线、面、切面 四个词进行概括。

图编排在 Eino 中表现为：Graph、Chain

## 完善的流处理(Streaming)

在类似 LLM 构建的长耗时应用的场景下，**流处理**在让终端用户觉得系统在实时响应方面至关重要

根据**输入、输出是否为流式**，可划分成 4 种交互模式。而不同的组件则仅能提供这 4 种交互模式中的一种或多种(绝大部分是 Invoke
能力，少数是 Stream 能力， 极少数是 Transform 能力)，如何将具有不同交互模式的组件，通过图编排连接并进行数据流的流转，对用户来说是不可避免的困难。

图编排可根据上下游节点的输入、输出是否是流，自动进行 流 和 非流 的转换，使得开发者可不再关注上下游节点的流处理关系能否匹配，极大地方便开发者对
AI 应用提供流处理的能力

## 高扩展性的切面(Callbacks)

Eino 像 LangChain 一样，提供了完善地回调系统，允许开发者在 AI 应用的各个阶段嵌入自己的钩子。方便开发者利用这些钩子扩展日志记录、监控、流式处理
等功能。

开发者可通过实现 callbacks.Handler、compose.GraphCompileCallback 等钩子对象，来订阅 AI 应用在构建、运行时各个阶段的事件。

站在图编排的视角来说，Eino 为图、节点的执行前后提供切面的注入、运行的机制。开发者可基于此机制，在不侵入主流程的前提下，灵活地设计和注入自己的切面能力。例如
Trace、埋点、日志等

# Eino 框架结构

![](.github/static/img/eino/eino_composite.png)

Eino 框架整体由 三部分 构成：

- Eino(Eino Core)：存放 Eino 的组件抽象，Graph、Chain 等编排能力，切面机制等
- EinoExt：Eino 的组件实现、通用切面实现、组件使用示例等，可放置各种各样的Eino扩展能力
- Eino Devops：Eino 相关的开发、调试、评测等可视化、管理能力。

Eino Core 中的六大概念：

- Components 抽象

    - 每一种 Component 均有对应的接口抽象及其对应的多种实现。可直接使用、也可被编排
        - 编排时，对应节点的输入输出与接口抽象保持一致
    - 类似于 ChatModel、PromptTemplate、Retriever、Indexer 等开箱即用的原子组件
    - Eino 中 Component 概念较为宽松，任意满足如下职责中的一个，即可被称为 Component
        - 可加入 Graph Node，作为编排对象被编排
        - 作为其他编排对象的依赖注入组件
- Flow 集成组件

    - 基于框架中的 Component、Graph ，针对常见的应用场景，提供开箱即用的预先编排好的集成组件能力。
    - 可能提供再次被编排的能力
    - 例如：Agent、MultiAgent、Multi Retriever 等
- Runnable -- 用户弱感知

    - 编排框架中的编排对象和编排产物。
    - 所有的 Component 在被编排时，均需转换成 Runnable 对象，一般用户不可见此过程。
    - 一张图编译成可执行对象时，本质是一个 Runnable 对象
- Compose 编排

    - 将各种 Component 实例，作为 Node 节点，以图的点线关系连接起来，数据流按照有向边的方向进行传输，并在不同节点中执行。
    - 支持 Graph、Chain、Workflow 等多种编排形式，本质均是通过有向图表达数据流的传递和节点的执行顺序
- 切面能力

    - Graph 中每个节点执行前后提供的切面能力。
    - 例如：Trace、埋点、日志等
- Stream

    - 添加到 Node 中的组件实例，其输入、输出既有可能是 流、也有可能是 非流。 Compose 编排可以将这些不同形式的输入输出进行衔接，传递数据流并执行节点。
      这个能力可称为流式编排能力
    - 例如，ChatModel 的输出、ASR 的输入输出 都是流式的

## Component

具体每种 Component 的职责，可具体看对应的接口定义

> 下文是示例性说明，不完整，以[代码仓库](https://github.com/cloudwego/eino-ext/tree/main/components)为准

```
eino/components // 组件根目录
├── document
│   ├── interface.go 
│   └── option.go
├── embedding
│   ├── callback_extra.go
│   ├── interface.go // 一个组件的抽象
│   ├── ark          // 与抽象同级的一个文件夹代表一种具体实现
│   ├── openai
│   └── option.go
├── indexer
│   ├── callback_extra.go
│   ├── interface.go
│   ├── option.go
│   └── volc_vikingdb
├── model
│   ├── callback_extra.go
│   ├── interface.go
│   ├── ark
│   ├── openai
│   └── option.go
├── prompt
│   ├── callback_extra.go
│   ├── chat_template.go
│   ├── chat_template_test.go
│   └── interface.go
├── retriever
│   ├── callback_extra.go
│   ├── interface.go
│   ├── option.go
│   └── volc_vikingdb
├── tool
│   ├── duckduckgo
│   ├── interface.go
│   └── option.go
├── types.go
```


## Runnable

```go
type Runnable[I, O any] interface {
    Invoke(ctx context.Context, input I, opts ...Option) (output O, err error)
    Stream(ctx context.Context, input I, opts ...Option) (output *schema.StreamReader[O], err error)
    Collect(ctx context.Context, input *schema.StreamReader[I], opts ...Option) (output O, err error)
    Transform(ctx context.Context, input *schema.StreamReader[I], opts ...Option) (output *schema.StreamReader[O], err error)
}
```

- Runnable 抽象根据输入、输出是否为流式，划分成 4 个 Lamba 算子
- Compose 编排中，添加到 Node 中的组件实例，会被统一转换成上述的 Runnable 抽象
- 当一个 Component 转换为 Runnable 时，根据其提供的任意 Lambda 算子，结合着 流化(Streaming)、合并(Concat) 能力，补全剩余的未提供的
  Lambda 算子

    - 流与非流间的转换：
      > 用 StreamReader[T] 和 T 分别指代 流 和 非流
        - 合并(Concat)
            - 将 StreamReader[T] 中的 T-Frame 接收完整，并合并成一个完整的 T
        - 流化(Streaming)
            - 将 T 转换成仅有一个 T-Frame 的 StreamReader[T]，进行流式传输
- 基于上述两种转换关系，Eino 便可根据用户提供的具有任意 N(N<=4) 种交互模式的接口，封装转换成一个完整的 Runnable[I, O]

<table>
<tr>
<td>源\目标<br/></td><td>Invoke[I, O any]()<br/></td><td>Stream[I, O any]()<br/></td><td>Collect[I, O any]()<br/></td><td>Transform[I, O any]()<br/></td></tr>
<tr>
<td>Invoke[I, O any]()<br/></td><td>-<br/></td><td>- Invoke输入直接透传<br/>- Invoke响应转成单帧流<br/></td><td>- Invoke输入转成单帧流<br/>- Invoke响应直接透传<br/></td><td>- Invoke输入转成单帧流<br/>- Invoke响应转成单帧流<br/></td></tr>
<tr>
<td>Stream[I, O any]()<br/></td><td>- Stream输入直接透传<br/>- Stream输出Concat后透传<br/></td><td>-<br/></td><td>- Stream输入转成单帧流<br/>- Stream输出Concat后透传<br/></td><td>- Stream输入转成单帧流<br/>- Stream输出直接透传<br/></td></tr>
<tr>
<td>Collect[I, O any]()<br/></td><td>- Collect输入Concat后透传<br/>- Collect输出直接透传<br/></td><td>- Collect输入Concat后透传<br/>- Collect输出转成单帧流<br/></td><td>-<br/></td><td>- Collect输入直接透传<br/>- Collect输出转成单帧流<br/><br/></td></tr>
<tr>
<td>Transform[I, O any]()<br/></td><td>- Transform输入Concat后透传<br/>- Transform输出Concat后透传<br/></td><td>- Transform输入Concat后透传<br/>- Transform输出直接透传<br/></td><td>- Transform输入直接透传<br/>- Transform输出Concat后透传<br/></td><td>-<br/></td></tr>
</table>

- 编程产物中具有的真正的流式能力是什么，取决于如下的编排范式

![](.github/static/img/eino/stream.png)

## Stream 流

Notice：Stream 流在 **生产**、**消费**、**复制**、**合并**、**转换**等场景下，处理逻辑均较为复杂。 实现时稍有考虑不周的地方，便可能导致
生产/消费者互相等待而夯死、Goroutine 泄露或溢出、内存泄露或溢出、CPU 负载高 等问题。 为了减少稳定性问题的产生，Eino 强要求使用
Eino 提供的 Stream 流，因此将 Stream 实现成了 Struct、而非定义成接口。

复杂的流操作的场景：

- 流式接口和非流式接口直接的转换

    - 流转成非流时，需要将流中的所有数据帧，Concat 成一个完整的数据结构
    - 非流转成流时，需要将一个数据结构转换成仅有一个数据帧的流
- 同一个数据流可能需要被多次读取和消费，比如多个切面。 因一个流仅能被完成读取一次，所以需要根据消费次数，将流进行 Copy

    - 流 Copy 时，需要考虑多个流的 消费协同、Close 协同。 任意一个流没有正常 Close，均可能导致资源无法正常释放
- 多个流合并成一个流

为了 Stream 的 API 接口更加清晰和易用，遂与 Golang 内置的 io.Pipe() 方法的定义对齐。

- API 接口定义为：`schema.Pipe[T any](cap int) (*StreamReader[T], *StreamWriter[T])`

    - 其中 cap 表示 Stream 的缓存有多大，即在无任何消费的情况下，Sender 可以无阻塞地发送 Chunk 的数量
    - `StreamWriter` 类似于 io.Pipe 中的 PipeWriter
    - `StreamReader` 类似于 io.Pipe 中的 PipeReader，只是多了一个 `Copy(n int) []*StreamReader[T]` 方法
- **WARN**：在任何地方见到 `*StreamReader[T]` 或 `*StreamWriter[T]` 都不要忘记 Close()，否则可能导致流无法正常释放。一般流的生产和消费都是单独
  Goroutine，从而导致 Goroutine 的泄露。

Stream 流 的 API 设计，源码链接：[eino/schema/stream.go](https://github.com/cloudwego/eino/blob/main/schema/stream.go)

## Compose 编排

### Graph

#### 点(Node)

- 把一个 Component 实例加入到 Graph 中，便形成一个 Node 节点
- Component 即可被独立使用，又可被 Graph 编排
- Add{Component}Node() 接口列举。 此处仅列举几个接口，更详细接口列表可查看最新的 Eino SDK

    - 对于通用的组件类型，均会抽象出一个标准行为语义，并给出不同的实现
    - 业务可通过使用 AddLambdaNode，添加任意定制函数作为节点

```go
// AddChatModelNode add node that implements model.ChatModel.
func (g *graph) AddChatModelNode(key string, node model.ChatModel, opts ...GraphAddNodeOpt) error {
return g.addNode(key, toChatModelNode(key, node, opts...))
}

// AddChatTemplateNode add node that implements prompt.ChatTemplate.
func (g *graph) AddChatTemplateNode(key string, node prompt.ChatTemplate, opts ...GraphAddNodeOpt) error {
return g.addNode(key, toChatTemplateNode(key, node, opts...))
}

func (g *graph) AddToolsNode(key string, node *ToolsNode, opts ...GraphAddNodeOpt) error {
return g.addNode(key, toToolsNode(key, node, opts...))
}

// AddLambdaNode add node that implements at least one of Invoke[I, O], Stream[I, O], Collect[I, O], Transform[I, O].
// due to the lack of supporting method generics, we need to use function generics to generate Lambda run as Runnable[I, O].
// for Invoke[I, O], use compose.InvokableLambda()
// for Stream[I, O], use compose.StreamableLambda()
// for Collect[I, O], use compose.CollectableLambda()
// for Transform[I, O], use compose.TransformableLambda()
// for arbitrary combinations of 4 kinds of lambda, use compose.AnyLambda()
func (g *graph) AddLambdaNode(key string, node *Lambda, opts ...GraphAddNodeOpt) error {
return g.addNode(key, toLambdaNode(key, node, opts...))
}

// AddGraphNode add one kind of Graph[I, O]、Chain[I, O]、StateChain[I, O, S] as a node.
// for Graph[I, O], comes from NewGraph[I, O]()
// for Chain[I, O], comes from NewChain[I, O]()
// for StateGraph[I, O, S], comes from NewStateGraph[I, O, S]()
func (g *graph) AddGraphNode(key string, node AnyGraph, opts ...GraphAddNodeOpt) error {
return g.addNode(key, toAnyGraphNode(key, node, opts...))
}

func (g *graph) AddRetrieverNode(key string, node retriever.Retriever, opts ...GraphAddNodeOpt) error {
return g.addNode(key, toRetrieverNode(key, node, opts...))
}
```

#### 线(Edge)

Eino 提供了多种添加线的方式

##### Add**Edge**

```go
// AddEdge adds an edge to the graph, edge means a data flow from startNode to endNode.
// the previous node's output type must be set to the next node's input type.
// NOTE: startNode and endNode must have been added to the graph before adding edge.
// e.g.
//
//  graph.AddNode("start_node_key", compose.NewPassthroughNode())
//  graph.AddNode("end_node_key", compose.NewPassthroughNode())
//
//  err := graph.AddEdge("start_node_key", "end_node_key")
func (g *graph) AddEdge(startNode, endNode string) (err error) {}
```

- 在两个节点间添加一条有向的数据传输链路，以控制数据的流动方向和节点的执行顺序

![](.github/static/img/eino/graph_add_edge.png)

##### **AddBranch**

```go
// AddBranch adds a branch to the graph.
// e.g.
//
//  condition := func(ctx context.Context, in string) (string, error) {
//     return "next_node_key", nil
//  }
//  endNodes := map[string]bool{"path01": true, "path02": true}
//  branch := compose.NewGraphBranch(condition, endNodes)
//
//  graph.AddBranch("start_node_key", branch)
func (g *graph) AddBranch(startNode string, branch *GraphBranch) (err error) {}
```

- 根据传入的自定义选择函数，运行时根据经运算条件从多个 Node 中选出命中 Node 执行

![](.github/static/img/eino/graph_add_branch.png)

##### **Parallel**

- 将多个 Node 平行并联， 形成多个节点并发执行的节点
- 无 AddParallel 方法，通过 AddEdge 构建并联的多条拓扑路径，以次形成 **Parallel **

![](.github/static/img/eino/graph_parallel.png)

#### 面(Graph)

- 通过 NewGraph 创建 graph 实例，并通过 graph.AddXXXNode、graph.AddEdge、graph.AddBranch 绘制点和线，最终形成一张可编译执行的图

```go
// 无状态的 Graph 编排
g := NewGraph[map[string]any, *schema.Message]()

type testState struct {
ms []string
}

genFn := func (ctx context.Context) *testState {
return &testState{}
}

// 有状态的 Graph 编排
sg := NewGraph[string, string](WithGenLocalState(genFn))

// 基于 Graph 编排简化 的 Chain
chain := NewChain[map[string]any, string]()
```

### Chain

> Chain - 简化的 Graph，将不同类型的 Node 按照先后顺序，进行连接，形成从头到尾的数据流传递和顺序执行。

#### **AppendXXX**

> XXX 可是 ChatMode、Prompt、Indexer、Retriever、Graph 等多种组件类型
>
> Chain 是简化的 Graph，因此可通过 AppendGraph 实现 Chain 和 Graph 的相互嵌套

- 将多个 Node 按照传入顺序首尾串联，串联的 Node 依次进行数据传递和执行

![](.github/static/img/eino/chain.png)

#### **AppendParallel**

> 添加一个节点，这个节点具有多个并发执行的多个子节点

```go
// Parallel run multiple nodes in parallel
//
// use `NewParallel()` to create a new parallel type
// Example:

parallel := NewParallel()
parallel.AddChatModel("output_key01", chat01)
parallel.AddChatModel("output_key01", chat02)

chain := NewChain[any, any]()
chain.AppendParallel(parallel)
```

- 创建一个 Parallel，容纳并发执行的多个子节点

![](.github/static/img/eino/chain_append_parallel.png)

#### **AppendBranch**

> 添加一个节点，这个节点通过 condition 计算方法，从多个子节点中，选择一个执行

```go
// NewChainBranch creates a new ChainBranch instance based on a given condition.
// It takes a generic type T and a GraphBranchCondition function for that type.
// The returned ChainBranch will have an empty key2BranchNode map and a condition function
// that wraps the provided cond to handle type assertions and error checking.
// eg.

condition := func (ctx context.Context, in string, opts ...any) (endNode string, err error) {
// logic to determine the next node
return "some_next_node_key", nil
}

cb := NewChainBranch[string](condition)
cb.AddPassthrough("next_node_key_01", xxx) // node in branch, represent one path of branch
cb.AddPassthrough("next_node_key_02", xxx) // node in branch

chain := NewChain[string, string]()
chain.AppendBranch(cb)
```

![](.github/static/img/eino/chain_append_branch.png)

## 切面(Callbacks)

- Component（包括Lambda）、Graph 编排共同解决“把业务逻辑定义出来”的问题。而 logging, tracing, metrics,
  上屏展示等横切面性质的功能，需要有机制把功能注入到 Component（包括 Lambda）、Graph 中。

- 另一方面，用户可能想拿到某个具体 Component 实现的执行过程中的中间信息，比如 VikingDBRetriever 额外给出查询的 DB
  Name，ArkChatModel 额外给出请求的 temperature 等参数。需要有机制把中间状态透出。

- Callbacks 支持“**横切面功能注入**”和“**中间状态透出**”，具体是：用户提供、注册“function”（Callback Handler），Component 和
  Graph 在固定的“时机”（或者说切面、位点）回调这些 function，给出对应的信息。

- Eino 中的 Component 和 Graph 等**实体**，在固定的**时机** (Callback Timing)，回调用户提供的 **function** (Callback
  Handler)，并把**自己是谁** (RunInfo)，以及**当时发生了什么** (Callback Input & Output) 传出去。

## 详细文档

// TODO：链接用户手册等文档

## 相关项目

- [EinoExt](https://github.com/cloudwego/eino-ext)：Eino 的组件实现、通用切面实现、组件使用示例等，可放置各种各样的Eino扩展能力。
- [EinoExamples](https://github.com/cloudwego/eino-examples)：- Eino 相关的 编排能力的Demo、场景化AI应用的Demo等。

## 安全

如果你在该项目中发现潜在的安全问题，或你认为可能发现了安全问题，请通过我们的[安全中心](https://security.bytedance.com/src)
或[漏洞报告邮箱](sec@bytedance.com)通知字节跳动安全团队。

请**不要**创建公开的 GitHub Issue。

## 联系我们

- 如何成为 member: [COMMUNITY MEMBERSHIP](https://github.com/cloudwego/community/blob/main/COMMUNITY_MEMBERSHIP.md)
- Issues: [Issues](https://github.com/cloudwego/eino/issues)
- 飞书用户群（[注册飞书](https://www.feishu.cn/)后扫码进群）
![LarkGroup](.github/static/img/eino/lark_group_zh.png)

## 开源许可证

本项目依据 [Apache-2.0 许可证](LICENSE.txt) 授权。
