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
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/schema"
	"github.com/cloudwego/eino/schema/claude"
	"github.com/cloudwego/eino/schema/gemini"
)

type serviceContractStore struct {
	loadReqs   []*LoadSessionEventsRequest
	appendReqs []*AppendSessionEventsRequest[*schema.Message]
	loadErr    error
	appendErr  error
	tail       string
}

func (s *serviceContractStore) LoadEvents(_ context.Context, req *LoadSessionEventsRequest) (*LoadSessionEventsResult[*schema.Message], error) {
	s.loadReqs = append(s.loadReqs, req)
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	return &LoadSessionEventsResult[*schema.Message]{SessionTailEventID: s.tail}, nil
}

func (s *serviceContractStore) AppendEvents(_ context.Context, req *AppendSessionEventsRequest[*schema.Message]) (*AppendSessionEventsResult, error) {
	s.appendReqs = append(s.appendReqs, req)
	if s.appendErr != nil {
		return nil, s.appendErr
	}
	if len(req.Events) > 0 {
		s.tail = req.Events[len(req.Events)-1].EventID
	}
	return &AppendSessionEventsResult{SessionTailEventID: s.tail}, nil
}

func TestUsageHelpersExtractAssistantMetadata(t *testing.T) {
	usage := &schema.TokenUsage{
		PromptTokens:     11,
		CompletionTokens: 7,
		PromptTokenDetails: schema.PromptTokenDetails{
			CachedTokens: 5,
		},
	}
	msg := schema.AssistantMessage("ok", nil)
	msg.ResponseMeta = &schema.ResponseMeta{FinishReason: "stop", Usage: usage}

	assert.Same(t, usage, assistantTokenUsage[*schema.Message](msg))
	assert.Equal(t, "stop", assistantFinishReason[*schema.Message](msg))
	got := modelUsageFromAssistant[*schema.Message](msg)
	require.NotNil(t, got)
	assert.Equal(t, 11, got.InputTokens)
	assert.Equal(t, 7, got.OutputTokens)
	assert.Equal(t, 5, got.CacheReadInputTokens)
	assert.Same(t, usage, got.Raw)

	assert.Nil(t, assistantTokenUsage[*schema.Message](schema.UserMessage("q")))
	assert.Empty(t, assistantFinishReason[*schema.Message](schema.UserMessage("q")))
	assert.Nil(t, modelUsageFromAssistant[*schema.Message](schema.AssistantMessage("no usage", nil)))
	assert.Nil(t, assistantTokenUsage[*schema.Message](nil))

	agenticUsage := &schema.TokenUsage{PromptTokens: 3, CompletionTokens: 4}
	agentic := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ResponseMeta: &schema.AgenticResponseMeta{
			TokenUsage:      agenticUsage,
			ClaudeExtension: &claude.ResponseMetaExtension{StopReason: "end_turn"},
		},
	}
	assert.Same(t, agenticUsage, assistantTokenUsage[*schema.AgenticMessage](agentic))
	assert.Equal(t, "end_turn", assistantFinishReason[*schema.AgenticMessage](agentic))

	agentic.ResponseMeta.ClaudeExtension = nil
	agentic.ResponseMeta.GeminiExtension = &gemini.ResponseMetaExtension{FinishReason: "STOP"}
	assert.Equal(t, "STOP", assistantFinishReason[*schema.AgenticMessage](agentic))
	assert.Empty(t, assistantFinishReason[*schema.AgenticMessage](&schema.AgenticMessage{Role: schema.AgenticRoleTypeUser}))
	assert.Nil(t, assistantTokenUsage[*schema.AgenticMessage](nil))
}

