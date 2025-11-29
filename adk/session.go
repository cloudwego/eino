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
	"fmt"
	"log"
)

// SessionStore is the interface for persisting session history.
// Implementations should encapsulate retry logic for transient failures.
// Any error returned from these methods represents an error situation that should not be retried.
// Implementations should also handle concurrency control (e.g., locking) if needed.
type SessionStore interface {
	// Get retrieves the session history from the store.
	Get(ctx context.Context, sessionID string) ([][]byte, error)
	// Add appends new entries to the session history.
	Add(ctx context.Context, sessionID string, entries [][]byte) error
	// Set overwrites the session history with the given entries.
	Set(ctx context.Context, sessionID string, entries [][]byte) error
}

// AfterGetSessionHandler processes loaded events in batch (for summarization, compaction).
// After processing, the resulting events are injected into the agent's run context.
type AfterGetSessionHandler func(ctx context.Context, events []*AgentEvent) ([]*AgentEvent, error)

// BeforeAddSessionHandler processes individual events before saving.
// It applies to events from both saveOutput (agent execution) and saveInput (user input).
// Returning nil means the event should not be saved.
type BeforeAddSessionHandler func(ctx context.Context, event *AgentEvent) (*AgentEvent, error)

// SessionService manages session lifecycle, including loading, saving, and hooks.
type SessionService struct {
	store           SessionStore
	afterGet        []AfterGetSessionHandler
	beforeAdd       []BeforeAddSessionHandler
	persistAfterGet bool
	s               *gobSerializer
}

// SessionServiceOption configures a SessionService.
type SessionServiceOption func(*SessionService)

// WithAfterGetSession adds handlers that run after loading session data.
func WithAfterGetSession(handlers ...AfterGetSessionHandler) SessionServiceOption {
	return func(s *SessionService) {
		s.afterGet = append(s.afterGet, handlers...)
	}
}

// WithBeforeAddSession adds handlers that run before saving session data.
// These handlers are applied to both saveOutput and saveInput operations.
func WithBeforeAddSession(handlers ...BeforeAddSessionHandler) SessionServiceOption {
	return func(s *SessionService) {
		s.beforeAdd = append(s.beforeAdd, handlers...)
	}
}

// WithPersistAfterGet enables automatic persistence of modified events after AfterGetSession handlers.
func WithPersistAfterGet() SessionServiceOption {
	return func(s *SessionService) {
		s.persistAfterGet = true
	}
}

