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
	backgroundshell "github.com/cloudwego/eino/adk/backgroundtask/shell"
	durablesubagent "github.com/cloudwego/eino/adk/backgroundtask/subagent"
	backgroundtool "github.com/cloudwego/eino/adk/backgroundtask/tool"
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

// TypedBackgroundConfig enables selected background-task capabilities for a
// DeepAgent's top-level agent under one shared task-ID space.
type TypedBackgroundConfig[M adk.MessageType] struct {
	// Manager is the single lifecycle authority shared by every enabled capability.
	Manager *backgroundtask.Manager
	// Executors is the registry configured on Manager and shared by every capability.
	Executors *backgroundtask.ExecutorRegistry
	// SubAgents enables reconstructable sub-agent runs.
	SubAgents *TypedDurableSubAgentConfig[M]
	// RecoverableShell enables task-ID-keyed commands that workers can recover.
	RecoverableShell *RecoverableShellConfig
	// LocalShell enables managed process-local Shell or StreamingShell runs.
	LocalShell *LocalShellConfig
	// ForegroundTimeoutMs and ShouldAutoBackground apply to every enabled capability.
	ForegroundTimeoutMs  *int
	ShouldAutoBackground func(context.Context, *backgroundtask.ForegroundCandidate) bool
	// TranscriptFormat customizes durable sub-agent session views.
	TranscriptFormat subagent.TranscriptFormat[M]
}

type BackgroundConfig = TypedBackgroundConfig[*schema.Message]

// TypedDurableSubAgentConfig configures reconstructable sub-agent runs.
type TypedDurableSubAgentConfig[M adk.MessageType] struct {
	// Executor owns durable sub-agent session dependencies.
	Executor *durablesubagent.Executor[M]
	// RunOptionsFactories reconstructs deployment-owned options on every worker
	// attempt and is forwarded to the durable sub-agent middleware. Equivalent
	// configuration must remain available for the full lifetime of resumable tasks.
	RunOptionsFactories map[string]durablesubagent.RunOptionsFactory
}

type DurableSubAgentConfig = TypedDurableSubAgentConfig[*schema.Message]

// RecoverableShellConfig configures recoverable managed shell commands.
type RecoverableShellConfig struct {
	Shell              backgroundshell.RecoverableShell
	OutputMaterializer backgroundtool.OutputMaterializer
}

