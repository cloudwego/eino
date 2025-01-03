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

package internal

import (
	"context"

	"github.com/cloudwego/eino/utils/generic"
)

type newGraphOptions struct {
	withState func(ctx context.Context) context.Context
}

type NewGraphOption func(ngo *newGraphOptions)

func WithGenLocalState[S any](gls func(ctx context.Context) (state S)) NewGraphOption {
	return func(ngo *newGraphOptions) {
		ngo.withState = func(ctx context.Context) context.Context {
			return context.WithValue(ctx, StateKey{}, gls(ctx))
		}
	}
}

func NewGraph[I, O any](opts ...NewGraphOption) *Graph[I, O] {
	options := &newGraphOptions{}
	for _, opt := range opts {
		opt(options)
	}

	g := &Graph[I, O]{
		newGraph(
			generic.TypeOf[I](),
			generic.TypeOf[O](),
			defaultStreamMapFilter[I],
			defaultValueChecker[I],
			defaultValueChecker[O],
			defaultStreamConverter[I],
			defaultStreamConverter[O],
			ComponentOfGraph,
			options.withState,
			options.withState != nil,
		),
	}

	return g
}

type Graph[I, O any] struct {
	*graph
}

func (g *Graph[I, O]) Compile(ctx context.Context, opts ...GraphCompileOption) (Runnable[I, O], error) {
	if len(globalGraphCompileCallbacks) > 0 {
		opts = append([]GraphCompileOption{WithGraphCompileCallbacks(globalGraphCompileCallbacks...)}, opts...)
	}
	option := newGraphCompileOptions(opts...)

	cr, err := g.graph.compile(ctx, option)
	if err != nil {
		return nil, err
	}

	cr.meta = &executorMeta{
		component:                  g.cmp,
		isComponentCallbackEnabled: true,
		componentImplType:          "",
	}

	cr.nodeInfo = &nodeInfo{
		name: option.graphName,
	}

	ctxWrapper := func(ctx context.Context, opts ...Option) context.Context {
		return initGraphCallbacks(ctx, cr.nodeInfo, cr.meta, opts...)
	}

	rp, err := toGenericRunnable[I, O](cr, ctxWrapper)
	if err != nil {
		return nil, err
	}

	return rp, nil
}
