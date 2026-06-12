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

const (
	defaultLoadPageSize          = 100
	defaultSessionAcquireTimeout = 5 * time.Second
)

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

var ErrRollbackTargetNotFound = errors.New("adk: rollback target turn not found")
var ErrInvalidRollbackTarget = errors.New("adk: invalid rollback target")
var ErrRollbackTargetInactive = errors.New("adk: rollback target is not active")
var ErrSessionHeadChanged = errors.New("adk: session committed turn_end head changed")
var ErrSessionBusy = errors.New("adk: session already has an active handle")
var ErrSessionFencingTokenRequired = errors.New("adk: session fencing token required")
var ErrSessionFencingTokenUnsupported = errors.New("adk: session fencing token unsupported")
var ErrSessionTailMismatch = errors.New("adk: session tail does not match expected tail")
var ErrDuplicateEventID = errors.New("adk: duplicate session event_id")
var ErrSessionFencingTokenInvalid = errors.New("adk: session handle fencing token is not current")
var ErrSessionFencingTokenExpired = errors.New("adk: session handle fencing token expired")

type SessionBusyError struct {
	ExpiresAt time.Time
}

func (e *SessionBusyError) Error() string { return ErrSessionBusy.Error() }
func (e *SessionBusyError) Unwrap() error { return ErrSessionBusy }

const (
	sessionRunnerCheckpointSuffix = "/runner_checkpoint"
)

// SessionEventStore is the provider-facing interface for a typed session event log.
// It is suitable for local development, tests, and single-process deployments
// when wrapped by NewLocalSessionService.
type SessionEventStore[M MessageType] interface {
	LoadEvents(ctx context.Context, req *LoadSessionEventsRequest) (*LoadSessionEventsResult[M], error)
	AppendEvents(ctx context.Context, req *AppendSessionEventsRequest[M]) (*AppendSessionEventsResult, error)
}

// FencedSessionEventStore is the provider-facing event log interface for
// production multi-process session ownership. AppendEventsFenced must validate
// the fencing token and expected tail in the same atomic append operation that
// writes events.
type FencedSessionEventStore[M MessageType] interface {
	LoadEvents(ctx context.Context, req *LoadSessionEventsRequest) (*LoadSessionEventsResult[M], error)
	AppendEventsFenced(ctx context.Context, req *FencedAppendSessionEventsRequest[M]) (*AppendSessionEventsResult, error)
}

// SessionFencingTokenFunc returns the current opaque fencing token for one
// externally-owned session ownership epoch.
//
// Runner calls this function only when it is about to append events through a
// fenced session service. It does not call the function while opening a session
// or loading events, and it does not manage token renewal or release. The owner
// that coordinates the session, such as a TurnLoop or application scheduler, is
// responsible for the token lifecycle.
type SessionFencingTokenFunc func(ctx context.Context) (string, error)

// FencedAppendSessionEventsRequest is the provider-facing append request for a
// fenced event store.
//
// AppendEventsFenced must validate FencingToken, ExpectedSessionTailEventID, and
// the event append in the same atomic append operation. If the expected tail does
// not match the current session tail, providers may return success only when the
// already-persisted events after ExpectedSessionTailEventID exactly match the
// requested EventID sequence and the current tail is the last requested EventID.
type FencedAppendSessionEventsRequest[M MessageType] struct {
	// SessionID identifies the session log to append to.
	SessionID string
	// FencingToken is an opaque owner proof supplied by SessionFencingTokenFunc.
	FencingToken string
	// ExpectedSessionTailEventID is the session tail that the caller observed
	// before this append. Empty means the caller expects an empty session log.
	ExpectedSessionTailEventID string
	// Events are appended as one batch. Each EventID must be non-empty and
	// unique within the session.
	Events []*SessionEvent[M]
}

// SessionService is the sealed runtime adapter consumed by Runner.
// External providers should implement SessionEventStore or FencedSessionEventStore
// and use NewLocalSessionService or NewFencedSessionService instead of
// implementing SessionService directly.
type SessionService[M MessageType] interface {
	openSession(ctx context.Context, req *openSessionRequest) (*openSessionResult[M], error)
}

type openSessionRequest struct {
	sessionID    string
	fencingToken SessionFencingTokenFunc
}

type openSessionResult[M MessageType] struct {
	handle sessionHandle[M]
}

type sessionHandle[M MessageType] interface {
	loadEvents(ctx context.Context, req *LoadSessionEventsRequest) (*LoadSessionEventsResult[M], error)
	appendEvents(ctx context.Context, req *AppendSessionEventsRequest[M]) (*AppendSessionEventsResult, error)
	currentTailEventID() string
	close(ctx context.Context) error
}

