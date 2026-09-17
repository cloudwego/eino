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

package sseha_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk/transport/mcp/sseha"
	"github.com/cloudwego/eino/adk/transport/mcp/sseha/redis"
)

// StreamableClient simulates an MCP client using Streamable HTTP transport.
type StreamableClient struct {
	client    *http.Client
	baseURL   string
	sessionID string
}

// NewStreamableClient creates a new Streamable HTTP client.
func NewStreamableClient(baseURL string) *StreamableClient {
	return &StreamableClient{
		client:  &http.Client{Timeout: 30 * time.Second},
		baseURL: baseURL,
	}
}

// JSONRPCRequest represents a JSON-RPC 2.0 request.
type JSONRPCReq struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      int            `json:"id,omitempty"`
	Method  string         `json:"method"`
	Params  map[string]any `json:"params,omitempty"`
}

// JSONRPCResp represents a JSON-RPC 2.0 response.
type JSONRPCResp struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      int           `json:"id,omitempty"`
	Result  map[string]any `json:"result,omitempty"`
	Error   *RPCError     `json:"error,omitempty"`
}

// RPCError represents a JSON-RPC 2.0 error.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// SendRequest sends a JSON-RPC request and returns the response.
// For streaming requests, use SendStreamingRequest.
func (c *StreamableClient) SendRequest(ctx context.Context, req *JSONRPCReq) (*JSONRPCResp, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/mcp", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if c.sessionID != "" {
		httpReq.Header.Set("Mcp-Session-Id", c.sessionID)
	}

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	// Extract session ID from response
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		c.sessionID = sid
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	var result JSONRPCResp
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &result, nil
}

// SendStreamingRequest sends a request and returns an SSE event channel.
func (c *StreamableClient) SendStreamingRequest(ctx context.Context, req *JSONRPCReq) (<-chan StreamSSEEvent, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/mcp", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if c.sessionID != "" {
		httpReq.Header.Set("Mcp-Session-Id", c.sessionID)
	}

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	// Extract session ID from response
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		c.sessionID = sid
	}

	eventCh := make(chan StreamSSEEvent, 100)
	go c.readSSEEvents(ctx, resp.Body, eventCh)

	return eventCh, nil
}

// StreamSSEEvent represents a parsed SSE event.
type StreamSSEEvent struct {
	Type string
	Data string
	ID   string
}

// readSSEEvents parses SSE stream and sends events to channel.
func (c *StreamableClient) readSSEEvents(ctx context.Context, body io.ReadCloser, eventCh chan<- StreamSSEEvent) {
	defer close(eventCh)
	defer body.Close()

	scanner := bufio.NewScanner(body)
	var currentEvent StreamSSEEvent

	for scanner.Scan() {
		line := scanner.Text()

		if line == "" {
			if currentEvent.Type != "" || currentEvent.Data != "" {
				select {
				case eventCh <- currentEvent:
				case <-ctx.Done():
					return
				}
			}
			currentEvent = StreamSSEEvent{}
			continue
		}

		colonIdx := strings.Index(line, ":")
		if colonIdx == -1 {
			continue
		}

		field := line[:colonIdx]
		value := line[colonIdx+1:]
		if strings.HasPrefix(value, " ") {
			value = value[1:]
		}

		switch field {
		case "event":
			currentEvent.Type = value
		case "data":
			if currentEvent.Data != "" {
				currentEvent.Data += "\n"
			}
			currentEvent.Data += value
		case "id":
			currentEvent.ID = value
		}
	}
}

// SendNotification sends a JSON-RPC notification (no response expected).
func (c *StreamableClient) SendNotification(ctx context.Context, method string, params map[string]any) error {
	body, err := json.Marshal(&JSONRPCReq{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	})
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/mcp", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if c.sessionID != "" {
		httpReq.Header.Set("Mcp-Session-Id", c.sessionID)
	}

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	return nil
}

