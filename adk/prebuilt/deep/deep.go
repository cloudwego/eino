/*
 * Copyright 2026 CloudWeGo Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

// Package deep provides a prebuilt agent with deep task orchestration.
package deep

import (
	"context"
	"fmt"

	"github.com/bytedance/sonic"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/backgroundtask"
	backgroundlocal "github.com/cloudwego/eino/adk/backgroundtask/local"
	durablesubagent "github.com/cloudwego/eino/adk/backgroundtask/subagent"
	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/adk/internal"
	backgroundtaskmw "github.com/cloudwego/eino/adk/middlewares/backgroundtask"
	filesystem2 "github.com/cloudwego/eino/adk/middlewares/filesystem"
	"github.com/cloudwego/eino/adk/middlewares/subagent"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
)

func init() {
	schema.RegisterName[TODO]("_eino_adk_prebuilt_deep_todo")
	schema.RegisterName[[]TODO]("_eino_adk_prebuilt_deep_todo_slice")
}

// TypedBackgroundConfig enables background-task execution for a DeepAgent's
// top-level agent. When set, shell commands and sub-agent runs can execute as managed
// background tasks under one task-ID space, and the task_output/task_stop control
// tools are injected once.
//
// It holds the shared Manager plus durable sub-agent identity and session
// dependencies. Shell-specific output configuration remains OutputDir.
type TypedBackgroundConfig[M adk.MessageType] struct {
	Local   *TypedLocalBackgroundConfig[M]
	Durable *TypedDurableBackgroundConfig[M]
	// Notifications is required because DeepAgent's background tools promise
	// completion notification.
	Notifications backgroundtask.NotificationDeliveryRuntime
}

type BackgroundConfig = TypedBackgroundConfig[*schema.Message]

type TypedLocalBackgroundConfig[M adk.MessageType] struct {
	Runner    *backgroundlocal.Runner
	OutputDir string
}

type TypedDurableBackgroundConfig[M adk.MessageType] struct {
	Manager              *backgroundtask.Manager
	ForegroundTimeoutMs  *int
	ShouldAutoBackground func(context.Context, *backgroundtask.Task) bool
	OutputDir            string
	// RunOptionsFactories reconstructs deployment-owned options on every worker
	// attempt and is forwarded to the durable sub-agent middleware. Equivalent
	// configuration must remain available for the full lifetime of resumable tasks.
	RunOptionsFactories map[string]durablesubagent.RunOptionsFactory
}

// TypedConfig defines the configuration for creating a DeepAgent parameterized by message type.
// An Agentic DeepAgent (M = *schema.AgenticMessage) only supports Agentic sub-agents,
// and a standard DeepAgent (M = *schema.Message) only supports standard sub-agents.
// This is enforced by the type system through the SubAgents field.
type TypedConfig[M adk.MessageType] struct {
	// Name is the identifier for the Deep agent.
	Name string
	// Description provides a brief explanation of the agent's purpose.
	Description string

	// ChatModel is the model used by DeepAgent for reasoning and task execution.
	// If the agent uses any tools, this model must support the model.WithTools call option,
	// as that's how the agent configures the model with tool information.
	ChatModel model.BaseModel[M]
	// Instruction contains the system prompt that guides the agent's behavior.
	// When empty, a built-in default system prompt will be used, which includes general assistant
	// behavior guidelines, security policies, coding style guidelines, and tool usage policies.
	Instruction string
	// SubAgents are specialized agents that can be invoked by the agent.
	// For M = *schema.AgenticMessage, only agentic sub-agents are accepted.
	SubAgents []adk.TypedAgent[M]
	// ToolsConfig provides the tools and tool-calling configurations available for the agent to invoke.
	ToolsConfig adk.ToolsConfig
	// MaxIteration limits the maximum number of reasoning iterations the agent can perform.
	MaxIteration int

	// Backend provides filesystem operations used by tools and offloading.
	// If set, filesystem tools (read_file, write_file, edit_file, glob, grep) will be registered.
	// For advanced filesystem middleware configuration, leave Backend, Shell, and StreamingShell empty
	// and pass a manually constructed filesystem middleware through Handlers.
	// Optional.
	Backend filesystem.Backend
	// Shell provides shell command execution capability.
	// If set, an execute tool will be registered to support shell command execution.
	// For advanced filesystem middleware configuration, leave Backend, Shell, and StreamingShell empty
	// and pass a manually constructed filesystem middleware through Handlers.
	// Optional. Mutually exclusive with StreamingShell.
	Shell filesystem.Shell
	// StreamingShell provides streaming shell command execution capability.
	// If set, a streaming execute tool will be registered to support streaming shell command execution.
	// For advanced filesystem middleware configuration, leave Backend, Shell, and StreamingShell empty
	// and pass a manually constructed filesystem middleware through Handlers.
	// Optional. Mutually exclusive with Shell.
	StreamingShell filesystem.StreamingShell

	// Background configures background-task execution for the top-level agent: it
	// can spawn sub-agents and run shell commands as managed background tasks under
	// one task-ID space, and the task_output/task_stop control tools are injected
	// once. Background is intentionally NOT propagated to the general or user
	// sub-agents: their shell runs stay foreground/buffered and they cannot launch
	// background work, so background orchestration is a top-level concern only. When
	// nil, the top-level agent has no background-task support. See BackgroundConfig.
	Background *TypedBackgroundConfig[M]

	// WithoutWriteTodos disables the built-in write_todos tool when set to true.
	WithoutWriteTodos bool
	// WithoutGeneralSubAgent disables the general-purpose subagent when set to true.
	WithoutGeneralSubAgent bool
	// TaskToolDescriptionGenerator allows customizing the description for the task tool.
	// If provided, this function generates the tool description based on available subagents.
	TaskToolDescriptionGenerator func(ctx context.Context, availableAgents []adk.TypedAgent[M]) (string, error)

	Middlewares []adk.AgentMiddleware

	// Handlers configures interface-based handlers for extending agent behavior.
	// Unlike Middlewares (struct-based), Handlers allow users to:
	//   - Add custom methods to their handler implementations
	//   - Return modified context from handler methods
	//   - Centralize configuration in struct fields instead of closures
	//
	// Handlers are processed after Middlewares, in registration order.
	// See adk.ChatModelAgentMiddleware documentation for when to use Handlers vs Middlewares.
	Handlers []adk.TypedChatModelAgentMiddleware[M]

	ModelRetryConfig *adk.TypedModelRetryConfig[M]
	// ModelFailoverConfig configures failover behavior for the ChatModel.
	// When set, the agent will automatically fail over to alternative models on errors.
	// This config is also propagated to the general sub-agent.
	ModelFailoverConfig *adk.ModelFailoverConfig[M]
	// OutputKey stores the agent's response in the session.
	// Optional. When set, stores output via AddSessionValue(ctx, outputKey, msg.Content).
	OutputKey string
}

// Config defines the configuration for creating a standard DeepAgent.
type Config = TypedConfig[*schema.Message]

// NewTyped creates a new typed Deep agent instance with the provided configuration.
// This function initializes built-in tools, creates a task tool for subagent orchestration,
// and returns a fully configured TypedChatModelAgent ready for execution.
func NewTyped[M adk.MessageType](ctx context.Context, cfg *TypedConfig[M]) (adk.TypedResumableAgent[M], error) {
	if cfg == nil {
		return nil, fmt.Errorf("deep: config is required")
	}
	if cfg.Background != nil {
		if (cfg.Background.Local == nil) == (cfg.Background.Durable == nil) {
			return nil, fmt.Errorf("deep: exactly one of Background.Local or Background.Durable is required")
		}
		if deepBackgroundManager(cfg.Background) == nil {
			return nil, fmt.Errorf("deep: background Manager is required")
		}
		if cfg.Background.Notifications == nil {
			return nil, fmt.Errorf("deep: background notification delivery is required")
		}
	}
	// Sub-agents never get the background configuration: their shell runs stay
	// foreground/buffered and they cannot launch background work.
	subAgentHandlers, err := buildTypedBuiltinAgentMiddlewares(ctx, cfg, nil)
	if err != nil {
		return nil, err
	}

	instruction := cfg.Instruction
	if len(instruction) == 0 {
		instruction = internal.SelectPrompt(internal.I18nPrompts{
			English: baseAgentInstruction,
			Chinese: baseAgentInstructionChinese,
		})
	}

	// The top-level agent's built-in handlers do get background support, so its own
	// shell runs are background-capable and tracked under the shared task-ID space.
	handlers, err := buildTypedBuiltinAgentMiddlewares(ctx, cfg, cfg.Background)
	if err != nil {
		return nil, err
	}

	if !cfg.WithoutGeneralSubAgent || len(cfg.SubAgents) > 0 {
		allSubAgents, err := buildSubAgentsList(ctx, cfg, instruction, subAgentHandlers)
		if err != nil {
			return nil, err
		}
		if len(allSubAgents) > 0 {
			subCfg := &subagent.TypedConfig[M]{
				SubAgents:                allSubAgents,
				ToolName:                 taskToolName,
				ToolDescriptionGenerator: cfg.TaskToolDescriptionGenerator,
			}
			if cfg.Background != nil {
				subCfg.Background = deepSubagentBackground(cfg)
			}
			subagentMW, err := subagent.NewTyped(ctx, subCfg)
			if err != nil {
				return nil, fmt.Errorf("failed to create subagent middleware: %w", err)
			}
			handlers = append(handlers, subagentMW)
		}
	}

	// When background support is configured, wire its control tools
	// (task_output/task_stop) exactly once at the top level.
	if manager := deepBackgroundManager(cfg.Background); manager != nil {
		controlMW, err := backgroundtaskmw.NewTyped(ctx, &backgroundtaskmw.TypedConfig[M]{
			Manager: manager, Notifications: cfg.Background.Notifications,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create background-task control middleware: %w", err)
		}
		handlers = append(handlers, controlMW)
	}

	return adk.NewTypedChatModelAgent(ctx, &adk.TypedChatModelAgentConfig[M]{
		Name:          cfg.Name,
		Description:   cfg.Description,
		Instruction:   instruction,
		Model:         cfg.ChatModel,
		ToolsConfig:   cfg.ToolsConfig,
		MaxIterations: cfg.MaxIteration,
		Middlewares:   cfg.Middlewares,
		Handlers:      append(handlers, cfg.Handlers...),

		GenModelInput:       typedGenModelInput[M],
		ModelRetryConfig:    cfg.ModelRetryConfig,
		ModelFailoverConfig: cfg.ModelFailoverConfig,
		OutputKey:           cfg.OutputKey,
	})
}

// New creates a new Deep agent instance with the provided configuration.
// This function initializes built-in tools, creates a task tool for subagent orchestration,
// and returns a fully configured ChatModelAgent ready for execution.
func New(ctx context.Context, cfg *Config) (adk.ResumableAgent, error) {
	return NewTyped(ctx, cfg)
}

func typedGenModelInput[M adk.MessageType](_ context.Context, instruction string, input *adk.TypedAgentInput[M]) ([]M, error) {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		msgs := make([]*schema.Message, 0, len(input.Messages)+1)
		inputMessages := input.Messages
		if instruction != "" {
			if len(inputMessages) > 0 {
				if msg, ok := any(inputMessages[0]).(*schema.Message); ok && msg.Role == schema.System {
					inputMessages = inputMessages[1:]
				}
			}
			msgs = append(msgs, schema.SystemMessage(instruction))
		}
		// Type assertion is safe here because M = *schema.Message.
		for _, m := range inputMessages {
			msgs = append(msgs, any(m).(*schema.Message))
		}
		result := make([]M, len(msgs))
		for i, m := range msgs {
			result[i] = any(m).(M)
		}
		return result, nil
	case *schema.AgenticMessage:
		msgs := make([]*schema.AgenticMessage, 0, len(input.Messages)+1)
		inputMessages := input.Messages
		if instruction != "" {
			if len(inputMessages) > 0 {
				if msg, ok := any(inputMessages[0]).(*schema.AgenticMessage); ok && msg.Role == schema.AgenticRoleTypeSystem {
					inputMessages = inputMessages[1:]
				}
			}
			msgs = append(msgs, schema.SystemAgenticMessage(instruction))
		}
		for _, m := range inputMessages {
			msgs = append(msgs, any(m).(*schema.AgenticMessage))
		}
		result := make([]M, len(msgs))
		for i, m := range msgs {
			result[i] = any(m).(M)
		}
		return result, nil
	}
	panic("unreachable")
}

func buildSubAgentsList[M adk.MessageType](ctx context.Context, cfg *TypedConfig[M], instruction string, handlers []adk.TypedChatModelAgentMiddleware[M]) ([]adk.TypedAgent[M], error) {
	var allSubAgents []adk.TypedAgent[M]

	if !cfg.WithoutGeneralSubAgent {
		agentDesc := internal.SelectPrompt(internal.I18nPrompts{
			English: generalAgentDescription,
			Chinese: generalAgentDescriptionChinese,
		})
		generalAgent, err := adk.NewTypedChatModelAgent(ctx, &adk.TypedChatModelAgentConfig[M]{
			Name:                generalAgentName,
			Description:         agentDesc,
			Instruction:         instruction,
			Model:               cfg.ChatModel,
			ToolsConfig:         cfg.ToolsConfig,
			MaxIterations:       cfg.MaxIteration,
			Middlewares:         cfg.Middlewares,
			Handlers:            append(handlers, cfg.Handlers...),
			GenModelInput:       typedGenModelInput[M],
			ModelRetryConfig:    cfg.ModelRetryConfig,
			ModelFailoverConfig: cfg.ModelFailoverConfig,
		})
		if err != nil {
			return nil, err
		}
		allSubAgents = append(allSubAgents, generalAgent)
	}

	allSubAgents = append(allSubAgents, cfg.SubAgents...)
	return allSubAgents, nil
}

func buildTypedBuiltinAgentMiddlewares[M adk.MessageType](ctx context.Context, cfg *TypedConfig[M], background *TypedBackgroundConfig[M]) ([]adk.TypedChatModelAgentMiddleware[M], error) {
	var ms []adk.TypedChatModelAgentMiddleware[M]
	if !cfg.WithoutWriteTodos {
		t, err := typedNewWriteTodos[M]()
		if err != nil {
			return nil, err
		}
		ms = append(ms, t)
	}

	if cfg.Backend != nil || cfg.Shell != nil || cfg.StreamingShell != nil {
		mwCfg := &filesystem2.MiddlewareConfig{
			Backend:        cfg.Backend,
			Shell:          cfg.Shell,
			StreamingShell: cfg.StreamingShell,
		}
		if manager := deepBackgroundManager(background); manager != nil {
			runner, runnerErr := deepLocalRunner(background)
			if runnerErr != nil {
				return nil, runnerErr
			}
			mwCfg.Background = &filesystem2.BackgroundConfig{
				Runner:        runner,
				Notifications: background.Notifications,
				OutputStore:   backendAppendOpener(cfg.Backend),
				OutputDir:     deepBackgroundOutputDir(background),
			}
		}
		fm, err := filesystem2.NewTyped[M](ctx, mwCfg)
		if err != nil {
			return nil, err
		}
		ms = append(ms, fm)
	}

	return ms, nil
}

func deepBackgroundManager[M adk.MessageType](background *TypedBackgroundConfig[M]) *backgroundtask.Manager {
	if background == nil {
		return nil
	}
	if background.Local != nil {
		if background.Local.Runner == nil {
			return nil
		}
		return background.Local.Runner.Manager()
	}
	if background.Durable != nil {
		return background.Durable.Manager
	}
	return nil
}

func deepLocalRunner[M adk.MessageType](
	background *TypedBackgroundConfig[M],
) (*backgroundlocal.Runner, error) {
	if background == nil {
		return nil, nil
	}
	if background.Local != nil {
		return background.Local.Runner, nil
	}
	if background.Durable == nil {
		return nil, nil
	}
	return backgroundlocal.New(&backgroundlocal.Config{
		Manager:              background.Durable.Manager,
		ForegroundTimeoutMs:  background.Durable.ForegroundTimeoutMs,
		ShouldAutoBackground: background.Durable.ShouldAutoBackground,
	})
}

func deepBackgroundOutputDir[M adk.MessageType](background *TypedBackgroundConfig[M]) string {
	if background == nil {
		return ""
	}
	if background.Local != nil {
		return background.Local.OutputDir
	}
	if background.Durable != nil {
		return background.Durable.OutputDir
	}
	return ""
}

func deepSubagentBackground[M adk.MessageType](
	cfg *TypedConfig[M],
) *subagent.TypedBackgroundConfig[M] {
	outputStore := backendAppendOpener(cfg.Backend)
	if cfg.Background.Local != nil {
		return &subagent.TypedBackgroundConfig[M]{
			Notifications: cfg.Background.Notifications,
			Local: &subagent.TypedLocalBackgroundConfig[M]{
				Runner: cfg.Background.Local.Runner, OutputStore: outputStore,
				OutputDir: cfg.Background.Local.OutputDir,
			},
		}
	}
	return &subagent.TypedBackgroundConfig[M]{
		Notifications: cfg.Background.Notifications,
		Durable: &subagent.TypedDurableBackgroundConfig[M]{
			Manager:              cfg.Background.Durable.Manager,
			ForegroundTimeoutMs:  cfg.Background.Durable.ForegroundTimeoutMs,
			ShouldAutoBackground: cfg.Background.Durable.ShouldAutoBackground,
			OutputStore:          outputStore,
			OutputDir:            cfg.Background.Durable.OutputDir,
			RunOptionsFactories:  cfg.Background.Durable.RunOptionsFactories,
		},
	}
}

// backendAppendOpener returns b as a filesystem.AppendOpener when it supports
// incremental append, or nil otherwise — in which case background tasks run without
// output files. The default InMemoryBackend implements AppendOpener.
func backendAppendOpener(b filesystem.Backend) filesystem.AppendOpener {
	ao, _ := b.(filesystem.AppendOpener)
	return ao
}

type TODO struct {
	Content    string `json:"content"`
	ActiveForm string `json:"activeForm"`
	Status     string `json:"status" jsonschema:"enum=pending,enum=in_progress,enum=completed"`
}

type writeTodosArguments struct {
	Todos []TODO `json:"todos"`
}

func typedNewWriteTodos[M adk.MessageType]() (adk.TypedChatModelAgentMiddleware[M], error) {
	toolDesc := internal.SelectPrompt(internal.I18nPrompts{
		English: writeTodosToolDescription,
		Chinese: writeTodosToolDescriptionChinese,
	})
	resultMsg := internal.SelectPrompt(internal.I18nPrompts{
		English: "Updated todo list to %s",
		Chinese: "已更新待办列表为 %s",
	})

	t, err := utils.InferTool("write_todos", toolDesc, func(ctx context.Context, input writeTodosArguments) (output string, err error) {
		adk.AddSessionValue(ctx, SessionKeyTodos, input.Todos)
		todos, err := sonic.MarshalString(input.Todos)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf(resultMsg, todos), nil
	})
	if err != nil {
		return nil, err
	}

	return typedBuildAppendPromptTool[M]("", t), nil
}
