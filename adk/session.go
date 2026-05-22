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

package adk

import (
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	einoserial "github.com/cloudwego/eino/internal/serialization"
	"github.com/cloudwego/eino/schema"
)

const (
	defaultSessionEventFlushBatchSize = 16
	defaultSessionEventFlushInterval  = 100 * time.Millisecond
	defaultSessionEventBufferSize     = 64
	defaultMaxFlushRetries            = 3
	defaultFlushRetryInitialBackoff   = 50 * time.Millisecond
	defaultLoadPageSize               = 100
)

// ErrPendingSessionCheckpoint is returned when a managed session has an
// interrupted in-flight turn that must be resumed before accepting new input.
var ErrPendingSessionCheckpoint = errors.New("adk: pending session checkpoint")

const (
	sessionRunnerCheckpointSuffix = "/runner_checkpoint"
)

// SessionStore persists Runner-managed session data.
// Events are stored as an append-only ordered log of JSON-encoded SessionEvent payloads.
// TurnEndState is persisted as a regular SessionEvent variant (with TurnEnd field set),
// not as a separate entity.
//
// Concurrency contract: A single session (identified by sessionID) MUST have at most one
// active writer (Runner turn) at a time. The Runner enforces this via ErrPendingSessionCheckpoint
// (new Run while a checkpoint is pending) and the single-goroutine event loop within a turn.
// Store implementations are NOT required to handle concurrent AppendEvents calls
// for the same sessionID. Different sessionIDs may be written concurrently without restriction.
type SessionStore interface {
	// AppendEvents appends one or more JSON-encoded SessionEvent payloads to the session log.
	// Events are appended in the order given. The store assigns ordering internally.
	AppendEvents(ctx context.Context, sessionID string, events [][]byte) error

	// LoadEvents loads session events with pagination support.
	// Returns events in chronological order (oldest first) or reverse chronological
	// order (newest first) depending on opts.Reverse.
	LoadEvents(ctx context.Context, sessionID string, opts *LoadEventsRequest) (*LoadEventsResult, error)
}

// LoadEventsRequest configures event loading pagination and direction.
type LoadEventsRequest struct {
	// After is an opaque position cursor. Events strictly after this position
	// are returned. On the first call, pass empty to start from the beginning.
	// On subsequent pages, pass the Next value from the previous LoadEventsResult.
	// When non-empty and Reverse is false, only events after this position are
	// returned (forward/chronological). When Reverse is true and After is empty,
	// events are returned newest-first from the log tail.
	After string
	// Limit is the maximum number of events to return. 0 means no limit (load all).
	Limit int
	// Reverse, when true, returns events in newest-first order.
	// Useful for finding the latest MessagesReplaced boundary efficiently.
	Reverse bool
}

// LoadEventsResult is the response from LoadEvents.
type LoadEventsResult struct {
	// Events are the JSON-encoded SessionEvent payloads.
	Events [][]byte
	// Next is the opaque cursor for the next page. Empty means no more pages.
	// Pass it back as LoadEventsRequest.After to continue pagination.
	Next string
}

// SessionEvent is the JSON-serializable persistence format for session events.
// Exactly one semantic content field is active per event. The MessagesReplaced field
// uses pointer-to-slice semantics (nil = absent, non-nil = active replacement).
//
// TurnEndState is persisted as a SessionEvent with the TurnEnd field set. The Messages
// field within TurnEnd is intentionally left nil — messages are reconstructed from the
// event log on read.
type SessionEvent[M MessageType] struct {
	// Timestamp is inherited from the source AgentEvent and represents the event
	// occurrence time, not the SessionStore persistence time.
	Timestamp time.Time `json:"timestamp,omitempty"`

	Message          M                        `json:"message,omitempty"`
	MessagesReplaced *[]M                     `json:"messages_replaced"`
	MessageUpdated   *MessageUpdatedEvent[M]  `json:"message_updated,omitempty"`
	MessageInserted  *MessageInsertedEvent[M] `json:"message_inserted,omitempty"`
	TurnEnd          *TurnEndState[M]         `json:"turn_end,omitempty"`
}

