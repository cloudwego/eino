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

package parser

// Options configures the document parser with source URI and extra metadata.
type Options struct {
	// uri of source.
	URI string

	// extra metadata will merge to each document.
	ExtraMeta map[string]any
}

// Option defines call option for Parser component, which is part of the component interface signature.
// Each Parser implementation could define its own options struct and option funcs within its own package,
// then wrap the impl specific option funcs into this type, before passing to Transform.
type Option struct {
	apply func(opts *Options)

	implSpecificOptFn any
}

// WithURI specifies the source URI of the document.
// It will be used as to select parser in ExtParser.
func WithURI(uri string) Option {
	return Option{
		apply: func(opts *Options) {
			opts.URI = uri
		},
	}
}

// WithExtraMeta attaches extra metadata to the parsed document.
func WithExtraMeta(meta map[string]any) Option {
	return Option{
		apply: func(opts *Options) {
			opts.ExtraMeta = meta
		},
	}
}

// GetCommonOptions extract parser Options from Option list, optionally providing a base Options with default values.
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

// WrapImplSpecificOptFn wraps the impl specific option functions into Option type.
// T: the type of the impl specific options struct.
// Parser implementations are required to use this function to convert its own option functions into the unified Option type.
// For example, if the Parser impl defines its own options struct:
//
//	type customOptions struct {
//	    conf string
//	}
//
// Then the impl needs to provide an option function as such:
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

// GetImplSpecificOptions provides Parser author the ability to extract their own custom options from the unified Option type.
// T: the type of the impl specific options struct.
// This function should be used within the Parser implementation's Transform function.
// It is recommended to provide a base T as the first argument, within which the Parser author can provide default values for the impl specific options.
func GetImplSpecificOptions[T any](base *T, opts ...Option) *T {
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