// LoadSessionEventsRequest configures typed event loading pagination and direction.
type LoadSessionEventsRequest struct {
	// SessionID identifies the session log to load.
	SessionID string
	// After is the last-seen event_id used as an exclusive append-position cursor.
	After string
	// Limit is the maximum number of events to return. 0 means no limit.
	Limit int
	// Reverse, when true, returns events in newest-first order.
	Reverse bool
	// Kinds filters events by their Kind field. Empty means no kind filter.
	Kinds []SessionEventKind
	// IncludeSessionTail requests that the store populate
	// LoadSessionEventsResult.SessionTailEventID for this load. Callers set it
	// only when they will append based on this load and therefore need the
	// authoritative log tail observed in the same snapshot as Events; the
	// reconstruct path sets it on the first (newest) reverse page only.
	//
	// When false, a store may leave SessionTailEventID empty to avoid the extra
	// work of resolving the tail (for example, a separate tail query in a
	// SQL-backed store). Stores for which the tail is free to compute may ignore
	// this flag and always populate it.
	IncludeSessionTail bool
}

// LoadSessionEventsResult is the response from SessionEventStore.LoadEvents.
type LoadSessionEventsResult[M MessageType] struct {
	// Events are typed SessionEvent values owned by the caller.
	Events []*SessionEvent[M]
	// Next is the event_id of the last event in this page in the direction of travel.
	Next string
	// SessionTailEventID is the last event_id visible in the session log snapshot
	// used for this load. It is the tail of the entire log snapshot, independent
	// of the request's Kinds, Limit, Reverse, and After fields; do not infer it
	// from Events.
	//
	// It is populated only when the request set IncludeSessionTail (a store may
	// always populate it when the tail is free to compute). When
	// IncludeSessionTail was set, empty means the visible session log is empty;
	// when it was not set, empty carries no information about the log.
	SessionTailEventID string
}

type AppendSessionEventsRequest[M MessageType] struct {
	// SessionID identifies the session log to append to.
	SessionID string
	// ExpectedSessionTailEventID is the session tail that the caller observed
	// before this append. Empty means the caller expects an empty session log.
	ExpectedSessionTailEventID string
	// Events are appended as one batch. Each EventID must be non-empty and
	// unique within the session.
	Events []*SessionEvent[M]
}

// AppendSessionEventsResult reports the new durable session tail after an
// append or exact batch replay.
type AppendSessionEventsResult struct {
	SessionTailEventID string
}

// SessionEvent is the JSON-serializable persistence format for session events.
// Exactly one semantic content field is active per event. The MessagesReplaced field
// uses pointer-to-slice semantics (nil = absent, non-nil = active replacement).
//
// TurnEndState is persisted as a SessionEvent with the TurnEnd field set. The Messages
// field within TurnEnd is intentionally left nil — messages are reconstructed from the
// event log on read.
type SessionEvent[M MessageType] struct {
	// SessionID identifies the session timeline this event belongs to. Runner-owned
	// root events use the runner SessionID; nested AgentTool events use their
	// synthetic child SessionID so live consumers can distinguish event ownership.
	SessionID string `json:"session_id,omitempty"`

	// EventID is the canonical, session-unique identity of this event.
	// Assigned exactly once by the Runner at event materialization
	// (in makeInputSessionEvent / toSessionEvent). Persister-level retries
	// re-send the same payload bytes and therefore the same EventID, which is
	// what enables AppendEvents idempotency. Runner-allocated EventIDs are
	// UUIDv4 strings; SessionService implementations treat EventID as an opaque
	// non-empty string and do NOT enforce UUIDv4 format.
	//
	// Distinct from MessageUpdatedEvent.MessageID: EventID identifies the
	// session event envelope; MessageID identifies a logical message inside
	// the session message array.
	EventID string `json:"event_id"`

	// Timestamp is inherited from the source AgentEvent and represents the event
	// occurrence time, not the SessionService persistence time.
	Timestamp time.Time `json:"timestamp,omitempty"`

	Kind SessionEventKind `json:"kind,omitempty"`

	// TurnID groups all events belonging to a single logical turn. A fresh Run
	// assigns a new UUID; a Resume preserves the original TurnID so downstream
	// consumers can correlate the entire turn (including the interrupted prefix
	// and the resumed suffix) as one unit.
	TurnID string `json:"turn_id,omitempty"`

	Message          M                        `json:"message,omitempty"`
	MessagesReplaced *[]M                     `json:"messages_replaced,omitempty"`
	MessageUpdated   *MessageUpdatedEvent[M]  `json:"message_updated,omitempty"`
	MessageInserted  *MessageInsertedEvent[M] `json:"message_inserted,omitempty"`
	MessagesDeleted  *MessagesDeletedEvent    `json:"messages_deleted,omitempty"`
	TurnEnd          *TurnEndState[M]         `json:"turn_end,omitempty"`
	Rollback         *SessionRollbackEvent    `json:"rollback,omitempty"`

	Lifecycle *LifecycleEvent    `json:"lifecycle,omitempty"`
	Error     *SessionErrorEvent `json:"error,omitempty"`
	Span      *SpanEvent         `json:"span,omitempty"`

	UserObservation *UserObservationEvent  `json:"user_observation,omitempty"`
	AgentInterrupt  *AgentInterruptEvent   `json:"agent_interrupt,omitempty"`
	Extension       *SessionExtensionEvent `json:"extension,omitempty"`
}

type SessionEventKind string

