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
	"fmt"
	"reflect"

	"github.com/cloudwego/eino/components/document"
	"github.com/cloudwego/eino/components/embedding"
	"github.com/cloudwego/eino/components/indexer"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/prompt"
	"github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/schema"
)

// WorkflowNode is the node of the Workflow.
type WorkflowNode struct {
	g                *graph
	key              string
	addInputs        []func() error
	staticValues     map[string]any
	dependencySetter func(fromNodeKey string, typ dependencyType)
	mappedFieldPath  map[string]any
}

// Workflow is wrapper of graph, replacing AddEdge with declaring dependencies and field mappings between nodes.
// Under the hood it uses NodeTriggerMode(AllPredecessor), so does not support cycles.
type Workflow[I, O any] struct {
	g                *graph
	workflowNodes    map[string]*WorkflowNode
	workflowBranches []*WorkflowBranch
	dependencies     map[string]map[string]dependencyType
}

type dependencyType int

const (
	normalDependency dependencyType = iota
	noDirectDependency
	branchDependency
)

// NewWorkflow creates a new Workflow.
func NewWorkflow[I, O any](opts ...NewGraphOption) *Workflow[I, O] {
	options := &newGraphOptions{}
	for _, opt := range opts {
		opt(options)
	}

	wf := &Workflow[I, O]{
		g: newGraphFromGeneric[I, O](
			ComponentOfWorkflow,
			options.withState,
			options.stateType,
			opts,
		),
		workflowNodes: make(map[string]*WorkflowNode),
		dependencies:  make(map[string]map[string]dependencyType),
	}

	return wf
}

// Compile builds the workflow into a runnable graph.
func (wf *Workflow[I, O]) Compile(ctx context.Context, opts ...GraphCompileOption) (Runnable[I, O], error) {
	return compileAnyGraph[I, O](ctx, wf, opts...)
}

// AddChatModelNode adds a chat model node and returns it.
func (wf *Workflow[I, O]) AddChatModelNode(key string, chatModel model.BaseChatModel, opts ...GraphAddNodeOpt) *WorkflowNode {
	_ = wf.g.AddChatModelNode(key, chatModel, opts...)
	return wf.initNode(key)
}

// AddChatTemplateNode adds a chat template node and returns it.
func (wf *Workflow[I, O]) AddChatTemplateNode(key string, chatTemplate prompt.ChatTemplate, opts ...GraphAddNodeOpt) *WorkflowNode {
	_ = wf.g.AddChatTemplateNode(key, chatTemplate, opts...)
	return wf.initNode(key)
}

// AddToolsNode adds a tools node and returns it.
func (wf *Workflow[I, O]) AddToolsNode(key string, tools *ToolsNode, opts ...GraphAddNodeOpt) *WorkflowNode {
	_ = wf.g.AddToolsNode(key, tools, opts...)
	return wf.initNode(key)
}

// AddRetrieverNode adds a retriever node and returns it.
func (wf *Workflow[I, O]) AddRetrieverNode(key string, retriever retriever.Retriever, opts ...GraphAddNodeOpt) *WorkflowNode {
	_ = wf.g.AddRetrieverNode(key, retriever, opts...)
	return wf.initNode(key)
}

// AddEmbeddingNode adds an embedding node and returns it.
func (wf *Workflow[I, O]) AddEmbeddingNode(key string, embedding embedding.Embedder, opts ...GraphAddNodeOpt) *WorkflowNode {
	_ = wf.g.AddEmbeddingNode(key, embedding, opts...)
	return wf.initNode(key)
}

// AddIndexerNode adds an indexer node to the workflow and returns it.
func (wf *Workflow[I, O]) AddIndexerNode(key string, indexer indexer.Indexer, opts ...GraphAddNodeOpt) *WorkflowNode {
	_ = wf.g.AddIndexerNode(key, indexer, opts...)
	return wf.initNode(key)
}

// AddLoaderNode adds a document loader node to the workflow and returns it.
func (wf *Workflow[I, O]) AddLoaderNode(key string, loader document.Loader, opts ...GraphAddNodeOpt) *WorkflowNode {
	_ = wf.g.AddLoaderNode(key, loader, opts...)
	return wf.initNode(key)
}

// AddDocumentTransformerNode adds a document transformer node and returns it.
func (wf *Workflow[I, O]) AddDocumentTransformerNode(key string, transformer document.Transformer, opts ...GraphAddNodeOpt) *WorkflowNode {
	_ = wf.g.AddDocumentTransformerNode(key, transformer, opts...)
	return wf.initNode(key)
}

