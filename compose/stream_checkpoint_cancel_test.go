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
	"testing"
	"time"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func TestWorkflowStreamCheckpointCancelDuringDrain(t *testing.T) {
	ctx, cancel := context.WithCancel(streamTestContext(t))
	defer cancel()
	consumed := make(chan struct{})
	writersDone := make(chan struct{}, 2)
	wf := compose.NewWorkflow[string, map[string]any]()
	wf.AddLambdaNode("A", compose.StreamableLambda(func(ctx context.Context, _ string) (*schema.StreamReader[string], error) {
		r, w := schema.Pipe[string](2)
		go func() {
			defer w.Close()
			defer func() { writersDone <- struct{}{} }()
			w.Send("A", nil)
			select {
			case <-consumed:
			case <-ctx.Done():
				return
			}
			w.Send("", compose.StatefulInterrupt(ctx, "approval", 1))
		}()
		return r, nil
	})).AddInput(compose.START)
	wf.AddLambdaNode("B", compose.StreamableLambda(func(ctx context.Context, _ string) (*schema.StreamReader[string], error) {
		r, w := schema.Pipe[string](1)
		go func() {
			defer w.Close()
			defer func() { writersDone <- struct{}{} }()
			w.Send("B", nil)
			<-ctx.Done()
			w.Send("", ctx.Err())
		}()
		return r, nil
	})).AddInput(compose.START)
	wf.AddLambdaNode("End", compose.CollectableLambda(func(_ context.Context, r *schema.StreamReader[map[string]any]) (map[string]any, error) {
		defer r.Close()
		got := map[string]any{}
		signaled := false
		for {
			v, e := r.Recv()
			if e != nil {
				return nil, e
			}
			for k, x := range v {
				got[k] = x
			}
			if len(got) == 2 && !signaled {
				close(consumed)
				signaled = true
			}
		}
	})).AddInput("A", compose.ToField("a")).AddInput("B", compose.ToField("b"))
	wf.End().AddInput("End")
	r, err := wf.Compile(ctx, compose.WithCheckPointStore(&streamReplayStore{}))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, e := r.Stream(ctx, "input", compose.WithCheckPointID("cancel")); done <- e }()
	select {
	case <-consumed:
	case <-time.After(3 * time.Second):
		t.Fatal("consumer not started")
	}
	cancel()
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("cancel lost")
		}
		t.Log(e)
	case <-time.After(3 * time.Second):
		t.Fatal("runner did not stop")
	}
	for i := 0; i < 2; i++ {
		select {
		case <-writersDone:
		case <-time.After(3 * time.Second):
			t.Fatal("producer leaked")
		}
	}
}
