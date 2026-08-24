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

package deep

import (
	"context"
	"fmt"
	"io"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/filesystem"
	filesystem2 "github.com/cloudwego/eino/adk/middlewares/filesystem"
	"github.com/cloudwego/eino/adk/prebuilt/planexecute"
	adksession "github.com/cloudwego/eino/adk/session"
	"github.com/cloudwego/eino/adk/task"
	"github.com/cloudwego/eino/adk/task/background"
	backgroundshell "github.com/cloudwego/eino/adk/task/shell"
	durablesubagent "github.com/cloudwego/eino/adk/task/subagent"
	backgroundtool "github.com/cloudwego/eino/adk/task/tool"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	mockModel "github.com/cloudwego/eino/internal/mock/components/model"
	"github.com/cloudwego/eino/schema"
)

func mustNewBackgroundManager(
	t testing.TB,
	ctx context.Context,
	config *background.Config,
) *background.Manager {
	t.Helper()
	if config == nil {
		config = &background.Config{}
	} else {
		copy := *config
		config = &copy
	}
	if config.SendTaskCreatedEvent == nil {
		config.SendTaskCreatedEvent = func(context.Context, *background.TaskSnapshot) error { return nil }
	}
	manager, err := background.New(ctx, config)
	require.NoError(t, err)
	return manager
}

type deepCompletionBarrier struct{}

func (deepCompletionBarrier) Check(
	context.Context,
	*durablesubagent.CompletionContext[*schema.Message],
) (durablesubagent.CompletionDecision, error) {
	return durablesubagent.Complete, nil
}

func mustDeepController(
	t *testing.T,
	manager *background.Manager,
) *durablesubagent.Controller[*schema.Message] {
	t.Helper()
	store := adksession.NewInMemoryStore[*schema.Message](nil)
	controller, err := durablesubagent.NewController(
		&durablesubagent.ControllerConfig[*schema.Message]{
			Manager: manager,
			Barrier: deepCompletionBarrier{},
			EventToInput: func(
				context.Context,
				[]*task.InputRecord,
			) (*adk.AgentInput, error) {
				return &adk.AgentInput{
					Messages: []*schema.Message{schema.UserMessage("event")},
				}, nil
			},
			SessionStore: store, CheckPointStore: store,
		},
	)
	require.NoError(t, err)
	return controller
}

type sequentialAgenticModel struct {
	responses []*schema.AgenticMessage
	callCount int32
}

func (m *sequentialAgenticModel) Generate(_ context.Context, _ []*schema.AgenticMessage, _ ...model.Option) (*schema.AgenticMessage, error) {
	idx := atomic.AddInt32(&m.callCount, 1) - 1
	if int(idx) >= len(m.responses) {
		return nil, fmt.Errorf("sequentialAgenticModel: no more responses (call #%d)", idx)
	}
	return m.responses[idx], nil
}

func (m *sequentialAgenticModel) Stream(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.StreamReader[*schema.AgenticMessage], error) {
	result, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.AgenticMessage{result}), nil
}

func agenticText(text string) *schema.AgenticMessage {
	return &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{
			schema.NewContentBlock(&schema.AssistantGenText{Text: text}),
		},
	}
}

func agenticToolCall(toolName, callID, args string) *schema.AgenticMessage {
	return &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{
			schema.NewContentBlock(&schema.FunctionToolCall{Name: toolName, CallID: callID, Arguments: args}),
		},
	}
}

func readAgenticText(msg *schema.AgenticMessage) string {
	if msg == nil {
		return ""
	}
	for _, block := range msg.ContentBlocks {
		if block == nil {
			continue
		}
		if block.AssistantGenText != nil {
			return block.AssistantGenText.Text
		}
	}
	return ""
}

func readAgenticInputText(msg *schema.AgenticMessage) string {
	if msg == nil {
		return ""
	}
	for _, block := range msg.ContentBlocks {
		if block == nil {
			continue
		}
		if block.UserInputText != nil {
			return block.UserInputText.Text
		}
	}
	return ""
}

type recordingDeepModel struct {
	mu       sync.Mutex
	inputs   [][]*schema.Message
	response *schema.Message
}

func (m *recordingDeepModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := append([]*schema.Message{}, input...)
	m.inputs = append(m.inputs, copied)
	if m.response != nil {
		return m.response, nil
	}
	return schema.AssistantMessage("ok", nil), nil
}

func (m *recordingDeepModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func (m *recordingDeepModel) snapshotInputs() [][]*schema.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([][]*schema.Message, len(m.inputs))
	for i, input := range m.inputs {
		out[i] = append([]*schema.Message{}, input...)
	}
	return out
}

type mockSearchTool struct{}

func (m *mockSearchTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "search",
		Desc: "search latest news",
	}, nil
}

func (m *mockSearchTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	return "latest news search result", nil
}

type deepMockShell struct{}

func (m *deepMockShell) Execute(ctx context.Context, req *filesystem.ExecuteRequest) (*filesystem.ExecuteResponse, error) {
	return &filesystem.ExecuteResponse{Output: "ok"}, nil
}

