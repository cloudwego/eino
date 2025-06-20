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

type agentToolOptions struct {
	agentName string
	opts      []AgentRunOption
}

func withAgentToolOptions(agentName string, opts []AgentRunOption) tool.Option {
	return tool.WrapImplSpecificOptFn(func(opt *agentToolOptions) {
		opt.agentName = agentName
		opt.opts = opts
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

type agentTool struct {
	agent Agent

	fullChatHistoryAsInput bool
}

func (at *agentTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	var param *schema.ParamsOneOf
	if !at.fullChatHistoryAsInput {
		param = defaultAgentToolParam
	}

	return &schema.ToolInfo{
		Name:        at.agent.Name(ctx),
		Desc:        at.agent.Description(ctx),
		ParamsOneOf: param,
	}, nil
}

func genTransferMessages(agentName string) []Message {
	tooCall := schema.ToolCall{Function: schema.FunctionCall{Name: TransferToAgentToolName, Arguments: agentName}}

	assistantMessage := schema.AssistantMessage("", []schema.ToolCall{tooCall})
	toolMessage := schema.ToolMessage(transferToAgentToolOutput(agentName), "", schema.WithToolName(TransferToAgentToolName))

	return []Message{
		rewriteMessage(assistantMessage, agentName),
		rewriteMessage(toolMessage, agentName),
	}
}

func newInvokableAgentToolRunner(agent Agent, store CheckPointStore) *Runner {
	return &Runner{
		a:               agent,
		enableStreaming: false,
		store:           store,
	}
}

func (at *agentTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var intData *agentToolInterruptInfo
	var bResume bool
	err := compose.ProcessState(ctx, func(ctx context.Context, s *State) error {
		toolCallID := compose.GetToolCallID(ctx)
		intData, bResume = s.AgentToolInterruptData[toolCallID]
		if bResume {
			delete(s.AgentToolInterruptData, toolCallID)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("failed to access state: %w", err)
	}

	var ms *mockStore
	var iter *AsyncIterator[*AgentEvent]
	if bResume {
		ms = newResumeStore(intData.Data)

		iter, err = newInvokableAgentToolRunner(at.agent, ms).Resume(ctx, mockCheckPointID, getOptionsByAgentName(at.agent.Name(ctx), opts)...)
		if err != nil {
			return "", err
		}
	} else {
		ms = newEmptyStore()
		var input []Message
		if at.fullChatHistoryAsInput {
			history, err := getReactChatHistory(ctx)
			if err != nil {
				return "", err
			}

			input = history
		} else {
			type request struct {
				Request string `json:"request"`
			}

			req := &request{}
			err := sonic.UnmarshalString(argumentsInJSON, req)
			if err != nil {
				return "", err
			}
			input = []Message{
				schema.UserMessage(req.Request),
			}
		}

		iter = newInvokableAgentToolRunner(at.agent, ms).Run(ctx, input, append(getOptionsByAgentName(at.agent.Name(ctx), opts), WithCheckPointID(mockCheckPointID))...)
	}

	interrupted := false
	var lastEvent *AgentEvent
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}

		if event.Err != nil {
			return "", event.Err
		}

		if event.Action != nil && event.Action.Interrupted != nil {
			interrupted = true
		} else {
			lastEvent = event
		}
	}

	if interrupted {
		data, existed, err_ := ms.Get(ctx, mockCheckPointID)
		if err_ != nil {
			return "", fmt.Errorf("failed to get interrupt info: %w", err)
		}
		if !existed {
			return "", fmt.Errorf("interrupt has happened, but cannot find interrupt info")
		}
		err = compose.ProcessState(ctx, func(ctx context.Context, st *State) error {
			st.AgentToolInterruptData[compose.GetToolCallID(ctx)] = &agentToolInterruptInfo{
				LastEvent: lastEvent,
				Data:      data,
			}
			return nil
		})
		if err != nil {
			return "", fmt.Errorf("failed to save agent tool checkpoint to state: %w", err)
		}
		return "", compose.InterruptAndRerun
	}

	if lastEvent == nil {
		return "", errors.New("no event returned")
	}

	var ret string
	if output := lastEvent.GetModelOutput(); output != nil {
		msg, e := output.GetMessage()
		if e != nil {
			return "", e
		}

		ret = msg.Content
	}

	if output := lastEvent.GetToolCallOutput(); output != nil {
		msg, e := output.Response.GetMessage()
		if e != nil {
			return "", e
		}

		ret = msg.Content
	}

	return ret, nil
}

type AgentToolOptions struct {
	fullChatHistoryAsInput bool
}

type AgentToolOption func(*AgentToolOptions)

func WithFullChatHistoryAsInput() AgentToolOption {
	return func(options *AgentToolOptions) {
		options.fullChatHistoryAsInput = true
	}
}

func getReactChatHistory(ctx context.Context) ([]Message, error) {
	var messages []Message
	var agentName string
	err := compose.ProcessState(ctx, func(ctx context.Context, st *State) error {
		messages = st.Messages
		agentName = st.AgentName
		return nil
	})

	messages = messages[:len(messages)-1] // remove the last assistant message, which is the tool call message
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

	history = append(history, genTransferMessages(agentName)...)

	return history, err
}

func NewAgentTool(_ context.Context, agent Agent, options ...AgentToolOption) tool.BaseTool {
	opts := &AgentToolOptions{}
	for _, opt := range options {
		opt(opts)
	}

	return &agentTool{agent: agent, fullChatHistoryAsInput: opts.fullChatHistoryAsInput}
}
