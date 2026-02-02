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
	"github.com/cloudwego/eino/internal/generic"
	"github.com/cloudwego/eino/internal/gmap"
)

// START is the start node of the graph. You can add your first edge with START.
const START = "start"

// END is the end node of the graph. You can add your last edge with END.
const END = "end"

// graphRunType is a custom type used to control the running mode of the graph.
type graphRunType string

const (
	// runTypePregel is a running mode of the graph that is suitable for large-scale graph processing tasks. Can have cycles in graph. Compatible with NodeTriggerType.AnyPredecessor.
	runTypePregel graphRunType = "Pregel"
	// runTypeDAG is a running mode of the graph that represents the graph as a directed acyclic graph, suitable for tasks that can be represented as a directed acyclic graph. Compatible with NodeTriggerType.AllPredecessor.
	runTypeDAG graphRunType = "DAG"
)

// String returns the string representation of the graph run type.
func (g graphRunType) String() string {
	return string(g)
}

type graph struct {
	nodes        map[string]*graphNode
	controlEdges map[string][]string
	dataEdges    map[string][]string
	branches     map[string][]*GraphBranch
	startNodes   []string
	endNodes     []string

	toValidateMap map[string][]struct {
		endNode  string
		mappings []*FieldMapping
	}

	stateType      reflect.Type
	stateGenerator func(ctx context.Context) any
	newOpts        []NewGraphOption

	expectedInputType, expectedOutputType reflect.Type

	*genericHelper

	fieldMappingRecords map[string][]*FieldMapping

	buildError error

	cmp component

	compiled bool

	handlerOnEdges   map[string]map[string][]handlerPair
	handlerPreNode   map[string][]handlerPair
	handlerPreBranch map[string][][]handlerPair
}

type newGraphConfig struct {
	inputType, outputType reflect.Type
	gh                    *genericHelper
	cmp                   component
	stateType             reflect.Type
	stateGenerator        func(ctx context.Context) any
	newOpts               []NewGraphOption
}

func newGraphFromGeneric[I, O any](
	cmp component,
	stateGenerator func(ctx context.Context) any,
	stateType reflect.Type,
	opts []NewGraphOption,
) *graph {
	return newGraph(&newGraphConfig{
		inputType:      generic.TypeOf[I](),
		outputType:     generic.TypeOf[O](),
		gh:             newGenericHelper[I, O](),
		cmp:            cmp,
		stateType:      stateType,
		stateGenerator: stateGenerator,
		newOpts:        opts,
	})
}

func newGraph(cfg *newGraphConfig) *graph {
	return &graph{
		nodes:        make(map[string]*graphNode),
		dataEdges:    make(map[string][]string),
		controlEdges: make(map[string][]string),
		branches:     make(map[string][]*GraphBranch),

		toValidateMap: make(map[string][]struct {
			endNode  string
			mappings []*FieldMapping
		}),

		expectedInputType:  cfg.inputType,
		expectedOutputType: cfg.outputType,
		genericHelper:      cfg.gh,

		fieldMappingRecords: make(map[string][]*FieldMapping),

		cmp: cfg.cmp,

		stateType:      cfg.stateType,
		stateGenerator: cfg.stateGenerator,
		newOpts:        cfg.newOpts,

		handlerOnEdges:   make(map[string]map[string][]handlerPair),
		handlerPreNode:   make(map[string][]handlerPair),
		handlerPreBranch: make(map[string][][]handlerPair),
	}
}

func (g *graph) component() component {
	return g.cmp
}

func isChain(cmp component) bool {
	return cmp == ComponentOfChain
}

func isWorkflow(cmp component) bool {
	return cmp == ComponentOfWorkflow
}

// ErrGraphCompiled is returned when attempting to modify a graph after it has been compiled
var ErrGraphCompiled = errors.New("graph has been compiled, cannot be modified")

