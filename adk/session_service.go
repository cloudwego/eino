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
	"context"
	"sync"
)

// LocalSessionServiceOptions configures the process-local session adapter.
type LocalSessionServiceOptions struct{}

// FencedSessionServiceOptions configures the fenced session adapter.
type FencedSessionServiceOptions struct{}

// NewLocalSessionService wraps a provider-facing event store as a sealed
// SessionService. It serializes active handles for the same session within the
// current process and does not provide cross-process fencing.
func NewLocalSessionService[M MessageType](store SessionEventStore[M]) SessionService[M] {
	if store == nil {
		return nil
	}
	return &localSessionService[M]{
		store:  store,
		locked: make(map[string]bool),
	}
}

// NewFencedSessionService wraps a fenced event store as a sealed SessionService.
// The returned handle keeps the fencing token internal to the adk package.
func NewFencedSessionService[M MessageType](store FencedSessionEventStore[M], _ FencedSessionServiceOptions) SessionService[M] {
	if store == nil {
		return nil
	}
	return &fencedSessionService[M]{store: store}
}

type localSessionService[M MessageType] struct {
	store SessionEventStore[M]

	mu     sync.Mutex
	locked map[string]bool
}

func (s *localSessionService[M]) openSession(_ context.Context, req *openSessionRequest) (*openSessionResult[M], error) {
	if req == nil || req.sessionID == "" {
		return nil, ErrSessionBusy
	}
	if req.requireFenced {
		return nil, ErrSessionFencingRequired
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.locked[req.sessionID] {
		return nil, ErrSessionBusy
	}
	s.locked[req.sessionID] = true
	return &openSessionResult[M]{
		handle: &localSessionHandle[M]{
			service:   s,
			store:     s.store,
			sessionID: req.sessionID,
		},
		fenced: false,
	}, nil
}

func (s *localSessionService[M]) AppendEvents(ctx context.Context, sessionID string, events []*SessionEvent[M]) error {
	res, err := s.openSession(ctx, &openSessionRequest{sessionID: sessionID})
	if err != nil {
		return err
	}
	defer res.handle.close(ctx)
	if _, err := res.handle.loadEvents(ctx, &LoadSessionEventsRequest{SessionID: sessionID, Reverse: true, Limit: 1}); err != nil {
		return err
	}
	_, err = res.handle.appendEvents(ctx, &AppendSessionEventsRequest[M]{SessionID: sessionID, Events: events})
	return err
}

func (s *localSessionService[M]) LoadEvents(ctx context.Context, sessionID string, opts *LoadSessionEventsRequest) (*LoadSessionEventsResult[M], error) {
	res, err := s.openSession(ctx, &openSessionRequest{sessionID: sessionID})
	if err != nil {
		return nil, err
	}
	defer res.handle.close(ctx)
	if opts == nil {
		opts = &LoadSessionEventsRequest{}
	}
	clone := *opts
	clone.SessionID = sessionID
	return res.handle.loadEvents(ctx, &clone)
}

func (s *localSessionService[M]) release(sessionID string) {
	s.mu.Lock()
	delete(s.locked, sessionID)
	s.mu.Unlock()
}

type localSessionHandle[M MessageType] struct {
	service   *localSessionService[M]
	store     SessionEventStore[M]
	sessionID string

	mu     sync.Mutex
	tailID string
	closed bool
}

func (h *localSessionHandle[M]) loadEvents(ctx context.Context, req *LoadSessionEventsRequest) (*LoadSessionEventsResult[M], error) {
	if req == nil {
		req = &LoadSessionEventsRequest{}
	}
	clone := *req
	clone.SessionID = h.sessionID
	res, err := h.store.LoadEvents(ctx, &clone)
	if err != nil {
		return nil, err
	}
	if res != nil {
		h.mu.Lock()
		h.tailID = res.SessionTailEventID
		h.mu.Unlock()
	}
	return res, nil
}

func (h *localSessionHandle[M]) appendEvents(ctx context.Context, req *AppendSessionEventsRequest[M]) (*AppendSessionEventsResult, error) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, ErrSessionBusy
	}
	tailID := h.tailID
	h.mu.Unlock()

	if req == nil {
		req = &AppendSessionEventsRequest[M]{}
	}
	clone := *req
	clone.SessionID = h.sessionID
	if clone.ExpectedSessionTailEventID == "" {
		clone.ExpectedSessionTailEventID = tailID
	}
	res, err := h.store.AppendEvents(ctx, &clone)
	if err != nil {
		return nil, err
	}
	if res != nil {
		h.mu.Lock()
		h.tailID = res.SessionTailEventID
		h.mu.Unlock()
	}
	return res, nil
}

func (h *localSessionHandle[M]) renew(context.Context) error { return nil }

