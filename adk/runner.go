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
	"reflect"
	"runtime/debug"
	"sync"

	"github.com/cloudwego/eino/internal/core"
	"github.com/cloudwego/eino/internal/safe"
	internalschema "github.com/cloudwego/eino/internal/schema"
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

type runnerSessionIDContextKey struct{}

// RunnerSessionID returns the current Runner invocation's session ID. It is
// request-scoped routing metadata, not a container for Runner dependencies.
func RunnerSessionID(ctx context.Context) (string, bool) {
	sessionID, ok := ctx.Value(runnerSessionIDContextKey{}).(string)
	return sessionID, ok && sessionID != ""
}

func (r *TypedRunner[M]) withRunnerSession(ctx context.Context) context.Context {
	return context.WithValue(ctx, runnerSessionIDContextKey{}, r.sessionID)
}

// CheckPointStore persists Runner checkpoints. A successful Set must mean that
// the checkpoint is durable and immediately visible to subsequent Get calls.
type CheckPointStore = core.CheckPointStore

type CheckPointDeleter = core.CheckPointDeleter

// ErrCheckpointSave identifies a Runner checkpoint persistence failure.
var ErrCheckpointSave = errors.New("adk: checkpoint save failed")

// ErrSessionEventPersistence identifies a Runner session-event persistence
// failure. It is distinct from ErrCheckpointSave because retrying a checkpoint
// write cannot repair an incomplete session event log.
var ErrSessionEventPersistence = errors.New("adk: session event persistence failed")

type checkpointSaveError struct {
	label string
	cause error
}

func (e *checkpointSaveError) Error() string {
	return fmt.Sprintf("%s: %v", e.label, e.cause)
}

func (e *checkpointSaveError) Unwrap() error { return e.cause }

func (e *checkpointSaveError) Is(target error) bool { return target == ErrCheckpointSave }

func newCheckpointSaveError(label string, cause error) error {
	return &checkpointSaveError{label: label, cause: cause}
}

type sessionEventPersistenceError struct {
	label string
	cause error
}

func (e *sessionEventPersistenceError) Error() string {
	return fmt.Sprintf("%s: %v", e.label, e.cause)
}

func (e *sessionEventPersistenceError) Unwrap() error { return e.cause }

func (e *sessionEventPersistenceError) Is(target error) bool {
	return target == ErrSessionEventPersistence
}

func newSessionEventPersistenceError(label string, cause error) error {
	return &sessionEventPersistenceError{label: label, cause: cause}
}

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
	return typedRunnerRunImpl(
		r.a, r.enableStreaming, r.store, r.sessionID, r.sessionStore, r.sessionConfig,
		r.withRunnerSession(ctx), messages, opts...,
	)
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
// With a SessionStore, Resume checks the session ID recorded in the checkpoint.
// If the checkpoint also records an idle event ID, Resume checks it against
// the session log. A session or version mismatch returns ErrCheckpointStale.
//
// When using this method, all interrupted agents will receive `isResumeFlow = false` when they
// call `GetResumeContext`, as no specific agent was targeted. This is suitable for the "Simple Confirmation"
// pattern where an agent only needs to know `wasInterrupted` is true to continue.
func (r *TypedRunner[M]) Resume(ctx context.Context, checkPointID string, opts ...AgentRunOption) (
	*AsyncIterator[*TypedAgentEvent[M]], error) {
	return r.ResumeWithParams(ctx, checkPointID, nil, opts...)
}

// ResumeWithParams continues an interrupted execution from a checkpoint with specific parameters.
// This is the most common and powerful way to resume, allowing you to target specific interrupt points
// (identified by their address/ID) and provide them with data.
//
// Session checkpoints use the same freshness check as Resume.
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
	iter, _, err := r.resumeInternal(ctx, checkPointID, params, opts...)
	return iter, err
}

