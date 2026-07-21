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

package adk

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// --- helpers shared across edge-case tests ---

// blockingChatModel blocks until unblockCh is closed, then returns a fixed response.
type blockingChatModel struct {
	unblockCh chan struct{}
	response  *schema.Message
	started   chan struct{}
	callCount int32
}

func newBlockingChatModel(response *schema.Message) *blockingChatModel {
	return &blockingChatModel{
		unblockCh: make(chan struct{}),
		response:  response,
		started:   make(chan struct{}, 1),
	}
}

func (m *blockingChatModel) Generate(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	atomic.AddInt32(&m.callCount, 1)
	select {
	case m.started <- struct{}{}:
	default:
	}
	<-m.unblockCh
	return m.response, nil
}

func (m *blockingChatModel) Stream(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	atomic.AddInt32(&m.callCount, 1)
	select {
	case m.started <- struct{}{}:
	default:
	}
	<-m.unblockCh
	return schema.StreamReaderFromArray([]*schema.Message{m.response}), nil
}

func (m *blockingChatModel) BindTools(_ []*schema.ToolInfo) error { return nil }

type contextAwareBlockingChatModel struct {
	release        chan struct{}
	response       *schema.Message
	started        chan struct{}
	cancelObserved chan struct{}
	callCount      int32
}

func newContextAwareBlockingChatModel(response *schema.Message) *contextAwareBlockingChatModel {
	return &contextAwareBlockingChatModel{
		release:        make(chan struct{}),
		response:       response,
		started:        make(chan struct{}, 4),
		cancelObserved: make(chan struct{}, 4),
	}
}

func (m *contextAwareBlockingChatModel) Generate(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	atomic.AddInt32(&m.callCount, 1)
	select {
	case m.started <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		select {
		case m.cancelObserved <- struct{}{}:
		default:
		}
		return nil, ctx.Err()
	case <-m.release:
		return m.response, nil
	}
}

func (m *contextAwareBlockingChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func (m *contextAwareBlockingChatModel) BindTools(_ []*schema.ToolInfo) error { return nil }

// errorChatModel returns an error from Generate/Stream.
type errorChatModel struct {
	err     error
	started chan struct{}
}

func (m *errorChatModel) Generate(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	if m.started != nil {
		select {
		case m.started <- struct{}{}:
		default:
		}
	}
	return nil, m.err
}

func (m *errorChatModel) Stream(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, m.err
}

func (m *errorChatModel) BindTools(_ []*schema.ToolInfo) error { return nil }

// plainResponseModel returns immediately with a fixed text response (no tool calls).
type plainResponseModel struct {
	text string
}

func (m *plainResponseModel) Generate(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage(m.text, nil), nil
}

func (m *plainResponseModel) Stream(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage(m.text, nil)}), nil
}

func (m *plainResponseModel) BindTools(_ []*schema.ToolInfo) error { return nil }

// blockingTool blocks until unblockCh is closed.
type blockingTool struct {
	name      string
	unblockCh chan struct{}
	started   chan struct{}
	callCount int32
}

func newBlockingTool(name string) *blockingTool {
	return &blockingTool{
		name:      name,
		unblockCh: make(chan struct{}),
		started:   make(chan struct{}, 4),
	}
}

func (t *blockingTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: t.name,
		Desc: "blocking tool",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"input": {Type: "string"},
		}),
	}, nil
}

func (t *blockingTool) InvokableRun(_ context.Context, _ string, _ ...tool.Option) (string, error) {
	atomic.AddInt32(&t.callCount, 1)
	select {
	case t.started <- struct{}{}:
	default:
	}
	<-t.unblockCh
	return "result", nil
}

func toolCallMsg(calls ...schema.ToolCall) *schema.Message {
	return &schema.Message{Role: schema.Assistant, ToolCalls: calls}
}

func toolCall(id, name, args string) schema.ToolCall {
	return schema.ToolCall{ID: id, Type: "function", Function: schema.FunctionCall{Name: name, Arguments: args}}
}

func drainEvents(iter *AsyncIterator[*AgentEvent]) ([]*AgentEvent, bool) {
	var events []*AgentEvent
	hasCancelError := false
	for {
		e, ok := iter.Next()
		if !ok {
			break
		}
		events = append(events, e)
		var ce *CancelError
		if e.Err != nil && errors.As(e.Err, &ce) {
			hasCancelError = true
		}
	}
	return events, hasCancelError
}

func drainCancelErrors(iter *AsyncIterator[*AgentEvent]) ([]*AgentEvent, []*CancelError) {
	var events []*AgentEvent
	var cancelErrors []*CancelError
	for {
		e, ok := iter.Next()
		if !ok {
			break
		}
		events = append(events, e)
		var ce *CancelError
		if e.Err != nil && errors.As(e.Err, &ce) {
			cancelErrors = append(cancelErrors, ce)
		}
	}
	return events, cancelErrors
}

func runChildAgentToEnd(ctx context.Context, child Agent, msg string) error {
	iter := child.Run(ctx, &AgentInput{Messages: []Message{schema.UserMessage(msg)}})
	for {
		event, ok := iter.Next()
		if !ok {
			return nil
		}
		if event.Err != nil {
			return event.Err
		}
	}
}

func newTransferSupervisorWithSubAgent(t *testing.T, ctx context.Context, mdl model.BaseChatModel, subAgent Agent) Agent {
	t.Helper()

	supervisorAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "supervisor",
		Description: "supervisor agent",
		Instruction: "you are a supervisor",
		Model:       mdl,
	})
	require.NoError(t, err)

	agentWithSubAgents, err := SetSubAgents(ctx, supervisorAgent, []Agent{subAgent})
	require.NoError(t, err)
	return agentWithSubAgents
}

func assertCancelErrorExcludesAddresses(t *testing.T, cancelErr *CancelError, names ...string) {
	t.Helper()

	for _, intCtx := range cancelErr.InterruptContexts {
		addr := fmt.Sprint(intCtx.Address)
		for _, name := range names {
			assert.NotContains(t, addr, name)
		}
	}
}

func assertCancelErrorIncludesAddress(t *testing.T, cancelErr *CancelError, name string) {
	t.Helper()

	for _, intCtx := range cancelErr.InterruptContexts {
		if strings.Contains(fmt.Sprint(intCtx.Address), name) {
			return
		}
	}
	t.Fatalf("cancel interrupt contexts do not include %q", name)
}

// --- tests ---

// TestWithCancel_BeforeExecutionStarts verifies that a cancel issued before
// the graph begins executing still produces a CancelError without invoking
// the model or tools.
func TestWithCancel_BeforeExecutionStarts(t *testing.T) {
	ctx := context.Background()

	blk := newBlockingChatModel(toolCallMsg(toolCall("c1", "bt", `{"input":"x"}`)))
	bt := newBlockingTool("bt")

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestAgent",
		Description: "test",
		Model:       blk,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{bt}},
		},
	})
	assert.NoError(t, err)

	cancelOpt, cancelFn := WithCancel()

	// Extract the cancelContext so we can wait for cancelChan to close,
	// ensuring the cancel is fully registered before Run starts.
	cc := getCommonOptions(nil, cancelOpt).cancelCtx

	// Call cancel BEFORE calling agent.Run.
	// The cancelFunc must succeed (not hang) even though execution hasn't started.
	cancelDone := make(chan error, 1)
	go func() {
		handle, _ := cancelFn()
		cancelDone <- handle.Wait()
	}()

	// Wait for cancelChan to close so the pre-execution check in runFunc
	// deterministically sees shouldCancel()=true (eliminates goroutine scheduling race).
	<-cc.cancelChan

	// Now start the run — it should see shouldCancel()=true and emit CancelError immediately.
	iter := agent.Run(ctx, &AgentInput{Messages: []Message{schema.UserMessage("hi")}}, cancelOpt)

	_, hasCancelError := drainEvents(iter)
	assert.True(t, hasCancelError, "expected CancelError when cancel precedes execution")

	// cancelFn must have already returned (or return quickly now that doneChan is closed).
	select {
	case cancelErr := <-cancelDone:
		// Either nil (cancel handled) or ErrExecutionEnded is acceptable
		// depending on exact timing; what matters is it didn't hang.
		_ = cancelErr
	case <-time.After(3 * time.Second):
		t.Fatal("cancelFn blocked indefinitely after pre-start cancel")
	}

	// Model and tool must not have been invoked.
	assert.Equal(t, int32(0), atomic.LoadInt32(&bt.callCount), "tool must not be called")
}

// TestWithCancel_AfterCompletion verifies cancelFn returns ErrExecutionEnded
// when called after a normal run finishes.
func TestWithCancel_AfterCompletion(t *testing.T) {
	ctx := context.Background()

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestAgent",
		Description: "test",
		Model:       &plainResponseModel{text: "done"},
	})
	require.NoError(t, err)

	cancelOpt, cancelFn := WithCancel()
	iter := agent.Run(ctx, &AgentInput{Messages: []Message{schema.UserMessage("hi")}}, cancelOpt)

	// Drain all events so the run completes.
	for {
		_, ok := iter.Next()
		if !ok {
			break
		}
	}

	handle, _ := cancelFn()
	cancelErr := handle.Wait()
	assert.ErrorIs(t, cancelErr, ErrExecutionEnded)
}

// TestWithCancel_DerivedCheckpointContextMarkedDoneAfterRun verifies that
// an explicitly derived checkpoint-aware cancel context is owned by the sub-agent run,
// even when the Go context also carries the parent cancel context.
func TestWithCancel_DerivedCheckpointCancelContextMarkedDoneAfterRun(t *testing.T) {
	ctx := context.Background()

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "ChildAgent",
		Description: "test child agent",
		Model:       &plainResponseModel{text: "done"},
	})
	require.NoError(t, err)

	parent := newCancelContext()
	parentCtx := withCancelContext(ctx, parent)
	child := parent.deriveCheckpointAwareCancelContext(parentCtx)

	childOpt := WrapImplSpecificOptFn(func(o *options) {
		o.cancelCtx = child
	})
	iter := agent.Run(parentCtx, &AgentInput{Messages: []Message{schema.UserMessage("hi")}}, childOpt)
	for {
		_, ok := iter.Next()
		if !ok {
			break
		}
	}

	select {
	case <-child.doneChan:
	case <-time.After(time.Second):
		t.Fatal("derived checkpoint cancel context was not marked done after child run completion")
	}
}

