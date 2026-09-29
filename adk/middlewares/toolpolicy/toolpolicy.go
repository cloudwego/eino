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

// Package toolpolicy provides a standard ADK middleware for tool-call policy
// control and human approval.
//
// A Policy decides for every tool call whether it may run (optionally with
// rewritten arguments), is denied (the model receives a safe tool result
// instead), or requires human approval. Approval requests ride on ADK's
// existing interrupt/resume machinery: the tool call interrupts with the
// policy's ApprovalInfo, and the approver resumes the run with an *Approval
// as resume data. No second checkpoint or approval storage mechanism is
// introduced; decision auditing can be layered on through the existing
// callback handlers.
//
// Usage:
//
//	mw, err := toolpolicy.New(ctx, &toolpolicy.Config{Policy: myPolicy})
//	if err != nil { ... }
//	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
//	    ...
//	    Handlers: []adk.ChatModelAgentMiddleware{mw},
//	})
//
//	resume with:
//	runner.ResumeWithParams(ctx, checkPointID, &adk.ResumeParams{
//	    Targets: map[string]any{interruptID: &toolpolicy.Approval{Approved: true}},
//	})
package toolpolicy

import (
	"context"
	"errors"
	"fmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// Decision is the policy verdict for a single tool call.
type Decision string

const (
	// DecisionAllow lets the tool call execute, optionally with rewritten arguments.
	DecisionAllow Decision = "allow"
	// DecisionDeny blocks the tool call; the model receives Result.Message as the tool result.
	DecisionDeny Decision = "deny"
	// DecisionRequireApproval interrupts the run until a human approves or rejects the call.
	DecisionRequireApproval Decision = "require_approval"
)

// Request describes a single tool call awaiting a policy decision.
type Request struct {
	// ToolName is the name of the tool being called.
	ToolName string
	// CallID is the unique identifier of this specific tool call.
	CallID string
	// Arguments is the raw JSON argument string produced by the model.
	Arguments string
}

// Result is the policy verdict for one tool call.
type Result struct {
	// Decision tells the middleware what to do with the call.
	Decision Decision

	// RewrittenArguments optionally replaces the arguments before execution.
	// It applies to DecisionAllow, and to an approval grant (see Approval).
	// Empty keeps the original arguments. Must be valid tool arguments.
	RewrittenArguments string

	// Message is returned to the model as the tool result when the call is
	// denied. Empty falls back to a default denial message containing the
	// tool name.
	Message string

	// ApprovalInfo is surfaced to the user through the interrupt event when
	// Decision is DecisionRequireApproval, so approvers can see what they are
	// approving. It is persisted in the checkpoint and therefore must be
	// gob-serializable (register custom types with schema.RegisterName).
	ApprovalInfo any
}

// Policy decides whether a tool call may proceed.
// Implementations must be safe for concurrent use: an agent may execute
// several tool calls in parallel.
type Policy interface {
	Decide(ctx context.Context, req *Request) (*Result, error)
}

// PolicyFunc adapts an ordinary function to a Policy.
type PolicyFunc func(ctx context.Context, req *Request) (*Result, error)

// Decide implements Policy.
func (f PolicyFunc) Decide(ctx context.Context, req *Request) (*Result, error) {
	return f(ctx, req)
}

// Approval is the resume data the middleware expects for an approval interrupt.
// Pass it as the interrupt target's value when resuming, e.g. via
// Runner.ResumeWithParams: Targets: map[string]any{interruptID: &toolpolicy.Approval{...}}.
type Approval struct {
	// Approved executes the tool call; otherwise the call is denied.
	Approved bool
	// Message optionally overrides the denial message returned to the model
	// when Approved is false.
	Message string
	// RewrittenArguments optionally replaces the call arguments before
	// execution when Approved is true. Empty keeps the original arguments.
	RewrittenArguments string
}

// approvalState is persisted in the checkpoint for a pending approval so that
// re-interrupts (when a sibling tool call is resumed instead) carry the same
// user-facing info, and so the original arguments survive the round trip.
type approvalState struct {
	Info      any
	Arguments string
}

func init() {
	schema.RegisterName[*approvalState]("_eino_adk_toolpolicy_approval_state")
	schema.RegisterName[*Approval]("_eino_adk_toolpolicy_approval")
}

// Config configures the tool policy middleware.
type Config struct {
	// Policy is required; the middleware fails closed (returns an error for
	// every wrapped tool call) if it is nil.
	Policy Policy
}

// NewTyped creates a new generic tool policy middleware.
func NewTyped[M adk.MessageType](_ context.Context, cfg *Config) (adk.TypedChatModelAgentMiddleware[M], error) {
	if cfg == nil || cfg.Policy == nil {
		return nil, errors.New("toolpolicy: Policy is required")
	}
	return &typedMiddleware[M]{
		TypedBaseChatModelAgentMiddleware: &adk.TypedBaseChatModelAgentMiddleware[M]{},
		policy:                            cfg.Policy,
	}, nil
}

// New creates a new tool policy middleware for *schema.Message agents.
func New(ctx context.Context, cfg *Config) (adk.ChatModelAgentMiddleware, error) {
	return NewTyped[*schema.Message](ctx, cfg)
}

type typedMiddleware[M adk.MessageType] struct {
	*adk.TypedBaseChatModelAgentMiddleware[M]
	policy Policy
}

// WrapInvokableToolCall implements adk.TypedChatModelAgentMiddleware.
func (m *typedMiddleware[M]) WrapInvokableToolCall(_ context.Context, endpoint adk.InvokableToolCallEndpoint, tCtx *adk.ToolContext) (adk.InvokableToolCallEndpoint, error) {
	return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
		action, err := m.gate(ctx, tCtx, argumentsInJSON)
		if err != nil {
			return "", err
		}
		if action.execute {
			return endpoint(ctx, action.arguments, opts...)
		}
		return action.denyMessage, nil
	}, nil
}

