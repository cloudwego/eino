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

package compose_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func TestWorkflowStreamCheckpointNestedSources(t *testing.T) {
	for _, returned := range []bool{false, true} {
		t.Run(fmt.Sprintf("returned_%v", returned), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(streamTestContext(t), 5*time.Second)
			defer cancel()
			var counts [2]streamCallCount
			store := &streamReplayStore{}
			callCtx := ctx
			input := "original"
			for phase := 0; phase < 2; phase++ {
				observed := make(chan struct{})
				var once sync.Once
				outer := compose.NewWorkflow[string, map[string]any]()
				for i, key := range []string{"a", "b"} {
					i, key := i, key
					child := compose.NewWorkflow[string, string]()
					child.AddLambdaNode("source", compose.StreamableLambda(func(ctx context.Context, in string) (*schema.StreamReader[string], error) {
						counts[i].Add(1)
						if in != "original" {
							return nil, fmt.Errorf("%s input=%q", key, in)
						}
						_, has, _ := compose.GetInterruptState[int](ctx)
						if has {
							return schema.StreamReaderFromArray([]string{key + "-final"}), nil
						}
						r, w := schema.Pipe[string](1)
						go func() {
							defer w.Close()
							if w.Send(key+"-preview", nil) {
								return
							}
							select {
							case <-observed:
							case <-ctx.Done():
								w.Send("", ctx.Err())
								return
							}
							w.Send("", compose.StatefulInterrupt(ctx, key+" approval", 1))
						}()
						return r, nil
					})).AddInput(compose.START)
					child.End().AddInput("source")
					outer.AddGraphNode(key, child).AddInput(compose.START)
				}
				consume := func(r *schema.StreamReader[map[string]any], emit func(map[string]any) bool) (map[string]any, error) {
					defer r.Close()
					result := map[string]any{}
					for {
						v, e := r.Recv()
						if _, ok := schema.GetSourceName(e); ok {
							continue
						}
						if errors.Is(e, io.EOF) {
							return result, nil
						}
						if e != nil {
							return nil, e
						}
						for k, x := range v {
							result[k] = x
						}
						if len(result) == 2 {
							once.Do(func() { close(observed) })
						}
						if emit != nil && emit(v) {
							return nil, context.Canceled
						}
					}
				}
				var consumer *compose.Lambda
				if returned {
					consumer = compose.TransformableLambda(func(_ context.Context, r *schema.StreamReader[map[string]any]) (*schema.StreamReader[map[string]any], error) {
						out, w := schema.Pipe[map[string]any](1)
						go func() {
							defer w.Close()
							_, e := consume(r, func(v map[string]any) bool { return w.Send(v, nil) })
							if e != nil {
								w.Send(nil, e)
							}
						}()
						return out, nil
					})
				} else {
					consumer = compose.CollectableLambda(func(_ context.Context, r *schema.StreamReader[map[string]any]) (map[string]any, error) {
						return consume(r, nil)
					})
				}
				n := outer.AddLambdaNode("middle", consumer)
				n.AddInput("a", compose.ToField("a"))
				n.AddInput("b", compose.ToField("b"))
				outer.End().AddInput("middle")
				run, e := outer.Compile(ctx, compose.WithCheckPointStore(store))
				if e != nil {
					t.Fatal(e)
				}
				sr, e := run.Stream(callCtx, input, compose.WithCheckPointID("nested-sources"))
				result := map[string]any{}
				if e == nil {
					for {
						v, re := sr.Recv()
						if _, ok := schema.GetSourceName(re); ok {
							continue
						}
						if errors.Is(re, io.EOF) {
							break
						}
						if re != nil {
							e = re
							break
						}
						for k, x := range v {
							result[k] = x
						}
					}
					sr.Close()
				}
				if phase == 0 {
					info := requireStreamInterrupt(t, e)
					if len(info.InterruptContexts) != 2 {
						t.Fatalf("interrupts=%d want2", len(info.InterruptContexts))
					}
					data := map[string]any{}
					for _, ic := range info.InterruptContexts {
						data[ic.ID] = "approved"
					}
					callCtx = compose.BatchResumeWithData(ctx, data)
					input = ""
					raw, ok, e := store.Get(ctx, "nested-sources")
					if e != nil || !ok {
						t.Fatal("missing checkpoint", e)
					}
					store = &streamReplayStore{}
					if e = store.Set(ctx, "nested-sources", raw); e != nil {
						t.Fatal(e)
					}
				} else {
					if e != nil {
						t.Fatal(e)
					}
					if result["a"] != "a-final" || result["b"] != "b-final" {
						t.Fatalf("result=%v", result)
					}
				}
			}
			if counts[0].Load() != 2 || counts[1].Load() != 2 {
				t.Fatalf("counts=%d,%d", counts[0].Load(), counts[1].Load())
			}
		})
	}
}
