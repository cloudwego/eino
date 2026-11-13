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

	"github.com/cloudwego/eino/internal/core"
	"github.com/cloudwego/eino/internal/serialization"
)

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

// interruptTasks separates consumed inputs from reruns, whose original input is
// retained only when the existing external-interrupt policy requests it.
type interruptTasks struct {
	pending   []*task
	rerun     []*task
	subgraphs []*task
}

// collect partitions already classified results. It never advances dependencies.
func (it *interruptTasks) collect(info *interruptTempInfo, tasks []*task) (completed []*task) {
	for _, t := range tasks {
		if _, ok := info.subGraphInterrupts[t.nodeKey]; ok {
			it.subgraphs = append(it.subgraphs, t)
			continue
		}
		rerun := false
		for _, key := range info.interruptRerunNodes {
			if key == t.nodeKey {
				it.rerun = append(it.rerun, t)
				rerun = true
				break
			}
		}
		if _, interrupted := info.interruptTaskNodes[t.nodeKey]; !rerun && !interrupted {
			completed = append(completed, t)
		}
	}
	return completed
}

func (ti *interruptTempInfo) hasWaits() bool {
	return len(ti.subGraphInterrupts)+len(ti.interruptRerunNodes) > 0
}

func graphInterruptRequested(signal *graphCancelSignal) bool {
	if signal == nil {
		return false
	}
	select {
	case <-signal.done:
		return true
	default:
		return false
	}
}

func (r *runner) collectLocalResults(ctx context.Context, info *interruptTempInfo, waiting *interruptTasks,
	completed, canceled []*task, cm *channelManager) error {
	if err := r.resolveInterruptCompletedTasks(info, completed); err != nil {
		return err
	}
	successful := waiting.collect(info, completed)
	for _, t := range canceled {
		info.interruptRerunNodes = appendIfNotExist(info.interruptRerunNodes, t.nodeKey)
		waiting.rerun = append(waiting.rerun, t)
	}
	if err := r.forwardCompletedTasks(ctx, successful, false, cm); err != nil {
		return newGraphRunError(fmt.Errorf("failed to forward completed tasks: %w", err))
	}
	return nil
}

// interruptSaveConfig carries the graph-level checkpoint publication settings.
type interruptSaveConfig struct {
	channels             map[string]channel
	isStream, isSubGraph bool
	checkPointID         *string
	publish              func(*subGraphInterruptError)
}

// saveInterrupt only builds and publishes a snapshot of already classified work.
// It must not read ready channels, create tasks, or forward completed results.
func (r *runner) saveInterrupt(
	ctx context.Context, tempInfo *interruptTempInfo, tasks *interruptTasks,
	config *interruptSaveConfig,
) error {
	cp := &checkpoint{
		Channels:       config.channels,
		Inputs:         make(map[string]any),
		SkipPreHandler: make(map[string]bool),
		SubGraphs:      make(map[string]*checkpoint),
	}
	if r.runCtx != nil {
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
	info := &InterruptInfo{
		State:           cp.State,
		AfterNodes:      tempInfo.interruptAfterNodes,
		BeforeNodes:     tempInfo.interruptBeforeNodes,
		RerunNodes:      tempInfo.interruptRerunNodes,
		RerunNodesExtra: tempInfo.interruptRerunExtra,
		SubGraphs:       make(map[string]*InterruptInfo),
	}
	for _, t := range tasks.pending {
		cp.Inputs[t.nodeKey] = t.input
		cp.SkipPreHandler[t.nodeKey] = t.skipPreHandler
	}
	for _, t := range tasks.rerun {
		if t.originalInput != nil {
			cp.Inputs[t.nodeKey] = t.originalInput
		}
	}
	for _, t := range tasks.subgraphs {
		cp.RerunNodes = appendIfNotExist(cp.RerunNodes, t.nodeKey)
		cp.SubGraphs[t.nodeKey] = tempInfo.subGraphInterrupts[t.nodeKey].CheckPoint
		cp.SkipPreHandler[t.nodeKey] = true
		info.SubGraphs[t.nodeKey] = tempInfo.subGraphInterrupts[t.nodeKey].Info
	}
	for _, key := range tempInfo.interruptRerunNodes {
		cp.RerunNodes = appendIfNotExist(cp.RerunNodes, key)
	}
	if err := r.validateCheckpointIntegrity(cp); err != nil {
		return fmt.Errorf("invalid checkpoint: %w", err)
	}
	signal, err := core.Interrupt(ctx, cp.State, nil, tempInfo.signals)
	if err != nil {
		return fmt.Errorf("failed to interrupt: %w", err)
	}
	cp.InterruptID2Addr, cp.InterruptID2State = core.SignalToPersistenceMaps(signal)
	if err = r.checkPointer.convertCheckPoint(cp, config.isStream, signal); err != nil {
		return fmt.Errorf("failed to convert checkpoint: %w", err)
	}
	if config.isSubGraph {
		interrupted := &subGraphInterruptError{Info: info, CheckPoint: cp, signal: signal}
		if config.publish != nil {
			config.publish(interrupted)
		}
		return interrupted
	}
	if config.checkPointID != nil {
		if err = r.checkPointer.set(ctx, *config.checkPointID, cp); err != nil {
			return fmt.Errorf("failed to set checkpoint: %w, checkPointID: %s", err, *config.checkPointID)
		}
	}
	info.InterruptContexts = core.ToInterruptContexts(signal, nil)
	return &interruptError{Info: info}
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