func (g *graph) addNode(key string, node *graphNode, options *graphAddNodeOpts) (err error) {
	if g.buildError != nil {
		return g.buildError
	}

	if g.compiled {
		return ErrGraphCompiled
	}

	defer func() {
		if err != nil {
			g.buildError = err
		}
	}()

	if key == END || key == START {
		return fmt.Errorf("node '%s' is reserved, cannot add manually", key)
	}

	if _, ok := g.nodes[key]; ok {
		return fmt.Errorf("node '%s' already present", key)
	}

	// check options
	if options.needState {
		if g.stateGenerator == nil {
			return fmt.Errorf("node '%s' needs state but graph state is not enabled", key)
		}
	}

	if options.nodeOptions.nodeKey != "" {
		if !isChain(g.cmp) {
			return errors.New("only chain support node key option")
		}
	}
	// end: check options

	// check pre- / post-handler type
	if options.processor != nil {
		if options.processor.statePreHandler != nil {
			// check state type
			if g.stateType != options.processor.preStateType {
				return fmt.Errorf("node[%s]'s pre handler state type[%v] is different from graph[%v]", key, options.processor.preStateType, g.stateType)
			}
			// check input type
			if node.inputType() == nil && options.processor.statePreHandler.outputType != reflect.TypeOf((*any)(nil)).Elem() {
				return fmt.Errorf("passthrough node[%s]'s pre handler type isn't any", key)
			} else if node.inputType() != nil && node.inputType() != options.processor.statePreHandler.outputType {
				return fmt.Errorf("node[%s]'s pre handler type[%v] is different from its input type[%v]", key, options.processor.statePreHandler.outputType, node.inputType())
			}
		}
		if options.processor.statePostHandler != nil {
			// check state type
			if g.stateType != options.processor.postStateType {
				return fmt.Errorf("node[%s]'s post handler state type[%v] is different from graph[%v]", key, options.processor.postStateType, g.stateType)
			}
			// check input type
			if node.outputType() == nil && options.processor.statePostHandler.inputType != reflect.TypeOf((*any)(nil)).Elem() {
				return fmt.Errorf("passthrough node[%s]'s post handler type isn't any", key)
			} else if node.outputType() != nil && node.outputType() != options.processor.statePostHandler.inputType {
				return fmt.Errorf("node[%s]'s post handler type[%v] is different from its output type[%v]", key, options.processor.statePostHandler.inputType, node.outputType())
			}
		}
	}

	g.nodes[key] = node

	return nil
}

func (g *graph) addEdgeWithMappings(startNode, endNode string, noControl bool, noData bool, mappings ...*FieldMapping) (err error) {
	if g.buildError != nil {
		return g.buildError
	}
	if g.compiled {
		return ErrGraphCompiled
	}

	if noControl && noData {
		return fmt.Errorf("edge[%s]-[%s] cannot be both noDirectDependency and noDataFlow", startNode, endNode)
	}

	defer func() {
		if err != nil {
			g.buildError = err
		}
	}()
	if startNode == END {
		return errors.New("END cannot be a start node")
	}
	if endNode == START {
		return errors.New("START cannot be an end node")
	}

	if _, ok := g.nodes[startNode]; !ok && startNode != START {
		return fmt.Errorf("edge start node '%s' needs to be added to graph first", startNode)
	}
	if _, ok := g.nodes[endNode]; !ok && endNode != END {
		return fmt.Errorf("edge end node '%s' needs to be added to graph first", endNode)
	}

	if !noControl {
		for i := range g.controlEdges[startNode] {
			if g.controlEdges[startNode][i] == endNode {
				return fmt.Errorf("control edge[%s]-[%s] have been added yet", startNode, endNode)
			}
		}

		g.controlEdges[startNode] = append(g.controlEdges[startNode], endNode)
		if startNode == START {
			g.startNodes = append(g.startNodes, endNode)
		}
		if endNode == END {
			g.endNodes = append(g.endNodes, startNode)
		}
	}
	if !noData {
		for i := range g.dataEdges[startNode] {
			if g.dataEdges[startNode][i] == endNode {
				return fmt.Errorf("data edge[%s]-[%s] have been added yet", startNode, endNode)
			}
		}

		g.addToValidateMap(startNode, endNode, mappings)
		err = g.updateToValidateMap()
		if err != nil {
			return err
		}
		g.dataEdges[startNode] = append(g.dataEdges[startNode], endNode)
	}

	return nil
}

// AddEmbeddingNode adds a node that implements embedding.Embedder.
// e.g.
//
//	embeddingNode, err := openai.NewEmbedder(ctx, &openai.EmbeddingConfig{
//		Model: "text-embedding-3-small",
//	})
//
//	graph.AddEmbeddingNode("embedding_node_key", embeddingNode)
func (g *graph) AddEmbeddingNode(key string, node embedding.Embedder, opts ...GraphAddNodeOpt) error {
	gNode, options := toEmbeddingNode(node, opts...)
	return g.addNode(key, gNode, options)
}

// AddRetrieverNode adds a node that implements retriever.Retriever.
// e.g.
//
//	retriever, err := vikingdb.NewRetriever(ctx, &vikingdb.RetrieverConfig{})
//
//	graph.AddRetrieverNode("retriever_node_key", retrieverNode)
func (g *graph) AddRetrieverNode(key string, node retriever.Retriever, opts ...GraphAddNodeOpt) error {
	gNode, options := toRetrieverNode(node, opts...)
	return g.addNode(key, gNode, options)
}

// AddLoaderNode adds a node that implements document.Loader.
// e.g.
//
//	loader, err := file.NewLoader(ctx, &file.LoaderConfig{})
//
//	graph.AddLoaderNode("loader_node_key", loader)
func (g *graph) AddLoaderNode(key string, node document.Loader, opts ...GraphAddNodeOpt) error {
	gNode, options := toLoaderNode(node, opts...)
	return g.addNode(key, gNode, options)
}

