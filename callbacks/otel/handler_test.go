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

package otel

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func newTestExporter() (*tracetest.InMemoryExporter, *sdktrace.TracerProvider) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exp),
	)
	return exp, tp
}

func getAttr(span sdktrace.ReadOnlySpan, key string) (attribute.Value, bool) {
	for _, kv := range span.Attributes() {
		if string(kv.Key) == key {
			return kv.Value, true
		}
	}
	return attribute.Value{}, false
}

func TestHandler_ChatModelSpan(t *testing.T) {
	exp, tp := newTestExporter()
	defer tp.Shutdown(context.Background())

	handler := NewHandler(WithTracerProvider(tp))

	info := &callbacks.RunInfo{
		Name:      "gpt-4",
		Type:      "OpenAI",
		Component: components.ComponentOfChatModel,
	}
	input := &model.CallbackInput{
		Messages: []*schema.Message{
			{Role: schema.User, Content: "hello"},
			{Role: schema.Assistant, Content: "hi"},
		},
		Config: &model.Config{Model: "gpt-4"},
	}

	ctx := handler.OnStart(context.Background(), info, input)
	output := &model.CallbackOutput{
		Message: &schema.Message{Role: schema.Assistant, Content: "response"},
		Config:  &model.Config{Model: "gpt-4-0613"},
		TokenUsage: &model.TokenUsage{
			PromptTokens:     10,
			CompletionTokens: 20,
			TotalTokens:      30,
		},
	}
	handler.OnEnd(ctx, info, output)

	spans := exp.GetSpans()
	require.Len(t, spans, 1)
	span := spans[0].Snapshot()

	assert.Equal(t, "gen_ai.client.chat", span.Name())

	// Check attributes
	v, ok := getAttr(span, "gen_ai.system")
	assert.True(t, ok)
	assert.Equal(t, "eino", v.AsString())

	v, ok = getAttr(span, "gen_ai.operation.name")
	assert.True(t, ok)
	assert.Equal(t, "chat", v.AsString())

	v, ok = getAttr(span, "gen_ai.request.model")
	assert.True(t, ok)
	assert.Equal(t, "gpt-4", v.AsString())

	v, ok = getAttr(span, "gen_ai.response.model")
	assert.True(t, ok)
	assert.Equal(t, "gpt-4-0613", v.AsString())

	v, ok = getAttr(span, "gen_ai.usage.input_tokens")
	assert.True(t, ok)
	assert.Equal(t, int64(10), v.AsInt64())

	v, ok = getAttr(span, "gen_ai.usage.output_tokens")
	assert.True(t, ok)
	assert.Equal(t, int64(20), v.AsInt64())

	v, ok = getAttr(span, "gen_ai.usage.total_tokens")
	assert.True(t, ok)
	assert.Equal(t, int64(30), v.AsInt64())

	v, ok = getAttr(span, "gen_ai.request.message_count")
	assert.True(t, ok)
	assert.Equal(t, int64(2), v.AsInt64())

	v, ok = getAttr(span, "eino.component")
	assert.True(t, ok)
	assert.Equal(t, "ChatModel", v.AsString())

	assert.Equal(t, codes.Ok, span.Status().Code)
}

func TestHandler_ToolSpan(t *testing.T) {
	exp, tp := newTestExporter()
	defer tp.Shutdown(context.Background())

	handler := NewHandler(WithTracerProvider(tp))

	info := &callbacks.RunInfo{
		Name:      "search",
		Type:      "CustomTool",
		Component: components.ComponentOfTool,
	}

	ctx := handler.OnStart(context.Background(), info, "query")
	handler.OnEnd(ctx, info, "result")

	spans := exp.GetSpans()
	require.Len(t, spans, 1)
	span := spans[0].Snapshot()

	assert.Equal(t, "gen_ai.tool search", span.Name())

	v, ok := getAttr(span, "gen_ai.tool.name")
	assert.True(t, ok)
	assert.Equal(t, "search", v.AsString())

	v, ok = getAttr(span, "gen_ai.system")
	assert.True(t, ok)
	assert.Equal(t, "eino", v.AsString())
}

func TestHandler_RetrieverSpan(t *testing.T) {
	exp, tp := newTestExporter()
	defer tp.Shutdown(context.Background())

	handler := NewHandler(WithTracerProvider(tp))

	info := &callbacks.RunInfo{
		Name:      "vector_store",
		Type:      "Chroma",
		Component: components.ComponentOfRetriever,
	}

	ctx := handler.OnStart(context.Background(), info, "test query")
	output := []*schema.Document{
		{Content: "doc1"},
		{Content: "doc2"},
		{Content: "doc3"},
	}
	handler.OnEnd(ctx, info, output)

	spans := exp.GetSpans()
	require.Len(t, spans, 1)
	span := spans[0].Snapshot()

	assert.Equal(t, "gen_ai.retriever vector_store", span.Name())

	v, ok := getAttr(span, "gen_ai.retriever.query")
	assert.True(t, ok)
	assert.Equal(t, "test query", v.AsString())

	v, ok = getAttr(span, "gen_ai.retriever.document_count")
	assert.True(t, ok)
	assert.Equal(t, int64(3), v.AsInt64())
}

