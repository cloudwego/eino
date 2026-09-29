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

package toolpolicy

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// --- endpoint-level unit tests ---

type recordingTool struct {
	called    bool
	gotArgs   string
	result    string
	callError error
}

func (t *recordingTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "rec", Desc: "recording tool"}, nil
}

func (t *recordingTool) InvokableRun(_ context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	t.called = true
	t.gotArgs = argumentsInJSON
	if t.callError != nil {
		return "", t.callError
	}
	return t.result, nil
}

func allowPolicy(ctx context.Context, req *Request) (*Result, error) {
	return &Result{Decision: DecisionAllow}, nil
}

func newInvokableMiddleware(t *testing.T, policy Policy) (adk.ChatModelAgentMiddleware, *recordingTool) {
	t.Helper()
	mw, err := New(context.Background(), &Config{Policy: policy})
	require.NoError(t, err)
	rt := &recordingTool{result: "tool-output"}
	return mw, rt
}

func TestNilPolicyRejected(t *testing.T) {
	_, err := New(context.Background(), &Config{})
	assert.Error(t, err)
	_, err = New(context.Background(), nil)
	assert.Error(t, err)
}

func TestAllowExecutesTool(t *testing.T) {
	mw, rt := newInvokableMiddleware(t, PolicyFunc(allowPolicy))

	ep, err := mw.WrapInvokableToolCall(context.Background(), rt.InvokableRun, &adk.ToolContext{Name: "rec", CallID: "c1"})
	require.NoError(t, err)

	out, err := ep(context.Background(), `{"a":1}`)
	require.NoError(t, err)
	assert.True(t, rt.called)
	assert.Equal(t, `{"a":1}`, rt.gotArgs)
	assert.Equal(t, "tool-output", out)
}

func TestAllowWithRewrittenArguments(t *testing.T) {
	mw, rt := newInvokableMiddleware(t, PolicyFunc(func(ctx context.Context, req *Request) (*Result, error) {
		return &Result{Decision: DecisionAllow, RewrittenArguments: `{"sanitized":true}`}, nil
	}))

	ep, err := mw.WrapInvokableToolCall(context.Background(), rt.InvokableRun, &adk.ToolContext{Name: "rec", CallID: "c1"})
	require.NoError(t, err)

	_, err = ep(context.Background(), `{"a":1}`)
	require.NoError(t, err)
	assert.Equal(t, `{"sanitized":true}`, rt.gotArgs)
}

func TestDenyShortCircuits(t *testing.T) {
	mw, rt := newInvokableMiddleware(t, PolicyFunc(func(ctx context.Context, req *Request) (*Result, error) {
		assert.Equal(t, "rec", req.ToolName)
		assert.Equal(t, "c1", req.CallID)
		assert.Equal(t, `{"a":1}`, req.Arguments)
		return &Result{Decision: DecisionDeny}, nil
	}))

	ep, err := mw.WrapInvokableToolCall(context.Background(), rt.InvokableRun, &adk.ToolContext{Name: "rec", CallID: "c1"})
	require.NoError(t, err)

	out, err := ep(context.Background(), `{"a":1}`)
	require.NoError(t, err)
	assert.False(t, rt.called, "denied tool must not execute")
	assert.Contains(t, out, "denied")
	assert.Contains(t, out, "rec")
}

func TestDenyWithCustomMessage(t *testing.T) {
	mw, rt := newInvokableMiddleware(t, PolicyFunc(func(ctx context.Context, req *Request) (*Result, error) {
		return &Result{Decision: DecisionDeny, Message: "no destructive ops on Fridays"}, nil
	}))

	ep, err := mw.WrapInvokableToolCall(context.Background(), rt.InvokableRun, &adk.ToolContext{Name: "rec", CallID: "c1"})
	require.NoError(t, err)

	out, err := ep(context.Background(), `{}`)
	require.NoError(t, err)
	assert.False(t, rt.called)
	assert.Equal(t, "no destructive ops on Fridays", out)
}

