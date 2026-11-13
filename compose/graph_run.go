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

	"github.com/cloudwego/eino/internal"
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
	inputStreamFilter          streamMapFilter
	inputConverter             handlerPair
	inputFieldMappingConverter handlerPair

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

	mergeConfigs map[string]FanInMergeConfig
}

func (r *runner) invoke(ctx context.Context, input any, opts ...Option) (any, error) {
	return r.run(ctx, false, input, opts...)
}

func (r *runner) transform(ctx context.Context, input streamReader, opts ...Option) (streamReader, error) {
	if r.options.localInterrupts {
		return nil, newGraphRunError(errors.New("WithLocalInterrupts does not support graph-level streaming; use Invoke"))
	}
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

func (r *runner) run(ctx context.Context, isStream bool, input any, opts ...Option) (result any, err error) {
	haveOnStart := false // delay triggering onGraphStart until state initialization is complete, so that the state can be accessed within onGraphStart.
	defer func() {
		if panicValue := recover(); panicValue != nil {
			panic(panicValue)
		}
		if !haveOnStart {
			ctx, input = onGraphStart(ctx, input, isStream)
		}
		if err != nil {
			ctx, err = onGraphError(ctx, err)
		} else {
			ctx, result = onGraphEnd(ctx, result, isStream)
		}
	}()

	var runWrapper runnableCallWrapper
	runWrapper = runnableInvoke
	if isStream {
		runWrapper = runnableTransform
	}

	// Initialize channel and task managers.
	cm := r.initChannelManager(isStream)
	tm := r.initTaskManager(runWrapper, getGraphCancel(ctx), opts...)
	maxSteps := r.options.maxRunSteps

	maxSteps, err = r.resolveMaxSteps(maxSteps, opts)
	if err != nil {
		return nil, err
	}

	// Extract and validate options for each node.
	optMap, extractErr := extractOption(r.chanSubscribeTo, opts...)
	if extractErr != nil {
		return nil, newGraphRunError(fmt.Errorf("graph extract option fail: %w", extractErr))
	}

	// Extract CheckPointID
	checkPointID, writeToCheckPointID, stateModifier, forceNewRun := getCheckPointInfo(opts...)
	subGraphCheckpointPublisher := getSubGraphCheckpointPublisher(opts...)
	if checkPointID != nil && r.checkPointer.store == nil {
		return nil, newGraphRunError(fmt.Errorf("receive checkpoint id but have not set checkpoint store"))
	}

	// Extract subgraph
	path, isSubGraph := getNodePath(ctx)

	// load checkpoint from ctx/store or init graph
	initialized := false
	var nextTasks []*task
	if cp := getCheckPointFromCtx(ctx); cp != nil {
		// in subgraph, try to load checkpoint from ctx
		initialized = true

		if err = r.validateCheckpointIntegrity(cp); err != nil {
			return nil, newGraphRunError(fmt.Errorf("invalid checkpoint: %w", err))
		}
		ctx, err = r.restoreCheckPointState(ctx, *path, getStateModifier(ctx), cp, isStream, cm)
		if err != nil {
			return nil, err
		}

		ctx, input = onGraphStart(ctx, input, isStream)
		haveOnStart = true

		nextTasks, err = r.restoreTasks(ctx, cp.Inputs, cp.SkipPreHandler, cp.RerunNodes, isStream, optMap)
		if err != nil {
			return nil, newGraphRunError(fmt.Errorf("restore tasks fail: %w", err))
		}
	} else if checkPointID != nil && !forceNewRun {
		cp, err = getCheckPointFromStore(ctx, *checkPointID, r.checkPointer)
		if err != nil {
			return nil, newGraphRunError(fmt.Errorf("load checkpoint from store fail: %w", err))
		}
		if cp != nil {
			// load checkpoint from store
			initialized = true

			if err = r.validateCheckpointIntegrity(cp); err != nil {
				return nil, newGraphRunError(fmt.Errorf("invalid checkpoint: %w", err))
			}
			ctx = setStateModifier(ctx, stateModifier)
			ctx = setCheckPointToCtx(ctx, cp)

			ctx, err = r.restoreCheckPointState(ctx, *NewNodePath(), stateModifier, cp, isStream, cm)
			if err != nil {
				return nil, err
			}

			ctx, input = onGraphStart(ctx, input, isStream)
			haveOnStart = true

			nextTasks, err = r.restoreTasks(ctx, cp.Inputs, cp.SkipPreHandler, cp.RerunNodes, isStream, optMap)
			if err != nil {
				return nil, newGraphRunError(fmt.Errorf("restore tasks fail: %w", err))
			}
		}
	}
	if !initialized {
		// have not inited from checkpoint
		if r.runCtx != nil {
			ctx = r.runCtx(ctx)
		}

		ctx, input = onGraphStart(ctx, input, isStream)
		haveOnStart = true

		var isEnd bool
		nextTasks, result, isEnd, err = r.calculateNextTasks(ctx, []*task{{
			nodeKey: START,
			call:    r.inputChannels,
			output:  input,
		}}, isStream, cm, optMap)
		if err != nil {
			return nil, newGraphRunError(fmt.Errorf("calculate next tasks fail: %w", err))
		}
		if isEnd {
			return result, nil
		}
		if len(nextTasks) == 0 && !r.options.localInterrupts {
			return nil, newGraphRunError(fmt.Errorf("no tasks to execute after graph start"))
		}

		if keys := getHitKey(nextTasks, r.interruptBeforeNodes); len(keys) > 0 {
			tempInfo := newInterruptTempInfo()
			tempInfo.interruptBeforeNodes = append(tempInfo.interruptBeforeNodes, keys...)
			return nil, r.handleInterrupt(ctx,
				tempInfo,
				nextTasks,
				cm.channels,
				isStream,
				isSubGraph,
				writeToCheckPointID,
				subGraphCheckpointPublisher,
			)
		}
	}

	// used to reporting NoTask error
	var lastCompletedTask []*task
	localInfo := newInterruptTempInfo()
	localTasks := &interruptTasks{}
	saveConfig := &interruptSaveConfig{
		channels: cm.channels, isStream: isStream, isSubGraph: isSubGraph,
		checkPointID: writeToCheckPointID, publish: subGraphCheckpointPublisher,
	}
	restoredPending := initialized && len(nextTasks) > 0
	// Global stops settle in-flight work without consuming additional ready inputs.
	stopLocal := func() error {
		completed, canceled := tm.waitAll()
		if e := r.collectLocalResults(ctx, localInfo, localTasks, completed, canceled, cm); e != nil {
			return e
		}
		if ctx.Err() != nil {
			return newGraphRunError(fmt.Errorf("context has been canceled: %w", ctx.Err()))
		}
		localTasks.pending = nextTasks
		return r.saveInterrupt(ctx, localInfo, localTasks, saveConfig)
	}

	// Main execution loop.
	for step := 0; ; step++ {
		// Check for context cancellation.
		select {
		case <-ctx.Done():
			_, _ = tm.waitAll()
			return nil, newGraphRunError(fmt.Errorf("context has been canceled: %w", ctx.Err()))
		default:
		}
		if !r.dag && step >= maxSteps {
			return nil, newGraphRunError(ErrExceedMaxSteps)
		}

		if r.options.localInterrupts {
			if !restoredPending && (tm.canceled || graphInterruptRequested(tm.cancel) || len(localInfo.interruptAfterNodes) > 0) {
				return nil, stopLocal()
			}
			// Restored tasks run first. Only then scan historical ready channels,
			// even if every restored task has interrupted again.
			if len(nextTasks) == 0 {
				ready, e := cm.getFromReadyChannels(ctx, false)
				if e != nil {
					return nil, newGraphRunError(e)
				}
				nextTasks, e = r.createTasks(ctx, ready, optMap)
				if e != nil {
					return nil, newGraphRunError(e)
				}
				localInfo.interruptBeforeNodes = getHitKey(nextTasks, r.interruptBeforeNodes)
				if len(localInfo.interruptBeforeNodes) > 0 {
					return nil, stopLocal()
				}
			}
			if len(nextTasks) == 0 && tm.num == 0 {
				if localInfo.hasWaits() {
					return nil, r.saveInterrupt(ctx, localInfo, localTasks, saveConfig)
				}
				value, ready, e := cm.getFromChannel(ctx, END)
				if e != nil {
					return nil, newGraphRunError(e)
				}
				if ready {
					return value, nil
				}
				return nil, newGraphRunError(fmt.Errorf("no tasks to execute, last completed nodes: %v", printTask(lastCompletedTask)))
			}
		}

		// 1. submit next tasks
		// 2. get completed tasks
		// 3. calculate next tasks

		err = tm.submit(nextTasks)
		nextTasks = nil // submitted inputs must never be dispatched again
		restoredPending = false
		if err != nil {
			return nil, newGraphRunError(fmt.Errorf("failed to submit tasks: %w", err))
		}

		var totalCanceledTasks []*task

		completedTasks, canceled, canceledTasks := tm.wait()
		if r.options.localInterrupts {
			localInfo.collectCanceledInfo(canceled, canceledTasks, completedTasks)
			if err = r.collectLocalResults(ctx, localInfo, localTasks, completedTasks, canceledTasks, cm); err != nil {
				return nil, err
			}
			lastCompletedTask = completedTasks
			continue
		}
		totalCanceledTasks = append(totalCanceledTasks, canceledTasks...)
		tempInfo := newInterruptTempInfo()
		tempInfo.collectCanceledInfo(canceled, canceledTasks, completedTasks)

		err = r.resolveInterruptCompletedTasks(tempInfo, completedTasks)
		if err != nil {
			return nil, err // err has been wrapped
		}

		if len(tempInfo.subGraphInterrupts)+len(tempInfo.interruptRerunNodes) > 0 {
			var newCompletedTasks []*task
			newCompletedTasks, canceledTasks = tm.waitAll()
			totalCanceledTasks = append(totalCanceledTasks, canceledTasks...)
			for _, ct := range canceledTasks {
				// handle timeout tasks as rerun
				tempInfo.interruptRerunNodes = append(tempInfo.interruptRerunNodes, ct.nodeKey)
			}

			err = r.resolveInterruptCompletedTasks(tempInfo, newCompletedTasks)
			if err != nil {
				return nil, err // err has been wrapped
			}

			// subgraph has interrupted
			// save other completed tasks to channel
			// save interrupted subgraph as next task with SkipPreHandler
			// report current graph interrupt info
			return nil, r.handleInterruptWithSubGraphAndRerunNodes(
				ctx,
				tempInfo,
				append(append(completedTasks, newCompletedTasks...), totalCanceledTasks...), // canceled tasks are handled as rerun
				nil,
				writeToCheckPointID,
				isSubGraph,
				cm,
				isStream,
				subGraphCheckpointPublisher,
			)
		}

		if len(completedTasks) == 0 {
			return nil, newGraphRunError(fmt.Errorf("no tasks to execute, last completed nodes: %v", printTask(lastCompletedTask)))
		}
		lastCompletedTask = completedTasks

		var isEnd bool
		nextTasks, result, isEnd, err = r.calculateNextTasks(ctx, completedTasks, isStream, cm, optMap)
		if err != nil {
			return nil, newGraphRunError(fmt.Errorf("failed to calculate next tasks: %w", err))
		}
		if isEnd {
			return result, nil
		}

		tempInfo.interruptBeforeNodes = getHitKey(nextTasks, r.interruptBeforeNodes)

		if len(tempInfo.interruptBeforeNodes) > 0 || len(tempInfo.interruptAfterNodes) > 0 {
			var newCompletedTasks []*task
			newCompletedTasks, canceledTasks = tm.waitAll()
			totalCanceledTasks = append(totalCanceledTasks, canceledTasks...)
			for _, ct := range canceledTasks {
				tempInfo.interruptRerunNodes = append(tempInfo.interruptRerunNodes, ct.nodeKey)
			}

			err = r.resolveInterruptCompletedTasks(tempInfo, newCompletedTasks)
			if err != nil {
				return nil, err // err has been wrapped
			}

			if len(tempInfo.subGraphInterrupts)+len(tempInfo.interruptRerunNodes) > 0 {
				return nil, r.handleInterruptWithSubGraphAndRerunNodes(
					ctx,
					tempInfo,
					append(newCompletedTasks, totalCanceledTasks...),
					nextTasks,
					writeToCheckPointID,
					isSubGraph,
					cm,
					isStream,
					subGraphCheckpointPublisher,
				)
			}

			var newNextTasks []*task
			newNextTasks, result, isEnd, err = r.calculateNextTasks(ctx, newCompletedTasks, isStream, cm, optMap)
			if err != nil {
				return nil, newGraphRunError(fmt.Errorf("failed to calculate next tasks: %w", err))
			}

			if isEnd {
				return result, nil
			}

			tempInfo.interruptBeforeNodes = append(tempInfo.interruptBeforeNodes, getHitKey(newNextTasks, r.interruptBeforeNodes)...)

			// simple interrupt
			return nil, r.handleInterrupt(
				ctx,
				tempInfo,
				append(nextTasks, newNextTasks...),
				cm.channels,
				isStream,
				isSubGraph,
				writeToCheckPointID,
				subGraphCheckpointPublisher,
			)
		}
	}
}

func (r *runner) resolveMaxSteps(maxSteps int, opts []Option) (int, error) {
	if r.dag {
		for i := range opts {
			if opts[i].maxRunSteps > 0 {
				return 0, newGraphRunError(fmt.Errorf("cannot set max run steps in dag"))
			}
		}
		return maxSteps, nil
	}
	for i := range opts {
		if opts[i].maxRunSteps > 0 {
			maxSteps = opts[i].maxRunSteps
		}
	}
	if maxSteps < 1 {
		return 0, newGraphRunError(errors.New("max run steps limit must be at least 1"))
	}
	return maxSteps, nil
}

func (r *runner) restoreCheckPointState(
	ctx context.Context,
	path NodePath,
	sm StateModifier,
	cp *checkpoint,
	isStream bool,
	cm *channelManager,
) (context.Context, error) {
	err := r.checkPointer.restoreCheckPoint(cp, isStream)
	if err != nil {
		return ctx, newGraphRunError(fmt.Errorf("restore checkpoint fail: %w", err))
	}

	err = cm.loadChannels(cp.Channels)
	if err != nil {
		return ctx, newGraphRunError(err)
	}
	if sm != nil && cp.State != nil {
		err = sm(ctx, path, cp.State)
		if err != nil {
			return ctx, newGraphRunError(fmt.Errorf("state modifier fail: %w", err))
		}
	}
	if cp.State != nil {
		isResumeTarget, hasData, data := GetResumeContext[any](ctx)
		if isResumeTarget && hasData {
			cp.State = data
		}

		var parent *internalState
		if prev := ctx.Value(stateKey{}); prev != nil {
			if p, ok := prev.(*internalState); ok {
				parent = p
			}
		}

		ctx = context.WithValue(ctx, stateKey{}, &internalState{state: cp.State, parent: parent})
	}

	return ctx, nil
}

func (r *runner) handleInterrupt(
	ctx context.Context, info *interruptTempInfo, nextTasks []*task,
	channels map[string]channel, isStream, isSubGraph bool, checkPointID *string,
	publish func(*subGraphInterruptError),
) error {
	return r.saveInterrupt(ctx, info, &interruptTasks{pending: nextTasks}, &interruptSaveConfig{
		channels: channels, isStream: isStream, isSubGraph: isSubGraph,
		checkPointID: checkPointID, publish: publish,
	})
}

func (r *runner) handleInterruptWithSubGraphAndRerunNodes(
	ctx context.Context, info *interruptTempInfo, completeTasks, pendingTasks []*task,
	checkPointID *string, isSubGraph bool, cm *channelManager, isStream bool,
	publish func(*subGraphInterruptError),
) error {
	tasks := &interruptTasks{pending: pendingTasks}
	if err := r.forwardCompletedTasks(ctx, tasks.collect(info, completeTasks), isStream, cm); err != nil {
		return fmt.Errorf("failed to forward completed tasks in interrupt: %w", err)
	}
	return r.saveInterrupt(ctx, info, tasks, &interruptSaveConfig{
		channels: cm.channels, isStream: isStream, isSubGraph: isSubGraph,
		checkPointID: checkPointID, publish: publish,
	})
}

// forwardCompletedTasks propagates successful results after node post-processing.
// Scheduling and checkpointing remain separate so local waits can defer END.
func (r *runner) forwardCompletedTasks(ctx context.Context, tasks []*task, isStream bool, cm *channelManager) error {
	values, controls, err := r.resolveCompletedTasks(ctx, tasks, isStream, cm)
	if err != nil {
		return err
	}
	if err = cm.updateValues(ctx, values); err != nil {
		return err
	}
	return cm.updateDependencies(ctx, controls)
}

func (r *runner) calculateNextTasks(ctx context.Context, completedTasks []*task, isStream bool, cm *channelManager, optMap map[string][]any) ([]*task, any, bool, error) {
	if err := r.forwardCompletedTasks(ctx, completedTasks, isStream, cm); err != nil {
		return nil, nil, false, err
	}
	nodeMap, err := cm.getFromReadyChannels(ctx, !r.options.localInterrupts)
	if err != nil {
		return nil, nil, false, fmt.Errorf("failed to get ready channels: %w", err)
	}
	var nextTasks []*task
	if len(nodeMap) > 0 {
		// Check if we've reached the END node.
		if v, ok := nodeMap[END]; ok {
			return nil, v, true, nil
		}

		// Create and submit the next batch of tasks.
		nextTasks, err = r.createTasks(ctx, nodeMap, optMap)
		if err != nil {
			return nil, nil, false, fmt.Errorf("failed to create tasks: %w", err)
		}
	}
	return nextTasks, nil, false, nil
}

func (r *runner) createTasks(ctx context.Context, nodeMap map[string]any, optMap map[string][]any) ([]*task, error) {
	var nextTasks []*task
	for nodeKey, nodeInput := range nodeMap {
		call, ok := r.chanSubscribeTo[nodeKey]
		if !ok {
			return nil, fmt.Errorf("node[%s] has not been registered", nodeKey)
		}

		taskCtx := ctx
		var subGraphCheckpointReady <-chan *subGraphInterruptError
		taskOpts := optMap[nodeKey]
		if call.action.nodeInfo != nil && call.action.nodeInfo.compileOption != nil {
			taskCtx = forwardCheckPoint(taskCtx, nodeKey)
		}
		if isSubGraphCall(call) {
			taskOpts, subGraphCheckpointReady = withSubGraphCheckpointPublisher(taskOpts)
		}

		nextTasks = append(nextTasks, &task{
			ctx:                     AppendAddressSegment(taskCtx, AddressSegmentNode, nodeKey),
			nodeKey:                 nodeKey,
			call:                    call,
			input:                   nodeInput,
			option:                  taskOpts,
			subGraphCheckpointReady: subGraphCheckpointReady,
		})
	}
	return nextTasks, nil
}

func getCheckPointInfo(opts ...Option) (checkPointID *string, writeToCheckPointID *string, stateModifier StateModifier, forceNewRun bool) {
	for _, opt := range opts {
		if opt.checkPointID != nil {
			checkPointID = opt.checkPointID
		}
		if opt.writeToCheckPointID != nil {
			writeToCheckPointID = opt.writeToCheckPointID
		}
		if opt.stateModifier != nil {
			stateModifier = opt.stateModifier
		}
		forceNewRun = opt.forceNewRun
	}
	if writeToCheckPointID == nil {
		writeToCheckPointID = checkPointID
	}
	return
}

func (r *runner) restoreTasks(
	ctx context.Context,
	inputs map[string]any,
	skipPreHandler map[string]bool,
	rerunNodes []string,
	isStream bool,
	optMap map[string][]any) ([]*task, error) {
	ret := make([]*task, 0, len(inputs))
	syntheticInputs := make(map[string]struct{})
	for _, key := range rerunNodes {
		if _, hasInput := inputs[key]; hasInput {
			continue
		}

		call, ok := r.chanSubscribeTo[key]
		if !ok {
			return nil, fmt.Errorf("channel[%s] from checkpoint is not registered", key)
		}
		if isStream {
			inputs[key] = call.action.inputEmptyStream()
		} else {
			inputs[key] = call.action.inputZeroValue()
		}
		syntheticInputs[key] = struct{}{}
	}
	for key, input := range inputs {
		call, ok := r.chanSubscribeTo[key]
		if !ok {
			return nil, fmt.Errorf("channel[%s] from checkpoint is not registered", key)
		}

		taskCtx := ctx
		var subGraphCheckpointReady <-chan *subGraphInterruptError
		taskOpts := optMap[key]
		if call.action.nodeInfo != nil && call.action.nodeInfo.compileOption != nil {
			// sub graph
			taskCtx = forwardCheckPoint(taskCtx, key)
		}
		if isSubGraphCall(call) {
			taskOpts, subGraphCheckpointReady = withSubGraphCheckpointPublisher(taskOpts)
		}

		newTask := &task{
			ctx:                     AppendAddressSegment(taskCtx, AddressSegmentNode, key),
			nodeKey:                 key,
			call:                    call,
			input:                   input,
			option:                  taskOpts,
			skipPreHandler:          skipPreHandler[key],
			subGraphCheckpointReady: subGraphCheckpointReady,
		}
		if _, ok := syntheticInputs[key]; ok {
			newTask.syntheticRerunInput = true
		}
		ret = append(ret, newTask)
	}
	return ret, nil
}

func (r *runner) validateCheckpointIntegrity(cp *checkpoint) error {
	for _, key := range cp.RerunNodes {
		call, ok := r.chanSubscribeTo[key]
		if !ok || !isSubGraphCall(call) {
			continue
		}
		if subCP, hasSubGraph := cp.SubGraphs[key]; hasSubGraph && subCP != nil {
			continue
		}
		if _, hasInput := cp.Inputs[key]; hasInput {
			continue
		}
		return fmt.Errorf("subgraph node %q is marked for rerun without a nested checkpoint or persisted input", key)
	}
	return nil
}

func isSubGraphCall(call *chanCall) bool {
	if call == nil || call.action == nil || call.action.meta == nil {
		return false
	}
	cmp := call.action.meta.component
	return cmp == ComponentOfGraph || cmp == ComponentOfChain || cmp == ComponentOfWorkflow
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

func (r *runner) initTaskManager(runWrapper runnableCallWrapper, cancelVal *graphCancelSignal, opts ...Option) *taskManager {
	tm := &taskManager{
		runWrapper:        runWrapper,
		opts:              opts,
		needAll:           !r.eager,
		done:              internal.NewUnboundedChan[*task](),
		runningTasks:      make(map[string]*task),
		persistRerunInput: cancelVal != nil,
	}
	if cancelVal != nil {
		tm.cancel = cancelVal
	}
	return tm
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

	for k, v := range chs {
		if cfg, ok := r.mergeConfigs[k]; ok {
			v.setMergeConfig(cfg)
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

func printTask(ts []*task) string {
	if len(ts) == 0 {
		return "[]"
	}
	sb := strings.Builder{}
	sb.WriteString("[")
	for i := 0; i < len(ts)-1; i++ {
		sb.WriteString(ts[i].nodeKey)
		sb.WriteString(", ")
	}
	sb.WriteString(ts[len(ts)-1].nodeKey)
	sb.WriteString("]")
	return sb.String()
}
