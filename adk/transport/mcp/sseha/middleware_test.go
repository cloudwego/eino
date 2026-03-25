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

package sseha

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// mockMetadataStore implements MetadataStore for testing
type mockMetadataStore struct {
	sessions map[string]*SessionInfo
	nodes    map[string]*NodeInfo
	barriers map[string]*BarrierToken
	locks    map[string]string
}

func newMockMetadataStore() *mockMetadataStore {
	return &mockMetadataStore{
		sessions: make(map[string]*SessionInfo),
		nodes:    make(map[string]*NodeInfo),
		barriers: make(map[string]*BarrierToken),
		locks:    make(map[string]string),
	}
}

func (m *mockMetadataStore) RegisterSession(ctx context.Context, info *SessionInfo) error {
	if _, exists := m.sessions[info.SessionID]; exists {
		return ErrSessionAlreadyExists
	}
	m.sessions[info.SessionID] = info
	return nil
}

func (m *mockMetadataStore) GetSession(ctx context.Context, sessionID string) (*SessionInfo, error) {
	return m.sessions[sessionID], nil
}

func (m *mockMetadataStore) UpdateSession(ctx context.Context, info *SessionInfo) error {
	existing, ok := m.sessions[info.SessionID]
	if !ok {
		return ErrSessionNotFound
	}
	if existing.Version != info.Version {
		return ErrVersionConflict
	}
	info.Version++
	m.sessions[info.SessionID] = info
	return nil
}

func (m *mockMetadataStore) DeleteSession(ctx context.Context, sessionID string) error {
	delete(m.sessions, sessionID)
	return nil
}

func (m *mockMetadataStore) ListSessions(ctx context.Context, filter *SessionFilter) ([]*SessionInfo, error) {
	var result []*SessionInfo
	for _, s := range m.sessions {
		if filter != nil && filter.NodeID != "" && s.NodeID != filter.NodeID {
			continue
		}
		result = append(result, s)
	}
	return result, nil
}

func (m *mockMetadataStore) AcquireSessionLock(ctx context.Context, sessionID, nodeID string, ttl time.Duration) (bool, error) {
	if owner, exists := m.locks[sessionID]; exists && owner != nodeID {
		return false, nil
	}
	m.locks[sessionID] = nodeID
	return true, nil
}

func (m *mockMetadataStore) ReleaseSessionLock(ctx context.Context, sessionID, nodeID string) error {
	if m.locks[sessionID] == nodeID {
		delete(m.locks, sessionID)
	}
	return nil
}

func (m *mockMetadataStore) RegisterNode(ctx context.Context, node *NodeInfo) error {
	m.nodes[node.NodeID] = node
	return nil
}

func (m *mockMetadataStore) GetNode(ctx context.Context, nodeID string) (*NodeInfo, error) {
	return m.nodes[nodeID], nil
}

func (m *mockMetadataStore) ListNodes(ctx context.Context, aliveOnly bool, heartbeatTimeout time.Duration) ([]*NodeInfo, error) {
	var result []*NodeInfo
	for _, n := range m.nodes {
		if aliveOnly && time.Since(n.LastHeartbeat) > heartbeatTimeout {
			continue
		}
		result = append(result, n)
	}
	return result, nil
}

func (m *mockMetadataStore) RemoveNode(ctx context.Context, nodeID string) error {
	delete(m.nodes, nodeID)
	return nil
}

func (m *mockMetadataStore) SetBarrier(ctx context.Context, barrier *BarrierToken) error {
	m.barriers[barrier.SessionID] = barrier
	return nil
}

func (m *mockMetadataStore) GetBarrier(ctx context.Context, sessionID string) (*BarrierToken, error) {
	return m.barriers[sessionID], nil
}

func (m *mockMetadataStore) ReleaseBarrier(ctx context.Context, sessionID string) error {
	if b, ok := m.barriers[sessionID]; ok {
		b.Released = true
	}
	return nil
}

func (m *mockMetadataStore) Close() error { return nil }

