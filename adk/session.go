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

// Package adk provides Agent Development Kit primitives for building multi-turn agents.
//
// Session vs Checkpoint vs Memory:
// - Session: durable conversation history across runs. Loaded during Run, saved for inputs/outputs.
// - Checkpoint: per-run snapshots on interruption, used only for Resume flows.
// - Memory: long-term context injection/recall, orthogonal and application-specific.
//
// Using SessionService:
//  1) Create a SessionStore (e.g., InMemorySessionStore for tests or a production store).
//  2) Create SessionService with optional handlers:
//     - AfterGetSessionHandler: batch transforms on loaded history (summarize/compact).
//     - BeforeAddSessionHandler: per-event filter/transform before saving; return nil to skip.
//  3) Pass SessionService in RunnerConfig and run with WithSessionID("<id>").
//
// Handlers examples:
//  // Summarize assistant messages after load
//  h1 := func(ctx context.Context, evs []*AgentEvent) ([]*AgentEvent, error) { /* modify evs */ return evs, nil }
//  // Skip saving specific assistant replies
//  h2 := func(ctx context.Context, ev *AgentEvent) (*AgentEvent, error) { /* return nil to skip */ return ev, nil }
//
// Retry Policy Guidance:
// Implementations of SessionStore should handle transient errors internally with retry/backoff.
// For example, a production store might wrap Add/Set with exponential backoff on network timeouts.
// Pseudocode:
//   for attempt := 0; attempt < max; attempt++ { err = store.Add(...); if transient(err) { sleep(backoff(attempt)); continue } break }
// Errors returned from SessionStore methods indicate cases that can't be retried or exhausted cases and are treated as CRITICAL.

import (
	"bytes"
	"context"
	"encoding/gob"
	"fmt"
	"log"
	"sync"
)

// StoreOption allows passing implementation-specific filters and metadata to SessionStore.
// Different store implementations can define their own option types and process them accordingly.
// This follows the same pattern as AgentRunOption for maximum flexibility.
//
// Example Usage:
//
//	// InMemorySessionStore defines its own options
//	type InMemoryStoreOptions struct {
//	    Limit    int
//	    Offset   int
//	    RoundID  string
//	}
//
//	// Helper functions create options using WrapStoreImplSpecificOptFn
//	func WithLimit(n int) StoreOption {
//	    return WrapStoreImplSpecificOptFn(func(o *InMemoryStoreOptions) {
//	        o.Limit = n
//	    })
//	}
//
//	// Store implementation extracts options using GetStoreImplSpecificOptions
//	func (s *InMemorySessionStore) Get(ctx, id string, opts ...StoreOption) ([][]byte, error) {
//	    o := GetStoreImplSpecificOptions(&InMemoryStoreOptions{}, opts...)
//	    // Use o.Limit, o.Offset, o.RoundID
//	}
type StoreOption struct {
	implSpecificOptFn any
}

// WrapStoreImplSpecificOptFn wraps an implementation-specific option function into a StoreOption.
// This allows different SessionStore implementations to define their own option types.
func WrapStoreImplSpecificOptFn[T any](optFn func(*T)) StoreOption {
	return StoreOption{
		implSpecificOptFn: optFn,
	}
}

// GetStoreImplSpecificOptions extracts implementation-specific options from a StoreOption list.
// Usage:
//
//	opts := GetStoreImplSpecificOptions(&MyStoreOptions{}, storeOpts...)
func GetStoreImplSpecificOptions[T any](base *T, opts ...StoreOption) *T {
	if base == nil {
		base = new(T)
	}

	for i := range opts {
		opt := opts[i]
		if opt.implSpecificOptFn != nil {
			optFn, ok := opt.implSpecificOptFn.(func(*T))
			if ok {
				optFn(base)
			}
		}
	}

	return base
}