type deepRecoverableShellStub struct{}

func (*deepRecoverableShellStub) StartCommand(
	context.Context,
	*backgroundshell.StartCommandRequest,
) (backgroundtool.Run, error) {
	return nil, nil
}

func (*deepRecoverableShellStub) RecoverCommand(
	context.Context,
	*backgroundshell.RecoverCommandRequest,
) (backgroundtool.Run, error) {
	return nil, nil
}

func TestGenModelInput(t *testing.T) {
	ctx := context.Background()

	t.Run("WithInstruction", func(t *testing.T) {
		input := &adk.AgentInput{
			Messages: []*schema.Message{
				schema.UserMessage("hello"),
			},
		}

		msgs, err := typedGenModelInput(ctx, "You are a helpful assistant", input)
		assert.NoError(t, err)
		assert.Len(t, msgs, 2)
		assert.Equal(t, schema.System, msgs[0].Role)
		assert.Equal(t, "You are a helpful assistant", msgs[0].Content)
		assert.Equal(t, schema.User, msgs[1].Role)
		assert.Equal(t, "hello", msgs[1].Content)
	})

	t.Run("WithInstructionStripsLeadingSystemMessage", func(t *testing.T) {
		input := &adk.AgentInput{
			Messages: []*schema.Message{
				schema.SystemMessage("old"),
				schema.UserMessage("hello"),
			},
		}

		msgs, err := typedGenModelInput(ctx, "new", input)
		assert.NoError(t, err)
		assert.Len(t, msgs, 2)
		assert.Equal(t, schema.System, msgs[0].Role)
		assert.Equal(t, "new", msgs[0].Content)
		assert.Equal(t, schema.User, msgs[1].Role)
		assert.Equal(t, "hello", msgs[1].Content)

		systemCount := 0
		for _, msg := range msgs {
			if msg.Role == schema.System {
				systemCount++
			}
		}
		assert.Equal(t, 1, systemCount)
	})

	t.Run("WithInstructionStripsLeadingAgenticSystemMessage", func(t *testing.T) {
		input := &adk.TypedAgentInput[*schema.AgenticMessage]{
			Messages: []*schema.AgenticMessage{
				schema.SystemAgenticMessage("old"),
				schema.UserAgenticMessage("hello"),
			},
		}

		msgs, err := typedGenModelInput(ctx, "new", input)
		assert.NoError(t, err)
		assert.Len(t, msgs, 2)
		assert.Equal(t, schema.AgenticRoleTypeSystem, msgs[0].Role)
		assert.Equal(t, "new", readAgenticInputText(msgs[0]))
		assert.Equal(t, schema.AgenticRoleTypeUser, msgs[1].Role)
		assert.Equal(t, "hello", readAgenticInputText(msgs[1]))

		systemCount := 0
		for _, msg := range msgs {
			if msg.Role == schema.AgenticRoleTypeSystem {
				systemCount++
			}
		}
		assert.Equal(t, 1, systemCount)
	})

	t.Run("WithoutInstruction", func(t *testing.T) {
		input := &adk.AgentInput{
			Messages: []*schema.Message{
				schema.UserMessage("hello"),
			},
		}

		msgs, err := typedGenModelInput(ctx, "", input)
		assert.NoError(t, err)
		assert.Len(t, msgs, 1)
		assert.Equal(t, schema.User, msgs[0].Role)
		assert.Equal(t, "hello", msgs[0].Content)
	})

	t.Run("WithoutInstructionPreservesLeadingSystemMessage", func(t *testing.T) {
		input := &adk.AgentInput{
			Messages: []*schema.Message{
				schema.SystemMessage("old"),
				schema.UserMessage("hello"),
			},
		}

		msgs, err := typedGenModelInput(ctx, "", input)
		assert.NoError(t, err)
		assert.Len(t, msgs, 2)
		assert.Equal(t, schema.System, msgs[0].Role)
		assert.Equal(t, "old", msgs[0].Content)
		assert.Equal(t, schema.User, msgs[1].Role)
		assert.Equal(t, "hello", msgs[1].Content)
	})
}

func TestDeepAgentTurn2DeduplicatesPersistedLeadingSystemMessage(t *testing.T) {
	ctx := context.Background()
	store := adksession.NewInMemoryStore[*schema.Message](nil)
	model := &recordingDeepModel{}
	agent, err := New(ctx, &Config{
		Name:                   "deep",
		Description:            "deep agent",
		ChatModel:              model,
		Instruction:            "you are deep agent",
		MaxIteration:           2,
		WithoutWriteTodos:      true,
		WithoutGeneralSubAgent: true,
	})
	assert.NoError(t, err)
	if err != nil {
		return
	}

	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:        agent,
		SessionID:    "deep-leading-system-dedup",
		SessionStore: store,
	})
	for _, input := range [][]adk.Message{
		{schema.UserMessage("turn one")},
		{schema.UserMessage("turn two")},
	} {
		iter := runner.Run(ctx, input)
		for {
			if _, ok := iter.Next(); !ok {
				break
			}
		}
	}

	inputs := model.snapshotInputs()
	assert.Len(t, inputs, 2)
	if len(inputs) < 2 {
		return
	}
	secondTurnInput := inputs[1]
	assert.NotEmpty(t, secondTurnInput)
	if len(secondTurnInput) == 0 {
		return
	}
	assert.Equal(t, schema.System, secondTurnInput[0].Role)
	assert.Equal(t, "you are deep agent", secondTurnInput[0].Content)

	systemCount := 0
	for _, msg := range secondTurnInput {
		if msg.Role == schema.System {
			systemCount++
		}
	}
	assert.Equal(t, 1, systemCount)
	if len(secondTurnInput) > 1 {
		assert.NotEqual(t, schema.System, secondTurnInput[1].Role, "reconstructed system message must not remain after fresh system message")
	}
}

