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

package adk

import (
	"context"
	"errors"
	"io"
	"log"

	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type failoverCurrentModelKey struct{}

type failoverCurrentModel struct {
	model model.BaseChatModel
}

func setFailoverCurrentModel(ctx context.Context, currentModel model.BaseChatModel) context.Context {
	return context.WithValue(ctx, failoverCurrentModelKey{}, &failoverCurrentModel{
		model: currentModel,
	})
}

func getFailoverCurrentModel(ctx context.Context) *failoverCurrentModel {
	if v := ctx.Value(failoverCurrentModelKey{}); v != nil {
		if fm, ok := v.(*failoverCurrentModel); ok {
			return fm
		}
	}
	return nil
}

type failoverProxyModel struct {
}

func (m *failoverProxyModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	current := getFailoverCurrentModel(ctx)
	if current == nil || current.model == nil {
		return nil, errors.New("failover current model not found in context")
	}

	target := current.model
	// Callbacks must be injected here per selected model to avoid missing or duplicate callbacks when failover
	// switches between models with different callback capabilities.
	if !components.IsCallbacksEnabled(target) {
		target = (&callbackInjectionModelWrapper{}).WrapModel(target)
	}
	return target.Generate(ctx, input, opts...)
}

func (m *failoverProxyModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	current := getFailoverCurrentModel(ctx)
	if current == nil || current.model == nil {
		return nil, errors.New("failover current model not found in context")
	}

	target := current.model
	// Callbacks must be injected here per selected model to avoid missing or duplicate callbacks when failover
	// switches between models with different callback capabilities.
	if !components.IsCallbacksEnabled(target) {
		target = (&callbackInjectionModelWrapper{}).WrapModel(target)
	}
	return target.Stream(ctx, input, opts...)
}

func (m *failoverProxyModel) IsCallbacksEnabled() bool {
	return true
}

func (m *failoverProxyModel) GetType() string {
	return "FailoverProxyModel"
}

// FailoverContext contains context information during failover process.
type FailoverContext struct {
	// FailoverAttempt is the current failover attempt number, starting from 0 for the first attempt.
	FailoverAttempt uint

	// InputMessages is the original input messages before any transformation.
	InputMessages []*schema.Message

	// LastOutputMessage is the output message from the last failed attempt (if any).
	// For streaming, this may be a partial message already received before the stream error.
	LastOutputMessage *schema.Message

	// LastErr is the error from the last failed attempt (if any).
	LastErr error
}

// ModelFailoverConfig configures failover behavior for ChatModel.
// When configured, each ChatModel call may be attempted multiple times with different models and/or transformed
// input messages selected by GetFailoverModel.
type ModelFailoverConfig struct {
	// MaxRetries specifies the maximum number of additional failover retries.
	// A value of 0 means no failover will be attempted (only the initial call).
	// A value of 2 means up to 2 failover attempts (3 total calls including the initial attempt).
	MaxRetries uint

	// ShouldFailover determines whether to fail over to the next model when an error occurs.
	// It receives the output message (may be nil if no output is available) and the error (non-nil on failure).
	// For streaming errors, outputMessage can carry a partial message accumulated before the error.
	// Note: When the context itself is cancelled (ctx.Err() != nil), failover will stop immediately
	// regardless of this function. However, if the model returns context.Canceled or context.DeadlineExceeded
	// as an error while the context is still active, this function will still be called.
	// Should not be nil when ModelFailoverConfig is set.
	// Return true to fail over to the next model, false to stop and return the current result/error.
	ShouldFailover func(ctx context.Context, outputMessage *schema.Message, outputErr error) bool

	// GetFailoverModel selects the model to use for the current failover attempt and optionally transforms input messages.
	// It receives the failover context containing attempt number, original input, and last result.
	// Return values:
	//   - failoverModel: The model to use for this failover attempt.
	//   - failoverModelInpMessages: The transformed input messages for the failover model. If nil, will use original input.
	//   - failoverErr: If non-nil, failover stops and this error is returned.
	// Should not be nil when ModelFailoverConfig is set via ChatModelAgentConfig.
	GetFailoverModel func(ctx context.Context, failoverCtx *FailoverContext) (
		failoverModel model.BaseChatModel, failoverModelInpMessages []*schema.Message, failoverErr error)
}

type failoverModelWrapper struct {
	config *ModelFailoverConfig
	inner  model.BaseChatModel
}

func newFailoverModelWrapper(inner model.BaseChatModel, config *ModelFailoverConfig) *failoverModelWrapper {
	return &failoverModelWrapper{
		config: config,
		inner:  inner,
	}
}

func (f *failoverModelWrapper) canFailover(ctx context.Context, outputMessage *schema.Message, outputErr error) bool {
	if ctx.Err() != nil {
		return false
	}

	// ShouldFailover is validated at agent construction; nil here indicates a programmer error.
	return f.config.ShouldFailover(ctx, outputMessage, outputErr)
}

func (f *failoverModelWrapper) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	if f.config.GetFailoverModel == nil {
		return f.inner.Generate(ctx, input, opts...)
	}

	var lastOutputMessage *schema.Message
	var lastErr error

	for attempt := uint(0); attempt <= f.config.MaxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		failoverCtx := &FailoverContext{
			FailoverAttempt:   attempt,
			InputMessages:     input,
			LastOutputMessage: lastOutputMessage,
			LastErr:           lastErr,
		}

		currentModel, currentInput, err := f.config.GetFailoverModel(ctx, failoverCtx)
		if err != nil {
			return nil, err
		}

		if currentInput == nil {
			currentInput = input
		}

		modelCtx := setFailoverCurrentModel(ctx, currentModel)
		result, err := f.inner.Generate(modelCtx, currentInput, opts...)
		lastOutputMessage = result
		lastErr = err

		if err == nil {
			return result, nil
		}

		if !f.canFailover(ctx, result, err) {
			return result, err
		}

		if attempt < f.config.MaxRetries {
			log.Printf("failover ChatModel.Generate attempt %d failed: %v", attempt, err)
		}
	}

	return lastOutputMessage, lastErr
}

