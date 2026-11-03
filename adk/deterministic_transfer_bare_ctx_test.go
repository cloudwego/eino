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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	mockModel "github.com/cloudwego/eino/internal/mock/components/model"
	"github.com/cloudwego/eino/schema"
)

// Run on a deterministic-transfer wrapper is a public entry that may be called
// with the caller's own context, which does not yet carry a run context. The
// flowAgent path used to dereference the parent run context and session without
// a nil check and panicked; it must run to completion instead.
func TestDeterministicTransferFlowAgentRunWithoutRunContext(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)
	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).Return(
		schema.AssistantMessage("hello", nil), nil).AnyTimes()

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "a",
		Description: "d",
		Instruction: "i",
		Model:       cm,
	})
	require.NoError(t, err)

	// AgentWithOptions is a public API that returns the internal flowAgent,
	// the branch AgentWithDeterministicTransferTo handles specially.
	flowWrapped := AgentWithOptions(ctx, agent)

	wrapped := AgentWithDeterministicTransferTo(ctx, &DeterministicTransferConfig{
		Agent:        flowWrapped,
		ToAgentNames: []string{"next"},
	})

	iter := wrapped.Run(context.Background(), &AgentInput{
		Messages: []Message{schema.UserMessage("hi")},
	})
	events := 0
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		events++
		assert.NoError(t, ev.Err)
	}
	assert.Positive(t, events)
}
