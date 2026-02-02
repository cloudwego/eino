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
	"runtime/debug"
	"time"

	"github.com/cloudwego/eino/internal"
	"github.com/cloudwego/eino/internal/safe"
)

type channel interface {
	reportValues(map[string]any) error
	reportDependencies([]string)
	reportSkip([]string) bool
	get(bool, string, *edgeHandlerManager) (any, bool, error)
	convertValues(fn func(map[string]any) error) error
	load(channel) error

	setMergeConfig(FanInMergeConfig)
}

type edgeHandlerManager struct {
	h map[string]map[string][]handlerPair
}

func (e *edgeHandlerManager) handle(from, to string, value any, isStream bool) (any, error) {
	if _, ok := e.h[from]; !ok {
		return value, nil
	}
	if _, ok := e.h[from][to]; !ok {
		return value, nil
	}
	if isStream {
		for _, v := range e.h[from][to] {
			value = v.transform(value.(streamReader))
		}
	} else {
		for _, v := range e.h[from][to] {
			var err error
			value, err = v.invoke(value)
			if err != nil {
				return nil, err
			}
		}
	}
	return value, nil
}

type preNodeHandlerManager struct {
	h map[string][]handlerPair
}

func (p *preNodeHandlerManager) handle(nodeKey string, value any, isStream bool) (any, error) {
	if _, ok := p.h[nodeKey]; !ok {
		return value, nil
	}
	if isStream {
		for _, v := range p.h[nodeKey] {
			value = v.transform(value.(streamReader))
		}
	} else {
		for _, v := range p.h[nodeKey] {
			var err error
			value, err = v.invoke(value)
			if err != nil {
				return nil, err
			}
		}
	}
	return value, nil
}

type preBranchHandlerManager struct {
	h map[string][][]handlerPair
}

func (p *preBranchHandlerManager) handle(nodeKey string, idx int, value any, isStream bool) (any, error) {
	if _, ok := p.h[nodeKey]; !ok {
		return value, nil
	}
	if isStream {
		for _, v := range p.h[nodeKey][idx] {
			value = v.transform(value.(streamReader))
		}
	} else {
		for _, v := range p.h[nodeKey][idx] {
			var err error
			value, err = v.invoke(value)
			if err != nil {
				return nil, err
			}
		}
	}
	return value, nil
}

type channelManager struct {
	isStream bool
	channels map[string]channel

	successors          map[string][]string
	dataPredecessors    map[string]map[string]struct{}
	controlPredecessors map[string]map[string]struct{}

	edgeHandlerManager    *edgeHandlerManager
	preNodeHandlerManager *preNodeHandlerManager
}

func (c *channelManager) loadChannels(channels map[string]channel) error {
	for key, ch := range c.channels {
		if nCh, ok := channels[key]; ok {
			if err := ch.load(nCh); err != nil {
				return fmt.Errorf("load channel[%s] fail: %w", key, err)
			}
		}
	}
	return nil
}

func (c *channelManager) updateValues(_ context.Context, values map[string] /*to*/ map[string] /*from*/ any) error {
	for target, fromMap := range values {
		toChannel, ok := c.channels[target]
		if !ok {
			return fmt.Errorf("target channel doesn't existed: %s", target)
		}
		dps, ok := c.dataPredecessors[target]
		if !ok {
			dps = map[string]struct{}{}
		}
		nFromMap := make(map[string]any, len(fromMap))
		for from, value := range fromMap {
			if _, ok = dps[from]; ok {
				nFromMap[from] = fromMap[from]
			} else {
				if sr, okk := value.(streamReader); okk {
					sr.close()
				}
			}
		}

		err := toChannel.reportValues(nFromMap)
		if err != nil {
			return fmt.Errorf("update target channel[%s] fail: %w", target, err)
		}
	}
	return nil
}

func (c *channelManager) updateDependencies(_ context.Context, dependenciesMap map[string][]string) error {
	for target, dependencies := range dependenciesMap {
		toChannel, ok := c.channels[target]
		if !ok {
			return fmt.Errorf("target channel doesn't existed: %s", target)
		}
		cps, ok := c.controlPredecessors[target]
		if !ok {
			cps = map[string]struct{}{}
		}
		var deps []string
		for _, from := range dependencies {
			if _, ok = cps[from]; ok {
				deps = append(deps, from)
			}
		}

		toChannel.reportDependencies(deps)
	}
	return nil
}

func (c *channelManager) getFromReadyChannels(_ context.Context) (map[string]any, error) {
	result := make(map[string]any)
	for target, ch := range c.channels {
		v, ready, err := ch.get(c.isStream, target, c.edgeHandlerManager)
		if err != nil {
			return nil, fmt.Errorf("get value from ready channel[%s] fail: %w", target, err)
		}
		if ready {
			v, err = c.preNodeHandlerManager.handle(target, v, c.isStream)
			if err != nil {
				return nil, err
			}
			result[target] = v
		}
	}
	return result, nil
}

