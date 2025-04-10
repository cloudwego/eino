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
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/schema"
)

func TestCommonError(t *testing.T) {
	g := NewGraph[string, string]()
	assert.NoError(t, g.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return "", errors.New("my error")
	})))
	assert.NoError(t, g.AddEdge(START, "1"))
	assert.NoError(t, g.AddEdge("1", END))

	ctx := context.Background()
	r, err := g.Compile(ctx)
	assert.NoError(t, err)

	// node error
	_, err = r.Invoke(ctx, "input")
	var ie *internalError
	assert.True(t, errors.As(err, &ie))
	assert.Equal(t, "my error", ie.origError.Error())

	// wrapper error
	sr, sw := schema.Pipe[string](0)
	sw.Close()
	_, err = r.Transform(ctx, sr)
	assert.True(t, errors.As(err, &ie))
	assert.ErrorContains(t, ie.origError, "stream reader is empty, concat fail")
	assert.Equal(t, []string{"1"}, ie.nodePath.path)
	assert.Equal(t, []defaultImplAction{actionTransformByInvoke}, ie.streamWrapperPath)
	println(err.Error())
}

func TestSubGraphNodeError(t *testing.T) {
	subG := NewGraph[string, string]()
	assert.NoError(t, subG.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return "", errors.New("my error")
	})))
	assert.NoError(t, subG.AddEdge(START, "1"))
	assert.NoError(t, subG.AddEdge("1", END))

	g := NewGraph[string, string]()
	assert.NoError(t, g.AddGraphNode("a", subG))
	assert.NoError(t, g.AddEdge(START, "a"))
	assert.NoError(t, g.AddEdge("a", END))

	ctx := context.Background()
	r, err := g.Compile(ctx)
	assert.NoError(t, err)
	_, err = r.Invoke(ctx, "input")
	var ie *internalError
	assert.True(t, errors.As(err, &ie))
	assert.Equal(t, "my error", ie.origError.Error())
	assert.Equal(t, []string{"a", "1"}, ie.nodePath.path)
}
