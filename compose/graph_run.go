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
	"sort"
	"strings"

	"github.com/cloudwego/eino/internal"
	"github.com/cloudwego/eino/internal/core"
	"github.com/cloudwego/eino/internal/serialization"
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
		if err = consumeCheckpointLayoutMetadata(cp); err != nil {
			return nil, newGraphRunError(fmt.Errorf("invalid checkpoint metadata: %w", err))
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
			if err = consumeCheckpointLayoutMetadata(cp); err != nil {
				return nil, newGraphRunError(fmt.Errorf("invalid checkpoint metadata: %w", err))
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
		if len(nextTasks) == 0 {
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

		// 1. submit next tasks
		// 2. get completed tasks
		// 3. calculate next tasks

		err = tm.submit(nextTasks)
		if err != nil {
			return nil, newGraphRunError(fmt.Errorf("failed to submit tasks: %w", err))
		}

		var totalCanceledTasks []*task

		completedTasks, canceled, canceledTasks := tm.wait()
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
					append(append(completedTasks, newCompletedTasks...), totalCanceledTasks...),
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

func newInterruptTempInfo() *interruptTempInfo {
	return &interruptTempInfo{
		subGraphInterrupts:  map[string]*subGraphInterruptError{},
		interruptTaskNodes:  map[string]struct{}{},
		interruptRerunExtra: map[string]any{},
	}
}

type interruptTempInfo struct {
	subGraphInterrupts   map[string]*subGraphInterruptError
	interruptTaskNodes   map[string]struct{}
	interruptRerunNodes  []string
	interruptBeforeNodes []string
	interruptAfterNodes  []string
	interruptRerunExtra  map[string]any

	signals []*core.InterruptSignal
}

func (ti *interruptTempInfo) appendSignal(signal *core.InterruptSignal) {
	if signal == nil {
		return
	}
	for _, existing := range ti.signals {
		if existing == signal || signal.ID != "" && existing.ID == signal.ID {
			return
		}
	}
	ti.signals = append(ti.signals, signal)
}

func (ti *interruptTempInfo) collectCanceledInfo(canceled bool, canceledTasks, completedTasks []*task) {
	if !canceled {
		return
	}

	if len(canceledTasks) > 0 {
		for _, t := range canceledTasks {
			ti.interruptRerunNodes = append(ti.interruptRerunNodes, t.nodeKey)
		}
	} else {
		for _, t := range completedTasks {
			ti.interruptAfterNodes = append(ti.interruptAfterNodes, t.nodeKey)
		}
	}
}

func (r *runner) resolveInterruptCompletedTasks(tempInfo *interruptTempInfo, completedTasks []*task) (err error) {
	for _, completedTask := range completedTasks {
		if completedTask.err != nil {
			if info := isSubGraphInterrupt(completedTask.err); info != nil {
				tempInfo.subGraphInterrupts[completedTask.nodeKey] = info
				tempInfo.appendSignal(info.signal)
				continue
			}

			ire := &core.InterruptSignal{}
			if errors.As(completedTask.err, &ire) {
				tempInfo.interruptTaskNodes[completedTask.nodeKey] = struct{}{}
				// A downstream node can observe an interrupt while consuming a
				// stream produced by an earlier node. Rerun the addressed node.
				rerunNodeKey := r.interruptOriginNodeKey(completedTask, ire.Address)
				tempInfo.interruptRerunNodes = appendIfNotExist(tempInfo.interruptRerunNodes, rerunNodeKey)
				if ire.Info != nil {
					tempInfo.interruptRerunExtra[rerunNodeKey] = ire.InterruptInfo.Info
				}

				tempInfo.appendSignal(ire)
				continue
			}

			return wrapGraphNodeError(completedTask.nodeKey, completedTask.err)
		}

		for _, key := range r.interruptAfterNodes {
			if key == completedTask.nodeKey {
				tempInfo.interruptAfterNodes = append(tempInfo.interruptAfterNodes, key)
				break
			}
		}
	}
	return nil
}

func (r *runner) interruptOriginNodeKey(completedTask *task, address Address) string {
	taskAddress := GetCurrentAddress(completedTask.ctx)
	if len(taskAddress) == 0 || taskAddress[len(taskAddress)-1].Type != AddressSegmentNode {
		return completedTask.nodeKey
	}

	graphAddress := taskAddress[:len(taskAddress)-1]
	if len(address) <= len(graphAddress) || !address[:len(graphAddress)].Equals(graphAddress) {
		return completedTask.nodeKey
	}

	segment := address[len(graphAddress)]
	if segment.Type == AddressSegmentNode {
		if _, ok := r.chanSubscribeTo[segment.ID]; ok {
			return segment.ID
		}
	}
	return completedTask.nodeKey
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
	tempInfo *interruptTempInfo,
	nextTasks []*task,
	channels map[string]channel,
	isStream bool,
	isSubGraph bool,
	checkPointID *string,
	publishSubGraphCheckpoint func(*subGraphInterruptError),
) error {
	cp := &checkpoint{
		Channels:       channels,
		Inputs:         make(map[string]any),
		SkipPreHandler: map[string]bool{},
	}
	if r.runCtx != nil {
		// current graph has enable state
		if state, ok := ctx.Value(stateKey{}).(*internalState); ok {
			state.mu.Lock()
			copiedState, err := deepCopyState(state.state)
			state.mu.Unlock()
			if err != nil {
				return fmt.Errorf("failed to copy state: %w", err)
			}
			cp.State = copiedState
		}
	}

	intInfo := &InterruptInfo{
		State:           cp.State,
		AfterNodes:      tempInfo.interruptAfterNodes,
		BeforeNodes:     tempInfo.interruptBeforeNodes,
		RerunNodes:      tempInfo.interruptRerunNodes,
		RerunNodesExtra: tempInfo.interruptRerunExtra,
		SubGraphs:       make(map[string]*InterruptInfo),
	}

	info := cp.State

	is, err := core.Interrupt(ctx, info, nil, tempInfo.signals)
	if err != nil {
		return fmt.Errorf("failed to interrupt: %w", err)
	}

	cp.InterruptID2Addr, cp.InterruptID2State = core.SignalToPersistenceMaps(is)
	if err = initializeCheckpointLayoutV1(cp); err != nil {
		return fmt.Errorf("failed to initialize checkpoint layout: %w", err)
	}

	for _, t := range nextTasks {
		cp.Inputs[t.nodeKey] = t.input
	}
	err = r.checkPointer.convertCheckPoint(cp, isStream)
	if err != nil {
		return fmt.Errorf("failed to convert checkpoint: %w", err)
	}
	if isSubGraph {
		subGraphInterrupt := &subGraphInterruptError{
			Info:       intInfo,
			CheckPoint: cp,
			signal:     is,
		}
		if publishSubGraphCheckpoint != nil {
			publishSubGraphCheckpoint(subGraphInterrupt)
		}
		return subGraphInterrupt
	} else if checkPointID != nil {
		err := r.checkPointer.set(ctx, *checkPointID, cp)
		if err != nil {
			return fmt.Errorf("failed to set checkpoint: %w, checkPointID: %s", err, *checkPointID)
		}
	}

	intInfo.InterruptContexts = core.ToInterruptContexts(is, nil)
	return &interruptError{Info: intInfo}
}

// deepCopyState creates a deep copy of the state using serialization
func deepCopyState(state any) (any, error) {
	if state == nil {
		return nil, nil
	}
	serializer := &serialization.InternalSerializer{}
	data, err := serializer.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal state: %w", err)
	}

	// Create new instance of the same type
	stateType := reflect.TypeOf(state)
	isPtr := stateType.Kind() == reflect.Ptr
	if isPtr {
		stateType = stateType.Elem()
	}
	newStatePtr := reflect.New(stateType).Interface()
	if err := serializer.Unmarshal(data, newStatePtr); err != nil {
		return nil, fmt.Errorf("failed to unmarshal state: %w", err)
	}
	if isPtr {
		return newStatePtr, nil
	}
	return reflect.ValueOf(newStatePtr).Elem().Interface(), nil
}

func (r *runner) handleInterruptWithSubGraphAndRerunNodes(
	ctx context.Context,
	tempInfo *interruptTempInfo,
	completeTasks []*task,
	checkPointID *string,
	isSubGraph bool,
	cm *channelManager,
	isStream bool,
	publishSubGraphCheckpoint func(*subGraphInterruptError),
) error {
	var rerunTasks, subgraphTasks, otherTasks []*task
	skipPreHandler := map[string]bool{}
	for _, t := range completeTasks {
		if _, ok := tempInfo.subGraphInterrupts[t.nodeKey]; ok {
			subgraphTasks = append(subgraphTasks, t)
			skipPreHandler[t.nodeKey] = true // subgraph won't run pre-handler again, but rerun nodes will
			continue
		}
		if _, ok := tempInfo.interruptTaskNodes[t.nodeKey]; ok {
			for _, key := range tempInfo.interruptRerunNodes {
				if key == t.nodeKey {
					rerunTasks = append(rerunTasks, t)
					break
				}
			}
			continue
		}
		rerun := false
		for _, key := range tempInfo.interruptRerunNodes {
			if key == t.nodeKey {
				rerunTasks = append(rerunTasks, t)
				rerun = true
				break
			}
		}
		if !rerun {
			otherTasks = append(otherTasks, t)
		}
	}

	// forward completed tasks
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
		SkipPreHandler: skipPreHandler,
		SubGraphs:      make(map[string]*checkpoint),
	}
	if r.runCtx != nil {
		// current graph has enable state
		if state, ok := ctx.Value(stateKey{}).(*internalState); ok {
			state.mu.Lock()
			copiedState, err_ := deepCopyState(state.state)
			state.mu.Unlock()
			if err_ != nil {
				return fmt.Errorf("failed to copy state: %w", err_)
			}
			cp.State = copiedState
		}
	}

	intInfo := &InterruptInfo{
		State:           cp.State,
		BeforeNodes:     tempInfo.interruptBeforeNodes,
		AfterNodes:      tempInfo.interruptAfterNodes,
		RerunNodes:      tempInfo.interruptRerunNodes,
		RerunNodesExtra: tempInfo.interruptRerunExtra,
		SubGraphs:       make(map[string]*InterruptInfo),
	}

	info := cp.State

	is, err := core.Interrupt(ctx, info, nil, tempInfo.signals)
	if err != nil {
		return fmt.Errorf("failed to interrupt: %w", err)
	}

	cp.InterruptID2Addr, cp.InterruptID2State = core.SignalToPersistenceMaps(is)
	if err = initializeCheckpointLayoutV1(cp); err != nil {
		return fmt.Errorf("failed to initialize checkpoint layout: %w", err)
	}

	for _, t := range subgraphTasks {
		cp.RerunNodes = append(cp.RerunNodes, t.nodeKey)
		cp.SubGraphs[t.nodeKey] = tempInfo.subGraphInterrupts[t.nodeKey].CheckPoint
		intInfo.SubGraphs[t.nodeKey] = tempInfo.subGraphInterrupts[t.nodeKey].Info
	}
	removeSubgraphOwnedInterruptStates(cp)
	cp.RerunNodes = append(cp.RerunNodes, tempInfo.interruptRerunNodes...)
	for _, t := range rerunTasks {
		if t.originalInput != nil {
			cp.Inputs[t.nodeKey] = t.originalInput
		}
	}
	if err = r.validateCheckpointIntegrity(cp); err != nil {
		return fmt.Errorf("invalid checkpoint: %w", err)
	}
	err = r.checkPointer.convertCheckPoint(cp, isStream)
	if err != nil {
		return fmt.Errorf("failed to convert checkpoint: %w", err)
	}
	if isSubGraph {
		subGraphInterrupt := &subGraphInterruptError{
			Info:       intInfo,
			CheckPoint: cp,
			signal:     is,
		}
		if publishSubGraphCheckpoint != nil {
			publishSubGraphCheckpoint(subGraphInterrupt)
		}
		return subGraphInterrupt
	} else if checkPointID != nil {
		err = r.checkPointer.set(ctx, *checkPointID, cp)
		if err != nil {
			return fmt.Errorf("failed to set checkpoint: %w, checkPointID: %s", err, *checkPointID)
		}
	}
	intInfo.InterruptContexts = core.ToInterruptContexts(is, nil)
	return &interruptError{Info: intInfo}
}

func (r *runner) calculateNextTasks(ctx context.Context, completedTasks []*task, isStream bool, cm *channelManager, optMap map[string][]any) ([]*task, any, bool, error) {
	writeChannelValues, controls, err := r.resolveCompletedTasks(ctx, completedTasks, isStream, cm)
	if err != nil {
		return nil, nil, false, err
	}
	nodeMap, err := cm.updateAndGet(ctx, writeChannelValues, controls)
	if err != nil {
		return nil, nil, false, fmt.Errorf("failed to update and get channels: %w", err)
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
			var err error
			taskCtx, err = forwardCheckPoint(taskCtx, nodeKey)
			if err != nil {
				return nil, err
			}
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
			var err error
			taskCtx, err = forwardCheckPoint(taskCtx, key)
			if err != nil {
				return nil, err
			}
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
	if err := validateCheckpointTreeMetadata(cp); err != nil {
		return err
	}
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
	if cp.StateLayoutVersion == checkpointStateLayoutVersionV1 {
		owners := make(map[string]checkpointStateOwner)
		if err := collectCheckpointStateOwners(cp, nil, owners); err != nil {
			return err
		}
		routes := make(map[string][]checkpointStateRoute)
		if err := collectCheckpointStateRoutes(cp, nil, routes); err != nil {
			return err
		}
		ids := sortedAddressIDs(cp.InterruptID2Addr)
		for _, id := range ids {
			owner := owners[id]
			if owner.count == 0 {
				return fmt.Errorf("interrupt ID %q has no checkpoint state owner", id)
			}
			if owner.count > 1 {
				return fmt.Errorf("interrupt ID %q has multiple checkpoint state owners", id)
			}
			expectedPath, err := deepestCheckpointRoutePath(id, routes[id])
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(owner.path, expectedPath) {
				return fmt.Errorf("interrupt ID %q state owner path %v does not match routing owner path %v",
					id, owner.path, expectedPath)
			}
		}
		for id := range owners {
			if _, ok := cp.InterruptID2Addr[id]; !ok {
				return fmt.Errorf("checkpoint state owner %q has no routing address", id)
			}
		}
		for id := range routes {
			if _, ok := cp.InterruptID2Addr[id]; !ok {
				return fmt.Errorf("nested routing entry %q is missing from the root routing index", id)
			}
		}
	}
	return nil
}

type checkpointStateOwner struct {
	count int
	path  []string
}

type checkpointStateRoute struct {
	address Address
	path    []string
}

func validateCheckpointTreeMetadata(cp *checkpoint) error {
	if err := validateCheckpointLayoutMetadata(cp); err != nil {
		return err
	}
	for key, sub := range cp.SubGraphs {
		if sub == nil {
			return fmt.Errorf("subgraph checkpoint %q is nil", key)
		}
		if err := validateCheckpointTreeMetadata(sub); err != nil {
			return fmt.Errorf("subgraph checkpoint %q has invalid metadata: %w", key, err)
		}
	}
	return nil
}

func collectCheckpointStateOwners(cp *checkpoint, path []string,
	owners map[string]checkpointStateOwner) error {
	for id := range cp.InterruptID2State {
		if !isCheckpointMetadataID(id) {
			owner := owners[id]
			owner.count++
			if owner.count == 1 {
				owner.path = append([]string(nil), path...)
			}
			owners[id] = owner
		}
	}
	keys := make([]string, 0, len(cp.SubGraphs))
	for key := range cp.SubGraphs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		sub := cp.SubGraphs[key]
		if sub == nil {
			return fmt.Errorf("subgraph checkpoint %q is nil", key)
		}
		childPath := append(append([]string(nil), path...), key)
		if err := collectCheckpointStateOwners(sub, childPath, owners); err != nil {
			return err
		}
	}
	return nil
}

func sortedAddressIDs(id2Addr map[string]Address) []string {
	ids := make([]string, 0, len(id2Addr))
	for id := range id2Addr {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func collectCheckpointStateRoutes(cp *checkpoint, path []string,
	routes map[string][]checkpointStateRoute) error {
	for id, address := range cp.InterruptID2Addr {
		existing := routes[id]
		if len(existing) > 0 && !existing[0].address.Equals(address) {
			return fmt.Errorf("interrupt ID %q has conflicting routing addresses %q and %q",
				id, existing[0].address.String(), address.String())
		}
		routes[id] = append(existing, checkpointStateRoute{
			address: address,
			path:    append([]string(nil), path...),
		})
	}
	keys := make([]string, 0, len(cp.SubGraphs))
	for key := range cp.SubGraphs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		sub := cp.SubGraphs[key]
		if sub == nil {
			return fmt.Errorf("subgraph checkpoint %q is nil", key)
		}
		childPath := append(append([]string(nil), path...), key)
		if err := collectCheckpointStateRoutes(sub, childPath, routes); err != nil {
			return err
		}
	}
	return nil
}

func deepestCheckpointRoutePath(id string, routes []checkpointStateRoute) ([]string, error) {
	if len(routes) == 0 {
		return nil, fmt.Errorf("interrupt ID %q has no routing entry", id)
	}
	deepest := routes[0].path
	for _, route := range routes[1:] {
		if len(route.path) > len(deepest) {
			deepest = route.path
		}
	}
	for _, route := range routes {
		if !checkpointPathIsPrefix(route.path, deepest) {
			return nil, fmt.Errorf("interrupt ID %q appears in unrelated checkpoint routing paths %v and %v",
				id, deepest, route.path)
		}
	}
	return deepest, nil
}

func checkpointPathIsPrefix(prefix, path []string) bool {
	if len(prefix) > len(path) {
		return false
	}
	for i := range prefix {
		if prefix[i] != path[i] {
			return false
		}
	}
	return true
}

func removeSubgraphOwnedInterruptStates(cp *checkpoint) {
	for _, sub := range cp.SubGraphs {
		removeCheckpointStateIDs(cp.InterruptID2State, sub)
	}
}

func removeCheckpointStateIDs(parent map[string]core.InterruptState, child *checkpoint) {
	for id := range child.InterruptID2State {
		if !isCheckpointMetadataID(id) {
			delete(parent, id)
		}
	}
	for _, nested := range child.SubGraphs {
		removeCheckpointStateIDs(parent, nested)
	}
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
