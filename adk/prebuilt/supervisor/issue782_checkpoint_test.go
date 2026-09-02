package supervisor

// Regression test for issue #782: in a supervisor multi-agent flow, an
// internal error from one sub-agent (e.g. exceeding max iterations) used to
// leave a gob-unencodable error value (compose.internalError) inside the run
// session. When a later sub-agent interrupted and the runner saved a
// checkpoint, gob failed with "type not registered for interface:
// compose.internalError", breaking the whole interrupt/resume flow.
//
// With the gobSafeError sanitization in agentEventWrapper.GobEncode, the
// checkpoint save must succeed.

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type issue782Store struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (s *issue782Store) Set(_ context.Context, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = value
	return nil
}

func (s *issue782Store) Get(_ context.Context, key string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[key]
	return v, ok, nil
}

// issue782Model either returns scripted messages in order, or (when
// alwaysToolCall is set) keeps returning a tool call so that the agent hits
// its MaxIterations limit and fails with an internal error.
type issue782Model struct {
	name           string
	steps          []*schema.Message
	alwaysToolCall string
	calls          int32
}

func (m *issue782Model) Generate(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	n := atomic.AddInt32(&m.calls, 1)
	if m.alwaysToolCall != "" {
		return issue782ToolCallMsg(m.alwaysToolCall, m.name), nil
	}
	if int(n) <= len(m.steps) {
		return m.steps[n-1], nil
	}
	return schema.AssistantMessage(m.name+" final answer", nil), nil
}

func (m *issue782Model) Stream(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, in, opts...)
	if err != nil {
		return nil, err
	}
	sr, sw := schema.Pipe[*schema.Message](1)
	sw.Send(msg, nil)
	sw.Close()
	return sr, nil
}

func (m *issue782Model) WithTools(_ []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func issue782TransferMsg(agent string) *schema.Message {
	return schema.AssistantMessage("", []schema.ToolCall{{
		ID:       "issue782_transfer_" + agent,
		Function: schema.FunctionCall{Name: "transfer_to_agent", Arguments: `{"agent_name":"` + agent + `"}`},
	}})
}

func issue782ToolCallMsg(name, caller string) *schema.Message {
	return schema.AssistantMessage("", []schema.ToolCall{{
		ID:       "issue782_call_" + name + "_" + caller,
		Function: schema.FunctionCall{Name: name, Arguments: "{}"},
	}})
}

type issue782LoopTool struct{ name string }

func (t *issue782LoopTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: t.name, Desc: "noop tool that always succeeds"}, nil
}

func (t *issue782LoopTool) InvokableRun(_ context.Context, _ string, _ ...tool.Option) (string, error) {
	return "loop ok", nil
}

type issue782InterruptTool struct{ name string }

func (t *issue782InterruptTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: t.name, Desc: "tool that triggers an interrupt"}, nil
}

func (t *issue782InterruptTool) InvokableRun(ctx context.Context, _ string, _ ...tool.Option) (string, error) {
	if was, _, _ := compose.GetInterruptState[any](ctx); !was {
		return "", compose.Interrupt(ctx, "need user input")
	}
	if isResume, has, data := compose.GetResumeContext[string](ctx); isResume && has {
		return data, nil
	}
	return "resumed without data", nil
}

func TestSupervisorSubAgentErrorThenInterruptCheckpoint(t *testing.T) {
	ctx := context.Background()

	// SubAgentA: exceeds MaxIterations -> internal node error -> the error
	// event enters the run session.
	subA, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          "SubAgentA",
		Description:   "agent that exceeds max iterations",
		Instruction:   "you must call the loop_tool",
		Model:         &issue782Model{name: "SubAgentA", alwaysToolCall: "loop_tool"},
		MaxIterations: 2,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{&issue782LoopTool{name: "loop_tool"}},
			},
		},
	})
	assert.NoError(t, err)

	// SubAgentB: interrupts, which triggers a checkpoint save.
	subB, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "SubAgentB",
		Description: "interruptible agent",
		Instruction: "call the interrupt_tool",
		Model: &issue782Model{name: "SubAgentB", steps: []*schema.Message{
			issue782ToolCallMsg("interrupt_tool", "SubAgentB"),
		}},
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{&issue782InterruptTool{name: "interrupt_tool"}},
			},
		},
	})
	assert.NoError(t, err)

	// Supervisor: transfer to SubAgentA first, then to SubAgentB.
	mainAgent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "MainAgent",
		Description: "supervisor",
		Instruction: "route work to sub agents",
		Model: &issue782Model{name: "MainAgent", steps: []*schema.Message{
			issue782TransferMsg("SubAgentA"),
			issue782TransferMsg("SubAgentB"),
		}},
	})
	assert.NoError(t, err)

	sup, err := New(ctx, &Config{
		Supervisor: mainAgent,
		SubAgents:  []adk.Agent{subA, subB},
	})
	assert.NoError(t, err)

	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           sup,
		CheckPointStore: &issue782Store{m: map[string][]byte{}},
	})

	// Phase 1: SubAgentA fails, flow continues to SubAgentB which interrupts
	// and saves a checkpoint. The save must not fail on the gob-unencodable
	// error left behind by SubAgentA.
	iter := runner.Run(ctx, []adk.Message{schema.UserMessage("start")},
		adk.WithCheckPointID("issue782"))

	var gobErr error
	var interrupted bool
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		if ev.Err != nil && strings.Contains(ev.Err.Error(), "gob") {
			gobErr = ev.Err
		}
		if ev.Action != nil && ev.Action.Interrupted != nil {
			interrupted = true
		}
	}
	assert.NoError(t, gobErr, "checkpoint save must not fail on gob-unencodable error values")
	assert.True(t, interrupted, "SubAgentB should interrupt")

	// Phase 2: resume must read back the saved checkpoint without gob errors.
	iter2 := runner.Run(ctx, []adk.Message{schema.UserMessage("user reply")},
		adk.WithCheckPointID("issue782"))

	for {
		ev, ok := iter2.Next()
		if !ok {
			break
		}
		if ev.Err != nil && strings.Contains(ev.Err.Error(), "gob") {
			gobErr = ev.Err
		}
	}
	assert.NoError(t, gobErr, "resume must not fail on the saved checkpoint")
}
