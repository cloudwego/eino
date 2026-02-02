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

package compose

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	mockModel "github.com/cloudwego/eino/internal/mock/components/model"
	"github.com/cloudwego/eino/schema"
)

type myInterruptState struct {
	OriginalInput string
}

type myResumeData struct {
	Message string
}

type resumeTestState struct {
	OnStartCalledOnResume bool `json:"on_start_called_on_resume"`
	Counter               int  `json:"counter"`
}

func init() {
	schema.Register[resumeTestState]()
}

func TestInterruptStateAndResumeForRootGraph(t *testing.T) {
	// create a graph with a lambda node
	// this lambda node will interrupt with a typed state and an info for end-user
	// verify the info thrown by the lambda node
	// resume with a structured resume data
	// within the lambda node, getRunCtx and verify the state and resume data
	g := NewGraph[string, string]()

	lambda := InvokableLambda(func(ctx context.Context, input string) (string, error) {
		wasInterrupted, hasState, state := GetInterruptState[*myInterruptState](ctx)
		if !wasInterrupted {
			// First run: interrupt with state
			return "", StatefulInterrupt(ctx,
				map[string]any{"reason": "scheduled maintenance"},
				&myInterruptState{OriginalInput: input},
			)
		}

		// This is a resumed run.
		assert.True(t, hasState)
		assert.Equal(t, "initial input", state.OriginalInput)

		isResume, hasData, data := GetResumeContext[*myResumeData](ctx)
		assert.True(t, isResume)
		assert.True(t, hasData)
		assert.Equal(t, "let's continue", data.Message)

		return "Resumed successfully with input: " + state.OriginalInput, nil
	})

	_ = g.AddLambdaNode("lambda", lambda)
	_ = g.AddEdge(START, "lambda")
	_ = g.AddEdge("lambda", END)

	graph, err := g.Compile(context.Background(), WithCheckPointStore(newInMemoryStore()), WithGraphName("root"))
	assert.NoError(t, err)

	// First invocation, which should be interrupted
	checkPointID := "test-checkpoint-1"
	_, err = graph.Invoke(context.Background(), "initial input", WithCheckPointID(checkPointID))

	// Verify the interrupt error and extracted info
	assert.Error(t, err)
	interruptInfo, isInterrupt := ExtractInterruptInfo(err)
	assert.True(t, isInterrupt)
	assert.NotNil(t, interruptInfo)

	interruptContexts := interruptInfo.InterruptContexts
	assert.Equal(t, 1, len(interruptContexts))
	assert.Equal(t, "runnable:root;node:lambda", interruptContexts[0].Address.String())
	assert.Equal(t, map[string]any{"reason": "scheduled maintenance"}, interruptContexts[0].Info)

	// Prepare resume data
	ctx := ResumeWithData(context.Background(), interruptContexts[0].ID,
		&myResumeData{Message: "let's continue"})

	// Resume execution
	output, err := graph.Invoke(ctx, "", WithCheckPointID(checkPointID))

	// Verify the final result
	assert.NoError(t, err)
	assert.Equal(t, "Resumed successfully with input: initial input", output)
}

func TestProcessStateInOnStartDuringResume(t *testing.T) {
	graphOnStartCallCount := 0
	processStateErrorOnResume := error(nil)

	cb := callbacks.NewHandlerBuilder().
		OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
			if info.Name == "test-process-state-onstart" {
				graphOnStartCallCount++
				err := ProcessState[*resumeTestState](ctx, func(ctx context.Context, s *resumeTestState) error {
					s.Counter++
					return nil
				})
				if graphOnStartCallCount > 1 {
					processStateErrorOnResume = err
				}
			}
			return ctx
		}).
		Build()

	g := NewGraph[string, string](WithGenLocalState(func(ctx context.Context) *resumeTestState {
		return &resumeTestState{}
	}))

	lambda := InvokableLambda(func(ctx context.Context, input string) (string, error) {
		wasInterrupted, _, _ := GetInterruptState[*myInterruptState](ctx)
		if !wasInterrupted {
			return "", StatefulInterrupt(ctx,
				map[string]any{"reason": "test interrupt"},
				&myInterruptState{OriginalInput: input},
			)
		}

		var stateCounter int
		err := ProcessState[*resumeTestState](ctx, func(ctx context.Context, s *resumeTestState) error {
			stateCounter = s.Counter
			return nil
		})
		assert.NoError(t, err)
		assert.Equal(t, 2, stateCounter, "Counter should be 2 (first run OnStart + resume OnStart)")

		return "success", nil
	})

	_ = g.AddLambdaNode("lambda", lambda)
	_ = g.AddEdge(START, "lambda")
	_ = g.AddEdge("lambda", END)

	graph, err := g.Compile(context.Background(),
		WithCheckPointStore(newInMemoryStore()),
		WithGraphName("test-process-state-onstart"),
	)
	assert.NoError(t, err)

	checkPointID := "test-checkpoint-process-state"
	_, err = graph.Invoke(context.Background(), "test input", WithCheckPointID(checkPointID), WithCallbacks(cb))

	assert.Error(t, err, "First invocation should return an error")
	interruptInfo, isInterrupt := ExtractInterruptInfo(err)
	assert.True(t, isInterrupt, "Should be an interrupt error")
	assert.NotNil(t, interruptInfo)
	assert.Equal(t, 1, graphOnStartCallCount, "Graph OnStart should be called once on first run")

	ctx := ResumeWithData(context.Background(), interruptInfo.InterruptContexts[0].ID, &myResumeData{})

	output, err := graph.Invoke(ctx, "", WithCheckPointID(checkPointID), WithCallbacks(cb))
	assert.NoError(t, err)
	assert.Equal(t, "success", output)
	assert.Equal(t, 2, graphOnStartCallCount, "Graph OnStart should be called twice (first run + resume)")
	assert.NoError(t, processStateErrorOnResume, "ProcessState should work in OnStart during resume")
}

