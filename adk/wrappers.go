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
	"reflect"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/cloudwego/eino/adk/internal"
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/internal/generic"
	"github.com/cloudwego/eino/schema"
)

type typedGenerateEndpoint[M MessageType] func(ctx context.Context, input []M, opts ...model.Option) (M, error)
type typedStreamEndpoint[M MessageType] func(ctx context.Context, input []M, opts ...model.Option) (*schema.StreamReader[M], error)

type typedModelWrapperConfig[M MessageType] struct {
	handlers       []TypedChatModelAgentMiddleware[M]
	middlewares    []AgentMiddleware
	retryConfig    *TypedModelRetryConfig[M]
	failoverConfig *ModelFailoverConfig[M]
	timeoutConfig  *ModelTimeoutConfig
	toolInfos      []*schema.ToolInfo
	cancelContext  *cancelContext
}

type modelWrapperConfig = typedModelWrapperConfig[*schema.Message]

func buildModelWrappers[M MessageType](m model.BaseModel[M], config *typedModelWrapperConfig[M]) model.BaseModel[M] {
	return buildModelWrappersImpl(m, config)
}

func buildModelWrappersImpl[M MessageType](m model.BaseModel[M], config *typedModelWrapperConfig[M]) model.BaseModel[M] {
	var wrapped = m

	if config.failoverConfig != nil {
		wrapped = &typedFailoverProxyModel[M]{}
	}

	if !components.IsCallbacksEnabled(wrapped) {
		wrapped = typedCallbackInjectionModelWrapper[M]{}.wrapModel(wrapped)
	}

	wrapped = &typedStateModelWrapper[M]{
		inner:               wrapped,
		original:            m,
		handlers:            config.handlers,
		middlewares:         config.middlewares,
		toolInfos:           config.toolInfos,
		modelRetryConfig:    config.retryConfig,
		modelFailoverConfig: config.failoverConfig,
		modelTimeoutConfig:  config.timeoutConfig,
		cancelContext:       config.cancelContext,
	}

	return wrapped
}

type typedCallbackInjectionModelWrapper[M MessageType] struct{}

func (w typedCallbackInjectionModelWrapper[M]) wrapModel(m model.BaseModel[M]) model.BaseModel[M] {
	return &typedCallbackInjectedModel[M]{inner: m}
}

type typedCallbackInjectedModel[M MessageType] struct {
	inner model.BaseModel[M]
}

func (m *typedCallbackInjectedModel[M]) Generate(ctx context.Context, input []M, opts ...model.Option) (M, error) {
	ctx = callbacks.OnStart(ctx, input)
	result, err := m.inner.Generate(ctx, input, opts...)
	if err != nil {
		callbacks.OnError(ctx, err)
		var zero M
		return zero, err
	}
	callbacks.OnEnd(ctx, result)
	return result, nil
}

func (m *typedCallbackInjectedModel[M]) Stream(ctx context.Context, input []M, opts ...model.Option) (*schema.StreamReader[M], error) {
	ctx = callbacks.OnStart(ctx, input)
	result, err := m.inner.Stream(ctx, input, opts...)
	if err != nil {
		callbacks.OnError(ctx, err)
		return nil, err
	}
	_, wrappedStream := callbacks.OnEndWithStreamOutput(ctx, result)
	return wrappedStream, nil
}

func handlersToToolMiddlewares[M MessageType](handlers []TypedChatModelAgentMiddleware[M]) []compose.ToolMiddleware {
	var middlewares []compose.ToolMiddleware
	// Forward iteration: compose.wrapToolCall applies middlewares in reverse order
	// (len-1 down to 0), so keeping the original handler order here means
	// handlers[0] ends up outermost — matching the model wrapping convention.
	for _, handler := range handlers {

		m := compose.ToolMiddleware{}

		h := handler
		m.Invokable = func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
				tCtx := &ToolContext{
					Name:   input.Name,
					CallID: input.CallID,
				}
				wrappedEndpoint, err := h.WrapInvokableToolCall(
					ctx,
					func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
						output, err := next(ctx, &compose.ToolInput{
							Name:        input.Name,
							CallID:      input.CallID,
							Arguments:   argumentsInJSON,
							CallOptions: opts,
						})
						if err != nil {
							return "", err
						}
						return output.Result, nil
					},
					tCtx,
				)
				if err != nil {
					return nil, err
				}
				result, err := wrappedEndpoint(ctx, input.Arguments, input.CallOptions...)
				if err != nil {
					return nil, err
				}
				return &compose.ToolOutput{Result: result}, nil
			}
		}

		m.Streamable = func(next compose.StreamableToolEndpoint) compose.StreamableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.StreamToolOutput, error) {
				tCtx := &ToolContext{
					Name:   input.Name,
					CallID: input.CallID,
				}
				wrappedEndpoint, err := h.WrapStreamableToolCall(
					ctx,
					func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (*schema.StreamReader[string], error) {
						output, err := next(ctx, &compose.ToolInput{
							Name:        input.Name,
							CallID:      input.CallID,
							Arguments:   argumentsInJSON,
							CallOptions: opts,
						})
						if err != nil {
							return nil, err
						}
						return output.Result, nil
					},
					tCtx,
				)
				if err != nil {
					return nil, err
				}
				result, err := wrappedEndpoint(ctx, input.Arguments, input.CallOptions...)
				if err != nil {
					return nil, err
				}
				return &compose.StreamToolOutput{Result: result}, nil
			}
		}

		m.EnhancedInvokable = func(next compose.EnhancedInvokableToolEndpoint) compose.EnhancedInvokableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.EnhancedInvokableToolOutput, error) {
				tCtx := &ToolContext{
					Name:   input.Name,
					CallID: input.CallID,
				}
				wrappedEndpoint, err := h.WrapEnhancedInvokableToolCall(
					ctx,
					func(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.ToolResult, error) {
						output, err := next(ctx, &compose.ToolInput{
							Name:        input.Name,
							CallID:      input.CallID,
							Arguments:   toolArgument.Text,
							CallOptions: opts,
						})
						if err != nil {
							return nil, err
						}
						return output.Result, nil
					},
					tCtx,
				)
				if err != nil {
					return nil, err
				}
				result, err := wrappedEndpoint(ctx, &schema.ToolArgument{Text: input.Arguments}, input.CallOptions...)
				if err != nil {
					return nil, err
				}
				return &compose.EnhancedInvokableToolOutput{Result: result}, nil
			}
		}

		m.EnhancedStreamable = func(next compose.EnhancedStreamableToolEndpoint) compose.EnhancedStreamableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.EnhancedStreamableToolOutput, error) {
				tCtx := &ToolContext{
					Name:   input.Name,
					CallID: input.CallID,
				}
				wrappedEndpoint, err := h.WrapEnhancedStreamableToolCall(
					ctx,
					func(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.StreamReader[*schema.ToolResult], error) {
						output, err := next(ctx, &compose.ToolInput{
							Name:        input.Name,
							CallID:      input.CallID,
							Arguments:   toolArgument.Text,
							CallOptions: opts,
						})
						if err != nil {
							return nil, err
						}
						return output.Result, nil
					},
					tCtx,
				)
				if err != nil {
					return nil, err
				}
				result, err := wrappedEndpoint(ctx, &schema.ToolArgument{Text: input.Arguments}, input.CallOptions...)
				if err != nil {
					return nil, err
				}
				return &compose.EnhancedStreamableToolOutput{Result: result}, nil
			}
		}

		middlewares = append(middlewares, m)
	}
	return middlewares
}

type typedEventSenderModelWrapper[M MessageType] struct {
	*TypedBaseChatModelAgentMiddleware[M]
}

// NewEventSenderModelWrapper creates a ChatModelAgentMiddleware that sends model output as agent events.
func NewEventSenderModelWrapper() ChatModelAgentMiddleware {
	return &typedEventSenderModelWrapper[*schema.Message]{
		TypedBaseChatModelAgentMiddleware: &TypedBaseChatModelAgentMiddleware[*schema.Message]{},
	}
}

func (w *typedEventSenderModelWrapper[M]) WrapModel(_ context.Context, m model.BaseModel[M], mc *TypedModelContext[M]) (model.BaseModel[M], error) {
	inner := m
	if mc != nil && mc.cancelContext != nil {
		inner = &typedCancelMonitoredModel[M]{
			inner:         inner,
			cancelContext: mc.cancelContext,
		}
	}
	var retryConfig *TypedModelRetryConfig[M]
	if mc != nil {
		retryConfig = mc.ModelRetryConfig
	}
	var failoverConfig *ModelFailoverConfig[M]
	if mc != nil {
		failoverConfig = mc.ModelFailoverConfig
	}
	var timeoutConfig *ModelTimeoutConfig
	if mc != nil {
		timeoutConfig = mc.ModelTimeoutConfig
	}
	return &typedEventSenderModel[M]{inner: inner, modelRetryConfig: retryConfig, modelFailoverConfig: failoverConfig, modelTimeoutConfig: timeoutConfig}, nil
}

type typedEventSenderModel[M MessageType] struct {
	inner               model.BaseModel[M]
	modelRetryConfig    *TypedModelRetryConfig[M]
	modelFailoverConfig *ModelFailoverConfig[M]
	modelTimeoutConfig  *ModelTimeoutConfig
}

func sendSessionTimelineEvent[M MessageType](ctx context.Context, se *SessionEvent[M]) {
	execCtx := getTypedChatModelAgentExecCtx[M](ctx)
	if execCtx == nil || execCtx.generator == nil || se == nil {
		return
	}
	if !execCtx.timelineEvents && !execCtx.internalTimelineEvents {
		return
	}
	if se.Timestamp.IsZero() {
		se.Timestamp = newEventTimestamp()
	}
	if se.EventID == "" {
		if err := assignSessionEventIDFromContext(ctx, se); err != nil {
			execCtx.send(ctx, &TypedAgentEvent[M]{Timestamp: newEventTimestamp(), Err: err})
			return
		}
	}
	if err := ValidateEmittedSessionEventKind(se); err != nil {
		execCtx.send(ctx, &TypedAgentEvent[M]{Timestamp: newEventTimestamp(), Err: err})
		return
	}
	execCtx.send(ctx, &TypedAgentEvent[M]{EventID: se.EventID, Timestamp: se.Timestamp, SessionEvent: se})
}

