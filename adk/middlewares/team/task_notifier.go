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

// task_notifier.go sends task_assignment messages to an assignee's inbox
// when a task is assigned via plantask.

package team

import (
	"context"

	"github.com/bytedance/sonic"

	"github.com/cloudwego/eino/adk/middlewares/plantask"
)

// taskAssignmentPayload is the typed payload for task assignment notifications.
type taskAssignmentPayload struct {
	protocolHeader
	TaskID      string `json:"taskId"`
	Subject     string `json:"subject"`
	Description string `json:"description"`
	AssignedBy  string `json:"assignedBy"`
}

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

		text, err := sonic.MarshalString(taskAssignmentPayload{
			protocolHeader: newProtocolHeader(messageTypeTaskAssignment, "", ""),
			TaskID:         a.TaskID,
			Subject:        a.Subject,
			Description:    a.Description,
			AssignedBy:     senderName,
		})
		if err != nil {
			return err
		}

		if err := mb.Send(ctx, &outboxMessage{
			To:   a.Owner,
			Type: messageTypeTaskAssignment,
			Text: text,
		}); err != nil {
			return err
		}

		return nil
	}
}
