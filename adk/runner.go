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
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"reflect"
	"runtime/debug"
	"sync"
	"time"

	"github.com/cloudwego/eino/internal/core"
	"github.com/cloudwego/eino/internal/safe"
	"github.com/cloudwego/eino/schema"
)

func errorIterator[M MessageType](err error) *AsyncIterator[*TypedAgentEvent[M]] {
	iter, gen := NewAsyncIteratorPair[*TypedAgentEvent[M]]()
	gen.Send(&TypedAgentEvent[M]{Err: err})
	gen.Close()
	return iter
}

func newUserMessage[M MessageType](query string) (M, error) {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		return any(schema.UserMessage(query)).(M), nil
	case *schema.AgenticMessage:
		return any(schema.UserAgenticMessage(query)).(M), nil
	default:
		return zero, fmt.Errorf("unsupported message type %T", zero)
	}
}

// TypedRunner is the primary entry point for executing an Agent.
// It manages the agent's lifecycle, including starting, resuming, and checkpointing.
//
// Execution always goes through the flowAgent pipeline, which handles
// multi-agent orchestration, callbacks, agent naming, run paths, and cancellation.
type TypedRunner[M MessageType] struct {
	a               TypedAgent[M]
	enableStreaming bool
	store           CheckPointStore
	sessionID       string
	sessionStore    SessionEventStore[M]
	sessionConfig   *SessionConfig[M]
}

// Runner is the default runner type using *schema.Message.
type Runner = TypedRunner[*schema.Message]

type CheckPointStore = core.CheckPointStore

type CheckPointDeleter = core.CheckPointDeleter

type TypedRunnerConfig[M MessageType] struct {
	Agent           TypedAgent[M]
	EnableStreaming bool

	CheckPointStore CheckPointStore

	SessionID     string
	SessionStore  SessionEventStore[M]
	SessionConfig *SessionConfig[M]
}

// RunnerConfig is the default runner config type using *schema.Message.
type RunnerConfig = TypedRunnerConfig[*schema.Message]

// ResumeParams contains all parameters needed to resume an execution.
// This struct provides an extensible way to pass resume parameters without
// requiring breaking changes to method signatures.
type ResumeParams struct {
	// Targets contains the addresses of components to be resumed as keys,
	// with their corresponding resume data as values
	Targets map[string]any
	// Future extensible fields can be added here without breaking changes
}

// NewRunner creates a new Runner with the given config.
func NewRunner(_ context.Context, conf RunnerConfig) *Runner {
	return NewTypedRunner(conf)
}

// NewTypedRunner creates a new TypedRunner with the given config.
func NewTypedRunner[M MessageType](conf TypedRunnerConfig[M]) *TypedRunner[M] {
	return &TypedRunner[M]{
		enableStreaming: conf.EnableStreaming,
		a:               conf.Agent,
		store:           conf.CheckPointStore,
		sessionID:       conf.SessionID,
		sessionStore:    conf.SessionStore,
		sessionConfig:   conf.SessionConfig,
	}
}

func (r *TypedRunner[M]) Run(ctx context.Context, messages []M,
	opts ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[M]] {
	return typedRunnerRunImpl(r.a, r.enableStreaming, r.store, r.sessionID, r.sessionStore, r.sessionConfig, ctx, messages, opts...)
}

// Query is a convenience method that starts a new execution with a single user query string.
func (r *TypedRunner[M]) Query(ctx context.Context,
	query string, opts ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[M]] {
	msgs, err := newUserMessage[M](query)
	if err != nil {
		return errorIterator[M](err)
	}
	return r.Run(ctx, []M{msgs}, opts...)
}

// Resume continues an interrupted execution from a checkpoint, using an "Implicit Resume All" strategy.
// This method is best for simpler use cases where the act of resuming implies that all previously
// interrupted points should proceed without specific data.
//
// When using this method, all interrupted agents will receive `isResumeFlow = false` when they
// call `GetResumeContext`, as no specific agent was targeted. This is suitable for the "Simple Confirmation"
// pattern where an agent only needs to know `wasInterrupted` is true to continue.
func (r *TypedRunner[M]) Resume(ctx context.Context, checkPointID string, opts ...AgentRunOption) (
	*AsyncIterator[*TypedAgentEvent[M]], error) {
	return r.resumeInternal(ctx, checkPointID, nil, opts...)
}

// ResumeWithParams continues an interrupted execution from a checkpoint with specific parameters.
// This is the most common and powerful way to resume, allowing you to target specific interrupt points
// (identified by their address/ID) and provide them with data.
//
// The params.Targets map should contain the addresses of the components to be resumed as keys. These addresses
// can point to any interruptible component in the entire execution graph, including ADK agents, compose
// graph nodes, or tools. The value can be the resume data for that component, or `nil` if no data is needed.
//
// When using this method:
//   - Components whose addresses are in the params.Targets map will receive `isResumeFlow = true` when they
//     call `GetResumeContext`.
//   - Interrupted components whose addresses are NOT in the params.Targets map must decide how to proceed:
//     -- "Leaf" components (the actual root causes of the original interrupt) MUST re-interrupt themselves
//     to preserve their state.
//     -- "Composite" agents (like SequentialAgent or ChatModelAgent) should generally proceed with their
//     execution. They act as conduits, allowing the resume signal to flow to their children. They will
//     naturally re-interrupt if one of their interrupted children re-interrupts, as they receive the
//     new `CompositeInterrupt` signal from them.
func (r *TypedRunner[M]) ResumeWithParams(ctx context.Context, checkPointID string, params *ResumeParams, opts ...AgentRunOption) (*AsyncIterator[*TypedAgentEvent[M]], error) {
	return r.resumeInternal(ctx, checkPointID, params.Targets, opts...)
}

func (r *TypedRunner[M]) resumeInternal(ctx context.Context, checkPointID string, resumeData map[string]any,
	opts ...AgentRunOption) (*AsyncIterator[*TypedAgentEvent[M]], error) {
	return typedRunnerResumeInternalImpl(r.a, r.store, r.sessionID, r.sessionStore, r.sessionConfig, ctx, checkPointID, resumeData, opts...)
}