// TestStreamableHTTP_BasicHandshake tests basic Streamable HTTP handshake.
func TestStreamableHTTP_BasicHandshake(t *testing.T) {
	client := redis.NewInMemoryClient()

	store := redis.NewMetadataStore(&redis.MetadataStoreConfig{
		Client:     client,
		SessionTTL: 1 * time.Hour,
	})
	bus := redis.NewEventBus(&redis.EventBusConfig{
		Client: client,
	})

	manager, err := sseha.NewSessionManager(&sseha.SessionManagerConfig{
		NodeID:        "streamable_server_1",
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

	// Create Streamable HTTP middleware
	streamMW := sseha.NewStreamableMiddleware(manager)

	// Handler receives JSON-RPC requests
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := sseha.GetRequestBody(r.Context())

		var req JSONRPCReq
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}

		haWriter, ok := sseha.GetHAWriter(r.Context())
		if !ok {
			http.Error(w, "HA writer not found", http.StatusInternalServerError)
			return
		}

		var result map[string]any
		switch req.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion": "2025-03-26",
				"capabilities": map[string]any{
					"tools": map[string]any{},
				},
				"serverInfo": map[string]any{
					"name":    "test-streamable-server",
					"version": "1.0.0",
				},
			}
		default:
			result = map[string]any{"status": "ok"}
		}

		resp := JSONRPCResp{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  result,
		}
		data, _ := json.Marshal(resp)
		_ = haWriter.SendEvent(r.Context(), "message", data)
	})

	server := httptest.NewServer(streamMW.Handler(handler))
	defer server.Close()

	// Client: Initialize
	streamClient := NewStreamableClient(server.URL)

	initReq := &JSONRPCReq{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "initialize",
		Params: map[string]any{
			"protocolVersion": "2025-03-26",
			"capabilities":    map[string]any{},
			"clientInfo": map[string]any{
				"name":    "test-client",
				"version": "1.0.0",
			},
		},
	}

	resp, err := streamClient.SendRequest(ctx, initReq)
	if err != nil {
		t.Fatalf("initialize request: %v", err)
	}

	t.Logf("Response: %+v", resp)
	t.Logf("Session ID: %s", streamClient.sessionID)

	if resp.ID != 1 {
		t.Errorf("expected response ID 1, got %d", resp.ID)
	}

	if streamClient.sessionID == "" {
		t.Error("expected non-empty session ID")
	}

	if resp.Result["protocolVersion"] != "2025-03-26" {
		t.Errorf("unexpected protocol version: %v", resp.Result["protocolVersion"])
	}
}

// TestStreamableHTTP_SessionContinuation tests session continuation with Mcp-Session-Id header.
func TestStreamableHTTP_SessionContinuation(t *testing.T) {
	client := redis.NewInMemoryClient()

	store := redis.NewMetadataStore(&redis.MetadataStoreConfig{
		Client:     client,
		SessionTTL: 1 * time.Hour,
	})
	bus := redis.NewEventBus(&redis.EventBusConfig{
		Client: client,
	})

	manager, _ := sseha.NewSessionManager(&sseha.SessionManagerConfig{
		NodeID:        "streamable_server_1",
		NodeAddress:   "localhost:8080",
		MetadataStore: store,
		EventBus:      bus,
	})

	ctx := context.Background()
	_ = manager.Start(ctx)
	defer func() { _ = manager.Close(ctx) }()

	streamMW := sseha.NewStreamableMiddleware(manager)

	var requestCount int
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++

		body := sseha.GetRequestBody(r.Context())
		var req JSONRPCReq
		json.Unmarshal(body, &req)

		haWriter, _ := sseha.GetHAWriter(r.Context())

		resp := JSONRPCResp{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"requestNumber": requestCount,
				"method":        req.Method,
			},
		}
		data, _ := json.Marshal(resp)
		_ = haWriter.SendEvent(r.Context(), "message", data)
	})

	server := httptest.NewServer(streamMW.Handler(handler))
	defer server.Close()

	streamClient := NewStreamableClient(server.URL)

	// First request - creates session
	_, err := streamClient.SendRequest(ctx, &JSONRPCReq{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "test1",
	})
	if err != nil {
		t.Fatalf("first request: %v", err)
	}

	sessionID := streamClient.sessionID
	t.Logf("Session ID after first request: %s", sessionID)

	if sessionID == "" {
		t.Fatal("expected session ID to be set")
	}

	// Second request - should use same session
	resp, err := streamClient.SendRequest(ctx, &JSONRPCReq{
		JSONRPC: "2.0",
		ID:      2,
		Method:  "test2",
	})
	if err != nil {
		t.Fatalf("second request: %v", err)
	}

	// Session ID should remain the same
	if streamClient.sessionID != sessionID {
		t.Errorf("session ID changed: %s -> %s", sessionID, streamClient.sessionID)
	}

	t.Logf("Second response: %+v", resp)
}

