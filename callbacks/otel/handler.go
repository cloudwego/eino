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

// Package otel provides a native OpenTelemetry callback handler for eino.
//
// It implements the callbacks.Handler interface and creates OpenTelemetry spans
// following the GenAI semantic conventions:
// https://opentelemetry.io/docs/specs/semconv/gen-ai/
//
// # Usage
//
//	import (
//	    "github.com/cloudwego/eino/callbacks"
//	    "github.com/cloudwego/eino/callbacks/otel"
//	)
//
//	handler := otel.NewHandler(otel.WithTracerProvider(tp))
//	ctx := callbacks.InitCallbacks(context.Background(), &callbacks.RunInfo{}, handler)
//
// Or register globally:
//
//	callbacks.AppendGlobalHandlers(otel.NewHandler())
//
// # Spans Created
//
// The handler creates spans for the following component types:
//   - ChatModel / AgenticModel: "gen_ai.client.chat" with model, usage, and message attributes
//   - Tool: "gen_ai.tool {name}" with tool name and input/output attributes
//   - Retriever: "gen_ai.retriever {name}" with query and document count attributes
//   - Graph / Chain / Workflow: "eino.{type} {name}" with graph name attributes
//   - Other components: "eino.{component}" spans
//
// # GenAI Attributes
//
// Standard OpenTelemetry GenAI semantic convention attributes are used:
//   - gen_ai.system: "eino"
//   - gen_ai.operation.name: "chat" for ChatModel
//   - gen_ai.request.model: requested model name
//   - gen_ai.response.model: actual model from response
//   - gen_ai.usage.input_tokens: prompt token count
//   - gen_ai.usage.output_tokens: completion token count
//   - gen_ai.tool.name: tool name
package otel

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const instrumentationName = "github.com/cloudwego/eino/callbacks/otel"

// GenAI semantic convention attribute keys.
// Reference: https://opentelemetry.io/docs/specs/semconv/gen-ai/genai-spans/
var (
	attrGenAISystem        = attribute.Key("gen_ai.system")
	attrGenAIOperation     = attribute.Key("gen_ai.operation.name")
	attrGenAIRequestModel  = attribute.Key("gen_ai.request.model")
	attrGenAIResponseModel = attribute.Key("gen_ai.response.model")
	attrGenAIUsageInput    = attribute.Key("gen_ai.usage.input_tokens")
	attrGenAIUsageOutput   = attribute.Key("gen_ai.usage.output_tokens")
	attrGenAIUsageTotal    = attribute.Key("gen_ai.usage.total_tokens")
	attrGenAIToolName      = attribute.Key("gen_ai.tool.name")
)

// Eino-specific attributes.
var (
	attrEinoComponent = attribute.Key("eino.component")
	attrEinoName      = attribute.Key("eino.name")
	attrEinoType      = attribute.Key("eino.type")
)

type spanCtxKey struct{}

// Handler is an OpenTelemetry callback handler that creates spans for eino operations.
// It implements callbacks.Handler and callbacks.TimingChecker.
type Handler struct {
	tracer trace.Tracer
}

// Option configures the Handler.
type Option func(*Handler)

// WithTracerProvider sets the OpenTelemetry TracerProvider.
// If not set, the global TracerProvider is used.
func WithTracerProvider(tp trace.TracerProvider) Option {
	return func(h *Handler) {
		if tp != nil {
			h.tracer = tp.Tracer(instrumentationName)
		}
	}
}

// WithTracer sets the OpenTelemetry Tracer directly.
func WithTracer(tracer trace.Tracer) Option {
	return func(h *Handler) {
		h.tracer = tracer
	}
}

// NewHandler creates a new OpenTelemetry callback handler.
// If no TracerProvider or Tracer is provided, the global TracerProvider is used.
func NewHandler(opts ...Option) *Handler {
	h := &Handler{}
	for _, opt := range opts {
		opt(h)
	}
	if h.tracer == nil {
		h.tracer = otel.GetTracerProvider().Tracer(instrumentationName)
	}
	return h
}

// Needed implements callbacks.TimingChecker.
// The handler only needs OnStart, OnEnd, and OnError timings.
// Stream timings are skipped to avoid unnecessary stream copying overhead.
func (h *Handler) Needed(ctx context.Context, info *callbacks.RunInfo, timing callbacks.CallbackTiming) bool {
	switch timing {
	case callbacks.TimingOnStart, callbacks.TimingOnEnd, callbacks.TimingOnError:
		return true
	default:
		return false
	}
}

// OnStart is called when a component starts executing.
// It creates a new span and stores it in the returned context.
func (h *Handler) OnStart(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
	if info == nil {
		return ctx
	}

	spanName, attrs := h.buildSpanInfo(info, input)
	ctx, span := h.tracer.Start(ctx, spanName, trace.WithAttributes(attrs...))
	return context.WithValue(ctx, spanCtxKey{}, span)
}

// OnEnd is called when a component finishes executing successfully.
// It ends the span stored in the context and adds output-related attributes.
func (h *Handler) OnEnd(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
	span := h.getSpan(ctx)
	if span == nil {
		return ctx
	}

	if info != nil {
		h.addOutputAttributes(span, info, output)
	}

	span.SetStatus(codes.Ok, "")
	span.End()
	return ctx
}

// OnError is called when a component encounters an error.
// It records the error on the span and ends it.
func (h *Handler) OnError(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
	span := h.getSpan(ctx)
	if span == nil {
		return ctx
	}

	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
	return ctx
}

