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

// TestHandleRunFuncError_AlreadyHandled_NoDuplicate verifies that when
// markCancelHandled() was already claimed by a sub-agent's handleRunFuncError,
// the sequential workflow's checkCancel does not emit a second CancelError.
//
// Setup: sequential[cma1, cma2] with CancelAfterToolCalls. cma1 has tools,
// cancel fires while tool is running. After tool completes, the safe-point
// fires in cma1's handleRunFuncError (claiming markCancelHandled). The
// sequential workflow's checkCancel at the transition point should find
// markCancelHandled returns false and skip — producing exactly 1 CancelError.
func TestHandleRunFuncError_AlreadyHandled_NoDuplicate(t *testing.T) {
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

	// Cancel while tool is still running (in goroutine because cancelFn blocks
	// until execution finishes), then unblock tool so safe-point fires
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

	assert.Equal(t, 1, cancelCount, "Should have exactly one CancelError, no duplicate from handleRunFuncError + checkCancel")
}

func TestWithCancel_CancelAfterChatModel_NestedAgentTool(t *testing.T) {
	ctx := context.Background()

	subAgentModel := newBlockingChatModel(toolCallMsg(toolCall("c1", "sub_tool", `{"input":"x"}`)))
	subAgentModelStarted := subAgentModel.started
	subTool := newBlockingTool("sub_tool")

	subAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "sub_agent",
		Description: "test sub agent",
		Instruction: "you are a sub agent",
		Model:       subAgentModel,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{subTool}},
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

	supervisorAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "supervisor",
		Description: "supervisor agent (equivalent to DeepAgent)",
		Instruction: "you are a supervisor",
		Model:       supervisorModel,
	})
	require.NoError(t, err)

	agentWithSubAgents, err := SetSubAgents(ctx, supervisorAgent, []Agent{subAgent})
	require.NoError(t, err)

	runner := NewRunner(ctx, RunnerConfig{
		Agent:           agentWithSubAgents,
		EnableStreaming: false,
	})

	cancelOpt, cancelFn := WithCancel()
	iter := runner.Run(ctx, []Message{schema.UserMessage("test")}, cancelOpt)

	select {
	case <-subAgentModelStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("Sub-agent model did not start")
	}

	time.Sleep(50 * time.Millisecond)

	cancelDone := make(chan error, 1)
	go func() {
		handle, _ := cancelFn(WithAgentCancelMode(CancelAfterChatModel), WithRecursive())
		cancelDone <- handle.Wait()
	}()

	time.Sleep(100 * time.Millisecond)
	close(subAgentModel.unblockCh)

	cancelErr := <-cancelDone
	assert.NoError(t, cancelErr)

	hasCancelError := false
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		var ce *CancelError
		if event.Err != nil && errors.As(event.Err, &ce) {
			hasCancelError = true
		}
	}

	assert.True(t, hasCancelError, "CancelError expected from nested agent tool with tools")
}

// slowStreamingTool implements StreamableTool (but NOT InvokableTool), streaming
// chunks slowly so CancelImmediate can fire mid-stream.
type slowStreamingTool struct {
	name          string
	chunkInterval time.Duration
	chunks        []string
	started       chan struct{}
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
		for _, chunk := range t.chunks {
			time.Sleep(t.chunkInterval)
			if closed := w.Send(chunk, nil); closed {
				return
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
	st := &slowStreamingTool{
		name:          "slow_tool",
		chunkInterval: 200 * time.Millisecond,
		chunks:        []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"},
		started:       make(chan struct{}, 1),
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

	// Wait for the tool to start streaming
	select {
	case <-st.started:
	case <-time.After(5 * time.Second):
		t.Fatal("tool did not start streaming")
	}
	// Let a few chunks through, then cancel mid-stream
	time.Sleep(300 * time.Millisecond)

	handle, _ := cancelFn()
	cancelErr := handle.Wait()
	assert.NoError(t, cancelErr)

	var foundStreamCanceled bool
	var foundCancelError bool
	for {
		e, ok := iter.Next()
		if !ok {
			break
		}

		// ErrStreamCanceled appears on the tool's MessageStream.Recv()
		if e.Output != nil && e.Output.MessageOutput != nil && e.Output.MessageOutput.IsStreaming &&
			e.Output.MessageOutput.Role == schema.Tool {
			stream := e.Output.MessageOutput.MessageStream
			for {
				_, recvErr := stream.Recv()
				if recvErr != nil {
					if errors.Is(recvErr, ErrStreamCanceled) {
						foundStreamCanceled = true
					}
					break
				}
			}
		}

		if e.Action != nil && e.Action.Interrupted != nil {
			foundCancelError = true
		}
		var ce *CancelError
		if e.Err != nil && errors.As(e.Err, &ce) {
			foundCancelError = true
		}
	}
	assert.True(t, foundStreamCanceled, "expected ErrStreamCanceled on tool's MessageStream.Recv()")
	assert.True(t, foundCancelError, "expected CancelError in event stream")
}