func (c *channelManager) updateAndGet(ctx context.Context, values map[string]map[string]any, dependencies map[string][]string) (map[string]any, error) {
	err := c.updateValues(ctx, values)
	if err != nil {
		return nil, fmt.Errorf("update channel fail: %w", err)
	}
	err = c.updateDependencies(ctx, dependencies)
	if err != nil {
		return nil, fmt.Errorf("update channel fail: %w", err)
	}
	return c.getFromReadyChannels(ctx)
}

func (c *channelManager) reportBranch(from string, skippedNodes []string) error {
	var nKeys []string
	for _, node := range skippedNodes {
		skipped := c.channels[node].reportSkip([]string{from})
		if skipped {
			nKeys = append(nKeys, node)
		}
	}

	for i := 0; i < len(nKeys); i++ {
		key := nKeys[i]

		if key == END {
			continue
		}
		if _, ok := c.successors[key]; !ok {
			return fmt.Errorf("unknown node: %s", key)
		}
		for _, successor := range c.successors[key] {
			skipped := c.channels[successor].reportSkip([]string{key})
			if skipped {
				nKeys = appendIfNotExist(nKeys, successor)
			}
			// todo: detect if end node has been skipped?
		}
	}
	return nil
}

func appendIfNotExist(s []string, elem string) []string {
	for _, i := range s {
		if i == elem {
			return s
		}
	}
	return append(s, elem)
}

type task struct {
	ctx            context.Context
	nodeKey        string
	call           *chanCall
	input          any
	originalInput  any
	output         any
	option         []any
	err            error
	skipPreHandler bool
}

type taskManager struct {
	runWrapper runnableCallWrapper
	opts       []Option
	needAll    bool

	num          uint32
	done         *internal.UnboundedChan[*task]
	runningTasks map[string]*task

	cancelCh chan *time.Duration
	canceled bool
	deadline *time.Time

	persistRerunInput bool
}

func (t *taskManager) execute(currentTask *task) {
	defer func() {
		panicInfo := recover()
		if panicInfo != nil {
			currentTask.output = nil
			currentTask.err = safe.NewPanicErr(panicInfo, debug.Stack())
		}

		t.done.Send(currentTask)
	}()

	ctx := initNodeCallbacks(currentTask.ctx, currentTask.nodeKey, currentTask.call.action.nodeInfo, currentTask.call.action.meta, t.opts...)
	currentTask.output, currentTask.err = t.runWrapper(ctx, currentTask.call.action, currentTask.input, currentTask.option...)
}

func (t *taskManager) submit(tasks []*task) error {
	if len(tasks) == 0 {
		return nil
	}

	// synchronously execute one task, if there are no other tasks in the task pool and meet one of the following conditions：
	// 1. the new task is the only one
	// 2. the task manager mode is set to needAll
	for i := 0; i < len(tasks); i++ {
		currentTask := tasks[i]

		if t.persistRerunInput {
			if sr, ok := currentTask.input.(streamReader); ok {
				copies := sr.copy(2)
				currentTask.originalInput, currentTask.input = copies[0], copies[1]
			} else {
				currentTask.originalInput = currentTask.input
			}
		}

		err := runPreHandler(currentTask, t.runWrapper)
		if err != nil {
			// pre-handler error, regarded as a failure of the task itself
			currentTask.err = err
			tasks = append(tasks[:i], tasks[i+1:]...)
			i--
			t.num++
			t.done.Send(currentTask)
		}

		t.runningTasks[currentTask.nodeKey] = currentTask
	}
	if len(tasks) == 0 {
		// all tasks' pre-handler failed
		return nil
	}

	var syncTask *task
	if t.num == 0 && (len(tasks) == 1 || t.needAll) && t.cancelCh == nil /*if graph can be interrupted by user, shouldn't sync run task*/ {
		syncTask = tasks[0]
		tasks = tasks[1:]
	}
	for _, currentTask := range tasks {
		t.num += 1
		go t.execute(currentTask)
	}
	if syncTask != nil {
		t.num += 1
		t.execute(syncTask)
	}
	return nil
}

func (t *taskManager) wait() (tasks []*task, canceled bool, canceledTasks []*task) {
	if t.needAll {
		tasks, canceledTasks = t.waitAll()
		return tasks, t.canceled, canceledTasks
	}

	ta, success, canceled := t.waitOne()
	if canceled {
		// has canceled and timeout, return canceled tasks
		for _, rta := range t.runningTasks {
			canceledTasks = append(canceledTasks, rta)
		}
		t.runningTasks = make(map[string]*task)
		t.num = 0
		return nil, true, canceledTasks
	}
	if t.canceled {
		// has canceled, but not timeout, wait all
		tasks, canceledTasks = t.waitAll()
		return append(tasks, ta), true, canceledTasks
	}
	if !success {
		return []*task{}, t.canceled, nil
	}

	return []*task{ta}, t.canceled, nil
}

