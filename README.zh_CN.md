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

**Eino['aino]** 是一个 Go 语言的 LLM 应用开发框架，借鉴了 LangChain、Google ADK 等开源项目，按照 Go 的惯例设计。

Eino 提供：
- **[组件](https://github.com/cloudwego/eino-ext)**：`ChatModel`、`Tool`、`Retriever`、`ChatTemplate` 等可复用模块，官方实现覆盖 OpenAI、Ollama 等
- **智能体开发套件（ADK）**：支持工具调用、多智能体协同、上下文管理、中断/恢复等人机交互，以及开箱即用的智能体模式
- **编排**：把组件组装成图或工作流，既能独立运行，也能作为工具给智能体调用
- **[示例](https://github.com/cloudwego/eino-examples)**：常见模式和实际场景的可运行代码

![](.github/static/img/eino/eino_concept.jpeg)

# 快速上手

## ChatModelAgent

配置好 ChatModel，加上工具（可选），就能跑起来：

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

加工具让智能体有更多能力：

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

智能体内部自动处理 ReAct 循环，自己判断什么时候调工具、什么时候回复。

→ [ChatModelAgent 示例](https://github.com/cloudwego/eino-examples/tree/main/adk/intro) · [文档](https://www.cloudwego.io/zh/docs/eino/core_modules/eino_adk/agent_implementation/chat_model/)

## DeepAgent

复杂任务用 DeepAgent，它会把问题拆成步骤，分派给子智能体，并追踪进度：

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

DeepAgent 可以配置成：协调多个专业智能体、跑 shell 命令、执行 Python、搜索网络。

→ [DeepAgent 示例](https://github.com/cloudwego/eino-examples/tree/main/adk/multiagent/deep) · [文档](https://www.cloudwego.io/zh/docs/eino/core_modules/eino_adk/agent_implementation/deepagents/)

## 编排

需要精确控制执行流程时，用 `compose` 搭图或工作流：

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

编排出来的流程可以包装成工具给智能体用，把确定性流程和自主决策结合起来：

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

这样你可以写出精确可控的业务流程，再让智能体决定什么时候调用。

→ [GraphTool 示例](https://github.com/cloudwego/eino-examples/tree/main/adk/common/tool/graphtool) · [编排文档](https://www.cloudwego.io/zh/docs/eino/core_modules/chain_and_graph_orchestration/)

# 主要特性

## 组件生态

Eino 定义了组件抽象（ChatModel、Tool、Retriever、Embedding 等），官方实现覆盖 OpenAI、Claude、Gemini、Ark、Ollama、Elasticsearch 等。

→ [eino-ext](https://github.com/cloudwego/eino-ext)

## 流式处理

Eino 在编排中自动处理流式：拼接、装箱、合并、复制。组件只需实现有业务意义的流式范式，框架处理剩下的。

→ [文档](https://www.cloudwego.io/zh/docs/eino/core_modules/chain_and_graph_orchestration/stream_programming_essentials/)

## 回调切面

在固定切点（OnStart、OnEnd、OnError、OnStartWithStreamInput、OnEndWithStreamOutput）注入日志、追踪、指标，适用于组件、图、智能体。

→ [文档](https://www.cloudwego.io/zh/docs/eino/core_modules/chain_and_graph_orchestration/callback_manual/)

## 中断/恢复

任何智能体或工具都能暂停等待人工输入，从检查点恢复。框架处理状态持久化和路由。

→ [文档](https://www.cloudwego.io/zh/docs/eino/core_modules/eino_adk/agent_hitl/) · [示例](https://github.com/cloudwego/eino-examples/tree/main/adk/human-in-the-loop)

# 框架结构

![](.github/static/img/eino/eino_framework.jpeg)

Eino 框架包含：

- Eino（本仓库）：类型定义、流处理机制、组件抽象、编排、智能体实现、切面机制

- [EinoExt](https://github.com/cloudwego/eino-ext)：组件实现、回调处理器、使用示例、评估器、提示优化器

- [Eino Devops](https://github.com/cloudwego/eino-ext/tree/main/devops)：可视化开发和调试

- [EinoExamples](https://github.com/cloudwego/eino-examples)：示例应用和最佳实践

## 文档

- [Eino 用户手册](https://www.cloudwego.io/zh/docs/eino/)
- [Eino: 快速开始](https://www.cloudwego.io/zh/docs/eino/quick_start/)

## 依赖
- Go 1.18 及以上

## 代码规范

本仓库使用 `golangci-lint`，本地检查：

```bash
golangci-lint run ./...
```

规则：
- 导出的函数、接口、package 等需要 GoDoc 注释
- 代码格式符合 `gofmt -s`
- import 顺序符合 `goimports`（std -> third party -> local）

## 安全

发现安全问题请通过[安全中心](https://security.bytedance.com/src)或[漏洞报告邮箱](sec@bytedance.com)联系字节跳动安全团队。

请**不要**创建公开的 GitHub Issue。

## 联系我们
- 成为 member：[COMMUNITY MEMBERSHIP](https://github.com/cloudwego/community/blob/main/COMMUNITY_MEMBERSHIP.md)
- Issues：[Issues](https://github.com/cloudwego/eino/issues)
- 飞书：扫码加入 CloudWeGo/eino 用户群

&ensp;&ensp;&ensp; <img src=".github/static/img/eino/lark_group_zh.png" alt="LarkGroup" width="200"/>

## 开源许可证

本项目基于 [Apache-2.0 许可证](LICENSE-APACHE) 开源。