// AddIndexerNode adds a node that implements indexer.Indexer.
// e.g.
//
//	indexer, err := vikingdb.NewIndexer(ctx, &vikingdb.IndexerConfig{})
//
//	graph.AddIndexerNode("indexer_node_key", indexer)
func (g *graph) AddIndexerNode(key string, node indexer.Indexer, opts ...GraphAddNodeOpt) error {
	gNode, options := toIndexerNode(node, opts...)
	return g.addNode(key, gNode, options)
}

// AddChatModelNode add node that implements model.BaseChatModel.
// e.g.
//
//	chatModel, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
//		Model: "gpt-4o",
//	})
//
//	graph.AddChatModelNode("chat_model_node_key", chatModel)
func (g *graph) AddChatModelNode(key string, node model.BaseChatModel, opts ...GraphAddNodeOpt) error {
	gNode, options := toChatModelNode(node, opts...)
	return g.addNode(key, gNode, options)
}

// AddChatTemplateNode add node that implements prompt.ChatTemplate.
// e.g.
//
//	chatTemplate, err := prompt.FromMessages(schema.FString, &schema.Message{
//		Role:    schema.System,
//		Content: "You are acting as a {role}.",
//	})
//
//	graph.AddChatTemplateNode("chat_template_node_key", chatTemplate)
func (g *graph) AddChatTemplateNode(key string, node prompt.ChatTemplate, opts ...GraphAddNodeOpt) error {
	gNode, options := toChatTemplateNode(node, opts...)
	return g.addNode(key, gNode, options)
}

// AddToolsNode adds a node that implements tools.ToolsNode.
// e.g.
//
//	toolsNode, err := tools.NewToolNode(ctx, &tools.ToolsNodeConfig{})
//
//	graph.AddToolsNode("tools_node_key", toolsNode)
func (g *graph) AddToolsNode(key string, node *ToolsNode, opts ...GraphAddNodeOpt) error {
	gNode, options := toToolsNode(node, opts...)
	return g.addNode(key, gNode, options)
}

// AddDocumentTransformerNode adds a node that implements document.Transformer.
// e.g.
//
//	markdownSplitter, err := markdown.NewHeaderSplitter(ctx, &markdown.HeaderSplitterConfig{})
//
//	graph.AddDocumentTransformerNode("document_transformer_node_key", markdownSplitter)
func (g *graph) AddDocumentTransformerNode(key string, node document.Transformer, opts ...GraphAddNodeOpt) error {
	gNode, options := toDocumentTransformerNode(node, opts...)
	return g.addNode(key, gNode, options)
}

// AddLambdaNode add node that implements at least one of Invoke[I, O], Stream[I, O], Collect[I, O], Transform[I, O].
// due to the lack of supporting method generics, we need to use function generics to generate Lambda run as Runnable[I, O].
// for Invoke[I, O], use compose.InvokableLambda()
// for Stream[I, O], use compose.StreamableLambda()
// for Collect[I, O], use compose.CollectableLambda()
// for Transform[I, O], use compose.TransformableLambda()
// for arbitrary combinations of 4 kinds of lambda, use compose.AnyLambda()
func (g *graph) AddLambdaNode(key string, node *Lambda, opts ...GraphAddNodeOpt) error {
	gNode, options := toLambdaNode(node, opts...)
	return g.addNode(key, gNode, options)
}

// AddGraphNode add one kind of Graph[I, O]、Chain[I, O]、StateChain[I, O, S] as a node.
// for Graph[I, O], comes from NewGraph[I, O]()
// for Chain[I, O], comes from NewChain[I, O]()
func (g *graph) AddGraphNode(key string, node AnyGraph, opts ...GraphAddNodeOpt) error {
	gNode, options := toAnyGraphNode(node, opts...)
	return g.addNode(key, gNode, options)
}

// AddPassthroughNode adds a passthrough node to the graph.
// mostly used in pregel mode of graph.
// e.g.
//
//	graph.AddPassthroughNode("passthrough_node_key")
func (g *graph) AddPassthroughNode(key string, opts ...GraphAddNodeOpt) error {
	gNode, options := toPassthroughNode(opts...)
	return g.addNode(key, gNode, options)
}

// AddBranch adds a branch to the graph.
// e.g.
//
//	condition := func(ctx context.Context, in string) (string, error) {
//		return "next_node_key", nil
//	}
//	endNodes := map[string]bool{"path01": true, "path02": true}
//	branch := compose.NewGraphBranch(condition, endNodes)
//
//	graph.AddBranch("start_node_key", branch)
func (g *graph) AddBranch(startNode string, branch *GraphBranch) (err error) {
	return g.addBranch(startNode, branch, false)
}

