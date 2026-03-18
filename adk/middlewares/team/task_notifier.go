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
	"time"

	"github.com/bytedance/sonic"

	"github.com/cloudwego/eino/adk/middlewares/plantask"
)

// newTaskAssignedNotifier returns an OnTaskAssigned callback that sends
// task_assignment messages to the assignee's mailbox.
func newTaskAssignedNotifier(conf *Config, teamNameFn func() string) func(ctx context.Context, a plantask.TaskAssignment) error {
	return func(ctx context.Context, a plantask.TaskAssignment) error {
		teamName := teamNameFn()
		if teamName == "" {
			return nil
		}

		senderName := a.AssignedBy
		if senderName == "" {
			senderName = LeaderAgentName
		}

		mb := newMailboxFromConfig(conf, teamName, senderName)
		defer mb.Close()

		text, err := sonic.MarshalString(map[string]any{
			"type":        string(messageTypeTaskAssignment),
			"taskId":      a.TaskID,
			"subject":     a.Subject,
			"description": a.Description,
			"assignedBy":  senderName,
			"timestamp":   time.Now().UTC().Format(time.RFC3339),
		})
		if err != nil {
			return err
		}

		return mb.Send(ctx, &outboxMessage{
			To:   a.Owner,
			Type: messageTypeTaskAssignment,
			Text: text,
		})
	}
}
