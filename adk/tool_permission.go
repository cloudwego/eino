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
	"sync"
)

type toolPermissionDecisionStore struct {
	mu       sync.RWMutex
	decision map[string]string
}

type toolPermissionDecisionKey struct{}

func contextWithToolPermissionDecisionStore(ctx context.Context) context.Context {
	if ctx.Value(toolPermissionDecisionKey{}) != nil {
		return ctx
	}
	return context.WithValue(ctx, toolPermissionDecisionKey{}, &toolPermissionDecisionStore{decision: map[string]string{}})
}

// SetToolPermissionDecision records the final permission decision for one tool
// call. Decisions are keyed by ToolContext.CallID / tool-use ID.
func SetToolPermissionDecision(ctx context.Context, toolCallID, decision string) {
	if toolCallID == "" || decision == "" {
		return
	}
	store, _ := ctx.Value(toolPermissionDecisionKey{}).(*toolPermissionDecisionStore)
	if store == nil {
		return
	}
	store.mu.Lock()
	store.decision[toolCallID] = decision
	store.mu.Unlock()
}

// GetToolPermissionDecision returns the decision recorded for a single tool
// call, or an empty string when no middleware participated.
func GetToolPermissionDecision(ctx context.Context, toolCallID string) string {
	store, _ := ctx.Value(toolPermissionDecisionKey{}).(*toolPermissionDecisionStore)
	if store == nil || toolCallID == "" {
		return ""
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	return store.decision[toolCallID]
}
