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
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type replayCase struct {
	name                                                                       string
	interruptions                                                              int
	viaTransform, lateSibling, nested, bothInterrupt, skippedBranch, beforeEnd bool
}

func TestWorkflowStreamCheckpointBranches(t *testing.T) {
	for _, tc := range []replayCase{
		{name: "original_input", interruptions: 1},
		{name: "interrupt_before_end", interruptions: 1, beforeEnd: true},
		{name: "repeat_interrupt", interruptions: 2},
		{name: "transform_chain", interruptions: 1, viaTransform: true},
		{name: "sibling_finishes_after_interrupt", interruptions: 1, lateSibling: true},
		{name: "nested_workflow", interruptions: 1, nested: true},
		{name: "both_sources_interrupt", interruptions: 1, bothInterrupt: true},
		{name: "skipped_branch", interruptions: 1, skippedBranch: true},
	} {
		t.Run(tc.name, func(t *testing.T) { runReplayCase(t, tc) })
	}
}

func runReplayCase(t *testing.T, tc replayCase) {
	ctx, cancel := context.WithTimeout(streamTestContext(t), 4*time.Second)
	defer cancel()
	var store compose.CheckPointStore = &streamReplayStore{}
	processDir := os.Getenv("EINO_STREAM_BRANCH_DIR")
	if processDir != "" {
		store = streamReplayFileStore{processDir}
	}
	var a, b, end, chosen, skipped streamCallCount
	if processDir != "" {
		t.Cleanup(func() {
			data, err := json.Marshal(map[string]int32{"A": a.Load(), "B": b.Load(), "End": end.Load()})
			if err != nil {
				t.Error(err)
				return
			}
			f, err := os.OpenFile(filepath.Join(processDir, "counts"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				t.Error(err)
				return
			}
			defer f.Close()
			if _, err = f.Write(append(data, '\n')); err != nil {
				t.Error(err)
			}
		})
	}
	var mu sync.Mutex
	previews := []string{}
	build := func() compose.Runnable[string, map[string]any] {
		observed := make(chan struct{})
		interrupted := make(chan struct{})
		var once sync.Once
		wf := compose.NewWorkflow[string, map[string]any]()
		wf.AddLambdaNode("A", compose.StreamableLambda(func(ctx context.Context, input string) (*schema.StreamReader[string], error) {
			a.Add(1)
			if input != "original-input" {
				return nil, fmt.Errorf("A lost original input: %q", input)
			}
			_, _, phase := compose.GetInterruptState[int](ctx)
			if tc.beforeEnd || phase >= tc.interruptions {
				return schema.StreamReaderFromArray([]string{"A-final"}), nil
			}
			r, w := schema.Pipe[string](2)
			go func() {
				defer w.Close()
				w.Send(fmt.Sprintf("A-preview-%d", phase), nil)
				select {
				case <-observed:
				case <-ctx.Done():
					w.Send("", ctx.Err())
					return
				}
				close(interrupted)
				w.Send("", compose.StatefulInterrupt(ctx, "A approval", phase+1))
			}()
			return r, nil
		})).AddInput(compose.START)
		wf.AddLambdaNode("B", compose.StreamableLambda(func(ctx context.Context, input string) (*schema.StreamReader[string], error) {
			b.Add(1)
			if tc.beforeEnd && input == "" {
				_, has, original := compose.GetInterruptState[string](ctx)
				if has {
					input = original
				}
			}
			if input != "original-input" {
				return nil, fmt.Errorf("B lost original input: %q", input)
			}
			was, _, _ := compose.GetInterruptState[int](ctx)
			if tc.beforeEnd && !was {
				return nil, compose.StatefulInterrupt(ctx, "B approval", input)
			}
			if !tc.lateSibling && (!tc.bothInterrupt || was) {
				return schema.StreamReaderFromArray([]string{"B-final"}), nil
			}
			r, w := schema.Pipe[string](2)
			go func() {
				defer w.Close()
				w.Send("B-", nil)
				select {
				case <-interrupted:
				case <-ctx.Done():
					w.Send("", ctx.Err())
					return
				}
				if tc.bothInterrupt {
					w.Send("", compose.StatefulInterrupt(ctx, "B approval", 1))
				} else {
					w.Send("final", nil)
				}
			}()
			return r, nil
		})).AddInput(compose.START)
		source := "A"
		if tc.viaTransform {
			wf.AddLambdaNode("M", compose.TransformableLambda(func(_ context.Context, r *schema.StreamReader[string]) (*schema.StreamReader[string], error) {
				return r, nil
			})).AddInput("A")
			source = "M"
		}
		c := wf.AddLambdaNode("End", compose.CollectableLambda(func(_ context.Context, r *schema.StreamReader[map[string]any]) (map[string]any, error) {
			end.Add(1)
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
				for k, val := range v {
					old, _ := out[k].(string)
					out[k] = old + val.(string)
				}
				if out["a"] != nil && out["b"] != nil {
					mu.Lock()
					previews = append(previews, fmt.Sprint(out))
					mu.Unlock()
					once.Do(func() { close(observed) })
				}
			}
		}))
		c.AddInput(source, compose.ToField("a"))
		c.AddInput("B", compose.ToField("b"))
		if tc.skippedBranch {
			wf.AddLambdaNode("Selector", compose.InvokableLambda(func(_ context.Context, s string) (string, error) { return s, nil })).AddInput(compose.START)
			wf.AddLambdaNode("Chosen", compose.InvokableLambda(func(_ context.Context, s string) (string, error) { chosen.Add(1); return s, nil })).AddInputWithOptions(compose.START, nil, compose.WithNoDirectDependency())
			wf.AddLambdaNode("Skipped", compose.InvokableLambda(func(_ context.Context, s string) (string, error) { skipped.Add(1); return s, nil })).AddInputWithOptions(compose.START, nil, compose.WithNoDirectDependency())
			wf.AddBranch("Selector", compose.NewGraphBranch(func(_ context.Context, _ string) (string, error) { return "Chosen", nil }, map[string]bool{"Chosen": true, "Skipped": true}))
			c.AddDependency("Chosen")
			c.AddInput("Skipped", compose.ToField("skipped"))
		}
		wf.End().AddInput("End")
		if tc.nested {
			parent := compose.NewWorkflow[string, map[string]any]()
			parent.AddGraphNode("nested", wf).AddInput(compose.START)
			parent.End().AddInput("nested")
			wf = parent
		}
		run, err := wf.Compile(ctx, compose.WithCheckPointStore(store), compose.WithFanInMergeConfig(map[string]compose.FanInMergeConfig{"End": {StreamMergeWithSourceEOF: true}}))
		if err != nil {
			t.Fatal(err)
		}
		return run
	}
	callCtx := ctx
	input := "original-input"
	if processDir != "" {
		raw, err := os.ReadFile(filepath.Join(processDir, "resume.json"))
		if err == nil {
			data := map[string]any{}
			if err = json.Unmarshal(raw, &data); err != nil {
				t.Fatal(err)
			}
			callCtx = compose.BatchResumeWithData(ctx, data)
			input = ""
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
	for attempt := 0; attempt <= tc.interruptions+1; attempt++ {
		sr, err := build().Stream(callCtx, input, compose.WithCheckPointID("matrix"))
		if err != nil {
			info, ok := compose.ExtractInterruptInfo(err)
			if !ok || len(info.InterruptContexts) == 0 {
				t.Fatalf("attempt %d: %v", attempt, err)
			}
			data := map[string]any{}
			for _, in := range info.InterruptContexts {
				data[in.ID] = "yes"
			}
			if processDir != "" {
				raw, err := json.Marshal(data)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(filepath.Join(processDir, "resume.json"), raw, 0600); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(filepath.Join(processDir, "status"), []byte("interrupt"), 0600); err != nil {
					t.Fatal(err)
				}
				return
			}
			callCtx = compose.BatchResumeWithData(ctx, data)
			input = ""
			continue
		}
		result, err := sr.Recv()
		sr.Close()
		t.Logf("result=%v err=%v calls A=%d B=%d End=%d previews=%v", result, err, a.Load(), b.Load(), end.Load(), previews)
		if err != nil || result["a"] != "A-final" || result["b"] != "B-final" {
			t.Fatalf("bad result %v %v", result, err)
		}
		if processDir != "" {
			if err = os.WriteFile(filepath.Join(processDir, "status"), []byte("success"), 0600); err != nil {
				t.Fatal(err)
			}
			return
		}
		if tc.skippedBranch && (chosen.Load() != 1 || skipped.Load() != 0) {
			t.Fatalf("chosen=%d skipped=%d", chosen.Load(), skipped.Load())
		}
		wantB := int32(1)
		if tc.bothInterrupt {
			wantB = 2
		}
		wantA, wantEnd := int32(tc.interruptions+1), int32(tc.interruptions+1)
		if tc.beforeEnd {
			wantA, wantB, wantEnd = 1, 2, 1
		}
		if a.Load() != wantA || b.Load() != wantB || end.Load() != wantEnd {
			t.Fatalf("wrong calls A=%d B=%d End=%d", a.Load(), b.Load(), end.Load())
		}
		return
	}
	t.Fatal("did not finish after expected resumes")
}