func TestWithCancel_RecursiveDoesNotTargetAgentEmbeddedInMiddleware(t *testing.T) {
	ctx := context.Background()

	innerModel := newBlockingChatModel(toolCallMsg(toolCall("inner-call", "inner-tool", `{"input":"x"}`)))
	innerToolCalled := make(chan struct{}, 1)
	innerTool := &callbackTool{
		name: "inner-tool",
		onCall: func() {
			innerToolCalled <- struct{}{}
		},
	}
	innerAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "InnerMiddlewareAgent",
		Description: "agent invoked as middleware implementation detail",
		Model:       innerModel,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{innerTool}},
			ReturnDirectly:  map[string]bool{"inner-tool": true},
		},
	})
	require.NoError(t, err)

	outerTool := newBlockingTool("outer-tool")
	outerAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "OuterAgent",
		Description: "outer agent",
		Model: &simpleChatModel{
			response: toolCallMsg(toolCall("outer-call", "outer-tool", `{"input":"x"}`)),
		},
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{outerTool}},
		},
		Handlers: []ChatModelAgentMiddleware{
			&testAfterModelRewriteStateHandler{fn: func(
				ctx context.Context,
				state *ChatModelAgentState,
				_ *ModelContext,
			) (context.Context, *ChatModelAgentState, error) {
				return ctx, state, runChildAgentToEnd(ctx, innerAgent, "run")
			}},
		},
	})
	require.NoError(t, err)

	cancelOpt, cancelFn := WithCancel()
	iter := outerAgent.Run(ctx, &AgentInput{Messages: []Message{schema.UserMessage("run")}}, cancelOpt)

	select {
	case <-innerModel.started:
	case <-time.After(5 * time.Second):
		t.Fatal("middleware's embedded agent did not start")
	}

	cancelDone := make(chan error, 1)
	go func() {
		handle, _ := cancelFn(WithAgentCancelMode(CancelAfterChatModel), WithRecursive())
		cancelDone <- handle.Wait()
	}()

	close(innerModel.unblockCh)

	select {
	case <-innerToolCalled:
	case <-time.After(5 * time.Second):
		t.Fatal("recursive cancel incorrectly targeted the middleware's embedded agent")
	}

	select {
	case err := <-cancelDone:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("outer cancel did not complete")
	}

	_, hasCancelError := drainEvents(iter)
	assert.True(t, hasCancelError, "outer agent should cancel at its own safe-point")
	assert.Equal(t, int32(0), atomic.LoadInt32(&outerTool.callCount), "outer tool must not run after cancellation")
}

// TestWithCancel_AfterBusinessInterrupt verifies cancelFn returns ErrExecutionEnded
// when called after the agent has been interrupted by business logic.
func TestWithCancel_AfterBusinessInterrupt(t *testing.T) {
	ctx := context.Background()

	// Use a model that triggers a compose.Interrupt so the agent stops with an interrupt.
	interruptModel := &interruptingChatModel{}

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestAgent",
		Description: "test",
		Model:       interruptModel,
	})
	require.NoError(t, err)

	store := newCancelTestStore()
	runner := NewRunner(ctx, RunnerConfig{
		Agent:           agent,
		CheckPointStore: store,
	})

	cancelOpt, cancelFn := WithCancel()
	iter := runner.Run(ctx, []Message{schema.UserMessage("hi")}, cancelOpt, WithCheckPointID("biz-interrupt-1"))

	// Drain — expect an interrupt action event, no cancel error.
	var gotInterrupt bool
	for {
		e, ok := iter.Next()
		if !ok {
			break
		}
		if e.Action != nil && e.Action.Interrupted != nil {
			gotInterrupt = true
		}
	}
	assert.True(t, gotInterrupt, "expected business interrupt event")

	handle, _ := cancelFn()
	cancelErr := handle.Wait()
	assert.ErrorIs(t, cancelErr, ErrExecutionEnded)
}

// TestWithCancel_AfterError verifies cancelFn returns ErrExecutionEnded
// when called after the agent errors out.
func TestWithCancel_AfterError(t *testing.T) {
	ctx := context.Background()

	modelErr := errors.New("model exploded")
	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestAgent",
		Description: "test",
		Model:       &errorChatModel{err: modelErr},
	})
	require.NoError(t, err)

	cancelOpt, cancelFn := WithCancel()
	iter := agent.Run(ctx, &AgentInput{Messages: []Message{schema.UserMessage("hi")}}, cancelOpt)

	for {
		_, ok := iter.Next()
		if !ok {
			break
		}
	}

	handle, _ := cancelFn()
	cancelErr := handle.Wait()
	assert.ErrorIs(t, cancelErr, ErrExecutionEnded)
}

// TestWithCancel_TimeoutEscalation tests that WithAgentCancelTimeout causes the
// cancel to escalate to immediate when the safe-point hasn't fired yet, and
// that the resulting CancelError has Escalated=true.
//
// Strategy: use CancelAfterChatModel mode. The model blocks (never completes),
// so the safe-point can't fire naturally. After the timeout, escalateToImmediate
// closes immediateChan which aborts the model stream via cancelMonitoredModel
// and causes a CancelError — no compose graph-interrupt races involved.
func TestWithCancel_TimeoutEscalation(t *testing.T) {
	ctx := context.Background()

	blk := newBlockingChatModel(schema.AssistantMessage("hello", nil))

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestAgent",
		Description: "test",
		Model:       blk,
	})
	require.NoError(t, err)

	runner := NewRunner(ctx, RunnerConfig{
		Agent:           agent,
		EnableStreaming: true, // use streaming so cancelMonitoredModel.Stream is exercised
	})

	timeout := 300 * time.Millisecond
	// CancelAfterChatModel + timeout: safe-point can't fire (model never finishes),
	// so after 300ms the timeout goroutine escalates to immediate.
	cancelOpt, cancelFn := WithCancel()
	iter := runner.Run(ctx, []Message{schema.UserMessage("go")}, cancelOpt)

	select {
	case <-blk.started:
	case <-time.After(5 * time.Second):
		t.Fatal("model did not start")
	}

	// Fire cancelFn; it will wait for escalation to complete.
	start := time.Now()
	handle, _ := cancelFn(WithAgentCancelMode(CancelAfterChatModel), WithAgentCancelTimeout(timeout))
	cancelErr := handle.Wait()
	elapsed := time.Since(start)

	assert.ErrorIs(t, cancelErr, ErrCancelTimeout, "cancel should return ErrCancelTimeout after timeout escalation")
	assert.True(t, elapsed >= timeout, "should wait at least the timeout duration, elapsed=%v", elapsed)
	assert.True(t, elapsed < 3*time.Second, "should complete shortly after timeout, elapsed=%v", elapsed)

	var cancelError *CancelError
	for {
		e, ok := iter.Next()
		if !ok {
			break
		}
		var ce *CancelError
		if e.Err != nil && errors.As(e.Err, &ce) {
			cancelError = ce
		}
	}
	if assert.NotNil(t, cancelError, "expected CancelError after timeout escalation") {
		assert.True(t, cancelError.Info.Escalated, "CancelError should report Escalated=true")
		assert.True(t, cancelError.Info.Timeout, "CancelError should report Timeout=true")
	}
}

// TestWithCancel_AfterChatModel_WithTools verifies CancelAfterChatModel fires
// when the model returns tool calls (the safe-point is on the tool-calls path).
func TestWithCancel_AfterChatModel_WithTools(t *testing.T) {
	ctx := context.Background()

	blk := newBlockingChatModel(toolCallMsg(toolCall("c1", "bt", `{"input":"x"}`)))
	bt := newBlockingTool("bt")

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestAgent",
		Description: "test",
		Model:       blk,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{bt}},
		},
	})
	require.NoError(t, err)

	cancelOpt, cancelFn := WithCancel()
	iter := agent.Run(ctx, &AgentInput{Messages: []Message{schema.UserMessage("hi")}}, cancelOpt)

	select {
	case <-blk.started:
	case <-time.After(5 * time.Second):
		t.Fatal("model did not start")
	}

	cancelDone := make(chan error, 1)
	go func() {
		handle, _ := cancelFn(WithAgentCancelMode(CancelAfterChatModel))
		cancelDone <- handle.Wait()
	}()

	time.Sleep(20 * time.Millisecond)

	close(blk.unblockCh)

	cancelErr := <-cancelDone
	assert.NoError(t, cancelErr)

	_, hasCancelError := drainEvents(iter)
	assert.True(t, hasCancelError, "CancelError expected after model returns tool calls")
}

// TestWithCancel_CancelImmediate_StreamAborted verifies that CancelImmediate
// during model execution surfaces CancelError and completes quickly.
// Uses blockingChatModel which blocks in Stream(), keeping the agent's run
// function alive so the cancel context stays in stateRunning.
func TestWithCancel_CancelImmediate_StreamAborted(t *testing.T) {
	ctx := context.Background()

	blk := newBlockingChatModel(schema.AssistantMessage("hello", nil))

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestAgent",
		Description: "test",
		Model:       blk,
	})
	require.NoError(t, err)

	runner := NewRunner(ctx, RunnerConfig{
		Agent:           agent,
		EnableStreaming: true,
	})

	cancelOpt, cancelFn := WithCancel()
	iter := runner.Run(ctx, []Message{schema.UserMessage("hi")}, cancelOpt)

	select {
	case <-blk.started:
	case <-time.After(5 * time.Second):
		t.Fatal("model did not start")
	}
	time.Sleep(50 * time.Millisecond)

	start := time.Now()
	handle, _ := cancelFn()
	cancelErr := handle.Wait()
	assert.NoError(t, cancelErr)
	elapsed := time.Since(start)
	assert.True(t, elapsed < 2*time.Second, "cancel should complete quickly, elapsed=%v", elapsed)

	var foundCancelError bool
	for {
		e, ok := iter.Next()
		if !ok {
			break
		}
		if e.Action != nil && e.Action.Interrupted != nil {
			foundCancelError = true
		}
		var ce *CancelError
		if e.Err != nil && errors.As(e.Err, &ce) {
			foundCancelError = true
		}
	}
	assert.True(t, foundCancelError, "expected CancelError in event stream")
}

// TestWithCancel_MultipleToolsConcurrent verifies that CancelAfterToolCalls
// waits for ALL concurrent tool calls to complete before cancelling.
func TestWithCancel_MultipleToolsConcurrent(t *testing.T) {
	ctx := context.Background()

	bt1 := newBlockingTool("tool1")
	bt2 := newBlockingTool("tool2")

	// Model calls both tools in one response.
	modelResp := toolCallMsg(
		toolCall("c1", "tool1", `{"input":"a"}`),
		toolCall("c2", "tool2", `{"input":"b"}`),
	)
	modelWithTools := &simpleChatModel{response: modelResp}

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestAgent",
		Description: "test",
		Model:       modelWithTools,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{bt1, bt2}},
		},
	})
	assert.NoError(t, err)

	cancelOpt, cancelFn := WithCancel()
	iter := agent.Run(ctx, &AgentInput{Messages: []Message{schema.UserMessage("go")}}, cancelOpt)

	// Wait for both tools to start.
	for i := 0; i < 2; i++ {
		select {
		case <-bt1.started:
		case <-bt2.started:
		case <-time.After(5 * time.Second):
			t.Fatal("tools did not start")
		}
	}

	// Request cancel after tool calls while both are still blocking.
	cancelDone := make(chan error, 1)
	go func() {
		handle, _ := cancelFn(WithAgentCancelMode(CancelAfterToolCalls))
		cancelDone <- handle.Wait()
	}()

	// Unblock both tools — cancel should fire only after both complete.
	time.Sleep(50 * time.Millisecond)
	close(bt1.unblockCh)
	time.Sleep(50 * time.Millisecond)
	close(bt2.unblockCh)

	cancelErr := <-cancelDone
	assert.NoError(t, cancelErr)

	assert.Equal(t, int32(1), atomic.LoadInt32(&bt1.callCount), "tool1 should complete")
	assert.Equal(t, int32(1), atomic.LoadInt32(&bt2.callCount), "tool2 should complete")

	_, hasCancelError := drainEvents(iter)
	assert.True(t, hasCancelError, "expected CancelError after concurrent tools completed")
}

