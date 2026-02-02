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
	"fmt"
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

type otherAgentEventWrapperForEncode agentEventWrapper

func (a *agentEventWrapper) GobEncode() ([]byte, error) {
	if a.concatenatedMessage != nil && a.Output != nil && a.Output.MessageOutput != nil && a.Output.MessageOutput.IsStreaming {
		a.Output.MessageOutput.MessageStream = schema.StreamReaderFromArray([]Message{a.concatenatedMessage})
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

	Session *runSession
}

func (rc *runContext) isRoot() bool {
	return len(rc.RunPath) == 1
}

func (rc *runContext) deepCopy() *runContext {
	copied := &runContext{
		RootInput: rc.RootInput,
		RunPath:   make([]RunStep, len(rc.RunPath)),
		Session:   rc.Session,
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

func ctxWithNewRunCtx(ctx context.Context, input *AgentInput, sharedParentSession bool) context.Context {
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
	return setRunCtx(ctx, &runContext{Session: session, RootInput: input})
}

func getSession(ctx context.Context) *runSession {
	runCtx := getRunCtx(ctx)
	if runCtx != nil {
		return runCtx.Session
	}

	return nil
}
