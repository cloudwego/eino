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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// StreamableMiddleware implements the MCP protocol over Streamable HTTP transport
// with HA support.
//
// Streamable HTTP is the recommended transport for MCP (2025-03-26 protocol version).
// Key differences from SSE transport:
//   - Single POST endpoint instead of dual-channel (GET /sse + POST /messages)
//   - Session ID passed via Mcp-Session-Id header instead of URL query
//   - Response can be JSON or upgraded to SSE stream
//   - Supports stateless mode (server can choose not to create session)
//
// This implementation follows the MCP specification for Streamable HTTP transport:
// - https://spec.modelcontextprotocol.io/specification/basic/transports/
//
// Usage:
//
//	manager, _ := sseha.NewSessionManager(config)
//	manager.Start(ctx)
//
//	streamMW := sseha.NewStreamableMiddleware(manager)
//	http.Handle("/mcp", streamMW.Handler(myMCPHandler))
type StreamableMiddleware struct {
	manager     *SessionManager
	eventSeqGen int64

	// sessions tracks active SSE streaming connections by session_id
	// (for responses that upgrade to SSE)
	sessions sync.Map // map[string]*streamableSession

	// StatelessMode controls whether the server creates sessions.
	// If true, no session management is performed (useful for stateless deployments).
	StatelessMode bool
}

// streamableSession tracks an active SSE streaming response.
type streamableSession struct {
	sessionInfo *SessionInfo
	eventChan   chan *SSEEvent
	mu          sync.Mutex
}

// NewStreamableMiddleware creates a new middleware for Streamable HTTP transport.
func NewStreamableMiddleware(manager *SessionManager) *StreamableMiddleware {
	return &StreamableMiddleware{
		manager: manager,
	}
}

// Handler returns an http.Handler that implements Streamable HTTP transport.
//
// Expected request format:
//   - POST /mcp (or any configured path)
//   - Optional: Mcp-Session-Id header for session continuation
//   - Content-Type: application/json
//
// Response format:
//   - For new sessions: Mcp-Session-Id header in response
//   - For streaming responses: Content-Type: text/event-stream
//   - For non-streaming: Content-Type: application/json
func (mw *StreamableMiddleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		mw.handleRequest(w, r, next)
	})
}

