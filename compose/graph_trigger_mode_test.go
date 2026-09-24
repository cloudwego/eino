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
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

// buildTriggerModeProbeGraph builds the following graph:
//
//	START -> x -> z -> END
//	START -> a -> y -> z
//
// x fires in super step 1, y fires in super step 2, so z's two predecessors
// finish in different super steps.
func buildTriggerModeProbeGraph(t *testing.T, zOpts ...GraphAddNodeOpt) (*Graph[string, string], *int, *[][]string) {
	var mu sync.Mutex
	zRuns := 0
	var zInputs [][]string

	g := NewGraph[string, string]()

	err := g.AddLambdaNode("x", InvokableLambda(func(ctx context.Context, input string) (map[string]any, error) {
		return map[string]any{"x": input}, nil
	}))
	assert.NoError(t, err)

	err = g.AddLambdaNode("a", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		return input, nil
	}))
	assert.NoError(t, err)

	err = g.AddLambdaNode("y", InvokableLambda(func(ctx context.Context, input string) (map[string]any, error) {
		return map[string]any{"y": input}, nil
	}))
	assert.NoError(t, err)

	err = g.AddLambdaNode("z", InvokableLambda(func(ctx context.Context, input map[string]any) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		zRuns++
		keys := make([]string, 0, len(input))
		for k := range input {
			keys = append(keys, k)
		}
		zInputs = append(zInputs, keys)
		return fmt.Sprintf("%v", input), nil
	}), zOpts...)
	assert.NoError(t, err)

	assert.NoError(t, g.AddEdge(START, "x"))
	assert.NoError(t, g.AddEdge(START, "a"))
	assert.NoError(t, g.AddEdge("a", "y"))
	assert.NoError(t, g.AddEdge("x", "z"))
	assert.NoError(t, g.AddEdge("y", "z"))
	assert.NoError(t, g.AddEdge("z", END))

	return g, &zRuns, &zInputs
}

func TestPerNodeTriggerModeDefaultIsAnyPredecessor(t *testing.T) {
	// Without WithTriggerMode, z keeps pregel semantics: it triggers as soon as
	// its first predecessor (x, super step 1) finishes. y finishes one super
	// step later, but by then END is already reachable through z, so the run
	// ends and y's value never reaches z.
	g, zRuns, zInputs := buildTriggerModeProbeGraph(t)

	r, err := g.Compile(context.Background())
	assert.NoError(t, err)

	out, err := r.Invoke(context.Background(), "hello")
	assert.NoError(t, err)
	assert.Equal(t, 1, *zRuns)
	assert.ElementsMatch(t, []string{"x"}, (*zInputs)[0])
	assert.Contains(t, out, "x")
	assert.NotContains(t, out, "y")
}

func TestPerNodeTriggerModeAllPredecessor(t *testing.T) {
	// With WithTriggerMode(AllPredecessor), z waits for both predecessors and
	// triggers exactly once with both values merged, even though they finish
	// in different super steps.
	g, zRuns, zInputs := buildTriggerModeProbeGraph(t, WithTriggerMode(AllPredecessor))

	r, err := g.Compile(context.Background())
	assert.NoError(t, err)

	out, err := r.Invoke(context.Background(), "hello")
	assert.NoError(t, err)
	assert.Equal(t, 1, *zRuns)
	assert.ElementsMatch(t, []string{"x", "y"}, (*zInputs)[0])
	assert.Contains(t, out, "x")
	assert.Contains(t, out, "y")

	// stream mode goes through the same channel machinery.
	*zRuns = 0
	*zInputs = nil
	sr, err := r.Stream(context.Background(), "hello")
	assert.NoError(t, err)
	for {
		_, err = sr.Recv()
		if err != nil {
			break
		}
	}
	sr.Close()
	assert.Equal(t, 1, *zRuns)
	assert.ElementsMatch(t, []string{"x", "y"}, (*zInputs)[0])
}

