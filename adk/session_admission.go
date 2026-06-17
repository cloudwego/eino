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
	"fmt"
	"reflect"
	"sync"
)

var localSessionAdmission = struct {
	mu     sync.Mutex
	locked map[string]bool
}{locked: make(map[string]bool)}

func openLocalSession[M MessageType](_ context.Context, store SessionEventStore[M], req *openSessionRequest) (*openSessionResult[M], error) {
	if store == nil || req == nil || req.sessionID == "" {
		return nil, ErrSessionBusy
	}
	key := localSessionAdmissionKey(store, req.sessionID)
	if key == "" {
		return nil, ErrSessionBusy
	}
	localSessionAdmission.mu.Lock()
	defer localSessionAdmission.mu.Unlock()
	if localSessionAdmission.locked[key] {
		return nil, ErrSessionBusy
	}
	localSessionAdmission.locked[key] = true
	return &openSessionResult[M]{
		handle: &localSessionHandle[M]{
			key:       key,
			store:     store,
			sessionID: req.sessionID,
		},
	}, nil
}

func localSessionAdmissionKey[M MessageType](store SessionEventStore[M], sessionID string) string {
	v := reflect.ValueOf(store)
	if !v.IsValid() {
		return ""
	}
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Map, reflect.Ptr, reflect.Slice:
		if v.IsNil() {
			return ""
		}
		return fmt.Sprintf("%T:%x/%s", store, v.Pointer(), sessionID)
	default:
		return fmt.Sprintf("%T:%v/%s", store, store, sessionID)
	}
}

func releaseLocalSession(key string) {
	localSessionAdmission.mu.Lock()
	delete(localSessionAdmission.locked, key)
	localSessionAdmission.mu.Unlock()
}

type localSessionHandle[M MessageType] struct {
	key       string
	store     SessionEventStore[M]
	sessionID string

	mu     sync.Mutex
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
	return nil
}

func (h *localSessionHandle[M]) close(context.Context) error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	h.mu.Unlock()
	releaseLocalSession(h.key)
	return nil
}