func (t *taskManager) waitOne() (ta *task, success bool, canceled bool) {
	if t.num == 0 {
		return nil, false, false
	}

	if t.cancelCh == nil {
		ta, _ = t.done.Receive()
	} else {
		ta, _, canceled = t.receive(t.done.Receive)
	}

	t.num--

	if canceled {
		return nil, false, true
	}

	delete(t.runningTasks, ta.nodeKey)

	if ta.originalInput != nil && (ta.err == nil || !isInterruptError(ta.err)) {
		if sr, ok := ta.originalInput.(streamReader); ok {
			sr.close()
		}
		ta.originalInput = nil
	}

	if ta.err != nil {
		// biz error, jump post processor
		return ta, true, false
	}
	runPostHandler(ta, t.runWrapper)
	return ta, true, false
}

func (t *taskManager) waitAll() (successTasks []*task, canceledTasks []*task) {
	result := make([]*task, 0, t.num)
	for {
		ta, success, canceled := t.waitOne()
		if canceled {
			for _, rt := range t.runningTasks {
				canceledTasks = append(canceledTasks, rt)
			}
			t.runningTasks = make(map[string]*task)
			t.num = 0
			return result, canceledTasks
		}
		if !success {
			return result, nil
		}
		result = append(result, ta)
	}
}

func (t *taskManager) receive(recv func() (*task, bool)) (ta *task, closed bool, canceled bool) {
	if t.deadline != nil {
		// have canceled, receive in a certain time
		return receiveWithDeadline(recv, *t.deadline)
	}
	if t.canceled {
		// canceled without timeout
		ta, closed = recv()
		return ta, closed, false
	}
	if t.cancelCh != nil {
		// have not canceled, receive while listening
		ta, closed, canceled, t.canceled, t.deadline = receiveWithListening(recv, t.cancelCh)
		return ta, closed, canceled
	}
	// won't cancel
	ta, closed = recv()
	return ta, closed, false
}

func receiveWithDeadline(recv func() (*task, bool), deadline time.Time) (ta *task, closed bool, canceled bool) {
	now := time.Now()
	if deadline.Before(now) {
		return nil, false, true
	}

	timeout := deadline.Sub(now)

	resultCh := make(chan struct{}, 1)

	go func() {
		ta, closed = recv()
		resultCh <- struct{}{}
	}()

	timeoutCh := time.After(timeout)

	select {
	case <-resultCh:
		return ta, closed, false
	case <-timeoutCh:
		return nil, false, true
	}
}

func receiveWithListening(recv func() (*task, bool), cancel chan *time.Duration) (*task, bool, bool, bool, *time.Time) {
	type pair struct {
		ta     *task
		closed bool
	}
	resultCh := make(chan pair, 1)
	var timeoutCh <-chan time.Time

	var deadline *time.Time
	canceled := false
	go func() {
		ta, closed := recv()
		resultCh <- pair{ta, closed}
	}()

	select {
	case p := <-resultCh:
		return p.ta, p.closed, false, false, nil
	case timeout, ok := <-cancel:
		if !ok {
			// unreachable
			break
		}
		canceled = true
		if timeout == nil {
			// canceled without timeout
			break
		}
		timeoutCh = time.After(*timeout)
		dt := time.Now().Add(*timeout)
		deadline = &dt
	}

	if timeoutCh != nil {
		select {
		case p := <-resultCh:
			return p.ta, p.closed, false, canceled, deadline
		case <-timeoutCh:
			return nil, false, true, canceled, deadline
		}
	}
	p := <-resultCh
	return p.ta, p.closed, false, canceled, nil
}

func runPreHandler(ta *task, runWrapper runnableCallWrapper) (err error) {
	defer func() {
		if e := recover(); e != nil {
			err = safe.NewPanicErr(fmt.Errorf("panic in pre handler: %v", e), debug.Stack())
		}
	}()
	if ta.call.preProcessor != nil && !ta.skipPreHandler {
		nInput, err := runWrapper(ta.ctx, ta.call.preProcessor, ta.input, ta.option...)
		if err != nil {
			return fmt.Errorf("run node[%s] pre processor fail: %w", ta.nodeKey, err)
		}
		ta.input = nInput
	}
	return nil
}

func runPostHandler(ta *task, runWrapper runnableCallWrapper) {
	defer func() {
		if e := recover(); e != nil {
			ta.err = safe.NewPanicErr(fmt.Errorf("panic in post handler: %v", e), debug.Stack())
		}
	}()
	if ta.call.postProcessor != nil {
		nOutput, err := runWrapper(ta.ctx, ta.call.postProcessor, ta.output, ta.option...)
		if err != nil {
			ta.err = fmt.Errorf("run node[%s] post processor fail: %w", ta.nodeKey, err)
		}
		ta.output = nOutput
	}
}
