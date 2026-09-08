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

package tool

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	adksession "github.com/cloudwego/eino/adk/session"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type sessionContextModel struct {
	sessionID string
	err       error
}

func (m *sessionContextModel) Generate(
	ctx context.Context,
	_ []*schema.Message,
	_ ...model.Option,
) (*schema.Message, error) {
	m.sessionID, m.err = sessionIDFromContext(ctx)
	return schema.AssistantMessage("done", nil), nil
}

func (m *sessionContextModel) Stream(
	ctx context.Context,
	input []*schema.Message,
	options ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, input, options...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func TestSessionIDFromContextIsOptionalAndReadsRunnerSession_BitsUT(t *testing.T) {
	sessionID, err := sessionIDFromContext(context.Background())
	require.Empty(t, sessionID)
	require.NoError(t, err)

	const expectedSessionID = "managed-tool-parent"
	chatModel := &sessionContextModel{}
	agent, err := adk.NewChatModelAgent(context.Background(), &adk.ChatModelAgentConfig{
		Name: "session-context", Instruction: "test", Model: chatModel,
	})
	require.NoError(t, err)
	runner := adk.NewRunner(context.Background(), adk.RunnerConfig{
		Agent: agent, SessionID: expectedSessionID,
		SessionStore: adksession.NewInMemoryStore[*schema.Message](nil),
	})
	iterator := runner.Query(context.Background(), "read session")
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		require.NoError(t, event.Err)
	}
	require.NoError(t, chatModel.err)
	require.Equal(t, expectedSessionID, chatModel.sessionID)
}
