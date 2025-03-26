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

type chanCall struct {
	action          *composableRunnable
	writeTo         []string
	writeToBranches []*GraphBranch

	controls []string // branch must control

	preProcessor, postProcessor *composableRunnable
}

type chanBuilder func(dependencies []string, indirectDependencies []string, zeroValue func() any, emptyStream func() streamReader) channel

type runner struct {
	chanSubscribeTo map[string]*chanCall

	successors          map[string][]string
	dataPredecessors    map[string][]string
	controlPredecessors map[string][]string

	inputChannels *chanCall

	chanBuilder chanBuilder // could be nil
	eager       bool
	dag         bool

	runCtx func(ctx context.Context) context.Context

	options graphCompileOptions

	inputType  reflect.Type
	outputType reflect.Type

	// take effect as a subgraph through toComposableRunnable
	inputStreamFilter                               streamMapFilter
	inputConverter                                  handlerPair
	inputFieldMappingConverter                      handlerPair
	inputConvertStreamPair, outputConvertStreamPair streamConvertPair

	*genericHelper

	// checks need to do because cannot check at compile
	runtimeCheckEdges    map[string]map[string]bool
	runtimeCheckBranches map[string][]bool

	edgeHandlerManager      *edgeHandlerManager
	preNodeHandlerManager   *preNodeHandlerManager
	preBranchHandlerManager *preBranchHandlerManager

	checkPointer         *checkPointer
	interruptBeforeNodes []string
	interruptAfterNodes  []string
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
	var err error
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

	// Extract CheckPointID
	checkPointID, stateModifier := getCheckPointInfo(opts...)
	if checkPointID != nil && r.checkPointer.store == nil {
		return nil, fmt.Errorf("receive checkpoint id but have not set checkpoint store")
	}

	// Extract subgraph
	path, isSubGraph := getNodeKey(ctx)

	// load checkpoint from ctx/store or init graph
	initialized := false
	var nextTasks []*task
	if isSubGraph {
		// in subgraph, try to load checkpoint from ctx
		if cp := getCheckPointFromCtx(ctx); cp != nil {
			// load checkpoint from ctx
			initialized = true // don't init again

			err = r.checkPointer.restoreCheckPoint(cp, isStream)
			if err != nil {
				return nil, fmt.Errorf("restore checkpoint fail: %w", err)
			}

			cm.channels = cp.Channels
			if sm := getStateModifier(ctx); sm != nil && cp.State != nil {
				err = sm(ctx, path.path, cp.State)
				if err != nil {
					return nil, fmt.Errorf("state modifier fail: %w", err)
				}
			}
			if cp.State != nil {
				ctx = context.WithValue(ctx, stateKey{}, &internalState{state: cp.State})
			}

			nextTasks, err = r.restoreTasks(ctx, cp.Inputs, cp.SkipPreHandler, optMap) // should restore after set state to context
			if err != nil {
				return nil, fmt.Errorf("assemble tasks fail: %w", err)
			}
		}
	} else if checkPointID != nil {
		cp, err := getCheckPointFromStore(ctx, *checkPointID, r.checkPointer, isStream)
		if err != nil {
			return nil, fmt.Errorf("load checkpoint fail: %w", err)
		}
		if cp != nil {
			// load checkpoint from store
			initialized = true

			err = r.checkPointer.restoreCheckPoint(cp, isStream)
			if err != nil {
				return nil, fmt.Errorf("restore checkpoint fail: %w", err)
			}

			cm.channels = cp.Channels
			ctx = setStateModifier(ctx, stateModifier)
			ctx = setCheckPointToCtx(ctx, cp)
			if stateModifier != nil && cp.State != nil {
				err = stateModifier(ctx, []string{}, cp.State)
				if err != nil {
					return nil, fmt.Errorf("state modifier fail: %w", err)
				}
			}
			if cp.State != nil {
				ctx = context.WithValue(ctx, stateKey{}, &internalState{state: cp.State})
			}

			// resume graph
			nextTasks, err = r.restoreTasks(ctx, cp.Inputs, cp.SkipPreHandler, optMap)
			if err != nil {
				return nil, fmt.Errorf("assemble tasks fail: %w", err)
			}
		}
	}
	if !initialized {
		// have not init from checkpoint
		if r.runCtx != nil {
			ctx = r.runCtx(ctx)
		}
		var result any
		nextTasks, result, err = r.calculateNextTasks(ctx, []*task{{
			nodeKey: START,
			call:    r.inputChannels,
			output:  input,
		}}, isStream, cm, optMap)
		if err != nil {
			return nil, fmt.Errorf("calculate next tasks fail: %w", err)
		}
		if result != nil {
			return result, nil
		}
	}

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

		// 1. submit next tasks
		// 2. get completed tasks
		// 3. calculate next tasks

		err = tm.submit(nextTasks)
		if err != nil {
			return nil, fmt.Errorf("failed to submit tasks: %w", err)
		}
		var completedTasks []*task
		completedTasks, err = tm.wait()
		if err != nil {
			return nil, fmt.Errorf("failed to wait for tasks: %w", err)
		}

		subGraphInterrupts := map[string]*subGraphInterruptError{}
		var interruptBeforeNodes []string
		var interruptAfterNodes []string

		for i := 0; i < len(completedTasks); i++ {
			if completedTasks[i].err != nil {
				if info := isSubGraphInterrupt(completedTasks[i].err); info != nil {
					subGraphInterrupts[completedTasks[i].nodeKey] = info
					continue
				} else {
					return nil, fmt.Errorf("execute node[%s] fail: %w", completedTasks[i].nodeKey, completedTasks[i].err)
				}
			}
			for _, key := range r.interruptAfterNodes {
				if key == completedTasks[i].nodeKey {
					interruptAfterNodes = append(interruptAfterNodes, key)
					break
				}
			}
		}

		if len(subGraphInterrupts) > 0 {
			cpt, err := tm.waitAll()
			if err != nil {
				return nil, fmt.Errorf("failed to wait all tasks: %w", err)
			}
			interruptAfterNodes = append(interruptAfterNodes, getHitKey(cpt, r.interruptAfterNodes)...)
			// subgraph has interrupted
			// save other completed tasks to channel
			// save interrupted subgraph as next task with SkipPreHandler
			// report current graph interrupt info
			return nil, r.handleInterruptWithSubGraph(
				ctx,
				subGraphInterrupts,
				interruptAfterNodes,
				append(completedTasks, cpt...),
				checkPointID,
				isSubGraph,
				cm,
				isStream,
			)
		}

		if len(completedTasks) == 0 {
			return nil, errors.New("no tasks to execute")
		}

		var result any
		nextTasks, result, err = r.calculateNextTasks(ctx, completedTasks, isStream, cm, optMap)
		if err != nil {
			return nil, fmt.Errorf("failed to calculate next tasks: %w", err)
		}
		if result != nil {
			return result, nil
		}

		for i := range nextTasks {
			for _, key := range r.interruptBeforeNodes {
				if key == nextTasks[i].nodeKey {
					interruptBeforeNodes = append(interruptBeforeNodes, key)
					break
				}
			}
		}
		if len(interruptBeforeNodes) > 0 || len(interruptAfterNodes) > 0 {
			newCompletedTasks, err := tm.waitAll()
			if err != nil {
				return nil, fmt.Errorf("failed to wait all tasks: %w", err)
			}
			interruptAfterNodes = append(interruptAfterNodes, getHitKey(newCompletedTasks, r.interruptAfterNodes)...)

			newNextTasks, result, err := r.calculateNextTasks(ctx, newCompletedTasks, isStream, cm, optMap)
			if err != nil {
				return nil, fmt.Errorf("failed to calculate next tasks: %w", err)
			}

			if result != nil {
				return result, nil
			}

			interruptBeforeNodes = append(interruptBeforeNodes, getHitKey(newNextTasks, r.interruptBeforeNodes)...)

			// simple interrupt
			return nil, r.handleInterrupt(ctx, interruptBeforeNodes, interruptAfterNodes, append(nextTasks, newNextTasks...), cm.channels, isStream, isSubGraph, checkPointID)
		}
	}
}

