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
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// HAMiddleware implements the MCP protocol over SSE transport with HA support.
//
// MCP uses a dual-channel communication pattern:
//  1. GET /sse - Establishes SSE connection, receives 'endpoint' event with session_id
//  2. POST /messages?session_id=xxx - Sends JSON-RPC requests, returns 202 Accepted
//     Response is delivered via the SSE connection
//
// This implementation follows the MCP specification for SSE transport:
// - https://spec.modelcontextprotocol.io/specification/basic/transports/
//
// Usage:
//
//	manager, _ := sseha.NewSessionManager(config)
//	manager.Start(ctx)
//
//	ha := sseha.NewHAMiddleware(manager)
//	http.Handle("/", ha.Handler(myMCPHandler))
type HAMiddleware struct {
	manager     *SessionManager
	eventSeqGen int64

	// sessions tracks active SSE connections by session_id
	sessions sync.Map // map[string]*mcpSession
}

// mcpSession tracks an active MCP SSE connection.
type mcpSession struct {
	sessionInfo *SessionInfo
	eventChan   chan *SSEEvent
	cancelFunc  context.CancelFunc
	mu          sync.Mutex
}

// NewHAMiddleware creates a new HA middleware for MCP protocol.
func NewHAMiddleware(manager *SessionManager) *HAMiddleware {
	return &HAMiddleware{
		manager: manager,
	}
}

// Handler returns an http.Handler that implements MCP protocol.
// It routes requests based on path:
//   - GET /sse -> SSE connection endpoint
//   - POST /messages -> JSON-RPC request endpoint
//
// Other paths are passed to the next handler.
func (mw *HAMiddleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case r.Method == http.MethodGet && path == "/sse":
			mw.handleSSEConnect(w, r)
		case r.Method == http.MethodPost && (path == "/messages" || strings.HasPrefix(path, "/messages")):
			mw.handleMessage(w, r, next)
		default:
			// Pass through to next handler for other paths
			if next != nil {
				next.ServeHTTP(w, r)
			} else {
				http.NotFound(w, r)
			}
		}
	})
}

// handleSSEConnect handles GET /sse requests.
// It establishes an SSE connection and sends the 'endpoint' event.
func (mw *HAMiddleware) handleSSEConnect(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Check for reconnection with Last-Event-ID
	sessionID := r.URL.Query().Get("session_id")
	lastEventID := r.Header.Get("Last-Event-ID")

	var session *SessionInfo
	var replayEvents []SSEEvent

	if sessionID != "" && lastEventID != "" {
		// Reconnection scenario - replay events
		events, err := mw.manager.HandleReconnection(ctx, sessionID, lastEventID)
		if err != nil {
			http.Error(w, fmt.Sprintf("reconnection failed: %v", err), http.StatusBadRequest)
			return
		}
		replayEvents = events

		info, err := mw.manager.Store().GetSession(ctx, sessionID)
		if err != nil || info == nil {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		session = info
	} else {
		// New session
		sessionID = generateSessionID()
		metadata := extractMetadata(r)
		info, err := mw.manager.CreateSession(ctx, sessionID, metadata)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to create session: %v", err), http.StatusInternalServerError)
			return
		}
		session = info
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-SSE-Session-ID", session.SessionID)

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Create session event channel
	sess := &mcpSession{
		sessionInfo: session,
		eventChan:   make(chan *SSEEvent, 100),
	}

	sessCtx, cancel := context.WithCancel(ctx)
	sess.cancelFunc = cancel
	mw.sessions.Store(sessionID, sess)
	defer func() {
		mw.sessions.Delete(sessionID)
		close(sess.eventChan)
	}()

	// Send endpoint event first (MCP protocol requirement)
	endpointEvent := &SSEEvent{
		EventType: "endpoint",
		Data:      []byte(fmt.Sprintf("/messages/?session_id=%s", sessionID)),
	}
	writeSSEEvent(w, endpointEvent)
	flusher.Flush()

	// Replay events for reconnection
	for _, event := range replayEvents {
		writeSSEEvent(w, &event)
	}
	if len(replayEvents) > 0 {
		flusher.Flush()
	}

	// Start ping ticker to keep connection alive
	pingTicker := time.NewTicker(15 * time.Second)
	defer pingTicker.Stop()

	// Event loop
	for {
		select {
		case <-sessCtx.Done():
			return

		case <-r.Context().Done():
			// Client disconnected
			_ = mw.manager.SuspendSession(context.Background(), sessionID)
			return

		case event := <-sess.eventChan:
			writeSSEEvent(w, event)
			flusher.Flush()

		case <-pingTicker.C:
			// Send SSE comment as ping
			fmt.Fprintf(w, ": ping - %s\n\n", time.Now().Format(time.RFC3339))
			flusher.Flush()
		}
	}
}

// handleMessage handles POST /messages?session_id=xxx requests.
// It receives JSON-RPC requests and routes them through the handler,
// then sends responses via the SSE connection.
func (mw *HAMiddleware) handleMessage(w http.ResponseWriter, r *http.Request, handler http.Handler) {
	ctx := r.Context()

	// Extract session_id from query
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		http.Error(w, "missing session_id", http.StatusBadRequest)
		return
	}

	// Find the session
	sessI, ok := mw.sessions.Load(sessionID)
	if !ok {
		// Session not found - might need to reconnect
		http.Error(w, "session not found, reconnect via GET /sse", http.StatusNotFound)
		return
	}

	sess := sessI.(*mcpSession)

	// Read request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	// Return 202 Accepted immediately (MCP protocol)
	w.WriteHeader(http.StatusAccepted)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	// Inject HA writer into context that sends events via the session channel
	haWriter := &mcpHAWriter{
		session:      sess,
		seqGen:       &mw.eventSeqGen,
		manager:      mw.manager,
		sourceNodeID: mw.manager.NodeID(),
	}

	haCtx := context.WithValue(ctx, haWriterKey{}, haWriter)
	haCtx = context.WithValue(haCtx, sessionInfoKey{}, sess.sessionInfo)

	// Store request body in context for handler to access
	haCtx = context.WithValue(haCtx, requestBodyKey{}, body)

	// Create a mock request for the handler
	mockReq, _ := http.NewRequestWithContext(haCtx, "POST", "/internal", nil)

	// Call the handler synchronously - it will send response via SSE
	rec := newResponseRecorder()
	handler.ServeHTTP(rec, mockReq)
}

