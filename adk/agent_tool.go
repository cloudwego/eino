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

// Package adk provides core agent development kit utilities and types.
package adk

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bytedance/sonic"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/internal/core"
	"github.com/cloudwego/eino/schema"
)

var (
	defaultAgentToolParam = schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
		"request": {
			Desc:     "request to be processed",
			Required: true,
			Type:     schema.String,
		},
	})
)

type AgentToolOptions struct {
	fullChatHistoryAsInput bool
	agentInputSchema       *schema.ParamsOneOf
	retryConfig            *AgentToolRetryConfig
}

type AgentToolOption func(*AgentToolOptions)

// AgentToolRetryConfig configures retries for an agent wrapped as a tool.
//
// MaxRetries is the number of additional attempts after the initial attempt.
// Retries start a fresh child-agent run and therefore should only be enabled
// for agents whose side effects are safe to repeat. Forwarded internal events
// from a failed attempt remain observable.
type AgentToolRetryConfig struct {
	MaxRetries int
	// IsRetryable decides whether an invocation error should be retried.
	// If nil, all errors except cancellation, deadline, and interrupt signals
	// are retryable.
	IsRetryable func(ctx context.Context, err error) bool
	// Backoff returns the delay before a retry. attempt is one-based.
	// If nil, retries start immediately.
	Backoff func(ctx context.Context, attempt int) time.Duration
}

// WithFullChatHistoryAsInput enables using the full chat history as input.
func WithFullChatHistoryAsInput() AgentToolOption {
	return func(options *AgentToolOptions) {
		options.fullChatHistoryAsInput = true
	}
}

// WithAgentInputSchema sets a custom input schema for the agent tool.
func WithAgentInputSchema(schema *schema.ParamsOneOf) AgentToolOption {
	return func(options *AgentToolOptions) {
		options.agentInputSchema = schema
	}
}

// WithAgentToolRetry enables whole-invocation retries for an agent tool.
//
// A nil config disables retries. Interrupt/resume invocations are never
// retried because they must preserve their existing checkpoint state.
func WithAgentToolRetry(config *AgentToolRetryConfig) AgentToolOption {
	return func(options *AgentToolOptions) {
		if config == nil {
			options.retryConfig = nil
			return
		}
		cloned := *config
		options.retryConfig = &cloned
	}
}

func withAgentToolEnableStreaming(enabled bool) tool.Option {
	return tool.WrapImplSpecificOptFn(func(opt *agentToolOptions) {
		opt.enableStreaming = enabled
	})
}

// NewAgentTool creates a tool that wraps an agent for invocation.
//
// The agent must have a non-empty Name and Description, as they are used as
// the tool's name and description respectively. This is validated when Info()
// is called during tool setup.
//
// Event Streaming:
// When EmitInternalEvents is enabled in ToolsConfig, the agent tool will emit AgentEvent
// from the inner agent to the parent agent's AsyncGenerator, allowing real-time streaming
// of the inner agent's output to the end-user via Runner.
//
// Note that these forwarded events are NOT recorded in the parent agent's runSession.
// They are only emitted to the end-user and have no effect on the parent agent's state
// or checkpoint. The only exception is Interrupted action, which is propagated via
// CompositeInterrupt to enable proper interrupt/resume across agent boundaries.
//
// Action Scoping:
// Actions emitted by the inner agent are scoped to the agent tool boundary:
//   - Interrupted: Propagated via CompositeInterrupt to allow proper interrupt/resume across boundaries
//   - Exit, TransferToAgent, BreakLoop: Ignored outside the agent tool; these actions only affect
//     the inner agent's execution and do not propagate to the parent agent
//
// This scoping ensures that nested agents cannot unexpectedly terminate or transfer control
// of their parent agent's execution flow.
func NewAgentTool(_ context.Context, agent Agent, options ...AgentToolOption) tool.BaseTool {
	opts := &AgentToolOptions{}
	for _, opt := range options {
		opt(opts)
	}

	return &agentTool{
		agent:                  agent,
		fullChatHistoryAsInput: opts.fullChatHistoryAsInput,
		inputSchema:            opts.agentInputSchema,
		retryConfig:            opts.retryConfig,
	}
}