func newModelSpanStartEvent[M MessageType](ctx context.Context, spanID string, started time.Time, opts ...model.Option) *SessionEvent[M] {
	meta := modelSpanMetaFromContext[M](ctx, opts...)
	meta.Model.Accepted = false
	return &SessionEvent[M]{
		Timestamp: started,
		Kind:      SessionEventSpanModelRequestStart,
		Span: &SpanEvent{
			SpanID:       spanID,
			Kind:         SpanKindModel,
			Name:         "model_request",
			StartedAt:    started,
			ParentSpanID: meta.ParentSpanID,
			Model:        meta.Model,
		},
	}
}

type modelSpanEndEventInput[M MessageType] struct {
	spanID       string
	startEventID string
	started      time.Time
	ended        time.Time
	msg          M
	err          error
	accepted     bool
	firstChunk   time.Duration
}

func newModelSpanEndEvent[M MessageType](ctx context.Context, in modelSpanEndEventInput[M], opts ...model.Option) *SessionEvent[M] {
	status := "ok"
	errStr := ""
	if in.err != nil {
		status = "error"
		errStr = in.err.Error()
		if errors.Is(in.err, context.Canceled) || errors.Is(in.err, ErrStreamCanceled) {
			status = "cancelled"
		}
	}
	return &SessionEvent[M]{
		Timestamp: in.ended,
		Kind:      SessionEventSpanModelRequestEnd,
		Span: &SpanEvent{
			SpanID:       in.spanID,
			Kind:         SpanKindModel,
			Name:         "model_request",
			StartedAt:    in.started,
			EndedAt:      in.ended,
			TTFTMS:       in.firstChunk.Milliseconds(),
			Status:       status,
			Err:          errStr,
			ParentSpanID: modelSpanMetaFromContext[M](ctx, opts...).ParentSpanID,
			Model:        modelSpanCompletionMeta(ctx, in.startEventID, in.msg, in.accepted && in.err == nil, in.err, opts...),
		},
	}
}

type modelSpanContextMeta struct {
	ParentSpanID string
	Model        *ModelSpanMeta
}

func modelSpanMetaFromContext[M MessageType](ctx context.Context, opts ...model.Option) modelSpanContextMeta {
	meta := &ModelSpanMeta{Attempt: 1}
	if common := model.GetCommonOptions(nil, opts...); common != nil && common.Model != nil {
		meta.Model = *common.Model
	}
	if currentModel, ok := typedGetFailoverCurrentModel[M](ctx); ok {
		if provider, ok := components.GetType(currentModel); ok {
			meta.Provider = provider
		}
	}
	parentSpanID := ""
	if failoverMeta, ok := getFailoverTimeline(ctx); ok {
		parentSpanID = failoverMeta.ParentSpanID
		if failoverMeta.Attempt > 0 {
			meta.Attempt = failoverMeta.Attempt
		}
	} else {
		_ = compose.ProcessState(ctx, func(_ context.Context, st *typedState[M]) error {
			meta.Attempt = st.getRetryAttempt() + 1
			return nil
		})
	}
	return modelSpanContextMeta{ParentSpanID: parentSpanID, Model: meta}
}

func modelSpanCompletionMeta[M MessageType](ctx context.Context, startEventID string, msg M, accepted bool, err error, opts ...model.Option) *ModelSpanMeta {
	meta := modelSpanMetaFromContext[M](ctx, opts...).Model
	meta.ModelRequestStartEventID = startEventID
	meta.Usage = modelUsageFromAssistant(msg)
	meta.FinishReason = assistantFinishReason(msg)
	meta.Accepted = accepted
	if timeoutErr, ok := AsModelTimeout(err); ok {
		meta.Timeout = &ModelTimeoutMeta{
			Phase:          string(timeoutErr.Phase),
			TimeoutMS:      timeoutErr.Timeout.Milliseconds(),
			ElapsedMS:      timeoutErr.Elapsed.Milliseconds(),
			ChunksReceived: timeoutErr.ChunksReceived,
		}
	}
	return meta
}

// lookupOrInitToolSpanInFlight returns the existing in-flight entry for the
// given tCtx.CallID (signalling a resumed call), or initializes a fresh entry
// (snapshotting CurrentModelSpanID / CurrentAssistantMessageEventID from
// typedState). It does NOT yet write the entry into typedState — the caller
// is responsible for invoking persistToolSpanInFlight after emitting the
// tool_call_start span and capturing its EventID.
//
// Reads (and the conditional snapshot) happen inside compose.ProcessState so
// that concurrent parallel tool calls are serialized safely — the framework
// guarantees the closure runs with exclusive access to typedState.
func lookupOrInitToolSpanInFlight[M MessageType](ctx context.Context, tCtx *ToolContext) (*toolSpanInFlight, bool) {
	var (
		entry    *toolSpanInFlight
		isResume bool
	)
	_ = compose.ProcessState(ctx, func(_ context.Context, st *typedState[M]) error {
		if existing, ok := st.ToolSpansInFlight[tCtx.CallID]; ok && existing != nil {
			entry = existing
			isResume = true
			return nil
		}
		entry = &toolSpanInFlight{
			SpanID:                  uuid.NewString(),
			StartedAt:               newEventTimestamp(),
			ParentSpanID:            st.CurrentModelSpanID,
			AssistantMessageEventID: st.CurrentAssistantMessageEventID,
		}
		return nil
	})
	return entry, isResume
}

// persistToolSpanInFlight writes the in-flight entry into typedState.ToolSpansInFlight
// keyed by callID. Called from the start-emission path after the start
// SessionEvent's EventID has been captured into the entry.
func persistToolSpanInFlight[M MessageType](ctx context.Context, callID string, entry *toolSpanInFlight) {
	_ = compose.ProcessState(ctx, func(_ context.Context, st *typedState[M]) error {
		if st.ToolSpansInFlight == nil {
			st.ToolSpansInFlight = make(map[string]*toolSpanInFlight)
		}
		st.ToolSpansInFlight[callID] = entry
		return nil
	})
}

// clearToolSpanInFlight removes the in-flight entry for callID. Called after
// the matching tool_call_end span has been emitted.
func clearToolSpanInFlight[M MessageType](ctx context.Context, callID string) {
	_ = compose.ProcessState(ctx, func(_ context.Context, st *typedState[M]) error {
		if st.ToolSpansInFlight == nil {
			return nil
		}
		delete(st.ToolSpansInFlight, callID)
		return nil
	})
}

func newToolSpanStartEvent[M MessageType](ctx context.Context, inFlight *toolSpanInFlight, tCtx *ToolContext) *SessionEvent[M] {
	return &SessionEvent[M]{
		Timestamp: inFlight.StartedAt,
		Kind:      SessionEventSpanToolCallStart,
		Span: &SpanEvent{
			SpanID:       inFlight.SpanID,
			ParentSpanID: inFlight.ParentSpanID,
			Kind:         SpanKindTool,
			Name:         "tool_call",
			StartedAt:    inFlight.StartedAt,
			Tool: &ToolSpanMeta{
				ToolUseID:               tCtx.CallID,
				Name:                    tCtx.Name,
				AssistantMessageEventID: inFlight.AssistantMessageEventID,
			},
		},
	}
}

type toolSpanEndEventInput struct {
	ended         time.Time
	err           error
	resultEventID string
}

func newToolSpanEndEvent[M MessageType](ctx context.Context, inFlight *toolSpanInFlight, tCtx *ToolContext, in toolSpanEndEventInput) *SessionEvent[M] {
	status := "ok"
	errStr := ""
	if in.err != nil {
		status = "error"
		errStr = in.err.Error()
		if errors.Is(in.err, context.Canceled) || errors.Is(in.err, ErrStreamCanceled) {
			status = "cancelled"
		}
	}
	ended := in.ended
	if ended.IsZero() {
		ended = newEventTimestamp()
	}
	return &SessionEvent[M]{
		Timestamp: ended,
		Kind:      SessionEventSpanToolCallEnd,
		Span: &SpanEvent{
			SpanID:       inFlight.SpanID,
			ParentSpanID: inFlight.ParentSpanID,
			Kind:         SpanKindTool,
			Name:         "tool_call",
			StartedAt:    inFlight.StartedAt,
			EndedAt:      ended,
			Status:       status,
			Err:          errStr,
			Tool: &ToolSpanMeta{
				ToolUseID:                tCtx.CallID,
				Name:                     tCtx.Name,
				ToolCallStartEventID:     inFlight.StartEventID,
				AssistantMessageEventID:  inFlight.AssistantMessageEventID,
				ToolResultMessageEventID: in.resultEventID,
			},
		},
	}
}

func (m *typedEventSenderModel[M]) Generate(ctx context.Context, input []M, opts ...model.Option) (M, error) {
	started := newEventTimestamp()
	spanID := uuid.NewString()
	startEvent := newModelSpanStartEvent[M](ctx, spanID, started, opts...)
	sendSessionTimelineEvent(ctx, startEvent)
	result, err := m.inner.Generate(ctx, input, opts...)
	ended := newEventTimestamp()
	sendSessionTimelineEvent(ctx, newModelSpanEndEvent(ctx, modelSpanEndEventInput[M]{
		spanID:       spanID,
		startEventID: startEvent.EventID,
		started:      started,
		ended:        ended,
		msg:          result,
		err:          err,
		accepted:     err == nil,
	}, opts...))
	if err != nil {
		var zero M
		return zero, err
	}
	timestamp := newEventTimestamp()

	execCtx := getTypedChatModelAgentExecCtx[M](ctx)
	if execCtx != nil && execCtx.suppressEventSend {
		return result, nil
	}
	if execCtx == nil || execCtx.generator == nil {
		var zero M
		return zero, errors.New("generator is nil when sending event in Generate: ensure agent state is properly initialized")
	}

	// Build a SessionEventMessage draft for the assistant message and route
	// its ID allocation through the runner's SessionEventIDGenerator[M] so
	// producer-owned identity applies. The same ID is used for the live
	// TypedAgentEvent below; the materialized SessionEvent later inherits it.
	assistantDraft := &SessionEvent[M]{Kind: SessionEventMessage, Message: copyMessage(result)}
	if err := assignSessionEventIDFromContext(ctx, assistantDraft); err != nil {
		var zero M
		return zero, err
	}
	assistantMsgEventID := assistantDraft.EventID

	// Persist the model span ID and assistant message event ID into typedState
	// so the tool wrapper can snapshot them into per-call ToolSpansInFlight
	// entries when emitting tool_call_start spans. The snapshot survives
	// interrupt/resume; the matching tool_call_end span (which may fire on
	// a later run) reads ParentSpanID and AssistantMessageEventID from the
	// snapshot, preserving the link to the original turn's model output.
	_ = compose.ProcessState(ctx, func(_ context.Context, st *typedState[M]) error {
		st.CurrentModelSpanID = spanID
		st.CurrentAssistantMessageEventID = assistantMsgEventID
		return nil
	})

	event := typedModelOutputEvent(copyMessage(result), nil)
	event.EventID = assistantMsgEventID
	event.Timestamp = timestamp
	execCtx.send(ctx, event)

	return result, nil
}

