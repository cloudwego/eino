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
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/cloudwego/eino/schema"
)

const defaultLoadPageSize = 100

// ErrInvalidEventID is returned by AppendEvents when a SessionEvent has an
// empty EventID. Protocol-level: persisters MUST NOT retry.
//
// Services accept any non-empty string as EventID. UUIDv4 is the Runner-side
// allocation format, but service implementations treat EventID as opaque.
var ErrInvalidEventID = errors.New("adk: session event has invalid event_id")

// ErrSessionEventIDGeneratorEmpty is returned by assignSessionEventID when the
// configured SessionEventIDGenerator returns an empty event_id. This is a
// generator-side contract violation surfaced before AppendEvents is called.
// It is a separate sentinel from ErrInvalidEventID (store-side) so callers
// can distinguish "application generator violated its contract" from "store
// rejected an event_id".
var ErrSessionEventIDGeneratorEmpty = errors.New("adk: session event id generator returned empty event id")

// ErrEventIDOutOfRange is returned by LoadEvents when
// LoadSessionEventsRequest.After references an event_id that does not exist in
// the session log. Callers can detect this and fall back to a full reload.
var ErrEventIDOutOfRange = errors.New("adk: session event id out of range")

var ErrRollbackTargetNotFound = errors.New("adk: rollback target event not found")
var ErrInvalidRollbackTarget = errors.New("adk: invalid rollback target")
var ErrRollbackTargetInactive = errors.New("adk: rollback target is not active")
var ErrSessionHeadChanged = errors.New("adk: session committed idle head changed")
var ErrSessionBusy = errors.New("adk: session already has an active handle")
var ErrDuplicateEventID = errors.New("adk: duplicate session event_id")

type SessionBusyError struct {
	ExpiresAt time.Time
}

func (e *SessionBusyError) Error() string { return ErrSessionBusy.Error() }
func (e *SessionBusyError) Unwrap() error { return ErrSessionBusy }

const (
	sessionRunnerCheckpointSuffix = "/runner_checkpoint"
)

// SessionEventStore is the provider-facing interface for a typed append-only
// session event log. Concurrent Run, Resume, or Rollback operations using the
// same store instance and session are rejected with ErrSessionBusy. Callers must
// serialize operations across store instances and processes.
type SessionEventStore[M MessageType] interface {
	// LoadEvents returns caller-owned events. Eino normalizes and validates their
	// Kind and payload before replay, including compatibility with historical kinds.
	LoadEvents(ctx context.Context, sessionID string, req *LoadSessionEventsRequest) (*LoadSessionEventsResult[M], error)
	// AppendEvents persists events whose Kind and payload have already been
	// validated by Eino. Implementations handle storage integrity, including event
	// IDs, duplicates, and serialization; they need not infer or validate Kind.
	// Callers writing directly to a store must supply complete, valid events.
	AppendEvents(ctx context.Context, sessionID string, events []*SessionEvent[M]) error
}

func openRunnerSession[M MessageType](ctx context.Context, store SessionEventStore[M], sessionID string) (*sessionHandle[M], error) {
	if store == nil {
		return nil, errors.New("adk: session store is nil")
	}
	return openLocalSession(ctx, store, sessionID)
}

// LoadSessionEventsRequest configures typed event loading pagination and direction.
type LoadSessionEventsRequest struct {
	// After is the event_id used as the exclusive starting cursor in the traversal
	// direction controlled by Reverse. The cursor event itself is not returned:
	// forward traversal loads events appended after it; reverse traversal loads
	// events appended before it. Empty starts at the oldest event when Reverse is
	// false, or the newest event when Reverse is true.
	After string
	// Limit is the maximum number of events to return. 0 means no limit.
	Limit int
	// Reverse controls the traversal direction: false traverses from oldest to
	// newest, true from newest to oldest. Order is based on append position,
	// not event timestamps.
	Reverse bool
	// Kinds filters events by their Kind field. Empty means no kind filter.
	Kinds []SessionEventKind
}

// LoadSessionEventsResult is the response from SessionEventStore.LoadEvents.
type LoadSessionEventsResult[M MessageType] struct {
	// Events are returned in traversal order: oldest to newest when Reverse is
	// false, newest to oldest when Reverse is true. The values are owned by the caller.
	Events []*SessionEvent[M]
	// Next is the event_id of the last event in this page when more events remain
	// in the traversal direction. Pass it as After with the same Reverse value to
	// load the next page. Empty means there are no more events in that direction.
	Next string
}

// SessionEvent is the JSON-serializable persistence format for session events.
// SessionEvent is session-local; stores receive the owning session id as a
// first-class argument. The pair (session_id, event_id) globally identifies a
// persisted event.
// Exactly one semantic content field is active per event. The MessagesReplaced field
// uses pointer-to-slice semantics (nil = absent, non-nil = active replacement).
// Persistence queues copy known payload structs and containers. Extra values
// and opaque application payloads remain shared and must not be mutated after
// publication. Tool parameter definitions are also shared as read-only data.
type SessionEvent[M MessageType] struct {
	// EventID is the canonical, session-unique identity of this event.
	// Assigned exactly once by the Runner at event materialization
	// (in makeInputSessionEvent / toSessionEvent). Persister-level retries
	// re-send the same payload bytes and therefore the same EventID, which is
	// what enables AppendEvents idempotency. Runner-allocated EventIDs are
	// UUIDv4 strings; SessionEventStore implementations treat EventID as an opaque
	// non-empty string and do NOT enforce UUIDv4 format.
	//
	// Distinct from MessageUpdatedEvent.MessageID: EventID identifies the
	// session event envelope; MessageID identifies a logical message inside
	// the session message array.
	EventID string `json:"event_id"`

	// TurnID is runner-owned, session-local correlation metadata. For a fresh
	// runner-managed Run or Query, it equals the opening
	// session.status_running EventID. Every event produced by that logical turn
	// carries the same value, and Resume preserves the value from the runner
	// checkpoint. Empty TurnID is valid for legacy records and events created
	// outside a runner-managed turn.
	TurnID string `json:"turn_id,omitempty"`

	// Timestamp is inherited from the source AgentEvent and represents the event
	// occurrence time, not the SessionEventStore persistence time.
	Timestamp time.Time `json:"timestamp,omitempty"`

	Kind SessionEventKind `json:"kind,omitempty"`

	// Extra carries event-envelope data. Application keys share the map with
	// framework-owned keys prefixed by "_eino_"; EventExtraProvider output cannot
	// create framework-owned keys. ADK ignores Extra for replay, reconstruction,
	// rollback, and kind classification. Values are serialized by the configured
	// SessionEventStore serializer; custom concrete types stored behind any must
	// be registered when durable round-trip behavior is required.
	Extra map[string]any `json:"extra,omitempty"`

	Message                 M                                `json:"message,omitempty"`
	MessageStreamIncomplete *MessageStreamIncompleteEvent[M] `json:"message_stream_incomplete,omitempty"`
	MessagesReplaced        *[]M                             `json:"messages_replaced,omitempty"`
	MessageUpdated          *MessageUpdatedEvent[M]          `json:"message_updated,omitempty"`
	MessageInserted         *MessageInsertedEvent[M]         `json:"message_inserted,omitempty"`
	MessagesDeleted         *MessagesDeletedEvent            `json:"messages_deleted,omitempty"`
	Rollback                *SessionRollbackEvent            `json:"rollback,omitempty"`

	Lifecycle *LifecycleEvent    `json:"lifecycle,omitempty"`
	Error     *SessionErrorEvent `json:"error,omitempty"`
	Span      *SpanEvent         `json:"span,omitempty"`

	Cancel    *CancelEvent           `json:"cancel,omitempty"`
	Interrupt *InterruptEvent        `json:"interrupt,omitempty"`
	Extension *SessionExtensionEvent `json:"extension,omitempty"`
}

// SessionEventVariant is the live AgentEvent envelope for session-related
// metadata. SessionID is live ownership metadata only; it is intentionally not
// part of durable SessionEvent payloads.
//
// Invariant: exactly one of Event or MessageStreamRef must be set.
// Event carries a fully materialized SessionEvent; MessageStreamRef carries
// only the reserved durable identity for a streaming message whose content
// remains in Output.MessageOutput.MessageStream.
type SessionEventVariant[M MessageType] struct {
	SessionID string

	Event            *SessionEvent[M]
	MessageStreamRef *MessageStreamRef
}

