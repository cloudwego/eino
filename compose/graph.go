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
	"runtime"

	"github.com/cloudwego/eino/components/document"
	"github.com/cloudwego/eino/components/embedding"
	"github.com/cloudwego/eino/components/indexer"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/prompt"
	"github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/internal/gmap"
	"github.com/cloudwego/eino/schema"
)

// START is the start node of the graph. You can add your first edge with START.
const START = "start"

// END is the end node of the graph. You can add your last edge with END.
const END = "end"

// errGraphFrozen is the error returned when the graph is frozen but still add node or edge.
var errGraphFrozen = errors.New("graph already frozen")

// GraphBranchCondition is the condition type for the branch.
type GraphBranchCondition[T any] func(ctx context.Context, in T) (endNode string, err error)

// StreamGraphBranchCondition is the condition type for the stream branch.
type StreamGraphBranchCondition[T any] func(ctx context.Context, in *schema.StreamReader[T]) (endNode string, err error)

// GraphBranch is the branch type for the graph.
// It is used to determine the next node based on the condition.
type GraphBranch struct {
	condition *composableRunnable
	endNodes  map[string]bool
	idx       int // used to distinguish branches in parallel
}

// GetEndNode returns the all end nodes of the branch.
func (gb *GraphBranch) GetEndNode() map[string]bool {
	return gb.endNodes
}

// NewGraphBranch creates a new graph branch.
// It is used to determine the next node based on the condition.
// eg.
//
//	condition := func(ctx context.Context, in string) (string, error) {
//		// logic to determine the next node
//		return "next_node_key", nil
//	}
//	endNodes := map[string]bool{"path01": true, "path02": true}
//	branch := compose.NewGraphBranch(condition, endNodes)
//
//	graph.AddBranch("key_of_node_before_branch", branch)
func NewGraphBranch[T any](condition GraphBranchCondition[T], endNodes map[string]bool) *GraphBranch {
	condRun := func(ctx context.Context, in T, opts ...any) (string, error) {
		endNode, err := condition(ctx, in)
		if err != nil {
			return "", err
		}

		if !endNodes[endNode] {
			return "", fmt.Errorf("branch invocation returns unintended end node: %s", endNode)
		}

		return endNode, nil
	}

	r := runnableLambda(condRun, nil, nil, nil, false)

	return &GraphBranch{
		condition: r,
		endNodes:  endNodes,
	}
}

// NewStreamGraphBranch creates a new stream graph branch.
// It is used to determine the next node based on the condition of stream input.
// eg.
//
//	condition := func(ctx context.Context, in *schema.StreamReader[T]) (string, error) {
//		// logic to determine the next node.
//		// to use the feature of stream, you can use the first chunk to determine the next node.
//		return "next_node_key", nil
//	}
//	endNodes := map[string]bool{"path01": true, "path02": true}
//	branch := compose.NewStreamGraphBranch(condition, endNodes)
//
//	graph.AddBranch("key_of_node_before_branch", branch)
func NewStreamGraphBranch[T any](condition StreamGraphBranchCondition[T],
	endNodes map[string]bool) *GraphBranch {

	condRun := func(ctx context.Context, in *schema.StreamReader[T], opts ...any) (string, error) {
		endNode, err := condition(ctx, in)
		if err != nil {
			return "", err
		}

		if !endNodes[endNode] {
			return "", fmt.Errorf("stream branch invocation returns unintended end node: %s", endNode)
		}

		return endNode, nil
	}

	r := runnableLambda(nil, nil, condRun, nil, false)

	return &GraphBranch{
		condition: r,
		endNodes:  endNodes,
	}
}

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
	nodes      map[string]*graphNode
	edges      map[string][]string
	branches   map[string][]*GraphBranch
	startNodes []string
	endNodes   []string

	toValidateMap map[string][]string

	frozen bool

	runCtx func(ctx context.Context) context.Context

	addNodeChecker nodeChecker
	compileChecker func(options *graphCompileOptions) error

	expectedInputType, expectedOutputType reflect.Type
	inputStreamFilter                     streamMapFilter
	inputValueChecker                     valueChecker
	inputStreamConverter                  streamConverter
	outputValueChecker                    valueChecker
	outputStreamConverter                 streamConverter

	runtimeCheckEdges    map[string]map[string]bool
	runtimeCheckBranches map[string][]bool
	runtimeGraphKey      string

	buildError error
}