func (g *graph) addBranch(startNode string, branch *GraphBranch, skipData bool) (err error) {
	if g.buildError != nil {
		return g.buildError
	}

	if g.compiled {
		return ErrGraphCompiled
	}

	defer func() {
		if err != nil {
			g.buildError = err
		}
	}()

	if startNode == END {
		return errors.New("END cannot be a start node")
	}

	if _, ok := g.nodes[startNode]; !ok && startNode != START {
		return fmt.Errorf("branch start node '%s' needs to be added to graph first", startNode)
	}

	if _, ok := g.handlerPreBranch[startNode]; !ok {
		g.handlerPreBranch[startNode] = [][]handlerPair{}
	}
	branch.idx = len(g.handlerPreBranch[startNode])

	if startNode != START && g.nodes[startNode].executorMeta.component == ComponentOfPassthrough {
		g.nodes[startNode].cr.inputType = branch.inputType
		g.nodes[startNode].cr.outputType = branch.inputType
		g.nodes[startNode].cr.genericHelper = branch.genericHelper.forPredecessorPassthrough()
	}

	// check branch condition type
	result := checkAssignable(g.getNodeOutputType(startNode), branch.inputType)
	if result == assignableTypeMustNot {
		return fmt.Errorf("condition's input type[%s] and start node[%s]'s output type[%s] are mismatched", branch.inputType.String(), startNode, g.getNodeOutputType(startNode).String())
	} else if result == assignableTypeMay {
		g.handlerPreBranch[startNode] = append(g.handlerPreBranch[startNode], []handlerPair{branch.inputConverter})
	} else {
		g.handlerPreBranch[startNode] = append(g.handlerPreBranch[startNode], []handlerPair{})
	}

	if !skipData {
		for endNode := range branch.endNodes {
			if _, ok := g.nodes[endNode]; !ok {
				if endNode != END {
					return fmt.Errorf("branch end node '%s' needs to be added to graph first", endNode)
				}
			}

			g.addToValidateMap(startNode, endNode, nil)
			e := g.updateToValidateMap()
			if e != nil {
				return e
			}

			if startNode == START {
				g.startNodes = append(g.startNodes, endNode)
			}
			if endNode == END {
				g.endNodes = append(g.endNodes, startNode)
			}
		}
	} else {
		for endNode := range branch.endNodes {
			if startNode == START {
				g.startNodes = append(g.startNodes, endNode)
			}
			if endNode == END {
				g.endNodes = append(g.endNodes, startNode)
			}
		}
		branch.noDataFlow = true
	}

	g.branches[startNode] = append(g.branches[startNode], branch)

	return nil
}

func (g *graph) addToValidateMap(startNode, endNode string, mapping []*FieldMapping) {
	g.toValidateMap[startNode] = append(g.toValidateMap[startNode], struct {
		endNode  string
		mappings []*FieldMapping
	}{endNode: endNode, mappings: mapping})
}

// updateToValidateMap after update node, check validate map
// check again if nodes in toValidateMap have been updated. because when there are multiple linked passthrough nodes, in the worst scenario, only one node can be updated at a time.
func (g *graph) updateToValidateMap() error {
	var startNodeOutputType, endNodeInputType reflect.Type
	for {
		hasChanged := false
		for startNode := range g.toValidateMap {
			startNodeOutputType = g.getNodeOutputType(startNode)

			for i := 0; i < len(g.toValidateMap[startNode]); i++ {
				endNode := g.toValidateMap[startNode][i]

				endNodeInputType = g.getNodeInputType(endNode.endNode)
				if startNodeOutputType == nil && endNodeInputType == nil {
					continue
				}

				// update toValidateMap
				g.toValidateMap[startNode] = append(g.toValidateMap[startNode][:i], g.toValidateMap[startNode][i+1:]...)
				i--

				hasChanged = true
				// assume that START and END type isn't empty
				if startNodeOutputType != nil && endNodeInputType == nil {
					g.nodes[endNode.endNode].cr.inputType = startNodeOutputType
					g.nodes[endNode.endNode].cr.outputType = g.nodes[endNode.endNode].cr.inputType
					g.nodes[endNode.endNode].cr.genericHelper = g.getNodeGenericHelper(startNode).forSuccessorPassthrough()
				} else if startNodeOutputType == nil /* redundant condition && endNodeInputType != nil */ {
					g.nodes[startNode].cr.inputType = endNodeInputType
					g.nodes[startNode].cr.outputType = g.nodes[startNode].cr.inputType
					g.nodes[startNode].cr.genericHelper = g.getNodeGenericHelper(endNode.endNode).forPredecessorPassthrough()
				} else if len(endNode.mappings) == 0 {
					// common node check
					result := checkAssignable(startNodeOutputType, endNodeInputType)
					if result == assignableTypeMustNot {
						return fmt.Errorf("graph edge[%s]-[%s]: start node's output type[%s] and end node's input type[%s] mismatch",
							startNode, endNode.endNode, startNodeOutputType.String(), endNodeInputType.String())
					} else if result == assignableTypeMay {
						// add runtime check edges
						if _, ok := g.handlerOnEdges[startNode]; !ok {
							g.handlerOnEdges[startNode] = make(map[string][]handlerPair)
						}
						g.handlerOnEdges[startNode][endNode.endNode] = append(g.handlerOnEdges[startNode][endNode.endNode], g.getNodeGenericHelper(endNode.endNode).inputConverter)
					}
					continue
				}

				if len(endNode.mappings) > 0 {
					if _, ok := g.handlerOnEdges[startNode]; !ok {
						g.handlerOnEdges[startNode] = make(map[string][]handlerPair)
					}
					g.fieldMappingRecords[endNode.endNode] = append(g.fieldMappingRecords[endNode.endNode], endNode.mappings...)

					// field mapping check
					checker, uncheckedSourcePaths, err := validateFieldMapping(g.getNodeOutputType(startNode), g.getNodeInputType(endNode.endNode), endNode.mappings)
					if err != nil {
						return err
					}

					g.handlerOnEdges[startNode][endNode.endNode] = append(g.handlerOnEdges[startNode][endNode.endNode], handlerPair{
						invoke: func(value any) (any, error) {
							return fieldMap(endNode.mappings, false, uncheckedSourcePaths)(value)
						},
						transform: streamFieldMap(endNode.mappings, uncheckedSourcePaths),
					})

					if checker != nil {
						g.handlerOnEdges[startNode][endNode.endNode] = append(g.handlerOnEdges[startNode][endNode.endNode], *checker)
					}
				}
			}
		}
		if !hasChanged {
			break
		}
	}

	return nil
}

