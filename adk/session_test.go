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
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/schema"
)

type errorAddStore struct {
	data   map[string][][]byte
	addErr error
}

func newErrorAddStore(addErr error) *errorAddStore {
	return &errorAddStore{data: make(map[string][][]byte), addErr: addErr}
}

func (e *errorAddStore) Get(_ context.Context, sessionID string) ([][]byte, error) {
	return e.data[sessionID], nil
}

func (e *errorAddStore) Add(_ context.Context, _ string, _ [][]byte) error {
	return e.addErr
}

func (e *errorAddStore) Set(_ context.Context, sessionID string, entries [][]byte) error {
	e.data[sessionID] = entries
	return nil
}

func TestSession_Saving_FiltersAndHandlers(t *testing.T) {
	ctx := context.Background()
	store := NewInMemorySessionStore()
	sessionID := "saving_filters_handlers"

	skipHandler := func(ctx context.Context, event *AgentEvent) (*AgentEvent, error) {
		if event.Output != nil && event.Output.MessageOutput != nil {
			m := event.Output.MessageOutput.Message
			if m != nil && m.Role == schema.Assistant && m.Content == "Skip this" {
				return nil, nil
			}
		}
		return event, nil
	}

	service := NewSessionService(store, WithBeforeAddSession(skipHandler))

	mockAgent_ := newMockRunnerAgent("TestAgent", "Test agent", []*AgentEvent{
		{AgentName: "TestAgent", Output: &AgentOutput{MessageOutput: &MessageVariant{Message: schema.AssistantMessage("Skip this", nil)}}},
		{AgentName: "TestAgent", Output: &AgentOutput{MessageOutput: &MessageVariant{Message: schema.AssistantMessage("Save this", nil)}}},
		{AgentName: "TestAgent", Action: &AgentAction{Interrupted: &InterruptInfo{}}},
	})

	runner := NewRunner(ctx, RunnerConfig{Agent: mockAgent_, SessionService: service})
	iter := runner.Run(ctx, []Message{schema.UserMessage("Hello")}, WithSessionID(sessionID))

	var received []*AgentEvent
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		received = append(received, ev)
	}
	assert.Equal(t, 3, len(received))
	assert.Equal(t, "Skip this", received[0].Output.MessageOutput.Message.Content)
	assert.Equal(t, "Save this", received[1].Output.MessageOutput.Message.Content)

	storedData, _ := store.Get(ctx, sessionID)
	assert.Equal(t, 2, len(storedData))

	var savedInput, savedResp *AgentEvent
	_ = gob.NewDecoder(bytes.NewReader(storedData[0])).Decode(&savedInput)
	_ = gob.NewDecoder(bytes.NewReader(storedData[1])).Decode(&savedResp)
	assert.Equal(t, "Hello", savedInput.Output.MessageOutput.Message.Content)
	assert.Equal(t, "Save this", savedResp.Output.MessageOutput.Message.Content)
}

func TestSession_BeforeAdd_ChainAndError(t *testing.T) {
	ctx := context.Background()
	store := NewInMemorySessionStore()
	sessionID := "before_add_chain_error"

	failingHandler := func(ctx context.Context, event *AgentEvent) (*AgentEvent, error) {
		return nil, errors.New("handler failed")
	}
	successHandler := func(ctx context.Context, event *AgentEvent) (*AgentEvent, error) {
		modified := *event
		if modified.AgentName == "" {
			modified.AgentName = "Input"
		}
		modified.AgentName = "Modified_" + modified.AgentName
		return &modified, nil
	}

	service := NewSessionService(store, WithBeforeAddSession(successHandler, failingHandler))

	mockAgent_ := newMockRunnerAgent("TestAgent", "Test agent", []*AgentEvent{
		{AgentName: "TestAgent", Output: &AgentOutput{MessageOutput: &MessageVariant{Message: schema.AssistantMessage("Response", nil)}}},
	})

	runner := NewRunner(ctx, RunnerConfig{Agent: mockAgent_, SessionService: service})
	iter := runner.Run(ctx, []Message{schema.UserMessage("Hello")}, WithSessionID(sessionID))

	var received *AgentEvent
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		received = ev
	}
	if received == nil {
		t.Fatal("No event received")
	}
	assert.Equal(t, "Modified_TestAgent", received.AgentName)

	data, _ := store.Get(ctx, sessionID)
	assert.Equal(t, 2, len(data))
	var in, out *AgentEvent
	_ = gob.NewDecoder(bytes.NewReader(data[0])).Decode(&in)
	_ = gob.NewDecoder(bytes.NewReader(data[1])).Decode(&out)
	assert.Equal(t, "Modified_Input", in.AgentName)
	assert.Equal(t, "Modified_TestAgent", out.AgentName)
}

