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

package compose

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/schema"
)

func TestWorkflowArrayFieldMapping(t *testing.T) {
	type numbers [2]int
	t.Run("array", func(t *testing.T) { testWorkflowArrayFieldMapping(t, [2]int{3, 5}) })
	t.Run("named array", func(t *testing.T) { testWorkflowArrayFieldMapping(t, numbers{3, 5}) })
	t.Run("array pointer", func(t *testing.T) { testWorkflowArrayFieldMapping(t, &[2]int{3, 5}) })
	t.Run("empty array", func(t *testing.T) { testWorkflowArrayFieldMapping(t, [0]int{}) })
}

func testWorkflowArrayFieldMapping[O any](t *testing.T, value O) {
	ctx := context.Background()
	input := map[string]any{"values": value}
	wf := NewWorkflow[map[string]any, O]()
	wf.End().AddInput(START, FromField("values"))
	r, err := wf.Compile(ctx)
	if !assert.NoError(t, err) {
		return
	}
	t.Run("invoke", func(t *testing.T) {
		assert.NotPanics(t, func() {
			out, invokeErr := r.Invoke(ctx, input)
			assert.NoError(t, invokeErr)
			assert.Equal(t, value, out)
		})
	})
	t.Run("transform", func(t *testing.T) {
		assert.NotPanics(t, func() {
			out, transformErr := r.Transform(ctx, schema.StreamReaderFromArray([]map[string]any{input}))
			if !assert.NoError(t, transformErr) {
				return
			}
			defer out.Close()
			chunk, recvErr := out.Recv()
			assert.NoError(t, recvErr)
			assert.Equal(t, value, chunk)
			_, recvErr = out.Recv()
			assert.ErrorIs(t, recvErr, io.EOF)
		})
	})
}