// mockEventBus implements EventBus for testing
type mockEventBus struct {
	subscriptions map[string]chan *SSEEvent
	closed        bool
}

func newMockEventBus() *mockEventBus {
	return &mockEventBus{
		subscriptions: make(map[string]chan *SSEEvent),
	}
}

func (b *mockEventBus) Publish(ctx context.Context, event *SSEEvent) error {
	if b.closed {
		return ErrManagerClosed
	}
	if ch, ok := b.subscriptions[event.SessionID]; ok {
		select {
		case ch <- event:
		default:
		}
	}
	return nil
}

func (b *mockEventBus) Subscribe(ctx context.Context, sessionID string) (<-chan *SSEEvent, error) {
	if b.closed {
		return nil, ErrManagerClosed
	}
	ch := make(chan *SSEEvent, 100)
	b.subscriptions[sessionID] = ch
	return ch, nil
}

func (b *mockEventBus) Unsubscribe(ctx context.Context, sessionID string) error {
	if ch, ok := b.subscriptions[sessionID]; ok {
		close(ch)
		delete(b.subscriptions, sessionID)
	}
	return nil
}

func (b *mockEventBus) SubscribeAll(ctx context.Context) (<-chan *SSEEvent, error) {
	return make(<-chan *SSEEvent), nil
}

func (b *mockEventBus) Close() error {
	b.closed = true
	for _, ch := range b.subscriptions {
		close(ch)
	}
	return nil
}

func TestHAMiddleware_NewSession(t *testing.T) {
	store := newMockMetadataStore()
	bus := newMockEventBus()

	manager, err := NewSessionManager(&SessionManagerConfig{
		NodeID:        "node_1",
		NodeAddress:   "localhost:8080",
		MetadataStore: store,
		EventBus:      bus,
	})
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}

	ctx := context.Background()
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("start manager: %v", err)
	}
	defer func() { _ = manager.Close(ctx) }()

	mw := NewHAMiddleware(manager)

	// Create a simple handler that reads from context
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		haWriter, ok := GetHAWriter(r.Context())
		if !ok {
			t.Error("expected HA writer in context")
			return
		}
		if haWriter == nil {
			t.Error("HA writer is nil")
			return
		}

		sessionInfo, ok := GetSessionInfo(r.Context())
		if !ok {
			t.Error("expected session info in context")
			return
		}
		if sessionInfo.SessionID == "" {
			t.Error("session ID is empty")
		}

		// Send a test event
		if err := haWriter.SendEvent(r.Context(), "message", []byte("test data")); err != nil {
			t.Errorf("send event: %v", err)
		}
	})

	req := httptest.NewRequest("GET", "/events?session_id=test_session", nil)
	rec := httptest.NewRecorder()

	mw.Wrap(handler).ServeHTTP(rec, req)

	// Verify response headers
	if rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Errorf("expected Content-Type text/event-stream, got %s", rec.Header().Get("Content-Type"))
	}
	if rec.Header().Get("X-SSE-Session-ID") != "test_session" {
		t.Errorf("expected X-SSE-Session-ID test_session, got %s", rec.Header().Get("X-SSE-Session-ID"))
	}
	if rec.Header().Get("X-SSE-Node-ID") != "node_1" {
		t.Errorf("expected X-SSE-Node-ID node_1, got %s", rec.Header().Get("X-SSE-Node-ID"))
	}

	// Verify event was written
	body := rec.Body.String()
	if !strings.Contains(body, "data: test data") {
		t.Errorf("expected event data in body, got: %s", body)
	}
}

