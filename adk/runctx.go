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
	"sync"

	"github.com/cloudwego/eino/schema"
)

type runSession struct {
	Events []*agentEventWrapper
	Values map[string]any

	interruptRunCtxs []*runContext // won't consider concurrency now

	mtx sync.Mutex
}

type agentEventWrapper struct {
	*AgentEvent
	mu                  sync.Mutex
	concatenatedMessage Message
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
		Values: make(map[string]any),
	}
}

func appendInterruptRunCtx(ctx context.Context, interruptRunCtx *runContext) {
	session := getSession(ctx)
	if session == nil {
		return
	}
	session.appendInterruptRunCtx(interruptRunCtx)
}

func GetSessionValues(ctx context.Context) map[string]any {
	session := getSession(ctx)
	if session == nil {
		return map[string]any{}
	}

	return session.getValues()
}

func AddSessionValue(ctx context.Context, key string, value any) {
	session := getSession(ctx)
	if session == nil {
		return
	}

	session.addValue(key, value)
}

func AddSessionValues(ctx context.Context, kvs map[string]any) {
	session := getSession(ctx)
	if session == nil {
		return
	}

	session.addValues(kvs)
}

func GetSessionValue(ctx context.Context, key string) (any, bool) {
	session := getSession(ctx)
	if session == nil {
		return nil, false
	}

	return session.getValue(key)
}

func (rs *runSession) addEvent(event *AgentEvent) {
	rs.mtx.Lock()
	rs.Events = append(rs.Events, &agentEventWrapper{
		AgentEvent: event,
	})
	rs.mtx.Unlock()
}

func (rs *runSession) getEvents() []*agentEventWrapper {
	rs.mtx.Lock()
	events := rs.Events
	rs.mtx.Unlock()

	return events
}

func (rs *runSession) appendInterruptRunCtx(runCtx *runContext) {
	rs.mtx.Lock()
	rs.interruptRunCtxs = append(rs.interruptRunCtxs, runCtx)
	rs.mtx.Unlock()
}

func (rs *runSession) getValues() map[string]any {
	rs.mtx.Lock()
	values := make(map[string]any, len(rs.Values))
	for k, v := range rs.Values {
		values[k] = v
	}
	rs.mtx.Unlock()

	return values
}

func (rs *runSession) addValue(key string, value any) {
	rs.mtx.Lock()
	rs.Values[key] = value
	rs.mtx.Unlock()
}

func (rs *runSession) addValues(kvs map[string]any) {
	rs.mtx.Lock()
	for k, v := range kvs {
		rs.Values[k] = v
	}
	rs.mtx.Unlock()
}

func (rs *runSession) getValue(key string) (any, bool) {
	rs.mtx.Lock()
	value, ok := rs.Values[key]
	rs.mtx.Unlock()

	return value, ok
}

type runContext struct {
	RootInput *AgentInput
	RunPath   []RunStep
	Addr      Address

	Session *runSession
}

func (rc *runContext) isRoot() bool {
	return len(rc.RunPath) == 1
}

func (rc *runContext) deepCopy() *runContext {
	copied := &runContext{
		RootInput: rc.RootInput,
		RunPath:   make([]RunStep, len(rc.RunPath)),
		Addr:      rc.Addr.DeepCopy(),
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

	runCtx.RunPath = append(runCtx.RunPath, RunStep{agentName})
	if runCtx.isRoot() && input != nil {
		runCtx.RootInput = input
	}

	return setRunCtx(ctx, runCtx), runCtx
}

func restoreRunPath(ctx context.Context, info *ResumeInfo) (context.Context, *runContext) {
	runCtx := getRunCtx(ctx)
	addr := runCtx.Addr
	state, ok := info.interruptStates[addr.String()]
	if !ok {
		return ctx, runCtx
	}

	runPath := state.RunPath
	runCtx = runCtx.deepCopy()
	runCtx.RunPath = runPath

	return setRunCtx(ctx, runCtx), runCtx
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
		runCtx.RunPath = append(runCtx.RunPath, RunStep{agentName})
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

func ctxWithNewRunCtx(ctx context.Context, parentAddr *Address) context.Context {
	var addr Address
	if parentAddr != nil {
		addr = *parentAddr
	}
	return setRunCtx(ctx, &runContext{Session: newRunSession(), Addr: addr})
}

func getSession(ctx context.Context) *runSession {
	runCtx := getRunCtx(ctx)
	if runCtx != nil {
		return runCtx.Session
	}

	return nil
}

func GetCurrentAddress(ctx context.Context) Address {
	runCtx := getRunCtx(ctx)
	if runCtx != nil {
		return runCtx.Addr
	}

	return nil
}