func (m *typedEventSenderModel[M]) Stream(ctx context.Context, input []M, opts ...model.Option) (*schema.StreamReader[M], error) {
	started := newEventTimestamp()
	spanID := uuid.NewString()
	startEvent := newModelSpanStartEvent[M](ctx, spanID, started, opts...)
	sendSessionTimelineEvent(ctx, startEvent)
	result, err := m.inner.Stream(ctx, input, opts...)
	if err != nil {
		sendSessionTimelineEvent(ctx, newModelSpanEndEvent(ctx, modelSpanEndEventInput[M]{
			spanID:       spanID,
			startEventID: startEvent.EventID,
			started:      started,
			ended:        newEventTimestamp(),
			msg:          *new(M),
			err:          err,
		}, opts...))
		return nil, err
	}
	timestamp := newEventTimestamp()

	execCtx := getTypedChatModelAgentExecCtx[M](ctx)
	if execCtx == nil || execCtx.generator == nil {
		result.Close()
		return nil, errors.New("generator is nil when sending event in Stream: ensure agent state is properly initialized")
	}

	streams := result.Copy(3)

	eventStream := streams[0]
	if convertOpts := m.buildStreamConvertOptions(ctx); len(convertOpts) > 0 {
		eventStream = schema.StreamReaderWithConvert(streams[0],
			func(msg M) (M, error) { return msg, nil },
			convertOpts...)
	}

	// Build a streaming-mode draft for the assistant message; the message
	// itself is materialized later by the consumer, but we route ID
	// allocation through the runner's SessionEventIDGenerator[M] now so the
	// live TypedAgentEvent and the eventual SessionEvent share a producer-
	// owned ID. Generators that need to recognize the assistant draft can
	// match on Kind==SessionEventMessage with a zero Message.
	var draftZero M
	assistantDraft := &SessionEvent[M]{Kind: SessionEventMessage, Message: draftZero}
	if err := assignSessionEventIDFromContext(ctx, assistantDraft); err != nil {
		result.Close()
		return nil, err
	}
	assistantMsgEventID := assistantDraft.EventID

	// Persist the model span ID and assistant message event ID into typedState
	// so the tool wrapper can snapshot them into per-call ToolSpansInFlight
	// entries when emitting tool_call_start spans. The snapshot survives
	// interrupt/resume; the matching tool_call_end span (which may fire on
	// a later run) reads ParentSpanID and AssistantMessageEventID from the
	// snapshot, preserving the link to the original turn's model output.
	_ = compose.ProcessState(ctx, func(_ context.Context, st *typedState[M]) error {
		st.CurrentModelSpanID = spanID
		st.CurrentAssistantMessageEventID = assistantMsgEventID
		return nil
	})

	var zero M
	event := typedModelOutputEvent[M](zero, eventStream)
	event.EventID = assistantMsgEventID
	event.Timestamp = timestamp
	execCtx.send(ctx, event)

	spanStream := streams[2]
	go func() {
		firstChunk := time.Duration(0)
		firstAt := time.Time{}
		var chunks []M
		var streamErr error
		for {
			msg, recvErr := spanStream.Recv()
			if recvErr == io.EOF {
				break
			}
			if recvErr != nil {
				streamErr = recvErr
				break
			}
			if firstAt.IsZero() {
				firstAt = newEventTimestamp()
				firstChunk = firstAt.Sub(started)
			}
			chunks = append(chunks, msg)
		}
		spanStream.Close()
		var final M
		if len(chunks) > 0 && streamErr == nil {
			final, streamErr = concatMessagesForSpan(chunks)
		}
		sendSessionTimelineEvent(ctx, newModelSpanEndEvent(ctx, modelSpanEndEventInput[M]{
			spanID:       spanID,
			startEventID: startEvent.EventID,
			started:      started,
			ended:        newEventTimestamp(),
			msg:          final,
			err:          streamErr,
			accepted:     streamErr == nil,
			firstChunk:   firstChunk,
		}, opts...))
	}()

	return streams[1], nil
}

func concatMessagesForSpan[M MessageType](chunks []M) (M, error) {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		msgs := make([]*schema.Message, 0, len(chunks))
		for _, chunk := range chunks {
			msgs = append(msgs, any(chunk).(*schema.Message))
		}
		msg, err := schema.ConcatMessages(msgs)
		if err != nil {
			return zero, err
		}
		return any(msg).(M), nil
	case *schema.AgenticMessage:
		msgs := make([]*schema.AgenticMessage, 0, len(chunks))
		for _, chunk := range chunks {
			msgs = append(msgs, any(chunk).(*schema.AgenticMessage))
		}
		msg, err := schema.ConcatAgenticMessages(msgs)
		if err != nil {
			return zero, err
		}
		return any(msg).(M), nil
	default:
		return zero, nil
	}
}

// buildStreamConvertOptions constructs ConvertOption hooks that gate stream termination behind
// the retry verdict signal protocol.
//
// Verdict signal lifecycle:
//   - streamWithShouldRetry creates a new retryVerdictSignal per retry attempt, stores it in
//     execCtx.retryVerdictSignal, and sends exactly one retryVerdict after ShouldRetry decides.
//   - The closures below capture a *retryVerdictSignal that is nil at closure-creation time; they
//     read the live value from execCtx.retryVerdictSignal, which is set before each model call.
//
// Two hooks cooperate to cover all stream termination paths:
//   - WithErrWrapper intercepts mid-stream errors. It blocks on the verdict to decide
//     whether to wrap the error as WillRetryError (rejected attempt) or pass it through (accepted).
//   - WithOnEOF intercepts clean EOF (successful stream). It blocks on the verdict to
//     either inject a WillRetryError (rejected) or pass through io.EOF (accepted).
//
// Both hooks share a sync.Once-guarded reader so the verdict channel is read at most once.
// This prevents a goroutine leak when a mid-stream error is followed by EOF: errWrapper fires
// first (caching the verdict), and onEOF reuses the cached value instead of blocking on a
// drained channel.
func (m *typedEventSenderModel[M]) buildStreamConvertOptions(ctx context.Context) []schema.ConvertOption {
	var retryAttempt int
	_ = compose.ProcessState(ctx, func(_ context.Context, st *typedState[M]) error {
		retryAttempt = st.getRetryAttempt()
		return nil
	})

	wrapWithCancelGuard := func(inner func(error) error) func(error) error {
		return func(err error) error {
			if errors.Is(err, ErrStreamCanceled) {
				return err
			}
			return inner(err)
		}
	}

	var opts []schema.ConvertOption

	var retryWrapper func(error) error
	if m.modelRetryConfig != nil {
		if m.modelRetryConfig.ShouldRetry != nil {
			execCtx := getTypedChatModelAgentExecCtx[M](ctx)
			signal := (*retryVerdictSignal)(nil)
			if execCtx != nil {
				signal = execCtx.retryVerdictSignal
			}
			if signal != nil {
				var (
					verdictOnce   sync.Once
					cachedVerdict retryVerdict
				)
				readVerdict := func() retryVerdict {
					verdictOnce.Do(func() {
						cachedVerdict = <-signal.ch
					})
					return cachedVerdict
				}

				retryWrapper = wrapWithCancelGuard(func(err error) error {
					verdict := readVerdict()
					if verdict.WillRetry {
						return &WillRetryError{
							ErrStr:       err.Error(),
							RetryAttempt: verdict.RetryAttempt,
							rejectReason: verdict.RejectReason,
							err:          err,
						}
					}
					return err
				})

				opts = append(opts, schema.WithOnEOF(func() (any, error) {
					verdict := readVerdict()
					if verdict.WillRetry {
						return nil, &WillRetryError{
							ErrStr:       verdict.Err.Error(),
							RetryAttempt: verdict.RetryAttempt,
							rejectReason: verdict.RejectReason,
							err:          verdict.Err,
						}
					}
					return nil, io.EOF
				}))
			}
		} else {
			retryWrapper = wrapWithCancelGuard(
				genErrWrapper(ctx, m.modelRetryConfig.MaxRetries, retryAttempt, m.modelRetryConfig.IsRetryAble),
			)
		}
	}

	hasFailover := m.modelFailoverConfig != nil
	// failoverHasMoreAttempts is set by failoverModelWrapper before each inner call.
	// It is true when additional failover attempts remain after the current one,
	// meaning stream errors should be wrapped as WillRetryError so the flow layer
	// skips them. On the final attempt it is false, so the error propagates normally.
	failoverHasMore := getFailoverHasMoreAttempts(ctx)

	if retryWrapper == nil && !(hasFailover && failoverHasMore) {
		return opts
	}

	combinedErrWrapper := func(err error) error {
		// If retry is configured and will retry this error, use the retry wrapper's WillRetryError.
		if retryWrapper != nil {
			wrapped := retryWrapper(err)
			if errors.As(wrapped, new(*WillRetryError)) {
				return wrapped
			}
		}
		// Retry won't handle this error (either exhausted or not configured), but
		// failover still has more attempts remaining. Wrap it as WillRetryError so
		// the flow layer skips this event from the failed attempt.
		if hasFailover && failoverHasMore {
			if errors.Is(err, ErrStreamCanceled) {
				return err
			}
			return &WillRetryError{ErrStr: err.Error(), err: err}
		}
		return err
	}
	opts = append(opts, schema.WithErrWrapper(combinedErrWrapper))

	return opts
}

func copyMessage[M MessageType](msg M) M {
	switch v := any(msg).(type) {
	case *schema.Message:
		cp := *v
		return any(&cp).(M)
	case *schema.AgenticMessage:
		cp := *v
		return any(&cp).(M)
	default:
		return msg
	}
}

// typedSetMessageID sets a specific message ID in Extra.
func typedSetMessageID[M MessageType](msg M, id string) {
	switch v := any(msg).(type) {
	case *schema.Message:
		v.Extra = internal.SetMessageID(v.Extra, id)
	case *schema.AgenticMessage:
		v.Extra = internal.SetMessageID(v.Extra, id)
	}
}

