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
	"container/list"
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
)

type graphCompileOptions struct {
	maxRunSteps     int
	graphName       string
	nodeTriggerMode NodeTriggerMode // default to AnyPredecessor (pregel)

	callbacks []GraphCompileCallback

	origOpts []GraphCompileOption

	getStateEnabled bool
}

func newGraphCompileOptions(opts ...GraphCompileOption) *graphCompileOptions {
	option := &graphCompileOptions{}

	for _, o := range opts {
		o(option)
	}

	option.origOpts = opts

	return option
}

type chanCall struct {
	action          *composableRunnable
	writeTo         []*edgeInfo
	writeToBranches []*GraphBranch

	preProcessor, postProcessor *composableRunnable
}

type chanBuilder func(d []string) channel

type runner struct {
	chanSubscribeTo map[string]*chanCall
	invertedEdges   map[string][]string
	successors      map[string][]string
	inputChannels   *chanCall

	chanBuilder chanBuilder // could be nil
	eager       bool
	dag         bool

	runCtx func(ctx context.Context) context.Context

	options graphCompileOptions

	inputType  reflect.Type
	outputType reflect.Type

	// take effect as a sub-graph through toComposableRunnable
	inputStreamFilter streamMapFilter

	inputConverter             handlerPair
	inputFieldMappingConverter handlerPair

	// checks need to do because cannot check at compile
	runtimeCheckEdges    map[string]map[string]bool
	runtimeCheckBranches map[string][]bool

	edgeHandlerManager      *edgeHandlerManager
	preNodeHandlerManager   *preNodeHandlerManager
	preBranchHandlerManager *preBranchHandlerManager
}

func (r *runner) invoke(ctx context.Context, input any, opts ...Option) (any, error) {
	return r.run(ctx, false, input, opts...)
}

func (r *runner) transform(ctx context.Context, input streamReader, opts ...Option) (streamReader, error) {
	s, err := r.run(ctx, true, input, opts...)
	if err != nil {
		return nil, err
	}

	return s.(streamReader), nil
}

type runnableCallWrapper func(context.Context, *composableRunnable, any, ...any) (any, error)

func runnableInvoke(ctx context.Context, r *composableRunnable, input any, opts ...any) (any, error) {
	return r.i(ctx, input, opts...)
}

func runnableTransform(ctx context.Context, r *composableRunnable, input any, opts ...any) (any, error) {
	return r.t(ctx, input.(streamReader), opts...)
}

