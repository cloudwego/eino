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
	"io"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/schema"
)

func TestMultiBranch(t *testing.T) {
	g := NewGraph[string, map[string]any]()
	emptyLambda := InvokableLambda(func(ctx context.Context, input string) (output string, err error) { return input, nil })
	err := g.AddLambdaNode("1", emptyLambda, WithOutputKey("1"))
	assert.NoError(t, err)
	err = g.AddLambdaNode("2", emptyLambda, WithOutputKey("2"))
	assert.NoError(t, err)
	err = g.AddLambdaNode("3", emptyLambda, WithOutputKey("3"))
	assert.NoError(t, err)

	err = g.AddBranch(START, NewGraphMultiBranch(func(ctx context.Context, in string) (endNode map[string]bool, err error) {
		return map[string]bool{"1": true, "2": true}, nil
	}, map[string]bool{"1": true, "2": true, "3": true}))
	assert.NoError(t, err)

	err = g.AddEdge("1", END)
	assert.NoError(t, err)
	err = g.AddEdge("2", END)
	assert.NoError(t, err)
	err = g.AddEdge("3", END)
	assert.NoError(t, err)

	ctx := context.Background()
	r, err := g.Compile(ctx)
	assert.NoError(t, err)

	result, err := r.Invoke(ctx, "start")
	assert.NoError(t, err)
	assert.Equal(t, map[string]any{
		"1": "start",
		"2": "start",
	}, result)

	streamResult, err := r.Stream(ctx, "start")
	assert.NoError(t, err)
	result = map[string]any{}
	for {
		chunk, err := streamResult.Recv()
		if err == io.EOF {
			break
		}
		assert.NoError(t, err)
		for k, v := range chunk {
			result[k] = v
		}
	}
	assert.Equal(t, map[string]any{
		"1": "start",
		"2": "start",
	}, result)
}

func TestStreamMultiBranch(t *testing.T) {
	g := NewGraph[string, map[string]any]()
	emptyLambda := InvokableLambda(func(ctx context.Context, input string) (output string, err error) { return input, nil })
	err := g.AddLambdaNode("1", emptyLambda, WithOutputKey("1"))
	assert.NoError(t, err)
	err = g.AddLambdaNode("2", emptyLambda, WithOutputKey("2"))
	assert.NoError(t, err)
	err = g.AddLambdaNode("3", emptyLambda, WithOutputKey("3"))
	assert.NoError(t, err)

	err = g.AddBranch(START, NewStreamGraphMultiBranch(func(ctx context.Context, in *schema.StreamReader[string]) (endNode map[string]bool, err error) {
		in.Close()
		return map[string]bool{"1": true, "2": true}, nil
	}, map[string]bool{"1": true, "2": true, "3": true}))
	assert.NoError(t, err)

	err = g.AddEdge("1", END)
	assert.NoError(t, err)
	err = g.AddEdge("2", END)
	assert.NoError(t, err)
	err = g.AddEdge("3", END)
	assert.NoError(t, err)

	ctx := context.Background()
	r, err := g.Compile(ctx)
	assert.NoError(t, err)

	result, err := r.Invoke(ctx, "start")
	assert.NoError(t, err)
	assert.Equal(t, map[string]any{
		"1": "start",
		"2": "start",
	}, result)

	streamResult, err := r.Stream(ctx, "start")
	assert.NoError(t, err)
	result = map[string]any{}
	for {
		chunk, err := streamResult.Recv()
		if err == io.EOF {
			break
		}
		assert.NoError(t, err)
		for k, v := range chunk {
			result[k] = v
		}
	}
	assert.Equal(t, map[string]any{
		"1": "start",
		"2": "start",
	}, result)
}