func TestPerNodeTriggerModeAllPredecessorWithBranchSkip(t *testing.T) {
	// An AllPredecessor node whose other predecessor (b) is skipped by a branch
	// still triggers, with only the completed predecessors' values. The skip
	// report must propagate through b's own any-predecessor channel to reach z.
	//
	// Topology: START -> x -> z -> END
	//           START -> branch -> b (skipped) -> z
	//                            -> nowhere -> z
	var zRuns int
	var zInputKeys []string

	g := NewGraph[string, string]()
	assert.NoError(t, g.AddLambdaNode("x", InvokableLambda(func(ctx context.Context, input string) (map[string]any, error) {
		return map[string]any{"x": input}, nil
	})))
	assert.NoError(t, g.AddLambdaNode("b", InvokableLambda(func(ctx context.Context, input string) (map[string]any, error) {
		return map[string]any{"b": input}, nil
	})))
	assert.NoError(t, g.AddLambdaNode("nowhere", InvokableLambda(func(ctx context.Context, input string) (map[string]any, error) {
		return map[string]any{"nw": input}, nil
	})))
	assert.NoError(t, g.AddLambdaNode("z", InvokableLambda(func(ctx context.Context, input map[string]any) (string, error) {
		zRuns++
		for k := range input {
			zInputKeys = append(zInputKeys, k)
		}
		return "done", nil
	}), WithTriggerMode(AllPredecessor)))

	assert.NoError(t, g.AddEdge(START, "x"))
	assert.NoError(t, g.AddEdge("x", "z"))
	assert.NoError(t, g.AddBranch(START, NewGraphBranch(func(ctx context.Context, in string) (string, error) {
		return "nowhere", nil // never selects "b", so "b" is skipped
	}, map[string]bool{"b": true, "nowhere": true})))
	assert.NoError(t, g.AddEdge("b", "z"))
	assert.NoError(t, g.AddEdge("nowhere", "z"))
	assert.NoError(t, g.AddEdge("z", END))

	r, err := g.Compile(context.Background())
	assert.NoError(t, err)

	out, err := r.Invoke(context.Background(), "hello")
	assert.NoError(t, err)
	assert.Equal(t, "done", out)
	assert.Equal(t, 1, zRuns)
	assert.ElementsMatch(t, []string{"x", "nw"}, zInputKeys)
}

func TestPerNodeTriggerModeWithCheckpointResume(t *testing.T) {
	// Hybrid channels (pregel + per-node dag channel) survive a checkpoint
	// round trip: interrupt before z, resume, and z still fires exactly once
	// with both predecessors' values.
	g, zRuns, zInputs := buildTriggerModeProbeGraph(t, WithTriggerMode(AllPredecessor))

	ctx := context.Background()
	r, err := g.Compile(ctx,
		WithCheckPointStore(newInMemoryStore()),
		WithInterruptBeforeNodes([]string{"z"}),
	)
	assert.NoError(t, err)

	_, err = r.Invoke(ctx, "hello", WithCheckPointID("cp-1"))
	assert.Error(t, err) // interrupted before z
	assert.Equal(t, 0, *zRuns)

	info, ok := ExtractInterruptInfo(err)
	assert.True(t, ok)

	rCtx := Resume(ctx, info.InterruptContexts[0].ID)
	out, err := r.Invoke(rCtx, "hello", WithCheckPointID("cp-1"))
	assert.NoError(t, err)
	assert.Contains(t, out, "x")
	assert.Contains(t, out, "y")
	assert.Equal(t, 1, *zRuns)
	assert.ElementsMatch(t, []string{"x", "y"}, (*zInputs)[0])
}

func TestPerNodeTriggerModeCompileErrors(t *testing.T) {
	t.Run("chain rejects per-node trigger mode", func(t *testing.T) {
		c := NewChain[string, string]()
		c.AppendLambda(InvokableLambda(func(ctx context.Context, input string) (string, error) {
			return input, nil
		}), WithTriggerMode(AllPredecessor))

		_, err := c.Compile(context.Background())
		assert.ErrorContains(t, err, "doesn't support per-node trigger mode option")
	})

	t.Run("dag graph rejects AnyPredecessor override", func(t *testing.T) {
		g, _, _ := buildTriggerModeProbeGraph(t, WithTriggerMode(AnyPredecessor))

		_, err := g.Compile(context.Background(), WithNodeTriggerMode(AllPredecessor))
		assert.ErrorContains(t, err, "not supported in DAG run mode")
	})

	t.Run("dag graph accepts redundant AllPredecessor override", func(t *testing.T) {
		g, zRuns, _ := buildTriggerModeProbeGraph(t, WithTriggerMode(AllPredecessor))

		r, err := g.Compile(context.Background(), WithNodeTriggerMode(AllPredecessor))
		assert.NoError(t, err)

		_, err = r.Invoke(context.Background(), "hello")
		assert.NoError(t, err)
		assert.Equal(t, 1, *zRuns)
	})

	t.Run("invalid trigger mode", func(t *testing.T) {
		g := NewGraph[string, string]()
		assert.NoError(t, g.AddLambdaNode("n", InvokableLambda(func(ctx context.Context, input string) (string, error) {
			return input, nil
		})))
		err := g.AddLambdaNode("bad", InvokableLambda(func(ctx context.Context, input string) (string, error) {
			return input, nil
		}), WithTriggerMode("not_a_mode"))
		assert.ErrorContains(t, err, "invalid trigger mode")

		_, err = g.Compile(context.Background())
		assert.ErrorContains(t, err, "invalid trigger mode")
	})
}