// MessageStreamRef identifies a streaming message whose content is available
// through Output.MessageOutput.MessageStream.
//
// With session persistence and WithTimelineEvents enabled, the Runner may emit
// a message or message_stream_incomplete event after the stream ends, using the
// same EventID. Consumers should use that event to update the message content.
// A corresponding event is not guaranteed, including when session persistence
// is disabled or the stream yields no chunks.
type MessageStreamRef struct {
	EventID   string
	TurnID    string
	Timestamp time.Time
	Kind      SessionEventKind
}

type SessionEventKind string

const (
	SessionEventMessage                 SessionEventKind = "message"
	SessionEventMessageStreamIncomplete SessionEventKind = "message_stream_incomplete"
	SessionEventMessagesReplaced        SessionEventKind = "messages_replaced"
	SessionEventMessageUpdated          SessionEventKind = "message_updated"
	SessionEventMessageInserted         SessionEventKind = "message_inserted"
	SessionEventMessagesDeleted         SessionEventKind = "messages_deleted"
	SessionEventRollback                SessionEventKind = "rollback"

	SessionEventSessionStatusRunning SessionEventKind = "session.status_running"
	SessionEventSessionStatusIdle    SessionEventKind = "session.status_idle"
	SessionEventSessionError         SessionEventKind = "session.error"

	SessionEventSpanModelRequestStart SessionEventKind = "span.model_request_start"
	SessionEventSpanModelRequestEnd   SessionEventKind = "span.model_request_end"
	SessionEventSpanToolCallStart     SessionEventKind = "span.tool_call_start"
	SessionEventSpanToolCallEnd       SessionEventKind = "span.tool_call_end"

	SessionEventCancel    SessionEventKind = "cancel"
	SessionEventInterrupt SessionEventKind = "interrupt"

	SessionEventExtensionPrefix = "x."
)

var knownSessionEventKinds = map[SessionEventKind]struct{}{
	SessionEventMessage:                 {},
	SessionEventMessageStreamIncomplete: {},
	SessionEventMessagesReplaced:        {},
	SessionEventMessageUpdated:          {},
	SessionEventMessageInserted:         {},
	SessionEventMessagesDeleted:         {},
	SessionEventRollback:                {},
	SessionEventSessionStatusRunning:    {},
	SessionEventSessionStatusIdle:       {},
	SessionEventSessionError:            {},
	SessionEventSpanModelRequestStart:   {},
	SessionEventSpanModelRequestEnd:     {},
	SessionEventSpanToolCallStart:       {},
	SessionEventSpanToolCallEnd:         {},
	SessionEventCancel:                  {},
	SessionEventInterrupt:               {},
}

func isKnownSessionEventKind(kind SessionEventKind) bool {
	if kind == "" {
		return false
	}
	if strings.HasPrefix(string(kind), SessionEventExtensionPrefix) {
		return true
	}
	_, ok := knownSessionEventKinds[kind]
	return ok
}

type LifecycleEvent struct {
	State      SessionRunState `json:"state,omitempty"`
	StopReason *StopReason     `json:"stop_reason,omitempty"`
}

type SessionRollbackEvent struct {
	ToEventID                 string `json:"to_event_id"`
	PreviousHeadCommitEventID string `json:"previous_head_commit_event_id,omitempty"`
}

type SessionRunState string

const (
	SessionRunStateRunning SessionRunState = "running"
	SessionRunStateIdle    SessionRunState = "idle"
)

type StopReason struct {
	// Type identifies why a session run returned to idle. It remains a string
	// for gob compatibility with previously persisted SessionEvent bytes;
	// compare it with the StopReason* constants.
	Type string `json:"type,omitempty"`
}

// StopReasonType identifies why a session run returned to idle.
type StopReasonType = string

const (
	// StopReasonEndTurn means the agent completed the current turn normally.
	StopReasonEndTurn StopReasonType = "end_turn"
	// StopReasonInterrupted means the run paused for an interrupt.
	StopReasonInterrupted StopReasonType = "interrupted"
	// StopReasonCancelled means the run was cancelled before normal completion.
	StopReasonCancelled StopReasonType = "cancelled"
	// StopReasonRetriesExhausted means model retry attempts were exhausted.
	StopReasonRetriesExhausted StopReasonType = "retries_exhausted"
	// StopReasonFailed means the run ended with a non-retry terminal failure.
	StopReasonFailed StopReasonType = "failed"
)

type SessionErrorEvent struct {
	// Type identifies the timeline error category. Known values are
	// SessionErrorTypeModelRetry, SessionErrorTypeModelFailover, and SessionErrorTypeFatal.
	Type        string       `json:"type,omitempty"`
	Message     string       `json:"message,omitempty"`
	RetryStatus *RetryStatus `json:"retry_status,omitempty"`
}

const (
	SessionErrorTypeModelRetry    = "model_retry"
	SessionErrorTypeModelFailover = "model_failover"
	SessionErrorTypeFatal         = "fatal"
)

type RetryStatus struct {
	// Type identifies whether a retry-like mechanism is still retrying or has
	// exhausted its attempts.
	Type RetryStatusType `json:"type,omitempty"`
}

// RetryStatusType identifies whether a retry-like mechanism is still retrying
// or has exhausted its configured attempts.
type RetryStatusType = string

const (
	// RetryStatusRetrying means another retry or failover attempt will be made.
	RetryStatusRetrying RetryStatusType = "retrying"
	// RetryStatusExhausted means the configured retry or failover attempts are exhausted.
	RetryStatusExhausted RetryStatusType = "exhausted"
)

type SpanEvent struct {
	// SpanID identifies one call and is shared by its start and end events.
	SpanID string `json:"span_id"`
	// ParentSpanID links a tool call to its model span. For model failover,
	// it identifies the group of attempts; that group has no separate span event.
	ParentSpanID string `json:"parent_span_id,omitempty"`

	Kind SpanKind `json:"kind"`

	// StartedAt is also carried on the end event so its duration is self-contained.
	StartedAt time.Time `json:"started_at,omitempty"`
	// EndedAt is zero on start events.
	EndedAt time.Time `json:"ended_at,omitempty"`

	// Status is empty on start events. A model response rejected by the retry
	// policy ends with SpanStatusError, even if the model call itself succeeded.
	Status SpanStatus `json:"status,omitempty"`
	// Err describes this call's failure or retry-policy rejection.
	Err string `json:"err,omitempty"`

	// Model and Tool are mutually exclusive: exactly one must be non-nil for
	// every Span-carrying SessionEvent. Eino validates this before emitting or
	// replaying the event.
	Model *ModelSpanMeta `json:"model,omitempty"`
	Tool  *ToolSpanMeta  `json:"tool,omitempty"`
}

type SpanKind string

const (
	SpanKindModel SpanKind = "model"
	SpanKindTool  SpanKind = "tool"
)

// SpanStatus is the outcome of a completed model or tool call.
type SpanStatus = string

const (
	SpanStatusOK        SpanStatus = "ok"
	SpanStatusError     SpanStatus = "error"
	SpanStatusCancelled SpanStatus = "cancelled"
)

// ModelSpanMeta describes one model-call attempt, not an aggregate across retries.
type ModelSpanMeta struct {
	// Provider is the component type reported by the current failover model,
	// when available. It is not necessarily a provider name and is empty when
	// no failover model is present in the context.
	Provider string `json:"provider,omitempty"`
	// Model is the model name from options (model.WithModel). Best-effort: empty
	// if the user configures model name directly on the ChatModel implementation
	// without passing model.WithModel in call-site options.
	Model string `json:"model,omitempty"`
	// Attempt is one-based. It is the failover attempt when a failover context
	// exists; otherwise it is the retry attempt. It does not count nested retries
	// separately within a failover attempt.
	Attempt int `json:"attempt,omitempty"`
	// ModelRequestStartEventID links an end event to its start event.
	ModelRequestStartEventID string `json:"model_request_start_event_id,omitempty"`
	// TTFTMS measures milliseconds from request start to the first successfully
	// received stream chunk, which need not contain a text token. Set only on
	// stream end events; zero also covers no chunk or a duration below 1 ms.
	TTFTMS int64 `json:"ttft_ms,omitempty"`
	// Usage is the usage reported by this attempt's response, including responses
	// rejected by the retry policy. It has the same meaning as message usage and
	// is not accumulated across attempts. Nil means unavailable, not zero usage.
	Usage        *schema.TokenUsage `json:"usage,omitempty"`
	FinishReason string             `json:"finish_reason,omitempty"`
	Timeout      *ModelTimeoutMeta  `json:"timeout,omitempty"`
}