func TestRequireApprovalInterrupts(t *testing.T) {
	mw, rt := newInvokableMiddleware(t, PolicyFunc(func(ctx context.Context, req *Request) (*Result, error) {
		return &Result{Decision: DecisionRequireApproval, ApprovalInfo: "needs admin sign-off"}, nil
	}))

	ep, err := mw.WrapInvokableToolCall(context.Background(), rt.InvokableRun, &adk.ToolContext{Name: "rec", CallID: "c1"})
	require.NoError(t, err)

	_, err = ep(context.Background(), `{}`)
	require.Error(t, err)
	assert.False(t, rt.called)

	info, ok := compose.IsInterruptRerunError(err)
	assert.True(t, ok, "expected an interrupt error, got: %v", err)
	assert.Equal(t, "needs admin sign-off", info)
}

func TestPolicyErrorPropagates(t *testing.T) {
	mw, rt := newInvokableMiddleware(t, PolicyFunc(func(ctx context.Context, req *Request) (*Result, error) {
		return nil, errors.New("policy backend down")
	}))

	ep, err := mw.WrapInvokableToolCall(context.Background(), rt.InvokableRun, &adk.ToolContext{Name: "rec", CallID: "c1"})
	require.NoError(t, err)

	_, err = ep(context.Background(), `{}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "policy backend down")
	assert.False(t, rt.called)
}

func TestNilPolicyResultRejected(t *testing.T) {
	mw, rt := newInvokableMiddleware(t, PolicyFunc(func(ctx context.Context, req *Request) (*Result, error) {
		return nil, nil
	}))

	ep, err := mw.WrapInvokableToolCall(context.Background(), rt.InvokableRun, &adk.ToolContext{Name: "rec", CallID: "c1"})
	require.NoError(t, err)

	_, err = ep(context.Background(), `{}`)
	require.Error(t, err)
	assert.False(t, rt.called)
}

func TestUnknownDecisionRejected(t *testing.T) {
	mw, rt := newInvokableMiddleware(t, PolicyFunc(func(ctx context.Context, req *Request) (*Result, error) {
		return &Result{Decision: "maybe"}, nil
	}))

	ep, err := mw.WrapInvokableToolCall(context.Background(), rt.InvokableRun, &adk.ToolContext{Name: "rec", CallID: "c1"})
	require.NoError(t, err)

	_, err = ep(context.Background(), `{}`)
	require.ErrorContains(t, err, "unknown decision")
	assert.False(t, rt.called)
}

type recordingStreamTool struct {
	called bool
}

func (t *recordingStreamTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "recs", Desc: "recording stream tool"}, nil
}

func (t *recordingStreamTool) StreamableRun(_ context.Context, _ string, _ ...tool.Option) (*schema.StreamReader[string], error) {
	t.called = true
	return schema.StreamReaderFromArray([]string{"chunk1", "chunk2"}), nil
}

func TestStreamableDenyReturnsMessageStream(t *testing.T) {
	mw, err := New(context.Background(), &Config{Policy: PolicyFunc(func(ctx context.Context, req *Request) (*Result, error) {
		return &Result{Decision: DecisionDeny, Message: "stream denied"}, nil
	})})
	require.NoError(t, err)

	st := &recordingStreamTool{}
	ep, err := mw.WrapStreamableToolCall(context.Background(), st.StreamableRun, &adk.ToolContext{Name: "recs", CallID: "c1"})
	require.NoError(t, err)

	sr, err := ep(context.Background(), `{}`)
	require.NoError(t, err)
	defer sr.Close()

	var sb strings.Builder
	for {
		chunk, err := sr.Recv()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		sb.WriteString(chunk)
	}
	assert.False(t, st.called)
	assert.Equal(t, "stream denied", sb.String())
}

func TestStreamableAllowExecutes(t *testing.T) {
	mw, err := New(context.Background(), &Config{Policy: PolicyFunc(allowPolicy)})
	require.NoError(t, err)

	st := &recordingStreamTool{}
	ep, err := mw.WrapStreamableToolCall(context.Background(), st.StreamableRun, &adk.ToolContext{Name: "recs", CallID: "c1"})
	require.NoError(t, err)

	sr, err := ep(context.Background(), `{}`)
	require.NoError(t, err)
	defer sr.Close()

	var chunks []string
	for {
		chunk, err := sr.Recv()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		chunks = append(chunks, chunk)
	}
	assert.True(t, st.called)
	assert.Equal(t, []string{"chunk1", "chunk2"}, chunks)
}

func TestEnhancedInvokableDenyReturnsTextResult(t *testing.T) {
	mw, err := New(context.Background(), &Config{Policy: PolicyFunc(func(ctx context.Context, req *Request) (*Result, error) {
		return &Result{Decision: DecisionDeny, Message: "enhanced denied"}, nil
	})})
	require.NoError(t, err)

	called := false
	var endpoint adk.EnhancedInvokableToolCallEndpoint = func(ctx context.Context, arg *schema.ToolArgument, opts ...tool.Option) (*schema.ToolResult, error) {
		called = true
		return &schema.ToolResult{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: "real"}}}, nil
	}

	ep, err := mw.WrapEnhancedInvokableToolCall(context.Background(), endpoint, &adk.ToolContext{Name: "en", CallID: "c1"})
	require.NoError(t, err)

	res, err := ep(context.Background(), &schema.ToolArgument{Text: `{"x":1}`})
	require.NoError(t, err)
	assert.False(t, called)
	require.Len(t, res.Parts, 1)
	assert.Equal(t, "enhanced denied", res.Parts[0].Text)
}

func TestEnhancedStreamableDenyReturnsTextResultStream(t *testing.T) {
	mw, err := New(context.Background(), &Config{Policy: PolicyFunc(func(ctx context.Context, req *Request) (*Result, error) {
		return &Result{Decision: DecisionDeny, Message: "enhanced stream denied"}, nil
	})})
	require.NoError(t, err)

	called := false
	var endpoint adk.EnhancedStreamableToolCallEndpoint = func(ctx context.Context, arg *schema.ToolArgument, opts ...tool.Option) (*schema.StreamReader[*schema.ToolResult], error) {
		called = true
		return schema.StreamReaderFromArray([]*schema.ToolResult{}), nil
	}

	ep, err := mw.WrapEnhancedStreamableToolCall(context.Background(), endpoint, &adk.ToolContext{Name: "ens", CallID: "c1"})
	require.NoError(t, err)

	sr, err := ep(context.Background(), &schema.ToolArgument{Text: `{}`})
	require.NoError(t, err)
	defer sr.Close()

	res, err := sr.Recv()
	require.NoError(t, err)
	assert.False(t, called)
	require.Len(t, res.Parts, 1)
	assert.Equal(t, "enhanced stream denied", res.Parts[0].Text)
}

// --- end-to-end approval flow through ChatModelAgent + Runner ---

type scriptedModel struct {
	mu       sync.Mutex
	messages []*schema.Message
	calls    int
}

func (m *scriptedModel) next() *schema.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	msg := m.messages[m.calls]
	m.calls++
	return msg
}

func (m *scriptedModel) Generate(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	return m.next(), nil
}

func (m *scriptedModel) Stream(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg := m.next()
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func (m *scriptedModel) WithTools(_ []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

type memoryStore struct {
	mu sync.Mutex
	m  map[string][]byte
}

func newMemoryStore() *memoryStore { return &memoryStore{m: map[string][]byte{}} }

func (s *memoryStore) Set(_ context.Context, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = value
	return nil
}

func (s *memoryStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[key]
	return v, ok, nil
}

type e2eTool struct {
	name   string
	called int
	lastIn string
	output string
}

func (t *e2eTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: t.name,
		Desc: "e2e tool " + t.name,
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"query": {Type: schema.String, Desc: "query"},
		}),
	}, nil
}

func (t *e2eTool) InvokableRun(_ context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	t.called++
	t.lastIn = argumentsInJSON
	return t.output, nil
}

func toolCallMessage(id, name, args string) *schema.Message {
	return schema.AssistantMessage("", []schema.ToolCall{
		{ID: id, Function: schema.FunctionCall{Name: name, Arguments: args}},
	})
}

// collectEvents drains an event iterator, returning all events.
func collectEvents(iter *adk.AsyncIterator[*adk.AgentEvent]) []*adk.AgentEvent {
	var events []*adk.AgentEvent
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		events = append(events, event)
	}
	return events
}

func findInterruptEvent(events []*adk.AgentEvent) *adk.AgentEvent {
	for _, e := range events {
		if e.Action != nil && e.Action.Interrupted != nil {
			return e
		}
	}
	return nil
}

func rootCauseInterruptID(t *testing.T, event *adk.AgentEvent) string {
	t.Helper()
	for _, ic := range event.Action.Interrupted.InterruptContexts {
		if ic.IsRootCause {
			return ic.ID
		}
	}
	t.Fatal("no root-cause interrupt context found")
	return ""
}

func setupApprovalAgent(t *testing.T, policy Policy, e2eTools ...*e2eTool) (adk.Agent, *scriptedModel) {
	t.Helper()
	ctx := context.Background()

	var tools []tool.BaseTool
	for _, et := range e2eTools {
		tools = append(tools, et)
	}

	var script []*schema.Message
	if len(e2eTools) == 1 {
		script = []*schema.Message{
			toolCallMessage("call-1", e2eTools[0].name, `{"query":"q1"}`),
			schema.AssistantMessage("final answer", nil),
		}
	} else {
		var calls []schema.ToolCall
		for i, et := range e2eTools {
			calls = append(calls, schema.ToolCall{
				ID:       "call-" + string(rune('1'+i)),
				Function: schema.FunctionCall{Name: et.name, Arguments: `{"query":"q"}`},
			})
		}
		script = []*schema.Message{
			schema.AssistantMessage("", calls),
			schema.AssistantMessage("final answer", nil),
		}
	}

	mdl := &scriptedModel{messages: script}

	mw, err := New(ctx, &Config{Policy: policy})
	require.NoError(t, err)

	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "tester",
		Description: "test agent",
		Model:       mdl,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: tools},
		},
		Handlers: []adk.ChatModelAgentMiddleware{mw},
	})
	require.NoError(t, err)
	return agent, mdl
}

func TestApprovalGrantedEndToEnd(t *testing.T) {
	ctx := context.Background()
	risky := &e2eTool{name: "risky_op", output: "op done"}

	policy := PolicyFunc(func(ctx context.Context, req *Request) (*Result, error) {
		return &Result{
			Decision:     DecisionRequireApproval,
			ApprovalInfo: "approve risky_op?",
		}, nil
	})

	agent, _ := setupApprovalAgent(t, policy, risky)
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent, CheckPointStore: newMemoryStore()})

	events := collectEvents(runner.Query(ctx, "do it", adk.WithCheckPointID("cp-1")))
	interrupt := findInterruptEvent(events)
	require.NotNil(t, interrupt, "expected an interrupt event")
	assert.Equal(t, 0, risky.called, "tool must not run before approval")

	interruptID := rootCauseInterruptID(t, interrupt)

	iter, err := runner.ResumeWithParams(ctx, "cp-1", &adk.ResumeParams{
		Targets: map[string]any{interruptID: &Approval{Approved: true}},
	})
	require.NoError(t, err)
	events = collectEvents(iter)

	assert.Equal(t, 1, risky.called)
	assert.Equal(t, `{"query":"q1"}`, risky.lastIn)
	last := events[len(events)-1]
	require.NotNil(t, last.Output)
	require.NotNil(t, last.Output.MessageOutput)
	assert.Equal(t, "final answer", last.Output.MessageOutput.Message.Content)
}

func TestApprovalRejectedEndToEnd(t *testing.T) {
	ctx := context.Background()
	risky := &e2eTool{name: "risky_op", output: "op done"}

	policy := PolicyFunc(func(ctx context.Context, req *Request) (*Result, error) {
		return &Result{Decision: DecisionRequireApproval, ApprovalInfo: "approve?"}, nil
	})

	agent, mdl := setupApprovalAgent(t, policy, risky)
	// The model must see the denial as the tool result, then answer.
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent, CheckPointStore: newMemoryStore()})

	events := collectEvents(runner.Query(ctx, "do it", adk.WithCheckPointID("cp-2")))
	interrupt := findInterruptEvent(events)
	require.NotNil(t, interrupt)
	interruptID := rootCauseInterruptID(t, interrupt)

	iter, err := runner.ResumeWithParams(ctx, "cp-2", &adk.ResumeParams{
		Targets: map[string]any{interruptID: &Approval{Approved: false, Message: "user said no"}},
	})
	require.NoError(t, err)
	events = collectEvents(iter)

	assert.Equal(t, 0, risky.called, "rejected tool must not run")
	_ = mdl
	last := events[len(events)-1]
	assert.Equal(t, "final answer", last.Output.MessageOutput.Message.Content)
}

func TestApprovalWithRewrittenArgumentsEndToEnd(t *testing.T) {
	ctx := context.Background()
	risky := &e2eTool{name: "risky_op", output: "op done"}

	policy := PolicyFunc(func(ctx context.Context, req *Request) (*Result, error) {
		return &Result{Decision: DecisionRequireApproval, ApprovalInfo: "approve?"}, nil
	})

	agent, _ := setupApprovalAgent(t, policy, risky)
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent, CheckPointStore: newMemoryStore()})

	events := collectEvents(runner.Query(ctx, "do it", adk.WithCheckPointID("cp-3")))
	interrupt := findInterruptEvent(events)
	require.NotNil(t, interrupt)
	interruptID := rootCauseInterruptID(t, interrupt)

	iter, err := runner.ResumeWithParams(ctx, "cp-3", &adk.ResumeParams{
		Targets: map[string]any{interruptID: &Approval{Approved: true, RewrittenArguments: `{"query":"rewritten"}`}},
	})
	require.NoError(t, err)
	collectEvents(iter)

	assert.Equal(t, 1, risky.called)
	assert.Equal(t, `{"query":"rewritten"}`, risky.lastIn)
}

func TestParallelApprovalsReInterruptEndToEnd(t *testing.T) {
	ctx := context.Background()
	toolA := &e2eTool{name: "op_a", output: "a done"}
	toolB := &e2eTool{name: "op_b", output: "b done"}

	policy := PolicyFunc(func(ctx context.Context, req *Request) (*Result, error) {
		return &Result{
			Decision:     DecisionRequireApproval,
			ApprovalInfo: "approve " + req.ToolName + "?",
		}, nil
	})

	agent, _ := setupApprovalAgent(t, policy, toolA, toolB)
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent, CheckPointStore: newMemoryStore()})

	// First run: both tool calls require approval and interrupt.
	events := collectEvents(runner.Query(ctx, "do both", adk.WithCheckPointID("cp-4")))
	interrupt := findInterruptEvent(events)
	require.NotNil(t, interrupt)
	assert.Equal(t, 0, toolA.called)
	assert.Equal(t, 0, toolB.called)

	var ids []string
	for _, ic := range interrupt.Action.Interrupted.InterruptContexts {
		if ic.IsRootCause {
			ids = append(ids, ic.ID)
		}
	}
	require.NotEmpty(t, ids)

	// Approve only the first pending call; the other must re-interrupt.
	iter, err := runner.ResumeWithParams(ctx, "cp-4", &adk.ResumeParams{
		Targets: map[string]any{ids[0]: &Approval{Approved: true}},
	})
	require.NoError(t, err)
	events = collectEvents(iter)

	secondInterrupt := findInterruptEvent(events)
	require.NotNil(t, secondInterrupt, "the unapproved sibling must re-interrupt")
	assert.Equal(t, 1, toolA.called+toolB.called, "exactly one tool should have run")

	secondID := rootCauseInterruptID(t, secondInterrupt)

	// Approve the remaining call; the agent completes.
	iter, err = runner.ResumeWithParams(ctx, "cp-4", &adk.ResumeParams{
		Targets: map[string]any{secondID: &Approval{Approved: true}},
	})
	require.NoError(t, err)
	events = collectEvents(iter)

	assert.Nil(t, findInterruptEvent(events))
	assert.Equal(t, 1, toolA.called)
	assert.Equal(t, 1, toolB.called)
	last := events[len(events)-1]
	assert.Equal(t, "final answer", last.Output.MessageOutput.Message.Content)
}

func TestTypedMiddlewareWorksForAgenticMessage(t *testing.T) {
	mw, err := NewTyped[*schema.AgenticMessage](context.Background(), &Config{Policy: PolicyFunc(func(ctx context.Context, req *Request) (*Result, error) {
		return &Result{Decision: DecisionDeny, Message: "agentic denied"}, nil
	})})
	require.NoError(t, err)

	rt := &recordingTool{result: "x"}
	ep, err := mw.WrapInvokableToolCall(context.Background(), rt.InvokableRun, &adk.ToolContext{Name: "rec", CallID: "c1"})
	require.NoError(t, err)

	out, err := ep(context.Background(), `{}`)
	require.NoError(t, err)
	assert.False(t, rt.called)
	assert.Equal(t, "agentic denied", out)
}
