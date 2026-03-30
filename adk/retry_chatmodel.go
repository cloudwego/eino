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
	// Use errors.As to extract the underlying RetryExhaustedError for details:
	//
	//   var retryErr *adk.RetryExhaustedError
	//   if errors.As(err, &retryErr) {
	//       if retryErr.LastErr != nil {
	//           fmt.Printf("last error was: %v\n", retryErr.LastErr)
	//       }
	//       if retryErr.LastMessage != nil {
	//           fmt.Printf("last unsatisfactory response: %s\n", retryErr.LastMessage.Content)
	//       }
	//   }
	ErrExceedMaxRetries = errors.New("exceeds max retries")
)

// RetryExhaustedError is returned when all retry attempts have been exhausted.
// It wraps the last error that occurred during retry attempts.
// When retries are triggered by unsatisfactory responses (not errors),
// LastErr may be nil and LastMessage contains the last model output.
type RetryExhaustedError struct {
	LastErr      error
	LastMessage  *schema.Message
	TotalRetries int
}

func (e *RetryExhaustedError) Error() string {
	if e.LastErr != nil {
		return fmt.Sprintf("exceeds max retries: last error: %v", e.LastErr)
	}
	if e.LastMessage != nil {
		return "exceeds max retries: last response was unsatisfactory"
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

	// IsRetryAble determines whether the model call should be retried.
	// It receives the model's output message and the error (either may be nil).
	//
	// When err != nil: a call error occurred; msg is nil.
	// When err == nil: the call succeeded; msg is the model output. Return true
	// to retry based on the response content (e.g. empty content, unexpected finish_reason).
	//
	// If nil, all errors are retried and successful responses are never retried.
	IsRetryAble func(ctx context.Context, msg *schema.Message, err error) bool

	// BackoffFunc calculates the delay before the next retry attempt.
	// The attempt parameter starts at 1 for the first retry.
	// If nil, a default exponential backoff with jitter is used:
	// base delay 100ms, exponentially increasing up to 10s max,
	// with random jitter (0-50% of delay) to prevent thundering herd.
	BackoffFunc func(ctx context.Context, attempt int) time.Duration
}

func defaultIsRetryAble(_ context.Context, _ *schema.Message, err error) bool {
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

func genErrWrapper(ctx context.Context, maxRetries, attempt int, isRetryAbleFunc func(ctx context.Context, msg *schema.Message, err error) bool) func(error) error {
	return func(err error) error {
		isRetryAble := isRetryAbleFunc == nil || isRetryAbleFunc(ctx, nil, err)
		hasRetriesLeft := attempt < maxRetries

		if isRetryAble && hasRetriesLeft {
			return &WillRetryError{ErrStr: err.Error(), RetryAttempt: attempt, err: err}
		}
		return err
	}
}

func consumeStreamForMessage(stream *schema.StreamReader[*schema.Message]) (*schema.Message, error) {
	defer stream.Close()
	var chunks []*schema.Message
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			if len(chunks) == 0 {
				return nil, nil
			}
			assembled, concatErr := schema.ConcatMessages(chunks)
			if concatErr != nil {
				return nil, concatErr
			}
			return assembled, nil
		}
		if err != nil {
			return nil, err
		}
		chunks = append(chunks, msg)
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
	var lastMessage *schema.Message
	for attempt := 0; attempt <= r.config.MaxRetries; attempt++ {
		out, err := r.inner.Generate(ctx, input, opts...)
		if err == nil {
			if !isRetryAble(ctx, out, nil) {
				return out, nil
			}
			lastMessage = out
			lastErr = nil
		} else {
			if !isRetryAble(ctx, nil, err) {
				return nil, err
			}
			lastErr = err
			lastMessage = nil
		}

		if attempt < r.config.MaxRetries {
			if err != nil {
				log.Printf("retrying ChatModel.Generate (attempt %d/%d): %v", attempt+1, r.config.MaxRetries, err)
			} else {
				log.Printf("retrying ChatModel.Generate (attempt %d/%d): unsatisfactory response", attempt+1, r.config.MaxRetries)
			}
			time.Sleep(backoffFunc(ctx, attempt+1))
		}
	}

	return lastMessage, &RetryExhaustedError{LastErr: lastErr, LastMessage: lastMessage, TotalRetries: r.config.MaxRetries}
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
	var lastMessage *schema.Message
	for attempt := 0; attempt <= r.config.MaxRetries; attempt++ {
		_ = compose.ProcessState(ctx, func(_ context.Context, st *State) error {
			st.setRetryAttempt(attempt)
			return nil
		})

		stream, err := r.inner.Stream(ctx, input, opts...)
		if err != nil {
			if !isRetryAble(ctx, nil, err) {
				return nil, err
			}
			lastErr = err
			lastMessage = nil
			if attempt < r.config.MaxRetries {
				log.Printf("retrying ChatModel.Stream (attempt %d/%d): %v", attempt+1, r.config.MaxRetries, err)
				time.Sleep(backoffFunc(ctx, attempt+1))
			}
			continue
		}

		copies := stream.Copy(2)
		checkCopy := copies[0]
		returnCopy := copies[1]

		assembledMsg, streamErr := consumeStreamForMessage(checkCopy)
		if streamErr != nil {
			returnCopy.Close()
			if !isRetryAble(ctx, nil, streamErr) {
				return nil, streamErr
			}
			lastErr = streamErr
			lastMessage = nil
			if attempt < r.config.MaxRetries {
				log.Printf("retrying ChatModel.Stream (attempt %d/%d): %v", attempt+1, r.config.MaxRetries, streamErr)
				time.Sleep(backoffFunc(ctx, attempt+1))
			}
			continue
		}

		if !isRetryAble(ctx, assembledMsg, nil) {
			return returnCopy, nil
		}

		returnCopy.Close()
		lastMessage = assembledMsg
		lastErr = nil
		if attempt < r.config.MaxRetries {
			log.Printf("retrying ChatModel.Stream (attempt %d/%d): unsatisfactory response", attempt+1, r.config.MaxRetries)
			time.Sleep(backoffFunc(ctx, attempt+1))
		}
	}

	return nil, &RetryExhaustedError{LastErr: lastErr, LastMessage: lastMessage, TotalRetries: r.config.MaxRetries}
}