// ModelTimeoutMeta records timeout details supplied by an error implementing
// ModelTimeoutSpanMeta() (phase string, timeout time.Duration, elapsed time.Duration, chunksReceived int).
type ModelTimeoutMeta struct {
	// Phase identifies which part of the model call exceeded its timeout budget.
	Phase string `json:"phase,omitempty"`
	// TimeoutMS is the configured timeout budget in milliseconds.
	TimeoutMS int64 `json:"timeout_ms,omitempty"`
	// ElapsedMS is the observed elapsed duration in milliseconds.
	ElapsedMS int64 `json:"elapsed_ms,omitempty"`
	// ChunksReceived is the number of stream chunks delivered before the timeout.
	ChunksReceived int `json:"chunks_received,omitempty"`
}

// ToolSpanMeta carries the operational metadata of a single tool call span.
// Inputs and outputs are NOT recorded here — they live on the assistant
// message and the tool result message respectively. The span is a stable
// identity envelope that joins those two messages together with timing
// and status.
//
// Tool spans for interrupted tool calls may straddle multiple Run/Resume
// invocations: the start span fires on the run where the call begins, and the
// end span fires on the run where the call completes after resume.
// Both spans share the same SpanID. Consumers correlating a start span to
// its eventual end span should follow SessionEvent.Span.SpanID (or use
// ToolUseID for cross-event correlation across resume boundaries).
type ToolSpanMeta struct {
	// ToolUseID is the model-assigned call ID; joins to the assistant
	// message's tool-call entry and the tool result message's call ID.
	ToolUseID string `json:"tool_use_id"`

	// Name is the tool name. Carried on both start and end so UIs can render
	// the span without resolving the assistant message.
	Name string `json:"name,omitempty"`

	// ToolCallStartEventID links the end span back to its start (mirrors
	// ModelSpanMeta.ModelRequestStartEventID). Set only on the end span.
	ToolCallStartEventID string `json:"tool_call_start_event_id,omitempty"`

	// AssistantMessageEventID is the SessionEvent ID of the assistant
	// message that emitted this tool call. Lets consumers fetch arguments
	// without scanning. Stable across interrupt/resume; the assistant message
	// ID established in the original turn is preserved on the eventual end
	// span via the in-flight span snapshot.
	AssistantMessageEventID string `json:"assistant_message_event_id,omitempty"`

	// ToolResultMessageEventID is the SessionEvent ID of the tool result
	// message. Set only on the end span; empty when the call errored before
	// producing one.
	ToolResultMessageEventID string `json:"tool_result_message_event_id,omitempty"`
}

// CancelEvent records a user-initiated cancellation in the durable session timeline.
type CancelEvent struct {
	Reason string `json:"reason,omitempty"`
}

// InterruptEvent records a business interrupt in the durable session timeline.
type InterruptEvent struct {
	// Contexts is the set of interrupt contexts that caused the agent to pause.
	// Each element represents a single root-cause interrupt point.
	Contexts []*InterruptContext `json:"contexts,omitempty"`
}

// InterruptContext describes a single interrupt point within a batch.
type InterruptContext struct {
	// InterruptID is the fully-qualified address of the interrupt point
	// (e.g. "agent:A;tool:lookup:call_1"). Use this as the key in ResumeParams.Targets.
	InterruptID string `json:"interrupt_id,omitempty"`
	// Info is the business-defined payload describing the interrupt, provided by
	// the component that triggered it (e.g. a middleware or a custom tool).
	// ADK treats it as opaque; consumers type-assert it to the concrete type the
	// triggering component documents to determine how to handle the interrupt.
	Info any `json:"info,omitempty"`
	// ToolUseID is set when the interrupt source is a specific tool call. It is
	// structural metadata derived from the interrupt address, identifying which
	// tool call paused; it carries no business semantics.
	ToolUseID string `json:"tool_use_id,omitempty"`
}

// SessionExtensionEvent carries application-owned timeline event payloads.
// The SessionEvent.Kind field is the application event type and must use the
// SessionEventExtensionPrefix namespace. Data is application-owned typed payload
// data. Custom payload types that need durable round-trip behavior must be
// registered with schema.RegisterName before session events are encoded and
// decoded. Consumers can inspect SessionEvent.Kind and type-assert Data to the
// registered concrete payload type.
type SessionExtensionEvent struct {
	Data any `json:"data,omitempty"`
}

// MessageStreamIncompleteEvent records the materialized prefix of a stream that
// failed before EOF. It is durable replay data and does not enter model context.
type MessageStreamIncompleteEvent[M MessageType] struct {
	Message M      `json:"message"`
	Error   string `json:"error,omitempty"`
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
	// Message is the inserted message (carries its own idempotency markers in Extra).
	Message M `json:"message"`
	// BeforeMessageID identifies the message BEFORE which this message was inserted,
	// using the eino message ID. Empty string means "append at end".
	BeforeMessageID string `json:"before_message_id,omitempty"`
}

// MessagesDeletedEvent represents a batch deletion within the messages array.
type MessagesDeletedEvent struct {
	// MessageIDs identifies the messages to delete via their eino-internal message IDs.
	MessageIDs []string `json:"message_ids"`
}

// SessionEventIDGenerator returns the EventID for a draft SessionEvent[M].
//
// Generators see the fully-populated session-local draft (Kind, Message, Span,
// Extension, payload, timestamp, ...) and may return a business-side identifier
// such as the matching application order/job/result ID. When a generator does not
// recognize a draft event, it should fall through to
// DefaultSessionEventIDGenerator[M] rather than allocating a UUID directly,
// so that the default behavior stays consistent with the framework default.
//
// The event argument is read-only; the returned string is the generator's only
// supported output. For streaming messages, the draft has Kind=message but no
// Message yet, because identity is reserved before the stream is materialized.
//
// A returned empty event_id is treated as a generator-side contract violation
// (ErrSessionEventIDGeneratorEmpty); the runner fails closed before the
// event is appended to the store.
type SessionEventIDGenerator[M MessageType] func(ctx context.Context, event *SessionEvent[M]) (string, error)

// SessionEventExtraProvider returns application-owned event-envelope data for a
// normalized SessionEvent draft. The returned map is the provider's only
// supported output channel; providers should treat the event argument as
// read-only.
type SessionEventExtraProvider[M MessageType] func(ctx context.Context, event *SessionEvent[M]) (map[string]any, error)

// DefaultSessionEventIDGenerator returns a UUID-based EventID for any draft.
// It is exported so that application-side SessionEventIDGenerator[M]
// implementations can fall through to default behavior when they do not
// recognize a draft event, e.g.:
//
//	func myGen(ctx context.Context, e *SessionEvent[M]) (string, error) {
//	    if id, ok := mapDraftToBusinessID(e); ok {
//	        return id, nil
//	    }
//	    return DefaultSessionEventIDGenerator[M](ctx, e)
//	}
func DefaultSessionEventIDGenerator[M MessageType](_ context.Context, _ *SessionEvent[M]) (string, error) {
	return uuid.NewString(), nil
}

// SessionConfig customizes session event identity and metadata.
type SessionConfig[M MessageType] struct {
	// EventIDGenerator decides the EventID of every SessionEvent[M] produced
	// by the runner / wrappers. The generator sees the fully-populated draft
	// before assignment and may map it to a business-side ID. If nil,
	// DefaultSessionEventIDGenerator[M] (UUID v4) is used.
	//
	// The generator is the sole authority for runner-generated event IDs:
	// drafts always have an empty EventID at the assignment boundary, and
	// the generator is always invoked. Returning an empty string fails the
	// turn closed (ErrSessionEventIDGeneratorEmpty).
	EventIDGenerator SessionEventIDGenerator[M]
	// EventExtraProvider decorates every runner / wrapper produced session event
	// with additional event-envelope Extra. Provider output is merged before
	// EventIDGenerator runs, so custom generators can inspect the final Extra map.
	// Returned keys prefixed with "_eino_" are rejected.
	EventExtraProvider SessionEventExtraProvider[M]
}

type reconstructedSessionState[M MessageType] struct {
	Messages []M
}

type runnerSessionCheckpoint struct {
	SessionID    string
	CheckPointID string
	TurnID       string
	Payload      []byte
}

