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
	"io"
	"sort"
	"sync"
	"time"

	"github.com/cloudwego/eino/schema"
)

// runSession CheckpointSchema: persisted via serialization.RunCtx (gob).
type runSession struct {
	Values    map[string]any
	valuesMtx *sync.Mutex

	Events     []*agentEventWrapper
	LaneEvents *laneEvents
	mtx        sync.Mutex

	TypedEvents     any
	TypedLaneEvents any
}

// laneEvents CheckpointSchema: persisted via serialization.RunCtx (gob).
type laneEvents struct {
	Events []*agentEventWrapper
	Parent *laneEvents
}

// agentEventWrapper CheckpointSchema: persisted via serialization.RunCtx (gob).
type agentEventWrapper struct {
	*AgentEvent
	mu                  sync.Mutex
	concatenatedMessage Message
	// TS is the timestamp (in nanoseconds) when this event was created.
	// It is primarily used by the laneEvents mechanism to order events
	// from different agents in a multi-agent flow.
	TS int64
	// StreamErr stores the error message if the MessageStream contained an error.
	// This field guards against multiple calls to getMessageFromWrappedEvent
	// when the stream has already been consumed and errored.
	// Normally when StreamErr happens, the Agent will return with the error,
	// unless retry is configured for the agent generating this stream, in which case
	// this StreamErr will be of type WillRetryError (indicating retry is pending).
	StreamErr error
}

type typedAgentEventWrapper[M MessageType] struct {
	event               *TypedAgentEvent[M]
	mu                  sync.Mutex
	concatenatedMessage M
	TS                  int64
	StreamErr           error
}

type typedLaneEventsOf[M MessageType] struct {
	Events []*typedAgentEventWrapper[M]
	Parent *typedLaneEventsOf[M]
}

func (g *typedLaneEventsOf[M]) len() int { return len(g.Events) }

// typedAgentEventWrapperForGob is a gob-serializable representation of typedAgentEventWrapper.
// We encode the event and TS separately to avoid the sync.Mutex and non-exported fields.
type typedAgentEventWrapperForGob[M MessageType] struct {
	Event *TypedAgentEvent[M]
	TS    int64
}

