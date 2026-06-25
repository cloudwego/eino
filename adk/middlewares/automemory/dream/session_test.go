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

package dream

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	adksession "github.com/cloudwego/eino/adk/session"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func TestNewSessionHistoryGrepTool(t *testing.T) {
	ctx := context.Background()
	store := adksession.NewInMemoryStore[*schema.Message](nil)
	sessionID := "session-1"

	appendEvent := func(eventID string, msg *schema.Message) {
		err := store.AppendEvents(ctx, sessionID, []*adk.SessionEvent[*schema.Message]{{
			EventID: eventID,
			Kind:    adk.SessionEventMessage,
			Message: msg,
		}})
		require.NoError(t, err)
	}

	appendEvent("e1", schema.UserMessage("hello there"))
	appendEvent("e2", schema.AssistantMessage("build failure: missing dependency", nil))
	appendEvent("e3", schema.ToolMessage("Build Failure: retry later", "call-1"))

	bt, err := newSessionHistoryGrepTool[*schema.Message](store)
	require.NoError(t, err)

	result, err := bt.(tool.InvokableTool).InvokableRun(
		withDreamRunMeta(ctx, &dreamRunMeta{SessionID: sessionID, SearchSessionIDs: []string{sessionID}}),
		`{"query":"build failure","limit":2}`,
	)
	require.NoError(t, err)
	require.Equal(t, "tool: Build Failure: retry later\nassistant: build failure: missing dependency", result)
}

func TestNewSessionHistoryGrepTool_SearchesRunScopedSessions(t *testing.T) {
	ctx := context.Background()
	store := adksession.NewInMemoryStore[*schema.Message](nil)

	appendEvent := func(sessionID, eventID string, msg *schema.Message) {
		err := store.AppendEvents(ctx, sessionID, []*adk.SessionEvent[*schema.Message]{{
			EventID: eventID,
			Kind:    adk.SessionEventMessage,
			Message: msg,
		}})
		require.NoError(t, err)
	}

	appendEvent("session-a", "a1", schema.AssistantMessage("build failure: missing dependency", nil))
	appendEvent("session-b", "b1", schema.ToolMessage("build failure: retry later", "call-1"))
	appendEvent("session-c", "c1", schema.AssistantMessage("build failure: should not be searched", nil))

	bt, err := newSessionHistoryGrepTool[*schema.Message](store)
	require.NoError(t, err)

	result, err := bt.(tool.InvokableTool).InvokableRun(
		withDreamRunMeta(ctx, &dreamRunMeta{
			SessionID:        "session-c",
			SearchSessionIDs: []string{"session-a", "session-b"},
		}),
		`{"query":"build failure","limit":5}`,
	)
	require.NoError(t, err)
	require.Contains(t, result, "[session-a] assistant: build failure: missing dependency")
	require.Contains(t, result, "[session-b] tool: build failure: retry later")
	require.NotContains(t, result, "should not be searched")
}

func TestNewSessionHistoryGrepTool_InfoUsesChineseDescription(t *testing.T) {
	require.NoError(t, adk.SetLanguage(adk.LanguageChinese))
	defer func() {
		require.NoError(t, adk.SetLanguage(adk.LanguageEnglish))
	}()

	bt, err := newSessionHistoryGrepTool[*schema.Message](adksession.NewInMemoryStore[*schema.Message](nil))
	require.NoError(t, err)

	info, err := bt.Info(context.Background())
	require.NoError(t, err)
	require.Contains(t, info.Desc, "在当前 dream 运行范围内的会话历史中按精确关键词搜索")
}

func TestNewSessionHistoryGrepTool_AllowsNilStore(t *testing.T) {
	bt, err := newSessionHistoryGrepTool[*schema.Message](nil)
	require.NoError(t, err)
	require.Nil(t, bt)
}
