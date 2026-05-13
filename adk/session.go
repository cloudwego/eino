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
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cloudwego/eino/schema"
)

const (
	defaultSessionEventFlushBatchSize = 16
	defaultSessionEventFlushInterval  = 100 * time.Millisecond
	defaultSessionEventBufferSize     = 64
)

// ErrPendingSessionCheckpoint is returned when a managed session has an
// interrupted in-flight turn that must be resumed before accepting new input.
var ErrPendingSessionCheckpoint = errors.New("adk: pending session checkpoint")

const (
	sessionRunnerCheckpointSuffix   = "/runner_checkpoint"
	sessionTurnLoopCheckpointSuffix = "/turn_loop_checkpoint"
)

// SessionStore persists Runner-managed session data.
// It is intentionally independent from CheckPointStore: session history and
// checkpoint/resume state are separate persistence planes.
type SessionStore interface {
	AppendEvents(ctx context.Context, sessionID string, turnIndex int, entries []EventRecord) error
	LoadEvents(ctx context.Context, sessionID string, fromTurnIndex, toTurnIndex int) ([]EventRecord, error)
	LoadLatestTurnEnd(ctx context.Context, sessionID string) (turnIndex int, turnEnd []byte, exists bool, err error)
	SaveTurnEnd(ctx context.Context, sessionID string, turnIndex int, turnEnd []byte) error
}

// EventRecord is a single serialized event ready for persistent storage.
type EventRecord struct {
	// TurnIndex identifies which turn this event belongs to.
	TurnIndex int
	// Seq is the monotonically increasing sequence number within the session,
	// used for idempotent deduplication and ordering.
	Seq int64
	// Kind describes the event type (e.g. "output", "action").
	Kind string
	// Payload is the serialized event content.
	Payload []byte
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
}

// TurnEndState is the agent-visible state materialized at a successful turn boundary.
type TurnEndState[M MessageType] struct {
	Messages          []M
	ToolInfos         []*schema.ToolInfo
	DeferredToolInfos []*schema.ToolInfo
	SessionValues     map[string]any
}

type runnerSessionCheckpoint struct {
	TurnIndex    int
	NextEventSeq int64
	Payload      []byte
}

func init() {
	schema.RegisterName[*TurnEndState[*schema.Message]]("_eino_adk_turn_end_state")
	schema.RegisterName[*TurnEndState[*schema.AgenticMessage]]("_eino_adk_agentic_turn_end_state")
}