// NewSessionService creates a new SessionService with the given store and options.
func NewSessionService(store SessionStore, opts ...SessionServiceOption) *SessionService {
	s := &SessionService{
		store: store,
		s:     &gobSerializer{},
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// marshalEvents serializes a batch of events using the gob serializer.
// The label tailors error messages to the caller context (e.g., "event", "input event").
func (s *SessionService) marshalEvents(events []*AgentEvent, label string) ([][]byte, error) {
	if len(events) == 0 {
		return nil, nil
	}
	entries := make([][]byte, 0, len(events))
	for _, event := range events {
		b, err := s.s.Marshal(event)
		if err != nil {
			return nil, fmt.Errorf("failed to serialize %s: %w", label, err)
		}
		entries = append(entries, b)
	}
	return entries, nil
}

// unmarshalEntries deserializes a batch of entries into events.
func (s *SessionService) unmarshalEntries(entries [][]byte) ([]*AgentEvent, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	events := make([]*AgentEvent, 0, len(entries))
	for _, entry := range entries {
		var event AgentEvent
		if err := s.s.Unmarshal(entry, &event); err != nil {
			return nil, fmt.Errorf("failed to deserialize session entry: %w", err)
		}
		events = append(events, &event)
	}
	return events, nil
}

// applyBeforeAdd runs the beforeAdd handler chain with consistent logging.
// Returns the transformed event; nil means "don't save".
func (s *SessionService) applyBeforeAdd(ctx context.Context, event *AgentEvent, op string) *AgentEvent {
	current := event
	for i, handler := range s.beforeAdd {
		transformed, err := handler(ctx, current)
		if err != nil {
			if op == "input event" {
				log.Printf("[WARN] BeforeAddSession handler[%d] failed for input event: %v, using last successful version", i, err)
			} else {
				log.Printf("[WARN] BeforeAddSession handler[%d] failed for event: %v, using last successful version", i, err)
			}
			break
		}
		if transformed == nil {
			current = nil
			break
		}
		current = transformed
	}
	return current
}

// load retrieves agent events from the session store.
// CRITICAL errors (store.Get, deserialization) abort and return error.
// NON-CRITICAL errors (handlers, persistAfterGet) log warnings and continue.
func (s *SessionService) load(ctx context.Context, sessionID string) ([]*AgentEvent, error) {
	// 1. load from store (CRITICAL)
	data, err := s.store.Get(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to load session: %w", err)
	}

	// 2. Deserialize (CRITICAL)
	events, err := s.unmarshalEntries(data)
	if err != nil {
		return nil, err
	}

	// 3. Apply AfterGetSession handlers (NON-CRITICAL)
	for i, handler := range s.afterGet {
		modifiedEvents, err := handler(ctx, events)
		if err != nil {
			log.Printf("[WARN] AfterGetSession handler[%d] failed: %v, skipping remaining handlers", i, err)
			break
		}
		events = modifiedEvents
	}

	// 4. PersistAfterGet if enabled (NON-CRITICAL)
	if s.persistAfterGet {
		if err := s.persistEvents(ctx, sessionID, events); err != nil {
			log.Printf("[WARN] PersistAfterGet failed: %v", err)
		}
	}

	return events, nil
}

// saveOutput consumes events from an iterator, applies handlers, and saves to session.
// Emits transformed events if persisted, original events if not persisted.
// Critical errors (serialization, store.Add) are sent as error events to the generator.
// Non-critical errors (handlers) log warnings and continue.
func (s *SessionService) saveOutput(
	ctx context.Context,
	sessionID string,
	iter *AsyncIterator[*AgentEvent],
	gen *AsyncGenerator[*AgentEvent],
) {
	var toSave []*AgentEvent

	// 1. Consume iterator and apply handlers
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}

		// Built-in filter: skip interrupt events (temporary state, not persisted)
		if event.Action != nil && event.Action.Interrupted != nil {
			gen.Send(event) // Emit original interrupt
			continue        // Don't save
		}

		// Apply BeforeAddSession handlers with chaining
		current := s.applyBeforeAdd(ctx, event, "event")

		if current != nil {
			gen.Send(current) // Emit transformed
			toSave = append(toSave, current)
		} else {
			gen.Send(event) // Emit original (not saved)
		}
	}

	// 2. Serialize (CRITICAL)
	entries, err := s.marshalEvents(toSave, "event")
	if err != nil {
		gen.Send(&AgentEvent{Err: err})
		return
	}

	// 3. Add to store (CRITICAL)
	if len(entries) > 0 {
		if err = s.store.Add(ctx, sessionID, entries); err != nil {
			errorEvent := &AgentEvent{Err: fmt.Errorf("failed to save session: %w", err)}
			gen.Send(errorEvent)
		}
	}
}

// saveInput persists input messages to the session store.
// Messages are converted to AgentEvents, processed by handlers, and saved.
// CRITICAL errors (serialization, store.Add) return error.
// NON-CRITICAL errors (handlers) log warnings and continue.
func (s *SessionService) saveInput(ctx context.Context, sessionID string, input []Message) error {
	var toSave []*AgentEvent

	for _, msg := range input {
		event := &AgentEvent{
			Output: &AgentOutput{
				MessageOutput: &MessageVariant{
					Message: msg,
				},
			},
		}

		// Apply BeforeAddSession handlers
		current := s.applyBeforeAdd(ctx, event, "input event")

		if current != nil {
			toSave = append(toSave, current)
		}
	}

	if len(toSave) == 0 {
		return nil
	}

	// Serialize (CRITICAL)
	entries, err := s.marshalEvents(toSave, "input event")
	if err != nil {
		return err
	}

	// Add to store (CRITICAL)
	if err = s.store.Add(ctx, sessionID, entries); err != nil {
		return fmt.Errorf("failed to save input to session: %w", err)
	}

	return nil
}

// persistEvents serializes and persists events to the store (used by PersistAfterGet).
func (s *SessionService) persistEvents(ctx context.Context, sessionID string, events []*AgentEvent) error {
	entries, err := s.marshalEvents(events, "event")
	if err != nil {
		return err
	}
	return s.store.Set(ctx, sessionID, entries)
}
