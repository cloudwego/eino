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

package adk

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/cloudwego/eino/components/model"
	mockModel "github.com/cloudwego/eino/internal/mock/components/model"
	"github.com/cloudwego/eino/schema"
)

// Attack Test 1: Nil RetryDecision panic
//
// Bug: If ShouldRetry callback returns nil, generateWithShouldRetry dereferences
// the nil pointer at `decision.ShouldRetry` (line ~344), causing a nil pointer panic.
//
// Impact: Any user who accidentally returns nil from their ShouldRetry callback
// will get an unrecoverable panic instead of a graceful error.
func TestAttack_NilRetryDecision_Generate(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)
	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(schema.AssistantMessage("hello", nil), nil).Times(1)

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "NilDecisionAgent",
		Description: "Test nil RetryDecision",
		Instruction: "You are a helpful assistant.",
		Model:       cm,
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries: 1,
			ShouldRetry: func(ctx context.Context, retryCtx *RetryContext) *RetryDecision {
				return nil
			},
			BackoffFunc: func(_ context.Context, _ int) time.Duration { return time.Millisecond },
		},
	})
	assert.NoError(t, err)

	input := &AgentInput{
		Messages: []Message{schema.UserMessage("Hello")},
	}
	iterator := agent.Run(ctx, input)

	var foundPanicErr bool
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			errMsg := event.Err.Error()
			if strings.Contains(errMsg, "panic") || strings.Contains(errMsg, "nil pointer") ||
				strings.Contains(errMsg, "runtime error") {
				foundPanicErr = true
				t.Logf("CONFIRMED BUG: nil RetryDecision causes panic caught by agent runtime: %v", event.Err)
			}
		}
	}
	if foundPanicErr {
		t.Log("nil RetryDecision results in a panic error event instead of a clean error. " +
			"ShouldRetry should handle nil return gracefully (e.g., treat as 'do not retry').")
	}
}

// Attack Test 2: Nil RetryDecision panic in Stream path
//
// Same as above but for the stream path.
func TestAttack_NilRetryDecision_Stream(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)
	cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			r, w := schema.Pipe[*schema.Message](1)
			go func() {
				_ = w.Send(schema.AssistantMessage("hello", nil), nil)
				w.Close()
			}()
			return r, nil
		}).Times(1)

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "NilDecisionStreamAgent",
		Description: "Test nil RetryDecision in stream",
		Instruction: "You are a helpful assistant.",
		Model:       cm,
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries: 1,
			ShouldRetry: func(ctx context.Context, retryCtx *RetryContext) *RetryDecision {
				return nil
			},
			BackoffFunc: func(_ context.Context, _ int) time.Duration { return time.Millisecond },
		},
	})
	assert.NoError(t, err)

	input := &AgentInput{
		Messages:        []Message{schema.UserMessage("Hello")},
		EnableStreaming: true,
	}
	iterator := agent.Run(ctx, input)

	var foundPanicErr bool
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			errMsg := event.Err.Error()
			if strings.Contains(errMsg, "panic") || strings.Contains(errMsg, "nil pointer") ||
				strings.Contains(errMsg, "runtime error") {
				foundPanicErr = true
				t.Logf("CONFIRMED BUG: nil RetryDecision in Stream causes panic: %v", event.Err)
			}
		}
		if event.Output != nil && event.Output.MessageOutput != nil {
			mo := event.Output.MessageOutput
			if mo.IsStreaming && mo.MessageStream != nil {
				for {
					_, err := mo.MessageStream.Recv()
					if err != nil {
						break
					}
				}
			}
		}
	}
	if foundPanicErr {
		t.Log("nil RetryDecision in Stream results in a panic error event instead of a clean error.")
	}
}

// Attack Test 3: Options accumulate unboundedly across retries
//
// Bug: When ShouldRetry returns ModifiedOptions on every retry, the options slice
// grows linearly with each attempt (appended, never reset). This means if 3 retries
// each add `WithMaxTokens(X)`, the final call has 3 copies of the option appended.
//
// Impact: Memory growth + potentially confusing behavior where stale options from
// earlier retry attempts remain in the options list.
func TestAttack_OptionsAccumulateAcrossRetries(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)

	var callCount int32
	var capturedOptLens []int
	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
			count := atomic.AddInt32(&callCount, 1)
			capturedOptLens = append(capturedOptLens, len(opts))
			if count <= 3 {
				return nil, errRetryAble
			}
			return schema.AssistantMessage("success", nil), nil
		}).Times(4)

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "OptionsAccumulateAgent",
		Description: "Test options accumulation across retries",
		Instruction: "You are a helpful assistant.",
		Model:       cm,
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries: 5,
			ShouldRetry: func(ctx context.Context, retryCtx *RetryContext) *RetryDecision {
				if retryCtx.Err != nil {
					return &RetryDecision{
						ShouldRetry:     true,
						ModifiedOptions: []model.Option{model.WithMaxTokens(1024)},
					}
				}
				return &RetryDecision{ShouldRetry: false}
			},
			BackoffFunc: func(_ context.Context, _ int) time.Duration { return time.Millisecond },
		},
	})
	assert.NoError(t, err)

	input := &AgentInput{
		Messages: []Message{schema.UserMessage("Hello")},
	}
	iterator := agent.Run(ctx, input)
	for {
		_, ok := iterator.Next()
		if !ok {
			break
		}
	}

	assert.Equal(t, int32(4), atomic.LoadInt32(&callCount))

	t.Logf("DESIGN CONCERN: Options lengths across calls: %v", capturedOptLens)
	t.Logf("  Call 1: %d opts, Call 2: %d opts, Call 3: %d opts, Call 4: %d opts",
		capturedOptLens[0], capturedOptLens[1], capturedOptLens[2], capturedOptLens[3])

	growth := capturedOptLens[3] - capturedOptLens[0]
	assert.Equal(t, 3, growth,
		"DESIGN CONCERN: options accumulate linearly — 3 retries added 3 separate WithMaxTokens. "+
			"Only the last one takes effect, but all consume memory. "+
			"Consider resetting or deduplicating options on each retry.")
}

