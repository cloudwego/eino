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
	"fmt"
	"io"
	"log"
	"math/rand"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

var (
	// ErrExceedMaxRetries is returned when the maximum number of retries has been exceeded.
	// Use errors.Is to check if an error is due to max retries being exceeded:
	//
	//   if errors.Is(err, adk.ErrExceedMaxRetries) {
	//       // handle max retries exceeded
	//   }
	//
	// Use errors.As to extract the underlying RetryExhaustedError for the last error details:
	//
	//   var retryErr *adk.RetryExhaustedError
	//   if errors.As(err, &retryErr) {
	//       fmt.Printf("last error was: %v\n", retryErr.LastErr)
	//   }
	ErrExceedMaxRetries = errors.New("exceeds max retries")
)

// RetryExhaustedError is returned when all retry attempts have been exhausted.
// It wraps the last error that occurred during retry attempts.
type RetryExhaustedError struct {
	LastErr      error
	TotalRetries int
}

func (e *RetryExhaustedError) Error() string {
	if e.LastErr != nil {
		return fmt.Sprintf("exceeds max retries: last error: %v", e.LastErr)
	}
	return "exceeds max retries"
}

func (e *RetryExhaustedError) Unwrap() error {
	return ErrExceedMaxRetries
}

// WillRetryError is emitted when a retryable error occurs and a retry will be attempted.
// It allows end-users to observe retry events in real-time via AgentEvent.
//
// Field design rationale:
//   - ErrStr (exported): Stores the error message string for Gob serialization during checkpointing.
//     This ensures the error message is preserved after checkpoint restore.
//   - err (unexported): Stores the original error for Unwrap() support at runtime.
//     This field is intentionally unexported because Gob serialization would fail for unregistered
//     concrete error types. Since end-users only need the original error when the AgentEvent first
//     occurs (not after restoring from checkpoint), skipping serialization is acceptable.
//     After checkpoint restore, err will be nil and Unwrap() returns nil.
type WillRetryError struct {
	ErrStr       string
	RetryAttempt int
	// OutputMessage is the model's output message from the attempt that triggered the retry, if any.
	// May be nil if the model returned an error without producing a message.
	// Note: in the ShouldRetry path, this field is currently not populated on WillRetryError
	// events emitted to the event stream, because the event sender layer does not have access
	// to the retry decision. It is only populated in RetryContext passed to ShouldRetry itself.
	OutputMessage *schema.Message
	err           error
}

func (e *WillRetryError) Error() string {
	return e.ErrStr
}

func (e *WillRetryError) Unwrap() error {
	return e.err
}

func init() {
	schema.RegisterName[*WillRetryError]("eino_adk_chatmodel_will_retry_error")
}

// RetryContext contains context information passed to ModelRetryConfig.ShouldRetry
// during a retry decision.
type RetryContext struct {
	// RetryAttempt is the current retry attempt number (1-based).
	// For the first retry decision (after the initial call), this is 1.
	RetryAttempt int

	// InputMessages is the input messages that were sent to the model for the current attempt.
	InputMessages []*schema.Message

	// Options is the model options that were used for the current attempt.
	Options []model.Option

	// OutputMessage is the output message from the model, if any.
	// This is non-nil when the model returned a message successfully.
	// For streaming, this is the fully concatenated message (the entire stream is consumed
	// before ShouldRetry is called, which means streaming latency benefits are deferred
	// until the decision is made).
	// May be nil if the model returned an error without producing a message, or if the
	// stream was empty (zero chunks before EOF).
	OutputMessage *schema.Message

	// Err is the error from the model call, if any.
	// May be nil if the model produced a message without error.
	// Note: both OutputMessage and Err can be nil simultaneously for empty streams.
	Err error
}

