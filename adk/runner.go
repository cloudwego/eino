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
	"runtime/debug"
	"sync"

	"github.com/google/uuid"

	"github.com/cloudwego/eino/internal/core"
	"github.com/cloudwego/eino/internal/safe"
	"github.com/cloudwego/eino/schema"
)

func errorIterator[M MessageType](err error) *AsyncIterator[*TypedAgentEvent[M]] {
	iter, gen := NewAsyncIteratorPair[*TypedAgentEvent[M]]()
	gen.Send(&TypedAgentEvent[M]{Timestamp: newEventTimestamp(), Err: err})
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
	sessionStore    SessionStore
	sessionPersist  *SessionPersistenceConfig
}

// Runner is the default runner type using *schema.Message.
type Runner = TypedRunner[*schema.Message]

type CheckPointStore = core.CheckPointStore

type CheckPointDeleter = core.CheckPointDeleter

type TypedRunnerConfig[M MessageType] struct {
	Agent           TypedAgent[M]
	EnableStreaming bool

	CheckPointStore CheckPointStore

	SessionID          string
	SessionStore       SessionStore
	SessionPersistence *SessionPersistenceConfig
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
		sessionPersist:  conf.SessionPersistence,
	}
}

func (r *TypedRunner[M]) Run(ctx context.Context, messages []M,
	opts ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[M]] {
	return typedRunnerRunImpl(r.a, r.enableStreaming, r.store, r.sessionID, r.sessionStore, r.sessionPersist, ctx, messages, opts...)
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
	return typedRunnerResumeInternalImpl(r.a, r.store, r.sessionID, r.sessionStore, r.sessionPersist, ctx, checkPointID, resumeData, opts...)
}

type runnerSessionRunState[M MessageType] struct {
	enabled         bool
	sessionID       string
	checkPointID    *string
	latestState     *TurnEndState[M]
	persistence     SessionPersistenceConfig
	sessionStore    SessionStore
	checkPointStore CheckPointStore
	runID           string
	turnID          string
	// inputMessages are the caller-provided messages for this turn (before history prepend).
	// Captured so the Runner can persist them as session events at turn start.
	inputMessages []M
}

func mergeSessionValues(restored, overrides map[string]any) map[string]any {
	if len(restored) == 0 && len(overrides) == 0 {
		return nil
	}
	merged := make(map[string]any, len(restored)+len(overrides))
	for k, v := range restored {
		merged[k] = v
	}
	for k, v := range overrides {
		merged[k] = v
	}
	return merged
}