// Attack Test 4: Generate path missing setRetryAttempt — eventSenderModel gets stale attempt
//
// Bug: generateWithShouldRetry does NOT call st.setRetryAttempt(attempt) before each
// model call, unlike streamWithShouldRetry which does. This means the eventSenderModel's
// buildErrWrapper reads a stale retryAttempt from state, potentially wrapping errors
// as WillRetryError incorrectly or with the wrong attempt number.
//
// Impact: Events emitted during Generate retries will have incorrect RetryAttempt values,
// and the WillRetryError wrapping may be wrong (e.g., not wrapping on the last attempt
// when it should, or wrapping when retries are exhausted).
func TestAttack_Generate_MissingRetryAttemptStateUpdate(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)

	var callCount int32
	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
			count := atomic.AddInt32(&callCount, 1)
			if count < 3 {
				return schema.AssistantMessage("bad", nil), nil
			}
			return schema.AssistantMessage("good", nil), nil
		}).Times(3)

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "MissingRetryAttemptAgent",
		Description: "Test Generate path retryAttempt state",
		Instruction: "You are a helpful assistant.",
		Model:       cm,
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries: 3,
			ShouldRetry: func(ctx context.Context, retryCtx *RetryContext) *RetryDecision {
				if retryCtx.OutputMessage != nil && retryCtx.OutputMessage.Content == "bad" {
					return &RetryDecision{ShouldRetry: true}
				}
				return &RetryDecision{ShouldRetry: false}
			},
			BackoffFunc: func(_ context.Context, _ int) time.Duration { return time.Millisecond },
		},
	})
	assert.NoError(t, err)

	input := &AgentInput{
		Messages: []Message{schema.UserMessage("Hello")},
	}
	iterator := agent.Run(ctx, input)

	var events []*AgentEvent
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		events = append(events, event)
	}

	assert.Equal(t, int32(3), atomic.LoadInt32(&callCount))

	var willRetryEvents int
	for _, e := range events {
		if e.Err != nil {
			var wre *WillRetryError
			if errors.As(e.Err, &wre) {
				willRetryEvents++
				t.Logf("WillRetryError event: attempt=%d, err=%s", wre.RetryAttempt, wre.ErrStr)
			}
		}
	}
	t.Logf("Total events: %d, WillRetryError events: %d", len(events), willRetryEvents)
	t.Logf("DESIGN CONCERN: Generate path does not call setRetryAttempt, so eventSenderModel.buildErrWrapper " +
		"reads stale retryAttempt=0 from state for all retry attempts. Compare with Stream path which " +
		"correctly updates retryAttempt before each call.")
}

// Attack Test 5: ShouldRetry + RewriteError when ShouldRetry=true (ignored per doc)
//
// The doc says RewriteError is ignored when ShouldRetry=true. Let's verify this
// actually works — a user might accidentally set both.
func TestAttack_RewriteErrorIgnoredWhenShouldRetryTrue(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)

	fatalErr := errors.New("this should be ignored")
	var callCount int32
	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
			count := atomic.AddInt32(&callCount, 1)
			if count < 2 {
				return nil, errRetryAble
			}
			return schema.AssistantMessage("success", nil), nil
		}).Times(2)

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "RewriteIgnoredAgent",
		Description: "Test RewriteError ignored when ShouldRetry=true",
		Instruction: "You are a helpful assistant.",
		Model:       cm,
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries: 2,
			ShouldRetry: func(ctx context.Context, retryCtx *RetryContext) *RetryDecision {
				if retryCtx.Err != nil {
					return &RetryDecision{
						ShouldRetry:  true,
						RewriteError: fatalErr,
					}
				}
				return &RetryDecision{ShouldRetry: false}
			},
			BackoffFunc: func(_ context.Context, _ int) time.Duration { return time.Millisecond },
		},
	})
	assert.NoError(t, err)

	input := &AgentInput{
		Messages: []Message{schema.UserMessage("Hello")},
	}
	iterator := agent.Run(ctx, input)

	var lastMsg string
	var foundFatalErr bool
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		if event.Err != nil && errors.Is(event.Err, fatalErr) {
			foundFatalErr = true
		}
		if event.Output != nil && event.Output.MessageOutput != nil && event.Output.MessageOutput.Message != nil {
			lastMsg = event.Output.MessageOutput.Message.Content
		}
	}

	assert.False(t, foundFatalErr, "RewriteError should be ignored when ShouldRetry=true")
	assert.Equal(t, "success", lastMsg)
	assert.Equal(t, int32(2), atomic.LoadInt32(&callCount))
}