// RetryDecision represents the decision made by ModelRetryConfig.ShouldRetry.
type RetryDecision struct {
	// ShouldRetry indicates whether the model call should be retried.
	// If false, the model output (or error) is accepted as-is, unless RewriteError is set.
	ShouldRetry bool

	// RewriteError, when non-nil, overrides the return value of the model call with this error.
	// The agent run will fail with this error.
	//
	// This is useful for two scenarios:
	//   - When the model returns a "seemingly correct" message (no error) that actually
	//     contains unrecoverable issues. RewriteError converts the successful output
	//     into a fatal error.
	//   - When the model returns an error, but you want to replace it with a different,
	//     more descriptive error (e.g., adding context or wrapping).
	//
	// When ShouldRetry is true, RewriteError is ignored.
	// When ShouldRetry is false and RewriteError is non-nil, the model call returns
	// RewriteError regardless of whether the original call had an error or a message.
	RewriteError error

	// ModifiedInputMessages, when non-nil, replaces the input messages for the next retry.
	//
	// This enables advanced recovery strategies like context compression or message trimming.
	// Only used when ShouldRetry is true. Ignored when ShouldRetry is false.
	ModifiedInputMessages []*schema.Message

	// PersistModifiedInputMessages controls whether ModifiedInputMessages are written
	// back to the agent's State, affecting subsequent model calls in the agent loop
	// (not just the next retry attempt).
	//
	// When true, the modified messages are persisted to State via compose.ProcessState.
	// When false (default), the modified messages are only used for the next retry attempt
	// within this retry cycle.
	//
	// Only used when ShouldRetry is true and ModifiedInputMessages is non-nil.
	PersistModifiedInputMessages bool

	// ModifiedOptions, when non-nil, provides additional model options for the next retry.
	// These options are appended to the existing options, taking precedence via last-wins semantics.
	//
	// This enables adjustments like increasing MaxTokens for the retry attempt.
	// Note: options accumulate across retries. If ShouldRetry returns ModifiedOptions on every
	// attempt, each set is appended to the previous ones. Only the last value for each option
	// key takes effect, but earlier values remain in the slice.
	// Only used when ShouldRetry is true. Ignored when ShouldRetry is false.
	ModifiedOptions []model.Option

	// Backoff specifies the duration to wait before the next retry attempt.
	// If zero, the default backoff function (from ModelRetryConfig.BackoffFunc or the
	// built-in exponential backoff) is used.
	//
	// This allows the ShouldRetry callback to dynamically control retry timing based on
	// the specific error or problematic message encountered.
	// Only used when ShouldRetry is true. Ignored when ShouldRetry is false.
	Backoff time.Duration
}

// ModelRetryConfig configures retry behavior for the ChatModel node.
// It defines how the agent should handle transient failures when calling the ChatModel.
type ModelRetryConfig struct {
	// MaxRetries specifies the maximum number of retry attempts.
	// A value of 0 means no retries will be attempted.
	// A value of 3 means up to 3 retry attempts (4 total calls including the initial attempt).
	MaxRetries int

	// ShouldRetry determines how to handle a model call result.
	// It receives context information about the current attempt including the output message
	// and/or error, and returns a decision on whether to retry, what to modify, etc.
	// Returning nil is treated as &RetryDecision{ShouldRetry: false} (accept as-is).
	//
	// If nil, defaults to retrying on any non-nil error (backward compatible with IsRetryAble).
	//
	// Note: When ShouldRetry is set, IsRetryAble is ignored.
	// Note: In streaming mode, the entire stream is consumed before ShouldRetry is called,
	// so streaming latency benefits are deferred. For large responses, this means the full
	// response is buffered in memory before the retry decision is made.
	ShouldRetry func(ctx context.Context, retryCtx *RetryContext) *RetryDecision

	// Deprecated: Use ShouldRetry instead for richer retry control including message
	// inspection, input modification, and option adjustment. When ShouldRetry is set,
	// IsRetryAble is ignored.
	IsRetryAble func(ctx context.Context, err error) bool

	// BackoffFunc calculates the delay before the next retry attempt.
	// The attempt parameter starts at 1 for the first retry.
	// Used as the default when RetryDecision.Backoff is zero.
	// If nil, a default exponential backoff with jitter is used:
	// base delay 100ms, exponentially increasing up to 10s max,
	// with random jitter (0-50% of delay) to prevent thundering herd.
	BackoffFunc func(ctx context.Context, attempt int) time.Duration
}

func defaultIsRetryAble(_ context.Context, err error) bool {
	return err != nil
}

func defaultBackoff(_ context.Context, attempt int) time.Duration {
	baseDelay := 100 * time.Millisecond
	maxDelay := 10 * time.Second

	if attempt <= 0 {
		return baseDelay
	}

	if attempt > 7 {
		return maxDelay + time.Duration(rand.Int63n(int64(maxDelay/2)))
	}

	delay := baseDelay * time.Duration(1<<uint(attempt-1))
	if delay > maxDelay {
		delay = maxDelay
	}

	jitter := time.Duration(rand.Int63n(int64(delay / 2)))
	return delay + jitter
}

