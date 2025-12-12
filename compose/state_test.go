/*
 * Copyright 2024 CloudWeGo Authors
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
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/schema"
)

type midStr string

func TestStateGraphWithEdge(t *testing.T) {

	ctx := context.Background()

	const (
		nodeOfL1 = "invokable"
		nodeOfL2 = "streamable"
		nodeOfL3 = "transformable"
	)

	type testState struct {
		ms []string
	}

	gen := func(ctx context.Context) *testState {
		return &testState{}
	}

	sg := NewGraph[string, string](WithGenLocalState(gen))

	l1 := InvokableLambda(func(ctx context.Context, in string) (out midStr, err error) {
		return midStr("InvokableLambda: " + in), nil
	})

	l1StateToInput := func(ctx context.Context, in string, state *testState) (string, error) {
		state.ms = append(state.ms, in)
		return in, nil
	}

	l1StateToOutput := func(ctx context.Context, out midStr, state *testState) (midStr, error) {
		state.ms = append(state.ms, string(out))
		return out, nil
	}

	err := sg.AddLambdaNode(nodeOfL1, l1,
		WithStatePreHandler(l1StateToInput), WithStatePostHandler(l1StateToOutput))
	assert.NoError(t, err)

	l2 := StreamableLambda(func(ctx context.Context, input midStr) (output *schema.StreamReader[string], err error) {
		outStr := "StreamableLambda: " + string(input)

		sr, sw := schema.Pipe[string](utf8.RuneCountInString(outStr))

		go func() {
			for _, field := range strings.Fields(outStr) {
				sw.Send(field+" ", nil)
			}
			sw.Close()
		}()

		return sr, nil
	})

	l2StateToOutput := func(ctx context.Context, out string, state *testState) (string, error) {
		state.ms = append(state.ms, out)
		return out, nil
	}

	err = sg.AddLambdaNode(nodeOfL2, l2, WithStatePostHandler(l2StateToOutput))
	assert.NoError(t, err)

	l3 := TransformableLambda(func(ctx context.Context, input *schema.StreamReader[string]) (
		output *schema.StreamReader[string], err error) {

		prefix := "TransformableLambda: "
		sr, sw := schema.Pipe[string](20)

		go func() {
			for _, field := range strings.Fields(prefix) {
				sw.Send(field+" ", nil)
			}
			defer input.Close()

			for {
				chunk, err := input.Recv()
				if err != nil {
					if err == io.EOF {
						break
					}
					// TODO: how to trace this kind of error in the goroutine of processing stream
					sw.Send(chunk, err)
					break
				}

				sw.Send(chunk, nil)

			}
			sw.Close()
		}()

		return sr, nil
	})

	l3StateToOutput := func(ctx context.Context, out string, state *testState) (string, error) {
		state.ms = append(state.ms, out)
		assert.Len(t, state.ms, 4)
		return out, nil
	}

	err = sg.AddLambdaNode(nodeOfL3, l3, WithStatePostHandler(l3StateToOutput))
	assert.NoError(t, err)

	err = sg.AddEdge(START, nodeOfL1)
	assert.NoError(t, err)

	err = sg.AddEdge(nodeOfL1, nodeOfL2)
	assert.NoError(t, err)

	err = sg.AddEdge(nodeOfL2, nodeOfL3)
	assert.NoError(t, err)

	err = sg.AddEdge(nodeOfL3, END)
	assert.NoError(t, err)

	run, err := sg.Compile(ctx)
	assert.NoError(t, err)

	out, err := run.Invoke(ctx, "how are you")
	assert.NoError(t, err)
	assert.Equal(t, "TransformableLambda: StreamableLambda: InvokableLambda: how are you ", out)

	stream, err := run.Stream(ctx, "how are you")
	assert.NoError(t, err)
	out, err = concatStreamReader(stream)
	assert.NoError(t, err)
	assert.Equal(t, "TransformableLambda: StreamableLambda: InvokableLambda: how are you ", out)

	sr, sw := schema.Pipe[string](1)
	sw.Send("how are you", nil)
	sw.Close()

	stream, err = run.Transform(ctx, sr)
	assert.NoError(t, err)
	out, err = concatStreamReader(stream)
	assert.NoError(t, err)
	assert.Equal(t, "TransformableLambda: StreamableLambda: InvokableLambda: how are you ", out)
}

func TestStateGraphUtils(t *testing.T) {
	t.Run("getState_success", func(t *testing.T) {
		type testStruct struct {
			UserID int64
		}

		ctx := context.Background()

		ctx = context.WithValue(ctx, stateKey{}, &internalState{
			state: &testStruct{UserID: 10},
		})

		var userID int64
		err := ProcessState[*testStruct](ctx, func(_ context.Context, state *testStruct) error {
			userID = state.UserID
			return nil
		})
		assert.NoError(t, err)
		assert.Equal(t, int64(10), userID)
	})

	t.Run("getState_nil", func(t *testing.T) {
		type testStruct struct {
			UserID int64
		}

		ctx := context.Background()
		ctx = context.WithValue(ctx, stateKey{}, &internalState{})

		err := ProcessState[*testStruct](ctx, func(_ context.Context, state *testStruct) error {
			return nil
		})
		assert.ErrorContains(t, err, "cannot find state with type: *compose.testStruct in states chain, "+
			"current state type: <nil>")
	})

	t.Run("getState_type_error", func(t *testing.T) {
		type testStruct struct {
			UserID int64
		}

		ctx := context.Background()
		ctx = context.WithValue(ctx, stateKey{}, &internalState{
			state: &testStruct{UserID: 10},
		})

		err := ProcessState[string](ctx, func(_ context.Context, state string) error {
			return nil
		})
		assert.ErrorContains(t, err, "cannot find state with type: string in states chain, "+
			"current state type: *compose.testStruct")

	})
}

func TestStateChain(t *testing.T) {
	ctx := context.Background()
	type testState struct {
		Field1 string
		Field2 string
	}
	sc := NewChain[string, string](WithGenLocalState(func(ctx context.Context) (state *testState) {
		return &testState{}
	}))

	r, err := sc.AppendLambda(InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		err = ProcessState[*testState](ctx, func(_ context.Context, state *testState) error {
			state.Field1 = "node1"
			return nil
		})
		if err != nil {
			return "", err
		}
		return input, nil
	}), WithStatePostHandler(func(ctx context.Context, out string, state *testState) (string, error) {
		state.Field2 = "node2"
		return out, nil
	})).
		AppendLambda(InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
			return input, nil
		}), WithStatePreHandler(func(ctx context.Context, in string, state *testState) (string, error) {
			return in + state.Field1 + state.Field2, nil
		})).Compile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.Invoke(ctx, "start")
	if err != nil {
		t.Fatal(err)
	}
	if result != "startnode1node2" {
		t.Fatal("result is unexpected")
	}
}

func TestStreamState(t *testing.T) {
	type testState struct {
		Field1 string
	}
	ctx := context.Background()
	s := &testState{Field1: "1"}
	g := NewGraph[string, string](WithGenLocalState(func(ctx context.Context) (state *testState) { return s }))
	err := g.AddLambdaNode("1", TransformableLambda(func(ctx context.Context, input *schema.StreamReader[string]) (output *schema.StreamReader[string], err error) {
		return input, nil
	}), WithStreamStatePreHandler(func(ctx context.Context, in *schema.StreamReader[string], state *testState) (*schema.StreamReader[string], error) {
		sr, sw := schema.Pipe[string](5)
		for i := 0; i < 5; i++ {
			sw.Send(state.Field1, nil)
		}
		sw.Close()
		return sr, nil
	}), WithStreamStatePostHandler(func(ctx context.Context, in *schema.StreamReader[string], state *testState) (*schema.StreamReader[string], error) {
		ss := in.Copy(2)
		for {
			chunk, err := ss[0].Recv()
			if err == io.EOF {
				return ss[1], nil
			}
			if err != nil {
				return nil, err
			}
			state.Field1 += chunk
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddEdge(START, "1")
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddEdge("1", END)
	if err != nil {
		t.Fatal(err)
	}
	r, err := g.Compile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sr, _ := schema.Pipe[string](1)
	streamResult, err := r.Transform(ctx, sr)
	if err != nil {
		t.Fatal(err)
	}
	if s.Field1 != "111111" {
		t.Fatal("state is unexpected")
	}
	for i := 0; i < 5; i++ {
		chunk, err := streamResult.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if chunk != "1" {
			t.Fatal("result is unexpected")
		}
	}
	_, err = streamResult.Recv()
	if err != io.EOF {
		t.Fatal("result is unexpected")
	}
}

// Nested Graph State Tests

type NestedOuterState struct {
	Value   string
	Counter int
}

type NestedInnerState struct {
	Value string
}

func init() {
	schema.RegisterName[*NestedOuterState]("NestedOuterState")
	schema.RegisterName[*NestedInnerState]("NestedInnerState")
}

func TestNestedGraphStateAccess(t *testing.T) {
	// Test that inner graph can access outer graph's state
	genOuterState := func(ctx context.Context) *NestedOuterState {
		return &NestedOuterState{Value: "outer", Counter: 0}
	}

	genInnerState := func(ctx context.Context) *NestedInnerState {
		return &NestedInnerState{Value: "inner"}
	}

	innerNode := func(ctx context.Context, input string) (string, error) {
		// Access both inner and outer state
		var outerValue string
		err := ProcessState(ctx, func(ctx context.Context, s *NestedOuterState) error {
			outerValue = s.Value
			return nil
		})
		if err != nil {
			return "", err
		}

		var innerValue string
		err = ProcessState(ctx, func(ctx context.Context, s *NestedInnerState) error {
			innerValue = s.Value
			return nil
		})
		if err != nil {
			return "", err
		}

		return fmt.Sprintf("%s_inner=%s_outer=%s", input, innerValue, outerValue), nil
	}

	innerGraph := NewGraph[string, string](WithGenLocalState(genInnerState))
	_ = innerGraph.AddLambdaNode("inner_node", InvokableLambda(innerNode))
	_ = innerGraph.AddEdge(START, "inner_node")
	_ = innerGraph.AddEdge("inner_node", END)

	outerGraph := NewGraph[string, string](WithGenLocalState(genOuterState))
	_ = outerGraph.AddGraphNode("inner_graph", innerGraph)
	_ = outerGraph.AddEdge(START, "inner_graph")
	_ = outerGraph.AddEdge("inner_graph", END)

	r, err := outerGraph.Compile(context.Background())
	assert.NoError(t, err)

	out, err := r.Invoke(context.Background(), "start")
	assert.NoError(t, err)
	assert.Equal(t, "start_inner=inner_outer=outer", out)
}

func TestNestedGraphStateShadowing(t *testing.T) {
	// Test that inner state shadows outer state of the same type (lexical scoping)
	type CommonState struct {
		Value string
	}

	genOuterState := func(ctx context.Context) *CommonState {
		return &CommonState{Value: "outer"}
	}

	genInnerState := func(ctx context.Context) *CommonState {
		return &CommonState{Value: "inner"}
	}

	innerNode := func(ctx context.Context, input string) (string, error) {
		var value string
		err := ProcessState(ctx, func(ctx context.Context, s *CommonState) error {
			// Should see "inner" because inner state shadows outer state
			value = s.Value
			return nil
		})
		if err != nil {
			return "", err
		}
		return input + "_" + value, nil
	}

	innerGraph := NewGraph[string, string](WithGenLocalState(genInnerState))
	_ = innerGraph.AddLambdaNode("inner_node", InvokableLambda(innerNode))
	_ = innerGraph.AddEdge(START, "inner_node")
	_ = innerGraph.AddEdge("inner_node", END)

	outerGraph := NewGraph[string, string](WithGenLocalState(genOuterState))
	_ = outerGraph.AddGraphNode("inner_graph", innerGraph)
	_ = outerGraph.AddEdge(START, "inner_graph")
	_ = outerGraph.AddEdge("inner_graph", END)

	r, err := outerGraph.Compile(context.Background())
	assert.NoError(t, err)

	out, err := r.Invoke(context.Background(), "start")
	assert.NoError(t, err)
	assert.Equal(t, "start_inner", out)
}

func TestNestedGraphStateAfterResume(t *testing.T) {
	// Test that state parent linking works correctly after resume
	// when the outer state is restored from checkpoint (new instance)
	genOuterState := func(ctx context.Context) *NestedOuterState {
		return &NestedOuterState{Value: "outer", Counter: 0}
	}

	genInnerState := func(ctx context.Context) *NestedInnerState {
		return &NestedInnerState{Value: "inner"}
	}

	// Node that modifies outer state
	outerNode := func(ctx context.Context, input string) (string, error) {
		err := ProcessState(ctx, func(ctx context.Context, s *NestedOuterState) error {
			s.Counter = 42
			return nil
		})
		if err != nil {
			return "", err
		}
		return input, nil
	}

	// Inner node that reads outer state
	innerNode := func(ctx context.Context, input string) (string, error) {
		var outerCounter int
		var outerValue string
		err := ProcessState(ctx, func(ctx context.Context, s *NestedOuterState) error {
			// Should see the modified counter value from the restored state
			outerCounter = s.Counter
			outerValue = s.Value
			return nil
		})
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s_counter=%d_value=%s", input, outerCounter, outerValue), nil
	}

	innerGraph := NewGraph[string, string](WithGenLocalState(genInnerState))
	_ = innerGraph.AddLambdaNode("inner_node", InvokableLambda(innerNode))
	_ = innerGraph.AddEdge(START, "inner_node")
	_ = innerGraph.AddEdge("inner_node", END)

	outerGraph := NewGraph[string, string](WithGenLocalState(genOuterState))
	_ = outerGraph.AddLambdaNode("outer_node", InvokableLambda(outerNode))
	_ = outerGraph.AddGraphNode("inner_graph", innerGraph, WithGraphCompileOptions(WithInterruptBeforeNodes([]string{"inner_node"})))
	_ = outerGraph.AddEdge(START, "outer_node")
	_ = outerGraph.AddEdge("outer_node", "inner_graph")
	_ = outerGraph.AddEdge("inner_graph", END)

	store := newInMemoryStore()
	r, err := outerGraph.Compile(context.Background(), WithCheckPointStore(store))
	assert.NoError(t, err)

	// First run - should interrupt after modifying outer state
	_, err = r.Invoke(context.Background(), "start", WithCheckPointID("state_resume_test"))
	assert.Error(t, err)

	// Resume - outer state should be restored with Counter=42
	// Inner graph should link to this restored outer state
	out, err := r.Invoke(context.Background(), "start", WithCheckPointID("state_resume_test"))
	assert.NoError(t, err)
	assert.Equal(t, "start_counter=42_value=outer", out)
}

func TestLambdaNestedGraphStateAccess(t *testing.T) {
	// Test that inner graph invoked from a lambda can access outer graph's state
	// This tests the case: outer graph -> lambda node -> inner graph (using CompositeInterrupt)
	genOuterState := func(ctx context.Context) *NestedOuterState {
		return &NestedOuterState{Value: "outer", Counter: 100}
	}

	genInnerState := func(ctx context.Context) *NestedInnerState {
		return &NestedInnerState{Value: "inner"}
	}

	// Inner node that accesses outer state
	innerNode := func(ctx context.Context, input string) (string, error) {
		var outerValue string
		var outerCounter int
		err := ProcessState(ctx, func(ctx context.Context, s *NestedOuterState) error {
			outerValue = s.Value
			outerCounter = s.Counter
			return nil
		})
		if err != nil {
			return "", err
		}

		var innerValue string
		err = ProcessState(ctx, func(ctx context.Context, s *NestedInnerState) error {
			innerValue = s.Value
			return nil
		})
		if err != nil {
			return "", err
		}

		return fmt.Sprintf("%s_inner=%s_outer=%s_%d", input, innerValue, outerValue, outerCounter), nil
	}

	// Build inner graph
	innerGraph := NewGraph[string, string](WithGenLocalState(genInnerState))
	_ = innerGraph.AddLambdaNode("inner_node", InvokableLambda(innerNode))
	_ = innerGraph.AddEdge(START, "inner_node")
	_ = innerGraph.AddEdge("inner_node", END)

	// Compile inner graph as a standalone runnable
	innerRunnable, err := innerGraph.Compile(context.Background())
	assert.NoError(t, err)

	// Lambda that invokes the inner graph
	lambdaNode := InvokableLambda(func(ctx context.Context, input string) (string, error) {
		// Simply invoke the inner graph - state context is passed through
		return innerRunnable.Invoke(ctx, input)
	})

	// Build outer graph
	outerGraph := NewGraph[string, string](WithGenLocalState(genOuterState))
	_ = outerGraph.AddLambdaNode("lambda_with_graph", lambdaNode)
	_ = outerGraph.AddEdge(START, "lambda_with_graph")
	_ = outerGraph.AddEdge("lambda_with_graph", END)

	r, err := outerGraph.Compile(context.Background())
	assert.NoError(t, err)

	out, err := r.Invoke(context.Background(), "start")
	assert.NoError(t, err)
	assert.Equal(t, "start_inner=inner_outer=outer_100", out)
}

func TestLambdaNestedGraphStateAfterResume(t *testing.T) {
	// Test that state parent linking works correctly after resume
	// in the lambda-nested case (outer graph -> lambda -> inner graph)
	genOuterState := func(ctx context.Context) *NestedOuterState {
		return &NestedOuterState{Value: "outer", Counter: 0}
	}

	genInnerState := func(ctx context.Context) *NestedInnerState {
		return &NestedInnerState{Value: "inner"}
	}

	// Outer node that modifies state
	outerNode := func(ctx context.Context, input string) (string, error) {
		err := ProcessState(ctx, func(ctx context.Context, s *NestedOuterState) error {
			s.Counter = 99
			return nil
		})
		if err != nil {
			return "", err
		}
		return input, nil
	}

	// Inner lambda that interrupts on first run, reads outer state on resume
	innerLambda := InvokableLambda(func(ctx context.Context, input string) (string, error) {
		wasInterrupted, _, _ := GetInterruptState[*NestedInnerState](ctx)
		if !wasInterrupted {
			// First run: interrupt
			return "", StatefulInterrupt(ctx, "inner interrupt", &NestedInnerState{Value: "inner"})
		}

		// Resumed: read outer state
		var outerCounter int
		var outerValue string
		err := ProcessState(ctx, func(ctx context.Context, s *NestedOuterState) error {
			// Should see the modified counter from the restored state
			outerCounter = s.Counter
			outerValue = s.Value
			return nil
		})
		if err != nil {
			return "", err
		}

		return fmt.Sprintf("%s_counter=%d_value=%s", input, outerCounter, outerValue), nil
	})

	// Build inner graph
	innerGraph := NewGraph[string, string](WithGenLocalState(genInnerState))
	_ = innerGraph.AddLambdaNode("inner_lambda", innerLambda)
	_ = innerGraph.AddEdge(START, "inner_lambda")
	_ = innerGraph.AddEdge("inner_lambda", END)

	// Compile inner graph as standalone runnable with checkpoint support
	innerRunnable, err := innerGraph.Compile(context.Background(),
		WithGraphName("inner"),
		WithCheckPointStore(newInMemoryStore()))
	assert.NoError(t, err)

	// Composite lambda that invokes the inner graph and handles interrupts
	compositeLambda := InvokableLambda(func(ctx context.Context, input string) (string, error) {
		output, err := innerRunnable.Invoke(ctx, input, WithCheckPointID("inner-cp"))
		if err != nil {
			_, isInterrupt := ExtractInterruptInfo(err)
			if !isInterrupt {
				return "", err
			}
			// Wrap the interrupt using CompositeInterrupt
			return "", CompositeInterrupt(ctx, "composite interrupt", nil, err)
		}
		return output, nil
	})

	// Build outer graph
	outerGraph := NewGraph[string, string](WithGenLocalState(genOuterState))
	_ = outerGraph.AddLambdaNode("outer_node", InvokableLambda(outerNode))
	_ = outerGraph.AddLambdaNode("composite_lambda", compositeLambda)
	_ = outerGraph.AddEdge(START, "outer_node")
	_ = outerGraph.AddEdge("outer_node", "composite_lambda")
	_ = outerGraph.AddEdge("composite_lambda", END)

	// Compile outer graph
	outerRunnable, err := outerGraph.Compile(context.Background(),
		WithGraphName("root"),
		WithCheckPointStore(newInMemoryStore()))
	assert.NoError(t, err)

	// First run - should interrupt after modifying outer state
	checkPointID := "lambda_state_resume_test"
	_, err = outerRunnable.Invoke(context.Background(), "start", WithCheckPointID(checkPointID))
	assert.Error(t, err)

	interruptInfo, isInterrupt := ExtractInterruptInfo(err)
	assert.True(t, isInterrupt)

	// Resume - outer state should be restored with Counter=99
	// Inner lambda should link to this restored outer state
	ctx := ResumeWithData(context.Background(), interruptInfo.InterruptContexts[0].ID, nil)
	out, err := outerRunnable.Invoke(ctx, "start", WithCheckPointID(checkPointID))
	assert.NoError(t, err)

	// Verify the inner lambda saw the modified counter from the restored outer state
	assert.Contains(t, out, "counter=99")
	assert.Contains(t, out, "value=outer")
}

func TestNestedGraphStateConcurrency(t *testing.T) {
	// Test that concurrent access to parent and child states uses correct locks
	// This verifies that ProcessState properly locks the parent state's mutex when accessing it
	genOuterState := func(ctx context.Context) *NestedOuterState {
		return &NestedOuterState{Value: "outer", Counter: 0}
	}

	genInnerState := func(ctx context.Context) *NestedInnerState {
		return &NestedInnerState{Value: "inner"}
	}

	// Inner node that concurrently modifies both outer and inner state
	innerNode := func(ctx context.Context, input string) (string, error) {
		var wg sync.WaitGroup
		errors := make(chan error, 20)

		// Launch 10 goroutines that modify outer state
		// If locks don't work correctly, we'll see race conditions
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				err := ProcessState(ctx, func(ctx context.Context, s *NestedOuterState) error {
					// ProcessState should hold the parent's lock during this entire function
					current := s.Counter
					time.Sleep(time.Millisecond) // Simulate work
					s.Counter = current + 1
					return nil
				})
				if err != nil {
					errors <- err
				}
			}()
		}

		// Launch 10 goroutines that modify inner state
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				err := ProcessState(ctx, func(ctx context.Context, s *NestedInnerState) error {
					// This uses the inner state's own lock
					return nil
				})
				if err != nil {
					errors <- err
				}
			}()
		}

		wg.Wait()
		close(errors)

		// Check for errors
		for err := range errors {
			return "", err
		}

		return input, nil
	}

	innerGraph := NewGraph[string, string](WithGenLocalState(genInnerState))
	_ = innerGraph.AddLambdaNode("inner_node", InvokableLambda(innerNode))
	_ = innerGraph.AddEdge(START, "inner_node")
	_ = innerGraph.AddEdge("inner_node", END)

	outerGraph := NewGraph[string, string](WithGenLocalState(genOuterState))
	_ = outerGraph.AddGraphNode("inner_graph", innerGraph)
	_ = outerGraph.AddEdge(START, "inner_graph")
	_ = outerGraph.AddEdge("inner_graph", END)

	r, err := outerGraph.Compile(context.Background())
	assert.NoError(t, err)

	_, err = r.Invoke(context.Background(), "start")
	assert.NoError(t, err)

	// Note: This test is primarily validated by running with -race flag
	// If locks don't work correctly, the race detector will catch it
}