// TestStreamableHTTP_StreamingResponse tests SSE streaming response.
func TestStreamableHTTP_StreamingResponse(t *testing.T) {
	client := redis.NewInMemoryClient()

	store := redis.NewMetadataStore(&redis.MetadataStoreConfig{
		Client:     client,
		SessionTTL: 1 * time.Hour,
	})
	bus := redis.NewEventBus(&redis.EventBusConfig{
		Client: client,
	})

	manager, _ := sseha.NewSessionManager(&sseha.SessionManagerConfig{
		NodeID:        "streamable_server_1",
		NodeAddress:   "localhost:8080",
		MetadataStore: store,
		EventBus:      bus,
	})

	ctx := context.Background()
	_ = manager.Start(ctx)
	defer func() { _ = manager.Close(ctx) }()

	streamMW := sseha.NewStreamableMiddleware(manager)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		haWriter, _ := sseha.GetHAWriter(r.Context())

		// Stream multiple events
		for i := 0; i < 3; i++ {
			data := fmt.Sprintf(`{"chunk": %d, "text": "streaming data %d"}`, i, i)
			_ = haWriter.SendEvent(r.Context(), "message", []byte(data))
			time.Sleep(50 * time.Millisecond)
		}
	})

	server := httptest.NewServer(streamMW.Handler(handler))
	defer server.Close()

	streamClient := NewStreamableClient(server.URL)

	// Request with SSE streaming
	eventCh, err := streamClient.SendStreamingRequest(ctx, &JSONRPCReq{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "stream",
	})
	if err != nil {
		t.Fatalf("streaming request: %v", err)
	}

	var events []StreamSSEEvent
	timeout := time.After(5 * time.Second)
	for {
		select {
		case event, ok := <-eventCh:
			if !ok {
				goto done
			}
			events = append(events, event)
			if len(events) >= 3 {
				goto done
			}
		case <-timeout:
			t.Fatalf("timeout waiting for events, got %d", len(events))
		}
	}
done:

	if len(events) != 3 {
		t.Errorf("expected 3 events, got %d", len(events))
	}

	for i, event := range events {
		t.Logf("Event %d: type=%s, data=%s", i, event.Type, event.Data)
	}
}

// TestStreamableHTTP_Notification tests notification handling (no response).
func TestStreamableHTTP_Notification(t *testing.T) {
	client := redis.NewInMemoryClient()

	store := redis.NewMetadataStore(&redis.MetadataStoreConfig{
		Client:     client,
		SessionTTL: 1 * time.Hour,
	})
	bus := redis.NewEventBus(&redis.EventBusConfig{
		Client: client,
	})

	manager, _ := sseha.NewSessionManager(&sseha.SessionManagerConfig{
		NodeID:        "streamable_server_1",
		NodeAddress:   "localhost:8080",
		MetadataStore: store,
		EventBus:      bus,
	})

	ctx := context.Background()
	_ = manager.Start(ctx)
	defer func() { _ = manager.Close(ctx) }()

	streamMW := sseha.NewStreamableMiddleware(manager)

	var notificationReceived bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := sseha.GetRequestBody(r.Context())
		var req JSONRPCReq
		json.Unmarshal(body, &req)

		if req.Method == "notifications/progress" {
			notificationReceived = true
		}
	})

	server := httptest.NewServer(streamMW.Handler(handler))
	defer server.Close()

	streamClient := NewStreamableClient(server.URL)

	// First, initialize to get a session
	_, _ = streamClient.SendRequest(ctx, &JSONRPCReq{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "initialize",
	})

	// Send a notification (no id field)
	err := streamClient.SendNotification(ctx, "notifications/progress", map[string]any{
		"progress": 50,
		"message":  "halfway",
	})
	if err != nil {
		t.Fatalf("send notification: %v", err)
	}

	if !notificationReceived {
		t.Error("notification was not received by handler")
	}
}

