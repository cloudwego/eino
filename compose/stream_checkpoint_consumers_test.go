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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type streamReplayCase struct {
	name                                                                     string
	interruptions                                                            int
	streaming, secondJoin, fanout, nested, consumerInterrupt, returnedReader bool
}

func TestWorkflowStreamCheckpointReplay(t *testing.T) {
	for _, tc := range []streamReplayCase{
		{name: "collect_middle", interruptions: 1},
		{name: "transform_middle", interruptions: 1, streaming: true},
		{name: "transform_repeat", interruptions: 2, streaming: true},
		{name: "two_fanins", interruptions: 1, streaming: true, secondJoin: true},
		{name: "two_consumers", interruptions: 1, streaming: true, fanout: true},
		{name: "nested_transform", interruptions: 1, streaming: true, nested: true},
		{name: "consumer_own_interrupt", interruptions: 1, consumerInterrupt: true},
		{name: "transform_own_interrupt", interruptions: 1, consumerInterrupt: true, streaming: true, returnedReader: true},
		{name: "returned_reader", interruptions: 1, streaming: true, returnedReader: true},
		{name: "returned_reader_repeat", interruptions: 2, streaming: true, returnedReader: true},
		{name: "returned_reader_nested", interruptions: 1, streaming: true, nested: true, returnedReader: true},
	} {
		t.Run(tc.name, func(t *testing.T) { runStreamReplayCase(t, tc) })
	}
}

