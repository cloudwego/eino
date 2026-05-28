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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/internal/core"
	mockModel "github.com/cloudwego/eino/internal/mock/components/model"
	"github.com/cloudwego/eino/schema"
)

const addressSegmentAgent core.AddressSegmentType = "agent"

func TestNewTypedSupportsBothMessageTypes(t *testing.T) {
	checker := func(context.Context, *adk.ToolContext, *schema.ToolArgument) (*GateCheckResult, error) {
		return &GateCheckResult{Decision: GateAllow}, nil
	}

	var _ adk.ChatModelAgentMiddleware = New(checker)
	var _ adk.TypedChatModelAgentMiddleware[*schema.AgenticMessage] = NewTyped[*schema.AgenticMessage](checker)
}

func TestWrapInvokableToolCall_AllowWithUpdatedInput(t *testing.T) {
	m := NewTyped[*schema.Message](func(ctx context.Context, tCtx *adk.ToolContext, args *schema.ToolArgument) (*GateCheckResult, error) {
		assert.Equal(t, "WriteFile", tCtx.Name)
		assert.Equal(t, "call_allow", tCtx.CallID)
		assert.Equal(t, `{"path":"/etc/passwd"}`, args.Text)
		return &GateCheckResult{Decision: GateAllow, UpdatedInput: `{"path":"/tmp/safe.txt"}`}, nil
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

func TestWrapInvokableToolCall_AllowWithExplicitEmptyUpdatedInput(t *testing.T) {
	m := NewTyped[*schema.Message](func(ctx context.Context, tCtx *adk.ToolContext, args *schema.ToolArgument) (*GateCheckResult, error) {
		return &GateCheckResult{Decision: GateAllow, HasUpdatedInput: true}, nil
	})

	received := "not called"
	endpoint := adk.InvokableToolCallEndpoint(func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
		received = argumentsInJSON
		return "ok", nil
	})

	wrapped, err := m.WrapInvokableToolCall(context.Background(), endpoint, &adk.ToolContext{Name: "WriteFile", CallID: "call_empty_update"})
	require.NoError(t, err)

	result, err := wrapped(context.Background(), `{"path":"/tmp/file"}`)
	require.NoError(t, err)
	assert.Equal(t, "ok", result)
	assert.Empty(t, received)
}

func TestWrapStreamableToolCall_Deny(t *testing.T) {
	m := NewTyped[*schema.Message](func(ctx context.Context, tCtx *adk.ToolContext, args *schema.ToolArgument) (*GateCheckResult, error) {
		return &GateCheckResult{Decision: GateDeny, Message: "blocked"}, nil
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

func TestWrapInvokableToolCall_Respond(t *testing.T) {
	m := NewTyped[*schema.Message](func(ctx context.Context, tCtx *adk.ToolContext, args *schema.ToolArgument) (*GateCheckResult, error) {
		return &GateCheckResult{Decision: GateAsk, Message: "approve shell?"}, nil
	})

	endpointCalled := false
	endpoint := adk.InvokableToolCallEndpoint(func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
		endpointCalled = true
		return "unexpected", nil
	})

	tCtx := &adk.ToolContext{Name: "Shell", CallID: "call_standard_respond"}
	wrapped, err := m.WrapInvokableToolCall(context.Background(), endpoint, tCtx)
	require.NoError(t, err)

	_, err = wrapped(withAddress(context.Background()), `{"cmd":"rm -rf /"}`)
	require.Error(t, err)

	var signal *core.InterruptSignal
	require.True(t, errors.As(err, &signal))

	result, err := wrapped(resumeContext(signal, &ResumeResponse{
		Action:  ResumeActionRespond,
		Message: "Explain first.",
	}), `{"cmd":"rm -rf /"}`)
	require.NoError(t, err)
	assert.False(t, endpointCalled)
	assert.Equal(t, formatRespondResult(tCtx.Name, "Explain first."), result)
	assert.NotContains(t, result, "Permission denied")
}

func TestWrapInvokableToolCall_ResumeApproveUsesSavedInterruptedArguments(t *testing.T) {
	m := NewTyped[*schema.Message](func(ctx context.Context, tCtx *adk.ToolContext, args *schema.ToolArgument) (*GateCheckResult, error) {
		return &GateCheckResult{Decision: GateAsk, Message: "approve write?"}, nil
	})

	var received string
	endpoint := adk.InvokableToolCallEndpoint(func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
		received = argumentsInJSON
		return "ok", nil
	})

	tCtx := &adk.ToolContext{Name: "WriteFile", CallID: "call_saved_args"}
	wrapped, err := m.WrapInvokableToolCall(context.Background(), endpoint, tCtx)
	require.NoError(t, err)

	_, err = wrapped(withAddress(context.Background()), `{"path":"/tmp/approved"}`)
	require.Error(t, err)
	var signal *core.InterruptSignal
	require.True(t, errors.As(err, &signal))

	result, err := wrapped(resumeContext(signal, &ResumeResponse{Action: ResumeActionApprove}), `{"path":"/etc/passwd"}`)
	require.NoError(t, err)
	assert.Equal(t, "ok", result)
	assert.Equal(t, `{"path":"/tmp/approved"}`, received)
}

func TestWrapInvokableToolCall_ResumeApproveWithExplicitEmptyUpdatedInput(t *testing.T) {
	m := NewTyped[*schema.Message](func(ctx context.Context, tCtx *adk.ToolContext, args *schema.ToolArgument) (*GateCheckResult, error) {
		return &GateCheckResult{Decision: GateAsk, Message: "approve empty override?"}, nil
	})

	received := "not called"
	endpoint := adk.InvokableToolCallEndpoint(func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
		received = argumentsInJSON
		return "ok", nil
	})

	tCtx := &adk.ToolContext{Name: "WriteFile", CallID: "call_resume_empty_update"}
	wrapped, err := m.WrapInvokableToolCall(context.Background(), endpoint, tCtx)
	require.NoError(t, err)

	_, err = wrapped(withAddress(context.Background()), `{"path":"/tmp/approved"}`)
	require.Error(t, err)
	var signal *core.InterruptSignal
	require.True(t, errors.As(err, &signal))

	result, err := wrapped(resumeContext(signal, &ResumeResponse{Action: ResumeActionApprove, HasUpdatedInput: true}), `{"path":"/etc/passwd"}`)
	require.NoError(t, err)
	assert.Equal(t, "ok", result)
	assert.Empty(t, received)
}

func TestPermissionGate_AskThenResumeApprovedWithUpdatedInput(t *testing.T) {
	m := NewTyped[*schema.Message](func(ctx context.Context, tCtx *adk.ToolContext, args *schema.ToolArgument) (*GateCheckResult, error) {
		return &GateCheckResult{Decision: GateAsk, Message: "approve write?"}, nil
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
		Action:       ResumeActionApprove,
		UpdatedInput: `{"path":"/tmp/safe.txt"}`,
	})

	result, err = m.permissionGate(resumeCtx, tCtx, &schema.ToolArgument{Text: `{"path":"/etc/passwd"}`})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.allowed)
	assert.Equal(t, `{"path":"/tmp/safe.txt"}`, result.argument.Text)
}

func TestPermissionGate_AskThenResumeDenied(t *testing.T) {
	m := NewTyped[*schema.Message](func(ctx context.Context, tCtx *adk.ToolContext, args *schema.ToolArgument) (*GateCheckResult, error) {
		return &GateCheckResult{Decision: GateAsk, Message: "approve delete?"}, nil
	})

	tCtx := &adk.ToolContext{Name: "DeleteDB", CallID: "call_deny_resume"}
	_, err := m.permissionGate(withAddress(context.Background()), tCtx, &schema.ToolArgument{Text: `{}`})
	require.Error(t, err)

	var signal *core.InterruptSignal
	require.True(t, errors.As(err, &signal))

	result, err := m.permissionGate(resumeContext(signal, &ResumeResponse{
		Action:  ResumeActionReject,
		Message: "user rejected",
	}), tCtx, &schema.ToolArgument{Text: `{}`})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.allowed)
	assert.Equal(t, "Permission denied for tool DeleteDB: user rejected", result.denyResult)
}

