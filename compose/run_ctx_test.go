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

	"github.com/cloudwego/eino/components/tool"
	mockModel "github.com/cloudwego/eino/internal/mock/components/model"
	"github.com/cloudwego/eino/schema"
)

type memoryCheckPointStore struct {
	data map[string][]byte
	mu   sync.Mutex
}

func (s *memoryCheckPointStore) Get(_ context.Context, checkPointID string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.data[checkPointID]
	return d, ok, nil
}

func (s *memoryCheckPointStore) Set(_ context.Context, checkPointID string, checkPoint []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data == nil {
		s.data = make(map[string][]byte)
	}
	s.data[checkPointID] = checkPoint
	return nil
}

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
	// within the lambda node, GetRunCtx and verify the state and resume data
	g := NewGraph[string, string]()

	lambda := InvokableLambda(func(ctx context.Context, input string) (string, error) {
		rCtx, ok := GetRunCtx(ctx)
		assert.True(t, ok)

		if rCtx.InterruptData == nil || !rCtx.InterruptData.Interrupted {
			// First run: interrupt with state
			assert.Nil(t, rCtx.ResumeData)

			// The "info" is for the end-user, the "state" is for resumption.
			return "", NewInterruptAndRerunErrWithState(
				map[string]any{"reason": "scheduled maintenance"},
				&myInterruptState{OriginalInput: input},
			)
		}

		// Second (resumed) run
		assert.NotNil(t, rCtx.InterruptData)
		assert.True(t, rCtx.InterruptData.Interrupted)

		// Verify the state from the first run
		persistedState, ok := rCtx.InterruptData.State.(*myInterruptState)
		assert.True(t, ok)
		assert.Equal(t, "initial input", persistedState.OriginalInput)

		// Verify the resume data passed in the Resume() call
		resumeData, ok := rCtx.ResumeData.(*myResumeData)
		assert.True(t, ok)
		assert.Equal(t, "let's continue", resumeData.Message)

		return "Resumed successfully with input: " + persistedState.OriginalInput, nil
	})

	_ = g.AddLambdaNode("lambda", lambda)
	_ = g.AddEdge(START, "lambda")
	_ = g.AddEdge("lambda", END)

	store := &memoryCheckPointStore{}
	graph, err := g.Compile(context.Background(), WithCheckPointStore(store))
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
	ctx := SetResumeInfo(context.Background(), interruptContexts[0].ID,
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
	// within the lambda node, GetRunCtx and verify the state and resume data
	subGraph := NewGraph[string, string]()

	lambda := InvokableLambda(func(ctx context.Context, input string) (string, error) {
		rCtx, ok := GetRunCtx(ctx)
		assert.True(t, ok)

		if rCtx.InterruptData == nil || !rCtx.InterruptData.Interrupted {
			// First run: interrupt with state
			assert.Nil(t, rCtx.ResumeData)
			return "", NewInterruptAndRerunErrWithState(
				map[string]any{"reason": "sub-graph maintenance"},
				&myInterruptState{OriginalInput: input},
			)
		}

		// Second (resumed) run
		assert.NotNil(t, rCtx.InterruptData)
		assert.True(t, rCtx.InterruptData.Interrupted)

		// Verify the state from the first run
		persistedState, ok := rCtx.InterruptData.State.(*myInterruptState)
		assert.True(t, ok)
		assert.Equal(t, "main input", persistedState.OriginalInput)

		// Verify the resume data passed in the Resume() call
		resumeData, ok := rCtx.ResumeData.(*myResumeData)
		assert.True(t, ok)
		assert.Equal(t, "let's continue sub-graph", resumeData.Message)

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

	store := &memoryCheckPointStore{}
	compiledMainGraph, err := mainGraph.Compile(context.Background(), WithCheckPointStore(store))
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
	ctx := SetResumeInfo(context.Background(), interruptContexts[0].ID,
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
	// within the Tool, GetRunCtx and verify the state and resume data
	ctrl := gomock.NewController(t)

	// 1. Define the interrupting tool
	mockTool := &mockInterruptingTool{}

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
	store := &memoryCheckPointStore{}
	compiledRootGraph, err := rootGraph.Compile(context.Background(), WithCheckPointStore(store))
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
	ctx := SetResumeInfo(context.Background(), expectedPath, &myResumeData{Message: "let's continue tool"})
	output, err := compiledRootGraph.Invoke(ctx, initialInput, WithCheckPointID(checkPointID))

	// 8. Verify final result
	assert.NoError(t, err)
	assert.NotNil(t, output)
	assert.Len(t, output, 1)
	assert.Equal(t, "Tool resumed successfully", output[0].Content)
}

// mockInterruptingTool is a helper for the nested tool interrupt test
type mockInterruptingTool struct{}

func (t *mockInterruptingTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "interrupt_tool",
		Desc: "A tool that interrupts execution.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"input": {Type: schema.String, Desc: "Some input", Required: true},
		}),
	}, nil
}

func (t *mockInterruptingTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	rCtx, ok := GetRunCtx(ctx)
	if !ok {
		return "", assert.AnError
	}

	var args map[string]string
	_ = json.Unmarshal([]byte(argumentsInJSON), &args)

	if rCtx.InterruptData == nil || !rCtx.InterruptData.Interrupted {
		// First run: interrupt
		return "", NewInterruptAndRerunErrWithState(
			map[string]any{"reason": "tool maintenance"},
			&myInterruptState{OriginalInput: args["input"]},
		)
	}

	// Second (resumed) run
	persistedState, ok := rCtx.InterruptData.State.(*myInterruptState)
	if !ok {
		return "", assert.AnError
	}
	if persistedState.OriginalInput != "test" {
		return "", assert.AnError
	}

	resumeData, ok := rCtx.ResumeData.(*myResumeData)
	if !ok {
		return "", assert.AnError
	}
	if resumeData.Message != "let's continue tool" {
		return "", assert.AnError
	}

	return "Tool resumed successfully", nil
}
