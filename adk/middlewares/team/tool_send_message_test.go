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
	"path/filepath"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/assert"
)

func TestNewSendMessageTool_EmptySender(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	tool, err := newSendMessageTool(mw, "")
	assert.Error(t, err)
	assert.Nil(t, tool)
	assert.Contains(t, err.Error(), "senderName is required")
}

func TestNewSendMessageTool_ValidSender(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	tool, err := newSendMessageTool(mw, "agent-1")
	assert.NoError(t, err)
	assert.NotNil(t, tool)
	assert.Equal(t, "agent-1", tool.senderName)
}

func TestSendMessageTool_Info(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	tool, err := newSendMessageTool(mw, LeaderAgentName)
	assert.NoError(t, err)

	info, err := tool.Info(context.Background())
	assert.NoError(t, err)
	assert.Equal(t, sendMessageToolName, info.Name)
	s, err := info.ParamsOneOf.ToJSONSchema()
	assert.NoError(t, err)
	typeParam, ok := s.Properties.Get("type")
	assert.True(t, ok)
	assert.Equal(t, []any{"message", "broadcast", "shutdown_request", "shutdown_response"}, typeParam.Enum)
}

func TestSendMessageTool_InvokableRun_NoActiveTeam(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	tool, err := newSendMessageTool(mw, LeaderAgentName)
	assert.NoError(t, err)

	_, err = tool.InvokableRun(context.Background(), `{"type":"message","recipient":"worker","content":"hi","summary":"test"}`)
	assert.ErrorIs(t, err, errTeamNotFound)
}