func TestSession_LoadAndSaveInput(t *testing.T) {
	ctx := context.Background()
	store := NewInMemorySessionStore()
	sessionID := "load_and_save"

	prev := &AgentEvent{Output: &AgentOutput{MessageOutput: &MessageVariant{Message: schema.UserMessage("Previous message")}}}
	buf := &bytes.Buffer{}
	_ = gob.NewEncoder(buf).Encode(prev)
	_ = store.Add(ctx, sessionID, [][]byte{buf.Bytes()})

	service := NewSessionService(store)
	mockAgent_ := newMockRunnerAgent("TestAgent", "Test agent", []*AgentEvent{
		{AgentName: "TestAgent", Output: &AgentOutput{MessageOutput: &MessageVariant{Message: schema.AssistantMessage("Response", nil)}}},
	})
	runner := NewRunner(ctx, RunnerConfig{Agent: mockAgent_, SessionService: service})

	iter := runner.Run(ctx, []Message{schema.UserMessage("New message")}, WithSessionID(sessionID))
	for {
		_, ok := iter.Next()
		if !ok {
			break
		}
	}

	data, _ := store.Get(ctx, sessionID)
	assert.Equal(t, 3, len(data))
	var h, in, out *AgentEvent
	_ = gob.NewDecoder(bytes.NewReader(data[0])).Decode(&h)
	_ = gob.NewDecoder(bytes.NewReader(data[1])).Decode(&in)
	_ = gob.NewDecoder(bytes.NewReader(data[2])).Decode(&out)
	assert.Equal(t, "Previous message", h.Output.MessageOutput.Message.Content)
	assert.Equal(t, "New message", in.Output.MessageOutput.Message.Content)
	assert.Equal(t, "Response", out.Output.MessageOutput.Message.Content)
}

func TestSession_SerializationAndStoreErrors(t *testing.T) {
	ctx := context.Background()
	store := NewInMemorySessionStore()
	sessionID := "round_trip_and_errors"

	service := NewSessionService(store)

	events := []*AgentEvent{
		{Output: &AgentOutput{MessageOutput: &MessageVariant{Message: schema.UserMessage("Hello")}}},
		{AgentName: "Agent", Output: &AgentOutput{MessageOutput: &MessageVariant{Message: schema.AssistantMessage("World", nil)}}},
	}
	err := service.persistEvents(ctx, sessionID, events)
	assert.NoError(t, err)
	loaded, err := service.load(ctx, sessionID)
	assert.NoError(t, err)
	assert.Equal(t, 2, len(loaded))
	assert.Equal(t, "Hello", loaded[0].Output.MessageOutput.Message.Content)
	assert.Equal(t, "World", loaded[1].Output.MessageOutput.Message.Content)

	badID := "bad_bytes"
	_ = store.Set(ctx, badID, [][]byte{[]byte("not-gob-data")})
	_, err = service.load(ctx, badID)
	assert.Error(t, err)

	errStore := newErrorAddStore(errors.New("store add failed"))
	s2 := NewSessionService(errStore)
	mockAgent_ := newMockRunnerAgent("TestAgent", "Test agent", []*AgentEvent{
		{AgentName: "TestAgent", Output: &AgentOutput{MessageOutput: &MessageVariant{Message: schema.AssistantMessage("Response", nil)}}},
	})
	runner := NewRunner(ctx, RunnerConfig{Agent: mockAgent_, SessionService: s2})
	iter := runner.Run(ctx, []Message{schema.UserMessage("Hi")}, WithSessionID("add-error"))
	var gotErrEvent bool
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		if ev.Err != nil {
			gotErrEvent = true
		}
	}
	assert.True(t, gotErrEvent)

	err = s2.saveInput(ctx, "sid", []Message{schema.UserMessage("Hi")})
	assert.Error(t, err)
}

