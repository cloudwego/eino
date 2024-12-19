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
	"runtime/debug"
	"sync"

	"github.com/cloudwego/eino/schema"
	"github.com/cloudwego/eino/utils/safe"
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

type channel interface {
	update(context.Context, map[string]any) error
	get(context.Context) (any, error)
	ready(context.Context) bool
	clear(context.Context)
}

type chanBuilder func(d []string) channel

type runner struct {
	chanSubscribeTo map[string]*chanCall
	invertedEdges   map[string][]string
	inputChannels   *chanCall

	chanBuilder chanBuilder // could be nil

	runCtx func(ctx context.Context) context.Context

	options graphCompileOptions

	inputType  reflect.Type
	outputType reflect.Type

	inputStreamFilter     streamMapFilter
	inputValueChecker     valueChecker
	inputStreamConverter  streamConverter
	outputValueChecker    valueChecker
	outputStreamConverter streamConverter

	runtimeCheckEdges    map[string]map[string]bool
	runtimeCheckBranches map[string][]bool
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

		inputType:            r.inputType,
		outputType:           r.outputType,
		inputStreamFilter:    r.inputStreamFilter,
		inputValueChecker:    r.inputValueChecker,
		inputStreamConverter: r.inputStreamConverter,
		optionType:           nil, // if option type is nil, graph will transmit all options.

		isPassthrough: false,
	}

	cr.i = genericInvokeWithCallbacks(cr.i)
	cr.t = genericTransformWithCallbacks(cr.t)

	return cr
}

func (r *runner) buildChannels() map[string]channel {
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

	return chs
}

type runnableCallWrapper func(context.Context, *composableRunnable, any, ...any) (any, error)

func runnableInvoke(ctx context.Context, r *composableRunnable, input any, opts ...any) (any, error) {
	return r.i(ctx, input, opts...)
}