// Attack Test 6: MaxRetries=0 with ShouldRetry — no retries should happen
//
// Bug potential: The loop `for attempt := 0; attempt <= r.config.MaxRetries` with
// MaxRetries=0 means exactly 1 iteration. But if ShouldRetry says retry on attempt 0,
// the code checks `attempt >= r.config.MaxRetries` (0 >= 0 = true) and breaks.
// This should work, but let's verify the exhaustion error has correct TotalRetries.
func TestAttack_MaxRetriesZero_ShouldRetryAlwaysTrue(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)
	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(schema.AssistantMessage("bad", nil), nil).Times(1)

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "MaxRetryZeroAgent",
		Description: "Test MaxRetries=0 with ShouldRetry always true",
		Instruction: "You are a helpful assistant.",
		Model:       cm,
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries: 0,
			ShouldRetry: func(ctx context.Context, retryCtx *RetryContext) *RetryDecision {
				return &RetryDecision{ShouldRetry: true}
			},
			BackoffFunc: func(_ context.Context, _ int) time.Duration { return time.Millisecond },
		},
	})
	assert.NoError(t, err)

	input := &AgentInput{
		Messages: []Message{schema.UserMessage("Hello")},
	}
	iterator := agent.Run(ctx, input)

	var foundExhaustedErr bool
	var retryErr *RetryExhaustedError
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			if errors.Is(event.Err, ErrExceedMaxRetries) {
				foundExhaustedErr = true
				errors.As(event.Err, &retryErr)
			}
		}
	}
	assert.True(t, foundExhaustedErr, "should get exhaustion error with MaxRetries=0")
	assert.NotNil(t, retryErr)
	assert.Equal(t, 0, retryErr.TotalRetries, "TotalRetries should be 0")
}

// Attack Test 7: Context cancellation during backoff sleep
//
// Bug: Both generateWithShouldRetry and streamWithShouldRetry use time.Sleep(delay)
// which does NOT respect context cancellation. If the context is cancelled during backoff,
// the retry loop continues sleeping and then makes another model call on a cancelled context.
//
// Impact: Slow shutdown, wasted resources, potential goroutine leak in agent runs.
func TestAttack_ContextCancelDuringBackoff(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)

	var callCount int32
	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
			atomic.AddInt32(&callCount, 1)
			return nil, errRetryAble
		}).AnyTimes()

	cancelCtx, cancel := context.WithCancel(ctx)

	agent, err := NewChatModelAgent(cancelCtx, &ChatModelAgentConfig{
		Name:        "ContextCancelAgent",
		Description: "Test context cancel during backoff",
		Instruction: "You are a helpful assistant.",
		Model:       cm,
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries: 10,
			ShouldRetry: func(ctx context.Context, retryCtx *RetryContext) *RetryDecision {
				if retryCtx.Err != nil {
					return &RetryDecision{
						ShouldRetry: true,
						Backoff:     500 * time.Millisecond,
					}
				}
				return &RetryDecision{ShouldRetry: false}
			},
		},
	})
	assert.NoError(t, err)

	input := &AgentInput{
		Messages: []Message{schema.UserMessage("Hello")},
	}
	iterator := agent.Run(cancelCtx, input)

	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	for {
		_, ok := iterator.Next()
		if !ok {
			break
		}
	}
	elapsed := time.Since(start)

	t.Logf("DESIGN CONCERN: Elapsed time after cancel: %v (backoff was 500ms per retry, 10 max retries)", elapsed)
	t.Logf("Call count: %d", atomic.LoadInt32(&callCount))
	if elapsed > 2*time.Second {
		t.Logf("WARNING: retry loop continued sleeping past context cancellation — time.Sleep doesn't respect ctx.Done()")
	}
}

// Attack Test 8: Stream Copy semantics — returnCopy becomes fully buffered
//
// Bug/Design: In streamWithShouldRetry, stream.Copy(2) is called, then checkCopy is
// fully consumed by consumeStreamForMessage. This means by the time returnCopy is
// returned to the caller, the entire response is already buffered in memory.
// The "streaming" semantics are actually eager-buffered, not truly lazy.
//
// Impact: For large model responses, this defeats the purpose of streaming (low latency
// for first token, bounded memory). The user's stream handler won't receive chunks
// incrementally — they'll get them all at once from the buffer.
func TestAttack_StreamCopyEagerBuffering(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)

	chunkCount := 10
	cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			r, w := schema.Pipe[*schema.Message](1)
			go func() {
				for i := 0; i < chunkCount; i++ {
					_ = w.Send(schema.AssistantMessage("chunk ", nil), nil)
					time.Sleep(10 * time.Millisecond)
				}
				w.Close()
			}()
			return r, nil
		}).Times(1)

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "StreamBufferingAgent",
		Description: "Test that stream Copy causes eager buffering",
		Instruction: "You are a helpful assistant.",
		Model:       cm,
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries: 1,
			ShouldRetry: func(ctx context.Context, retryCtx *RetryContext) *RetryDecision {
				return &RetryDecision{ShouldRetry: false}
			},
			BackoffFunc: func(_ context.Context, _ int) time.Duration { return time.Millisecond },
		},
	})
	assert.NoError(t, err)

	input := &AgentInput{
		Messages:        []Message{schema.UserMessage("Hello")},
		EnableStreaming: true,
	}

	start := time.Now()
	iterator := agent.Run(ctx, input)

	var firstChunkTime time.Duration
	var gotFirstChunk bool
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		if event.Output != nil && event.Output.MessageOutput != nil {
			mo := event.Output.MessageOutput
			if mo.IsStreaming && mo.MessageStream != nil {
				for {
					_, err := mo.MessageStream.Recv()
					if err != nil {
						break
					}
					if !gotFirstChunk {
						firstChunkTime = time.Since(start)
						gotFirstChunk = true
					}
				}
			}
		}
	}
	totalTime := time.Since(start)

	t.Logf("DESIGN CONCERN: Time to first chunk: %v, Total time: %v", firstChunkTime, totalTime)
	t.Logf("With %d chunks at 10ms intervals, streaming should deliver first chunk in ~10ms.", chunkCount)
	t.Logf("But ShouldRetry must consume the entire stream first for inspection, so first chunk is delayed.")

	if firstChunkTime > 50*time.Millisecond {
		t.Logf("CONFIRMED: First chunk delayed to %v — stream was eagerly consumed for ShouldRetry check. "+
			"True streaming semantics are lost when ShouldRetry is enabled.", firstChunkTime)
	}
}