func TestSessionService_AfterGet_PersistAndRun_Integration(t *testing.T) {
	ctx := context.Background()
	store := NewInMemorySessionStore()
	sessionID := "after_get_persist_integration"

	// Pre-populate store with history: user + assistant
	prevUser := &AgentEvent{Output: &AgentOutput{MessageOutput: &MessageVariant{Message: schema.UserMessage("Prev1")}}}
	prevAssistant := &AgentEvent{AgentName: "AgentA", Output: &AgentOutput{MessageOutput: &MessageVariant{Message: schema.AssistantMessage("Prev2", nil)}}}
	buf1, buf2 := &bytes.Buffer{}, &bytes.Buffer{}
	_ = gob.NewEncoder(buf1).Encode(prevUser)
	_ = gob.NewEncoder(buf2).Encode(prevAssistant)
	_ = store.Add(ctx, sessionID, [][]byte{buf1.Bytes(), buf2.Bytes()})

	// AfterGet handlers: transform and then tag
	h1 := func(ctx context.Context, events []*AgentEvent) ([]*AgentEvent, error) {
		out := make([]*AgentEvent, 0, len(events)+1)
		for _, ev := range events {
			if ev.Output != nil && ev.Output.MessageOutput != nil {
				msg := ev.Output.MessageOutput.Message
				if msg != nil && msg.Role == schema.Assistant {
					// modify assistant content
					ev2 := *ev
					ev2.Output = &AgentOutput{MessageOutput: &MessageVariant{Message: schema.AssistantMessage(msg.Content+"-Transformed", nil)}}
					out = append(out, &ev2)
					continue
				}
			}
			out = append(out, ev)
		}
		// Append summary event
		out = append(out, &AgentEvent{AgentName: "Summarizer", Output: &AgentOutput{MessageOutput: &MessageVariant{Message: schema.AssistantMessage("Summary", nil)}}})
		return out, nil
	}

	h2 := func(ctx context.Context, events []*AgentEvent) ([]*AgentEvent, error) {
		out := make([]*AgentEvent, 0, len(events))
		for _, ev := range events {
			// ensure AgentName tagged
			name := ev.AgentName
			if name == "" {
				name = "Input"
			}
			ev2 := *ev
			ev2.AgentName = "AG_" + name
			out = append(out, &ev2)
		}
		return out, nil
	}

	// BeforeAdd handler to prefix agent name on save
	ba := func(ctx context.Context, ev *AgentEvent) (*AgentEvent, error) {
		ev2 := *ev
		if ev2.AgentName == "" {
			ev2.AgentName = "Input"
		}
		ev2.AgentName = "BA_" + ev2.AgentName
		return &ev2, nil
	}

	service := NewSessionService(store, WithAfterGetSession(h1, h2), WithPersistAfterGet(), WithBeforeAddSession(ba))

	// Run with session to trigger load (AfterGet + persist) and saving input/output
	mockAgent_ := newMockRunnerAgent("TestAgent", "Test agent", []*AgentEvent{
		{AgentName: "TestAgent", Output: &AgentOutput{MessageOutput: &MessageVariant{Message: schema.AssistantMessage("Response", nil)}}},
	})
	runner := NewRunner(ctx, RunnerConfig{Agent: mockAgent_, SessionService: service})

	iter := runner.Run(ctx, []Message{schema.UserMessage("New")}, WithSessionID(sessionID))
	for {
		_, ok := iter.Next()
		if !ok {
			break
		}
	}

	// Verify store contents: transformed history persisted, plus input and response (interrupts are filtered by default)
	data, _ := store.Get(ctx, sessionID)
	// transformed history should be 3 entries (Prev1, Prev2-Transformed, Summary) then +2 new entries
	assert.Equal(t, 5, len(data))

	// Decode and check first three transformed entries
	var e0, e1, e2 *AgentEvent
	_ = gob.NewDecoder(bytes.NewReader(data[0])).Decode(&e0)
	_ = gob.NewDecoder(bytes.NewReader(data[1])).Decode(&e1)
	_ = gob.NewDecoder(bytes.NewReader(data[2])).Decode(&e2)
	// After h2, AgentName should be tagged
	assert.Equal(t, "AG_Input", e0.AgentName)
	assert.Equal(t, "AG_AgentA", e1.AgentName)
	assert.Equal(t, "AG_Summarizer", e2.AgentName)
	// content transformed
	assert.Equal(t, "Prev2-Transformed", e1.Output.MessageOutput.Message.Content)

	// Input event saved with BeforeAdd prefix
	var e3 *AgentEvent
	_ = gob.NewDecoder(bytes.NewReader(data[3])).Decode(&e3)
	assert.Equal(t, "BA_Input", e3.AgentName)
	assert.Equal(t, "New", e3.Output.MessageOutput.Message.Content)

	// Response saved with BeforeAdd prefix
	var e4 *AgentEvent
	_ = gob.NewDecoder(bytes.NewReader(data[4])).Decode(&e4)
	_ = assert.Equal(t, "BA_TestAgent", e4.AgentName)
	assert.Equal(t, "Response", e4.Output.MessageOutput.Message.Content)
}

