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

package compose

import (
	"context"
	"reflect"

	"github.com/cloudwego/eino/internal/generic"
)

type newGraphOptions struct {
	withState func(ctx context.Context) any
	stateType reflect.Type
}

// NewGraphOption configures behavior when creating a new graph, such as
// providing local state generation.
type NewGraphOption func(ngo *newGraphOptions)

// WithGenLocalState registers a function to generate per-run local state
// that can be shared across nodes in the graph.
func WithGenLocalState[S any](gls GenLocalState[S]) NewGraphOption {
	return func(ngo *newGraphOptions) {
		ngo.withState = func(ctx context.Context) any {
			return gls(ctx)
		}
		ngo.stateType = generic.TypeOf[S]()
	}
}

// NewGraph create a directed graph that can compose components, lambda, chain, parallel etc.
// simultaneously provide flexible and multi-granular aspect governance capabilities.
// I: the input type of graph compiled product
// O: the output type of graph compiled product
//
// To share state between nodes, use WithGenLocalState option:
//
//	type testState struct {
//		UserInfo *UserInfo
//		KVs     map[string]any
//	}
//
//	genStateFunc := func(ctx context.Context) *testState {
//		return &testState{}
//	}
//
//	graph := compose.NewGraph[string, string](WithGenLocalState(genStateFunc))
//
//	// you can use WithStatePreHandler and WithStatePostHandler to do something with state
//	graph.AddNode("node1", someNode, compose.WithPreHandler(func(ctx context.Context, in string, state *testState) (string, error) {
//		// do something with state
//		return in, nil
//	}), compose.WithPostHandler(func(ctx context.Context, out string, state *testState) (string, error) {
//		// do something with state
//		return out, nil
//	}))
func NewGraph[I, O any](opts ...NewGraphOption) *Graph[I, O] {
	options := &newGraphOptions{}
	for _, opt := range opts {
		opt(options)
	}

	g := &Graph[I, O]{
		newGraphFromGeneric[I, O](
			ComponentOfGraph,
			options.withState,
			options.stateType,
			opts,
		),
	}

	return g
}

// Graph is a generic graph that can be used to compose components.
// I: the input type of graph compiled product
// O: the output type of graph compiled product
type Graph[I, O any] struct {
	*graph
}

// AddEdge adds an edge to the graph, edge means a data flow from startNode to endNode.
// the previous node's output type must be set to the next node's input type.
// NOTE: startNode and endNode must have been added to the graph before adding edge.
// e.g.
//
//	graph.AddNode("start_node_key", compose.NewPassthroughNode())
//	graph.AddNode("end_node_key", compose.NewPassthroughNode())
//
//	err := graph.AddEdge("start_node_key", "end_node_key")
func (g *Graph[I, O]) AddEdge(startNode, endNode string) (err error) {
	return g.graph.addEdgeWithMappings(startNode, endNode, false, false)
}

// Compile take the raw graph and compile it into a form ready to be run.
// e.g.
//
//	graph, err := compose.NewGraph[string, string]()
//	if err != nil {...}
//
//	runnable, err := graph.Compile(ctx, compose.WithGraphName("my_graph"))
//	if err != nil {...}
//
//	runnable.Invoke(ctx, "input") // invoke
//	runnable.Stream(ctx, "input") // stream
//	runnable.Collect(ctx, inputReader) // collect
//	runnable.Transform(ctx, inputReader) // transform
func (g *Graph[I, O]) Compile(ctx context.Context, opts ...GraphCompileOption) (Runnable[I, O], error) {
	return compileAnyGraph[I, O](ctx, g, opts...)
}

func compileAnyGraph[I, O any](ctx context.Context, g AnyGraph, opts ...GraphCompileOption) (Runnable[I, O], error) {
	if len(globalGraphCompileCallbacks) > 0 {
		opts = append([]GraphCompileOption{WithGraphCompileCallbacks(globalGraphCompileCallbacks...)}, opts...)
	}
	option := newGraphCompileOptions(opts...)

	cr, err := g.compile(ctx, option)
	if err != nil {
		return nil, err
	}

	cr.meta = &executorMeta{
		component:                  g.component(),
		isComponentCallbackEnabled: true,
		componentImplType:          "",
	}

	cr.nodeInfo = &nodeInfo{
		name: option.graphName,
	}

	ctxWrapper := func(ctx context.Context, opts ...Option) context.Context {
		return initGraphCallbacks(AppendAddressSegment(ctx, AddressSegmentRunnable, option.graphName), cr.nodeInfo, cr.meta, opts...)
	}

	rp, err := toGenericRunnable[I, O](cr, ctxWrapper)
	if err != nil {
		return nil, err
	}

	return rp, nil
}
