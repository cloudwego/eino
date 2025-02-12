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
	"io"
	"strings"
	"testing"
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
		assert.ErrorContains(t, err, "unexpected state type. expected: *compose.testStruct, got: <nil>")
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
		assert.ErrorContains(t, err, "unexpected state type. expected: string, got: *compose.testStruct")

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