func TestInterruptStateAndResumeForSubGraph(t *testing.T) {
	// create a graph
	// create a another graph with a lambda node, as this graph as a sub-graph of the previous graph
	// this lambda node will interrupt with a typed state and an info for end-user
	// verify the info thrown by the lambda node
	// resume with a structured resume data
	// within the lambda node, getRunCtx and verify the state and resume data
	subGraph := NewGraph[string, string]()

	lambda := InvokableLambda(func(ctx context.Context, input string) (string, error) {
		wasInterrupted, hasState, state := GetInterruptState[*myInterruptState](ctx)
		if !wasInterrupted {
			// First run: interrupt with state
			return "", StatefulInterrupt(ctx,
				map[string]any{"reason": "sub-graph maintenance"},
				&myInterruptState{OriginalInput: input},
			)
		}

		// Second (resumed) run
		assert.True(t, hasState)
		assert.Equal(t, "main input", state.OriginalInput)

		isResume, hasData, data := GetResumeContext[*myResumeData](ctx)
		assert.True(t, isResume)
		assert.True(t, hasData)
		assert.Equal(t, "let's continue sub-graph", data.Message)

		return "Sub-graph resumed successfully", nil
	})

	_ = subGraph.AddLambdaNode("inner_lambda", lambda)
	_ = subGraph.AddEdge(START, "inner_lambda")
	_ = subGraph.AddEdge("inner_lambda", END)

	// Create the main graph
	mainGraph := NewGraph[string, string]()
	_ = mainGraph.AddGraphNode("sub_graph_node", subGraph)
	_ = mainGraph.AddEdge(START, "sub_graph_node")
	_ = mainGraph.AddEdge("sub_graph_node", END)

	compiledMainGraph, err := mainGraph.Compile(context.Background(), WithCheckPointStore(newInMemoryStore()))
	assert.NoError(t, err)

	// First invocation, which should be interrupted
	checkPointID := "test-subgraph-checkpoint-1"
	_, err = compiledMainGraph.Invoke(context.Background(), "main input", WithCheckPointID(checkPointID))

	// Verify the interrupt error and extracted info
	assert.Error(t, err)
	interruptInfo, isInterrupt := ExtractInterruptInfo(err)
	assert.True(t, isInterrupt)
	assert.NotNil(t, interruptInfo)

	interruptContexts := interruptInfo.InterruptContexts
	assert.Equal(t, 1, len(interruptContexts))
	assert.Equal(t, "runnable:;node:sub_graph_node;node:inner_lambda", interruptContexts[0].Address.String())
	assert.Equal(t, map[string]any{"reason": "sub-graph maintenance"}, interruptContexts[0].Info)

	// Prepare resume data
	ctx := ResumeWithData(context.Background(), interruptContexts[0].ID,
		&myResumeData{Message: "let's continue sub-graph"})

	// Resume execution
	output, err := compiledMainGraph.Invoke(ctx, "", WithCheckPointID(checkPointID))

	// Verify the final result
	assert.NoError(t, err)
	assert.Equal(t, "Sub-graph resumed successfully", output)
}

func TestInterruptStateAndResumeForToolInNestedSubGraph(t *testing.T) {
	// create a ROOT graph.
	// create a sub graph A, add A to ROOT graph using AddGraphNode.
	// create a sub-sub graph B, add B to A using AddGraphNode.
	// within sub-sub graph B, add a ChatModelNode, which is a Mock chat model that implements the ToolCallingChatModel
	// interface.
	// add a Mock InvokableTool to this mock chat model.
	// within sub-sub graph B, also add a ToolsNode that will execute this Mock InvokableTool.
	// this tool will interrupt with a typed state and an info for end-user
	// verify the info thrown by the tool.
	// resume with a structured resume data.
	// within the Tool, getRunCtx and verify the state and resume data
	ctrl := gomock.NewController(t)

	// 1. Define the interrupting tool
	mockTool := &mockInterruptingTool{tt: t}

	// 2. Define the sub-sub-graph (B)
	subSubGraphB := NewGraph[[]*schema.Message, []*schema.Message]()

	// Mock Chat Model that calls the tool
	mockChatModel := mockModel.NewMockToolCallingChatModel(ctrl)
	mockChatModel.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).Return(&schema.Message{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{
			{ID: "tool_call_123", Function: schema.FunctionCall{Name: "interrupt_tool", Arguments: `{"input": "test"}`}},
		},
	}, nil).AnyTimes()
	mockChatModel.EXPECT().WithTools(gomock.Any()).Return(mockChatModel, nil).AnyTimes()

	toolsNode, err := NewToolNode(context.Background(), &ToolsNodeConfig{Tools: []tool.BaseTool{mockTool}})
	assert.NoError(t, err)

	_ = subSubGraphB.AddChatModelNode("model", mockChatModel)
	_ = subSubGraphB.AddToolsNode("tools", toolsNode)
	_ = subSubGraphB.AddEdge(START, "model")
	_ = subSubGraphB.AddEdge("model", "tools")
	_ = subSubGraphB.AddEdge("tools", END)

	// 3. Define sub-graph (A)
	subGraphA := NewGraph[[]*schema.Message, []*schema.Message]()
	_ = subGraphA.AddGraphNode("sub_graph_b", subSubGraphB)
	_ = subGraphA.AddEdge(START, "sub_graph_b")
	_ = subGraphA.AddEdge("sub_graph_b", END)

	// 4. Define root graph
	rootGraph := NewGraph[[]*schema.Message, []*schema.Message]()
	_ = rootGraph.AddGraphNode("sub_graph_a", subGraphA)
	_ = rootGraph.AddEdge(START, "sub_graph_a")
	_ = rootGraph.AddEdge("sub_graph_a", END)

	// 5. Compile and run
	compiledRootGraph, err := rootGraph.Compile(context.Background(), WithCheckPointStore(newInMemoryStore()),
		WithGraphName("root"))
	assert.NoError(t, err)

	// First invocation - should interrupt
	checkPointID := "test-nested-tool-interrupt"
	initialInput := []*schema.Message{schema.UserMessage("hello")}
	_, err = compiledRootGraph.Invoke(context.Background(), initialInput, WithCheckPointID(checkPointID))

	// 6. Verify the interrupt
	assert.Error(t, err)
	interruptInfo, isInterrupt := ExtractInterruptInfo(err)
	assert.True(t, isInterrupt)
	assert.NotNil(t, interruptInfo)

	interruptContexts := interruptInfo.InterruptContexts
	assert.Len(t, interruptContexts, 1) // Only the root cause is returned

	// Verify the root cause context
	rootCause := interruptContexts[0]
	expectedPath := "runnable:root;node:sub_graph_a;node:sub_graph_b;node:tools;tool:interrupt_tool:tool_call_123"
	assert.Equal(t, expectedPath, rootCause.Address.String())
	assert.True(t, rootCause.IsRootCause)
	assert.Equal(t, map[string]any{"reason": "tool maintenance"}, rootCause.Info)

	// Verify the parent via the Parent field
	assert.NotNil(t, rootCause.Parent)
	assert.Equal(t, "runnable:root;node:sub_graph_a;node:sub_graph_b;node:tools", rootCause.Parent.Address.String())
	assert.False(t, rootCause.Parent.IsRootCause)

	// 7. Resume execution
	ctx := ResumeWithData(context.Background(), rootCause.ID, &myResumeData{Message: "let's continue tool"})
	output, err := compiledRootGraph.Invoke(ctx, initialInput, WithCheckPointID(checkPointID))

	// 8. Verify final result
	assert.NoError(t, err)
	assert.NotNil(t, output)
	assert.Len(t, output, 1)
	assert.Equal(t, "Tool resumed successfully", output[0].Content)
}