// MessageUpdatedEvent represents a single message replacement within the messages array.
type MessageUpdatedEvent[M MessageType] struct {
	// MessageID identifies the target message via its eino-internal message ID
	// (stored in Extra["_eino_msg_id"]). UUID v4 assigned by ChatModelAgent for each
	// assistant output and tool result, guaranteed unique across turns.
	MessageID string `json:"message_id"`
	// Message is the new content (with placeholder).
	Message M `json:"message"`
}

// MessageInsertedEvent represents a message inserted by a middleware.
type MessageInsertedEvent[M MessageType] struct {
	// Message is the inserted message (carries its own idempotency markers in Extra/metadata).
	Message M `json:"message"`
	// BeforeMessageID identifies the message BEFORE which this message was inserted,
	// using the eino message ID. Empty string means "append at end".
	BeforeMessageID string `json:"before_message_id,omitempty"`
}

// SessionPersistenceConfig tunes managed-session event flushing.
type SessionPersistenceConfig struct {
	// EventFlushBatchSize is the maximum number of events accumulated before
	// triggering a flush to the SessionStore. Defaults to 16.
	EventFlushBatchSize int
	// EventFlushInterval is how often the background goroutine flushes
	// buffered events, even if the batch size has not been reached.
	// Defaults to 100ms.
	EventFlushInterval time.Duration
	// EventBufferSize is the capacity of the in-memory event channel between
	// the event producer and the background flush goroutine. Defaults to 64.
	EventBufferSize int
	// MaxFlushRetries is the maximum number of retry attempts when AppendEvents
	// fails. After exhausting retries, the error is latched and the turn fails.
	// Defaults to 3. Set to 0 to disable retries (fail on first error).
	MaxFlushRetries int
	// FlushRetryInitialBackoff is the base delay before the first retry.
	// Subsequent retries use exponential backoff (2x multiplier) with jitter.
	// Defaults to 50ms.
	FlushRetryInitialBackoff time.Duration
	// LoadPageSize is the number of events fetched per page when loading events
	// for reconstruction or tail replay. Defaults to 100.
	LoadPageSize int
}

// TurnEndState is the agent-visible state materialized at a successful turn boundary.
type TurnEndState[M MessageType] struct {
	Messages          []M
	ToolInfos         []*schema.ToolInfo
	DeferredToolInfos []*schema.ToolInfo
	SessionValues     map[string]any
}

type runnerSessionCheckpoint struct {
	Payload []byte
}

func init() {
	schema.RegisterName[*TurnEndState[*schema.Message]]("_eino_adk_turn_end_state")
	schema.RegisterName[*TurnEndState[*schema.AgenticMessage]]("_eino_adk_agentic_turn_end_state")

	// Register SessionEvent and helper types for HumanReadableSerializer.
	schema.RegisterName[*SessionEvent[*schema.Message]]("_eino_adk_session_event")
	schema.RegisterName[*SessionEvent[*schema.AgenticMessage]]("_eino_adk_agentic_session_event")
	schema.RegisterName[*MessageUpdatedEvent[*schema.Message]]("_eino_adk_message_updated_event")
	schema.RegisterName[*MessageUpdatedEvent[*schema.AgenticMessage]]("_eino_adk_agentic_message_updated_event")
	schema.RegisterName[*MessageInsertedEvent[*schema.Message]]("_eino_adk_message_inserted_event")
	schema.RegisterName[*MessageInsertedEvent[*schema.AgenticMessage]]("_eino_adk_agentic_message_inserted_event")
}