func TestWriteTodos(t *testing.T) {
	m, err := buildTypedBuiltinAgentMiddlewares(context.Background(), &Config{WithoutWriteTodos: false}, nil)
	assert.NoError(t, err)

	wt := m[0].(*typedAppendPromptTool[*schema.Message]).t.(tool.InvokableTool)

	todos := `[{"content":"content1","activeForm":"","status":"pending"},{"content":"content2","activeForm":"","status":"pending"}]`
	args := fmt.Sprintf(`{"todos": %s}`, todos)

	result, err := wt.InvokableRun(context.Background(), args)
	assert.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("Updated todo list to %s", todos), result)
}

func TestDeepAgentFilesystemExecuteDefaults(t *testing.T) {
	ctx := context.Background()
	backend := filesystem.NewInMemoryBackend()

	tests := []struct {
		name        string
		cfg         *Config
		wantToolLen int
	}{
		{
			name: "backend and shell",
			cfg: &Config{
				WithoutWriteTodos: true,
				Backend:           backend,
				Shell:             &deepMockShell{},
			},
			wantToolLen: 7,
		},
		{
			name: "shell only",
			cfg: &Config{
				WithoutWriteTodos: true,
				Shell:             &deepMockShell{},
			},
			wantToolLen: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handlers, err := buildTypedBuiltinAgentMiddlewares(ctx, tt.cfg, nil)
			assert.NoError(t, err)
			assert.Len(t, handlers, 1)

			_, runCtx, err := handlers[0].BeforeAgent(ctx, &adk.ChatModelAgentContext[*schema.Message]{})
			assert.NoError(t, err)
			assert.NotNil(t, runCtx)
			assert.Len(t, runCtx.Tools, tt.wantToolLen)

			toolNames := make(map[string]bool)
			var executeTool tool.BaseTool
			for _, tl := range runCtx.Tools {
				info, infoErr := tl.Info(ctx)
				assert.NoError(t, infoErr)
				toolNames[info.Name] = true
				if info.Name == filesystem2.ToolNameExecute {
					executeTool = tl
				}
			}

			assert.NotNil(t, executeTool)
			assert.False(t, toolNames["execute_output"])
			assert.False(t, toolNames["execute_wait"])
			assert.False(t, toolNames["execute_stop"])
			assert.False(t, toolNames["execute_list"])

			info, err := executeTool.Info(ctx)
			assert.NoError(t, err)
			js, err := info.ParamsOneOf.ToJSONSchema()
			assert.NoError(t, err)
			_, ok := js.Properties.Get("command")
			assert.True(t, ok)
			_, ok = js.Properties.Get("mode")
			assert.False(t, ok)
			_, ok = js.Properties.Get("wait_ms")
			assert.False(t, ok)
		})
	}
}

func TestDeepAgentManagerWiring(t *testing.T) {
	ctx := context.Background()

	// With a Manager, the top-level built-in handlers route execute through it, so
	// the execute tool gains a run_in_background field.
	mgr := mustNewBackgroundManager(t, ctx, &background.Config{})
	defer func() { _ = mgr.Close(ctx) }()

	handlers, err := buildTypedBuiltinAgentMiddlewares(ctx, &Config{
		WithoutWriteTodos: true,
	}, &TaskConfig{
		Manager:    mgr,
		LocalShell: &LocalShellConfig{Shell: &deepMockShell{}},
	})
	assert.NoError(t, err)
	assert.Len(t, handlers, 1)

	_, runCtx, err := handlers[0].BeforeAgent(ctx, &adk.ChatModelAgentContext[*schema.Message]{})
	assert.NoError(t, err)
	assert.NotNil(t, runCtx)
	assert.Len(t, runCtx.Tools, 1)

	info, err := runCtx.Tools[0].Info(ctx)
	assert.NoError(t, err)
	js, err := info.ParamsOneOf.ToJSONSchema()
	assert.NoError(t, err)
	_, ok := js.Properties.Get("run_in_background")
	assert.True(t, ok, "managed execute must expose run_in_background")

	// Without a Manager, the same handlers produce a command-only execute tool.
	plain, err := buildTypedBuiltinAgentMiddlewares(ctx, &Config{
		WithoutWriteTodos: true,
		Shell:             &deepMockShell{},
	}, nil)
	assert.NoError(t, err)
	_, plainCtx, err := plain[0].BeforeAgent(ctx, &adk.ChatModelAgentContext[*schema.Message]{})
	assert.NoError(t, err)
	plainInfo, err := plainCtx.Tools[0].Info(ctx)
	assert.NoError(t, err)
	plainJS, err := plainInfo.ParamsOneOf.ToJSONSchema()
	assert.NoError(t, err)
	_, ok = plainJS.Properties.Get("run_in_background")
	assert.False(t, ok, "unmanaged execute must not expose run_in_background")
}