const PathSegmentTypeProcess AddressSegmentType = "process"

// processState is the state for a single sub-process in the batch test.
type processState struct {
	Step int
}

// batchState is the composite state for the whole batch lambda.
type batchState struct {
	ProcessStates map[string]*processState
	Results       map[string]string
}

type processResumeData struct {
	Instruction string
}

func init() {
	schema.RegisterName[*myInterruptState]("my_interrupt_state")
	schema.RegisterName[*batchState]("batch_state")
	schema.RegisterName[*processState]("process_state")
}

func TestMultipleInterruptsAndResumes(t *testing.T) {
	// define a new lambda node that act as a 'batch' node
	// it kick starts 3 parallel processes, each will interrupt on first run, while preserving their own state.
	// each of the process should have their own user-facing interrupt info.
	// define a new AddressSegmentType for these sub processes.
	// the lambda should use StatefulInterrupt to interrupt and preserve the state,
	// which is a specific struct type that implements the CompositeInterruptState interface.
	// there should also be a specific struct that that implements the CompositeInterruptInfo interface,
	// which helps the end-user to fetch the nested interrupt info.
	// put this lambda node within a graph and invoke the graph.
	// simulate the user getting the flat list of 3 interrupt points using GetInterruptContexts
	// the user then decides to resume two of the three interrupt points
	// the first resume has resume data, while the second resume does not.(ResumeWithData vs. Resume)
	// verify the resume data and state for the resumed interrupt points.
	processIDs := []string{"p0", "p1", "p2"}

	// This is the logic for a single "process"
	runProcess := func(ctx context.Context, id string) (string, error) {
		// Check if this specific process was interrupted before
		wasInterrupted, hasState, pState := GetInterruptState[*processState](ctx)
		if !wasInterrupted {
			// First run for this process, interrupt it.
			return "", StatefulInterrupt(ctx,
				map[string]any{"reason": "process " + id + " needs input"},
				&processState{Step: 1},
			)
		}

		assert.True(t, hasState)
		assert.Equal(t, 1, pState.Step)

		// Check if we are being resumed
		isResume, hasData, pData := GetResumeContext[*processResumeData](ctx)
		if !isResume {
			// Not being resumed, so interrupt again.
			return "", StatefulInterrupt(ctx,
				map[string]any{"reason": "process " + id + " still needs input"},
				pState,
			)
		}

		// We are being resumed.
		if hasData {
			// Resumed with data
			return "process " + id + " done with instruction: " + pData.Instruction, nil
		}
		// Resumed without data
		return "process " + id + " done", nil
	}

	// This is the main "batch" lambda that orchestrates the processes
	batchLambda := InvokableLambda(func(ctx context.Context, _ string) (map[string]string, error) {
		// Restore the state of the batch node itself
		_, _, persistedBatchState := GetInterruptState[*batchState](ctx)
		if persistedBatchState == nil {
			persistedBatchState = &batchState{
				Results: make(map[string]string),
			}
		}

		var errs []error

		for _, id := range processIDs {
			// If this process already completed in a previous run, skip it.
			if _, done := persistedBatchState.Results[id]; done {
				continue
			}

			// Create a sub-context for each process
			subCtx := AppendAddressSegment(ctx, PathSegmentTypeProcess, id)
			res, err := runProcess(subCtx, id)

			if err != nil {
				_, ok := IsInterruptRerunError(err)
				assert.True(t, ok)
				errs = append(errs, err)
			} else {
				// Process completed, save its result to the state for the next run.
				persistedBatchState.Results[id] = res
			}
		}

		if len(errs) > 0 {
			return nil, CompositeInterrupt(ctx, nil, persistedBatchState, errs...)
		}

		return persistedBatchState.Results, nil
	})

	g := NewGraph[string, map[string]string]()
	_ = g.AddLambdaNode("batch", batchLambda)
	_ = g.AddEdge(START, "batch")
	_ = g.AddEdge("batch", END)

	graph, err := g.Compile(context.Background(), WithCheckPointStore(newInMemoryStore()),
		WithGraphName("root"))
	assert.NoError(t, err)

	// --- 1. First invocation, all 3 processes should interrupt ---
	checkPointID := "multi-interrupt-test"
	_, err = graph.Invoke(context.Background(), "", WithCheckPointID(checkPointID))

	assert.Error(t, err)
	interruptInfo, isInterrupt := ExtractInterruptInfo(err)
	assert.True(t, isInterrupt)
	interruptContexts := interruptInfo.InterruptContexts
	assert.Len(t, interruptContexts, 3) // Only the 3 root causes

	found := make(map[string]bool)
	addrToID := make(map[string]string)
	var parentCtx *InterruptCtx
	for _, iCtx := range interruptContexts {
		addrStr := iCtx.Address.String()
		found[addrStr] = true
		addrToID[addrStr] = iCtx.ID
		assert.True(t, iCtx.IsRootCause)
		assert.Equal(t, map[string]any{"reason": "process " + iCtx.Address[2].ID + " needs input"}, iCtx.Info)
		// Check that all share the same parent
		assert.NotNil(t, iCtx.Parent)
		if parentCtx == nil {
			parentCtx = iCtx.Parent
			assert.Equal(t, "runnable:root;node:batch", parentCtx.Address.String())
			assert.False(t, parentCtx.IsRootCause)
		} else {
			assert.Same(t, parentCtx, iCtx.Parent)
		}
	}
	assert.True(t, found["runnable:root;node:batch;process:p0"])
	assert.True(t, found["runnable:root;node:batch;process:p1"])
	assert.True(t, found["runnable:root;node:batch;process:p2"])

	// --- 2. Second invocation, resume 2 of 3 processes ---
	// Resume p0 with data, and p2 without data. p1 remains interrupted.
	resumeCtx := ResumeWithData(context.Background(), addrToID["runnable:root;node:batch;process:p0"], &processResumeData{Instruction: "do it"})
	resumeCtx = Resume(resumeCtx, addrToID["runnable:root;node:batch;process:p2"])

	_, err = graph.Invoke(resumeCtx, "", WithCheckPointID(checkPointID))

	// Expect an interrupt again, but only for p1
	assert.Error(t, err)
	interruptInfo2, isInterrupt2 := ExtractInterruptInfo(err)
	assert.True(t, isInterrupt2)
	interruptContexts2 := interruptInfo2.InterruptContexts
	assert.Len(t, interruptContexts2, 1) // Only p1 is left
	rootCause2 := interruptContexts2[0]
	assert.Equal(t, "runnable:root;node:batch;process:p1", rootCause2.Address.String())
	assert.NotNil(t, rootCause2.Parent)
	assert.Equal(t, "runnable:root;node:batch", rootCause2.Parent.Address.String())

	// --- 3. Third invocation, resume the last process ---
	finalResumeCtx := Resume(context.Background(), rootCause2.ID)
	finalOutput, err := graph.Invoke(finalResumeCtx, "", WithCheckPointID(checkPointID))

	assert.NoError(t, err)
	assert.Equal(t, "process p0 done with instruction: do it", finalOutput["p0"])
	assert.Equal(t, "process p1 done", finalOutput["p1"])
	assert.Equal(t, "process p2 done", finalOutput["p2"])
}