// WrapStreamableToolCall implements adk.TypedChatModelAgentMiddleware.
func (m *typedMiddleware[M]) WrapStreamableToolCall(_ context.Context, endpoint adk.StreamableToolCallEndpoint, tCtx *adk.ToolContext) (adk.StreamableToolCallEndpoint, error) {
	return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (*schema.StreamReader[string], error) {
		action, err := m.gate(ctx, tCtx, argumentsInJSON)
		if err != nil {
			return nil, err
		}
		if action.execute {
			return endpoint(ctx, action.arguments, opts...)
		}
		return schema.StreamReaderFromArray([]string{action.denyMessage}), nil
	}, nil
}

// WrapEnhancedInvokableToolCall implements adk.TypedChatModelAgentMiddleware.
func (m *typedMiddleware[M]) WrapEnhancedInvokableToolCall(_ context.Context, endpoint adk.EnhancedInvokableToolCallEndpoint, tCtx *adk.ToolContext) (adk.EnhancedInvokableToolCallEndpoint, error) {
	return func(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.ToolResult, error) {
		action, err := m.gate(ctx, tCtx, toolArgumentText(toolArgument))
		if err != nil {
			return nil, err
		}
		if action.execute {
			return endpoint(ctx, &schema.ToolArgument{Text: action.arguments}, opts...)
		}
		return textToolResult(action.denyMessage), nil
	}, nil
}

// WrapEnhancedStreamableToolCall implements adk.TypedChatModelAgentMiddleware.
func (m *typedMiddleware[M]) WrapEnhancedStreamableToolCall(_ context.Context, endpoint adk.EnhancedStreamableToolCallEndpoint, tCtx *adk.ToolContext) (adk.EnhancedStreamableToolCallEndpoint, error) {
	return func(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.StreamReader[*schema.ToolResult], error) {
		action, err := m.gate(ctx, tCtx, toolArgumentText(toolArgument))
		if err != nil {
			return nil, err
		}
		if action.execute {
			return endpoint(ctx, &schema.ToolArgument{Text: action.arguments}, opts...)
		}
		return schema.StreamReaderFromArray([]*schema.ToolResult{textToolResult(action.denyMessage)}), nil
	}, nil
}

