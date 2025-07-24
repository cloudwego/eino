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

import "github.com/cloudwego/eino/compose"

type Options struct {
	checkPointID    *string
	checkPointStore compose.CheckPointStore
	enableStreaming *bool
}

func (o *Options) CheckPointID() (string, bool) {
	if o.checkPointID == nil {
		return "", false
	}
	return *o.checkPointID, true
}

// AgentRunOption is the call option for adk Agent.
type AgentRunOption struct {
	implSpecificOptFn any

	// specify which Agent can see this AgentRunOption, if empty, all agents can see this AgentRunOption
	agentNames []string

	nestedOption *AgentRunOption
}

func (o AgentRunOption) DesignateAgent(name ...string) AgentRunOption {
	o.agentNames = append(o.agentNames, name...)
	return o
}

func (o AgentRunOption) WrapIn(parent string) AgentRunOption {
	return AgentRunOption{
		nestedOption: &o,
	}.DesignateAgent(parent)
}

func GetCommonOptions(base *Options, opts ...AgentRunOption) *Options {
	if base == nil {
		base = &Options{}
	}

	return GetImplSpecificOptions[Options](base, opts...)
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

	// TODO: concurrent wrapper needs to carry over options for all its concurrent agents
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

func WithEnableStreaming(enable bool) AgentRunOption {
	return WrapImplSpecificOptFn(func(t *Options) {
		t.enableStreaming = &enable
	})
}

// unwrapOptions unwraps nested AgentRunOption if needed
func unwrapOptions(agentName string, opts ...AgentRunOption) []AgentRunOption {
	var unwrappedOpts []AgentRunOption
	for i := range opts {
		opt := opts[i]
		if len(opt.agentNames) == 0 { // not set agentNames, definitely not a wrapped option
			unwrappedOpts = append(unwrappedOpts, opt)
			continue
		}

		if opt.nestedOption == nil { // no nestedOption, just append
			unwrappedOpts = append(unwrappedOpts, opt)
			continue
		}

		var isCurrent bool
		for j := range opt.agentNames {
			if opt.agentNames[j] == agentName {
				isCurrent = true
				break
			}
		}

		if !isCurrent {
			unwrappedOpts = append(unwrappedOpts, *opt.nestedOption)
		} else {
			unwrappedOpts = append(unwrappedOpts, opt)
		}
	}

	return unwrappedOpts
}