// TestStreamableHTTP_StatelessMode tests stateless mode (no session management).
func TestStreamableHTTP_StatelessMode(t *testing.T) {
	client := redis.NewInMemoryClient()

	store := redis.NewMetadataStore(&redis.MetadataStoreConfig{
		Client:     client,
		SessionTTL: 1 * time.Hour,
	})
	bus := redis.NewEventBus(&redis.EventBusConfig{
		Client: client,
	})

	manager, _ := sseha.NewSessionManager(&sseha.SessionManagerConfig{
		NodeID:        "streamable_server_1",
		NodeAddress:   "localhost:8080",
		MetadataStore: store,
		EventBus:      bus,
	})

	ctx := context.Background()
	_ = manager.Start(ctx)
	defer func() { _ = manager.Close(ctx) }()

	streamMW := sseha.NewStreamableMiddleware(manager)
	streamMW.StatelessMode = true // Enable stateless mode

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		haWriter, _ := sseha.GetHAWriter(r.Context())

		resp := JSONRPCResp{
			JSONRPC: "2.0",
			ID:      1,
			Result:  map[string]any{"stateless": true},
		}
		data, _ := json.Marshal(resp)
		_ = haWriter.SendEvent(r.Context(), "message", data)
	})

	server := httptest.NewServer(streamMW.Handler(handler))
	defer server.Close()

	streamClient := NewStreamableClient(server.URL)

	resp, err := streamClient.SendRequest(ctx, &JSONRPCReq{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "test",
	})
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	// In stateless mode, no session ID should be returned
	if streamClient.sessionID != "" {
		t.Errorf("expected no session ID in stateless mode, got: %s", streamClient.sessionID)
	}

	t.Logf("Stateless response: %+v", resp)
}

// TestStreamableHTTP_SessionNotFound tests error handling for invalid session.
func TestStreamableHTTP_SessionNotFound(t *testing.T) {
	client := redis.NewInMemoryClient()

	store := redis.NewMetadataStore(&redis.MetadataStoreConfig{
		Client:     client,
		SessionTTL: 1 * time.Hour,
	})
	bus := redis.NewEventBus(&redis.EventBusConfig{
		Client: client,
	})

	manager, _ := sseha.NewSessionManager(&sseha.SessionManagerConfig{
		NodeID:        "streamable_server_1",
		NodeAddress:   "localhost:8080",
		MetadataStore: store,
		EventBus:      bus,
	})

	ctx := context.Background()
	_ = manager.Start(ctx)
	defer func() { _ = manager.Close(ctx) }()

	streamMW := sseha.NewStreamableMiddleware(manager)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Should not reach here
		t.Error("handler should not be called for invalid session")
	})

	server := httptest.NewServer(streamMW.Handler(handler))
	defer server.Close()

	// Create client with fake session ID
	streamClient := NewStreamableClient(server.URL)
	streamClient.sessionID = "nonexistent_session_id"

	_, err := streamClient.SendRequest(ctx, &JSONRPCReq{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "test",
	})

	if err == nil {
		t.Error("expected error for invalid session")
	}

	t.Logf("Expected error: %v", err)
}