// LocalShellConfig configures managed process-local shell commands.
type LocalShellConfig struct {
	Shell          filesystem.Shell
	StreamingShell filesystem.StreamingShell
	OutputDir      string
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
	// Optional. Mutually exclusive with StreamingShell and Background.LocalShell.
	Shell filesystem.Shell
	// StreamingShell provides streaming shell command execution capability.
	// If set, a streaming execute tool will be registered to support streaming shell command execution.
	// For advanced filesystem middleware configuration, leave Backend, Shell, and StreamingShell empty
	// and pass a manually constructed filesystem middleware through Handlers.
	// Optional. Mutually exclusive with Shell and Background.LocalShell.
	StreamingShell filesystem.StreamingShell
	// Background configures selected background-task capabilities for the
	// top-level agent under one task-ID space, and injects task_output/task_stop
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

func validateTypedConfig[M adk.MessageType](cfg *TypedConfig[M]) error {
	if cfg == nil {
		return fmt.Errorf("deep: config is required")
	}
	if cfg.Background == nil {
		return nil
	}
	if cfg.Background.Manager == nil {
		return fmt.Errorf("deep: background Manager is required")
	}
	if cfg.Background.Executors == nil {
		return fmt.Errorf("deep: background executor registry is required")
	}
	if cfg.Background.SubAgents == nil &&
		cfg.Background.RecoverableShell == nil &&
		cfg.Background.LocalShell == nil {
		return fmt.Errorf("deep: at least one background capability is required")
	}
	if cfg.Background.SubAgents != nil &&
		cfg.Background.SubAgents.Executor == nil {
		return fmt.Errorf("deep: background SubAgents Executor is required")
	}
	if cfg.Background.RecoverableShell != nil &&
		cfg.Background.RecoverableShell.Shell == nil {
		return fmt.Errorf("deep: background RecoverableShell Shell is required")
	}
	if cfg.Background.LocalShell != nil &&
		cfg.Background.LocalShell.Shell == nil &&
		cfg.Background.LocalShell.StreamingShell == nil {
		return fmt.Errorf(
			"deep: Background.LocalShell requires Shell or StreamingShell",
		)
	}
	if cfg.Background.LocalShell != nil &&
		cfg.Background.LocalShell.Shell != nil &&
		cfg.Background.LocalShell.StreamingShell != nil {
		return fmt.Errorf(
			"deep: Background.LocalShell Shell and StreamingShell are mutually exclusive",
		)
	}
	if cfg.Background.LocalShell != nil &&
		(cfg.Shell != nil || cfg.StreamingShell != nil) {
		return fmt.Errorf(
			"deep: foreground Shell or StreamingShell cannot be combined with Background.LocalShell",
		)
	}
	if cfg.Background.RecoverableShell != nil &&
		(cfg.Shell != nil || cfg.StreamingShell != nil) {
		return fmt.Errorf(
			"deep: recoverable shell, Shell, and StreamingShell are mutually exclusive",
		)
	}
	if cfg.Background.RecoverableShell != nil &&
		cfg.Background.LocalShell != nil {
		return fmt.Errorf(
			"deep: Background.RecoverableShell and Background.LocalShell are mutually exclusive",
		)
	}
	return nil
}

// NewTyped creates a new typed Deep agent instance with the provided configuration.
// This function initializes built-in tools, creates a task tool for subagent orchestration,
// and returns a fully configured TypedChatModelAgent ready for execution.
func NewTyped[M adk.MessageType](ctx context.Context, cfg *TypedConfig[M]) (adk.TypedResumableAgent[M], error) {
	if err := validateTypedConfig(cfg); err != nil {
		return nil, err
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
			if cfg.Background != nil && cfg.Background.SubAgents != nil {
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
		progressReaders := make(map[string]backgroundtaskmw.TaskProgressReader)
		if cfg.Background.SubAgents != nil {
			subagentReader, err := subagent.NewDurableTaskProgressReader(
				cfg.Background.SubAgents.Executor,
				cfg.Background.TranscriptFormat,
			)
			if err != nil {
				return nil, fmt.Errorf("create durable sub-agent progress reader: %w", err)
			}
			progressReaders[durablesubagent.ExecutorKey] = subagentReader
		}
		reader, err := backgroundtool.NewProgressReader(manager, 0)
		if err != nil {
			return nil, fmt.Errorf("create managed-tool progress reader: %w", err)
		}
		progressReaders[backgroundtool.ExecutorKey] = reader
		progressReaders[backgroundtool.RecoverableExecutorKey] = reader
		controlMW, err := backgroundtaskmw.NewTyped(ctx, &backgroundtaskmw.TypedConfig[M]{
			Manager: manager, ProgressReadersByExecutorKey: progressReaders,
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

	recoverableShell := deepRecoverableShell(background)
	localShell, localStreamingShell := deepLocalShells(background)
	shell := cfg.Shell
	streamingShell := cfg.StreamingShell
	if localShell != nil || localStreamingShell != nil {
		shell = localShell
		streamingShell = localStreamingShell
	}
	if cfg.Backend != nil || shell != nil || streamingShell != nil ||
		recoverableShell != nil {
		mwCfg := &filesystem2.MiddlewareConfig{
			Backend: cfg.Backend, Shell: shell, StreamingShell: streamingShell,
		}
		if deepFilesystemBackgroundEnabled(background) {
			if background.RecoverableShell != nil {
				mwCfg.Background = &filesystem2.BackgroundConfig{
					NotificationSessionID: deepNotificationSessionID,
					Recoverable: &filesystem2.RecoverableBackgroundConfig{
						Shell: recoverableShell, Manager: background.Manager,
						Executors:            background.Executors,
						OutputMaterializer:   background.RecoverableShell.OutputMaterializer,
						ForegroundTimeoutMs:  background.ForegroundTimeoutMs,
						ShouldAutoBackground: background.ShouldAutoBackground,
					},
				}
			} else {
				runner, err := deepLocalShellRunner(background)
				if err != nil {
					return nil, err
				}
				mwCfg.Background = &filesystem2.BackgroundConfig{
					NotificationSessionID: deepNotificationSessionID,
					Local: &filesystem2.LocalBackgroundConfig{
						Runner: runner, OutputStore: backendAppendOpener(cfg.Backend),
						OutputDir: deepShellOutputDir(background),
					},
				}
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

func deepNotificationSessionID(ctx context.Context) (string, error) {
	sessionID, _ := adk.RunnerSessionID(ctx)
	return sessionID, nil
}

func deepBackgroundManager[M adk.MessageType](background *TypedBackgroundConfig[M]) *backgroundtask.Manager {
	if background == nil {
		return nil
	}
	return background.Manager
}

func deepLocalShellRunner[M adk.MessageType](
	background *TypedBackgroundConfig[M],
) (*backgroundlocal.Runner, error) {
	if background == nil || background.LocalShell == nil {
		return nil, nil
	}
	return backgroundlocal.New(&backgroundlocal.Config{
		Manager:              background.Manager,
		Executors:            background.Executors,
		ForegroundTimeoutMs:  background.ForegroundTimeoutMs,
		ShouldAutoBackground: background.ShouldAutoBackground,
	})
}

func deepRecoverableShell[M adk.MessageType](
	background *TypedBackgroundConfig[M],
) backgroundshell.RecoverableShell {
	if background == nil || background.RecoverableShell == nil {
		return nil
	}
	return background.RecoverableShell.Shell
}

func deepLocalShells[M adk.MessageType](
	background *TypedBackgroundConfig[M],
) (filesystem.Shell, filesystem.StreamingShell) {
	if background == nil || background.LocalShell == nil {
		return nil, nil
	}
	return background.LocalShell.Shell, background.LocalShell.StreamingShell
}

func deepFilesystemBackgroundEnabled[M adk.MessageType](
	background *TypedBackgroundConfig[M],
) bool {
	return background != nil &&
		(background.LocalShell != nil || background.RecoverableShell != nil)
}

func deepShellOutputDir[M adk.MessageType](background *TypedBackgroundConfig[M]) string {
	if background == nil {
		return ""
	}
	if background.LocalShell != nil {
		return background.LocalShell.OutputDir
	}
	return ""
}

func deepSubagentBackground[M adk.MessageType](
	cfg *TypedConfig[M],
) *subagent.TypedBackgroundConfig[M] {
	return &subagent.TypedBackgroundConfig[M]{
		TranscriptFormat: cfg.Background.TranscriptFormat,
		Durable: &subagent.TypedDurableBackgroundConfig[M]{
			Manager:              cfg.Background.Manager,
			Executors:            cfg.Background.Executors,
			Executor:             cfg.Background.SubAgents.Executor,
			ForegroundTimeoutMs:  cfg.Background.ForegroundTimeoutMs,
			ShouldAutoBackground: cfg.Background.ShouldAutoBackground,
			RunOptionsFactories:  cfg.Background.SubAgents.RunOptionsFactories,
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
