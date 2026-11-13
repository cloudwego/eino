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
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/internal/core"
	"github.com/cloudwego/eino/schema"
)

// This gate is observed by the runner's errors.As, rather than by the node
// returning its interrupt. Use only with eager InvokableLambda and a plain
// context: external cancellation can inspect errors in the task manager too.
type localObservedInterrupt struct {
	err     error
	observe func()
}

func (e *localObservedInterrupt) Error() string { return e.err.Error() }
func (e *localObservedInterrupt) Unwrap() error { return e.err }
func (e *localObservedInterrupt) As(target any) bool {
	if _, ok := target.(**core.InterruptSignal); ok {
		e.observe()
	}
	return false
}

func localGate(t *testing.T) (<-chan struct{}, func()) {
	t.Helper()
	ch := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(ch) }) }
	t.Cleanup(release)
	return ch, release
}

func localAwait(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Error("timed out waiting for test gate")
	}
}

type localCounts struct {
	mu                 sync.Mutex
	entered, completed map[string]int
}

func newLocalCounts() *localCounts {
	return &localCounts{entered: make(map[string]int), completed: make(map[string]int)}
}
func (c *localCounts) enter(key string)    { c.mu.Lock(); defer c.mu.Unlock(); c.entered[key]++ }
func (c *localCounts) complete(key string) { c.mu.Lock(); defer c.mu.Unlock(); c.completed[key]++ }
func (c *localCounts) counts(key string) (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.entered[key], c.completed[key]
}
func localCount(t *testing.T, c *localCounts, key string, entered, completed int) {
	t.Helper()
	e, d := c.counts(key)
	require.Equal(t, entered, e, "%s entries", key)
	require.Equal(t, completed, d, "%s completions", key)
}

func localInfo(t *testing.T, err error) *InterruptInfo {
	t.Helper()
	info, ok := ExtractInterruptInfo(err)
	require.True(t, ok, "expected interrupt, got %v", err)
	return info
}

func localIDs(info *InterruptInfo) map[string]string {
	ids := make(map[string]string)
	for _, point := range info.InterruptContexts {
		if point.IsRootCause {
			ids[point.Address[len(point.Address)-1].ID] = point.ID
		}
	}
	return ids
}

// START -> A, prepare; prepare -> B,C; A,B,C -> join -> END.
// A must be collected before prepare finishes. Non-eager tests deliberately
// use no gate, since waitAll must finish prepare before classifying A.
func localCrossDAG(t *testing.T, local, eager, implicit bool, store CheckPointStore, extra ...GraphCompileOption) (Runnable[map[string]any, map[string]any], *localCounts) {
	t.Helper()
	counts := newLocalCounts()
	observed, observe := localGate(t)
	g := NewGraph[map[string]any, map[string]any]()
	for _, name := range []string{"A", "B", "C"} {
		key := name
		require.NoError(t, g.AddLambdaNode(key, InvokableLambda(func(ctx context.Context, in map[string]any) (map[string]any, error) {
			counts.enter(key)
			was, has, state := GetInterruptState[string](ctx)
			target, _, _ := GetResumeContext[any](ctx)
			if !was || (!implicit && !target) {
				if !has {
					state = "saved-" + key
				}
				err := StatefulInterrupt(ctx, key, state)
				if key == "A" && eager {
					err = &localObservedInterrupt{err: err, observe: observe}
				}
				return nil, err
			}
			require.True(t, has)
			require.Equal(t, "saved-"+key, state)
			counts.complete(key)
			return map[string]any{key: state}, nil
		})))
	}
	require.NoError(t, g.AddLambdaNode("prepare", InvokableLambda(func(ctx context.Context, in map[string]any) (map[string]any, error) {
		counts.enter("prepare")
		if eager {
			localAwait(t, observed)
		}
		counts.complete("prepare")
		return in, nil
	})))
	require.NoError(t, g.AddLambdaNode("join", InvokableLambda(func(ctx context.Context, in map[string]any) (map[string]any, error) {
		counts.enter("join")
		counts.complete("join")
		return in, nil
	})))
	for _, edge := range [][2]string{{START, "A"}, {START, "prepare"}, {"prepare", "B"}, {"prepare", "C"}, {"A", "join"}, {"B", "join"}, {"C", "join"}, {"join", END}} {
		require.NoError(t, g.AddEdge(edge[0], edge[1]))
	}
	opts := []GraphCompileOption{WithNodeTriggerMode(AllPredecessor), WithCheckPointStore(store)}
	if local {
		opts = append(opts, WithLocalInterrupts())
	}
	if !eager {
		opts = append(opts, WithEagerExecutionDisabled())
	}
	opts = append(opts, extra...)
	r, err := g.Compile(context.Background(), opts...)
	require.NoError(t, err)
	return r, counts
}