func TestCommonOptionsAndFilteringContracts(t *testing.T) {
	values := map[string]any{"k": "v"}
	base := getCommonOptions(nil,
		WithSessionValues(values),
		withEnableSessionEvents(),
		WithTimelineEvents(),
		withEnableInternalTimelineEvents(),
		WithSkipTransferMessages(),
		withSharedParentSession(),
		WithCallbacks(nil),
		WithRefreshToolInfos(),
	)
	require.NotNil(t, base)
	assert.Equal(t, values, base.sessionValues)
	assert.True(t, base.enableSessionEvents)
	assert.True(t, base.enableTimelineEvents)
	assert.True(t, base.enableInternalTimelineEvents)
	assert.True(t, base.skipTransferMessages)
	assert.True(t, base.sharedParentSession)
	assert.True(t, base.refreshToolInfos)
	assert.Len(t, base.handlers, 1)

	custom := GetImplSpecificOptions(&struct{ Seen bool }{}, WrapImplSpecificOptFn(func(o *struct{ Seen bool }) {
		o.Seen = true
	}))
	assert.True(t, custom.Seen)

	undesignatedCallback := WithCallbacks(nil)
	currentCallback := WithCallbacks(nil).DesignateAgent("parent")
	otherCallback := WithCallbacks(nil).DesignateAgent("child")
	nonCallback := WithRefreshToolInfos()
	filtered := filterCallbackHandlersForNestedAgents("parent", []AgentRunOption{
		undesignatedCallback,
		currentCallback,
		otherCallback,
		nonCallback,
		{},
	})
	assert.Len(t, filtered, 3)
	assert.Equal(t, []string{"child"}, filtered[0].agentNames)
	assert.NotNil(t, filtered[1].implSpecificOptFn)
	assert.Nil(t, filtered[2].implSpecificOptFn)

	cancelOpt := WrapImplSpecificOptFn(func(o *options) {
		o.cancelCtx = &cancelContext{}
	})
	filtered = filterCancelOption([]AgentRunOption{cancelOpt, nonCallback, {}})
	assert.Len(t, filtered, 2)
	assert.NotNil(t, filtered[0].implSpecificOptFn)
	assert.Nil(t, filtered[1].implSpecificOptFn)

	assert.Nil(t, filterCallbackHandlersForNestedAgents("parent", nil))
	assert.Nil(t, filterCancelOption(nil))
	assert.Nil(t, filterOptions("parent", nil))
	assert.Len(t, filterOptions("parent", []AgentRunOption{nonCallback.DesignateAgent("parent"), otherCallback, {}}), 2)
}

func TestToolPermissionDecisionStoreContracts(t *testing.T) {
	ctx := context.Background()
	assert.Empty(t, GetToolPermissionDecision(ctx, "call-1"))
	SetToolPermissionDecision(ctx, "call-1", "allowed")
	assert.Empty(t, GetToolPermissionDecision(ctx, "call-1"))

	ctx = contextWithToolPermissionDecisionStore(ctx)
	same := contextWithToolPermissionDecisionStore(ctx)
	assert.Same(t, ctx, same)

	SetToolPermissionDecision(ctx, "", "allowed")
	SetToolPermissionDecision(ctx, "call-1", "")
	assert.Empty(t, GetToolPermissionDecision(ctx, "call-1"))

	SetToolPermissionDecision(ctx, "call-1", "allowed")
	SetToolPermissionDecision(ctx, "call-2", "denied")
	assert.Equal(t, "allowed", GetToolPermissionDecision(ctx, "call-1"))
	assert.Equal(t, "denied", GetToolPermissionDecision(ctx, "call-2"))
	assert.Empty(t, GetToolPermissionDecision(ctx, ""))
}

func TestLocalSessionServiceHandleContracts(t *testing.T) {
	ctx := context.Background()
	assert.Nil(t, NewLocalSessionService[*schema.Message](nil))

	store := &serviceContractStore{tail: "tail-0"}
	service := NewLocalSessionService[*schema.Message](store)
	require.NotNil(t, service)

	_, err := service.openSession(ctx, nil)
	require.ErrorIs(t, err, ErrSessionBusy)
	_, err = service.openSession(ctx, &openSessionRequest{})
	require.ErrorIs(t, err, ErrSessionBusy)

	opened, err := service.openSession(ctx, &openSessionRequest{sessionID: "sid"})
	require.NoError(t, err)
	require.NotNil(t, opened)

	_, err = service.openSession(ctx, &openSessionRequest{sessionID: "sid"})
	require.ErrorIs(t, err, ErrSessionBusy)

	res, err := opened.handle.loadEvents(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "tail-0", res.SessionTailEventID)
	require.Len(t, store.loadReqs, 1)
	assert.Equal(t, "sid", store.loadReqs[0].SessionID)
	assert.Equal(t, "tail-0", opened.handle.currentTailEventID())

	event := validTestPayload()
	resAppend, err := opened.handle.appendEvents(ctx, &AppendSessionEventsRequest[*schema.Message]{
		Events: []*SessionEvent[*schema.Message]{event},
	})
	require.NoError(t, err)
	assert.Equal(t, event.EventID, resAppend.SessionTailEventID)
	require.Len(t, store.appendReqs, 1)
	assert.Equal(t, "sid", store.appendReqs[0].SessionID)
	assert.Equal(t, "tail-0", store.appendReqs[0].ExpectedSessionTailEventID)
	assert.Equal(t, event.EventID, opened.handle.currentTailEventID())

	resAppend, err = opened.handle.appendEvents(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, event.EventID, resAppend.SessionTailEventID)
	require.Len(t, store.appendReqs, 2)
	assert.Equal(t, "sid", store.appendReqs[1].SessionID)
	assert.Equal(t, event.EventID, store.appendReqs[1].ExpectedSessionTailEventID)

	require.NoError(t, opened.handle.close(ctx))
	require.NoError(t, opened.handle.close(ctx))
	_, err = opened.handle.appendEvents(ctx, nil)
	require.ErrorIs(t, err, ErrSessionBusy)

	reopened, err := service.openSession(ctx, &openSessionRequest{sessionID: "sid"})
	require.NoError(t, err)
	require.NoError(t, reopened.handle.close(ctx))

	store.loadErr = errors.New("load failed")
	opened, err = service.openSession(ctx, &openSessionRequest{sessionID: "sid-load-err"})
	require.NoError(t, err)
	_, err = opened.handle.loadEvents(ctx, &LoadSessionEventsRequest{})
	require.ErrorContains(t, err, "load failed")
	require.NoError(t, opened.handle.close(ctx))

	store.loadErr = nil
	store.appendErr = errors.New("append failed")
	opened, err = service.openSession(ctx, &openSessionRequest{sessionID: "sid-append-err"})
	require.NoError(t, err)
	_, err = opened.handle.appendEvents(ctx, &AppendSessionEventsRequest[*schema.Message]{
		Events: []*SessionEvent[*schema.Message]{validTestPayload()},
	})
	require.ErrorContains(t, err, "append failed")
	require.NoError(t, opened.handle.close(ctx))
}

