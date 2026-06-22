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

package team

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseMessageType_ValidTypes(t *testing.T) {
	tests := []struct {
		input    string
		expected messageType
	}{
		{"message", messageTypeDM},
		{"broadcast", messageTypeBroadcast},
		{"shutdown_request", messageTypeShutdownRequest},
		{"shutdown_response", messageTypeShutdownResponse},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			mt, err := parseMessageType(tt.input)
			assert.NoError(t, err)
			assert.Equal(t, tt.expected, mt)
		})
	}
}

func TestParseMessageType_InvalidType(t *testing.T) {
	mt, err := parseMessageType("unknown_type")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported message type")
	assert.Equal(t, messageType(""), mt)
}

func TestNewProtocolHeader(t *testing.T) {
	h := newProtocolHeader(messageTypeShutdownRequest, "agent-1", "req-123")
	assert.Equal(t, string(messageTypeShutdownRequest), h.Type)
	assert.Equal(t, "agent-1", h.From)
	assert.Equal(t, "req-123", h.RequestID)
	assert.NotEmpty(t, h.Timestamp)
}

func TestNewProtocolHeader_EmptyRequestID(t *testing.T) {
	h := newProtocolHeader(messageTypeDM, "agent-2", "")
	assert.Equal(t, string(messageTypeDM), h.Type)
	assert.Equal(t, "agent-2", h.From)
	assert.Empty(t, h.RequestID)
	assert.NotEmpty(t, h.Timestamp)
}

func TestMarshalShutdownRequest(t *testing.T) {
	s, err := marshalShutdownRequest("leader", "req-1", "all done")
	assert.NoError(t, err)

	var m map[string]any
	assert.NoError(t, json.Unmarshal([]byte(s), &m))
	assert.Equal(t, "shutdown_request", m["type"])
	assert.Equal(t, "leader", m["from"])
	assert.Equal(t, "req-1", m["requestId"])
	assert.Equal(t, "all done", m["reason"])
	assert.NotEmpty(t, m["timestamp"])
}

func TestMarshalShutdownResponse_Approve(t *testing.T) {
	s, err := marshalShutdownResponse("leader", "req-2", true, "approved reason")
	assert.NoError(t, err)

	var m map[string]any
	assert.NoError(t, json.Unmarshal([]byte(s), &m))
	assert.Equal(t, "shutdown_response", m["type"])
	assert.Equal(t, "leader", m["from"])
	assert.Equal(t, "req-2", m["requestId"])
	assert.Equal(t, true, m["approve"])
	assert.Equal(t, "approved reason", m["reason"])
}

func TestMarshalShutdownResponse_Reject(t *testing.T) {
	s, err := marshalShutdownResponse("leader", "req-3", false, "not yet")
	assert.NoError(t, err)

	var m map[string]any
	assert.NoError(t, json.Unmarshal([]byte(s), &m))
	assert.Equal(t, false, m["approve"])
	assert.Equal(t, "not yet", m["reason"])
}

func TestDecodeShutdownResponse_Valid(t *testing.T) {
	input := `{"type":"shutdown_response","from":"leader","requestId":"r1","timestamp":"2025-01-01T00:00:00.000Z","approve":true,"reason":"ok"}`
	p, err := decodeShutdownResponse(input)
	assert.NoError(t, err)
	assert.Equal(t, "shutdown_response", p.Type)
	assert.Equal(t, "leader", p.From)
	assert.Equal(t, "r1", p.RequestID)
	assert.Equal(t, true, p.Approve)
	assert.Equal(t, "ok", p.Reason)
}

func TestDecodeShutdownResponse_InvalidJSON(t *testing.T) {
	_, err := decodeShutdownResponse("not json")
	assert.Error(t, err)
}