func (f *failoverModelWrapper) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (
	*schema.StreamReader[*schema.Message], error) {
	if f.config.GetFailoverModel == nil {
		return f.inner.Stream(ctx, input, opts...)
	}

	var lastOutputMessage *schema.Message
	var lastErr error

	for attempt := uint(0); attempt <= f.config.MaxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		failoverCtx := &FailoverContext{
			FailoverAttempt:   attempt,
			InputMessages:     input,
			LastOutputMessage: lastOutputMessage,
			LastErr:           lastErr,
		}

		currentModel, currentInput, err := f.config.GetFailoverModel(ctx, failoverCtx)
		if err != nil {
			return nil, err
		}

		if currentInput == nil {
			currentInput = input
		}

		modelCtx := setFailoverCurrentModel(ctx, currentModel)
		stream, err := f.inner.Stream(modelCtx, currentInput, opts...)
		if err != nil {
			lastErr = err
			lastOutputMessage = nil

			if !f.canFailover(ctx, nil, err) {
				return nil, err
			}

			if attempt < f.config.MaxRetries {
				log.Printf("failover ChatModel.Stream attempt %d failed: %v", attempt, err)
			}
			continue
		}

		// The stream returned by f.inner.Stream is already Copy'd by the inner eventSender layer: one
		// copy is forwarded to the client in real time via events. Therefore consuming a copy here does
		// NOT block client-side streaming.
		//
		// We Copy the stream into two readers:
		//   - checkCopy: consumed synchronously to surface mid-stream errors and decide whether to fail over.
		//   - returnCopy: returned to the caller (stateModelWrapper), which also consumes synchronously to
		//     build state (AfterModelRewriteState), so waiting here adds no extra latency.
		//
		// If checkCopy errors and failover is allowed, we close returnCopy and retry with the next model.
		// Otherwise we return returnCopy.
		//
		// NOTE on duplicate events during failover: when a retry happens, events from the failed attempt
		// may already have been emitted to the client, and the retry will emit a new stream. Client-side
		// handlers are expected to handle multiple rounds (e.g., reset on retry or deduplicate by attempt
		// metadata).
		copies := stream.Copy(2)
		checkCopy := copies[0]
		returnCopy := copies[1]

		outMsg, streamErr := consumeStream(checkCopy)
		if streamErr != nil {
			lastOutputMessage = outMsg
			lastErr = streamErr
			returnCopy.Close()

			if !f.canFailover(ctx, outMsg, streamErr) {
				return nil, streamErr
			}

			if attempt < f.config.MaxRetries {
				log.Printf("failover ChatModel.Stream attempt %d failed: %v", attempt, streamErr)
			}
			continue
		}

		return returnCopy, nil
	}

	return nil, lastErr
}

func consumeStream(stream *schema.StreamReader[*schema.Message]) (*schema.Message, error) {
	defer stream.Close()
	chunks := make([]*schema.Message, 0)
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			// ignore concat error
			msg, _ := schema.ConcatMessages(chunks)
			return msg, err
		}

		chunks = append(chunks, chunk)
	}

	// Stream completed successfully (EOF). ConcatMessages error is not a stream error,
	// so ignore it to avoid incorrectly triggering failover.
	msg, _ := schema.ConcatMessages(chunks)
	return msg, nil
}