// AddGraphNode adds a nested graph node to the workflow and returns it.
func (wf *Workflow[I, O]) AddGraphNode(key string, graph AnyGraph, opts ...GraphAddNodeOpt) *WorkflowNode {
	_ = wf.g.AddGraphNode(key, graph, opts...)
	return wf.initNode(key)
}

// AddLambdaNode adds a lambda node to the workflow and returns it.
func (wf *Workflow[I, O]) AddLambdaNode(key string, lambda *Lambda, opts ...GraphAddNodeOpt) *WorkflowNode {
	_ = wf.g.AddLambdaNode(key, lambda, opts...)
	return wf.initNode(key)
}

// End returns the WorkflowNode representing END node.
func (wf *Workflow[I, O]) End() *WorkflowNode {
	if node, ok := wf.workflowNodes[END]; ok {
		return node
	}
	return wf.initNode(END)
}

// AddPassthroughNode adds a passthrough node to the workflow and returns it.
func (wf *Workflow[I, O]) AddPassthroughNode(key string, opts ...GraphAddNodeOpt) *WorkflowNode {
	_ = wf.g.AddPassthroughNode(key, opts...)
	return wf.initNode(key)
}

// AddInput creates both data and execution dependencies between nodes.
// It configures how data flows from the predecessor node (fromNodeKey) to the current node,
// and ensures the current node only executes after the predecessor completes.
//
// Parameters:
//   - fromNodeKey: the key of the predecessor node
//   - inputs: field mappings that specify how data should flow from the predecessor
//     to the current node. If no mappings are provided, the entire output of the
//     predecessor will be used as input.
//
// Example:
//
//	// Map between specific field
//	node.AddInput("userNode", MapFields("user.name", "displayName"))
//
//	// Use entire output
//	node.AddInput("dataNode")
//
// Returns the current node for method chaining.
func (n *WorkflowNode) AddInput(fromNodeKey string, inputs ...*FieldMapping) *WorkflowNode {
	return n.addDependencyRelation(fromNodeKey, inputs, &workflowAddInputOpts{})
}

type workflowAddInputOpts struct {
	// noDirectDependency indicates whether to create a data mapping without establishing
	// a direct execution dependency. When true, the current node can access data from
	// the predecessor node but its execution is not directly blocked by it.
	noDirectDependency bool
	// dependencyWithoutInput indicates whether to create an execution dependency
	// without any data mapping. When true, the current node will wait for the
	// predecessor node to complete but won't receive any data from it.
	dependencyWithoutInput bool
}

// WorkflowAddInputOpt configures behavior of AddInputWithOptions.
type WorkflowAddInputOpt func(*workflowAddInputOpts)

func getAddInputOpts(opts []WorkflowAddInputOpt) *workflowAddInputOpts {
	opt := &workflowAddInputOpts{}
	for _, o := range opts {
		o(opt)
	}
	return opt
}

// WithNoDirectDependency creates a data mapping without establishing a direct execution dependency.
// The predecessor node will still complete before the current node executes, but through indirect
// execution paths rather than a direct dependency.
//
// In a workflow graph, node dependencies typically serve two purposes:
// 1. Execution order: determining when nodes should run
// 2. Data flow: specifying how data passes between nodes
//
// This option separates these concerns by:
//   - Creating data mapping from the predecessor to the current node
//   - Relying on the predecessor's path to reach the current node through other nodes
//     that have direct execution dependencies
//
// Example:
//
//	node.AddInputWithOptions("dataNode", mappings, WithNoDirectDependency())
//
// Important:
//
//  1. Branch scenarios: When connecting nodes on different sides of a branch,
//     WithNoDirectDependency MUST be used to let the branch itself handle the
//     execution order, preventing incorrect dependencies that could bypass the branch.
//
//  2. Execution guarantee: The predecessor will still complete before the current
//     node executes because the predecessor must have a path (through other nodes)
//     that eventually reaches the current node.
//
//  3. Graph validity: There MUST be a path from the predecessor that eventually
//     reaches the current node through other nodes with direct dependencies.
//     This ensures the execution order while avoiding redundant direct dependencies.
//
// Common use cases:
// - Cross-branch data access where the branch handles execution order
// - Avoiding redundant dependencies when a path already exists
func WithNoDirectDependency() WorkflowAddInputOpt {
	return func(opt *workflowAddInputOpts) {
		opt.noDirectDependency = true
	}
}