// TestWithCancel_GraphInterruptRaceBeforeSet verifies that a CancelImmediate
// issued before setGraphInterruptFunc is called still results in cancellation.
// This exercises the retroactive-fire path in setGraphInterruptFunc.
func TestWithCancel_GraphInterruptRaceBeforeSet(t *testing.T) {
	ctx := context.Background()

	blk := newBlockingChatModel(schema.AssistantMessage("hi", nil))

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestAgent",
		Description: "test",
		Model:       blk,
	})
	require.NoError(t, err)

	cancelOpt, cancelFn := WithCancel()

	// Cancel immediately before run starts.
	go func() {
		handle, _ := cancelFn()
		_ = handle.Wait()
	}()

	iter := agent.Run(ctx, &AgentInput{Messages: []Message{schema.UserMessage("hi")}}, cancelOpt)

	done := make(chan struct{})
	go func() {
		defer close(done)
		drainEvents(iter)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("iteration did not complete after pre-start CancelImmediate")
	}
}

// TestWithCancel_NoCheckpointStore verifies cancel completes and does not panic
// when no checkpoint store is configured.
func TestWithCancel_NoCheckpointStore(t *testing.T) {
	ctx := context.Background()

	blk := newBlockingChatModel(schema.AssistantMessage("hi", nil))

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestAgent",
		Description: "test",
		Model:       blk,
	})
	require.NoError(t, err)

	runner := NewRunner(ctx, RunnerConfig{
		Agent: agent,
		// No CheckPointStore set.
	})

	cancelOpt, cancelFn := WithCancel()
	iter := runner.Run(ctx, []Message{schema.UserMessage("hi")}, cancelOpt)

	select {
	case <-blk.started:
	case <-time.After(5 * time.Second):
		t.Fatal("model did not start")
	}
	time.Sleep(30 * time.Millisecond)

	handle, _ := cancelFn()
	cancelErr := handle.Wait()
	assert.NoError(t, cancelErr)

	var ce *CancelError
	for {
		e, ok := iter.Next()
		if !ok {
			break
		}
		if e.Err != nil && errors.As(e.Err, &ce) {
			break
		}
	}
	assert.NotNil(t, ce, "expected CancelError even without checkpoint store")
}

// TestWithCancel_ModelError verifies that a model error marks the cancelCtx as
// done so that a subsequent cancelFn call returns ErrExecutionEnded.
func TestWithCancel_ModelError(t *testing.T) {
	ctx := context.Background()

	modelErr := errors.New("model failed")
	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestAgent",
		Description: "test",
		Model:       &errorChatModel{err: modelErr},
	})
	require.NoError(t, err)

	cancelOpt, cancelFn := WithCancel()
	iter := agent.Run(ctx, &AgentInput{Messages: []Message{schema.UserMessage("hi")}}, cancelOpt)

	var gotModelErr bool
	for {
		e, ok := iter.Next()
		if !ok {
			break
		}
		if e.Err != nil && !errors.As(e.Err, new(*CancelError)) {
			gotModelErr = true
		}
	}
	assert.True(t, gotModelErr, "expected non-cancel error event from model failure")

	handle, _ := cancelFn()
	cancelErr := handle.Wait()
	assert.ErrorIs(t, cancelErr, ErrExecutionEnded, "cancelFn should return ErrExecutionEnded after model error")
}

// TestWithCancel_Resume_SafePoint covers CancelAfterChatModel and
// CancelAfterToolCalls on a Resume path.
func TestWithCancel_Resume_SafePoint(t *testing.T) {
	ctx := context.Background()

	// --- phase 1: run to get a checkpoint via CancelImmediate ---
	blk := newBlockingChatModel(toolCallMsg(toolCall("c1", "bt", `{"input":"x"}`)))
	bt := newSlowTool("bt", 50*time.Millisecond, "result")

	agent1, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestAgent",
		Description: "test",
		Model:       blk,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{bt}},
		},
	})
	assert.NoError(t, err)

	store := newCancelTestStore()
	runner1 := NewRunner(ctx, RunnerConfig{
		Agent:           agent1,
		CheckPointStore: store,
	})

	cancelOpt1, cancelFn1 := WithCancel()
	iter1 := runner1.Run(ctx, []Message{schema.UserMessage("hi")}, cancelOpt1, WithCheckPointID("resume-sp-1"))

	select {
	case <-blk.started:
	case <-time.After(5 * time.Second):
		t.Fatal("model did not start in phase 1")
	}
	_, _ = cancelFn1()
	drainEvents(iter1)

	// --- phase 2: resume, cancel after chat model ---
	resumeModel := newBlockingChatModel(toolCallMsg(toolCall("c1", "bt", `{"input":"x"}`)))

	bt2 := newSlowTool("bt", 50*time.Millisecond, "result")
	agent2, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestAgent",
		Description: "test",
		Model:       resumeModel,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{bt2}},
		},
	})
	assert.NoError(t, err)

	runner2 := NewRunner(ctx, RunnerConfig{
		Agent:           agent2,
		CheckPointStore: store,
	})

	cancelOpt2, cancelFn2 := WithCancel()
	resumeIter, err := runner2.Resume(ctx, "resume-sp-1", cancelOpt2)
	require.NoError(t, err)

	select {
	case <-resumeModel.started:
	case <-time.After(5 * time.Second):
		t.Fatal("model did not start in phase 2")
	}

	cancelDone := make(chan error, 1)
	go func() {
		handle, _ := cancelFn2(WithAgentCancelMode(CancelAfterChatModel))
		cancelDone <- handle.Wait()
	}()

	time.Sleep(50 * time.Millisecond)

	close(resumeModel.unblockCh)

	cancelErr := <-cancelDone
	assert.NoError(t, cancelErr)

	_, hasCancelError := drainEvents(resumeIter)
	assert.True(t, hasCancelError, "CancelError expected after resumed model returns tool calls")
}

// callbackTool is a tool that calls onCall when invoked.
type callbackTool struct {
	name   string
	onCall func()
}

func (t *callbackTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: t.name,
		Desc: "callback tool",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"input": {Type: "string"},
		}),
	}, nil
}

func (t *callbackTool) InvokableRun(_ context.Context, _ string, _ ...tool.Option) (string, error) {
	if t.onCall != nil {
		t.onCall()
	}
	return "ok", nil
}

// interruptingChatModel returns a compose.Interrupt error to simulate a
// business interrupt during execution.
type interruptingChatModel struct{}

func (m *interruptingChatModel) Generate(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	return nil, compose.Interrupt(ctx, "test interrupt")
}

func (m *interruptingChatModel) Stream(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, compose.Interrupt(ctx, "test interrupt")
}

func (m *interruptingChatModel) BindTools(_ []*schema.ToolInfo) error { return nil }

// TestWithCancel_TargetedResume_CancelImmediate cancels an agent via CancelImmediate,
// extracts InterruptContexts from the resulting CancelError, and uses them
// for targeted resumption via Runner.ResumeWithParams.
func TestWithCancel_TargetedResume_CancelImmediate(t *testing.T) {
	ctx := context.Background()

	blk := newBlockingChatModel(toolCallMsg(toolCall("c1", "st", `{"input":"x"}`)))
	st := newSlowTool("st", 50*time.Millisecond, "result")

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestAgent",
		Description: "test",
		Model:       blk,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{st}},
		},
	})
	require.NoError(t, err)

	store := newCancelTestStore()
	runner := NewRunner(ctx, RunnerConfig{
		Agent:           agent,
		CheckPointStore: store,
	})

	cancelOpt, cancelFn := WithCancel()
	iter := runner.Run(ctx, []Message{schema.UserMessage("go")}, cancelOpt, WithCheckPointID("targeted-imm-1"))

	select {
	case <-blk.started:
	case <-time.After(5 * time.Second):
		t.Fatal("model did not start")
	}

	handle, _ := cancelFn() // CancelImmediate (default)
	cancelErr := handle.Wait()
	assert.NoError(t, cancelErr)

	var cancelError *CancelError
	for {
		e, ok := iter.Next()
		if !ok {
			break
		}
		var ce *CancelError
		if e.Err != nil && errors.As(e.Err, &ce) {
			cancelError = ce
		}
	}

	require.NotNil(t, cancelError, "expected CancelError")
	require.NotEmpty(t, cancelError.InterruptContexts, "CancelError should have InterruptContexts for targeted resume")

	// --- resume with targeted params ---
	targets := make(map[string]any)
	for _, ic := range cancelError.InterruptContexts {
		targets[ic.ID] = nil
	}

	resumeModel := &plainResponseModel{text: "resumed"}
	agent2, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestAgent",
		Description: "test",
		Model:       resumeModel,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{st}},
		},
	})
	require.NoError(t, err)

	runner2 := NewRunner(ctx, RunnerConfig{
		Agent:           agent2,
		CheckPointStore: store,
	})

	resumeIter, err := runner2.ResumeWithParams(ctx, "targeted-imm-1", &ResumeParams{Targets: targets})
	require.NoError(t, err)

	var gotOutput bool
	for {
		e, ok := resumeIter.Next()
		if !ok {
			break
		}
		if e.Err != nil {
			t.Fatalf("unexpected error during targeted resume: %v", e.Err)
		}
		if e.Output != nil && e.Output.MessageOutput != nil {
			gotOutput = true
		}
	}
	assert.True(t, gotOutput, "targeted resume should produce output")
}

