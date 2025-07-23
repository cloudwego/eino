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

package prebuilt

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/internal/safe"
	"github.com/cloudwego/eino/schema"
)

type SupervisorConfig struct {
	Supervisor adk.Agent
	SubAgents  []adk.Agent
}

type BackToParentWrapper struct {
	adk.Agent

	parentAgentName string
}

func (a *BackToParentWrapper) Run(ctx context.Context, input *adk.AgentInput,
	opts ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {

	ctx = adk.ClearRunCtx(ctx)
	aIter := a.Agent.Run(ctx, input, opts...)

	iterator, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	go func() {
		defer func() {
			panicErr := recover()
			if panicErr != nil {
				e := safe.NewPanicErr(panicErr, debug.Stack())
				generator.Send(&adk.AgentEvent{Err: e})
			}

			generator.Close()
		}()

		var transferredBack bool
		for {
			event, ok := aIter.Next()
			if !ok {
				break
			}

			if event.Action != nil {
				if event.Action.Exit {
					generator.Send(&adk.AgentEvent{
						Err: errors.New("can only transfer back to parent. got EXIT"),
					})
					return
				}
				if event.Action.Interrupted != nil { // interrupted, just emit the event as the final event
					generator.Send(event)
					return
				}
				if event.Action.TransferToAgent != nil {
					if event.Action.TransferToAgent.DestAgentName == a.parentAgentName {
						transferredBack = true
					} else {
						generator.Send(&adk.AgentEvent{
							Err: fmt.Errorf("can only transfer back to parent, actual: %s", event.Action.TransferToAgent.DestAgentName),
						})
						return
					}
				}
			}

			generator.Send(event)

			if event.Err != nil {
				return
			}
		}

		if !transferredBack { // only transfer back to parent manually if haven't done it already
			aMsg, tMsg := adk.GenTransferMessages(ctx, a.parentAgentName)
			aEvent := adk.EventFromMessage(aMsg, nil, schema.Assistant, "")
			generator.Send(aEvent)
			tEvent := adk.EventFromMessage(tMsg, nil, schema.Tool, tMsg.ToolName)
			tEvent.Action = &adk.AgentAction{
				TransferToAgent: &adk.TransferToAgentAction{
					DestAgentName: a.parentAgentName,
				},
			}
			generator.Send(tEvent)
		}
	}()

	return iterator
}

func NewSupervisor(ctx context.Context, conf *SupervisorConfig) (adk.Agent, error) {
	subAgents := make([]adk.Agent, 0, len(conf.SubAgents))
	supervisorName := conf.Supervisor.Name(ctx)
	for _, subAgent := range conf.SubAgents {
		subAgents = append(subAgents, &BackToParentWrapper{
			Agent:           subAgent,
			parentAgentName: supervisorName,
		})
	}

	return adk.SetSubAgents(ctx, conf.Supervisor, subAgents)
}