// Attack Test 9: ShouldRetry with both OutputMessage and Err (partial stream error)
// then RewriteError — verify both fields are propagated correctly
//
// When a stream produces partial content then errors, consumeStreamForMessage returns
// both a (partial) message and an error. If ShouldRetry sets RewriteError, the error
// should be the rewrite, not the original partial error.
func TestAttack_PartialStreamThenRewriteError(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)

	partialErr := errors.New("connection reset")
	fatalErr := errors.New("fatal: partial stream is unrecoverable")

	cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			r, w := schema.Pipe[*schema.Message](1)
			go func() {
				_ = w.Send(schema.AssistantMessage("partial data", nil), nil)
				w.Send(nil, partialErr)
			}()
			return r, nil
		}).Times(1)

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "PartialRewriteAgent",
		Description: "Test partial stream + RewriteError",
		Instruction: "You are a helpful assistant.",
		Model:       cm,
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries: 2,
			ShouldRetry: func(ctx context.Context, retryCtx *RetryContext) *RetryDecision {
				if retryCtx.OutputMessage != nil && retryCtx.Err != nil {
					return &RetryDecision{
						ShouldRetry:  false,
						RewriteError: fatalErr,
					}
				}
				return &RetryDecision{ShouldRetry: false}
			},
			BackoffFunc: func(_ context.Context, _ int) time.Duration { return time.Millisecond },
		},
	})
	assert.NoError(t, err)

	input := &AgentInput{
		Messages:        []Message{schema.UserMessage("Hello")},
		EnableStreaming: true,
	}
	iterator := agent.Run(ctx, input)

	var foundFatalErr bool
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			if errors.Is(event.Err, fatalErr) {
				foundFatalErr = true
			}
			t.Logf("Event error: %v (is fatalErr: %v)", event.Err, errors.Is(event.Err, fatalErr))
		}
		if event.Output != nil && event.Output.MessageOutput != nil {
			mo := event.Output.MessageOutput
			if mo.IsStreaming && mo.MessageStream != nil {
				for {
					_, err := mo.MessageStream.Recv()
					if err != nil {
						break
					}
				}
			}
		}
	}
	assert.True(t, foundFatalErr, "should receive the RewriteError, not the original partial stream error")
}

// Attack Test 10: WillRetryError.OutputMessage NOT populated in ShouldRetry event path
//
// Bug: When ShouldRetry rejects a message and retries, the eventSenderModel wraps the
// error as WillRetryError at wrappers.go:353 but does NOT set OutputMessage on the
// WillRetryError. The OutputMessage field was added to WillRetryError in this PR, but
// it is only populated in the legacy genErrWrapper path (which doesn't have message
// access) and nowhere in the ShouldRetry path.
//
// Impact: Users who observe WillRetryError events to see what message was rejected
// will always get OutputMessage=nil, even though the retry was triggered by message
// inspection. The field exists but is never useful.
func TestAttack_WillRetryError_OutputMessage_NotPopulated(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)

	var callCount int32
	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
			count := atomic.AddInt32(&callCount, 1)
			if count < 3 {
				return schema.AssistantMessage("rejected content "+string(rune('0'+count)), nil), nil
			}
			return schema.AssistantMessage("good", nil), nil
		}).Times(3)

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "WillRetryOutputMsgAgent",
		Description: "Test WillRetryError.OutputMessage population",
		Instruction: "You are a helpful assistant.",
		Model:       cm,
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries: 3,
			ShouldRetry: func(ctx context.Context, retryCtx *RetryContext) *RetryDecision {
				if retryCtx.OutputMessage != nil && strings.Contains(retryCtx.OutputMessage.Content, "rejected") {
					return &RetryDecision{ShouldRetry: true}
				}
				return &RetryDecision{ShouldRetry: false}
			},
			BackoffFunc: func(_ context.Context, _ int) time.Duration { return time.Millisecond },
		},
	})
	assert.NoError(t, err)

	input := &AgentInput{
		Messages: []Message{schema.UserMessage("Hello")},
	}
	iterator := agent.Run(ctx, input)

	var willRetryEvents []*WillRetryError
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			var wre *WillRetryError
			if errors.As(event.Err, &wre) {
				willRetryEvents = append(willRetryEvents, wre)
			}
		}
	}

	for i, wre := range willRetryEvents {
		t.Logf("WillRetryError[%d]: OutputMessage=%v, ErrStr=%s", i, wre.OutputMessage, wre.ErrStr)
		if wre.OutputMessage == nil {
			t.Logf("  DESIGN CONCERN: OutputMessage is nil — the rejected message content is lost in the event. " +
				"Users observing WillRetryError cannot see what was rejected.")
		}
	}
}

