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
	"fmt"
	"time"

	"github.com/bytedance/sonic"
)

// SendIdleNotification sends an idle notification from a teammate to the leader.
func SendIdleNotification(ctx context.Context, mailbox *mailbox, info *idleInfo) error {
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	textData := map[string]any{
		"type":       "idle_notification",
		"from":       info.AgentName,
		"timestamp":  now,
		"idleReason": info.Status,
	}
	text, err := sonic.MarshalString(textData)
	if err != nil {
		return fmt.Errorf("marshal idle info: %w", err)
	}
	return mailbox.Send(ctx, &outboxMessage{
		To:   LeaderAgentName,
		Type: messageTypeIdleNotification,
		Text: text,
	})
}

// FormatIdleNotification formats an idle notification as readable text.
func FormatIdleNotification(info *idleInfo) string {
	return fmt.Sprintf("Teammate %q completed turn %d and is now %s",
		info.AgentName, info.TurnCount, info.Status)
}
