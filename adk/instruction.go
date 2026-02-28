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

package adk

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/adk/internal"
)

const (
	TransferToAgentInstruction = `Available other agents: %s

Decision rule:
- If you're best suited for the question according to your description: ANSWER
- If another agent is better according its description: CALL '%s' function with their agent name

When transferring: OUTPUT ONLY THE FUNCTION CALL`

	TransferToAgentInstructionChinese = `可用的其他 agent：%s

决策规则：
- 如果根据你的职责描述，你最适合回答这个问题：ANSWER
- 如果根据其职责描述，另一个 agent 更适合：调用 %s 函数，并传入该 agent 的名称

当进行移交时：只输出函数调用，不要输出其他任何内容`

	agentDescriptionTpl        = "\n- Agent name: %s\n  Agent description: %s"
	agentDescriptionTplChinese = "\n- Agent 名字: %s\n  Agent 描述: %s"
)

func genTransferToAgentInstruction(ctx context.Context, agents []Agent) string {
	tpl := internal.SelectPrompt(internal.I18nPrompts{
		English: agentDescriptionTpl,
		Chinese: agentDescriptionTplChinese,
	})
	instruction := internal.SelectPrompt(internal.I18nPrompts{
		English: TransferToAgentInstruction,
		Chinese: TransferToAgentInstructionChinese,
	})

	var sb strings.Builder
	for _, agent := range agents {
		sb.WriteString(fmt.Sprintf(tpl, agent.Name(ctx), agent.Description(ctx)))
	}

	return fmt.Sprintf(instruction, sb.String(), TransferToAgentToolName)
}