func valueOrEmpty(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func prepareRunnerSessionRun[M MessageType]( //nolint:revive // argument-limit
	ctx context.Context,
	checkPointStore CheckPointStore,
	requestedCheckPointID *string,
	sessionID string,
	sessionStore SessionStore,
	sessionPersistence *SessionPersistenceConfig,
) (*runnerSessionRunState[M], error) {
	state := &runnerSessionRunState[M]{}
	if sessionID == "" || sessionStore == nil {
		return state, nil
	}
	state.enabled = true
	state.sessionID = sessionID
	state.runID = uuid.NewString()
	state.turnID = uuid.NewString()
	state.sessionStore = sessionStore
	state.checkPointStore = checkPointStore
	state.persistence = normalizeSessionPersistenceConfig(sessionPersistence)
	state.latestState = &TurnEndState[M]{}

	pageSize := state.persistence.LoadPageSize

	reconstructed, err := reconstructSessionState[M](ctx, sessionStore, sessionID, pageSize, state.persistence.EventSerializer)
	if err != nil {
		return nil, fmt.Errorf("failed to reconstruct session[%s]: %w", sessionID, err)
	}
	if reconstructed != nil {
		state.latestState = reconstructed
	}

	if checkPointStore == nil {
		return state, nil
	}
	checkPointID := sessionRunnerCheckpointID(sessionID)
	if requestedCheckPointID != nil && *requestedCheckPointID != "" {
		checkPointID = *requestedCheckPointID
	}
	state.checkPointID = &checkPointID
	_, existed, err := loadRunnerSessionCheckpoint(ctx, checkPointStore, checkPointID)
	if err != nil {
		return nil, err
	}
	if !existed {
		return state, nil
	}
	return nil, fmt.Errorf("%w: session %q has a pending checkpoint; resume or discard it before new input", ErrPendingSessionCheckpoint, sessionID)
}

func prepareRunnerSessionResume[M MessageType](
	ctx context.Context,
	checkPointStore CheckPointStore,
	sessionID string,
	sessionStore SessionStore,
	sessionPersistence *SessionPersistenceConfig,
	checkPointID string,
) (*runnerSessionRunState[M], string, error) {
	state := &runnerSessionRunState[M]{}
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
	state.runID = uuid.NewString()
	state.turnID = uuid.NewString()
	state.sessionStore = sessionStore
	state.checkPointStore = checkPointStore
	state.persistence = normalizeSessionPersistenceConfig(sessionPersistence)
	state.latestState = &TurnEndState[M]{}

	pageSize := state.persistence.LoadPageSize

	reconstructed, err := reconstructSessionState[M](ctx, sessionStore, sessionID, pageSize, state.persistence.EventSerializer)
	if err != nil {
		return nil, "", fmt.Errorf("failed to reconstruct session[%s]: %w", sessionID, err)
	}
	if reconstructed != nil {
		state.latestState = reconstructed
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
	if checkPointID == "" {
		_, existed, err := loadRunnerSessionCheckpoint(ctx, checkPointStore, effectiveCheckPointID)
		if err != nil {
			return nil, "", err
		}
		if !existed {
			return nil, "", fmt.Errorf("no pending session checkpoint for session %q", sessionID)
		}
	}
	return state, effectiveCheckPointID, nil
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
	if store == nil {
		return nil
	}
	payload, err := encodeRunnerCheckPointImpl(enableStreaming, ctx, info, is)
	if err != nil {
		return err
	}
	data, err := encodeRunnerSessionCheckpoint(&runnerSessionCheckpoint{
		Payload: payload,
	})
	if err != nil {
		return err
	}
	return store.Set(ctx, checkPointID, data)
}

func typedRunnerRunImpl[M MessageType](a TypedAgent[M], enableStreaming bool, store CheckPointStore, sessionID string, sessionStore SessionStore, sessionPersistence *SessionPersistenceConfig, ctx context.Context, messages []M, opts ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[M]] { //nolint:revive // argument-limit
	o := getCommonOptions(nil, opts...)
	exposeTimelineEvents := o.enableTimelineEvents

	sessionState, err := prepareRunnerSessionRun[M](ctx, store, o.checkPointID, sessionID, sessionStore, sessionPersistence)
	if err != nil {
		return errorIterator[M](err)
	}
	if sessionState.enabled {
		// Capture caller-provided messages BEFORE prepending history. These will be
		// emitted as session events at turn start so they appear in the event log.
		sessionState.inputMessages = append([]M{}, messages...)
		// Assign eino message IDs to input messages (needed for BeforeMessageID references
		// emitted by middlewares that anchor on user messages).
		for _, msg := range sessionState.inputMessages {
			EnsureMessageID(msg)
		}
		messages = append(append([]M{}, sessionState.latestState.Messages...), sessionState.inputMessages...)
		o.sessionValues = mergeSessionValues(sessionState.latestState.SessionValues, o.sessionValues)
		opts = append(opts, withEnableSessionEvents())
		opts = append(opts, withEnableInternalTimelineEvents())
		if !o.refreshToolInfos && len(sessionState.latestState.ToolInfos) > 0 {
			opts = append(opts, withPreviousTurnToolInfos(
				sessionState.latestState.ToolInfos,
				sessionState.latestState.DeferredToolInfos,
			))
		}
	}

	input := &TypedAgentInput[M]{
		Messages:        messages,
		EnableStreaming: enableStreaming,
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
		ctx = contextWithToolPermissionDecisionStore(ctx)
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
	ctx = contextWithToolPermissionDecisionStore(ctx)
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

func typedRunnerResumeInternalImpl[M MessageType](a TypedAgent[M], store CheckPointStore, sessionID string, sessionStore SessionStore, sessionPersistence *SessionPersistenceConfig, ctx context.Context, checkPointID string, resumeData map[string]any, //nolint:revive // argument-limit
	opts ...AgentRunOption) (*AsyncIterator[*TypedAgentEvent[M]], error) {
	if store == nil {
		return nil, fmt.Errorf("failed to resume: store is nil")
	}

	o := getCommonOptions(nil, opts...)
	exposeTimelineEvents := o.enableTimelineEvents
	sessionState, effectiveCheckPointID, err := prepareRunnerSessionResume[M](ctx, store, sessionID, sessionStore, sessionPersistence, checkPointID)
	if err != nil {
		return nil, err
	}
	checkPointID = effectiveCheckPointID
	if sessionState.enabled {
		opts = append(opts, withEnableSessionEvents())
		opts = append(opts, withEnableInternalTimelineEvents())
	}

	ctx, runCtx, resumeInfo, err := runnerLoadCheckPointForSession(store, ctx, checkPointID, sessionState.enabled)
	if err != nil {
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
	ctx = contextWithToolPermissionDecisionStore(ctx)
	AddSessionValues(ctx, o.sessionValues)

	if len(resumeData) > 0 {
		ctx = core.BatchResumeWithData(ctx, resumeData)
	}

	var zero M
	if _, ok := any(zero).(*schema.Message); ok {
		concreteAgent, _ := any(a).(Agent)
		fa := toFlowAgent(ctx, concreteAgent)
		ra, ok := any(fa).(ResumableAgent)
		if !ok {
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
			gen.Send(&TypedAgentEvent[M]{Timestamp: newEventTimestamp(), Err: e})
		}

		gen.Close()
	}()
	var (
		interruptSignal *core.InterruptSignal
		legacyData      any
		interrupted     bool
		cancelled       bool
		retryExhausted  bool
		sawTurnEnd      bool
		persister       *sessionEventPersister[M]
		persistErr      error
		// pendingCheckpoint defers checkpoint save to finalize() so the persister
		// can flush enqueued events first. Writing the checkpoint before the flush
		// completes risks a checkpoint that references events not yet durable.
		pendingCheckpoint *deferredRunnerCheckpoint
	)
	if sessionState != nil && sessionState.enabled {
		persister = newSessionEventPersister[M](ctx, sessionState.sessionStore, sessionState.sessionID, sessionState.persistence)
	}
	setPersistErr := func(err error) {
		if err != nil && persistErr == nil {
			persistErr = err
		}
	}
	annotateSessionEvent := func(se *SessionEvent[M]) *SessionEvent[M] {
		if se == nil || sessionState == nil || !sessionState.enabled {
			return se
		}
		se.RunID = sessionState.runID
		se.TurnID = sessionState.turnID
		return se
	}
	enqueueSessionEvent := func(se *SessionEvent[M]) {
		if persister == nil || se == nil {
			return
		}
		annotateSessionEvent(se)
		if err := ValidateEmittedSessionEventKind(se); err != nil {
			setPersistErr(err)
			return
		}
		data, err := encodeSessionEventWithSerializer(se, sessionState.persistence.EventSerializer)
		if err != nil {
			setPersistErr(err)
			return
		}
		if err := persister.enqueue(data); err != nil {
			setPersistErr(err)
		}
	}
	sendTimelineEvent := func(se *SessionEvent[M]) {
		if se == nil {
			return
		}
		annotateSessionEvent(se)
		if se.EventID == "" {
			se.EventID = uuid.NewString()
		}
		if se.Timestamp.IsZero() {
			se.Timestamp = newEventTimestamp()
		}
		if err := ValidateEmittedSessionEventKind(se); err != nil {
			setPersistErr(err)
			return
		}
		event := &TypedAgentEvent[M]{EventID: se.EventID, Timestamp: se.Timestamp, SessionEvent: se}
		enqueueSessionEvent(se)
		if enableTimelineEvents {
			gen.Send(event)
		}
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
			gen.Send(&TypedAgentEvent[M]{Timestamp: newEventTimestamp(), Err: fmt.Errorf("%s: %w", errLabel, err)})
		}
	}

	// Emit caller-provided input messages as session events at turn start, so the
	// event log carries the user's input alongside the agent's output. Skipped on
	// resume (sessionState.inputMessages is nil).
	if persister != nil {
		sendTimelineEvent(&SessionEvent[M]{
			EventID:   uuid.NewString(),
			Timestamp: newEventTimestamp(),
			Kind:      SessionEventSessionStatusRunning,
			Lifecycle: &LifecycleEvent{Scope: LifecycleScopeSession, State: SessionRunStateRunning},
		})
	}
	if persister != nil && len(sessionState.inputMessages) > 0 {
		for _, msg := range sessionState.inputMessages {
			se := makeInputSessionEvent[M](msg)
			enqueueSessionEvent(se)
		}
	}
	for {
		event, ok := aIter.Next()
		if !ok {
			break
		}
		if event.Timestamp.IsZero() {
			event.Timestamp = newEventTimestamp()
		}
		if event.SessionEvent != nil {
			if _, err := normalizeAgentSessionEvent(event); err != nil {
				setPersistErr(err)
				event.Err = err
			}
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
		}

		if event.Action != nil && event.Action.internalInterrupted != nil {
			if interruptSignal != nil {
				panic("multiple interrupt actions should not happen in Runner")
			}
			interruptSignal = event.Action.internalInterrupted
			interruptContexts := core.ToInterruptContexts(interruptSignal, allowedAddressSegmentTypes)
			event = &TypedAgentEvent[M]{
				Timestamp: event.Timestamp,
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
			// Track TurnEnd presence for commit validation.
			if isTurnEndAgentEvent(event) {
				sawTurnEnd = true
			}

			// Skip persistence (but not live delivery) for events tagged with a
			// different SessionID (inner agent events forwarded via AgentTool).
			fromOtherSession := event.SessionID != "" && event.SessionID != sessionState.sessionID

			if !fromOtherSession {
				if event.EventID == "" {
					event.EventID = uuid.NewString()
				}
				// Streaming output is split into two stream copies: copies[1] is
				// rewritten onto the live event and sent immediately so live
				// consumers see no extra latency, copies[0] is then drained
				// synchronously to materialize the persisted SessionEvent. The
				// live send MUST happen first — the AsyncIterator's send buffer
				// keeps the consumer un-blocked while this loop drains the
				// persistence copy.
				if event.Output != nil && event.Output.MessageOutput != nil &&
					event.Output.MessageOutput.IsStreaming && event.Output.MessageOutput.MessageStream != nil {
					copies := event.Output.MessageOutput.MessageStream.Copy(2)
					liveOutput := *event.Output
					liveMV := *event.Output.MessageOutput
					liveMV.MessageStream = copies[1]

					// Rewrite the live event to the second stream copy and send it
					// before materializing the persisted copy. This keeps managed
					// sessions from delaying live stream delivery on persistence.
					liveOutput.MessageOutput = &liveMV
					event.Output = &liveOutput
					liveEvent := event
					if !enableTimelineEvents {
						liveEvent = stripSessionEventFields(liveEvent)
					}
					if liveEvent != nil {
						gen.Send(liveEvent)
					}
					liveDelivered = true

					persistCopy := &TypedMessageVariant[M]{IsStreaming: true, MessageStream: copies[0]}
					persistedMsg, err := persistCopy.GetMessage()
					if err != nil {
						setPersistErr(err)
						continue
					}

					persistMV := *event.Output.MessageOutput
					persistMV.Message = persistedMsg
					persistMV.MessageStream = nil
					persistMV.IsStreaming = false
					persistOutput := *event.Output
					persistOutput.MessageOutput = &persistMV
					persistEvent := *event
					persistEvent.Output = &persistOutput

					se, err := toSessionEventChecked(&persistEvent)
					if err != nil {
						setPersistErr(err)
						continue
					}
					if se != nil {
						enqueueSessionEvent(se)
					}
				} else {
					// Non-streaming events go through toSessionEvent directly.
					se, err := toSessionEventChecked(event)
					if err != nil {
						setPersistErr(err)
						se = nil
					}
					if se != nil {
						enqueueSessionEvent(se)
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
		case !sawTurnEnd:
			stopReason = "failed"
		}
		if interrupted {
			sendTimelineEvent(&SessionEvent[M]{
				EventID:         uuid.NewString(),
				Timestamp:       newEventTimestamp(),
				Kind:            SessionEventUserInterrupt,
				UserObservation: &UserObservationEvent{Interrupt: &UserInterruptEvent{Reason: "interrupted"}},
			})
		}
		sendTimelineEvent(&SessionEvent[M]{
			EventID:   uuid.NewString(),
			Timestamp: newEventTimestamp(),
			Kind:      SessionEventSessionStatusIdle,
			Lifecycle: &LifecycleEvent{Scope: LifecycleScopeSession, State: SessionRunStateIdle, StopReason: &StopReason{Type: stopReason}},
		})
		res := &sessionTurnResult[M]{
			persister:         persister,
			persistErr:        persistErr,
			interrupted:       interrupted,
			cancelled:         cancelled,
			sawTurnEnd:        sawTurnEnd,
			sessionState:      sessionState,
			store:             store,
			checkPointID:      checkPointID,
			enableStreaming:   enableStreaming,
			pendingCheckpoint: pendingCheckpoint,
		}
		if err := res.finalize(ctx); err != nil {
			gen.Send(&TypedAgentEvent[M]{Timestamp: newEventTimestamp(), Err: err})
		}
	}
}

// deferredRunnerCheckpoint captures the arguments needed to persist a runner
// checkpoint after the session event persister has flushed. Saving the
// checkpoint earlier would risk a checkpoint that references events not yet
// durable in the SessionStore.
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
	sawTurnEnd        bool
	sessionState      *runnerSessionRunState[M]
	store             CheckPointStore
	checkPointID      *string
	enableStreaming   bool
	pendingCheckpoint *deferredRunnerCheckpoint
}

func (r *sessionTurnResult[M]) finalize(ctx context.Context) error {
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
	if r.interrupted || r.cancelled {
		return nil
	}
	if r.persistErr != nil {
		return fmt.Errorf("failed to persist session events: %w", r.persistErr)
	}
	if !r.sawTurnEnd {
		return fmt.Errorf("failed to commit session[%s]: missing SessionEventTurnEnd", r.sessionState.sessionID)
	}
	if r.checkPointID != nil && r.store != nil {
		if err := deleteCheckPointIfSupported(ctx, r.store, *r.checkPointID); err != nil {
			return fmt.Errorf("failed to delete session checkpoint: %w", err)
		}
	}
	return nil
}