// GetMessageID returns the eino-internal message ID from the given message, or "".
func GetMessageID[M MessageType](msg M) string {
	switch v := any(msg).(type) {
	case *schema.Message:
		return internal.GetMessageID(v.Extra)
	case *schema.AgenticMessage:
		return internal.GetMessageID(v.Extra)
	default:
		return ""
	}
}

// EnsureMessageID assigns a UUID v4 message ID if the message doesn't have one.
// Idempotent: if ID already set, no-op.
// Middleware authors should call this before SendEvent if they create messages.
func EnsureMessageID[M MessageType](msg M) {
	switch v := any(msg).(type) {
	case *schema.Message:
		v.Extra = internal.EnsureMessageID(v.Extra)
	case *schema.AgenticMessage:
		v.Extra = internal.EnsureMessageID(v.Extra)
	}
}

func typedPopToolGenAction[M MessageType](ctx context.Context, toolName string) *AgentAction {
	toolCallID := compose.GetToolCallID(ctx)

	var action *AgentAction
	_ = compose.ProcessState(ctx, func(ctx context.Context, st *typedState[M]) error {
		if len(toolCallID) > 0 {
			if a := st.popToolGenAction(toolCallID); a != nil {
				action = a
				return nil
			}
		}

		if a := st.popToolGenAction(toolName); a != nil {
			action = a
		}

		return nil
	})

	return action
}

type typedEventSenderToolWrapper[M MessageType] struct {
	*TypedBaseChatModelAgentMiddleware[M]
}

func (*typedEventSenderToolWrapper[M]) isEventSenderToolWrapper() {}

// eventSenderToolWrapperMarker enables cross-type detection of eventSenderToolWrapper
// in generic contexts. hasUserEventSenderToolWrapper[M] receives
// []TypedChatModelAgentMiddleware[M], so when M is *schema.AgenticMessage, a direct
// type assertion to *eventSenderToolWrapper (which implements the *schema.Message alias)
// would fail. The marker interface bridges this gap.
type eventSenderToolWrapperMarker interface{ isEventSenderToolWrapper() }

// NewEventSenderToolWrapper returns a ChatModelAgentMiddleware that sends tool result events.
// By default, the framework places this before all user middlewares (outermost), so events
// reflect the fully processed tool output. To control exactly where events are emitted,
// include this in ChatModelAgentConfig.Handlers at the desired position.
// When detected in Handlers, the framework skips the default event sender to avoid duplicates.
func NewEventSenderToolWrapper() ChatModelAgentMiddleware {
	return newTypedEventSenderToolWrapper[*schema.Message]()
}

// newTypedEventSenderToolWrapper creates a typed event sender wrapper for the given message type.
// This is used internally to ensure the default event sender matches the agent's message type
// (e.g. *schema.AgenticMessage agents need an AgenticMessage-typed wrapper so that
// compose.ProcessState can access the correct state type).
func newTypedEventSenderToolWrapper[M MessageType]() *typedEventSenderToolWrapper[M] {
	return &typedEventSenderToolWrapper[M]{
		TypedBaseChatModelAgentMiddleware: &TypedBaseChatModelAgentMiddleware[M]{},
	}
}

// textToFunctionToolResultBlocks wraps a plain text string into FunctionToolResultBlocks.
func textToFunctionToolResultBlocks(text string) []*schema.FunctionToolResultContentBlock {
	if text == "" {
		return nil
	}
	return []*schema.FunctionToolResultContentBlock{
		{Type: schema.FunctionToolResultContentBlockTypeText, Text: &schema.UserInputText{Text: text}},
	}
}

// functionToolResultAgenticMessage constructs a function tool result message with AgenticRoleType "user".
func functionToolResultAgenticMessage(callID, name string, content []*schema.FunctionToolResultContentBlock) *schema.AgenticMessage {
	return &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeUser,
		ContentBlocks: []*schema.ContentBlock{
			schema.NewContentBlock(&schema.FunctionToolResult{
				CallID:  callID,
				Name:    name,
				Content: content,
			}),
		},
	}
}

func markAgenticMessageStreamingMeta(msg *schema.AgenticMessage, index int) {
	if msg == nil {
		return
	}
	for _, block := range msg.ContentBlocks {
		if block == nil {
			continue
		}
		block.StreamingMeta = &schema.StreamingMeta{Index: index}
	}
}

func toolSearchResultAgenticMessage(callID, name string, tr *schema.ToolResult) (*schema.AgenticMessage, bool) {
	if tr == nil || len(tr.Parts) != 1 {
		return nil, false
	}

	part := tr.Parts[0]
	if part.Type != schema.ToolPartTypeToolSearchResult || part.ToolSearchResult == nil {
		return nil, false
	}

	return &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeUser,
		ContentBlocks: []*schema.ContentBlock{
			schema.NewContentBlock(&schema.ToolSearchFunctionToolResult{
				CallID: callID,
				Name:   name,
				Result: part.ToolSearchResult,
			}),
		},
	}, true
}

func toolResultAgenticMessage(callID, name string, tr *schema.ToolResult) *schema.AgenticMessage {
	if msg, ok := toolSearchResultAgenticMessage(callID, name, tr); ok {
		return msg
	}
	return functionToolResultAgenticMessage(callID, name, toolResultToBlocks(tr))
}

// toolResultToBlocks converts a ToolResult's multimodal parts into FunctionToolResultBlocks.
// This preserves all media types (text, image, audio, video, file), unlike toolResultText
// which only extracts text.
func toolResultToBlocks(tr *schema.ToolResult) []*schema.FunctionToolResultContentBlock {
	if tr == nil || len(tr.Parts) == 0 {
		return nil
	}
	blocks := make([]*schema.FunctionToolResultContentBlock, 0, len(tr.Parts))
	for _, p := range tr.Parts {
		var block *schema.FunctionToolResultContentBlock
		switch p.Type {
		case schema.ToolPartTypeText:
			block = &schema.FunctionToolResultContentBlock{
				Type:  schema.FunctionToolResultContentBlockTypeText,
				Text:  &schema.UserInputText{Text: p.Text},
				Extra: p.Extra,
			}
		case schema.ToolPartTypeImage:
			if p.Image != nil {
				block = &schema.FunctionToolResultContentBlock{
					Type: schema.FunctionToolResultContentBlockTypeImage,
					Image: &schema.UserInputImage{
						URL:        derefString(p.Image.URL),
						Base64Data: derefString(p.Image.Base64Data),
						MIMEType:   p.Image.MIMEType,
					},
					Extra: p.Extra,
				}
			}
		case schema.ToolPartTypeAudio:
			if p.Audio != nil {
				block = &schema.FunctionToolResultContentBlock{
					Type: schema.FunctionToolResultContentBlockTypeAudio,
					Audio: &schema.UserInputAudio{
						URL:        derefString(p.Audio.URL),
						Base64Data: derefString(p.Audio.Base64Data),
						MIMEType:   p.Audio.MIMEType,
					},
					Extra: p.Extra,
				}
			}
		case schema.ToolPartTypeVideo:
			if p.Video != nil {
				block = &schema.FunctionToolResultContentBlock{
					Type: schema.FunctionToolResultContentBlockTypeVideo,
					Video: &schema.UserInputVideo{
						URL:        derefString(p.Video.URL),
						Base64Data: derefString(p.Video.Base64Data),
						MIMEType:   p.Video.MIMEType,
					},
					Extra: p.Extra,
				}
			}
		case schema.ToolPartTypeFile:
			if p.File != nil {
				block = &schema.FunctionToolResultContentBlock{
					Type: schema.FunctionToolResultContentBlockTypeFile,
					File: &schema.UserInputFile{
						URL:        derefString(p.File.URL),
						Base64Data: derefString(p.File.Base64Data),
						MIMEType:   p.File.MIMEType,
					},
					Extra: p.Extra,
				}
			}
		}
		if block != nil {
			blocks = append(blocks, block)
		}
	}
	return blocks
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// typedToolInvokeEvent constructs the tool result event for the invoke path,
// dispatching on M to create the correct message and event types.
func typedToolInvokeEvent[M MessageType](callID, toolName, result, toolMsgID string) *TypedAgentEvent[M] {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		msg := schema.ToolMessage(result, callID, schema.WithToolName(toolName))
		msg.Extra = internal.SetMessageID(msg.Extra, toolMsgID)
		event := EventFromMessage(msg, nil, schema.Tool, toolName)
		return any(event).(*TypedAgentEvent[M])
	case *schema.AgenticMessage:
		msg := functionToolResultAgenticMessage(callID, toolName, textToFunctionToolResultBlocks(result))
		msg.Extra = internal.SetMessageID(msg.Extra, toolMsgID)
		event := EventFromAgenticMessage(msg, nil, schema.AgenticRoleTypeUser)
		return any(event).(*TypedAgentEvent[M])
	default:
		return nil
	}
}

// typedToolStreamEvent constructs the tool result event for the stream path,
// dispatching on M to create the correct message stream and event types.
func typedToolStreamEvent[M MessageType](callID, toolName, toolMsgID string, stream *schema.StreamReader[string]) *TypedAgentEvent[M] {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		first := true
		cvt := func(in string) (Message, error) {
			msg := schema.ToolMessage(in, callID, schema.WithToolName(toolName))
			if first {
				first = false
				msg.Extra = internal.SetMessageID(msg.Extra, toolMsgID)
			}
			return msg, nil
		}
		msgStream := schema.StreamReaderWithConvert(stream, cvt)
		event := EventFromMessage(nil, msgStream, schema.Tool, toolName)
		return any(event).(*TypedAgentEvent[M])
	case *schema.AgenticMessage:
		first := true
		cvt := func(in string) (*schema.AgenticMessage, error) {
			msg := functionToolResultAgenticMessage(callID, toolName, textToFunctionToolResultBlocks(in))
			markAgenticMessageStreamingMeta(msg, 0)
			if first {
				first = false
				msg.Extra = internal.SetMessageID(msg.Extra, toolMsgID)
			}
			return msg, nil
		}
		msgStream := schema.StreamReaderWithConvert(stream, cvt)
		event := EventFromAgenticMessage(nil, msgStream, schema.AgenticRoleTypeUser)
		return any(event).(*TypedAgentEvent[M])
	default:
		return nil
	}
}