// toolsNodeResumeTargetCallback captures isResumeTarget for ToolsNode during OnStart
type toolsNodeResumeTargetCallback struct {
	mu                sync.Mutex
	isResumeTargetLog []bool
}

func (c *toolsNodeResumeTargetCallback) OnStart(ctx context.Context, info *callbacks.RunInfo, _ callbacks.CallbackInput) context.Context {
	if info.Component == ComponentOfToolsNode {
		isResumeTarget, _, _ := GetResumeContext[any](ctx)
		c.mu.Lock()
		c.isResumeTargetLog = append(c.isResumeTargetLog, isResumeTarget)
		c.mu.Unlock()
	}
	return ctx
}

func (c *toolsNodeResumeTargetCallback) OnEnd(ctx context.Context, _ *callbacks.RunInfo, _ callbacks.CallbackOutput) context.Context {
	return ctx
}

func (c *toolsNodeResumeTargetCallback) OnError(ctx context.Context, _ *callbacks.RunInfo, _ error) context.Context {
	return ctx
}

func (c *toolsNodeResumeTargetCallback) OnStartWithStreamInput(ctx context.Context, _ *callbacks.RunInfo, input *schema.StreamReader[callbacks.CallbackInput]) context.Context {
	input.Close()
	return ctx
}

func (c *toolsNodeResumeTargetCallback) OnEndWithStreamOutput(ctx context.Context, _ *callbacks.RunInfo, output *schema.StreamReader[callbacks.CallbackOutput]) context.Context {
	output.Close()
	return ctx
}

// mockReentryTool is a helper for the reentry test
type mockReentryTool struct {
	t                     *testing.T
	mu                    sync.Mutex
	isResumeTargetByRunID map[string]bool
}

func (t *mockReentryTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name:        "reentry_tool",
		Desc:        "A tool that can be re-entered in a resumed graph.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{"input": {Type: schema.String}}),
	}, nil
}

func (t *mockReentryTool) InvokableRun(ctx context.Context, _ string, _ ...tool.Option) (string, error) {
	wasInterrupted, hasState, _ := tool.GetInterruptState[any](ctx)
	isResume, hasData, data := tool.GetResumeContext[*myResumeData](ctx)

	callID := GetToolCallID(ctx)

	t.mu.Lock()
	if t.isResumeTargetByRunID != nil {
		t.isResumeTargetByRunID[callID] = isResume
	}
	t.mu.Unlock()

	// Special handling for the re-entrant call to make assertions explicit.
	if callID == "call_3" {
		if !isResume {
			// This is the first run of the re-entrant call. Its context must be clean.
			// This is the core assertion for this test.
			assert.False(t.t, wasInterrupted, "re-entrant call 'call_3' should not have been interrupted on its first run")
			assert.False(t.t, hasState, "re-entrant call 'call_3' should not have state on its first run")
			// Now, interrupt it as part of the test flow.
			return "", tool.StatefulInterrupt(ctx, nil, "some state for "+callID)
		}
		// This is the resumed run of the re-entrant call.
		assert.True(t.t, wasInterrupted, "resumed call 'call_3' must have been interrupted")
		assert.True(t.t, hasData, "resumed call 'call_3' should have data")
		return "Resumed " + data.Message, nil
	}

	// Standard logic for the initial calls (call_1, call_2)
	if !wasInterrupted {
		// First run for call_1 and call_2, should interrupt.
		return "", tool.StatefulInterrupt(ctx, nil, "some state for "+callID)
	}

	// From here, wasInterrupted is true for call_1 and call_2.
	if isResume {
		// The user is explicitly resuming this call.
		assert.True(t.t, hasData, "call %s should have resume data", callID)
		return "Resumed " + data.Message, nil
	}

	// The tool was interrupted before, but is not being resumed now. Re-interrupt.
	return "", tool.StatefulInterrupt(ctx, nil, "some state for "+callID)
}