// SessionStore is the interface for persisting session history.
//
// SessionID Design:
// The sessionID should encapsulate all application-level metadata needed for isolation and routing.
// Two common patterns:
//  1. Structured ID: "app_123:user_456:session_789" (implementation can parse for sharding/indexing)
//  2. Globally Unique ID: Use UUIDs or similar, ensuring uniqueness across all users/apps
//
// The framework treats sessionID as an opaque string; metadata extraction is the store's responsibility.
//
// Deletion Policy:
// This interface intentionally omits a Delete method. Session cleanup (e.g., GDPR compliance, TTL expiry)
// is an application-level concern, not a runtime execution concern. Implementations should provide
// deletion capabilities outside this interface (e.g., admin APIs, background jobs).
//
// Error Handling:
// Implementations should encapsulate retry logic for transient failures.
// Any error returned from these methods represents a case that can't be retried or exhausted retries,
// and will be treated as CRITICAL by SessionService.
// Implementations should also handle concurrency control (e.g., locking) if needed.
type SessionStore interface {
	// Get retrieves the session history from the store.
	// Accepts options like WithLimit, WithOffset for filtering.
	Get(ctx context.Context, sessionID string, opts ...StoreOption) ([][]byte, error)
	// Add appends new entries to the session history.
	// Accepts options like WithRoundID for metadata attachment.
	Add(ctx context.Context, sessionID string, entries [][]byte, opts ...StoreOption) error
	// Set overwrites the session history with the given entries.
	// Accepts options like WithRoundID for metadata attachment.
	Set(ctx context.Context, sessionID string, entries [][]byte, opts ...StoreOption) error
}

// AfterGetSessionHandler processes loaded events in batch (for summarization, compaction).
// After processing, the resulting events are injected into the agent's run context.
type AfterGetSessionHandler func(ctx context.Context, events []*AgentEvent) ([]*AgentEvent, error)

// BeforeAddSessionHandler processes individual events before saving.
// It applies to events from both saveOutput (agent execution) and saveInput (user input).
// Returning nil means the event should not be saved.
type BeforeAddSessionHandler func(ctx context.Context, event *AgentEvent) (*AgentEvent, error)

// EventSerializer defines how AgentEvents are converted to/from bytes.
// Implementations can use any format (e.g., gob, json, protobuf).
type EventSerializer interface {
	Marshal(event *AgentEvent) ([]byte, error)
	Unmarshal(data []byte) (*AgentEvent, error)
}

// defaultGobSerializer implements EventSerializer using encoding/gob.
type defaultGobSerializer struct{}

// Marshal converts an AgentEvent to gob-encoded bytes.
func (s *defaultGobSerializer) Marshal(event *AgentEvent) ([]byte, error) {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(event); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Unmarshal converts gob-encoded bytes to an AgentEvent.
func (s *defaultGobSerializer) Unmarshal(data []byte) (*AgentEvent, error) {
	var event AgentEvent
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&event); err != nil {
		return nil, err
	}
	return &event, nil
}