func TestPermissionGate_AskThenResumeRespond(t *testing.T) {
	m := NewTyped[*schema.Message](func(ctx context.Context, tCtx *adk.ToolContext, args *schema.ToolArgument) (*GateCheckResult, error) {
		return &GateCheckResult{Decision: GateAsk, Message: "approve shell?"}, nil
	})

	tCtx := &adk.ToolContext{Name: "Shell", CallID: "call_respond"}
	_, err := m.permissionGate(withAddress(context.Background()), tCtx, &schema.ToolArgument{Text: `{"cmd":"rm -rf /"}`})
	require.Error(t, err)

	var signal *core.InterruptSignal
	require.True(t, errors.As(err, &signal))

	result, err := m.permissionGate(resumeContext(signal, &ResumeResponse{
		Action:  ResumeActionRespond,
		Message: "Please explain why this command is necessary first.",
	}), tCtx, &schema.ToolArgument{Text: `{"cmd":"rm -rf /"}`})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.allowed)
	assert.Equal(t, "Tool Shell was not executed. User response: Please explain why this command is necessary first.", result.denyResult)
	assert.NotContains(t, result.denyResult, "Permission denied")
}

func TestPermissionGate_ResumeRejectDoesNotExecute(t *testing.T) {
	m := NewTyped[*schema.Message](func(ctx context.Context, tCtx *adk.ToolContext, args *schema.ToolArgument) (*GateCheckResult, error) {
		return &GateCheckResult{Decision: GateAsk, Message: "approve delete?"}, nil
	})

	tCtx := &adk.ToolContext{Name: "DeleteDB", CallID: "call_reject_default"}
	_, err := m.permissionGate(withAddress(context.Background()), tCtx, &schema.ToolArgument{Text: `{}`})
	require.Error(t, err)

	var signal *core.InterruptSignal
	require.True(t, errors.As(err, &signal))

	result, err := m.permissionGate(resumeContext(signal, &ResumeResponse{
		Action: ResumeActionReject,
	}), tCtx, &schema.ToolArgument{Text: `{}`})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.allowed)
	assert.Equal(t, "Permission denied for tool DeleteDB: rejected by user", result.denyResult)
}

