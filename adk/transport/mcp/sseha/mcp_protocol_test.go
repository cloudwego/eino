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

// MCPClient implements a real MCP protocol client over SSE transport.
// MCP uses dual-channel communication:
//   - GET /sse: SSE long-polling for server-to-client messages
//   - POST /messages?session_id=xxx: client-to-server JSON-RPC requests
type MCPClient struct {
	client      *http.Client
	baseURL     string
	sessionID   string
	lastEventID string
	sseConn     *http.Response
}

// NewMCPClient creates a new MCP client.
func NewMCPClient(baseURL string) *MCPClient {
	return &MCPClient{
		client:  &http.Client{Timeout: 30 * time.Second},
		baseURL: baseURL,
	}
}

// JSONRPCRequest represents a JSON-RPC 2.0 request.
type JSONRPCRequest struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      int            `json:"id,omitempty"`
	Method  string         `json:"method"`
	Params  map[string]any `json:"params,omitempty"`
}

// JSONRPCResponse represents a JSON-RPC 2.0 response.
type JSONRPCResponse struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      int            `json:"id,omitempty"`
	Result  map[string]any `json:"result,omitempty"`
	Error   *JSONRPCError  `json:"error,omitempty"`
}

// JSONRPCError represents a JSON-RPC 2.0 error.
type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// SSEEvent represents a parsed SSE event.
type SSEEvent struct {
	Type string
	Data string
	ID   string
}

// Connect establishes SSE connection following MCP protocol.
// 1. GET /sse with Accept: text/event-stream
// 2. Receive event: endpoint with session_id
func (c *MCPClient) Connect(ctx context.Context) error {
	url := c.baseURL + "/sse"

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-store")
	req.Header.Set("Connection", "keep-alive")

	if c.lastEventID != "" {
		req.Header.Set("Last-Event-ID", c.lastEventID)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	contentType := resp.Header.Get("Content-Type")
	if !strings.Contains(contentType, "text/event-stream") {
		resp.Body.Close()
		return fmt.Errorf("expected Content-Type text/event-stream, got %s", contentType)
	}

	c.sseConn = resp

	// Read the first event - should be 'endpoint' with session_id
	event, err := c.readEvent()
	if err != nil {
		return fmt.Errorf("read endpoint event: %w", err)
	}

	if event.Type != "endpoint" {
		return fmt.Errorf("expected endpoint event, got %s", event.Type)
	}

	// Parse session_id from endpoint URL
	// Format: /messages/?session_id=xxx
	if strings.Contains(event.Data, "session_id=") {
		parts := strings.Split(event.Data, "session_id=")
		if len(parts) > 1 {
			c.sessionID = strings.Split(parts[1], "&")[0]
		}
	}

	return nil
}

// readEvent reads a single SSE event from the stream.
func (c *MCPClient) readEvent() (*SSEEvent, error) {
	if c.sseConn == nil {
		return nil, fmt.Errorf("no SSE connection")
	}

	scanner := bufio.NewScanner(c.sseConn.Body)
	var event SSEEvent

	for scanner.Scan() {
		line := scanner.Text()

		// Empty line signals end of event
		if line == "" {
			if event.Type != "" || event.Data != "" {
				return &event, nil
			}
			continue
		}

		// Skip comments (like ping)
		if strings.HasPrefix(line, ":") {
			continue
		}

		// Parse field: value
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
			event.Type = value
		case "data":
			if event.Data != "" {
				event.Data += "\n"
			}
			event.Data += value
		case "id":
			event.ID = value
			c.lastEventID = value
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return nil, io.EOF
}

// SendRequest sends a JSON-RPC request via POST /messages endpoint.
// Server should return 202 Accepted and send response via SSE.
func (c *MCPClient) SendRequest(ctx context.Context, req *JSONRPCRequest) error {
	if c.sessionID == "" {
		return fmt.Errorf("no session - call Connect first")
	}

	// Ensure jsonrpc version is set
	if req.JSONRPC == "" {
		req.JSONRPC = "2.0"
	}

	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	url := fmt.Sprintf("%s/messages/?session_id=%s", c.baseURL, c.sessionID)

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	// MCP spec: Server should return 202 Accepted
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	return nil
}

// ReceiveResponse waits for a JSON-RPC response via SSE.
func (c *MCPClient) ReceiveResponse(ctx context.Context) (*JSONRPCResponse, error) {
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
			event, err := c.readEvent()
			if err != nil {
				return nil, err
			}

			if event.Type == "message" {
				var resp JSONRPCResponse
				if err := json.Unmarshal([]byte(event.Data), &resp); err != nil {
					return nil, fmt.Errorf("unmarshal response: %w", err)
				}
				return &resp, nil
			}
		}
	}
}