func genErrWrapper(ctx context.Context, maxRetries, attempt int, isRetryAbleFunc func(ctx context.Context, err error) bool) func(error) error {
	return func(err error) error {
		isRetryAble := isRetryAbleFunc == nil || isRetryAbleFunc(ctx, err)
		hasRetriesLeft := attempt < maxRetries

		if isRetryAble && hasRetriesLeft {
			return &WillRetryError{ErrStr: err.Error(), RetryAttempt: attempt, err: err}
		}
		return err
	}
}

func consumeStreamForError(stream *schema.StreamReader[*schema.Message]) error {
	defer stream.Close()
	for {
		_, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

type retryVerdictSignal struct {
	ch chan retryVerdict
}

type retryVerdict struct {
	WillRetry    bool
	RetryAttempt int
	Err          error
}

// retryModelWrapper wraps a BaseChatModel with retry logic.
// This is used inside the model wrapper chain, positioned between eventSenderModelWrapper
// and stateModelWrapper, so that retry only affects the inner chain (event sending, user wrappers,
// callback injection) without re-running state management (BeforeModelRewriteState/AfterModelRewriteState).
type retryModelWrapper struct {
	inner  model.BaseChatModel
	config *ModelRetryConfig
}

func newRetryModelWrapper(inner model.BaseChatModel, config *ModelRetryConfig) *retryModelWrapper {
	return &retryModelWrapper{inner: inner, config: config}
}

func (r *retryModelWrapper) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	if r.config.ShouldRetry != nil {
		return r.generateWithShouldRetry(ctx, input, opts...)
	}
	return r.generateLegacy(ctx, input, opts...)
}

func (r *retryModelWrapper) generateLegacy(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	isRetryAble := r.config.IsRetryAble
	if isRetryAble == nil {
		isRetryAble = defaultIsRetryAble
	}
	backoffFunc := r.config.BackoffFunc
	if backoffFunc == nil {
		backoffFunc = defaultBackoff
	}

	var lastErr error
	for attempt := 0; attempt <= r.config.MaxRetries; attempt++ {
		out, err := r.inner.Generate(ctx, input, opts...)
		if err == nil {
			return out, nil
		}

		// Never retry interrupt errors (e.g. cancel safe-point interrupts).
		if _, ok := compose.ExtractInterruptInfo(err); ok {
			return nil, err
		}

		if errors.Is(err, ErrStreamCanceled) {
			return nil, err
		}

		if !isRetryAble(ctx, err) {
			return nil, err
		}

		lastErr = err
		if attempt < r.config.MaxRetries {
			log.Printf("retrying ChatModel.Generate (attempt %d/%d): %v", attempt+1, r.config.MaxRetries, err)
			time.Sleep(backoffFunc(ctx, attempt+1))
		}
	}

	return nil, &RetryExhaustedError{LastErr: lastErr, TotalRetries: r.config.MaxRetries}
}

func (r *retryModelWrapper) generateWithShouldRetry(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	backoffFunc := r.config.BackoffFunc
	if backoffFunc == nil {
		backoffFunc = defaultBackoff
	}

	execCtx := getChatModelAgentExecCtx(ctx)

	currentInput := input
	currentOpts := opts
	var lastErr error

	defer func() {
		_ = compose.ProcessState(ctx, func(_ context.Context, st *State) error {
			st.setRetryAttempt(0)
			return nil
		})
	}()

	for attempt := 0; attempt <= r.config.MaxRetries; attempt++ {
		_ = compose.ProcessState(ctx, func(_ context.Context, st *State) error {
			st.setRetryAttempt(attempt)
			return nil
		})

		if execCtx != nil {
			execCtx.suppressEventSend = true
		}
		out, err := r.inner.Generate(ctx, currentInput, currentOpts...)
		if execCtx != nil {
			execCtx.suppressEventSend = false
		}

		if err != nil {
			if _, ok := compose.ExtractInterruptInfo(err); ok {
				return nil, err
			}

			if errors.Is(err, ErrStreamCanceled) {
				return nil, err
			}
		}

		retryCtx := &RetryContext{
			RetryAttempt:  attempt + 1,
			InputMessages: currentInput,
			Options:       currentOpts,
			OutputMessage: out,
			Err:           err,
		}
		decision := r.config.ShouldRetry(ctx, retryCtx)
		if decision == nil {
			decision = &RetryDecision{}
		}

		if !decision.ShouldRetry {
			if decision.RewriteError != nil {
				return nil, decision.RewriteError
			}
			if err != nil {
				return nil, err
			}
			if execCtx != nil && execCtx.generator != nil {
				msgCopy := *out
				event := EventFromMessage(&msgCopy, nil, schema.Assistant, "")
				execCtx.send(event)
			}
			return out, nil
		}

		lastErr = err
		if lastErr == nil {
			lastErr = fmt.Errorf("model output rejected by ShouldRetry at attempt %d", attempt+1)
		}

		if attempt >= r.config.MaxRetries {
			break
		}

		r.applyDecisionForRetry(&currentInput, &currentOpts, ctx, decision)

		delay := decision.Backoff
		if delay == 0 {
			delay = backoffFunc(ctx, attempt+1)
		}

		log.Printf("retrying ChatModel.Generate (attempt %d/%d): %v", attempt+1, r.config.MaxRetries, lastErr)
		if err := r.contextAwareSleep(ctx, delay); err != nil {
			return nil, err
		}
	}

	return nil, &RetryExhaustedError{LastErr: lastErr, TotalRetries: r.config.MaxRetries}
}

func (r *retryModelWrapper) contextAwareSleep(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(delay):
		return nil
	}
}