// handleRequest processes a single MCP request.
func (mw *StreamableMiddleware) handleRequest(w http.ResponseWriter, r *http.Request, handler http.Handler) {
	ctx := r.Context()

	// Read request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	// Check for existing session via header
	sessionID := r.Header.Get("Mcp-Session-Id")

	var session *SessionInfo

	if sessionID != "" {
		// Existing session - validate it
		info, err := mw.manager.Store().GetSession(ctx, sessionID)
		if err != nil || info == nil {
			// Session not found - client needs to reinitialize
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		session = info
	} else if !mw.StatelessMode {
		// New session - create one
		sessionID = generateSessionID()
		metadata := extractStreamableMetadata(r)
		info, err := mw.manager.CreateSession(ctx, sessionID, metadata)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to create session: %v", err), http.StatusInternalServerError)
			return
		}
		session = info
	}

	// Check if client wants SSE streaming
	acceptHeader := r.Header.Get("Accept")
	wantsSSE := strings.Contains(acceptHeader, "text/event-stream")

	// Check if request is a JSON-RPC notification (no response expected)
	isNotification := mw.isNotification(body)

	if isNotification {
		// Notification - no response body, just process and return 202
		mw.processNotification(ctx, sessionID, body, handler)
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// For streaming responses, set up SSE
	if wantsSSE {
		mw.handleStreamingRequest(w, r, sessionID, session, body, handler)
		return
	}

	// Non-streaming request - process and return JSON response
	mw.handleNonStreamingRequest(w, r, sessionID, session, body, handler)
}

// handleStreamingRequest handles a request that wants SSE streaming response.
func (mw *StreamableMiddleware) handleStreamingRequest(
	w http.ResponseWriter,
	r *http.Request,
	sessionID string,
	session *SessionInfo,
	body []byte,
	handler http.Handler,
) {
	ctx := r.Context()

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Set session ID header
	if sessionID != "" {
		w.Header().Set("Mcp-Session-Id", sessionID)
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Create or reuse session event channel
	var streamSess *streamableSession
	if existingSess, ok := mw.sessions.Load(sessionID); ok {
		streamSess = existingSess.(*streamableSession)
	} else {
		streamSess = &streamableSession{
			sessionInfo: session,
			eventChan:   make(chan *SSEEvent, 100),
		}
		mw.sessions.Store(sessionID, streamSess)

		defer func() {
			mw.sessions.Delete(sessionID)
			close(streamSess.eventChan)
		}()
	}

	// Inject HA writer and request body into context
	haWriter := &streamableHAWriter{
		session:      streamSess,
		seqGen:       &mw.eventSeqGen,
		manager:      mw.manager,
		sourceNodeID: mw.manager.NodeID(),
	}

	haCtx := context.WithValue(ctx, haWriterKey{}, haWriter)
	if session != nil {
		haCtx = context.WithValue(haCtx, sessionInfoKey{}, session)
	}
	haCtx = context.WithValue(haCtx, requestBodyKey{}, body)

	// Process the request in a goroutine
	done := make(chan struct{})
	go func() {
		defer close(done)
		mockReq, _ := http.NewRequestWithContext(haCtx, "POST", "/internal", nil)
		rec := newResponseRecorder()
		handler.ServeHTTP(rec, mockReq)
	}()

	// Stream events to client
	pingTicker := time.NewTicker(15 * time.Second)
	defer pingTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Client disconnected
			if sessionID != "" {
				_ = mw.manager.SuspendSession(context.Background(), sessionID)
			}
			return

		case <-done:
			// Handler finished - flush any remaining events and close
			flusher.Flush()
			return

		case event := <-streamSess.eventChan:
			writeSSEEvent(w, event)
			flusher.Flush()

		case <-pingTicker.C:
			fmt.Fprintf(w, ": ping - %s\n\n", time.Now().Format(time.RFC3339))
			flusher.Flush()
		}
	}
}

// handleNonStreamingRequest handles a request with JSON response.
func (mw *StreamableMiddleware) handleNonStreamingRequest(
	w http.ResponseWriter,
	r *http.Request,
	sessionID string,
	session *SessionInfo,
	body []byte,
	handler http.Handler,
) {
	ctx := r.Context()

	// Set JSON response headers
	w.Header().Set("Content-Type", "application/json")
	if sessionID != "" {
		w.Header().Set("Mcp-Session-Id", sessionID)
	}

	// For non-streaming, we need to collect the response synchronously
	// Create a temporary event collector
	eventCollector := &jsonResponseCollector{
		events: make([]json.RawMessage, 0),
	}

	haWriter := &collectorHAWriter{
		collector:    eventCollector,
		seqGen:       &mw.eventSeqGen,
		manager:      mw.manager,
		sourceNodeID: mw.manager.NodeID(),
		sessionID:    sessionID,
	}

	haCtx := context.WithValue(ctx, haWriterKey{}, haWriter)
	if session != nil {
		haCtx = context.WithValue(haCtx, sessionInfoKey{}, session)
	}
	haCtx = context.WithValue(haCtx, requestBodyKey{}, body)

	// Process the request
	mockReq, _ := http.NewRequestWithContext(haCtx, "POST", "/internal", nil)
	rec := newResponseRecorder()
	handler.ServeHTTP(rec, mockReq)

	// Write the collected response
	if len(eventCollector.events) > 0 {
		// Return the last event as the response (typical for request/response pattern)
		lastEvent := eventCollector.events[len(eventCollector.events)-1]
		w.Write(lastEvent)
	} else {
		// No events - return empty response
		w.Write([]byte("{}"))
	}
}