func (g *graph) getNodeGenericHelper(name string) *genericHelper {
	if name == START {
		return g.genericHelper.forPredecessorPassthrough()
	} else if name == END {
		return g.genericHelper.forSuccessorPassthrough()
	}
	return g.nodes[name].getGenericHelper()
}

func (g *graph) getNodeInputType(name string) reflect.Type {
	if name == START {
		return g.inputType()
	} else if name == END {
		return g.outputType()
	}
	return g.nodes[name].inputType()
}

func (g *graph) getNodeOutputType(name string) reflect.Type {
	if name == START {
		return g.inputType()
	} else if name == END {
		return g.outputType()
	}
	return g.nodes[name].outputType()
}

func (g *graph) inputType() reflect.Type {
	return g.expectedInputType
}

func (g *graph) outputType() reflect.Type {
	return g.expectedOutputType
}

func (g *graph) compile(ctx context.Context, opt *graphCompileOptions) (*composableRunnable, error) {
	if g.buildError != nil {
		return nil, g.buildError
	}

	// get run type
	runType := runTypePregel
	cb := pregelChannelBuilder
	if isChain(g.cmp) || isWorkflow(g.cmp) {
		if opt != nil && opt.nodeTriggerMode != "" {
			return nil, errors.New(fmt.Sprintf("%s doesn't support node trigger mode option", g.cmp))
		}
	}
	if (opt != nil && opt.nodeTriggerMode == AllPredecessor) || isWorkflow(g.cmp) {
		runType = runTypeDAG
		cb = dagChannelBuilder
	}

	// get eager type
	eager := false
	if isWorkflow(g.cmp) || runType == runTypeDAG {
		eager = true
	}
	if opt != nil && opt.eagerDisabled {
		eager = false
	}

	if len(g.startNodes) == 0 {
		return nil, errors.New("start node not set")
	}
	if len(g.endNodes) == 0 {
		return nil, errors.New("end node not set")
	}

	// toValidateMap isn't empty means there are nodes that cannot infer type
	for _, v := range g.toValidateMap {
		if len(v) > 0 {
			return nil, fmt.Errorf("some node's input or output types cannot be inferred: %v", g.toValidateMap)
		}
	}

	for key := range g.fieldMappingRecords {
		// not allowed to map multiple fields to the same field
		toMap := make(map[string]bool)
		for _, mapping := range g.fieldMappingRecords[key] {
			if _, ok := toMap[mapping.to]; ok {
				return nil, fmt.Errorf("duplicate mapping target field: %s of node[%s]", mapping.to, key)
			}
			toMap[mapping.to] = true
		}

		// add map to input converter
		g.handlerPreNode[key] = append(g.handlerPreNode[key], g.getNodeGenericHelper(key).inputFieldMappingConverter)
	}

	key2SubGraphs := g.beforeChildGraphsCompile(opt)
	chanSubscribeTo := make(map[string]*chanCall)
	for name, node := range g.nodes {
		node.beforeChildGraphCompile(name, key2SubGraphs)

		r, err := node.compileIfNeeded(ctx)
		if err != nil {
			return nil, err
		}

		chCall := &chanCall{
			action:   r,
			writeTo:  g.dataEdges[name],
			controls: g.controlEdges[name],

			preProcessor:  node.nodeInfo.preProcessor,
			postProcessor: node.nodeInfo.postProcessor,
		}

		branches := g.branches[name]
		if len(branches) > 0 {
			branchRuns := make([]*GraphBranch, 0, len(branches))
			branchRuns = append(branchRuns, branches...)

			chCall.writeToBranches = branchRuns
		}

		chanSubscribeTo[name] = chCall
	}

	dataPredecessors := make(map[string][]string)
	controlPredecessors := make(map[string][]string)
	for start, ends := range g.controlEdges {
		for _, end := range ends {
			if _, ok := controlPredecessors[end]; !ok {
				controlPredecessors[end] = []string{start}
			} else {
				controlPredecessors[end] = append(controlPredecessors[end], start)
			}
		}
	}
	for start, ends := range g.dataEdges {
		for _, end := range ends {
			if _, ok := dataPredecessors[end]; !ok {
				dataPredecessors[end] = []string{start}
			} else {
				dataPredecessors[end] = append(dataPredecessors[end], start)
			}
		}
	}
	for start, branches := range g.branches {
		for _, branch := range branches {
			for end := range branch.endNodes {
				if _, ok := controlPredecessors[end]; !ok {
					controlPredecessors[end] = []string{start}
				} else {
					controlPredecessors[end] = append(controlPredecessors[end], start)
				}

				if !branch.noDataFlow {
					if _, ok := dataPredecessors[end]; !ok {
						dataPredecessors[end] = []string{start}
					} else {
						dataPredecessors[end] = append(dataPredecessors[end], start)
					}
				}
			}
		}
	}

	inputChannels := &chanCall{
		writeTo:         g.dataEdges[START],
		controls:        g.controlEdges[START],
		writeToBranches: make([]*GraphBranch, len(g.branches[START])),
	}
	copy(inputChannels.writeToBranches, g.branches[START])

	var mergeConfigs map[string]FanInMergeConfig
	if opt != nil {
		mergeConfigs = opt.mergeConfigs
	}
	if mergeConfigs == nil {
		mergeConfigs = make(map[string]FanInMergeConfig)
	}

	r := &runner{
		chanSubscribeTo:     chanSubscribeTo,
		controlPredecessors: controlPredecessors,
		dataPredecessors:    dataPredecessors,

		inputChannels: inputChannels,

		eager: eager,

		chanBuilder: cb,

		inputType:     g.inputType(),
		outputType:    g.outputType(),
		genericHelper: g.genericHelper,

		preBranchHandlerManager: &preBranchHandlerManager{h: g.handlerPreBranch},
		preNodeHandlerManager:   &preNodeHandlerManager{h: g.handlerPreNode},
		edgeHandlerManager:      &edgeHandlerManager{h: g.handlerOnEdges},

		mergeConfigs: mergeConfigs,
	}

	successors := make(map[string][]string)
	for ch := range r.chanSubscribeTo {
		successors[ch] = getSuccessors(r.chanSubscribeTo[ch])
	}
	r.successors = successors

	if g.stateGenerator != nil {
		r.runCtx = func(ctx context.Context) context.Context {
			var parent *internalState
			if p, ok := ctx.Value(stateKey{}).(*internalState); ok {
				parent = p
			}

			return context.WithValue(ctx, stateKey{}, &internalState{
				state:  g.stateGenerator(ctx),
				parent: parent,
			})
		}
	}

	if runType == runTypeDAG {
		err := validateDAG(r.chanSubscribeTo, controlPredecessors)
		if err != nil {
			return nil, err
		}
		r.dag = true
	}

	if opt != nil {
		inputPairs := make(map[string]streamConvertPair)
		outputPairs := make(map[string]streamConvertPair)
		for key, c := range r.chanSubscribeTo {
			inputPairs[key] = c.action.inputStreamConvertPair
			outputPairs[key] = c.action.outputStreamConvertPair
		}
		inputPairs[END] = r.outputConvertStreamPair
		outputPairs[START] = r.inputConvertStreamPair
		r.checkPointer = newCheckPointer(inputPairs, outputPairs, opt.checkPointStore, opt.serializer)

		r.interruptBeforeNodes = opt.interruptBeforeNodes
		r.interruptAfterNodes = opt.interruptAfterNodes
		r.options = *opt
	}

	// default options
	if r.dag && r.options.maxRunSteps > 0 {
		return nil, fmt.Errorf("cannot set max run steps in dag mode")
	} else if !r.dag && r.options.maxRunSteps == 0 {
		r.options.maxRunSteps = len(r.chanSubscribeTo) + 10
	}

	g.compiled = true

	g.onCompileFinish(ctx, opt, key2SubGraphs)

	return r.toComposableRunnable(), nil
}