func TestLocalInterruptCrossDAG(t *testing.T) {
	for _, eager := range []bool{true, false} {
		for _, local := range []bool{false, true} {
			t.Run(fmt.Sprintf("local=%v/eager=%v", local, eager), func(t *testing.T) {
				r, counts := localCrossDAG(t, local, eager, false, newInMemoryStore())
				_, err := r.Invoke(context.Background(), map[string]any{"input": "value"}, WithCheckPointID("cross"))
				ids := localIDs(localInfo(t, err))
				if local {
					require.Len(t, ids, 3)
					localCount(t, counts, "B", 1, 0)
					localCount(t, counts, "C", 1, 0)
				} else {
					require.Len(t, ids, 1)
					localCount(t, counts, "B", 0, 0)
					localCount(t, counts, "C", 0, 0)
				}
				require.Contains(t, ids, "A")
				localCount(t, counts, "A", 1, 0)
				localCount(t, counts, "prepare", 1, 1)
				localCount(t, counts, "join", 0, 0)
			})
		}
	}
}

func TestLocalInterruptWorkflowSkipAndDataDependencies(t *testing.T) {
	for _, eager := range []bool{true, false} {
		t.Run(fmt.Sprint(eager), func(t *testing.T) {
			counts := newLocalCounts()
			w := NewWorkflow[string, map[string]any]()
			w.AddLambdaNode("wait", InvokableLambda(func(ctx context.Context, in string) (string, error) {
				counts.enter("wait")
				if target, _, _ := GetResumeContext[any](ctx); !target {
					return "", StatefulInterrupt(ctx, "wait", in)
				}
				_, _, saved := GetInterruptState[string](ctx)
				return saved, nil
			})).AddInput(START)
			w.AddLambdaNode("keep", InvokableLambda(func(ctx context.Context, in string) (string, error) {
				counts.enter("keep")
				return in + "!", nil
			})).AddInputWithOptions(START, nil, WithNoDirectDependency())
			w.AddLambdaNode("skip", InvokableLambda(func(ctx context.Context, in string) (string, error) {
				counts.enter("skip")
				return in, nil
			})).AddInputWithOptions(START, nil, WithNoDirectDependency())
			w.AddBranch(START, NewGraphBranch(func(ctx context.Context, in string) (string, error) {
				counts.enter("branch")
				return "keep", nil
			}, map[string]bool{"keep": true, "skip": true}))
			w.AddLambdaNode("join", InvokableLambda(func(ctx context.Context, in map[string]any) (map[string]any, error) {
				counts.enter("join")
				return in, nil
			})).
				AddInputWithOptions("wait", []*FieldMapping{ToField("approval")}, WithNoDirectDependency()).
				AddInput("keep", ToField("kept")).AddInput("skip", ToField("skipped"))
			w.End().AddInput("join")
			opts := []GraphCompileOption{WithLocalInterrupts(), WithCheckPointStore(newInMemoryStore())}
			if !eager {
				opts = append(opts, WithEagerExecutionDisabled())
			}
			r, err := w.Compile(context.Background(), opts...)
			require.NoError(t, err)
			_, err = r.Invoke(context.Background(), "original", WithCheckPointID("skip"))
			info := localInfo(t, err)
			localCount(t, counts, "keep", 1, 0)
			localCount(t, counts, "skip", 0, 0)
			localCount(t, counts, "join", 0, 0)
			out, err := r.Invoke(Resume(context.Background(), localIDs(info)["wait"]), "", WithCheckPointID("skip"))
			require.NoError(t, err)
			require.Equal(t, map[string]any{"approval": "original", "kept": "original!"}, out)
			localCount(t, counts, "branch", 1, 0)
			localCount(t, counts, "keep", 1, 0)
			localCount(t, counts, "skip", 0, 0)
			localCount(t, counts, "join", 1, 0)
		})
	}
}