// AddInputWithOptions creates a dependency between nodes with custom configuration options.
// It allows fine-grained control over both data flow and execution dependencies.
//
// Parameters:
//   - fromNodeKey: the key of the predecessor node
//   - inputs: field mappings that specify how data flows from the predecessor to the current node.
//     If no mappings are provided, the entire output of the predecessor will be used as input.
//   - opts: configuration options that control how the dependency is established
//
// Example:
//
//	// Create data mapping without direct execution dependency
//	node.AddInputWithOptions("dataNode", mappings, WithNoDirectDependency())
//
// Returns the current node for method chaining.
func (n *WorkflowNode) AddInputWithOptions(fromNodeKey string, inputs []*FieldMapping, opts ...WorkflowAddInputOpt) *WorkflowNode {
	return n.addDependencyRelation(fromNodeKey, inputs, getAddInputOpts(opts))
}

// AddDependency creates an execution-only dependency between nodes.
// The current node will wait for the predecessor node to complete before executing,
// but no data will be passed between them.
//
// Parameters:
//   - fromNodeKey: the key of the predecessor node that must complete before this node starts
//
// Example:
//
//	// Wait for "setupNode" to complete before executing
//	node.AddDependency("setupNode")
//
// This is useful when:
// - You need to ensure execution order without data transfer
// - The predecessor performs setup or initialization that must complete first
// - You want to explicitly separate execution dependencies from data flow
//
// Returns the current node for method chaining.
func (n *WorkflowNode) AddDependency(fromNodeKey string) *WorkflowNode {
	return n.addDependencyRelation(fromNodeKey, nil, &workflowAddInputOpts{dependencyWithoutInput: true})
}

// SetStaticValue sets a static value for a field path that will be available
// during workflow execution. These values are determined at compile time and
// remain constant throughout the workflow's lifecycle.
//
// Example:
//
//	node.SetStaticValue(FieldPath{"query"}, "static query")
func (n *WorkflowNode) SetStaticValue(path FieldPath, value any) *WorkflowNode {
	n.staticValues[path.join()] = value
	return n
}

func (n *WorkflowNode) addDependencyRelation(fromNodeKey string, inputs []*FieldMapping, options *workflowAddInputOpts) *WorkflowNode {
	for _, input := range inputs {
		input.fromNodeKey = fromNodeKey
	}

	if options.noDirectDependency {
		n.addInputs = append(n.addInputs, func() error {
			var paths []FieldPath
			for _, input := range inputs {
				paths = append(paths, input.targetPath())
			}
			if err := n.checkAndAddMappedPath(paths); err != nil {
				return err
			}

			if err := n.g.addEdgeWithMappings(fromNodeKey, n.key, true, false, inputs...); err != nil {
				return err
			}
			n.dependencySetter(fromNodeKey, noDirectDependency)
			return nil
		})
	} else if options.dependencyWithoutInput {
		n.addInputs = append(n.addInputs, func() error {
			if len(inputs) > 0 {
				return fmt.Errorf("dependency without input should not have inputs. node: %s, fromNode: %s, inputs: %v", n.key, fromNodeKey, inputs)
			}
			if err := n.g.addEdgeWithMappings(fromNodeKey, n.key, false, true); err != nil {
				return err
			}
			n.dependencySetter(fromNodeKey, normalDependency)
			return nil
		})
	} else {
		n.addInputs = append(n.addInputs, func() error {
			var paths []FieldPath
			for _, input := range inputs {
				paths = append(paths, input.targetPath())
			}
			if err := n.checkAndAddMappedPath(paths); err != nil {
				return err
			}

			if err := n.g.addEdgeWithMappings(fromNodeKey, n.key, false, false, inputs...); err != nil {
				return err
			}
			n.dependencySetter(fromNodeKey, normalDependency)
			return nil
		})
	}

	return n
}

func (n *WorkflowNode) checkAndAddMappedPath(paths []FieldPath) error {
	if v, ok := n.mappedFieldPath[""]; ok {
		if _, ok = v.(struct{}); ok {
			return fmt.Errorf("entire output has already been mapped for node: %s", n.key)
		}
	} else {
		if len(paths) == 0 {
			n.mappedFieldPath[""] = struct{}{}
			return nil
		} else {
			n.mappedFieldPath[""] = map[string]any{}
		}
	}

	for _, targetPath := range paths {
		m := n.mappedFieldPath[""].(map[string]any)
		var traversed FieldPath
		for i, path := range targetPath {
			traversed = append(traversed, path)
			if v, ok := m[path]; ok {
				if _, ok = v.(struct{}); ok {
					return fmt.Errorf("two terminal field paths conflict for node %s: %v, %v", n.key, traversed, targetPath)
				}
			}

			if i < len(targetPath)-1 {
				m[path] = make(map[string]any)
				m = m[path].(map[string]any)
			} else {
				m[path] = struct{}{}
			}
		}
	}

	return nil
}