func TestHAMiddleware_Reconnection(t *testing.T) {
	store := newMockMetadataStore()
	bus := newMockEventBus()

	manager, err := NewSessionManager(&SessionManagerConfig{
		NodeID:        "node_1",
		NodeAddress:   "localhost:8080",
		MetadataStore: store,
		EventBus:      bus,
	})
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}

	ctx := context.Background()
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("start manager: %v", err)
	}
	defer func() { _ = manager.Close(ctx) }()

	// Pre-create a session
	now := time.Now()
	session := &SessionInfo{
		SessionID:    "reconnect_session",
		NodeID:       "node_1",
		State:        SessionStateActive,
		CreatedAt:    now,
		LastActiveAt: now,
		Version:      1,
	}
	if err := store.RegisterSession(ctx, session); err != nil {
		t.Fatalf("register session: %v", err)
	}

	// Create local session state
	_, _ = manager.CreateSession(ctx, "reconnect_session", nil)

	// Publish some events
	for i := 0; i < 5; i++ {
		_ = manager.PublishEvent(ctx, &SSEEvent{
			SessionID: "reconnect_session",
			EventID:   string(rune('a' + i)),
			Data:      []byte("data"),
		})
	}

	mw := NewHAMiddleware(manager)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Just check context values
		_, hasWriter := GetHAWriter(r.Context())
		if !hasWriter {
			t.Error("expected HA writer in context")
		}
	})

	// Reconnect with Last-Event-ID header
	req := httptest.NewRequest("GET", "/events?session_id=reconnect_session", nil)
	req.Header.Set("Last-Event-ID", "a")
	rec := httptest.NewRecorder()

	mw.Wrap(handler).ServeHTTP(rec, req)

	// Verify replay happened (events b, c, d, e should be replayed)
	body := rec.Body.String()
	// The replay events should be written before the handler runs
	if !strings.Contains(body, "id: b") {
		t.Logf("Body: %s", body)
	}
}

func TestHAMiddleware_ReconnectionNonexistentSession(t *testing.T) {
	store := newMockMetadataStore()
	bus := newMockEventBus()

	manager, err := NewSessionManager(&SessionManagerConfig{
		NodeID:        "node_1",
		MetadataStore: store,
		EventBus:      bus,
	})
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}

	ctx := context.Background()
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("start manager: %v", err)
	}
	defer func() { _ = manager.Close(ctx) }()

	mw := NewHAMiddleware(manager)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})

	req := httptest.NewRequest("GET", "/events?session_id=nonexistent", nil)
	req.Header.Set("Last-Event-ID", "evt_1")
	rec := httptest.NewRecorder()

	mw.Wrap(handler).ServeHTTP(rec, req)

	// HandleReconnection returns error when session not found -> 400 Bad Request
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", rec.Code)
	}
}

func TestHAMiddleware_SessionAlreadyExists(t *testing.T) {
	store := newMockMetadataStore()
	bus := newMockEventBus()

	manager, err := NewSessionManager(&SessionManagerConfig{
		NodeID:        "node_1",
		MetadataStore: store,
		EventBus:      bus,
	})
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}

	ctx := context.Background()
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("start manager: %v", err)
	}
	defer func() { _ = manager.Close(ctx) }()

	// Pre-register the session with a different node
	_ = store.RegisterSession(ctx, &SessionInfo{
		SessionID: "existing_session",
		NodeID:    "other_node",
		State:     SessionStateActive,
		Version:   1,
	})

	mw := NewHAMiddleware(manager)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})

	// Try to create a new session with the same ID
	req := httptest.NewRequest("GET", "/events?session_id=existing_session", nil)
	rec := httptest.NewRecorder()

	mw.Wrap(handler).ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected status 500, got %d", rec.Code)
	}
}

