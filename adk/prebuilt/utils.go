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

	"github.com/cloudwego/eino/adk"
)

type setSessionKVsAgent struct {
	kvs map[string]any
}

func (s *setSessionKVsAgent) Name(_ context.Context) string {
	return "set_session_kvs"
}

func (s *setSessionKVsAgent) Description(_ context.Context) string {
	return "set session kvs"
}

func (s *setSessionKVsAgent) Run(ctx context.Context, _ *adk.AgentInput,
	_ ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {

	adk.SetSessionValues(ctx, s.kvs)
	iterator, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	generator.Close()

	return iterator
}

type outputSessionKVsAgent struct {
	adk.Agent
}

func (o *outputSessionKVsAgent) Run(ctx context.Context, input *adk.AgentInput,
	options ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {

	iterator, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()

	iterator_ := o.Agent.Run(ctx, input, options...)
	go func() {
		defer generator.Close()
		for {
			event, ok := iterator_.Next()
			if !ok {
				break
			}
			generator.Send(event)
		}

		kvs := adk.GetSessionValues(ctx)

		event := &adk.AgentEvent{
			Output: &adk.AgentOutput{CustomizedOutput: kvs},
		}
		generator.Send(event)
	}()

	return iterator
}

func AgentWithSessionKVs(ctx context.Context, agent adk.Agent,
	sessionKVs map[string]any) (adk.Agent, error) {

	s := &setSessionKVsAgent{
		kvs: sessionKVs,
	}

	return adk.NewSequentialAgent(ctx, &adk.SequentialAgentConfig{
		SubAgents: []adk.Agent{s, agent},
	})
}

func AgentOutputSessionKVs(ctx context.Context, agent adk.Agent) (adk.Agent, error) {
	return &outputSessionKVsAgent{Agent: agent}, nil
}