// Close closes the SSE connection.
func (c *MCPClient) Close() error {
	if c.sseConn != nil {
		return c.sseConn.Body.Close()
	}
	return nil
}

// TestMCPProtocol_BasicHandshake tests the basic MCP protocol handshake:
// 1. GET /sse -> receive endpoint event with session_id
// 2. POST /messages initialize request
// 3. Receive initialize response via SSE
func TestMCPProtocol_BasicHandshake(t *testing.T) {
	// Setup HA infrastructure
	client := redis.NewInMemoryClient()

	store := redis.NewMetadataStore(&redis.MetadataStoreConfig{
		Client:     client,
		SessionTTL: 1 * time.Hour,
	})
	bus := redis.NewEventBus(&redis.EventBusConfig{
		Client: client,
	})

	manager, err := sseha.NewSessionManager(&sseha.SessionManagerConfig{
		NodeID:        "mcp_server_1",
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

	// Create MCP handler that follows MCP protocol
	mw := sseha.NewMCPMiddleware(manager)

	// Handler receives JSON-RPC requests via POST /messages
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Get request body from context
		body := sseha.GetRequestBody(r.Context())

		var req JSONRPCRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}

		// Get HA writer from context
		haWriter, ok := sseha.GetHAWriter(r.Context())
		if !ok {
			http.Error(w, "HA writer not found", http.StatusInternalServerError)
			return
		}

		// Handle MCP methods
		var result map[string]any
		switch req.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities": map[string]any{
					"tools": map[string]any{},
				},
				"serverInfo": map[string]any{
					"name":    "test-mcp-server",
					"version": "1.0.0",
				},
			}
		case "tools/list":
			result = map[string]any{
				"tools": []map[string]any{
					{
						"name":        "test_tool",
						"description": "A test tool",
						"inputSchema": map[string]any{
							"type": "object",
						},
					},
				},
			}
		case "tools/call":
			result = map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": "tool executed successfully"},
				},
				"isError": false,
			}
		default:
			// Send error response via SSE
			errResp := JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error: &JSONRPCError{
					Code:    -32601,
					Message: "Method not found",
				},
			}
			data, _ := json.Marshal(errResp)
			_ = haWriter.SendEvent(r.Context(), "message", data)
			return
		}

		// Send success response via SSE
		resp := JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  result,
		}
		data, _ := json.Marshal(resp)
		_ = haWriter.SendEvent(r.Context(), "message", data)
	})

	// Create MCP server
	server := httptest.NewServer(mw.Handler(handler))
	defer server.Close()

	// MCP Client: Establish connection
	mcpClient := NewMCPClient(server.URL)

	err = mcpClient.Connect(ctx)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer mcpClient.Close()

	t.Logf("Connected to MCP server, session_id=%s", mcpClient.sessionID)

	if mcpClient.sessionID == "" {
		t.Error("expected non-empty session_id")
	}

	// MCP Client: Send initialize request
	initReq := &JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "initialize",
		Params: map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo": map[string]any{
				"name":    "test-client",
				"version": "1.0.0",
			},
		},
	}

	if err := mcpClient.SendRequest(ctx, initReq); err != nil {
		t.Fatalf("send initialize: %v", err)
	}

	t.Log("Sent initialize request")

	// MCP Client: Receive initialize response
	respCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	resp, err := mcpClient.ReceiveResponse(respCtx)
	if err != nil {
		t.Fatalf("receive response: %v", err)
	}

	if resp.ID != 1 {
		t.Errorf("expected response ID 1, got %d", resp.ID)
	}

	if resp.Error != nil {
		t.Errorf("unexpected error: %v", resp.Error)
	}

	if resp.Result["protocolVersion"] != "2024-11-05" {
		t.Errorf("unexpected protocol version: %v", resp.Result["protocolVersion"])
	}

	t.Logf("Received initialize response: %+v", resp)
}

