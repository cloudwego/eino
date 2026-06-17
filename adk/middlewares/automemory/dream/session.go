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
	"fmt"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/internal"
	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
)

type grepSessionHistoryInput struct {
	Query string `json:"query" jsonschema:"required,description=the narrow term to search in current session history"`
	Limit int    `json:"limit,omitempty" jsonschema:"description=maximum number of matching lines to return"`
}

type dreamRunMeta struct {
	MemoryDirectory  string
	SessionID        string
	SearchSessionIDs []string
}

type dreamRunMetaKey struct{}

func withDreamRunMeta(ctx context.Context, meta *dreamRunMeta) context.Context {
	return context.WithValue(ctx, dreamRunMetaKey{}, meta)
}

func getDreamRunMeta(ctx context.Context) *dreamRunMeta {
	if v := ctx.Value(dreamRunMetaKey{}); v != nil {
		if meta, ok := v.(*dreamRunMeta); ok {
			return meta
		}
	}
	return nil
}

func newSessionHistoryGrepTool[M adk.MessageType](store adk.SessionEventStore[M]) (tool.BaseTool, error) {
	if store == nil {
		return nil, nil
	}

	t, err := toolutils.InferTool("grep_session_history", internal.SelectPrompt(internal.I18nPrompts{
		English: "Search the session histories included in the current dream run with a narrow query and return matching lines.",
		Chinese: "在当前 dream 运行范围内的会话历史中按精确关键词搜索，并返回匹配行。",
	}), func(ctx context.Context, input grepSessionHistoryInput) (string, error) {
		meta := getDreamRunMeta(ctx)
		if meta == nil {
			return "", fmt.Errorf("grep_session_history: missing dream run metadata")
		}
		sessionIDs := resolveSearchSessionIDs(meta)
		if len(sessionIDs) == 0 {
			return "", fmt.Errorf("grep_session_history: no searchable sessions in current dream run")
		}
		query := strings.TrimSpace(input.Query)
		if query == "" {
			return "", fmt.Errorf("grep_session_history: empty query")
		}
		limit := input.Limit
		if limit <= 0 {
			limit = 50
		}

		pageSize := limit
		if pageSize < 100 {
			pageSize = 100
		}

		var (
			after string
			found []string
		)
		includeSessionPrefix := len(sessionIDs) > 1
		for _, sessionID := range sessionIDs {
			after = ""
			for len(found) < limit {
				result, err := store.LoadEvents(ctx, &adk.LoadSessionEventsRequest{
					SessionID: sessionID,
					After:     after,
					Limit:     pageSize,
					Reverse:   true,
					Kinds:     []adk.SessionEventKind{adk.SessionEventMessage},
				})
				if err != nil {
					return "", err
				}
				if result == nil || len(result.Events) == 0 {
					break
				}
				for _, ev := range result.Events {
					found = appendMatchingSessionHistoryLines(found, sessionID, sessionEventMessageString(ev.Message), query, limit, includeSessionPrefix)
					if len(found) >= limit {
						break
					}
				}
				if result.Next == "" {
					break
				}
				after = result.Next
			}
			if len(found) >= limit {
				break
			}
		}

		return strings.Join(found, "\n"), nil
	})
	if err != nil {
		return nil, err
	}

	return t, nil
}

func resolveSearchSessionIDs(meta *dreamRunMeta) []string {
	if meta == nil {
		return nil
	}
	if len(meta.SearchSessionIDs) > 0 {
		return meta.SearchSessionIDs
	}
	if meta.SessionID != "" {
		return []string{meta.SessionID}
	}
	return nil
}

func appendMatchingSessionHistoryLines(dst []string, sessionID, message, query string, limit int, includeSessionPrefix bool) []string {
	needle := strings.ToLower(query)
	for _, line := range strings.Split(message, "\n") {
		if strings.Contains(strings.ToLower(line), needle) {
			if includeSessionPrefix {
				line = fmt.Sprintf("[%s] %s", sessionID, line)
			}
			dst = append(dst, line)
			if len(dst) >= limit {
				return dst
			}
		}
	}
	return dst
}

func sessionEventMessageString[M adk.MessageType](msg M) string {
	switch m := any(msg).(type) {
	case *schema.Message:
		if m == nil {
			return ""
		}
		return m.String()
	case *schema.AgenticMessage:
		if m == nil {
			return ""
		}
		return m.String()
	default:
		return ""
	}
}