func TestDeepRecoverableShellDoesNotCreateLocalRunner(t *testing.T) {
	manager := mustNewBackgroundManager(t, context.Background(), nil)
	defer manager.Close(context.Background())
	shell := &deepRecoverableShellStub{}
	background := &TaskConfig{
		Manager: manager,
		RecoverableShell: &RecoverableShellConfig{
			Shell: shell,
		},
	}

	require.Same(t, shell, deepRecoverableShell(background))
	require.Empty(t, deepShellOutputDir(background))
	runner, err := deepLocalShellRunner(background)
	require.NoError(t, err)
	require.Nil(t, runner)

	handlers, err := buildTypedBuiltinAgentMiddlewares(
		context.Background(),
		&Config{WithoutWriteTodos: true},
		background,
	)
	require.NoError(t, err)
	require.Len(t, handlers, 1)
}

func TestDeepTaskConfigRequiresExplicitCapabilities(t *testing.T) {
	backgroundType := reflect.TypeOf(TaskConfig{})
	for _, field := range []string{
		"Manager", "SubAgents", "RecoverableShell", "LocalShell",
		"ForegroundTimeoutMs", "ShouldAutoBackground",
	} {
		_, ok := backgroundType.FieldByName(field)
		require.True(t, ok, "TaskConfig.%s must exist", field)
	}
	for _, field := range []string{"Local", "Durable"} {
		_, ok := backgroundType.FieldByName(field)
		require.False(t, ok, "TaskConfig.%s must stay removed", field)
	}
	_, hasLegacyRecoverableShell := reflect.TypeOf(Config{}).FieldByName("RecoverableShell")
	require.False(t, hasLegacyRecoverableShell)

	_, err := New(context.Background(), &Config{Tasks: &TaskConfig{}})
	require.ErrorContains(t, err, "Manager is required")

	manager := mustNewBackgroundManager(t, context.Background(), nil)
	_, err = New(context.Background(), &Config{Tasks: &TaskConfig{
		Manager: manager,
	}})
	require.ErrorContains(t, err, "at least one background capability is required")

	_, err = New(context.Background(), &Config{Tasks: &TaskConfig{
		Manager:    manager,
		LocalShell: &LocalShellConfig{},
	}})
	require.ErrorContains(t, err, "requires Shell or StreamingShell")

	_, err = New(context.Background(), &Config{Tasks: &TaskConfig{
		Manager:   manager,
		SubAgents: &TypedDurableSubAgentConfig[*schema.Message]{},
	}})
	require.ErrorContains(t, err, "Controller is required")

	controllerManager := mustNewBackgroundManager(t, context.Background(), nil)
	defer controllerManager.Close(context.Background())
	controller := mustDeepController(t, controllerManager)
	derived := &TaskConfig{
		SubAgents: &TypedDurableSubAgentConfig[*schema.Message]{
			Runtime: controller,
		},
	}
	require.NoError(t, validateTypedConfig(&Config{Tasks: derived}))
	require.Same(t, controllerManager, deepBackgroundManager(derived))

	otherManager := mustNewBackgroundManager(t, context.Background(), nil)
	defer otherManager.Close(context.Background())
	derived.Manager = otherManager
	err = validateTypedConfig(&Config{Tasks: derived})
	require.ErrorContains(t, err, "must share the same Manager")
}

func TestDeepSubAgentBackgroundForwardsRunOptionsFactories(t *testing.T) {
	factory := func() ([]adk.AgentRunOption, error) {
		return []adk.AgentRunOption{adk.WithTimelineEvents()}, nil
	}
	format := func(
		_ context.Context,
		agentName string,
		message *schema.Message,
	) (string, error) {
		return agentName + ": " + message.Content, nil
	}
	manager := mustNewBackgroundManager(t, context.Background(), nil)
	defer manager.Close(context.Background())
	controller := mustDeepController(t, manager)
	background := deepSubagentBackground(&TypedConfig[*schema.Message]{
		Tasks: &TypedTaskConfig[*schema.Message]{
			Manager:          manager,
			TranscriptFormat: format,
			SubAgents: &TypedDurableSubAgentConfig[*schema.Message]{
				Runtime: controller,
				RunOptionsFactories: map[string]durablesubagent.RunOptionsFactory{
					"worker": factory,
				},
			},
		},
	})
	require.NotNil(t, background.Durable)
	require.Same(t, controller, background.Durable.Runtime)
	require.NotNil(t, background.Durable.RunOptionsFactories["worker"])
	options, err := background.Durable.RunOptionsFactories["worker"]()
	require.NoError(t, err)
	assert.Len(t, options, 1)
	formatted, err := background.TranscriptFormat(
		context.Background(), "worker", schema.AssistantMessage("done", nil),
	)
	require.NoError(t, err)
	assert.Equal(t, "worker: done", formatted)
}

