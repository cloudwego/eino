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

// NewLocalSessionService wraps a provider-facing event store as a sealed
// SessionService. It serializes active handles for the same session within the
// current process and does not provide cross-process write coordination.
func NewLocalSessionService[M MessageType](store SessionEventStore[M]) SessionService[M] {
	if store == nil {
		return nil
	}
	return &localSessionService[M]{
		store:  store,
		locked: make(map[string]bool),
	}
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
	}, nil
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
	return h.store.LoadEvents(ctx, &clone)
}

func (h *localSessionHandle[M]) appendEvents(ctx context.Context, req *AppendSessionEventsRequest[M]) error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return ErrSessionBusy
	}
	h.mu.Unlock()

	if req == nil {
		req = &AppendSessionEventsRequest[M]{}
	}
	clone := *req
	clone.SessionID = h.sessionID
	if err := h.store.AppendEvents(ctx, &clone); err != nil {
		return err
	}
	if len(clone.Events) > 0 {
		h.mu.Lock()
		h.tailID = clone.Events[len(clone.Events)-1].EventID
		h.mu.Unlock()
	}
	return nil
}

func (h *localSessionHandle[M]) currentTailEventID() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.tailID
}

func (h *localSessionHandle[M]) setCurrentTailEventID(tailID string) {
	h.mu.Lock()
	h.tailID = tailID
	h.mu.Unlock()
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

func refreshSessionTail[M MessageType](ctx context.Context, handle sessionHandle[M], sessionID string) (string, error) {
	result, err := handle.loadEvents(ctx, &LoadSessionEventsRequest{
		SessionID: sessionID,
		Reverse:   true,
		Limit:     1,
	})
	if err != nil {
		return "", err
	}
	tailID := ""
	if result != nil && len(result.Events) > 0 && result.Events[0] != nil {
		tailID = result.Events[0].EventID
	}
	if h, ok := handle.(*localSessionHandle[M]); ok {
		h.setCurrentTailEventID(tailID)
	}
	return tailID, nil
}
