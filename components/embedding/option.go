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

package embedding

// Options is the options for the embedding.
type Options struct {
	// Model is the model name for the embedding.
	Model *string
}

// Option is the call option for Embedder component.
type Option struct {
	apply func(opts *Options)
}

// WithModel is the option to set the model for the embedding.
func WithModel(model string) Option {
	return Option{
		apply: func(opts *Options) {
			opts.Model = &model
		},
	}
}

// GetCommonOptions extract embedding Options from Option list, optionally providing a base Options with default values.
// eg.
//
//	defaultModelName := "default_model"
//	embeddingOption := &embedding.Options{
//		Model: &defaultModelName,
//	}
//	embeddingOption := embedding.GetCommonOptions(embeddingOption, opts...)
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