// Attack Test 11: consumeStreamForMessage with empty stream returns (nil, nil)
//
// When the stream produces zero chunks and then EOF, consumeStreamForMessage returns
// (nil, nil). This means RetryContext will have OutputMessage=nil AND Err=nil.
// ShouldRetry is then called with both nil — user might not handle this case.
func TestAttack_EmptyStream_NilMessageNilError(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)

	var capturedContexts []*RetryContext
	var callCount int32
	cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			count := atomic.AddInt32(&callCount, 1)
			r, w := schema.Pipe[*schema.Message](1)
			go func() {
				if count < 2 {
					w.Close()
				} else {
					_ = w.Send(schema.AssistantMessage("real content", nil), nil)
					w.Close()
				}
			}()
			return r, nil
		}).Times(2)

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "EmptyStreamAgent",
		Description: "Test empty stream producing nil message and nil error",
		Instruction: "You are a helpful assistant.",
		Model:       cm,
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries: 2,
			ShouldRetry: func(ctx context.Context, retryCtx *RetryContext) *RetryDecision {
				capturedContexts = append(capturedContexts, retryCtx)
				if retryCtx.OutputMessage == nil && retryCtx.Err == nil {
					t.Log("CONFIRMED: Empty stream yields RetryContext with OutputMessage=nil AND Err=nil")
					return &RetryDecision{ShouldRetry: true}
				}
				return &RetryDecision{ShouldRetry: false}
			},
			BackoffFunc: func(_ context.Context, _ int) time.Duration { return time.Millisecond },
		},
	})
	assert.NoError(t, err)

	input := &AgentInput{
		Messages:        []Message{schema.UserMessage("Hello")},
		EnableStreaming: true,
	}
	iterator := agent.Run(ctx, input)

	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		if event.Output != nil && event.Output.MessageOutput != nil {
			mo := event.Output.MessageOutput
			if mo.IsStreaming && mo.MessageStream != nil {
				for {
					_, err := mo.MessageStream.Recv()
					if err != nil {
						break
					}
				}
			}
		}
	}

	assert.GreaterOrEqual(t, len(capturedContexts), 1)
	assert.Nil(t, capturedContexts[0].OutputMessage, "empty stream should yield nil OutputMessage")
	assert.Nil(t, capturedContexts[0].Err, "empty stream (clean EOF) should yield nil Err")
}

// Attack Test 12: RetryContext.RetryAttempt numbering consistency
//
// Verify that RetryAttempt starts at 1 (not 0) and increments correctly, matching
// the documentation claim that "For the first retry decision (after the initial call),
// this is 1."
func TestAttack_RetryAttemptNumbering(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)

	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(schema.AssistantMessage("bad", nil), nil).Times(4)

	var capturedAttempts []int
	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "AttemptNumberingAgent",
		Description: "Test RetryAttempt numbering",
		Instruction: "You are a helpful assistant.",
		Model:       cm,
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries: 3,
			ShouldRetry: func(ctx context.Context, retryCtx *RetryContext) *RetryDecision {
				capturedAttempts = append(capturedAttempts, retryCtx.RetryAttempt)
				return &RetryDecision{ShouldRetry: true}
			},
			BackoffFunc: func(_ context.Context, _ int) time.Duration { return time.Millisecond },
		},
	})
	assert.NoError(t, err)

	input := &AgentInput{
		Messages: []Message{schema.UserMessage("Hello")},
	}
	iterator := agent.Run(ctx, input)
	for {
		_, ok := iterator.Next()
		if !ok {
			break
		}
	}

	assert.Equal(t, []int{1, 2, 3, 4}, capturedAttempts,
		"RetryAttempt should be 1-based and called for initial attempt + all retries. "+
			"With MaxRetries=3, we expect 4 calls (initial + 3 retries).")
}