func newGraph( // nolint: byted_s_args_length_limit
	inputType, outputType reflect.Type,
	filter streamMapFilter,
	inputChecker, outputChecker valueChecker,
	inputConv, outputConv streamConverter,
	graphKey string,
) *graph {
	return &graph{
		nodes:    make(map[string]*graphNode),
		edges:    make(map[string][]string),
		branches: make(map[string][]*GraphBranch),

		toValidateMap: make(map[string][]string),

		addNodeChecker: nodeCheckerOfForbidProcessor(nodeCheckerOfForbidNodeKey(baseNodeChecker)),
		compileChecker: defaultCompileChecker,

		expectedInputType:     inputType,
		expectedOutputType:    outputType,
		inputStreamFilter:     filter,
		inputValueChecker:     inputChecker,
		inputStreamConverter:  inputConv,
		outputValueChecker:    outputChecker,
		outputStreamConverter: outputConv,

		runtimeCheckEdges:    make(map[string]map[string]bool),
		runtimeCheckBranches: make(map[string][]bool),
		runtimeGraphKey:      graphKey,
	}
}

func (g *graph) freeze() {
	g.frozen = true
}

func (g *graph) isFrozen() bool {
	return g.frozen
}

func (g *graph) addNode(name string, node *graphNode) (err error) {
	if g.buildError != nil {
		return g.buildError
	}
	defer func() {
		if err != nil {
			g.buildError = err
		}
	}()

	if g.frozen {
		return errGraphFrozen
	}

	if name == END || name == START {
		return fmt.Errorf("node '%s' is reserved, cannot add manually", name)
	}

	if _, ok := g.nodes[name]; ok {
		return fmt.Errorf("node '%s' already present", name)
	}

	if err = g.addNodeChecker(name, node); err != nil {
		return err
	}

	g.nodes[name] = node

	return nil
}

// AddEdge adds an edge to the graph, edge means a data flow from startNode to endNode.
// the previous node's output type must can be set to the next node's input type.
// NOTE: startNode and endNode must have been added to the graph before adding edge.
// eg.
//
//	graph.AddNode("start_node_key", compose.NewPassthroughNode())
//	graph.AddNode("end_node_key", compose.NewPassthroughNode())
//
//	err := graph.AddEdge("start_node_key", "end_node_key")
func (g *graph) AddEdge(startNode, endNode string) (err error) {
	if g.buildError != nil {
		return g.buildError
	}
	defer func() {
		if err != nil {
			g.buildError = err
		}
	}()

	if g.frozen {
		return errGraphFrozen
	}

	if startNode == END {
		return errors.New("END cannot be a start node")
	}

	if endNode == START {
		return errors.New("START cannot be an end node")
	}

	for i := range g.edges[startNode] {
		if g.edges[startNode][i] == endNode {
			return fmt.Errorf("edge[%s]-[%s] have been added yet", startNode, endNode)
		}
	}

	if _, ok := g.nodes[startNode]; !ok && startNode != START {
		return fmt.Errorf("edge start node '%s' needs to be added to graph first", startNode)
	}

	if _, ok := g.nodes[endNode]; !ok && endNode != END {
		return fmt.Errorf("edge end node '%s' needs to be added to graph first", endNode)
	}

	err = g.validateAndInferType(startNode, endNode)
	if err != nil {
		return err
	}

	g.edges[startNode] = append(g.edges[startNode], endNode)

	if startNode == START {
		g.startNodes = append(g.startNodes, endNode)
	}

	if endNode == END {
		g.endNodes = append(g.endNodes, startNode)
	}

	err = g.updateToValidateMap()
	if err != nil {
		return err
	}

	return nil
}

// AddEmbeddingNode adds a node that implements embedding.Embedder.
// eg.
//
//	embeddingNode, err := openai.NewEmbedder(ctx, &openai.EmbeddingConfig{
//		Model: "text-embedding-3-small",
//	})
//
//	graph.AddEmbeddingNode("embedding_node_key", embeddingNode)
func (g *graph) AddEmbeddingNode(key string, node embedding.Embedder, opts ...GraphAddNodeOpt) error {
	return g.addNode(key, toEmbeddingNode(node, opts...))
}

