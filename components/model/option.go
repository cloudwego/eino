/*
 * Copyright 2024 CloudWeGo Authors
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

package model

import "github.com/cloudwego/eino/schema"

// Options is the common options for the model.
type Options struct {
	// Temperature is the temperature for the model, which controls the randomness of the model.
	Temperature *float32
	// MaxTokens is the max number of tokens, if reached the max tokens, the model will stop generating, and mostly return an finish reason of "length".
	MaxTokens *int
	// Model is the model name.
	Model *string
	// TopP is the top p for the model, which controls the diversity of the model.
	TopP *float32
	// Stop is the stop words for the model, which controls the stopping condition of the model.
	Stop []string
	// Tools is a list of tools the model may call.
	Tools []*schema.ToolInfo
	// ToolChoice controls which tool is called by the model.
	ToolChoice *schema.ToolChoice
	// AllowedToolNames specifies a list of tool names that the model is allowed to call.
	// This allows for constraining the model to a specific subset of the available tools.
	AllowedToolNames []string
}

// Option is the call option for ChatModel component.
type Option struct {
	apply func(opts *Options)

	implSpecificOptFn any
}

// WithTemperature is the option to set the temperature for the model.
func WithTemperature(temperature float32) Option {
	return Option{
		apply: func(opts *Options) {
			opts.Temperature = &temperature
		},
	}
}

// WithMaxTokens is the option to set the max tokens for the model.
func WithMaxTokens(maxTokens int) Option {
	return Option{
		apply: func(opts *Options) {
			opts.MaxTokens = &maxTokens
		},
	}
}

// WithModel is the option to set the model name.
func WithModel(name string) Option {
	return Option{
		apply: func(opts *Options) {
			opts.Model = &name
		},
	}
}

// WithTopP is the option to set the top p for the model.
func WithTopP(topP float32) Option {
	return Option{
		apply: func(opts *Options) {
			opts.TopP = &topP
		},
	}
}

// WithStop is the option to set the stop words for the model.
func WithStop(stop []string) Option {
	return Option{
		apply: func(opts *Options) {
			opts.Stop = stop
		},
	}
}

// WithTools is the option to set tools for the model.
func WithTools(tools []*schema.ToolInfo) Option {
	if tools == nil {
		tools = []*schema.ToolInfo{}
	}
	return Option{
		apply: func(opts *Options) {
			opts.Tools = tools
		},
	}
}

// WithToolChoice sets the tool choice for the model. It also allows for providing a list of
// tool names to constrain the model to a specific subset of the available tools.
func WithToolChoice(toolChoice schema.ToolChoice, allowedToolNames ...string) Option {
	return Option{
		apply: func(opts *Options) {
			opts.ToolChoice = &toolChoice
			opts.AllowedToolNames = allowedToolNames
		},
	}
}

// WrapImplSpecificOptFn is the option to wrap the implementation specific option function.
func WrapImplSpecificOptFn[T any](optFn func(*T)) Option {
	return Option{
		implSpecificOptFn: optFn,
	}
}

// GetCommonOptions extract model Options from Option list, optionally providing a base Options with default values.
func GetCommonOptions(base *Options, opts ...Option) *Options {
	if base == nil {
		base = &Options{}
	}

	for i := range opts {
		opt := opts[i]
		if opt.apply != nil {
			opt.apply(base)
		}
	}

	return base
}

// GetImplSpecificOptions extract the implementation specific options from Option list, optionally providing a base options with default values.
// e.g.
//
//	myOption := &MyOption{
//		Field1: "default_value",
//	}
//
//	myOption := model.GetImplSpecificOptions(myOption, opts...)
func GetImplSpecificOptions[T any](base *T, opts ...Option) *T {
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
