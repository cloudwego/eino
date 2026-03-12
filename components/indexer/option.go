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

package indexer

import "github.com/cloudwego/eino/components/embedding"

// Options is the options for the indexer.
type Options struct {
	// SubIndexes is the sub indexes to be indexed.
	SubIndexes []string
	// Embedding is the embedding component.
	Embedding embedding.Embedder
}

// WithSubIndexes is the option to set the sub indexes for the indexer.
func WithSubIndexes(subIndexes []string) Option {
	return Option{
		apply: func(opts *Options) {
			opts.SubIndexes = subIndexes
		},
	}
}

// WithEmbedding is the option to set the embedder for the indexer, which convert document to embeddings.
func WithEmbedding(emb embedding.Embedder) Option {
	return Option{
		apply: func(opts *Options) {
			opts.Embedding = emb
		},
	}
}

// Option is a call-time option for an Indexer.
type Option struct {
	apply func(opts *Options)

	implSpecificOptFn any
}

// GetCommonOptions extracts standard [Options] from opts, merging onto base.
// Implementors must call this inside Store:
//
//	func (idx *MyIndexer) Store(ctx context.Context, docs []*schema.Document, opts ...indexer.Option) ([]string, error) {
//	    options := indexer.GetCommonOptions(nil, opts...)
//	    // use options.Embedding to generate vectors before storage
//	}
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
// can be passed alongside standard options. For use by Indexer implementors.
func WrapImplSpecificOptFn[T any](optFn func(*T)) Option {
	return Option{
		implSpecificOptFn: optFn,
	}
}

// GetImplSpecificOptions extracts implementation-specific options from opts,
// merging onto base. Call alongside [GetCommonOptions] inside Store.
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
