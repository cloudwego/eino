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

	"github.com/cloudwego/eino/schema"
)

type graphCompileOptions struct {
	maxRunSteps     int
	graphName       string
	nodeTriggerMode NodeTriggerMode // default to AnyPredecessor (pregel)

	callbacks []GraphCompileCallback

	origOpts []GraphCompileOption
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
	writeTo         []string
	writeToBranches []*GraphBranch

	preProcessor, postProcessor *composableRunnable
}

type chanBuilder func(d []string) channel

type runner struct {
	chanSubscribeTo map[string]*chanCall
	invertedEdges   map[string][]string
	inputChannels   *chanCall

	chanBuilder chanBuilder // could be nil
	eager       bool

	runCtx func(ctx context.Context) context.Context

	options graphCompileOptions

	inputType  reflect.Type
	outputType reflect.Type

	// take effect as a sub-graph through toComposableRunnable
	inputStreamFilter                streamMapFilter
	inputValueChecker                valueChecker
	inputStreamConverter             streamConverter
	inputFieldMappingConverter       fieldMappingConverter
	inputStreamFieldMappingConverter streamFieldMappingConverter
	// used for convert runner's output
	outputValueChecker                valueChecker
	outputStreamConverter             streamConverter
	outputFieldMappingConverter       fieldMappingConverter
	outputStreamFieldMappingConverter streamFieldMappingConverter

	// checks need to do because cannot check at compile
	runtimeCheckEdges    map[string]map[string]bool
	runtimeCheckBranches map[string][]bool

	// field mapping records, used to:
	// 1. convert predecessor's output to map and filter key
	// 2. convert input from map to struct that successor expect
	fieldMappingsOnEdge *fieldMappingsOnEdge
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
	var runWrapper runnableCallWrapper
	runWrapper = runnableInvoke
	if isStream {
		runWrapper = runnableTransform
	}

	cm := r.initChannelManager()
	tm := r.initTaskManager(runWrapper, opts...)
	maxSteps := r.options.maxRunSteps
	if r.runCtx != nil {
		ctx = r.runCtx(ctx)
	}

	for i := range opts {
		if opts[i].maxRunSteps > 0 {
			maxSteps = opts[i].maxRunSteps
		}
	}
	if maxSteps < 1 {
		return nil, errors.New("recursion_limit must be at least 1")
	}

	optMap, extractErr := extractOption(r.chanSubscribeTo, opts...)
	if extractErr != nil {
		return nil, fmt.Errorf("graph extract option fail: %w", extractErr)
	}

	var completedTasks []*task
	// init start task
	completedTasks = append(completedTasks, &task{
		nodeKey: START,
		call:    r.inputChannels,
		output:  input,
	})

	for step := 0; ; step++ {
		// check if runner should stop
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("context has been canceled, error: %w", ctx.Err())
		default:
		}
		if step == maxSteps {
			return nil, ErrExceedMaxSteps
		}

		// 1. calculate active edge
		// 2. handle field mapping if needed
		// 3. parse data type if needed
		wChValues, err := r.resolveEdges(ctx, completedTasks, isStream)
		if err != nil {
			return nil, err
		}

		nodeMap, err := cm.updateAndGet(ctx, wChValues)
		if err != nil {
			return nil, fmt.Errorf("update and get channel fail: %w", err)
		}
		if len(nodeMap) > 0 {
			// conv field mapping to expected struct if needed
			nodeMap, err = r.convFieldMappingIfNeeded(nodeMap, isStream)
			if err != nil {
				return nil, fmt.Errorf("conv field mapping to struct fail: %w", err)
			}

			if v, ok := nodeMap[END]; ok {
				// reach end
				return v, nil
			}

			// build task from node map
			nextTasks, convErr := r.convTasks(ctx, nodeMap, optMap)
			if convErr != nil {
				return nil, convErr
			}
			err = tm.submit(nextTasks)
			if err != nil {
				return nil, fmt.Errorf("submit tasks fail: %w", err)
			}
		}

		completedTasks, err = tm.wait()
		if err != nil {
			return nil, fmt.Errorf("wait tasks fail: %w", err)
		}
		if len(completedTasks) == 0 {
			return nil, errors.New("no tasks to execute")
		}
	}
}

func (r *runner) convFieldMappingIfNeeded(nodeMap map[string]any, isStream bool) (map[string]any, error) {
	for nodeKey, nodeInput := range nodeMap {
		var conv func(any) (any, error)
		var streamConv func(streamReader) (streamReader, error)

		if nodeKey == END {
			conv = r.outputFieldMappingConverter
			streamConv = r.outputStreamFieldMappingConverter
		} else {
			call, ok := r.chanSubscribeTo[nodeKey]
			if !ok {
				return nil, fmt.Errorf("node[%s] has not been registered", nodeKey)
			}
			conv = call.action.inputFieldMappingConverter
			streamConv = call.action.inputStreamFieldMappingConverter
		}

		if r.fieldMappingsOnEdge.needConvMap2Struct(nodeKey) {
			if isStream {
				nNodeInput, err := streamConv(nodeInput.(streamReader))
				if err != nil {
					return nil, fmt.Errorf("conv node[%s]'s input from FieldMapping fail: %w", nodeKey, err)
				}
				nodeMap[nodeKey] = nNodeInput
			} else {
				nNodeInput, err := conv(nodeInput)
				if err != nil {
					return nil, fmt.Errorf("conv node[%s]'s input from FieldMapping fail: %w", nodeKey, err)
				}
				nodeMap[nodeKey] = nNodeInput
			}
		}
	}
	return nodeMap, nil
}