func TestSendMessageTool_InvokableRun_InvalidJSON(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	ctx := context.Background()

	createTool := newTeamCreateTool(mw)
	_, err := createTool.InvokableRun(ctx, `{"team_name":"myteam"}`)
	assert.NoError(t, err)

	tool, err := newSendMessageTool(mw, LeaderAgentName)
	assert.NoError(t, err)

	_, err = tool.InvokableRun(ctx, `not json`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parse SendMessage args")
}

func TestSendMessageTool_InvokableRun_EmptyType(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	ctx := context.Background()

	createTool := newTeamCreateTool(mw)
	_, err := createTool.InvokableRun(ctx, `{"team_name":"myteam"}`)
	assert.NoError(t, err)

	tool, err := newSendMessageTool(mw, LeaderAgentName)
	assert.NoError(t, err)

	_, err = tool.InvokableRun(ctx, `{"type":""}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "'type' is required")
}

func TestSendMessageTool_InvokableRun_InvalidType(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	ctx := context.Background()

	createTool := newTeamCreateTool(mw)
	_, err := createTool.InvokableRun(ctx, `{"team_name":"myteam"}`)
	assert.NoError(t, err)

	tool, err := newSendMessageTool(mw, LeaderAgentName)
	assert.NoError(t, err)

	_, err = tool.InvokableRun(ctx, `{"type":"unknown"}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported message type")
}

func TestSendMessageTool_InvokableRun_DM_MissingRecipient(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	ctx := context.Background()

	createTool := newTeamCreateTool(mw)
	_, err := createTool.InvokableRun(ctx, `{"team_name":"myteam"}`)
	assert.NoError(t, err)

	tool, err := newSendMessageTool(mw, LeaderAgentName)
	assert.NoError(t, err)

	_, err = tool.InvokableRun(ctx, `{"type":"message","content":"hello","summary":"hi"}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "'recipient' is required")
}

func TestSendMessageTool_InvokableRun_DM_MissingContent(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	ctx := context.Background()

	createTool := newTeamCreateTool(mw)
	_, err := createTool.InvokableRun(ctx, `{"team_name":"myteam"}`)
	assert.NoError(t, err)

	tool, err := newSendMessageTool(mw, LeaderAgentName)
	assert.NoError(t, err)

	_, err = tool.InvokableRun(ctx, `{"type":"message","recipient":"worker","summary":"hi"}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "'content' is required")
}

func TestSendMessageTool_InvokableRun_DM_MissingSummary(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	ctx := context.Background()

	createTool := newTeamCreateTool(mw)
	_, err := createTool.InvokableRun(ctx, `{"team_name":"myteam"}`)
	assert.NoError(t, err)

	tool, err := newSendMessageTool(mw, LeaderAgentName)
	assert.NoError(t, err)

	_, err = tool.InvokableRun(ctx, `{"type":"message","recipient":"worker","content":"hello"}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "'summary' is required")
}

func TestSendMessageTool_InvokableRun_DM_Success(t *testing.T) {
	mw, conf := newTestTeamMiddleware()
	ctx := context.Background()

	createTool := newTeamCreateTool(mw)
	_, err := createTool.InvokableRun(ctx, `{"team_name":"myteam"}`)
	assert.NoError(t, err)

	teamName := mw.getTeamName()

	cm := conf
	err = cm.AddMember(ctx, teamName, teamMember{Name: "worker", JoinedAt: time.Now()})
	assert.NoError(t, err)

	inboxPath := inboxFilePath(conf.BaseDir, teamName, "worker")
	err = conf.Backend.Write(ctx, &WriteRequest{FilePath: inboxPath, Content: "[]"})
	assert.NoError(t, err)

	tool, err := newSendMessageTool(mw, LeaderAgentName)
	assert.NoError(t, err)

	result, err := tool.InvokableRun(ctx, `{"type":"message","recipient":"worker","content":"hello","summary":"greeting"}`)
	assert.NoError(t, err)
	assert.Contains(t, result, "success")
	assert.Contains(t, result, "Message sent to worker")

	backend := conf.Backend.(*inMemoryBackend)
	backend.mu.RLock()
	content := backend.files[inboxPath]
	backend.mu.RUnlock()

	var msgs []InboxMessage
	err = sonic.UnmarshalString(content, &msgs)
	assert.NoError(t, err)
	assert.Len(t, msgs, 1)
	assert.Equal(t, LeaderAgentName, msgs[0].From)
	assert.Equal(t, "worker", msgs[0].To)
	assert.Equal(t, "hello", msgs[0].Text)
	assert.Equal(t, "greeting", msgs[0].Summary)
}

func TestSendMessageTool_InvokableRun_Broadcast(t *testing.T) {
	mw, conf := newTestTeamMiddleware()
	ctx := context.Background()

	createTool := newTeamCreateTool(mw)
	_, err := createTool.InvokableRun(ctx, `{"team_name":"myteam"}`)
	assert.NoError(t, err)

	teamName := mw.getTeamName()

	cm := conf
	err = cm.AddMember(ctx, teamName, teamMember{Name: "worker1", JoinedAt: time.Now()})
	assert.NoError(t, err)
	err = cm.AddMember(ctx, teamName, teamMember{Name: "worker2", JoinedAt: time.Now()})
	assert.NoError(t, err)

	for _, name := range []string{"worker1", "worker2"} {
		inboxPath := inboxFilePath(conf.BaseDir, teamName, name)
		err = conf.Backend.Write(ctx, &WriteRequest{FilePath: inboxPath, Content: "[]"})
		assert.NoError(t, err)
	}

	tool, err := newSendMessageTool(mw, LeaderAgentName)
	assert.NoError(t, err)

	result, err := tool.InvokableRun(ctx, `{"type":"broadcast","content":"attention all","summary":"announcement"}`)
	assert.NoError(t, err)
	assert.Contains(t, result, "success")
	assert.Contains(t, result, "broadcast")

	backend := conf.Backend.(*inMemoryBackend)
	for _, name := range []string{"worker1", "worker2"} {
		inboxPath := inboxFilePath(conf.BaseDir, teamName, name)
		backend.mu.RLock()
		content := backend.files[inboxPath]
		backend.mu.RUnlock()

		var msgs []InboxMessage
		err = sonic.UnmarshalString(content, &msgs)
		assert.NoError(t, err)
		assert.Len(t, msgs, 1)
		assert.Equal(t, "attention all", msgs[0].Text)
		assert.Equal(t, LeaderAgentName, msgs[0].From)
	}

	leaderInboxPath := inboxFilePath(conf.BaseDir, teamName, LeaderAgentName)
	backend.mu.RLock()
	leaderContent := backend.files[leaderInboxPath]
	backend.mu.RUnlock()

	var leaderMsgs []InboxMessage
	err = sonic.UnmarshalString(leaderContent, &leaderMsgs)
	assert.NoError(t, err)
	assert.Len(t, leaderMsgs, 0)
}

func TestSendMessageTool_InvokableRun_ShutdownRequest_MissingRecipient(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	ctx := context.Background()

	createTool := newTeamCreateTool(mw)
	_, err := createTool.InvokableRun(ctx, `{"team_name":"myteam"}`)
	assert.NoError(t, err)

	tool, err := newSendMessageTool(mw, LeaderAgentName)
	assert.NoError(t, err)

	_, err = tool.InvokableRun(ctx, `{"type":"shutdown_request"}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "'recipient' is required")
}

func TestSendMessageTool_InvokableRun_ShutdownResponse_MissingFields(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	ctx := context.Background()

	createTool := newTeamCreateTool(mw)
	_, err := createTool.InvokableRun(ctx, `{"team_name":"myteam"}`)
	assert.NoError(t, err)

	tool, err := newSendMessageTool(mw, LeaderAgentName)
	assert.NoError(t, err)

	_, err = tool.InvokableRun(ctx, `{"type":"shutdown_response"}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "'request_id' is required")

	_, err = tool.InvokableRun(ctx, `{"type":"shutdown_response","request_id":"req-1"}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "'approve' is required")
}

func TestSendMessageTool_InvokableRun_ShutdownRequest_Success(t *testing.T) {
	mw, conf := newTestTeamMiddleware()
	ctx := context.Background()

	createTool := newTeamCreateTool(mw)
	_, err := createTool.InvokableRun(ctx, `{"team_name":"myteam"}`)
	assert.NoError(t, err)

	teamName := mw.getTeamName()

	cm := conf
	err = cm.AddMember(ctx, teamName, teamMember{Name: "worker", JoinedAt: time.Now()})
	assert.NoError(t, err)

	inboxPath := inboxFilePath(conf.BaseDir, teamName, "worker")
	err = conf.Backend.Write(ctx, &WriteRequest{FilePath: inboxPath, Content: "[]"})
	assert.NoError(t, err)

	tool, err := newSendMessageTool(mw, LeaderAgentName)
	assert.NoError(t, err)

	result, err := tool.InvokableRun(ctx, `{"type":"shutdown_request","recipient":"worker"}`)
	assert.NoError(t, err)
	assert.Contains(t, result, "success")
	assert.Contains(t, result, "request_id")
	assert.Contains(t, result, "shutdown")
}

func TestSendMessageTool_ValidateRecipient_NonMember(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	ctx := context.Background()

	createTool := newTeamCreateTool(mw)
	_, err := createTool.InvokableRun(ctx, `{"team_name":"myteam"}`)
	assert.NoError(t, err)

	tool, err := newSendMessageTool(mw, LeaderAgentName)
	assert.NoError(t, err)

	err = tool.validateRecipient(ctx, mw.getTeamName(), messageTypeDM, "nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not a member")
}

func TestSendMessageTool_ValidateRecipient_Broadcast(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	ctx := context.Background()

	createTool := newTeamCreateTool(mw)
	_, err := createTool.InvokableRun(ctx, `{"team_name":"myteam"}`)
	assert.NoError(t, err)

	tool, err := newSendMessageTool(mw, LeaderAgentName)
	assert.NoError(t, err)

	err = tool.validateRecipient(ctx, mw.getTeamName(), messageTypeBroadcast, "*")
	assert.NoError(t, err)
}

func TestSendMessageTool_ResolveRecipient_Broadcast(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	tool, err := newSendMessageTool(mw, LeaderAgentName)
	assert.NoError(t, err)

	to, err := tool.resolveRecipient(messageTypeBroadcast, &sendMessageArgs{Recipient: "someone"})
	assert.NoError(t, err)
	assert.Equal(t, "*", to)
}

func TestSendMessageTool_ResolveRecipient_DM(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	tool, err := newSendMessageTool(mw, LeaderAgentName)
	assert.NoError(t, err)

	to, err := tool.resolveRecipient(messageTypeDM, &sendMessageArgs{Recipient: "worker"})
	assert.NoError(t, err)
	assert.Equal(t, "worker", to)
}

func TestSendMessageTool_ResolveRecipient_ShutdownResponse_DefaultLeader(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	tool, err := newSendMessageTool(mw, "worker")
	assert.NoError(t, err)

	to, err := tool.resolveRecipient(messageTypeShutdownResponse, &sendMessageArgs{})
	assert.NoError(t, err)
	assert.Equal(t, LeaderAgentName, to)
}

func TestBuildApprovalResultMessage_ShutdownResponse(t *testing.T) {
	msg := buildApprovalResultMessage(messageTypeShutdownResponse, "worker", true)
	assert.Equal(t, "Shutdown approved", msg)
}

func TestBuildApprovalResultMessage_ShutdownRejected(t *testing.T) {
	msg := buildApprovalResultMessage(messageTypeShutdownResponse, "worker", false)
	assert.Equal(t, "Shutdown rejected", msg)
}

func TestBuildApprovalResultMessage_Default(t *testing.T) {
	msg := buildApprovalResultMessage(messageTypeDM, "worker", true)
	assert.Equal(t, "OK", msg)
}

func TestSendMessageTool_BuildRoutingResult_DM(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	tool, err := newSendMessageTool(mw, LeaderAgentName)
	assert.NoError(t, err)

	args := &sendMessageArgs{
		Content: "hello",
		Summary: "greeting",
	}
	result := tool.buildRoutingResult("worker", args)
	assert.Equal(t, LeaderAgentName, result["sender"])
	assert.Equal(t, "@worker", result["target"])
	assert.Equal(t, "greeting", result["summary"])
	assert.Equal(t, "hello", result["content"])
}

func TestSendMessageTool_BuildRoutingResult_Broadcast(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	tool, err := newSendMessageTool(mw, LeaderAgentName)
	assert.NoError(t, err)

	args := &sendMessageArgs{
		Content: "attention",
		Summary: "announcement",
	}
	result := tool.buildRoutingResult("*", args)
	assert.Equal(t, LeaderAgentName, result["sender"])
	assert.Equal(t, "*", result["target"])
	assert.Equal(t, "announcement", result["summary"])
	assert.Equal(t, "attention", result["content"])
}

func TestSendMessageTool_ShutdownRequestID(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	tool, err := newSendMessageTool(mw, LeaderAgentName)
	assert.NoError(t, err)

	id := tool.shutdownRequestID("worker")
	assert.Contains(t, id, "shutdown-")
	assert.Contains(t, id, "@worker")
}

func TestSendMessageTool_BuildOutboxMessage_DM(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	tool, err := newSendMessageTool(mw, LeaderAgentName)
	assert.NoError(t, err)

	args := &sendMessageArgs{Content: "hello", Summary: "hi"}
	msg, err := tool.buildOutboxMessage(messageTypeDM, "worker", false, args)
	assert.NoError(t, err)
	assert.Equal(t, "worker", msg.To)
	assert.Equal(t, messageTypeDM, msg.Type)
	assert.Equal(t, "hello", msg.Text)
	assert.Equal(t, "hi", msg.Summary)
}

func TestSendMessageTool_BuildOutboxMessage_Broadcast(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	tool, err := newSendMessageTool(mw, LeaderAgentName)
	assert.NoError(t, err)

	args := &sendMessageArgs{Content: "broadcast msg", Summary: "alert"}
	msg, err := tool.buildOutboxMessage(messageTypeBroadcast, "*", false, args)
	assert.NoError(t, err)
	assert.Equal(t, "*", msg.To)
	assert.Equal(t, messageTypeBroadcast, msg.Type)
	assert.Equal(t, "broadcast msg", msg.Text)
	assert.Equal(t, "alert", msg.Summary)
}

func TestSendMessageTool_BuildOutboxMessage_ShutdownRequest(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	tool, err := newSendMessageTool(mw, LeaderAgentName)
	assert.NoError(t, err)

	args := &sendMessageArgs{Content: "please shutdown"}
	msg, err := tool.buildOutboxMessage(messageTypeShutdownRequest, "worker", false, args)
	assert.NoError(t, err)
	assert.Equal(t, "worker", msg.To)
	assert.Equal(t, messageTypeShutdownRequest, msg.Type)
	assert.NotEmpty(t, msg.RequestID)
	assert.NotEmpty(t, msg.Text)
}

func TestSendMessageTool_BuildOutboxMessage_ShutdownResponse(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	tool, err := newSendMessageTool(mw, "worker")
	assert.NoError(t, err)

	args := &sendMessageArgs{RequestID: "req-123", Content: "done"}
	msg, err := tool.buildOutboxMessage(messageTypeShutdownResponse, LeaderAgentName, true, args)
	assert.NoError(t, err)
	assert.Equal(t, LeaderAgentName, msg.To)
	assert.Equal(t, messageTypeShutdownResponse, msg.Type)
	assert.NotEmpty(t, msg.Text)
}

func TestSendMessageTool_BuildResult_DM(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	tool, err := newSendMessageTool(mw, LeaderAgentName)
	assert.NoError(t, err)

	args := &sendMessageArgs{Content: "hello", Summary: "greeting"}
	msg := &outboxMessage{To: "worker", Type: messageTypeDM}
	result := tool.buildResult(messageTypeDM, "worker", false, msg, args)
	assert.Equal(t, true, result["success"])
	assert.Contains(t, result["message"], "Message sent to worker")
	routing := result["routing"].(map[string]any)
	assert.Equal(t, "@worker", routing["target"])
	assert.Equal(t, LeaderAgentName, routing["sender"])
}

func TestSendMessageTool_BuildResult_Broadcast(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	tool, err := newSendMessageTool(mw, LeaderAgentName)
	assert.NoError(t, err)

	args := &sendMessageArgs{Content: "msg", Summary: "alert"}
	msg := &outboxMessage{To: "*", Type: messageTypeBroadcast}
	result := tool.buildResult(messageTypeBroadcast, "*", false, msg, args)
	assert.Equal(t, true, result["success"])
	assert.Contains(t, result["message"], "broadcast")
	routing := result["routing"].(map[string]any)
	assert.Equal(t, "*", routing["target"])
}

func TestSendMessageTool_BuildResult_ShutdownRequest(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	tool, err := newSendMessageTool(mw, LeaderAgentName)
	assert.NoError(t, err)

	args := &sendMessageArgs{}
	msg := &outboxMessage{To: "worker", Type: messageTypeShutdownRequest, RequestID: "req-999"}
	result := tool.buildResult(messageTypeShutdownRequest, "worker", false, msg, args)
	assert.Equal(t, true, result["success"])
	assert.Contains(t, result["message"], "Shutdown request sent")
	assert.Equal(t, "req-999", result["request_id"])
	assert.Equal(t, "worker", result["target"])
}

func TestSendMessageTool_BuildResult_ShutdownResponse(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	tool, err := newSendMessageTool(mw, "worker")
	assert.NoError(t, err)

	args := &sendMessageArgs{}
	msg := &outboxMessage{To: LeaderAgentName, Type: messageTypeShutdownResponse}
	result := tool.buildResult(messageTypeShutdownResponse, LeaderAgentName, true, msg, args)
	assert.Equal(t, true, result["success"])
	assert.Equal(t, "Shutdown approved", result["message"])
}

func TestSendMessageTool_InvokableRun_DM_NonMemberRecipient(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	ctx := context.Background()

	createTool := newTeamCreateTool(mw)
	_, err := createTool.InvokableRun(ctx, `{"team_name":"myteam"}`)
	assert.NoError(t, err)

	tool, err := newSendMessageTool(mw, LeaderAgentName)
	assert.NoError(t, err)

	_, err = tool.InvokableRun(ctx, `{"type":"message","recipient":"nonexistent","content":"hello","summary":"hi"}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not a member")
}

func TestSendMessageTool_InvokableRun_ShutdownResponse_ByLeader(t *testing.T) {
	mw, conf := newTestTeamMiddleware()
	ctx := context.Background()

	createTool := newTeamCreateTool(mw)
	_, err := createTool.InvokableRun(ctx, `{"team_name":"myteam"}`)
	assert.NoError(t, err)

	teamName := mw.getTeamName()

	cm := conf
	err = cm.AddMember(ctx, teamName, teamMember{Name: "worker", JoinedAt: time.Now()})
	assert.NoError(t, err)

	inboxPath := inboxFilePath(conf.BaseDir, teamName, "worker")
	err = conf.Backend.Write(ctx, &WriteRequest{FilePath: inboxPath, Content: "[]"})
	assert.NoError(t, err)

	tool, err := newSendMessageTool(mw, LeaderAgentName)
	assert.NoError(t, err)

	result, err := tool.InvokableRun(ctx, `{"type":"shutdown_response","recipient":"worker","request_id":"req-1","approve":true}`)
	assert.NoError(t, err)
	assert.Contains(t, result, "success")
	assert.Contains(t, result, "Shutdown approved")
}

func TestSendMessageTool_InvokableRun_Broadcast_NoContent(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	ctx := context.Background()

	createTool := newTeamCreateTool(mw)
	_, err := createTool.InvokableRun(ctx, `{"team_name":"myteam"}`)
	assert.NoError(t, err)

	tool, err := newSendMessageTool(mw, LeaderAgentName)
	assert.NoError(t, err)

	_, err = tool.InvokableRun(ctx, `{"type":"broadcast","summary":"hi"}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "'content' is required")
}

func TestSendMessageTool_InvokableRun_Broadcast_NoSummary(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	ctx := context.Background()

	createTool := newTeamCreateTool(mw)
	_, err := createTool.InvokableRun(ctx, `{"team_name":"myteam"}`)
	assert.NoError(t, err)

	tool, err := newSendMessageTool(mw, LeaderAgentName)
	assert.NoError(t, err)

	_, err = tool.InvokableRun(ctx, `{"type":"broadcast","content":"hello"}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "'summary' is required")
}

func TestSendMessageTool_InvokableRun_ShutdownRequest_NonMember(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	ctx := context.Background()

	createTool := newTeamCreateTool(mw)
	_, err := createTool.InvokableRun(ctx, `{"team_name":"myteam"}`)
	assert.NoError(t, err)

	tool, err := newSendMessageTool(mw, LeaderAgentName)
	assert.NoError(t, err)

	_, err = tool.InvokableRun(ctx, `{"type":"shutdown_request","recipient":"ghost"}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not a member")
}

func TestSendMessageTool_InvokableRun_DM_VerifyInboxContent(t *testing.T) {
	mw, conf := newTestTeamMiddleware()
	ctx := context.Background()

	createTool := newTeamCreateTool(mw)
	_, err := createTool.InvokableRun(ctx, `{"team_name":"myteam"}`)
	assert.NoError(t, err)

	teamName := mw.getTeamName()

	cm := conf
	err = cm.AddMember(ctx, teamName, teamMember{Name: "worker", JoinedAt: time.Now()})
	assert.NoError(t, err)

	inboxPath := inboxFilePath(conf.BaseDir, teamName, "worker")
	err = conf.Backend.Write(ctx, &WriteRequest{FilePath: inboxPath, Content: "[]"})
	assert.NoError(t, err)

	tool, err := newSendMessageTool(mw, LeaderAgentName)
	assert.NoError(t, err)

	_, err = tool.InvokableRun(ctx, `{"type":"message","recipient":"worker","content":"task one","summary":"do this"}`)
	assert.NoError(t, err)
	_, err = tool.InvokableRun(ctx, `{"type":"message","recipient":"worker","content":"task two","summary":"and this"}`)
	assert.NoError(t, err)

	backend := conf.Backend.(*inMemoryBackend)
	backend.mu.RLock()
	content := backend.files[inboxPath]
	backend.mu.RUnlock()

	var msgs []InboxMessage
	err = sonic.UnmarshalString(content, &msgs)
	assert.NoError(t, err)
	assert.Len(t, msgs, 2)
	assert.Equal(t, "task one", msgs[0].Text)
	assert.Equal(t, "task two", msgs[1].Text)
}

func TestSendMessageTool_InvokableRun_ShutdownRequest_VerifyResult(t *testing.T) {
	mw, conf := newTestTeamMiddleware()
	ctx := context.Background()

	createTool := newTeamCreateTool(mw)
	_, err := createTool.InvokableRun(ctx, `{"team_name":"myteam"}`)
	assert.NoError(t, err)

	teamName := mw.getTeamName()

	cm := conf
	err = cm.AddMember(ctx, teamName, teamMember{Name: "worker", JoinedAt: time.Now()})
	assert.NoError(t, err)

	inboxPath := inboxFilePath(conf.BaseDir, teamName, "worker")
	err = conf.Backend.Write(ctx, &WriteRequest{FilePath: inboxPath, Content: "[]"})
	assert.NoError(t, err)

	tool, err := newSendMessageTool(mw, LeaderAgentName)
	assert.NoError(t, err)

	result, err := tool.InvokableRun(ctx, `{"type":"shutdown_request","recipient":"worker"}`)
	assert.NoError(t, err)

	var resultMap map[string]any
	err = sonic.UnmarshalString(result, &resultMap)
	assert.NoError(t, err)
	assert.Equal(t, true, resultMap["success"])
	assert.NotEmpty(t, resultMap["request_id"])
	assert.Equal(t, "worker", resultMap["target"])

	backend := conf.Backend.(*inMemoryBackend)
	backend.mu.RLock()
	content := backend.files[inboxPath]
	backend.mu.RUnlock()

	var msgs []InboxMessage
	err = sonic.UnmarshalString(content, &msgs)
	assert.NoError(t, err)
	assert.Len(t, msgs, 1)
	assert.Equal(t, LeaderAgentName, msgs[0].From)
}

func TestSendMessageTool_ValidateRecipient_EmptyTo(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	ctx := context.Background()

	createTool := newTeamCreateTool(mw)
	_, err := createTool.InvokableRun(ctx, `{"team_name":"myteam"}`)
	assert.NoError(t, err)

	tool, err := newSendMessageTool(mw, LeaderAgentName)
	assert.NoError(t, err)

	err = tool.validateRecipient(ctx, mw.getTeamName(), messageTypeDM, "")
	assert.NoError(t, err)
}

func TestSendMessageTool_ValidateRecipient_MemberExists(t *testing.T) {
	mw, conf := newTestTeamMiddleware()
	ctx := context.Background()

	createTool := newTeamCreateTool(mw)
	_, err := createTool.InvokableRun(ctx, `{"team_name":"myteam"}`)
	assert.NoError(t, err)

	teamName := mw.getTeamName()
	cm := conf
	err = cm.AddMember(ctx, teamName, teamMember{Name: "worker", JoinedAt: time.Now()})
	assert.NoError(t, err)

	tool, err := newSendMessageTool(mw, LeaderAgentName)
	assert.NoError(t, err)

	err = tool.validateRecipient(ctx, teamName, messageTypeDM, "worker")
	assert.NoError(t, err)
}

func TestSendMessageTool_InvokableRun_Broadcast_ExcludesSender(t *testing.T) {
	mw, conf := newTestTeamMiddleware()
	ctx := context.Background()

	createTool := newTeamCreateTool(mw)
	_, err := createTool.InvokableRun(ctx, `{"team_name":"myteam"}`)
	assert.NoError(t, err)

	teamName := mw.getTeamName()

	cm := conf
	err = cm.AddMember(ctx, teamName, teamMember{Name: "worker", JoinedAt: time.Now()})
	assert.NoError(t, err)

	workerInboxPath := inboxFilePath(conf.BaseDir, teamName, "worker")
	err = conf.Backend.Write(ctx, &WriteRequest{FilePath: workerInboxPath, Content: "[]"})
	assert.NoError(t, err)

	tool, err := newSendMessageTool(mw, LeaderAgentName)
	assert.NoError(t, err)

	_, err = tool.InvokableRun(ctx, `{"type":"broadcast","content":"hello team","summary":"msg"}`)
	assert.NoError(t, err)

	backend := conf.Backend.(*inMemoryBackend)

	backend.mu.RLock()
	workerContent := backend.files[workerInboxPath]
	backend.mu.RUnlock()
	var workerMsgs []InboxMessage
	err = sonic.UnmarshalString(workerContent, &workerMsgs)
	assert.NoError(t, err)
	assert.Len(t, workerMsgs, 1)

	leaderInboxPath := filepath.Join(conf.BaseDir, "teams", teamName, "inboxes", LeaderAgentName+".json")
	backend.mu.RLock()
	leaderContent := backend.files[leaderInboxPath]
	backend.mu.RUnlock()
	var leaderMsgs []InboxMessage
	err = sonic.UnmarshalString(leaderContent, &leaderMsgs)
	assert.NoError(t, err)
	assert.Len(t, leaderMsgs, 0)
}

func TestSendMessageTool_InvokableRun_TeammateAsSender(t *testing.T) {
	mw, conf := newTestTeamMiddleware()
	ctx := context.Background()

	createTool := newTeamCreateTool(mw)
	_, err := createTool.InvokableRun(ctx, `{"team_name":"myteam"}`)
	assert.NoError(t, err)

	teamName := mw.getTeamName()

	cm := conf
	err = cm.AddMember(ctx, teamName, teamMember{Name: "worker", JoinedAt: time.Now()})
	assert.NoError(t, err)

	leaderInboxPath := inboxFilePath(conf.BaseDir, teamName, LeaderAgentName)

	tool, err := newSendMessageTool(mw, "worker")
	assert.NoError(t, err)

	result, err := tool.InvokableRun(ctx, `{"type":"message","recipient":"team-lead","content":"update","summary":"progress report"}`)
	assert.NoError(t, err)
	assert.Contains(t, result, "success")

	backend := conf.Backend.(*inMemoryBackend)
	backend.mu.RLock()
	content := backend.files[leaderInboxPath]
	backend.mu.RUnlock()

	var msgs []InboxMessage
	err = sonic.UnmarshalString(content, &msgs)
	assert.NoError(t, err)
	assert.Len(t, msgs, 1)
	assert.Equal(t, "worker", msgs[0].From)
	assert.Equal(t, LeaderAgentName, msgs[0].To)
	assert.Equal(t, "update", msgs[0].Text)
}

func TestSendMessageTool_BuildOutboxMessage_DefaultCase(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	tool, err := newSendMessageTool(mw, LeaderAgentName)
	assert.NoError(t, err)

	args := &sendMessageArgs{Content: "some content", Summary: "note"}
	msg, err := tool.buildOutboxMessage(messageTypeTaskAssignment, "worker", false, args)
	assert.NoError(t, err)
	assert.Equal(t, "worker", msg.To)
	assert.Equal(t, messageTypeTaskAssignment, msg.Type)
	assert.Equal(t, "some content", msg.Text)
}

func TestSendMessageTool_BuildResult_DefaultCase(t *testing.T) {
	mw, _ := newTestTeamMiddleware()
	tool, err := newSendMessageTool(mw, LeaderAgentName)
	assert.NoError(t, err)

	args := &sendMessageArgs{Content: "hello"}
	msg := &outboxMessage{To: "worker", Type: messageTypeTaskAssignment}
	result := tool.buildResult(messageTypeTaskAssignment, "worker", false, msg, args)
	assert.Equal(t, true, result["success"])
	assert.Equal(t, "Message sent to worker", result["message"])
}