func getHitKey(tasks []*task, keys []string) []string {
	var ret []string
	for _, t := range tasks {
		for _, key := range keys {
			if key == t.nodeKey {
				ret = append(ret, t.nodeKey)
			}
		}
	}
	return ret
}

func (r *runner) handleInterrupt(
	ctx context.Context,
	interruptBeforeNodes []string,
	interruptAfterNodes []string,
	nextTasks []*task,
	channels map[string]channel,
	isStream bool,
	isSubGraph bool,
	checkPointID *string,
) error {
	cp := &checkpoint{
		Channels:       channels,
		Inputs:         make(map[string]any),
		SkipPreHandler: false,
	}
	if state, ok := ctx.Value(stateKey{}).(*internalState); ok {
		cp.State = state.state
	}
	intInfo := &InterruptInfo{
		State:       cp.State,
		AfterNodes:  interruptAfterNodes,
		BeforeNodes: interruptBeforeNodes,
	}
	for _, t := range nextTasks {
		cp.Inputs[t.nodeKey] = t.input
	}
	err := r.checkPointer.convertCheckPoint(cp, isStream)
	if err != nil {
		return fmt.Errorf("failed to convert checkpoint: %w", err)
	}
	if isSubGraph {
		return &subGraphInterruptError{
			Info:       intInfo,
			CheckPoint: cp,
		}
	} else if checkPointID != nil {
		err := r.checkPointer.set(ctx, *checkPointID, cp)
		if err != nil {
			return fmt.Errorf("failed to set checkpoint: %w, checkPointID: %s", err, *checkPointID)
		}
	}
	return &interruptError{Info: intInfo}
}