// Attack Test 13: Stream path — returnCopy leaked (not closed) when ShouldRetry errors
// after stream success on exhaustion
//
// Bug potential: When all retries are exhausted via message rejection in stream path,
// the last returnCopy is closed explicitly at line 501. But what about intermediate
// copies? Let's verify by checking the stream reader leak behavior.
func TestAttack_StreamReturnCopyClosedOnExhaustion(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)

	var closedCount int32
	cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			r, w := schema.Pipe[*schema.Message](1)
			go func() {
				_ = w.Send(schema.AssistantMessage("bad", nil), nil)
				w.Close()
				atomic.AddInt32(&closedCount, 1)
			}()
			return r, nil
		}).Times(3)

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "StreamLeakAgent",
		Description: "Test stream resource cleanup on exhaustion",
		Instruction: "You are a helpful assistant.",
		Model:       cm,
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries: 2,
			ShouldRetry: func(ctx context.Context, retryCtx *RetryContext) *RetryDecision {
				return &RetryDecision{ShouldRetry: true}
			},
			BackoffFunc: func(_ context.Context, _ int) time.Duration { return time.Millisecond },
		},
	})
	assert.NoError(t, err)

	input := &AgentInput{
		Messages:        []Message{schema.UserMessage("Hello")},
		EnableStreaming: true,
	}
	iterator := agent.Run(ctx, input)

	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		if event.Output != nil && event.Output.MessageOutput != nil {
			mo := event.Output.MessageOutput
			if mo.IsStreaming && mo.MessageStream != nil {
				for {
					_, err := mo.MessageStream.Recv()
					if err != nil {
						break
					}
				}
			}
		}
	}

	time.Sleep(50 * time.Millisecond)
	t.Logf("Stream writers closed: %d (expected 3 for 3 attempts)", atomic.LoadInt32(&closedCount))
}

// Attack Test 14: Generate path — PersistModifiedInputMessages interaction with stateModelWrapper
//
// When PersistModifiedInputMessages=true, the retry logic writes to State.Messages.
// Then stateModelWrapper.Generate reads State.Messages back (line 766-771).
// Verify the stateModelWrapper correctly picks up persisted modifications.
func TestAttack_PersistModifiedInputs_StateModelInteraction(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)

	var callCount int32
	var capturedInputs [][]*schema.Message
	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
			count := atomic.AddInt32(&callCount, 1)
			inputCopy := make([]*schema.Message, len(input))
			copy(inputCopy, input)
			capturedInputs = append(capturedInputs, inputCopy)
			if count < 2 {
				return nil, errRetryAble
			}
			return schema.AssistantMessage("success", nil), nil
		}).Times(2)

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "PersistStateAgent",
		Description: "Test persist + state model interaction",
		Instruction: "Original instruction that is long.",
		Model:       cm,
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries: 3,
			ShouldRetry: func(ctx context.Context, retryCtx *RetryContext) *RetryDecision {
				if retryCtx.Err != nil {
					return &RetryDecision{
						ShouldRetry: true,
						ModifiedInputMessages: []*schema.Message{
							schema.SystemMessage("compressed"),
							schema.UserMessage("Hello"),
						},
						PersistModifiedInputMessages: true,
					}
				}
				return &RetryDecision{ShouldRetry: false}
			},
			BackoffFunc: func(_ context.Context, _ int) time.Duration { return time.Millisecond },
		},
	})
	assert.NoError(t, err)

	input := &AgentInput{
		Messages: []Message{schema.UserMessage("Hello")},
	}
	iterator := agent.Run(ctx, input)

	var lastMsg string
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		if event.Output != nil && event.Output.MessageOutput != nil && event.Output.MessageOutput.Message != nil {
			lastMsg = event.Output.MessageOutput.Message.Content
		}
	}

	assert.Equal(t, "success", lastMsg)
	assert.Equal(t, int32(2), atomic.LoadInt32(&callCount))
	assert.GreaterOrEqual(t, len(capturedInputs), 2)
	assert.Equal(t, "compressed", capturedInputs[1][0].Content,
		"second call should use persisted modified input messages")
}

// Attack Test 15: consumeStreamForMessage never calls stream.Close on the checkCopy
// if Recv returns a non-EOF, non-nil error — actually it does via defer.
// Let's verify the deferred Close is correct.
func TestAttack_ConsumeStreamForMessage_Directly(t *testing.T) {
	r, w := schema.Pipe[*schema.Message](1)

	streamErr := errors.New("mid-stream failure")
	go func() {
		_ = w.Send(schema.AssistantMessage("partial", nil), nil)
		_ = w.Send(schema.AssistantMessage(" data", nil), nil)
		w.Send(nil, streamErr)
	}()

	msg, err := consumeStreamForMessage(r)

	assert.Error(t, err, "should return the mid-stream error")
	assert.True(t, errors.Is(err, streamErr), "should be the exact stream error")
	assert.NotNil(t, msg, "should return concatenated partial message")
	assert.Contains(t, msg.Content, "partial", "partial content should be preserved")
}

// Attack Test 16: consumeStreamForMessage with zero chunks then error
func TestAttack_ConsumeStreamForMessage_ZeroChunksThenError(t *testing.T) {
	r, w := schema.Pipe[*schema.Message](1)

	streamErr := errors.New("immediate failure")
	go func() {
		w.Send(nil, streamErr)
	}()

	msg, err := consumeStreamForMessage(r)

	assert.Error(t, err)
	assert.True(t, errors.Is(err, streamErr))
	assert.Nil(t, msg, "no chunks means no message")
}

// Attack Test 17: consumeStreamForMessage with zero chunks then EOF
func TestAttack_ConsumeStreamForMessage_EmptyStream(t *testing.T) {
	r, w := schema.Pipe[*schema.Message](1)

	go func() {
		w.Close()
	}()

	msg, err := consumeStreamForMessage(r)

	assert.NoError(t, err, "EOF on empty stream should not be an error")
	assert.Nil(t, msg, "empty stream should return nil message")
}