// TestMCPProtocol_ToolInvocation tests MCP tool invocation flow.
func TestMCPProtocol_ToolInvocation(t *testing.T) {
	client := redis.NewInMemoryClient()

	store := redis.NewMetadataStore(&redis.MetadataStoreConfig{
		Client:     client,
		SessionTTL: 1 * time.Hour,
	})
	bus := redis.NewEventBus(&redis.EventBusConfig{
		Client: client,
	})

	manager, _ := sseha.NewSessionManager(&sseha.SessionManagerConfig{
		NodeID:        "mcp_server_1",
		NodeAddress:   "localhost:8080",
		MetadataStore: store,
		EventBus:      bus,
	})

	ctx := context.Background()
	_ = manager.Start(ctx)
	defer func() { _ = manager.Close(ctx) }()

	mw := sseha.NewMCPMiddleware(manager)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := sseha.GetRequestBody(r.Context())

		var req JSONRPCRequest
		json.Unmarshal(body, &req)

		haWriter, _ := sseha.GetHAWriter(r.Context())

		var result map[string]any
		switch req.Method {
		case "tools/list":
			result = map[string]any{
				"tools": []map[string]any{
					{
						"name":        "get_weather",
						"description": "Get weather for a city",
						"inputSchema": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"city": map[string]any{"type": "string"},
							},
						},
					},
				},
			}
		case "tools/call":
			result = map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": "Weather in Beijing: Sunny, 25°C"},
				},
				"isError": false,
			}
		}

		resp := JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  result,
		}
		data, _ := json.Marshal(resp)
		_ = haWriter.SendEvent(r.Context(), "message", data)
	})

	server := httptest.NewServer(mw.Handler(handler))
	defer server.Close()

	// Connect
	mcpClient := NewMCPClient(server.URL)
	if err := mcpClient.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer mcpClient.Close()

	// List tools
	listReq := &JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "tools/list",
	}
	if err := mcpClient.SendRequest(ctx, listReq); err != nil {
		t.Fatalf("send tools/list: %v", err)
	}

	respCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	resp, err := mcpClient.ReceiveResponse(respCtx)
	cancel()
	if err != nil {
		t.Fatalf("receive tools/list response: %v", err)
	}

	t.Logf("Tools: %v", resp.Result["tools"])

	// Call tool
	callReq := &JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      2,
		Method:  "tools/call",
		Params: map[string]any{
			"name": "get_weather",
			"arguments": map[string]any{
				"city": "Beijing",
			},
		},
	}
	if err := mcpClient.SendRequest(ctx, callReq); err != nil {
		t.Fatalf("send tools/call: %v", err)
	}

	respCtx, cancel = context.WithTimeout(ctx, 3*time.Second)
	resp, err = mcpClient.ReceiveResponse(respCtx)
	cancel()
	if err != nil {
		t.Fatalf("receive tools/call response: %v", err)
	}

	t.Logf("Tool result: %v", resp.Result)
}

// TestMCPProtocol_ReconnectWithLastEventID tests MCP reconnection with event replay.
func TestMCPProtocol_ReconnectWithLastEventID(t *testing.T) {
	client := redis.NewInMemoryClient()

	store := redis.NewMetadataStore(&redis.MetadataStoreConfig{
		Client:     client,
		SessionTTL: 1 * time.Hour,
	})
	bus := redis.NewEventBus(&redis.EventBusConfig{
		Client: client,
	})

	manager, _ := sseha.NewSessionManager(&sseha.SessionManagerConfig{
		NodeID:        "mcp_server_1",
		NodeAddress:   "localhost:8080",
		MetadataStore: store,
		EventBus:      bus,
	})

	ctx := context.Background()
	_ = manager.Start(ctx)
	defer func() { _ = manager.Close(ctx) }()

	mw := sseha.NewMCPMiddleware(manager)

	var requestCount int
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		haWriter, _ := sseha.GetHAWriter(r.Context())

		requestCount++

		body := sseha.GetRequestBody(r.Context())
		var req JSONRPCRequest
		json.Unmarshal(body, &req)

		resp := JSONRPCResponse{
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

	server := httptest.NewServer(mw.Handler(handler))
	defer server.Close()

	// First connection
	mcpClient := NewMCPClient(server.URL)
	if err := mcpClient.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}

	sessionID := mcpClient.sessionID
	t.Logf("First connection: session_id=%s", sessionID)

	// Send a request
	_ = mcpClient.SendRequest(ctx, &JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "test1",
	})

	respCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	resp, _ := mcpClient.ReceiveResponse(respCtx)
	cancel()
	t.Logf("Response 1: %+v, lastEventID=%s", resp, mcpClient.lastEventID)

	// Close connection
	mcpClient.Close()

	// Reconnect with same session
	mcpClient2 := NewMCPClient(server.URL)
	mcpClient2.sessionID = sessionID
	mcpClient2.lastEventID = mcpClient.lastEventID

	// Reconnect should use Last-Event-ID header
	if err := mcpClient2.Connect(ctx); err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	defer mcpClient2.Close()

	t.Logf("Reconnected: session_id=%s", mcpClient2.sessionID)

	// Send another request
	_ = mcpClient2.SendRequest(ctx, &JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      2,
		Method:  "test2",
	})

	respCtx, cancel = context.WithTimeout(ctx, 2*time.Second)
	resp, _ = mcpClient2.ReceiveResponse(respCtx)
	cancel()
	t.Logf("Response 2: %+v", resp)
}