// typedToolEnhancedInvokeEvent constructs the tool result event for the enhanced invoke path.
// For *schema.Message it builds a multimodal tool message; for *schema.AgenticMessage it
// uses the string content of the result (AgenticToolsNode only uses the string path).
func typedToolEnhancedInvokeEvent[M MessageType](callID, toolName, toolMsgID string, result *schema.ToolResult) (*TypedAgentEvent[M], error) {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		msg := schema.ToolMessage("", callID, schema.WithToolName(toolName))
		var err error
		msg.UserInputMultiContent, err = result.ToMessageInputParts()
		if err != nil {
			return nil, err
		}
		msg.Extra = internal.SetMessageID(msg.Extra, toolMsgID)
		event := EventFromMessage(msg, nil, schema.Tool, toolName)
		return any(event).(*TypedAgentEvent[M]), nil
	case *schema.AgenticMessage:
		msg := toolResultAgenticMessage(callID, toolName, result)
		msg.Extra = internal.SetMessageID(msg.Extra, toolMsgID)
		event := EventFromAgenticMessage(msg, nil, schema.AgenticRoleTypeUser)
		return any(event).(*TypedAgentEvent[M]), nil
	default:
		return nil, nil
	}
}

// typedToolEnhancedStreamEvent constructs the tool result event for the enhanced stream path.
// For *schema.Message it builds multimodal tool messages; for *schema.AgenticMessage it
// converts each chunk's multimodal parts into FunctionToolResultBlocks.
func typedToolEnhancedStreamEvent[M MessageType](callID, toolName, toolMsgID string, stream *schema.StreamReader[*schema.ToolResult]) *TypedAgentEvent[M] {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		first := true
		cvt := func(in *schema.ToolResult) (Message, error) {
			msg := schema.ToolMessage("", callID, schema.WithToolName(toolName))
			var cvtErr error
			msg.UserInputMultiContent, cvtErr = in.ToMessageInputParts()
			if cvtErr != nil {
				return nil, cvtErr
			}
			if first {
				first = false
				msg.Extra = internal.SetMessageID(msg.Extra, toolMsgID)
			}
			return msg, nil
		}
		msgStream := schema.StreamReaderWithConvert(stream, cvt)
		event := EventFromMessage(nil, msgStream, schema.Tool, toolName)
		return any(event).(*TypedAgentEvent[M])
	case *schema.AgenticMessage:
		first := true
		cvt := func(in *schema.ToolResult) (*schema.AgenticMessage, error) {
			msg := toolResultAgenticMessage(callID, toolName, in)
			markAgenticMessageStreamingMeta(msg, 0)
			if first {
				first = false
				msg.Extra = internal.SetMessageID(msg.Extra, toolMsgID)
			}
			return msg, nil
		}
		msgStream := schema.StreamReaderWithConvert(stream, cvt)
		event := EventFromAgenticMessage(nil, msgStream, schema.AgenticRoleTypeUser)
		return any(event).(*TypedAgentEvent[M])
	default:
		return nil
	}
}

func (w *typedEventSenderToolWrapper[M]) WrapInvokableToolCall(_ context.Context, endpoint InvokableToolCallEndpoint, tCtx *ToolContext) (InvokableToolCallEndpoint, error) {
	return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
		inFlight, isResume := lookupOrInitToolSpanInFlight[M](ctx, tCtx)
		if !isResume {
			startEvent := newToolSpanStartEvent[M](ctx, inFlight, tCtx)
			sendSessionTimelineEvent(ctx, startEvent)
			inFlight.StartEventID = startEvent.EventID
			persistToolSpanInFlight[M](ctx, tCtx.CallID, inFlight)
		}

		result, err := endpoint(ctx, argumentsInJSON, opts...)
		if err != nil {
			if _, isInterrupt := compose.IsInterruptRerunError(err); isInterrupt {
				// An interrupt-shape error means the tool did not complete; the call is
				// paused awaiting resume. Defer the end span: leave the in-flight entry
				// in typedState so the next invocation of this wrapper for the same
				// CallID reuses the SpanID and emits the matching end span. See §3.1
				// and §3.6 of the design plan for the full lifecycle.
				return "", err
			}
			sendSessionTimelineEvent(ctx, newToolSpanEndEvent[M](ctx, inFlight, tCtx, toolSpanEndEventInput{
				ended: newEventTimestamp(),
				err:   err,
			}))
			clearToolSpanInFlight[M](ctx, tCtx.CallID)
			return "", err
		}
		timestamp := newEventTimestamp()

		toolName := tCtx.Name
		callID := tCtx.CallID

		prePopAction := typedPopToolGenAction[M](ctx, toolName)
		toolMsgID := uuid.NewString()
		event := typedToolInvokeEvent[M](callID, toolName, result, toolMsgID)
		// Route the tool result message ID through the runner's
		// SessionEventIDGenerator[M] via a SessionEventMessage draft so
		// custom-tool-result generators see the populated message. Fail-closed:
		// on allocation failure, skip both the tool result event and the
		// matching tool span end so no orphaned ToolResultMessageEventID
		// reference is left in the timeline.
		toolResultDraft := &SessionEvent[M]{Kind: SessionEventMessage, Message: event.Output.MessageOutput.Message}
		if idErr := assignSessionEventIDFromContext(ctx, toolResultDraft); idErr != nil {
			if execCtx := getTypedChatModelAgentExecCtx[M](ctx); execCtx != nil && execCtx.generator != nil {
				execCtx.send(ctx, &TypedAgentEvent[M]{Timestamp: newEventTimestamp(), Err: idErr})
			}
			clearToolSpanInFlight[M](ctx, tCtx.CallID)
			return "", idErr
		}
		resultEventID := toolResultDraft.EventID
		event.EventID = resultEventID
		event.Timestamp = timestamp
		if prePopAction != nil {
			event.Action = prePopAction
		}

		execCtx := getTypedChatModelAgentExecCtx[M](ctx)
		_ = compose.ProcessState(ctx, func(_ context.Context, st *typedState[M]) error {
			st.setToolMsgID(toolName, callID, toolMsgID)
			if st.getReturnDirectlyToolCallID() == callID {
				st.setReturnDirectlyEvent(event)
			} else {
				execCtx.send(ctx, event)
			}
			return nil
		})

		sendSessionTimelineEvent(ctx, newToolSpanEndEvent[M](ctx, inFlight, tCtx, toolSpanEndEventInput{
			ended:         newEventTimestamp(),
			resultEventID: resultEventID,
		}))
		clearToolSpanInFlight[M](ctx, tCtx.CallID)

		return result, nil
	}, nil
}

func (w *typedEventSenderToolWrapper[M]) WrapStreamableToolCall(_ context.Context, endpoint StreamableToolCallEndpoint, tCtx *ToolContext) (StreamableToolCallEndpoint, error) {
	return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (*schema.StreamReader[string], error) {
		inFlight, isResume := lookupOrInitToolSpanInFlight[M](ctx, tCtx)
		if !isResume {
			startEvent := newToolSpanStartEvent[M](ctx, inFlight, tCtx)
			sendSessionTimelineEvent(ctx, startEvent)
			inFlight.StartEventID = startEvent.EventID
			persistToolSpanInFlight[M](ctx, tCtx.CallID, inFlight)
		}

		result, err := endpoint(ctx, argumentsInJSON, opts...)
		if err != nil {
			if _, isInterrupt := compose.IsInterruptRerunError(err); isInterrupt {
				// Defer end span; in-flight entry remains for resume. See §3.1.
				return nil, err
			}
			sendSessionTimelineEvent(ctx, newToolSpanEndEvent[M](ctx, inFlight, tCtx, toolSpanEndEventInput{
				ended: newEventTimestamp(),
				err:   err,
			}))
			clearToolSpanInFlight[M](ctx, tCtx.CallID)
			return nil, err
		}
		timestamp := newEventTimestamp()

		toolName := tCtx.Name
		callID := tCtx.CallID

		prePopAction := typedPopToolGenAction[M](ctx, toolName)
		streams := result.Copy(2)

		toolMsgID := uuid.NewString()
		// Streaming tool result: the materialized message is not yet
		// available, so the draft carries a zero M and SessionEventMessage
		// kind. ID allocation flows through the runner's
		// SessionEventIDGenerator[M]. Fail-closed: on allocation failure,
		// skip the tool result event AND the tool span end so no orphaned
		// ToolResultMessageEventID reference is left behind.
		var toolResultDraftMsg M
		toolResultDraft := &SessionEvent[M]{Kind: SessionEventMessage, Message: toolResultDraftMsg}
		if idErr := assignSessionEventIDFromContext(ctx, toolResultDraft); idErr != nil {
			if execCtx := getTypedChatModelAgentExecCtx[M](ctx); execCtx != nil && execCtx.generator != nil {
				execCtx.send(ctx, &TypedAgentEvent[M]{Timestamp: newEventTimestamp(), Err: idErr})
			}
			streams[0].Close()
			streams[1].Close()
			clearToolSpanInFlight[M](ctx, tCtx.CallID)
			return nil, idErr
		}
		resultEventID := toolResultDraft.EventID

		// End-span emission for streamable tools attaches to the caller's
		// stream copy via schema.WithOnEOF (success path) and
		// schema.WithErrWrapper (error / cancellation path). Both hooks fire
		// synchronously inside the consumer's recv() call, so the end span
		// is enqueued before the agent's event generator closes — this is
		// what avoids the race the previous goroutine drainer hit.
		//
		// Residual risk: the hooks fire only when the consumer drives the
		// stream to a terminal state (io.EOF or non-EOF error). If a
		// consumer abandons the stream mid-flight (e.g. calls Close() early,
		// or a tool implementation ignores ctx and produces unbounded
		// chunks while the consumer stops calling Recv), neither hook fires
		// and only the start span is persisted. Correctness is unaffected;
		// observability shows an unmatched start span. If this ever becomes
		// load-bearing, the fix is to reintroduce a goroutine drainer as a
		// fallback gated by spanEndOnce, with a per-run WaitGroup on the
		// exec ctx so the agent waits for it before closing the generator.
		//
		// emitEnd is attached to the caller's stream copy via WithOnEOF and
		// WithErrWrapper. v3 makes it interrupt-aware: an interrupt-shape
		// streamErr means the tool did not complete on this run, so neither
		// emit the end span nor delete the in-flight entry. The next resume
		// re-invokes this wrapper, sees the in-flight entry, and continues
		// until a terminal state (success EOF, hard error, or cancellation)
		// fires. See §3.5 of the design plan for the full lifecycle.
		var spanEndOnce sync.Once
		emitEnd := func(streamErr error) {
			spanEndOnce.Do(func() {
				if streamErr != nil {
					if _, isInterrupt := compose.IsInterruptRerunError(streamErr); isInterrupt {
						return
					}
				}
				in := toolSpanEndEventInput{
					ended: newEventTimestamp(),
					err:   streamErr,
				}
				if streamErr == nil {
					in.resultEventID = resultEventID
				}
				sendSessionTimelineEvent(ctx, newToolSpanEndEvent[M](ctx, inFlight, tCtx, in))
				clearToolSpanInFlight[M](ctx, tCtx.CallID)
			})
		}

		event := typedToolStreamEvent[M](callID, toolName, toolMsgID, streams[0])
		event.EventID = resultEventID
		event.Timestamp = timestamp
		event.Action = prePopAction

		execCtx := getTypedChatModelAgentExecCtx[M](ctx)
		_ = compose.ProcessState(ctx, func(_ context.Context, st *typedState[M]) error {
			st.setToolMsgID(toolName, callID, toolMsgID)
			if st.getReturnDirectlyToolCallID() == callID {
				st.setReturnDirectlyEvent(event)
			} else {
				execCtx.send(ctx, event)
			}
			return nil
		})

		callerStream := schema.StreamReaderWithConvert(streams[1],
			func(s string) (string, error) { return s, nil },
			schema.WithOnEOF(func() (any, error) {
				emitEnd(nil)
				return nil, io.EOF
			}),
			schema.WithErrWrapper(func(streamErr error) error {
				emitEnd(streamErr)
				return streamErr
			}),
		)
		return callerStream, nil
	}, nil
}