func TestHandler_ErrorSpan(t *testing.T) {
	exp, tp := newTestExporter()
	defer tp.Shutdown(context.Background())

	handler := NewHandler(WithTracerProvider(tp))

	info := &callbacks.RunInfo{
		Name:      "gpt-4",
		Component: components.ComponentOfChatModel,
	}

	ctx := handler.OnStart(context.Background(), info, nil)
	testErr := errors.New("API rate limit exceeded")
	handler.OnError(ctx, info, testErr)

	spans := exp.GetSpans()
	require.Len(t, spans, 1)
	span := spans[0].Snapshot()

	assert.Equal(t, codes.Error, span.Status().Code)
	assert.Equal(t, "API rate limit exceeded", span.Status().Description)
}

func TestHandler_NilInfo(t *testing.T) {
	exp, tp := newTestExporter()
	defer tp.Shutdown(context.Background())

	handler := NewHandler(WithTracerProvider(tp))

	// Should not panic with nil info
	ctx := handler.OnStart(context.Background(), nil, nil)
	assert.NotNil(t, ctx)

	handler.OnEnd(ctx, nil, nil)
	handler.OnError(ctx, nil, nil)

	spans := exp.GetSpans()
	assert.Len(t, spans, 0)
}

func TestHandler_NestedSpans(t *testing.T) {
	exp, tp := newTestExporter()
	defer tp.Shutdown(context.Background())

	handler := NewHandler(WithTracerProvider(tp))

	// Outer span (Graph)
	outerInfo := &callbacks.RunInfo{
		Name:      "my_graph",
		Component: components.Component("Graph"),
	}
	ctx := handler.OnStart(context.Background(), outerInfo, nil)

	// Inner span (ChatModel)
	innerInfo := &callbacks.RunInfo{
		Name:      "gpt-4",
		Component: components.ComponentOfChatModel,
	}
	innerCtx := handler.OnStart(ctx, innerInfo, &model.CallbackInput{
		Messages: []*schema.Message{{Role: schema.User, Content: "hi"}},
		Config:   &model.Config{Model: "gpt-4"},
	})
	handler.OnEnd(innerCtx, innerInfo, &model.CallbackOutput{
		Message: &schema.Message{Role: schema.Assistant, Content: "hello"},
	})

	handler.OnEnd(ctx, outerInfo, nil)

	spans := exp.GetSpans()
	require.Len(t, spans, 2)

	// The inner span should have the outer span as parent
	innerSpan := spans[0].Snapshot()
	outerSpan := spans[1].Snapshot()

	assert.Equal(t, "gen_ai.client.chat", innerSpan.Name())
	assert.Equal(t, "eino.Graph my_graph", outerSpan.Name())
	assert.Equal(t, outerSpan.SpanContext().SpanID(), innerSpan.Parent().SpanID())
}

func TestHandler_TimingChecker(t *testing.T) {
	handler := NewHandler()

	info := &callbacks.RunInfo{Component: components.ComponentOfChatModel}

	assert.True(t, handler.Needed(context.Background(), info, callbacks.TimingOnStart))
	assert.True(t, handler.Needed(context.Background(), info, callbacks.TimingOnEnd))
	assert.True(t, handler.Needed(context.Background(), info, callbacks.TimingOnError))
	assert.False(t, handler.Needed(context.Background(), info, callbacks.TimingOnStartWithStreamInput))
	assert.False(t, handler.Needed(context.Background(), info, callbacks.TimingOnEndWithStreamOutput))
}

func TestHandler_EmbeddingSpan(t *testing.T) {
	exp, tp := newTestExporter()
	defer tp.Shutdown(context.Background())

	handler := NewHandler(WithTracerProvider(tp))

	info := &callbacks.RunInfo{
		Name:      "text-embedding-3",
		Component: components.ComponentOfEmbedding,
	}

	ctx := handler.OnStart(context.Background(), info, []string{"hello", "world"})
	handler.OnEnd(ctx, info, [][]float32{{0.1, 0.2}, {0.3, 0.4}})

	spans := exp.GetSpans()
	require.Len(t, spans, 1)
	span := spans[0].Snapshot()

	assert.Equal(t, "gen_ai.client.embed", span.Name())

	v, ok := getAttr(span, "gen_ai.operation.name")
	assert.True(t, ok)
	assert.Equal(t, "embed", v.AsString())
}

func TestHandler_WithTracer(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	tracer := tp.Tracer("custom-tracer")

	handler := NewHandler(WithTracer(tracer))
	info := &callbacks.RunInfo{
		Name:      "test",
		Component: components.ComponentOfChatModel,
	}

	ctx := handler.OnStart(context.Background(), info, nil)
	handler.OnEnd(ctx, info, nil)

	spans := exp.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, "gen_ai.client.chat", spans[0].Snapshot().Name())
}
