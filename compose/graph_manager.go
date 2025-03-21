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
	"fmt"
	"runtime/debug"
	"sync"

	"github.com/cloudwego/eino/internal/safe"
)

type channel interface {
	update(context.Context, map[string]any) error
	get(context.Context) (any, error)
	ready(context.Context) bool
	reportSkip([]string) (bool, error)
	reportDone(string) error
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
		var newValue streamReader
		if value == nil {
			newValue = (streamReader)(nil)
		} else {
			newValue = value.(streamReader)
		}
		for _, v := range p.h[nodeKey] {
			value = v.transform(newValue)
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
	isStream   bool
	successors map[string][]string
	channels   map[string]channel

	edgeHandlerManager    *edgeHandlerManager
	preNodeHandlerManager *preNodeHandlerManager
}

func (c *channelManager) updateValues(ctx context.Context, values map[string] /*to*/ map[string] /*from*/ any, isStream bool) error {
	for target, fromMap := range values {
		toChannel, ok := c.channels[target]
		if !ok {
			return fmt.Errorf("target channel doesn't existed: %s", target)
		}
		nFromMap := make(map[string]any, len(fromMap))
		for from, value := range fromMap {
			var err error
			nFromMap[from], err = c.edgeHandlerManager.handle(from, target, value, isStream)
			if err != nil {
				return err
			}
		}
		err := toChannel.update(ctx, nFromMap)
		if err != nil {
			return fmt.Errorf("update target channel[%s] fail: %w", target, err)
		}
	}
	return nil
}

func (c *channelManager) getFromReadyChannels(ctx context.Context, isStream bool) (map[string]any, error) {
	result := make(map[string]any)
	for target, ch := range c.channels {
		if ch.ready(ctx) {
			v, err := ch.get(ctx)
			if err != nil {
				return nil, fmt.Errorf("get value from ready channel[%s] fail: %w", target, err)
			}
			v, err = c.preNodeHandlerManager.handle(target, v, isStream)
			if err != nil {
				return nil, err
			}
			result[target] = v
		}
	}
	return result, nil
}

func (c *channelManager) reportBranch(from string, skippedNodes []string) error {
	var nKeys []string
	for _, node := range skippedNodes {
		skipped, err := c.channels[node].reportSkip([]string{from})
		if err != nil {
			return err
		}
		if skipped {
			nKeys = append(nKeys, node)
		}
	}

	for i := 0; i < len(nKeys); i++ {
		key := nKeys[i]
		if _, ok := c.successors[key]; !ok {
			return fmt.Errorf("unknown node: %s", key)
		}
		for _, successor := range c.successors[key] {
			skipped, err := c.channels[successor].reportSkip([]string{key})
			if err != nil {
				return err
			}
			if skipped {
				nKeys = append(nKeys, successor)
			}
			// todo: detect if end node has been skipped?
		}
	}
	return nil
}

func (c *channelManager) reportControl(from string, to []string) error {
	for _, node := range to {
		if err := c.channels[node].reportDone(from); err != nil {
			return err
		}
	}

	return nil
}

type task struct {
	ctx     context.Context
	nodeKey string
	call    *chanCall
	input   any
	output  any
	option  []any
	err     error
}

type taskManager struct {
	runWrapper runnableCallWrapper
	opts       []Option
	needAll    bool

	mu   sync.Mutex
	l    *list.List
	done chan *task
	num  uint32
}

func (t *taskManager) executor(currentTask *task) {
	defer func() {
		panicInfo := recover()
		if panicInfo != nil {
			currentTask.output = nil
			currentTask.err = safe.NewPanicErr(panicInfo, debug.Stack())
		}
		t.mu.Lock()
		t.l.PushBack(currentTask)
		t.updateChan()
		t.mu.Unlock()
	}()

	ctx := initNodeCallbacks(currentTask.ctx, currentTask.nodeKey, currentTask.call.action.nodeInfo, currentTask.call.action.meta, t.opts...)
	currentTask.output, currentTask.err = t.runWrapper(ctx, currentTask.call.action, currentTask.input, currentTask.option...)
}

func (t *taskManager) submit(tasks []*task) error {
	// synchronously execute one task, if there are no other tasks in the task pool and meet one of the following conditions：
	// 1. the new task is the only one
	// 2. the task manager mode is set to needAll
	for _, currentTask := range tasks {
		if currentTask.call.preProcessor != nil {
			nInput, err := t.runWrapper(currentTask.ctx, currentTask.call.preProcessor, currentTask.input, currentTask.option...)
			if err != nil {
				return fmt.Errorf("run node[%s] pre processor fail: %w", currentTask.nodeKey, err)
			}
			currentTask.input = nInput
		}
	}
	var syncTask *task
	if t.num == 0 && (len(tasks) == 1 || t.needAll) {
		syncTask = tasks[0]
		tasks = tasks[1:]
	}
	for _, currentTask := range tasks {
		t.num += 1
		go t.executor(currentTask)
	}
	if syncTask != nil {
		t.num += 1
		t.executor(syncTask)
	}
	return nil
}

func (t *taskManager) wait() ([]*task, error) {
	if t.needAll {
		return t.waitAll()
	}
	ta, success, err := t.waitOne()
	if err != nil {
		return nil, err
	}
	if !success {
		return []*task{}, nil
	}
	return []*task{ta}, nil
}

func (t *taskManager) waitOne() (*task, bool, error) {
	if t.num == 0 {
		return nil, false, nil
	}
	t.num--
	ta := <-t.done
	t.mu.Lock()
	t.updateChan()
	t.mu.Unlock()

	if ta.err != nil {
		return nil, false, fmt.Errorf("execute node[%s] fail: %w", ta.nodeKey, ta.err)
	}
	if ta.call.postProcessor != nil {
		nOutput, err := t.runWrapper(ta.ctx, ta.call.postProcessor, ta.output, ta.option...)
		if err != nil {
			return nil, false, fmt.Errorf("run node[%s] post processor fail: %w", ta.nodeKey, err)
		}
		ta.output = nOutput
	}
	return ta, true, nil
}

func (t *taskManager) waitAll() ([]*task, error) {
	result := make([]*task, 0, t.num)
	for {
		ta, success, err := t.waitOne()
		if err != nil {
			return nil, err
		}
		if !success {
			return result, nil
		}
		result = append(result, ta)
	}
}

func (t *taskManager) updateChan() {
	for t.l.Len() > 0 {
		select {
		case t.done <- t.l.Front().Value.(*task):
			t.l.Remove(t.l.Front())
		default:
			return
		}
	}
}
