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
	TransferToAgentInstruction = "You have a list of other agents to transfer to:\n%s\nIf you are the best to answer the question according to your description, you can answer it.\nIf another agent is better for answering the question according to its description, call `" + TransferToAgentToolName + "` function to transfer the question to that agent. When transferring, do not generate any text other than the function call."
)

func genTransferToAgentInstruction(ctx context.Context, agents []Agent) string {
	var sb strings.Builder
	for _, agent := range agents {
		sb.WriteString(fmt.Sprintf("\nAgent name: %s\n Agent description: %s\n",
			agent.Name(ctx), agent.Description(ctx)))
	}

	return fmt.Sprintf(TransferToAgentInstruction, sb.String())
}