func TestReentryForResumedTools(t *testing.T) {
	// create a 'ReAct' style graph with a ChatModel node and a ToolsNode.
	// within the ToolsNode there is an interruptible tool that will emit interrupt on first run.
	// During the first invocation of the graph, there should be two tool calls (of the same tool) that interrupt.
	// The user chooses to resume one of the interrupted tool call in second invocation,
	// and this time, the resumed tool call should be successful, while the other should interrupt immediately again.
	// The user then chooses to resume the other interrupted tool call in third invocation,
	// and this time, the ChatModel decides to call the tool again,
	// and this time the tool's runCtx should think it was not interrupted nor resumed.
	ctrl := gomock.NewController(t)

	// 1. Define the interrupting tool and callback
	reentryTool := &mockReentryTool{t: t, isResumeTargetByRunID: make(map[string]bool)}
	toolsNodeCB := &toolsNodeResumeTargetCallback{}

	// 2. Define the graph
	g := NewGraph[[]*schema.Message, *schema.Message]()

	// Mock Chat Model that drives the ReAct loop
	mockChatModel := mockModel.NewMockToolCallingChatModel(ctrl)
	toolsNode, err := NewToolNode(context.Background(), &ToolsNodeConfig{Tools: []tool.BaseTool{reentryTool}})
	assert.NoError(t, err)

	// Expectation for the 1st invocation: model returns two tool calls
	mockChatModel.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).Return(&schema.Message{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{
			{ID: "call_1", Function: schema.FunctionCall{Name: "reentry_tool", Arguments: `{"input": "a"}`}},
			{ID: "call_2", Function: schema.FunctionCall{Name: "reentry_tool", Arguments: `{"input": "b"}`}},
		},
	}, nil).Times(1)

	// Expectation for the 2nd invocation (after resuming call_1): model does nothing, graph continues
	// Expectation for the 3rd invocation (after resuming call_2): model calls the tool again
	mockChatModel.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.Message, error) {
		return &schema.Message{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{
				{ID: "call_3", Function: schema.FunctionCall{Name: "reentry_tool", Arguments: `{"input": "c"}`}},
			},
		}, nil
	}).Times(1)

	// Expectation for the final invocation: model returns final answer
	mockChatModel.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).Return(&schema.Message{
		Role:    schema.Assistant,
		Content: "all done",
	}, nil).Times(1)

	_ = g.AddChatModelNode("model", mockChatModel)
	_ = g.AddToolsNode("tools", toolsNode)
	_ = g.AddEdge(START, "model")

	// Add the crucial branch to decide whether to call tools or end.
	modelBranch := func(ctx context.Context, msg *schema.Message) (string, error) {
		if len(msg.ToolCalls) > 0 {
			return "tools", nil
		}
		return END, nil
	}
	err = g.AddBranch("model", NewGraphBranch(modelBranch, map[string]bool{"tools": true, END: true}))
	assert.NoError(t, err)

	_ = g.AddEdge("tools", "model") // Loop back for ReAct style

	// 3. Compile and run
	graph, err := g.Compile(context.Background(), WithCheckPointStore(newInMemoryStore()),
		WithGraphName("root"))
	assert.NoError(t, err)
	checkPointID := "reentry-test"

	// --- 1. First invocation: call_1 and call_2 should interrupt ---
	_, err = graph.Invoke(context.Background(), []*schema.Message{schema.UserMessage("start")}, WithCheckPointID(checkPointID), WithCallbacks(toolsNodeCB))
	assert.Error(t, err)
	interruptInfo1, _ := ExtractInterruptInfo(err)
	interrupts1 := interruptInfo1.InterruptContexts
	assert.Len(t, interrupts1, 2) // Only the two tool calls
	found1 := make(map[string]bool)
	addrToID1 := make(map[string]string)
	for _, iCtx := range interrupts1 {
		addrStr := iCtx.Address.String()
		found1[addrStr] = true
		addrToID1[addrStr] = iCtx.ID
		assert.True(t, iCtx.IsRootCause)
		assert.NotNil(t, iCtx.Parent)
		assert.Equal(t, "runnable:root;node:tools", iCtx.Parent.Address.String())
	}
	assert.True(t, found1["runnable:root;node:tools;tool:reentry_tool:call_1"])
	assert.True(t, found1["runnable:root;node:tools;tool:reentry_tool:call_2"])

	// First invocation: neither call_1 nor call_2 should be resume targets
	assert.False(t, reentryTool.isResumeTargetByRunID["call_1"], "first run: call_1 should not be resume target")
	assert.False(t, reentryTool.isResumeTargetByRunID["call_2"], "first run: call_2 should not be resume target")

	// First invocation: ToolsNode should NOT be a resume target
	assert.Len(t, toolsNodeCB.isResumeTargetLog, 1, "ToolsNode OnStart should be called once in first invocation")
	assert.False(t, toolsNodeCB.isResumeTargetLog[0], "first run: ToolsNode should NOT be resume target")

	// Clear for next invocation
	reentryTool.isResumeTargetByRunID = make(map[string]bool)
	toolsNodeCB.isResumeTargetLog = nil

	// --- 2. Second invocation: resume call_1, expect call_2 to interrupt again ---
	resumeCtx2 := ResumeWithData(context.Background(), addrToID1["runnable:root;node:tools;tool:reentry_tool:call_1"],
		&myResumeData{Message: "resume call 1"})
	_, err = graph.Invoke(resumeCtx2, []*schema.Message{schema.UserMessage("start")}, WithCheckPointID(checkPointID), WithCallbacks(toolsNodeCB))
	assert.Error(t, err)
	interruptInfo2, _ := ExtractInterruptInfo(err)
	interrupts2 := interruptInfo2.InterruptContexts
	assert.Len(t, interrupts2, 1) // Only call_2
	rootCause2 := interrupts2[0]
	assert.Equal(t, "runnable:root;node:tools;tool:reentry_tool:call_2", rootCause2.Address.String())
	assert.NotNil(t, rootCause2.Parent)
	assert.Equal(t, "runnable:root;node:tools", rootCause2.Parent.Address.String())

	// Second invocation: call_1 is resumed, call_2 is NOT resumed (re-interrupts)
	assert.True(t, reentryTool.isResumeTargetByRunID["call_1"], "second run: call_1 should be resume target")
	assert.False(t, reentryTool.isResumeTargetByRunID["call_2"], "second run: call_2 should NOT be resume target (it re-interrupts)")

	// Second invocation: ToolsNode SHOULD be a resume target (because call_1 child is being resumed)
	assert.Len(t, toolsNodeCB.isResumeTargetLog, 1, "ToolsNode OnStart should be called once in second invocation")
	assert.True(t, toolsNodeCB.isResumeTargetLog[0], "second run: ToolsNode SHOULD be resume target (child call_1 is being resumed)")

	// Clear for next invocation
	reentryTool.isResumeTargetByRunID = make(map[string]bool)
	toolsNodeCB.isResumeTargetLog = nil

	// --- 3. Third invocation: resume call_2, model makes a new call (call_3) which should interrupt ---
	resumeCtx3 := ResumeWithData(context.Background(), rootCause2.ID, &myResumeData{Message: "resume call 2"})
	_, err = graph.Invoke(resumeCtx3, []*schema.Message{schema.UserMessage("start")}, WithCheckPointID(checkPointID), WithCallbacks(toolsNodeCB))
	assert.Error(t, err)
	interruptInfo3, _ := ExtractInterruptInfo(err)
	interrupts3 := interruptInfo3.InterruptContexts
	assert.Len(t, interrupts3, 1) // Only call_3
	rootCause3 := interrupts3[0]
	assert.Equal(t, "runnable:root;node:tools;tool:reentry_tool:call_3", rootCause3.Address.String()) // Note: this is the new call_3
	assert.NotNil(t, rootCause3.Parent)
	assert.Equal(t, "runnable:root;node:tools", rootCause3.Parent.Address.String())

	// Third invocation: call_2 is resumed, call_3 is new (not resumed)
	assert.True(t, reentryTool.isResumeTargetByRunID["call_2"], "third run: call_2 should be resume target")
	assert.False(t, reentryTool.isResumeTargetByRunID["call_3"], "third run: call_3 should NOT be resume target (it's new)")

	// Third invocation: ToolsNode is called twice (once for call_2 resume, once for call_3 new)
	// First call: ToolsNode SHOULD be resume target (call_2 is being resumed)
	// Second call: ToolsNode should NOT be resume target (call_3 is new, no children to resume)
	assert.Len(t, toolsNodeCB.isResumeTargetLog, 2, "ToolsNode OnStart should be called twice in third invocation")
	assert.True(t, toolsNodeCB.isResumeTargetLog[0], "third run first ToolsNode call: SHOULD be resume target (child call_2 is being resumed)")
	assert.False(t, toolsNodeCB.isResumeTargetLog[1], "third run second ToolsNode call: should NOT be resume target (call_3 is new)")

	// Clear for next invocation
	reentryTool.isResumeTargetByRunID = make(map[string]bool)
	toolsNodeCB.isResumeTargetLog = nil

	// --- 4. Final invocation: resume call_3, expect final answer ---
	resumeCtx4 := ResumeWithData(context.Background(), rootCause3.ID,
		&myResumeData{Message: "resume call 3"})
	output, err := graph.Invoke(resumeCtx4, []*schema.Message{schema.UserMessage("start")}, WithCheckPointID(checkPointID), WithCallbacks(toolsNodeCB))
	assert.NoError(t, err)
	assert.Equal(t, "all done", output.Content)

	// Fourth invocation: call_3 is resumed
	assert.True(t, reentryTool.isResumeTargetByRunID["call_3"], "fourth run: call_3 should be resume target")

	// Fourth invocation: ToolsNode SHOULD be resume target (call_3 is being resumed)
	assert.Len(t, toolsNodeCB.isResumeTargetLog, 1, "ToolsNode OnStart should be called once in fourth invocation")
	assert.True(t, toolsNodeCB.isResumeTargetLog[0], "fourth run: ToolsNode SHOULD be resume target (child call_3 is being resumed)")
}

