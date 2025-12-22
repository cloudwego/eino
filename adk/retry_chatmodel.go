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
	"time"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
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
	LastErr error
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
	IsRetryAble func(error) bool

	// BackoffFunc calculates the delay before the next retry attempt.
	// The attempt parameter starts at 1 for the first retry.
	// If nil, no delay is applied between retries.
	// Example: func(attempt int) time.Duration { return time.Duration(attempt) * 100 * time.Millisecond }
	BackoffFunc func(attempt int) time.Duration
}

func defaultIsRetryAble(err error) bool {
	return err != nil
}

func defaultBackoff(attempt int) time.Duration {
	return time.Duration(attempt) * 100 * time.Millisecond
}

type retryChatModel struct {
	inner  model.ToolCallingChatModel
	config *ModelRetryConfig
}

func newRetryChatModel(inner model.ToolCallingChatModel, config *ModelRetryConfig) *retryChatModel {
	return &retryChatModel{inner: inner, config: config}
}

func (r *retryChatModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	newInner, err := r.inner.WithTools(tools)
	if err != nil {
		return nil, err
	}
	return &retryChatModel{inner: newInner, config: r.config}, nil
}

func (r *retryChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	ctx = callbacks.EnsureRunInfo(ctx, r.GetType(), components.ComponentOfChatModel)

	if ch, ok := r.inner.(components.Checker); ok && ch.IsCallbacksEnabled() {
		return r.generateWithRetry(ctx, input, opts...)
	}

	nCtx := callbacks.OnStart(ctx, &model.CallbackInput{Messages: input})
	out, err := r.generateWithRetry(nCtx, input, opts...)
	if err != nil {
		callbacks.OnError(nCtx, err)
		return nil, err
	}
	callbacks.OnEnd(nCtx, &model.CallbackOutput{Message: out})
	return out, nil
}

func (r *retryChatModel) generateWithRetry(ctx context.Context,
	input []*schema.Message, opts ...model.Option) (*schema.Message, error) {

	isRetryAble := r.config.IsRetryAble
	if isRetryAble == nil {
		isRetryAble = defaultIsRetryAble
	}
	backoffFunc := r.config.BackoffFunc
	if backoffFunc == nil {
		backoffFunc = defaultBackoff
	}

	var lastErr error
	for {
		remaining, err := r.getRemainingRetries(ctx)
		if err != nil || remaining <= 0 {
			if lastErr != nil {
				return nil, &RetryExhaustedError{LastErr: lastErr}
			}
			return nil, &RetryExhaustedError{}
		}

		out, err := r.inner.Generate(ctx, input, opts...)
		if err == nil {
			return out, nil
		}

		if !isRetryAble(err) {
			return nil, err
		}

		lastErr = err
		r.decrementRetries(ctx)

		time.Sleep(backoffFunc(r.config.MaxRetries - remaining + 1))
	}
}

func (r *retryChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (
	*schema.StreamReader[*schema.Message], error) {

	ctx = callbacks.EnsureRunInfo(ctx, r.GetType(), components.ComponentOfChatModel)

	if ch, ok := r.inner.(components.Checker); ok && ch.IsCallbacksEnabled() {
		return r.streamWithRetry(ctx, input, opts...)
	}

	nCtx := callbacks.OnStart(ctx, &model.CallbackInput{Messages: input})
	sr, err := r.streamWithRetry(nCtx, input, opts...)
	if err != nil {
		callbacks.OnError(nCtx, err)
		return nil, err
	}
	out := schema.StreamReaderWithConvert(sr, func(m *schema.Message) (*model.CallbackOutput, error) {
		return &model.CallbackOutput{Message: m}, nil
	})
	_, out = callbacks.OnEndWithStreamOutput(nCtx, out)
	return schema.StreamReaderWithConvert(out, func(o *model.CallbackOutput) (*schema.Message, error) {
		return o.Message, nil
	}), nil
}

func (r *retryChatModel) streamWithRetry(ctx context.Context,
	input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {

	isRetryAble := r.config.IsRetryAble
	if isRetryAble == nil {
		isRetryAble = defaultIsRetryAble
	}
	backoffFunc := r.config.BackoffFunc
	if backoffFunc == nil {
		backoffFunc = defaultBackoff
	}

	var lastErr error
	for {
		remaining, err := r.getRemainingRetries(ctx)
		if err != nil || remaining <= 0 {
			if lastErr != nil {
				return nil, &RetryExhaustedError{LastErr: lastErr}
			}
			return nil, &RetryExhaustedError{}
		}

		sr, err := r.inner.Stream(ctx, input, opts...)
		if err == nil {
			return sr, nil
		}

		if !isRetryAble(err) {
			return nil, err
		}

		lastErr = err
		r.decrementRetries(ctx)

		time.Sleep(backoffFunc(r.config.MaxRetries - remaining + 1))
	}
}

func (r *retryChatModel) getRemainingRetries(ctx context.Context) (int, error) {
	var remaining int
	err := compose.ProcessState(ctx, func(_ context.Context, st *State) error {
		remaining = st.RemainingRetries
		return nil
	})
	return remaining, err
}

func (r *retryChatModel) decrementRetries(ctx context.Context) {
	_ = compose.ProcessState(ctx, func(_ context.Context, st *State) error {
		st.RemainingRetries--
		return nil
	})
}

func (r *retryChatModel) GetType() string {
	if gt, ok := r.inner.(interface{ GetType() string }); ok {
		return gt.GetType()
	}
	return "RetryChatModel"
}

func (r *retryChatModel) IsCallbacksEnabled() bool { return true }