// isNotification checks if the JSON-RPC request is a notification (no id field).
func (mw *StreamableMiddleware) isNotification(body []byte) bool {
	var req struct {
		ID any `json:"id"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return false
	}
	return req.ID == nil
}

// processNotification processes a JSON-RPC notification (no response expected).
func (mw *StreamableMiddleware) processNotification(
	ctx context.Context,
	sessionID string,
	body []byte,
	handler http.Handler,
) {
	haCtx := context.WithValue(ctx, requestBodyKey{}, body)

	// Look up session info if available
	if sessionID != "" {
		if info, err := mw.manager.Store().GetSession(ctx, sessionID); err == nil && info != nil {
			haCtx = context.WithValue(haCtx, sessionInfoKey{}, info)
		}
	}

	mockReq, _ := http.NewRequestWithContext(haCtx, "POST", "/internal", nil)
	rec := newResponseRecorder()
	handler.ServeHTTP(rec, mockReq)
}

// SendEventToSession sends an event to a specific session's SSE stream.
func (mw *StreamableMiddleware) SendEventToSession(sessionID string, event *SSEEvent) error {
	sessI, ok := mw.sessions.Load(sessionID)
	if !ok {
		return fmt.Errorf("session %s not found or not streaming", sessionID)
	}

	sess := sessI.(*streamableSession)
	select {
	case sess.eventChan <- event:
		return nil
	default:
		return fmt.Errorf("session %s event channel full", sessionID)
	}
}

// streamableHAWriter implements HAWriter for Streamable HTTP SSE streaming.
type streamableHAWriter struct {
	session      *streamableSession
	seqGen       *int64
	manager      *SessionManager
	sourceNodeID string
}

// SendEvent sends an SSE event via the session's event channel.
func (w *streamableHAWriter) SendEvent(ctx context.Context, eventType string, data []byte) error {
	seq := atomic.AddInt64(w.seqGen, 1)
	eventID := fmt.Sprintf("%d", seq)

	var sessionID string
	if w.session != nil && w.session.sessionInfo != nil {
		sessionID = w.session.sessionInfo.SessionID
	}

	event := &SSEEvent{
		SessionID:    sessionID,
		EventID:      eventID,
		EventType:    eventType,
		Data:         data,
		SourceNodeID: w.sourceNodeID,
		Timestamp:    time.Now(),
	}

	// Publish to event bus for HA
	if sessionID != "" && w.manager != nil {
		_ = w.manager.PublishEvent(ctx, event)
	}

	// Send to session's event channel
	if w.session != nil {
		select {
		case w.session.eventChan <- event:
			return nil
		default:
			return fmt.Errorf("event channel full")
		}
	}

	return nil
}

// jsonResponseCollector collects events for non-streaming JSON responses.
type jsonResponseCollector struct {
	events []json.RawMessage
}

// collectorHAWriter implements HAWriter for collecting events into JSON response.
type collectorHAWriter struct {
	collector    *jsonResponseCollector
	seqGen       *int64
	manager      *SessionManager
	sourceNodeID string
	sessionID    string
}

// SendEvent collects the event data for JSON response.
func (w *collectorHAWriter) SendEvent(ctx context.Context, eventType string, data []byte) error {
	w.collector.events = append(w.collector.events, data)

	// Also publish to event bus for HA
	if w.sessionID != "" && w.manager != nil {
		seq := atomic.AddInt64(w.seqGen, 1)
		event := &SSEEvent{
			SessionID:    w.sessionID,
			EventID:      fmt.Sprintf("%d", seq),
			EventType:    eventType,
			Data:         data,
			SourceNodeID: w.sourceNodeID,
			Timestamp:    time.Now(),
		}
		_ = w.manager.PublishEvent(ctx, event)
	}

	return nil
}

// extractStreamableMetadata extracts session metadata from request headers.
func extractStreamableMetadata(r *http.Request) map[string]string {
	metadata := make(map[string]string)

	// Client ID for tracking
	if clientID := r.Header.Get("X-Client-ID"); clientID != "" {
		metadata["client_id"] = clientID
	}

	// Protocol version hint
	if version := r.Header.Get("Mcp-Protocol-Version"); version != "" {
		metadata["protocol_version"] = version
	}

	return metadata
}