func (r *runner) handleInterruptWithSubGraph(
	ctx context.Context,
	subGraphInterrupts map[string]*subGraphInterruptError,
	interruptAfterNodes []string,
	completeTasks []*task,
	checkPointID *string,
	isSubGraph bool,
	cm *channelManager,
	isStream bool,
) error {
	var interruptedTasks, otherTasks []*task
	for _, t := range completeTasks {
		if _, ok := subGraphInterrupts[t.nodeKey]; !ok {
			otherTasks = append(otherTasks, t)
		} else {
			interruptedTasks = append(interruptedTasks, t)
		}
	}

	toValue, controls, err := r.resolveCompletedTasks(ctx, otherTasks, isStream, cm)
	if err != nil {
		return fmt.Errorf("failed to resolve completed tasks in interrupt: %w", err)
	}
	err = cm.updateValues(ctx, toValue)
	if err != nil {
		return fmt.Errorf("failed to update values in interrupt: %w", err)
	}
	err = cm.updateDependencies(ctx, controls)
	if err != nil {
		return fmt.Errorf("failed to update dependencies in interrupt: %w", err)
	}

	cp := &checkpoint{
		Channels:       cm.channels,
		Inputs:         make(map[string]any),
		SkipPreHandler: true,
		SubGraphs:      make(map[string]*checkpoint),
	}
	if state, ok := ctx.Value(stateKey{}).(*internalState); ok {
		cp.State = state.state
	}
	intInfo := &InterruptInfo{
		State:      cp.State,
		AfterNodes: interruptAfterNodes,
		SubGraphs:  make(map[string]*InterruptInfo),
	}
	for _, t := range interruptedTasks {
		cp.Inputs[t.nodeKey] = t.input // t.input is empty, need checkpointer to handle
		cp.SubGraphs[t.nodeKey] = subGraphInterrupts[t.nodeKey].CheckPoint
		intInfo.SubGraphs[t.nodeKey] = subGraphInterrupts[t.nodeKey].Info
	}
	err = r.checkPointer.convertCheckPoint(cp, isStream)
	if err != nil {
		return fmt.Errorf("failed to convert checkpoint: %w", err)
	}
	if isSubGraph {
		return &subGraphInterruptError{
			Info:       intInfo,
			CheckPoint: cp,
		}
	} else if checkPointID != nil {
		err = r.checkPointer.set(ctx, *checkPointID, cp)
		if err != nil {
			return fmt.Errorf("failed to set checkpoint: %w, checkPointID: %s", err, *checkPointID)
		}
	}
	return &interruptError{Info: intInfo}
}