// OnStartWithStreamInput is called when a component receives streaming input.
// This handler does not process streaming inputs individually; the span is
// created in OnStart instead. This method returns the context unchanged.
func (h *Handler) OnStartWithStreamInput(ctx context.Context, info *callbacks.RunInfo,
	input *schema.StreamReader[callbacks.CallbackInput]) context.Context {
	return ctx
}

// OnEndWithStreamOutput is called when a component returns streaming output.
// This handler does not process streaming outputs individually.
// This method returns the context unchanged.
func (h *Handler) OnEndWithStreamOutput(ctx context.Context, info *callbacks.RunInfo,
	output *schema.StreamReader[callbacks.CallbackOutput]) context.Context {
	return ctx
}

func (h *Handler) getSpan(ctx context.Context) trace.Span {
	if span, ok := ctx.Value(spanCtxKey{}).(trace.Span); ok && span != nil {
		return span
	}
	return nil
}

// buildSpanInfo creates the span name and initial attributes based on the component type.
func (h *Handler) buildSpanInfo(info *callbacks.RunInfo, input callbacks.CallbackInput) (string, []attribute.KeyValue) {
	var attrs []attribute.KeyValue
	attrs = append(attrs,
		attrGenAISystem.String("eino"),
		attrEinoComponent.String(string(info.Component)),
	)
	if info.Name != "" {
		attrs = append(attrs, attrEinoName.String(info.Name))
	}
	if info.Type != "" {
		attrs = append(attrs, attrEinoType.String(info.Type))
	}

	switch info.Component {
	case components.ComponentOfChatModel, components.ComponentOfAgenticModel:
		spanName := "gen_ai.client.chat"
		attrs = append(attrs, attrGenAIOperation.String("chat"))

		// Extract model name and message count from input
		if modelInput := model.ConvCallbackInput(input); modelInput != nil {
			if modelInput.Config != nil && modelInput.Config.Model != "" {
				attrs = append(attrs, attrGenAIRequestModel.String(modelInput.Config.Model))
			}
			attrs = append(attrs,
				attribute.Key("gen_ai.request.message_count").Int(len(modelInput.Messages)),
			)
			if len(modelInput.Tools) > 0 {
				attrs = append(attrs,
					attribute.Key("gen_ai.request.tool_count").Int(len(modelInput.Tools)),
				)
			}
		}
		return spanName, attrs

	case components.ComponentOfTool:
		spanName := fmt.Sprintf("gen_ai.tool %s", info.Name)
		if info.Name != "" {
			attrs = append(attrs, attrGenAIToolName.String(info.Name))
		}
		return spanName, attrs

	case components.ComponentOfRetriever:
		spanName := fmt.Sprintf("gen_ai.retriever %s", info.Name)
		attrs = append(attrs, attrGenAIOperation.String("retrieve"))
		// Try to extract query from input (string or []string)
		switch v := input.(type) {
		case string:
			attrs = append(attrs, attribute.Key("gen_ai.retriever.query").String(v))
		case []string:
			if len(v) > 0 {
				attrs = append(attrs, attribute.Key("gen_ai.retriever.query").String(v[0]))
			}
		}
		return spanName, attrs

	case components.ComponentOfEmbedding:
		spanName := "gen_ai.client.embed"
		attrs = append(attrs, attrGenAIOperation.String("embed"))
		return spanName, attrs

	default:
		// Graph, Chain, Workflow, Lambda, etc.
		spanName := fmt.Sprintf("eino.%s", string(info.Component))
		if info.Name != "" {
			spanName = fmt.Sprintf("%s %s", spanName, info.Name)
		}
		return spanName, attrs
	}
}

// addOutputAttributes adds output-related attributes to the span based on component type.
func (h *Handler) addOutputAttributes(span trace.Span, info *callbacks.RunInfo, output callbacks.CallbackOutput) {
	switch info.Component {
	case components.ComponentOfChatModel, components.ComponentOfAgenticModel:
		if modelOutput := model.ConvCallbackOutput(output); modelOutput != nil {
			if modelOutput.Config != nil && modelOutput.Config.Model != "" {
				span.SetAttributes(attrGenAIResponseModel.String(modelOutput.Config.Model))
			}
			if modelOutput.TokenUsage != nil {
				if modelOutput.TokenUsage.PromptTokens > 0 {
					span.SetAttributes(attrGenAIUsageInput.Int(modelOutput.TokenUsage.PromptTokens))
				}
				if modelOutput.TokenUsage.CompletionTokens > 0 {
					span.SetAttributes(attrGenAIUsageOutput.Int(modelOutput.TokenUsage.CompletionTokens))
				}
				if modelOutput.TokenUsage.TotalTokens > 0 {
					span.SetAttributes(attrGenAIUsageTotal.Int(modelOutput.TokenUsage.TotalTokens))
				}
			}
			if modelOutput.Message != nil && modelOutput.Message.Role != "" {
				span.SetAttributes(
					attribute.Key("gen_ai.response.role").String(string(modelOutput.Message.Role)),
				)
			}
		}

	case components.ComponentOfRetriever:
		// Try to count retrieved documents
		switch v := output.(type) {
		case []*schema.Document:
			span.SetAttributes(
				attribute.Key("gen_ai.retriever.document_count").Int(len(v)),
			)
		case []schema.Document:
			span.SetAttributes(
				attribute.Key("gen_ai.retriever.document_count").Int(len(v)),
			)
		}
	}
}

// Compile-time interface checks.
var (
	_ callbacks.Handler       = (*Handler)(nil)
	_ callbacks.TimingChecker = (*Handler)(nil)
)