func TestLocalInterruptSupportMatrix(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []NodeTriggerMode{"", AnyPredecessor, AllPredecessor} {
		t.Run(string(mode), func(t *testing.T) {
			g := NewGraph[string, string]()
			require.NoError(t, g.AddEdge(START, END))
			opts := []GraphCompileOption{WithLocalInterrupts()}
			if mode != "" {
				opts = append(opts, WithNodeTriggerMode(mode))
			}
			r, err := g.Compile(ctx, opts...)
			if mode == AllPredecessor {
				require.NoError(t, err)
				out, err := r.Invoke(ctx, "direct")
				require.NoError(t, err)
				require.Equal(t, "direct", out)
			} else {
				require.ErrorContains(t, err, "requires Workflow or AllPredecessor")
			}
		})
	}
	c := NewChain[string, string]().AppendLambda(InvokableLambda(func(ctx context.Context, in string) (string, error) { return in, nil }))
	_, err := c.Compile(ctx, WithLocalInterrupts())
	require.ErrorContains(t, err, "requires Workflow or AllPredecessor")
	w := NewWorkflow[string, string]()
	w.End().AddInput(START)
	r, err := w.Compile(ctx, WithLocalInterrupts())
	require.NoError(t, err)
	out, err := r.Invoke(ctx, "direct")
	require.NoError(t, err)
	require.Equal(t, "direct", out)
}

func TestLocalInterruptStreamingRejectedBeforeEffects(t *testing.T) {
	for _, method := range []string{"Stream", "Collect", "Transform"} {
		t.Run(method, func(t *testing.T) {
			counts := newLocalCounts()
			store := newInMemoryStore()
			g := NewGraph[string, string](WithGenLocalState(func(ctx context.Context) *testStruct {
				counts.enter("state")
				return &testStruct{}
			}))
			require.NoError(t, g.AddLambdaNode("node", InvokableLambda(func(ctx context.Context, in string) (string, error) {
				counts.enter("node")
				return in, nil
			})))
			require.NoError(t, g.AddEdge(START, "node"))
			require.NoError(t, g.AddEdge("node", END))
			r, err := g.Compile(context.Background(), WithNodeTriggerMode(AllPredecessor), WithLocalInterrupts(), WithCheckPointStore(store))
			require.NoError(t, err)
			stream := schema.StreamReaderWithConvert(schema.StreamReaderFromArray([]string{"input"}), func(in string) (string, error) {
				counts.enter("read")
				return in, nil
			})
			defer stream.Close()
			switch method {
			case "Stream":
				_, err = r.Stream(context.Background(), "input", WithCheckPointID("stream"))
			case "Collect":
				_, err = r.Collect(context.Background(), stream, WithCheckPointID("stream"))
			case "Transform":
				_, err = r.Transform(context.Background(), stream, WithCheckPointID("stream"))
			}
			require.ErrorContains(t, err, "does not support graph-level streaming")
			for _, key := range []string{"state", "node", "read"} {
				localCount(t, counts, key, 0, 0)
			}
			require.Empty(t, store.m)
		})
	}
}

func TestLocalInterruptStreamingComponentInInvoke(t *testing.T) {
	g := NewGraph[string, string]()
	require.NoError(t, g.AddLambdaNode("stream", StreamableLambda(func(ctx context.Context, in string) (*schema.StreamReader[string], error) {
		return schema.StreamReaderFromArray([]string{in, "!"}), nil
	})))
	require.NoError(t, g.AddEdge(START, "stream"))
	require.NoError(t, g.AddEdge("stream", END))
	r, err := g.Compile(context.Background(), WithNodeTriggerMode(AllPredecessor), WithLocalInterrupts())
	require.NoError(t, err)
	out, err := r.Invoke(context.Background(), "ok")
	require.NoError(t, err)
	require.Equal(t, "ok!", out)
}