// NewTyped derives the shared Manager from the Controller and injects the
// task_output/task_stop control tools exactly once at the top level.
func TestDeepAgentNewTypedWithControllerManager(t *testing.T) {
	ctx := context.Background()
	store := background.NewInMemoryStore(nil)
	mgr := mustNewBackgroundManager(t, ctx, &background.Config{
		Tasks: store, TaskEvents: store})
	defer func() { _ = mgr.Close(ctx) }()
	controller := mustDeepController(t, mgr)

	cm := mockModel.NewMockToolCallingChatModel(gomock.NewController(t))
	agent, err := New(ctx, &Config{
		Name:        "deep",
		Description: "deep agent",
		ChatModel:   cm,
		Shell:       &deepMockShell{},
		Tasks: &TaskConfig{
			SubAgents: &TypedDurableSubAgentConfig[*schema.Message]{
				Runtime: controller,
			},
		},
	})
	assert.NoError(t, err)
	assert.NotNil(t, agent)
}

func TestDeepAgentManualFilesystemMiddlewarePath(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)
	cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

	fsMW, err := filesystem2.New(ctx, &filesystem2.MiddlewareConfig{
		Shell:             &deepMockShell{},
		ExecuteToolConfig: &filesystem2.ExecuteToolConfig{},
	})
	assert.NoError(t, err)

	_, runCtx, err := fsMW.BeforeAgent(ctx, &adk.ChatModelAgentContext[*schema.Message]{})
	assert.NoError(t, err)
	assert.Len(t, runCtx.Tools, 1)
	info, err := runCtx.Tools[0].Info(ctx)
	assert.NoError(t, err)
	assert.Equal(t, filesystem2.ToolNameExecute, info.Name)
	js, err := info.ParamsOneOf.ToJSONSchema()
	assert.NoError(t, err)
	_, ok := js.Properties.Get("command")
	assert.True(t, ok)

	agent, err := New(ctx, &Config{
		Name:                   "deep",
		Description:            "deep agent",
		ChatModel:              cm,
		WithoutWriteTodos:      true,
		WithoutGeneralSubAgent: true,
		Handlers:               []adk.ChatModelAgentMiddleware{fsMW},
	})
	assert.NoError(t, err)
	assert.NotNil(t, agent)
}

func TestDeepSubAgentSharesSessionValues(t *testing.T) {
	ctx := context.Background()
	spy := &spySubAgent{}

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)
	cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

	calls := 0
	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.Message, error) {
			calls++
			if calls == 1 {
				c := schema.ToolCall{ID: "id-1", Type: "function"}
				c.Function.Name = taskToolName
				c.Function.Arguments = fmt.Sprintf(`{"subagent_type":"%s","description":"from_parent"}`, spy.Name(ctx))
				return schema.AssistantMessage("", []schema.ToolCall{c}), nil
			}
			return schema.AssistantMessage("done", nil), nil
		}).AnyTimes()

	agent, err := New(ctx, &Config{
		Name:                   "deep",
		Description:            "deep agent",
		ChatModel:              cm,
		Instruction:            "you are deep agent",
		SubAgents:              []adk.Agent{spy},
		ToolsConfig:            adk.ToolsConfig{},
		MaxIteration:           2,
		WithoutWriteTodos:      true,
		WithoutGeneralSubAgent: true,
	})
	assert.NoError(t, err)

	r := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})
	it := r.Run(ctx, []adk.Message{schema.UserMessage("hi")}, adk.WithSessionValues(map[string]any{"parent_key": "parent_val"}))
	for {
		if _, ok := it.Next(); !ok {
			break
		}
	}

	assert.Equal(t, "parent_val", spy.seenParentValue)
}

func TestDeepSubAgentFollowsStreamingMode(t *testing.T) {
	ctx := context.Background()
	spy := &spyStreamingSubAgent{}

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)
	cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

	subName := spy.Name(ctx)
	cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(schema.StreamReaderFromArray([]*schema.Message{
			schema.AssistantMessage("", []schema.ToolCall{
				{
					ID:   "id-1",
					Type: "function",
					Function: schema.FunctionCall{
						Name:      taskToolName,
						Arguments: fmt.Sprintf(`{"subagent_type":"%s","description":"from_parent"}`, subName),
					},
				},
			}),
		}), nil).
		Times(1)
	cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(schema.StreamReaderFromArray([]*schema.Message{
			schema.AssistantMessage("done", nil),
		}), nil).
		Times(1)

	agent, err := New(ctx, &Config{
		Name:                   "deep",
		Description:            "deep agent",
		ChatModel:              cm,
		Instruction:            "you are deep agent",
		SubAgents:              []adk.Agent{spy},
		ToolsConfig:            adk.ToolsConfig{},
		MaxIteration:           2,
		WithoutWriteTodos:      true,
		WithoutGeneralSubAgent: true,
	})
	assert.NoError(t, err)

	r := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent, EnableStreaming: true})
	it := r.Run(ctx, []adk.Message{schema.UserMessage("hi")})
	for {
		if _, ok := it.Next(); !ok {
			break
		}
	}

	assert.True(t, spy.seenEnableStreaming)
}

