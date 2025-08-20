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
	"reflect"

	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/internal/generic"
)

// the info of most original executable object directly provided by the user
type executorMeta struct {

	// automatically identified based on the way of addNode
	component component

	// indicates whether the executable object user provided could execute the callback aspect itself.
	// if it could, the callback in the corresponding graph node won't be executed
	// for components, the value comes from callbacks.Checker
	isComponentCallbackEnabled bool

	// for components, the value comes from components.Typer
	// for lambda, the value comes from the user's explicit config
	// if componentImplType is empty, then the class name or func name in the instance will be inferred, but no guarantee.
	componentImplType string
}

type nodeInfo struct {

	// the name of graph node for display purposes, not unique.
	// passed from WithNodeName()
	name string

	inputKey  string
	outputKey string

	preProcessor, postProcessor *composableRunnable

	compileOption *graphCompileOptions // if the node is an AnyGraph, it will need compile options of its own
}

// graphNode the complete information of the node in graph
type graphNode struct {
	cr *composableRunnable

	g AnyGraph

	nodeInfo     *nodeInfo
	executorMeta *executorMeta

	instance any
	opts     []GraphAddNodeOpt
}

func (gn *graphNode) getGenericHelper() *genericHelper {
	var ret *genericHelper
	if gn.g != nil {
		ret = gn.g.getGenericHelper()
	} else if gn.cr != nil {
		ret = gn.cr.genericHelper
	} else {
		return nil
	}

	if gn.nodeInfo != nil {
		if len(gn.nodeInfo.inputKey) > 0 {
			ret = ret.forMapInput()
		}
		if len(gn.nodeInfo.outputKey) > 0 {
			ret = ret.forMapOutput()
		}
	}
	return ret
}

func (gn *graphNode) inputType() reflect.Type {
	if gn.nodeInfo != nil && len(gn.nodeInfo.inputKey) != 0 {
		return generic.TypeOf[map[string]any]()
	}
	// priority follow compile
	if gn.g != nil {
		return gn.g.inputType()
	} else if gn.cr != nil {
		return gn.cr.inputType
	}

	return nil
}

func (gn *graphNode) outputType() reflect.Type {
	if gn.nodeInfo != nil && len(gn.nodeInfo.outputKey) != 0 {
		return generic.TypeOf[map[string]any]()
	}
	// priority follow compile
	if gn.g != nil {
		return gn.g.outputType()
	} else if gn.cr != nil {
		return gn.cr.outputType
	}

	return nil
}

func (gn *graphNode) compileIfNeeded(ctx context.Context) (*composableRunnable, error) {
	var r *composableRunnable
	if gn.g != nil {
		cr, err := gn.g.compile(ctx, gn.nodeInfo.compileOption)
		if err != nil {
			return nil, err
		}

		r = cr
		gn.cr = cr
	} else if gn.cr != nil {
		r = gn.cr
	} else {
		return nil, errors.New("no graph or component provided")
	}

	r.meta = gn.executorMeta
	r.nodeInfo = gn.nodeInfo

	if gn.nodeInfo.outputKey != "" {
		r = outputKeyedComposableRunnable(gn.nodeInfo.outputKey, r)
	}

	if gn.nodeInfo.inputKey != "" {
		r = inputKeyedComposableRunnable(gn.nodeInfo.inputKey, r)
	}

	return r, nil
}

func parseExecutorInfoFromComponent(c component, executor any) *executorMeta {

	componentImplType, ok := components.GetType(executor)
	if !ok {
		componentImplType = generic.ParseTypeName(reflect.ValueOf(executor))
	}

	return &executorMeta{
		component:                  c,
		isComponentCallbackEnabled: components.IsCallbacksEnabled(executor),
		componentImplType:          componentImplType,
	}
}

func getNodeInfo(opts ...GraphAddNodeOpt) (*nodeInfo, *graphAddNodeOpts) {

	opt := getGraphAddNodeOpts(opts...)

	return &nodeInfo{
		name:          opt.nodeOptions.nodeName,
		inputKey:      opt.nodeOptions.inputKey,
		outputKey:     opt.nodeOptions.outputKey,
		preProcessor:  opt.processor.statePreHandler,
		postProcessor: opt.processor.statePostHandler,
		compileOption: newGraphCompileOptions(opt.nodeOptions.graphCompileOption...),
	}, opt
}