// resumeInternal also reports whether the loaded runner state can be retired by TurnLoop.
func (r *TypedRunner[M]) resumeInternal(ctx context.Context, checkPointID string, params *ResumeParams,
	opts ...AgentRunOption) (*AsyncIterator[*TypedAgentEvent[M]], bool, error) {
	var resumeData map[string]any
	if params != nil {
		resumeData = params.Targets
	}
	state := &runnerSessionRunState[M]{}
	iter, err := typedRunnerResumeInternalImpl(
		r.a, r.store, r.sessionID, r.sessionStore, r.sessionConfig,
		r.withRunnerSession(ctx), checkPointID, resumeData, state, opts...,
	)
	return iter, state.checkpointConsumed, err
}

type runnerSessionRunState[M MessageType] struct {
	checkpointConsumed bool // set synchronously during resume admission
	enabled            bool
	sessionID          string
	checkPointID       *string
	latestState        *reconstructedSessionState[M]
	sessionConfig      SessionConfig[M]
	sessionStore       SessionEventStore[M]
	sessionHandle      *sessionHandle[M]
	checkPointStore    CheckPointStore
	turnID             string
	initialTimeline    []*SessionEvent[M]
}

func (state *runnerSessionRunState[M]) close(ctx context.Context) {
	if state != nil && state.sessionHandle != nil {
		_ = state.sessionHandle.close(ctx)
	}
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

func (state *runnerSessionRunState[M]) prepareRun( //nolint:revive // argument-limit
	ctx context.Context,
	checkPointStore CheckPointStore,
	requestedCheckPointID *string,
	sessionID string,
	sessionStore SessionEventStore[M],
	sessionConfig *SessionConfig[M],
) error {
	if isNilCheckPointStore(checkPointStore) {
		checkPointStore = nil
	}
	if sessionID == "" || sessionStore == nil {
		return nil
	}
	state.enabled = true
	state.sessionID = sessionID
	state.sessionStore = sessionStore
	state.checkPointStore = checkPointStore
	state.sessionConfig = normalizeSessionConfig(sessionConfig)
	state.latestState = &reconstructedSessionState[M]{}
	handle, err := openRunnerSession[M](ctx, sessionStore, sessionID)
	if err != nil {
		return err
	}
	state.sessionHandle = handle

	reconstructResult, err := reconstructSessionState[M](ctx, handle, defaultLoadPageSize, state.sessionConfig.MaxReplayLoadPages)
	if err != nil {
		return fmt.Errorf("failed to reconstruct session[%s]: %w", sessionID, err)
	}
	if reconstructResult != nil && reconstructResult.state != nil {
		state.latestState = reconstructResult.state
	}

	if isNilCheckPointStore(checkPointStore) {
		return nil
	}
	checkPointID := sessionRunnerCheckpointID(sessionID)
	if requestedCheckPointID != nil && *requestedCheckPointID != "" {
		checkPointID = *requestedCheckPointID
	}
	state.checkPointID = &checkPointID
	// The ID is used to save a new checkpoint or clean up a stale one after a
	// successful turn. A fresh turn restores its messages from the event log.
	return nil
}

func (state *runnerSessionRunState[M]) prepareResume( //nolint:revive // argument-limit
	ctx context.Context,
	checkPointStore CheckPointStore,
	sessionID string,
	sessionStore SessionEventStore[M],
	sessionConfig *SessionConfig[M],
	checkPointID string,
) (*runnerSessionCheckpoint, error) {
	if sessionID == "" || sessionStore == nil {
		if checkPointID == "" {
			return nil, errors.New("failed to resume: checkpoint ID is empty")
		}
		return nil, nil
	}
	state.enabled = true
	state.sessionID = sessionID
	state.sessionStore = sessionStore
	state.checkPointStore = checkPointStore
	state.sessionConfig = normalizeSessionConfig(sessionConfig)
	handle, err := openRunnerSession[M](ctx, sessionStore, sessionID)
	if err != nil {
		return nil, err
	}
	state.sessionHandle = handle

	effectiveCheckPointID := checkPointID
	if effectiveCheckPointID == "" {
		effectiveCheckPointID = sessionRunnerCheckpointID(sessionID)
	}
	state.checkPointID = &effectiveCheckPointID

	cp, existed, err := loadRunnerSessionCheckpoint(ctx, checkPointStore, effectiveCheckPointID)
	if err != nil {
		return nil, err
	}
	if !existed {
		if checkPointID == "" {
			return nil, fmt.Errorf("no pending session checkpoint for session %q", sessionID)
		}
		return nil, fmt.Errorf("checkpoint[%s] not exist", effectiveCheckPointID)
	}
	if err := state.validateResumeCheckpoint(ctx, cp); err != nil {
		return nil, err
	}
	state.turnID = cp.TurnID
	return cp, nil
}

func (state *runnerSessionRunState[M]) validateResumeCheckpoint(ctx context.Context, cp *runnerSessionCheckpoint) error {
	handle := state.sessionHandle
	if cp.SessionID == "" && cp.IdleEventID == "" {
		return nil // The earliest session checkpoints recorded neither field.
	}
	// Even a checkpoint without a log position can identify its owning session.
	if cp.SessionID != handle.sessionID {
		return fmt.Errorf("%w: checkpoint belongs to session %q, requested session %q", ErrCheckpointStale, cp.SessionID, handle.sessionID)
	}
	if cp.IdleEventID == "" {
		return nil
	}
	latest, err := handle.latestCheckpointEvent(ctx)
	if err != nil {
		return err
	}
	var latestEventID string
	if latest != nil {
		latestEventID = latest.EventID
	}
	if latestEventID != cp.IdleEventID {
		// A different event in the owning session supersedes this checkpoint.
		// A missing log or a session mismatch is insufficient evidence to retire it.
		state.checkpointConsumed = latestEventID != ""
		return fmt.Errorf("%w: session %q checkpoint idle event %q, latest relevant event %q",
			ErrCheckpointStale, handle.sessionID, cp.IdleEventID, latestEventID)
	}
	return nil
}

func (state *runnerSessionRunState[M]) appendResumeStartedEvent(ctx context.Context) error {
	resumeEvent := &SessionEvent[M]{
		Timestamp: newEventTimestamp(),
		TurnID:    state.turnID,
		Kind:      sessionEventResumeRequestStarted,
		Extension: &SessionExtensionEvent{},
	}
	if err := prepareSessionEventEnvelope(ctx, resumeEvent, state.sessionConfig.EventIDGenerator, state.sessionConfig.EventExtraProvider, sessionEventEnvelopeOptions{}); err != nil {
		return err
	}
	if state.turnID == "" {
		state.turnID = resumeEvent.EventID
		resumeEvent.TurnID = state.turnID
	}
	if err := state.sessionHandle.appendEvents(ctx, []*SessionEvent[M]{resumeEvent}); err != nil {
		return err
	}
	state.initialTimeline = append(state.initialTimeline, resumeEvent)
	return nil
}

func (state *runnerSessionRunState[M]) startResume(ctx context.Context) error {
	if state.enabled {
		if err := state.appendRunStart(ctx, nil); err != nil {
			return err
		}
	}
	// Without a session log the equivalent boundary is dispatch to the resumable agent.
	state.checkpointConsumed = true
	return nil
}

// appendRunStart writes running and all new inputs in one ordered batch before agent dispatch.
func (state *runnerSessionRunState[M]) appendRunStart(ctx context.Context, inputMessages []M) error {
	running := &SessionEvent[M]{
		Timestamp: newEventTimestamp(),
		Kind:      SessionEventSessionStatusRunning,
		Lifecycle: &LifecycleEvent{State: SessionRunStateRunning},
	}
	if err := prepareSessionEventEnvelope(ctx, running, state.sessionConfig.EventIDGenerator, state.sessionConfig.EventExtraProvider, sessionEventEnvelopeOptions{}); err != nil {
		return err
	}
	running.TurnID = state.turnID
	if running.TurnID == "" {
		running.TurnID = running.EventID
	}

	events := make([]*SessionEvent[M], 1+len(inputMessages))
	events[0] = running
	for i, msg := range inputMessages {
		event := makeInputSessionEvent(msg)
		event.TurnID = running.TurnID
		if err := prepareSessionEventEnvelope(ctx, event, state.sessionConfig.EventIDGenerator, state.sessionConfig.EventExtraProvider, sessionEventEnvelopeOptions{}); err != nil {
			return err
		}
		events[i+1] = event
	}
	if err := state.sessionHandle.appendEvents(ctx, events); err != nil {
		return err
	}
	state.turnID = running.TurnID
	state.initialTimeline = append(state.initialTimeline, events...)
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

func deleteCheckPointIfSupported(ctx context.Context, store CheckPointStore, checkPointID string) error {
	if isNilCheckPointStore(store) {
		return nil
	}
	if deleter, ok := store.(CheckPointDeleter); ok {
		return deleter.Delete(ctx, checkPointID)
	}
	return nil
}

func typedRunnerRunImpl[M MessageType](a TypedAgent[M], enableStreaming bool, store CheckPointStore, sessionID string, sessionStore SessionEventStore[M], sessionConfig *SessionConfig[M], ctx context.Context, messages []M, opts ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[M]] { //nolint:revive // argument-limit
	o := getCommonOptions(nil, opts...)
	exposeTimelineEvents := o.enableTimelineEvents

	sessionState := &runnerSessionRunState[M]{}
	handedOff := false
	defer func() {
		if !handedOff {
			sessionState.close(ctx)
		}
	}()
	err := sessionState.prepareRun(ctx, store, o.checkPointID, sessionID, sessionStore, sessionConfig)
	if err != nil {
		return errorIterator[M](err)
	}
	if sessionState.enabled {
		inputMessages := messages
		messages = append(append([]M{}, sessionState.latestState.Messages...), inputMessages...)
		sessionState.latestState.Messages = nil
		// Assign IDs before messages can be both persisted and inspected by
		// middleware, avoiding concurrent lazy ID mutation during event snapshotting.
		for _, msg := range messages {
			EnsureMessageID(msg)
		}
		if err := sessionState.appendRunStart(ctx, inputMessages); err != nil {
			return errorIterator[M](err)
		}
		opts = append(opts, withEnableSessionEvents())
		opts = append(opts, withEnableInternalTimelineEvents())
	}

	input := &TypedAgentInput[M]{
		Messages:        messages,
		EnableStreaming: enableStreaming,
	}

	if sessionState.enabled {
		ctx = contextWithSessionEventContext[M](ctx, sessionState.sessionConfig.EventIDGenerator, sessionState.sessionConfig.EventExtraProvider, sessionState.turnID)
	}

	var iter *AsyncIterator[*TypedAgentEvent[M]]
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

		iter = any(fa.Run(ctx, concreteInput, opts...)).(*AsyncIterator[*TypedAgentEvent[M]])
	} else {
		fa := toTypedFlowAgent(a)
		if store != nil {
			fa.checkPointStore = store
		}
		ctx = ctxWithNewTypedRunCtx(ctx, input, o.sharedParentSession)
		AddSessionValues(ctx, o.sessionValues)
		iter = fa.Run(ctx, input, opts...)
	}

	niter, gen := NewAsyncIteratorPair[*TypedAgentEvent[M]]()
	checkPointID := o.checkPointID
	if sessionState.checkPointID != nil {
		checkPointID = sessionState.checkPointID
	}
	go typedRunnerHandleIterImpl(enableStreaming, store, ctx, iter, gen, checkPointID, o.cancelCtx, exposeTimelineEvents, sessionState)
	handedOff = true
	return niter
}

func typedRunnerResumeInternalImpl[M MessageType](a TypedAgent[M], store CheckPointStore, sessionID string, sessionStore SessionEventStore[M], sessionConfig *SessionConfig[M], ctx context.Context, checkPointID string, resumeData map[string]any, //nolint:revive // argument-limit
	sessionState *runnerSessionRunState[M], opts ...AgentRunOption) (*AsyncIterator[*TypedAgentEvent[M]], error) {
	if isNilCheckPointStore(store) {
		return nil, fmt.Errorf("failed to resume: store is nil")
	}

	o := getCommonOptions(nil, opts...)
	exposeTimelineEvents := o.enableTimelineEvents
	handedOff := false
	defer func() {
		if !handedOff {
			sessionState.close(ctx)
		}
	}()
	cp, err := sessionState.prepareResume(ctx, store, sessionID, sessionStore, sessionConfig, checkPointID)
	if err != nil {
		return nil, err
	}
	var (
		resumeCtx  context.Context
		runCtx     *runContext
		resumeInfo *ResumeInfo
	)
	if cp == nil {
		resumeCtx, runCtx, resumeInfo, err = runnerLoadCheckPointImpl(store, ctx, checkPointID)
	} else {
		checkPointID = *sessionState.checkPointID
		opts = append(opts, withEnableSessionEvents(), withEnableInternalTimelineEvents())
		resumeCtx, runCtx, resumeInfo, err = runnerLoadCheckPointBytes(ctx, cp.Payload)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load from checkpoint: %w", err)
	}
	if runCtx == nil || runCtx.Session == nil {
		return nil, errors.New("failed to load from checkpoint: missing run session")
	}
	// Publish the resume request only after the checkpoint yields a usable session.
	if sessionState.enabled {
		if err := sessionState.appendResumeStartedEvent(ctx); err != nil {
			return nil, err
		}
	}
	ctx = resumeCtx

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
		ctx = contextWithSessionEventContext[M](ctx, sessionState.sessionConfig.EventIDGenerator, sessionState.sessionConfig.EventExtraProvider, sessionState.turnID)
	}

	if len(resumeData) > 0 {
		ctx = core.BatchResumeWithData(ctx, resumeData)
	}

	var iter *AsyncIterator[*TypedAgentEvent[M]]
	var zero M
	if _, ok := any(zero).(*schema.Message); ok {
		concreteAgent, _ := any(a).(Agent)
		fa := toFlowAgent(ctx, concreteAgent)
		ra, ok := any(fa).(ResumableAgent)
		if !ok {
			return nil, fmt.Errorf("agent %T does not support resume", a)
		}
		if err := sessionState.startResume(ctx); err != nil {
			return nil, err
		}
		iter = any(ra.Resume(ctx, resumeInfo, opts...)).(*AsyncIterator[*TypedAgentEvent[M]])
	} else {
		fa := toTypedFlowAgent(a)
		ra, ok := any(fa).(TypedResumableAgent[M])
		if !ok {
			return nil, fmt.Errorf("agent %T does not support resume", a)
		}
		if err := sessionState.startResume(ctx); err != nil {
			return nil, err
		}
		iter = ra.Resume(ctx, resumeInfo, opts...)
	}

	niter, gen := NewAsyncIteratorPair[*TypedAgentEvent[M]]()
	go typedRunnerHandleIterImpl(enableStreaming, store, ctx, iter, gen, &checkPointID, o.cancelCtx, exposeTimelineEvents, sessionState)
	handedOff = true
	return niter, nil
}