// gateAction is the outcome of the policy gate for one tool call: either
// execute the endpoint with the given arguments, or short-circuit with a
// denial message as the tool result.
type gateAction struct {
	execute     bool
	arguments   string
	denyMessage string
}

// gate evaluates the policy for one tool call, handling the first-run,
// pending-approval, and resumed-approval paths.
func (m *typedMiddleware[M]) gate(ctx context.Context, tCtx *adk.ToolContext, argumentsInJSON string) (gateAction, error) {
	wasInterrupted, hasState, state := tool.GetInterruptState[*approvalState](ctx)
	if wasInterrupted {
		return resumeGate(ctx, tCtx, hasState, state)
	}

	result, err := m.policy.Decide(ctx, &Request{
		ToolName:  tCtx.Name,
		CallID:    tCtx.CallID,
		Arguments: argumentsInJSON,
	})
	if err != nil {
		return gateAction{}, fmt.Errorf("toolpolicy: policy decide failed for tool '%s': %w", tCtx.Name, err)
	}
	if result == nil {
		return gateAction{}, fmt.Errorf("toolpolicy: policy returned nil result for tool '%s'", tCtx.Name)
	}

	switch result.Decision {
	case DecisionAllow, "":
		return gateAction{execute: true, arguments: orDefault(result.RewrittenArguments, argumentsInJSON)}, nil
	case DecisionDeny:
		return gateAction{denyMessage: denialMessage(tCtx.Name, result.Message)}, nil
	case DecisionRequireApproval:
		st := &approvalState{Info: result.ApprovalInfo, Arguments: argumentsInJSON}
		return gateAction{}, tool.StatefulInterrupt(ctx, result.ApprovalInfo, st)
	default:
		return gateAction{}, fmt.Errorf("toolpolicy: unknown decision '%s' for tool '%s'", result.Decision, tCtx.Name)
	}
}

// resumeGate handles a tool call that was previously interrupted for approval.
func resumeGate(ctx context.Context, tCtx *adk.ToolContext, hasState bool, state *approvalState) (gateAction, error) {
	if !hasState || state == nil {
		return gateAction{}, fmt.Errorf("toolpolicy: approval interrupt state missing for tool '%s'", tCtx.Name)
	}

	isTarget, hasData, approval := tool.GetResumeContext[*Approval](ctx)
	if !isTarget {
		// A sibling interrupt was resumed instead; this call still waits for
		// its own approval, so interrupt again with the same info.
		return gateAction{}, tool.StatefulInterrupt(ctx, state.Info, state)
	}
	if !hasData || approval == nil {
		return gateAction{}, fmt.Errorf("toolpolicy: approval for tool '%s' requires resume data of type *toolpolicy.Approval", tCtx.Name)
	}

	if approval.Approved {
		return gateAction{execute: true, arguments: orDefault(approval.RewrittenArguments, state.Arguments)}, nil
	}
	return gateAction{denyMessage: denialMessage(tCtx.Name, approval.Message)}, nil
}

func denialMessage(toolName, custom string) string {
	if custom != "" {
		return custom
	}
	return fmt.Sprintf("Tool call '%s' was denied by policy.", toolName)
}

func orDefault(rewritten, original string) string {
	if rewritten != "" {
		return rewritten
	}
	return original
}

func toolArgumentText(arg *schema.ToolArgument) string {
	if arg == nil {
		return ""
	}
	return arg.Text
}

func textToolResult(text string) *schema.ToolResult {
	return &schema.ToolResult{
		Parts: []schema.ToolOutputPart{
			{Type: schema.ToolPartTypeText, Text: text},
		},
	}
}