func (r *runner) convTasks(ctx context.Context, nodeMap map[string]any, optMap map[string][]any) ([]*task, error) {
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

func (r *runner) resolveEdges(ctx context.Context, completedTasks []*task, isStream bool) (map[string]map[string]any, error) {
	wChValues := make(map[string]map[string]any)
	for _, t := range completedTasks {
		// update channel & new_next_tasks
		vs := copyItem(t.output, len(t.call.writeTo)+len(t.call.writeToBranches)*2)
		nextNodeKeys, err := r.calculateNext(ctx, t.nodeKey, t.call,
			vs[len(t.call.writeTo)+len(t.call.writeToBranches):], isStream)
		if err != nil {
			return nil, fmt.Errorf("calculate next step fail, node: %s, error: %w", t.nodeKey, err)
		}
		for i, next := range nextNodeKeys {
			if _, ok := wChValues[next]; !ok {
				wChValues[next] = make(map[string]any)
			}
			// field mapping if needed
			useFieldMapping := false
			vs[i], useFieldMapping, err = r.fieldMappingsOnEdge.execute(t.nodeKey, next, vs[i], isStream)
			if err != nil {
				return nil, fmt.Errorf("field mapping fail, edge: [%s]-[%s], error: %w", t.nodeKey, next, err)
			}
			if !useFieldMapping {
				// check type if needed
				vs[i], err = r.validateTypeAndParseIfNeeded(t.nodeKey, next, isStream, vs[i])
				if err != nil {
					return nil, fmt.Errorf("validate type of edge[%s]-[%s] fail: %w", t.nodeKey, next, err)
				}
			}

			wChValues[next][t.nodeKey] = vs[i]
		}
	}
	return wChValues, nil
}

func (r *runner) calculateNext(ctx context.Context, curNodeKey string, startChan *chanCall, input []any, isStream bool) ([]string, error) { // nolint: byted_s_args_length_limit
	if len(input) < len(startChan.writeToBranches) {
		// unreachable
		return nil, errors.New("calculate next input length is shorter than branches")
	}
	runWrapper := runnableInvoke
	if isStream {
		runWrapper = runnableTransform
	}

	ret := make([]string, 0, len(startChan.writeTo))
	ret = append(ret, startChan.writeTo...)

	for i, branch := range startChan.writeToBranches {
		// check branch input type if needed
		if r.runtimeCheckBranches[curNodeKey][branch.idx] {
			if isStream {
				input[i] = branch.condition.inputStreamConverter(input[i].(streamReader))
			} else {
				err := branch.condition.inputValueChecker(input[i])
				if err != nil {
					return nil, fmt.Errorf("branch[%s]-[%d] runtime value check fail: %w", curNodeKey, branch.idx, err)
				}
			}
		}

		wCh, e := runWrapper(ctx, branch.condition, input[i])
		if e != nil { // nolint:byted_s_too_many_nests_in_func
			return nil, fmt.Errorf("branch run error: %w", e)
		}

		// process branch output
		var w string
		var ok bool
		if isStream { // nolint:byted_s_too_many_nests_in_func
			var sr streamReader
			var csr *schema.StreamReader[string]
			sr, ok = wCh.(streamReader)
			if !ok {
				return nil, errors.New("stream branch return isn't IStreamReader")
			}
			csr, ok = unpackStreamReader[string](sr)
			if !ok {
				return nil, errors.New("unpack branch result fail")
			}

			var se error
			w, se = concatStreamReader(csr)
			if se != nil {
				return nil, fmt.Errorf("concat branch result error: %w", se)
			}
		} else { // nolint:byted_s_too_many_nests_in_func
			w, ok = wCh.(string)
			if !ok {
				return nil, errors.New("invoke branch result isn't string")
			}
		}
		ret = append(ret, w)
	}
	return ret, nil
}

func (r *runner) validateTypeAndParseIfNeeded(cur, next string, isStream bool, value any) (any, error) {
	if _, ok := r.runtimeCheckEdges[cur]; !ok {
		return value, nil
	}
	if _, ok := r.runtimeCheckEdges[cur][next]; !ok {
		return value, nil
	}

	if next == END {
		if isStream {
			value = r.outputStreamConverter(value.(streamReader))
			return value, nil
		}
		err := r.outputValueChecker(value)
		if err != nil {
			return nil, fmt.Errorf("edge[%s]-[%s] runtime value check fail: %w", cur, next, err)
		}
		return value, nil

	}
	if isStream {
		value = r.chanSubscribeTo[next].action.inputStreamConverter(value.(streamReader))
		return value, nil
	}
	err := r.chanSubscribeTo[next].action.inputValueChecker(value)
	if err != nil {
		return nil, fmt.Errorf("edge[%s]-[%s] runtime value check fail: %w", cur, next, err)
	}
	return value, nil
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

func (r *runner) initChannelManager() *channelManager {
	builder := r.chanBuilder
	if builder == nil {
		builder = func(d []string) channel {
			return &pregelChannel{}
		}
	}

	chs := make(map[string]channel)
	for ch := range r.chanSubscribeTo {
		chs[ch] = builder(r.invertedEdges[ch])
	}

	chs[END] = builder(r.invertedEdges[END])

	return (*channelManager)(&chs)
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

		inputType:                        r.inputType,
		outputType:                       r.outputType,
		inputStreamFilter:                r.inputStreamFilter,
		inputValueChecker:                r.inputValueChecker,
		inputStreamConverter:             r.inputStreamConverter,
		inputFieldMappingConverter:       r.inputFieldMappingConverter,
		inputStreamFieldMappingConverter: r.inputStreamFieldMappingConverter,
		optionType:                       nil, // if option type is nil, graph will transmit all options.

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
