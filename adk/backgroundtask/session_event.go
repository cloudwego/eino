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

package backgroundtask

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// SessionEventTaskCreated is appended to a parent session after its background
// task has been durably created.
const SessionEventTaskCreated adk.SessionEventKind = "x.eino.background_task.created"

// TaskCreatedSessionEvent is the extension payload for
// SessionEventTaskCreated.
type TaskCreatedSessionEvent struct {
	TaskID      string `json:"task_id"`
	Description string `json:"description"`
}

var taskCreatedSessionEventNamespace = uuid.MustParse("fddebb92-8d8a-4ae4-a652-7a49bbb4de2e")

func init() {
	schema.RegisterName[*TaskCreatedSessionEvent](
		"_eino_adk_background_task_created_session_event",
	)
}

// TaskCreatedSessionEventID returns the deterministic session-local EventID
// for taskID. Immediate Runner emission and outbox-based recovery must use the
// same ID so TaskCreated delivery is idempotent.
func TaskCreatedSessionEventID(taskID string) string {
	return uuid.NewSHA1(taskCreatedSessionEventNamespace, []byte(taskID)).String()
}

// TaskCreatedSessionEventSender creates a Config.SendTaskCreatedEvent callback.
// It emits through the active ChatModelAgent run so Runner remains the sole
// writer of the parent session timeline.
func TaskCreatedSessionEventSender[M adk.MessageType]() func(context.Context, *Task) error {
	return func(ctx context.Context, task *Task) error {
		if task == nil || task.Spec.ID == "" || task.Spec.SessionID == "" {
			return errors.New(
				"backgroundtask: task id and parent session id are required for task-created event",
			)
		}
		sessionID, ok := adk.RunnerSessionID(ctx)
		if !ok || sessionID != task.Spec.SessionID {
			return errors.New(
				"backgroundtask: task-created event requires the matching parent Runner session",
			)
		}
		return adk.TypedSendEvent(ctx, &adk.TypedAgentEvent[M]{
			SessionEventVariant: &adk.SessionEventVariant[M]{
				SessionID: sessionID,
				Event: &adk.SessionEvent[M]{
					EventID:   TaskCreatedSessionEventID(task.Spec.ID),
					Timestamp: task.CreatedAt,
					Kind:      SessionEventTaskCreated,
					Extension: &adk.SessionExtensionEvent{
						Data: &TaskCreatedSessionEvent{
							TaskID: task.Spec.ID, Description: task.Spec.Description,
						},
					},
				},
			},
		})
	}
}