func (r *runner) calculateNextTasks(ctx context.Context, completedTasks []*task, isStream bool, cm *channelManager, optMap map[string][]any) ([]*task, any, error) {
	writeChannelValues, controls, err := r.resolveCompletedTasks(ctx, completedTasks, isStream, cm)
	if err != nil {
		return nil, nil, err
	}
	nodeMap, err := cm.updateAndGet(ctx, writeChannelValues, controls)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to update and get channels: %w", err)
	}
	var nextTasks []*task
	if len(nodeMap) > 0 {
		// Check if we've reached the END node.
		if v, ok := nodeMap[END]; ok {
			return nil, v, nil
		}

		// Create and submit the next batch of tasks.
		nextTasks, err = r.createTasks(ctx, nodeMap, optMap)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create tasks: %w", err)
		}
	}
	return nextTasks, nil, nil
}

func (r *runner) createTasks(ctx context.Context, nodeMap map[string]any, optMap map[string][]any) ([]*task, error) {
	var nextTasks []*task
	for nodeKey, nodeInput := range nodeMap {
		call, ok := r.chanSubscribeTo[nodeKey]
		if !ok {
			return nil, fmt.Errorf("node[%s] has not been registered", nodeKey)
		}

		nextTasks = append(nextTasks, &task{
			ctx:     forwardCheckPoint(setNodeKey(ctx, nodeKey), nodeKey),
			nodeKey: nodeKey,
			call:    call,
			input:   nodeInput,
			option:  optMap[nodeKey],
		})
	}
	return nextTasks, nil
}

func getCheckPointInfo(opts ...Option) (checkPointID *string, stateModifier StateModifier) {
	for _, opt := range opts {
		if opt.checkPointID != nil {
			checkPointID = opt.checkPointID
		}
		if opt.stateModifier != nil {
			stateModifier = opt.stateModifier
		}
	}
	return checkPointID, stateModifier
}

func (r *runner) restoreTasks(ctx context.Context, inputs map[string]any, skipPreHandler bool, optMap map[string][]any) ([]*task, error) {
	ret := make([]*task, 0, len(inputs))
	for key, input := range inputs {
		newTask := &task{
			ctx:            forwardCheckPoint(setNodeKey(ctx, key), key),
			nodeKey:        key,
			call:           nil,
			input:          input,
			option:         nil,
			skipPreHandler: skipPreHandler,
		}
		if opt, ok := optMap[key]; ok {
			newTask.option = opt
		}

		call, ok := r.chanSubscribeTo[key]
		if !ok {
			return nil, fmt.Errorf("channel[%s] from checkpoint is not registered", key)
		}
		newTask.call = call

		ret = append(ret, newTask)
	}
	return ret, nil
}

func (r *runner) resolveCompletedTasks(ctx context.Context, completedTasks []*task, isStream bool, cm *channelManager) (map[string]map[string]any, map[string][]string, error) {
	writeChannelValues := make(map[string]map[string]any)
	newDependencies := make(map[string][]string)
	for _, t := range completedTasks {
		for _, key := range t.call.controls {
			newDependencies[key] = append(newDependencies[key], t.nodeKey)
		}

		// update channel & new_next_tasks
		vs := copyItem(t.output, len(t.call.writeTo)+len(t.call.writeToBranches)*2)
		nextNodeKeys, err := r.calculateBranch(ctx, t.nodeKey, t.call,
			vs[len(t.call.writeTo)+len(t.call.writeToBranches):], isStream, cm)
		if err != nil {
			return nil, nil, fmt.Errorf("calculate next step fail, node: %s, error: %w", t.nodeKey, err)
		}

		for _, key := range nextNodeKeys {
			newDependencies[key] = append(newDependencies[key], t.nodeKey)
		}
		nextNodeKeys = append(nextNodeKeys, t.call.writeTo...)

		// If branches generates more than one successor, the inputs need to be copied accordingly.
		if len(nextNodeKeys) > 0 {
			toCopyNum := len(nextNodeKeys) - len(t.call.writeTo) - len(t.call.writeToBranches)
			nVs := copyItem(vs[len(t.call.writeTo)+len(t.call.writeToBranches)-1], toCopyNum+1)
			vs = append(vs[:len(t.call.writeTo)+len(t.call.writeToBranches)-1], nVs...)

			for i, next := range nextNodeKeys {
				if _, ok := writeChannelValues[next]; !ok {
					writeChannelValues[next] = make(map[string]any)
				}
				writeChannelValues[next][t.nodeKey] = vs[i]
			}
		}
	}
	return writeChannelValues, newDependencies, nil
}

