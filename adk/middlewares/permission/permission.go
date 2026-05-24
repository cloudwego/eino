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

// Package permission provides a ChatModelAgentMiddleware that gates tool execution
// behind a user-defined permission check.
package permission

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/internal"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func init() {
	schema.RegisterName[*AskInfo]("_eino_adk_permission_ask_info")
	schema.RegisterName[*AskState]("_eino_adk_permission_ask_state")
}

// Decision is the result of a permission check.
type Decision string

const (
	// Allow executes the tool call.
	Allow Decision = "allow"
	// Deny skips tool execution and returns Message as the tool result.
	Deny Decision = "deny"
	// Ask interrupts the agent run for external approval.
	Ask Decision = "ask"
)

// ToolCallDecision determines how a tool call should proceed.
type ToolCallDecision struct {
	Decision Decision

	// Message is used as the deny reason or approval prompt.
	Message string

	// UpdatedInput replaces ToolArgument.Text when the tool is allowed.
	UpdatedInput string

	// Reason is optional user-defined metadata for logging or auditing.
	Reason string
}

// Checker evaluates a tool call before execution.
//
// Returning an error signals an infrastructure failure and aborts the agent loop.
// Permission rejections should return Decision: Deny instead.
type Checker func(ctx context.Context, tCtx *adk.ToolContext, args *schema.ToolArgument) (*ToolCallDecision, error)

// AskInfo is the user-facing interrupt payload emitted for Ask decisions.
type AskInfo struct {
	ToolName  string
	CallID    string
	Arguments string
	Message   string
}

// AskState is the persisted interrupt state used to re-interrupt non-targeted resumes.
type AskState struct {
	Info *AskInfo
}

// ResumeResponse is the data expected when resuming an Ask interrupt.
type ResumeResponse struct {
	Approved bool

	// UpdatedInput replaces the original arguments when Approved is true.
	UpdatedInput string

	// DenyMessage is returned as the tool result when Approved is false.
	DenyMessage string
}

// Middleware gates tool calls with a permission Checker.
type Middleware[M adk.MessageType] struct {
	*adk.TypedBaseChatModelAgentMiddleware[M]
	checker Checker
}

// NewTyped creates a typed permission middleware.
func NewTyped[M adk.MessageType](checker Checker) *Middleware[M] {
	return &Middleware[M]{
		TypedBaseChatModelAgentMiddleware: &adk.TypedBaseChatModelAgentMiddleware[M]{},
		checker:                           checker,
	}
}

// New creates a permission middleware for the default *schema.Message agent path.
func New(checker Checker) *Middleware[*schema.Message] {
	return NewTyped[*schema.Message](checker)
}

type gateResult struct {
	allowed    bool
	denyResult string
	argument   *schema.ToolArgument
}

func (m *Middleware[M]) permissionGate(
	ctx context.Context,
	tCtx *adk.ToolContext,
	argument *schema.ToolArgument,
) (*gateResult, error) {
	if argument == nil {
		argument = &schema.ToolArgument{}
	}

	wasInterrupted, hasState, savedState := tool.GetInterruptState[*AskState](ctx)
	isTarget, hasData, response := tool.GetResumeContext[*ResumeResponse](ctx)

	if wasInterrupted && !isTarget {
		if !hasState || savedState == nil {
			return nil, fmt.Errorf("permission: missing AskState for resumed tool %q (call_id=%s)", tCtx.Name, tCtx.CallID)
		}
		return nil, tool.StatefulInterrupt(ctx, savedState.Info, savedState)
	}

	if isTarget && hasData {
		if !response.Approved {
			return &gateResult{denyResult: formatDenyResult(tCtx.Name, response.DenyMessage)}, nil
		}
		return &gateResult{
			allowed:  true,
			argument: withUpdatedInput(argument, response.UpdatedInput),
		}, nil
	}

	if isTarget && !hasData {
		return nil, fmt.Errorf(
			"permission: tool %q (call_id=%s) was targeted for resume but received nil "+
				"or type-mismatched ResumeResponse; the caller must supply a *permission.ResumeResponse "+
				"via ResumeWithParams", tCtx.Name, tCtx.CallID)
	}

	if m.checker == nil {
		return nil, fmt.Errorf("permission: checker is nil for tool %q (call_id=%s)", tCtx.Name, tCtx.CallID)
	}

	decision, err := m.checker(ctx, tCtx, argument)
	if err != nil {
		return nil, fmt.Errorf(
			"permission: checker error for tool %q (call_id=%s, args=%s): %w",
			tCtx.Name, tCtx.CallID, argument.Text, err)
	}
	if decision == nil {
		return nil, fmt.Errorf(
			"permission: checker returned nil ToolCallDecision for tool %q (call_id=%s); "+
				"return a valid *ToolCallDecision with Decision set to Allow, Deny, or Ask",
			tCtx.Name, tCtx.CallID)
	}

	switch decision.Decision {
	case Allow:
		return &gateResult{
			allowed:  true,
			argument: withUpdatedInput(argument, decision.UpdatedInput),
		}, nil
	case Deny:
		return &gateResult{denyResult: formatDenyResult(tCtx.Name, decision.Message)}, nil
	case Ask:
		info := &AskInfo{
			ToolName:  tCtx.Name,
			CallID:    tCtx.CallID,
			Arguments: argument.Text,
			Message:   decision.Message,
		}
		state := &AskState{Info: info}
		return nil, tool.StatefulInterrupt(ctx, info, state)
	default:
		return &gateResult{denyResult: formatDenyResult(tCtx.Name,
			fmt.Sprintf("unknown permission decision %q; expected allow, deny, or ask", decision.Decision))}, nil
	}
}

