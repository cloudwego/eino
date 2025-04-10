/*
 * Copyright 2025 CloudWeGo Authors
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

package agent

import (
	"errors"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// ChatModelWithTools returns a BaseChatModel with tools bound. It prioritizes toolCallingModel over model_.
// If toolCallingModel is provided, it will be used with WithTools method.
// If only model_ is provided, it will try to cast it to ToolCallingChatModel first, otherwise use BindTools.
func ChatModelWithTools(model_ model.ChatModel, toolCallingModel model.ToolCallingChatModel,
	toolInfos []*schema.ToolInfo) (model.BaseChatModel, error) {

	if toolCallingModel != nil {
		return toolCallingModel.WithTools(toolInfos)
	}

	if model_ != nil {
		if m, ok := model_.(model.ToolCallingChatModel); ok {
			return m.WithTools(toolInfos)
		}

		err := model_.BindTools(toolInfos)
		if err != nil {
			return nil, err
		}

		return model_, nil
	}

	return nil, errors.New("no chat model provided")
}