func (e *typedAgentEventWrapper[M]) GobEncode() ([]byte, error) {
	if e.event != nil && e.event.Output != nil && e.event.Output.MessageOutput != nil && e.event.Output.MessageOutput.IsStreaming {
		// Materialize the stream before encoding.
		var zero M
		if any(e.concatenatedMessage) == any(zero) && e.StreamErr == nil {
			e.consumeStream()
		}
	}

	buf := &bytes.Buffer{}
	err := gob.NewEncoder(buf).Encode(&typedAgentEventWrapperForGob[M]{
		Event: e.event,
		TS:    e.TS,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to gob encode generic agent event wrapper: %w", err)
	}
	return buf.Bytes(), nil
}

func (e *typedAgentEventWrapper[M]) GobDecode(b []byte) error {
	g := &typedAgentEventWrapperForGob[M]{}
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(g); err != nil {
		return fmt.Errorf("failed to gob decode generic agent event wrapper: %w", err)
	}
	e.event = g.Event
	e.TS = g.TS
	return nil
}

func (e *typedAgentEventWrapper[M]) consumeStream() {
	e.mu.Lock()
	defer e.mu.Unlock()

	var zero M
	if any(e.concatenatedMessage) != any(zero) {
		return
	}

	s := e.event.Output.MessageOutput.MessageStream
	var msgs []M

	defer s.Close()
	for {
		msg, err := s.Recv()
		if err != nil {
			if err == io.EOF {
				break
			}
			e.StreamErr = err
			e.event.Output.MessageOutput.MessageStream = schema.StreamReaderFromArray(msgs)
			return
		}
		msgs = append(msgs, msg)
	}

	if len(msgs) == 0 {
		e.StreamErr = errors.New("no messages in typedAgentEventWrapper.MessageStream")
		e.event.Output.MessageOutput.MessageStream = schema.StreamReaderFromArray(msgs)
		return
	}

	if len(msgs) == 1 {
		e.concatenatedMessage = msgs[0]
	} else {
		var err error
		e.concatenatedMessage, err = concatMessageStream(schema.StreamReaderFromArray(msgs))
		if err != nil {
			e.StreamErr = err
			e.event.Output.MessageOutput.MessageStream = schema.StreamReaderFromArray(msgs)
			return
		}
	}

	e.event.Output.MessageOutput.MessageStream = schema.StreamReaderFromArray([]M{e.concatenatedMessage})
}

type otherAgentEventWrapperForEncode agentEventWrapper

func (a *agentEventWrapper) GobEncode() ([]byte, error) {
	if a.Output != nil && a.Output.MessageOutput != nil && a.Output.MessageOutput.IsStreaming {
		// Materialize the stream before encoding. An unconsumed stream that
		// ends with a non-EOF error (WillRetryError, ErrStreamCanceled) would
		// cause MessageVariant.GobEncode to fail. consumeStream replaces the
		// stream with an error-free, materialized version.
		if a.concatenatedMessage == nil && a.StreamErr == nil {
			a.consumeStream()
		}
	}

	buf := &bytes.Buffer{}
	err := gob.NewEncoder(buf).Encode((*otherAgentEventWrapperForEncode)(a))
	if err != nil {
		return nil, fmt.Errorf("failed to gob encode agent event wrapper: %w", err)
	}
	return buf.Bytes(), nil
}

func (a *agentEventWrapper) GobDecode(b []byte) error {
	return gob.NewDecoder(bytes.NewReader(b)).Decode((*otherAgentEventWrapperForEncode)(a))
}

func newRunSession() *runSession {
	return &runSession{
		Values:    make(map[string]any),
		valuesMtx: &sync.Mutex{},
	}
}

// GetSessionValues returns all session key-value pairs for the current run.
func GetSessionValues(ctx context.Context) map[string]any {
	session := getSession(ctx)
	if session == nil {
		return map[string]any{}
	}

	return session.getValues()
}

// AddSessionValue sets a single session key-value pair for the current run.
func AddSessionValue(ctx context.Context, key string, value any) {
	session := getSession(ctx)
	if session == nil {
		return
	}

	session.addValue(key, value)
}

// AddSessionValues sets multiple session key-value pairs for the current run.
func AddSessionValues(ctx context.Context, kvs map[string]any) {
	session := getSession(ctx)
	if session == nil {
		return
	}

	session.addValues(kvs)
}

// GetSessionValue retrieves a session value by key and reports whether it exists.
func GetSessionValue(ctx context.Context, key string) (any, bool) {
	session := getSession(ctx)
	if session == nil {
		return nil, false
	}

	return session.getValue(key)
}

func (rs *runSession) addEvent(event *AgentEvent) {
	wrapper := &agentEventWrapper{AgentEvent: event, TS: time.Now().UnixNano()}
	// If LaneEvents is not nil, we are in a parallel lane.
	// Append to the lane's local event slice (lock-free).
	if rs.LaneEvents != nil {
		rs.LaneEvents.Events = append(rs.LaneEvents.Events, wrapper)
		return
	}

	// Otherwise, we are on the main path. Append to the shared Events slice (with lock).
	rs.mtx.Lock()
	rs.Events = append(rs.Events, wrapper)
	rs.mtx.Unlock()
}

func (rs *runSession) getEvents() []*agentEventWrapper {
	// If there are no in-flight lane events, we can return the main slice directly.
	if rs.LaneEvents == nil {
		rs.mtx.Lock()
		events := rs.Events
		rs.mtx.Unlock()
		return events
	}

	// If there are in-flight events, we must construct the full view.
	// First, get the committed history from the main Events slice.
	rs.mtx.Lock()
	committedEvents := make([]*agentEventWrapper, len(rs.Events))
	copy(committedEvents, rs.Events)
	rs.mtx.Unlock()

	// Then, assemble the in-flight events by traversing the linked list.
	// Reading the .Parent pointer is safe without a lock because the parent of a lane is immutable after creation.
	var laneSlices [][]*agentEventWrapper
	totalLaneSize := 0
	for lane := rs.LaneEvents; lane != nil; lane = lane.Parent {
		if len(lane.Events) > 0 {
			laneSlices = append(laneSlices, lane.Events)
			totalLaneSize += len(lane.Events)
		}
	}

	// Combine committed and in-flight history.
	finalEvents := make([]*agentEventWrapper, 0, len(committedEvents)+totalLaneSize)
	finalEvents = append(finalEvents, committedEvents...)
	for i := len(laneSlices) - 1; i >= 0; i-- {
		finalEvents = append(finalEvents, laneSlices[i]...)
	}

	return finalEvents
}

func addTypedEvent[M MessageType](session *runSession, event *TypedAgentEvent[M]) {
	var zero M
	if _, ok := any(zero).(*schema.Message); ok {
		session.addEvent(any(event).(*AgentEvent))
		return
	}
	wrapper := &typedAgentEventWrapper[M]{event: event, TS: time.Now().UnixNano()}
	if laneStore, ok := session.TypedLaneEvents.(*typedLaneEventsOf[M]); ok && laneStore != nil {
		laneStore.Events = append(laneStore.Events, wrapper)
		return
	}
	store, _ := session.TypedEvents.(*[]*typedAgentEventWrapper[M])
	if store == nil {
		s := make([]*typedAgentEventWrapper[M], 0)
		store = &s
		session.TypedEvents = store
	}
	session.mtx.Lock()
	*store = append(*store, wrapper)
	session.mtx.Unlock()
}

func getTypedEvents[M MessageType](session *runSession) []*typedAgentEventWrapper[M] {
	var zero M
	if _, ok := any(zero).(*schema.Message); ok {
		events := session.getEvents()
		result := make([]*typedAgentEventWrapper[M], 0, len(events))
		for _, e := range events {
			w := &typedAgentEventWrapper[M]{
				event:     any(e.AgentEvent).(*TypedAgentEvent[M]),
				TS:        e.TS,
				StreamErr: e.StreamErr,
			}
			if e.concatenatedMessage != nil {
				w.concatenatedMessage = any(e.concatenatedMessage).(M)
			}
			result = append(result, w)
		}
		return result
	}

	store, _ := session.TypedEvents.(*[]*typedAgentEventWrapper[M])

	if session.TypedLaneEvents == nil {
		if store == nil {
			return nil
		}
		session.mtx.Lock()
		result := make([]*typedAgentEventWrapper[M], len(*store))
		copy(result, *store)
		session.mtx.Unlock()
		return result
	}

	var committed []*typedAgentEventWrapper[M]
	if store != nil {
		session.mtx.Lock()
		committed = make([]*typedAgentEventWrapper[M], len(*store))
		copy(committed, *store)
		session.mtx.Unlock()
	}

	var laneSlices [][]*typedAgentEventWrapper[M]
	totalLaneSize := 0
	for lane, ok := session.TypedLaneEvents.(*typedLaneEventsOf[M]); ok && lane != nil; lane, ok = any(lane.Parent).(*typedLaneEventsOf[M]) {
		if len(lane.Events) > 0 {
			laneSlices = append(laneSlices, lane.Events)
			totalLaneSize += len(lane.Events)
		}
		if lane.Parent == nil {
			break
		}
	}

	result := make([]*typedAgentEventWrapper[M], 0, len(committed)+totalLaneSize)
	result = append(result, committed...)
	for i := len(laneSlices) - 1; i >= 0; i-- {
		result = append(result, laneSlices[i]...)
	}
	return result
}

func (rs *runSession) getValues() map[string]any {
	rs.valuesMtx.Lock()
	values := make(map[string]any, len(rs.Values))
	for k, v := range rs.Values {
		values[k] = v
	}
	rs.valuesMtx.Unlock()

	return values
}

func (rs *runSession) addValue(key string, value any) {
	rs.valuesMtx.Lock()
	rs.Values[key] = value
	rs.valuesMtx.Unlock()
}

func (rs *runSession) addValues(kvs map[string]any) {
	rs.valuesMtx.Lock()
	for k, v := range kvs {
		rs.Values[k] = v
	}
	rs.valuesMtx.Unlock()
}

func (rs *runSession) getValue(key string) (any, bool) {
	rs.valuesMtx.Lock()
	value, ok := rs.Values[key]
	rs.valuesMtx.Unlock()

	return value, ok
}

type runContext struct {
	RootInput *AgentInput
	RunPath   []RunStep

	TypedRootInput any

	Session *runSession
}

func (rc *runContext) isRoot() bool {
	return len(rc.RunPath) == 1
}

func (rc *runContext) deepCopy() *runContext {
	copied := &runContext{
		RootInput:      rc.RootInput,
		TypedRootInput: rc.TypedRootInput,
		RunPath:        make([]RunStep, len(rc.RunPath)),
		Session:        rc.Session,
	}

	copy(copied.RunPath, rc.RunPath)

	return copied
}

type runCtxKey struct{}

func getRunCtx(ctx context.Context) *runContext {
	runCtx, ok := ctx.Value(runCtxKey{}).(*runContext)
	if !ok {
		return nil
	}
	return runCtx
}

func setRunCtx(ctx context.Context, runCtx *runContext) context.Context {
	return context.WithValue(ctx, runCtxKey{}, runCtx)
}

func initRunCtx(ctx context.Context, agentName string, input *AgentInput) (context.Context, *runContext) {
	runCtx := getRunCtx(ctx)
	if runCtx != nil {
		runCtx = runCtx.deepCopy()
	} else {
		runCtx = &runContext{Session: newRunSession()}
	}

	runCtx.RunPath = append(runCtx.RunPath, RunStep{agentName: agentName})
	if runCtx.isRoot() && input != nil {
		runCtx.RootInput = input
	}

	return setRunCtx(ctx, runCtx), runCtx
}

func initTypedRunCtx[M MessageType](ctx context.Context, agentName string, input *TypedAgentInput[M]) (context.Context, *runContext) {
	runCtx := getRunCtx(ctx)
	if runCtx != nil {
		runCtx = runCtx.deepCopy()
	} else {
		runCtx = &runContext{Session: newRunSession()}
	}

	runCtx.RunPath = append(runCtx.RunPath, RunStep{agentName: agentName})
	if runCtx.isRoot() && input != nil {
		var zero M
		if _, ok := any(zero).(*schema.Message); ok {
			runCtx.RootInput = any(input).(*AgentInput)
		} else {
			runCtx.TypedRootInput = input
		}
	}

	return setRunCtx(ctx, runCtx), runCtx
}

func joinRunCtxs(parentCtx context.Context, childCtxs ...context.Context) {
	switch len(childCtxs) {
	case 0:
		return
	case 1:
		// Optimization for the common case of a single branch.
		newEvents := unwindLaneEvents(childCtxs...)
		commitEvents(parentCtx, newEvents)
		return
	}

	// 1. Collect all new events from the leaf nodes of each context's lane.
	newEvents := unwindLaneEvents(childCtxs...)

	// 2. Sort the collected events by their creation timestamp for chronological order.
	sort.Slice(newEvents, func(i, j int) bool {
		return newEvents[i].TS < newEvents[j].TS
	})

	// 3. Commit the sorted events to the parent.
	commitEvents(parentCtx, newEvents)
}

func joinTypedRunCtxs[M MessageType](parentCtx context.Context, childCtxs ...context.Context) {
	var zero M
	if _, ok := any(zero).(*schema.Message); ok {
		joinRunCtxs(parentCtx, childCtxs...)
		return
	}
	switch len(childCtxs) {
	case 0:
		return
	case 1:
		newEvents := unwindLaneEvents(childCtxs...)
		commitEvents(parentCtx, newEvents)
		newTypedEvents := unwindTypedLaneEvents[M](childCtxs...)
		commitTypedEvents[M](parentCtx, newTypedEvents)
		return
	}
	newEvents := unwindLaneEvents(childCtxs...)
	sort.Slice(newEvents, func(i, j int) bool {
		return newEvents[i].TS < newEvents[j].TS
	})
	commitEvents(parentCtx, newEvents)

	newTypedEvents := unwindTypedLaneEvents[M](childCtxs...)
	sort.Slice(newTypedEvents, func(i, j int) bool {
		return newTypedEvents[i].TS < newTypedEvents[j].TS
	})
	commitTypedEvents[M](parentCtx, newTypedEvents)
}

func unwindTypedLaneEvents[M MessageType](ctxs ...context.Context) []*typedAgentEventWrapper[M] {
	var allNewEvents []*typedAgentEventWrapper[M]
	for _, ctx := range ctxs {
		runCtx := getRunCtx(ctx)
		if runCtx != nil && runCtx.Session != nil {
			if gl, ok := runCtx.Session.TypedLaneEvents.(*typedLaneEventsOf[M]); ok && gl != nil {
				allNewEvents = append(allNewEvents, gl.Events...)
			}
		}
	}
	return allNewEvents
}

func commitTypedEvents[M MessageType](ctx context.Context, newEvents []*typedAgentEventWrapper[M]) {
	if len(newEvents) == 0 {
		return
	}
	runCtx := getRunCtx(ctx)
	if runCtx == nil || runCtx.Session == nil {
		return
	}
	if gl, ok := runCtx.Session.TypedLaneEvents.(*typedLaneEventsOf[M]); ok && gl != nil {
		gl.Events = append(gl.Events, newEvents...)
	} else {
		store, _ := runCtx.Session.TypedEvents.(*[]*typedAgentEventWrapper[M])
		if store == nil {
			s := make([]*typedAgentEventWrapper[M], 0)
			store = &s
			runCtx.Session.TypedEvents = store
		}
		runCtx.Session.mtx.Lock()
		*store = append(*store, newEvents...)
		runCtx.Session.mtx.Unlock()
	}
}

// commitEvents appends a slice of new events to the correct parent lane or main event log.
func commitEvents(ctx context.Context, newEvents []*agentEventWrapper) {
	runCtx := getRunCtx(ctx)
	if runCtx == nil || runCtx.Session == nil {
		// Should not happen, but handle defensively.
		return
	}

	// If the context we are committing to is itself a lane, append to its event slice.
	if runCtx.Session.LaneEvents != nil {
		runCtx.Session.LaneEvents.Events = append(runCtx.Session.LaneEvents.Events, newEvents...)
	} else {
		// Otherwise, commit to the main, shared Events slice with a lock.
		runCtx.Session.mtx.Lock()
		runCtx.Session.Events = append(runCtx.Session.Events, newEvents...)
		runCtx.Session.mtx.Unlock()
	}
}

// unwindLaneEvents traverses the LaneEvents of the given contexts and collects
// all events from the leaf nodes.
func unwindLaneEvents(ctxs ...context.Context) []*agentEventWrapper {
	var allNewEvents []*agentEventWrapper
	for _, ctx := range ctxs {
		runCtx := getRunCtx(ctx)
		if runCtx != nil && runCtx.Session != nil && runCtx.Session.LaneEvents != nil {
			allNewEvents = append(allNewEvents, runCtx.Session.LaneEvents.Events...)
		}
	}
	return allNewEvents
}

func forkRunCtx(ctx context.Context) context.Context {
	parentRunCtx := getRunCtx(ctx)
	if parentRunCtx == nil || parentRunCtx.Session == nil {
		// Should not happen in a parallel workflow, but handle defensively.
		return ctx
	}

	// Create a new session for the child lane by manually copying the parent's session fields.
	// This is crucial to ensure a new mutex is created and that the LaneEvents pointer is unique.
	childSession := &runSession{
		Events:    parentRunCtx.Session.Events, // Share the committed history
		Values:    parentRunCtx.Session.Values, // Share the values map
		valuesMtx: parentRunCtx.Session.valuesMtx,
	}

	// Fork the lane events within the new session struct.
	childSession.LaneEvents = &laneEvents{
		Parent: parentRunCtx.Session.LaneEvents,
		Events: make([]*agentEventWrapper, 0),
	}

	// Create a new runContext for the child lane, pointing to the new session.
	childRunCtx := &runContext{
		RootInput: parentRunCtx.RootInput,
		RunPath:   make([]RunStep, len(parentRunCtx.RunPath)),
		Session:   childSession,
	}
	copy(childRunCtx.RunPath, parentRunCtx.RunPath)

	return setRunCtx(ctx, childRunCtx)
}

func forkTypedRunCtx[M MessageType](ctx context.Context) context.Context {
	var zero M
	if _, ok := any(zero).(*schema.Message); ok {
		return forkRunCtx(ctx)
	}
	parentRunCtx := getRunCtx(ctx)
	if parentRunCtx == nil || parentRunCtx.Session == nil {
		return ctx
	}
	childSession := &runSession{
		Events:      parentRunCtx.Session.Events,
		TypedEvents: parentRunCtx.Session.TypedEvents,
		Values:      parentRunCtx.Session.Values,
		valuesMtx:   parentRunCtx.Session.valuesMtx,
	}
	childSession.LaneEvents = &laneEvents{
		Parent: parentRunCtx.Session.LaneEvents,
		Events: make([]*agentEventWrapper, 0),
	}
	var parentTypedLane *typedLaneEventsOf[M]
	if gl, ok := parentRunCtx.Session.TypedLaneEvents.(*typedLaneEventsOf[M]); ok {
		parentTypedLane = gl
	}
	childSession.TypedLaneEvents = &typedLaneEventsOf[M]{
		Parent: parentTypedLane,
		Events: make([]*typedAgentEventWrapper[M], 0),
	}
	childRunCtx := &runContext{
		RootInput:      parentRunCtx.RootInput,
		TypedRootInput: parentRunCtx.TypedRootInput,
		RunPath:        make([]RunStep, len(parentRunCtx.RunPath)),
		Session:        childSession,
	}
	copy(childRunCtx.RunPath, parentRunCtx.RunPath)
	return setRunCtx(ctx, childRunCtx)
}

// updateRunPathOnly creates a new context with an updated RunPath, but does NOT modify the Address.
// This is used by sequential workflows to accumulate execution history for LLM context,
// without incorrectly chaining the static addresses of peer agents.
func updateRunPathOnly(ctx context.Context, agentNames ...string) context.Context {
	runCtx := getRunCtx(ctx)
	if runCtx == nil {
		// This should not happen in a sequential workflow context, but handle defensively.
		runCtx = &runContext{Session: newRunSession()}
	} else {
		runCtx = runCtx.deepCopy()
	}

	for _, agentName := range agentNames {
		runCtx.RunPath = append(runCtx.RunPath, RunStep{agentName: agentName})
	}

	return setRunCtx(ctx, runCtx)
}

// ClearRunCtx clears the run context of the multi-agents. This is particularly useful
// when a customized agent with a multi-agents inside it is set as a subagent of another
// multi-agents. In such cases, it's not expected to pass the outside run context to the
// inside multi-agents, so this function helps isolate the contexts properly.
func ClearRunCtx(ctx context.Context) context.Context {
	return context.WithValue(ctx, runCtxKey{}, nil)
}

func ctxWithNewTypedRunCtx[M MessageType](ctx context.Context, input *TypedAgentInput[M], sharedParentSession bool) context.Context {
	var session *runSession
	if sharedParentSession {
		if parentSession := getSession(ctx); parentSession != nil {
			session = &runSession{
				Values:    parentSession.Values,
				valuesMtx: parentSession.valuesMtx,
			}
		}
	}
	if session == nil {
		session = newRunSession()
	}
	var zero M
	rc := &runContext{Session: session}
	if _, ok := any(zero).(*schema.Message); ok {
		rc.RootInput = any(input).(*AgentInput)
	} else {
		rc.TypedRootInput = input
	}
	return setRunCtx(ctx, rc)
}

func getSession(ctx context.Context) *runSession {
	runCtx := getRunCtx(ctx)
	if runCtx != nil {
		return runCtx.Session
	}

	return nil
}
