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

package tool

// Option defines call option for InvokableTool or StreamableTool component, which is part of component interface signature.
// Each tool implementation could define its own options struct and option functions within its own package,
// then wrap the impl specific option functions into this type, before passing to InvokableRun or StreamableRun.
type Option struct {
	implSpecificOptFn any
}

// WrapImplSpecificOptFn wraps the impl specific option functions into Option type.
// T: the type of the impl specific options struct.
// Tool implementations are required to use this function to convert its own option functions into the unified Option type.
// For example, if the tool defines its own options struct:
//
//	type customOptions struct {
//	    conf string
//	}
//
// Then the tool needs to provide an option function as such:
//
//	func WithConf(conf string) Option {
//	    return WrapImplSpecificOptFn(func(o *customOptions) {
//			o.conf = conf
//		}
//	}
//
// .
func WrapImplSpecificOptFn[T any](optFn func(*T)) Option {
	return Option{
		implSpecificOptFn: optFn,
	}
}

// GetImplSpecificOptions provides tool author the ability to extract their own custom options from the unified Option type.
// T: the type of the impl specific options struct.
// This function should be used within the tool implementation's InvokableRun or StreamableRun functions.
// It is recommended to provide a base T as the first argument, within which the tool author can provide default values for the impl specific options.
// eg.
//
//	type customOptions struct {
//	    conf string
//	}
//	defaultOptions := &customOptions{}
//
//	customOptions := tool.GetImplSpecificOptions(defaultOptions, opts...)
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
