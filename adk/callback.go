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

package adk

import (
	"context"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	icb "github.com/cloudwego/eino/internal/callbacks"
)

// AgentCallbackInput represents the input passed to agent callbacks during OnStart.
// Use ConvAgentCallbackInput to safely convert from callbacks.CallbackInput.
type AgentCallbackInput struct {
	// Input contains the agent input for a new run. Nil when resuming.
	Input *AgentInput
	// ResumeInfo contains resume information when resuming from an interrupt. Nil for new runs.
	ResumeInfo *ResumeInfo
}

// AgentCallbackOutput represents the output passed to agent callbacks during OnEnd.
// Use ConvAgentCallbackOutput to safely convert from callbacks.CallbackOutput.
//
// Important: The Events iterator should be consumed asynchronously to avoid blocking
// the agent execution. Each callback handler receives an independent copy of the iterator.
type AgentCallbackOutput struct {
	// Events provides a stream of agent events. Each handler receives its own copy.
	Events *AsyncIterator[*AgentEvent]
}

func copyEventIterator(iter *AsyncIterator[*AgentEvent], n int) []*AsyncIterator[*AgentEvent] {
	if n <= 0 {
		return nil
	}
	if n == 1 {
		return []*AsyncIterator[*AgentEvent]{iter}
	}

	pairs := make([]*AsyncIterator[*AgentEvent], n)
	generators := make([]*AsyncGenerator[*AgentEvent], n)
	for i := 0; i < n; i++ {
		pairs[i], generators[i] = NewAsyncIteratorPair[*AgentEvent]()
	}

	go func() {
		defer func() {
			for _, g := range generators {
				g.Close()
			}
		}()

		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			for i := 0; i < n-1; i++ {
				generators[i].Send(copyAgentEvent(event))
			}
			generators[n-1].Send(event)
		}
	}()

	return pairs
}

func copyAgentCallbackOutput(out *AgentCallbackOutput, n int) []*AgentCallbackOutput {
	if out == nil || out.Events == nil {
		result := make([]*AgentCallbackOutput, n)
		for i := 0; i < n; i++ {
			result[i] = out
		}
		return result
	}
	iters := copyEventIterator(out.Events, n)
	result := make([]*AgentCallbackOutput, n)
	for i, iter := range iters {
		result[i] = &AgentCallbackOutput{Events: iter}
	}
	return result
}

// ConvAgentCallbackInput converts a generic CallbackInput to AgentCallbackInput.
// Returns nil if the input is not an AgentCallbackInput.
func ConvAgentCallbackInput(input callbacks.CallbackInput) *AgentCallbackInput {
	if v, ok := input.(*AgentCallbackInput); ok {
		return v
	}
	return nil
}

// ConvAgentCallbackOutput converts a generic CallbackOutput to AgentCallbackOutput.
// Returns nil if the output is not an AgentCallbackOutput.
func ConvAgentCallbackOutput(output callbacks.CallbackOutput) *AgentCallbackOutput {
	if v, ok := output.(*AgentCallbackOutput); ok {
		return v
	}
	return nil
}

func initAgentCallbacks(ctx context.Context, agentName, agentType string, opts ...AgentRunOption) context.Context {
	ri := &callbacks.RunInfo{
		Name:      agentName,
		Type:      agentType,
		Component: ComponentOfAgent,
	}

	o := getCommonOptions(nil, opts...)
	if len(o.handlers) == 0 {
		return icb.ReuseHandlers(ctx, ri)
	}
	return icb.AppendHandlers(ctx, ri, o.handlers...)
}

func getAgentType(agent Agent) string {
	if typer, ok := agent.(components.Typer); ok {
		return typer.GetType()
	}
	return ""
}
