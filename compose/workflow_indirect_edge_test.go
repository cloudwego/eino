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
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIndirectEdgeIllegal(t *testing.T) {
	t.Run("no path from source to target", func(t *testing.T) {
		// Graph: START -> A, START -> B
		// B declares noDirectDependency on A, but there is no control path A -> B.
		wf := NewWorkflow[string, map[string]any]()

		wf.AddLambdaNode("A", InvokableLambda(func(ctx context.Context, in string) (output string, err error) {
			return in + "_A", nil
		})).AddInput(START)

		wf.AddLambdaNode("B", InvokableLambda(func(ctx context.Context, in map[string]string) (output string, err error) {
			return in["A"] + "_" + in[START], nil
		})).AddInput(START, ToField(START)).
			AddInputWithOptions("A", []*FieldMapping{ToField("A")}, WithNoDirectDependency())

		wf.End().AddInput("B", ToField("B"))

		_, err := wf.Compile(context.Background())
		assert.Error(t, err)
		assert.ErrorContains(t, err, "illegal indirect edge")
	})

	t.Run("disconnected nodes", func(t *testing.T) {
		// Graph: START -> A, START -> B, B -> END
		// END declares noDirectDependency on A, but A has no path to END (A is disconnected).
		wf := NewWorkflow[string, map[string]any]()

		wf.AddLambdaNode("A", InvokableLambda(func(ctx context.Context, in string) (output string, err error) {
			return in + "_A", nil
		})).AddInput(START)

		wf.AddLambdaNode("B", InvokableLambda(func(ctx context.Context, in string) (output string, err error) {
			return in + "_B", nil
		})).AddInput(START)

		wf.End().AddInput("B", ToField("B")).
			AddInputWithOptions("A", []*FieldMapping{ToField("A")}, WithNoDirectDependency())

		_, err := wf.Compile(context.Background())
		assert.Error(t, err)
		assert.ErrorContains(t, err, "illegal indirect edge")
	})

	t.Run("valid indirect edge through branch", func(t *testing.T) {
		// Graph: START -> branch_node, branch -> A, branch -> END
		// A declares noDirectDependency on START, which is valid because START -> branch_node -> A.
		wf := NewWorkflow[map[string]any, map[string]any]()

		wf.AddLambdaNode("A", InvokableLambda(func(ctx context.Context, in map[string]any) (output map[string]any, err error) {
			return in, nil
		})).AddInputWithOptions(START, nil, WithNoDirectDependency())

		wf.AddPassthroughNode("branch_node").AddInput(START)

		wf.AddBranch("branch_node", NewGraphBranch(func(ctx context.Context, in map[string]any) (string, error) {
			return "A", nil
		}, map[string]bool{"A": true, END: true}))

		wf.End().AddInput("A", ToField("A"))

		_, err := wf.Compile(context.Background())
		assert.NoError(t, err)
	})

	t.Run("valid indirect edge through chain", func(t *testing.T) {
		// Graph: START -> A -> B -> END
		// END declares noDirectDependency on A, which is valid because A -> B -> END.
		wf := NewWorkflow[string, map[string]any]()

		wf.AddLambdaNode("A", InvokableLambda(func(ctx context.Context, in string) (output string, err error) {
			return in + "_A", nil
		})).AddInput(START)

		wf.AddLambdaNode("B", InvokableLambda(func(ctx context.Context, in string) (output string, err error) {
			return in + "_B", nil
		})).AddInput("A")

		wf.End().AddInput("B", ToField("B")).
			AddInputWithOptions("A", []*FieldMapping{ToField("A")}, WithNoDirectDependency())

		_, err := wf.Compile(context.Background())
		assert.NoError(t, err)
	})
}
