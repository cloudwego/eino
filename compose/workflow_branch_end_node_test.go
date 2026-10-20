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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkflowCompileBranchEndNodeNotAdded(t *testing.T) {
	ctx := context.Background()

	t.Run("missing end node reports an error", func(t *testing.T) {
		wf := NewWorkflow[string, string]()
		wf.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, in string) (string, error) {
			return in, nil
		})).AddInput(START)
		wf.AddBranch("1", NewGraphBranch(func(ctx context.Context, in string) (string, error) {
			return "2", nil
		}, map[string]bool{"2": true}))
		wf.End().AddInput("1")

		_, err := wf.Compile(ctx)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "branch end node '2' needs to be added to graph first")
	})

	t.Run("end node added after the branch still compiles", func(t *testing.T) {
		wf := NewWorkflow[string, string]()
		wf.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, in string) (string, error) {
			return in, nil
		})).AddInput(START)
		wf.AddBranch("1", NewGraphBranch(func(ctx context.Context, in string) (string, error) {
			return "2", nil
		}, map[string]bool{"2": true}))
		wf.AddLambdaNode("2", InvokableLambda(func(ctx context.Context, in string) (string, error) {
			return in + "!", nil
		})).AddInput("1")
		wf.End().AddInput("2")

		r, err := wf.Compile(ctx)
		require.NoError(t, err)
		out, err := r.Invoke(ctx, "hi")
		require.NoError(t, err)
		assert.Equal(t, "hi!", out)
	})

	t.Run("END as branch target still compiles", func(t *testing.T) {
		wf := NewWorkflow[string, string]()
		wf.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, in string) (string, error) {
			return in, nil
		})).AddInput(START)
		wf.AddBranch("1", NewGraphBranch(func(ctx context.Context, in string) (string, error) {
			return END, nil
		}, map[string]bool{END: true}))
		wf.End().AddInput("1")

		r, err := wf.Compile(ctx)
		require.NoError(t, err)
		out, err := r.Invoke(ctx, "hi")
		require.NoError(t, err)
		assert.Equal(t, "hi", out)
	})
}