// TestWithCancel_TargetedResume_SafePoint cancels an agent via CancelAfterChatModel
// (safe-point) and verifies that InterruptContexts are populated on the CancelError
// and that targeted resume via ResumeWithParams succeeds.
// Since safe-point cancels now use compose.Interrupt, compose saves checkpoint data,
// making the cancel fully resumable.
func TestWithCancel_TargetedResume_SafePoint(t *testing.T) {
	ctx := context.Background()

	// The model returns a tool call so the react graph routes to toolPreHandle,
	// which detects CancelAfterChatModel and fires compose.Interrupt.
	blk := newBlockingChatModel(toolCallMsg(toolCall("c1", "st", `{"input":"x"}`)))
	st := newSlowTool("st", 0, "result")

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestAgent",
		Description: "test",
		Model:       blk,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{st}},
		},
	})
	require.NoError(t, err)

	store := newCancelTestStore()
	runner := NewRunner(ctx, RunnerConfig{
		Agent:           agent,
		CheckPointStore: store,
	})

	cancelOpt, cancelFn := WithCancel()
	iter := runner.Run(ctx, []Message{schema.UserMessage("go")}, cancelOpt, WithCheckPointID("targeted-sp-1"))

	select {
	case <-blk.started:
	case <-time.After(5 * time.Second):
		t.Fatal("model did not start")
	}

	// Start cancelFn in background so the CAS happens before the model unblocks.
	cancelDone := make(chan error, 1)
	go func() {
		handle, _ := cancelFn(WithAgentCancelMode(CancelAfterChatModel))
		cancelDone <- handle.Wait()
	}()
	time.Sleep(50 * time.Millisecond)
	close(blk.unblockCh)

	cancelErr := <-cancelDone
	assert.NoError(t, cancelErr)

	var cancelError *CancelError
	for {
		e, ok := iter.Next()
		if !ok {
			break
		}
		var ce *CancelError
		if e.Err != nil && errors.As(e.Err, &ce) {
			cancelError = ce
		}
	}

	require.NotNil(t, cancelError, "expected CancelError")
	require.NotEmpty(t, cancelError.InterruptContexts, "CancelError should have InterruptContexts for targeted resume")

	// --- resume with targeted params ---
	targets := make(map[string]any)
	for _, ic := range cancelError.InterruptContexts {
		targets[ic.ID] = nil
	}

	resumeModel := &plainResponseModel{text: "resumed"}
	agent2, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestAgent",
		Description: "test",
		Model:       resumeModel,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{st}},
		},
	})
	require.NoError(t, err)

	runner2 := NewRunner(ctx, RunnerConfig{
		Agent:           agent2,
		CheckPointStore: store,
	})

	resumeIter, err := runner2.ResumeWithParams(ctx, "targeted-sp-1", &ResumeParams{Targets: targets})
	require.NoError(t, err)

	var gotOutput bool
	for {
		e, ok := resumeIter.Next()
		if !ok {
			break
		}
		if e.Err != nil {
			t.Fatalf("unexpected error during targeted resume: %v", e.Err)
		}
		if e.Output != nil && e.Output.MessageOutput != nil {
			gotOutput = true
		}
	}
	assert.True(t, gotOutput, "targeted resume should produce output")
}

// TestWithCancel_Resume_CancelAfterChatModel_MessagePreserved tests both the
// ReAct (with-tools) and noTools paths to ensure that when a
// CancelAfterChatModel safe-point fires and the run is later resumed, the
// original Message returned by the chat model is preserved through the
// StatefulInterrupt checkpoint.
//
// For the ReAct path: the model returns a tool-call message. On resume the
// cancelCheck node must return that same message so the branch routes to the
// ToolNode and the tool actually executes.
//
// For the noTools path: the model returns a plain text message. On resume the
// cancel-check lambda must return that same message as the chain output.
func TestWithCancel_Resume_CancelAfterChatModel_MessagePreserved(t *testing.T) {
	t.Run("react_path_tool_call_preserved", func(t *testing.T) {
		ctx := context.Background()

		// Phase-2 model returns no tool calls so the graph ends.
		// We track whether the tool actually executes on resume.
		toolExecuted := make(chan struct{}, 1)
		st := &callbackTool{
			name: "my_tool",
			onCall: func() {
				select {
				case toolExecuted <- struct{}{}:
				default:
				}
			},
		}

		// Phase-1 model returns a tool call.
		blk := newBlockingChatModel(toolCallMsg(toolCall("c1", "my_tool", `{"input":"x"}`)))

		agent1, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "test",
			Model:       blk,
			ToolsConfig: ToolsConfig{
				ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{st}},
			},
		})
		require.NoError(t, err)

		store := newCancelTestStore()
		runner1 := NewRunner(ctx, RunnerConfig{
			Agent:           agent1,
			CheckPointStore: store,
		})

		cancelOpt1, cancelFn1 := WithCancel()
		iter1 := runner1.Run(ctx, []Message{schema.UserMessage("hi")},
			cancelOpt1, WithCheckPointID("react-msg-preserved-1"))

		select {
		case <-blk.started:
		case <-time.After(5 * time.Second):
			t.Fatal("model did not start in phase 1")
		}

		cancelDone := make(chan error, 1)
		go func() {
			handle, _ := cancelFn1(WithAgentCancelMode(CancelAfterChatModel))
			cancelDone <- handle.Wait()
		}()
		time.Sleep(50 * time.Millisecond)
		close(blk.unblockCh)

		cancelErr := <-cancelDone
		assert.NoError(t, cancelErr)

		_, hasCancelError := drainEvents(iter1)
		assert.True(t, hasCancelError, "expected CancelError from phase 1")

		// Phase 2: resume. The model for phase-2 returns plain text (no tool
		// calls) so the react graph ends after one iteration. But first the
		// tool from the checkpoint must execute.
		resumeModel := &plainResponseModel{text: "done"}
		agent2, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "test",
			Model:       resumeModel,
			ToolsConfig: ToolsConfig{
				ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{st}},
			},
		})
		require.NoError(t, err)

		runner2 := NewRunner(ctx, RunnerConfig{
			Agent:           agent2,
			CheckPointStore: store,
		})

		resumeIter, err := runner2.Resume(ctx, "react-msg-preserved-1")
		require.NoError(t, err)

		for {
			e, ok := resumeIter.Next()
			if !ok {
				break
			}
			if e.Err != nil {
				t.Fatalf("unexpected error during resume: %v", e.Err)
			}
		}

		// The key assertion: the tool must have been called during resume,
		// which can only happen if the tool-call message was preserved.
		select {
		case <-toolExecuted:
			// success
		default:
			t.Fatal("tool was not executed on resume — the tool-call message was lost")
		}
	})

}

// TestWithCancel_SafePoint_DirectWorkflowChildAbortOnly verifies that a
// ChatModelAgent invoked as a direct workflow child does not participate in
// safe-point checkpointing. Safe-point cancel remains local to checkpoint-aware
// scopes; direct children only receive recursive immediate abort.
func TestWithCancel_SafePoint_DirectWorkflowChildAbortOnly(t *testing.T) {
	ctx := context.Background()

	bt := newBlockingTool("bt")

	// cma1: model returns a tool call immediately, tool blocks until unblocked
	cma1Model := newBlockingChatModel(toolCallMsg(toolCall("c1", "bt", `{"input":"x"}`)))
	close(cma1Model.unblockCh) // model returns immediately

	agent1, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name: "agent1", Description: "first", Instruction: "test",
		Model: cma1Model,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{bt}},
		},
	})
	require.NoError(t, err)

	agent2Model := &plainResponseModel{text: "agent2-response"}
	agent2, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name: "agent2", Description: "second", Instruction: "test",
		Model: agent2Model,
	})
	require.NoError(t, err)

	seqAgent, err := NewSequentialAgent(ctx, &SequentialAgentConfig{
		Name: "seq", Description: "sequential", SubAgents: []Agent{agent1, agent2},
	})
	require.NoError(t, err)

	runner := NewRunner(ctx, RunnerConfig{
		Agent: seqAgent, EnableStreaming: false,
	})

	cancelOpt, cancelFn := WithCancel()
	iter := runner.Run(ctx, []Message{schema.UserMessage("test")}, cancelOpt)

	// Wait for tool to start
	select {
	case <-bt.started:
	case <-time.After(5 * time.Second):
		t.Fatal("Tool did not start")
	}

	// Cancel while the direct child tool is still running. This should not
	// produce a sub-agent checkpoint: direct non-boundary nested runs ignore safe-point
	// cancel and continue until they finish normally.
	go func() {
		handle, _ := cancelFn(WithAgentCancelMode(CancelAfterToolCalls))
		_ = handle.Wait()
	}()

	// Give cancel time to register, then unblock tool
	time.Sleep(50 * time.Millisecond)
	close(bt.unblockCh)

	cancelCount := 0
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		var ce *CancelError
		if event.Err != nil && errors.As(event.Err, &ce) {
			cancelCount++
		}
	}

	assert.Equal(t, 0, cancelCount, "direct workflow child should not emit a safe-point CancelError")
}

func TestWithCancel_RecursiveImmediate_TransferChildCheckpoint(t *testing.T) {
	ctx := context.Background()

	subAgentModel := newContextAwareBlockingChatModel(schema.AssistantMessage("sub-agent done", nil))
	subAgentModelStarted := subAgentModel.started

	subAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "sub_agent",
		Description: "test sub agent",
		Instruction: "you are a sub agent",
		Model:       subAgentModel,
	})
	require.NoError(t, err)

	supervisorModel := &simpleChatModel{
		response: &schema.Message{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID: "call_1", Type: "function",
				Function: schema.FunctionCall{
					Name:      TransferToAgentToolName,
					Arguments: `{"agent_name": "sub_agent"}`,
				},
			}},
		},
	}

	store := newCancelTestStore()
	checkpointID := "recursive-immediate-transfer-child-checkpoint"
	runner := NewRunner(ctx, RunnerConfig{
		Agent:           newTransferSupervisorWithSubAgent(t, ctx, supervisorModel, subAgent),
		EnableStreaming: false,
		CheckPointStore: store,
	})

	cancelOpt, cancelFn := WithCancel()
	iter := runner.Run(ctx, []Message{schema.UserMessage("test")}, cancelOpt, WithCheckPointID(checkpointID))

	select {
	case <-subAgentModelStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("Sub-agent model did not start")
	}

	time.Sleep(50 * time.Millisecond)

	cancelDone := make(chan error, 1)
	go func() {
		handle, _ := cancelFn(WithRecursive())
		cancelDone <- handle.Wait()
	}()

	cancelErr := <-cancelDone
	require.NoError(t, cancelErr)

	_, cancelErrors := drainCancelErrors(iter)
	require.Len(t, cancelErrors, 1, "transfer child should emit a checkpoint CancelError")
	assert.Equal(t, CancelImmediate, cancelErrors[0].Info.Mode)

	assertCancelErrorIncludesAddress(t, cancelErrors[0], "sub_agent")

	resumeSubAgentCalls := int32(0)
	resumeSubAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "sub_agent",
		Description: "test sub agent",
		Instruction: "you are a sub agent",
		Model: &countingChatModel{
			callCount: &resumeSubAgentCalls,
			responses: []*schema.Message{
				schema.AssistantMessage("sub-agent resumed", nil),
			},
		},
	})
	require.NoError(t, err)

	resumeRunner := NewRunner(ctx, RunnerConfig{
		Agent:           newTransferSupervisorWithSubAgent(t, ctx, supervisorModel, resumeSubAgent),
		EnableStreaming: false,
		CheckPointStore: store,
	})
	resumeIter, err := resumeRunner.Resume(ctx, checkpointID)
	require.NoError(t, err)
	resumeEvents, resumeCancelErrors := drainCancelErrors(resumeIter)
	require.Empty(t, resumeCancelErrors)
	require.NotEmpty(t, resumeEvents)
	assert.Equal(t, int32(1), atomic.LoadInt32(&resumeSubAgentCalls), "transfer target should resume from its own checkpoint")
}