// mockInterruptingTool is a helper for the nested tool interrupt test
type mockInterruptingTool struct {
	tt *testing.T
}

func (t *mockInterruptingTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "interrupt_tool",
		Desc: "A tool that interrupts execution.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"input": {Type: schema.String, Desc: "Some input", Required: true},
		}),
	}, nil
}

func (t *mockInterruptingTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	var args map[string]string
	_ = json.Unmarshal([]byte(argumentsInJSON), &args)

	wasInterrupted, hasState, state := tool.GetInterruptState[*myInterruptState](ctx)
	if !wasInterrupted {
		// First run: interrupt
		return "", tool.StatefulInterrupt(ctx,
			map[string]any{"reason": "tool maintenance"},
			&myInterruptState{OriginalInput: args["input"]},
		)
	}

	// Second (resumed) run
	assert.True(t.tt, hasState)
	assert.Equal(t.tt, "test", state.OriginalInput)

	isResume, hasData, data := tool.GetResumeContext[*myResumeData](ctx)
	assert.True(t.tt, isResume)
	assert.True(t.tt, hasData)
	assert.Equal(t.tt, "let's continue tool", data.Message)

	return "Tool resumed successfully", nil
}

func TestGraphInterruptWithinLambda(t *testing.T) {
	// this test case aims to verify behaviors when a standalone graph is within a lambda,
	// which in turn is within the root graph.
	// the expected behavior is:
	// - internal graph will naturally append to the Address
	// - internal graph interrupts, where the Address includes steps for both the root graph and the internal graph
	// - lambda extracts InterruptInfo, then GetInterruptContexts
	// - lambda then acts as a composite node, uses CompositeInterrupt to pass up the
	//   internal interrupt points
	// - the root graph interrupts
	// - end-user extracts the interrupt ID and related info
	// - end-user uses ResumeWithData to resume the ID
	// - lambda node resumes, invokes the inner graph as usual
	// - the internal graph resumes the interrupted node
	// To implement this test, within the internal graph you can define another lambda node that can interrupt resume.

	// 1. Define the innermost lambda that actually interrupts
	interruptingLambda := InvokableLambda(func(ctx context.Context, input string) (string, error) {
		wasInterrupted, hasState, state := GetInterruptState[*myInterruptState](ctx)
		if !wasInterrupted {
			return "", StatefulInterrupt(ctx, "inner interrupt", &myInterruptState{OriginalInput: input})
		}

		assert.True(t, hasState)
		assert.Equal(t, "top level input", state.OriginalInput)

		isResume, hasData, data := GetResumeContext[*myResumeData](ctx)
		assert.True(t, isResume)
		assert.True(t, hasData)
		assert.Equal(t, "resume inner", data.Message)

		return "inner lambda resumed successfully", nil
	})

	// 2. Define the internal graph that contains the interrupting lambda
	innerGraph := NewGraph[string, string]()
	_ = innerGraph.AddLambdaNode("inner_lambda", interruptingLambda)
	_ = innerGraph.AddEdge(START, "inner_lambda")
	_ = innerGraph.AddEdge("inner_lambda", END)
	// Give the inner graph a name so it can create its "runnable" addr step.
	compiledInnerGraph, err := innerGraph.Compile(context.Background(), WithGraphName("inner"), WithCheckPointStore(newInMemoryStore()))
	assert.NoError(t, err)

	// 3. Define the outer lambda that acts as a composite node
	compositeLambda := InvokableLambda(func(ctx context.Context, input string) (string, error) {
		// The lambda invokes the inner graph. If the inner graph interrupts, this lambda
		// must act as a proper composite node and wrap the error.
		output, err := compiledInnerGraph.Invoke(ctx, input, WithCheckPointID("inner-cp"))
		if err != nil {
			_, isInterrupt := ExtractInterruptInfo(err)
			if !isInterrupt {
				return "", err // Not an interrupt, just fail
			}

			// The composite interrupt itself can be stateless, as it's just a wrapper.
			// It signals to the framework to look inside the subErrs and correctly
			// prepend the current addr to the paths of the inner interrupts.
			return "", CompositeInterrupt(ctx, "composite interrupt from lambda", nil, err)
		}
		return output, nil
	})

	// 4. Define the root graph
	rootGraph := NewGraph[string, string]()
	_ = rootGraph.AddLambdaNode("composite_lambda", compositeLambda)
	_ = rootGraph.AddEdge(START, "composite_lambda")
	_ = rootGraph.AddEdge("composite_lambda", END)
	// Give the root graph a name for its "runnable" addr step.
	compiledRootGraph, err := rootGraph.Compile(context.Background(), WithGraphName("root"), WithCheckPointStore(newInMemoryStore()))
	assert.NoError(t, err)

	// 5. First invocation - should interrupt
	checkPointID := "graph-in-lambda-test"
	_, err = compiledRootGraph.Invoke(context.Background(), "top level input", WithCheckPointID(checkPointID))

	// 6. Verify the interrupt
	assert.Error(t, err)
	interruptInfo, isInterrupt := ExtractInterruptInfo(err)
	assert.True(t, isInterrupt)
	interruptContexts := interruptInfo.InterruptContexts
	assert.Len(t, interruptContexts, 1) // Only the root cause is returned

	// The addr is now fully qualified, including the runnable steps from both graphs.
	rootCause := interruptContexts[0]
	expectedPath := "runnable:root;node:composite_lambda;runnable:inner;node:inner_lambda"
	assert.Equal(t, expectedPath, rootCause.Address.String())
	assert.Equal(t, "inner interrupt", rootCause.Info)
	assert.True(t, rootCause.IsRootCause)

	// Check parent hierarchy
	assert.NotNil(t, rootCause.Parent)
	assert.Equal(t, "runnable:root;node:composite_lambda;runnable:inner", rootCause.Parent.Address.String())
	assert.Nil(t, rootCause.Parent.Info) // The inner runnable doesn't have its own info
	assert.False(t, rootCause.Parent.IsRootCause)

	// Check grandparent
	assert.NotNil(t, rootCause.Parent.Parent)
	assert.Equal(t, "runnable:root;node:composite_lambda", rootCause.Parent.Parent.Address.String())
	assert.Equal(t, "composite interrupt from lambda", rootCause.Parent.Parent.Info)
	assert.False(t, rootCause.Parent.Parent.IsRootCause)

	// 7. Resume execution using the complete, fully-qualified ID
	resumeCtx := ResumeWithData(context.Background(), rootCause.ID, &myResumeData{Message: "resume inner"})
	finalOutput, err := compiledRootGraph.Invoke(resumeCtx, "top level input", WithCheckPointID(checkPointID))

	// 8. Verify final result
	assert.NoError(t, err)
	assert.Equal(t, "inner lambda resumed successfully", finalOutput)
}

