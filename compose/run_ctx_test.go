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
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

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

func init() {
	_ = RegisterSerializableType[*myInterruptState]("my_interrupt_state")
}

func TestInterruptStateAndResumeForRootGraph(t *testing.T) {
	// create a graph with a lambda node
	// this lambda node will interrupt with a typed state and an info for end-user
	// verify the info thrown by the lambda node
	// resume with a structured resume data
	// within the lambda node, getRunCtx and verify the state and resume data
	g := NewGraph[string, string]()

	lambda := InvokableLambda(func(ctx context.Context, input string) (string, error) {
		state, hasState, wasInterrupted := GetInterruptState[*myInterruptState](ctx)
		if !wasInterrupted {
			// First run: interrupt with state
			return "", NewStatefulInterruptAndRerunErr(
				map[string]any{"reason": "scheduled maintenance"},
				&myInterruptState{OriginalInput: input},
			)
		}

		// This is a resumed run.
		assert.True(t, hasState)
		assert.Equal(t, "initial input", state.OriginalInput)

		data, hasData, isResume := GetResumeContext[*myResumeData](ctx)
		assert.True(t, isResume)
		assert.True(t, hasData)
		assert.Equal(t, "let's continue", data.Message)

		return "Resumed successfully with input: " + state.OriginalInput, nil
	})

	_ = g.AddLambdaNode("lambda", lambda)
	_ = g.AddEdge(START, "lambda")
	_ = g.AddEdge("lambda", END)

	graph, err := g.Compile(context.Background(), WithCheckPointStore(newInMemoryStore()))
	assert.NoError(t, err)

	// First invocation, which should be interrupted
	checkPointID := "test-checkpoint-1"
	_, err = graph.Invoke(context.Background(), "initial input", WithCheckPointID(checkPointID))

	// Verify the interrupt error and extracted info
	assert.Error(t, err)
	interruptInfo, isInterrupt := ExtractInterruptInfo(err)
	assert.True(t, isInterrupt)
	assert.NotNil(t, interruptInfo)
	interruptContexts := interruptInfo.GetInterruptContexts()
	assert.Equal(t, 1, len(interruptContexts))
	assert.Equal(t, "node:lambda", interruptContexts[0].ID)
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

func TestInterruptStateAndResumeForSubGraph(t *testing.T) {
	// create a graph
	// create a another graph with a lambda node, as this graph as a sub-graph of the previous graph
	// this lambda node will interrupt with a typed state and an info for end-user
	// verify the info thrown by the lambda node
	// resume with a structured resume data
	// within the lambda node, getRunCtx and verify the state and resume data
	subGraph := NewGraph[string, string]()

	lambda := InvokableLambda(func(ctx context.Context, input string) (string, error) {
		state, hasState, wasInterrupted := GetInterruptState[*myInterruptState](ctx)
		if !wasInterrupted {
			// First run: interrupt with state
			return "", NewStatefulInterruptAndRerunErr(
				map[string]any{"reason": "sub-graph maintenance"},
				&myInterruptState{OriginalInput: input},
			)
		}

		// Second (resumed) run
		assert.True(t, hasState)
		assert.Equal(t, "main input", state.OriginalInput)

		data, hasData, isResume := GetResumeContext[*myResumeData](ctx)
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

	interruptContexts := interruptInfo.GetInterruptContexts()
	assert.Equal(t, 1, len(interruptContexts))
	assert.Equal(t, "node:sub_graph_node;node:inner_lambda", interruptContexts[0].ID)
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
	compiledRootGraph, err := rootGraph.Compile(context.Background(), WithCheckPointStore(newInMemoryStore()))
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

	interruptContexts := interruptInfo.GetInterruptContexts()
	assert.Equal(t, 1, len(interruptContexts))
	expectedPath := "node:sub_graph_a;node:sub_graph_b;node:tools;tool:tool_call_123"
	assert.Equal(t, expectedPath, interruptContexts[0].ID)
	assert.Equal(t, map[string]any{"reason": "tool maintenance"}, interruptContexts[0].Info)

	// 7. Resume execution
	ctx := ResumeWithData(context.Background(), expectedPath, &myResumeData{Message: "let's continue tool"})
	output, err := compiledRootGraph.Invoke(ctx, initialInput, WithCheckPointID(checkPointID))

	// 8. Verify final result
	assert.NoError(t, err)
	assert.NotNil(t, output)
	assert.Len(t, output, 1)
	assert.Equal(t, "Tool resumed successfully", output[0].Content)
}

const PathSegmentTypeProcess PathSegmentType = "process"

// processState is the state for a single sub-process in the batch test.
type processState struct {
	Step int
}

// batchState is the composite state for the whole batch lambda.
type batchState struct {
	ProcessStates map[string]*processState
	Results       map[string]string
}

func (b *batchState) GetSubStateForPath(p PathSegment) (any, bool) {
	if p.Type != PathSegmentTypeProcess {
		return nil, false
	}
	s, ok := b.ProcessStates[p.ID]
	return s, ok
}

func (b *batchState) IsPathInterrupted(p PathSegment) bool {
	if p.Type != PathSegmentTypeProcess {
		return false
	}
	_, ok := b.ProcessStates[p.ID]
	return ok
}

// batchInfo is the composite info for the whole batch lambda.
type batchInfo struct {
	ProcessInfos map[string]any
}

func (b *batchInfo) GetSubInterruptContexts() []*InterruptCtx {
	var ctxs []*InterruptCtx
	for id, info := range b.ProcessInfos {
		ctxs = append(ctxs, &InterruptCtx{
			Path: []PathSegment{{Type: PathSegmentTypeProcess, ID: id}},
			Info: info,
		})
	}
	return ctxs
}

type processResumeData struct {
	Instruction string
}

func init() {
	_ = RegisterSerializableType[*myInterruptState]("my_interrupt_state")
	_ = RegisterSerializableType[*batchState]("batch_state")
	_ = RegisterSerializableType[*processState]("process_state")
}

func TestMultipleInterruptsAndResumes(t *testing.T) {
	// define a new lambda node that act as a 'batch' node
	// it kick starts 3 parallel processes, each will interrupt on first run, while preserving their own state.
	// each of the process should have their own user-facing interrupt info.
	// define a new PathSegmentType for these sub processes.
	// the lambda should use NewStatefulInterruptAndRerunErr to interrupt and preserve the state,
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
		pState, hasState, wasInterrupted := GetInterruptState[*processState](ctx)
		if !wasInterrupted {
			// First run for this process, interrupt it.
			return "", NewStatefulInterruptAndRerunErr(
				map[string]any{"reason": "process " + id + " needs input"},
				&processState{Step: 1},
			)
		}

		assert.True(t, hasState)
		assert.Equal(t, 1, pState.Step)

		// Check if we are being resumed
		pData, hasData, isResume := GetResumeContext[*processResumeData](ctx)
		if !isResume {
			// Not being resumed, so interrupt again.
			return "", NewStatefulInterruptAndRerunErr(
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
		persistedBatchState, _, _ := GetInterruptState[*batchState](ctx)
		if persistedBatchState == nil {
			persistedBatchState = &batchState{
				ProcessStates: make(map[string]*processState),
				Results:       make(map[string]string),
			}
		}

		// This run's state
		childInterruptInfo := &batchInfo{ProcessInfos: make(map[string]any)}
		childInterruptState := &batchState{
			ProcessStates: make(map[string]*processState),
			Results:       persistedBatchState.Results, // Carry over completed results
		}
		anyInterrupted := false

		for _, id := range processIDs {
			// If this process already completed in a previous run, skip it.
			if _, done := childInterruptState.Results[id]; done {
				continue
			}

			// Create a sub-context for each process
			subCtx := SetRunCtx(ctx, PathSegmentTypeProcess, id)
			res, err := runProcess(subCtx, id)

			if err != nil {
				info, state, ok := isInterruptRerunError(err)
				assert.True(t, ok)
				anyInterrupted = true
				childInterruptInfo.ProcessInfos[id] = info
				childInterruptState.ProcessStates[id] = state.(*processState)
			} else {
				// Process completed, save its result to the state for the next run.
				childInterruptState.Results[id] = res
			}
		}

		if anyInterrupted {
			return nil, NewStatefulInterruptAndRerunErr(childInterruptInfo, childInterruptState)
		}

		return childInterruptState.Results, nil
	})

	g := NewGraph[string, map[string]string]()
	_ = g.AddLambdaNode("batch", batchLambda)
	_ = g.AddEdge(START, "batch")
	_ = g.AddEdge("batch", END)

	graph, err := g.Compile(context.Background(), WithCheckPointStore(newInMemoryStore()))
	assert.NoError(t, err)

	// --- 1. First invocation, all 3 processes should interrupt ---
	checkPointID := "multi-interrupt-test"
	_, err = graph.Invoke(context.Background(), "", WithCheckPointID(checkPointID))

	assert.Error(t, err)
	interruptInfo, isInterrupt := ExtractInterruptInfo(err)
	assert.True(t, isInterrupt)
	interruptContexts := interruptInfo.GetInterruptContexts()
	assert.Len(t, interruptContexts, 3)

	// Verify all 3 interrupt points are exposed
	found := make(map[string]bool)
	for _, iCtx := range interruptContexts {
		found[iCtx.ID] = true
		assert.Equal(t, map[string]any{"reason": "process " + iCtx.Path[1].ID + " needs input"}, iCtx.Info)
	}
	assert.True(t, found["node:batch;process:p0"])
	assert.True(t, found["node:batch;process:p1"])
	assert.True(t, found["node:batch;process:p2"])

	// --- 2. Second invocation, resume 2 of 3 processes ---
	// Resume p0 with data, and p2 without data. p1 remains interrupted.
	resumeCtx := ResumeWithData(context.Background(), "node:batch;process:p0", &processResumeData{Instruction: "do it"})
	resumeCtx = Resume(resumeCtx, "node:batch;process:p2")

	_, err = graph.Invoke(resumeCtx, "", WithCheckPointID(checkPointID))

	// Expect an interrupt again, but only for p1
	assert.Error(t, err)
	interruptInfo2, isInterrupt2 := ExtractInterruptInfo(err)
	assert.True(t, isInterrupt2)
	interruptContexts2 := interruptInfo2.GetInterruptContexts()
	assert.Len(t, interruptContexts2, 1)
	assert.Equal(t, "node:batch;process:p1", interruptContexts2[0].ID)

	// --- 3. Third invocation, resume the last process ---
	finalResumeCtx := Resume(context.Background(), "node:batch;process:p1")
	finalOutput, err := graph.Invoke(finalResumeCtx, "", WithCheckPointID(checkPointID))

	assert.NoError(t, err)
	assert.Equal(t, "process p0 done with instruction: do it", finalOutput["p0"])
	assert.Equal(t, "process p1 done", finalOutput["p1"])
	assert.Equal(t, "process p2 done", finalOutput["p2"])
}

// mockReentryTool is a helper for the reentry test
type mockReentryTool struct {
	t *testing.T
}

func (t *mockReentryTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name:        "reentry_tool",
		Desc:        "A tool that can be re-entered in a resumed graph.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{"input": {Type: schema.String}}),
	}, nil
}

func (t *mockReentryTool) InvokableRun(ctx context.Context, _ string, _ ...tool.Option) (string, error) {
	_, hasState, wasInterrupted := GetInterruptState[any](ctx)
	data, hasData, isResume := GetResumeContext[*myResumeData](ctx)

	callID := GetToolCallID(ctx)

	// Special handling for the re-entrant call to make assertions explicit.
	if callID == "call_3" {
		if !isResume {
			// This is the first run of the re-entrant call. Its context must be clean.
			// This is the core assertion for this test.
			assert.False(t.t, wasInterrupted, "re-entrant call 'call_3' should not have been interrupted on its first run")
			assert.False(t.t, hasState, "re-entrant call 'call_3' should not have state on its first run")
			// Now, interrupt it as part of the test flow.
			return "", NewStatefulInterruptAndRerunErr(nil, "some state for "+callID)
		}
		// This is the resumed run of the re-entrant call.
		assert.True(t.t, wasInterrupted, "resumed call 'call_3' must have been interrupted")
		assert.True(t.t, hasData, "resumed call 'call_3' should have data")
		return "Resumed " + data.Message, nil
	}

	// Standard logic for the initial calls (call_1, call_2)
	if !wasInterrupted {
		// First run for call_1 and call_2, should interrupt.
		return "", NewStatefulInterruptAndRerunErr(nil, "some state for "+callID)
	}

	// From here, wasInterrupted is true for call_1 and call_2.
	if isResume {
		// The user is explicitly resuming this call.
		assert.True(t.t, hasData, "call %s should have resume data", callID)
		return "Resumed " + data.Message, nil
	}

	// The tool was interrupted before, but is not being resumed now. Re-interrupt.
	return "", NewStatefulInterruptAndRerunErr(nil, "some state for "+callID)
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

	// 1. Define the interrupting tool
	reentryTool := &mockReentryTool{t: t}

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
	mockChatModel.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).Return(&schema.Message{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{
			{ID: "call_3", Function: schema.FunctionCall{Name: "reentry_tool", Arguments: `{"input": "c"}`}},
		},
	}, nil).Times(1)

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
	graph, err := g.Compile(context.Background(), WithCheckPointStore(newInMemoryStore()))
	assert.NoError(t, err)
	checkPointID := "reentry-test"

	// --- 1. First invocation: call_1 and call_2 should interrupt ---
	_, err = graph.Invoke(context.Background(), []*schema.Message{schema.UserMessage("start")}, WithCheckPointID(checkPointID))
	assert.Error(t, err)
	interruptInfo1, _ := ExtractInterruptInfo(err)
	interrupts1 := interruptInfo1.GetInterruptContexts()
	assert.Len(t, interrupts1, 2)
	assert.Contains(t, []string{interrupts1[0].ID, interrupts1[1].ID}, "node:tools;tool:call_1")
	assert.Contains(t, []string{interrupts1[0].ID, interrupts1[1].ID}, "node:tools;tool:call_2")

	// --- 2. Second invocation: resume call_1, expect call_2 to interrupt again ---
	resumeCtx2 := ResumeWithData(context.Background(), "node:tools;tool:call_1", &myResumeData{Message: "resume call 1"})
	_, err = graph.Invoke(resumeCtx2, []*schema.Message{schema.UserMessage("start")}, WithCheckPointID(checkPointID))
	assert.Error(t, err)
	interruptInfo2, _ := ExtractInterruptInfo(err)
	interrupts2 := interruptInfo2.GetInterruptContexts()
	assert.Len(t, interrupts2, 1)
	assert.Equal(t, "node:tools;tool:call_2", interrupts2[0].ID)

	// --- 3. Third invocation: resume call_2, model makes a new call (call_3) which should interrupt ---
	resumeCtx3 := ResumeWithData(context.Background(), "node:tools;tool:call_2", &myResumeData{Message: "resume call 2"})
	_, err = graph.Invoke(resumeCtx3, []*schema.Message{schema.UserMessage("start")}, WithCheckPointID(checkPointID))
	assert.Error(t, err)
	interruptInfo3, _ := ExtractInterruptInfo(err)
	interrupts3 := interruptInfo3.GetInterruptContexts()
	assert.Len(t, interrupts3, 1)
	assert.Equal(t, "node:tools;tool:call_3", interrupts3[0].ID) // Note: this is the new call_3

	// --- 4. Final invocation: resume call_3, expect final answer ---
	resumeCtx4 := ResumeWithData(context.Background(), "node:tools;tool:call_3", &myResumeData{Message: "resume call 3"})
	output, err := graph.Invoke(resumeCtx4, []*schema.Message{schema.UserMessage("start")}, WithCheckPointID(checkPointID))
	assert.NoError(t, err)
	assert.Equal(t, "all done", output.Content)
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

	state, hasState, wasInterrupted := GetInterruptState[*myInterruptState](ctx)
	if !wasInterrupted {
		// First run: interrupt
		return "", NewStatefulInterruptAndRerunErr(
			map[string]any{"reason": "tool maintenance"},
			&myInterruptState{OriginalInput: args["input"]},
		)
	}

	// Second (resumed) run
	assert.True(t.tt, hasState)
	assert.Equal(t.tt, "test", state.OriginalInput)

	data, hasData, isResume := GetResumeContext[*myResumeData](ctx)
	assert.True(t.tt, isResume)
	assert.True(t.tt, hasData)
	assert.Equal(t.tt, "let's continue tool", data.Message)

	return "Tool resumed successfully", nil
}