func init() {
	// Register SessionEvent and helper types for HumanReadableSerializer.
	schema.RegisterName[*SessionEvent[*schema.Message]]("_eino_adk_session_event")
	schema.RegisterName[*SessionEvent[*schema.AgenticMessage]]("_eino_adk_agentic_session_event")
	schema.RegisterName[*MessageStreamIncompleteEvent[*schema.Message]]("_eino_adk_message_stream_incomplete_event")
	schema.RegisterName[*MessageStreamIncompleteEvent[*schema.AgenticMessage]]("_eino_adk_agentic_message_stream_incomplete_event")
	schema.RegisterName[*MessageUpdatedEvent[*schema.Message]]("_eino_adk_message_updated_event")
	schema.RegisterName[*MessageUpdatedEvent[*schema.AgenticMessage]]("_eino_adk_agentic_message_updated_event")
	schema.RegisterName[*MessageInsertedEvent[*schema.Message]]("_eino_adk_message_inserted_event")
	schema.RegisterName[*MessageInsertedEvent[*schema.AgenticMessage]]("_eino_adk_agentic_message_inserted_event")
	schema.RegisterName[*MessagesDeletedEvent]("_eino_adk_messages_deleted_event")
	schema.RegisterName[*LifecycleEvent]("_eino_adk_lifecycle_event")
	schema.RegisterName[*SessionErrorEvent]("_eino_adk_session_error_event")
	schema.RegisterName[*RetryStatus]("_eino_adk_retry_status")
	schema.RegisterName[*SpanEvent]("_eino_adk_span_event")
	schema.RegisterName[*ModelSpanMeta]("_eino_adk_model_span_meta")
	schema.RegisterName[*ModelTimeoutMeta]("_eino_adk_model_timeout_meta")
	schema.RegisterName[*ToolSpanMeta]("_eino_adk_tool_span_meta")
	// Note: SessionEventVariant and MessageStreamRef are not registered here
	// because they never reach a serializer: checkpoint sanitizers strip
	// SessionEventVariant before gob encoding, and the store serializer only
	// encodes *SessionEvent[M] (the materialized form, not the variant).
	schema.RegisterName[*CancelEvent]("_eino_adk_cancel_event")
	schema.RegisterName[*InterruptEvent]("_eino_adk_interrupt_event")
	schema.RegisterName[*InterruptContext]("_eino_adk_interrupt_context")
	schema.RegisterName[*SessionExtensionEvent]("_eino_adk_session_extension_event")
	schema.RegisterName[*SessionRollbackEvent]("_eino_adk_session_rollback_event")
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
	return &event, nil
}

func normalizeSerializer(serializer schema.Serializer) schema.Serializer {
	if serializer == nil {
		return sessionSerializer
	}
	return serializer
}

// makeInputSessionEvent wraps an input message as a SessionEvent draft.
//
// The returned draft has an empty EventID; the caller must assign one via
// assignSessionEventID before sending or persisting the event.
func makeInputSessionEvent[M MessageType](msg M) *SessionEvent[M] {
	return &SessionEvent[M]{Timestamp: newEventTimestamp(), Kind: SessionEventMessage, Message: msg}
}

// toSessionEventChecked copies an already-prepared session event for persistence.
// It does not allocate identity, fill metadata, or modify the live agent event.
func toSessionEventChecked[M MessageType](event *TypedAgentEvent[M]) (*SessionEvent[M], error) {
	if event == nil {
		return nil, nil
	}
	if event.SessionEventVariant != nil && event.SessionEventVariant.Event != nil {
		se := *event.SessionEventVariant.Event
		if se.EventID == "" {
			return nil, ErrInvalidEventID
		}
		if se.Kind == "" {
			return nil, errors.New("emitted session event must set non-empty Kind")
		}
		return &se, nil
	}
	if event.Output != nil && event.Output.MessageOutput != nil &&
		!isNilMessage(event.Output.MessageOutput.Message) {
		return nil, errors.New("persistable AgentEvent has no SessionEventVariant.Event")
	}
	return nil, nil
}

func (v *SessionEventVariant[M]) isForeignTo(sessionID string) bool {
	return v != nil && v.SessionID != "" && v.SessionID != sessionID
}

// prepareLiveSessionEvent checks the variant before delivery. Only its owner
// may assign identity, stamp the turn or invoke the event callbacks.
func prepareLiveSessionEvent[M MessageType](ctx context.Context, v *SessionEventVariant[M], sc sessionEventContext[M], foreign bool) error {
	if v == nil {
		return nil
	}
	if (v.Event == nil) == (v.MessageStreamRef == nil) {
		return errors.New("session event variant must set exactly one payload")
	}
	if ref := v.MessageStreamRef; ref != nil {
		if ref.Kind != SessionEventMessage {
			return fmt.Errorf("message stream ref kind must be %q, got %q", SessionEventMessage, ref.Kind)
		}
		if !foreign && sc.turnID != "" {
			ref.TurnID = sc.turnID
		}
		return nil
	}
	if foreign {
		return validateEmittedSessionEventKind(v.Event)
	}
	var err error
	v.Event, err = prepareReceivedSessionEvent(ctx, v.Event, sc)
	return err
}

// classifySessionEvent checks that exactly one payload is active before
// validating its contents and deriving the canonical event kind.
func classifySessionEvent[M MessageType](event *SessionEvent[M]) (SessionEventKind, error) {
	if event == nil {
		return "", errors.New("nil session event")
	}
	if count := countActiveSessionEventPayloads(event); count != 1 {
		return "", fmt.Errorf("session event must have exactly one active payload, got %d", count)
	}

	switch {
	case !isNilMessage(event.Message):
		return SessionEventMessage, nil
	case event.MessageStreamIncomplete != nil:
		if isNilMessage(event.MessageStreamIncomplete.Message) {
			return "", errors.New("message stream incomplete event must set non-nil Message")
		}
		return SessionEventMessageStreamIncomplete, nil
	case event.MessagesReplaced != nil:
		for i, message := range *event.MessagesReplaced {
			if isNilMessage(message) {
				return "", fmt.Errorf("MessagesReplaced[%d] must not be nil", i)
			}
		}
		return SessionEventMessagesReplaced, nil
	case event.MessageUpdated != nil:
		if event.MessageUpdated.MessageID == "" {
			return "", errors.New("MessageUpdated.MessageID must not be empty")
		}
		if isNilMessage(event.MessageUpdated.Message) {
			return "", errors.New("MessageUpdated.Message must not be nil")
		}
		return SessionEventMessageUpdated, nil
	case event.MessageInserted != nil:
		if isNilMessage(event.MessageInserted.Message) {
			return "", errors.New("MessageInserted.Message must not be nil")
		}
		return SessionEventMessageInserted, nil
	case event.MessagesDeleted != nil:
		if err := validateMessageIDs("MessagesDeleted.MessageIDs", event.MessagesDeleted.MessageIDs); err != nil {
			return "", err
		}
		return SessionEventMessagesDeleted, nil
	case event.Rollback != nil:
		if event.EventID == "" {
			return "", errors.New("rollback session event must set non-empty EventID")
		}
		if event.Rollback.ToEventID == "" {
			return "", errors.New("rollback session event must set non-empty ToEventID")
		}
		return SessionEventRollback, nil
	case event.Lifecycle != nil:
		switch event.Lifecycle.State {
		case SessionRunStateRunning:
			return SessionEventSessionStatusRunning, nil
		case SessionRunStateIdle:
			return SessionEventSessionStatusIdle, nil
		default:
			return "", fmt.Errorf("unknown lifecycle state %q", event.Lifecycle.State)
		}
	case event.Error != nil:
		return SessionEventSessionError, nil
	case event.Span != nil:
		return classifySpanSessionEvent(event.Span)
	case event.Cancel != nil:
		return SessionEventCancel, nil
	case event.Interrupt != nil:
		return SessionEventInterrupt, nil
	case event.Extension != nil:
		if event.Kind == "" {
			return "", errors.New("session extension event must set kind")
		}
		if !strings.HasPrefix(string(event.Kind), SessionEventExtensionPrefix) {
			return "", fmt.Errorf("session extension event kind %q must start with %q", event.Kind, SessionEventExtensionPrefix)
		}
		return event.Kind, nil
	default:
		return "", errors.New("session event has no recognized payload")
	}
}