type spySubAgent struct {
	seenParentValue any
}

func (s *spySubAgent) Name(context.Context) string        { return "spy-subagent" }
func (s *spySubAgent) Description(context.Context) string { return "spy" }
func (s *spySubAgent) Run(ctx context.Context, _ *adk.AgentInput, _ ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
	s.seenParentValue, _ = adk.GetSessionValue(ctx, "parent_key")
	it, gen := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	gen.Send(adk.EventFromMessage(schema.AssistantMessage("ok", nil), nil, schema.Assistant, ""))
	gen.Close()
	return it
}

type spyStreamingSubAgent struct {
	seenEnableStreaming bool
}

func (s *spyStreamingSubAgent) Name(context.Context) string        { return "spy-streaming-subagent" }
func (s *spyStreamingSubAgent) Description(context.Context) string { return "spy" }
func (s *spyStreamingSubAgent) Run(_ context.Context, input *adk.AgentInput, _ ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
	if input != nil {
		s.seenEnableStreaming = input.EnableStreaming
	}
	it, gen := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	gen.Send(adk.EventFromMessage(schema.AssistantMessage("ok", nil), nil, schema.Assistant, ""))
	gen.Close()
	return it
}

func TestDeepAgentWithPlanExecuteSubAgent_InternalEventsEmitted(t *testing.T) {
	ctx := context.Background()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	deepModel := mockModel.NewMockToolCallingChatModel(ctrl)
	plannerModel := mockModel.NewMockToolCallingChatModel(ctrl)
	executorModel := mockModel.NewMockToolCallingChatModel(ctrl)
	replannerModel := mockModel.NewMockToolCallingChatModel(ctrl)

	deepModel.EXPECT().WithTools(gomock.Any()).Return(deepModel, nil).AnyTimes()
	plannerModel.EXPECT().WithTools(gomock.Any()).Return(plannerModel, nil).AnyTimes()
	executorModel.EXPECT().WithTools(gomock.Any()).Return(executorModel, nil).AnyTimes()
	replannerModel.EXPECT().WithTools(gomock.Any()).Return(replannerModel, nil).AnyTimes()

	plannerModel.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, input []*schema.Message, opts ...interface{}) (*schema.StreamReader[*schema.Message], error) {
			sr, sw := schema.Pipe[*schema.Message](1)
			go func() {
				defer sw.Close()
				planJSON := `{"steps":["step1"]}`
				msg := schema.AssistantMessage("", []schema.ToolCall{
					{
						ID:   "plan_call_1",
						Type: "function",
						Function: schema.FunctionCall{
							Name:      "plan",
							Arguments: planJSON,
						},
					},
				})
				sw.Send(msg, nil)
			}()
			return sr, nil
		},
	).Times(1)

	executorModel.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.Message, error) {
			return schema.AssistantMessage("executed step1", nil), nil
		},
	).Times(1)

	replannerModel.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, input []*schema.Message, opts ...interface{}) (*schema.StreamReader[*schema.Message], error) {
			sr, sw := schema.Pipe[*schema.Message](1)
			go func() {
				defer sw.Close()
				responseJSON := `{"response":"final response"}`
				msg := schema.AssistantMessage("", []schema.ToolCall{
					{
						ID:   "respond_call_1",
						Type: "function",
						Function: schema.FunctionCall{
							Name:      "respond",
							Arguments: responseJSON,
						},
					},
				})
				sw.Send(msg, nil)
			}()
			return sr, nil
		},
	).Times(1)

	planner, err := planexecute.NewPlanner(ctx, &planexecute.PlannerConfig{
		ToolCallingChatModel: plannerModel,
	})
	assert.NoError(t, err)

	executor, err := planexecute.NewExecutor(ctx, &planexecute.ExecutorConfig{
		Model: executorModel,
	})
	assert.NoError(t, err)

	replanner, err := planexecute.NewReplanner(ctx, &planexecute.ReplannerConfig{
		ChatModel: replannerModel,
	})
	assert.NoError(t, err)

	planExecuteAgent, err := planexecute.New(ctx, &planexecute.Config{
		Planner:   planner,
		Executor:  executor,
		Replanner: replanner,
	})
	assert.NoError(t, err)

	namedPlanExecuteAgent := &namedPlanExecuteAgent{
		ResumableAgent: planExecuteAgent,
		name:           "plan_execute_subagent",
		description:    "a plan execute subagent",
	}

	deepModelCalls := 0
	deepModel.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.Message, error) {
			deepModelCalls++
			if deepModelCalls == 1 {
				c := schema.ToolCall{ID: "id-1", Type: "function"}
				c.Function.Name = taskToolName
				c.Function.Arguments = fmt.Sprintf(`{"subagent_type":"%s","description":"execute the plan"}`, namedPlanExecuteAgent.name)
				return schema.AssistantMessage("", []schema.ToolCall{c}), nil
			}
			return schema.AssistantMessage("done", nil), nil
		}).AnyTimes()

	deepAgent, err := New(ctx, &Config{
		Name:                   "deep",
		Description:            "deep agent",
		ChatModel:              deepModel,
		Instruction:            "you are deep agent",
		SubAgents:              []adk.Agent{namedPlanExecuteAgent},
		ToolsConfig:            adk.ToolsConfig{EmitInternalEvents: true},
		MaxIteration:           5,
		WithoutWriteTodos:      true,
		WithoutGeneralSubAgent: true,
	})
	assert.NoError(t, err)

	r := adk.NewRunner(ctx, adk.RunnerConfig{Agent: deepAgent})
	it := r.Run(ctx, []adk.Message{schema.UserMessage("hi")})

	var events []*adk.AgentEvent
	for {
		event, ok := it.Next()
		if !ok {
			break
		}
		events = append(events, event)
	}

	assert.Greater(t, len(events), 0, "should have at least one event")

	var deepAgentEvents []*adk.AgentEvent
	var plannerEvents []*adk.AgentEvent
	var executorEvents []*adk.AgentEvent
	var replannerEvents []*adk.AgentEvent
	var planExecuteEvents []*adk.AgentEvent

	for _, event := range events {
		switch event.AgentName {
		case "deep":
			deepAgentEvents = append(deepAgentEvents, event)
		case "planner":
			plannerEvents = append(plannerEvents, event)
		case "executor":
			executorEvents = append(executorEvents, event)
		case "replanner":
			replannerEvents = append(replannerEvents, event)
		case "plan_execute_replan", "execute_replan":
			planExecuteEvents = append(planExecuteEvents, event)
		}
	}

	assert.Greater(t, len(deepAgentEvents), 0, "should have events from deep agent")

	assert.Greater(t, len(plannerEvents), 0, "planner internal events should be emitted when EmitInternalEvents is true")
	assert.Greater(t, len(executorEvents), 0, "executor internal events should be emitted when EmitInternalEvents is true")
	assert.Greater(t, len(replannerEvents), 0, "replanner internal events should be emitted when EmitInternalEvents is true")

	t.Logf("Total events: %d", len(events))
	t.Logf("Deep agent events: %d", len(deepAgentEvents))
	t.Logf("Planner events: %d", len(plannerEvents))
	t.Logf("Executor events: %d", len(executorEvents))
	t.Logf("Replanner events: %d", len(replannerEvents))
	t.Logf("PlanExecute events: %d", len(planExecuteEvents))
}