func consumeStreamForMessage(stream *schema.StreamReader[*schema.Message]) (*schema.Message, error) {
	defer stream.Close()
	var chunks []*schema.Message
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			if len(chunks) == 0 {
				return nil, nil
			}
			msg, concatErr := schema.ConcatMessages(chunks)
			return msg, concatErr
		}
		if err != nil {
			if len(chunks) == 0 {
				return nil, err
			}
			msg, _ := schema.ConcatMessages(chunks)
			return msg, err
		}
		chunks = append(chunks, chunk)
	}
}

func (r *retryModelWrapper) streamWithShouldRetry(ctx context.Context, input []*schema.Message, opts ...model.Option) (
	*schema.StreamReader[*schema.Message], error) {

	backoffFunc := r.config.BackoffFunc
	if backoffFunc == nil {
		backoffFunc = defaultBackoff
	}

	defer func() {
		_ = compose.ProcessState(ctx, func(_ context.Context, st *State) error {
			st.setRetryAttempt(0)
			return nil
		})
	}()

	execCtx := getChatModelAgentExecCtx(ctx)

	currentInput := input
	currentOpts := opts
	var lastErr error

	for attempt := 0; attempt <= r.config.MaxRetries; attempt++ {
		_ = compose.ProcessState(ctx, func(_ context.Context, st *State) error {
			st.setRetryAttempt(attempt)
			return nil
		})

		signal := &retryVerdictSignal{ch: make(chan retryVerdict, 1)}
		if execCtx != nil {
			execCtx.retryVerdictSignal = signal
		}

		stream, err := r.inner.Stream(ctx, currentInput, currentOpts...)
		if err != nil {
			signal.ch <- retryVerdict{WillRetry: false}

			if _, ok := compose.ExtractInterruptInfo(err); ok {
				return nil, err
			}

			if errors.Is(err, ErrStreamCanceled) {
				return nil, err
			}

			retryCtx := &RetryContext{
				RetryAttempt:  attempt + 1,
				InputMessages: currentInput,
				Options:       currentOpts,
				Err:           err,
			}
			decision := r.config.ShouldRetry(ctx, retryCtx)

			if !decision.ShouldRetry {
				if decision.RewriteError != nil {
					return nil, decision.RewriteError
				}
				return nil, err
			}

			lastErr = err
			if attempt < r.config.MaxRetries {
				r.applyDecisionForRetry(&currentInput, &currentOpts, ctx, decision)
				delay := decision.Backoff
				if delay == 0 {
					delay = backoffFunc(ctx, attempt+1)
				}
				log.Printf("retrying ChatModel.Stream (attempt %d/%d): %v", attempt+1, r.config.MaxRetries, err)
				if err := r.contextAwareSleep(ctx, delay); err != nil {
					return nil, err
				}
			}
			continue
		}

		copies := stream.Copy(2)
		checkCopy := copies[0]
		returnCopy := copies[1]

		msg, streamErr := consumeStreamForMessage(checkCopy)

		if errors.Is(streamErr, ErrStreamCanceled) {
			signal.ch <- retryVerdict{WillRetry: false}
			returnCopy.Close()
			return nil, streamErr
		}

		retryCtx := &RetryContext{
			RetryAttempt:  attempt + 1,
			InputMessages: currentInput,
			Options:       currentOpts,
			OutputMessage: msg,
			Err:           streamErr,
		}
		decision := r.config.ShouldRetry(ctx, retryCtx)
		if decision == nil {
			decision = &RetryDecision{}
		}

		if !decision.ShouldRetry {
			signal.ch <- retryVerdict{WillRetry: false}

			if decision.RewriteError != nil {
				returnCopy.Close()
				return nil, decision.RewriteError
			}
			if streamErr != nil {
				returnCopy.Close()
				return nil, streamErr
			}
			return returnCopy, nil
		}

		verdictErr := streamErr
		if verdictErr == nil {
			verdictErr = fmt.Errorf("model output rejected by ShouldRetry at attempt %d", attempt+1)
		}
		signal.ch <- retryVerdict{
			WillRetry:    true,
			RetryAttempt: attempt,
			Err:          verdictErr,
		}
		returnCopy.Close()

		lastErr = verdictErr

		if attempt < r.config.MaxRetries {
			r.applyDecisionForRetry(&currentInput, &currentOpts, ctx, decision)
			delay := decision.Backoff
			if delay == 0 {
				delay = backoffFunc(ctx, attempt+1)
			}
			log.Printf("retrying ChatModel.Stream (attempt %d/%d): %v", attempt+1, r.config.MaxRetries, lastErr)
			if err := r.contextAwareSleep(ctx, delay); err != nil {
				return nil, err
			}
		}
	}

	return nil, &RetryExhaustedError{LastErr: lastErr, TotalRetries: r.config.MaxRetries}
}