func countActiveSessionEventPayloads[M MessageType](event *SessionEvent[M]) int {
	if event == nil {
		return 0
	}
	count := 0
	if !isNilMessage(event.Message) {
		count++
	}
	if event.MessageStreamIncomplete != nil {
		count++
	}
	if event.MessagesReplaced != nil {
		count++
	}
	if event.MessageUpdated != nil {
		count++
	}
	if event.MessageInserted != nil {
		count++
	}
	if event.MessagesDeleted != nil {
		count++
	}
	if event.Rollback != nil {
		count++
	}
	if event.Lifecycle != nil {
		count++
	}
	if event.Error != nil {
		count++
	}
	if event.Span != nil {
		count++
	}
	if event.Cancel != nil {
		count++
	}
	if event.Interrupt != nil {
		count++
	}
	if event.Extension != nil {
		count++
	}
	return count
}

func classifySpanSessionEvent(span *SpanEvent) (SessionEventKind, error) {
	if (span.Model != nil) == (span.Tool != nil) {
		return "", errors.New("span event must populate exactly one of Model or Tool")
	}
	switch span.Kind {
	case SpanKindModel:
		if span.Model == nil {
			return "", errors.New("model span requires Span.Model meta")
		}
		switch {
		case !span.StartedAt.IsZero() && span.EndedAt.IsZero():
			return SessionEventSpanModelRequestStart, nil
		case !span.EndedAt.IsZero():
			return SessionEventSpanModelRequestEnd, nil
		default:
			return "", errors.New("model span must have start or end timestamp")
		}
	case SpanKindTool:
		if span.Tool == nil {
			return "", errors.New("tool span requires Span.Tool meta")
		}
		switch {
		case !span.StartedAt.IsZero() && span.EndedAt.IsZero():
			return SessionEventSpanToolCallStart, nil
		case !span.EndedAt.IsZero():
			return SessionEventSpanToolCallEnd, nil
		default:
			return "", errors.New("tool span must have start or end timestamp")
		}
	default:
		return "", fmt.Errorf("unknown span kind %q", span.Kind)
	}
}

// normalizeLoadedSessionEventKind preserves unknown records without a recognized
// payload so logs from older (e.g. "turn_end") or newer writers can be replayed.
func normalizeLoadedSessionEventKind[M MessageType](event *SessionEvent[M]) error {
	if event != nil && event.Kind != "" && !isKnownSessionEventKind(event.Kind) && countActiveSessionEventPayloads(event) == 0 {
		return nil
	}
	return normalizeSessionEventKind(event)
}

// normalizeSessionEventKind fills a missing Kind from a single valid payload.
// Events with an explicit Kind are checked without mutation.
func normalizeSessionEventKind[M MessageType](event *SessionEvent[M]) error {
	kind, err := classifySessionEvent(event)
	if err != nil {
		return err
	}
	if event.Kind == "" {
		event.Kind = kind
	} else if event.Kind != kind {
		return fmt.Errorf("session event kind %q does not match payload %q", event.Kind, kind)
	}
	return nil
}

// validateEmittedSessionEventKind enforces that runtime-emitted session events
// carry an explicit Kind matching their active payload.
func validateEmittedSessionEventKind[M MessageType](event *SessionEvent[M]) error {
	if event == nil {
		return errors.New("nil session event")
	}
	if event.Kind == "" {
		return errors.New("emitted session event must set non-empty Kind")
	}
	return normalizeSessionEventKind(event)
}

func isSessionDurableBoundaryKind(kind SessionEventKind) bool {
	switch kind {
	case SessionEventMessage, SessionEventSessionStatusIdle, SessionEventInterrupt:
		return true
	default:
		return false
	}
}

func normalizeSessionConfig[M MessageType](cfg *SessionConfig[M]) SessionConfig[M] {
	normalized := SessionConfig[M]{
		EventIDGenerator: DefaultSessionEventIDGenerator[M],
	}
	if cfg == nil {
		return normalized
	}
	if cfg.EventIDGenerator != nil {
		normalized.EventIDGenerator = cfg.EventIDGenerator
	}
	if cfg.EventExtraProvider != nil {
		normalized.EventExtraProvider = cfg.EventExtraProvider
	}
	return normalized
}

const sessionEventExtraFrameworkPrefix = "_eino_"

func mergeSessionEventExtra(base, added map[string]any) (map[string]any, error) {
	if len(base) == 0 && len(added) == 0 {
		return nil, nil
	}
	out := make(map[string]any, len(base)+len(added))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range added {
		if strings.HasPrefix(k, sessionEventExtraFrameworkPrefix) {
			return nil, fmt.Errorf("session event extra provider cannot return framework-owned key %q", k)
		}
		out[k] = v
	}
	return out, nil
}

func applySessionEventExtraProvider[M MessageType](
	ctx context.Context,
	event *SessionEvent[M],
	provider SessionEventExtraProvider[M],
) error {
	if event == nil {
		return nil
	}
	if provider == nil {
		return nil
	}
	added, err := provider(ctx, event)
	if err != nil {
		return fmt.Errorf("adk: session event extra provider: %w", err)
	}
	merged, err := mergeSessionEventExtra(event.Extra, added)
	if err != nil {
		return err
	}
	event.Extra = merged
	return nil
}

type sessionEventEnvelopeOptions struct {
	// RequireEventID rejects an empty EventID instead of allocating one.
	// Existing IDs are always retained, regardless of this option.
	RequireEventID bool
}

// prepareSessionEventEnvelope fills Timestamp, merges Extra, and assigns an ID
// when needed. It does not inspect or validate Kind or payloads.
// Callers pass a nil provider for events that were prepared earlier; stream
// materialization may supply a provider while preserving the reserved ID.
func prepareSessionEventEnvelope[M MessageType](
	ctx context.Context,
	event *SessionEvent[M],
	gen SessionEventIDGenerator[M],
	provider SessionEventExtraProvider[M],
	opts sessionEventEnvelopeOptions,
) error {
	if event == nil {
		return nil
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = newEventTimestamp()
	}
	if err := applySessionEventExtraProvider(ctx, event, provider); err != nil {
		return err
	}
	if event.EventID == "" {
		if opts.RequireEventID {
			return ErrInvalidEventID
		}
		if err := assignSessionEventID(ctx, event, gen); err != nil {
			return err
		}
	}
	return nil
}

// prepareReceivedSessionEvent copies an event received from an agent or SendEvent.
// Drafts without an ID may omit Kind; identified events must supply it. Both
// paths check payload consistency before filling metadata. Only drafts run the
// configured callbacks, so forwarding an identified event does not rerun them.
// The copy is returned even on error for the caller's error delivery path.
func prepareReceivedSessionEvent[M MessageType](
	ctx context.Context,
	event *SessionEvent[M],
	sc sessionEventContext[M],
) (*SessionEvent[M], error) {
	if event == nil {
		return nil, nil
	}
	prepared := *event
	stampSessionEventTurnID(&prepared, sc.turnID)
	var gen SessionEventIDGenerator[M]
	var provider SessionEventExtraProvider[M]
	if prepared.EventID == "" {
		if err := normalizeSessionEventKind(&prepared); err != nil {
			return &prepared, err
		}
		gen, provider = sc.generator, sc.provider
	} else if err := validateEmittedSessionEventKind(&prepared); err != nil {
		return &prepared, err
	}
	err := prepareSessionEventEnvelope(ctx, &prepared, gen, provider, sessionEventEnvelopeOptions{})
	return &prepared, err
}

// reserveMessageStreamRef allocates identity before the message is available.
// The temporary callback draft always has Kind=message and no payload; only the
// returned reference is emitted. Materialization must reuse its EventID.
func reserveMessageStreamRef[M MessageType](
	ctx context.Context,
	timestamp time.Time,
	sc sessionEventContext[M],
) (*MessageStreamRef, error) {
	draft := &SessionEvent[M]{Timestamp: timestamp, TurnID: sc.turnID, Kind: SessionEventMessage}
	if err := prepareSessionEventEnvelope(ctx, draft, sc.generator, sc.provider, sessionEventEnvelopeOptions{}); err != nil {
		return nil, err
	}
	return &MessageStreamRef{
		EventID:   draft.EventID,
		TurnID:    draft.TurnID,
		Timestamp: draft.Timestamp,
		Kind:      SessionEventMessage,
	}, nil
}