func getSuccessors(c *chanCall) []string {
	ret := make([]string, len(c.writeTo))
	copy(ret, c.writeTo)
	ret = append(ret, c.controls...)
	for _, branch := range c.writeToBranches {
		for node := range branch.endNodes {
			ret = append(ret, node)
		}
	}
	return uniqueSlice(ret)
}

func uniqueSlice(s []string) []string {
	seen := make(map[string]struct{}, len(s))
	cur := 0
	for i := range s {
		if _, ok := seen[s[i]]; !ok {
			seen[s[i]] = struct{}{}
			s[cur] = s[i]
			cur++
		}
	}
	return s[:cur]
}

type subGraphCompileCallback struct {
	closure func(ctx context.Context, info *GraphInfo)
}

// OnFinish is called when the graph is compiled.
func (s *subGraphCompileCallback) OnFinish(ctx context.Context, info *GraphInfo) {
	s.closure(ctx, info)
}

func (g *graph) beforeChildGraphsCompile(opt *graphCompileOptions) map[string]*GraphInfo {
	if opt == nil || len(opt.callbacks) == 0 {
		return nil
	}

	return make(map[string]*GraphInfo)
}

func (gn *graphNode) beforeChildGraphCompile(nodeKey string, key2SubGraphs map[string]*GraphInfo) {
	if gn.g == nil || key2SubGraphs == nil {
		return
	}

	subGraphCallback := func(ctx2 context.Context, subGraph *GraphInfo) {
		key2SubGraphs[nodeKey] = subGraph
	}

	gn.nodeInfo.compileOption.callbacks = append(gn.nodeInfo.compileOption.callbacks, &subGraphCompileCallback{closure: subGraphCallback})
}