func (r *retryModelWrapper) applyDecisionForRetry(currentInput *[]*schema.Message, currentOpts *[]model.Option, ctx context.Context, decision *RetryDecision) {
	if decision.ModifiedInputMessages != nil {
		*currentInput = decision.ModifiedInputMessages
		if decision.PersistModifiedInputMessages {
			modifiedInput := *currentInput
			_ = compose.ProcessState(ctx, func(_ context.Context, st *State) error {
				st.Messages = modifiedInput
				return nil
			})
		}
	}

	if decision.ModifiedOptions != nil {
		cloned := make([]model.Option, len(*currentOpts), len(*currentOpts)+len(decision.ModifiedOptions))
		copy(cloned, *currentOpts)
		*currentOpts = append(cloned, decision.ModifiedOptions...)
	}
}

func (r *retryModelWrapper) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (
	*schema.StreamReader[*schema.Message], error) {

	if r.config.ShouldRetry != nil {
		return r.streamWithShouldRetry(ctx, input, opts...)
	}
	return r.streamLegacy(ctx, input, opts...)
}

func (r *retryModelWrapper) streamLegacy(ctx context.Context, input []*schema.Message, opts ...model.Option) (
	*schema.StreamReader[*schema.Message], error) {

	isRetryAble := r.config.IsRetryAble
	if isRetryAble == nil {
		isRetryAble = defaultIsRetryAble
	}
	backoffFunc := r.config.BackoffFunc
	if backoffFunc == nil {
		backoffFunc = defaultBackoff
	}

	defer func() {
		_ = compose.ProcessState(ctx, func(_ context.Context, st *State) error {
			st.setRetryAttempt(0)
			return nil
		})
	}()

	var lastErr error
	for attempt := 0; attempt <= r.config.MaxRetries; attempt++ {
		_ = compose.ProcessState(ctx, func(_ context.Context, st *State) error {
			st.setRetryAttempt(attempt)
			return nil
		})

		stream, err := r.inner.Stream(ctx, input, opts...)
		if err != nil {
			if _, ok := compose.ExtractInterruptInfo(err); ok {
				return nil, err
			}
			if errors.Is(err, ErrStreamCanceled) {
				return nil, err
			}
			if !isRetryAble(ctx, err) {
				return nil, err
			}
			lastErr = err
			if attempt < r.config.MaxRetries {
				log.Printf("retrying ChatModel.Stream (attempt %d/%d): %v", attempt+1, r.config.MaxRetries, err)
				time.Sleep(backoffFunc(ctx, attempt+1))
			}
			continue
		}

		copies := stream.Copy(2)
		checkCopy := copies[0]
		returnCopy := copies[1]

		streamErr := consumeStreamForError(checkCopy)
		if streamErr == nil {
			return returnCopy, nil
		}

		returnCopy.Close()
		if errors.Is(streamErr, ErrStreamCanceled) {
			return nil, streamErr
		}
		if !isRetryAble(ctx, streamErr) {
			return nil, streamErr
		}

		lastErr = streamErr
		if attempt < r.config.MaxRetries {
			log.Printf("retrying ChatModel.Stream (attempt %d/%d): %v", attempt+1, r.config.MaxRetries, streamErr)
			time.Sleep(backoffFunc(ctx, attempt+1))
		}
	}

	return nil, &RetryExhaustedError{LastErr: lastErr, TotalRetries: r.config.MaxRetries}
}
