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
	"sync"
)

type runSession struct {
	events []*agentEventWrapper
	values map[string]any

	mtx sync.Mutex
}

type agentEventWrapper struct {
	*AgentEvent
	mu                  sync.Mutex
	concatenatedMessage Message
}

func newRunSession() *runSession {
	return &runSession{
		values: make(map[string]any),
	}
}

func GetSessionValues(ctx context.Context) map[string]any {
	session := getSession(ctx)
	if session == nil {
		return map[string]any{}
	}

	return session.getValues()
}

func SetSessionValue(ctx context.Context, key string, value any) {
	session := getSession(ctx)
	if session == nil {
		return
	}

	session.setValue(key, value)
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
	rs.events = append(rs.events, &agentEventWrapper{
		AgentEvent: event,
	})
	rs.mtx.Unlock()
}

func (rs *runSession) getEvents() []*agentEventWrapper {
	rs.mtx.Lock()
	events := rs.events
	rs.mtx.Unlock()

	return events
}

func (rs *runSession) getValues() map[string]any {
	rs.mtx.Lock()
	values := make(map[string]any, len(rs.values))
	for k, v := range rs.values {
		values[k] = v
	}
	rs.mtx.Unlock()

	return values
}

func (rs *runSession) setValue(key string, value any) {
	rs.mtx.Lock()
	rs.values[key] = value
	rs.mtx.Unlock()
}

func (rs *runSession) getValue(key string) (any, bool) {
	rs.mtx.Lock()
	value, ok := rs.values[key]
	rs.mtx.Unlock()

	return value, ok
}

type runContext struct {
	rootInput *AgentInput
	runPath   []string

	session *runSession
}

func (rc *runContext) isRoot() bool {
	return len(rc.runPath) == 1
}

func (rc *runContext) deepCopy() *runContext {
	copied := &runContext{
		rootInput: rc.rootInput,
		runPath:   make([]string, len(rc.runPath)),
		session:   rc.session,
	}

	copy(copied.runPath, rc.runPath)

	return copied
}

type runCtxKey struct{}

func initRunCtx(ctx context.Context, agentName string, input *AgentInput) (context.Context, *runContext) {
	v := ctx.Value(runCtxKey{})
	var runCtx *runContext
	if v != nil {
		runCtx = v.(*runContext).deepCopy()
	}

	if runCtx == nil {
		runCtx = &runContext{session: newRunSession()}
	}

	runCtx.runPath = append(runCtx.runPath, agentName)
	if runCtx.isRoot() {
		runCtx.rootInput = input
	}

	return context.WithValue(ctx, runCtxKey{}, runCtx), runCtx
}

func ClearRunCtx(ctx context.Context) context.Context {
	return context.WithValue(ctx, runCtxKey{}, nil)
}

func getSession(ctx context.Context) *runSession {
	v := ctx.Value(runCtxKey{})

	if v != nil {
		runCtx := v.(*runContext)
		return runCtx.session
	}

	return nil
}