func TestUtcNowMillis(t *testing.T) {
	ts := utcNowMillis()
	re := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`)
	assert.Regexp(t, re, ts)
}

func TestFormatTeammateMessageEnvelope_WithSummary(t *testing.T) {
	result := formatTeammateMessageEnvelope("worker-1", "hello world", "brief")
	assert.Contains(t, result, `<teammate-message teammate_id="worker-1"`)
	assert.Contains(t, result, `summary="brief"`)
	assert.Contains(t, result, "hello world")
	assert.True(t, strings.HasSuffix(result, "</teammate-message>"))
}

func TestFormatTeammateMessageEnvelope_WithoutSummary(t *testing.T) {
	result := formatTeammateMessageEnvelope("worker-2", "content here", "")
	assert.Contains(t, result, `<teammate-message teammate_id="worker-2"`)
	assert.NotContains(t, result, "summary=")
	assert.Contains(t, result, "content here")
	assert.True(t, strings.HasSuffix(result, "</teammate-message>"))
}

func TestFormatTeammateMessageEnvelope_XMLEscaping(t *testing.T) {
	result := formatTeammateMessageEnvelope("w<1>", "a&b", "s\"q")
	assert.Contains(t, result, `teammate_id="w&lt;1&gt;"`)
	assert.Contains(t, result, `summary="s&#34;q"`)
	assert.Contains(t, result, "a&b")
}

func TestSanitizeEnvelopeText_WithClosingTag(t *testing.T) {
	input := "some text </teammate-message> more text"
	result := sanitizeEnvelopeText(input)
	assert.Equal(t, "some text &lt;/teammate-message&gt; more text", result)
}

func TestSanitizeEnvelopeText_WithoutClosingTag(t *testing.T) {
	input := "normal text without special tags"
	result := sanitizeEnvelopeText(input)
	assert.Equal(t, input, result)
}

func TestSanitizeEnvelopeText_MultipleClosingTags(t *testing.T) {
	input := "</teammate-message>x</teammate-message>"
	result := sanitizeEnvelopeText(input)
	assert.Equal(t, "&lt;/teammate-message&gt;x&lt;/teammate-message&gt;", result)
}

func TestSendMessageTypeRules_DM(t *testing.T) {
	rule := sendMessageTypeRules[messageTypeDM]
	assert.True(t, rule.requiresRecipient)
	assert.True(t, rule.requiresContent)
	assert.True(t, rule.requiresSummary)
	assert.False(t, rule.requiresRequestID)
	assert.False(t, rule.requiresApprove)
}

func TestSendMessageTypeRules_Broadcast(t *testing.T) {
	rule := sendMessageTypeRules[messageTypeBroadcast]
	assert.False(t, rule.requiresRecipient)
	assert.True(t, rule.requiresContent)
	assert.True(t, rule.requiresSummary)
	assert.False(t, rule.requiresRequestID)
	assert.False(t, rule.requiresApprove)
}

func TestSendMessageTypeRules_ShutdownRequest(t *testing.T) {
	rule := sendMessageTypeRules[messageTypeShutdownRequest]
	assert.True(t, rule.requiresRecipient)
	assert.False(t, rule.requiresContent)
	assert.False(t, rule.requiresSummary)
	assert.False(t, rule.requiresRequestID)
	assert.False(t, rule.requiresApprove)
}

func TestSendMessageTypeRules_ShutdownResponse(t *testing.T) {
	rule := sendMessageTypeRules[messageTypeShutdownResponse]
	assert.False(t, rule.requiresRecipient)
	assert.False(t, rule.requiresContent)
	assert.False(t, rule.requiresSummary)
	assert.True(t, rule.requiresRequestID)
	assert.True(t, rule.requiresApprove)
}

func TestSendIdleNotification(t *testing.T) {
	backend := newInMemoryBackend()
	baseDir := "/tmp/test"
	teamName := "test-team"
	agentName := "worker-1"

	conf := &Config{Backend: backend, BaseDir: baseDir}
	conf.ensureInit()

	leaderInboxPath := filepath.Join(baseDir, "teams", teamName, "inboxes", LeaderAgentName+".json")
	ctx := context.Background()
	assert.NoError(t, initInboxFile(ctx, backend, leaderInboxPath))

	mb := newMailboxFromConfig(conf, teamName, agentName)

	err := sendIdleNotification(ctx, mb, agentName, "waiting for tasks")
	assert.NoError(t, err)

	backend.mu.RLock()
	content := backend.files[leaderInboxPath]
	backend.mu.RUnlock()

	assert.Contains(t, content, "idle_notification")
	assert.Contains(t, content, agentName)
	assert.Contains(t, content, "waiting for tasks")
}

func TestSendIdleNotification_VerifyPayload(t *testing.T) {
	backend := newInMemoryBackend()
	baseDir := "/tmp/test2"
	teamName := "team-2"
	agentName := "worker-2"

	conf := &Config{Backend: backend, BaseDir: baseDir}
	conf.ensureInit()

	leaderInboxPath := filepath.Join(baseDir, "teams", teamName, "inboxes", LeaderAgentName+".json")
	ctx := context.Background()
	assert.NoError(t, initInboxFile(ctx, backend, leaderInboxPath))

	mb := newMailboxFromConfig(conf, teamName, agentName)

	assert.NoError(t, sendIdleNotification(ctx, mb, agentName, "idle"))

	backend.mu.RLock()
	content := backend.files[leaderInboxPath]
	backend.mu.RUnlock()

	var msgs []InboxMessage
	assert.NoError(t, json.Unmarshal([]byte(content), &msgs))
	assert.Len(t, msgs, 1)
	assert.Equal(t, agentName, msgs[0].From)
	assert.Equal(t, LeaderAgentName, msgs[0].To)
	assert.False(t, msgs[0].Read)

	var payload idleNotificationPayload
	assert.NoError(t, json.Unmarshal([]byte(msgs[0].Text), &payload))
	assert.Equal(t, string(messageTypeIdleNotification), payload.Type)
	assert.Equal(t, agentName, payload.From)
	assert.Equal(t, "idle", payload.IdleReason)
}