func TestLegacyInterrupt(t *testing.T) {
	// this test case aims to test the behavior of the deprecated InterruptAndRerun,
	// NewInterruptAndRerunErr within CompositeInterrupt.
	// Define two sub-processes(functions), one interrupts with InterruptAndRerun,
	// the other interrupts with NewInterruptAndRerunErr.
	// create a lambda as a composite node, within the lambda invokes the two sub-processes.
	// create the graph, add lambda node and invoke it.
	// after verifying the interrupt points, just invokes again without explicit resume.
	// verify the same interrupt IDs again.
	// then finally Resume() the graph.

	// 1. Define the sub-processes that use legacy and modern interrupts
	subProcess1 := func(ctx context.Context) (string, error) {
		isResume, _, data := GetResumeContext[string](ctx)
		if isResume {
			return data, nil
		}
		return "", deprecatedInterruptAndRerun
	}
	subProcess2 := func(ctx context.Context) (string, error) {
		isResume, _, data := GetResumeContext[string](ctx)
		if isResume {
			return data, nil
		}
		return "", deprecatedInterruptAndRerunErr("legacy info")
	}
	subProcess3 := func(ctx context.Context) (string, error) {
		isResume, _, data := GetResumeContext[string](ctx)
		if isResume {
			return data, nil
		}
		// Use the modern, addr-aware interrupt function
		return "", Interrupt(ctx, "modern info")
	}

	// 2. Define the composite lambda
	compositeLambda := InvokableLambda(func(ctx context.Context, input string) (string, error) {
		// If the lambda itself is being resumed, it means the whole process is done.
		isResume, _, data := GetResumeContext[string](ctx)

		// Run sub-processes and collect their errors
		var (
			errs   []error
			outStr string
		)

		const PathStepCustom AddressSegmentType = "custom"
		subCtx1 := AppendAddressSegment(ctx, PathStepCustom, "1")
		out1, err1 := subProcess1(subCtx1)
		if err1 != nil {
			// Wrap the legacy error to give it a addr
			wrappedErr := WrapInterruptAndRerunIfNeeded(ctx, AddressSegment{Type: PathStepCustom, ID: "1"}, err1)
			errs = append(errs, wrappedErr)
		} else {
			outStr += out1
		}
		subCtx2 := AppendAddressSegment(ctx, PathStepCustom, "2")
		out2, err2 := subProcess2(subCtx2)
		if err2 != nil {
			// Wrap the legacy error to give it a addr
			wrappedErr := WrapInterruptAndRerunIfNeeded(ctx, AddressSegment{Type: PathStepCustom, ID: "2"}, err2)
			errs = append(errs, wrappedErr)
		} else {
			outStr += out2
		}
		subCtx3 := AppendAddressSegment(ctx, PathStepCustom, "3")
		out3, err3 := subProcess3(subCtx3)
		if err3 != nil {
			// The error from Interrupt() is already addr-aware. WrapInterruptAndRerunIfNeeded
			// should handle this gracefully and return the error as-is.
			wrappedErr := WrapInterruptAndRerunIfNeeded(ctx, AddressSegment{Type: PathStepCustom, ID: "3"}, err3)
			errs = append(errs, wrappedErr)
		} else {
			outStr += out3
		}

		if len(errs) > 0 {
			// Return a composite interrupt containing the wrapped legacy errors
			return "", CompositeInterrupt(ctx, "legacy composite", nil, errs...)
		}

		if isResume {
			outStr = outStr + " " + data
		}

		return outStr, nil
	})

	// 3. Create and compile the graph
	rootGraph := NewGraph[string, string]()
	_ = rootGraph.AddLambdaNode("legacy_composite", compositeLambda)
	_ = rootGraph.AddEdge(START, "legacy_composite")
	_ = rootGraph.AddEdge("legacy_composite", END)
	compiledGraph, err := rootGraph.Compile(context.Background(), WithGraphName("root"), WithCheckPointStore(newInMemoryStore()))
	assert.NoError(t, err)

	// 4. First invocation - should interrupt
	checkPointID := "legacy-interrupt-test"
	_, err = compiledGraph.Invoke(context.Background(), "input", WithCheckPointID(checkPointID))

	// 5. Verify the three interrupt points
	assert.Error(t, err)
	info, isInterrupt := ExtractInterruptInfo(err)
	assert.True(t, isInterrupt)
	assert.Len(t, info.InterruptContexts, 3) // Only the 3 root causes

	found := make(map[string]any)
	addrToID := make(map[string]string)
	var parentCtx *InterruptCtx
	for _, iCtx := range info.InterruptContexts {
		addrStr := iCtx.Address.String()
		found[addrStr] = iCtx.Info
		addrToID[addrStr] = iCtx.ID
		assert.True(t, iCtx.IsRootCause)
		// Check parent
		assert.NotNil(t, iCtx.Parent)
		if parentCtx == nil {
			parentCtx = iCtx.Parent
			assert.Equal(t, "runnable:root;node:legacy_composite", parentCtx.Address.String())
			assert.Equal(t, "legacy composite", parentCtx.Info)
			assert.False(t, parentCtx.IsRootCause)
		} else {
			assert.Same(t, parentCtx, iCtx.Parent)
		}
	}
	expectedID1 := "runnable:root;node:legacy_composite;custom:1"
	expectedID2 := "runnable:root;node:legacy_composite;custom:2"
	expectedID3 := "runnable:root;node:legacy_composite;custom:3"
	assert.Contains(t, found, expectedID1)
	assert.Nil(t, found[expectedID1]) // From InterruptAndRerun
	assert.Contains(t, found, expectedID2)
	assert.Equal(t, "legacy info", found[expectedID2]) // From NewInterruptAndRerunErr
	assert.Contains(t, found, expectedID3)
	assert.Equal(t, "modern info", found[expectedID3]) // From Interrupt

	// 6. Second invocation (re-run without resume) - should yield the same interrupts
	_, err = compiledGraph.Invoke(context.Background(), "input", WithCheckPointID(checkPointID))
	assert.Error(t, err)
	info2, isInterrupt2 := ExtractInterruptInfo(err)
	assert.True(t, isInterrupt2)
	assert.Len(t, info2.InterruptContexts, 3, "Should have the same number of interrupts on re-run")

	// 7. Third invocation - Resume all three interrupt points with specific data
	resumeData := map[string]any{
		addrToID[expectedID1]: "output1",
		addrToID[expectedID2]: "output2",
		addrToID[expectedID3]: "output3",
	}
	resumeCtx := BatchResumeWithData(context.Background(), resumeData)
	// TODO: The legacy interrupt wrapping does not currently work correctly with BatchResumeWithData.
	// The graph re-interrupts instead of completing. This should be fixed in the core framework.
	_, err = compiledGraph.Invoke(resumeCtx, "input", WithCheckPointID(checkPointID))
	assert.Error(t, err)
}

