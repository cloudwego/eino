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

English | [中文](README.zh_CN.md)

# Overview

**Eino['aino]** (pronounced similarly to "I know") aims to be the ultimate LLM application development framework in Golang. Drawing inspirations from many excellent LLM application development frameworks in the open-source community such as LangChain & LlamaIndex, etc., as well as learning from cutting-edge research and real world applications, Eino offers an LLM application development framework that emphasizes on simplicity, scalability, reliability and effectiveness that better aligns with Golang programming conventions.

What Eino provides are:
- a carefully curated list of **component** abstractions and implementations that can be easily reused and combined to build LLM applications
- a powerful **composition** framework that does the heavy lifting of strong type checking, stream processing, concurrency management, aspect injection, option assignment, etc. for the user.
- a set of meticulously designed **API** that obsesses on simplicity and clarity.
- an ever-growing collection of best practices in the form of bundled **flows** and **examples**.
- a useful set of tools that covers the entire development cycle, from visualized development and debugging to online tracing and evaluation.

With the above arsenal, Eino can standardize, simplify, and improve efficiency at different stages of the AI application development cycle:
![](.github/static/img/eino/eino_concept.jpeg)

# A quick walkthrough

Use a component directly:
```Go
model, _ := openai.NewChatModel(ctx, config) // create an invokable LLM instance
message, _ := model.Generate(ctx, []*Message{
    SystemMessage("you are a helpful assistant."),
    UserMessage("what does the future AI App look like?")})
```

Of course, you can do that, Eino provides lots of useful components to use out of the box. But you can do more by using orchestration, for three reasons:
- orchestration encapsulates common patterns of LLM application.
- orchestration solves the difficult problem of processing stream response by the LLM.
- orchestration handles type safety, concurrency management, aspect injection and option assignment for you.

Eino provides three set of APIs for orchestration

| API      | Characteristics and usage                                             |
| -------- |-----------------------------------------------------------------------|
| Chain    | Simple chained directed graph that can only go forward.               |
| Graph    | Cyclic or Acyclic directed graph. Powerful and flexible.              |
| Workflow | Acyclic graph that supports data mapping at struct field level. |

Let's create a simple chain: a ChatTemplate followed by a ChatModel.

![](.github/static/img/eino/simple_chain.png)

```Go
chain, _ := NewChain[map[string]any, *Message]().
           AppendChatTemplate(prompt).
           AppendChatModel(model).
           Compile(ctx)

chain.Invoke(ctx, map[string]any{"query": "what's your name?"})
```

Now let's create a graph that uses a ChatModel to generate answer or tool calls, then uses a ToolsNode to execute those tools if needed.

![](.github/static/img/eino/tool_call_graph.png)

```Go
graph := NewGraph[map[string]any, *schema.Message]()

_ = graph.AddChatTemplateNode("node_template", chatTpl)
_ = graph.AddChatModelNode("node_model", chatModel)
_ = graph.AddToolsNode("node_tools", toolsNode)
_ = graph.AddLambdaNode("node_converter", takeOne)

_ = graph.AddEdge(START, "node_template")
_ = graph.AddEdge("node_template", "node_model")
_ = graph.AddBranch("node_model", branch)
_ = graph.AddEdge("node_tools", "node_converter")
_ = graph.AddEdge("node_converter", END)

compiledGraph, err := graph.Compile(ctx)
if err != nil {
return err
}
out, err := compiledGraph.Invoke(ctx, map[string]any{"query":"Beijing's weather this weekend"})
```

Now let's create a workflow that flexibly maps input & output at the field level:

![](.github/static/img/eino/simple_workflow.png)

```Go
type Input1 struct {
    Input string
}

type Output1 struct {
    Output string
}

type Input2 struct {
    Role schema.RoleType
}

type Output2 struct {
    Output string
}

type Input3 struct {
    Query string
    MetaData string
}

var (
    ctx context.Context
    m model.BaseChatModel
    lambda1 func(context.Context, Input1) (Output1, error)
    lambda2 func(context.Context, Input2) (Output2, error)
    lambda3 func(context.Context, Input3) (*schema.Message, error)
)

wf := NewWorkflow[[]*schema.Message, *schema.Message]()
wf.AddChatModelNode("model", m).AddInput(START)
wf.AddLambdaNode("lambda1", InvokableLambda(lambda1)).
    AddInput("model", MapFields("Content", "Input"))
wf.AddLambdaNode("lambda2", InvokableLambda(lambda2)).
    AddInput("model", MapFields("Role", "Role"))
wf.AddLambdaNode("lambda3", InvokableLambda(lambda3)).
    AddInput("lambda1", MapFields("Output", "Query")).
    AddInput("lambda2", MapFields("Output", "MetaData"))
wf.End().AddInput("lambda3")
runnable, err := wf.Compile(ctx)
if err != nil {
    return err
}
our, err := runnable.Invoke(ctx, []*schema.Message{
    schema.UserMessage("kick start this workflow!"),
})
```

Now let's create a 'ReAct' agent: A ChatModel binds to Tools. It receives input Messages and decides independently whether to call the Tool or output the final result. The execution result of the Tool will again become the input Message for the ChatModel and serve as the context for the next round of independent judgment.

![](.github/static/img/eino/react.png)

We provide a complete implementation for ReAct Agent out of the box in the `flow` package. Check out the code here: [flow/agent/react](https://github.com/cloudwego/eino/blob/main/flow/agent/react/react.go)

Our implementation of ReAct Agent uses Eino's **graph orchestration** exclusively, which provides the following benefits out of the box:
- Type checking: it makes sure the two nodes' input and output types match at compile time.
- Stream processing: concatenates message stream before passing to chatModel and toolsNode if needed, and copies the stream into callback handlers.
- Concurrency management: the shared state can be safely read and written because the StatePreHandler is concurrency safe.
- Aspect injection: injects callback aspects before and after the execution of ChatModel if the specified ChatModel implementation hasn't injected itself.
- Option assignment: call options are assigned either globally, to specific component type or to specific node.

For example, you could easily extend the compiled graph with callbacks:
```Go
handler := NewHandlerBuilder().
  OnStartFn(
    func(ctx context.Context, info *RunInfo, input CallbackInput) context.Context) {
        log.Infof("onStart, runInfo: %v, input: %v", info, input)
    }).
  OnEndFn(
    func(ctx context.Context, info *RunInfo, output CallbackOutput) context.Context) {
        log.Infof("onEnd, runInfo: %v, out: %v", info, output)
    }).
  Build()
  
compiledGraph.Invoke(ctx, input, WithCallbacks(handler))
```

or you could easily assign options to different nodes:
```Go
// assign to All nodes
compiledGraph.Invoke(ctx, input, WithCallbacks(handler))

// assign only to ChatModel nodes
compiledGraph.Invoke(ctx, input, WithChatModelOption(WithTemperature(0.5))

// assign only to node_1
compiledGraph.Invoke(ctx, input, WithCallbacks(handler).DesignateNode("node_1"))
```

# Key Features

## Rich Components

- Encapsulates common building blocks into **component abstractions**, each have multiple **component implementations** that are ready to be used out of the box.
    - component abstractions such as ChatModel, Tool, ChatTemplate, Retriever, Document Loader, Lambda, etc.
    - each component type has an interface of its own: defined Input & Output Type, defined Option type, and streaming paradigms that make sense.
    - implementations are transparent. Abstractions are all you care about when orchestrating components together.

- Implementations can be nested and captures complex business logic.
    - ReAct Agent, MultiQueryRetriever, Host MultiAgent, etc. They consist of multiple components and non-trivial business logic.
    - They are still transparent from the outside. A MultiQueryRetriever can be used anywhere that accepts a Retriever.

## Powerful Orchestration

- Data flows from Retriever / Document Loaders / ChatTemplate to ChatModel, then flows to Tools and parsed as Final Answer. This directed, controlled flow of data through multiple components can be implemented through **graph orchestration**.
- Component instances are graph nodes, and edges are data flow channels.
- Graph orchestration is powerful and flexible enough to implement complex business logic:
  - type checking, stream processing, concurrency management, aspect injection and option assignment are handled by the framework.
  - branch out execution at runtime, read and write global state, or do field level data mapping using workflow(currently in alpha stage).


## Complete Stream Processing

- Stream processing is important because ChatModel outputs chunks of messages in real time as it generates them. It's especially important with orchestration because more components need to handle streaming data.
- Eino automatically **concatenates** stream chunks for downstream nodes that only accepts non-stream input, such as ToolsNode.
- Eino automatically **boxes** non stream into stream when stream is needed during graph execution.  
- Eino automatically **merges** multiple streams as they converge into a single downward node.
- Eino automatically **copies** stream as they fan out to different downward node, or is passed to callback handlers.
- Orchestration elements such as **branch** and **state handlers** are also stream aware.
- With these streaming processing abilities, the streaming paradigms of components themselves become transparent to the user. 
- A compiled Graph can run with 4 different streaming paradigms:

| Streaming Paradigm | Explanation                                                                 |
| ------------------ | --------------------------------------------------------------------------- |
| Invoke             | Accepts non-stream type I and returns non-stream type O                     |
| Stream             | Accepts non-stream type I and returns stream type StreamReader[O]           |
| Collect            | Accepts stream type StreamReader[I] and returns non-stream type O           |
| Transform          | Accepts stream type StreamReader[I] and returns stream type StreamReader[O] |

## Highly Extensible Aspects (Callbacks)

- Aspects handle cross-cutting concerns such as logging, tracing, metrics, etc., as well as exposing internal details of component implementations.
- Five aspects are supported: **OnStart, OnEnd, OnError, OnStartWithStreamInput, OnEndWithStreamOutput**.
- Developers can easily create custom callback handlers, add them during graph run via options, and they will be invoked during graph run.
- Graph can also inject aspects to those component implementations that do not support callbacks on their own.

# Eino Framework Structure

![](.github/static/img/eino/eino_framework.jpeg)

The Eino framework consists of several parts:

- Eino(this repo): Contains Eino's type definitions, streaming mechanism, component abstractions, orchestration capabilities, aspect mechanisms, etc.

- [EinoExt](https://github.com/cloudwego/eino-ext): Component implementations, callback handlers implementations, component usage examples, and various tools such as evaluators, prompt optimizers.

- [Eino Devops](https://github.com/cloudwego/eino-ext/tree/main/devops): visualized developing, visualized debugging
  etc.

- [EinoExamples](https://github.com/cloudwego/eino-examples) is the repo containing example applications and best practices for Eino.

## Detailed Documentation

For learning and using Eino, we provide a comprehensive Eino User Manual to help you quickly understand the concepts in Eino and master the skills of developing AI applications based on Eino. Start exploring through the [Eino User Manual](https://www.cloudwego.io/zh/docs/eino/) now!

For a quick introduction to building AI applications with Eino, we recommend starting with [Eino: Quick Start](https://www.cloudwego.io/zh/docs/eino/quick_start/)

## Dependencies
- Go 1.18 and above.

## Code Style

This repo uses `golangci-lint` to enforce basic code conventions. You can check locally with:

```bash
golangci-lint run ./...
```

Rules enforced include:
- Exported functions, interfaces, packages, etc. should have proper GoDoc comments.
- Code should be formatted with `gofmt -s`.
- Import order should follow `goimports` (std -> third party -> local).

## Security

If you discover a potential security issue in this project, or think you may
have discovered a security issue, we ask that you notify Bytedance Security via our [security center](https://security.bytedance.com/src) or [vulnerability reporting email](sec@bytedance.com).

Please do **not** create a public GitHub issue.

## Contact US
- How to become a member: [COMMUNITY MEMBERSHIP](https://github.com/cloudwego/community/blob/main/COMMUNITY_MEMBERSHIP.md)
- Issues: [Issues](https://github.com/cloudwego/eino/issues)
- Lark: Scan the QR code below with [Register Feishu](https://www.feishu.cn/en/) to join our CloudWeGo/eino user group.

&ensp;&ensp;&ensp; <img src=".github/static/img/eino/lark_group_zh.png" alt="LarkGroup" width="200"/>

## License

This project is licensed under the [Apache-2.0 License](LICENSE-APACHE).