// SendEventToSession sends an event to a specific session's SSE connection.
func (mw *HAMiddleware) SendEventToSession(sessionID string, event *SSEEvent) error {
	sessI, ok := mw.sessions.Load(sessionID)
	if !ok {
		return fmt.Errorf("session %s not found", sessionID)
	}

	sess := sessI.(*mcpSession)
	select {
	case sess.eventChan <- event:
		return nil
	default:
		return fmt.Errorf("session %s event channel full", sessionID)
	}
}

// mcpHAWriter implements HAWriter for MCP protocol.
type mcpHAWriter struct {
	session      *mcpSession
	seqGen       *int64
	manager      *SessionManager
	sourceNodeID string
}

// SendEvent sends an SSE event via the session's event channel.
func (w *mcpHAWriter) SendEvent(ctx context.Context, eventType string, data []byte) error {
	seq := atomic.AddInt64(w.seqGen, 1)
	eventID := fmt.Sprintf("%d", seq)

	event := &SSEEvent{
		SessionID:    w.session.sessionInfo.SessionID,
		EventID:      eventID,
		EventType:    eventType,
		Data:         data,
		SourceNodeID: w.sourceNodeID,
		Timestamp:    time.Now(),
	}

	// Publish to event bus for HA
	_ = w.manager.PublishEvent(ctx, event)

	// Send to session's event channel
	select {
	case w.session.eventChan <- event:
		return nil
	default:
		return fmt.Errorf("event channel full")
	}
}

// writeSSEEvent formats and writes an SSE event to the response.
func writeSSEEvent(w http.ResponseWriter, event *SSEEvent) {
	if event.EventID != "" {
		fmt.Fprintf(w, "id: %s\n", event.EventID)
	}
	if event.EventType != "" {
		fmt.Fprintf(w, "event: %s\n", event.EventType)
	}
	fmt.Fprintf(w, "data: %s\n\n", string(event.Data))
}

// Context keys for accessing HA objects from within handlers.
type haWriterKey struct{}
type sessionInfoKey struct{}
type requestBodyKey struct{}

// HAWriter is the interface for HA-aware SSE writers.
type HAWriter interface {
	SendEvent(ctx context.Context, eventType string, data []byte) error
}

// GetHAWriter retrieves the HAWriter from the request context.
func GetHAWriter(ctx context.Context) (HAWriter, bool) {
	w, ok := ctx.Value(haWriterKey{}).(HAWriter)
	return w, ok
}

// GetSessionInfo retrieves the current SessionInfo from the request context.
func GetSessionInfo(ctx context.Context) (*SessionInfo, bool) {
	info, ok := ctx.Value(sessionInfoKey{}).(*SessionInfo)
	return info, ok
}

// GetRequestBody retrieves the request body from the context.
func GetRequestBody(ctx context.Context) []byte {
	body, _ := ctx.Value(requestBodyKey{}).([]byte)
	return body
}

// extractMetadata pulls session hints from the request headers/query.
func extractMetadata(r *http.Request) map[string]string {
	metadata := make(map[string]string)

	// Partition hint from query parameter
	if partition := r.URL.Query().Get("partition"); partition != "" {
		metadata["partition"] = partition
	}

	// Affinity hint from header
	if affinity := r.Header.Get("X-SSE-Affinity"); affinity != "" {
		metadata["affinity"] = affinity
	}

	// Client ID for tracking
	if clientID := r.Header.Get("X-Client-ID"); clientID != "" {
		metadata["client_id"] = clientID
	}

	return metadata
}

// generateSessionID creates a unique session ID.
// The ID encodes a partition hint for session affinity (SEP-2001 §2.2).
func generateSessionID() string {
	now := time.Now()
	return fmt.Sprintf("sse_%d_%d", now.UnixNano(), now.UnixNano()%1000)
}

// responseRecorder is a simple response recorder for internal use.
type responseRecorder struct {
	header http.Header
	body   []byte
	status int
}

func newResponseRecorder() *responseRecorder {
	return &responseRecorder{
		header: make(http.Header),
		status: http.StatusOK,
	}
}

func (r *responseRecorder) Header() http.Header {
	return r.header
}

func (r *responseRecorder) Write(data []byte) (int, error) {
	r.body = append(r.body, data...)
	return len(data), nil
}

func (r *responseRecorder) WriteHeader(statusCode int) {
	r.status = statusCode
}