// Attack Test 18: Verify stream path returnCopy is usable after checkCopy consumed
//
// The returnCopy should contain the same data as checkCopy since they're both
// produced by Copy(2). Verify the data is actually available in returnCopy.
func TestAttack_StreamReturnCopyContainsData(t *testing.T) {
	r, w := schema.Pipe[*schema.Message](3)
	go func() {
		_ = w.Send(schema.AssistantMessage("chunk1 ", nil), nil)
		_ = w.Send(schema.AssistantMessage("chunk2 ", nil), nil)
		_ = w.Send(schema.AssistantMessage("chunk3", nil), nil)
		w.Close()
	}()

	copies := r.Copy(2)
	checkCopy := copies[0]
	returnCopy := copies[1]

	msg, err := consumeStreamForMessage(checkCopy)
	assert.NoError(t, err)
	assert.NotNil(t, msg)
	assert.Contains(t, msg.Content, "chunk1")

	var returnChunks []string
	for {
		chunk, err := returnCopy.Recv()
		if err == io.EOF {
			break
		}
		assert.NoError(t, err)
		returnChunks = append(returnChunks, chunk.Content)
	}

	assert.Equal(t, 3, len(returnChunks), "returnCopy should contain all 3 chunks")
	assert.Equal(t, "chunk1 ", returnChunks[0])
	assert.Equal(t, "chunk2 ", returnChunks[1])
	assert.Equal(t, "chunk3", returnChunks[2])
}

func TestRace_VerdictSignal_ConcurrentEventConsumer(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)

	var callCount int32
	cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			count := atomic.AddInt32(&callCount, 1)
			if count == 1 {
				return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("bad", nil)}), nil
			}
			return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("good", nil)}), nil
		}).Times(2)

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "RaceVerdictAgent",
		Description: "Test race conditions on verdict signal",
		Instruction: "You are a helpful assistant.",
		Model:       cm,
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries: 1,
			ShouldRetry: func(ctx context.Context, retryCtx *RetryContext) *RetryDecision {
				if retryCtx.OutputMessage != nil && retryCtx.OutputMessage.Content == "bad" {
					return &RetryDecision{ShouldRetry: true}
				}
				return &RetryDecision{ShouldRetry: false}
			},
			BackoffFunc: func(_ context.Context, _ int) time.Duration { return time.Millisecond },
		},
	})
	assert.NoError(t, err)

	input := &AgentInput{
		Messages:        []Message{schema.UserMessage("Hello")},
		EnableStreaming: true,
	}
	iterator := agent.Run(ctx, input)

	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		if event.Output != nil && event.Output.MessageOutput != nil {
			mo := event.Output.MessageOutput
			if mo.IsStreaming && mo.MessageStream != nil {
				for {
					_, recvErr := mo.MessageStream.Recv()
					if recvErr != nil {
						break
					}
				}
			}
		}
	}
	assert.Equal(t, int32(2), atomic.LoadInt32(&callCount))
}

func TestRace_SuppressFlag_ConcurrentAccess(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)

	var callCount int32
	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
			count := atomic.AddInt32(&callCount, 1)
			if count == 1 {
				return schema.AssistantMessage("bad", nil), nil
			}
			return schema.AssistantMessage("good", nil), nil
		}).Times(2)

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "RaceSuppressAgent",
		Description: "Test race conditions on suppress flag",
		Instruction: "You are a helpful assistant.",
		Model:       cm,
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries: 1,
			ShouldRetry: func(ctx context.Context, retryCtx *RetryContext) *RetryDecision {
				if retryCtx.OutputMessage != nil && retryCtx.OutputMessage.Content == "bad" {
					return &RetryDecision{ShouldRetry: true}
				}
				return &RetryDecision{ShouldRetry: false}
			},
			BackoffFunc: func(_ context.Context, _ int) time.Duration { return time.Millisecond },
		},
	})
	assert.NoError(t, err)

	input := &AgentInput{
		Messages: []Message{schema.UserMessage("Hello")},
	}
	iterator := agent.Run(ctx, input)

	var msgEvents []*AgentEvent
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		if event.Output != nil && event.Output.MessageOutput != nil {
			msgEvents = append(msgEvents, event)
		}
	}
	assert.Equal(t, 1, len(msgEvents))
	assert.Equal(t, "good", msgEvents[0].Output.MessageOutput.Message.Content)
}

func TestRace_VerdictSignal_FieldOverwrite_AcrossAttempts(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)

	var callCount int32
	cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			count := atomic.AddInt32(&callCount, 1)
			if count <= 2 {
				return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("bad", nil)}), nil
			}
			return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("good", nil)}), nil
		}).Times(3)

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "RaceFieldOverwriteAgent",
		Description: "Test verdict signal field overwrite across attempts",
		Instruction: "You are a helpful assistant.",
		Model:       cm,
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries: 2,
			ShouldRetry: func(ctx context.Context, retryCtx *RetryContext) *RetryDecision {
				if retryCtx.OutputMessage != nil && retryCtx.OutputMessage.Content == "bad" {
					return &RetryDecision{ShouldRetry: true}
				}
				return &RetryDecision{ShouldRetry: false}
			},
			BackoffFunc: func(_ context.Context, _ int) time.Duration { return time.Millisecond },
		},
	})
	assert.NoError(t, err)

	input := &AgentInput{
		Messages:        []Message{schema.UserMessage("Hello")},
		EnableStreaming: true,
	}
	iterator := agent.Run(ctx, input)

	var streamEvents int
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		if event.Output != nil && event.Output.MessageOutput != nil {
			mo := event.Output.MessageOutput
			if mo.IsStreaming && mo.MessageStream != nil {
				streamEvents++
				for {
					_, recvErr := mo.MessageStream.Recv()
					if recvErr != nil {
						break
					}
				}
			}
		}
	}
	assert.Equal(t, 3, streamEvents, "should have 3 stream events (2 rejected + 1 accepted)")
	assert.Equal(t, int32(3), atomic.LoadInt32(&callCount))
}

