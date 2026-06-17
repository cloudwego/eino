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
}

func (s *serviceContractStore) LoadEvents(_ context.Context, req *LoadSessionEventsRequest) (*LoadSessionEventsResult[*schema.Message], error) {
	s.loadReqs = append(s.loadReqs, req)
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	return &LoadSessionEventsResult[*schema.Message]{}, nil
}

func (s *serviceContractStore) AppendEvents(_ context.Context, req *AppendSessionEventsRequest[*schema.Message]) error {
	s.appendReqs = append(s.appendReqs, req)
	if s.appendErr != nil {
		return s.appendErr
	}
	return nil
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

func TestLocalSessionStoreHandleContracts(t *testing.T) {
	ctx := context.Background()
	var nilStore SessionEventStore[*schema.Message]
	_, err := openLocalSession[*schema.Message](ctx, nilStore, &openSessionRequest{sessionID: "sid"})
	require.ErrorIs(t, err, ErrSessionBusy)

	store := &serviceContractStore{}
	require.NotNil(t, store)

	_, err = openLocalSession[*schema.Message](ctx, store, nil)
	require.ErrorIs(t, err, ErrSessionBusy)
	_, err = openLocalSession[*schema.Message](ctx, store, &openSessionRequest{})
	require.ErrorIs(t, err, ErrSessionBusy)

	opened, err := openLocalSession[*schema.Message](ctx, store, &openSessionRequest{sessionID: "sid"})
	require.NoError(t, err)
	require.NotNil(t, opened)

	_, err = openLocalSession[*schema.Message](ctx, store, &openSessionRequest{sessionID: "sid"})
	require.ErrorIs(t, err, ErrSessionBusy)

	res, err := opened.handle.loadEvents(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Len(t, store.loadReqs, 1)
	assert.Equal(t, "sid", store.loadReqs[0].SessionID)

	event := validTestPayload()
	err = opened.handle.appendEvents(ctx, &AppendSessionEventsRequest[*schema.Message]{
		Events: []*SessionEvent[*schema.Message]{event},
	})
	require.NoError(t, err)
	require.Len(t, store.appendReqs, 1)
	assert.Equal(t, "sid", store.appendReqs[0].SessionID)

	err = opened.handle.appendEvents(ctx, nil)
	require.NoError(t, err)
	require.Len(t, store.appendReqs, 2)
	assert.Equal(t, "sid", store.appendReqs[1].SessionID)

	require.NoError(t, opened.handle.close(ctx))
	require.NoError(t, opened.handle.close(ctx))
	err = opened.handle.appendEvents(ctx, nil)
	require.ErrorIs(t, err, ErrSessionBusy)

	reopened, err := openLocalSession[*schema.Message](ctx, store, &openSessionRequest{sessionID: "sid"})
	require.NoError(t, err)
	require.NoError(t, reopened.handle.close(ctx))

	store.loadErr = errors.New("load failed")
	opened, err = openLocalSession[*schema.Message](ctx, store, &openSessionRequest{sessionID: "sid-load-err"})
	require.NoError(t, err)
	_, err = opened.handle.loadEvents(ctx, &LoadSessionEventsRequest{})
	require.ErrorContains(t, err, "load failed")
	require.NoError(t, opened.handle.close(ctx))

	store.loadErr = nil
	store.appendErr = errors.New("append failed")
	opened, err = openLocalSession[*schema.Message](ctx, store, &openSessionRequest{sessionID: "sid-append-err"})
	require.NoError(t, err)
	err = opened.handle.appendEvents(ctx, &AppendSessionEventsRequest[*schema.Message]{
		Events: []*SessionEvent[*schema.Message]{validTestPayload()},
	})
	require.ErrorContains(t, err, "append failed")
	require.NoError(t, opened.handle.close(ctx))
}
