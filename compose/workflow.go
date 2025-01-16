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
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/cloudwego/eino/components/document"
	"github.com/cloudwego/eino/components/embedding"
	"github.com/cloudwego/eino/components/indexer"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/prompt"
	"github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/utils/generic"
)

// Mapping is the mapping from one node's output to current node's input.
type Mapping struct {
	fromNodeKey string
	from        string
	to          string
}

func (m *Mapping) empty() bool {
	return len(m.from) == 0 && len(m.to) == 0
}

// From chooses a field value from fromNode's output struct or struct pointer with the specific field name, to serve as the source of the Mapping.
func (m *Mapping) From(name string) *Mapping {
	m.from = name
	return m
}

// To chooses a field from currentNode's input struct or struct pointer with the specific field name, to serve as the destination of the Mapping.
func (m *Mapping) To(name string) *Mapping {
	m.to = name
	return m
}

// String returns the string representation of the Mapping.
func (m *Mapping) String() string {
	var sb strings.Builder
	sb.WriteString("from ")

	if m.from != "" {
		sb.WriteString(m.from)
		sb.WriteString("(field) of ")
	}

	sb.WriteString("node '")
	sb.WriteString(m.fromNodeKey)
	sb.WriteString("'")

	if m.to != "" {
		sb.WriteString(" to ")
		sb.WriteString(m.to)
		sb.WriteString("(field)")
	}

	sb.WriteString("; ")
	return sb.String()
}

// NewMapping creates a new Mapping with the specified fromNodeKey.
func NewMapping(fromNodeKey string) *Mapping {
	return &Mapping{fromNodeKey: fromNodeKey}
}

// WorkflowNode is the node of the Workflow.
type WorkflowNode struct {
	key    string
	inputs []*Mapping
}

// Workflow is wrapper of Graph, replacing AddEdge with declaring Mapping between one node's output and current node's input.
// Under the hood it uses NodeTriggerMode(AllPredecessor), so does not support branches or cycles.
type Workflow[I, O any] struct {
	gg *Graph[I, O]

	nodes map[string]*WorkflowNode
	end   []*Mapping
	err   error
}

// NewWorkflow creates a new Workflow.
func NewWorkflow[I, O any](opts ...NewGraphOption) *Workflow[I, O] {
	wf := &Workflow[I, O]{
		gg:    NewGraph[I, O](opts...),
		nodes: make(map[string]*WorkflowNode),
	}

	wf.gg.cmp = ComponentOfWorkflow
	return wf
}

func (wf *Workflow[I, O]) Compile(ctx context.Context, opts ...GraphCompileOption) (Runnable[I, O], error) {
	if wf.err != nil {
		return nil, wf.err
	}

	options := newGraphCompileOptions(opts...)
	if options.nodeTriggerMode == AnyPredecessor {
		return nil, errors.New("workflow does not support NodeTriggerMode(AnyPredecessor)")
	}

	opts = append(opts, WithNodeTriggerMode(AllPredecessor))

	if err := wf.addEdgesWithMapping(); err != nil {
		return nil, err
	}

	return wf.gg.Compile(ctx, opts...)
}

func (wf *Workflow[I, O]) AddChatModelNode(key string, chatModel model.ChatModel, opts ...GraphAddNodeOpt) *WorkflowNode {
	node := &WorkflowNode{key: key}
	if wf.err != nil {
		return node
	}

	wf.nodes[key] = node

	err := wf.gg.AddChatModelNode(key, chatModel, opts...)
	if err != nil {
		wf.err = err
		return node
	}
	return node
}

func (wf *Workflow[I, O]) AddChatTemplateNode(key string, chatTemplate prompt.ChatTemplate, opts ...GraphAddNodeOpt) *WorkflowNode {
	node := &WorkflowNode{key: key}
	if wf.err != nil {
		return node
	}

	wf.nodes[key] = node

	err := wf.gg.AddChatTemplateNode(key, chatTemplate, opts...)
	if err != nil {
		wf.err = err
		return node
	}

	return node
}

func (wf *Workflow[I, O]) AddToolsNode(key string, tools *ToolsNode, opts ...GraphAddNodeOpt) *WorkflowNode {
	node := &WorkflowNode{key: key}
	if wf.err != nil {
		return node
	}

	wf.nodes[key] = node

	err := wf.gg.AddToolsNode(key, tools, opts...)
	if err != nil {
		wf.err = err
		return node
	}
	return node
}

func (wf *Workflow[I, O]) AddRetrieverNode(key string, retriever retriever.Retriever, opts ...GraphAddNodeOpt) *WorkflowNode {
	node := &WorkflowNode{key: key}
	if wf.err != nil {
		return node
	}

	wf.nodes[key] = node

	err := wf.gg.AddRetrieverNode(key, retriever, opts...)
	if err != nil {
		wf.err = err
		return node
	}
	return node
}

func (wf *Workflow[I, O]) AddEmbeddingNode(key string, embedding embedding.Embedder, opts ...GraphAddNodeOpt) *WorkflowNode {
	node := &WorkflowNode{key: key}
	if wf.err != nil {
		return node
	}

	wf.nodes[key] = node

	err := wf.gg.AddEmbeddingNode(key, embedding, opts...)
	if err != nil {
		wf.err = err
		return node
	}
	return node
}

