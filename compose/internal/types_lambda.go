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

package internal

import (
	"context"

	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/schema"
)

// Invoke is the type of the invokable lambda function.
type Invoke[I, O, TOption any] func(ctx context.Context, input I, opts ...TOption) (output O, err error)

// Stream is the type of the streamable lambda function.
type Stream[I, O, TOption any] func(ctx context.Context,
	input I, opts ...TOption) (output *schema.StreamReader[O], err error)

// Collect is the type of the collectable lambda function.
type Collect[I, O, TOption any] func(ctx context.Context,
	input *schema.StreamReader[I], opts ...TOption) (output O, err error)

// Transform is the type of the transformable lambda function.
type Transform[I, O, TOption any] func(ctx context.Context,
	input *schema.StreamReader[I], opts ...TOption) (output *schema.StreamReader[O], err error)

// Lambda is the node that wraps the user provided lambda function.
// It can be used as a node in Graph or Chain (include Parallel and Branch).
// Create a Lambda by using AnyLambda/InvokableLambda/StreamableLambda/CollectableLambda/TransformableLambda.
// e.g.
//
//	lambda := compose.InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
//		return input, nil
//	})
type Lambda struct {
	executor *composableRunnable
}

func (l *Lambda) IsCallbacksEnabled() bool {
	return l.executor.meta.isComponentCallbackEnabled
}

func (l *Lambda) Component() components.Component {
	return l.executor.meta.component
}

func (l *Lambda) GetType() string {
	return l.executor.meta.componentImplType
}

type lambdaOpts struct {
	// same as executorMeta.isComponentCallbackEnabled
	// indicates whether the executable lambda user provided could execute the callback aspect itself.
	// if it could, the callback in the corresponding graph node won't be executed anymore
	enableComponentCallback bool

	// same as executorMeta.componentImplType
	// for AnyLambda, the value comes from the user's explicit config
	// if componentImplType is empty, then the class name or func name in the instance will be inferred, but no guarantee.
	componentImplType string
}

// LambdaOpt is the option for creating a Lambda.
type LambdaOpt func(o *lambdaOpts)

// WithLambdaCallbackEnable enables the callback aspect of the lambda function.
func WithLambdaCallbackEnable(y bool) LambdaOpt {
	return func(o *lambdaOpts) {
		o.enableComponentCallback = y
	}
}

// WithLambdaType sets the type of the lambda function.
func WithLambdaType(t string) LambdaOpt {
	return func(o *lambdaOpts) {
		o.componentImplType = t
	}
}

func AnyLambda[I, O, TOption any](
	i func(ctx context.Context, input I, opts ...TOption) (output O, err error),
	s func(ctx context.Context, input I, opts ...TOption) (output *schema.StreamReader[O], err error),
	c func(ctx context.Context, input *schema.StreamReader[I], opts ...TOption) (output O, err error),
	t func(ctx context.Context, input *schema.StreamReader[I], opts ...TOption) (output *schema.StreamReader[O], err error),
	opts ...LambdaOpt) *Lambda {

	opt := getLambdaOpt(opts...)

	executor := runnableLambda(i, s, c, t,
		!opt.enableComponentCallback,
	)
	executor.meta = &executorMeta{
		component:                  ComponentOfLambda,
		isComponentCallbackEnabled: opt.enableComponentCallback,
		componentImplType:          opt.componentImplType,
	}

	return &Lambda{
		executor: executor,
	}
}

func getLambdaOpt(opts ...LambdaOpt) *lambdaOpts {
	opt := &lambdaOpts{
		enableComponentCallback: false,
		componentImplType:       "",
	}

	for _, optFn := range opts {
		optFn(opt)
	}
	return opt
}
