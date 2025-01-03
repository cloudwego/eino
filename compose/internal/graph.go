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
	"errors"
	"fmt"
	"reflect"

	"github.com/cloudwego/eino/internal/gmap"
	"github.com/cloudwego/eino/schema"
)

// START is the start node of the graph. You can add your first edge with START.
const START = "start"

// END is the end node of the graph. You can add your last edge with END.
const END = "end"

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

func NewGraphBranch[T any](condition func(ctx context.Context, in T) (endNode string, err error), endNodes map[string]bool) *GraphBranch {
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

func NewStreamGraphBranch[T any](condition func(ctx context.Context, in *schema.StreamReader[T]) (endNode string, err error),
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
	nodes      map[string]*GraphNode
	edges      map[string][]string
	branches   map[string][]*GraphBranch
	startNodes []string
	endNodes   []string

	toValidateMap map[string][]string

	runCtx func(ctx context.Context) context.Context

	expectedInputType, expectedOutputType reflect.Type
	inputStreamFilter                     streamMapFilter
	inputValueChecker                     valueChecker
	inputStreamConverter                  streamConverter
	outputValueChecker                    valueChecker
	outputStreamConverter                 streamConverter

	runtimeCheckEdges    map[string]map[string]bool
	runtimeCheckBranches map[string][]bool

	buildError error

	cmp component

	enableState bool

	compiled bool
}

func newGraph( // nolint: byted_s_args_length_limit
	inputType, outputType reflect.Type,
	filter streamMapFilter,
	inputChecker, outputChecker valueChecker,
	inputConv, outputConv streamConverter,
	cmp component,
	runCtx func(ctx context.Context) context.Context,
	enableState bool,
) *graph {
	return &graph{
		nodes:    make(map[string]*GraphNode),
		edges:    make(map[string][]string),
		branches: make(map[string][]*GraphBranch),

		toValidateMap: make(map[string][]string),

		expectedInputType:     inputType,
		expectedOutputType:    outputType,
		inputStreamFilter:     filter,
		inputValueChecker:     inputChecker,
		inputStreamConverter:  inputConv,
		outputValueChecker:    outputChecker,
		outputStreamConverter: outputConv,

		runtimeCheckEdges:    make(map[string]map[string]bool),
		runtimeCheckBranches: make(map[string][]bool),

		cmp: cmp,

		runCtx: runCtx,

		enableState: enableState,
	}
}

func (g *graph) component() component {
	return g.cmp
}

func isChain(cmp component) bool {
	return cmp == ComponentOfChain
}

// ErrGraphCompiled is returned when attempting to modify a graph after it has been compiled
var ErrGraphCompiled = errors.New("graph has been compiled, cannot be modified")

func (g *graph) AddNode(key string, node *GraphNode, options *GraphAddNodeOpts) (err error) {
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
		if !g.enableState {
			return fmt.Errorf("node '%s' needs state but graph state is not enabled", key)
		}
	}

	if options.nodeOptions.nodeKey != "" {
		if !isChain(g.cmp) {
			return errors.New("only chain support node key option")
		}
	}
	// end: check options

	g.nodes[key] = node

	return nil
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
func (g *graph) AddEdge(startNode, endNode string) (err error) {
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

		e := g.validateAndInferType(startNode, endNode)
		if e != nil {
			return e
		}

		if startNode == START {
			g.startNodes = append(g.startNodes, endNode)
		}
		if endNode == END {
			g.endNodes = append(g.endNodes, startNode)
		}

		e = g.updateToValidateMap()
		if e != nil {
			return e
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

	runType := runTypePregel
	cb := pregelChannelBuilder
	if opt != nil {
		if opt.nodeTriggerMode != "" {
			if isChain(g.cmp) {
				return nil, errors.New("chain doesn't support node trigger mode option")
			}

			if opt.nodeTriggerMode == AllPredecessor {
				runType = runTypeDAG
				cb = dagChannelBuilder
			}
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
			branchRuns = append(branchRuns, branches...)

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
	copy(inputChannels.writeToBranches, g.branches[START])

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
		err := validateDAG(r.chanSubscribeTo, r.invertedEdges)
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

	g.compiled = true

	g.onCompileFinish(ctx, opt, key2SubGraphs)

	return r.toComposableRunnable(), nil
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

func (gn *GraphNode) beforeChildGraphCompile(nodeKey string, key2SubGraphs map[string]*GraphInfo) {
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
		Name:       opt.graphName,
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

	gInfo := g.toGraphInfo(opt, key2SubGraphs)

	for _, cb := range opt.callbacks {
		cb.OnFinish(ctx, gInfo)
	}
}

func (g *graph) GetType() string {
	return ""
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

func NewNodePath(path ...string) *NodePath {
	return &NodePath{path: path}
}

type NodePath struct {
	path []string
}
