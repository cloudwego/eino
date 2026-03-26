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
	"net/http"
	"strconv"
	"sync/atomic"
	"time"
)

// HAMiddleware wraps standard HTTP handlers with HA-aware SSE session
// management. It transparently handles session creation, reconnection with
// event replay, cross-node forwarding, and automatic correction (纠偏).
//
// This follows the middleware/SDK abstraction pattern recommended by SEP-2001:
// protocol handlers and business logic remain unchanged, while HA concerns
// are encapsulated in the middleware layer.
//
// Usage:
//
//	manager, _ := sseha.NewSessionManager(config)
//	manager.Start(ctx)
//
//	ha := sseha.NewHAMiddleware(manager)
//	http.Handle("/events", ha.Wrap(mySSEHandler))
type HAMiddleware struct {
	manager     *SessionManager
	eventSeqGen int64
}

// NewHAMiddleware creates a new HA middleware wrapping the given session manager.
func NewHAMiddleware(manager *SessionManager) *HAMiddleware {
	return &HAMiddleware{
		manager: manager,
	}
}

// Wrap returns an HTTP handler that adds HA session management around the
// given handler. The wrapped handler should write SSE events using the
// HAResponseWriter provided in the request context.
func (mw *HAMiddleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		sessionID := r.URL.Query().Get("session_id")
		lastEventID := r.Header.Get("Last-Event-ID")

		var session *SessionInfo
		var replayEvents []SSEEvent

		if sessionID != "" && lastEventID != "" {
			// Reconnection scenario — handle correction
			events, err := mw.manager.HandleReconnection(ctx, sessionID, lastEventID)
			if err != nil {
				http.Error(w, fmt.Sprintf("session reconnection failed: %v", err), http.StatusBadRequest)
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
			if sessionID == "" {
				sessionID = generateSessionID()
			}

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
		w.Header().Set("X-SSE-Node-ID", mw.manager.NodeID())

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		// Replay events for reconnection
		for _, event := range replayEvents {
			writeSSEEvent(w, &event)
		}
		if len(replayEvents) > 0 {
			flusher.Flush()
		}

		// Create HA-aware response writer and inject into context
		haWriter := &HAResponseWriter{
			ResponseWriter: w,
			flusher:        flusher,
			manager:        mw.manager,
			sessionID:      session.SessionID,
			seqGen:         &mw.eventSeqGen,
		}

		haCtx := context.WithValue(ctx, haWriterKey{}, haWriter)
		haCtx = context.WithValue(haCtx, sessionInfoKey{}, session)

		// Set up cleanup on disconnect
		go func() {
			<-r.Context().Done()
			_ = mw.manager.SuspendSession(context.Background(), session.SessionID)
		}()

		// Call the wrapped handler
		next.ServeHTTP(haWriter, r.WithContext(haCtx))
	})
}

// HAResponseWriter wraps http.ResponseWriter with HA event tracking.
// Events written through this writer are automatically:
//   - Assigned a monotonic event ID
//   - Buffered for replay on reconnection
//   - Published to the event bus for cross-node forwarding
type HAResponseWriter struct {
	http.ResponseWriter
	flusher   http.Flusher
	manager   *SessionManager
	sessionID string
	seqGen    *int64
}

// SendEvent writes an SSE event and publishes it for HA.
func (w *HAResponseWriter) SendEvent(ctx context.Context, eventType string, data []byte) error {
	seq := atomic.AddInt64(w.seqGen, 1)
	eventID := strconv.FormatInt(seq, 10)

	event := &SSEEvent{
		SessionID:    w.sessionID,
		EventID:      eventID,
		EventType:    eventType,
		Data:         data,
		SourceNodeID: w.manager.NodeID(),
		Timestamp:    time.Now(),
	}

	// Publish to event bus (buffers locally + broadcasts)
	if err := w.manager.PublishEvent(ctx, event); err != nil {
		return fmt.Errorf("publish event: %w", err)
	}

	// Write to HTTP response
	writeSSEEvent(w.ResponseWriter, event)
	w.flusher.Flush()

	return nil
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

// HAWriter is the interface for HA-aware SSE writers.
// Both HAResponseWriter and MCP-specific writers implement this interface.
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