func TestSessionService_AfterGet_ErrorAndPersist_Integration(t *testing.T) {
	ctx := context.Background()
	store := NewInMemorySessionStore()
	sessionID := "after_get_error_persist"

	// Pre-populate store
	ev := &AgentEvent{Output: &AgentOutput{MessageOutput: &MessageVariant{Message: schema.UserMessage("Seed")}}}
	buf := &bytes.Buffer{}
	_ = gob.NewEncoder(buf).Encode(ev)
	_ = store.Add(ctx, sessionID, [][]byte{buf.Bytes()})

	// First AfterGet fails, second should be skipped
	hFail := func(ctx context.Context, events []*AgentEvent) ([]*AgentEvent, error) {
		return nil, errors.New("boom")
	}
	hSkipped := func(ctx context.Context, events []*AgentEvent) ([]*AgentEvent, error) {
		return []*AgentEvent{}, nil
	}

	// BeforeAdd that skips saving assistant responses with content "SkipMe"
	baSkip := func(ctx context.Context, ev *AgentEvent) (*AgentEvent, error) {
		if ev.Output != nil && ev.Output.MessageOutput != nil {
			m := ev.Output.MessageOutput.Message
			if m != nil && m.Role == schema.Assistant && m.Content == "SkipMe" {
				return nil, nil
			}
		}
		return ev, nil
	}

	service := NewSessionService(store, WithAfterGetSession(hFail, hSkipped), WithPersistAfterGet(), WithBeforeAddSession(baSkip))

	mockAgent_ := newMockRunnerAgent("AgentB", "Test agent", []*AgentEvent{
		{AgentName: "AgentB", Output: &AgentOutput{MessageOutput: &MessageVariant{Message: schema.AssistantMessage("SkipMe", nil)}}},
		{AgentName: "AgentB", Output: &AgentOutput{MessageOutput: &MessageVariant{Message: schema.AssistantMessage("KeepMe", nil)}}},
	})

	runner := NewRunner(ctx, RunnerConfig{Agent: mockAgent_, SessionService: service})
	iter := runner.Run(ctx, []Message{schema.UserMessage("New2")}, WithSessionID(sessionID))
	for {
		_, ok := iter.Next()
		if !ok {
			break
		}
	}

	// AfterGet failed: history persisted as original (1 entry), then input (1), then only the second assistant (skipping first)
	data, _ := store.Get(ctx, sessionID)
	assert.Equal(t, 3, len(data))

	var h0, h1, h2 *AgentEvent
	_ = gob.NewDecoder(bytes.NewReader(data[0])).Decode(&h0)
	_ = gob.NewDecoder(bytes.NewReader(data[1])).Decode(&h1)
	_ = gob.NewDecoder(bytes.NewReader(data[2])).Decode(&h2)
	assert.Equal(t, "Seed", h0.Output.MessageOutput.Message.Content)
	assert.Equal(t, "New2", h1.Output.MessageOutput.Message.Content)
	assert.Equal(t, "KeepMe", h2.Output.MessageOutput.Message.Content)
}
