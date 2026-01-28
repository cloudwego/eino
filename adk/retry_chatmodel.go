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
// It defines how the agent should handle transient failures when calling the ChatModel.
type ModelRetryConfig struct {
	// MaxRetries specifies the maximum number of retry attempts.
	// A value of 0 means no retries will be attempted.
	// A value of 3 means up to 3 retry attempts (4 total calls including the initial attempt).
	MaxRetries int

	// IsRetryAble is a function that determines whether an error should trigger a retry.
	// If nil, all errors are considered retry-able.
	// Return true if the error is transient and the operation should be retried.
	// Return false if the error is permanent and should be propagated immediately.
	IsRetryAble func(ctx context.Context, err error) bool

	// BackoffFunc calculates the delay before the next retry attempt.
	// The attempt parameter starts at 1 for the first retry.
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

func (r *retryModelWrapper) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (
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