func encodeGob(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func encodeTurnEndState[M MessageType](state *TurnEndState[M]) ([]byte, error) {
	return encodeGob(state)
}

func decodeTurnEndState[M MessageType](payload []byte) (*TurnEndState[M], error) {
	var state TurnEndState[M]
	if err := gob.NewDecoder(bytes.NewReader(payload)).Decode(&state); err != nil {
		return nil, err
	}
	return &state, nil
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

func sessionTurnLoopCheckpointID(sessionID string) string {
	return "session/" + sessionID + sessionTurnLoopCheckpointSuffix
}

func encodeAgentEvent[M MessageType](event *TypedAgentEvent[M]) ([]byte, error) {
	return encodeGob(event)
}

func normalizeSessionPersistenceConfig(cfg *SessionPersistenceConfig) SessionPersistenceConfig {
	normalized := SessionPersistenceConfig{
		EventFlushBatchSize: defaultSessionEventFlushBatchSize,
		EventFlushInterval:  defaultSessionEventFlushInterval,
		EventBufferSize:     defaultSessionEventBufferSize,
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
	return normalized
}

type sessionEventPersister[M MessageType] struct {
	ctx       context.Context
	store     SessionStore
	sessionID string
	turnIndex int
	cfg       SessionPersistenceConfig

	ch     chan EventRecord
	done   chan struct{}
	closed int32 // atomic: 1 after closeAndWait is called

	mu  sync.Mutex
	err error
}

func newSessionEventPersister[M MessageType](
	ctx context.Context,
	store SessionStore,
	sessionID string,
	turnIndex int,
	cfg *SessionPersistenceConfig,
) *sessionEventPersister[M] {
	p := &sessionEventPersister[M]{
		ctx:       ctx,
		store:     store,
		sessionID: sessionID,
		turnIndex: turnIndex,
		cfg:       normalizeSessionPersistenceConfig(cfg),
		done:      make(chan struct{}),
	}
	p.ch = make(chan EventRecord, p.cfg.EventBufferSize)
	go p.run()
	return p
}

func (p *sessionEventPersister[M]) enqueue(record EventRecord) error {
	if len(record.Payload) == 0 {
		return p.getErr()
	}
	if err := p.getErr(); err != nil {
		return err
	}
	if atomic.LoadInt32(&p.closed) != 0 {
		return p.getErr()
	}
	select {
	case p.ch <- record:
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

	var batch []EventRecord
	flush := func() {
		if len(batch) == 0 || p.getErr() != nil {
			batch = nil
			return
		}
		entries := make([]EventRecord, len(batch))
		copy(entries, batch)
		batch = nil
		if err := p.store.AppendEvents(p.ctx, p.sessionID, p.turnIndex, entries); err != nil {
			p.setErr(err)
		}
	}

	for {
		select {
		case record, ok := <-p.ch:
			if !ok {
				flush()
				return
			}
			if p.getErr() != nil {
				continue
			}
			batch = append(batch, record)
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

func eventHasPersistedPayload[M MessageType](event *TypedAgentEvent[M]) bool {
	return event.AgentName != "" || len(event.RunPath) > 0 || event.Output != nil ||
		event.Action != nil || event.TurnEndState != nil
}

func agentEventKind[M MessageType](event *TypedAgentEvent[M]) string {
	var kinds []string
	if event.Output != nil {
		kinds = append(kinds, "output")
	}
	if event.Action != nil {
		kinds = append(kinds, "action")
	}
	if event.TurnEndState != nil {
		kinds = append(kinds, "turn_end")
	}
	if len(kinds) == 0 {
		return "metadata"
	}
	return strings.Join(kinds, ",")
}

func stripSessionEventFields[M MessageType](event *TypedAgentEvent[M]) *TypedAgentEvent[M] {
	if event == nil {
		return nil
	}
	if event.TurnEndState == nil {
		return event
	}
	stripped := *event
	stripped.TurnEndState = nil
	if stripped.Output == nil && stripped.Action == nil && stripped.Err == nil {
		return nil
	}
	return &stripped
}

func splitPersistentAndLiveEvent[M MessageType](event *TypedAgentEvent[M]) (*TypedAgentEvent[M], *TypedAgentEvent[M]) {
	if event == nil {
		return nil, nil
	}

	live := *event
	persisted := *event
	persisted.Err = nil

	if event.Output != nil {
		liveOutput := *event.Output
		persistedOutput := *event.Output
		live.Output = &liveOutput
		persisted.Output = &persistedOutput
		if event.Output.MessageOutput != nil {
			liveMV := *event.Output.MessageOutput
			persistedMV := *event.Output.MessageOutput
			if event.Output.MessageOutput.IsStreaming && event.Output.MessageOutput.MessageStream != nil {
				copies := event.Output.MessageOutput.MessageStream.Copy(2)
				persistedMV.MessageStream = copies[0]
				liveMV.MessageStream = copies[1]
			}
			live.Output.MessageOutput = &liveMV
			persisted.Output.MessageOutput = &persistedMV
		}
	}

	if !eventHasPersistedPayload(&persisted) {
		return nil, &live
	}
	return &persisted, &live
}

func makeEventRecord[M MessageType](turnIndex int, seq int64, event *TypedAgentEvent[M]) (EventRecord, error) {
	if event == nil || !eventHasPersistedPayload(event) {
		return EventRecord{}, nil
	}
	payload, err := encodeAgentEvent(event)
	if err != nil {
		return EventRecord{}, err
	}
	return EventRecord{
		TurnIndex: turnIndex,
		Seq:       seq,
		Kind:      agentEventKind(event),
		Payload:   payload,
	}, nil
}