// assignSessionEventID assigns the EventID of a draft SessionEvent[M] using
// gen, falling back to DefaultSessionEventIDGenerator[M] when gen is nil. It
// is the single authoritative entry point for SessionEvent[M] ID allocation
// in ADK; runner / wrappers paths must route every draft through this helper
// before sending or persisting the event.
//
// Callers MUST construct the draft with EventID == "" and populate all
// other available session-local fields (Kind, payload, timestamp). Streaming
// reservations have Kind=message and no payload yet. A nil event is a no-op.
//
// On generator-side contract violations, the helper returns:
//   - ErrSessionEventIDGeneratorEmpty when gen returns an empty id;
//   - the generator's wrapped error otherwise.
//
// The runner is expected to fail closed on these errors and not append the
// event to the store.
func assignSessionEventID[M MessageType](
	ctx context.Context,
	event *SessionEvent[M],
	gen SessionEventIDGenerator[M],
) error {
	if event == nil {
		return nil
	}
	if gen == nil {
		gen = DefaultSessionEventIDGenerator[M]
	}
	id, err := gen(ctx, event)
	if err != nil {
		return fmt.Errorf("adk: session event id generator: %w", err)
	}
	if id == "" {
		return ErrSessionEventIDGeneratorEmpty
	}
	event.EventID = id
	return nil
}

type sessionEventContextKey[M MessageType] struct{}

type sessionEventContext[M MessageType] struct {
	generator SessionEventIDGenerator[M]
	provider  SessionEventExtraProvider[M]
	turnID    string
}

func contextWithSessionEventContext[M MessageType](ctx context.Context, gen SessionEventIDGenerator[M], provider SessionEventExtraProvider[M], turnID string) context.Context {
	if gen == nil && provider == nil && turnID == "" {
		return ctx
	}
	existing := sessionEventContextFromContext[M](ctx)
	if gen == nil {
		gen = existing.generator
	}
	if provider == nil {
		provider = existing.provider
	}
	if turnID == "" {
		turnID = existing.turnID
	}
	return context.WithValue(ctx, sessionEventContextKey[M]{}, &sessionEventContext[M]{
		generator: gen,
		provider:  provider,
		turnID:    turnID,
	})
}

func sessionEventContextFromContext[M MessageType](ctx context.Context) sessionEventContext[M] {
	if v := ctx.Value(sessionEventContextKey[M]{}); v != nil {
		if sc, ok := v.(*sessionEventContext[M]); ok && sc != nil {
			return *sc
		}
	}
	return sessionEventContext[M]{}
}

func stampSessionEventTurnID[M MessageType](event *SessionEvent[M], turnID string) {
	if event == nil || turnID == "" {
		return
	}
	event.TurnID = turnID
}

type sessionEventPersister[M MessageType] struct {
	ctx     context.Context
	handle  *sessionHandle[M]
	pending []*SessionEvent[M]

	mu  sync.Mutex
	err error
}

func newSessionEventPersister[M MessageType](
	ctx context.Context,
	handle *sessionHandle[M],
) *sessionEventPersister[M] {
	return &sessionEventPersister[M]{
		ctx:    ctx,
		handle: handle,
	}
}

func (p *sessionEventPersister[M]) enqueueAsync(event *SessionEvent[M]) error {
	if event == nil || event.EventID == "" {
		return p.getErr()
	}
	if err := p.getErr(); err != nil {
		return err
	}
	snapshot := cloneSessionEvent(event)
	p.mu.Lock()
	p.pending = append(p.pending, snapshot)
	p.mu.Unlock()
	return nil
}

func (p *sessionEventPersister[M]) commitBoundary(event *SessionEvent[M]) error {
	if event == nil || event.EventID == "" {
		return p.getErr()
	}
	snapshot := cloneSessionEvent(event)
	if err := p.flushPending(); err != nil {
		return err
	}
	return p.appendEvents([]*SessionEvent[M]{snapshot})
}

func (p *sessionEventPersister[M]) flushPending() error {
	if err := p.getErr(); err != nil {
		return err
	}
	p.mu.Lock()
	events := make([]*SessionEvent[M], len(p.pending))
	copy(events, p.pending)
	p.mu.Unlock()
	if len(events) == 0 {
		return nil
	}
	if err := p.appendEvents(events); err != nil {
		return err
	}
	p.mu.Lock()
	p.pending = nil
	p.mu.Unlock()
	return nil
}

func (p *sessionEventPersister[M]) closeAndWait() error {
	return p.flushPending()
}

