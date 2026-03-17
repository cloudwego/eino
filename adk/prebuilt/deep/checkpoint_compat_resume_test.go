package deep

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	mockModel "github.com/cloudwego/eino/internal/mock/components/model"
	"github.com/cloudwego/eino/schema"
)

type compatCheckpointStore struct {
	data map[string][]byte
}

func newCompatCheckpointStore() *compatCheckpointStore {
	return &compatCheckpointStore{data: make(map[string][]byte)}
}

func (s *compatCheckpointStore) Set(_ context.Context, key string, value []byte) error {
	s.data[key] = append([]byte(nil), value...)
	return nil
}

func (s *compatCheckpointStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	v, ok := s.data[key]
	if !ok {
		return nil, false, nil
	}
	return append([]byte(nil), v...), true, nil
}

type interruptingSubAgentTool struct {
	name string
}

func (t *interruptingSubAgentTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: t.name,
		Desc: "interrupts on first call and resumes from stored state",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"action": {Type: schema.String},
		}),
	}, nil
}

func (t *interruptingSubAgentTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	_, _, _ = tool.GetInterruptState[string](ctx)
	return "resumed", nil
}

func readTestdataBytes(t *testing.T, filename string) []byte {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	assert.True(t, ok)
	p := filepath.Join(filepath.Dir(file), "testdata", filename)
	b, err := os.ReadFile(p)
	assert.NoError(t, err)
	assert.NotEmpty(t, b)
	return b
}

func TestDeepAgentCheckpointCompat_V0_8_ResumeFromV0_7(t *testing.T) {
	ctx := context.Background()

	checkpointID := "checkpoint_compat_v0_7_37"
	data := readTestdataBytes(t, "checkpoint_data_v0.7.37.bin")

	store := newCompatCheckpointStore()
	assert.NoError(t, store.Set(ctx, checkpointID, data))

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	interruptToolName := "interrupt_in_subagent_tool"
	subTool := &interruptingSubAgentTool{name: interruptToolName}

	deepModel := mockModel.NewMockBaseChatModel(ctrl)
	subModel := mockModel.NewMockBaseChatModel(ctrl)

	deepModel.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.Message, error) {
			return schema.AssistantMessage("deep done", nil), nil
		}).AnyTimes()

	subModel.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.Message, error) {
			return schema.AssistantMessage("sub done", nil), nil
		}).AnyTimes()

	subAgent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "sub_chatmodel_agent",
		Description: "sub agent",
		Model:       subModel,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{subTool},
			},
		},
		MaxIterations: 4,
	})
	assert.NoError(t, err)

	deepAgent, err := New(ctx, &Config{
		Name:                   "deep",
		Description:            "deep agent",
		ChatModel:              deepModel,
		SubAgents:              []adk.Agent{subAgent},
		MaxIteration:           4,
		WithoutWriteTodos:      true,
		WithoutGeneralSubAgent: true,
	})
	assert.NoError(t, err)

	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           deepAgent,
		CheckPointStore: store,
	})

	it, err := runner.Resume(ctx, checkpointID)
	assert.NoError(t, err)

	var sawDeepDone bool
	var sawAnyOutput bool
	for {
		ev, ok := it.Next()
		if !ok {
			break
		}
		assert.NoError(t, ev.Err)
		if ev.Output != nil && ev.Output.MessageOutput != nil && ev.Output.MessageOutput.Message != nil {
			sawAnyOutput = true
			msg := ev.Output.MessageOutput.Message
			if msg.Role == schema.Assistant && strings.Contains(msg.Content, "deep done") {
				sawDeepDone = true
			}
		}
	}

	assert.True(t, sawAnyOutput)
	assert.True(t, sawDeepDone)
}

func TestDeepAgentCheckpointCompat_V0_8_ResumeFromV0_8_2(t *testing.T) {
	ctx := context.Background()

	checkpointID := "checkpoint_compat_v0_8_2"
	data := readTestdataBytes(t, "checkpoint_data_v0.8.2.bin")

	store := newCompatCheckpointStore()
	assert.NoError(t, store.Set(ctx, checkpointID, data))

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	interruptToolName := "interrupt_in_subagent_tool"
	subTool := &interruptingSubAgentTool{name: interruptToolName}

	deepModel := mockModel.NewMockBaseChatModel(ctrl)
	subModel := mockModel.NewMockBaseChatModel(ctrl)

	deepModel.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.Message, error) {
			return schema.AssistantMessage("deep done", nil), nil
		}).AnyTimes()

	subModel.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.Message, error) {
			return schema.AssistantMessage("sub done", nil), nil
		}).AnyTimes()

	subAgent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "sub_chatmodel_agent",
		Description: "sub agent",
		Model:       subModel,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{subTool},
			},
		},
		MaxIterations: 4,
	})
	assert.NoError(t, err)

	deepAgent, err := New(ctx, &Config{
		Name:                   "deep",
		Description:            "deep agent",
		ChatModel:              deepModel,
		SubAgents:              []adk.Agent{subAgent},
		MaxIteration:           4,
		WithoutWriteTodos:      true,
		WithoutGeneralSubAgent: true,
	})
	assert.NoError(t, err)

	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           deepAgent,
		CheckPointStore: store,
	})

	it, err := runner.Resume(ctx, checkpointID)
	assert.NoError(t, err)

	var sawDeepDone bool
	var sawAnyOutput bool
	for {
		ev, ok := it.Next()
		if !ok {
			break
		}
		assert.NoError(t, ev.Err)
		if ev.Output != nil && ev.Output.MessageOutput != nil && ev.Output.MessageOutput.Message != nil {
			sawAnyOutput = true
			msg := ev.Output.MessageOutput.Message
			if msg.Role == schema.Assistant && strings.Contains(msg.Content, "deep done") {
				sawDeepDone = true
			}
		}
	}

	assert.True(t, sawAnyOutput)
	assert.True(t, sawDeepDone)
}