const (
	SessionEventMessage          SessionEventKind = "message"
	SessionEventMessagesReplaced SessionEventKind = "messages_replaced"
	SessionEventMessageUpdated   SessionEventKind = "message_updated"
	SessionEventMessageInserted  SessionEventKind = "message_inserted"
	SessionEventMessagesDeleted  SessionEventKind = "messages_deleted"
	SessionEventTurnEnd          SessionEventKind = "turn_end"
	SessionEventRollback         SessionEventKind = "rollback"

	SessionEventSessionStatusRunning     SessionEventKind = "session.status_running"
	SessionEventSessionStatusIdle        SessionEventKind = "session.status_idle"
	SessionEventSessionStatusRescheduled SessionEventKind = "session.status_rescheduled"
	SessionEventSessionError             SessionEventKind = "session.error"

	SessionEventSpanModelRequestStart SessionEventKind = "span.model_request_start"
	SessionEventSpanModelRequestEnd   SessionEventKind = "span.model_request_end"
	SessionEventSpanToolCallStart     SessionEventKind = "span.tool_call_start"
	SessionEventSpanToolCallEnd       SessionEventKind = "span.tool_call_end"

	SessionEventUserInterrupt  SessionEventKind = "user.interrupt"
	SessionEventAgentInterrupt SessionEventKind = "agent.interrupt"

	SessionEventExtensionPrefix = "x."
)

type LifecycleEvent struct {
	Scope      LifecycleScope  `json:"scope,omitempty"`
	State      SessionRunState `json:"state,omitempty"`
	StopReason *StopReason     `json:"stop_reason,omitempty"`
}

type SessionRollbackEvent struct {
	ToEventID             string `json:"to_event_id"`
	ToTurnID              string `json:"to_turn_id,omitempty"`
	PreviousHeadTurnEndID string `json:"previous_head_turn_end_id,omitempty"`
	PreviousHeadTurnID    string `json:"previous_head_turn_id,omitempty"`
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
	Type string `json:"type,omitempty"`
}

type SpanEvent struct {
	SpanID       string `json:"span_id"`
	ParentSpanID string `json:"parent_span_id,omitempty"`

	Kind SpanKind `json:"kind"`
	Name string   `json:"name,omitempty"`

	StartedAt time.Time `json:"started_at,omitempty"`
	EndedAt   time.Time `json:"ended_at,omitempty"`

	TTFTMS int64 `json:"ttft_ms,omitempty"`

	Status string `json:"status,omitempty"`
	Err    string `json:"err,omitempty"`

	// Model and Tool are mutually exclusive: exactly one must be non-nil for
	// every Span-carrying SessionEvent. ClassifySessionEvent enforces this
	// invariant.
	Model *ModelSpanMeta `json:"model,omitempty"`
	Tool  *ToolSpanMeta  `json:"tool,omitempty"`
}

type SpanKind string

const (
	SpanKindModel SpanKind = "model"
	SpanKindTool  SpanKind = "tool"
)

type ModelSpanMeta struct {
	Provider string `json:"provider,omitempty"`
	// Model is the model name from options (model.WithModel). Best-effort: empty
	// if the user configures model name directly on the ChatModel implementation
	// without passing model.WithModel in call-site options.
	Model                    string            `json:"model,omitempty"`
	Attempt                  int               `json:"attempt,omitempty"`
	ModelRequestStartEventID string            `json:"model_request_start_event_id,omitempty"`
	Usage                    *ModelUsage       `json:"usage,omitempty"`
	FinishReason             string            `json:"finish_reason,omitempty"`
	Timeout                  *ModelTimeoutMeta `json:"timeout,omitempty"`
	Accepted                 bool              `json:"accepted"`
}

// ModelTimeoutMeta records timeout details for a model span that ended with a ModelTimeoutError.
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