func TestWithCancel_RecursiveSafePoint_TransferChildCheckpoint(t *testing.T) {
	ctx := context.Background()

	subTool := &slowTool{
		name:   "sub_tool",
		delay:  10 * time.Millisecond,
		result: "sub tool result",
	}
	subAgentModel := newContextAwareBlockingChatModel(&schema.Message{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{{
			ID: "sub_call_1", Type: "function",
			Function: schema.FunctionCall{
				Name:      "sub_tool",
				Arguments: `{"input":"x"}`,
			},
		}},
	})
	subAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "sub_agent",
		Description: "test sub agent",
		Instruction: "you are a sub agent",
		Model:       subAgentModel,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{subTool},
			},
		},
	})
	require.NoError(t, err)

	supervisorModel := &simpleChatModel{
		response: &schema.Message{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID: "call_1", Type: "function",
				Function: schema.FunctionCall{
					Name:      TransferToAgentToolName,
					Arguments: `{"agent_name": "sub_agent"}`,
				},
			}},
		},
	}
	store := newCancelTestStore()
	checkpointID := "recursive-safe-point-transfer-child-checkpoint"
	runner := NewRunner(ctx, RunnerConfig{
		Agent:           newTransferSupervisorWithSubAgent(t, ctx, supervisorModel, subAgent),
		EnableStreaming: false,
		CheckPointStore: store,
	})

	cancelOpt, cancelFn := WithCancel()
	iter := runner.Run(ctx, []Message{schema.UserMessage("test")}, cancelOpt, WithCheckPointID(checkpointID))

	select {
	case <-subAgentModel.started:
	case <-time.After(10 * time.Second):
		t.Fatal("Sub-agent model did not start")
	}

	handle, contributed := cancelFn(WithAgentCancelMode(CancelAfterChatModel), WithRecursive())
	require.True(t, contributed)
	cancelDone := make(chan error, 1)
	go func() {
		cancelDone <- handle.Wait()
	}()
	time.Sleep(50 * time.Millisecond)
	close(subAgentModel.release)

	_, cancelErrors := drainCancelErrors(iter)
	require.NoError(t, <-cancelDone)
	require.Len(t, cancelErrors, 1, "transfer child should emit a safe-point checkpoint CancelError")
	assert.Equal(t, CancelAfterChatModel, cancelErrors[0].Info.Mode)

	assertCancelErrorIncludesAddress(t, cancelErrors[0], "sub_agent")

	resumeSubAgentCalls := int32(0)
	resumeSubAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "sub_agent",
		Description: "test sub agent",
		Instruction: "you are a sub agent",
		Model: &countingChatModel{
			callCount: &resumeSubAgentCalls,
			responses: []*schema.Message{
				schema.AssistantMessage("sub-agent resumed", nil),
			},
		},
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{subTool},
			},
		},
	})
	require.NoError(t, err)
	resumeRunner := NewRunner(ctx, RunnerConfig{
		Agent:           newTransferSupervisorWithSubAgent(t, ctx, supervisorModel, resumeSubAgent),
		EnableStreaming: false,
		CheckPointStore: store,
	})
	resumeIter, err := resumeRunner.Resume(ctx, checkpointID)
	require.NoError(t, err)
	resumeEvents, resumeCancelErrors := drainCancelErrors(resumeIter)
	require.Empty(t, resumeCancelErrors)
	require.NotEmpty(t, resumeEvents)
	assert.Equal(t, int32(1), atomic.LoadInt32(&resumeSubAgentCalls), "transfer target should resume from its safe-point checkpoint")
}

func TestWithCancel_RecursiveImmediate_MiddlewareChildAbortOnly(t *testing.T) {
	ctx := context.Background()

	childModel := newContextAwareBlockingChatModel(schema.AssistantMessage("child done", nil))
	childAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "MiddlewareChild",
		Description: "direct middleware child",
		Instruction: "child",
		Model:       childModel,
	})
	require.NoError(t, err)

	outerAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "OuterMiddlewareAgent",
		Description: "outer",
		Instruction: "outer",
		Model:       &plainResponseModel{text: "outer done"},
		Middlewares: []AgentMiddleware{{
			BeforeChatModel: func(ctx context.Context, _ *ChatModelAgentState) error {
				return runChildAgentToEnd(ctx, childAgent, "child work")
			},
		}},
	})
	require.NoError(t, err)

	store := newCancelTestStore()
	checkpointID := "recursive-immediate-middleware-child-abort-only"
	runner := NewRunner(ctx, RunnerConfig{Agent: outerAgent, CheckPointStore: store})

	cancelOpt, cancelFn := WithCancel()
	iter := runner.Run(ctx, []Message{schema.UserMessage("go")}, cancelOpt, WithCheckPointID(checkpointID))

	select {
	case <-childModel.started:
	case <-time.After(5 * time.Second):
		t.Fatal("middleware child model did not start")
	}

	handle, _ := cancelFn(WithRecursive())

	select {
	case <-childModel.cancelObserved:
	case <-time.After(time.Second):
		t.Fatal("middleware child did not observe recursive immediate context cancellation")
	}

	require.NoError(t, handle.Wait())
	_, cancelErrors := drainCancelErrors(iter)
	require.Len(t, cancelErrors, 1, "outer stream should emit exactly one root CancelError")
	assertCancelErrorExcludesAddresses(t, cancelErrors[0], "MiddlewareChild")

	resumeChildCalls := int32(0)
	resumeChildModel := &countingChatModel{
		callCount: &resumeChildCalls,
		responses: []*schema.Message{
			schema.AssistantMessage("fresh child run after resume", nil),
		},
	}
	resumeChildAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "MiddlewareChild",
		Description: "direct middleware child",
		Instruction: "child",
		Model:       resumeChildModel,
	})
	require.NoError(t, err)

	resumeOuterAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "OuterMiddlewareAgent",
		Description: "outer",
		Instruction: "outer",
		Model:       &plainResponseModel{text: "outer done after resume"},
		Middlewares: []AgentMiddleware{{
			BeforeChatModel: func(ctx context.Context, _ *ChatModelAgentState) error {
				return runChildAgentToEnd(ctx, resumeChildAgent, "child work")
			},
		}},
	})
	require.NoError(t, err)

	resumeRunner := NewRunner(ctx, RunnerConfig{Agent: resumeOuterAgent, CheckPointStore: store})
	resumeIter, err := resumeRunner.Resume(ctx, checkpointID)
	require.NoError(t, err)
	_, resumeCancelErrors := drainCancelErrors(resumeIter)
	require.Empty(t, resumeCancelErrors)
	assert.Equal(t, int32(1), atomic.LoadInt32(&resumeChildCalls), "middleware child should be invoked as a fresh Run on root resume")
}

func TestWithCancel_RecursiveImmediate_MiddlewareChildAgentToolCheckpointResumeBarrier(t *testing.T) {
	ctx := context.Background()

	leafModel := newBlockingChatModel(schema.AssistantMessage("leaf done", nil))
	t.Cleanup(func() {
		close(leafModel.unblockCh)
	})
	leafAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "MiddlewareLeafAgent",
		Description: "leaf agent wrapped as a tool under a direct middleware child",
		Instruction: "leaf",
		Model:       leafModel,
	})
	require.NoError(t, err)

	childModelCallCount := int32(0)
	childModel := &countingChatModel{
		callCount: &childModelCallCount,
		responses: []*schema.Message{
			toolCallMsg(toolCall("child-leaf", "MiddlewareLeafAgent", `{"request":"leaf work"}`)),
			schema.AssistantMessage("child done", nil),
		},
	}
	childAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "MiddlewareChildWithAgentTool",
		Description: "direct middleware child with an agent tool",
		Instruction: "child",
		Model:       childModel,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{NewAgentTool(ctx, leafAgent)},
			},
		},
	})
	require.NoError(t, err)

	childInterruptSeen := int32(0)
	resumeChildCalls := int32(0)
	resumeLeafCalls := int32(0)
	outerAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "OuterMiddlewareAgent",
		Description: "outer",
		Instruction: "outer",
		Model:       &plainResponseModel{text: "outer done"},
		Middlewares: []AgentMiddleware{{
			BeforeChatModel: func(ctx context.Context, _ *ChatModelAgentState) error {
				iter := childAgent.Run(ctx, &AgentInput{Messages: []Message{schema.UserMessage("child work")}})
				for {
					event, ok := iter.Next()
					if !ok {
						return nil
					}
					if event.Action != nil && event.Action.Interrupted != nil {
						atomic.StoreInt32(&childInterruptSeen, 1)
					}
					if event.Err != nil {
						return event.Err
					}
				}
			},
		}},
	})
	require.NoError(t, err)

	store := newCancelTestStore()
	checkpointID := "recursive-immediate-middleware-agent-tool-resume-barrier"
	runner := NewRunner(ctx, RunnerConfig{Agent: outerAgent, CheckPointStore: store})

	cancelOpt, cancelFn := WithCancel()
	iter := runner.Run(ctx, []Message{schema.UserMessage("go")}, cancelOpt, WithCheckPointID(checkpointID))

	select {
	case <-leafModel.started:
	case <-time.After(5 * time.Second):
		t.Fatal("leaf agent model did not start")
	}

	handle, _ := cancelFn(WithRecursive())
	assert.NoError(t, handle.Wait(),
		"the root may handle its own immediate interrupt, but not a checkpoint hidden behind a direct middleware Run")
	_, cancelErrors := drainCancelErrors(iter)
	require.Len(t, cancelErrors, 1, "root stream should expose only the root immediate cancel")
	assertCancelErrorExcludesAddresses(t, cancelErrors[0], "MiddlewareLeafAgent")
	assert.Equal(t, int32(0), atomic.LoadInt32(&childInterruptSeen),
		"the abort-only middleware child must swallow the nested AgentTool interrupt action instead of surfacing it as a resumable boundary")

	resumeLeafModel := &countingChatModel{
		callCount: &resumeLeafCalls,
		responses: []*schema.Message{
			schema.AssistantMessage("leaf done after resume", nil),
		},
	}
	resumeLeafAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "MiddlewareLeafAgent",
		Description: "leaf agent wrapped as a tool under a direct middleware child",
		Instruction: "leaf",
		Model:       resumeLeafModel,
	})
	require.NoError(t, err)

	resumeChildModel := &countingChatModel{
		callCount: &resumeChildCalls,
		responses: []*schema.Message{
			toolCallMsg(toolCall("child-leaf", "MiddlewareLeafAgent", `{"request":"leaf work"}`)),
			schema.AssistantMessage("child done after resume", nil),
		},
	}
	resumeChildAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "MiddlewareChildWithAgentTool",
		Description: "direct middleware child with an agent tool",
		Instruction: "child",
		Model:       resumeChildModel,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{NewAgentTool(ctx, resumeLeafAgent)},
			},
		},
	})
	require.NoError(t, err)

	resumeOuterAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "OuterMiddlewareAgent",
		Description: "outer",
		Instruction: "outer",
		Model:       &plainResponseModel{text: "outer done after resume"},
		Middlewares: []AgentMiddleware{{
			BeforeChatModel: func(ctx context.Context, _ *ChatModelAgentState) error {
				return runChildAgentToEnd(ctx, resumeChildAgent, "child work")
			},
		}},
	})
	require.NoError(t, err)

	resumeRunner := NewRunner(ctx, RunnerConfig{Agent: resumeOuterAgent, CheckPointStore: store})
	resumeIter, err := resumeRunner.Resume(ctx, checkpointID)
	require.NoError(t, err)
	_, resumeCancelErrors := drainCancelErrors(resumeIter)
	require.Empty(t, resumeCancelErrors)
	assert.GreaterOrEqual(t, atomic.LoadInt32(&resumeChildCalls), int32(1), "middleware child should be invoked as a fresh Run on root resume")
	assert.Equal(t, int32(1), atomic.LoadInt32(&resumeLeafCalls), "nested AgentTool should run fresh, not resume from the hidden checkpoint")
}