type namedPlanExecuteAgent struct {
	adk.ResumableAgent
	name        string
	description string
}

func (n *namedPlanExecuteAgent) Name(_ context.Context) string {
	return n.name
}

func (n *namedPlanExecuteAgent) Description(_ context.Context) string {
	return n.description
}

func TestAgenticDeepAgentEmitInternalEventsFromSubAgent(t *testing.T) {
	ctx := context.Background()

	subAgentModel := &sequentialAgenticModel{
		responses: []*schema.AgenticMessage{
			agenticToolCall("search", "search-call-1", `{"query":"latest news"}`),
			agenticText("search result from subagent"),
		},
	}
	webSearchAgent, err := adk.NewTypedChatModelAgent(ctx, &adk.TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "web_searcher",
		Description: "agent dedicated to web search",
		Model:       subAgentModel,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{&mockSearchTool{}},
			},
		},
	})
	assert.NoError(t, err)

	deepModel := &sequentialAgenticModel{
		responses: []*schema.AgenticMessage{
			agenticToolCall(taskToolName, "task-call-1", `{"subagent_type":"web_searcher","description":"search latest news"}`),
			agenticText("deep final answer"),
		},
	}
	deepAgent, err := NewTyped(ctx, &TypedConfig[*schema.AgenticMessage]{
		Name:                   "deep",
		Description:            "deep agent",
		ChatModel:              deepModel,
		SubAgents:              []adk.TypedAgent[*schema.AgenticMessage]{webSearchAgent},
		ToolsConfig:            adk.ToolsConfig{EmitInternalEvents: true},
		WithoutWriteTodos:      true,
		WithoutGeneralSubAgent: true,
	})
	assert.NoError(t, err)

	runner := adk.NewTypedRunner(adk.TypedRunnerConfig[*schema.AgenticMessage]{
		Agent:           deepAgent,
		EnableStreaming: true,
	})
	iter := runner.Run(ctx, []*schema.AgenticMessage{
		schema.UserAgenticMessage("search latest news"),
	})

	var emittedTexts []string
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		if event.Output.MessageOutput.IsStreaming {
			stream := event.Output.MessageOutput.MessageStream
			for {
				chunk, err := stream.Recv()
				if err == io.EOF {
					break
				}
				assert.NoError(t, err)
				if text := readAgenticText(chunk); text != "" {
					emittedTexts = append(emittedTexts, fmt.Sprintf("%s:%s", event.AgentName, text))
				}
			}
			continue
		}
		if text := readAgenticText(event.Output.MessageOutput.Message); text != "" {
			emittedTexts = append(emittedTexts, fmt.Sprintf("%s:%s", event.AgentName, text))
		}
	}

	t.Logf("emitted texts: %v", emittedTexts)
	assert.Contains(t, emittedTexts, "deep:deep final answer", "expected DeepAgent final result to be emitted")
	assert.Contains(t, emittedTexts, "web_searcher:search result from subagent", "expected DeepAgent to emit Agentic sub-agent events when EmitInternalEvents is true")
}