func (m *Middleware[M]) WrapInvokableToolCall(
	_ context.Context,
	endpoint adk.InvokableToolCallEndpoint,
	tCtx *adk.ToolContext,
) (adk.InvokableToolCallEndpoint, error) {
	return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
		result, err := m.permissionGate(ctx, tCtx, &schema.ToolArgument{Text: argumentsInJSON})
		if err != nil {
			return "", err
		}
		if !result.allowed {
			return result.denyResult, nil
		}
		return endpoint(ctx, result.argument.Text, opts...)
	}, nil
}

func (m *Middleware[M]) WrapStreamableToolCall(
	_ context.Context,
	endpoint adk.StreamableToolCallEndpoint,
	tCtx *adk.ToolContext,
) (adk.StreamableToolCallEndpoint, error) {
	return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (*schema.StreamReader[string], error) {
		result, err := m.permissionGate(ctx, tCtx, &schema.ToolArgument{Text: argumentsInJSON})
		if err != nil {
			return nil, err
		}
		if !result.allowed {
			return schema.StreamReaderFromArray([]string{result.denyResult}), nil
		}
		return endpoint(ctx, result.argument.Text, opts...)
	}, nil
}

func (m *Middleware[M]) WrapEnhancedInvokableToolCall(
	_ context.Context,
	endpoint adk.EnhancedInvokableToolCallEndpoint,
	tCtx *adk.ToolContext,
) (adk.EnhancedInvokableToolCallEndpoint, error) {
	return func(ctx context.Context, argument *schema.ToolArgument, opts ...tool.Option) (*schema.ToolResult, error) {
		result, err := m.permissionGate(ctx, tCtx, argument)
		if err != nil {
			return nil, err
		}
		if !result.allowed {
			return denyToolResult(result.denyResult), nil
		}
		return endpoint(ctx, result.argument, opts...)
	}, nil
}

func (m *Middleware[M]) WrapEnhancedStreamableToolCall(
	_ context.Context,
	endpoint adk.EnhancedStreamableToolCallEndpoint,
	tCtx *adk.ToolContext,
) (adk.EnhancedStreamableToolCallEndpoint, error) {
	return func(ctx context.Context, argument *schema.ToolArgument, opts ...tool.Option) (*schema.StreamReader[*schema.ToolResult], error) {
		result, err := m.permissionGate(ctx, tCtx, argument)
		if err != nil {
			return nil, err
		}
		if !result.allowed {
			return schema.StreamReaderFromArray([]*schema.ToolResult{denyToolResult(result.denyResult)}), nil
		}
		return endpoint(ctx, result.argument, opts...)
	}, nil
}

func withUpdatedInput(argument *schema.ToolArgument, updatedInput string) *schema.ToolArgument {
	if updatedInput == "" {
		return argument
	}
	cloned := *argument
	cloned.Text = updatedInput
	return &cloned
}

func denyToolResult(denyMsg string) *schema.ToolResult {
	return &schema.ToolResult{
		Parts: []schema.ToolOutputPart{
			{Type: schema.ToolPartTypeText, Text: denyMsg},
		},
	}
}

func formatDenyResult(toolName, message string) string {
	tpl := internal.SelectPrompt(internal.I18nPrompts{
		English: "Permission denied for tool %s: %s",
		Chinese: "工具 %s 权限被拒绝: %s",
	})
	return fmt.Sprintf(tpl, toolName, message)
}