// NewTypedAgentTool creates a new agent tool that wraps a TypedAgent as a tool.BaseTool.
func NewTypedAgentTool[M MessageType](_ context.Context, agent TypedAgent[M], options ...AgentToolOption) tool.BaseTool {
	opts := &AgentToolOptions{}
	for _, opt := range options {
		opt(opts)
	}

	return &typedAgentTool[M]{
		agent:                  agent,
		fullChatHistoryAsInput: opts.fullChatHistoryAsInput,
		inputSchema:            opts.agentInputSchema,
		retryConfig:            opts.retryConfig,
	}
}

type typedAgentTool[M MessageType] struct {
	agent TypedAgent[M]

	fullChatHistoryAsInput bool
	inputSchema            *schema.ParamsOneOf
	retryConfig            *AgentToolRetryConfig
}

type agentTool = typedAgentTool[*schema.Message]

type agentToolRequest struct {
	Request string `json:"request"`
}

func (at *typedAgentTool[M]) Info(ctx context.Context) (*schema.ToolInfo, error) {
	name := at.agent.Name(ctx)
	if name == "" {
		return nil, errors.New("agent tool requires a non-empty Name")
	}
	desc := at.agent.Description(ctx)
	if desc == "" {
		return nil, errors.New("agent tool requires a non-empty Description")
	}
	param := at.inputSchema
	if param == nil {
		param = defaultAgentToolParam
	}

	return &schema.ToolInfo{
		Name:        name,
		Desc:        desc,
		ParamsOneOf: param,
	}, nil
}

func (at *typedAgentTool[M]) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	if at.retryConfig == nil {
		return at.invokeOnce(ctx, argumentsInJSON, opts...)
	}
	if at.retryConfig.MaxRetries < 0 {
		return "", errors.New("agent tool retry MaxRetries must be non-negative")
	}

	wasInterrupted, _, _ := tool.GetInterruptState[[]byte](ctx)
	if wasInterrupted {
		return at.invokeOnce(ctx, argumentsInJSON, opts...)
	}

	for attempt := 0; ; attempt++ {
		result, err := at.invokeOnce(ctx, argumentsInJSON, opts...)
		if err == nil {
			return result, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		if attempt >= at.retryConfig.MaxRetries || !at.shouldRetry(ctx, err) {
			return "", err
		}
		if err = waitAgentToolRetry(ctx, at.retryConfig, attempt+1); err != nil {
			return "", err
		}
	}
}