func (p *sessionEventPersister[M]) appendEvents(events []*SessionEvent[M]) error {
	err := p.handle.appendEvents(p.ctx, events)
	if err != nil {
		p.setErr(err)
		return err
	}
	return nil
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

func stripSessionEventFields[M MessageType](event *TypedAgentEvent[M]) *TypedAgentEvent[M] {
	if event == nil {
		return nil
	}
	if event.SessionEventVariant == nil {
		return event
	}
	stripped := *event
	stripped.SessionEventVariant = nil
	if stripped.Output == nil && stripped.Action == nil && stripped.Err == nil {
		return nil
	}
	return &stripped
}

// applySessionEvent applies a single SessionEvent to the message array, mutating in place.
// Non-message events are ignored.
func applySessionEvent[M MessageType](messages *[]M, event *SessionEvent[M]) error {
	if !isContextSessionEvent(event) {
		return nil
	}
	replay := newMessageReplay(*messages)
	if err := replay.apply(event); err != nil {
		return err
	}
	*messages = replay.visibleMessages()
	return nil
}

func isContextSessionEvent[M MessageType](event *SessionEvent[M]) bool {
	if event == nil {
		return false
	}
	return !isNilMessage(event.Message) || event.MessagesReplaced != nil ||
		event.MessageUpdated != nil || event.MessageInserted != nil || event.MessagesDeleted != nil
}

type messageReplayEntry[M MessageType] struct {
	message M
	deleted bool
}

// messageReplay retains deleted entries as positional tombstones until replay completes.
// Tombstones remain valid insertion anchors, but updates and repeated deletes still
// require a visible target so replay continues to reject corrupted event sequences.
type messageReplay[M MessageType] struct {
	entries []messageReplayEntry[M]
}

func newMessageReplay[M MessageType](messages []M) *messageReplay[M] {
	replay := &messageReplay[M]{entries: make([]messageReplayEntry[M], 0, len(messages))}
	for _, message := range messages {
		replay.entries = append(replay.entries, messageReplayEntry[M]{message: message})
	}
	return replay
}

func (r *messageReplay[M]) apply(event *SessionEvent[M]) error {
	if !isContextSessionEvent(event) {
		return nil
	}

	switch {
	case event.MessagesReplaced != nil:
		r.entries = newMessageReplay(*event.MessagesReplaced).entries

	case event.MessageUpdated != nil:
		upd := event.MessageUpdated
		if replacementID := GetMessageID(upd.Message); replacementID != "" && replacementID != upd.MessageID {
			return fmt.Errorf("apply event: MessageUpdated target %q but replacement has ID %q — identity mismatch", upd.MessageID, replacementID)
		}
		for i := range r.entries {
			if !r.entries[i].deleted && GetMessageID(r.entries[i].message) == upd.MessageID {
				r.entries[i].message = upd.Message
				return nil
			}
		}
		return fmt.Errorf("reconstruct: target message %q not found for update", upd.MessageID)

	case event.MessageInserted != nil:
		ins := event.MessageInserted
		entry := messageReplayEntry[M]{message: ins.Message}
		if ins.BeforeMessageID == "" {
			r.entries = append(r.entries, entry)
			return nil
		}
		for i := range r.entries {
			if GetMessageID(r.entries[i].message) == ins.BeforeMessageID {
				r.entries = append(r.entries, messageReplayEntry[M]{})
				copy(r.entries[i+1:], r.entries[i:])
				r.entries[i] = entry
				return nil
			}
		}
		return fmt.Errorf("apply event: anchor message %q not found for insertion", ins.BeforeMessageID)

	case event.MessagesDeleted != nil:
		ids := event.MessagesDeleted.MessageIDs
		if err := validateMessageIDs("MessagesDeleted.MessageIDs", ids); err != nil {
			return err
		}
		targets := make(map[string]struct{}, len(ids))
		for _, id := range ids {
			targets[id] = struct{}{}
		}
		found := make(map[string]struct{}, len(ids))
		for i := range r.entries {
			if r.entries[i].deleted {
				continue
			}
			if _, ok := targets[GetMessageID(r.entries[i].message)]; ok {
				found[GetMessageID(r.entries[i].message)] = struct{}{}
			}
		}
		for _, id := range ids {
			if _, ok := found[id]; !ok {
				return fmt.Errorf("reconstruct: target message %q not found for deletion", id)
			}
		}
		for i := range r.entries {
			if _, ok := targets[GetMessageID(r.entries[i].message)]; ok {
				r.entries[i].deleted = true
			}
		}

	default:
		if !isNilMessage(event.Message) {
			r.entries = append(r.entries, messageReplayEntry[M]{message: event.Message})
		}
	}
	return nil
}

func (r *messageReplay[M]) visibleMessages() []M {
	messages := make([]M, 0, len(r.entries))
	for _, entry := range r.entries {
		if !entry.deleted {
			messages = append(messages, entry.message)
		}
	}
	return messages
}

func validateMessageIDs(field string, ids []string) error {
	if len(ids) == 0 {
		return fmt.Errorf("%s must not be empty", field)
	}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id == "" {
			return fmt.Errorf("%s must not contain empty message ID", field)
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("%s contains duplicate message ID %q", field, id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

type sessionReconstructResult[M MessageType] struct {
	state *reconstructedSessionState[M]
}

// sessionReplayEventKinds is the set of event kinds required to project the active
// session log and reconstruct model-facing state.
var sessionReplayEventKinds = []SessionEventKind{
	SessionEventMessage,
	SessionEventMessagesReplaced,
	SessionEventMessageUpdated,
	SessionEventMessageInserted,
	SessionEventMessagesDeleted,
	SessionEventSessionStatusIdle,
	SessionEventInterrupt,
	SessionEventCancel,
	SessionEventRollback,
}

type RollbackSessionOptions[M MessageType] struct {
	CheckPointStore     CheckPointStore
	ExpectedHeadEventID string
	EventIDGenerator    SessionEventIDGenerator[M]
}

type RollbackSessionOption[M MessageType] func(*RollbackSessionOptions[M])

// WithRollbackSessionCheckPointStore deletes session-derived checkpoints after a successful rollback.
func WithRollbackSessionCheckPointStore[M MessageType](store CheckPointStore) RollbackSessionOption[M] {
	return func(opts *RollbackSessionOptions[M]) {
		opts.CheckPointStore = store
	}
}

// WithRollbackSessionExpectedHeadEventID requires the current active committed
// idle event to match eventID before rollback.
func WithRollbackSessionExpectedHeadEventID[M MessageType](eventID string) RollbackSessionOption[M] {
	return func(opts *RollbackSessionOptions[M]) {
		opts.ExpectedHeadEventID = eventID
	}
}

// WithRollbackEventIDGenerator overrides the EventID generator for the rollback
// event. The generator sees the fully-populated rollback draft (kind,
// SessionRollbackEvent payload, timestamp) before assignment. If nil or not set,
// DefaultSessionEventIDGenerator[M] (UUID v4) is used.
func WithRollbackEventIDGenerator[M MessageType](gen SessionEventIDGenerator[M]) RollbackSessionOption[M] {
	return func(opts *RollbackSessionOptions[M]) {
		opts.EventIDGenerator = gen
	}
}

// RollbackSession appends a rollback marker that makes the committed idle event
// with targetEventID the latest active committed boundary.
func RollbackSession[M MessageType](
	ctx context.Context,
	store SessionEventStore[M],
	sessionID string,
	targetEventID string,
	opts ...RollbackSessionOption[M],
) error {
	if store == nil {
		return errors.New("adk: rollback session store is nil")
	}
	if sessionID == "" {
		return errors.New("adk: rollback sessionID is empty")
	}
	if targetEventID == "" {
		return ErrRollbackTargetNotFound
	}
	var cfg RollbackSessionOptions[M]
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	handle, err := openRunnerSession[M](ctx, store, sessionID)
	if err != nil {
		return err
	}
	defer handle.close(ctx)

	activeEvents, err := loadActiveSessionEventsReverse[M](ctx, handle, defaultLoadPageSize)
	if err != nil {
		return err
	}
	target, head, err := resolveRollbackTarget[M](activeEvents, targetEventID)
	if err != nil {
		if errors.Is(err, ErrRollbackTargetNotFound) {
			evidence, evidenceErr := findPhysicalRollbackTargetEvidence[M](ctx, handle, targetEventID, defaultLoadPageSize)
			if evidenceErr != nil {
				return evidenceErr
			}
			switch evidence {
			case rollbackTargetEvidenceCommitted:
				return ErrRollbackTargetInactive
			case rollbackTargetEvidenceUncommitted:
				return ErrInvalidRollbackTarget
			}
		}
		return err
	}
	if cfg.ExpectedHeadEventID != "" && (head == nil || head.EventID != cfg.ExpectedHeadEventID) {
		return ErrSessionHeadChanged
	}

	rb := &SessionEvent[M]{
		Timestamp: newEventTimestamp(),
		Kind:      SessionEventRollback,
		Rollback: &SessionRollbackEvent{
			ToEventID:                 target.EventID,
			PreviousHeadCommitEventID: head.EventID,
		},
	}
	if err := assignSessionEventID(ctx, rb, cfg.EventIDGenerator); err != nil {
		return err
	}
	if err := handle.appendEvents(ctx, []*SessionEvent[M]{rb}); err != nil {
		return err
	}
	if cfg.CheckPointStore != nil {
		if deleter, ok := cfg.CheckPointStore.(CheckPointDeleter); ok {
			if err := deleter.Delete(ctx, sessionRunnerCheckpointID(sessionID)); err != nil {
				return fmt.Errorf("failed to delete session checkpoint after rollback: %w", err)
			}
		}
	}
	return nil
}

// reconstructSessionState rebuilds session state by replaying the active log.
// Committed idle lifecycle events are loaded for rollback projection but are not
// applied as reconstructed state.
func reconstructSessionState[M MessageType](
	ctx context.Context,
	handle *sessionHandle[M],
	pageSize int,
) (*sessionReconstructResult[M], error) {
	allEvents, err := loadSessionEventsForReconstructionReverse[M](ctx, handle, pageSize)
	if err != nil {
		return nil, err
	}
	if len(allEvents) == 0 {
		return nil, nil
	}

	state, err := replayDurableContextEvents(allEvents)
	if err != nil {
		return nil, err
	}
	return &sessionReconstructResult[M]{state: state}, nil
}

func loadSessionEventsForReconstructionReverse[M MessageType](
	ctx context.Context,
	handle *sessionHandle[M],
	pageSize int,
) ([]*SessionEvent[M], error) {
	return loadProjectedSessionEventsReverse(ctx, handle, pageSize, true)
}

func loadActiveSessionEventsReverse[M MessageType](
	ctx context.Context,
	handle *sessionHandle[M],
	pageSize int,
) ([]*SessionEvent[M], error) {
	return loadProjectedSessionEventsReverse(ctx, handle, pageSize, false)
}

func loadProjectedSessionEventsReverse[M MessageType](
	ctx context.Context,
	handle *sessionHandle[M],
	pageSize int,
	stopAtMessagesReplaced bool,
) ([]*SessionEvent[M], error) {
	if pageSize <= 0 {
		pageSize = defaultLoadPageSize
	}
	var physicalReverse []*SessionEvent[M]
	var after string
	var sawRollback bool
	for {
		result, err := handle.loadEvents(ctx, &LoadSessionEventsRequest{
			After:   after,
			Limit:   pageSize,
			Reverse: true,
			Kinds:   sessionReplayEventKinds,
		})
		if err != nil {
			return nil, err
		}
		if result == nil || len(result.Events) == 0 {
			break
		}

		stopAt := len(result.Events)
		foundBoundary := false
		if stopAtMessagesReplaced && !sawRollback {
			for i, event := range result.Events {
				// A newer rollback may project the active head before this replacement,
				// so reconstruction needs the complete physical history.
				if event.Kind == SessionEventRollback {
					sawRollback = true
					break
				}
				if event.MessagesReplaced != nil {
					stopAt = i + 1
					foundBoundary = true
					break
				}
			}
		}
		physicalReverse = append(physicalReverse, result.Events[:stopAt]...)
		if foundBoundary {
			break
		}
		if result.Next == "" {
			break
		}
		after = result.Next
	}
	return projectActiveEventsFromReverse(physicalReverse)
}

func projectActiveEventsFromReverse[M MessageType](
	physicalReverse []*SessionEvent[M],
) ([]*SessionEvent[M], error) {
	active := make([]*SessionEvent[M], 0, len(physicalReverse))
	activeLen := 0
	posByEventID := make(map[string]int, len(physicalReverse))
	for i := len(physicalReverse) - 1; i >= 0; i-- {
		event := physicalReverse[i]
		if event.Kind == SessionEventRollback {
			rb, err := decodeRollbackSessionEvent(event)
			if err != nil {
				return nil, err
			}
			pos, ok := posByEventID[rb.ToEventID]
			if !ok || pos >= activeLen || active[pos].EventID != rb.ToEventID {
				return nil, ErrRollbackTargetInactive
			}
			target := active[pos]
			if !isCommittedIdleEvent(target) {
				return nil, ErrInvalidRollbackTarget
			}
			activeLen = pos + 1
			continue
		}
		if activeLen < len(active) {
			active[activeLen] = event
			active = active[:activeLen+1]
		} else {
			active = append(active, event)
		}
		posByEventID[event.EventID] = activeLen
		activeLen++
	}
	if activeLen < len(active) {
		active = active[:activeLen]
	}
	return active, nil
}

type rollbackTargetEvidence int

const (
	rollbackTargetEvidenceNone rollbackTargetEvidence = iota
	rollbackTargetEvidenceUncommitted
	rollbackTargetEvidenceCommitted
)

func findPhysicalRollbackTargetEvidence[M MessageType](
	ctx context.Context,
	handle *sessionHandle[M],
	targetEventID string,
	pageSize int,
) (rollbackTargetEvidence, error) {
	if pageSize <= 0 {
		pageSize = defaultLoadPageSize
	}
	var after string
	var evidence rollbackTargetEvidence
	for {
		result, err := handle.loadEvents(ctx, &LoadSessionEventsRequest{
			After:   after,
			Limit:   pageSize,
			Reverse: false,
			Kinds:   sessionReplayEventKinds,
		})
		if err != nil {
			return rollbackTargetEvidenceNone, err
		}
		if result == nil || len(result.Events) == 0 {
			break
		}
		for _, event := range result.Events {
			if event.Kind == SessionEventRollback {
				continue
			}
			if event.EventID != targetEventID {
				continue
			}
			if isCommittedIdleEvent(event) {
				return rollbackTargetEvidenceCommitted, nil
			}
			evidence = rollbackTargetEvidenceUncommitted
		}
		if result.Next == "" {
			break
		}
		after = result.Next
	}
	return evidence, nil
}

func decodeRollbackSessionEvent[M MessageType](event *SessionEvent[M]) (*SessionRollbackEvent, error) {
	if event == nil || event.EventID == "" || event.Kind != SessionEventRollback || event.Rollback == nil {
		return nil, ErrInvalidRollbackTarget
	}
	if event.Rollback.ToEventID == "" {
		return nil, ErrInvalidRollbackTarget
	}
	return event.Rollback, nil
}

func resolveRollbackTarget[M MessageType](
	activeEvents []*SessionEvent[M],
	targetEventID string,
) (target *SessionEvent[M], head *SessionEvent[M], err error) {
	var sawTargetEvent bool
	for _, event := range activeEvents {
		if event != nil && event.EventID == targetEventID {
			sawTargetEvent = true
		}
		if !isCommittedIdleEvent(event) {
			continue
		}
		head = event
		if event.EventID == targetEventID {
			target = event
		}
	}
	if target != nil {
		return target, head, nil
	}
	if sawTargetEvent {
		return nil, nil, ErrInvalidRollbackTarget
	}
	return nil, nil, ErrRollbackTargetNotFound
}

func replayDurableContextEvents[M MessageType](events []*SessionEvent[M]) (*reconstructedSessionState[M], error) {
	if len(events) == 0 {
		return nil, nil
	}
	var messages []M
	startIdx := 0
	boundaryIdx := -1
	for i := 0; i < len(events); i++ {
		if events[i].MessagesReplaced != nil {
			boundaryIdx = i
		}
	}

	if boundaryIdx >= 0 {
		messages = append([]M{}, *events[boundaryIdx].MessagesReplaced...)
		startIdx = boundaryIdx + 1
	}

	replay := newMessageReplay(messages)
	state := &reconstructedSessionState[M]{Messages: messages}
	for i := startIdx; i < len(events); i++ {
		if err := replay.apply(events[i]); err != nil {
			return nil, fmt.Errorf("reconstruct: %w", err)
		}
	}
	state.Messages = replay.visibleMessages()
	reorderParallelToolResults(state.Messages)
	return state, nil
}

// reorderParallelToolResults canonicalizes reconstructed model context without
// changing the completion-ordered session log or live event delivery. It only
// reorders results belonging to the preceding multi-tool assistant message;
// unrecognized results keep their original positions.
func reorderParallelToolResults[M MessageType](messages []M) {
	for i := 0; i < len(messages); i++ {
		callIDs, ok := messageToolCallIDs(messages[i])
		if !ok || len(callIDs) < 2 {
			continue
		}

		resultStart := i + 1
		resultEnd := resultStart
		resultIDs := make([]string, 0, len(callIDs))
		for resultEnd < len(messages) {
			callID, isResult := messageToolResultCallID(messages[resultEnd])
			if !isResult {
				break
			}
			resultIDs = append(resultIDs, callID)
			resultEnd++
		}
		if len(resultIDs) < 2 {
			continue
		}

		knownCallIDs := make(map[string]struct{}, len(callIDs))
		validGroup := true
		for _, callID := range callIDs {
			if callID == "" {
				validGroup = false
				break
			}
			if _, exists := knownCallIDs[callID]; exists {
				validGroup = false
				break
			}
			knownCallIDs[callID] = struct{}{}
		}
		if !validGroup {
			continue
		}
		resultsByCallID := make(map[string][]M, len(resultIDs))
		knownResultCount := 0
		for offset, callID := range resultIDs {
			if _, exists := knownCallIDs[callID]; exists {
				resultsByCallID[callID] = append(resultsByCallID[callID], messages[resultStart+offset])
				knownResultCount++
			}
		}
		if knownResultCount < 2 {
			continue
		}

		ordered := make([]M, 0, knownResultCount)
		for _, callID := range callIDs {
			ordered = append(ordered, resultsByCallID[callID]...)
		}
		orderedIndex := 0
		for offset, callID := range resultIDs {
			if _, exists := knownCallIDs[callID]; !exists {
				continue
			}
			messages[resultStart+offset] = ordered[orderedIndex]
			orderedIndex++
		}
		i = resultEnd - 1
	}
}

func messageToolCallIDs[M MessageType](message M) ([]string, bool) {
	switch msg := any(message).(type) {
	case *schema.Message:
		if msg == nil || msg.Role != schema.Assistant || len(msg.ToolCalls) == 0 {
			return nil, false
		}
		callIDs := make([]string, 0, len(msg.ToolCalls))
		for _, toolCall := range msg.ToolCalls {
			callIDs = append(callIDs, toolCall.ID)
		}
		return callIDs, true
	case *schema.AgenticMessage:
		if msg == nil || msg.Role != schema.AgenticRoleTypeAssistant {
			return nil, false
		}
		var callIDs []string
		for _, block := range msg.ContentBlocks {
			if block != nil && block.Type == schema.ContentBlockTypeFunctionToolCall && block.FunctionToolCall != nil {
				callIDs = append(callIDs, block.FunctionToolCall.CallID)
			}
		}
		return callIDs, len(callIDs) > 0
	default:
		return nil, false
	}
}

func messageToolResultCallID[M MessageType](message M) (string, bool) {
	switch msg := any(message).(type) {
	case *schema.Message:
		if msg == nil || msg.Role != schema.Tool {
			return "", false
		}
		return msg.ToolCallID, true
	case *schema.AgenticMessage:
		if msg == nil || msg.Role != schema.AgenticRoleTypeUser || len(msg.ContentBlocks) != 1 {
			return "", false
		}
		block := msg.ContentBlocks[0]
		if block == nil {
			return "", false
		}
		switch block.Type {
		case schema.ContentBlockTypeFunctionToolResult:
			if block.FunctionToolResult != nil {
				return block.FunctionToolResult.CallID, true
			}
		case schema.ContentBlockTypeToolSearchResult:
			if block.ToolSearchFunctionToolResult != nil {
				return block.ToolSearchFunctionToolResult.CallID, true
			}
		}
		return "", false
	default:
		return "", false
	}
}

func isCommittedIdleEvent[M MessageType](event *SessionEvent[M]) bool {
	return event != nil &&
		event.Kind == SessionEventSessionStatusIdle &&
		event.Lifecycle != nil &&
		event.Lifecycle.State == SessionRunStateIdle &&
		event.Lifecycle.StopReason != nil &&
		event.Lifecycle.StopReason.Type == StopReasonEndTurn
}