func (w *typedEventSenderToolWrapper[M]) WrapEnhancedInvokableToolCall(_ context.Context, endpoint EnhancedInvokableToolCallEndpoint, tCtx *ToolContext) (EnhancedInvokableToolCallEndpoint, error) {
	return func(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.ToolResult, error) {
		inFlight, isResume := lookupOrInitToolSpanInFlight[M](ctx, tCtx)
		if !isResume {
			startEvent := newToolSpanStartEvent[M](ctx, inFlight, tCtx)
			sendSessionTimelineEvent(ctx, startEvent)
			inFlight.StartEventID = startEvent.EventID
			persistToolSpanInFlight[M](ctx, tCtx.CallID, inFlight)
		}

		result, err := endpoint(ctx, toolArgument, opts...)
		if err != nil {
			if _, isInterrupt := compose.IsInterruptRerunError(err); isInterrupt {
				// Defer end span; in-flight entry remains for resume. See §3.1.
				return nil, err
			}
			sendSessionTimelineEvent(ctx, newToolSpanEndEvent[M](ctx, inFlight, tCtx, toolSpanEndEventInput{
				ended: newEventTimestamp(),
				err:   err,
			}))
			clearToolSpanInFlight[M](ctx, tCtx.CallID)
			return nil, err
		}
		timestamp := newEventTimestamp()

		toolName := tCtx.Name
		callID := tCtx.CallID

		prePopAction := typedPopToolGenAction[M](ctx, toolName)
		toolMsgID := uuid.NewString()
		event, eventErr := typedToolEnhancedInvokeEvent[M](callID, toolName, toolMsgID, result)
		if eventErr != nil {
			sendSessionTimelineEvent(ctx, newToolSpanEndEvent[M](ctx, inFlight, tCtx, toolSpanEndEventInput{
				ended: newEventTimestamp(),
				err:   eventErr,
			}))
			clearToolSpanInFlight[M](ctx, tCtx.CallID)
			return nil, eventErr
		}
		// Route the enhanced-invoke tool result message ID through the
		// runner's SessionEventIDGenerator[M] via a SessionEventMessage
		// draft. Fail-closed: on allocation failure, skip both the tool
		// result event and the matching tool span end so no orphaned
		// ToolResultMessageEventID reference is left behind.
		toolResultDraft := &SessionEvent[M]{Kind: SessionEventMessage, Message: event.Output.MessageOutput.Message}
		if idErr := assignSessionEventIDFromContext(ctx, toolResultDraft); idErr != nil {
			if execCtx := getTypedChatModelAgentExecCtx[M](ctx); execCtx != nil && execCtx.generator != nil {
				execCtx.send(ctx, &TypedAgentEvent[M]{Timestamp: newEventTimestamp(), Err: idErr})
			}
			clearToolSpanInFlight[M](ctx, tCtx.CallID)
			return nil, idErr
		}
		resultEventID := toolResultDraft.EventID
		event.EventID = resultEventID
		event.Timestamp = timestamp
		if prePopAction != nil {
			event.Action = prePopAction
		}

		execCtx := getTypedChatModelAgentExecCtx[M](ctx)
		_ = compose.ProcessState(ctx, func(_ context.Context, st *typedState[M]) error {
			st.setToolMsgID(toolName, callID, toolMsgID)
			if st.getReturnDirectlyToolCallID() == callID {
				st.setReturnDirectlyEvent(event)
			} else {
				execCtx.send(ctx, event)
			}
			return nil
		})

		sendSessionTimelineEvent(ctx, newToolSpanEndEvent[M](ctx, inFlight, tCtx, toolSpanEndEventInput{
			ended:         newEventTimestamp(),
			resultEventID: resultEventID,
		}))
		clearToolSpanInFlight[M](ctx, tCtx.CallID)

		return result, nil
	}, nil
}

func (w *typedEventSenderToolWrapper[M]) WrapEnhancedStreamableToolCall(_ context.Context, endpoint EnhancedStreamableToolCallEndpoint, tCtx *ToolContext) (EnhancedStreamableToolCallEndpoint, error) {
	return func(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.StreamReader[*schema.ToolResult], error) {
		inFlight, isResume := lookupOrInitToolSpanInFlight[M](ctx, tCtx)
		if !isResume {
			startEvent := newToolSpanStartEvent[M](ctx, inFlight, tCtx)
			sendSessionTimelineEvent(ctx, startEvent)
			inFlight.StartEventID = startEvent.EventID
			persistToolSpanInFlight[M](ctx, tCtx.CallID, inFlight)
		}

		result, err := endpoint(ctx, toolArgument, opts...)
		if err != nil {
			if _, isInterrupt := compose.IsInterruptRerunError(err); isInterrupt {
				// Defer end span; in-flight entry remains for resume. See §3.1.
				return nil, err
			}
			sendSessionTimelineEvent(ctx, newToolSpanEndEvent[M](ctx, inFlight, tCtx, toolSpanEndEventInput{
				ended: newEventTimestamp(),
				err:   err,
			}))
			clearToolSpanInFlight[M](ctx, tCtx.CallID)
			return nil, err
		}
		timestamp := newEventTimestamp()

		toolName := tCtx.Name
		callID := tCtx.CallID

		prePopAction := typedPopToolGenAction[M](ctx, toolName)
		streams := result.Copy(2)

		toolMsgID := uuid.NewString()
		// Streaming tool result: the materialized message is not yet
		// available, so the draft carries a zero M and SessionEventMessage
		// kind. ID allocation flows through the runner's
		// SessionEventIDGenerator[M]. Fail-closed: on allocation failure,
		// skip the tool result event AND the tool span end so no orphaned
		// ToolResultMessageEventID reference is left behind.
		var toolResultDraftMsg M
		toolResultDraft := &SessionEvent[M]{Kind: SessionEventMessage, Message: toolResultDraftMsg}
		if idErr := assignSessionEventIDFromContext(ctx, toolResultDraft); idErr != nil {
			if execCtx := getTypedChatModelAgentExecCtx[M](ctx); execCtx != nil && execCtx.generator != nil {
				execCtx.send(ctx, &TypedAgentEvent[M]{Timestamp: newEventTimestamp(), Err: idErr})
			}
			streams[0].Close()
			streams[1].Close()
			clearToolSpanInFlight[M](ctx, tCtx.CallID)
			return nil, idErr
		}
		resultEventID := toolResultDraft.EventID

		// End-span emission for streamable tools attaches to the caller's
		// stream copy via schema.WithOnEOF (success path) and
		// schema.WithErrWrapper (error / cancellation path). Both hooks fire
		// synchronously inside the consumer's recv() call, so the end span
		// is enqueued before the agent's event generator closes — this is
		// what avoids the race the previous goroutine drainer hit.
		//
		// Residual risk: the hooks fire only when the consumer drives the
		// stream to a terminal state (io.EOF or non-EOF error). If a
		// consumer abandons the stream mid-flight (e.g. calls Close() early,
		// or a tool implementation ignores ctx and produces unbounded
		// chunks while the consumer stops calling Recv), neither hook fires
		// and only the start span is persisted. Correctness is unaffected;
		// observability shows an unmatched start span. If this ever becomes
		// load-bearing, the fix is to reintroduce a goroutine drainer as a
		// fallback gated by spanEndOnce, with a per-run WaitGroup on the
		// exec ctx so the agent waits for it before closing the generator.
		//
		// emitEnd is attached to the caller's stream copy via WithOnEOF and
		// WithErrWrapper. v3 makes it interrupt-aware: an interrupt-shape
		// streamErr means the tool did not complete on this run, so neither
		// emit the end span nor delete the in-flight entry. The next resume
		// re-invokes this wrapper, sees the in-flight entry, and continues
		// until a terminal state (success EOF, hard error, or cancellation)
		// fires. See §3.5 of the design plan for the full lifecycle.
		var spanEndOnce sync.Once
		emitEnd := func(streamErr error) {
			spanEndOnce.Do(func() {
				if streamErr != nil {
					if _, isInterrupt := compose.IsInterruptRerunError(streamErr); isInterrupt {
						return
					}
				}
				in := toolSpanEndEventInput{
					ended: newEventTimestamp(),
					err:   streamErr,
				}
				if streamErr == nil {
					in.resultEventID = resultEventID
				}
				sendSessionTimelineEvent(ctx, newToolSpanEndEvent[M](ctx, inFlight, tCtx, in))
				clearToolSpanInFlight[M](ctx, tCtx.CallID)
			})
		}

		event := typedToolEnhancedStreamEvent[M](callID, toolName, toolMsgID, streams[0])
		event.EventID = resultEventID
		event.Timestamp = timestamp
		event.Action = prePopAction

		execCtx := getTypedChatModelAgentExecCtx[M](ctx)
		_ = compose.ProcessState(ctx, func(_ context.Context, st *typedState[M]) error {
			st.setToolMsgID(toolName, callID, toolMsgID)
			if st.getReturnDirectlyToolCallID() == callID {
				st.setReturnDirectlyEvent(event)
			} else {
				execCtx.send(ctx, event)
			}
			return nil
		})

		callerStream := schema.StreamReaderWithConvert(streams[1],
			func(tr *schema.ToolResult) (*schema.ToolResult, error) { return tr, nil },
			schema.WithOnEOF(func() (any, error) {
				emitEnd(nil)
				return nil, io.EOF
			}),
			schema.WithErrWrapper(func(streamErr error) error {
				emitEnd(streamErr)
				return streamErr
			}),
		)
		return callerStream, nil
	}, nil
}

