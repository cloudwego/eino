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

	"github.com/google/uuid"

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

// ErrInvalidEventID is returned by AppendEvents when a SessionEventPayload's
// EventID field is empty. Protocol-level: persisters MUST NOT retry.
//
// Note: stores accept any non-empty string as EventID. UUIDv4 is the
// Runner-side allocation format (see SessionEvent.EventID) but is NOT
// validated at the SessionStore boundary; downstream stores MAY accept other
// non-empty identifiers (e.g. for migration or testing).
var ErrInvalidEventID = errors.New("adk: session event has invalid event_id")

// ErrEventIDOutOfRange is returned by LoadEvents when LoadEventsRequest.After
// references an event_id that does not exist in the session log (e.g. due to
// log compaction or a stale SSE Last-Event-ID). Callers can detect this and
// fall back to a full reload.
var ErrEventIDOutOfRange = errors.New("adk: session event id out of range")

// SessionEventPayload is the storage-layer representation of a single session event.
// The framework pre-extracts EventID from the typed SessionEvent before
// serialization so that stores can dedup and index without parsing Data.
type SessionEventPayload struct {
	// EventID is the canonical, session-unique identity. Pre-extracted by
	// the framework; stores MUST NOT parse Data to obtain it.
	EventID string
	// Data is the serialized SessionEvent payload produced by the configured
	// EventSerializer. Stores treat this as opaque bytes.
	Data []byte
}

// protocolErrors enumerates protocol-level sentinels that persisters MUST
// fail-fast on. Future protocol-level sentinels MUST be added here so that
// isProtocolError stays the single source of truth.
var protocolErrors = []error{ErrInvalidEventID}

