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

	"github.com/cloudwego/eino/schema"
)

type GraphAddNodeOpts struct {
	nodeOptions *nodeOptions
	processor   *processorOpts

	needState bool
}

// GraphAddNodeOpt is a functional option type for adding a node to a graph.
// e.g.
//
//	graph.AddNode("node_name", node, compose.WithInputKey("input_key"), compose.WithOutputKey("output_key"))
type GraphAddNodeOpt func(o *GraphAddNodeOpts)

type nodeOptions struct {
	nodeName string

	nodeKey string

	inputKey  string
	outputKey string

	graphCompileOption []GraphCompileOption // when this node is itself an AnyGraph, this option will be used to compile the node as a nested graph
}

// WithNodeName sets the name of the node.
func WithNodeName(n string) GraphAddNodeOpt {
	return func(o *GraphAddNodeOpts) {
		o.nodeOptions.nodeName = n
	}
}

// WithNodeKey set the node key, which is used to identify the node in the chain.
// only for use in Chain/StateChain.
func WithNodeKey(key string) GraphAddNodeOpt {
	return func(o *GraphAddNodeOpts) {
		o.nodeOptions.nodeKey = key
	}
}

// WithInputKey sets the input key of the node.
// this will change the input value of the node, for example, if the pre node's output is map[string]any{"key01": "value01"},
// and the current node's input key is "key01", then the current node's input value will be "value01".
func WithInputKey(k string) GraphAddNodeOpt {
	return func(o *GraphAddNodeOpts) {
		o.nodeOptions.inputKey = k
	}
}

// WithOutputKey sets the output key of the node.
// this will change the output value of the node, for example, if the current node's output key is "key01",
// then the node's output value will be map[string]any{"key01": value}.
func WithOutputKey(k string) GraphAddNodeOpt {
	return func(o *GraphAddNodeOpts) {
		o.nodeOptions.outputKey = k
	}
}

// WithGraphCompileOptions when the node is an AnyGraph, use this option to set compile option for the node.
// e.g.
//
//	graph.AddNode("node_name", node, compose.WithGraphCompileOptions(compose.WithGraphName("my_sub_graph")))
func WithGraphCompileOptions(opts ...GraphCompileOption) GraphAddNodeOpt {
	return func(o *GraphAddNodeOpts) {
		o.nodeOptions.graphCompileOption = opts
	}
}

func WithStatePreHandler[I, S any](pre func(ctx context.Context, in I, state S) (I, error)) GraphAddNodeOpt {
	return func(o *GraphAddNodeOpts) {
		o.processor.statePreHandler = convertPreHandler(pre)
		o.needState = true
	}
}

func WithStatePostHandler[O, S any](post func(ctx context.Context, out O, state S) (O, error)) GraphAddNodeOpt {
	return func(o *GraphAddNodeOpts) {
		o.processor.statePostHandler = convertPostHandler(post)
		o.needState = true
	}
}

func WithStreamStatePreHandler[I, S any](pre func(ctx context.Context, in *schema.StreamReader[I], state S) (*schema.StreamReader[I], error)) GraphAddNodeOpt {
	return func(o *GraphAddNodeOpts) {
		o.processor.statePreHandler = streamConvertPreHandler(pre)
		o.needState = true
	}
}

func WithStreamStatePostHandler[O, S any](post func(ctx context.Context, out *schema.StreamReader[O], state S) (*schema.StreamReader[O], error)) GraphAddNodeOpt {
	return func(o *GraphAddNodeOpts) {
		o.processor.statePostHandler = streamConvertPostHandler(post)
		o.needState = true
	}
}

type processorOpts struct {
	statePreHandler  *composableRunnable
	statePostHandler *composableRunnable
}

func getGraphAddNodeOpts(opts ...GraphAddNodeOpt) *GraphAddNodeOpts {
	opt := &GraphAddNodeOpts{
		nodeOptions: &nodeOptions{
			nodeName: "",
			nodeKey:  "",
		},
		processor: &processorOpts{
			statePreHandler:  nil,
			statePostHandler: nil,
		},
	}

	for _, fn := range opts {
		fn(opt)
	}

	return opt
}