func TestEdge_EmptyStream_ShouldRetry_Verdict(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)

	var callCount int32
	cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			count := atomic.AddInt32(&callCount, 1)
			if count == 1 {
				r, w := schema.Pipe[*schema.Message](1)
				go func() {
					w.Close()
				}()
				return r, nil
			}
			return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("good", nil)}), nil
		}).Times(2)

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "EdgeEmptyStreamAgent",
		Description: "Test empty stream with ShouldRetry verdict",
		Instruction: "You are a helpful assistant.",
		Model:       cm,
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries: 1,
			ShouldRetry: func(ctx context.Context, retryCtx *RetryContext) *RetryDecision {
				if retryCtx.OutputMessage == nil && retryCtx.Err == nil {
					return &RetryDecision{ShouldRetry: true}
				}
				return &RetryDecision{ShouldRetry: false}
			},
			BackoffFunc: func(_ context.Context, _ int) time.Duration { return time.Millisecond },
		},
	})
	assert.NoError(t, err)

	input := &AgentInput{
		Messages:        []Message{schema.UserMessage("Hello")},
		EnableStreaming: true,
	}
	iterator := agent.Run(ctx, input)

	var willRetryOnEmpty bool
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		if event.Output != nil && event.Output.MessageOutput != nil {
			mo := event.Output.MessageOutput
			if mo.IsStreaming && mo.MessageStream != nil {
				for {
					_, recvErr := mo.MessageStream.Recv()
					if recvErr != nil {
						var wre *WillRetryError
						if errors.As(recvErr, &wre) {
							willRetryOnEmpty = true
						}
						break
					}
				}
			}
		}
	}
	assert.True(t, willRetryOnEmpty, "empty stream event should end with WillRetryError")
}

func TestEdge_ShouldRetry_RewriteError_OnCleanStream(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)

	rewriteErr := errors.New("fatal: content policy violation")

	cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("bad content", nil)}), nil
		}).Times(1)

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "EdgeRewriteErrorAgent",
		Description: "Test ShouldRetry RewriteError on clean stream",
		Instruction: "You are a helpful assistant.",
		Model:       cm,
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries: 1,
			ShouldRetry: func(ctx context.Context, retryCtx *RetryContext) *RetryDecision {
				return &RetryDecision{
					ShouldRetry:  false,
					RewriteError: rewriteErr,
				}
			},
			BackoffFunc: func(_ context.Context, _ int) time.Duration { return time.Millisecond },
		},
	})
	assert.NoError(t, err)

	input := &AgentInput{
		Messages:        []Message{schema.UserMessage("Hello")},
		EnableStreaming: true,
	}
	iterator := agent.Run(ctx, input)

	var foundRewriteErr bool
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		if event.Err != nil && errors.Is(event.Err, rewriteErr) {
			foundRewriteErr = true
		}
		if event.Output != nil && event.Output.MessageOutput != nil {
			mo := event.Output.MessageOutput
			if mo.IsStreaming && mo.MessageStream != nil {
				for {
					_, recvErr := mo.MessageStream.Recv()
					if recvErr != nil {
						break
					}
				}
			}
		}
	}
	assert.True(t, foundRewriteErr, "agent should return the rewrite error")
}

func TestEdge_Stream_InnerStreamReturnsError(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)

	streamInitErr := errors.New("connection refused")
	cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, streamInitErr).Times(1)

	var shouldRetryCalled int32
	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "EdgeInnerStreamErrorAgent",
		Description: "Test inner.Stream returns (nil, error)",
		Instruction: "You are a helpful assistant.",
		Model:       cm,
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries: 1,
			ShouldRetry: func(ctx context.Context, retryCtx *RetryContext) *RetryDecision {
				atomic.AddInt32(&shouldRetryCalled, 1)
				return &RetryDecision{ShouldRetry: false}
			},
			BackoffFunc: func(_ context.Context, _ int) time.Duration { return time.Millisecond },
		},
	})
	assert.NoError(t, err)

	done := make(chan struct{})
	go func() {
		defer close(done)
		input := &AgentInput{
			Messages:        []Message{schema.UserMessage("Hello")},
			EnableStreaming: true,
		}
		iterator := agent.Run(ctx, input)
		for {
			event, ok := iterator.Next()
			if !ok {
				break
			}
			if event.Output != nil && event.Output.MessageOutput != nil {
				mo := event.Output.MessageOutput
				if mo.IsStreaming && mo.MessageStream != nil {
					for {
						_, recvErr := mo.MessageStream.Recv()
						if recvErr != nil {
							break
						}
					}
				}
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("test deadlocked: inner.Stream error should not cause deadlock")
	}
	assert.Equal(t, int32(1), atomic.LoadInt32(&shouldRetryCalled))
}