func (h *localSessionHandle[M]) currentTailEventID() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.tailID
}

func (h *localSessionHandle[M]) close(context.Context) error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	h.mu.Unlock()
	h.service.release(h.sessionID)
	return nil
}

type fencedSessionService[M MessageType] struct {
	store FencedSessionEventStore[M]
}

func (s *fencedSessionService[M]) openSession(ctx context.Context, req *openSessionRequest) (*openSessionResult[M], error) {
	if req == nil || req.sessionID == "" {
		return nil, ErrSessionBusy
	}
	token, err := s.store.AcquireFencingToken(ctx, req.sessionID)
	if err != nil {
		return nil, err
	}
	return &openSessionResult[M]{
		handle: &fencedSessionHandle[M]{
			store:     s.store,
			sessionID: req.sessionID,
			token:     token,
		},
		fenced: true,
	}, nil
}

func (s *fencedSessionService[M]) AppendEvents(ctx context.Context, sessionID string, events []*SessionEvent[M]) error {
	res, err := s.openSession(ctx, &openSessionRequest{sessionID: sessionID, requireFenced: true})
	if err != nil {
		return err
	}
	defer res.handle.close(ctx)
	if _, err := res.handle.loadEvents(ctx, &LoadSessionEventsRequest{SessionID: sessionID, Reverse: true, Limit: 1}); err != nil {
		return err
	}
	_, err = res.handle.appendEvents(ctx, &AppendSessionEventsRequest[M]{SessionID: sessionID, Events: events})
	return err
}

func (s *fencedSessionService[M]) LoadEvents(ctx context.Context, sessionID string, opts *LoadSessionEventsRequest) (*LoadSessionEventsResult[M], error) {
	res, err := s.openSession(ctx, &openSessionRequest{sessionID: sessionID, requireFenced: true})
	if err != nil {
		return nil, err
	}
	defer res.handle.close(ctx)
	if opts == nil {
		opts = &LoadSessionEventsRequest{}
	}
	clone := *opts
	clone.SessionID = sessionID
	return res.handle.loadEvents(ctx, &clone)
}

type fencedSessionHandle[M MessageType] struct {
	store     FencedSessionEventStore[M]
	sessionID string

	mu     sync.Mutex
	token  *SessionFencingToken
	tailID string
	closed bool
}

func (h *fencedSessionHandle[M]) loadEvents(ctx context.Context, req *LoadSessionEventsRequest) (*LoadSessionEventsResult[M], error) {
	if req == nil {
		req = &LoadSessionEventsRequest{}
	}
	clone := *req
	clone.SessionID = h.sessionID
	res, err := h.store.LoadEvents(ctx, &clone)
	if err != nil {
		return nil, err
	}
	if res != nil {
		h.mu.Lock()
		h.tailID = res.SessionTailEventID
		h.mu.Unlock()
	}
	return res, nil
}

func (h *fencedSessionHandle[M]) appendEvents(ctx context.Context, req *AppendSessionEventsRequest[M]) (*AppendSessionEventsResult, error) {
	h.mu.Lock()
	token := h.token
	tailID := h.tailID
	closed := h.closed
	h.mu.Unlock()
	if closed {
		return nil, ErrSessionFencingTokenInvalid
	}
	if token == nil || token.Token == "" {
		return nil, ErrSessionFencingTokenInvalid
	}
	if req == nil {
		req = &AppendSessionEventsRequest[M]{}
	}
	freq := &FencedAppendSessionEventsRequest[M]{
		SessionID:                  h.sessionID,
		FencingToken:               token.Token,
		ExpectedSessionTailEventID: req.ExpectedSessionTailEventID,
		Events:                     req.Events,
	}
	if freq.ExpectedSessionTailEventID == "" {
		freq.ExpectedSessionTailEventID = tailID
	}
	res, err := h.store.AppendEventsFenced(ctx, freq)
	if err != nil {
		return nil, err
	}
	if res != nil {
		h.mu.Lock()
		h.tailID = res.SessionTailEventID
		h.mu.Unlock()
	}
	return res, nil
}

func (h *fencedSessionHandle[M]) renew(ctx context.Context) error {
	h.mu.Lock()
	token := h.token
	h.mu.Unlock()
	if token == nil {
		return ErrSessionFencingTokenInvalid
	}
	next, err := h.store.RenewFencingToken(ctx, token)
	if err != nil {
		return err
	}
	h.mu.Lock()
	h.token = next
	h.mu.Unlock()
	return nil
}

func (h *fencedSessionHandle[M]) currentTailEventID() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.tailID
}

func (h *fencedSessionHandle[M]) close(ctx context.Context) error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	token := h.token
	h.mu.Unlock()
	if token == nil {
		return nil
	}
	return h.store.ReleaseFencingToken(ctx, token)
}