// drainStringToolResultForSpan and drainEnhancedToolResultForSpan are no
// longer needed; the streamable wrappers attach end-span emission via
// schema.WithOnEOF / schema.WithErrWrapper hooks on the caller's stream copy
// instead. The hook approach fires synchronously during the consumer's read
// loop, eliminating the goroutine race where the agent's event generator
// could close before the drainer's emission landed.

func hasUserEventSenderToolWrapper[M MessageType](handlers []TypedChatModelAgentMiddleware[M]) bool {
	for _, handler := range handlers {
		if _, ok := any(handler).(eventSenderToolWrapperMarker); ok {
			return true
		}
	}
	return false
}

type typedStateModelWrapper[M MessageType] struct {
	inner               model.BaseModel[M]
	original            model.BaseModel[M]
	handlers            []TypedChatModelAgentMiddleware[M]
	middlewares         []AgentMiddleware
	toolInfos           []*schema.ToolInfo
	modelRetryConfig    *TypedModelRetryConfig[M]
	modelFailoverConfig *ModelFailoverConfig[M]
	modelTimeoutConfig  *ModelTimeoutConfig
	cancelContext       *cancelContext
}

type stateModelWrapper = typedStateModelWrapper[*schema.Message]

func (w *typedStateModelWrapper[M]) IsCallbacksEnabled() bool {
	return true
}

func (w *typedStateModelWrapper[M]) GetType() string {
	if typer, ok := any(w.original).(components.Typer); ok {
		return typer.GetType()
	}
	return generic.ParseTypeName(reflect.ValueOf(w.original))
}

func (w *typedStateModelWrapper[M]) hasUserEventSender() bool {
	for _, handler := range w.handlers {
		if _, ok := any(handler).(*typedEventSenderModelWrapper[M]); ok {
			return true
		}
	}
	return false
}

func (w *typedStateModelWrapper[M]) wrapGenerateEndpoint(endpoint typedGenerateEndpoint[M]) typedGenerateEndpoint[M] {
	// === ID Assignment layer (innermost, framework-controlled) ===
	// Ensures model output has a message ID before any WrapModel handler or event sender sees it.
	// Copies the result to avoid mutating a potentially shared pointer returned by the model.
	{
		realInner := endpoint
		endpoint = func(ctx context.Context, input []M, opts ...model.Option) (M, error) {
			result, err := realInner(ctx, input, opts...)
			if err != nil {
				return result, err
			}
			if GetMessageID(result) == "" {
				result = copyMessage(result)
				EnsureMessageID(result)
			}
			return result, nil
		}
	}

	hasUserEventSender := w.hasUserEventSender()
	retryConfig := w.modelRetryConfig
	failoverConfig := w.modelFailoverConfig
	timeoutConfig := w.modelTimeoutConfig
	cc := w.cancelContext

	for i := len(w.handlers) - 1; i >= 0; i-- {
		handler := w.handlers[i]
		innerEndpoint := endpoint
		baseToolInfos := w.toolInfos
		endpoint = func(ctx context.Context, input []M, opts ...model.Option) (M, error) {
			baseOpts := &model.Options{Tools: baseToolInfos}
			commonOpts := model.GetCommonOptions(baseOpts, opts...)
			mc := &TypedModelContext[M]{Tools: commonOpts.Tools, ModelRetryConfig: retryConfig, ModelTimeoutConfig: timeoutConfig, cancelContext: cc}
			wrappedModel, err := handler.WrapModel(ctx, &typedEndpointModel[M]{generate: innerEndpoint}, mc)
			if err != nil {
				var zero M
				return zero, err
			}
			return wrappedModel.Generate(ctx, input, opts...)
		}
	}

	if isModelTimeoutConfigActive(timeoutConfig) {
		innerEndpoint := endpoint
		endpoint = func(ctx context.Context, input []M, opts ...model.Option) (M, error) {
			timeoutWrapper := newTypedTimeoutModelWrapper[M](&typedEndpointModel[M]{generate: innerEndpoint}, timeoutConfig)
			return timeoutWrapper.Generate(ctx, input, opts...)
		}
	}

	if !hasUserEventSender {
		innerEndpoint := endpoint
		eventSender := &typedEventSenderModelWrapper[M]{
			TypedBaseChatModelAgentMiddleware: &TypedBaseChatModelAgentMiddleware[M]{},
		}
		endpoint = func(ctx context.Context, input []M, opts ...model.Option) (M, error) {
			execCtx := getTypedChatModelAgentExecCtx[M](ctx)
			if execCtx == nil || execCtx.generator == nil {
				return innerEndpoint(ctx, input, opts...)
			}
			mc := &TypedModelContext[M]{ModelRetryConfig: retryConfig, ModelFailoverConfig: failoverConfig, ModelTimeoutConfig: timeoutConfig, cancelContext: cc}
			wrappedModel, err := eventSender.WrapModel(ctx, &typedEndpointModel[M]{generate: innerEndpoint}, mc)
			if err != nil {
				var zero M
				return zero, err
			}
			return wrappedModel.Generate(ctx, input, opts...)
		}
	}

	if w.modelRetryConfig != nil {
		innerEndpoint := endpoint
		endpoint = func(ctx context.Context, input []M, opts ...model.Option) (M, error) {
			retryWrapper := newTypedRetryModelWrapper[M](&typedEndpointModel[M]{generate: innerEndpoint}, w.modelRetryConfig)
			return retryWrapper.Generate(ctx, input, opts...)
		}
	}

	if w.modelFailoverConfig != nil {
		config := w.modelFailoverConfig
		innerEndpoint := endpoint
		endpoint = func(ctx context.Context, input []M, opts ...model.Option) (M, error) {
			failoverWrapper := newFailoverModelWrapper[M](&typedEndpointModel[M]{generate: innerEndpoint}, config)
			return failoverWrapper.Generate(ctx, input, opts...)
		}
	}

	return endpoint
}

func (w *typedStateModelWrapper[M]) wrapStreamEndpoint(endpoint typedStreamEndpoint[M]) typedStreamEndpoint[M] {
	// === ID Assignment layer (innermost, framework-controlled) ===
	// Pre-allocates a UUID and injects it into the first chunk only.
	// Only the first chunk carries the ID in Extra to avoid concatStrings corruption
	// during ConcatMessages (which string-concatenates duplicate Extra keys).
	{
		realInner := endpoint
		endpoint = func(ctx context.Context, input []M, opts ...model.Option) (*schema.StreamReader[M], error) {
			reader, err := realInner(ctx, input, opts...)
			if err != nil {
				return nil, err
			}
			msgID := uuid.NewString()
			first := true
			return schema.StreamReaderWithConvert(reader, func(msg M) (M, error) {
				if first {
					first = false
					if GetMessageID(msg) == "" {
						typedSetMessageID(msg, msgID)
					}
				}
				return msg, nil
			}), nil
		}
	}

	hasUserEventSender := w.hasUserEventSender()
	retryConfig := w.modelRetryConfig
	failoverConfig := w.modelFailoverConfig
	timeoutConfig := w.modelTimeoutConfig
	cc := w.cancelContext

	for i := len(w.handlers) - 1; i >= 0; i-- {
		handler := w.handlers[i]
		innerEndpoint := endpoint
		baseToolInfos := w.toolInfos
		endpoint = func(ctx context.Context, input []M, opts ...model.Option) (*schema.StreamReader[M], error) {
			baseOpts := &model.Options{Tools: baseToolInfos}
			commonOpts := model.GetCommonOptions(baseOpts, opts...)
			mc := &TypedModelContext[M]{Tools: commonOpts.Tools, ModelRetryConfig: retryConfig, ModelTimeoutConfig: timeoutConfig, cancelContext: cc}
			wrappedModel, err := handler.WrapModel(ctx, &typedEndpointModel[M]{stream: innerEndpoint}, mc)
			if err != nil {
				return nil, err
			}
			return wrappedModel.Stream(ctx, input, opts...)
		}
	}

	if isModelTimeoutConfigActive(timeoutConfig) {
		innerEndpoint := endpoint
		endpoint = func(ctx context.Context, input []M, opts ...model.Option) (*schema.StreamReader[M], error) {
			timeoutWrapper := newTypedTimeoutModelWrapper[M](&typedEndpointModel[M]{stream: innerEndpoint}, timeoutConfig)
			return timeoutWrapper.Stream(ctx, input, opts...)
		}
	}

	if !hasUserEventSender {
		innerEndpoint := endpoint
		eventSender := &typedEventSenderModelWrapper[M]{
			TypedBaseChatModelAgentMiddleware: &TypedBaseChatModelAgentMiddleware[M]{},
		}
		endpoint = func(ctx context.Context, input []M, opts ...model.Option) (*schema.StreamReader[M], error) {
			execCtx := getTypedChatModelAgentExecCtx[M](ctx)
			if execCtx == nil || execCtx.generator == nil {
				return innerEndpoint(ctx, input, opts...)
			}
			mc := &TypedModelContext[M]{ModelRetryConfig: retryConfig, ModelFailoverConfig: failoverConfig, ModelTimeoutConfig: timeoutConfig, cancelContext: cc}
			wrappedModel, err := eventSender.WrapModel(ctx, &typedEndpointModel[M]{stream: innerEndpoint}, mc)
			if err != nil {
				return nil, err
			}
			return wrappedModel.Stream(ctx, input, opts...)
		}
	}

	if w.modelRetryConfig != nil {
		innerEndpoint := endpoint
		endpoint = func(ctx context.Context, input []M, opts ...model.Option) (*schema.StreamReader[M], error) {
			retryWrapper := newTypedRetryModelWrapper[M](&typedEndpointModel[M]{stream: innerEndpoint}, w.modelRetryConfig)
			return retryWrapper.Stream(ctx, input, opts...)
		}
	}

	if w.modelFailoverConfig != nil {
		config := w.modelFailoverConfig
		innerEndpoint := endpoint
		endpoint = func(ctx context.Context, input []M, opts ...model.Option) (*schema.StreamReader[M], error) {
			failoverWrapper := newFailoverModelWrapper[M](&typedEndpointModel[M]{stream: innerEndpoint}, config)
			return failoverWrapper.Stream(ctx, input, opts...)
		}
	}

	return endpoint
}