func TestDeepAgentOutputKey(t *testing.T) {
	t.Run("OutputKeyStoresInSession", func(t *testing.T) {
		ctx := context.Background()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		cm := mockModel.NewMockToolCallingChatModel(ctrl)
		cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("Hello from DeepAgent", nil), nil).
			Times(1)

		agent, err := New(ctx, &Config{
			Name:                   "deep",
			Description:            "deep agent",
			ChatModel:              cm,
			Instruction:            "you are deep agent",
			MaxIteration:           2,
			WithoutWriteTodos:      true,
			WithoutGeneralSubAgent: true,
			OutputKey:              "deep_output",
		})
		assert.NoError(t, err)

		var capturedSessionValues map[string]any
		wrappedAgent := &sessionCaptureAgent{
			Agent:          agent,
			captureSession: func(values map[string]any) { capturedSessionValues = values },
		}

		r := adk.NewRunner(ctx, adk.RunnerConfig{Agent: wrappedAgent})
		it := r.Run(ctx, []adk.Message{schema.UserMessage("hi")})
		for {
			if _, ok := it.Next(); !ok {
				break
			}
		}

		assert.Contains(t, capturedSessionValues, "deep_output")
		assert.Equal(t, "Hello from DeepAgent", capturedSessionValues["deep_output"])
	})

	t.Run("OutputKeyWithStreamingStoresInSession", func(t *testing.T) {
		ctx := context.Background()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		cm := mockModel.NewMockToolCallingChatModel(ctrl)
		cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

		cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.StreamReaderFromArray([]*schema.Message{
				schema.AssistantMessage("Hello", nil),
				schema.AssistantMessage(" from", nil),
				schema.AssistantMessage(" DeepAgent", nil),
			}), nil).
			Times(1)

		agent, err := New(ctx, &Config{
			Name:                   "deep",
			Description:            "deep agent",
			ChatModel:              cm,
			Instruction:            "you are deep agent",
			MaxIteration:           2,
			WithoutWriteTodos:      true,
			WithoutGeneralSubAgent: true,
			OutputKey:              "deep_output",
		})
		assert.NoError(t, err)

		var capturedSessionValues map[string]any
		wrappedAgent := &sessionCaptureAgent{
			Agent:          agent,
			captureSession: func(values map[string]any) { capturedSessionValues = values },
		}

		r := adk.NewRunner(ctx, adk.RunnerConfig{Agent: wrappedAgent, EnableStreaming: true})
		it := r.Run(ctx, []adk.Message{schema.UserMessage("hi")})
		for {
			if _, ok := it.Next(); !ok {
				break
			}
		}

		assert.Contains(t, capturedSessionValues, "deep_output")
		assert.Equal(t, "Hello from DeepAgent", capturedSessionValues["deep_output"])
	})

	t.Run("OutputKeyNotSetWhenEmpty", func(t *testing.T) {
		ctx := context.Background()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		cm := mockModel.NewMockToolCallingChatModel(ctrl)
		cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("Hello from DeepAgent", nil), nil).
			Times(1)

		agent, err := New(ctx, &Config{
			Name:                   "deep",
			Description:            "deep agent",
			ChatModel:              cm,
			Instruction:            "you are deep agent",
			MaxIteration:           2,
			WithoutWriteTodos:      true,
			WithoutGeneralSubAgent: true,
		})
		assert.NoError(t, err)

		var capturedSessionValues map[string]any
		wrappedAgent := &sessionCaptureAgent{
			Agent:          agent,
			captureSession: func(values map[string]any) { capturedSessionValues = values },
		}

		r := adk.NewRunner(ctx, adk.RunnerConfig{Agent: wrappedAgent})
		it := r.Run(ctx, []adk.Message{schema.UserMessage("hi")})
		for {
			if _, ok := it.Next(); !ok {
				break
			}
		}

		assert.NotContains(t, capturedSessionValues, "deep_output")
	})
}

type sessionCaptureAgent struct {
	adk.Agent
	captureSession func(map[string]any)
}

func (s *sessionCaptureAgent) Run(ctx context.Context, input *adk.AgentInput, opts ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
	innerIt := s.Agent.Run(ctx, input, opts...)
	it, gen := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	go func() {
		defer gen.Close()
		for {
			event, ok := innerIt.Next()
			if !ok {
				break
			}
			gen.Send(event)
		}
		s.captureSession(adk.GetSessionValues(ctx))
	}()
	return it
}