func TestPermissionGate_ResumeApproveWithUpdatedInput(t *testing.T) {
	m := NewTyped[*schema.Message](func(ctx context.Context, tCtx *adk.ToolContext, args *schema.ToolArgument) (*GateCheckResult, error) {
		return &GateCheckResult{Decision: GateAsk, Message: "sanitize?"}, nil
	})

	tCtx := &adk.ToolContext{Name: "WriteFile", CallID: "call_approve_update"}
	_, err := m.permissionGate(withAddress(context.Background()), tCtx, &schema.ToolArgument{Text: `{"path":"/etc/passwd"}`})
	require.Error(t, err)

	var signal *core.InterruptSignal
	require.True(t, errors.As(err, &signal))

	result, err := m.permissionGate(resumeContext(signal, &ResumeResponse{
		Action:       ResumeActionApprove,
		UpdatedInput: `{"path":"/tmp/safe.txt"}`,
	}), tCtx, &schema.ToolArgument{Text: `{"path":"/etc/passwd"}`})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.allowed)
	assert.Equal(t, `{"path":"/tmp/safe.txt"}`, result.argument.Text)
}

func TestPermissionGate_InvalidResumeAction(t *testing.T) {
	m := NewTyped[*schema.Message](func(ctx context.Context, tCtx *adk.ToolContext, args *schema.ToolArgument) (*GateCheckResult, error) {
		return &GateCheckResult{Decision: GateAsk, Message: "approve?"}, nil
	})

	tCtx := &adk.ToolContext{Name: "Shell", CallID: "call_invalid_resume"}
	_, err := m.permissionGate(withAddress(context.Background()), tCtx, &schema.ToolArgument{Text: `{}`})
	require.Error(t, err)

	var signal *core.InterruptSignal
	require.True(t, errors.As(err, &signal))

	result, err := m.permissionGate(resumeContext(signal, &ResumeResponse{}), tCtx, &schema.ToolArgument{Text: `{}`})
	assert.Nil(t, result)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty resume action")

	result, err = m.permissionGate(resumeContext(signal, &ResumeResponse{Action: ResumeAction("unknown")}), tCtx, &schema.ToolArgument{Text: `{}`})
	assert.Nil(t, result)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown resume action")

	result, err = m.permissionGate(resumeContext(signal, &ResumeResponse{Action: ResumeActionRespond}), tCtx, &schema.ToolArgument{Text: `{}`})
	assert.Nil(t, result)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty response message")
}