func (at *typedAgentTool[M]) invokeOnce(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	if cancelCtx := getCancelContext(ctx); cancelCtx != nil {
		cancelCtx.markCheckpointAwareDescendant()
	}

	gen, enableStreaming := getEmitGeneratorAndEnableStreaming[M](opts)
	var ms *bridgeStore
	var iter *AsyncIterator[*TypedAgentEvent[M]]
	var err error

	wasInterrupted, hasState, state := tool.GetInterruptState[[]byte](ctx)
	if !wasInterrupted {
		ms = newBridgeStore()

		var input []M
		if at.fullChatHistoryAsInput {
			var zero M
			if _, ok := any(zero).(*schema.Message); !ok {
				// fullChatHistoryAsInput is only supported for *schema.Message agents and will not
				// be extended to *schema.AgenticMessage. The chat history format and role semantics
				// differ fundamentally between Message and AgenticMessage, and the history rewriting
				// logic (role attribution, system message filtering, transfer messages) is specific
				// to the Message model.
				return "", fmt.Errorf("fullChatHistoryAsInput is only supported for *schema.Message agents")
			}
			msgInput, histErr := getReactChatHistory(ctx, at.agent.Name(ctx))
			if histErr != nil {
				return "", histErr
			}
			input = any(msgInput).([]M)
		} else {
			if at.inputSchema == nil {
				req := &agentToolRequest{}
				err = sonic.UnmarshalString(argumentsInJSON, req)
				if err != nil {
					return "", err
				}
				argumentsInJSON = req.Request
			}
			input = newTypedUserMessages[M](argumentsInJSON)
		}

		runner := newTypedInvokableAgentToolRunner(at.agent, ms, enableStreaming)
		iter = runner.Run(ctx, input,
			append(extractAndDeriveAgentToolCancelCtx(ctx, at.agent.Name(ctx), opts), WithCheckPointID(bridgeCheckpointID), withSharedParentSession())...)
	} else {
		if !hasState {
			return "", fmt.Errorf("agent tool '%s' interrupt has happened, but cannot find interrupt state", at.agent.Name(ctx))
		}

		ms = newResumeBridgeStore(bridgeCheckpointID, state)

		agentOpts := extractAndDeriveAgentToolCancelCtx(ctx, at.agent.Name(ctx), opts)
		agentOpts = append(agentOpts, withSharedParentSession())

		runner := newTypedInvokableAgentToolRunner(at.agent, ms, enableStreaming)
		iter, err = runner.Resume(ctx, bridgeCheckpointID, agentOpts...)
		if err != nil {
			return "", err
		}
	}

	var lastEvent *TypedAgentEvent[M]
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}

		if lastEvent != nil &&
			lastEvent.Output != nil &&
			lastEvent.Output.MessageOutput != nil &&
			lastEvent.Output.MessageOutput.MessageStream != nil {
			lastEvent.Output.MessageOutput.MessageStream.Close()
		}

		if event.Err != nil {
			return "", event.Err
		}

		if gen != nil {
			if event.Action == nil || event.Action.Interrupted == nil {
				if parentRunCtx := getRunCtx(ctx); parentRunCtx != nil && len(parentRunCtx.RunPath) > 0 {
					rp := make([]RunStep, 0, len(parentRunCtx.RunPath)+len(event.RunPath))
					rp = append(rp, parentRunCtx.RunPath...)
					rp = append(rp, event.RunPath...)
					event.RunPath = rp
				}
				tmp := copyTypedAgentEvent(event)
				gen.Send(event)
				event = tmp
			}
		}

		lastEvent = event
	}

	if lastEvent != nil && lastEvent.Action != nil && lastEvent.Action.Interrupted != nil {
		data, existed, err_ := ms.Get(ctx, bridgeCheckpointID)
		if err_ != nil {
			return "", fmt.Errorf("failed to get interrupt info: %w", err_)
		}
		if !existed {
			return "", fmt.Errorf("interrupt has happened, but cannot find interrupt info")
		}

		return "", tool.CompositeInterrupt(ctx, "agent tool interrupt", data,
			lastEvent.Action.internalInterrupted)
	}

	if lastEvent == nil {
		return "", errors.New("no event returned")
	}

	var ret string
	if lastEvent.Output != nil {
		if output := lastEvent.Output.MessageOutput; output != nil {
			msg, err := output.GetMessage()
			if err != nil {
				return "", err
			}
			ret = extractTextContent(msg)
		}
	}

	return ret, nil
}

func (at *typedAgentTool[M]) shouldRetry(ctx context.Context, err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	var cancelErr *CancelError
	if errors.As(err, &cancelErr) {
		return false
	}
	var interruptSignal *core.InterruptSignal
	if errors.As(err, &interruptSignal) {
		return false
	}
	var interruptProvider core.InterruptContextsProvider
	if errors.As(err, &interruptProvider) {
		return false
	}
	return at.retryConfig.IsRetryable == nil || at.retryConfig.IsRetryable(ctx, err)
}

