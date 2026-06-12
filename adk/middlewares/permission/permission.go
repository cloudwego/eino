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
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/internal"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func init() {
	schema.RegisterName[*AskInfo]("_eino_adk_permission_ask_info")
	schema.RegisterName[*AskState]("_eino_adk_permission_ask_state")
	schema.RegisterName[*DecisionEvent]("_eino_adk_permission_decision_event")
}

// GateDecision is the result of a pre-execution permission check.
type GateDecision string

const (
	// GateAllow bypasses the permission UI and executes the tool call.
	GateAllow GateDecision = "allow"
	// GateDeny skips tool execution and uses Message as the denial reason
	// formatted through formatDenyResult.
	GateDeny GateDecision = "deny"
	// GateAsk interrupts the agent run for external approval.
	GateAsk GateDecision = "ask"
)

const (
	// SessionEventPermissionDecision records a valid user resume decision for a
	// previously interrupted permission ask.
	SessionEventPermissionDecision adk.SessionEventKind = adk.SessionEventKind(adk.SessionEventExtensionPrefix + "permission.decision")
)

// GateCheckResult determines how a tool call should proceed before execution.
type GateCheckResult struct {
	Decision GateDecision

	// Message is used as the deny reason or approval prompt.
	Message string

	// UpdatedInput replaces ToolArgument.Text when the tool is allowed.
	// Non-empty values are treated as replacements for backward compatibility.
	UpdatedInput string
	// HasUpdatedInput allows UpdatedInput to intentionally replace arguments with
	// an empty string.
	HasUpdatedInput bool

	// Reason is optional user-defined metadata for logging or auditing.
	Reason string
}

// Checker evaluates whether a tool call should be gated before execution.
//
// Returning an error signals an infrastructure failure and aborts the agent loop.
// Permission rejections should return GateDeny instead. Remembered preferences
// such as "always allow this action" should return GateAllow.
type Checker func(ctx context.Context, tCtx *adk.ToolContext, args *schema.ToolArgument) (*GateCheckResult, error)

// AskInfo is the user-facing interrupt payload emitted for Ask decisions.
type AskInfo struct {
	ToolName string
	Summary  string `json:",omitempty"`
}

// AskState is the private persisted interrupt state used to resume Ask decisions.
type AskState struct {
	Info *AskInfo

	ToolName  string
	CallID    string
	Arguments string
}

// ResumeAction resolves a previously interrupted permission ask.
type ResumeAction string

const (
	// ResumeActionApprove executes the pending tool call.
	ResumeActionApprove ResumeAction = "approve"
	// ResumeActionReject rejects the pending tool call without execution.
	ResumeActionReject ResumeAction = "reject"
	// ResumeActionRespond returns alternate model-visible text without executing the tool.
	ResumeActionRespond ResumeAction = "respond"
)

// ResumeResponse is the data expected when resuming an Ask interrupt.
type ResumeResponse struct {
	Action ResumeAction

	// UpdatedInput replaces the original arguments when Action is ResumeActionApprove.
	// Non-empty values are treated as replacements for backward compatibility.
	UpdatedInput string
	// HasUpdatedInput allows UpdatedInput to intentionally replace arguments with
	// an empty string.
	HasUpdatedInput bool

	// Message is used as the rejection reason or model-visible response text.
	Message string
}

// DecisionEvent is the typed payload for SessionEventPermissionDecision.
// It intentionally omits the original saved tool arguments; only user-provided
// UpdatedInput is carried when it is part of an approval decision.
type DecisionEvent struct {
	Action          ResumeAction `json:"action"`
	ToolName        string       `json:"tool_name"`
	ToolUseID       string       `json:"tool_use_id,omitempty"`
	DecisionText    string       `json:"decision_text,omitempty"`
	UpdatedInput    string       `json:"updated_input,omitempty"`
	HasUpdatedInput bool         `json:"has_updated_input,omitempty"`
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

type normalizedResumeDecision struct {
	Action          ResumeAction
	UpdatedInput    string
	HasUpdatedInput bool
	DecisionText    string
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
		return nil, tool.StatefulInterrupt(ctx, savedState.publicInfo(), savedState)
	}

	if isTarget && hasData {
		if !hasState || savedState == nil {
			return nil, fmt.Errorf("permission: missing AskState for targeted resume of tool %q (call_id=%s)", tCtx.Name, tCtx.CallID)
		}
		if err := emitDecisionEvent[M](ctx, tCtx, savedState, response); err != nil {
			return nil, err
		}
		return handleResumeResponse(ctx, tCtx, &schema.ToolArgument{Text: savedState.Arguments}, response)
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
			"permission: checker returned nil GateCheckResult for tool %q (call_id=%s); "+
				"return a valid *GateCheckResult with Decision set to GateAllow, GateDeny, or GateAsk",
			tCtx.Name, tCtx.CallID)
	}

	switch decision.Decision {
	case GateAllow:
		return &gateResult{
			allowed:  true,
			argument: withUpdatedInput(argument, decision.UpdatedInput, decision.HasUpdatedInput || decision.UpdatedInput != ""),
		}, nil
	case GateDeny:
		return &gateResult{denyResult: formatDenyResult(tCtx.Name, decision.Message)}, nil
	case GateAsk:
		info := &AskInfo{
			ToolName: tCtx.Name,
			Summary:  publicSummary(decision.Message, tCtx.CallID, argument.Text),
		}
		state := &AskState{
			Info:      info,
			ToolName:  tCtx.Name,
			CallID:    tCtx.CallID,
			Arguments: argument.Text,
		}
		return nil, tool.StatefulInterrupt(ctx, info, state)
	case "":
		return nil, fmt.Errorf("permission: empty gate decision for tool %q (call_id=%s); expected allow, deny, or ask",
			tCtx.Name, tCtx.CallID)
	default:
		return nil, fmt.Errorf("permission: unknown gate decision %q for tool %q (call_id=%s); expected allow, deny, or ask",
			decision.Decision, tCtx.Name, tCtx.CallID)
	}
}