func TestPermissionGate_InvalidGateDecision(t *testing.T) {
	tCtx := &adk.ToolContext{Name: "Shell", CallID: "call_invalid_gate"}

	tests := []struct {
		name     string
		decision GateDecision
		wantErr  string
	}{
		{name: "empty", decision: "", wantErr: "empty gate decision"},
		{name: "unknown", decision: GateDecision("unknown"), wantErr: "unknown gate decision"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			m := NewTyped[*schema.Message](func(ctx context.Context, tCtx *adk.ToolContext, args *schema.ToolArgument) (*GateCheckResult, error) {
				return &GateCheckResult{Decision: tt.decision}, nil
			})

			result, err := m.permissionGate(context.Background(), tCtx, &schema.ToolArgument{Text: `{}`})
			assert.Nil(t, result)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestWrapEnhancedInvokableToolCall_Deny(t *testing.T) {
	m := NewTyped[*schema.Message](func(ctx context.Context, tCtx *adk.ToolContext, args *schema.ToolArgument) (*GateCheckResult, error) {
		return &GateCheckResult{Decision: GateDeny, Message: "enhanced blocked"}, nil
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
	m := NewTyped[*schema.Message](func(ctx context.Context, tCtx *adk.ToolContext, args *schema.ToolArgument) (*GateCheckResult, error) {
		return &GateCheckResult{Decision: GateAllow, UpdatedInput: `{"safe":true}`}, nil
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

func TestWrapEnhancedInvokableToolCall_Respond(t *testing.T) {
	m := NewTyped[*schema.Message](func(ctx context.Context, tCtx *adk.ToolContext, args *schema.ToolArgument) (*GateCheckResult, error) {
		return &GateCheckResult{Decision: GateAsk, Message: "approve enhanced?"}, nil
	})

	endpointCalled := false
	endpoint := adk.EnhancedInvokableToolCallEndpoint(func(ctx context.Context, argument *schema.ToolArgument, opts ...tool.Option) (*schema.ToolResult, error) {
		endpointCalled = true
		return nil, nil
	})

	tCtx := &adk.ToolContext{Name: "Enhanced", CallID: "call_enhanced_respond"}
	wrapped, err := m.WrapEnhancedInvokableToolCall(context.Background(), endpoint, tCtx)
	require.NoError(t, err)

	_, err = wrapped(withAddress(context.Background()), &schema.ToolArgument{Text: `{}`})
	require.Error(t, err)

	var signal *core.InterruptSignal
	require.True(t, errors.As(err, &signal))

	result, err := wrapped(resumeContext(signal, &ResumeResponse{
		Action:  ResumeActionRespond,
		Message: "Explain first.",
	}), &schema.ToolArgument{Text: `{}`})
	require.NoError(t, err)
	assert.False(t, endpointCalled)
	require.NotNil(t, result)
	require.Len(t, result.Parts, 1)
	assert.Equal(t, schema.ToolPartTypeText, result.Parts[0].Type)
	assert.Equal(t, formatRespondResult(tCtx.Name, "Explain first."), result.Parts[0].Text)
}

func TestWrapEnhancedStreamableToolCall_Respond(t *testing.T) {
	m := NewTyped[*schema.Message](func(ctx context.Context, tCtx *adk.ToolContext, args *schema.ToolArgument) (*GateCheckResult, error) {
		return &GateCheckResult{Decision: GateAsk, Message: "approve enhanced stream?"}, nil
	})

	endpointCalled := false
	endpoint := adk.EnhancedStreamableToolCallEndpoint(func(ctx context.Context, argument *schema.ToolArgument, opts ...tool.Option) (*schema.StreamReader[*schema.ToolResult], error) {
		endpointCalled = true
		return nil, nil
	})

	tCtx := &adk.ToolContext{Name: "EnhancedStream", CallID: "call_enhanced_stream_respond"}
	wrapped, err := m.WrapEnhancedStreamableToolCall(context.Background(), endpoint, tCtx)
	require.NoError(t, err)

	_, err = wrapped(withAddress(context.Background()), &schema.ToolArgument{Text: `{}`})
	require.Error(t, err)

	var signal *core.InterruptSignal
	require.True(t, errors.As(err, &signal))

	reader, err := wrapped(resumeContext(signal, &ResumeResponse{
		Action:  ResumeActionRespond,
		Message: "Use a safer approach.",
	}), &schema.ToolArgument{Text: `{}`})
	require.NoError(t, err)
	assert.False(t, endpointCalled)
	require.NotNil(t, reader)

	chunk, err := reader.Recv()
	require.NoError(t, err)
	require.Len(t, chunk.Parts, 1)
	assert.Equal(t, schema.ToolPartTypeText, chunk.Parts[0].Type)
	assert.Equal(t, formatRespondResult(tCtx.Name, "Use a safer approach."), chunk.Parts[0].Text)

	_, err = reader.Recv()
	assert.ErrorIs(t, err, io.EOF)
}

func TestRespondFormattingIsByteIdenticalAcrossResultTypes(t *testing.T) {
	want := formatRespondResult("ToolA", "continue without running")
	assert.True(t, strings.HasPrefix(want, "Tool ToolA was not executed. User response: "))
	assert.Equal(t, want, denyToolResult(want).Parts[0].Text)
}

func TestPermissionDecisionAppearsInToolUseTimeline(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)
	captureTool := &permissionCaptureTool{name: "permission_tool"}
	info, err := captureTool.Info(ctx)
	require.NoError(t, err)

	generateCount := 0
	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.Message, error) {
			generateCount++
			if generateCount == 1 {
				return schema.AssistantMessage("calling tool", []schema.ToolCall{
					{ID: "permission_call", Function: schema.FunctionCall{Name: info.Name, Arguments: `{"path":"/tmp/file"}`}},
				}), nil
			}
			return schema.AssistantMessage("done", nil), nil
		}).AnyTimes()
	cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

	checkerCalled := false
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "PermissionTimelineAgent",
		Instruction: "use tools",
		Model:       cm,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{captureTool},
			},
		},
		Handlers: []adk.ChatModelAgentMiddleware{
			New(func(ctx context.Context, tCtx *adk.ToolContext, args *schema.ToolArgument) (*GateCheckResult, error) {
				checkerCalled = true
				return &GateCheckResult{Decision: GateAllow}, nil
			}),
		},
	})
	require.NoError(t, err)

	// In v3 the EvaluatedPermission field is removed from ToolSpanMeta. The gate
	// decision is no longer surfaced on the tool span; for non-interrupted calls
	// (gate=allow here), the decision is implicit in the tool result message
	// content (a real tool invocation, not the deny prefix). We verify the
	// real tool received its arguments and a tool_call_end span with status=ok
	// was emitted.
	var (
		sawToolCallEndOK bool
	)
	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:         agent,
		SessionID:     "permission-timeline",
		SessionStore:  &permissionSessionStore{},
		SessionConfig: &adk.SessionConfig{EventFlushBatchSize: 1},
	})
	iter := runner.Query(ctx, "use the tool", adk.WithTimelineEvents())
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		require.NoError(t, event.Err)
		if event.SessionEvent == nil || event.SessionEvent.Span == nil || event.SessionEvent.Span.Tool == nil {
			continue
		}
		if event.SessionEvent.Kind == adk.SessionEventSpanToolCallEnd && event.SessionEvent.Span.Status == "ok" {
			sawToolCallEndOK = true
		}
	}

	assert.True(t, checkerCalled)
	assert.True(t, sawToolCallEndOK, "expected a tool_call_end span with status=ok for the allow path")
	assert.Equal(t, `{"path":"/tmp/file"}`, captureTool.received)
}

