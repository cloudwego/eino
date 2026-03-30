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
// If retries were exhausted due to response-triggered retries (ShouldRetry returning true
// on a successful response), LastResp contains the last response and LastErr may be nil.
type RetryExhaustedError struct {
	LastErr      error
	LastResp     *schema.Message
	TotalRetries int
}

func (e *RetryExhaustedError) Error() string {
	if e.LastErr != nil {
		return fmt.Sprintf("exceeds max retries: last error: %v", e.LastErr)
	}
	if e.LastResp != nil {
		return "exceeds max retries: last response rejected by ShouldRetry"
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
	err          error
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

// ModelRetryConfig configures retry behavior for the ChatModel node.
// It defines how the agent should handle transient failures and unacceptable responses
// when calling the ChatModel.
type ModelRetryConfig struct {
	// MaxRetries specifies the maximum number of retry attempts.
	// A value of 0 means no retries will be attempted.
	// A value of 3 means up to 3 retry attempts (4 total calls including the initial attempt).
	// Error-triggered and response-triggered retries share this budget.
	MaxRetries int

	// Deprecated: Use ShouldRetry instead.
	// IsRetryAble is only used when ShouldRetry is nil, as a fallback for error-only retry.
	// If both ShouldRetry and IsRetryAble are set, ShouldRetry takes precedence.
	IsRetryAble func(ctx context.Context, err error) bool

	// ShouldRetry decides whether to retry based on the model's output.
	// Exactly one of resp/err will be non-nil:
	//   - err != nil: the model call failed with an error
	//   - resp != nil: the model returned a successful response
	// Return true to retry, false to accept the result as-is.
	//
	// If nil, falls back to: retry all errors (filtered by IsRetryAble if set),
	// never retry successful responses.
	//
	// For Stream mode: the response is the concatenated result of the full stream.
	// The user will have already received the streamed chunks via AgentEvents
	// before this function is called (because the retry wrapper sits outside the event sender).
	ShouldRetry func(ctx context.Context, resp *schema.Message, err error) bool

	// ModifyInput is called before each retry attempt to optionally transform the input messages.
	// It is called for BOTH error-triggered retries and response-triggered retries.
	//
	// Parameters:
	//   - input: the current input messages (may have been modified by a previous retry)
	//   - resp: the model's response message, or nil if the retry was triggered by an error
	//   - err: the error that triggered the retry, or nil if triggered by ShouldRetry on a response
	//
	// Returns the new input messages to use for the retry call.
	// If nil, the input is reused unchanged on each retry (preserving current behavior).
	ModifyInput func(ctx context.Context, input []*schema.Message, resp *schema.Message, err error) ([]*schema.Message, error)

	// BackoffFunc calculates the delay before the next retry attempt.
	// The attempt parameter starts at 1 for the first retry.
	// If nil, a default exponential backoff with jitter is used:
	// base delay 100ms, exponentially increasing up to 10s max,
	// with random jitter (0-50% of delay) to prevent thundering herd.
	BackoffFunc func(ctx context.Context, attempt int) time.Duration
}

// RetryOnError is a convenience constructor for ShouldRetry that only retries on errors.
// It wraps an error-checking function into the ShouldRetry signature.
// Successful responses are never retried.
//
// Example:
//
//	config := &ModelRetryConfig{
//	    MaxRetries:  3,
//	    ShouldRetry: RetryOnError(func(ctx context.Context, err error) bool {
//	        return isTransientError(err)
//	    }),
//	}
func RetryOnError(fn func(ctx context.Context, err error) bool) func(ctx context.Context, resp *schema.Message, err error) bool {
	return func(ctx context.Context, resp *schema.Message, err error) bool {
		if err != nil {
			return fn(ctx, err)
		}
		return false
	}
}

func defaultIsRetryAble(_ context.Context, err error) bool {
	return err != nil
}

func effectiveShouldRetry(config *ModelRetryConfig) func(ctx context.Context, resp *schema.Message, err error) bool {
	if config.ShouldRetry != nil {
		return config.ShouldRetry
	}
	isRetryAble := config.IsRetryAble
	if isRetryAble == nil {
		isRetryAble = defaultIsRetryAble
	}
	return func(ctx context.Context, resp *schema.Message, err error) bool {
		return err != nil && isRetryAble(ctx, err)
	}
}

func effectiveShouldRetryError(config *ModelRetryConfig) func(ctx context.Context, err error) bool {
	if config.ShouldRetry != nil {
		return func(ctx context.Context, err error) bool {
			return config.ShouldRetry(ctx, nil, err)
		}
	}
	if config.IsRetryAble != nil {
		return config.IsRetryAble
	}
	return defaultIsRetryAble
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

func genErrWrapper(ctx context.Context, maxRetries, attempt int, shouldRetryErr func(ctx context.Context, err error) bool) func(error) error {
	return func(err error) error {
		isRetryAble := shouldRetryErr == nil || shouldRetryErr(ctx, err)
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

func consumeStreamForMessage(stream *schema.StreamReader[*schema.Message]) (*schema.Message, error) {
	return schema.ConcatMessageStream(stream)
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
	shouldRetry := effectiveShouldRetry(r.config)
	backoffFunc := r.config.BackoffFunc
	if backoffFunc == nil {
		backoffFunc = defaultBackoff
	}

	currentInput := input
	var lastErr error
	var lastResp *schema.Message
	for attempt := 0; attempt <= r.config.MaxRetries; attempt++ {
		out, err := r.inner.Generate(ctx, currentInput, opts...)

		if err != nil {
			if _, ok := compose.ExtractInterruptInfo(err); ok {
				return nil, err
			}
		}

		retry := false
		if err != nil {
			if !shouldRetry(ctx, nil, err) {
				return nil, err
			}
			lastErr = err
			lastResp = nil
			retry = true
		} else if shouldRetry(ctx, out, nil) {
			lastResp = out
			lastErr = nil
			retry = true
		}

		if !retry {
			return out, nil
		}

		if attempt < r.config.MaxRetries {
			if r.config.ModifyInput != nil {
				newInput, modErr := r.config.ModifyInput(ctx, currentInput, out, err)
				if modErr != nil {
					return nil, modErr
				}
				currentInput = newInput
			}
			if err != nil {
				log.Printf("retrying ChatModel.Generate (attempt %d/%d): %v", attempt+1, r.config.MaxRetries, err)
			} else {
				log.Printf("retrying ChatModel.Generate (attempt %d/%d): response rejected by ShouldRetry", attempt+1, r.config.MaxRetries)
			}
			time.Sleep(backoffFunc(ctx, attempt+1))
		}
	}

	return nil, &RetryExhaustedError{LastErr: lastErr, LastResp: lastResp, TotalRetries: r.config.MaxRetries}
}

func (r *retryModelWrapper) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (
	*schema.StreamReader[*schema.Message], error) {

	shouldRetry := effectiveShouldRetry(r.config)
	backoffFunc := r.config.BackoffFunc
	if backoffFunc == nil {
		backoffFunc = defaultBackoff
	}

	hasShouldRetryForResp := r.config.ShouldRetry != nil

	defer func() {
		_ = compose.ProcessState(ctx, func(_ context.Context, st *State) error {
			st.setRetryAttempt(0)
			return nil
		})
	}()

	currentInput := input
	var lastErr error
	var lastResp *schema.Message
	for attempt := 0; attempt <= r.config.MaxRetries; attempt++ {
		_ = compose.ProcessState(ctx, func(_ context.Context, st *State) error {
			st.setRetryAttempt(attempt)
			return nil
		})

		stream, err := r.inner.Stream(ctx, currentInput, opts...)
		if err != nil {
			if _, ok := compose.ExtractInterruptInfo(err); ok {
				return nil, err
			}
			if !shouldRetry(ctx, nil, err) {
				return nil, err
			}
			lastErr = err
			lastResp = nil
			if attempt < r.config.MaxRetries {
				if r.config.ModifyInput != nil {
					newInput, modErr := r.config.ModifyInput(ctx, currentInput, nil, err)
					if modErr != nil {
						return nil, modErr
					}
					currentInput = newInput
				}
				log.Printf("retrying ChatModel.Stream (attempt %d/%d): %v", attempt+1, r.config.MaxRetries, err)
				time.Sleep(backoffFunc(ctx, attempt+1))
			}
			continue
		}

		copies := stream.Copy(2)
		checkCopy := copies[0]
		returnCopy := copies[1]

		if hasShouldRetryForResp {
			fullMsg, concatErr := consumeStreamForMessage(checkCopy)
			if concatErr != nil {
				returnCopy.Close()
				if !shouldRetry(ctx, nil, concatErr) {
					return nil, concatErr
				}
				lastErr = concatErr
				lastResp = nil
				if attempt < r.config.MaxRetries {
					if r.config.ModifyInput != nil {
						newInput, modErr := r.config.ModifyInput(ctx, currentInput, nil, concatErr)
						if modErr != nil {
							return nil, modErr
						}
						currentInput = newInput
					}
					log.Printf("retrying ChatModel.Stream (attempt %d/%d): %v", attempt+1, r.config.MaxRetries, concatErr)
					time.Sleep(backoffFunc(ctx, attempt+1))
				}
				continue
			}

			if shouldRetry(ctx, fullMsg, nil) {
				returnCopy.Close()
				lastResp = fullMsg
				lastErr = nil
				if attempt < r.config.MaxRetries {
					if r.config.ModifyInput != nil {
						newInput, modErr := r.config.ModifyInput(ctx, currentInput, fullMsg, nil)
						if modErr != nil {
							return nil, modErr
						}
						currentInput = newInput
					}
					log.Printf("retrying ChatModel.Stream (attempt %d/%d): response rejected by ShouldRetry", attempt+1, r.config.MaxRetries)
					time.Sleep(backoffFunc(ctx, attempt+1))
				}
				continue
			}

			return returnCopy, nil
		}

		streamErr := consumeStreamForError(checkCopy)
		if streamErr == nil {
			return returnCopy, nil
		}

		returnCopy.Close()
		if !shouldRetry(ctx, nil, streamErr) {
			return nil, streamErr
		}

		lastErr = streamErr
		lastResp = nil
		if attempt < r.config.MaxRetries {
			if r.config.ModifyInput != nil {
				newInput, modErr := r.config.ModifyInput(ctx, currentInput, nil, streamErr)
				if modErr != nil {
					return nil, modErr
				}
				currentInput = newInput
			}
			log.Printf("retrying ChatModel.Stream (attempt %d/%d): %v", attempt+1, r.config.MaxRetries, streamErr)
			time.Sleep(backoffFunc(ctx, attempt+1))
		}
	}

	return nil, &RetryExhaustedError{LastErr: lastErr, LastResp: lastResp, TotalRetries: r.config.MaxRetries}
}