func (g *graph) toGraphInfo(opt *graphCompileOptions, key2SubGraphs map[string]*GraphInfo) *GraphInfo {
	gInfo := &GraphInfo{
		CompileOptions: opt.origOpts,
		Nodes:          make(map[string]GraphNodeInfo, len(g.nodes)),
		Edges:          gmap.Clone(g.controlEdges),
		DataEdges:      gmap.Clone(g.dataEdges),
		Branches: gmap.Map(g.branches, func(startNode string, branches []*GraphBranch) (string, []GraphBranch) {
			branchInfo := make([]GraphBranch, 0, len(branches))
			for _, b := range branches {
				branchInfo = append(branchInfo, GraphBranch{
					invoke:        b.invoke,
					collect:       b.collect,
					inputType:     b.inputType,
					genericHelper: b.genericHelper,
					endNodes:      gmap.Clone(b.endNodes),
				})
			}
			return startNode, branchInfo
		}),
		InputType:       g.expectedInputType,
		OutputType:      g.expectedOutputType,
		Name:            opt.graphName,
		GenStateFn:      g.stateGenerator,
		NewGraphOptions: g.newOpts,
	}

	for key := range g.nodes {
		gNode := g.nodes[key]
		if gNode.executorMeta.component == ComponentOfPassthrough {
			gInfo.Nodes[key] = GraphNodeInfo{
				Component:        gNode.executorMeta.component,
				GraphAddNodeOpts: gNode.opts,
				InputType:        gNode.cr.inputType,
				OutputType:       gNode.cr.outputType,
				Name:             gNode.nodeInfo.name,
				InputKey:         gNode.cr.nodeInfo.inputKey,
				OutputKey:        gNode.cr.nodeInfo.outputKey,
			}
			continue
		}

		gNodeInfo := &GraphNodeInfo{
			Component:        gNode.executorMeta.component,
			Instance:         gNode.instance,
			GraphAddNodeOpts: gNode.opts,
			InputType:        gNode.cr.inputType,
			OutputType:       gNode.cr.outputType,
			Name:             gNode.nodeInfo.name,
			InputKey:         gNode.cr.nodeInfo.inputKey,
			OutputKey:        gNode.cr.nodeInfo.outputKey,
			Mappings:         g.fieldMappingRecords[key],
		}

		if gi, ok := key2SubGraphs[key]; ok {
			gNodeInfo.GraphInfo = gi
		}

		gInfo.Nodes[key] = *gNodeInfo
	}

	return gInfo
}

func (g *graph) onCompileFinish(ctx context.Context, opt *graphCompileOptions, key2SubGraphs map[string]*GraphInfo) {
	if opt == nil {
		return
	}

	if len(opt.callbacks) == 0 {
		return
	}

	gInfo := g.toGraphInfo(opt, key2SubGraphs)

	for _, cb := range opt.callbacks {
		cb.OnFinish(ctx, gInfo)
	}
}