// TestToolSpan_PermissionDenyEmitsBothSpansOnSameRun verifies plan §4.5.1 #6:
// when the permission gate denies on first invocation (no interrupt), the
// tool wrapper emits a tool_call_start + tool_call_end pair on the SAME run.
// The end span carries Status="ok" with a populated ToolResultMessageEventID
// — the deny content is the tool result, not an error.
func TestToolSpan_PermissionDenyEmitsBothSpansOnSameRun(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)
	captureTool := &permissionCaptureTool{name: "denied_tool"}
	info, err := captureTool.Info(ctx)
	require.NoError(t, err)

	generateCount := 0
	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.Message, error) {
			generateCount++
			if generateCount == 1 {
				return schema.AssistantMessage("calling", []schema.ToolCall{
					{ID: "deny_call", Function: schema.FunctionCall{Name: info.Name, Arguments: `{"path":"/etc/passwd"}`}},
				}), nil
			}
			return schema.AssistantMessage("done", nil), nil
		}).AnyTimes()
	cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "PermissionDenyAgent",
		Instruction: "use tools",
		Model:       cm,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{captureTool},
			},
		},
		Handlers: []adk.ChatModelAgentMiddleware{
			New(func(ctx context.Context, tCtx *adk.ToolContext, args *schema.ToolArgument) (*GateCheckResult, error) {
				return &GateCheckResult{Decision: GateDeny, Message: "blocked"}, nil
			}),
		},
	})
	require.NoError(t, err)

	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:         agent,
		SessionID:     "permission-deny-span",
		SessionStore:  &permissionSessionStore{},
		SessionConfig: &adk.SessionConfig{EventFlushBatchSize: 1},
	})

	var (
		startSpanID  string
		startEventID string
		endSpan      *adk.SessionEvent[*schema.Message]
		startCount   int
		endCount     int
	)

	iter := runner.Query(ctx, "use the tool", adk.WithTimelineEvents())
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		require.NoError(t, event.Err)
		if event.SessionEvent == nil || event.SessionEvent.Span == nil || event.SessionEvent.Span.Tool == nil {
			continue
		}
		switch event.SessionEvent.Kind {
		case adk.SessionEventSpanToolCallStart:
			startCount++
			startSpanID = event.SessionEvent.Span.SpanID
			startEventID = event.SessionEvent.EventID
		case adk.SessionEventSpanToolCallEnd:
			endCount++
			endSpan = event.SessionEvent
		}
	}

	assert.Equal(t, 1, startCount, "expected exactly one tool_call_start span on the deny run")
	assert.Equal(t, 1, endCount, "expected exactly one tool_call_end span on the deny run")
	require.NotNil(t, endSpan)
	assert.Equal(t, startSpanID, endSpan.Span.SpanID, "end span shares SpanID with start span on the same run")
	assert.Equal(t, startEventID, endSpan.Span.Tool.ToolCallStartEventID, "end span links back to start via ToolCallStartEventID")
	assert.Equal(t, "ok", endSpan.Span.Status, "deny path produces a tool result (not an error); end span status is ok")
	assert.NotEmpty(t, endSpan.Span.Tool.ToolResultMessageEventID, "deny end span must carry the ToolResultMessageEventID")
	// The real tool must NOT have been invoked when the gate denies.
	assert.Empty(t, captureTool.received, "deny path must not invoke the underlying tool")
}

type permissionCaptureTool struct {
	name     string
	received string
}

func (t *permissionCaptureTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: t.name,
		Desc: "permission capture tool",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"path": {Type: schema.String, Desc: "path"},
		}),
	}, nil
}

func (t *permissionCaptureTool) InvokableRun(_ context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	t.received = argumentsInJSON
	return "ok", nil
}

type permissionSessionStore struct {
	events []adk.SessionEventPayload
}

func (s *permissionSessionStore) AppendEvents(_ context.Context, _ string, events []adk.SessionEventPayload) error {
	s.events = append(s.events, events...)
	return nil
}

func (s *permissionSessionStore) LoadEvents(_ context.Context, _ string, _ *adk.LoadEventsRequest) (*adk.LoadEventsResult, error) {
	return &adk.LoadEventsResult{Events: nil}, nil
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
