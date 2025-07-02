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
)

const (
	TransferToAgentInstruction = `Available other agents: %s

Decision rule:
- If you're best suited for the question according to your description: ANSWER
- If another agent is better according its description: CALL '%s' function with their agent name

When transferring: OUTPUT ONLY THE FUNCTION CALL`
)

func genTransferToAgentInstruction(ctx context.Context, agents []Agent) string {
	var sb strings.Builder
	for _, agent := range agents {
		sb.WriteString(fmt.Sprintf("\n- Agent name: %s\n  Agent description: %s",
			agent.Name(ctx), agent.Description(ctx)))
	}

	return fmt.Sprintf(TransferToAgentInstruction, sb.String(), TransferToAgentToolName)
}
