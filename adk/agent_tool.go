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

	"github.com/bytedance/sonic"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
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
}

type AgentToolOption func(*AgentToolOptions)

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

func withAgentToolEnableStreaming(enabled bool) tool.Option {
	return tool.WrapImplSpecificOptFn(func(opt *agentToolOptions) {
		opt.enableStreaming = enabled
	})
}

// NewAgentTool creates a tool that wraps an agent for invocation.
func NewAgentTool(_ context.Context, agent Agent, options ...AgentToolOption) tool.BaseTool {
	opts := &AgentToolOptions{}
	for _, opt := range options {
		opt(opts)
	}

	return &agentTool{
		agent:                  agent,
		fullChatHistoryAsInput: opts.fullChatHistoryAsInput,
		inputSchema:            opts.agentInputSchema,
	}
}

type agentTool struct {
	agent Agent

	fullChatHistoryAsInput bool
	inputSchema            *schema.ParamsOneOf
}

func (at *agentTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	param := at.inputSchema
	if param == nil {
		param = defaultAgentToolParam
	}

	return &schema.ToolInfo{
		Name:        at.agent.Name(ctx),
		Desc:        at.agent.Description(ctx),
		ParamsOneOf: param,
	}, nil
}

func (at *agentTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	gen, enableStreaming := getEmitGeneratorAndEnableStreaming(opts)
	var ms *bridgeStore
	var iter *AsyncIterator[*AgentEvent]
	var err error

	wasInterrupted, hasState, state := compose.GetInterruptState[[]byte](ctx)
	if !wasInterrupted {
		ms = newBridgeStore()
		var input []Message
		if at.fullChatHistoryAsInput {
			input, err = getReactChatHistory(ctx, at.agent.Name(ctx))
			if err != nil {
				return "", err
			}
		} else {
			if at.inputSchema == nil {
				// default input schema
				type request struct {
					Request string `json:"request"`
				}

				req := &request{}
				err = sonic.UnmarshalString(argumentsInJSON, req)
				if err != nil {
					return "", err
				}
				argumentsInJSON = req.Request
			}
			input = []Message{
				schema.UserMessage(argumentsInJSON),
			}
		}

		iter = newInvokableAgentToolRunner(at.agent, ms, enableStreaming).Run(ctx, input,
			append(getOptionsByAgentName(at.agent.Name(ctx), opts), WithCheckPointID(bridgeCheckpointID), withSharedParentSession())...)
	} else {
		if !hasState {
			return "", fmt.Errorf("agent tool '%s' interrupt has happened, but cannot find interrupt state", at.agent.Name(ctx))
		}

		ms = newResumeBridgeStore(state)

		iter, err = newInvokableAgentToolRunner(at.agent, ms, enableStreaming).
			Resume(ctx, bridgeCheckpointID, append(getOptionsByAgentName(at.agent.Name(ctx), opts), withSharedParentSession())...)
		if err != nil {
			return "", err
		}
	}

	var lastEvent *AgentEvent
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
				tmp := copyAgentEvent(event)
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

		return "", compose.CompositeInterrupt(ctx, "agent tool interrupt", data,
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
			ret = msg.Content
		}
	}

	return ret, nil
}

// agentToolOptions is a wrapper structure used to convert AgentRunOption slices to tool.Option.
// It stores the agent name and corresponding run options for tool-specific processing.
type agentToolOptions struct {
	agentName       string
	opts            []AgentRunOption
	generator       *AsyncGenerator[*AgentEvent]
	enableStreaming bool
}

func withAgentToolOptions(agentName string, opts []AgentRunOption) tool.Option {
	return tool.WrapImplSpecificOptFn(func(opt *agentToolOptions) {
		opt.agentName = agentName
		opt.opts = opts
	})
}

func withAgentToolEventGenerator(gen *AsyncGenerator[*AgentEvent]) tool.Option {
	return tool.WrapImplSpecificOptFn(func(o *agentToolOptions) {
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

func getEmitGeneratorAndEnableStreaming(opts []tool.Option) (*AsyncGenerator[*AgentEvent], bool) {
	o := tool.GetImplSpecificOptions[agentToolOptions](nil, opts...)
	if o == nil {
		return nil, false
	}

	return o.generator, o.enableStreaming
}

func getReactChatHistory(ctx context.Context, destAgentName string) ([]Message, error) {
	var messages []Message
	var agentName string
	err := compose.ProcessState(ctx, func(ctx context.Context, st *State) error {
		messages = make([]Message, len(st.Messages)-1)
		copy(messages, st.Messages[:len(st.Messages)-1]) // remove the last assistant message, which is the tool call message
		agentName = st.AgentName
		return nil
	})

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

	return history, err
}

func newInvokableAgentToolRunner(agent Agent, store compose.CheckPointStore, enableStreaming bool) *Runner {
	return &Runner{
		a:               agent,
		enableStreaming: enableStreaming,
		store:           store,
	}
}
