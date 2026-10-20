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
	"io"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func TestToolTimeoutNodeIntegration(t *testing.T) {
	middleware, err := NewToolTimeoutMiddleware(50 * time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	slowStarted := make(chan struct{})
	fastDone := make(chan struct{})
	slowTool := newTool(&schema.ToolInfo{Name: "slow"}, func(ctx context.Context, _ *struct{}) (string, error) {
		close(slowStarted)
		<-ctx.Done()
		return "", ctx.Err()
	})
	fastTool := newTool(&schema.ToolInfo{Name: "fast"}, func(ctx context.Context, _ *struct{}) (string, error) {
		<-slowStarted
		close(fastDone)
		return "ok", nil
	})
	node, err := NewToolNode(context.Background(), &ToolsNodeConfig{
		Tools:               []tool.BaseTool{slowTool, fastTool},
		ToolCallMiddlewares: []ToolMiddleware{middleware},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = node.Invoke(context.Background(), schema.AssistantMessage("", []schema.ToolCall{
		{ID: "1", Function: schema.FunctionCall{Name: "slow", Arguments: `{}`}},
		{ID: "2", Function: schema.FunctionCall{Name: "fast", Arguments: `{}`}},
	}))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Invoke() error = %v, want deadline exceeded", err)
	}
	select {
	case <-fastDone:
	default:
		t.Fatal("fast tool did not complete while slow tool timed out")
	}
}

func TestToolTimeoutMergedStreams(t *testing.T) {
	middleware, err := NewToolTimeoutMiddleware(50 * time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	slow := newStreamableTool(&schema.ToolInfo{Name: "slow"}, func(ctx context.Context, _ *struct{}) (*schema.StreamReader[string], error) {
		reader, writer := schema.Pipe[string](0)
		go func() {
			defer writer.Close()
			<-ctx.Done()
		}()
		return reader, nil
	})
	fast := newStreamableTool(&schema.ToolInfo{Name: "fast"}, func(ctx context.Context, _ *struct{}) (*schema.StreamReader[string], error) {
		return schema.StreamReaderFromArray([]string{"ok"}), nil
	})
	node, err := NewToolNode(context.Background(), &ToolsNodeConfig{
		Tools:               []tool.BaseTool{slow, fast},
		ToolCallMiddlewares: []ToolMiddleware{middleware},
	})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := node.Stream(context.Background(), schema.AssistantMessage("", []schema.ToolCall{
		{ID: "1", Function: schema.FunctionCall{Name: "slow", Arguments: `{}`}},
		{ID: "2", Function: schema.FunctionCall{Name: "fast", Arguments: `{}`}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	seenFast := false
	for {
		msgs, err := reader.Recv()
		if errors.Is(err, context.DeadlineExceeded) {
			if !seenFast {
				t.Fatal("fast stream did not complete before slow stream timed out")
			}
			return
		}
		if err != nil {
			t.Fatalf("Recv() error = %v, want deadline exceeded", err)
		}
		for _, msg := range msgs {
			if msg != nil && msg.Content == `"ok"` {
				seenFast = true
			}
		}
	}
}

func TestToolTimeoutMiddleware(t *testing.T) {
	if _, err := NewToolTimeoutMiddleware(0); err == nil {
		t.Fatal("zero timeout should be rejected")
	}
	if _, err := NewToolTimeoutMiddleware(-time.Second); err == nil {
		t.Fatal("negative timeout should be rejected")
	}

	middleware, err := NewToolTimeoutMiddleware(60 * time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	slow := middleware.Invokable(func(ctx context.Context, _ *ToolInput) (*ToolOutput, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	fast := middleware.Invokable(func(ctx context.Context, _ *ToolInput) (*ToolOutput, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
			return &ToolOutput{Result: "ok"}, nil
		}
	})
	result := make(chan error, 1)
	go func() {
		_, err := slow(context.Background(), &ToolInput{Name: "slow"})
		result <- err
	}()
	<-started
	out, err := fast(context.Background(), &ToolInput{Name: "fast"})
	if err != nil || out.Result != "ok" {
		t.Fatalf("fast call = (%v, %v), want ok", out, err)
	}
	if err := <-result; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("slow call error = %v, want deadline exceeded", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	enhanced := middleware.EnhancedInvokable(func(ctx context.Context, _ *ToolInput) (*EnhancedInvokableToolOutput, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	cancel()
	if _, err := enhanced(ctx, &ToolInput{Name: "enhanced"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("enhanced call error = %v, want canceled", err)
	}
}

func TestToolTimeoutStreams(t *testing.T) {
	middleware, err := NewToolTimeoutMiddleware(60 * time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		wrap func(context.Context) (*schema.StreamReader[string], error)
	}{
		{"standard", func(ctx context.Context) (*schema.StreamReader[string], error) {
			out, err := middleware.Streamable(func(ctx context.Context, _ *ToolInput) (*StreamToolOutput, error) {
				reader, writer := schema.Pipe[string](0)
				go func() {
					defer writer.Close()
					<-ctx.Done()
				}()
				return &StreamToolOutput{Result: reader}, nil
			})(ctx, &ToolInput{Name: "stream"})
			if err != nil {
				return nil, err
			}
			return out.Result, nil
		}},
		{"enhanced", func(ctx context.Context) (*schema.StreamReader[string], error) {
			out, err := middleware.EnhancedStreamable(func(ctx context.Context, _ *ToolInput) (*EnhancedStreamableToolOutput, error) {
				reader, writer := schema.Pipe[*schema.ToolResult](0)
				go func() {
					defer writer.Close()
					<-ctx.Done()
				}()
				return &EnhancedStreamableToolOutput{Result: reader}, nil
			})(ctx, &ToolInput{Name: "enhanced stream"})
			if err != nil {
				return nil, err
			}
			converted := schema.StreamReaderWithConvert(out.Result, func(v *schema.ToolResult) (string, error) { return "", nil })
			return converted, nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader, err := tc.wrap(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			_, err = reader.Recv()
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Recv() = %v, want deadline exceeded", err)
			}
			_, err = reader.Recv()
			if !errors.Is(err, io.EOF) {
				t.Fatalf("Recv() after deadline = %v, want EOF", err)
			}
		})
	}
}

func TestToolTimeoutStreamParentCancellation(t *testing.T) {
	middleware, err := NewToolTimeoutMiddleware(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	endpoint := middleware.Streamable(func(ctx context.Context, _ *ToolInput) (*StreamToolOutput, error) {
		reader, writer := schema.Pipe[string](0)
		go func() {
			defer writer.Close()
			<-ctx.Done()
		}()
		return &StreamToolOutput{Result: reader}, nil
	})
	out, err := endpoint(ctx, &ToolInput{Name: "stream"})
	if err != nil {
		t.Fatal(err)
	}
	defer out.Result.Close()
	cancel()
	_, err = out.Result.Recv()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Recv() = %v, want canceled", err)
	}
}

func TestToolTimeoutStreamSourceError(t *testing.T) {
	middleware, err := NewToolTimeoutMiddleware(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	want := errors.New("source failed")
	endpoint := middleware.Streamable(func(ctx context.Context, _ *ToolInput) (*StreamToolOutput, error) {
		reader, writer := schema.Pipe[string](1)
		writer.Send("", want)
		writer.Close()
		return &StreamToolOutput{Result: reader}, nil
	})
	out, err := endpoint(context.Background(), &ToolInput{Name: "stream"})
	if err != nil {
		t.Fatal(err)
	}
	defer out.Result.Close()
	_, err = out.Result.Recv()
	if !errors.Is(err, want) {
		t.Fatalf("Recv() = %v, want source error", err)
	}
}

func TestToolTimeoutStreamAfterFirstChunk(t *testing.T) {
	middleware, err := NewToolTimeoutMiddleware(50 * time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := middleware.Streamable(func(ctx context.Context, _ *ToolInput) (*StreamToolOutput, error) {
		reader, writer := schema.Pipe[string](0)
		go func() {
			defer writer.Close()
			if writer.Send("first", nil) {
				return
			}
			<-ctx.Done()
		}()
		return &StreamToolOutput{Result: reader}, nil
	})
	out, err := endpoint(context.Background(), &ToolInput{Name: "stream"})
	if err != nil {
		t.Fatal(err)
	}
	defer out.Result.Close()
	first, err := out.Result.Recv()
	if err != nil || first != "first" {
		t.Fatalf("first Recv() = (%q, %v), want first", first, err)
	}
	time.Sleep(70 * time.Millisecond)
	_, err = out.Result.Recv()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Recv() after deadline = %v, want deadline exceeded", err)
	}
}

func TestToolTimeoutStreamCompletes(t *testing.T) {
	middleware, err := NewToolTimeoutMiddleware(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := middleware.Streamable(func(ctx context.Context, _ *ToolInput) (*StreamToolOutput, error) {
		return &StreamToolOutput{Result: schema.StreamReaderFromArray([]string{"done"})}, nil
	})
	out, err := endpoint(context.Background(), &ToolInput{Name: "stream"})
	if err != nil {
		t.Fatal(err)
	}
	defer out.Result.Close()
	value, err := out.Result.Recv()
	if err != nil || value != "done" {
		t.Fatalf("Recv() = (%q, %v), want done", value, err)
	}
	_, err = out.Result.Recv()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("final Recv() = %v, want EOF", err)
	}
}

func TestToolTimeoutPausedConsumer(t *testing.T) {
	middleware, err := NewToolTimeoutMiddleware(40 * time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := middleware.Streamable(func(ctx context.Context, _ *ToolInput) (*StreamToolOutput, error) {
		reader, writer := schema.Pipe[string](0)
		go func() {
			defer writer.Close()
			writer.Send("first", nil)
			<-ctx.Done()
		}()
		return &StreamToolOutput{Result: reader}, nil
	})
	out, err := endpoint(context.Background(), &ToolInput{Name: "stream"})
	if err != nil {
		t.Fatal(err)
	}
	defer out.Result.Close()
	time.Sleep(100 * time.Millisecond)
	result := make(chan error, 1)
	go func() {
		_, err := out.Result.Recv()
		result <- err
	}()
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Recv() after pausing = %v, want deadline exceeded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("deadline was blocked by a paused reader")
	}
}

func TestToolTimeoutClosesPipeSourceOnDeadline(t *testing.T) {
	middleware, err := NewToolTimeoutMiddleware(40 * time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	producerDone := make(chan struct{})
	endpoint := middleware.Streamable(func(context.Context, *ToolInput) (*StreamToolOutput, error) {
		reader, writer := schema.Pipe[string](0)
		go func() {
			defer writer.Close()
			defer close(producerDone)
			<-reader.Done()
		}()
		return &StreamToolOutput{Result: reader}, nil
	})
	out, err := endpoint(context.Background(), &ToolInput{Name: "stream"})
	if err != nil {
		t.Fatal(err)
	}
	defer out.Result.Close()
	_, err = out.Result.Recv()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Recv() = %v, want deadline exceeded", err)
	}
	select {
	case <-producerDone:
	case <-time.After(time.Second):
		t.Fatal("source producer did not observe reader closure")
	}
}

func TestToolTimeoutClosesErroredStream(t *testing.T) {
	middleware, err := NewToolTimeoutMiddleware(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	reader, writer := schema.Pipe[string](0)
	defer writer.Close()
	endpoint := middleware.Streamable(func(context.Context, *ToolInput) (*StreamToolOutput, error) {
		return &StreamToolOutput{Result: reader}, errors.New("tool failed")
	})
	if _, err := endpoint(context.Background(), &ToolInput{Name: "stream"}); err == nil {
		t.Fatal("expected tool failure")
	}
	select {
	case <-reader.Done():
	default:
		t.Fatal("discarded stream was not closed")
	}
}

func TestToolTimeoutStreamClose(t *testing.T) {
	middleware, err := NewToolTimeoutMiddleware(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	toolDone := make(chan struct{})
	endpoint := middleware.Streamable(func(ctx context.Context, _ *ToolInput) (*StreamToolOutput, error) {
		reader, writer := schema.Pipe[string](0)
		go func() {
			defer writer.Close()
			<-ctx.Done()
			close(toolDone)
		}()
		return &StreamToolOutput{Result: reader}, nil
	})
	out, err := endpoint(context.Background(), &ToolInput{Name: "stream"})
	if err != nil {
		t.Fatal(err)
	}
	out.Result.Close()
	select {
	case <-toolDone:
	case <-time.After(time.Second):
		t.Fatal("closing output did not cancel the tool")
	}
}
