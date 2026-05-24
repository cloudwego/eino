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

package permission

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/internal/core"
	"github.com/cloudwego/eino/schema"
)

const addressSegmentAgent core.AddressSegmentType = "agent"

func TestNewTypedSupportsBothMessageTypes(t *testing.T) {
	checker := func(context.Context, *adk.ToolContext, *schema.ToolArgument) (*ToolCallDecision, error) {
		return &ToolCallDecision{Decision: Allow}, nil
	}

	var _ adk.ChatModelAgentMiddleware = New(checker)
	var _ adk.TypedChatModelAgentMiddleware[*schema.AgenticMessage] = NewTyped[*schema.AgenticMessage](checker)
}

func TestWrapInvokableToolCall_AllowWithUpdatedInput(t *testing.T) {
	m := NewTyped[*schema.Message](func(ctx context.Context, tCtx *adk.ToolContext, args *schema.ToolArgument) (*ToolCallDecision, error) {
		assert.Equal(t, "WriteFile", tCtx.Name)
		assert.Equal(t, "call_allow", tCtx.CallID)
		assert.Equal(t, `{"path":"/etc/passwd"}`, args.Text)
		return &ToolCallDecision{Decision: Allow, UpdatedInput: `{"path":"/tmp/safe.txt"}`}, nil
	})

	var received string
	endpoint := adk.InvokableToolCallEndpoint(func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
		received = argumentsInJSON
		return "ok", nil
	})

	wrapped, err := m.WrapInvokableToolCall(context.Background(), endpoint, &adk.ToolContext{Name: "WriteFile", CallID: "call_allow"})
	require.NoError(t, err)

	result, err := wrapped(context.Background(), `{"path":"/etc/passwd"}`)
	require.NoError(t, err)
	assert.Equal(t, "ok", result)
	assert.Equal(t, `{"path":"/tmp/safe.txt"}`, received)
}

func TestWrapStreamableToolCall_Deny(t *testing.T) {
	m := NewTyped[*schema.Message](func(ctx context.Context, tCtx *adk.ToolContext, args *schema.ToolArgument) (*ToolCallDecision, error) {
		return &ToolCallDecision{Decision: Deny, Message: "blocked"}, nil
	})

	endpointCalled := false
	endpoint := adk.StreamableToolCallEndpoint(func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (*schema.StreamReader[string], error) {
		endpointCalled = true
		return schema.StreamReaderFromArray([]string{"unexpected"}), nil
	})

	wrapped, err := m.WrapStreamableToolCall(context.Background(), endpoint, &adk.ToolContext{Name: "Shell", CallID: "call_deny"})
	require.NoError(t, err)

	reader, err := wrapped(context.Background(), `{}`)
	require.NoError(t, err)
	require.NotNil(t, reader)
	assert.False(t, endpointCalled)

	chunk, err := reader.Recv()
	require.NoError(t, err)
	assert.Equal(t, "Permission denied for tool Shell: blocked", chunk)

	_, err = reader.Recv()
	assert.ErrorIs(t, err, io.EOF)
}

func TestPermissionGate_AskThenResumeApprovedWithUpdatedInput(t *testing.T) {
	m := NewTyped[*schema.Message](func(ctx context.Context, tCtx *adk.ToolContext, args *schema.ToolArgument) (*ToolCallDecision, error) {
		return &ToolCallDecision{Decision: Ask, Message: "approve write?"}, nil
	})

	tCtx := &adk.ToolContext{Name: "WriteFile", CallID: "call_ask"}
	ctx := withAddress(context.Background())

	result, err := m.permissionGate(ctx, tCtx, &schema.ToolArgument{Text: `{"path":"/etc/passwd"}`})
	assert.Nil(t, result)
	require.Error(t, err)

	var signal *core.InterruptSignal
	require.True(t, errors.As(err, &signal))
	require.NotNil(t, signal.InterruptState.State)

	askState, ok := signal.InterruptState.State.(*AskState)
	require.True(t, ok)
	require.NotNil(t, askState.Info)
	assert.Equal(t, "WriteFile", askState.Info.ToolName)
	assert.Equal(t, "call_ask", askState.Info.CallID)
	assert.Equal(t, `{"path":"/etc/passwd"}`, askState.Info.Arguments)

	resumeCtx := resumeContext(signal, &ResumeResponse{
		Approved:     true,
		UpdatedInput: `{"path":"/tmp/safe.txt"}`,
	})

	result, err = m.permissionGate(resumeCtx, tCtx, &schema.ToolArgument{Text: `{"path":"/etc/passwd"}`})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.allowed)
	assert.Equal(t, `{"path":"/tmp/safe.txt"}`, result.argument.Text)
}

