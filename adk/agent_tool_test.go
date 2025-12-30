/*
 * Copyright 2025 CloudWeGo Authors
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

package adk

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// mockAgent implements the Agent interface for testing
type mockAgentForTool struct {
	name        string
	description string
	responses   []*AgentEvent
}

func (a *mockAgentForTool) Name(_ context.Context) string {
	return a.name
}

func (a *mockAgentForTool) Description(_ context.Context) string {
	return a.description
}

func (a *mockAgentForTool) Run(_ context.Context, _ *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	iterator, generator := NewAsyncIteratorPair[*AgentEvent]()

	go func() {
		defer generator.Close()

		for _, event := range a.responses {
			generator.Send(event)

			// If the event has an Exit action, stop sending events
			if event.Action != nil && event.Action.Exit {
				break
			}
		}
	}()

	return iterator
}

func newMockAgentForTool(name, description string, responses []*AgentEvent) *mockAgentForTool {
	return &mockAgentForTool{
		name:        name,
		description: description,
		responses:   responses,
	}
}

func TestAgentTool_Info(t *testing.T) {
	// Create a mock agent
	mockAgent_ := newMockAgentForTool("TestAgent", "Test agent description", nil)

	// Create an agentTool with the mock agent
	agentTool_ := NewAgentTool(context.Background(), mockAgent_)

	// Test the Info method
	ctx := context.Background()
	info, err := agentTool_.Info(ctx)

	// Verify results
	assert.NoError(t, err)
	assert.NotNil(t, info)
	assert.Equal(t, "TestAgent", info.Name)
	assert.Equal(t, "Test agent description", info.Desc)
	assert.NotNil(t, info.ParamsOneOf)
}

func TestAgentTool_SharedParentSessionValues(t *testing.T) {
	ctx := context.Background()

	inner := &sessionValuesAgent{name: "inner"}
	innerTool := NewAgentTool(ctx, inner).(tool.InvokableTool)

	input := &AgentInput{Messages: []Message{schema.UserMessage("q")}}
	ctx, _ = initRunCtx(ctx, "outer", input)
	AddSessionValue(ctx, "parent_key", "parent_val")
	parentSession := getRunCtx(ctx).Session

	_, err := innerTool.InvokableRun(ctx, `{"request":"hello"}`)
	assert.NoError(t, err)

	assert.Equal(t, "parent_val", inner.seenParentValue)
	assert.NotNil(t, inner.capturedSession)
	assert.NotSame(t, parentSession, inner.capturedSession)
	assert.NotNil(t, parentSession.valuesMtx)
	assert.Same(t, parentSession.valuesMtx, inner.capturedSession.valuesMtx)

	mtx := parentSession.valuesMtx
	mtx.Lock()
	inner.capturedSession.Values["direct_child_key"] = "direct_child_val"
	mtx.Unlock()

	mtx.Lock()
	v2, ok2 := parentSession.Values["direct_child_key"]
	mtx.Unlock()
	assert.True(t, ok2)
	assert.Equal(t, "direct_child_val", v2)

	mtx.Lock()
	parentSession.Values["direct_parent_key"] = "direct_parent_val"
	mtx.Unlock()

	mtx.Lock()
	v3, ok3 := inner.capturedSession.Values["direct_parent_key"]
	mtx.Unlock()
	assert.True(t, ok3)
	assert.Equal(t, "direct_parent_val", v3)

	v, ok := GetSessionValue(ctx, "child_key")
	assert.True(t, ok)
	assert.Equal(t, "child_val", v)
}

type sessionValuesAgent struct {
	name            string
	seenParentValue any
	capturedSession *runSession
}

func (a *sessionValuesAgent) Name(context.Context) string        { return a.name }
func (a *sessionValuesAgent) Description(context.Context) string { return "test" }
func (a *sessionValuesAgent) Run(ctx context.Context, _ *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	if rc := getRunCtx(ctx); rc != nil {
		a.capturedSession = rc.Session
	}
	a.seenParentValue, _ = GetSessionValue(ctx, "parent_key")
	AddSessionValue(ctx, "child_key", "child_val")

	it, gen := NewAsyncIteratorPair[*AgentEvent]()
	gen.Send(&AgentEvent{
		AgentName: a.name,
		Output: &AgentOutput{
			MessageOutput: &MessageVariant{
				IsStreaming: false,
				Message:     schema.AssistantMessage("ok", nil),
				Role:        schema.Assistant,
			},
		},
	})
	gen.Close()
	return it
}

func TestAgentTool_InvokableRun(t *testing.T) {
	// Create a context
	ctx := context.Background()

	// Test cases
	tests := []struct {
		name           string
		agentResponses []*AgentEvent
		request        string
		expectedOutput string
		expectError    bool
	}{
		{
			name: "successful model response",
			agentResponses: []*AgentEvent{
				{
					AgentName: "TestAgent",
					Output: &AgentOutput{
						MessageOutput: &MessageVariant{
							IsStreaming: false,
							Message:     schema.AssistantMessage("Test response", nil),
							Role:        schema.Assistant,
						},
					},
				},
			},
			request:        `{"request":"Test request"}`,
			expectedOutput: "Test response",
			expectError:    false,
		},
		{
			name: "successful tool call response",
			agentResponses: []*AgentEvent{
				{
					AgentName: "TestAgent",
					Output: &AgentOutput{
						MessageOutput: &MessageVariant{
							IsStreaming: false,
							Message:     schema.ToolMessage("Tool response", "test-id"),
							Role:        schema.Tool,
						},
					},
				},
			},
			request:        `{"request":"Test tool request"}`,
			expectedOutput: "Tool response",
			expectError:    false,
		},
		{
			name:           "invalid request JSON",
			agentResponses: nil,
			request:        `invalid json`,
			expectedOutput: "",
			expectError:    true,
		},
		{
			name:           "no events returned",
			agentResponses: []*AgentEvent{},
			request:        `{"request":"Test request"}`,
			expectedOutput: "",
			expectError:    true,
		},
		{
			name: "error in event",
			agentResponses: []*AgentEvent{
				{
					AgentName: "TestAgent",
					Err:       assert.AnError,
				},
			},
			request:        `{"request":"Test request"}`,
			expectedOutput: "",
			expectError:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a mock agent with the test responses
			mockAgent_ := newMockAgentForTool("TestAgent", "Test agent description", tt.agentResponses)

			// Create an agentTool with the mock agent
			agentTool_ := NewAgentTool(ctx, mockAgent_)

			// Call InvokableRun
			output, err := agentTool_.(tool.InvokableTool).InvokableRun(ctx, tt.request)

			// Verify results
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedOutput, output)
			}
		})
	}
}

func TestGetReactHistory(t *testing.T) {
	g := compose.NewGraph[string, []Message](compose.WithGenLocalState(func(ctx context.Context) (state *State) {
		return &State{
			Messages: []Message{
				schema.UserMessage("user query"),
				schema.AssistantMessage("", []schema.ToolCall{{ID: "tool call id 1", Function: schema.FunctionCall{Name: "tool1", Arguments: "arguments1"}}}),
				schema.ToolMessage("tool result 1", "tool call id 1", schema.WithToolName("tool1")),
				schema.AssistantMessage("", []schema.ToolCall{{ID: "tool call id 2", Function: schema.FunctionCall{Name: "tool2", Arguments: "arguments2"}}}),
			},
			AgentName: "MyAgent",
		}
	}))
	assert.NoError(t, g.AddLambdaNode("1", compose.InvokableLambda(func(ctx context.Context, input string) (output []Message, err error) {
		return getReactChatHistory(ctx, "DestAgentName")
	})))
	assert.NoError(t, g.AddEdge(compose.START, "1"))
	assert.NoError(t, g.AddEdge("1", compose.END))

	ctx := context.Background()
	runner, err := g.Compile(ctx)
	assert.NoError(t, err)
	result, err := runner.Invoke(ctx, "")
	assert.NoError(t, err)
	assert.Equal(t, []Message{
		schema.UserMessage("user query"),
		schema.UserMessage("For context: [MyAgent] called tool: `tool1` with arguments: arguments1."),
		schema.UserMessage("For context: [MyAgent] `tool1` tool returned result: tool result 1."),
		schema.UserMessage("For context: [MyAgent] called tool: `transfer_to_agent` with arguments: DestAgentName."),
		schema.UserMessage("For context: [MyAgent] `transfer_to_agent` tool returned result: successfully transferred to agent [DestAgentName]."),
	}, result)
}

// mockAgentWithInputCapture implements the Agent interface for testing and captures the input it receives
type mockAgentWithInputCapture struct {
	name          string
	description   string
	capturedInput []Message
	responses     []*AgentEvent
}

func (a *mockAgentWithInputCapture) Name(_ context.Context) string {
	return a.name
}

func (a *mockAgentWithInputCapture) Description(_ context.Context) string {
	return a.description
}

func (a *mockAgentWithInputCapture) Run(_ context.Context, input *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	a.capturedInput = input.Messages

	iterator, generator := NewAsyncIteratorPair[*AgentEvent]()

	go func() {
		defer generator.Close()

		for _, event := range a.responses {
			generator.Send(event)

			// If the event has an Exit action, stop sending events
			if event.Action != nil && event.Action.Exit {
				break
			}
		}
	}()

	return iterator
}

func newMockAgentWithInputCapture(name, description string, responses []*AgentEvent) *mockAgentWithInputCapture {
	return &mockAgentWithInputCapture{
		name:        name,
		description: description,
		responses:   responses,
	}
}

func TestAgentToolWithOptions(t *testing.T) {
	// Test Case 1: WithFullChatHistoryAsInput
	t.Run("WithFullChatHistoryAsInput", func(t *testing.T) {
		ctx := context.Background()

		// 1. Set up a mock agent that will capture the input it receives
		mockAgent := newMockAgentWithInputCapture("test-agent", "a test agent", []*AgentEvent{
			{
				AgentName: "test-agent",
				Output: &AgentOutput{
					MessageOutput: &MessageVariant{
						IsStreaming: false,
						Message:     schema.AssistantMessage("done", nil),
						Role:        schema.Assistant,
					},
				},
			},
		})

		// 2. Create an agentTool with the option
		agentTool := NewAgentTool(ctx, mockAgent, WithFullChatHistoryAsInput())

		// 3. Set up a context with a chat history using a graph
		history := []Message{
			schema.UserMessage("first user message"),
			schema.AssistantMessage("first assistant response", nil),
		}

		g := compose.NewGraph[string, string](compose.WithGenLocalState(func(ctx context.Context) (state *State) {
			return &State{
				AgentName: "react-agent",
				Messages:  append(history, schema.AssistantMessage("tool call msg", nil)),
			}
		}))

		assert.NoError(t, g.AddLambdaNode("1", compose.InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
			// Run the tool within the graph context that has the state
			_, err = agentTool.(tool.InvokableTool).InvokableRun(ctx, `{"request":"some ignored input"}`)
			return "done", err
		})))
		assert.NoError(t, g.AddEdge(compose.START, "1"))
		assert.NoError(t, g.AddEdge("1", compose.END))

		runner, err := g.Compile(ctx)
		assert.NoError(t, err)

		// 4. Run the graph which will execute the tool with the state
		_, err = runner.Invoke(ctx, "")
		assert.NoError(t, err)

		// 5. Assert that the agent received the full history
		// The agent should receive: history (minus last assistant message) + transfer messages
		assert.Len(t, mockAgent.capturedInput, 4) // 2 from history + 2 transfer messages
		assert.Equal(t, "first user message", mockAgent.capturedInput[0].Content)
		assert.Equal(t, "For context: [react-agent] said: first assistant response.", mockAgent.capturedInput[1].Content)
		assert.Equal(t, "For context: [react-agent] called tool: `transfer_to_agent` with arguments: test-agent.", mockAgent.capturedInput[2].Content)
		assert.Equal(t, "For context: [react-agent] `transfer_to_agent` tool returned result: successfully transferred to agent [test-agent].", mockAgent.capturedInput[3].Content)
	})

	// Test Case 2: WithAgentInputSchema
	t.Run("WithAgentInputSchema", func(t *testing.T) {
		ctx := context.Background()

		// 1. Define a custom schema
		customSchema := schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"custom_arg": {
				Desc:     "a custom argument",
				Required: true,
				Type:     schema.String,
			},
		})

		// 2. Set up a mock agent to capture input
		mockAgent := newMockAgentWithInputCapture("schema-agent", "agent with custom schema", []*AgentEvent{
			{
				AgentName: "schema-agent",
				Output: &AgentOutput{
					MessageOutput: &MessageVariant{
						IsStreaming: false,
						Message:     schema.AssistantMessage("schema processed", nil),
						Role:        schema.Assistant,
					},
				},
			},
		})

		// 3. Create agentTool with the custom schema option
		agentTool := NewAgentTool(ctx, mockAgent, WithAgentInputSchema(customSchema))

		// 4. Verify the Info() method returns the custom schema
		info, err := agentTool.Info(ctx)
		assert.NoError(t, err)
		assert.Equal(t, customSchema, info.ParamsOneOf)

		// 5. Run the tool with arguments matching the custom schema
		_, err = agentTool.(tool.InvokableTool).InvokableRun(ctx, `{"custom_arg":"hello world"}`)
		assert.NoError(t, err)

		// 6. Assert that the agent received the correctly parsed argument
		// With custom schema, the agent should receive the raw JSON as input
		assert.Len(t, mockAgent.capturedInput, 1)
		assert.Equal(t, `{"custom_arg":"hello world"}`, mockAgent.capturedInput[0].Content)
	})

	// Test Case 3: WithAgentInputSchema with complex schema
	t.Run("WithAgentInputSchema_ComplexSchema", func(t *testing.T) {
		ctx := context.Background()

		// 1. Define a complex custom schema with multiple parameters
		complexSchema := schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"name": {
				Desc:     "user name",
				Required: true,
				Type:     schema.String,
			},
			"age": {
				Desc:     "user age",
				Required: false,
				Type:     schema.Integer,
			},
			"active": {
				Desc:     "user status",
				Required: false,
				Type:     schema.Boolean,
			},
		})

		// 2. Set up a mock agent
		mockAgent := newMockAgentWithInputCapture("complex-agent", "agent with complex schema", []*AgentEvent{
			{
				AgentName: "complex-agent",
				Output: &AgentOutput{
					MessageOutput: &MessageVariant{
						IsStreaming: false,
						Message:     schema.AssistantMessage("complex processed", nil),
						Role:        schema.Assistant,
					},
				},
			},
		})

		// 3. Create agentTool with the complex schema option
		agentTool := NewAgentTool(ctx, mockAgent, WithAgentInputSchema(complexSchema))

		// 4. Verify the Info() method returns the complex schema
		info, err := agentTool.Info(ctx)
		assert.NoError(t, err)
		assert.Equal(t, complexSchema, info.ParamsOneOf)

		// 5. Run the tool with complex arguments
		_, err = agentTool.(tool.InvokableTool).InvokableRun(ctx, `{"name":"John","age":30,"active":true}`)
		assert.NoError(t, err)

		// 6. Assert that the agent received the complex JSON
		assert.Len(t, mockAgent.capturedInput, 1)
		assert.Equal(t, `{"name":"John","age":30,"active":true}`, mockAgent.capturedInput[0].Content)
	})

	// Test Case 4: Both options together
	t.Run("BothOptionsTogether", func(t *testing.T) {
		ctx := context.Background()

		// 1. Define a custom schema
		customSchema := schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"query": {
				Desc:     "search query",
				Required: true,
				Type:     schema.String,
			},
		})

		// 2. Set up a mock agent
		mockAgent := newMockAgentWithInputCapture("combined-agent", "agent with both options", []*AgentEvent{
			{
				AgentName: "combined-agent",
				Output: &AgentOutput{
					MessageOutput: &MessageVariant{
						IsStreaming: false,
						Message:     schema.AssistantMessage("combined processed", nil),
						Role:        schema.Assistant,
					},
				},
			},
		})

		// 3. Create agentTool with both options
		agentTool := NewAgentTool(ctx, mockAgent, WithAgentInputSchema(customSchema), WithFullChatHistoryAsInput())

		// 4. Set up a context with chat history using a graph
		history := []Message{
			schema.UserMessage("previous conversation"),
			schema.AssistantMessage("previous response", nil),
		}

		g := compose.NewGraph[string, string](compose.WithGenLocalState(func(ctx context.Context) (state *State) {
			return &State{
				AgentName: "react-agent",
				Messages:  append(history, schema.AssistantMessage("tool call", nil)),
			}
		}))

		assert.NoError(t, g.AddLambdaNode("1", compose.InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
			// Run the tool within the graph context that has the state
			_, err = agentTool.(tool.InvokableTool).InvokableRun(ctx, `{"query":"current query"}`)
			return "done", err
		})))
		assert.NoError(t, g.AddEdge(compose.START, "1"))
		assert.NoError(t, g.AddEdge("1", compose.END))

		runner, err := g.Compile(ctx)
		assert.NoError(t, err)

		// 5. Run the graph which will execute the tool with the state
		_, err = runner.Invoke(ctx, "")
		assert.NoError(t, err)

		// 6. Verify both options work together
		info, err := agentTool.Info(ctx)
		assert.NoError(t, err)
		assert.Equal(t, customSchema, info.ParamsOneOf)

		// The agent should receive full history + the custom query
		assert.Len(t, mockAgent.capturedInput, 4) // 2 history + 2 transfer messages
		assert.Equal(t, "previous conversation", mockAgent.capturedInput[0].Content)
		assert.Equal(t, "For context: [react-agent] said: previous response.", mockAgent.capturedInput[1].Content)
		assert.Equal(t, "For context: [react-agent] called tool: `transfer_to_agent` with arguments: combined-agent.", mockAgent.capturedInput[2].Content)
		assert.Equal(t, "For context: [react-agent] `transfer_to_agent` tool returned result: successfully transferred to agent [combined-agent].", mockAgent.capturedInput[3].Content)
	})
}

type fakeTCM struct{ toolNameToCall string }

func (f *fakeTCM) Generate(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	tc := schema.ToolCall{ID: "id-1", Type: "function"}
	tc.Function.Name = f.toolNameToCall
	tc.Function.Arguments = `{"request":"hello"}`
	return schema.AssistantMessage("", []schema.ToolCall{tc}), nil
}
func (f *fakeTCM) Stream(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, _ := f.Generate(ctx, input)
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}
func (f *fakeTCM) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	if len(tools) > 0 {
		f.toolNameToCall = tools[0].Name
	}
	return f, nil
}

type emitOnceModel struct{}

func (e *emitOnceModel) Generate(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage("inner2", nil), nil
}
func (e *emitOnceModel) Stream(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m, _ := e.Generate(ctx, input)
	return schema.StreamReaderFromArray([]*schema.Message{m}), nil
}
func (e *emitOnceModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return e, nil
}

type emitEventsAgent struct{ events []*AgentEvent }

func (e *emitEventsAgent) Name(context.Context) string        { return "emit" }
func (e *emitEventsAgent) Description(context.Context) string { return "test" }
func (e *emitEventsAgent) Run(context.Context, *AgentInput, ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	it, gen := NewAsyncIteratorPair[*AgentEvent]()
	go func() {
		for _, ev := range e.events {
			gen.Send(ev)
		}
		gen.Close()
	}()
	return it
}

// spyAgent captures runSession from ctx in a single nested run
type spyAgent struct {
	a        Agent
	mu       sync.Mutex
	captured *runSession
}

func (s *spyAgent) Name(ctx context.Context) string        { return s.a.Name(ctx) }
func (s *spyAgent) Description(ctx context.Context) string { return s.a.Description(ctx) }
func (s *spyAgent) Run(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	if rc := getRunCtx(ctx); rc != nil {
		s.mu.Lock()
		s.captured = rc.Session
		s.mu.Unlock()
	}
	return s.a.Run(ctx, input, options...)
}

func (s *spyAgent) getCaptured() *runSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.captured
}

func TestNestedAgentTool_RunPath(t *testing.T) {
	ctx := context.Background()

	inner2, _ := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "inner2",
		Description: "leaf",
		Model:       &emitOnceModel{},
		ToolsConfig: ToolsConfig{EmitInternalEvents: true},
	})
	inner2Spy := &spyAgent{a: inner2}
	inner2Tool := NewAgentTool(ctx, inner2Spy)

	inner, _ := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "inner",
		Description: "mid",
		Model:       &fakeTCM{},
		ToolsConfig: ToolsConfig{EmitInternalEvents: true, ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{inner2Tool}}},
	})
	innerSpy := &spyAgent{a: inner}
	innerTool := NewAgentTool(ctx, innerSpy)

	outer, _ := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "outer",
		Description: "top",
		Model:       &fakeTCM{},
		ToolsConfig: ToolsConfig{EmitInternalEvents: true, ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{innerTool}}},
	})

	input := &AgentInput{Messages: []Message{schema.UserMessage("q")}}
	ctx, outerRunCtx := initRunCtx(ctx, "outer", input)
	r := NewRunner(ctx, RunnerConfig{Agent: outer, EnableStreaming: false, CheckPointStore: newBridgeStore()})
	it := r.Run(ctx, []Message{schema.UserMessage("q")})

	var target *AgentEvent
	for {
		ev, ok := it.Next()
		if !ok {
			break
		}
		if ev.Output != nil && ev.Output.MessageOutput != nil && !ev.Output.MessageOutput.IsStreaming {
			if ev.Output.MessageOutput.Message != nil && ev.Output.MessageOutput.Message.Content == "inner2" {
				target = ev
				break
			}
		}
	}
	if target == nil {
		t.Fatalf("no inner2 event found in ephemerals")
	}

	got := make([]string, len(target.RunPath))
	for i := range target.RunPath {
		got[i] = target.RunPath[i].agentName
	}
	want := []string{"outer", "inner", "inner2"}
	if len(got) != len(want) {
		t.Fatalf("unexpected runPath len: got %d want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("runPath mismatch at %d: got %s want %s; full: %+v", i, got[i], want[i], got)
		}
	}

	for _, w := range outerRunCtx.Session.getEvents() {
		if w.AgentName != "outer" {
			t.Fatalf("outer session contains non-outer event: %s", w.AgentName)
		}
	}
	if innerSpy.getCaptured() == nil {
		t.Fatalf("inner spy did not capture session")
	}
	for _, w := range innerSpy.getCaptured().getEvents() {
		if w.AgentName != "inner" {
			t.Fatalf("inner session contains non-inner event: %s", w.AgentName)
		}
	}
	if inner2Spy.getCaptured() == nil {
		t.Fatalf("inner2 spy did not capture session")
	}
	for _, w := range inner2Spy.getCaptured().getEvents() {
		if w.AgentName != "inner2" {
			t.Fatalf("inner2 session contains non-inner2 event: %s", w.AgentName)
		}
	}
}

func TestNestedAgentTool_NoInternalEventsWhenDisabled(t *testing.T) {
	ctx := context.Background()

	inner2, _ := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "inner2",
		Description: "leaf",
		Model:       &emitOnceModel{},
		ToolsConfig: ToolsConfig{EmitInternalEvents: false},
	})
	inner2Tool := NewAgentTool(ctx, inner2)

	inner, _ := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "inner",
		Description: "mid",
		Model:       &fakeTCM{},
		ToolsConfig: ToolsConfig{EmitInternalEvents: false, ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{inner2Tool}}},
	})
	innerTool := NewAgentTool(ctx, inner)

	outer, _ := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "outer",
		Description: "top",
		Model:       &fakeTCM{},
		ToolsConfig: ToolsConfig{EmitInternalEvents: false, ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{innerTool}}},
	})

	r := NewRunner(ctx, RunnerConfig{Agent: outer, EnableStreaming: false, CheckPointStore: newBridgeStore()})
	it := r.Run(ctx, []Message{schema.UserMessage("q")})

	for {
		ev, ok := it.Next()
		if !ok {
			break
		}
		if ev.AgentName == "inner2" {
			t.Fatalf("inner2 internal event should not be emitted when disabled")
		}
	}
}

func TestNestedAgentTool_InnerToolResultNotEmittedToOuter(t *testing.T) {
	ctx := context.Background()

	innerTool := &simpleTool{name: "inner_tool", result: "inner_tool_result"}
	inner, _ := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "inner",
		Description: "inner agent with tool",
		Model:       &fakeTCM{},
		ToolsConfig: ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{innerTool}}},
	})
	innerAgentTool := NewAgentTool(ctx, inner)

	outer, _ := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "outer",
		Description: "outer agent",
		Model:       &fakeTCM{},
		ToolsConfig: ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{innerAgentTool}}},
	})

	r := NewRunner(ctx, RunnerConfig{Agent: outer, EnableStreaming: false, CheckPointStore: newBridgeStore()})
	it := r.Run(ctx, []Message{schema.UserMessage("q")})

	var allEvents []*AgentEvent
	for {
		ev, ok := it.Next()
		if !ok {
			break
		}
		allEvents = append(allEvents, ev)
	}

	for _, ev := range allEvents {
		if ev.Output != nil && ev.Output.MessageOutput != nil &&
			ev.Output.MessageOutput.Message != nil &&
			ev.Output.MessageOutput.Message.Role == schema.Tool &&
			ev.AgentName == "outer" &&
			ev.Output.MessageOutput.Message.Content == "inner_tool_result" {
			t.Fatalf("inner agent's tool result (inner_tool_result) should not be emitted as outer agent's event, but got event with AgentName=%s, Content=%s",
				ev.AgentName, ev.Output.MessageOutput.Message.Content)
		}
	}
}

type simpleTool struct {
	name   string
	result string
}

func (s *simpleTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: s.name, Desc: "simple tool"}, nil
}

func (s *simpleTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	return s.result, nil
}

func TestAgentTool_InterruptWithoutCheckpoint(t *testing.T) {
	ctx := context.Background()
	ctx, _ = initRunCtx(ctx, "TestAgent", &AgentInput{Messages: []Message{}})

	interrupted := &AgentEvent{AgentName: "TestAgent"}
	interrupted.Action = StatefulInterrupt(ctx, "info", "state").Action

	err := compositeInterruptFromLast(ctx, &bridgeStore{}, interrupted)
	if err == nil {
		t.Fatalf("expected error for interrupt without checkpoint")
	}
	if !strings.Contains(err.Error(), "interrupt occurred but checkpoint data is missing") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func compositeInterruptFromLast(ctx context.Context, ms *bridgeStore, lastEvent *AgentEvent) error {
	if lastEvent == nil || lastEvent.Action == nil || lastEvent.Action.Interrupted == nil {
		return nil
	}
	data, existed, err := ms.Get(ctx, bridgeCheckpointID)
	if err != nil {
		return fmt.Errorf("failed to get interrupt info: %w", err)
	}
	if !existed {
		return fmt.Errorf("interrupt occurred but checkpoint data is missing")
	}
	return compose.CompositeInterrupt(ctx, "agent tool interrupt", data, lastEvent.Action.internalInterrupted)
}

func TestAgentTool_InvokableRun_FinalOnly(t *testing.T) {
	ctx := context.Background()

	inner2, _ := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "inner2",
		Description: "leaf",
		Model:       &emitOnceModel{},
		ToolsConfig: ToolsConfig{EmitInternalEvents: true},
	})
	invTool := NewAgentTool(ctx, inner2)
	out, err := invTool.(tool.InvokableTool).InvokableRun(ctx, `{"request":"q"}`)
	if err != nil {
		t.Fatalf("invokable run error: %v", err)
	}
	if out != "inner2" {
		t.Fatalf("unexpected output: %s", out)
	}
}

type streamingAgent struct{}

func (s *streamingAgent) Name(context.Context) string        { return "stream" }
func (s *streamingAgent) Description(context.Context) string { return "test" }
func (s *streamingAgent) Run(context.Context, *AgentInput, ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	it, gen := NewAsyncIteratorPair[*AgentEvent]()
	go func() {
		mv := &MessageVariant{IsStreaming: true, MessageStream: schema.StreamReaderFromArray([]Message{schema.AssistantMessage("a", nil), schema.AssistantMessage("b", nil)})}
		gen.Send(&AgentEvent{AgentName: "stream", Output: &AgentOutput{MessageOutput: mv}})
		gen.Close()
	}()
	return it
}

func TestAgentTool_InvokableRun_StreamingVariant(t *testing.T) {
	ctx := context.Background()
	agent := &streamingAgent{}
	it := NewAgentTool(ctx, agent)
	out, err := it.(tool.InvokableTool).InvokableRun(ctx, `{"request":"q"}`)
	if err != nil {
		t.Fatalf("invokable run error: %v", err)
	}
	if out != "ab" {
		t.Fatalf("unexpected output: %s", out)
	}
}

func TestSequentialWorkflow_WithChatModelAgentTool_NestedRunPathAndSessions(t *testing.T) {
	ctx := context.Background()

	inner2, _ := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "inner2",
		Description: "leaf",
		Model:       &emitOnceModel{},
		ToolsConfig: ToolsConfig{EmitInternalEvents: true},
	})
	inner2Spy := &spyAgent{a: inner2}
	inner2ToolSpy := NewAgentTool(ctx, inner2Spy)

	innerWithSpy, _ := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "inner",
		Description: "mid",
		Model:       &fakeTCM{},
		ToolsConfig: ToolsConfig{EmitInternalEvents: true, ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{inner2ToolSpy}}},
	})
	innerSpy := &spyAgent{a: innerWithSpy}

	outer, err := NewSequentialAgent(ctx, &SequentialAgentConfig{
		Name:        "outer-seq",
		Description: "workflow",
		SubAgents:   []Agent{innerSpy},
	})
	if err != nil {
		t.Fatalf("new sequential agent err: %v", err)
	}

	input := &AgentInput{Messages: []Message{schema.UserMessage("q")}}
	ctx, outerRunCtx := initRunCtx(ctx, "outer-seq", input)
	r := NewRunner(ctx, RunnerConfig{Agent: outer, EnableStreaming: false, CheckPointStore: newBridgeStore()})
	it := r.Run(ctx, []Message{schema.UserMessage("q")})

	var target *AgentEvent
	for {
		ev, ok := it.Next()
		if !ok {
			break
		}
		if ev.Output != nil && ev.Output.MessageOutput != nil && !ev.Output.MessageOutput.IsStreaming {
			if ev.Output.MessageOutput.Message != nil && ev.Output.MessageOutput.Message.Content == "inner2" {
				target = ev
				break
			}
		}
	}
	if target == nil {
		t.Fatalf("no inner2 event found")
	}

	got := make([]string, len(target.RunPath))
	for i := range target.RunPath {
		got[i] = target.RunPath[i].agentName
	}
	want := []string{"outer-seq", "inner", "inner2"}
	if len(got) != len(want) {
		t.Fatalf("unexpected runPath len: got %d want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("runPath mismatch at %d: got %s want %s; full: %+v", i, got[i], want[i], got)
		}
	}

	for _, w := range outerRunCtx.Session.getEvents() {
		if w.AgentName != "outer-seq" {
			t.Fatalf("outer session contains non-outer event: %s", w.AgentName)
		}
	}
	if innerSpy.getCaptured() == nil {
		t.Fatalf("inner spy did not capture session")
	}
	for _, w := range innerSpy.getCaptured().getEvents() {
		if w.AgentName != "inner" {
			t.Fatalf("inner session contains non-inner event: %s", w.AgentName)
		}
	}
	if inner2Spy.getCaptured() == nil {
		t.Fatalf("inner2 spy did not capture session")
	}
	for _, w := range inner2Spy.getCaptured().getEvents() {
		if w.AgentName != "inner2" {
			t.Fatalf("inner2 session contains non-inner2 event: %s", w.AgentName)
		}
	}
}

type badAgent struct{ parent string }

func (b *badAgent) Name(context.Context) string        { return "bad" }
func (b *badAgent) Description(context.Context) string { return "misuse" }
func (b *badAgent) Run(ctx context.Context, input *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	it, gen := NewAsyncIteratorPair[*AgentEvent]()
	go func() {
		ev := EventFromMessage(schema.AssistantMessage("x", nil), nil, schema.Assistant, "")
		ev.RunPath = []RunStep{{agentName: b.parent}, {agentName: "bad"}}
		gen.Send(ev)
		gen.Close()
	}()
	return it
}

func TestRunPathMisuse_DuplicatedHeadAndNoParentRecording(t *testing.T) {
	ctx := context.Background()
	input := &AgentInput{Messages: []Message{schema.UserMessage("q")}}
	ctx, outerRunCtx := initRunCtx(ctx, "outer", input)
	fa := toFlowAgent(ctx, &badAgent{parent: "outer"})

	it := fa.Run(ctx, input)
	var last *AgentEvent
	for {
		ev, ok := it.Next()
		if !ok {
			break
		}
		last = ev
	}
	if last == nil {
		t.Fatalf("no event emitted")
	}

	got := make([]string, len(last.RunPath))
	for i := range last.RunPath {
		got[i] = last.RunPath[i].agentName
	}
	want := []string{"outer", "bad", "outer", "bad"}
	if len(got) != len(want) {
		t.Fatalf("unexpected runPath len: got %d want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("runPath mismatch at %d: got %s want %s; full: %+v", i, got[i], want[i], got)
		}
	}

	evs := outerRunCtx.Session.getEvents()
	if len(evs) != 0 {
		t.Fatalf("outer session should not record misused event, recorded=%d", len(evs))
	}
}

func TestRunPathGating_IgnoresInnerExitAndAllowsOutput(t *testing.T) {
	ctx := context.Background()

	innerExit := &AgentEvent{Action: &AgentAction{Exit: true}, RunPath: []RunStep{{agentName: "inner"}}}
	finalOut := EventFromMessage(schema.AssistantMessage("ok", nil), nil, schema.Assistant, "")

	sub := &emitEventsAgent{events: []*AgentEvent{innerExit, finalOut}}
	fa := toFlowAgent(ctx, sub)

	it := fa.Run(ctx, &AgentInput{Messages: []Message{schema.UserMessage("q")}})

	var sawFinal bool
	for {
		ev, ok := it.Next()
		if !ok {
			break
		}
		if ev.Output != nil && ev.Output.MessageOutput != nil && !ev.Output.MessageOutput.IsStreaming {
			if ev.Output.MessageOutput.Message != nil && ev.Output.MessageOutput.Message.Content == "ok" {
				sawFinal = true
			}
		}
	}
	if !sawFinal {
		t.Fatalf("final output not observed; parent may have exited on inner Exit action")
	}
}

func TestRunPathGating_IgnoresInnerTransfer(t *testing.T) {
	ctx := context.Background()

	innerTransfer := &AgentEvent{Action: NewTransferToAgentAction("ghost"), RunPath: []RunStep{{agentName: "inner"}}}
	finalOut := EventFromMessage(schema.AssistantMessage("done", nil), nil, schema.Assistant, "")

	sub := &emitEventsAgent{events: []*AgentEvent{innerTransfer, finalOut}}
	fa := toFlowAgent(ctx, sub)

	it := fa.Run(ctx, &AgentInput{Messages: []Message{schema.UserMessage("q")}})

	var outputs int
	for {
		ev, ok := it.Next()
		if !ok {
			break
		}
		if ev.Output != nil && ev.Output.MessageOutput != nil && !ev.Output.MessageOutput.IsStreaming {
			if ev.Output.MessageOutput.Message != nil {
				outputs++
			}
		}
	}
	if outputs == 0 {
		t.Fatalf("no outputs observed; parent may have transferred on inner transfer action")
	}
}

type streamAgent struct{}

func (s *streamAgent) Name(context.Context) string        { return "s" }
func (s *streamAgent) Description(context.Context) string { return "s" }
func (s *streamAgent) Run(context.Context, *AgentInput, ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	it, gen := NewAsyncIteratorPair[*AgentEvent]()
	go func() {
		frames := []*schema.Message{
			schema.AssistantMessage("hello ", nil),
			schema.AssistantMessage("world", nil),
		}
		stream := schema.StreamReaderFromArray(frames)
		gen.Send(EventFromMessage(nil, stream, schema.Assistant, ""))
		gen.Close()
	}()
	return it
}

func TestInvokableAgentTool_InfoAndRun(t *testing.T) {
	ctx := context.Background()

	at := NewAgentTool(ctx, &streamAgent{})
	info, err := at.Info(ctx)
	assert.NoError(t, err)
	assert.Equal(t, "s", info.Name)
	assert.Equal(t, "s", info.Desc)
	js, err := info.ParamsOneOf.ToJSONSchema()
	assert.NoError(t, err)
	found := false
	for _, r := range js.Required {
		if r == "request" {
			found = true
			break
		}
	}
	assert.True(t, found)
	prop, ok := js.Properties.Get("request")
	assert.True(t, ok)
	assert.Equal(t, string(schema.String), prop.Type)

	custom := schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
		"x": {Desc: "arg", Required: true, Type: schema.String},
	})
	at2 := NewAgentTool(ctx, &streamAgent{}, WithAgentInputSchema(custom))
	info2, err := at2.Info(ctx)
	assert.NoError(t, err)
	assert.Equal(t, custom, info2.ParamsOneOf)
	out, err := at.(tool.InvokableTool).InvokableRun(ctx, `{"request":"x"}`)
	assert.NoError(t, err)
	assert.Equal(t, "hello world", out)
}

type emptyAgent struct{}

func (e *emptyAgent) Name(context.Context) string        { return "empty" }
func (e *emptyAgent) Description(context.Context) string { return "empty" }
func (e *emptyAgent) Run(context.Context, *AgentInput, ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	it, gen := NewAsyncIteratorPair[*AgentEvent]()
	go func() { gen.Close() }()
	return it
}

type noOutputAgent struct{}

func (n *noOutputAgent) Name(context.Context) string        { return "no" }
func (n *noOutputAgent) Description(context.Context) string { return "no" }
func (n *noOutputAgent) Run(context.Context, *AgentInput, ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	it, gen := NewAsyncIteratorPair[*AgentEvent]()
	go func() { gen.Send(&AgentEvent{}); gen.Close() }()
	return it
}

func TestInvokableAgentTool_ErrorCases(t *testing.T) {
	ctx := context.Background()

	atEmpty := NewAgentTool(ctx, &emptyAgent{})
	out, err := atEmpty.(tool.InvokableTool).InvokableRun(ctx, `{"request":"x"}`)
	assert.Equal(t, "", out)
	assert.Error(t, err)

	atNo := NewAgentTool(ctx, &noOutputAgent{})
	out2, err := atNo.(tool.InvokableTool).InvokableRun(ctx, `{"request":"x"}`)
	assert.NoError(t, err)
	assert.Equal(t, "", out2)
}
