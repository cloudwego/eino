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

package subagent

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/internal"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

type taskStopInput struct {
	TaskID string `json:"task_id" jsonschema:"required" jsonschema_description:"The ID of the background task to stop"`
}

func newTaskStopTool[M adk.MessageType](mgr *TypedTaskMgr[M]) (tool.InvokableTool, error) {
	desc := internal.SelectPrompt(internal.I18nPrompts{
		English: taskStopToolDescription,
		Chinese: taskStopToolDescriptionChinese,
	})
	return utils.InferTool(taskStopToolName, desc, func(ctx context.Context, input taskStopInput) (string, error) {
		if err := mgr.Cancel(input.TaskID); err != nil {
			return fmt.Sprintf("Failed to stop task %q: %s", input.TaskID, err.Error()), nil
		}
		return fmt.Sprintf("Successfully stopped task: %s", input.TaskID), nil
	})
}