func (g *graph) getGenericHelper() *genericHelper {
	return g.genericHelper
}

func (g *graph) GetType() string {
	return ""
}

func transferTask(script [][]string, invertedEdges map[string][]string) [][]string {
	utilMap := map[string]bool{}
	for i := len(script) - 1; i >= 0; i-- {
		for j := 0; j < len(script[i]); j++ {
			// deduplicate
			if _, ok := utilMap[script[i][j]]; ok {
				script[i] = append(script[i][:j], script[i][j+1:]...)
				j--
				continue
			}
			utilMap[script[i][j]] = true

			target := i
			for k := i + 1; k < len(script); k++ {
				hasDependencies := false
				for l := range script[k] {
					for _, dependency := range invertedEdges[script[i][j]] {
						if script[k][l] == dependency {
							hasDependencies = true
							break
						}
					}
					if hasDependencies {
						break
					}
				}
				if hasDependencies {
					break
				}
				target = k
			}
			if target != i {
				script[target] = append(script[target], script[i][j])
				script[i] = append(script[i][:j], script[i][j+1:]...)
				j--
			}
		}
	}

	return script
}

func validateDAG(chanSubscribeTo map[string]*chanCall, controlPredecessors map[string][]string) error {
	m := map[string]int{}
	for node := range chanSubscribeTo {
		if edges, ok := controlPredecessors[node]; ok {
			m[node] = len(edges)
			for _, pre := range edges {
				if pre == START {
					m[node] -= 1
				}
			}
		} else {
			m[node] = 0
		}
	}
	hasChanged := true
	for hasChanged {
		hasChanged = false
		for node := range m {
			if m[node] == 0 {
				hasChanged = true
				for _, subNode := range chanSubscribeTo[node].controls {
					if subNode == END {
						continue
					}
					m[subNode]--
				}
				for _, subBranch := range chanSubscribeTo[node].writeToBranches {
					for subNode := range subBranch.endNodes {
						if subNode == END {
							continue
						}
						m[subNode]--
					}
				}
				m[node] = -1
			}
		}
	}

	var loopStarts []string
	for k, v := range m {
		if v > 0 {
			loopStarts = append(loopStarts, k)
		}
	}
	if len(loopStarts) > 0 {
		return fmt.Errorf("%w: %s", DAGInvalidLoopErr, formatLoops(findLoops(loopStarts, chanSubscribeTo)))
	}
	return nil
}

// DAGInvalidLoopErr indicates the graph contains a cycle and is invalid.
var DAGInvalidLoopErr = errors.New("DAG is invalid, has loop")

func findLoops(startNodes []string, chanCalls map[string]*chanCall) [][]string {
	controlSuccessors := map[string][]string{}
	for node, ch := range chanCalls {
		controlSuccessors[node] = append(controlSuccessors[node], ch.controls...)
		for _, b := range ch.writeToBranches {
			for end := range b.endNodes {
				controlSuccessors[node] = append(controlSuccessors[node], end)
			}
		}
	}

	visited := map[string]bool{}
	var dfs func(path []string) [][]string
	dfs = func(path []string) [][]string {
		var ret [][]string
		pathEnd := path[len(path)-1]
		successors, ok := controlSuccessors[pathEnd]
		if !ok {
			return nil
		}
		for _, successor := range successors {
			visited[successor] = true

			if successor == END {
				continue
			}

			var looped bool
			for i, node := range path {
				if node == successor {
					ret = append(ret, append(path[i:], successor))
					looped = true
					break
				}
			}
			if looped {
				continue
			}

			ret = append(ret, dfs(append(path, successor))...)
		}
		return ret
	}

	var ret [][]string
	for _, node := range startNodes {
		if !visited[node] {
			ret = append(ret, dfs([]string{node})...)
		}
	}
	return ret
}

func formatLoops(loops [][]string) string {
	sb := strings.Builder{}
	for _, loop := range loops {
		if len(loop) == 0 {
			continue
		}
		sb.WriteString("[")
		sb.WriteString(loop[0])
		for i := 1; i < len(loop); i++ {
			sb.WriteString("->")
			sb.WriteString(loop[i])
		}
		sb.WriteString("]")
	}
	return sb.String()
}

// NewNodePath specifies a path to a node in the graph, which is composed of node keys.
// Starting from the top graph,
// following this set of node keys can lead to a specific node in the top graph or a subgraph.
//
// e.g.
// NewNodePath("sub_graph_node_key", "node_key_within_sub_graph")
func NewNodePath(nodeKeyPath ...string) *NodePath {
	return &NodePath{path: nodeKeyPath}
}

// NodePath represents a path composed of node keys to locate a node.
type NodePath struct {
	path []string
}

// GetPath returns the sequence of node keys in the path.
func (p *NodePath) GetPath() []string {
	return p.path
}