func TestWithCancel_RecursiveImmediate_BeforeAgentChildAbortOnly(t *testing.T) {
	ctx := context.Background()

	childModel := newContextAwareBlockingChatModel(schema.AssistantMessage("child done", nil))
	childAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "BeforeAgentMiddlewareChild",
		Description: "direct BeforeAgent child",
		Instruction: "child",
		Model:       childModel,
	})
	require.NoError(t, err)

	outerAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "OuterBeforeAgentMiddlewareAgent",
		Description: "outer",
		Instruction: "outer",
		Model:       &plainResponseModel{text: "outer done"},
		Handlers: []ChatModelAgentMiddleware{
			&testBeforeAgentHandler{fn: func(ctx context.Context, runCtx *ChatModelAgentContext[*schema.Message]) (context.Context, *ChatModelAgentContext[*schema.Message], error) {
				iter := childAgent.Run(ctx, &AgentInput{Messages: []Message{schema.UserMessage("child work")}})
				for {
					event, ok := iter.Next()
					if !ok {
						return ctx, runCtx, nil
					}
					if event.Err != nil {
						return ctx, runCtx, event.Err
					}
				}
			}},
		},
	})
	require.NoError(t, err)

	cancelOpt, cancelFn := WithCancel()
	iterCh := make(chan *AsyncIterator[*AgentEvent], 1)
	go func() {
		iterCh <- NewRunner(ctx, RunnerConfig{Agent: outerAgent}).Run(ctx, []Message{schema.UserMessage("go")}, cancelOpt)
	}()

	select {
	case <-childModel.started:
	case <-time.After(5 * time.Second):
		t.Fatal("BeforeAgent middleware child model did not start")
	}

	handle, _ := cancelFn(WithRecursive())

	select {
	case <-childModel.cancelObserved:
	case <-time.After(time.Second):
		t.Fatal("BeforeAgent middleware child did not observe recursive immediate context cancellation")
	}

	require.NoError(t, handle.Wait())
	var iter *AsyncIterator[*AgentEvent]
	select {
	case iter = <-iterCh:
	case <-time.After(5 * time.Second):
		t.Fatal("outer Run did not return after BeforeAgent child cancellation")
	}
	_, cancelErrors := drainCancelErrors(iter)
	require.Len(t, cancelErrors, 1, "outer stream should emit exactly one root CancelError")
	assertCancelErrorExcludesAddresses(t, cancelErrors[0], "BeforeAgentMiddlewareChild")
}

func TestWithCancel_RecursiveImmediate_MultiLevelMiddlewareChildrenAbortOnly(t *testing.T) {
	ctx := context.Background()

	cModel := newContextAwareBlockingChatModel(schema.AssistantMessage("c done", nil))
	agentC, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "MiddlewareChildC",
		Description: "direct middleware child C",
		Instruction: "child C",
		Model:       cModel,
	})
	require.NoError(t, err)

	bCancelObserved := make(chan struct{}, 1)
	agentB, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "MiddlewareChildB",
		Description: "direct middleware child B",
		Instruction: "child B",
		Model:       &plainResponseModel{text: "b done"},
		Middlewares: []AgentMiddleware{{
			BeforeChatModel: func(ctx context.Context, _ *ChatModelAgentState) error {
				go func() {
					<-ctx.Done()
					select {
					case bCancelObserved <- struct{}{}:
					default:
					}
				}()
				return runChildAgentToEnd(ctx, agentC, "c work")
			},
		}},
	})
	require.NoError(t, err)

	outerAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "OuterMultiLevelMiddlewareAgent",
		Description: "outer",
		Instruction: "outer",
		Model:       &plainResponseModel{text: "outer done"},
		Middlewares: []AgentMiddleware{{
			BeforeChatModel: func(ctx context.Context, _ *ChatModelAgentState) error {
				return runChildAgentToEnd(ctx, agentB, "b work")
			},
		}},
	})
	require.NoError(t, err)

	cancelOpt, cancelFn := WithCancel()
	iter := NewRunner(ctx, RunnerConfig{Agent: outerAgent}).Run(ctx, []Message{schema.UserMessage("go")}, cancelOpt)

	select {
	case <-cModel.started:
	case <-time.After(5 * time.Second):
		t.Fatal("middleware child C model did not start")
	}

	handle, _ := cancelFn(WithRecursive())

	select {
	case <-bCancelObserved:
	case <-time.After(time.Second):
		t.Fatal("middleware child B did not observe recursive immediate context cancellation")
	}
	select {
	case <-cModel.cancelObserved:
	case <-time.After(time.Second):
		t.Fatal("middleware child C did not observe recursive immediate context cancellation")
	}

	require.NoError(t, handle.Wait())
	_, cancelErrors := drainCancelErrors(iter)
	require.Len(t, cancelErrors, 1, "outer stream should emit exactly one root CancelError")
	assertCancelErrorExcludesAddresses(t, cancelErrors[0], "MiddlewareChildB", "MiddlewareChildC")
}

func TestWithCancel_NonRecursiveImmediate_DoesNotAbortMiddlewareChild(t *testing.T) {
	ctx := context.Background()

	childModel := newContextAwareBlockingChatModel(schema.AssistantMessage("child done", nil))
	released := false
	t.Cleanup(func() {
		if !released {
			close(childModel.release)
		}
	})
	childAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "MiddlewareChild",
		Description: "direct middleware child",
		Instruction: "child",
		Model:       childModel,
	})
	require.NoError(t, err)

	outerAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "OuterMiddlewareAgent",
		Description: "outer",
		Instruction: "outer",
		Model:       &plainResponseModel{text: "outer done"},
		Middlewares: []AgentMiddleware{{
			BeforeChatModel: func(ctx context.Context, _ *ChatModelAgentState) error {
				return runChildAgentToEnd(ctx, childAgent, "child work")
			},
		}},
	})
	require.NoError(t, err)

	cancelOpt, cancelFn := WithCancel()
	iter := NewRunner(ctx, RunnerConfig{Agent: outerAgent}).Run(ctx, []Message{schema.UserMessage("go")}, cancelOpt)

	select {
	case <-childModel.started:
	case <-time.After(5 * time.Second):
		t.Fatal("middleware child model did not start")
	}

	handle, _ := cancelFn()
	assertNotClosedWithin(t, childModel.cancelObserved, 100*time.Millisecond)

	close(childModel.release)
	released = true

	require.NoError(t, handle.Wait())
	_, cancelErrors := drainCancelErrors(iter)
	require.Len(t, cancelErrors, 1)
}

// slowStreamingTool implements StreamableTool (but NOT InvokableTool), streaming
// chunks slowly so CancelImmediate can fire mid-stream.
type slowStreamingTool struct {
	name          string
	chunkInterval time.Duration
	chunks        []string
	started       chan struct{}
	gate          chan struct{} // if non-nil, blocks after first chunk until closed
}

func (t *slowStreamingTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: t.name,
		Desc: "slow streaming tool",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"input": {Type: "string"},
		}),
	}, nil
}

func (t *slowStreamingTool) StreamableRun(_ context.Context, _ string, _ ...tool.Option) (*schema.StreamReader[string], error) {
	r, w := schema.Pipe[string](1)
	go func() {
		defer w.Close()
		select {
		case t.started <- struct{}{}:
		default:
		}
		for i, chunk := range t.chunks {
			time.Sleep(t.chunkInterval)
			if closed := w.Send(chunk, nil); closed {
				return
			}
			// After the second chunk, block on gate so the caller can
			// issue a cancel while the tool is deterministically still streaming.
			// We wait until chunk index 1 (second chunk) so that the framework
			// has time to receive the first chunk and forward the streaming
			// event to the iterator, ensuring ErrStreamCanceled is observable.
			if i == 1 && t.gate != nil {
				<-t.gate
			}
		}
	}()
	return r, nil
}

// toolCallStreamModel returns a tool-call message on the first Stream call,
// then a plain text response on subsequent calls.
type toolCallStreamModel struct {
	callCount int32
}

func (m *toolCallStreamModel) Generate(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	if atomic.AddInt32(&m.callCount, 1) == 1 {
		return toolCallMsg(toolCall("c1", "slow_tool", `{"input":"x"}`)), nil
	}
	return schema.AssistantMessage("done", nil), nil
}

func (m *toolCallStreamModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func (m *toolCallStreamModel) BindTools(_ []*schema.ToolInfo) error { return nil }

// TestWithCancel_CancelImmediate_StreamableToolAborted verifies that CancelImmediate
// during StreamableTool streaming surfaces ErrStreamCanceled on the tool's
// MessageStream.Recv(), just like it does for ChatModel streaming.
func TestWithCancel_CancelImmediate_StreamableToolAborted(t *testing.T) {
	ctx := context.Background()

	tcm := &toolCallStreamModel{}
	gate := make(chan struct{})
	st := &slowStreamingTool{
		name:          "slow_tool",
		chunkInterval: 100 * time.Millisecond,
		chunks:        []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"},
		started:       make(chan struct{}, 1),
		gate:          gate,
	}

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestAgent",
		Description: "test",
		Model:       tcm,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{st}},
		},
	})
	require.NoError(t, err)

	runner := NewRunner(ctx, RunnerConfig{
		Agent:           agent,
		EnableStreaming: true,
	})

	cancelOpt, cancelFn := WithCancel()
	iter := runner.Run(ctx, []Message{schema.UserMessage("hi")}, cancelOpt)

	// Wait for the tool to start streaming and send its first chunk.
	// The tool then blocks on the gate, guaranteeing the execution is
	// still in progress when we issue the cancel.
	select {
	case <-st.started:
	case <-time.After(5 * time.Second):
		t.Fatal("tool did not start streaming")
	}

	// Drain events in a separate goroutine so we can issue the cancel
	// from the main goroutine after confirming the tool stream event
	// has been received.
	type result struct {
		foundStreamCanceled bool
		foundCancelError    bool
	}
	resultCh := make(chan result, 1)
	toolStreamReady := make(chan struct{})
	go func() {
		var r result
		for {
			e, ok := iter.Next()
			if !ok {
				break
			}

			// ErrStreamCanceled appears on the tool's MessageStream.Recv()
			if e.Output != nil && e.Output.MessageOutput != nil && e.Output.MessageOutput.IsStreaming &&
				e.Output.MessageOutput.Role == schema.Tool {
				stream := e.Output.MessageOutput.MessageStream
				// Consume the first chunk so we are sure the stream is active,
				// then signal readiness. This ensures cancel fires while we are
				// blocked inside Recv(), preventing a race where cancel completes
				// before we start consuming.
				if _, firstErr := stream.Recv(); firstErr == nil {
					close(toolStreamReady)
				} else {
					if errors.Is(firstErr, ErrStreamCanceled) {
						r.foundStreamCanceled = true
					}
					close(toolStreamReady)
					continue
				}
				for {
					_, recvErr := stream.Recv()
					if recvErr != nil {
						if errors.Is(recvErr, ErrStreamCanceled) {
							r.foundStreamCanceled = true
						}
						break
					}
				}
			}

			if e.Action != nil && e.Action.Interrupted != nil {
				r.foundCancelError = true
			}
			var ce *CancelError
			if e.Err != nil && errors.As(e.Err, &ce) {
				r.foundCancelError = true
			}
		}
		resultCh <- r
	}()

	// Wait for the iterator goroutine to receive the tool streaming event.
	// At this point the tool goroutine is blocked on the gate, and the
	// iterator goroutine is blocked on stream.Recv(), so the execution is
	// guaranteed to still be in progress.
	select {
	case <-toolStreamReady:
	case <-time.After(5 * time.Second):
		t.Fatal("tool stream event was not received by the iterator")
	}

	// Issue cancel while the tool goroutine is blocked on gate.
	// wrapStreamWithCancelMonitoring detects immediateChan and sends
	// ErrStreamCanceled to the consumer side. We do NOT close gate here —
	// keeping the tool goroutine blocked ensures the graph interrupt (timeout=0)
	// wins the race against normal completion. Close gate in defer for cleanup.
	defer close(gate)
	handle, _ := cancelFn()
	cancelErr := handle.Wait()

	r := <-resultCh

	if errors.Is(cancelErr, ErrExecutionEnded) {
		// On slower runtimes (e.g. Go 1.19 CI), the execution can complete
		// before the cancel signal is delivered — this is a valid race outcome.
		t.Log("cancel raced with completion (ErrExecutionEnded) — skipping cancel assertions")
		return
	}
	assert.NoError(t, cancelErr)
	assert.True(t, r.foundStreamCanceled, "expected ErrStreamCanceled on tool's MessageStream.Recv()")
	assert.True(t, r.foundCancelError, "expected CancelError in event stream")
}

