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

func (at *agentTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	var input []Message
	if at.fullChatHistoryAsInput {
		history, err := getReactChatHistory(ctx, at.agent.Name(ctx))
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

	events := NewRunner(ctx, RunnerConfig{EnableStreaming: false}).Run(ctx, at.agent, input)
	var lastEvent *AgentEvent
	for {
		event, ok := events.Next()
		if !ok {
			break
		}

		if event.Err != nil {
			return "", event.Err
		}

		lastEvent = event
	}

	if lastEvent == nil {
		return "", errors.New("no event returned")
	}

	var ret string
	if lastEvent.Output != nil {
		if output := lastEvent.Output.MessageOutput; output != nil {
			msg, e := output.GetMessage()
			if e != nil {
				return "", e
			}

			ret = msg.Content
		}
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

func getReactChatHistory(ctx context.Context, destAgentName string) ([]Message, error) {
	var messages []Message
	var agentName string
	err := compose.ProcessState(ctx, func(ctx context.Context, st *State) error {
		messages = st.Messages
		agentName = st.AgentName
		return nil
	})

	messages = messages[:len(messages)-1] // remove the last assistant message, which is the tool call message
	history := make([]Message, 0, len(messages))
	history = append(history, messages...)
	a, t := GenTransferMessages(ctx, destAgentName)
	history = append(history, a, t)
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

func NewAgentTool(_ context.Context, agent Agent, options ...AgentToolOption) tool.BaseTool {
	opts := &AgentToolOptions{}
	for _, opt := range options {
		opt(opts)
	}

	return &agentTool{agent: agent, fullChatHistoryAsInput: opts.fullChatHistoryAsInput}
}