func encodeGob(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func encodeRunnerSessionCheckpoint(c *runnerSessionCheckpoint) ([]byte, error) {
	return encodeGob(c)
}

func decodeRunnerSessionCheckpoint(payload []byte) (*runnerSessionCheckpoint, error) {
	var c runnerSessionCheckpoint
	if err := gob.NewDecoder(bytes.NewReader(payload)).Decode(&c); err != nil {
		return nil, err
	}
	return &c, nil
}

func sessionRunnerCheckpointID(sessionID string) string {
	return "session/" + sessionID + sessionRunnerCheckpointSuffix
}

var sessionSerializer = &einoserial.HumanReadableSerializer{}

func encodeSessionEvent[M MessageType](event *SessionEvent[M]) ([]byte, error) {
	return sessionSerializer.Marshal(event)
}

func decodeSessionEvent[M MessageType](data []byte) (*SessionEvent[M], error) {
	var event SessionEvent[M]
	if err := sessionSerializer.Unmarshal(data, &event); err != nil {
		return nil, err
	}
	return &event, nil
}

// EncodeSessionEvent encodes a SessionEvent into the same JSON wire format used
// by the Runner when persisting events. Symmetric to DecodeSessionEvent. Public
// for external SessionStore implementations that need to construct payloads
// (e.g. for migration tooling, test fixtures).
func EncodeSessionEvent[M MessageType](event *SessionEvent[M]) ([]byte, error) {
	return encodeSessionEvent(event)
}

// DecodeSessionEvent decodes a raw JSON session event payload (as stored by SessionStore)
// into a typed SessionEvent. Public entry point for Go consumers needing type-exact
// deserialization. External (Python/JS) consumers can use plain JSON parsing.
func DecodeSessionEvent[M MessageType](data []byte) (*SessionEvent[M], error) {
	return decodeSessionEvent[M](data)
}

// makeInputSessionEvent wraps an input message as a SessionEvent.
func makeInputSessionEvent[M MessageType](msg M) *SessionEvent[M] {
	return &SessionEvent[M]{Timestamp: newEventTimestamp(), Message: msg}
}

// toSessionEvent converts an internal TypedAgentEvent into the persistence format.
// Returns nil if the event has no persistable content.
func toSessionEvent[M MessageType](event *TypedAgentEvent[M]) *SessionEvent[M] {
	if event == nil {
		return nil
	}
	se := &SessionEvent[M]{Timestamp: event.Timestamp}
	switch {
	case event.TurnEndState != nil:
		se.TurnEnd = &TurnEndState[M]{
			ToolInfos:         event.TurnEndState.ToolInfos,
			DeferredToolInfos: event.TurnEndState.DeferredToolInfos,
			SessionValues:     event.TurnEndState.SessionValues,
			// Messages intentionally omitted — reconstructed from event log on read.
		}
	case event.MessagesReplaced != nil:
		se.MessagesReplaced = event.MessagesReplaced
	case event.MessageUpdated != nil:
		se.MessageUpdated = event.MessageUpdated
	case event.MessageInserted != nil:
		se.MessageInserted = event.MessageInserted
	case event.Output != nil && event.Output.MessageOutput != nil:
		if !isNilMessage(event.Output.MessageOutput.Message) {
			se.Message = event.Output.MessageOutput.Message
		} else {
			return nil
		}
	default:
		return nil
	}
	return se
}

func normalizeSessionPersistenceConfig(cfg *SessionPersistenceConfig) SessionPersistenceConfig {
	normalized := SessionPersistenceConfig{
		EventFlushBatchSize:      defaultSessionEventFlushBatchSize,
		EventFlushInterval:       defaultSessionEventFlushInterval,
		EventBufferSize:          defaultSessionEventBufferSize,
		MaxFlushRetries:          defaultMaxFlushRetries,
		FlushRetryInitialBackoff: defaultFlushRetryInitialBackoff,
		LoadPageSize:             defaultLoadPageSize,
	}
	if cfg == nil {
		return normalized
	}
	if cfg.EventFlushBatchSize > 0 {
		normalized.EventFlushBatchSize = cfg.EventFlushBatchSize
	}
	if cfg.EventFlushInterval > 0 {
		normalized.EventFlushInterval = cfg.EventFlushInterval
	}
	if cfg.EventBufferSize > 0 {
		normalized.EventBufferSize = cfg.EventBufferSize
	}
	if cfg.MaxFlushRetries > 0 {
		normalized.MaxFlushRetries = cfg.MaxFlushRetries
	} else if cfg.MaxFlushRetries < 0 {
		// Explicitly set to 0 to disable retries.
		normalized.MaxFlushRetries = 0
	}
	if cfg.FlushRetryInitialBackoff > 0 {
		normalized.FlushRetryInitialBackoff = cfg.FlushRetryInitialBackoff
	}
	if cfg.LoadPageSize > 0 {
		normalized.LoadPageSize = cfg.LoadPageSize
	}
	return normalized
}

type sessionEventPersister[M MessageType] struct {
	ctx       context.Context
	store     SessionStore
	sessionID string
	cfg       SessionPersistenceConfig

	ch     chan []byte
	done   chan struct{}
	closed int32 // atomic: 1 after closeAndWait is called

	mu  sync.Mutex
	err error
}

func newSessionEventPersister[M MessageType](
	ctx context.Context,
	store SessionStore,
	sessionID string,
	cfg SessionPersistenceConfig,
) *sessionEventPersister[M] {
	p := &sessionEventPersister[M]{
		ctx:       ctx,
		store:     store,
		sessionID: sessionID,
		cfg:       cfg,
		done:      make(chan struct{}),
	}
	p.ch = make(chan []byte, p.cfg.EventBufferSize)
	go p.run()
	return p
}

func (p *sessionEventPersister[M]) enqueue(payload []byte) error {
	if len(payload) == 0 {
		return p.getErr()
	}
	if err := p.getErr(); err != nil {
		return err
	}
	if atomic.LoadInt32(&p.closed) != 0 {
		return p.getErr()
	}
	select {
	case p.ch <- payload:
		return nil
	case <-p.ctx.Done():
		return p.ctx.Err()
	}
}

func (p *sessionEventPersister[M]) closeAndWait() error {
	atomic.StoreInt32(&p.closed, 1)
	close(p.ch)
	<-p.done
	return p.getErr()
}

func (p *sessionEventPersister[M]) run() {
	defer close(p.done)
	timer := time.NewTimer(p.cfg.EventFlushInterval)
	defer timer.Stop()

	var batch [][]byte
	flush := func() {
		if len(batch) == 0 || p.getErr() != nil {
			batch = nil
			return
		}
		entries := make([][]byte, len(batch))
		copy(entries, batch)
		batch = nil

		var lastErr error
		for attempt := 0; attempt <= p.cfg.MaxFlushRetries; attempt++ {
			if attempt > 0 {
				backoff := p.cfg.FlushRetryInitialBackoff << uint(attempt-1)
				jitter := time.Duration(rand.Int63n(int64(backoff)/4 + 1))
				select {
				case <-time.After(backoff + jitter):
				case <-p.ctx.Done():
					p.setErr(p.ctx.Err())
					return
				}
			}
			if err := p.store.AppendEvents(p.ctx, p.sessionID, entries); err != nil {
				lastErr = err
				continue
			}
			return // success
		}
		p.setErr(lastErr)
	}

	for {
		select {
		case payload, ok := <-p.ch:
			if !ok {
				flush()
				return
			}
			if p.getErr() != nil {
				continue
			}
			batch = append(batch, payload)
			if len(batch) >= p.cfg.EventFlushBatchSize {
				flush()
				resetTimer(timer, p.cfg.EventFlushInterval)
			}
		case <-timer.C:
			flush()
			resetTimer(timer, p.cfg.EventFlushInterval)
		}
	}
}

func (p *sessionEventPersister[M]) setErr(err error) {
	if err == nil {
		return
	}
	p.mu.Lock()
	if p.err == nil {
		p.err = err
	}
	p.mu.Unlock()
}

func (p *sessionEventPersister[M]) getErr() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

func resetTimer(timer *time.Timer, d time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(d)
}

func stripSessionEventFields[M MessageType](event *TypedAgentEvent[M]) *TypedAgentEvent[M] {
	if event == nil {
		return nil
	}
	if event.TurnEndState == nil && event.MessagesReplaced == nil &&
		event.MessageUpdated == nil && event.MessageInserted == nil &&
		event.SessionID == "" {
		return event
	}
	stripped := *event
	stripped.TurnEndState = nil
	stripped.MessagesReplaced = nil
	stripped.MessageUpdated = nil
	stripped.MessageInserted = nil
	stripped.SessionID = ""
	if stripped.Output == nil && stripped.Action == nil && stripped.Err == nil {
		return nil
	}
	return &stripped
}

// applySessionEvent applies a single SessionEvent to the message array, mutating in place.
// TurnEnd events are metadata-only and do not mutate messages.
func applySessionEvent[M MessageType](messages *[]M, event *SessionEvent[M]) error {
	switch {
	case event.TurnEnd != nil:
		// TurnEnd is metadata-only; does not affect the message array.
		return nil

	case event.MessagesReplaced != nil:
		*messages = append([]M{}, *event.MessagesReplaced...)

	case event.MessageUpdated != nil:
		upd := event.MessageUpdated
		if replacementID := GetMessageID(upd.Message); replacementID != "" && replacementID != upd.MessageID {
			return fmt.Errorf("apply event: MessageUpdated target %q but replacement has ID %q — identity mismatch", upd.MessageID, replacementID)
		}
		if err := replaceMessageByID(messages, upd.MessageID, upd.Message); err != nil {
			return err
		}

	case event.MessageInserted != nil:
		ins := event.MessageInserted
		if ins.BeforeMessageID == "" {
			*messages = append(*messages, ins.Message)
		} else {
			inserted := false
			for j, msg := range *messages {
				if GetMessageID(msg) == ins.BeforeMessageID {
					var zero M
					*messages = append(*messages, zero)
					copy((*messages)[j+1:], (*messages)[j:])
					(*messages)[j] = ins.Message
					inserted = true
					break
				}
			}
			if !inserted {
				return fmt.Errorf("apply event: anchor message %q not found for insertion", ins.BeforeMessageID)
			}
		}

	default:
		if !isNilMessage(event.Message) {
			*messages = append(*messages, event.Message)
		}
	}
	return nil
}

// replaceMessageByID finds the message with the given ID and replaces it.
func replaceMessageByID[M MessageType](messages *[]M, msgID string, newMsg M) error {
	for i, msg := range *messages {
		if GetMessageID(msg) == msgID {
			(*messages)[i] = newMsg
			return nil
		}
	}
	return fmt.Errorf("reconstruct: target message %q not found for update", msgID)
}

// reconstructSessionState rebuilds session state by:
// 1. Reverse-scanning to find the latest TurnEnd (stash metadata) and MessagesReplaced.
// 2. Forward-replaying from the MessagesReplaced boundary to rebuild messages.
// Returns a TurnEndState with Messages populated from replay, plus ToolInfos/SessionValues
// from the stashed TurnEnd event. Returns nil if no events exist.
func reconstructSessionState[M MessageType](
	ctx context.Context,
	store SessionStore,
	sessionID string,
	pageSize int,
) (*TurnEndState[M], error) {
	var stashedTurnEnd *TurnEndState[M]
	var allEvents []*SessionEvent[M]
	var after string
	boundaryIdx := -1

	for {
		result, err := store.LoadEvents(ctx, sessionID, &LoadEventsRequest{
			After:   after,
			Limit:   pageSize,
			Reverse: true,
		})
		if err != nil {
			return nil, err
		}
		if result == nil || len(result.Events) == 0 {
			break
		}

		stop := false
		for _, data := range result.Events {
			event, err := decodeSessionEvent[M](data)
			if err != nil {
				return nil, err
			}
			if event.TurnEnd != nil && stashedTurnEnd == nil {
				stashedTurnEnd = event.TurnEnd
			}
			allEvents = append(allEvents, event)
			if event.MessagesReplaced != nil {
				boundaryIdx = len(allEvents) - 1
				stop = true
				break
			}
		}
		if stop {
			break
		}
		if result.Next == "" {
			break
		}
		after = result.Next
	}

	if len(allEvents) == 0 {
		return nil, nil
	}

	// allEvents is in reverse-chronological order. Reverse to get chronological.
	reverseSessionEvents(allEvents)
	if boundaryIdx >= 0 {
		boundaryIdx = len(allEvents) - 1 - boundaryIdx
	}

	var messages []M
	startIdx := 0

	if boundaryIdx >= 0 {
		messages = append([]M{}, *allEvents[boundaryIdx].MessagesReplaced...)
		startIdx = boundaryIdx + 1
	}

	for i := startIdx; i < len(allEvents); i++ {
		if err := applySessionEvent(&messages, allEvents[i]); err != nil {
			return nil, fmt.Errorf("reconstruct: %w", err)
		}
	}

	state := &TurnEndState[M]{Messages: messages}
	if stashedTurnEnd != nil {
		state.ToolInfos = stashedTurnEnd.ToolInfos
		state.DeferredToolInfos = stashedTurnEnd.DeferredToolInfos
		state.SessionValues = stashedTurnEnd.SessionValues
	}
	return state, nil
}

func reverseSessionEvents[M MessageType](events []*SessionEvent[M]) {
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}
}