// TestWithCancel_CancelImmediate_NestedAgentTool_ResumeFromToolsNode verifies that
// when a nested ChatModelAgent (wrapped as an AgentTool inside an outer ChatModelAgent)
// is canceled via CancelImmediate and then resumed with Runner.Resume (no params),
// the outer agent resumes from the ToolsNode rather than restarting from the beginning.
//
// Regression test: previously, the outer ChatModelAgent would restart from its Init/ChatModel
// node instead of resuming from the ToolsNode, causing the outer model to be called again
// with the original user message before the AgentTool and inner ChatModelAgent were resumed.
func TestWithCancel_CancelImmediate_NestedAgentTool_ResumeFromToolsNode(t *testing.T) {
	for _, tc := range []struct {
		name            string
		enableStreaming bool
		innerHasTools   bool
		recursive       bool
	}{
		{"Invoke_InnerNoTools_NonRecursive", false, false, false},
		{"Stream_InnerNoTools_NonRecursive", true, false, false},
		{"Invoke_InnerWithTools_NonRecursive", false, true, false},
		{"Stream_InnerWithTools_NonRecursive", true, true, false},
		{"Invoke_InnerNoTools_Recursive", false, false, true},
		{"Stream_InnerNoTools_Recursive", true, false, true},
		{"Invoke_InnerWithTools_Recursive", false, true, true},
		{"Stream_InnerWithTools_Recursive", true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()

			// --- inner agent: its model blocks so we can cancel mid-execution ---
			var innerTools []tool.BaseTool
			var innerModelResp *schema.Message
			if tc.innerHasTools {
				innerModelResp = toolCallMsg(toolCall("ic1", "inner_tool", `{"input":"x"}`))
				innerTools = []tool.BaseTool{newBlockingTool("inner_tool")}
			} else {
				innerModelResp = &schema.Message{Role: schema.Assistant, Content: "inner agent done"}
			}
			innerModel := newBlockingChatModel(innerModelResp)
			t.Cleanup(func() {
				close(innerModel.unblockCh)
			})

			innerCfg := &ChatModelAgentConfig{
				Name:        "InnerAgent",
				Description: "inner agent that blocks",
				Instruction: "you are an inner agent",
				Model:       innerModel,
			}
			if len(innerTools) > 0 {
				innerCfg.ToolsConfig = ToolsConfig{
					ToolsNodeConfig: compose.ToolsNodeConfig{Tools: innerTools},
				}
			}
			innerAgent, err := NewChatModelAgent(ctx, innerCfg)
			require.NoError(t, err)

			// --- outer agent: counting model ---
			// Call 1: returns a tool call that invokes InnerAgent.
			// Call 2 (only needed on resume): returns a plain final answer.
			outerModelCallCount := int32(0)
			outerModel := &countingChatModel{
				callCount: &outerModelCallCount,
				responses: []*schema.Message{
					toolCallMsg(toolCall("c1", "InnerAgent", `{"request":"do something"}`)),
					schema.AssistantMessage("outer completed", nil),
				},
			}

			outerAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
				Name:        "OuterAgent",
				Description: "outer agent with nested agent tool",
				Instruction: "you are an outer agent",
				Model:       outerModel,
				ToolsConfig: ToolsConfig{
					ToolsNodeConfig: compose.ToolsNodeConfig{
						Tools: []tool.BaseTool{NewAgentTool(ctx, innerAgent)},
					},
				},
			})
			require.NoError(t, err)

			store := newCancelTestStore()
			checkpointID := "cancel-nested-resume-" + tc.name

			runner1 := NewRunner(ctx, RunnerConfig{
				Agent:           outerAgent,
				EnableStreaming: tc.enableStreaming,
				CheckPointStore: store,
			})

			// --- phase 1: run and cancel while inner agent model is blocked ---
			cancelOpt, cancelFn := WithCancel()
			iter := runner1.Run(ctx, []Message{schema.UserMessage("go")}, cancelOpt, WithCheckPointID(checkpointID))

			// Wait for inner model to start (meaning outer model already returned tool call).
			select {
			case <-innerModel.started:
			case <-time.After(5 * time.Second):
				t.Fatal("inner model did not start")
			}

			// At this point outerModel should have been called exactly once.
			assert.Equal(t, int32(1), atomic.LoadInt32(&outerModelCallCount),
				"outer model should have been called once before cancel")

			// Cancel immediately. Recursive cases additionally propagate the cancel
			// request into the AgentTool's internal ChatModelAgent.
			var handle *CancelHandle
			if tc.recursive {
				handle, _ = cancelFn(WithRecursive())
			} else {
				handle, _ = cancelFn()
			}
			cancelErr := handle.Wait()
			assert.NoError(t, cancelErr)

			_, hasCancelError := drainEvents(iter)
			assert.True(t, hasCancelError, "expected CancelError from canceled nested agent tool")

			// --- phase 2: resume with Runner.Resume (no ResumeWithParams, no interrupt ID) ---
			// Build fresh agents for resume. Recursive cancel should resume the
			// inner ChatModelAgent inside AgentTool before the top-level
			// ChatModelAgent produces the final answer.
			resumeFirstModelCall := make(chan string, 5)
			resumeOuterModelCallCount := int32(0)
			resumeOuterModel := &countingChatModel{
				callCount: &resumeOuterModelCallCount,
				callCh:    resumeFirstModelCall,
				callLabel: "outer",
				responses: []*schema.Message{
					schema.AssistantMessage("outer completed after resume", nil),
				},
			}

			resumeInnerModelCallCount := int32(0)
			resumeInnerResponses := []*schema.Message{schema.AssistantMessage("inner agent done after resume", nil)}
			if len(innerTools) > 0 {
				resumeInnerResponses = []*schema.Message{
					toolCallMsg(toolCall("ic1", "inner_tool", `{"input":"x"}`)),
					schema.AssistantMessage("inner agent done after resume", nil),
				}
			}
			resumeInnerModel := &countingChatModel{
				callCount: &resumeInnerModelCallCount,
				callCh:    resumeFirstModelCall,
				callLabel: "inner",
				responses: resumeInnerResponses,
			}
			resumeInnerCfg := &ChatModelAgentConfig{
				Name:        "InnerAgent",
				Description: "inner agent that returns immediately on resume",
				Instruction: "you are an inner agent",
				Model:       resumeInnerModel,
			}
			if len(innerTools) > 0 {
				resumeInnerCfg.ToolsConfig = ToolsConfig{
					ToolsNodeConfig: compose.ToolsNodeConfig{
						Tools: []tool.BaseTool{newSlowTool("inner_tool", 0, "inner tool result")},
					},
				}
			}
			resumeInnerAgent, err := NewChatModelAgent(ctx, resumeInnerCfg)
			require.NoError(t, err)

			resumeOuterAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
				Name:        "OuterAgent",
				Description: "outer agent with nested agent tool",
				Instruction: "you are an outer agent",
				Model:       resumeOuterModel,
				ToolsConfig: ToolsConfig{
					ToolsNodeConfig: compose.ToolsNodeConfig{
						Tools: []tool.BaseTool{NewAgentTool(ctx, resumeInnerAgent)},
					},
				},
			})
			require.NoError(t, err)

			runner2 := NewRunner(ctx, RunnerConfig{
				Agent:           resumeOuterAgent,
				EnableStreaming: tc.enableStreaming,
				CheckPointStore: store,
			})

			resumeIter, err := runner2.Resume(ctx, checkpointID)
			require.NoError(t, err)

			select {
			case firstModel := <-resumeFirstModelCall:
				if tc.recursive {
					assert.Equal(t, "inner", firstModel,
						"recursive cancel should resume the AgentTool/internal ChatModelAgent first")
				} else {
					assert.Contains(t, []string{"outer", "inner"}, firstModel,
						"non-recursive cancel does not define whether a root or already-persisted inner checkpoint resumes first")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("no model call observed during resume")
			}

			var resumeEvents []*AgentEvent
			for {
				event, ok := resumeIter.Next()
				if !ok {
					break
				}
				if event.Err != nil {
					t.Fatalf("unexpected error during resume: %v", event.Err)
				}
				resumeEvents = append(resumeEvents, event)
			}

			// The outer model should have been called exactly once during resume
			// (to produce the final answer after receiving tool results).
			// If it was called with the original user message (restarting from scratch),
			// the counting model would either exceed its response list or the call count
			// would be wrong.
			assert.Equal(t, int32(1), atomic.LoadInt32(&resumeOuterModelCallCount),
				"outer model should be called exactly once during resume (for final answer after tool results), "+
					"not restarted from the beginning")

			// Verify we got the completion output.
			var gotOutput bool
			for _, event := range resumeEvents {
				content, err := messageOutputContent(event)
				require.NoError(t, err)
				if content == "outer completed after resume" {
					gotOutput = true
				}
			}
			assert.True(t, gotOutput, "should get final output from resumed outer agent")
		})
	}
}