// isProtocolError reports whether err matches any protocol-level sentinel.
// Used by the persister flush loop to bypass retry/backoff for protocol
// violations while still applying the policy to infrastructure errors.
func isProtocolError(err error) bool {
	for _, target := range protocolErrors {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

const (
	sessionRunnerCheckpointSuffix = "/runner_checkpoint"
)

// SessionStore persists Runner-managed session data.
// Events are stored as an append-only ordered log of serialized SessionEvent payloads (as SessionEventPayload).
// TurnEndState is persisted as a regular SessionEvent variant (with TurnEnd field set),
// not as a separate entity.
//
// Concurrency contract: A single session (identified by sessionID) MUST have at most one
// active writer (Runner turn) at a time. The Runner skips any pending checkpoint
// on fresh Run (rather than blocking), so this constraint is caller-enforced:
// callers must serialize Run/Resume calls for the same sessionID.
// Store implementations are NOT required to handle concurrent AppendEvents calls
// for the same sessionID. Different sessionIDs may be written concurrently without restriction.
//
// Identity vs ordering: the Runner assigns each SessionEvent a session-unique
// event_id (UUIDv4 — see SessionEvent.EventID). The SessionStore owns append
// ordering and is responsible for resolving event_id ↔ position when servicing
// LoadEvents.After / .Next. From the store's perspective, event_id is an
// opaque non-empty string identity; UUIDv4 is the Runner allocation format,
// not a store-enforced validation rule. External consumers (SSE) treat
// event_id as the canonical event identity for de-duplication and
// Last-Event-ID resume.
//
// Errors are split into two classes:
//   - Protocol-level (e.g. ErrInvalidEventID): the input payload violates the
//     wire contract (empty EventID). Stores MUST return
//     such errors immediately; persisters MUST NOT retry them. Use
//     isProtocolError(err) to test membership.
//   - Infrastructure-level (e.g. network/db unavailable): transient; persisters
//     apply the configured retry/backoff policy.
//
// SSE consumer contract:
//   - SSE adapters MUST emit each SessionEvent's event_id as the SSE `id:` line
//     so browsers/clients can populate Last-Event-ID on reconnect.
//   - On reconnect, the SSE adapter passes Last-Event-ID as
//     LoadEventsRequest.After (with Reverse=false) to resume forward delivery.
//   - If LoadEvents returns ErrEventIDOutOfRange, the adapter SHOULD treat the
//     client's cursor as expired and fall back to a full reload (After="").
type SessionStore interface {
	// AppendEvents appends one or more SessionEventPayload entries to the session log.
	// Events are appended in the order given. The store assigns ordering internally.
	//
	// Each SessionEventPayload.EventID MUST be non-empty. If the EventID field
	// is empty, the store MUST return ErrInvalidEventID (a sentinel;
	// persisters will not retry it). Stores treat event_id as an opaque non-empty
	// string and MUST NOT validate format (UUIDv4 is the Runner allocation
	// convention, not a store-enforced contract). If a payload with an event_id
	// already present in the session is appended, the store MUST silently skip it
	// (no error, no duplicate entry); payload bytes are NOT compared —
	// first-write-wins.
	//
	// Batch atomicity is NOT required: on a mid-batch ErrInvalidEventID, earlier
	// valid payloads MAY have been persisted. Callers MUST treat AppendEvents as
	// best-effort batch + idempotent retry — re-issuing the same batch is safe
	// because already-stored event_ids are silently skipped.
	AppendEvents(ctx context.Context, sessionID string, events []SessionEventPayload) error

	// LoadEvents loads session events with pagination support.
	// Returns events in chronological order (oldest first) or reverse chronological
	// order (newest first) depending on opts.Reverse.
	LoadEvents(ctx context.Context, sessionID string, opts *LoadEventsRequest) (*LoadEventsResult, error)
}

// LoadEventsRequest configures event loading pagination and direction.
type LoadEventsRequest struct {
	// After is the last-seen event_id used as a directional cursor:
	//   - When Reverse=false: returns events strictly NEWER than the event with
	//     this id (in append order). Empty means start from the head.
	//   - When Reverse=true: returns events strictly OLDER than the event with
	//     this id. Empty means start from the tail.
	//
	// If the supplied event_id is not found in the session log, the store MUST
	// return ErrEventIDOutOfRange (a sentinel). Callers (e.g. SSE adapters) can
	// catch this to fall back to a full re-load instead of failing the request.
	After string
	// Limit is the maximum number of events to return. 0 means no limit (load all).
	Limit int
	// Reverse, when true, returns events in newest-first order.
	// Useful for finding the latest MessagesReplaced boundary efficiently.
	Reverse bool
}

// LoadEventsResult is the response from LoadEvents.
type LoadEventsResult struct {
	// Events are the serialized SessionEvent payloads.
	Events []SessionEventPayload
	// Next is the event_id of the LAST event in this page in the direction of
	// travel — i.e. the newest event for forward, the oldest event for reverse.
	// Pass it back as LoadEventsRequest.After (with the same Reverse flag) to
	// continue. Empty when the page reached the corresponding end of the log.
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
	// EventID is the canonical, session-unique identity of this event.
	// Assigned exactly once by the Runner at event materialization
	// (in makeInputSessionEvent / toSessionEvent). Persister-level retries
	// re-send the same payload bytes and therefore the same EventID, which is
	// what enables AppendEvents idempotency. Runner-allocated EventIDs are
	// UUIDv4 strings; SessionStore implementations treat EventID as an opaque
	// non-empty string and do NOT enforce UUIDv4 format (see SessionStore docs).
	//
	// Distinct from MessageUpdatedEvent.MessageID: EventID identifies the
	// session event envelope; MessageID identifies a logical message inside
	// the session message array.
	EventID string `json:"event_id"`

	// Timestamp is inherited from the source AgentEvent and represents the event
	// occurrence time, not the SessionStore persistence time.
	Timestamp time.Time `json:"timestamp,omitempty"`

	Kind SessionEventKind `json:"kind,omitempty"`

	// TurnID groups all events belonging to a single logical turn. A fresh Run
	// assigns a new UUID; a Resume preserves the original TurnID so downstream
	// consumers can correlate the entire turn (including the interrupted prefix
	// and the resumed suffix) as one unit.
	TurnID string `json:"turn_id,omitempty"`

	Message          M                        `json:"message,omitempty"`
	MessagesReplaced *[]M                     `json:"messages_replaced"`
	MessageUpdated   *MessageUpdatedEvent[M]  `json:"message_updated,omitempty"`
	MessageInserted  *MessageInsertedEvent[M] `json:"message_inserted,omitempty"`
	TurnEnd          *TurnEndState[M]         `json:"turn_end,omitempty"`

	Lifecycle *LifecycleEvent    `json:"lifecycle,omitempty"`
	Error     *SessionErrorEvent `json:"error,omitempty"`
	Span      *SpanEvent         `json:"span,omitempty"`

	AgentObservation *AgentObservationEvent `json:"agent_observation,omitempty"`
	UserObservation  *UserObservationEvent  `json:"user_observation,omitempty"`
}

type SessionEventKind string

const (
	SessionEventMessage          SessionEventKind = "message"
	SessionEventMessagesReplaced SessionEventKind = "messages_replaced"
	SessionEventMessageUpdated   SessionEventKind = "message_updated"
	SessionEventMessageInserted  SessionEventKind = "message_inserted"
	SessionEventTurnEnd          SessionEventKind = "turn_end"

	SessionEventSessionStatusRunning     SessionEventKind = "session.status_running"
	SessionEventSessionStatusIdle        SessionEventKind = "session.status_idle"
	SessionEventSessionStatusRescheduled SessionEventKind = "session.status_rescheduled"
	SessionEventSessionError             SessionEventKind = "session.error"

	SessionEventSpanModelRequestStart SessionEventKind = "span.model_request_start"
	SessionEventSpanModelRequestEnd   SessionEventKind = "span.model_request_end"

	SessionEventAgentThinking   SessionEventKind = "agent.thinking"
	SessionEventAgentToolUse    SessionEventKind = "agent.tool_use"
	SessionEventAgentToolResult SessionEventKind = "agent.tool_result"
	SessionEventUserInterrupt   SessionEventKind = "user.interrupt"
)

type LifecycleEvent struct {
	Scope      LifecycleScope  `json:"scope,omitempty"`
	State      SessionRunState `json:"state,omitempty"`
	Reason     string          `json:"reason,omitempty"`
	StopReason *StopReason     `json:"stop_reason,omitempty"`
}

type LifecycleScope string

const (
	LifecycleScopeSession LifecycleScope = "session"
)

type SessionRunState string

const (
	SessionRunStateRunning     SessionRunState = "running"
	SessionRunStateIdle        SessionRunState = "idle"
	SessionRunStateRescheduled SessionRunState = "rescheduled"
)

type StopReason struct {
	Type string `json:"type,omitempty"`
}

type SessionErrorEvent struct {
	// Type identifies the timeline error category. Known values are
	// SessionErrorTypeModelRetry and SessionErrorTypeModelFailover.
	Type        string       `json:"type,omitempty"`
	Message     string       `json:"message,omitempty"`
	RetryStatus *RetryStatus `json:"retry_status,omitempty"`
}

const (
	SessionErrorTypeModelRetry    = "model_retry"
	SessionErrorTypeModelFailover = "model_failover"
)

type RetryStatus struct {
	Type string `json:"type,omitempty"`
}

type SpanEvent struct {
	SpanID       string `json:"span_id"`
	ParentSpanID string `json:"parent_span_id,omitempty"`

	Kind SpanKind `json:"kind"`
	Name string   `json:"name,omitempty"`

	StartedAt time.Time `json:"started_at,omitempty"`
	EndedAt   time.Time `json:"ended_at,omitempty"`

	DurationMS           int64 `json:"duration_ms,omitempty"`
	FirstChunkDurationMS int64 `json:"first_chunk_duration_ms,omitempty"`

	Status string `json:"status,omitempty"`
	Err    string `json:"err,omitempty"`

	Model *ModelSpanMeta `json:"model,omitempty"`
}

type SpanKind string

const (
	SpanKindModel SpanKind = "model"
)

type ModelSpanMeta struct {
	Provider                 string      `json:"provider,omitempty"`
	Model                    string      `json:"model,omitempty"`
	Attempt                  int         `json:"attempt,omitempty"`
	ModelRequestStartEventID string      `json:"model_request_start_event_id,omitempty"`
	Usage                    *ModelUsage `json:"usage,omitempty"`
	FinishReason             string      `json:"finish_reason,omitempty"`
	Accepted                 bool        `json:"accepted"`
}

type ModelUsage struct {
	InputTokens              int                `json:"input_tokens,omitempty"`
	OutputTokens             int                `json:"output_tokens,omitempty"`
	CacheCreationInputTokens int                `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int                `json:"cache_read_input_tokens,omitempty"`
	Raw                      *schema.TokenUsage `json:"raw,omitempty"`
}

type AgentObservationEvent struct {
	Thinking   *AgentThinkingEvent   `json:"thinking,omitempty"`
	ToolUse    *AgentToolUseEvent    `json:"tool_use,omitempty"`
	ToolResult *AgentToolResultEvent `json:"tool_result,omitempty"`
}

type AgentThinkingEvent struct{}

type AgentToolUseEvent struct {
	ToolUseID           string         `json:"tool_use_id,omitempty"`
	Name                string         `json:"name,omitempty"`
	Input               map[string]any `json:"input,omitempty"`
	EvaluatedPermission string         `json:"evaluated_permission,omitempty"`
}

type AgentToolResultEvent struct {
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   any    `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

type UserObservationEvent struct {
	Interrupt *UserInterruptEvent `json:"interrupt,omitempty"`
}

type UserInterruptEvent struct {
	Reason string `json:"reason,omitempty"`
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
	// EventSerializer encodes and decodes SessionEvent payloads persisted
	// through SessionStore. Defaults to schema.HumanReadableSerializer.
	//
	// The serializer output is stored opaquely by SessionStore implementations;
	// no format constraint is imposed on the byte representation.
	EventSerializer schema.Serializer
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
	schema.RegisterName[*LifecycleEvent]("_eino_adk_lifecycle_event")
	schema.RegisterName[*SessionErrorEvent]("_eino_adk_session_error_event")
	schema.RegisterName[*RetryStatus]("_eino_adk_retry_status")
	schema.RegisterName[*SpanEvent]("_eino_adk_span_event")
	schema.RegisterName[*ModelSpanMeta]("_eino_adk_model_span_meta")
	schema.RegisterName[*ModelUsage]("_eino_adk_model_usage")
	schema.RegisterName[*AgentObservationEvent]("_eino_adk_agent_observation_event")
	schema.RegisterName[*AgentThinkingEvent]("_eino_adk_agent_thinking_event")
	schema.RegisterName[*AgentToolUseEvent]("_eino_adk_agent_tool_use_event")
	schema.RegisterName[*AgentToolResultEvent]("_eino_adk_agent_tool_result_event")
	schema.RegisterName[*UserObservationEvent]("_eino_adk_user_observation_event")
	schema.RegisterName[*UserInterruptEvent]("_eino_adk_user_interrupt_event")
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

var sessionSerializer schema.Serializer = &schema.HumanReadableSerializer{}

func encodeSessionEvent[M MessageType](event *SessionEvent[M]) ([]byte, error) {
	return encodeSessionEventWithSerializer(event, sessionSerializer)
}

func decodeSessionEvent[M MessageType](data []byte) (*SessionEvent[M], error) {
	return decodeSessionEventWithSerializer[M](data, sessionSerializer)
}

func encodeSessionEventWithSerializer[M MessageType](event *SessionEvent[M], serializer schema.Serializer) ([]byte, error) {
	return normalizeSerializer(serializer).Marshal(event)
}

func decodeSessionEventWithSerializer[M MessageType](data []byte, serializer schema.Serializer) (*SessionEvent[M], error) {
	var event SessionEvent[M]
	if err := normalizeSerializer(serializer).Unmarshal(data, &event); err != nil {
		return nil, err
	}
	if err := NormalizeSessionEventKind(&event); err != nil {
		return nil, err
	}
	return &event, nil
}

func normalizeSerializer(serializer schema.Serializer) schema.Serializer {
	if serializer == nil {
		return sessionSerializer
	}
	return serializer
}

// makeInputSessionEvent wraps an input message as a SessionEvent.
func makeInputSessionEvent[M MessageType](msg M) *SessionEvent[M] {
	return &SessionEvent[M]{EventID: uuid.NewString(), Timestamp: newEventTimestamp(), Kind: SessionEventMessage, Message: msg}
}

// toSessionEvent converts an internal TypedAgentEvent into the persistence format.
// Returns nil if the event has no persistable content. Reuses event.EventID
// allocated upstream by execCtx.send so live and persisted views share identity.
func toSessionEvent[M MessageType](event *TypedAgentEvent[M]) *SessionEvent[M] {
	se, _ := toSessionEventChecked(event)
	return se
}

func toSessionEventChecked[M MessageType](event *TypedAgentEvent[M]) (*SessionEvent[M], error) {
	if event == nil {
		return nil, nil
	}
	if event.SessionEvent != nil {
		se, err := normalizeAgentSessionEvent(event)
		if err != nil {
			return nil, err
		}
		if err := ValidateEmittedSessionEventKind(&se); err != nil {
			return nil, err
		}
		return &se, nil
	}
	se := &SessionEvent[M]{Timestamp: event.Timestamp}
	switch {
	case event.Output != nil && event.Output.MessageOutput != nil:
		if !isNilMessage(event.Output.MessageOutput.Message) {
			se.Kind = SessionEventMessage
			se.Message = event.Output.MessageOutput.Message
		} else {
			return nil, nil
		}
	default:
		return nil, nil
	}
	if event.EventID != "" {
		se.EventID = event.EventID
	} else {
		return nil, errors.New("persistable AgentEvent has empty EventID")
	}
	return se, NormalizeSessionEventKind(se)
}

func normalizeAgentSessionEvent[M MessageType](event *TypedAgentEvent[M]) (SessionEvent[M], error) {
	if event == nil || event.SessionEvent == nil {
		return SessionEvent[M]{}, errors.New("missing session event")
	}
	se := *event.SessionEvent
	if event.EventID != "" && se.EventID != "" && event.EventID != se.EventID {
		return SessionEvent[M]{}, fmt.Errorf("session event identity mismatch: agent event %q session event %q", event.EventID, se.EventID)
	}
	switch {
	case event.EventID != "":
		se.EventID = event.EventID
	case se.EventID != "":
		event.EventID = se.EventID
	default:
		id := uuid.NewString()
		event.EventID = id
		se.EventID = id
	}
	switch {
	case !event.Timestamp.IsZero() && se.Timestamp.IsZero():
		se.Timestamp = event.Timestamp
	case event.Timestamp.IsZero() && !se.Timestamp.IsZero():
		event.Timestamp = se.Timestamp
	case event.Timestamp.IsZero() && se.Timestamp.IsZero():
		ts := newEventTimestamp()
		event.Timestamp = ts
		se.Timestamp = ts
	}
	if se.TurnEnd != nil {
		turnEnd := *se.TurnEnd
		turnEnd.Messages = nil
		se.TurnEnd = &turnEnd
	}
	event.EventID = se.EventID
	event.Timestamp = se.Timestamp
	event.SessionEvent = &se
	return se, nil
}

func validateAgentSessionEventIdentity[M MessageType](event *TypedAgentEvent[M]) error {
	if event == nil || event.SessionEvent == nil {
		return nil
	}
	if event.EventID == "" || event.SessionEvent.EventID == "" || event.EventID != event.SessionEvent.EventID {
		return fmt.Errorf("session event identity mismatch: agent event %q session event %q", event.EventID, event.SessionEvent.EventID)
	}
	return nil
}

// ClassifySessionEvent derives the canonical event kind from the single active
// payload carried by event.
func ClassifySessionEvent[M MessageType](event *SessionEvent[M]) (SessionEventKind, error) {
	if event == nil {
		return "", errors.New("nil session event")
	}
	var kinds []SessionEventKind
	add := func(kind SessionEventKind) {
		kinds = append(kinds, kind)
	}
	if !isNilMessage(event.Message) {
		add(SessionEventMessage)
	}
	if event.MessagesReplaced != nil {
		add(SessionEventMessagesReplaced)
	}
	if event.MessageUpdated != nil {
		add(SessionEventMessageUpdated)
	}
	if event.MessageInserted != nil {
		add(SessionEventMessageInserted)
	}
	if event.TurnEnd != nil {
		add(SessionEventTurnEnd)
	}
	if event.Lifecycle != nil {
		switch event.Lifecycle.State {
		case SessionRunStateRunning:
			add(SessionEventSessionStatusRunning)
		case SessionRunStateIdle:
			add(SessionEventSessionStatusIdle)
		case SessionRunStateRescheduled:
			add(SessionEventSessionStatusRescheduled)
		default:
			return "", fmt.Errorf("unknown lifecycle state %q", event.Lifecycle.State)
		}
	}
	if event.Error != nil {
		add(SessionEventSessionError)
	}
	if event.Span != nil {
		switch {
		case event.Span.Kind != SpanKindModel:
			return "", fmt.Errorf("unknown span kind %q", event.Span.Kind)
		case !event.Span.StartedAt.IsZero() && event.Span.EndedAt.IsZero():
			add(SessionEventSpanModelRequestStart)
		case !event.Span.EndedAt.IsZero():
			add(SessionEventSpanModelRequestEnd)
		default:
			return "", errors.New("model span must have start or end timestamp")
		}
	}
	if event.AgentObservation != nil {
		var observationKinds []SessionEventKind
		if event.AgentObservation.Thinking != nil {
			observationKinds = append(observationKinds, SessionEventAgentThinking)
		}
		if event.AgentObservation.ToolUse != nil {
			observationKinds = append(observationKinds, SessionEventAgentToolUse)
		}
		if event.AgentObservation.ToolResult != nil {
			observationKinds = append(observationKinds, SessionEventAgentToolResult)
		}
		if len(observationKinds) != 1 {
			return "", fmt.Errorf("agent observation must have exactly one active payload, got %d", len(observationKinds))
		}
		switch observationKinds[0] {
		case SessionEventAgentThinking:
			add(SessionEventAgentThinking)
		case SessionEventAgentToolUse:
			add(SessionEventAgentToolUse)
		case SessionEventAgentToolResult:
			add(SessionEventAgentToolResult)
		}
	}
	if event.UserObservation != nil {
		if event.UserObservation.Interrupt == nil {
			return "", errors.New("user observation has no active payload")
		}
		add(SessionEventUserInterrupt)
	}
	if len(kinds) != 1 {
		return "", fmt.Errorf("session event must have exactly one active payload, got %d", len(kinds))
	}
	return kinds[0], nil
}

// NormalizeSessionEventKind fills an empty Kind from the active payload and
// rejects mismatches between Kind and payload shape.
func NormalizeSessionEventKind[M MessageType](event *SessionEvent[M]) error {
	kind, err := ClassifySessionEvent(event)
	if err != nil {
		return err
	}
	if event.Kind != "" && event.Kind != kind {
		return fmt.Errorf("session event kind %q does not match payload %q", event.Kind, kind)
	}
	event.Kind = kind
	return nil
}

// ValidateEmittedSessionEventKind enforces that runtime-emitted session events
// carry an explicit Kind matching their active payload.
func ValidateEmittedSessionEventKind[M MessageType](event *SessionEvent[M]) error {
	if event == nil {
		return errors.New("nil session event")
	}
	if event.Kind == "" {
		return errors.New("emitted session event must set non-empty Kind")
	}
	return NormalizeSessionEventKind(event)
}

func normalizeSessionPersistenceConfig(cfg *SessionPersistenceConfig) SessionPersistenceConfig {
	normalized := SessionPersistenceConfig{
		EventFlushBatchSize:      defaultSessionEventFlushBatchSize,
		EventFlushInterval:       defaultSessionEventFlushInterval,
		EventBufferSize:          defaultSessionEventBufferSize,
		MaxFlushRetries:          defaultMaxFlushRetries,
		FlushRetryInitialBackoff: defaultFlushRetryInitialBackoff,
		LoadPageSize:             defaultLoadPageSize,
		EventSerializer:          sessionSerializer,
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
	if cfg.EventSerializer != nil {
		normalized.EventSerializer = cfg.EventSerializer
	}
	return normalized
}

type sessionEventPersister[M MessageType] struct {
	ctx       context.Context
	store     SessionStore
	sessionID string
	cfg       SessionPersistenceConfig

	ch     chan SessionEventPayload
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
	p.ch = make(chan SessionEventPayload, p.cfg.EventBufferSize)
	go p.run()
	return p
}

func (p *sessionEventPersister[M]) enqueue(payload SessionEventPayload) error {
	if payload.EventID == "" {
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

	var batch []SessionEventPayload
	flush := func() {
		if len(batch) == 0 || p.getErr() != nil {
			batch = nil
			return
		}
		entries := make([]SessionEventPayload, len(batch))
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
				if isProtocolError(err) {
					// Protocol-level: fail fast, no retry, no backoff.
					p.setErr(err)
					return
				}
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
	if event.SessionEvent == nil && event.SessionID == "" {
		return event
	}
	stripped := *event
	stripped.SessionEvent = nil
	stripped.SessionID = ""
	if stripped.Output == nil && stripped.Action == nil && stripped.Err == nil {
		return nil
	}
	return &stripped
}

// applySessionEvent applies a single SessionEvent to the message array, mutating in place.
// TurnEnd events are metadata-only and do not mutate messages.
func applySessionEvent[M MessageType](messages *[]M, event *SessionEvent[M]) error {
	if !isContextSessionEvent(event) {
		return nil
	}
	return applyContextSessionEventInPlace(event, messages)
}

func isContextSessionEvent[M MessageType](event *SessionEvent[M]) bool {
	if event == nil {
		return false
	}
	return !isNilMessage(event.Message) || event.MessagesReplaced != nil ||
		event.MessageUpdated != nil || event.MessageInserted != nil
}

func isTurnEndSessionEvent[M MessageType](event *SessionEvent[M]) bool {
	return event != nil && event.TurnEnd != nil
}

func isTurnEndAgentEvent[M MessageType](event *TypedAgentEvent[M]) bool {
	return event != nil && event.SessionEvent != nil &&
		event.SessionEvent.Kind == SessionEventTurnEnd && event.SessionEvent.TurnEnd != nil
}

func applyContextSessionEvent[M MessageType](messages []M, event *SessionEvent[M]) ([]M, error) {
	out := append([]M{}, messages...)
	err := applyContextSessionEventInPlace(event, &out)
	return out, err
}

func applyContextSessionEventInPlace[M MessageType](event *SessionEvent[M], out *[]M) error {
	switch {
	case event.MessagesReplaced != nil:
		*out = append([]M{}, *event.MessagesReplaced...)

	case event.MessageUpdated != nil:
		upd := event.MessageUpdated
		if replacementID := GetMessageID(upd.Message); replacementID != "" && replacementID != upd.MessageID {
			return fmt.Errorf("apply event: MessageUpdated target %q but replacement has ID %q — identity mismatch", upd.MessageID, replacementID)
		}
		if err := replaceMessageByID(out, upd.MessageID, upd.Message); err != nil {
			return err
		}

	case event.MessageInserted != nil:
		ins := event.MessageInserted
		if ins.BeforeMessageID == "" {
			*out = append(*out, ins.Message)
		} else {
			inserted := false
			for j, msg := range *out {
				if GetMessageID(msg) == ins.BeforeMessageID {
					var zero M
					*out = append(*out, zero)
					copy((*out)[j+1:], (*out)[j:])
					(*out)[j] = ins.Message
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
			*out = append(*out, event.Message)
		}
	}
	return nil
}

func applyTurnEndSessionEvent[M MessageType](state *TurnEndState[M], event *SessionEvent[M]) *TurnEndState[M] {
	if state == nil {
		state = &TurnEndState[M]{}
	}
	if event == nil || event.TurnEnd == nil {
		return state
	}
	state.ToolInfos = event.TurnEnd.ToolInfos
	state.DeferredToolInfos = event.TurnEnd.DeferredToolInfos
	state.SessionValues = event.TurnEnd.SessionValues
	return state
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

type sessionReconstructResult[M MessageType] struct {
	state          *TurnEndState[M]
	inFlightTurnID string // TurnID from events after the last committed TurnEnd (the interrupted turn)
}

// reconstructSessionState rebuilds session state from the append log.
// Durable context events are replayed through the log tail, including messages
// after the latest TurnEnd. The latest TurnEnd remains the metadata boundary for
// tool infos, deferred tool infos, and session values. This preserves framework
// context fidelity; provider-specific sanitization for dangling tool-call
// structures remains a caller or middleware concern.
func reconstructSessionState[M MessageType](
	ctx context.Context,
	store SessionStore,
	sessionID string,
	pageSize int,
	serializer schema.Serializer,
) (*sessionReconstructResult[M], error) {
	var allEvents []*SessionEvent[M]
	var after string

	for {
		result, err := store.LoadEvents(ctx, sessionID, &LoadEventsRequest{
			After:   after,
			Limit:   pageSize,
			Reverse: false,
		})
		if err != nil {
			return nil, err
		}
		if result == nil || len(result.Events) == 0 {
			break
		}

		for _, ep := range result.Events {
			event, err := decodeSessionEventWithSerializer[M](ep.Data, serializer)
			if err != nil {
				return nil, err
			}
			allEvents = append(allEvents, event)
		}
		if result.Next == "" {
			break
		}
		after = result.Next
	}

	if len(allEvents) == 0 {
		return nil, nil
	}

	committedEndIdx := latestCommittedTurnEnd(allEvents)
	contextTailIdx := len(allEvents) - 1
	if committedEndIdx < 0 {
		// Compatibility for historical/session-fixture logs written before
		// TurnEnd became the explicit commit boundary.
		committedEndIdx = contextTailIdx
	}

	// After the last committed TurnEnd, any events belong to an interrupted
	// turn. The first TurnID found identifies that turn — all events within a
	// single turn share the same TurnID, so only the first match is needed.
	var inFlightTurnID string
	for i := committedEndIdx + 1; i <= contextTailIdx; i++ {
		if allEvents[i] != nil && allEvents[i].TurnID != "" {
			inFlightTurnID = allEvents[i].TurnID
			break
		}
	}

	state, err := replayDurableContextEvents(allEvents, committedEndIdx, contextTailIdx)
	if err != nil {
		return nil, err
	}
	return &sessionReconstructResult[M]{state: state, inFlightTurnID: inFlightTurnID}, nil
}

func replayDurableContextEvents[M MessageType](events []*SessionEvent[M], metadataTurnEndPos int, contextTailPos int) (*TurnEndState[M], error) {
	if len(events) == 0 || metadataTurnEndPos < 0 || contextTailPos < 0 {
		return nil, nil
	}
	if metadataTurnEndPos >= len(events) {
		metadataTurnEndPos = len(events) - 1
	}
	if contextTailPos >= len(events) {
		contextTailPos = len(events) - 1
	}
	if contextTailPos < metadataTurnEndPos {
		contextTailPos = metadataTurnEndPos
	}

	var messages []M
	startIdx := 0
	boundaryIdx := -1
	for i := 0; i <= contextTailPos; i++ {
		if events[i].MessagesReplaced != nil {
			boundaryIdx = i
		}
	}

	if boundaryIdx >= 0 {
		messages = append([]M{}, *events[boundaryIdx].MessagesReplaced...)
		startIdx = boundaryIdx + 1
	}

	for i := startIdx; i <= contextTailPos; i++ {
		if err := applySessionEvent(&messages, events[i]); err != nil {
			return nil, fmt.Errorf("reconstruct: %w", err)
		}
	}

	state := &TurnEndState[M]{Messages: messages}
	state = applyTurnEndSessionEvent(state, events[metadataTurnEndPos])
	return state, nil
}

func latestCommittedTurnEnd[M MessageType](events []*SessionEvent[M]) int {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i] != nil && events[i].Kind == SessionEventTurnEnd && events[i].TurnID != "" && events[i].TurnEnd != nil {
			return i
		}
	}
	// Fallback for legacy logs that lack Kind/TurnID on TurnEnd events.
	for i := len(events) - 1; i >= 0; i-- {
		if events[i] != nil && events[i].TurnEnd != nil {
			return i
		}
	}
	return -1
}