// SessionService manages the retrieval and persistence of session history.
// It acts as a middleware between the Runner and the SessionStore.
type SessionService struct {
	store           SessionStore
	serializer      EventSerializer
	beforeAdd       []BeforeAddSessionHandler
	afterGet        []AfterGetSessionHandler
	persistAfterGet bool
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

// WithPersistAfterGetSession enables automatic persistence of modified events after AfterGetSession handlers.
func WithPersistAfterGetSession() SessionServiceOption {
	return func(s *SessionService) { s.persistAfterGet = true }
}

// WithEventSerializer configures a custom serializer for the SessionService.
func WithEventSerializer(serializer EventSerializer) SessionServiceOption {
	return func(s *SessionService) {
		s.serializer = serializer
	}
}

// NewSessionService creates a new SessionService with the given store and options.
func NewSessionService(store SessionStore, opts ...SessionServiceOption) *SessionService {
	s := &SessionService{
		store:      store,
		serializer: &defaultGobSerializer{},
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// MarshalEvent converts an AgentEvent to bytes using the configured serializer.
func (s *SessionService) MarshalEvent(event *AgentEvent) ([]byte, error) {
	return s.serializer.Marshal(event)
}

// UnmarshalEvent converts bytes to an AgentEvent using the configured serializer.
func (s *SessionService) UnmarshalEvent(data []byte) (*AgentEvent, error) {
	return s.serializer.Unmarshal(data)
}

// marshalEvents serializes a batch of events using the configured serializer.
// The label tailors error messages to the caller context (e.g., "event", "input event").
func (s *SessionService) marshalEvents(events []*AgentEvent, label string) ([][]byte, error) {
	if len(events) == 0 {
		return nil, nil
	}
	entries := make([][]byte, 0, len(events))
	for _, event := range events {
		b, err := s.serializer.Marshal(event)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal %s: %w", label, err)
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
		event, err := s.serializer.Unmarshal(entry)
		if err != nil {
			return nil, fmt.Errorf("failed to deserialize session entry: %w", err)
		}
		events = append(events, event)
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
func (s *SessionService) load(ctx context.Context, sessionID string, opts ...StoreOption) ([]*AgentEvent, error) {
	// 1. load from store (CRITICAL)
	data, err := s.store.Get(ctx, sessionID, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to load session (sessionID=%s): %w", sessionID, err)
	}

	// 2. Deserialize (CRITICAL)
	events, err := s.unmarshalEntries(data)
	if err != nil {
		return nil, fmt.Errorf("failed to decode session entries (sessionID=%s): %w", sessionID, err)
	}

	// 3. Apply AfterGetSession handlers (NON-CRITICAL)
	for i, handler := range s.afterGet {
		modifiedEvents, err := handler(ctx, events)
		if err != nil {
			log.Printf("[WARN] AfterGetSession handler[%d] failed (sessionID=%s): %v, skipping remaining handlers", i, sessionID, err)
			break
		}
		events = modifiedEvents
	}

	// 4. PersistAfterGet if enabled (NON-CRITICAL)
	if s.persistAfterGet {
		if err := s.persistEvents(ctx, sessionID, events); err != nil {
			log.Printf("[WARN] PersistAfterGet failed (sessionID=%s): %v", sessionID, err)
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
	opts ...StoreOption,
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
		gen.Send(&AgentEvent{Err: fmt.Errorf("failed to serialize events (sessionID=%s): %w", sessionID, err)})
		return
	}

	// 3. Add to store (CRITICAL)
	if len(entries) > 0 {
		if err = s.store.Add(ctx, sessionID, entries, opts...); err != nil {
			errorEvent := &AgentEvent{Err: fmt.Errorf("failed to save session (sessionID=%s): %w", sessionID, err)}
			gen.Send(errorEvent)
		}
	}
}

// saveInput persists input messages to the session store.
// Messages are converted to AgentEvents, processed by handlers, and saved.
// CRITICAL errors (serialization, store.Add) return error.
// NON-CRITICAL errors (handlers) log warnings and continue.
func (s *SessionService) saveInput(ctx context.Context, sessionID string, input []Message, opts ...StoreOption) error {
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
		return fmt.Errorf("failed to serialize input (sessionID=%s): %w", sessionID, err)
	}

	// Add to store (CRITICAL)
	if err = s.store.Add(ctx, sessionID, entries, opts...); err != nil {
		return fmt.Errorf("failed to save input to session (sessionID=%s): %w", sessionID, err)
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

// InMemoryStoreOptions defines options specific to InMemorySessionStore.
type InMemoryStoreOptions struct {
	// Limit specifies the maximum number of events to return (latest N)
	Limit int
	// Offset specifies how many events to skip from the beginning
	Offset int
	// RoundID attaches metadata when saving or filters when querying
	RoundID string
}

// WithLimit requests the latest N events from InMemorySessionStore.
// Commonly used for "load last 10 turns" scenarios.
func WithLimit(n int) StoreOption {
	return WrapStoreImplSpecificOptFn(func(o *InMemoryStoreOptions) {
		o.Limit = n
	})
}

// WithOffset skips the first N events in InMemorySessionStore.
// Typically used in combination with WithLimit for pagination.
func WithOffset(n int) StoreOption {
	return WrapStoreImplSpecificOptFn(func(o *InMemoryStoreOptions) {
		o.Offset = n
	})
}

// WithRoundID attaches or filters by round identifier in InMemorySessionStore.
// When used with Add/Set, attaches the roundID metadata to stored events.
// When used with Get, filters events to return only those matching the roundID.
func WithRoundID(id string) StoreOption {
	return WrapStoreImplSpecificOptFn(func(o *InMemoryStoreOptions) {
		o.RoundID = id
	})
}

// InMemorySessionStore is a simple in-memory implementation of SessionStore.
//
// Scope and Behavior:
//   - Intended for testing and non-production scenarios; data is process-local and volatile.
//   - Serializes store operations per sessionID via a mutex; does NOT enforce single-runner policy.
//     Multiple runners may still target the same sessionID at the application level.
//   - Uses copy-on-read/write to avoid shared slice mutation by callers.
//   - Supports metadata attachment (RoundID) to demonstrate how stores can index/filter by metadata.
//
// This store is useful for tests and examples where persistence is not required.
// Production deployments should use a resilient store with retries and durability guarantees.
type InMemorySessionStore struct {
	data  map[string][]storedEntry
	locks map[string]*sync.Mutex
	mu    sync.Mutex // Protects the maps themselves
}

// storedEntry represents a single session event with optional metadata
type storedEntry struct {
	data    []byte
	roundID string
}

// NewInMemorySessionStore creates a new in-memory session store.
func NewInMemorySessionStore() *InMemorySessionStore {
	return &InMemorySessionStore{
		data:  make(map[string][]storedEntry),
		locks: make(map[string]*sync.Mutex),
	}
}

// getSessionLock returns the mutex for the given sessionID, creating one if it doesn't exist.
func (s *InMemorySessionStore) getSessionLock(sessionID string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.locks[sessionID]; !exists {
		s.locks[sessionID] = &sync.Mutex{}
	}
	return s.locks[sessionID]
}

// Get retrieves the session history from the store.
// This implementation supports WithLimit, WithOffset, and WithRoundID options.
func (s *InMemorySessionStore) Get(_ context.Context, sessionID string, opts ...StoreOption) ([][]byte, error) {
	lock := s.getSessionLock(sessionID)
	lock.Lock()
	defer lock.Unlock()

	entries := s.data[sessionID]
	if entries == nil {
		return [][]byte{}, nil
	}

	// Extract options
	o := GetStoreImplSpecificOptions(&InMemoryStoreOptions{}, opts...)

	// Apply RoundID filter first
	filtered := entries
	if o.RoundID != "" {
		filtered = make([]storedEntry, 0, len(entries))
		for _, entry := range entries {
			if entry.roundID == o.RoundID {
				filtered = append(filtered, entry)
			}
		}
	}

	// Apply Offset (skip first N)
	if o.Offset > 0 {
		if o.Offset >= len(filtered) {
			return [][]byte{}, nil
		}
		filtered = filtered[o.Offset:]
	}

	// Apply Limit (latest N after offset and filter)
	if o.Limit > 0 && len(filtered) > o.Limit {
		// Return the latest N events (from the end of the remaining slice)
		filtered = filtered[len(filtered)-o.Limit:]
	}

	// Extract data bytes from filtered entries
	result := make([][]byte, len(filtered))
	for i, entry := range filtered {
		result[i] = entry.data
	}

	return result, nil
}

// Add appends new entries to the session history.
// Supports WithRoundID option for metadata attachment.
func (s *InMemorySessionStore) Add(_ context.Context, sessionID string, entries [][]byte, opts ...StoreOption) error {
	lock := s.getSessionLock(sessionID)
	lock.Lock()
	defer lock.Unlock()

	// Extract options
	o := GetStoreImplSpecificOptions(&InMemoryStoreOptions{}, opts...)

	// Wrap entries with metadata
	for _, data := range entries {
		s.data[sessionID] = append(s.data[sessionID], storedEntry{
			data:    data,
			roundID: o.RoundID,
		})
	}

	return nil
}

// Set overwrites the session history with the given entries.
// Supports WithRoundID option for metadata attachment.
func (s *InMemorySessionStore) Set(_ context.Context, sessionID string, entries [][]byte, opts ...StoreOption) error {
	lock := s.getSessionLock(sessionID)
	lock.Lock()
	defer lock.Unlock()

	// Extract options
	o := GetStoreImplSpecificOptions(&InMemoryStoreOptions{}, opts...)

	// Wrap entries with metadata
	newEntries := make([]storedEntry, 0, len(entries))
	for _, data := range entries {
		newEntries = append(newEntries, storedEntry{
			data:    data,
			roundID: o.RoundID,
		})
	}

	s.data[sessionID] = newEntries
	return nil
}
