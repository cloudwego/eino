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

package retriever

import "github.com/cloudwego/eino/components/embedding"

// Options is the options for the retriever.
type Options struct {
	// Index is the index for the retriever, index in different retriever may be different.
	Index *string
	// SubIndex is the sub index for the retriever, sub index in different retriever may be different.
	SubIndex *string
	// TopK is the top k for the retriever, which means the top number of documents to retrieve.
	TopK *int
	// ScoreThreshold is the score threshold for the retriever, eg 0.5 means the score of the document must be greater than 0.5.
	ScoreThreshold *float64
	// Embedding is the embedder for the retriever, which is used to embed the query for retrieval	.
	Embedding embedding.Embedder

	// DSLInfo carries backend-specific filter/query expressions. The structure and
	// semantics are defined by the underlying store implementation.
	DSLInfo map[string]any
}

// WithIndex wraps the index option.
func WithIndex(index string) Option {
	return Option{
		apply: func(opts *Options) {
			opts.Index = &index
		},
	}
}

// WithSubIndex wraps the sub index option.
func WithSubIndex(subIndex string) Option {
	return Option{
		apply: func(opts *Options) {
			opts.SubIndex = &subIndex
		},
	}
}

// WithTopK wraps the top k option.
func WithTopK(topK int) Option {
	return Option{
		apply: func(opts *Options) {
			opts.TopK = &topK
		},
	}
}

// WithScoreThreshold wraps the score threshold option.
func WithScoreThreshold(threshold float64) Option {
	return Option{
		apply: func(opts *Options) {
			opts.ScoreThreshold = &threshold
		},
	}
}

// WithEmbedding wraps the embedder option.
func WithEmbedding(emb embedding.Embedder) Option {
	return Option{
		apply: func(opts *Options) {
			opts.Embedding = emb
		},
	}
}

// WithDSLInfo wraps the dsl info option.
func WithDSLInfo(dsl map[string]any) Option {
	return Option{
		apply: func(opts *Options) {
			opts.DSLInfo = dsl
		},
	}
}

// Option is a call-time option for a Retriever.
type Option struct {
	apply func(opts *Options)

	implSpecificOptFn any
}

// GetCommonOptions extracts standard [Options] from opts, merging onto base.
// Implementors must call this to honour caller-provided options:
//
//	func (r *MyRetriever) Retrieve(ctx context.Context, query string, opts ...retriever.Option) ([]*schema.Document, error) {
//	    options := retriever.GetCommonOptions(&retriever.Options{TopK: &r.defaultTopK}, opts...)
//	    // use options.TopK, options.ScoreThreshold, options.Embedding, etc.
//	}
func GetCommonOptions(base *Options, opts ...Option) *Options {
	if base == nil {
		base = &Options{}
	}

	for i := range opts {
		if opts[i].apply != nil {
			opts[i].apply(base)
		}
	}

	return base
}

// WrapImplSpecificOptFn wraps an implementation-specific option function so it
// can be passed alongside standard options. For use by Retriever implementors.
func WrapImplSpecificOptFn[T any](optFn func(*T)) Option {
	return Option{
		implSpecificOptFn: optFn,
	}
}

// GetImplSpecificOptions extracts implementation-specific options from opts,
// merging onto base. Call alongside [GetCommonOptions] inside Retrieve.
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