func (s *AskState) publicInfo() *AskInfo {
	if s == nil {
		return nil
	}
	if s.Info != nil {
		return s.Info
	}
	return &AskInfo{ToolName: s.ToolName}
}

func publicSummary(message, callID, arguments string) string {
	if message == "" {
		return ""
	}
	if callID != "" && strings.Contains(message, callID) {
		return ""
	}
	if arguments != "" && strings.Contains(message, arguments) {
		return ""
	}
	return message
}

func handleResumeResponse(
	ctx context.Context,
	tCtx *adk.ToolContext,
	argument *schema.ToolArgument,
	response *ResumeResponse,
) (*gateResult, error) {
	decision, err := normalizeResumeDecision(tCtx, response)
	if err != nil {
		return nil, err
	}

	switch decision.Action {
	case ResumeActionApprove:
		return &gateResult{
			allowed:  true,
			argument: withUpdatedInput(argument, decision.UpdatedInput, decision.HasUpdatedInput),
		}, nil
	case ResumeActionReject:
		return &gateResult{denyResult: formatDenyResult(tCtx.Name, decision.DecisionText)}, nil
	case ResumeActionRespond:
		return &gateResult{denyResult: formatRespondResult(tCtx.Name, decision.DecisionText)}, nil
	default:
		return nil, fmt.Errorf("permission: unknown resume action %q for tool %q (call_id=%s); expected approve, reject, or respond",
			decision.Action, tCtx.Name, tCtx.CallID)
	}
}

func normalizeResumeDecision(tCtx *adk.ToolContext, response *ResumeResponse) (*normalizedResumeDecision, error) {
	toolName, callID := "", ""
	if tCtx != nil {
		toolName = tCtx.Name
		callID = tCtx.CallID
	}
	if response == nil {
		return nil, fmt.Errorf("permission: nil ResumeResponse for tool %q (call_id=%s)", toolName, callID)
	}

	decision := &normalizedResumeDecision{Action: response.Action}
	switch response.Action {
	case ResumeActionApprove:
		decision.HasUpdatedInput = response.HasUpdatedInput || response.UpdatedInput != ""
		if decision.HasUpdatedInput {
			decision.UpdatedInput = response.UpdatedInput
		}
		return decision, nil
	case ResumeActionReject:
		decision.DecisionText = response.Message
		if decision.DecisionText == "" {
			decision.DecisionText = "rejected by user"
		}
		return decision, nil
	case ResumeActionRespond:
		if response.Message == "" {
			return nil, fmt.Errorf("permission: empty response message for respond action on tool %q (call_id=%s)",
				toolName, callID)
		}
		decision.DecisionText = response.Message
		return decision, nil
	case "":
		return nil, fmt.Errorf("permission: empty resume action for tool %q (call_id=%s); expected approve, reject, or respond",
			toolName, callID)
	default:
		return nil, fmt.Errorf("permission: unknown resume action %q for tool %q (call_id=%s); expected approve, reject, or respond",
			response.Action, toolName, callID)
	}
}

func emitDecisionEvent[M adk.MessageType](ctx context.Context, tCtx *adk.ToolContext, state *AskState, response *ResumeResponse) error {
	if tCtx == nil {
		return fmt.Errorf("permission: nil ToolContext for resume decision event")
	}
	if state == nil {
		return fmt.Errorf("permission: nil AskState for resume decision event on tool %q (call_id=%s)", tCtx.Name, tCtx.CallID)
	}
	decision, err := normalizeResumeDecision(tCtx, response)
	if err != nil {
		return err
	}
	payload := &DecisionEvent{
		Action:          decision.Action,
		ToolName:        state.ToolName,
		ToolUseID:       state.CallID,
		DecisionText:    decision.DecisionText,
		UpdatedInput:    decision.UpdatedInput,
		HasUpdatedInput: decision.HasUpdatedInput,
	}
	return adk.TypedSendEvent[M](ctx, &adk.TypedAgentEvent[M]{
		SessionEvent: &adk.SessionEvent[M]{
			Kind:      SessionEventPermissionDecision,
			Extension: &adk.SessionExtensionEvent{Data: payload},
		},
	})
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

func withUpdatedInput(argument *schema.ToolArgument, updatedInput string, hasUpdatedInput bool) *schema.ToolArgument {
	if !hasUpdatedInput {
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

func formatRespondResult(toolName, message string) string {
	tpl := internal.SelectPrompt(internal.I18nPrompts{
		English: "Tool %s was not executed. User response: %s",
		Chinese: "工具 %s 未执行。用户回复: %s",
	})
	return fmt.Sprintf(tpl, toolName, message)
}