// AddRetrieverNode adds a node that implements retriever.Retriever.
// eg.
//
//	retriever, err := vikingdb.NewRetriever(ctx, &vikingdb.RetrieverConfig{})
//
//	graph.AddRetrieverNode("retriever_node_key", retrieverNode)
func (g *graph) AddRetrieverNode(key string, node retriever.Retriever, opts ...GraphAddNodeOpt) error {
	return g.addNode(key, toRetrieverNode(node, opts...))
}

// Deprecated: use AddLoaderNode instead.
func (g *graph) AddLoaderSplitterNode(key string, node document.LoaderSplitter, opts ...GraphAddNodeOpt) error {
	return g.addNode(key, toLoaderSplitterNode(node, opts...))
}

// AddLoaderNode adds a node that implements document.Loader.
// eg.
//
//	loader, err := file.NewLoader(ctx, &file.LoaderConfig{})
//
//	graph.AddLoaderNode("loader_node_key", loader)
func (g *graph) AddLoaderNode(key string, node document.Loader, opts ...GraphAddNodeOpt) error {
	return g.addNode(key, toLoaderNode(node, opts...))
}

// AddIndexerNode adds a node that implements indexer.Indexer.
// eg.
//
//	indexer, err := vikingdb.NewIndexer(ctx, &vikingdb.IndexerConfig{})
//
//	graph.AddIndexerNode("indexer_node_key", indexer)
func (g *graph) AddIndexerNode(key string, node indexer.Indexer, opts ...GraphAddNodeOpt) error {
	return g.addNode(key, toIndexerNode(node, opts...))
}

// AddChatModelNode add node that implements model.ChatModel.
// eg.
//
//	chatModel, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
//		Model: "gpt-4o",
//	})
//
//	graph.AddChatModelNode("chat_model_node_key", chatModel)
func (g *graph) AddChatModelNode(key string, node model.ChatModel, opts ...GraphAddNodeOpt) error {
	return g.addNode(key, toChatModelNode(node, opts...))
}

// AddChatTemplateNode add node that implements prompt.ChatTemplate.
// eg.
//
//	chatTemplate, err := prompt.FromMessages(schema.FString, &schema.Message{
//		Role:    schema.System,
//		Content: "You are acting as a {role}.",
//	})
//
//	graph.AddChatTemplateNode("chat_template_node_key", chatTemplate)
func (g *graph) AddChatTemplateNode(key string, node prompt.ChatTemplate, opts ...GraphAddNodeOpt) error {
	return g.addNode(key, toChatTemplateNode(node, opts...))
}

// AddToolsNode adds a node that implements tools.ToolsNode.
// eg.
//
//	toolsNode, err := tools.NewToolNode(ctx, &tools.ToolsNodeConfig{})
//
//	graph.AddToolsNode("tools_node_key", toolsNode)
func (g *graph) AddToolsNode(key string, node *ToolsNode, opts ...GraphAddNodeOpt) error {
	return g.addNode(key, toToolsNode(node, opts...))
}

// AddDocumentTransformerNode adds a node that implements document.Transformer.
// eg.
//
//	markdownSplitter, err := markdown.NewHeaderSplitter(ctx, &markdown.HeaderSplitterConfig{})
//
//	graph.AddDocumentTransformerNode("document_transformer_node_key", markdownSplitter)
func (g *graph) AddDocumentTransformerNode(key string, node document.Transformer, opts ...GraphAddNodeOpt) error {
	return g.addNode(key, toDocumentTransformerNode(node, opts...))
}

// AddLambdaNode add node that implements at least one of Invoke[I, O], Stream[I, O], Collect[I, O], Transform[I, O].
// due to the lack of supporting method generics, we need to use function generics to generate Lambda run as Runnable[I, O].
// for Invoke[I, O], use compose.InvokableLambda()
// for Stream[I, O], use compose.StreamableLambda()
// for Collect[I, O], use compose.CollectableLambda()
// for Transform[I, O], use compose.TransformableLambda()
// for arbitrary combinations of 4 kinds of lambda, use compose.AnyLambda()
func (g *graph) AddLambdaNode(key string, node *Lambda, opts ...GraphAddNodeOpt) error {
	return g.addNode(key, toLambdaNode(node, opts...))
}

// AddGraphNode add one kind of Graph[I, O]、Chain[I, O]、StateChain[I, O, S] as a node.
// for Graph[I, O], comes from NewGraph[I, O]()
// for Chain[I, O], comes from NewChain[I, O]()
// for StateGraph[I, O, S], comes from NewStateGraph[I, O, S]()
func (g *graph) AddGraphNode(key string, node AnyGraph, opts ...GraphAddNodeOpt) error {
	return g.addNode(key, toAnyGraphNode(node, opts...))
}