func runStreamReplayCase(t *testing.T, tc streamReplayCase) {
	ctx, cancel := context.WithTimeout(streamTestContext(t), 5*time.Second)
	defer cancel()
	var store compose.CheckPointStore = &streamReplayStore{}
	processDir := os.Getenv("EINO_STREAM_REPLAY_DIR")
	startAttempt := 0
	if processDir != "" {
		store = streamReplayFileStore{processDir}
		var err error
		startAttempt, err = strconv.Atoi(os.Getenv("EINO_STREAM_REPLAY_PHASE"))
		if err != nil {
			t.Fatal(err)
		}
	}
	var a, b, c1, c2, d, stable streamCallCount
	callCtx := ctx
	input := "original"
	if processDir != "" {
		defer func() {
			raw, err := json.Marshal(map[string]int32{"a": a.Load(), "b": b.Load(), "c1": c1.Load(), "stable": stable.Load()})
			if err != nil {
				t.Error(err)
				return
			}
			if err = os.WriteFile(filepath.Join(processDir, fmt.Sprintf("calls-%d.json", startAttempt)), raw, 0600); err != nil {
				t.Error(err)
			}
		}()
		if startAttempt > 0 {
			raw, err := os.ReadFile(filepath.Join(processDir, "resume.json"))
			if err != nil {
				t.Fatal(err)
			}
			var data map[string]any
			if err = json.Unmarshal(raw, &data); err != nil {
				t.Fatal(err)
			}
			callCtx = compose.BatchResumeWithData(ctx, data)
			input = ""
		}
	}
	for attempt := startAttempt; attempt <= tc.interruptions; attempt++ {
		observed := make(chan struct{})
		remainingConsumers := 1
		if tc.fanout {
			remainingConsumers++
		}
		var observedMu sync.Mutex
		markConsumed := func() {
			observedMu.Lock()
			defer observedMu.Unlock()
			remainingConsumers--
			if remainingConsumers == 0 {
				close(observed)
			}
		}
		wf := compose.NewWorkflow[string, map[string]any]()
		wf.AddLambdaNode("A", compose.StreamableLambda(func(ctx context.Context, in string) (*schema.StreamReader[string], error) {
			a.Add(1)
			if in != "original" {
				return nil, fmt.Errorf("A input=%q", in)
			}
			_, _, phase := compose.GetInterruptState[int](ctx)
			if tc.consumerInterrupt || phase >= tc.interruptions {
				return schema.StreamReaderFromArray([]string{"a-final"}), nil
			}
			r, w := schema.Pipe[string](1)
			go func() {
				defer w.Close()
				if w.Send("a-preview", nil) {
					return
				}
				select {
				case <-observed:
				case <-ctx.Done():
					w.Send("", ctx.Err())
					return
				}
				w.Send("", compose.StatefulInterrupt(ctx, "A approval", phase+1))
			}()
			return r, nil
		})).AddInput(compose.START)
		wf.AddLambdaNode("B", compose.StreamableLambda(func(ctx context.Context, in string) (*schema.StreamReader[string], error) {
			b.Add(1)
			if in != "original" {
				return nil, fmt.Errorf("B input=%q", in)
			}
			return schema.StreamReaderFromArray([]string{"b-final"}), nil
		})).AddInput(compose.START)
		addConsumer := func(name string, calls *streamCallCount) {
			var once sync.Once
			collect := func(ctx context.Context, r *schema.StreamReader[map[string]any], emit func(map[string]any) bool) (map[string]any, error) {
				defer r.Close()
				out := map[string]any{}
				for {
					v, err := r.Recv()
					if _, ok := schema.GetSourceName(err); ok {
						continue
					}
					if errors.Is(err, io.EOF) {
						break
					}
					if err != nil {
						return nil, err
					}
					transformed := map[string]any{}
					for k, x := range v {
						before, _ := out[k].(string)
						out[k] = before + strings.ToUpper(x.(string))
						transformed[k] = strings.ToUpper(x.(string))
					}
					if out["a"] != nil && out["b"] != nil {
						once.Do(markConsumed)
					}
					if emit != nil && emit(transformed) {
						return nil, context.Canceled
					}
				}
				if tc.consumerInterrupt {
					_, has, _ := compose.GetInterruptState[int](ctx)
					if !has {
						return nil, compose.StatefulInterrupt(ctx, "C approval", 1)
					}
				}
				return out, nil
			}
			var l *compose.Lambda
			if tc.streaming {
				l = compose.TransformableLambda(func(ctx context.Context, r *schema.StreamReader[map[string]any]) (*schema.StreamReader[map[string]any], error) {
					calls.Add(1)
					reader, writer := schema.Pipe[map[string]any](1)
					go func() {
						defer writer.Close()
						_, err := collect(ctx, r, func(v map[string]any) bool { return writer.Send(v, nil) })
						if err != nil {
							writer.Send(nil, err)
						}
					}()
					return reader, nil
				})
			} else {
				l = compose.CollectableLambda(func(ctx context.Context, r *schema.StreamReader[map[string]any]) (map[string]any, error) {
					calls.Add(1)
					return collect(ctx, r, nil)
				})
			}
			node := wf.AddLambdaNode(name, l)
			node.AddInput("A", compose.ToField("a"))
			node.AddInput("B", compose.ToField("b"))
		}
		addConsumer("C1", &c1)
		source := "C1"
		if tc.secondJoin {
			wf.AddLambdaNode("Stable", compose.InvokableLambda(func(_ context.Context, in string) (map[string]any, error) {
				stable.Add(1)
				return map[string]any{"s": "stable"}, nil
			})).AddInput(compose.START)
			node := wf.AddLambdaNode("C2", compose.TransformableLambda(func(_ context.Context, r *schema.StreamReader[map[string]any]) (*schema.StreamReader[map[string]any], error) {
				c2.Add(1)
				return r, nil
			}))
			node.AddInput("C1", compose.MapFields("a", "a"), compose.MapFields("b", "b"))
			node.AddInput("Stable", compose.MapFields("s", "s"))
			source = "C2"
		}
		if tc.fanout {
			addConsumer("C2", &c2)
		}
		if tc.returnedReader {
			wf.End().AddInput(source)
		} else {
			terminal := wf.AddLambdaNode("D", compose.CollectableLambda(func(_ context.Context, r *schema.StreamReader[map[string]any]) (map[string]any, error) {
				d.Add(1)
				defer r.Close()
				out := map[string]any{}
				for {
					v, err := r.Recv()
					if _, ok := schema.GetSourceName(err); ok {
						continue
					}
					if errors.Is(err, io.EOF) {
						return out, nil
					}
					if err != nil {
						return nil, err
					}
					for k, x := range v {
						if sub, ok := x.(map[string]any); ok {
							old, _ := out[k].(map[string]any)
							if old == nil {
								old = map[string]any{}
							}
							for key, val := range sub {
								prior, _ := old[key].(string)
								old[key] = prior + val.(string)
							}
							out[k] = old
						} else {
							old, _ := out[k].(string)
							out[k] = old + x.(string)
						}
					}
				}
			}))
			if tc.fanout {
				terminal.AddInput("C1", compose.ToField("left"))
				terminal.AddInput("C2", compose.ToField("right"))
			} else {
				terminal.AddInput(source)
			}
			wf.End().AddInput("D")
		}

		if tc.nested {
			outer := compose.NewWorkflow[string, map[string]any]()
			outer.AddGraphNode("child", wf).AddInput(compose.START)
			outer.End().AddInput("child")
			wf = outer
		}
		run, err := wf.Compile(ctx, compose.WithCheckPointStore(store))
		if err != nil {
			t.Fatal(err)
		}
		sr, err := run.Stream(callCtx, input, compose.WithCheckPointID(tc.name))
		result := map[string]any{}
		if err == nil {
			for {
				value, readErr := sr.Recv()
				if _, ok := schema.GetSourceName(readErr); ok {
					continue
				}
				if errors.Is(readErr, io.EOF) {
					break
				}
				if readErr != nil {
					err = readErr
					break
				}
				for key, x := range value {
					if text, ok := x.(string); ok {
						old, _ := result[key].(string)
						result[key] = old + text
					} else {
						result[key] = x
					}
				}
			}
			sr.Close()
		}

		if attempt < tc.interruptions {
			info := requireStreamInterrupt(t, err)
			if processDir != "" {
				data := map[string]any{info.InterruptContexts[0].ID: "approved"}
				raw, e := json.Marshal(data)
				if e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(filepath.Join(processDir, "resume.json"), raw, 0600); e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(filepath.Join(processDir, "status"), []byte("interrupt"), 0600); e != nil {
					t.Fatal(e)
				}
				return
			}
			callCtx = compose.ResumeWithData(ctx, info.InterruptContexts[0].ID, "approved")
			input = ""
			raw, ok, e := store.Get(ctx, tc.name)
			if !ok || e != nil {
				t.Fatal("checkpoint missing", e)
			}
			store = &streamReplayStore{}
			if e = store.Set(ctx, tc.name, raw); e != nil {
				t.Fatal(e)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		check := func(v map[string]any) {
			if v["a"] != "A-FINAL" || v["b"] != "B-FINAL" {
				t.Fatalf("bad result: %#v", v)
			}
		}
		if tc.fanout {
			for _, key := range []string{"left", "right"} {
				v, ok := result[key].(map[string]any)
				if !ok {
					t.Fatalf("missing %s: %#v", key, result)
				}
				check(v)
			}
		} else {
			check(result)
		}
		if tc.secondJoin && result["s"] != "stable" {
			t.Fatalf("lost second sibling: %#v", result)
		}
	}
	if processDir != "" {
		if err := os.WriteFile(filepath.Join(processDir, "status"), []byte("success"), 0600); err != nil {
			t.Fatal(err)
		}
		return
	}
	wantA := tc.interruptions + 1
	if tc.consumerInterrupt {
		wantA = 1
	}
	if a.Load() != int32(wantA) || b.Load() != 1 || c1.Load() != int32(tc.interruptions+1) {
		t.Fatalf("calls A=%d B=%d C1=%d C2=%d D=%d", a.Load(), b.Load(), c1.Load(), c2.Load(), d.Load())
	}
	if tc.secondJoin && stable.Load() != 1 {
		t.Fatalf("stable sibling rerun %d", stable.Load())
	}
	t.Logf("A=%d B=%d C1=%d C2=%d D=%d stable=%d", a.Load(), b.Load(), c1.Load(), c2.Load(), d.Load(), stable.Load())
}

func TestWorkflowStreamCheckpointReplayAcrossProcesses(t *testing.T) {
	for _, name := range []string{"collect_middle", "transform_repeat", "two_fanins", "returned_reader_nested"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			phases := 2
			if name == "transform_repeat" {
				phases = 3
			}
			totals := map[string]int{}
			for phase := 0; phase < phases; phase++ {
				cmd := exec.CommandContext(streamTestContext(t), os.Args[0], "-test.run=^TestWorkflowStreamCheckpointReplay$/^"+name+"$", "-test.timeout=15s", "-test.v")
				cmd.Env = append(os.Environ(), "EINO_STREAM_REPLAY_DIR="+dir, "EINO_STREAM_REPLAY_PHASE="+strconv.Itoa(phase))
				output, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("phase %d: %v\n%s", phase, err, output)
				}
				status, err := os.ReadFile(filepath.Join(dir, "status"))
				if err != nil {
					t.Fatal(err)
				}
				want := "interrupt"
				if phase == phases-1 {
					want = "success"
				}
				if string(status) != want {
					t.Fatalf("status=%s want=%s", status, want)
				}
				raw, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("calls-%d.json", phase)))
				if err != nil {
					t.Fatal(err)
				}
				var counts map[string]int
				if err = json.Unmarshal(raw, &counts); err != nil {
					t.Fatal(err)
				}
				for k, v := range counts {
					totals[k] += v
				}
			}
			if totals["a"] != phases || totals["b"] != 1 || totals["c1"] != phases {
				t.Fatalf("totals=%v", totals)
			}
			if name == "two_fanins" && totals["stable"] != 1 {
				t.Fatalf("totals=%v", totals)
			}
			t.Logf("fresh processes=%d totals=%v", phases, totals)
		})
	}
}
