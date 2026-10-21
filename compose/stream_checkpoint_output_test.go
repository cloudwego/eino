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
	"io"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func TestWorkflowStreamCheckpointReturnedReaderCloseReleasesProducer(t *testing.T) {
	done := make(chan struct{})
	wf := compose.NewWorkflow[string, string]()
	wf.AddLambdaNode("A", compose.StreamableLambda(func(_ context.Context, _ string) (*schema.StreamReader[string], error) {
		r, w := schema.Pipe[string](0)
		go func() { defer close(done); defer w.Close(); w.Send("pending", nil) }()
		return r, nil
	})).AddInput(compose.START)
	wf.End().AddInput("A")
	run, err := wf.Compile(streamTestContext(t), compose.WithCheckPointStore(&streamReplayStore{}))
	if err != nil {
		t.Fatal(err)
	}
	sr, err := run.Stream(streamTestContext(t), "input", compose.WithCheckPointID("close"))
	if err != nil {
		t.Fatal(err)
	}
	sr.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close retained an unread stream copy")
	}
}

type failingStreamReplayStore struct{ *streamReplayStore }

func (s failingStreamReplayStore) Set(context.Context, string, []byte) error {
	return errors.New("checkpoint write unavailable")
}

func TestWorkflowStreamCheckpointReturnedReaderFailure(t *testing.T) {
	for _, kind := range []string{"ordinary_error", "checkpoint_write_failure", "cancellation"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(streamTestContext(t))
			defer cancel()
			store := &streamReplayStore{}
			var checkpoint compose.CheckPointStore = store
			if kind == "checkpoint_write_failure" {
				checkpoint = failingStreamReplayStore{store}
			}
			wf := compose.NewWorkflow[string, string]()
			wf.AddLambdaNode("A", compose.StreamableLambda(func(ctx context.Context, _ string) (*schema.StreamReader[string], error) {
				r, w := schema.Pipe[string](1)
				go func() {
					defer w.Close()
					if w.Send("first", nil) {
						return
					}
					switch kind {
					case "ordinary_error":
						w.Send("", errors.New("provider exploded"))
					case "checkpoint_write_failure":
						w.Send("", compose.StatefulInterrupt(ctx, "approve", 1))
					case "cancellation":
						<-ctx.Done()
						w.Send("", ctx.Err())
					}
				}()
				return r, nil
			})).AddInput(compose.START)
			wf.End().AddInput("A")
			run, err := wf.Compile(ctx, compose.WithCheckPointStore(checkpoint))
			if err != nil {
				t.Fatal(err)
			}
			sr, err := run.Stream(ctx, "input", compose.WithCheckPointID(kind))
			if err != nil {
				t.Fatal(err)
			}
			defer sr.Close()
			if v, err := sr.Recv(); v != "first" || err != nil {
				t.Fatalf("first=%q %v", v, err)
			}
			if kind == "cancellation" {
				cancel()
			}
			_, err = sr.Recv()
			expected := map[string]string{"ordinary_error": "provider exploded", "checkpoint_write_failure": "checkpoint write unavailable", "cancellation": "context canceled"}[kind]
			if err == nil || errors.Is(err, io.EOF) || !strings.Contains(err.Error(), expected) {
				t.Fatalf("error=%v want %s", err, expected)
			}
			if _, ok, _ := store.Get(ctx, kind); ok {
				t.Fatal("unexpected resumable checkpoint")
			}
		})
	}
}