// AddPassthroughNode adds a passthrough node to the graph.
// mostly used in pregel mode of graph.
// eg.
//
//	graph.AddPassthroughNode("passthrough_node_key")
func (g *graph) AddPassthroughNode(key string, opts ...GraphAddNodeOpt) error {
	return g.addNode(key, toPassthroughNode(opts...))
}

// AddBranch adds a branch to the graph.
// eg.
//
//	condition := func(ctx context.Context, in string) (string, error) {
//		return "next_node_key", nil
//	}
//	endNodes := map[string]bool{"path01": true, "path02": true}
//	branch := compose.NewGraphBranch(condition, endNodes)
//
//	graph.AddBranch("start_node_key", branch)
func (g *graph) AddBranch(startNode string, branch *GraphBranch) (err error) {
	if g.buildError != nil {
		return g.buildError
	}
	defer func() {
		if err != nil {
			g.buildError = err
		}
	}()

	if g.frozen {
		return errGraphFrozen
	}

	if startNode == END {
		return errors.New("END cannot be a start node")
	}

	if _, ok := g.nodes[startNode]; !ok && startNode != START {
		return fmt.Errorf("branch start node '%s' needs to be added to graph first", startNode)
	}

	if len(branch.endNodes) == 1 {
		return fmt.Errorf("number of branches is 1")
	}

	if _, ok := g.runtimeCheckBranches[startNode]; !ok {
		g.runtimeCheckBranches[startNode] = []bool{}
	}
	branch.idx = len(g.runtimeCheckBranches[startNode])

	// check branch condition type
	result := checkAssignable(g.getNodeOutputType(startNode), branch.condition.inputType)
	if result == assignableTypeMustNot {
		return fmt.Errorf("condition input type[%s] and start node output type[%s] are mismatched", branch.condition.inputType.String(), g.getNodeOutputType(startNode).String())
	} else if result == assignableTypeMay {
		g.runtimeCheckBranches[startNode] = append(g.runtimeCheckBranches[startNode], true)
	} else {
		g.runtimeCheckBranches[startNode] = append(g.runtimeCheckBranches[startNode], false)
	}

	for endNode := range branch.endNodes {
		if _, ok := g.nodes[endNode]; !ok {
			if endNode != END {
				return fmt.Errorf("branch end node '%s' needs to be added to graph first", endNode)
			}
		}

		err := g.validateAndInferType(startNode, endNode)
		if err != nil {
			return err
		}

		if startNode == START {
			g.startNodes = append(g.startNodes, endNode)
		}
		if endNode == END {
			g.endNodes = append(g.endNodes, startNode)
		}

		err = g.updateToValidateMap()
		if err != nil {
			return err
		}
	}

	g.branches[startNode] = append(g.branches[startNode], branch)

	return nil
}