func (r *runner) run(ctx context.Context, isStream bool, input any, opts ...Option) (any, error) {
	// Choose the appropriate wrapper function based on whether we're handling a stream or not.
	var runWrapper runnableCallWrapper
	runWrapper = runnableInvoke
	if isStream {
		runWrapper = runnableTransform
	}

	// Initialize channel and task managers.
	cm := r.initChannelManager(isStream)
	tm := r.initTaskManager(runWrapper, opts...)
	maxSteps := r.options.maxRunSteps
	if r.runCtx != nil {
		ctx = r.runCtx(ctx)
	}

	if r.dag {
		for i := range opts {
			if opts[i].maxRunSteps > 0 {
				return nil, fmt.Errorf("cannot set max run steps in dag")
			}
		}
	} else {
		// Update maxSteps if provided in options.
		for i := range opts {
			if opts[i].maxRunSteps > 0 {
				maxSteps = opts[i].maxRunSteps
			}
		}
		if maxSteps < 1 {
			return nil, errors.New("max run steps limit must be at least 1")
		}
	}

	// Extract and validate options for each node.
	optMap, extractErr := extractOption(r.chanSubscribeTo, opts...)
	if extractErr != nil {
		return nil, fmt.Errorf("graph extract option fail: %w", extractErr)
	}

	// Initialize with START node task.
	var completedTasks []*task
	completedTasks = append(completedTasks, &task{
		nodeKey: START,
		call:    r.inputChannels,
		output:  input,
	})

	// Main execution loop.
	for step := 0; ; step++ {
		// Check for context cancellation.
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("context has been canceled: %w", ctx.Err())
		default:
		}
		if !r.dag && step >= maxSteps {
			return nil, ErrExceedMaxSteps
		}

		toUpdateMap := make(map[string]map[string]any)
		toPassControlMap := make(map[string][]string)
		toSkipMap := make(map[string]map[string]struct{})
		for _, t := range completedTasks {
			vForEdge, vForBranchRun, vForDataBranch := copyTaskOutput(t)

			next, toUpdate := r.resolveEdges(t)
			toPassControlMap[t.nodeKey] = append(toPassControlMap[t.nodeKey], next...)

			for i, w := range toUpdate {
				if _, ok := toUpdateMap[w]; !ok {
					toUpdateMap[w] = make(map[string]any)
				}

				toUpdateMap[w][t.nodeKey] = vForEdge[i]
			}

			selected, skipped, toUpdate, err := r.resolveBranches(ctx, t, isStream, vForBranchRun)
			if err != nil {
				return nil, err
			}

			if _, ok := toSkipMap[t.nodeKey]; !ok {
				toSkipMap[t.nodeKey] = make(map[string]struct{})
			}
			for _, node := range skipped {
				toSkipMap[t.nodeKey][node] = struct{}{}
			}

			toPassControlMap[t.nodeKey] = append(toPassControlMap[t.nodeKey], selected...)

			if len(vForDataBranch) < len(toUpdate) {
				toCopyNum := len(toUpdate) - len(vForDataBranch) + 1
				copied := copyItem(vForDataBranch[len(vForDataBranch)-1], toCopyNum)
				vForDataBranch = append(vForDataBranch[:len(vForDataBranch)-1], copied...)
			}

			for i, w := range toUpdate {
				if _, ok := toUpdateMap[w]; !ok {
					toUpdateMap[w] = make(map[string]any)
				}

				toUpdateMap[w][t.nodeKey] = vForDataBranch[i]
			}
		}

		if err := cm.updateValues(ctx, toUpdateMap, isStream); err != nil {
			return nil, err
		}

		for fromNodeKey, toList := range toPassControlMap {
			if err := cm.reportControl(fromNodeKey, toList); err != nil {
				return nil, err
			}
		}

		for fromNodeKey, toSkip := range toSkipMap {
			skippedNodeList := make([]string, 0, len(toSkip))
			if toControl, ok := toPassControlMap[fromNodeKey]; ok {
				for _, nodeKey := range toControl {
					if _, ok := toSkip[nodeKey]; ok {
						delete(toSkip, nodeKey)
					}
				}
			}

			for nodeKey := range toSkip {
				skippedNodeList = append(skippedNodeList, nodeKey)
			}

			if err := cm.reportBranch(fromNodeKey, skippedNodeList); err != nil {
				return nil, err
			}
		}

		// resolve control and report skipped nodes
		// resolve data and update channel values
		// get ready channels
		nodeMap, err := cm.getFromReadyChannels(ctx, isStream)
		if err != nil {
			return nil, fmt.Errorf("failed to update and get channels: %w", err)
		}
		if len(nodeMap) > 0 {
			// Check if we've reached the END node.
			if v, ok := nodeMap[END]; ok {
				return v, nil
			}

			// Create and submit next batch of tasks.
			nextTasks, convErr := r.createTasks(ctx, nodeMap, optMap)
			if convErr != nil {
				return nil, fmt.Errorf("failed to create tasks: %w", convErr)
			}
			err = tm.submit(nextTasks)
			if err != nil {
				return nil, fmt.Errorf("failed to submit tasks: %w", err)
			}
		}

		// Wait for tasks to complete and prepare for next iteration.
		completedTasks, err = tm.wait()
		if err != nil {
			return nil, fmt.Errorf("failed to wait for tasks: %w", err)
		}
		if len(completedTasks) == 0 {
			return nil, errors.New("no tasks to execute")
		}
	}
}

func (r *runner) resolveEdges(t *task) (next []string, toUpdate []string) {
	for _, edge := range t.call.writeTo {
		if !edge.noDataFlow {
			toUpdate = append(toUpdate, edge.endNode)
		}

		if !edge.noControlFlow {
			next = append(next, edge.endNode)
		}
	}

	return next, toUpdate
}

func (r *runner) resolveBranches(ctx context.Context, t *task, isStream bool, vs []any) (selected []string, skipped []string, toUpdate []string, err error) {
	skippedNodes := make(map[string]struct{})
	for i, branch := range t.call.writeToBranches {
		// check branch input type if needed
		var err error
		vs[i], err = r.preBranchHandlerManager.handle(t.nodeKey, i, vs[i], isStream)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("branch[%s]-[%d] pre handler fail: %w", t.nodeKey, branch.idx, err)
		}

		// process branch output
		var ws []string
		if isStream {
			ws, err = branch.collect(ctx, vs[i].(streamReader))
			if err != nil {
				return nil, nil, nil, fmt.Errorf("branch collect run error: %w", err)
			}
		} else {
			ws, err = branch.invoke(ctx, vs[i])
			if err != nil {
				return nil, nil, nil, fmt.Errorf("branch invoke run error: %w", err)
			}
		}

		for node := range branch.endNodes {
			skip := true
			for _, w := range ws {
				if node == w {
					skip = false
					break
				}
			}
			if skip {
				skippedNodes[node] = struct{}{}
			}
		}

		if !branch.noDataFlow {
			toUpdate = append(toUpdate, ws...)
		}

		selected = append(selected, ws...)
	}

	for skip := range skippedNodes {
		skipped = append(skipped, skip)
	}

	return selected, skipped, toUpdate, nil
}

