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

**Eino['aino]** is an LLM application development framework in Golang. It draws from LangChain, Google ADK, and other open-source frameworks, and is designed to follow Golang conventions.

Eino provides:
- **[Components](https://github.com/cloudwego/eino-ext)**: reusable building blocks like `ChatModel`, `Tool`, `Retriever`, and `ChatTemplate`, with official implementations for OpenAI, Ollama, and more.
- **Agent Development Kit (ADK)**: build AI agents with tool use, multi-agent coordination, context management, interrupt/resume for human-in-the-loop, and ready-to-use agent patterns.
- **Composition**: connect components into graphs and workflows that can run standalone or be exposed as tools for agents.
- **[Examples](https://github.com/cloudwego/eino-examples)**: working code for common patterns and real-world use cases.

![](.github/static/img/eino/eino_concept.jpeg)

# Quick Start

## ChatModelAgent

Configure a ChatModel, optionally add tools, and you have a working agent:

```Go
chatModel, _ := openai.NewChatModel(ctx, &openai.ChatModelConfig{
    Model:  "gpt-4o",
    APIKey: os.Getenv("OPENAI_API_KEY"),
})

agent, _ := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
    Model: chatModel,
})

runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})
iter := runner.Query(ctx, "Hello, who are you?")
for {
    event, ok := iter.Next()
    if !ok {
        break
    }
    fmt.Println(event.Message.Content)
}
```

Add tools to give the agent capabilities:

```Go
agent, _ := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
    Model: chatModel,
    ToolsConfig: adk.ToolsConfig{
        ToolsNodeConfig: compose.ToolsNodeConfig{
            Tools: []tool.BaseTool{weatherTool, calculatorTool},
        },
    },
})
```

The agent handles the ReAct loop internally — it decides when to call tools and when to respond.

→ [ChatModelAgent examples](https://github.com/cloudwego/eino-examples/tree/main/adk/intro) · [docs](https://www.cloudwego.io/docs/eino/core_modules/eino_adk/agent_implementation/chat_model/)

## DeepAgent

For complex tasks, use DeepAgent. It breaks down problems into steps, delegates to sub-agents, and tracks progress:

```Go
deepAgent, _ := deep.New(ctx, &deep.Config{
    ChatModel: chatModel,
    SubAgents: []adk.Agent{researchAgent, codeAgent},
    ToolsConfig: adk.ToolsConfig{
        ToolsNodeConfig: compose.ToolsNodeConfig{
            Tools: []tool.BaseTool{shellTool, pythonTool, webSearchTool},
        },
    },
})

runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: deepAgent})
iter := runner.Query(ctx, "Analyze the sales data in report.csv and generate a summary chart")
```

DeepAgent can be configured to coordinate multiple specialized agents, run shell commands, execute Python code, and search the web.

→ [DeepAgent example](https://github.com/cloudwego/eino-examples/tree/main/adk/multiagent/deep) · [docs](https://www.cloudwego.io/docs/eino/core_modules/eino_adk/agent_implementation/deepagents/)

## Composition

When you need precise control over execution flow, use `compose` to build graphs and workflows:

```Go
graph := compose.NewGraph[*Input, *Output]()
graph.AddLambdaNode("validate", validateFn)
graph.AddChatModelNode("generate", chatModel)
graph.AddLambdaNode("format", formatFn)

graph.AddEdge(compose.START, "validate")
graph.AddEdge("validate", "generate")
graph.AddEdge("generate", "format")
graph.AddEdge("format", compose.END)

runnable, _ := graph.Compile(ctx)
result, _ := runnable.Invoke(ctx, input)
```

Compositions can be exposed as tools for agents, bridging deterministic workflows with autonomous behavior:

```Go
tool, _ := graphtool.NewInvokableGraphTool(graph, "data_pipeline", "Process and validate data")

agent, _ := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
    Model: chatModel,
    ToolsConfig: adk.ToolsConfig{
        ToolsNodeConfig: compose.ToolsNodeConfig{
            Tools: []tool.BaseTool{tool},
        },
    },
})
```

This lets you build domain-specific pipelines with exact control, then let agents decide when to use them.

→ [GraphTool examples](https://github.com/cloudwego/eino-examples/tree/main/adk/common/tool/graphtool) · [compose docs](https://www.cloudwego.io/docs/eino/core_modules/chain_and_graph_orchestration/)

# Key Features

## Component Ecosystem

Eino defines component abstractions (ChatModel, Tool, Retriever, Embedding, etc.) with official implementations for OpenAI, Claude, Gemini, Ark, Ollama, Elasticsearch, and more.

→ [eino-ext](https://github.com/cloudwego/eino-ext)

## Stream Processing

Eino automatically handles streaming throughout orchestration: concatenating, boxing, merging, and copying streams as data flows between nodes. Components only implement the streaming paradigms that make sense for them; the framework handles the rest.

→ [docs](https://www.cloudwego.io/docs/eino/core_modules/chain_and_graph_orchestration/stream_programming_essentials/)

## Callback Aspects

Inject logging, tracing, and metrics at fixed points (OnStart, OnEnd, OnError, OnStartWithStreamInput, OnEndWithStreamOutput) across components, graphs, and agents.

→ [docs](https://www.cloudwego.io/docs/eino/core_modules/chain_and_graph_orchestration/callback_manual/)

## Interrupt/Resume

Any agent or tool can pause execution for human input and resume from checkpoint. The framework handles state persistence and routing.

→ [docs](https://www.cloudwego.io/docs/eino/core_modules/eino_adk/agent_hitl/) · [examples](https://github.com/cloudwego/eino-examples/tree/main/adk/human-in-the-loop)

# Framework Structure

![](.github/static/img/eino/eino_framework.jpeg)

The Eino framework consists of:

- Eino (this repo): Type definitions, streaming mechanism, component abstractions, orchestration, agent implementations, aspect mechanisms

- [EinoExt](https://github.com/cloudwego/eino-ext): Component implementations, callback handlers, usage examples, evaluators, prompt optimizers

- [Eino Devops](https://github.com/cloudwego/eino-ext/tree/main/devops): Visualized development and debugging

- [EinoExamples](https://github.com/cloudwego/eino-examples): Example applications and best practices

## Documentation

- [Eino User Manual](https://www.cloudwego.io/zh/docs/eino/)
- [Eino: Quick Start](https://www.cloudwego.io/zh/docs/eino/quick_start/)

## Dependencies
- Go 1.18 and above.

## Code Style

This repo uses `golangci-lint`. Check locally with:

```bash
golangci-lint run ./...
```

Rules enforced:
- Exported functions, interfaces, packages, etc. should have GoDoc comments
- Code should be formatted with `gofmt -s`
- Import order should follow `goimports` (std -> third party -> local)

## Security

If you discover a potential security issue, notify Bytedance Security via the [security center](https://security.bytedance.com/src) or [vulnerability reporting email](sec@bytedance.com).

Do **not** create a public GitHub issue.

## Contact
- Membership: [COMMUNITY MEMBERSHIP](https://github.com/cloudwego/community/blob/main/COMMUNITY_MEMBERSHIP.md)
- Issues: [Issues](https://github.com/cloudwego/eino/issues)
- Lark: Scan the QR code below with [Feishu](https://www.feishu.cn/en/) to join the CloudWeGo/eino user group.

&ensp;&ensp;&ensp; <img src=".github/static/img/eino/lark_group_zh.png" alt="LarkGroup" width="200"/>

## License

This project is licensed under the [Apache-2.0 License](LICENSE-APACHE).