func (g *graph) validateAndInferType(startNode, endNode string) error {
	startNodeOutputType := g.getNodeOutputType(startNode)
	endNodeInputType := g.getNodeInputType(endNode)

	// assume that START and END type isn't empty
	// check and update current node. if cannot validate, save edge to toValidateMap
	if startNodeOutputType == nil && endNodeInputType == nil {
		// type of passthrough have not been inferred yet. defer checking to compile.
		g.toValidateMap[startNode] = append(g.toValidateMap[startNode], endNode)
	} else if startNodeOutputType != nil && endNodeInputType == nil {
		// end node is passthrough, propagate start node output type to it
		g.nodes[endNode].cr.inputType = startNodeOutputType
		g.nodes[endNode].cr.outputType = g.nodes[endNode].cr.inputType
	} else if startNodeOutputType == nil /* redundant condition && endNodeInputType != nil */ {
		// start node is passthrough, propagate end node input type to it
		g.nodes[startNode].cr.inputType = endNodeInputType
		g.nodes[startNode].cr.outputType = g.nodes[startNode].cr.inputType
	} else {
		// common node check
		result := checkAssignable(startNodeOutputType, endNodeInputType)
		if result == assignableTypeMustNot {
			return fmt.Errorf("graph edge[%s]-[%s]: start node's output type[%s] and end node's input type[%s] mismatch",
				startNode, endNode, startNodeOutputType.String(), endNodeInputType.String())
		} else if result == assignableTypeMay {
			// add runtime check edges
			if _, ok := g.runtimeCheckEdges[startNode]; !ok {
				g.runtimeCheckEdges[startNode] = make(map[string]bool)
			}
			g.runtimeCheckEdges[startNode][endNode] = true
		}
	}
	return nil
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

				endNodeInputType = g.getNodeInputType(endNode)
				if startNodeOutputType == nil && endNodeInputType == nil {
					continue
				}

				// update toValidateMap
				g.toValidateMap[startNode] = append(g.toValidateMap[startNode][:i], g.toValidateMap[startNode][i+1:]...)
				i--

				hasChanged = true
				// assume that START and END type isn't empty
				if startNodeOutputType != nil && endNodeInputType == nil {
					g.nodes[endNode].cr.inputType = startNodeOutputType
					g.nodes[endNode].cr.outputType = g.nodes[endNode].cr.inputType
				} else if startNodeOutputType == nil /* redundant condition && endNodeInputType != nil */ {
					g.nodes[startNode].cr.inputType = endNodeInputType
					g.nodes[startNode].cr.outputType = g.nodes[startNode].cr.inputType
				} else {
					// common node check
					result := checkAssignable(startNodeOutputType, endNodeInputType)
					if result == assignableTypeMustNot {
						return fmt.Errorf("graph edge[%s]-[%s]: start node's output type[%s] and end node's input type[%s] mismatch",
							startNode, endNode, startNodeOutputType.String(), endNodeInputType.String())
					} else if result == assignableTypeMay {
						// add runtime check edges
						if _, ok := g.runtimeCheckEdges[startNode]; !ok {
							g.runtimeCheckEdges[startNode] = make(map[string]bool)
						}
						g.runtimeCheckEdges[startNode][endNode] = true
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

	err := g.compileChecker(opt)
	if err != nil {
		return nil, err
	}

	runType := runTypePregel
	cb := pregelChannelBuilder
	if opt != nil {
		if opt.nodeTriggerMode == AllPredecessor {
			runType = runTypeDAG
			cb = dagChannelBuilder
		}
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

	// dag doesn't support branch
	if runType == runTypeDAG && len(g.branches) > 0 {
		return nil, fmt.Errorf("dag doesn't support branch for now")
	}

	key2SubGraphs := g.beforeChildGraphsCompile(opt)
	chanSubscribeTo := make(map[string]*chanCall)
	for name, node := range g.nodes {
		node.beforeChildGraphCompile(name, key2SubGraphs)

		r, err := node.compileIfNeeded(ctx)
		if err != nil {
			return nil, err
		}

		writeTo := g.edges[name]
		chCall := &chanCall{
			action:  r,
			writeTo: writeTo,

			preProcessor:  node.nodeInfo.preProcessor,
			postProcessor: node.nodeInfo.postProcessor,
		}

		branches := g.branches[name]
		if len(branches) > 0 {
			branchRuns := make([]*GraphBranch, 0, len(branches))
			for _, branch := range branches {
				branchRuns = append(branchRuns, branch)
			}

			chCall.writeToBranches = branchRuns
		}

		chanSubscribeTo[name] = chCall

	}

	invertedEdges := make(map[string][]string)
	for start, ends := range g.edges {
		for _, end := range ends {
			if _, ok := invertedEdges[end]; !ok {
				invertedEdges[end] = []string{start}
			} else {
				invertedEdges[end] = append(invertedEdges[end], start)
			}

		}
	}

	inputChannels := &chanCall{
		writeTo:         g.edges[START],
		writeToBranches: make([]*GraphBranch, len(g.branches[START])),
	}
	for i := range g.branches[START] {
		inputChannels.writeToBranches[i] = g.branches[START][i]
	}

	// validate dag
	if runType == runTypeDAG {
		for _, node := range g.startNodes {
			if len(invertedEdges[node]) != 1 {
				return nil, fmt.Errorf("dag start node[%s] should not have predecessor other than 'start', but got: %v", node, invertedEdges[node])
			}
		}
	}

	r := &runner{
		invertedEdges:   invertedEdges,
		chanSubscribeTo: chanSubscribeTo,
		inputChannels:   inputChannels,

		runCtx:      g.runCtx,
		chanBuilder: cb,

		inputType:             g.inputType(),
		outputType:            g.outputType(),
		inputStreamFilter:     g.inputStreamFilter,
		inputValueChecker:     g.inputValueChecker,
		inputStreamConverter:  g.inputStreamConverter,
		outputValueChecker:    g.outputValueChecker,
		outputStreamConverter: g.outputStreamConverter,

		runtimeCheckEdges:    g.runtimeCheckEdges,
		runtimeCheckBranches: g.runtimeCheckBranches,
	}

	if runType == runTypeDAG {
		err = validateDAG(r.chanSubscribeTo, r.invertedEdges)
		if err != nil {
			return nil, err
		}
	}

	if opt != nil {
		r.options = *opt
	}

	// default options
	if r.options.maxRunSteps == 0 {
		r.options.maxRunSteps = len(r.chanSubscribeTo) + 10
	}

	g.freeze()

	g.onCompileFinish(ctx, opt, key2SubGraphs)

	return r.toComposableRunnable()
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

func (g *graph) toGraphInfo(ctx context.Context, opt *graphCompileOptions, key2SubGraphs map[string]*GraphInfo) *GraphInfo {

	graphKey := g.runtimeGraphKey
	if opt.graphKey != "" {
		graphKey = opt.graphKey
	}

	gInfo := &GraphInfo{
		Key:            graphKey,
		CompileOptions: opt.origOpts,
		Nodes:          make(map[string]GraphNodeInfo, len(g.nodes)),
		Edges:          gmap.Clone(g.edges),
		Branches: gmap.Map(g.branches, func(startNode string, branches []*GraphBranch) (string, []GraphBranch) {
			branchInfo := make([]GraphBranch, 0, len(branches))
			for _, b := range branches {
				branchInfo = append(branchInfo, GraphBranch{
					condition: b.condition,
					endNodes:  gmap.Clone(b.endNodes),
				})
			}
			return startNode, branchInfo
		}),
		InputType:  g.expectedInputType,
		OutputType: g.expectedOutputType,
	}

	for key := range g.nodes {
		gNode := g.nodes[key]
		if gNode.executorMeta.component == ComponentOfPassthrough {
			gInfo.Nodes[key] = GraphNodeInfo{
				Component:        gNode.executorMeta.component,
				GraphAddNodeOpts: gNode.opts,
				InputType:        gNode.cr.inputType,
				OutputType:       gNode.cr.outputType,
				Name:             gNode.getNodeName(),
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
			Name:             gNode.getNodeName(),
			InputKey:         gNode.cr.nodeInfo.inputKey,
			OutputKey:        gNode.cr.nodeInfo.outputKey,
		}

		if gi, ok := key2SubGraphs[key]; ok {
			gNodeInfo.GraphInfo = gi
		}

		gInfo.Nodes[key] = *gNodeInfo
	}

	if g.runCtx != nil {
		gInfo.GenStateFn = func(ctx context.Context) any {
			stateCtx := g.runCtx(ctx)
			state, err := GetState[any](stateCtx)
			if err != nil {
				return nil
			}

			return state
		}
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

	gInfo := g.toGraphInfo(ctx, opt, key2SubGraphs)

	for _, cb := range opt.callbacks {
		cb.OnFinish(ctx, gInfo)
	}
}

func (g *graph) GetType() string {
	return ""
}

func defaultCompileChecker(options *graphCompileOptions) error {
	return nil
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

func validateDAG(chanSubscribeTo map[string]*chanCall, invertedEdges map[string][]string) error {
	m := map[string]int{}
	for node := range chanSubscribeTo {
		if edges, ok := invertedEdges[node]; ok {
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
				for _, subNode := range chanSubscribeTo[node].writeTo {
					if subNode == END {
						continue
					}
					m[subNode]--
				}
				m[node] = -1
			}
		}
	}

	for k, v := range m {
		if v > 0 {
			return fmt.Errorf("DAG invalid, node[%s] has loop", k)
		}
	}
	return nil
}

func wrapCompileChecker(checkers ...func(options *graphCompileOptions) error) func(options *graphCompileOptions) error {
	return func(options *graphCompileOptions) error {
		for _, checker := range checkers {
			if checker == nil {
				continue
			}

			if err := checker(options); err != nil {
				return err
			}
		}

		return nil
	}
}

func defaultGraphKey() string {
	pcs := make([]uintptr, 1)
	_ = runtime.Callers(3, pcs)
	frame, _ := runtime.CallersFrames(pcs).Next()
	return fmt.Sprintf("%s:%d", frame.Function, frame.Line)
}