func TestPermissionGate_AskThenResumeDenied(t *testing.T) {
	m := NewTyped[*schema.Message](func(ctx context.Context, tCtx *adk.ToolContext, args *schema.ToolArgument) (*ToolCallDecision, error) {
		return &ToolCallDecision{Decision: Ask, Message: "approve delete?"}, nil
	})

	tCtx := &adk.ToolContext{Name: "DeleteDB", CallID: "call_deny_resume"}
	_, err := m.permissionGate(withAddress(context.Background()), tCtx, &schema.ToolArgument{Text: `{}`})
	require.Error(t, err)

	var signal *core.InterruptSignal
	require.True(t, errors.As(err, &signal))

	result, err := m.permissionGate(resumeContext(signal, &ResumeResponse{
		Approved:    false,
		DenyMessage: "user rejected",
	}), tCtx, &schema.ToolArgument{Text: `{}`})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.allowed)
	assert.Equal(t, "Permission denied for tool DeleteDB: user rejected", result.denyResult)
}

func TestWrapEnhancedInvokableToolCall_Deny(t *testing.T) {
	m := NewTyped[*schema.Message](func(ctx context.Context, tCtx *adk.ToolContext, args *schema.ToolArgument) (*ToolCallDecision, error) {
		return &ToolCallDecision{Decision: Deny, Message: "enhanced blocked"}, nil
	})

	endpointCalled := false
	endpoint := adk.EnhancedInvokableToolCallEndpoint(func(ctx context.Context, argument *schema.ToolArgument, opts ...tool.Option) (*schema.ToolResult, error) {
		endpointCalled = true
		return nil, nil
	})

	wrapped, err := m.WrapEnhancedInvokableToolCall(context.Background(), endpoint, &adk.ToolContext{Name: "Enhanced", CallID: "call_enhanced"})
	require.NoError(t, err)

	result, err := wrapped(context.Background(), &schema.ToolArgument{Text: `{}`})
	require.NoError(t, err)
	assert.False(t, endpointCalled)
	require.NotNil(t, result)
	require.Len(t, result.Parts, 1)
	assert.Equal(t, schema.ToolPartTypeText, result.Parts[0].Type)
	assert.Equal(t, "Permission denied for tool Enhanced: enhanced blocked", result.Parts[0].Text)
}

func TestWrapEnhancedStreamableToolCall_AllowWithUpdatedInput(t *testing.T) {
	m := NewTyped[*schema.Message](func(ctx context.Context, tCtx *adk.ToolContext, args *schema.ToolArgument) (*ToolCallDecision, error) {
		return &ToolCallDecision{Decision: Allow, UpdatedInput: `{"safe":true}`}, nil
	})

	var received string
	endpoint := adk.EnhancedStreamableToolCallEndpoint(func(ctx context.Context, argument *schema.ToolArgument, opts ...tool.Option) (*schema.StreamReader[*schema.ToolResult], error) {
		received = argument.Text
		return schema.StreamReaderFromArray([]*schema.ToolResult{
			{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: "ok"}}},
		}), nil
	})

	wrapped, err := m.WrapEnhancedStreamableToolCall(context.Background(), endpoint, &adk.ToolContext{Name: "EnhancedStream", CallID: "call_stream"})
	require.NoError(t, err)

	reader, err := wrapped(context.Background(), &schema.ToolArgument{Text: `{"unsafe":true}`})
	require.NoError(t, err)
	require.NotNil(t, reader)
	assert.Equal(t, `{"safe":true}`, received)

	chunk, err := reader.Recv()
	require.NoError(t, err)
	require.Len(t, chunk.Parts, 1)
	assert.Equal(t, "ok", chunk.Parts[0].Text)
}

func withAddress(ctx context.Context) context.Context {
	return core.AppendAddressSegment(ctx, addressSegmentAgent, "test-agent", "")
}

func resumeContext(signal *core.InterruptSignal, response *ResumeResponse) context.Context {
	id2Addr, id2State := core.SignalToPersistenceMaps(signal)
	ctx := context.Background()
	ctx = core.PopulateInterruptState(ctx, id2Addr, id2State)
	ctx = core.BatchResumeWithData(ctx, map[string]any{signal.ID: response})
	return withAddress(ctx)
}
