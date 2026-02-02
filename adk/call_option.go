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

type options struct {
	sharedParentSession  bool
	sessionValues        map[string]any
	checkPointID         *string
	skipTransferMessages bool
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

	return GetImplSpecificOptions[options](base, opts...)
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