func (r *runner) calculateBranch(ctx context.Context, curNodeKey string, startChan *chanCall, input []any, isStream bool, cm *channelManager) ([]string, error) {
	if len(input) < len(startChan.writeToBranches) {
		// unreachable
		return nil, errors.New("calculate next input length is shorter than branches")
	}

	ret := make([]string, 0, len(startChan.writeToBranches))

	skippedNodes := make(map[string]struct{})
	for i, branch := range startChan.writeToBranches {
		// check branch input type if needed
		var err error
		input[i], err = r.preBranchHandlerManager.handle(curNodeKey, i, input[i], isStream)
		if err != nil {
			return nil, fmt.Errorf("branch[%s]-[%d] pre handler fail: %w", curNodeKey, branch.idx, err)
		}

		// process branch output
		var ws []string
		if isStream {
			ws, err = branch.collect(ctx, input[i].(streamReader))
			if err != nil {
				return nil, fmt.Errorf("branch collect run error: %w", err)
			}
		} else {
			ws, err = branch.invoke(ctx, input[i])
			if err != nil {
				return nil, fmt.Errorf("branch invoke run error: %w", err)
			}
		}

		for node := range branch.endNodes {
			skipped := true
			for _, w := range ws {
				if node == w {
					skipped = false
					break
				}
			}
			if skipped {
				skippedNodes[node] = struct{}{}
			}
		}

		ret = append(ret, ws...)
	}

	// When a node has multiple branches,
	// there may be a situation where a succeeding node is selected by some branches and discarded by the other branches,
	// in which case the succeeding node should not be skipped.
	var skippedNodeList []string
	for _, selected := range ret {
		if _, ok := skippedNodes[selected]; ok {
			delete(skippedNodes, selected)
		}
	}
	for skipped := range skippedNodes {
		skippedNodeList = append(skippedNodeList, skipped)
	}

	err := cm.reportBranch(curNodeKey, skippedNodeList)
	if err != nil {
		return nil, err
	}
	return ret, nil
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
		chs[ch] = builder(r.controlPredecessors[ch], r.dataPredecessors[ch], r.chanSubscribeTo[ch].action.inputZeroValue, r.chanSubscribeTo[ch].action.inputEmptyStream)
	}

	chs[END] = builder(r.controlPredecessors[END], r.dataPredecessors[END], r.outputZeroValue, r.outputEmptyStream)

	dataPredecessors := make(map[string]map[string]struct{})
	for k, vs := range r.dataPredecessors {
		dataPredecessors[k] = make(map[string]struct{})
		for _, v := range vs {
			dataPredecessors[k][v] = struct{}{}
		}
	}
	controlPredecessors := make(map[string]map[string]struct{})
	for k, vs := range r.controlPredecessors {
		controlPredecessors[k] = make(map[string]struct{})
		for _, v := range vs {
			controlPredecessors[k][v] = struct{}{}
		}
	}

	return &channelManager{
		isStream:            isStream,
		channels:            chs,
		successors:          r.successors,
		dataPredecessors:    dataPredecessors,
		controlPredecessors: controlPredecessors,

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

		inputType:     r.inputType,
		outputType:    r.outputType,
		genericHelper: r.genericHelper,
		optionType:    nil, // if option type is nil, graph will transmit all options.
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