func TestFencedSessionServiceHandleContracts(t *testing.T) {
	ctx := context.Background()
	assert.Nil(t, NewFencedSessionService[*schema.Message](nil, FencedSessionServiceOptions{}))

	store := newTestFencedSessionStore("token-1")
	service := NewFencedSessionService[*schema.Message](store, FencedSessionServiceOptions{})

	_, err := service.openSession(ctx, nil)
	require.ErrorIs(t, err, ErrSessionBusy)
	_, err = service.openSession(ctx, &openSessionRequest{sessionID: "sid"})
	require.ErrorIs(t, err, ErrSessionFencingTokenRequired)

	opened, err := service.openSession(ctx, &openSessionRequest{
		sessionID:    "sid",
		fencingToken: func(context.Context) (string, error) { return "token-1", nil },
	})
	require.NoError(t, err)

	res, err := opened.handle.loadEvents(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, res.SessionTailEventID)
	assert.Empty(t, opened.handle.currentTailEventID())

	first := validTestPayload()
	resAppend, err := opened.handle.appendEvents(ctx, &AppendSessionEventsRequest[*schema.Message]{
		Events: []*SessionEvent[*schema.Message]{first},
	})
	require.NoError(t, err)
	assert.Equal(t, first.EventID, resAppend.SessionTailEventID)
	assert.Equal(t, first.EventID, opened.handle.currentTailEventID())
	assert.Equal(t, []string{"token-1"}, store.appendedTokens())

	store.helper.loadErr = errors.New("load failed")
	_, err = opened.handle.loadEvents(ctx, &LoadSessionEventsRequest{})
	require.ErrorContains(t, err, "load failed")
	store.helper.loadErr = nil

	require.NoError(t, opened.handle.close(ctx))
	require.NoError(t, opened.handle.close(ctx))
	_, err = opened.handle.appendEvents(ctx, nil)
	require.ErrorIs(t, err, ErrSessionFencingTokenInvalid)

	nilTokenHandle := &fencedSessionHandle[*schema.Message]{store: store, sessionID: "sid-nil-token"}
	_, err = nilTokenHandle.appendEvents(ctx, nil)
	require.ErrorIs(t, err, ErrSessionFencingTokenInvalid)

	noToken, err := service.openSession(ctx, &openSessionRequest{
		sessionID:    "sid-2",
		fencingToken: func(context.Context) (string, error) { return "", nil },
	})
	require.NoError(t, err)
	_, err = noToken.handle.appendEvents(ctx, nil)
	require.ErrorIs(t, err, ErrSessionFencingTokenInvalid)

	tokenErr := errors.New("token failed")
	tokenFail, err := service.openSession(ctx, &openSessionRequest{
		sessionID:    "sid-3",
		fencingToken: func(context.Context) (string, error) { return "", tokenErr },
	})
	require.NoError(t, err)
	_, err = tokenFail.handle.appendEvents(ctx, nil)
	require.ErrorIs(t, err, tokenErr)
}