func (wf *Workflow[I, O]) AddIndexerNode(key string, indexer indexer.Indexer, opts ...GraphAddNodeOpt) *WorkflowNode {
	node := &WorkflowNode{key: key}
	if wf.err != nil {
		return node
	}

	wf.nodes[key] = node

	err := wf.gg.AddIndexerNode(key, indexer, opts...)
	if err != nil {
		wf.err = err
		return node
	}
	return node
}

func (wf *Workflow[I, O]) AddLoaderNode(key string, loader document.Loader, opts ...GraphAddNodeOpt) *WorkflowNode {
	node := &WorkflowNode{key: key}
	if wf.err != nil {
		return node
	}

	wf.nodes[key] = node

	err := wf.gg.AddLoaderNode(key, loader, opts...)
	if err != nil {
		wf.err = err
		return node
	}
	return node
}

func (wf *Workflow[I, O]) AddDocumentTransformerNode(key string, transformer document.Transformer, opts ...GraphAddNodeOpt) *WorkflowNode {
	node := &WorkflowNode{key: key}
	if wf.err != nil {
		return node
	}

	wf.nodes[key] = node

	err := wf.gg.AddDocumentTransformerNode(key, transformer, opts...)
	if err != nil {
		wf.err = err
		return node
	}
	return node
}

func (wf *Workflow[I, O]) AddGraphNode(key string, graph AnyGraph, opts ...GraphAddNodeOpt) *WorkflowNode {
	node := &WorkflowNode{key: key}
	if wf.err != nil {
		return node
	}

	wf.nodes[key] = node

	err := wf.gg.AddGraphNode(key, graph, opts...)
	if err != nil {
		wf.err = err
		return node
	}
	return node
}

func (wf *Workflow[I, O]) AddLambdaNode(key string, lambda *Lambda, opts ...GraphAddNodeOpt) *WorkflowNode {
	node := &WorkflowNode{key: key}
	if wf.err != nil {
		return node
	}

	wf.nodes[key] = node

	err := wf.gg.AddLambdaNode(key, lambda, opts...)
	if err != nil {
		wf.err = err
		return node
	}

	return node
}

func (n *WorkflowNode) AddInput(inputs ...*Mapping) *WorkflowNode {
	n.inputs = append(n.inputs, inputs...)
	return n
}

func (wf *Workflow[I, O]) AddEnd(inputs ...*Mapping) {
	wf.end = inputs
}

func (wf *Workflow[I, O]) compile(ctx context.Context, options *graphCompileOptions) (*composableRunnable, error) {
	if options.nodeTriggerMode == AnyPredecessor {
		return nil, errors.New("workflow does not support NodeTriggerMode(AnyPredecessor)")
	}
	options.nodeTriggerMode = AllPredecessor
	if err := wf.addEdgesWithMapping(); err != nil {
		return nil, err
	}
	return wf.gg.compile(ctx, options)
}

func (wf *Workflow[I, O]) inputType() reflect.Type {
	return generic.TypeOf[I]()
}

func (wf *Workflow[I, O]) outputType() reflect.Type {
	return generic.TypeOf[O]()
}

func (wf *Workflow[I, O]) component() component {
	return wf.gg.component()
}

func (wf *Workflow[I, O]) addEdgesWithMapping() (err error) {
	var toNode string
	for _, node := range wf.nodes {
		toNode = node.key
		if len(node.inputs) == 0 {
			return fmt.Errorf("workflow node = %s has no input", toNode)
		}

		fromNode2Mappings := make(map[string][]*Mapping, len(node.inputs))
		for i := range node.inputs {
			input := node.inputs[i]
			fromNodeKey := input.fromNodeKey
			fromNode2Mappings[fromNodeKey] = append(fromNode2Mappings[fromNodeKey], input)
		}

		for fromNode, mappings := range fromNode2Mappings {
			if err = checkMappingGroup(mappings); err != nil {
				return err
			}

			if mappings[0].empty() {
				if err = wf.gg.AddEdge(fromNode, toNode); err != nil {
					return err
				}
			} else if err = wf.gg.addEdgeWithMappings(fromNode, toNode, mappings...); err != nil {
				return err
			}
		}
	}

	if len(wf.end) == 0 {
		return errors.New("workflow END has no input mapping")
	}

	fromNode2EndMappings := make(map[string][]*Mapping, len(wf.end))
	for i := range wf.end {
		input := wf.end[i]
		fromNodeKey := input.fromNodeKey
		fromNode2EndMappings[fromNodeKey] = append(fromNode2EndMappings[fromNodeKey], input)
	}

	for fromNode, mappings := range fromNode2EndMappings {
		if err = checkMappingGroup(mappings); err != nil {
			return err
		}

		if mappings[0].empty() {
			if err = wf.gg.AddEdge(fromNode, END); err != nil {
				return err
			}
		} else if err = wf.gg.addEdgeWithMappings(fromNode, END, mappings...); err != nil {
			return err
		}
	}

	return nil
}