type runnerSessionRunState[M MessageType] struct {
	enabled         bool
	sessionID       string
	checkPointID    *string
	latestState     *reconstructedSessionState[M]
	sessionConfig   SessionConfig[M]
	sessionStore    SessionEventStore[M]
	sessionHandle   sessionHandle[M]
	checkPointStore CheckPointStore
	turnID          string
	initialTimeline []*SessionEvent[M]
	// inputMessages are the caller-provided messages for this turn (before history prepend).
	// Captured so the Runner can persist them as session events at turn start.
	inputMessages []M
}

func valueOrEmpty(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func isNilCheckPointStore(store CheckPointStore) bool {
	if store == nil {
		return true
	}
	v := reflect.ValueOf(store)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

func openRunnerSession[M MessageType](
	ctx context.Context,
	store SessionEventStore[M],
	sessionID string,
	cfg SessionConfig[M],
) (*openSessionResult[M], error) {
	if store == nil {
		return nil, errors.New("adk: session store is nil")
	}
	deadline := timeNow().Add(cfg.SessionAcquireTimeout)
	var lastErr error
	for {
		result, err := openLocalSession(ctx, store, &openSessionRequest{
			sessionID: sessionID,
		})
		if err == nil {
			if result == nil || result.handle == nil {
				return nil, ErrSessionBusy
			}
			return result, nil
		}
		if !errors.Is(err, ErrSessionBusy) {
			return nil, err
		}
		lastErr = err
		if !timeNow().Before(deadline) {
			return nil, lastErr
		}
		wait := 10 * time.Millisecond
		var busy *SessionBusyError
		if errors.As(err, &busy) && busy.ExpiresAt.After(timeNow()) {
			until := time.Until(busy.ExpiresAt)
			if until < wait {
				wait = until
			}
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func timeNow() time.Time {
	return time.Now()
}

func prepareRunnerSessionRun[M MessageType]( //nolint:revive // argument-limit
	ctx context.Context,
	checkPointStore CheckPointStore,
	requestedCheckPointID *string,
	sessionID string,
	sessionStore SessionEventStore[M],
	sessionConfig *SessionConfig[M],
) (*runnerSessionRunState[M], error) {
	state := &runnerSessionRunState[M]{}
	if isNilCheckPointStore(checkPointStore) {
		checkPointStore = nil
	}
	if sessionID == "" || sessionStore == nil {
		return state, nil
	}
	state.enabled = true
	state.sessionID = sessionID
	state.sessionStore = sessionStore
	state.checkPointStore = checkPointStore
	state.sessionConfig = normalizeSessionConfig(sessionConfig)
	state.latestState = &reconstructedSessionState[M]{}
	openResult, err := openRunnerSession[M](ctx, sessionStore, sessionID, state.sessionConfig)
	if err != nil {
		return nil, err
	}
	state.sessionHandle = openResult.handle

	reconstructResult, err := reconstructSessionState[M](ctx, state.sessionHandle, sessionID, defaultLoadPageSize)
	if err != nil {
		_ = state.sessionHandle.close(ctx)
		return nil, fmt.Errorf("failed to reconstruct session[%s]: %w", sessionID, err)
	}
	if reconstructResult != nil && reconstructResult.state != nil {
		state.latestState = reconstructResult.state
	}
	runningEvent := &SessionEvent[M]{
		Timestamp: newEventTimestamp(),
		Kind:      SessionEventSessionStatusRunning,
		Lifecycle: &LifecycleEvent{State: SessionRunStateRunning},
	}
	err = assignSessionEventID(ctx, runningEvent, state.sessionConfig.EventIDGenerator)
	if err != nil {
		_ = state.sessionHandle.close(ctx)
		return nil, err
	}
	state.turnID = runningEvent.EventID
	runningEvent.TurnID = state.turnID
	err = appendRunnerSessionControlEvent(ctx, state, runningEvent)
	if err != nil {
		_ = state.sessionHandle.close(ctx)
		return nil, err
	}
	state.initialTimeline = append(state.initialTimeline, runningEvent)

	if isNilCheckPointStore(checkPointStore) {
		return state, nil
	}
	checkPointID := sessionRunnerCheckpointID(sessionID)
	if requestedCheckPointID != nil && *requestedCheckPointID != "" {
		checkPointID = *requestedCheckPointID
	}
	state.checkPointID = &checkPointID
	_, existed, err := loadRunnerSessionCheckpoint(ctx, checkPointStore, checkPointID)
	if err != nil {
		_ = state.sessionHandle.close(ctx)
		return nil, err
	}
	if !existed {
		return state, nil
	}
	// Pending checkpoint exists but caller chose Run (fresh turn) instead of Resume.
	// We intentionally do NOT delete the checkpoint here — it remains available for a
	// future Resume call. Session correctness is guaranteed by event log replay regardless
	// of checkpoint presence. If this fresh turn completes successfully, finalize() will
	// clean up the stale checkpoint at that point.
	return state, nil
}

func prepareRunnerSessionResume[M MessageType]( //nolint:revive // argument-limit
	ctx context.Context,
	checkPointStore CheckPointStore,
	sessionID string,
	sessionStore SessionEventStore[M],
	sessionConfig *SessionConfig[M],
	checkPointID string,
) (*runnerSessionRunState[M], string, error) {
	state := &runnerSessionRunState[M]{}
	if isNilCheckPointStore(checkPointStore) {
		checkPointStore = nil
	}
	// Non-session-mode resume: explicit checkpoint ID, no session boot needed.
	if checkPointID != "" && (sessionID == "" || sessionStore == nil) {
		return state, checkPointID, nil
	}
	// Implicit session-mode resume requires both sessionID and sessionStore.
	if checkPointID == "" && (sessionID == "" || sessionStore == nil) {
		return nil, "", errors.New("failed to resume: checkpoint ID is empty")
	}
	state.enabled = true
	state.sessionID = sessionID
	state.sessionStore = sessionStore
	state.checkPointStore = checkPointStore
	state.sessionConfig = normalizeSessionConfig(sessionConfig)
	state.latestState = &reconstructedSessionState[M]{}
	openResult, err := openRunnerSession[M](ctx, sessionStore, sessionID, state.sessionConfig)
	if err != nil {
		return nil, "", err
	}
	state.sessionHandle = openResult.handle

	reconstructResult, err := reconstructSessionState[M](ctx, state.sessionHandle, sessionID, defaultLoadPageSize)
	if err != nil {
		_ = state.sessionHandle.close(ctx)
		return nil, "", fmt.Errorf("failed to reconstruct session[%s]: %w", sessionID, err)
	}
	if reconstructResult != nil && reconstructResult.state != nil {
		state.latestState = reconstructResult.state
	}
	// Pick the checkpoint ID: caller-provided takes precedence over the implicit
	// session-scoped one. The session-scoped key still drives existence checks
	// when the caller did not supply a checkpoint.
	effectiveCheckPointID := checkPointID
	if effectiveCheckPointID == "" {
		effectiveCheckPointID = sessionRunnerCheckpointID(sessionID)
	}
	state.checkPointID = &effectiveCheckPointID

	// Existence check is only required for implicit session resume — the caller
	// passing an explicit checkpoint ID has asserted the checkpoint should exist
	// and any error will surface from the subsequent load. For implicit resume,
	// the absence of a pending checkpoint is fatal and reported here.
	cp, existed, err := loadRunnerSessionCheckpoint(ctx, checkPointStore, effectiveCheckPointID)
	if err != nil {
		_ = state.sessionHandle.close(ctx)
		return nil, "", err
	}
	if !existed {
		_ = state.sessionHandle.close(ctx)
		if checkPointID == "" {
			return nil, "", fmt.Errorf("no pending session checkpoint for session %q", sessionID)
		}
		return nil, "", fmt.Errorf("checkpoint[%s] not exist", effectiveCheckPointID)
	}
	resumeEvent := &SessionEvent[M]{
		Timestamp: newEventTimestamp(),
		Kind:      SessionEventKind(SessionEventExtensionPrefix + "resume.request_started"),
		Extension: &SessionExtensionEvent{},
	}
	if cp != nil && cp.TurnID != "" {
		state.turnID = cp.TurnID
		resumeEvent.TurnID = state.turnID
	}
	if err := assignSessionEventID(ctx, resumeEvent, state.sessionConfig.EventIDGenerator); err != nil {
		_ = state.sessionHandle.close(ctx)
		return nil, "", err
	}
	if state.turnID == "" {
		state.turnID = resumeEvent.EventID
		resumeEvent.TurnID = state.turnID
	}
	if err := appendRunnerSessionControlEvent(ctx, state, resumeEvent); err != nil {
		_ = state.sessionHandle.close(ctx)
		return nil, "", err
	}
	state.initialTimeline = append(state.initialTimeline, resumeEvent)
	return state, effectiveCheckPointID, nil
}

func appendRunnerSessionControlEvent[M MessageType](
	ctx context.Context,
	state *runnerSessionRunState[M],
	event *SessionEvent[M],
) error {
	if state == nil || !state.enabled || state.sessionHandle == nil || event == nil {
		return nil
	}
	if err := ValidateEmittedSessionEventKind(event); err != nil {
		return err
	}
	err := state.sessionHandle.appendEvents(ctx, []*SessionEvent[M]{event})
	return err
}

func appendRunnerSessionInputEvents[M MessageType](
	ctx context.Context,
	state *runnerSessionRunState[M],
	messages []M,
) error {
	if state == nil || !state.enabled || state.sessionHandle == nil || len(messages) == 0 {
		return nil
	}
	for _, msg := range messages {
		se := makeInputSessionEvent[M](msg)
		stampSessionEventTurnID(se, state.turnID)
		if err := assignSessionEventID(ctx, se, state.sessionConfig.EventIDGenerator); err != nil {
			return err
		}
		if err := ValidateEmittedSessionEventKind(se); err != nil {
			return err
		}
		if err := state.sessionHandle.appendEvents(ctx, []*SessionEvent[M]{se}); err != nil {
			return err
		}
		state.initialTimeline = append(state.initialTimeline, se)
	}
	return nil
}

func loadRunnerSessionCheckpoint(ctx context.Context, store CheckPointStore, checkPointID string) (*runnerSessionCheckpoint, bool, error) {
	data, existed, err := store.Get(ctx, checkPointID)
	if err != nil {
		return nil, false, fmt.Errorf("failed to load session checkpoint[%s]: %w", checkPointID, err)
	}
	if !existed {
		return nil, false, nil
	}
	cp, err := decodeRunnerSessionCheckpoint(data)
	if err != nil {
		return nil, false, fmt.Errorf("failed to decode session checkpoint[%s]: %w", checkPointID, err)
	}
	return cp, true, nil
}

func runnerLoadCheckPointForSession(store CheckPointStore, ctx context.Context, checkPointID string, sessionMode bool) (
	context.Context, *runContext, *ResumeInfo, error) {
	if !sessionMode {
		return runnerLoadCheckPointImpl(store, ctx, checkPointID)
	}
	cp, existed, err := loadRunnerSessionCheckpoint(ctx, store, checkPointID)
	if err != nil {
		return nil, nil, nil, err
	}
	if !existed {
		return nil, nil, nil, fmt.Errorf("checkpoint[%s] not exist", checkPointID)
	}
	return runnerLoadCheckPointBytes(ctx, cp.Payload)
}

func runnerLoadCheckPointBytes(ctx context.Context, data []byte) (
	context.Context, *runContext, *ResumeInfo, error) {
	data = preprocessADKCheckpoint(data)
	s := &serialization{}
	err := gob.NewDecoder(bytes.NewReader(data)).Decode(s)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to decode checkpoint: %w", err)
	}
	ctx = core.PopulateInterruptState(ctx, s.InterruptID2Address, s.InterruptID2State)
	return ctx, s.RunCtx, &ResumeInfo{
		EnableStreaming: s.EnableStreaming,
		InterruptInfo:   s.Info,
	}, nil
}

func deleteCheckPointIfSupported(ctx context.Context, store CheckPointStore, checkPointID string) error {
	if isNilCheckPointStore(store) {
		return nil
	}
	if deleter, ok := store.(CheckPointDeleter); ok {
		return deleter.Delete(ctx, checkPointID)
	}
	return nil
}

func saveRunnerCheckpoint[M MessageType]( //nolint:revive // argument-limit
	enableStreaming bool,
	store CheckPointStore,
	ctx context.Context,
	checkPointID string,
	info *InterruptInfo,
	is *core.InterruptSignal,
	sessionState *runnerSessionRunState[M],
) error {
	if sessionState == nil || !sessionState.enabled {
		return runnerSaveCheckPointImpl(enableStreaming, store, ctx, checkPointID, info, is)
	}
	if isNilCheckPointStore(store) {
		return nil
	}
	payload, err := encodeRunnerCheckPointWithRunCtx(
		enableStreaming,
		sanitizeRunContextForSessionCheckpoint[M](getRunCtx(ctx)),
		info,
		is,
	)
	if err != nil {
		return err
	}
	data, err := encodeRunnerSessionCheckpoint(&runnerSessionCheckpoint{
		SessionID:    sessionState.sessionID,
		CheckPointID: checkPointID,
		TurnID:       sessionState.turnID,
		Payload:      payload,
	})
	if err != nil {
		return err
	}
	return store.Set(ctx, checkPointID, data)
}

func typedRunnerRunImpl[M MessageType](a TypedAgent[M], enableStreaming bool, store CheckPointStore, sessionID string, sessionStore SessionEventStore[M], sessionConfig *SessionConfig[M], ctx context.Context, messages []M, opts ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[M]] { //nolint:revive // argument-limit
	o := getCommonOptions(nil, opts...)
	exposeTimelineEvents := o.enableTimelineEvents

	sessionState, err := prepareRunnerSessionRun[M](ctx, store, o.checkPointID, sessionID, sessionStore, sessionConfig)
	if err != nil {
		return errorIterator[M](err)
	}
	if sessionState.enabled {
		// Capture caller-provided messages BEFORE prepending history. These will be
		// emitted as session events at turn start so they appear in the event log.
		sessionState.inputMessages = append([]M{}, messages...)
		messages = append(append([]M{}, sessionState.latestState.Messages...), sessionState.inputMessages...)
		// Assign IDs before messages can be both persisted and inspected by
		// middleware, avoiding concurrent lazy ID mutation during event snapshotting.
		for _, msg := range messages {
			EnsureMessageID(msg)
		}
		if err := appendRunnerSessionInputEvents(ctx, sessionState, sessionState.inputMessages); err != nil {
			_ = sessionState.sessionHandle.close(ctx)
			return errorIterator[M](err)
		}
		sessionState.inputMessages = nil
		opts = append(opts, withEnableSessionEvents())
		opts = append(opts, withEnableInternalTimelineEvents())
		opts = append(opts, withInitialModelContext(&ModelContextEvent{
			ToolInfos:         sessionState.latestState.ToolInfos,
			DeferredToolInfos: sessionState.latestState.DeferredToolInfos,
		}, sessionState.latestState.sawModelContext))
	}

	input := &TypedAgentInput[M]{
		Messages:        messages,
		EnableStreaming: enableStreaming,
	}

	if sessionState.enabled {
		ctx = contextWithSessionEventIDGenerator[M](ctx, sessionState.sessionConfig.EventIDGenerator)
		ctx = contextWithSessionEventTurnID[M](ctx, sessionState.turnID)
	}

	var zero M
	if _, ok := any(zero).(*schema.Message); ok {
		concreteAgent, _ := any(a).(Agent)
		fa := toFlowAgent(ctx, concreteAgent)
		if store != nil {
			fa.checkPointStore = store
		}
		concreteInput := any(input).(*AgentInput)
		ctx = ctxWithNewTypedRunCtx(ctx, input, o.sharedParentSession)
		AddSessionValues(ctx, o.sessionValues)

		iter := fa.Run(ctx, concreteInput, opts...)

		// Short-circuit: no checkpoint to save, no cancel to handle, and no need to
		// strip session-internal fields (enableSessionEvents means the caller wants
		// them). The intermediate iterator pair adds no value in this case.
		if store == nil && o.cancelCtx == nil && exposeTimelineEvents && !sessionState.enabled {
			return any(iter).(*AsyncIterator[*TypedAgentEvent[M]])
		}

		niter, gen := NewAsyncIteratorPair[*TypedAgentEvent[M]]()
		checkPointID := o.checkPointID
		if sessionState.checkPointID != nil {
			checkPointID = sessionState.checkPointID
		}
		go typedRunnerHandleIterImpl(enableStreaming, store, ctx, any(iter).(*AsyncIterator[*TypedAgentEvent[M]]), gen, checkPointID, o.cancelCtx, exposeTimelineEvents, sessionState)
		return niter
	}

	fa := toTypedFlowAgent(a)
	if store != nil {
		fa.checkPointStore = store
	}

	ctx = ctxWithNewTypedRunCtx(ctx, input, o.sharedParentSession)
	AddSessionValues(ctx, o.sessionValues)

	iter := fa.Run(ctx, input, opts...)

	// Short-circuit: no checkpoint to save, no cancel to handle, and no need to
	// strip session-internal fields (enableSessionEvents means the caller wants
	// them). The intermediate iterator pair adds no value in this case.
	if store == nil && o.cancelCtx == nil && exposeTimelineEvents && !sessionState.enabled {
		return iter
	}

	niter, gen := NewAsyncIteratorPair[*TypedAgentEvent[M]]()
	checkPointID := o.checkPointID
	if sessionState.checkPointID != nil {
		checkPointID = sessionState.checkPointID
	}
	go typedRunnerHandleIterImpl(enableStreaming, store, ctx, iter, gen, checkPointID, o.cancelCtx, exposeTimelineEvents, sessionState)
	return niter
}

func typedRunnerResumeInternalImpl[M MessageType](a TypedAgent[M], store CheckPointStore, sessionID string, sessionStore SessionEventStore[M], sessionConfig *SessionConfig[M], ctx context.Context, checkPointID string, resumeData map[string]any, //nolint:revive // argument-limit
	opts ...AgentRunOption) (*AsyncIterator[*TypedAgentEvent[M]], error) {
	if isNilCheckPointStore(store) {
		return nil, fmt.Errorf("failed to resume: store is nil")
	}

	o := getCommonOptions(nil, opts...)
	exposeTimelineEvents := o.enableTimelineEvents
	sessionState, effectiveCheckPointID, err := prepareRunnerSessionResume[M](ctx, store, sessionID, sessionStore, sessionConfig, checkPointID)
	if err != nil {
		return nil, err
	}
	checkPointID = effectiveCheckPointID
	if sessionState.enabled {
		opts = append(opts, withEnableSessionEvents())
		opts = append(opts, withEnableInternalTimelineEvents())
		opts = append(opts, withInitialModelContext(&ModelContextEvent{
			ToolInfos:         sessionState.latestState.ToolInfos,
			DeferredToolInfos: sessionState.latestState.DeferredToolInfos,
		}, sessionState.latestState.sawModelContext))
	}

	ctx, runCtx, resumeInfo, err := runnerLoadCheckPointForSession(store, ctx, checkPointID, sessionState.enabled)
	if err != nil {
		if sessionState != nil && sessionState.enabled && sessionState.sessionHandle != nil {
			_ = sessionState.sessionHandle.close(ctx)
		}
		return nil, fmt.Errorf("failed to load from checkpoint: %w", err)
	}

	// Resume uses the streaming mode persisted in the checkpoint, not the value the
	// caller passed when constructing the runner. This is the runner's own invariant:
	// the checkpoint is the source of truth for what mode the original execution was
	// running in, and any new checkpoint written during this resume must preserve it.
	enableStreaming := resumeInfo.EnableStreaming

	if o.sharedParentSession {
		parentSession := getSession(ctx)
		if parentSession != nil {
			runCtx.Session.Values = parentSession.Values
			runCtx.Session.valuesMtx = parentSession.valuesMtx
		}
	}
	if runCtx.Session.valuesMtx == nil {
		runCtx.Session.valuesMtx = &sync.Mutex{}
	}
	if runCtx.Session.Values == nil {
		runCtx.Session.Values = make(map[string]any)
	}

	ctx = setRunCtx(ctx, runCtx)
	AddSessionValues(ctx, o.sessionValues)

	if sessionState.enabled {
		ctx = contextWithSessionEventIDGenerator[M](ctx, sessionState.sessionConfig.EventIDGenerator)
		ctx = contextWithSessionEventTurnID[M](ctx, sessionState.turnID)
	}

	if len(resumeData) > 0 {
		ctx = core.BatchResumeWithData(ctx, resumeData)
	}

	var zero M
	if _, ok := any(zero).(*schema.Message); ok {
		concreteAgent, _ := any(a).(Agent)
		fa := toFlowAgent(ctx, concreteAgent)
		ra, ok := any(fa).(ResumableAgent)
		if !ok {
			if sessionState.enabled && sessionState.sessionHandle != nil {
				_ = sessionState.sessionHandle.close(ctx)
			}
			return nil, fmt.Errorf("agent %T does not support resume", a)
		}
		aIter := ra.Resume(ctx, resumeInfo, opts...)

		niter, gen := NewAsyncIteratorPair[*TypedAgentEvent[M]]()
		go typedRunnerHandleIterImpl(enableStreaming, store, ctx, any(aIter).(*AsyncIterator[*TypedAgentEvent[M]]), gen, &checkPointID, o.cancelCtx, exposeTimelineEvents, sessionState)
		return niter, nil
	}

	fa := toTypedFlowAgent(a)
	ra, ok := any(fa).(TypedResumableAgent[M])
	if !ok {
		if sessionState.enabled && sessionState.sessionHandle != nil {
			_ = sessionState.sessionHandle.close(ctx)
		}
		return nil, fmt.Errorf("agent %T does not support resume", a)
	}
	aIter := ra.Resume(ctx, resumeInfo, opts...)

	niter, gen := NewAsyncIteratorPair[*TypedAgentEvent[M]]()
	go typedRunnerHandleIterImpl(enableStreaming, store, ctx, aIter, gen, &checkPointID, o.cancelCtx, exposeTimelineEvents, sessionState)
	return niter, nil
}

func typedRunnerHandleIterImpl[M MessageType](enableStreaming bool, store CheckPointStore, ctx context.Context, aIter *AsyncIterator[*TypedAgentEvent[M]], //nolint:revive,cyclop,funlen // argument-limit; event loop branches by event kind
	gen *AsyncGenerator[*TypedAgentEvent[M]], checkPointID *string, cancelCtx *cancelContext, enableTimelineEvents bool, sessionState *runnerSessionRunState[M]) {
	defer func() {
		panicErr := recover()
		if panicErr != nil {
			e := safe.NewPanicErr(panicErr, debug.Stack())
			gen.Send(&TypedAgentEvent[M]{Err: e})
		}

		gen.Close()
	}()
	var (
		interruptSignal   *core.InterruptSignal
		interruptContexts []*InterruptCtx
		legacyData        any
		interrupted       bool
		cancelled         bool
		retryExhausted    bool
		terminalErr       error
		persister         *sessionEventPersister[M]
		persistErr        error

		// pendingCheckpoint defers checkpoint save to finalize() so the persister
		// can flush enqueued events first. Writing the checkpoint before the flush
		// completes risks a checkpoint that references events not yet durable.
		pendingCheckpoint *deferredRunnerCheckpoint
	)
	if sessionState != nil && sessionState.enabled {
		persister = newSessionEventPersister[M](ctx, sessionState.sessionHandle, sessionState.sessionID)
	}
	if enableTimelineEvents && sessionState != nil && sessionState.enabled {
		for _, se := range sessionState.initialTimeline {
			if se != nil {
				gen.Send(&TypedAgentEvent[M]{SessionEventVariant: &SessionEventVariant[M]{SessionID: sessionState.sessionID, Event: se}})
			}
		}
	}
	stampParentSessionEvent := func(se *SessionEvent[M]) {
		if sessionState == nil || !sessionState.enabled {
			return
		}
		stampSessionEventTurnID(se, sessionState.turnID)
	}
	stampParentStreamRef := func(ref *MessageStreamRef) {
		if ref == nil || sessionState == nil || !sessionState.enabled || sessionState.turnID == "" {
			return
		}
		ref.TurnID = sessionState.turnID
	}
	setPersistErr := func(err error) {
		if err != nil && persistErr == nil {
			persistErr = err
		}
	}
	enqueueAsyncSessionEvent := func(se *SessionEvent[M]) error {
		if persister == nil || se == nil {
			return nil
		}
		if err := persister.enqueueAsync(se); err != nil {
			setPersistErr(err)
			return err
		}
		return nil
	}
	commitSessionBoundary := func(se *SessionEvent[M]) error {
		if persister == nil || se == nil {
			return nil
		}
		if err := persister.commitBoundary(se); err != nil {
			setPersistErr(err)
			return err
		}
		return nil
	}
	persistSessionEvent := func(se *SessionEvent[M]) error {
		if persister == nil || se == nil {
			return nil
		}
		stampParentSessionEvent(se)
		if err := ValidateEmittedSessionEventKind(se); err != nil {
			setPersistErr(err)
			return err
		}
		if isSessionDurableBoundaryKind(se.Kind) {
			return commitSessionBoundary(se)
		}
		return enqueueAsyncSessionEvent(se)
	}
	sendTimelineEvent := func(se *SessionEvent[M]) bool {
		if se == nil {
			return false
		}
		stampParentSessionEvent(se)
		if se.EventID == "" {
			if err := assignSessionEventIDFromContext(ctx, se); err != nil {
				setPersistErr(err)
				return false
			}
		}
		if se.Timestamp.IsZero() {
			se.Timestamp = newEventTimestamp()
		}
		if err := persistSessionEvent(se); err != nil {
			return false
		}
		event := &TypedAgentEvent[M]{SessionEventVariant: &SessionEventVariant[M]{SessionID: sessionState.sessionID, Event: se}}
		if enableTimelineEvents {
			gen.Send(event)
		}
		return true
	}
	reserveMessageStreamRef := func(event *TypedAgentEvent[M]) (*MessageStreamRef, error) {
		if event != nil && event.SessionEventVariant != nil && event.SessionEventVariant.MessageStreamRef != nil {
			ref := event.SessionEventVariant.MessageStreamRef
			if ref.Timestamp.IsZero() {
				ref.Timestamp = newEventTimestamp()
			}
			stampParentStreamRef(ref)
			ref.Kind = SessionEventMessage
			if ref.EventID == "" {
				draft := &SessionEvent[M]{
					Timestamp: ref.Timestamp,
					Kind:      SessionEventMessage,
				}
				stampParentSessionEvent(draft)
				if err := assignSessionEventID(ctx, draft, sessionState.sessionConfig.EventIDGenerator); err != nil {
					setPersistErr(err)
					return nil, err
				}
				ref.EventID = draft.EventID
				ref.TurnID = draft.TurnID
				ref.Timestamp = draft.Timestamp
			}
			return ref, nil
		}
		draft := &SessionEvent[M]{
			Timestamp: newEventTimestamp(),
			Kind:      SessionEventMessage,
		}
		stampParentSessionEvent(draft)
		if err := assignSessionEventID(ctx, draft, sessionState.sessionConfig.EventIDGenerator); err != nil {
			setPersistErr(err)
			return nil, err
		}
		return &MessageStreamRef{
			EventID:   draft.EventID,
			TurnID:    draft.TurnID,
			Timestamp: draft.Timestamp,
			Kind:      SessionEventMessage,
		}, nil
	}
	toSessionEventCheckedWithGenerator := func(event *TypedAgentEvent[M]) (*SessionEvent[M], error) {
		se, err := toSessionEventChecked(event)
		if err == nil || event == nil || event.SessionEventVariant != nil ||
			event.Output == nil || event.Output.MessageOutput == nil ||
			isNilMessage(event.Output.MessageOutput.Message) {
			return se, err
		}
		draft := &SessionEvent[M]{
			Timestamp: newEventTimestamp(),
			Kind:      SessionEventMessage,
			Message:   event.Output.MessageOutput.Message,
		}
		stampParentSessionEvent(draft)
		if idErr := assignSessionEventID(ctx, draft, sessionState.sessionConfig.EventIDGenerator); idErr != nil {
			return nil, idErr
		}
		event.SessionEventVariant = &SessionEventVariant[M]{SessionID: sessionState.sessionID, Event: draft}
		return draft, NormalizeSessionEventKind(draft)
	}
	// saveCheckpointNow is the path used when no session persister is active —
	// the checkpoint is written immediately because there are no queued events
	// to flush. In session mode, the same payload is captured into
	// pendingCheckpoint and committed inside finalize() after persister.closeAndWait.
	saveCheckpointNow := func(info *InterruptInfo, sig *core.InterruptSignal, errLabel string) {
		if checkPointID == nil {
			return
		}
		if info == nil {
			info = &InterruptInfo{}
		}
		info.CheckPointID = *checkPointID
		if persister != nil {
			pendingCheckpoint = &deferredRunnerCheckpoint{info: info, signal: sig, errLabel: errLabel}
			return
		}
		if err := saveRunnerCheckpoint(enableStreaming, store, ctx, *checkPointID, info, sig, sessionState); err != nil {
			gen.Send(&TypedAgentEvent[M]{Err: fmt.Errorf("%s: %w", errLabel, err)})
		}
	}

	for {
		event, ok := aIter.Next()
		if !ok {
			break
		}
		fromOtherSession := event.SessionEventVariant != nil &&
			sessionState != nil && sessionState.enabled &&
			event.SessionEventVariant.SessionID != "" &&
			event.SessionEventVariant.SessionID != sessionState.sessionID
		if !fromOtherSession && event.SessionEventVariant != nil && event.SessionEventVariant.Event != nil {
			stampParentSessionEvent(event.SessionEventVariant.Event)
			gen := DefaultSessionEventIDGenerator[M]
			if sessionState != nil && sessionState.enabled {
				gen = sessionState.sessionConfig.EventIDGenerator
				if gen == nil {
					gen = DefaultSessionEventIDGenerator[M]
				}
			}
			if _, err := normalizeAgentSessionEventWithAssigner(event, func(draft *SessionEvent[M]) (string, error) {
				return gen(ctx, draft)
			}); err != nil {
				setPersistErr(err)
				event.Err = err
			}
		}
		if !fromOtherSession && event.SessionEventVariant != nil && event.SessionEventVariant.MessageStreamRef != nil {
			stampParentStreamRef(event.SessionEventVariant.MessageStreamRef)
		}
		if err := validateAgentSessionEventIdentity(event); err != nil {
			setPersistErr(err)
			event.Err = err
		}

		if event.Err != nil {
			var retryErr *RetryExhaustedError
			if errors.As(event.Err, &retryErr) {
				retryExhausted = true
			}
			var cancelErr *CancelError
			if errors.As(event.Err, &cancelErr) {
				cancelled = true
				if cancelCtx != nil && cancelCtx.isRoot() && cancelCtx.shouldCancel() {
					cancelCtx.markCancelHandled()
				}
				if cancelErr.interruptSignal != nil && checkPointID != nil {
					cancelErr.InterruptContexts = core.ToInterruptContexts(cancelErr.interruptSignal, allowedAddressSegmentTypes)
					saveCheckpointNow(&InterruptInfo{}, cancelErr.interruptSignal, "failed to save checkpoint on cancel")
				}
				if !enableTimelineEvents {
					event = stripSessionEventFields(event)
					if event == nil {
						break
					}
				}
				gen.Send(event)
				break
			}
			if terminalErr == nil {
				terminalErr = event.Err
			}
		}

		if event.Action != nil && event.Action.internalInterrupted != nil {
			if interruptSignal != nil {
				panic("multiple interrupt actions should not happen in Runner")
			}
			interruptSignal = event.Action.internalInterrupted
			interruptContexts = core.ToInterruptContexts(interruptSignal, allowedAddressSegmentTypes)
			event = &TypedAgentEvent[M]{
				AgentName: event.AgentName,
				RunPath:   event.RunPath,
				Output:    event.Output,
				Action: &AgentAction{
					Interrupted: &InterruptInfo{
						Data:              event.Action.Interrupted.Data,
						CheckPointID:      valueOrEmpty(checkPointID),
						InterruptContexts: interruptContexts,
					},
					internalInterrupted: interruptSignal,
				},
			}
			legacyData = event.Action.Interrupted.Data
			interrupted = true

			if checkPointID != nil {
				saveCheckpointNow(&InterruptInfo{Data: legacyData}, interruptSignal, "failed to save checkpoint")
			}
		}

		liveDelivered := false
		if persister != nil {
			// Skip persistence (but not live delivery) for events owned by a
			// different session (inner agent events forwarded via AgentTool).
			if !fromOtherSession {
				if event.Output != nil && event.Output.MessageOutput != nil &&
					event.Output.MessageOutput.IsStreaming && event.Output.MessageOutput.MessageStream != nil {
					ref, err := reserveMessageStreamRef(event)
					if err != nil {
						continue
					}
					// Streaming output is split into two stream copies: copies[1] is
					// rewritten onto the live event and sent immediately so live
					// consumers see no extra latency. The message boundary is committed
					// after copies[0] is drained and fully materialized.
					copies := event.Output.MessageOutput.MessageStream.Copy(2)
					liveOutput := *event.Output
					liveMV := *event.Output.MessageOutput
					liveMV.MessageStream = copies[1]

					liveOutput.MessageOutput = &liveMV
					event.Output = &liveOutput
					event.SessionEventVariant = &SessionEventVariant[M]{SessionID: sessionState.sessionID, MessageStreamRef: ref}
					liveEvent := event
					if !enableTimelineEvents {
						liveEvent = stripSessionEventFields(liveEvent)
					}
					if liveEvent != nil {
						gen.Send(liveEvent)
					}
					liveDelivered = true

					persistedMsg, hasChunks, streamErr, err := materializeMessageStreamPrefix(copies[0])
					if err != nil {
						// Prefix projection is best-effort replay data. A concat failure
						// should not fail the turn after the source stream already failed.
						continue
					}
					if streamErr != nil {
						if hasChunks {
							_ = persistSessionEvent(&SessionEvent[M]{
								EventID:   ref.EventID,
								TurnID:    ref.TurnID,
								Timestamp: ref.Timestamp,
								Kind:      SessionEventMessageStreamIncomplete,
								MessageStreamIncomplete: &MessageStreamIncompleteEvent[M]{
									Message: persistedMsg,
									Error:   streamErr.Error(),
								},
							})
						}
						continue
					}
					if !hasChunks {
						continue
					}

					_ = persistSessionEvent(&SessionEvent[M]{
						EventID:   ref.EventID,
						TurnID:    ref.TurnID,
						Timestamp: ref.Timestamp,
						Kind:      SessionEventMessage,
						Message:   persistedMsg,
					})
				} else {
					// Non-streaming events go through toSessionEvent directly.
					se, err := toSessionEventCheckedWithGenerator(event)
					if err != nil {
						setPersistErr(err)
						se = nil
					}
					if se != nil {
						if err := persistSessionEvent(se); err != nil {
							continue
						}
						// Backfill SessionEventVariant onto the live event so downstream
						// consumers (TurnLoop/onAgentEvents) see message events
						// with their persisted SessionEvent identity, consistent
						// with how span events are already delivered.
						event.SessionEventVariant = &SessionEventVariant[M]{SessionID: sessionState.sessionID, Event: se}
					}
				}
			}
		}

		if liveDelivered {
			continue
		}

		if !enableTimelineEvents {
			event = stripSessionEventFields(event)
			if event == nil {
				continue
			}
		}
		gen.Send(event)
	}
	if persister != nil {
		stopReason := "end_turn"
		switch {
		case interrupted:
			stopReason = "interrupted"
		case cancelled:
			stopReason = "cancelled"
		case retryExhausted:
			stopReason = "retries_exhausted"
		case persistErr != nil:
			stopReason = "failed"
		case terminalErr != nil:
			stopReason = "failed"
		}
		if stopReason == "failed" {
			errMsg := ""
			if persistErr != nil {
				errMsg = persistErr.Error()
			} else if terminalErr != nil {
				errMsg = terminalErr.Error()
			}
			sendTimelineEvent(&SessionEvent[M]{
				Timestamp: newEventTimestamp(),
				Kind:      SessionEventSessionError,
				Error:     &SessionErrorEvent{Type: SessionErrorTypeFatal, Message: errMsg},
			})
		}
		if interrupted {
			sendTimelineEvent(&SessionEvent[M]{
				Timestamp: newEventTimestamp(),
				Kind:      SessionEventInterrupt,
				Interrupt: buildInterruptEvent(interruptContexts),
			})
		}
		if cancelled {
			sendTimelineEvent(&SessionEvent[M]{
				Timestamp: newEventTimestamp(),
				Kind:      SessionEventCancel,
				Cancel:    &CancelEvent{Reason: "cancelled"},
			})
		}
		sendTimelineEvent(&SessionEvent[M]{
			Timestamp: newEventTimestamp(),
			Kind:      SessionEventSessionStatusIdle,
			Lifecycle: &LifecycleEvent{State: SessionRunStateIdle, StopReason: &StopReason{Type: stopReason}},
		})
		res := &sessionTurnResult[M]{
			persister:         persister,
			persistErr:        persistErr,
			interrupted:       interrupted,
			cancelled:         cancelled,
			terminalErr:       terminalErr,
			sessionState:      sessionState,
			store:             store,
			checkPointID:      checkPointID,
			enableStreaming:   enableStreaming,
			pendingCheckpoint: pendingCheckpoint,
		}
		if err := res.finalize(ctx); err != nil {
			gen.Send(&TypedAgentEvent[M]{Err: err})
		}
	}
}

func buildInterruptEvent(
	contexts []*InterruptCtx,
) *InterruptEvent {
	event := &InterruptEvent{
		Contexts: make([]*InterruptContext, 0, len(contexts)),
	}
	for _, ctx := range contexts {
		if ctx == nil {
			continue
		}
		aic := &InterruptContext{
			InterruptID: ctx.ID,
			Info:        ctx.Info,
		}
		if toolUseID := extractToolUseID(ctx); toolUseID != "" {
			aic.ToolUseID = toolUseID
		}
		event.Contexts = append(event.Contexts, aic)
	}
	return event
}

func extractToolUseID(ctx *InterruptCtx) string {
	for _, segment := range ctx.Address {
		if segment.Type != AddressSegmentTool {
			continue
		}
		if segment.SubID != "" {
			return segment.SubID
		}
		return segment.ID
	}
	return ""
}

// deferredRunnerCheckpoint captures the arguments needed to persist a runner
// checkpoint after the session event persister has flushed. Saving the
// checkpoint earlier would risk a checkpoint that references events not yet
// durable in the SessionEventStore.
type deferredRunnerCheckpoint struct {
	info     *InterruptInfo
	signal   *core.InterruptSignal
	errLabel string
}

// sessionTurnResult bundles the accumulated state from a Runner turn's event
// loop and drives the session commit-or-abort decision.
type sessionTurnResult[M MessageType] struct {
	persister         *sessionEventPersister[M]
	persistErr        error
	interrupted       bool
	cancelled         bool
	terminalErr       error
	sessionState      *runnerSessionRunState[M]
	store             CheckPointStore
	checkPointID      *string
	enableStreaming   bool
	pendingCheckpoint *deferredRunnerCheckpoint
}

func (r *sessionTurnResult[M]) finalize(ctx context.Context) error {
	defer func() {
		if r.sessionState != nil && r.sessionState.sessionHandle != nil {
			_ = r.sessionState.sessionHandle.close(ctx)
		}
	}()
	if err := r.persister.closeAndWait(); err != nil && r.persistErr == nil {
		r.persistErr = err
	}
	// For interrupt/cancel paths, the checkpoint write is deferred until here so
	// the persister's queued events are durable BEFORE the checkpoint references
	// them. If event persistence failed, skip the checkpoint write entirely so
	// resume cannot load a checkpoint that points to a corrupt event log.
	if r.pendingCheckpoint != nil && r.checkPointID != nil {
		if r.persistErr != nil {
			return fmt.Errorf("%s: skipped because session event persistence failed: %w", r.pendingCheckpoint.errLabel, r.persistErr)
		}
		if err := saveRunnerCheckpoint(r.enableStreaming, r.store, ctx, *r.checkPointID, r.pendingCheckpoint.info, r.pendingCheckpoint.signal, r.sessionState); err != nil {
			return fmt.Errorf("%s: %w", r.pendingCheckpoint.errLabel, err)
		}
	}
	if r.persistErr != nil {
		return fmt.Errorf("failed to persist session events: %w", r.persistErr)
	}
	if r.interrupted || r.cancelled {
		return nil
	}
	if r.terminalErr != nil {
		return nil
	}
	if r.checkPointID != nil && !isNilCheckPointStore(r.store) {
		if err := deleteCheckPointIfSupported(ctx, r.store, *r.checkPointID); err != nil {
			return fmt.Errorf("failed to delete session checkpoint: %w", err)
		}
	}
	return nil
}