func runnableTransform(ctx context.Context, r *composableRunnable, input any, opts ...any) (any, error) {
	return r.t(ctx, input.(streamReader), opts...)
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

func (r *runner) run(ctx context.Context, isStream bool, input any, opts ...Option) (any, error) {
	var err error
	var runWrapper runnableCallWrapper
	runWrapper = runnableInvoke
	if isStream {
		runWrapper = runnableTransform
	}

	chs := r.buildChannels()

	maxSteps := r.options.maxRunSteps
	for i := range opts {
		if opts[i].maxRunSteps > 0 {
			maxSteps = opts[i].maxRunSteps
		}
	}

	if maxSteps < 1 {
		return nil, errors.New("recursion_limit must be at least 1")
	}

	if r.runCtx != nil {
		ctx = r.runCtx(ctx)
	}

	optMap, err := extractOption(r.chanSubscribeTo, opts...)
	if err != nil {
		return nil, fmt.Errorf("graph extract option fail: %w", err)
	}

	type task struct {
		nodeKey string
		call    *chanCall
		input   any
		output  any
		option  []any
		err     error
	}

	taskPreProcessor := func(ctx context.Context, t *task) error {
		if t.call.preProcessor == nil {
			return nil
		}
		var e error
		t.input, e = runWrapper(ctx, t.call.preProcessor, t.input, t.option...)
		return e
	}

	taskPostProcessor := func(ctx context.Context, t *task) error {
		if t.call.postProcessor == nil {
			return nil
		}
		var e error
		t.output, e = runWrapper(ctx, t.call.postProcessor, t.output, t.option...)
		if e != nil {
			t.err = e
			t.output = nil
			return e
		}
		return nil
	}

	run := func(ctx context.Context, t *task) {
		defer func() {
			panicInfo := recover()
			if panicInfo != nil {
				t.output = nil
				t.err = safe.NewPanicErr(panicInfo, debug.Stack())
			}
		}()

		// callback
		ctx = initNodeCallbacks(ctx, t.nodeKey, t.call.action.nodeInfo, t.call.action.meta, opts...)

		out, e := runWrapper(ctx, t.call.action, t.input, t.option...)
		if e != nil {
			t.output = out
			t.err = e
			return
		}

		t.output = out
		t.err = nil
	}

	nextTasks := make([]*task, 0)
	// init start task
	nextTasks = append(nextTasks, &task{
		nodeKey: START,
		call:    r.inputChannels,
		output:  input,
	})

	for step := 0; ; step++ {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("context has been canceled, error: %w", ctx.Err())
		default:
		}

		if step == maxSteps {
			return nil, ErrExceedMaxSteps
		}

		// calculate next tasks
		wChValues := make(map[string]map[string]any)
		for _, t := range nextTasks {
			// update channel & new_next_tasks
			vs_ := copyItem(t.output, len(t.call.writeTo)+len(t.call.writeToBranches)*2)
			nexts, err_ := r.calculateNext(ctx, t.nodeKey, t.call, runWrapper,
				vs_[len(t.call.writeTo)+len(t.call.writeToBranches):], isStream)
			if err_ != nil {
				return nil, fmt.Errorf("calculate next step fail, node: %s, error: %w", t.nodeKey, err_)
			}
			for i, next := range nexts {
				if _, ok := wChValues[next]; !ok {
					wChValues[next] = make(map[string]any)
				}
				// check type if needed
				vs_[i], err = r.parserOrValidateTypeIfNeeded(t.nodeKey, next, isStream, vs_[i])
				if err != nil {
					return nil, err
				}

				wChValues[next][t.nodeKey] = vs_[i]
			}
		}

		// return directly when arrive end.
		if values, ok := wChValues[END]; ok {
			err = chs[END].update(ctx, values)
			if err != nil {
				return nil, err
			}
			break
		}

		var newNextTasks []*task
		for wCh, values := range wChValues {
			ch, ok := chs[wCh]
			if !ok {
				return nil, fmt.Errorf("write_to_channel (node): %s not present in the graph", wCh)
			}

			err = ch.update(ctx, values)
			if err != nil {
				return nil, err
			}

			if ch.ready(ctx) {
				in, e := ch.get(ctx)
				if e != nil {
					return nil, fmt.Errorf("get node[%s] input from channel fail: %w", wCh, e)
				}
				var call *chanCall
				call, ok = r.chanSubscribeTo[wCh]
				if !ok {
					return nil, fmt.Errorf("node[%s] has not been registered", wCh)
				}
				newNextTasks = append(newNextTasks, &task{nodeKey: wCh, call: call, input: in, option: optMap[wCh]})
			}
		}
		nextTasks = newNextTasks

		if len(nextTasks) == 0 {
			return nil, errors.New("no tasks to execute")
		}

		for i := 0; i < len(nextTasks); i++ {
			e := taskPreProcessor(ctx, nextTasks[i])
			if e != nil {
				return nil, fmt.Errorf("pre-process[%s] input error: %w", nextTasks[i].nodeKey, e)
			}
		}

		if len(nextTasks) == 1 {
			run(ctx, nextTasks[0])
		} else {
			var wg sync.WaitGroup
			for i := 1; i < len(nextTasks); i++ {
				wg.Add(1)
				go func(t *task) {
					defer wg.Done()
					defer func() {
						panicErr := recover()
						if panicErr != nil {
							t.err = safe.NewPanicErr(panicErr, debug.Stack()) // nolint: byted_returned_err_should_do_check
						}
					}()
					run(ctx, t)
				}(nextTasks[i])
			}
			run(ctx, nextTasks[0])
			wg.Wait()
		}

		for i := 0; i < len(nextTasks); i++ {
			t := nextTasks[i]
			if t.err != nil {
				return nil, fmt.Errorf("node[%s] execute fail: \n%w", t.nodeKey, t.err)
			}

			e := taskPostProcessor(ctx, t)
			if e != nil {
				return nil, fmt.Errorf("post-process[%s] input error: %w", t.nodeKey, e)
			}
		}
	}

	if !chs[END].ready(ctx) {
		return nil, fmt.Errorf("arrives at END node but its value is not ready")
	}
	out, err := chs[END].get(ctx)
	if err != nil {
		return nil, err
	}

	return out, nil
}

func (r *runner) calculateNext(ctx context.Context, curNodeKey string, startChan *chanCall, runWrapper runnableCallWrapper, input []any, isStream bool) ([]string, error) { // nolint: byted_s_args_length_limit
	if len(input) < len(startChan.writeToBranches) {
		// unreachable
		return nil, errors.New("calculate next input length is shorter than branches")
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

func (r *runner) parserOrValidateTypeIfNeeded(cur, next string, isStream bool, value any) (any, error) {
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
