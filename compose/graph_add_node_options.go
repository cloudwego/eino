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
	"reflect"

	"github.com/cloudwego/eino/internal/generic"
)

type graphAddNodeOpts struct {
	nodeOptions *nodeOptions
	processor   *processorOpts

	needState bool
}

// GraphAddNodeOpt is a functional option type for adding a node to a graph.
// e.g.
//
//	graph.AddNode("node_name", node, compose.WithInputKey("input_key"), compose.WithOutputKey("output_key"))
type GraphAddNodeOpt func(o *graphAddNodeOpts)

type nodeOptions struct {
	nodeName string

	nodeKey string

	inputKey  string
	outputKey string

	graphCompileOption []GraphCompileOption // when this node is itself an AnyGraph, this option will be used to compile the node as a nested graph
}

// WithNodeName sets the name of the node.
func WithNodeName(n string) GraphAddNodeOpt {
	return func(o *graphAddNodeOpts) {
		o.nodeOptions.nodeName = n
	}
}

// WithNodeKey set the node key, which is used to identify the node in the chain.
// only for use in Chain/StateChain.
func WithNodeKey(key string) GraphAddNodeOpt {
	return func(o *graphAddNodeOpts) {
		o.nodeOptions.nodeKey = key
	}
}

// WithInputKey sets the input key of the node.
// this will change the input value of the node, for example, if the pre node's output is map[string]any{"key01": "value01"},
// and the current node's input key is "key01", then the current node's input value will be "value01".
func WithInputKey(k string) GraphAddNodeOpt {
	return func(o *graphAddNodeOpts) {
		o.nodeOptions.inputKey = k
	}
}

// WithOutputKey sets the output key of the node.
// this will change the output value of the node, for example, if the current node's output key is "key01",
// then the node's output value will be map[string]any{"key01": value}.
func WithOutputKey(k string) GraphAddNodeOpt {
	return func(o *graphAddNodeOpts) {
		o.nodeOptions.outputKey = k
	}
}

// WithGraphCompileOptions when the node is an AnyGraph, use this option to set compile option for the node.
// e.g.
//
//	graph.AddNode("node_name", node, compose.WithGraphCompileOptions(compose.WithGraphName("my_sub_graph")))
func WithGraphCompileOptions(opts ...GraphCompileOption) GraphAddNodeOpt {
	return func(o *graphAddNodeOpts) {
		o.nodeOptions.graphCompileOption = opts
	}
}

// WithStatePreHandler modify node's input of I according to state S and input or store input information into state, and it's thread-safe.
// notice: this option requires Graph to be created with WithGenLocalState option.
// I: input type of the Node like ChatModel, Lambda, Retriever etc.
// S: state type defined in WithGenLocalState
func WithStatePreHandler[I, S any](pre StatePreHandler[I, S]) GraphAddNodeOpt {
	return func(o *graphAddNodeOpts) {
		o.processor.statePreHandler = convertPreHandler(pre)
		o.processor.preStateType = generic.TypeOf[S]()
		o.needState = true
	}
}

// WithStatePostHandler modify node's output of O according to state S and output or store output information into state, and it's thread-safe.
// notice: this option requires Graph to be created with WithGenLocalState option.
// O: output type of the Node like ChatModel, Lambda, Retriever etc.
// S: state type defined in WithGenLocalState
func WithStatePostHandler[O, S any](post StatePostHandler[O, S]) GraphAddNodeOpt {
	return func(o *graphAddNodeOpts) {
		o.processor.statePostHandler = convertPostHandler(post)
		o.processor.postStateType = generic.TypeOf[S]()
		o.needState = true
	}
}

// WithStreamStatePreHandler modify node's streaming input of I according to state S and input or store input information into state, and it's thread-safe.
// notice: this option requires Graph to be created with WithGenLocalState option.
// when to use: when upstream node's output is an actual stream, and you want the current node's input to remain an actual stream after state pre handler.
// caution: while StreamStatePreHandler is thread safe, modifying state within your own goroutine is NOT.
// I: input type of the Node like ChatModel, Lambda, Retriever etc.
// S: state type defined in WithGenLocalState
func WithStreamStatePreHandler[I, S any](pre StreamStatePreHandler[I, S]) GraphAddNodeOpt {
	return func(o *graphAddNodeOpts) {
		o.processor.statePreHandler = streamConvertPreHandler(pre)
		o.processor.preStateType = generic.TypeOf[S]()
		o.needState = true
	}
}

// WithStreamStatePostHandler modify node's streaming output of O according to state S and output or store output information into state, and it's thread-safe.
// notice: this option requires Graph to be created with WithGenLocalState option.
// when to use: when current node's output is an actual stream, and you want the downstream node's input to remain an actual stream after state post handler.
// caution: while StreamStatePostHandler is thread safe, modifying state within your own goroutine is NOT.
// O: output type of the Node like ChatModel, Lambda, Retriever etc.
// S: state type defined in WithGenLocalState
func WithStreamStatePostHandler[O, S any](post StreamStatePostHandler[O, S]) GraphAddNodeOpt {
	return func(o *graphAddNodeOpts) {
		o.processor.statePostHandler = streamConvertPostHandler(post)
		o.processor.postStateType = generic.TypeOf[S]()
		o.needState = true
	}
}

type processorOpts struct {
	statePreHandler  *composableRunnable
	preStateType     reflect.Type // used for type validation
	statePostHandler *composableRunnable
	postStateType    reflect.Type // used for type validation
}

func getGraphAddNodeOpts(opts ...GraphAddNodeOpt) *graphAddNodeOpts {
	opt := &graphAddNodeOpts{
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