func (w *typedStateModelWrapper[M]) Generate(ctx context.Context, _ []M, opts ...model.Option) (M, error) {
	var (
		stateMessages          []M
		stateToolInfos         []*schema.ToolInfo
		stateDeferredToolInfos []*schema.ToolInfo
	)
	_ = compose.ProcessState(ctx, func(_ context.Context, st *typedState[M]) error {
		stateMessages = st.Messages
		stateToolInfos = st.ToolInfos
		stateDeferredToolInfos = st.DeferredToolInfos
		return nil
	})

	// Backfill: old checkpoints or fresh starts have nil ToolInfos.
	// Use compose-level tools from opts (which always reflects the latest bc.toolInfos)
	// rather than w.toolInfos (which may be stale if the graph was reused).
	if stateToolInfos == nil {
		composeLevelOpts := model.GetCommonOptions(&model.Options{}, opts...)
		if composeLevelOpts.Tools != nil {
			stateToolInfos = composeLevelOpts.Tools
		} else {
			stateToolInfos = w.toolInfos
		}
		if stateDeferredToolInfos == nil && composeLevelOpts.DeferredTools != nil {
			stateDeferredToolInfos = composeLevelOpts.DeferredTools
		}
	}

	state := &TypedChatModelAgentState[M]{
		Messages:          stateMessages,
		ToolInfos:         stateToolInfos,
		DeferredToolInfos: stateDeferredToolInfos,
	}

	if msgState, ok := any(state).(*ChatModelAgentState); ok {
		for _, m := range w.middlewares {
			if m.BeforeChatModel != nil {
				if err := m.BeforeChatModel(ctx, msgState); err != nil {
					var zero M
					return zero, err
				}
			}
		}
	}

	baseOpts := &model.Options{Tools: w.toolInfos}
	commonOpts := model.GetCommonOptions(baseOpts, opts...)
	mc := &TypedModelContext[M]{Tools: commonOpts.Tools, ModelRetryConfig: w.modelRetryConfig, ModelTimeoutConfig: w.modelTimeoutConfig, cancelContext: w.cancelContext}
	for _, handler := range w.handlers {
		var err error
		ctx, state, err = handler.BeforeModelRewriteState(ctx, state, mc)
		if err != nil {
			var zero M
			return zero, err
		}
	}

	// Persist state (including tool infos) after BeforeModelRewriteState.
	_ = compose.ProcessState(ctx, func(_ context.Context, st *typedState[M]) error {
		st.Messages = state.Messages
		st.ToolInfos = state.ToolInfos
		st.DeferredToolInfos = state.DeferredToolInfos
		return nil
	})

	// Derive model options from state. Append after caller opts so state takes precedence
	// (model.GetCommonOptions applies left-to-right, last wins).
	// Use explicit copy to avoid mutating the caller's opts slice.
	derivedOpts := make([]model.Option, len(opts), len(opts)+2)
	copy(derivedOpts, opts)
	derivedOpts = append(derivedOpts, model.WithTools(state.ToolInfos))
	if state.DeferredToolInfos != nil {
		derivedOpts = append(derivedOpts, model.WithDeferredTools(state.DeferredToolInfos))
	}

	wrappedEndpoint := w.wrapGenerateEndpoint(w.inner.Generate)
	result, err := wrappedEndpoint(ctx, state.Messages, derivedOpts...)
	if err != nil {
		var zero M
		return zero, err
	}

	// Re-read State.Messages after Generate completes: when ShouldRetry uses
	// PersistModifiedInputMessages, applyDecisionForRetry writes modified messages to State.
	// We must pick up those changes before appending the model result.
	if w.modelRetryConfig != nil && w.modelRetryConfig.ShouldRetry != nil {
		_ = compose.ProcessState(ctx, func(_ context.Context, st *typedState[M]) error {
			state.Messages = st.Messages
			return nil
		})
	}

	EnsureMessageID(result)
	state.Messages = append(state.Messages, result)

	for _, handler := range w.handlers {
		ctx, state, err = handler.AfterModelRewriteState(ctx, state, mc)
		if err != nil {
			var zero M
			return zero, err
		}
	}

	if msgState, ok := any(state).(*ChatModelAgentState); ok {
		for _, m := range w.middlewares {
			if m.AfterChatModel != nil {
				if err := m.AfterChatModel(ctx, msgState); err != nil {
					var zero M
					return zero, err
				}
			}
		}
	}

	// Persist state (including tool infos) after AfterModelRewriteState.
	_ = compose.ProcessState(ctx, func(_ context.Context, st *typedState[M]) error {
		st.Messages = state.Messages
		st.ToolInfos = state.ToolInfos
		st.DeferredToolInfos = state.DeferredToolInfos
		return nil
	})

	if len(state.Messages) == 0 {
		var zero M
		return zero, errors.New("no messages left in state after model call")
	}
	return state.Messages[len(state.Messages)-1], nil
}

func (w *typedStateModelWrapper[M]) Stream(ctx context.Context, _ []M, opts ...model.Option) (*schema.StreamReader[M], error) {
	var (
		stateMessages          []M
		stateToolInfos         []*schema.ToolInfo
		stateDeferredToolInfos []*schema.ToolInfo
	)
	_ = compose.ProcessState(ctx, func(_ context.Context, st *typedState[M]) error {
		stateMessages = st.Messages
		stateToolInfos = st.ToolInfos
		stateDeferredToolInfos = st.DeferredToolInfos
		return nil
	})

	// Backfill: old checkpoints or fresh starts have nil ToolInfos.
	// Use compose-level tools from opts (which always reflects the latest bc.toolInfos)
	// rather than w.toolInfos (which may be stale if the graph was reused).
	if stateToolInfos == nil {
		composeLevelOpts := model.GetCommonOptions(&model.Options{}, opts...)
		if composeLevelOpts.Tools != nil {
			stateToolInfos = composeLevelOpts.Tools
		} else {
			stateToolInfos = w.toolInfos
		}
		if stateDeferredToolInfos == nil && composeLevelOpts.DeferredTools != nil {
			stateDeferredToolInfos = composeLevelOpts.DeferredTools
		}
	}

	state := &TypedChatModelAgentState[M]{
		Messages:          stateMessages,
		ToolInfos:         stateToolInfos,
		DeferredToolInfos: stateDeferredToolInfos,
	}

	if msgState, ok := any(state).(*ChatModelAgentState); ok {
		for _, m := range w.middlewares {
			if m.BeforeChatModel != nil {
				if err := m.BeforeChatModel(ctx, msgState); err != nil {
					return nil, err
				}
			}
		}
	}

	baseOpts := &model.Options{Tools: w.toolInfos}
	commonOpts := model.GetCommonOptions(baseOpts, opts...)
	mc := &TypedModelContext[M]{Tools: commonOpts.Tools, ModelRetryConfig: w.modelRetryConfig, ModelTimeoutConfig: w.modelTimeoutConfig, cancelContext: w.cancelContext}
	for _, handler := range w.handlers {
		var err error
		ctx, state, err = handler.BeforeModelRewriteState(ctx, state, mc)
		if err != nil {
			return nil, err
		}
	}

	// Persist state (including tool infos) after BeforeModelRewriteState.
	_ = compose.ProcessState(ctx, func(_ context.Context, st *typedState[M]) error {
		st.Messages = state.Messages
		st.ToolInfos = state.ToolInfos
		st.DeferredToolInfos = state.DeferredToolInfos
		return nil
	})

	// Derive model options from state. Append after caller opts so state takes precedence
	// (model.GetCommonOptions applies left-to-right, last wins).
	// Use explicit copy to avoid mutating the caller's opts slice.
	derivedOpts := make([]model.Option, len(opts), len(opts)+2)
	copy(derivedOpts, opts)
	derivedOpts = append(derivedOpts, model.WithTools(state.ToolInfos))
	if state.DeferredToolInfos != nil {
		derivedOpts = append(derivedOpts, model.WithDeferredTools(state.DeferredToolInfos))
	}

	wrappedEndpoint := w.wrapStreamEndpoint(w.inner.Stream)
	stream, err := wrappedEndpoint(ctx, state.Messages, derivedOpts...)
	if err != nil {
		return nil, err
	}
	result, err := concatMessageStream(stream)
	if err != nil {
		return nil, err
	}

	// Re-read State.Messages after Stream completes: same rationale as in Generate above.
	if w.modelRetryConfig != nil && w.modelRetryConfig.ShouldRetry != nil {
		_ = compose.ProcessState(ctx, func(_ context.Context, st *typedState[M]) error {
			state.Messages = st.Messages
			return nil
		})
	}

	EnsureMessageID(result)
	state.Messages = append(state.Messages, result)

	for _, handler := range w.handlers {
		ctx, state, err = handler.AfterModelRewriteState(ctx, state, mc)
		if err != nil {
			return nil, err
		}
	}

	if msgState, ok := any(state).(*ChatModelAgentState); ok {
		for _, m := range w.middlewares {
			if m.AfterChatModel != nil {
				if err := m.AfterChatModel(ctx, msgState); err != nil {
					return nil, err
				}
			}
		}
	}

	// Persist state (including tool infos) after AfterModelRewriteState.
	_ = compose.ProcessState(ctx, func(_ context.Context, st *typedState[M]) error {
		st.Messages = state.Messages
		st.ToolInfos = state.ToolInfos
		st.DeferredToolInfos = state.DeferredToolInfos
		return nil
	})

	if len(state.Messages) == 0 {
		return nil, errors.New("no messages left in state after model call")
	}
	return schema.StreamReaderFromArray([]M{state.Messages[len(state.Messages)-1]}), nil
}

type typedEndpointModel[M MessageType] struct {
	generate typedGenerateEndpoint[M]
	stream   typedStreamEndpoint[M]
}

func (m *typedEndpointModel[M]) Generate(ctx context.Context, input []M, opts ...model.Option) (M, error) {
	if m.generate != nil {
		return m.generate(ctx, input, opts...)
	}
	var zero M
	return zero, errors.New("generate endpoint not set")
}

func (m *typedEndpointModel[M]) Stream(ctx context.Context, input []M, opts ...model.Option) (*schema.StreamReader[M], error) {
	if m.stream != nil {
		return m.stream(ctx, input, opts...)
	}
	return nil, errors.New("stream endpoint not set")
}
