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

package document

import "github.com/cloudwego/eino/components/document/parser"

// LoaderOptions configures document loaders, including parser options.
type LoaderOptions struct {
	ParserOptions []parser.Option
}

// LoaderOption defines call option for Loader component, which is part of the component interface signature.
// Each Loader implementation could define its own options struct and option funcs within its own package,
// then wrap the impl specific option funcs into this type, before passing to Load.
type LoaderOption struct {
	apply func(opts *LoaderOptions)

	implSpecificOptFn any
}

// WrapLoaderImplSpecificOptFn wraps the impl specific option functions into LoaderOption type.
// T: the type of the impl specific options struct.
// Loader implementations are required to use this function to convert its own option functions into the unified LoaderOption type.
// For example, if the Loader impl defines its own options struct:
//
//	type customOptions struct {
//	    conf string
//	}
//
// Then the impl needs to provide an option function as such:
//
//	func WithConf(conf string) Option {
//	    return WrapLoaderImplSpecificOptFn(func(o *customOptions) {
//			o.conf = conf
//		}
//	}
func WrapLoaderImplSpecificOptFn[T any](optFn func(*T)) LoaderOption {
	return LoaderOption{
		implSpecificOptFn: optFn,
	}
}

// GetLoaderImplSpecificOptions provides Loader author the ability to extract their own custom options from the unified LoaderOption type.
// T: the type of the impl specific options struct.
// This function should be used within the Loader implementation's Load function.
// It is recommended to provide a base T as the first argument, within which the Loader author can provide default values for the impl specific options.
// eg.
//
//	myOption := &MyOption{
//		Field1: "default_value",
//	}
//	myOption := loader.GetLoaderImplSpecificOptions(myOption, opts...)
func GetLoaderImplSpecificOptions[T any](base *T, opts ...LoaderOption) *T {
	if base == nil {
		base = new(T)
	}

	for i := range opts {
		opt := opts[i]
		if opt.implSpecificOptFn != nil {
			s, ok := opt.implSpecificOptFn.(func(*T))
			if ok {
				s(base)
			}
		}
	}

	return base
}

// GetLoaderCommonOptions extract loader Options from Option list, optionally providing a base Options with default values.
func GetLoaderCommonOptions(base *LoaderOptions, opts ...LoaderOption) *LoaderOptions {
	if base == nil {
		base = &LoaderOptions{}
	}

	for i := range opts {
		opt := opts[i]
		if opt.apply != nil {
			opt.apply(base)
		}
	}

	return base
}

// WithParserOptions attaches parser options to a loader request.
func WithParserOptions(opts ...parser.Option) LoaderOption {
	return LoaderOption{
		apply: func(o *LoaderOptions) {
			o.ParserOptions = opts
		},
	}
}

// TransformerOption defines call option for Transformer component, which is part of the component interface signature.
// Each Transformer implementation could define its own options struct and option funcs within its own package,
// then wrap the impl specific option funcs into this type, before passing to Transform.
type TransformerOption struct {
	implSpecificOptFn any
}

// WrapTransformerImplSpecificOptFn wraps the impl specific option functions into TransformerOption type.
// T: the type of the impl specific options struct.
// Transformer implementations are required to use this function to convert its own option functions into the unified TransformerOption type.
// For example, if the Transformer impl defines its own options struct:
//
//	type customOptions struct {
//	    conf string
//	}
//
// Then the impl needs to provide an option function as such:
//
//	func WithConf(conf string) TransformerOption {
//	    return WrapTransformerImplSpecificOptFn(func(o *customOptions) {
//			o.conf = conf
//		}
//	}
//
// .
func WrapTransformerImplSpecificOptFn[T any](optFn func(*T)) TransformerOption {
	return TransformerOption{
		implSpecificOptFn: optFn,
	}
}

// GetTransformerImplSpecificOptions provides Transformer author the ability to extract their own custom options from the unified TransformerOption type.
// T: the type of the impl specific options struct.
// This function should be used within the Transformer implementation's Transform function.
// It is recommended to provide a base T as the first argument, within which the Transformer author can provide default values for the impl specific options.
// eg.
//
//	myOption := &MyOption{
//		Field1: "default_value",
//	}
//	myOption := transformer.GetTransformerImplSpecificOptions(myOption, opts...)
func GetTransformerImplSpecificOptions[T any](base *T, opts ...TransformerOption) *T {
	if base == nil {
		base = new(T)
	}

	for i := range opts {
		opt := opts[i]
		if opt.implSpecificOptFn != nil {
			s, ok := opt.implSpecificOptFn.(func(*T))
			if ok {
				s(base)
			}
		}
	}

	return base
}
