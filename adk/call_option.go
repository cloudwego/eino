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

import "github.com/cloudwego/eino/callbacks"

type options struct {
	sharedParentSession  bool
	sessionValues        map[string]any
	checkPointID         *string
	skipTransferMessages bool
	handlers             []callbacks.Handler
}

// AgentRunOption is the call option for adk Agent.
type AgentRunOption struct {
	implSpecificOptFn any

	// specify which Agent can see this AgentRunOption, if empty, all Agents can see this AgentRunOption
	agentNames []string
}

func (o AgentRunOption) DesignateAgent(name ...string) AgentRunOption {
	o.agentNames = append(o.agentNames, name...)
	return o
}

func getCommonOptions(base *options, opts ...AgentRunOption) *options {
	if base == nil {
		base = &options{}
	}

	return GetImplSpecificOptions(base, opts...)
}

// WithSessionValues sets session-scoped values for the agent run.
func WithSessionValues(v map[string]any) AgentRunOption {
	return WrapImplSpecificOptFn(func(o *options) {
		o.sessionValues = v
	})
}

// WithSkipTransferMessages disables forwarding transfer messages during execution.
func WithSkipTransferMessages() AgentRunOption {
	return WrapImplSpecificOptFn(func(t *options) {
		t.skipTransferMessages = true
	})
}

func withSharedParentSession() AgentRunOption {
	return WrapImplSpecificOptFn(func(o *options) {
		o.sharedParentSession = true
	})
}

// WithCallbacks adds callback handlers to receive agent lifecycle events.
// Handlers receive OnStart with AgentCallbackInput and OnEnd with AgentCallbackOutput.
// Multiple handlers can be added; each receives an independent copy of the event stream.
func WithCallbacks(handlers ...callbacks.Handler) AgentRunOption {
	return WrapImplSpecificOptFn(func(o *options) {
		o.handlers = append(o.handlers, handlers...)
	})
}

// WrapImplSpecificOptFn is the option to wrap the implementation specific option function.
func WrapImplSpecificOptFn[T any](optFn func(*T)) AgentRunOption {
	return AgentRunOption{
		implSpecificOptFn: optFn,
	}
}

// GetImplSpecificOptions extract the implementation specific options from AgentRunOption list, optionally providing a base options with default values.
// e.g.
//
//	myOption := &MyOption{
//		Field1: "default_value",
//	}
//
//	myOption := model.GetImplSpecificOptions(myOption, opts...)
func GetImplSpecificOptions[T any](base *T, opts ...AgentRunOption) *T {
	if base == nil {
		base = new(T)
	}

	for i := range opts {
		opt := opts[i]
		if opt.implSpecificOptFn != nil {
			optFn, ok := opt.implSpecificOptFn.(func(*T))
			if ok {
				optFn(base)
			}
		}
	}

	return base
}

// filterCallbackHandlersForNestedAgents removes callback handlers that have already been applied
// to the current agent before passing opts to nested inner agents.
//
// This is necessary for workflow agents (LoopAgent, SequentialAgent, ParallelAgent) because:
//  1. Callback handlers designated for the current agent are applied via initAgentCallbacks(),
//     which stores them in the context.
//  2. Nested inner agents inherit this context, so they automatically receive these callbacks.
//  3. If we also pass these handlers in opts to inner agents, they would be applied twice,
//     causing duplicate callback invocations.
//
// Note: This only applies to workflow agents where inner agents inherit context from the parent.
// For flowAgent's sub-agents (which are peer agents that transfer to each other), the full opts
// are passed since they don't inherit the parent's callback context.
func filterCallbackHandlersForNestedAgents(currentAgentName string, opts []AgentRunOption) []AgentRunOption {
	if len(opts) == 0 {
		return nil
	}
	var filteredOpts []AgentRunOption
	for i := range opts {
		opt := opts[i]
		if opt.implSpecificOptFn == nil {
			filteredOpts = append(filteredOpts, opt)
			continue
		}
		if _, isCallbackOpt := opt.implSpecificOptFn.(func(*options)); isCallbackOpt {
			testOpt := &options{}
			opt.implSpecificOptFn.(func(*options))(testOpt)
			if len(testOpt.handlers) > 0 {
				if len(opt.agentNames) == 0 {
					continue
				}
				matched := false
				for _, name := range opt.agentNames {
					if name == currentAgentName {
						matched = true
						break
					}
				}
				if matched {
					continue
				}
			}
		}
		filteredOpts = append(filteredOpts, opt)
	}
	return filteredOpts
}

func filterOptions(agentName string, opts []AgentRunOption) []AgentRunOption {
	if len(opts) == 0 {
		return nil
	}
	var filteredOpts []AgentRunOption
	for i := range opts {
		opt := opts[i]
		if len(opt.agentNames) == 0 {
			filteredOpts = append(filteredOpts, opt)
			continue
		}
		for j := range opt.agentNames {
			if opt.agentNames[j] == agentName {
				filteredOpts = append(filteredOpts, opt)
				break
			}
		}
	}
	return filteredOpts
}