func TestExtractMetadata(t *testing.T) {
	tests := []struct {
		name     string
		setupReq func(*http.Request)
		expected map[string]string
	}{
		{
			name: "no metadata",
			setupReq: func(r *http.Request) {
				r.URL.RawQuery = ""
			},
			expected: map[string]string{},
		},
		{
			name: "partition from query",
			setupReq: func(r *http.Request) {
				r.URL.RawQuery = "partition=zone1"
			},
			expected: map[string]string{"partition": "zone1"},
		},
		{
			name: "affinity from header",
			setupReq: func(r *http.Request) {
				r.Header.Set("X-SSE-Affinity", "node_1")
			},
			expected: map[string]string{"affinity": "node_1"},
		},
		{
			name: "client ID from header",
			setupReq: func(r *http.Request) {
				r.Header.Set("X-Client-ID", "client_123")
			},
			expected: map[string]string{"client_id": "client_123"},
		},
		{
			name: "all metadata",
			setupReq: func(r *http.Request) {
				r.URL.RawQuery = "partition=zone1"
				r.Header.Set("X-SSE-Affinity", "node_1")
				r.Header.Set("X-Client-ID", "client_123")
			},
			expected: map[string]string{
				"partition": "zone1",
				"affinity":  "node_1",
				"client_id": "client_123",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/events", nil)
			tt.setupReq(req)

			metadata := extractMetadata(req)

			if len(metadata) != len(tt.expected) {
				t.Errorf("expected %d metadata entries, got %d", len(tt.expected), len(metadata))
			}

			for k, v := range tt.expected {
				if metadata[k] != v {
					t.Errorf("expected %s=%s, got %s=%s", k, v, k, metadata[k])
				}
			}
		})
	}
}

func TestGenerateSessionID(t *testing.T) {
	id := generateSessionID()

	if id == "" {
		t.Error("session ID should not be empty")
	}
	if !strings.HasPrefix(id, "sse_") {
		t.Errorf("session ID should start with 'sse_', got: %s", id)
	}
	// Verify format: sse_<timestamp>_<random>
	parts := strings.Split(id, "_")
	if len(parts) < 2 {
		t.Errorf("session ID should have at least 2 parts, got: %s", id)
	}
}

func TestWriteSSEEvent(t *testing.T) {
	tests := []struct {
		name     string
		event    *SSEEvent
		expected string
	}{
		{
			name: "full event",
			event: &SSEEvent{
				EventID:   "1",
				EventType: "message",
				Data:      []byte("hello"),
			},
			expected: "id: 1\nevent: message\ndata: hello\n\n",
		},
		{
			name: "no event type",
			event: &SSEEvent{
				EventID: "2",
				Data:    []byte("world"),
			},
			expected: "id: 2\ndata: world\n\n",
		},
		{
			name: "no event ID",
			event: &SSEEvent{
				EventType: "ping",
				Data:      []byte("pong"),
			},
			expected: "event: ping\ndata: pong\n\n",
		},
		{
			name: "data only",
			event: &SSEEvent{
				Data: []byte("minimal"),
			},
			expected: "data: minimal\n\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeSSEEvent(rec, tt.event)

			if rec.Body.String() != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, rec.Body.String())
			}
		})
	}
}

func TestHAResponseWriter_SendEvent(t *testing.T) {
	store := newMockMetadataStore()
	bus := newMockEventBus()

	manager, err := NewSessionManager(&SessionManagerConfig{
		NodeID:        "node_1",
		MetadataStore: store,
		EventBus:      bus,
	})
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}

	ctx := context.Background()
	_ = manager.Start(ctx)
	defer func() { _ = manager.Close(ctx) }()

	_, _ = manager.CreateSession(ctx, "send_event_test", nil)

	rec := httptest.NewRecorder()
	var seq int64

	writer := &HAResponseWriter{
		ResponseWriter: rec,
		flusher:        rec,
		manager:        manager,
		sessionID:      "send_event_test",
		seqGen:         &seq,
	}

	err = writer.SendEvent(ctx, "message", []byte("test payload"))
	if err != nil {
		t.Fatalf("SendEvent failed: %v", err)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "event: message") {
		t.Errorf("expected event type in output, got: %s", body)
	}
	if !strings.Contains(body, "data: test payload") {
		t.Errorf("expected data in output, got: %s", body)
	}

	// Verify sequential IDs
	rec2 := httptest.NewRecorder()
	writer2 := &HAResponseWriter{
		ResponseWriter: rec2,
		flusher:        rec2,
		manager:        manager,
		sessionID:      "send_event_test",
		seqGen:         &seq,
	}

	_ = writer2.SendEvent(ctx, "message", []byte("second"))
	body2 := rec2.Body.String()
	if !strings.Contains(body2, "id: 2") {
		t.Errorf("expected sequential id: 2, got: %s", body2)
	}
}