// WorkflowBranch represents a branch added to a workflow.
// Each branch may define its own end nodes and mappings.
type WorkflowBranch struct {
	fromNodeKey string
	*GraphBranch
}

// AddBranch adds a branch to the workflow.
//
// End Nodes Field Mappings:
// End nodes of the branch are required to define their own field mappings.
// This is a key distinction between Graph's Branch and Workflow's Branch:
// - Graph's Branch: Automatically passes its input to the selected node.
// - Workflow's Branch: Does not pass its input to the selected node.
func (wf *Workflow[I, O]) AddBranch(fromNodeKey string, branch *GraphBranch) *WorkflowBranch {
	wb := &WorkflowBranch{
		fromNodeKey: fromNodeKey,
		GraphBranch: branch,
	}

	wf.workflowBranches = append(wf.workflowBranches, wb)
	return wb
}

// AddEnd connects a node to END with optional field mappings.
// Deprecated: use *Workflow[I,O].End() to obtain a WorkflowNode instance for END, then work with it just like a normal WorkflowNode.
func (wf *Workflow[I, O]) AddEnd(fromNodeKey string, inputs ...*FieldMapping) *Workflow[I, O] {
	for _, input := range inputs {
		input.fromNodeKey = fromNodeKey
	}
	_ = wf.g.addEdgeWithMappings(fromNodeKey, END, false, false, inputs...)
	return wf
}

func (wf *Workflow[I, O]) compile(ctx context.Context, options *graphCompileOptions) (*composableRunnable, error) {
	if wf.g.buildError != nil {
		return nil, wf.g.buildError
	}

	for _, wb := range wf.workflowBranches {
		for endNode := range wb.endNodes {
			if endNode == END {
				if _, ok := wf.dependencies[END]; !ok {
					wf.dependencies[END] = make(map[string]dependencyType)
				}
				wf.dependencies[END][wb.fromNodeKey] = branchDependency
			} else {
				n := wf.workflowNodes[endNode]
				n.dependencySetter(wb.fromNodeKey, branchDependency)
			}
		}
		_ = wf.g.addBranch(wb.fromNodeKey, wb.GraphBranch, true)
	}

	for _, n := range wf.workflowNodes {
		for _, addInput := range n.addInputs {
			if err := addInput(); err != nil {
				return nil, err
			}
		}
		n.addInputs = nil
	}

	for _, n := range wf.workflowNodes {
		if len(n.staticValues) > 0 {
			value := make(map[string]any, len(n.staticValues))
			var paths []FieldPath
			for path, v := range n.staticValues {
				value[path] = v
				paths = append(paths, splitFieldPath(path))
			}

			if err := n.checkAndAddMappedPath(paths); err != nil {
				return nil, err
			}

			pair := handlerPair{
				invoke: func(in any) (any, error) {
					values := []any{in, value}
					return mergeValues(values, nil)
				},
				transform: func(in streamReader) streamReader {
					sr := schema.StreamReaderFromArray([]map[string]any{value})
					newS, err := mergeValues([]any{in, packStreamReader(sr)}, nil)
					if err != nil {
						errSR, errSW := schema.Pipe[map[string]any](1)
						errSW.Send(nil, err)
						errSW.Close()
						return packStreamReader(errSR)
					}

					return newS.(streamReader)
				},
			}

			for i := range paths {
				wf.g.fieldMappingRecords[n.key] = append(wf.g.fieldMappingRecords[n.key], ToFieldPath(paths[i]))
			}

			wf.g.handlerPreNode[n.key] = []handlerPair{pair}
		}
	}

	// TODO: check indirect edges are legal

	return wf.g.compile(ctx, options)
}

func (wf *Workflow[I, O]) initNode(key string) *WorkflowNode {
	n := &WorkflowNode{
		g:            wf.g,
		key:          key,
		staticValues: make(map[string]any),
		dependencySetter: func(fromNodeKey string, typ dependencyType) {
			if _, ok := wf.dependencies[key]; !ok {
				wf.dependencies[key] = make(map[string]dependencyType)
			}
			wf.dependencies[key][fromNodeKey] = typ
		},
		mappedFieldPath: make(map[string]any),
	}
	wf.workflowNodes[key] = n
	return n
}

func (wf *Workflow[I, O]) getGenericHelper() *genericHelper {
	return wf.g.getGenericHelper()
}

func (wf *Workflow[I, O]) inputType() reflect.Type {
	return wf.g.inputType()
}

func (wf *Workflow[I, O]) outputType() reflect.Type {
	return wf.g.outputType()
}

func (wf *Workflow[I, O]) component() component {
	return wf.g.component()
}
