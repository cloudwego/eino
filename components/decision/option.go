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

package decision

// Options is the options for the decision.
type Options struct {
	// Model is the model name for the decision.
	Model *string
}

// Option is a call-time option for a Decider.
type Option struct {
	apply func(opts *Options)

	implSpecificOptFn any
}

// WithModel is the option to set the model for the decision.
func WithModel(model string) Option {
	return Option{
		apply: func(opts *Options) {
			opts.Model = &model
		},
	}
}

// GetCommonOptions extract decision Options from Option list, optionally providing a base Options with default values.
// eg.
//
//	defaultModelName := "default_model"
//	decisionOption := &decision.Options{
//		Model: &defaultModelName,
//	}
//	decisionOption := decision.GetCommonOptions(decisionOption, opts...)
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

// WrapImplSpecificOptFn wraps an implementation-specific option function so it
// can be passed alongside standard options. For use by Decider implementors:
//
//	func WithMyParam(v string) decision.Option {
//	    return decision.WrapImplSpecificOptFn(func(o *MyOptions) {
//	        o.MyParam = v
//	    })
//	}
func WrapImplSpecificOptFn[T any](optFn func(*T)) Option {
	return Option{
		implSpecificOptFn: optFn,
	}
}

// GetImplSpecificOptions extracts implementation-specific options from opts,
// merging them onto base. Call alongside [GetCommonOptions] inside Decide:
//
//	func (d *MyDecider) Decide(ctx context.Context, state any, questions map[string]decision.Question, opts ...decision.Option) (map[string]*decision.Answer, error) {
//	    common := decision.GetCommonOptions(nil, opts...)
//	    mine   := decision.GetImplSpecificOptions(&MyOptions{}, opts...)
//	    // use common.Model, mine.MyParam, etc.
//	}
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