func typedRunnerHandleIterImpl[M MessageType](enableStreaming bool, store CheckPointStore, ctx context.Context, aIter *AsyncIterator[*TypedAgentEvent[M]], //nolint:revive,cyclop,funlen // argument-limit; event loop branches by event kind
	gen *AsyncGenerator[*TypedAgentEvent[M]], checkPointID *string, cancelCtx *cancelContext, enableTimelineEvents bool, sessionState *runnerSessionRunState[M]) {
	var finalErr error
	defer func() {
		if panicErr := recover(); panicErr != nil {
			finalErr = safe.NewPanicErr(panicErr, debug.Stack())
		}
		if finalErr != nil {
			gen.Send(&TypedAgentEvent[M]{Err: finalErr})
		}
		gen.Close()
	}()
	// Release admission before publishing the final error or closing the iterator.
	defer sessionState.close(ctx)
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
		persister = newSessionEventPersister[M](ctx, sessionState.sessionHandle)
	}
	if enableTimelineEvents && sessionState != nil && sessionState.enabled {
		for _, se := range sessionState.initialTimeline {
			if se != nil {
				gen.Send(&TypedAgentEvent[M]{SessionEventVariant: &SessionEventVariant[M]{SessionID: sessionState.sessionID, Event: se}})
			}
		}
	}
	stampOwnTurnID := func(se *SessionEvent[M]) {
		if sessionState == nil || !sessionState.enabled {
			return
		}
		stampSessionEventTurnID(se, sessionState.turnID)
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
		stampOwnTurnID(se)
		if isSessionDurableBoundaryKind(se.Kind) {
			return commitSessionBoundary(se)
		}
		return enqueueAsyncSessionEvent(se)
	}
	sendTimelineEvent := func(se *SessionEvent[M]) string {
		if se == nil {
			return ""
		}
		stampOwnTurnID(se)
		sc := sessionEventContextFromContext[M](ctx)
		sc.turnID = se.TurnID
		var err error
		se, err = prepareReceivedSessionEvent(ctx, se, sc)
		if err != nil {
			setPersistErr(err)
			return ""
		}
		if err := persistSessionEvent(se); err != nil {
			return ""
		}
		eventID := se.EventID
		if enableTimelineEvents {
			gen.Send(&TypedAgentEvent[M]{SessionEventVariant: &SessionEventVariant[M]{SessionID: sessionState.sessionID, Event: se}})
		}
		return eventID
	}
	var receivedEventContext sessionEventContext[M]
	if sessionState != nil && sessionState.enabled {
		receivedEventContext = sessionEventContext[M]{
			generator: sessionState.sessionConfig.EventIDGenerator,
			provider:  sessionState.sessionConfig.EventExtraProvider,
			turnID:    sessionState.turnID,
		}
	}
	prepareStreamRef := func(event *TypedAgentEvent[M]) (*MessageStreamRef, error) {
		var ref *MessageStreamRef
		if event.SessionEventVariant != nil {
			ref = event.SessionEventVariant.MessageStreamRef
		}
		if ref == nil {
			return reserveMessageStreamRef(ctx, newEventTimestamp(), receivedEventContext)
		}
		if ref.Timestamp.IsZero() {
			ref.Timestamp = newEventTimestamp()
		}
		ref.Kind = SessionEventMessage
		if ref.EventID == "" {
			reserved, err := reserveMessageStreamRef(ctx, ref.Timestamp, receivedEventContext)
			if err != nil {
				return nil, err
			}
			*ref = *reserved
		}
		return ref, nil
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
		if err := runnerSaveCheckPointImpl(enableStreaming, store, ctx, *checkPointID, info, sig); err != nil {
			gen.Send(&TypedAgentEvent[M]{Err: newCheckpointSaveError(errLabel, err)})
		}
	}

	parentSessionID, _ := RunnerSessionID(ctx)
	if sessionState != nil && sessionState.enabled {
		parentSessionID = sessionState.sessionID
	}
	liveOnly := persister == nil && store == nil && cancelCtx == nil && enableTimelineEvents
	for {
		event, ok := aIter.Next()
		if !ok {
			break
		}
		variant := event.SessionEventVariant
		foreignEvent := variant.isForeignTo(parentSessionID)
		sessionEventErr := prepareLiveSessionEvent(ctx, variant, receivedEventContext, foreignEvent)
		if sessionEventErr != nil {
			setPersistErr(sessionEventErr)
			event.Err = sessionEventErr
			event = stripSessionEventFields(event)
		}

		// A live-only run forwards actions and errors as produced by the agent.
		if liveOnly {
			gen.Send(event)
			continue
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
			// Skip persistence (but not live delivery) for events tagged with a
			// foreign SessionID (inner agent events forwarded via AgentTool).
			if !foreignEvent && sessionEventErr == nil {
				if event.Output != nil && event.Output.MessageOutput != nil &&
					event.Output.MessageOutput.IsStreaming && event.Output.MessageOutput.MessageStream != nil {
					ref, err := prepareStreamRef(event)
					if err != nil {
						setPersistErr(err)
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

					persistedMsg, hasChunks, streamErr, err := internalschema.ConcatMessageStreamPrefix(copies[0])
					if err != nil {
						// Prefix projection is best-effort replay data. A concat failure
						// should not fail the turn after the source stream already failed.
						continue
					}
					if streamErr != nil {
						if hasChunks {
							draft := &SessionEvent[M]{
								EventID:   ref.EventID,
								TurnID:    ref.TurnID,
								Timestamp: ref.Timestamp,
								Kind:      SessionEventMessageStreamIncomplete,
								MessageStreamIncomplete: &MessageStreamIncompleteEvent[M]{
									Message: persistedMsg,
									Error:   streamErr.Error(),
								},
							}
							if err := prepareSessionEventEnvelope(ctx, draft, sessionState.sessionConfig.EventIDGenerator, sessionState.sessionConfig.EventExtraProvider, sessionEventEnvelopeOptions{RequireEventID: true}); err != nil {
								setPersistErr(err)
								continue
							}
							sendTimelineEvent(draft)
						}
						continue
					}
					if !hasChunks {
						continue
					}

					draft := &SessionEvent[M]{
						EventID:   ref.EventID,
						TurnID:    ref.TurnID,
						Timestamp: ref.Timestamp,
						Kind:      SessionEventMessage,
						Message:   persistedMsg,
					}
					if err := prepareSessionEventEnvelope(ctx, draft, sessionState.sessionConfig.EventIDGenerator, sessionState.sessionConfig.EventExtraProvider, sessionEventEnvelopeOptions{RequireEventID: true}); err != nil {
						setPersistErr(err)
						continue
					}
					sendTimelineEvent(draft)
				} else {
					// Custom agents may emit a complete message without session metadata.
					// Prepare that message explicitly before converting it for persistence.
					if event.SessionEventVariant == nil && event.Output != nil &&
						event.Output.MessageOutput != nil && !isNilMessage(event.Output.MessageOutput.Message) {
						draft := &SessionEvent[M]{
							Kind:    SessionEventMessage,
							Message: event.Output.MessageOutput.Message,
						}
						stampOwnTurnID(draft)
						if err := prepareSessionEventEnvelope(ctx, draft, receivedEventContext.generator, receivedEventContext.provider, sessionEventEnvelopeOptions{}); err != nil {
							setPersistErr(err)
						} else {
							event.SessionEventVariant = &SessionEventVariant[M]{SessionID: sessionState.sessionID, Event: draft}
						}
					}
					se, err := toSessionEventChecked(event)
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
		stopReason := StopReasonEndTurn
		switch {
		case interrupted:
			stopReason = StopReasonInterrupted
		case cancelled:
			stopReason = StopReasonCancelled
		case retryExhausted:
			stopReason = StopReasonRetriesExhausted
		case persistErr != nil:
			stopReason = StopReasonFailed
		case terminalErr != nil:
			stopReason = StopReasonFailed
		}
		if stopReason == StopReasonFailed {
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
		idleEventID := sendTimelineEvent(&SessionEvent[M]{
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
			idleEventID:       idleEventID,
		}
		finalErr = res.finalize(ctx)
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
	idleEventID       string
}

func (r *sessionTurnResult[M]) saveCheckpoint(ctx context.Context) error {
	if isNilCheckPointStore(r.store) {
		return nil
	}
	if r.idleEventID == "" {
		return errors.New("adk: session checkpoint requires a persisted idle event")
	}
	payload, err := encodeRunnerCheckPointWithRunCtx(
		r.enableStreaming,
		sanitizeRunContextForSessionCheckpoint[M](getRunCtx(ctx)),
		r.pendingCheckpoint.info,
		r.pendingCheckpoint.signal,
	)
	if err != nil {
		return err
	}
	data, err := encodeRunnerSessionCheckpoint(&runnerSessionCheckpoint{
		SessionID:    r.sessionState.sessionID,
		CheckPointID: *r.checkPointID,
		TurnID:       r.sessionState.turnID,
		IdleEventID:  r.idleEventID,
		Payload:      payload,
	})
	if err != nil {
		return err
	}
	return r.store.Set(ctx, *r.checkPointID, data)
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
			return newSessionEventPersistenceError(
				r.pendingCheckpoint.errLabel+
					": skipped because session event persistence failed",
				r.persistErr,
			)
		}
		if err := r.saveCheckpoint(ctx); err != nil {
			return newCheckpointSaveError(r.pendingCheckpoint.errLabel, err)
		}
	}
	if r.persistErr != nil {
		return newSessionEventPersistenceError(
			"failed to persist session events",
			r.persistErr,
		)
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