func copyTaskOutput(t *task) (forEdges []any, forBranchRun []any, forBranchUpdate []any) {
	// copy to: edges that has data flow, all branches, then branches with data flow
	var copyN, dataEdgeN int
	for _, edge := range t.call.writeTo {
		if !edge.noDataFlow {
			copyN++
			dataEdgeN++
		}
	}
	copyN += len(t.call.writeToBranches)
	for _, branch := range t.call.writeToBranches {
		if !branch.noDataFlow {
			copyN++
		}
	}
	vs := copyItem(t.output, copyN)

	forEdges = vs[:dataEdgeN]
	forBranchRun = vs[dataEdgeN : dataEdgeN+len(t.call.writeToBranches)]
	forBranchUpdate = vs[dataEdgeN+len(t.call.writeToBranches):]
	return
}

func (r *runner) createTasks(ctx context.Context, nodeMap map[string]any, optMap map[string][]any) ([]*task, error) {
	var nextTasks []*task
	for nodeKey, nodeInput := range nodeMap {
		call, ok := r.chanSubscribeTo[nodeKey]
		if !ok {
			return nil, fmt.Errorf("node[%s] has not been registered", nodeKey)
		}

		nextTasks = append(nextTasks, &task{
			ctx:     ctx,
			nodeKey: nodeKey,
			call:    call,
			input:   nodeInput,
			option:  optMap[nodeKey],
		})
	}
	return nextTasks, nil
}

func (r *runner) initTaskManager(runWrapper runnableCallWrapper, opts ...Option) *taskManager {
	return &taskManager{
		runWrapper: runWrapper,
		opts:       opts,
		needAll:    !r.eager,
		mu:         sync.Mutex{},
		l:          list.New(),
		done:       make(chan *task, 1),
	}
}

func (r *runner) initChannelManager(isStream bool) *channelManager {
	builder := r.chanBuilder
	if builder == nil {
		builder = pregelChannelBuilder
	}

	chs := make(map[string]channel)
	for ch := range r.chanSubscribeTo {
		chs[ch] = builder(r.invertedEdges[ch])
	}

	chs[END] = builder(r.invertedEdges[END])

	return &channelManager{
		isStream:   isStream,
		channels:   chs,
		successors: r.successors,

		edgeHandlerManager:    r.edgeHandlerManager,
		preNodeHandlerManager: r.preNodeHandlerManager,
	}
}

func (r *runner) toComposableRunnable() *composableRunnable {
	cr := &composableRunnable{
		i: func(ctx context.Context, input any, opts ...any) (output any, err error) {
			tos, err := convertOption[Option](opts...)
			if err != nil {
				return nil, err
			}
			return r.invoke(ctx, input, tos...)
		},
		t: func(ctx context.Context, input streamReader, opts ...any) (output streamReader, err error) {
			tos, err := convertOption[Option](opts...)
			if err != nil {
				return nil, err
			}
			return r.transform(ctx, input, tos...)
		},

		inputType:                  r.inputType,
		outputType:                 r.outputType,
		inputStreamFilter:          r.inputStreamFilter,
		inputConverter:             r.inputConverter,
		inputFieldMappingConverter: r.inputFieldMappingConverter,
		optionType:                 nil, // if option type is nil, graph will transmit all options.

		isPassthrough: false,
	}

	cr.i = genericInvokeWithCallbacks(cr.i)
	cr.t = genericTransformWithCallbacks(cr.t)

	return cr
}

func copyItem(item any, n int) []any {
	if n < 2 {
		return []any{item}
	}

	ret := make([]any, n)
	if s, ok := item.(streamReader); ok {
		ss := s.copy(n)
		for i := range ret {
			ret[i] = ss[i]
		}

		return ret
	}

	for i := range ret {
		ret[i] = item
	}

	return ret
}