func TestLocalInterruptEarlyEndAndConversion(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(fmt.Sprint(invalid), func(t *testing.T) {
			store := newInMemoryStore()
			counts := newLocalCounts()
			g := NewGraph[string, string]()
			g.handlerPreNode[END] = []handlerPair{{invoke: func(value any) (any, error) {
				counts.enter("terminal-conversion")
				return value, nil
			}}}
			require.NoError(t, g.AddLambdaNode("early", InvokableLambda(func(ctx context.Context, in string) (any, error) {
				counts.enter("early")
				if invalid {
					return 123, nil
				}
				return in, nil
			})))
			require.NoError(t, g.AddLambdaNode("wait", InvokableLambda(func(ctx context.Context, in string) (string, error) {
				if target, _, _ := GetResumeContext[any](ctx); !target {
					return "", Interrupt(ctx, "wait")
				}
				return in, nil
			})))
			require.NoError(t, g.AddLambdaNode("sink", InvokableLambda(func(ctx context.Context, in string) (string, error) { return in, nil })))
			for _, edge := range [][2]string{{START, "early"}, {START, "wait"}, {"early", END}, {"wait", "sink"}} {
				require.NoError(t, g.AddEdge(edge[0], edge[1]))
			}
			r, err := g.Compile(context.Background(), WithNodeTriggerMode(AllPredecessor), WithLocalInterrupts(), WithCheckPointStore(store))
			require.NoError(t, err)
			_, err = r.Invoke(context.Background(), "terminal", WithCheckPointID("end"))
			ids := localIDs(localInfo(t, err))
			require.Len(t, ids, 1)
			localCount(t, counts, "terminal-conversion", 0, 0)
			out, err := r.Invoke(Resume(context.Background(), ids["wait"]), "", WithCheckPointID("end"))
			if invalid {
				require.Error(t, err)
				_, ok := ExtractInterruptInfo(err)
				require.False(t, ok)
			} else {
				require.NoError(t, err)
				require.Equal(t, "terminal", out)
				localCount(t, counts, "terminal-conversion", 1, 0)
			}
			localCount(t, counts, "early", 1, 0)
		})
	}
}

func TestLocalInterruptErrorsTakePrecedence(t *testing.T) {
	sentinel := errors.New("business failure")
	for _, failure := range []string{"error", "cancel", "panic", "post-error", "post-interrupt"} {
		t.Run(failure, func(t *testing.T) {
			observed, observe := localGate(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			counts := newLocalCounts()
			g := NewGraph[string, string](WithGenLocalState(func(context.Context) *testStruct { return &testStruct{} }))
			require.NoError(t, g.AddLambdaNode("wait", InvokableLambda(func(ctx context.Context, in string) (string, error) {
				return "", &localObservedInterrupt{err: Interrupt(ctx, "wait"), observe: observe}
			})))
			var nodeOpts []GraphAddNodeOpt
			if failure == "post-error" || failure == "post-interrupt" {
				nodeOpts = append(nodeOpts, WithStatePostHandler(func(ctx context.Context, out string, state *testStruct) (string, error) {
					if failure == "post-interrupt" {
						return "", Interrupt(ctx, "post")
					}
					return "", sentinel
				}))
			}
			require.NoError(t, g.AddLambdaNode("fail", InvokableLambda(func(ctx context.Context, in string) (string, error) {
				localAwait(t, observed)
				switch failure {
				case "error":
					return "", sentinel
				case "cancel":
					cancel()
					return "", ctx.Err()
				case "panic":
					panic("node panic")
				}
				return in, nil
			}), nodeOpts...))
			require.NoError(t, g.AddLambdaNode("successor", InvokableLambda(func(ctx context.Context, in string) (string, error) { counts.enter("successor"); return in, nil })))
			for _, edge := range [][2]string{{START, "wait"}, {START, "fail"}, {"fail", "successor"}, {"successor", END}, {"wait", END}} {
				require.NoError(t, g.AddEdge(edge[0], edge[1]))
			}
			r, err := g.Compile(ctx, WithNodeTriggerMode(AllPredecessor), WithLocalInterrupts(), WithGraphName("errors"))
			require.NoError(t, err)
			cb := callbacks.NewHandlerBuilder().OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, in callbacks.CallbackInput) context.Context {
				if info.Name == "errors" {
					counts.enter("start")
				}
				return ctx
			}).OnErrorFn(func(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
				if info.Name == "errors" {
					counts.enter("error")
				}
				return ctx
			}).OnEndFn(func(ctx context.Context, info *callbacks.RunInfo, out callbacks.CallbackOutput) context.Context {
				if info.Name == "errors" {
					counts.enter("end")
				}
				return ctx
			}).Build()
			_, err = r.Invoke(ctx, "input", WithCallbacks(cb))
			require.Error(t, err)
			if failure == "post-interrupt" {
				require.Len(t, localIDs(localInfo(t, err)), 2)
			} else {
				_, ok := ExtractInterruptInfo(err)
				require.False(t, ok)
				switch failure {
				case "error", "post-error":
					require.ErrorIs(t, err, sentinel)
				case "cancel":
					require.ErrorIs(t, err, context.Canceled)
				case "panic":
					require.ErrorContains(t, err, "node panic")
				}
			}
			localCount(t, counts, "successor", 0, 0)
			localCount(t, counts, "start", 1, 0)
			localCount(t, counts, "error", 1, 0)
			localCount(t, counts, "end", 0, 0)
		})
	}
}