func waitAgentToolRetry(ctx context.Context, config *AgentToolRetryConfig, attempt int) error {
	if config.Backoff == nil {
		return nil
	}
	delay := config.Backoff(ctx, attempt)
	if delay <= 0 {
		return nil
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// agentToolOptions is a wrapper structure used to convert AgentRunOption slices to tool.Option.
// It stores the agent name and corresponding run options for tool-specific processing.
type agentToolOptions struct {
	agentName       string
	opts            []AgentRunOption
	enableStreaming bool
}

// typedAgentToolEventOptions carries the parent runner's event generator for a
// specific message type. This keeps forwarded internal events type-compatible
// with the parent event stream.
type typedAgentToolEventOptions[M MessageType] struct {
	generator *AsyncGenerator[*TypedAgentEvent[M]]
}

func withAgentToolOptions(agentName string, opts []AgentRunOption) tool.Option {
	return tool.WrapImplSpecificOptFn(func(opt *agentToolOptions) {
		opt.agentName = agentName
		opt.opts = opts
	})
}

func withAgentToolEventGenerator(gen *AsyncGenerator[*AgentEvent]) tool.Option {
	return withTypedAgentToolEventGenerator(gen)
}

func withTypedAgentToolEventGenerator[M MessageType](gen *AsyncGenerator[*TypedAgentEvent[M]]) tool.Option {
	return tool.WrapImplSpecificOptFn(func(o *typedAgentToolEventOptions[M]) {
		o.generator = gen
	})
}

func getOptionsByAgentName(agentName string, opts []tool.Option) []AgentRunOption {
	var ret []AgentRunOption
	for _, opt := range opts {
		o := tool.GetImplSpecificOptions[agentToolOptions](nil, opt)
		if o != nil && o.agentName == agentName {
			ret = append(ret, o.opts...)
		}
	}
	return ret
}

func extractAndDeriveAgentToolCancelCtx(ctx context.Context, agentName string, opts []tool.Option) []AgentRunOption {
	agentOpts := getOptionsByAgentName(agentName, opts)
	childCancelCtx := deriveCheckpointAwareSubAgentCancelContext(ctx, agentOpts)
	return appendCancelContextOption(agentOpts, childCancelCtx)
}

func getEmitGeneratorAndEnableStreaming[M MessageType](opts []tool.Option) (*AsyncGenerator[*TypedAgentEvent[M]], bool) {
	o := tool.GetImplSpecificOptions[agentToolOptions](nil, opts...)
	eventOptions := tool.GetImplSpecificOptions[typedAgentToolEventOptions[M]](nil, opts...)
	if o == nil && eventOptions == nil {
		return nil, false
	}

	var gen *AsyncGenerator[*TypedAgentEvent[M]]
	if eventOptions != nil {
		gen = eventOptions.generator
	}

	var enableStreaming bool
	if o != nil {
		enableStreaming = o.enableStreaming
	}

	return gen, enableStreaming
}

func getReactChatHistory(ctx context.Context, destAgentName string) ([]Message, error) {
	var messages []Message
	err := compose.ProcessState(ctx, func(ctx context.Context, st *State) error {
		if len(st.Messages) == 0 {
			return nil
		}
		messages = make([]Message, len(st.Messages)-1)
		copy(messages, st.Messages[:len(st.Messages)-1])
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get chat history from state: %w", err)
	}

	var agentName string
	if runCtx := getRunCtx(ctx); runCtx != nil && len(runCtx.RunPath) > 0 {
		agentName = runCtx.RunPath[len(runCtx.RunPath)-1].agentName
	}

	a, t := GenTransferMessages(ctx, destAgentName)
	messages = append(messages, a, t)
	history := make([]Message, 0, len(messages))
	for _, msg := range messages {
		if msg.Role == schema.System {
			continue
		}

		if msg.Role == schema.Assistant || msg.Role == schema.Tool {
			msg = rewriteMessage(msg, agentName)
		}

		history = append(history, msg)
	}

	return history, nil
}

func newTypedUserMessages[M MessageType](text string) []M {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		return any([]Message{schema.UserMessage(text)}).([]M)
	case *schema.AgenticMessage:
		return any([]*schema.AgenticMessage{schema.UserAgenticMessage(text)}).([]M)
	default:
		return nil
	}
}

func newTypedInvokableAgentToolRunner[M MessageType](agent TypedAgent[M], store compose.CheckPointStore, enableStreaming bool) *TypedRunner[M] {
	return &TypedRunner[M]{
		a:               agent,
		enableStreaming: enableStreaming,
		store:           store,
	}
}