type wrapperToolForTest struct {
	compiledGraph     Runnable[string, string]
	isResumeTargetLog []bool
}

func (w *wrapperToolForTest) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "wrapperTool",
		Desc: "A tool that wraps a nested graph",
	}, nil
}

func (w *wrapperToolForTest) InvokableRun(ctx context.Context, input string, opts ...tool.Option) (string, error) {
	isResumeTarget, _, _ := tool.GetResumeContext[any](ctx)
	w.isResumeTargetLog = append(w.isResumeTargetLog, isResumeTarget)

	result, err := w.compiledGraph.Invoke(ctx, input)
	if err != nil {
		if _, ok := ExtractInterruptInfo(err); ok {
			return "", tool.CompositeInterrupt(ctx, "wrapper tool interrupt", nil, err)
		}
		return "", err
	}
	return result, nil
}

func TestToolCompositeInterruptWithNestedGraphInterrupt(t *testing.T) {
	ctx := context.Background()

	var innerNodeIsResumeTarget bool
	subSubGraph := NewGraph[string, string]()
	err := subSubGraph.AddLambdaNode("interruptNode", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		wasInterrupted, _, _ := GetInterruptState[any](ctx)
		if !wasInterrupted {
			return "", Interrupt(ctx, "sub-sub graph interrupt info")
		}
		isResumeTarget, _, _ := GetResumeContext[any](ctx)
		innerNodeIsResumeTarget = isResumeTarget
		return "resumed successfully", nil
	}))
	assert.NoError(t, err)
	assert.NoError(t, subSubGraph.AddEdge(START, "interruptNode"))
	assert.NoError(t, subSubGraph.AddEdge("interruptNode", END))

	nestedGraph := NewGraph[string, string]()
	err = nestedGraph.AddGraphNode("subSubGraph", subSubGraph)
	assert.NoError(t, err)
	assert.NoError(t, nestedGraph.AddEdge(START, "subSubGraph"))
	assert.NoError(t, nestedGraph.AddEdge("subSubGraph", END))
	compiledNestedGraph, err := nestedGraph.Compile(ctx)
	assert.NoError(t, err)

	wrapperTool := &wrapperToolForTest{compiledGraph: compiledNestedGraph.(Runnable[string, string])}

	toolsNode, err := NewToolNode(ctx, &ToolsNodeConfig{Tools: []tool.BaseTool{wrapperTool}})
	assert.NoError(t, err)

	outerGraph := NewGraph[*schema.Message, []*schema.Message]()
	err = outerGraph.AddToolsNode("tools", toolsNode)
	assert.NoError(t, err)
	assert.NoError(t, outerGraph.AddEdge(START, "tools"))
	assert.NoError(t, outerGraph.AddEdge("tools", END))

	compiledOuterGraph, err := outerGraph.Compile(ctx, WithCheckPointStore(newInMemoryStore()))
	assert.NoError(t, err)

	checkpointID := "test-wrapper-tool-resume"
	inputMsg := &schema.Message{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{
			{ID: "call_1", Function: schema.FunctionCall{Name: "wrapperTool", Arguments: `"test"`}},
		},
	}

	_, err = compiledOuterGraph.Invoke(ctx, inputMsg, WithCheckPointID(checkpointID))
	assert.Error(t, err)

	info, ok := ExtractInterruptInfo(err)
	assert.True(t, ok, "should be an interrupt error")
	assert.NotNil(t, info)
	assert.NotEmpty(t, info.InterruptContexts)

	rootCause := info.InterruptContexts[0]
	assert.Equal(t, "sub-sub graph interrupt info", rootCause.Info)
	assert.True(t, rootCause.IsRootCause)

	var wrapperToolParent *InterruptCtx
	for p := rootCause.Parent; p != nil; p = p.Parent {
		if p.Info == "wrapper tool interrupt" {
			wrapperToolParent = p
			break
		}
	}
	assert.NotNil(t, wrapperToolParent, "should have parent from wrapper tool with info 'wrapper tool interrupt'")

	assert.Len(t, wrapperTool.isResumeTargetLog, 1)
	assert.False(t, wrapperTool.isResumeTargetLog[0], "first invocation: wrapper tool should not be resume target")

	resumeCtx := Resume(ctx, rootCause.ID)
	_, err = compiledOuterGraph.Invoke(resumeCtx, inputMsg, WithCheckPointID(checkpointID))
	assert.NoError(t, err)

	assert.True(t, innerNodeIsResumeTarget, "inner node should be resume target")

	assert.Len(t, wrapperTool.isResumeTargetLog, 2)
	assert.True(t, wrapperTool.isResumeTargetLog[1], "second invocation: wrapper tool should be resume target because its child is targeted")
}
