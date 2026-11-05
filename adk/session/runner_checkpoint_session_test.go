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

package session

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

type checkpointSessionAgent struct {
	resumes int32
}

func (a *checkpointSessionAgent) Name(context.Context) string        { return "checkpoint-agent" }
func (a *checkpointSessionAgent) Description(context.Context) string { return "checkpoint test" }

func (a *checkpointSessionAgent) Run(ctx context.Context, _ *adk.AgentInput, _ ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
	iter, gen := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	go func() {
		defer gen.Close()
		gen.Send(adk.Interrupt(ctx, "continue?"))
	}()
	return iter
}

func (a *checkpointSessionAgent) Resume(context.Context, *adk.ResumeInfo, ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
	atomic.AddInt32(&a.resumes, 1)
	iter, gen := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	go func() {
		defer gen.Close()
		gen.Send(&adk.AgentEvent{AgentName: "checkpoint-agent", Output: &adk.AgentOutput{
			MessageOutput: &adk.MessageVariant{Message: schema.AssistantMessage("resumed", nil), Role: schema.Assistant},
		}})
	}()
	return iter
}

func TestRunnerResumeRejectsAnotherSessionCheckpoint(t *testing.T) {
	for _, backend := range []string{"memory", "file"} {
		t.Run(backend, func(t *testing.T) {
			testRunnerResumeRejectsAnotherSessionCheckpoint(t, backend)
		})
	}
}

func testRunnerResumeRejectsAnotherSessionCheckpoint(t *testing.T, backend string) {
	ctx := context.Background()
	store := NewInMemoryStore[*schema.Message](nil)
	var events adk.SessionEventStore[*schema.Message] = store
	if backend == "file" {
		fileStore, err := NewFileStore[*schema.Message](t.TempDir(), nil)
		require.NoError(t, err)
		events = fileStore
	}
	agent := &checkpointSessionAgent{}
	newRunner := func(id string) *adk.Runner {
		return adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent, CheckPointStore: store, SessionID: id, SessionStore: events})
	}
	a := newRunner("session-a")
	iter := a.Run(ctx, []*schema.Message{schema.UserMessage("start")})
	checkpointID := ""
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		require.NoError(t, event.Err)
		if event.Action != nil && event.Action.Interrupted != nil {
			checkpointID = event.Action.Interrupted.CheckPointID
		}
	}
	require.NotEmpty(t, checkpointID)
	before, found, err := store.Get(ctx, checkpointID)
	require.NoError(t, err)
	require.True(t, found)
	b := newRunner("session-b")
	sourceEvents, err := events.LoadEvents(ctx, "session-a", nil)
	require.NoError(t, err)
	resumed, err := b.Resume(ctx, checkpointID)
	if err == nil {
		for {
			_, ok := resumed.Next()
			if !ok {
				break
			}
		}
		_, stillStored, getErr := store.Get(ctx, checkpointID)
		require.NoError(t, getErr)
		wrongSession, loadErr := events.LoadEvents(ctx, "session-b", nil)
		require.NoError(t, loadErr)
		t.Logf("wrong-session resume: executions=%d, original checkpoint retained=%v, destination events=%d", atomic.LoadInt32(&agent.resumes), stillStored, len(wrongSession.Events))
	}
	require.Error(t, err, "a checkpoint must not resume under a different session")
	require.Zero(t, atomic.LoadInt32(&agent.resumes))
	after, found, err := store.Get(ctx, checkpointID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, before, after)
	sourceAfter, err := events.LoadEvents(ctx, "session-a", nil)
	require.NoError(t, err)
	require.Equal(t, sourceEvents, sourceAfter)
	bEvents, err := events.LoadEvents(ctx, "session-b", nil)
	require.NoError(t, err)
	require.Empty(t, bEvents.Events, "rejected resume must not append to the destination session")
	// The failed resume must release B's admission so a fresh turn can start.
	fresh := b.Run(ctx, []*schema.Message{schema.UserMessage("new turn")})
	for {
		event, ok := fresh.Next()
		if !ok {
			break
		}
		require.NoError(t, event.Err)
	}
	resumed, err = a.Resume(ctx, checkpointID)
	require.NoError(t, err)
	for {
		event, ok := resumed.Next()
		if !ok {
			break
		}
		require.NoError(t, event.Err)
	}
	require.EqualValues(t, 1, atomic.LoadInt32(&agent.resumes))
	resumed, err = b.Resume(ctx, "")
	require.NoError(t, err)
	for {
		event, ok := resumed.Next()
		if !ok {
			break
		}
		require.NoError(t, event.Err)
	}
	require.EqualValues(t, 2, atomic.LoadInt32(&agent.resumes))
}