func TestWithCancel_CancelImmediate_RecursiveAgentTool_ResumeDeepestAgentTool(t *testing.T) {
	ctx := context.Background()

	leafModel := newBlockingChatModel(schema.AssistantMessage("leaf done", nil))
	t.Cleanup(func() {
		close(leafModel.unblockCh)
	})
	leafAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "LeafAgent",
		Description: "leaf agent that blocks",
		Instruction: "you are a leaf agent",
		Model:       leafModel,
	})
	require.NoError(t, err)

	middleModelCallCount := int32(0)
	middleModel := &countingChatModel{
		callCount: &middleModelCallCount,
		responses: []*schema.Message{
			toolCallMsg(toolCall("middle-leaf", "LeafAgent", `{"request":"leaf work"}`)),
			schema.AssistantMessage("middle done", nil),
		},
	}
	middleAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "MiddleAgent",
		Description: "middle agent with an agent tool",
		Instruction: "you are a middle agent",
		Model:       middleModel,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{NewAgentTool(ctx, leafAgent)},
			},
		},
	})
	require.NoError(t, err)

	outerModelCallCount := int32(0)
	outerModel := &countingChatModel{
		callCount: &outerModelCallCount,
		responses: []*schema.Message{
			toolCallMsg(toolCall("outer-middle", "MiddleAgent", `{"request":"middle work"}`)),
			schema.AssistantMessage("outer done", nil),
		},
	}
	outerAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "OuterAgent",
		Description: "outer agent with recursive agent tool nesting",
		Instruction: "you are an outer agent",
		Model:       outerModel,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{NewAgentTool(ctx, middleAgent)},
			},
		},
	})
	require.NoError(t, err)

	store := newCancelTestStore()
	checkpointID := "cancel-recursive-agent-tool-resume"
	runner1 := NewRunner(ctx, RunnerConfig{Agent: outerAgent, CheckPointStore: store})

	cancelOpt, cancelFn := WithCancel()
	iter := runner1.Run(ctx, []Message{schema.UserMessage("go")}, cancelOpt, WithCheckPointID(checkpointID))

	select {
	case <-leafModel.started:
	case <-time.After(5 * time.Second):
		t.Fatal("leaf model did not start")
	}

	handle, _ := cancelFn(WithRecursive())
	require.NoError(t, handle.Wait())
	_, hasCancelError := drainEvents(iter)
	assert.True(t, hasCancelError, "expected CancelError from recursive nested agent tool")

	firstModelCall := make(chan string, 8)
	resumeLeafModelCallCount := int32(0)
	resumeLeafModel := &countingChatModel{
		callCount: &resumeLeafModelCallCount,
		callCh:    firstModelCall,
		callLabel: "leaf",
		responses: []*schema.Message{schema.AssistantMessage("leaf done after resume", nil)},
	}
	resumeLeafAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "LeafAgent",
		Description: "leaf agent that returns on resume",
		Instruction: "you are a leaf agent",
		Model:       resumeLeafModel,
	})
	require.NoError(t, err)

	resumeMiddleModelCallCount := int32(0)
	resumeMiddleModel := &countingChatModel{
		callCount: &resumeMiddleModelCallCount,
		callCh:    firstModelCall,
		callLabel: "middle",
		responses: []*schema.Message{schema.AssistantMessage("middle done after resume", nil)},
	}
	resumeMiddleAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "MiddleAgent",
		Description: "middle agent with an agent tool",
		Instruction: "you are a middle agent",
		Model:       resumeMiddleModel,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{NewAgentTool(ctx, resumeLeafAgent)},
			},
		},
	})
	require.NoError(t, err)

	resumeOuterModelCallCount := int32(0)
	resumeOuterModel := &countingChatModel{
		callCount: &resumeOuterModelCallCount,
		callCh:    firstModelCall,
		callLabel: "outer",
		responses: []*schema.Message{schema.AssistantMessage("outer done after resume", nil)},
	}
	resumeOuterAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "OuterAgent",
		Description: "outer agent with recursive agent tool nesting",
		Instruction: "you are an outer agent",
		Model:       resumeOuterModel,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{NewAgentTool(ctx, resumeMiddleAgent)},
			},
		},
	})
	require.NoError(t, err)

	runner2 := NewRunner(ctx, RunnerConfig{Agent: resumeOuterAgent, CheckPointStore: store})
	resumeIter, err := runner2.Resume(ctx, checkpointID)
	require.NoError(t, err)

	select {
	case first := <-firstModelCall:
		assert.Equal(t, "leaf", first, "recursive AgentTool nesting should resume the deepest internal agent first")
	case <-time.After(5 * time.Second):
		t.Fatal("no model call observed during resume")
	}

	resumeEvents, hasResumeCancelError := drainEvents(resumeIter)
	require.False(t, hasResumeCancelError, "resume should complete without another CancelError")
	assert.NotEmpty(t, resumeEvents)
	assert.Equal(t, int32(1), atomic.LoadInt32(&resumeLeafModelCallCount))
	assert.Equal(t, int32(1), atomic.LoadInt32(&resumeMiddleModelCallCount))
	assert.Equal(t, int32(1), atomic.LoadInt32(&resumeOuterModelCallCount))
}

func TestWithCancel_CancelImmediate_ConcurrentAgentTools_ResumeWithoutRestart(t *testing.T) {
	ctx := context.Background()

	innerAModel := newBlockingChatModel(schema.AssistantMessage("inner A done", nil))
	innerBModel := newBlockingChatModel(schema.AssistantMessage("inner B done", nil))
	t.Cleanup(func() {
		close(innerAModel.unblockCh)
		close(innerBModel.unblockCh)
	})

	innerAAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "InnerAgentA",
		Description: "inner agent A",
		Instruction: "you are inner agent A",
		Model:       innerAModel,
	})
	require.NoError(t, err)
	innerBAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "InnerAgentB",
		Description: "inner agent B",
		Instruction: "you are inner agent B",
		Model:       innerBModel,
	})
	require.NoError(t, err)

	outerModelCallCount := int32(0)
	outerModel := &countingChatModel{
		callCount: &outerModelCallCount,
		responses: []*schema.Message{
			toolCallMsg(
				toolCall("outer-a", "InnerAgentA", `{"request":"work A"}`),
				toolCall("outer-b", "InnerAgentB", `{"request":"work B"}`),
			),
			schema.AssistantMessage("outer done", nil),
		},
	}
	outerAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "OuterAgent",
		Description: "outer agent with concurrent agent tools",
		Instruction: "you are an outer agent",
		Model:       outerModel,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{
					NewAgentTool(ctx, innerAAgent),
					NewAgentTool(ctx, innerBAgent),
				},
			},
		},
	})
	require.NoError(t, err)

	store := newCancelTestStore()
	checkpointID := "cancel-concurrent-agent-tools-resume"
	runner1 := NewRunner(ctx, RunnerConfig{Agent: outerAgent, CheckPointStore: store})

	cancelOpt, cancelFn := WithCancel()
	iter := runner1.Run(ctx, []Message{schema.UserMessage("go")}, cancelOpt, WithCheckPointID(checkpointID))

	for _, started := range []chan struct{}{innerAModel.started, innerBModel.started} {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("both concurrent inner models should start before cancel")
		}
	}

	handle, _ := cancelFn(WithRecursive())
	require.NoError(t, handle.Wait())
	_, hasCancelError := drainEvents(iter)
	assert.True(t, hasCancelError, "expected CancelError from concurrent agent tools")

	firstModelCall := make(chan string, 8)
	resumeInnerAModelCallCount := int32(0)
	resumeInnerAModel := &countingChatModel{
		callCount: &resumeInnerAModelCallCount,
		callCh:    firstModelCall,
		callLabel: "innerA",
		responses: []*schema.Message{schema.AssistantMessage("inner A done after resume", nil)},
	}
	resumeInnerBModelCallCount := int32(0)
	resumeInnerBModel := &countingChatModel{
		callCount: &resumeInnerBModelCallCount,
		callCh:    firstModelCall,
		callLabel: "innerB",
		responses: []*schema.Message{schema.AssistantMessage("inner B done after resume", nil)},
	}

	resumeInnerAAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "InnerAgentA",
		Description: "inner agent A",
		Instruction: "you are inner agent A",
		Model:       resumeInnerAModel,
	})
	require.NoError(t, err)
	resumeInnerBAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "InnerAgentB",
		Description: "inner agent B",
		Instruction: "you are inner agent B",
		Model:       resumeInnerBModel,
	})
	require.NoError(t, err)

	resumeOuterModelCallCount := int32(0)
	resumeOuterModel := &countingChatModel{
		callCount: &resumeOuterModelCallCount,
		callCh:    firstModelCall,
		callLabel: "outer",
		responses: []*schema.Message{schema.AssistantMessage("outer done after resume", nil)},
	}
	resumeOuterAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "OuterAgent",
		Description: "outer agent with concurrent agent tools",
		Instruction: "you are an outer agent",
		Model:       resumeOuterModel,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{
					NewAgentTool(ctx, resumeInnerAAgent),
					NewAgentTool(ctx, resumeInnerBAgent),
				},
			},
		},
	})
	require.NoError(t, err)

	runner2 := NewRunner(ctx, RunnerConfig{Agent: resumeOuterAgent, CheckPointStore: store})
	resumeIter, err := runner2.Resume(ctx, checkpointID)
	require.NoError(t, err)

	select {
	case first := <-firstModelCall:
		assert.Contains(t, []string{"innerA", "innerB"}, first,
			"concurrent AgentTools should resume an internal agent before the outer model")
	case <-time.After(5 * time.Second):
		t.Fatal("no model call observed during resume")
	}

	resumeEvents, hasResumeCancelError := drainEvents(resumeIter)
	require.False(t, hasResumeCancelError, "resume should complete without another CancelError")
	assert.NotEmpty(t, resumeEvents)
	assert.Equal(t, int32(1), atomic.LoadInt32(&resumeInnerAModelCallCount))
	assert.Equal(t, int32(1), atomic.LoadInt32(&resumeInnerBModelCallCount))
	assert.Equal(t, int32(1), atomic.LoadInt32(&resumeOuterModelCallCount))
}

// countingChatModel is a chat model that counts calls and records inputs.
// It returns responses from a fixed slice, indexed by call count.
type countingChatModel struct {
	callCount *int32
	inputsCh  chan []*schema.Message // optional: receives a copy of each input
	callCh    chan string            // optional: receives callLabel when Generate is called
	callLabel string
	responses []*schema.Message
}

func (m *countingChatModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	idx := int(atomic.AddInt32(m.callCount, 1)) - 1
	if m.callCh != nil {
		select {
		case m.callCh <- m.callLabel:
		default:
		}
	}
	if m.inputsCh != nil {
		cp := make([]*schema.Message, len(input))
		copy(cp, input)
		select {
		case m.inputsCh <- cp:
		default:
		}
	}
	if idx >= len(m.responses) {
		return nil, fmt.Errorf("countingChatModel: call %d exceeds %d responses (outer model was called too many times - possible restart from beginning)", idx+1, len(m.responses))
	}
	return m.responses[idx], nil
}

func (m *countingChatModel) Stream(_ context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(context.Background(), input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func (m *countingChatModel) BindTools(_ []*schema.ToolInfo) error { return nil }

func messageOutputContent(event *AgentEvent) (string, error) {
	if event.Output == nil || event.Output.MessageOutput == nil {
		return "", nil
	}
	mo := event.Output.MessageOutput
	if mo.IsStreaming {
		msg, err := schema.ConcatMessageStream(mo.MessageStream)
		if err != nil {
			return "", err
		}
		if msg == nil {
			return "", nil
		}
		return msg.Content, nil
	}
	if mo.Message == nil {
		return "", nil
	}
	return mo.Message.Content, nil
}
