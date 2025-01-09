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

	"github.com/cloudwego/eino/utils/safe"
)

type channel interface {
	update(context.Context, map[string]any) error
	get(context.Context) (any, error)
	ready(context.Context) bool
}

type channelManager map[string]channel

func (c *channelManager) updateValues(ctx context.Context, values map[string]map[string]any) error {
	for target, v := range values {
		toChannel, ok := (*c)[target]
		if !ok {
			return fmt.Errorf("target channel isn't existed: %s", target)
		}
		err := toChannel.update(ctx, v)
		if err != nil {
			return fmt.Errorf("update target channel[%s] fail: %w", target, err)
		}
	}
	return nil
}

func (c *channelManager) getReady(ctx context.Context) (map[string]any, error) {
	result := make(map[string]any)
	for target, ch := range *c {
		if ch.ready(ctx) {
			v, err := ch.get(ctx)
			if err != nil {
				return nil, fmt.Errorf("get value from ready channel[%s] fail: %w", target, err)
			}
			result[target] = v
		}
	}
	return result, nil
}
func (c *channelManager) updateAndGet(ctx context.Context, values map[string]map[string]any) (map[string]any, error) {
	err := c.updateValues(ctx, values)
	if err != nil {
		return nil, fmt.Errorf("update channel fail: %w", err)
	}
	return c.getReady(ctx)
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

func (t *taskManager) submit(tasks []*task) error {
	for _, currentTask := range tasks {
		currentTask := currentTask
		if currentTask.call.preProcessor != nil {
			nInput, err := t.runWrapper(currentTask.ctx, currentTask.call.preProcessor, currentTask.input, currentTask.option...)
			if err != nil {
				return fmt.Errorf("run node[%s] pre processor fail: %w", currentTask.nodeKey, err)
			}
			currentTask.input = nInput
		}
		t.num += 1
		go func() { // TODO: control tasks & save thread
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
		}()
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