type ModelUsage struct {
	InputTokens              int                `json:"input_tokens,omitempty"`
	OutputTokens             int                `json:"output_tokens,omitempty"`
	CacheCreationInputTokens int                `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int                `json:"cache_read_input_tokens,omitempty"`
	Raw                      *schema.TokenUsage `json:"raw,omitempty"`
}

// ToolSpanMeta carries the operational metadata of a single tool call span.
// Inputs and outputs are NOT recorded here — they live on the assistant
// message and the tool result message respectively. The span is a stable
// identity envelope that joins those two messages together with timing
// and status.
//
// Tool spans for permission-gated calls may straddle multiple Run/Resume
// invocations: the start span fires on the run where the call begins
// (typically before the user is asked), and the end span fires on the run
// where the call completes (after the user has approved/rejected/responded).
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

type UserObservationEvent struct {
	Interrupt *UserInterruptEvent `json:"interrupt,omitempty"`
}

type UserInterruptEvent struct {
	Reason string `json:"reason,omitempty"`
}

// AgentInterruptEvent records a business interrupt in the durable session timeline.
type AgentInterruptEvent struct {
	// Contexts is the set of interrupt contexts that caused the agent to pause.
	// Each element represents a single root-cause interrupt point.
	Contexts []*AgentInterruptContext `json:"contexts,omitempty"`
}

// AgentInterruptContext describes a single interrupt point within a batch.
type AgentInterruptContext struct {
	// InterruptID is the fully-qualified address of the interrupt point
	// (e.g. "agent:A;tool:lookup:call_1"). Use this as the key in ResumeParams.Targets.
	InterruptID string `json:"interrupt_id,omitempty"`
	// Info is the business-defined payload describing the interrupt, provided by
	// the component that triggered it (e.g. a middleware or a custom tool).
	// ADK treats it as opaque; consumers type-assert it to the concrete type the
	// triggering component documents (e.g. *permission.AskInfo) to determine how
	// to handle the interrupt.
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

// MessagesDeletedEvent represents a batch deletion within the messages array.
type MessagesDeletedEvent struct {
	// MessageIDs identifies the messages to delete via their eino-internal message IDs.
	MessageIDs []string `json:"message_ids"`
}

// SessionEventIDGenerator returns the EventID for a draft SessionEvent[M].
//
// Generators see the fully-populated draft (Kind, Message, Span, Extension,
// SessionID, TurnID, ...) and may return a business-side identifier such as
// the matching application order/job/result ID. When a generator does not
// recognize a draft event, it should fall through to
// DefaultSessionEventIDGenerator[M] rather than allocating a UUID directly,
// so that the default behavior stays consistent with the framework default.
//
// A returned empty event_id is treated as a generator-side contract violation
// (ErrSessionEventIDGeneratorEmpty); the runner fails closed before the
// event is appended to the store.
type SessionEventIDGenerator[M MessageType] func(ctx context.Context, event *SessionEvent[M]) (string, error)

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

// SessionConfig tunes managed-session admission and event identity.
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
	// SessionAcquireTimeout bounds how long Runner may wait to acquire any session
	// handle before failing the current Run/Resume/Rollback attempt.
	//
	// This is not a fenced-only option and does not configure the fenced handle's
	// fencing token TTL. It applies to the session admission path in both local
	// and fenced services.
	SessionAcquireTimeout time.Duration
}

// TurnEndState is the agent-visible state materialized at a successful turn boundary.
type TurnEndState[M MessageType] struct {
	Messages          []M
	ToolInfos         []*schema.ToolInfo
	DeferredToolInfos []*schema.ToolInfo
	SessionValues     map[string]any
}

type runnerSessionCheckpoint struct {
	SessionID          string
	TurnID             string
	CheckPointID       string
	SessionTailEventID string
	Payload            []byte
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
	schema.RegisterName[*MessagesDeletedEvent]("_eino_adk_messages_deleted_event")
	schema.RegisterName[*LifecycleEvent]("_eino_adk_lifecycle_event")
	schema.RegisterName[*SessionErrorEvent]("_eino_adk_session_error_event")
	schema.RegisterName[*RetryStatus]("_eino_adk_retry_status")
	schema.RegisterName[*SpanEvent]("_eino_adk_span_event")
	schema.RegisterName[*ModelSpanMeta]("_eino_adk_model_span_meta")
	schema.RegisterName[*ModelTimeoutMeta]("_eino_adk_model_timeout_meta")
	schema.RegisterName[*ModelUsage]("_eino_adk_model_usage")
	schema.RegisterName[*ToolSpanMeta]("_eino_adk_tool_span_meta")
	schema.RegisterName[*UserObservationEvent]("_eino_adk_user_observation_event")
	schema.RegisterName[*UserInterruptEvent]("_eino_adk_user_interrupt_event")
	schema.RegisterName[*AgentInterruptEvent]("_eino_adk_agent_interrupt_event")
	schema.RegisterName[*AgentInterruptContext]("_eino_adk_agent_interrupt_context")
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
	if err := NormalizeSessionEventKind(event); err != nil {
		return nil, err
	}
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

func snapshotSessionEvent[M MessageType](event *SessionEvent[M]) (*SessionEvent[M], error) {
	data, err := encodeSessionEvent(event)
	if err != nil {
		return nil, err
	}
	return decodeSessionEvent[M](data)
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
// assignSessionEventIDFromContext (or assignSessionEventID) before sending or
// persisting the event.
func makeInputSessionEvent[M MessageType](msg M) *SessionEvent[M] {
	return &SessionEvent[M]{Timestamp: newEventTimestamp(), Kind: SessionEventMessage, Message: msg}
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
	return normalizeAgentSessionEventWithAssigner(event, func(*SessionEvent[M]) (string, error) {
		return uuid.NewString(), nil
	})
}

// normalizeAgentSessionEventWithAssigner unifies the agent event / session
// event identity, allocating a fresh ID via assign when neither side carries
// one. The assigner is invoked with the (still-empty-ID) draft session event
// so callers may route allocation through SessionEventIDGenerator[M].
func normalizeAgentSessionEventWithAssigner[M MessageType](
	event *TypedAgentEvent[M],
	assign func(*SessionEvent[M]) (string, error),
) (SessionEvent[M], error) {
	if event == nil || event.SessionEvent == nil {
		return SessionEvent[M]{}, errors.New("missing session event")
	}
	if assign == nil {
		assign = func(*SessionEvent[M]) (string, error) { return uuid.NewString(), nil }
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
		id, err := assign(&se)
		if err != nil {
			return SessionEvent[M]{}, err
		}
		if id == "" {
			return SessionEvent[M]{}, ErrSessionEventIDGeneratorEmpty
		}
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
	if event.MessagesDeleted != nil {
		if err := validateMessageIDs("MessagesDeleted.MessageIDs", event.MessagesDeleted.MessageIDs); err != nil {
			return "", err
		}
		add(SessionEventMessagesDeleted)
	}
	if event.TurnEnd != nil {
		add(SessionEventTurnEnd)
	}
	if event.Rollback != nil {
		if event.EventID == "" {
			return "", errors.New("rollback session event must set non-empty EventID")
		}
		if event.Rollback.ToEventID == "" {
			return "", errors.New("rollback session event must set non-empty ToEventID")
		}
		add(SessionEventRollback)
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
		if (event.Span.Model != nil) == (event.Span.Tool != nil) {
			return "", errors.New("span event must populate exactly one of Model or Tool")
		}
		switch event.Span.Kind {
		case SpanKindModel:
			if event.Span.Model == nil {
				return "", errors.New("model span requires Span.Model meta")
			}
			switch {
			case !event.Span.StartedAt.IsZero() && event.Span.EndedAt.IsZero():
				add(SessionEventSpanModelRequestStart)
			case !event.Span.EndedAt.IsZero():
				add(SessionEventSpanModelRequestEnd)
			default:
				return "", errors.New("model span must have start or end timestamp")
			}
		case SpanKindTool:
			if event.Span.Tool == nil {
				return "", errors.New("tool span requires Span.Tool meta")
			}
			switch {
			case !event.Span.StartedAt.IsZero() && event.Span.EndedAt.IsZero():
				add(SessionEventSpanToolCallStart)
			case !event.Span.EndedAt.IsZero():
				add(SessionEventSpanToolCallEnd)
			default:
				return "", errors.New("tool span must have start or end timestamp")
			}
		default:
			return "", fmt.Errorf("unknown span kind %q", event.Span.Kind)
		}
	}
	if event.UserObservation != nil {
		if event.UserObservation.Interrupt == nil {
			return "", errors.New("user observation has no active payload")
		}
		add(SessionEventUserInterrupt)
	}
	if event.AgentInterrupt != nil {
		add(SessionEventAgentInterrupt)
	}
	if event.Extension != nil {
		if event.Kind == "" {
			return "", errors.New("session extension event must set kind")
		}
		if !strings.HasPrefix(string(event.Kind), SessionEventExtensionPrefix) {
			return "", fmt.Errorf("session extension event kind %q must start with %q", event.Kind, SessionEventExtensionPrefix)
		}
		add(event.Kind)
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

func isSessionDurableBoundaryKind(kind SessionEventKind) bool {
	switch kind {
	case SessionEventMessage, SessionEventTurnEnd, SessionEventAgentInterrupt:
		return true
	default:
		return false
	}
}

func normalizeSessionConfig[M MessageType](cfg *SessionConfig[M]) SessionConfig[M] {
	normalized := SessionConfig[M]{
		EventIDGenerator:      DefaultSessionEventIDGenerator[M],
		SessionAcquireTimeout: defaultSessionAcquireTimeout,
	}
	if cfg == nil {
		return normalized
	}
	if cfg.EventIDGenerator != nil {
		normalized.EventIDGenerator = cfg.EventIDGenerator
	}
	if cfg.SessionAcquireTimeout > 0 {
		normalized.SessionAcquireTimeout = cfg.SessionAcquireTimeout
	}
	return normalized
}

// assignSessionEventID assigns the EventID of a draft SessionEvent[M] using
// gen, falling back to DefaultSessionEventIDGenerator[M] when gen is nil. It
// is the single authoritative entry point for SessionEvent[M] ID allocation
// in ADK; runner / wrappers paths must route every draft through this helper
// (or its context wrapper assignSessionEventIDFromContext) before sending or
// persisting the event.
//
// Callers MUST construct the draft with EventID == "" and populate every
// other relevant field (SessionID, TurnID, Kind, payload, timestamp) so the
// generator sees a complete draft. A nil event is a no-op.
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

type sessionEventIDGeneratorKey[M MessageType] struct{}

// contextWithSessionEventIDGenerator stores the typed SessionEventIDGenerator[M]
// in ctx so that deeply-nested wrappers (model / tool / middleware) can route
// SessionEvent[M] draft ID allocation through the runner's configured
// generator without explicit parameter threading.
//
// This is an internal plumbing escape hatch; the only payload allowed in ctx
// under this key is the generator function itself. Storing business IDs,
// per-event state, or anything else under this key is forbidden — see the
// "scoped exception" note in the design plan.
func contextWithSessionEventIDGenerator[M MessageType](ctx context.Context, gen SessionEventIDGenerator[M]) context.Context {
	if gen == nil {
		return ctx
	}
	return context.WithValue(ctx, sessionEventIDGeneratorKey[M]{}, gen)
}

func sessionEventIDGeneratorFromContext[M MessageType](ctx context.Context) SessionEventIDGenerator[M] {
	if v := ctx.Value(sessionEventIDGeneratorKey[M]{}); v != nil {
		if gen, ok := v.(SessionEventIDGenerator[M]); ok {
			return gen
		}
	}
	return nil
}

// assignSessionEventIDFromContext assigns the EventID of a draft
// SessionEvent[M] using the SessionEventIDGenerator[M] stored in ctx (falling
// back to DefaultSessionEventIDGenerator[M] when none is set). Wrappers and
// runner closures call this helper after populating the rest of the draft.
func assignSessionEventIDFromContext[M MessageType](ctx context.Context, event *SessionEvent[M]) error {
	if event == nil {
		return nil
	}
	return assignSessionEventID(ctx, event, sessionEventIDGeneratorFromContext[M](ctx))
}

type sessionEventPersister[M MessageType] struct {
	ctx       context.Context
	handle    sessionHandle[M]
	sessionID string
	pending   []*SessionEvent[M]

	mu  sync.Mutex
	err error
}

func newSessionEventPersister[M MessageType](
	ctx context.Context,
	handle sessionHandle[M],
	sessionID string,
) *sessionEventPersister[M] {
	return &sessionEventPersister[M]{
		ctx:       ctx,
		handle:    handle,
		sessionID: sessionID,
	}
}

func (p *sessionEventPersister[M]) enqueueAsync(event *SessionEvent[M]) error {
	if event == nil || event.EventID == "" {
		return p.getErr()
	}
	snapshot, err := snapshotSessionEvent(event)
	if err != nil {
		p.setErr(err)
		return err
	}
	if err := p.getErr(); err != nil {
		return err
	}
	p.mu.Lock()
	p.pending = append(p.pending, snapshot)
	p.mu.Unlock()
	return nil
}

func (p *sessionEventPersister[M]) commitBoundary(event *SessionEvent[M]) error {
	if event == nil || event.EventID == "" {
		return p.getErr()
	}
	snapshot, err := snapshotSessionEvent(event)
	if err != nil {
		p.setErr(err)
		return err
	}
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
	_, err := p.handle.appendEvents(p.ctx, &AppendSessionEventsRequest[M]{
		SessionID: p.sessionID,
		Events:    events,
	})
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
	if event.SessionEvent == nil {
		return event
	}
	stripped := *event
	stripped.SessionEvent = nil
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
		event.MessageUpdated != nil || event.MessageInserted != nil || event.MessagesDeleted != nil
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

	case event.MessagesDeleted != nil:
		if err := deleteMessagesByID(out, event.MessagesDeleted.MessageIDs); err != nil {
			return err
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

func deleteMessagesByID[M MessageType](messages *[]M, ids []string) error {
	if err := validateMessageIDs("MessagesDeleted.MessageIDs", ids); err != nil {
		return err
	}
	targets := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		targets[id] = struct{}{}
	}
	found := make(map[string]struct{}, len(ids))
	for _, msg := range *messages {
		id := GetMessageID(msg)
		if _, ok := targets[id]; ok {
			found[id] = struct{}{}
		}
	}
	for _, id := range ids {
		if _, ok := found[id]; !ok {
			return fmt.Errorf("reconstruct: target message %q not found for deletion", id)
		}
	}

	retained := (*messages)[:0]
	for _, msg := range *messages {
		if _, ok := targets[GetMessageID(msg)]; ok {
			continue
		}
		retained = append(retained, msg)
	}
	*messages = retained
	return nil
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
	state          *TurnEndState[M]
	inFlightTurnID string // TurnID from events after the last committed TurnEnd (the interrupted turn)
}

// modelContextSessionEventKinds is the set of event kinds required to reconstruct
// model-facing session state (messages + turn metadata). Timeline-only events
// (lifecycle, span, error, interrupt) are excluded.
var modelContextSessionEventKinds = []SessionEventKind{
	SessionEventMessage,
	SessionEventMessagesReplaced,
	SessionEventMessageUpdated,
	SessionEventMessageInserted,
	SessionEventMessagesDeleted,
	SessionEventTurnEnd,
	SessionEventRollback,
}

type RollbackSessionOptions[M MessageType] struct {
	CheckPointStore     CheckPointStore
	ExpectedHeadTurnID  string
	EventIDGenerator    SessionEventIDGenerator[M]
	SessionFencingToken SessionFencingTokenFunc
}

type RollbackSessionOption[M MessageType] func(*RollbackSessionOptions[M])

// WithRollbackSessionCheckPointStore deletes session-derived checkpoints after a successful rollback.
func WithRollbackSessionCheckPointStore[M MessageType](store CheckPointStore) RollbackSessionOption[M] {
	return func(opts *RollbackSessionOptions[M]) {
		opts.CheckPointStore = store
	}
}

// WithRollbackSessionExpectedHeadTurnID requires the current active head turn to match turnID before rollback.
func WithRollbackSessionExpectedHeadTurnID[M MessageType](turnID string) RollbackSessionOption[M] {
	return func(opts *RollbackSessionOptions[M]) {
		opts.ExpectedHeadTurnID = turnID
	}
}

// WithRollbackEventIDGenerator overrides the EventID generator for the rollback
// event. The generator sees the fully-populated rollback draft (kind, turn IDs,
// SessionRollbackEvent payload) before assignment. If nil or not set,
// DefaultSessionEventIDGenerator[M] (UUID v4) is used.
func WithRollbackEventIDGenerator[M MessageType](gen SessionEventIDGenerator[M]) RollbackSessionOption[M] {
	return func(opts *RollbackSessionOptions[M]) {
		opts.EventIDGenerator = gen
	}
}

// WithRollbackSessionFencingToken supplies the external owner proof used when
// rolling back through a fenced session service.
func WithRollbackSessionFencingToken[M MessageType](fn SessionFencingTokenFunc) RollbackSessionOption[M] {
	return func(opts *RollbackSessionOptions[M]) {
		opts.SessionFencingToken = fn
	}
}

// RollbackSession appends a rollback marker that makes targetTurnID the latest active committed turn.
func RollbackSession[M MessageType](
	ctx context.Context,
	service SessionService[M],
	sessionID string,
	targetTurnID string,
	opts ...RollbackSessionOption[M],
) error {
	if service == nil {
		return errors.New("adk: rollback session service is nil")
	}
	if sessionID == "" {
		return errors.New("adk: rollback sessionID is empty")
	}
	if targetTurnID == "" {
		return ErrRollbackTargetNotFound
	}
	var cfg RollbackSessionOptions[M]
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	openResult, err := service.openSession(ctx, &openSessionRequest{sessionID: sessionID, fencingToken: cfg.SessionFencingToken})
	if err != nil {
		return err
	}
	if openResult == nil || openResult.handle == nil {
		return ErrSessionBusy
	}
	defer openResult.handle.close(ctx)

	activeEvents, err := loadActiveSessionEventsReverse[M](ctx, openResult.handle, sessionID, defaultLoadPageSize)
	if err != nil {
		return err
	}
	target, head, err := resolveRollbackTarget[M](activeEvents, targetTurnID)
	if err != nil {
		if errors.Is(err, ErrRollbackTargetNotFound) {
			evidence, evidenceErr := findPhysicalRollbackTargetEvidence[M](ctx, openResult.handle, sessionID, targetTurnID, defaultLoadPageSize)
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
	if cfg.ExpectedHeadTurnID != "" && (head == nil || head.TurnID != cfg.ExpectedHeadTurnID) {
		return ErrSessionHeadChanged
	}

	rb := &SessionEvent[M]{
		Timestamp: newEventTimestamp(),
		Kind:      SessionEventRollback,
		Rollback: &SessionRollbackEvent{
			ToEventID:             target.EventID,
			ToTurnID:              target.TurnID,
			PreviousHeadTurnEndID: head.EventID,
			PreviousHeadTurnID:    head.TurnID,
		},
	}
	if err := assignSessionEventID(ctx, rb, cfg.EventIDGenerator); err != nil {
		return err
	}
	if _, err := openResult.handle.appendEvents(ctx, &AppendSessionEventsRequest[M]{
		SessionID: sessionID,
		Events:    []*SessionEvent[M]{rb},
	}); err != nil {
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

// reconstructSessionState rebuilds session state from the append log.
// Durable context events are replayed through the log tail, including messages
// after the latest TurnEnd. The latest TurnEnd remains the metadata boundary for
// tool infos, deferred tool infos, and session values. This preserves framework
// context fidelity; provider-specific sanitization for dangling tool-call
// structures remains a caller or middleware concern.
func reconstructSessionState[M MessageType](
	ctx context.Context,
	handle sessionHandle[M],
	sessionID string,
	pageSize int,
) (*sessionReconstructResult[M], error) {
	allEvents, err := loadActiveSessionEventsReverse[M](ctx, handle, sessionID, pageSize)
	if err != nil {
		return nil, err
	}
	if len(allEvents) == 0 {
		return nil, nil
	}

	committedEndIdx := latestCommittedTurnEnd(allEvents)
	contextTailIdx := len(allEvents) - 1
	inFlightStartIdx := committedEndIdx + 1
	if committedEndIdx < 0 {
		// Compatibility for historical/session-fixture logs written before
		// TurnEnd became the explicit commit boundary.
		inFlightStartIdx = 0
		committedEndIdx = contextTailIdx
	}

	// After the last committed TurnEnd, any events belong to an interrupted
	// turn. The first TurnID found identifies that turn — all events within a
	// single turn share the same TurnID, so only the first match is needed.
	var inFlightTurnID string
	for i := inFlightStartIdx; i <= contextTailIdx; i++ {
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

func loadActiveSessionEventsReverse[M MessageType](
	ctx context.Context,
	handle sessionHandle[M],
	sessionID string,
	pageSize int,
) ([]*SessionEvent[M], error) {
	if pageSize <= 0 {
		pageSize = defaultLoadPageSize
	}
	var physicalReverse []*SessionEvent[M]
	var after string
	for {
		result, err := handle.loadEvents(ctx, &LoadSessionEventsRequest{
			SessionID: sessionID,
			After:     after,
			Limit:     pageSize,
			Reverse:   true,
			Kinds:     modelContextSessionEventKinds,
			// The first reverse page is the newest page, so its snapshot tail is
			// the log tail the caller appends against. Later (older) pages do not
			// need it.
			IncludeSessionTail: after == "",
		})
		if err != nil {
			return nil, err
		}
		if result == nil || len(result.Events) == 0 {
			break
		}
		physicalReverse = append(physicalReverse, result.Events...)
		if result.Next == "" {
			break
		}
		after = result.Next
	}
	if err := validateRollbackTargetsForwardFromReverse[M](physicalReverse); err != nil {
		return nil, err
	}
	return projectActiveEventsFromReverse[M](physicalReverse)
}

func projectActiveEventsFromReverse[M MessageType](
	physicalReverse []*SessionEvent[M],
) ([]*SessionEvent[M], error) {
	var activeReverse []*SessionEvent[M]
	var skipUntilEventID string
	var skipUntilTurnID string
	for _, event := range physicalReverse {
		if skipUntilEventID != "" {
			if event.EventID != skipUntilEventID {
				continue
			}
			if event.Kind != SessionEventTurnEnd {
				return nil, ErrInvalidRollbackTarget
			}
			if skipUntilTurnID != "" {
				if event.Kind != SessionEventTurnEnd || event.TurnEnd == nil || event.TurnID != skipUntilTurnID {
					return nil, ErrInvalidRollbackTarget
				}
			}
			activeReverse = append(activeReverse, event)
			skipUntilEventID = ""
			skipUntilTurnID = ""
			continue
		}
		if event.Kind == SessionEventRollback {
			rb, err := decodeRollbackSessionEvent(event)
			if err != nil {
				return nil, err
			}
			skipUntilEventID = rb.ToEventID
			skipUntilTurnID = rb.ToTurnID
			continue
		}
		activeReverse = append(activeReverse, event)
	}
	if skipUntilEventID != "" {
		return nil, ErrRollbackTargetInactive
	}
	for i, j := 0, len(activeReverse)-1; i < j; i, j = i+1, j-1 {
		activeReverse[i], activeReverse[j] = activeReverse[j], activeReverse[i]
	}
	return activeReverse, nil
}

func validateRollbackTargetsForwardFromReverse[M MessageType](
	physicalReverse []*SessionEvent[M],
) error {
	active := make([]*SessionEvent[M], 0, len(physicalReverse))
	activeLen := 0
	posByEventID := make(map[string]int, len(physicalReverse))
	for i := len(physicalReverse) - 1; i >= 0; i-- {
		event := physicalReverse[i]
		if event.Kind == SessionEventRollback {
			rb, err := decodeRollbackSessionEvent(event)
			if err != nil {
				return err
			}
			pos, ok := posByEventID[rb.ToEventID]
			if !ok || pos >= activeLen || active[pos].EventID != rb.ToEventID {
				return ErrRollbackTargetInactive
			}
			if active[pos].Kind != SessionEventTurnEnd {
				return ErrInvalidRollbackTarget
			}
			if rb.ToTurnID != "" {
				target := active[pos]
				if target.Kind != SessionEventTurnEnd || target.TurnEnd == nil || target.TurnID != rb.ToTurnID {
					return ErrInvalidRollbackTarget
				}
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
	return nil
}

type rollbackTargetEvidence int

const (
	rollbackTargetEvidenceNone rollbackTargetEvidence = iota
	rollbackTargetEvidenceUncommitted
	rollbackTargetEvidenceCommitted
)

func findPhysicalRollbackTargetEvidence[M MessageType](
	ctx context.Context,
	handle sessionHandle[M],
	sessionID string,
	targetTurnID string,
	pageSize int,
) (rollbackTargetEvidence, error) {
	if pageSize <= 0 {
		pageSize = defaultLoadPageSize
	}
	var after string
	var evidence rollbackTargetEvidence
	for {
		result, err := handle.loadEvents(ctx, &LoadSessionEventsRequest{
			SessionID: sessionID,
			After:     after,
			Limit:     pageSize,
			Reverse:   false,
			Kinds:     modelContextSessionEventKinds,
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
			if event.TurnID != targetTurnID {
				continue
			}
			if event.Kind == SessionEventTurnEnd && event.TurnEnd != nil {
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
	targetTurnID string,
) (target *SessionEvent[M], head *SessionEvent[M], err error) {
	var sawTargetTurnEvidence bool
	for _, event := range activeEvents {
		if event.Kind != SessionEventTurnEnd {
			if !sawTargetTurnEvidence {
				if event.TurnID == targetTurnID {
					sawTargetTurnEvidence = true
				}
			}
			continue
		}
		if event.Kind != SessionEventTurnEnd || event.TurnEnd == nil || event.TurnID == "" {
			return nil, nil, ErrInvalidRollbackTarget
		}
		head = event
		if event.TurnID == targetTurnID {
			target = event
			sawTargetTurnEvidence = true
		}
	}
	if target != nil {
		return target, head, nil
	}
	if sawTargetTurnEvidence {
		return nil, nil, ErrInvalidRollbackTarget
	}
	return nil, nil, ErrRollbackTargetNotFound
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
