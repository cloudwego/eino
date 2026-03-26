/*
 * Copyright 2026 CloudWeGo Authors
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

package team

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/adk"
)

func newTestTurnLoop() *adk.TurnLoop[TurnInput] {
	return adk.NewTurnLoop(adk.TurnLoopConfig[TurnInput]{
		GenInput: func(ctx context.Context, loop *adk.TurnLoop[TurnInput], items []TurnInput) (*adk.GenInputResult[TurnInput], error) {
			return &adk.GenInputResult[TurnInput]{Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, loop *adk.TurnLoop[TurnInput], items []TurnInput) (adk.Agent, error) {
			return nil, errors.New("not used")
		},
	})
}

func TestNewSourceRouter(t *testing.T) {
	r := newSourceRouter("leader", nopLogger{})
	assert.NotNil(t, r)
	assert.Equal(t, "leader", r.defaultAgent)
	assert.NotNil(t, r.loops)
}

func TestSourceRouter_RegisterLoop_GetLoop(t *testing.T) {
	r := newSourceRouter("leader", nopLogger{})
	loop := newTestTurnLoop()
	r.RegisterLoop("agent-a", loop)
	assert.Same(t, loop, r.getLoop("agent-a"))
}

func TestSourceRouter_UnregisterLoop(t *testing.T) {
	r := newSourceRouter("leader", nopLogger{})
	loop := newTestTurnLoop()
	r.RegisterLoop("agent-a", loop)
	assert.Same(t, loop, r.getLoop("agent-a"))
	r.UnregisterLoop("agent-a")
	assert.Nil(t, r.getLoop("agent-a"))
}

func TestSourceRouter_Push_RegisteredAgent(t *testing.T) {
	r := newSourceRouter("leader", nopLogger{})
	loop := newTestTurnLoop()
	r.RegisterLoop("agent-a", loop)

	accepted, _ := r.Push(TurnInput{TargetAgent: "agent-a", Messages: []string{"hello"}})
	assert.True(t, accepted)
}

func TestSourceRouter_Push_UnknownAgent_FallsBackToDefault(t *testing.T) {
	r := newSourceRouter("leader", nopLogger{})
	defaultLoop := newTestTurnLoop()
	r.RegisterLoop("leader", defaultLoop)

	accepted, _ := r.Push(TurnInput{TargetAgent: "unknown-agent", Messages: []string{"hello"}})
	assert.True(t, accepted)
}

func TestSourceRouter_Push_NoLoopsRegistered(t *testing.T) {
	r := newSourceRouter("leader", nopLogger{})
	accepted, ch := r.Push(TurnInput{TargetAgent: "agent-a", Messages: []string{"hello"}})
	assert.False(t, accepted)
	assert.Nil(t, ch)
}

func TestSourceRouter_ConcurrentRegisterUnregister(t *testing.T) {
	r := newSourceRouter("leader", nopLogger{})
	const goroutines = 50

	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			name := fmt.Sprintf("agent-%d", idx)
			loop := newTestTurnLoop()
			r.RegisterLoop(name, loop)
		}(i)
	}

	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			name := fmt.Sprintf("agent-%d", idx)
			r.UnregisterLoop(name)
		}(i)
	}

	wg.Wait()
}
