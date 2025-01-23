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

// InvokeWOOpt is the type of the invokable lambda function without options.
type InvokeWOOpt[I, O any] func(ctx context.Context, input I) (output O, err error)

// StreamWOOpt is the type of the streamable lambda function without options.
type StreamWOOpt[I, O any] func(ctx context.Context,
	input I) (output *schema.StreamReader[O], err error)

// CollectWOOpt is the type of the collectable lambda function without options.
type CollectWOOpt[I, O any] func(ctx context.Context,
	input *schema.StreamReader[I]) (output O, err error)

// TransformWOOpts is the type of the transformable lambda function without options.
type TransformWOOpts[I, O any] func(ctx context.Context,
	input *schema.StreamReader[I]) (output *schema.StreamReader[O], err error)

// Lambda is the node that wraps the user provided lambda function.
// It can be used as a node in Graph or Chain (include Parallel and Branch).
// Create a Lambda by using AnyLambda/InvokableLambda/StreamableLambda/CollectableLambda/TransformableLambda.
// eg.
//
//	lambda := compose.InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
//		return input, nil
//	})
type Lambda struct {
	executor *composableRunnable
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

type unreachableOption struct{}

// InvokableLambdaWithOption creates a Lambda with invokable lambda function and options.
func InvokableLambdaWithOption[I, O, TOption any](i Invoke[I, O, TOption], opts ...LambdaOpt) *Lambda {
	return anyLambda(i, nil, nil, nil, opts...)
}

// InvokableLambda creates a Lambda with invokable lambda function without options.
func InvokableLambda[I, O any](i InvokeWOOpt[I, O], opts ...LambdaOpt) *Lambda {
	f := func(ctx context.Context, input I, opts_ ...unreachableOption) (output O, err error) {
		return i(ctx, input)
	}

	return anyLambda(f, nil, nil, nil, opts...)
}

// StreamableLambdaWithOption creates a Lambda with streamable lambda function and options.
func StreamableLambdaWithOption[I, O, TOption any](s Stream[I, O, TOption], opts ...LambdaOpt) *Lambda {
	return anyLambda(nil, s, nil, nil, opts...)
}

// StreamableLambda creates a Lambda with streamable lambda function without options.
func StreamableLambda[I, O any](s StreamWOOpt[I, O], opts ...LambdaOpt) *Lambda {
	f := func(ctx context.Context, input I, opts_ ...unreachableOption) (
		output *schema.StreamReader[O], err error) {

		return s(ctx, input)
	}

	return anyLambda(nil, f, nil, nil, opts...)
}

// CollectableLambdaWithOption creates a Lambda with collectable lambda function and options.
func CollectableLambdaWithOption[I, O, TOption any](c Collect[I, O, TOption], opts ...LambdaOpt) *Lambda {
	return anyLambda(nil, nil, c, nil, opts...)
}

// CollectableLambda creates a Lambda with collectable lambda function without options.
func CollectableLambda[I, O any](c CollectWOOpt[I, O], opts ...LambdaOpt) *Lambda {
	f := func(ctx context.Context, input *schema.StreamReader[I],
		opts_ ...unreachableOption) (output O, err error) {

		return c(ctx, input)
	}

	return anyLambda(nil, nil, f, nil, opts...)
}

// TransformableLambdaWithOption creates a Lambda with transformable lambda function and options.
func TransformableLambdaWithOption[I, O, TOption any](t Transform[I, O, TOption], opts ...LambdaOpt) *Lambda {
	return anyLambda(nil, nil, nil, t, opts...)
}

// TransformableLambda creates a Lambda with transformable lambda function without options.
func TransformableLambda[I, O any](t TransformWOOpts[I, O], opts ...LambdaOpt) *Lambda {

	f := func(ctx context.Context, input *schema.StreamReader[I],
		opts_ ...unreachableOption) (output *schema.StreamReader[O], err error) {

		return t(ctx, input)
	}

	return anyLambda(nil, nil, nil, f, opts...)
}

// AnyLambda creates a Lambda with any lambda function.
// you can only implement one or more of the four lambda functions, and the rest use nil.
// eg.
//
//	invokeFunc := func(ctx context.Context, input string, opts ...myOption) (output string, err error) {
//		// ...
//	}
//	streamFunc := func(ctx context.Context, input string, opts ...myOption) (output *schema.StreamReader[string], err error) {
//		// ...
//	}
//
// lambda := compose.AnyLambda(invokeFunc, streamFunc, nil, nil)
func AnyLambda[I, O, TOption any](i Invoke[I, O, TOption], s Stream[I, O, TOption],
	c Collect[I, O, TOption], t Transform[I, O, TOption], opts ...LambdaOpt) (*Lambda, error) {

	if i == nil && s == nil && c == nil && t == nil {
		return nil, fmt.Errorf("needs to have at least one of four lambda types: invoke/stream/collect/transform, got none")
	}

	return anyLambda(i, s, c, t, opts...), nil
}

func anyLambda[I, O, TOption any](i Invoke[I, O, TOption], s Stream[I, O, TOption],
	c Collect[I, O, TOption], t Transform[I, O, TOption], opts ...LambdaOpt) *Lambda {

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

// ToList creates a Lambda that converts input I to a []I.
// It's useful when you want to convert a single input to a list of inputs.
// eg.
//
//	lambda := compose.ToList[*schema.Message]()
//	chain := compose.NewChain[[]*schema.Message, []*schema.Message]()
//
//	chain.AddChatModel(chatModel) // chatModel returns *schema.Message, but we need []*schema.Message
//	chain.AddLambda(lambda) // convert *schema.Message to []*schema.Message
func ToList[I any](opts ...LambdaOpt) *Lambda {
	i := func(ctx context.Context, input I, opts_ ...unreachableOption) (output []I, err error) {
		return []I{input}, nil
	}

	f := func(ctx context.Context, inputS *schema.StreamReader[I], opts_ ...unreachableOption) (outputS *schema.StreamReader[[]I], err error) {
		return schema.StreamReaderWithConvert(inputS, func(i I) ([]I, error) {
			return []I{i}, nil
		}), nil
	}

	return anyLambda(i, nil, nil, f, opts...)
}

// MessageParser creates a lambda that parses a message into an object T, usually used after a chatmodel.
// usage:
//
//	parser := schema.NewMessageJSONParser[MyStruct](&schema.MessageJSONParseConfig{
//		ParseFrom: schema.MessageParseFromContent,
//	})
//	parserLambda := MessageParser(parser)
//
//	chain := NewChain[*schema.Message, MyStruct]()
//	chain.AppendChatModel(chatModel)
//	chain.AppendLambda(parserLambda)
//
//	r, err := chain.Compile(context.Background())
//
//	// parsed is a MyStruct object
//	parsed, err := r.Invoke(context.Background(), &schema.Message{
//		Role:    schema.MessageRoleUser,
//		Content: "return a json string for my struct",
//	})
func MessageParser[T any](p schema.MessageParser[T], opts ...LambdaOpt) *Lambda {
	i := func(ctx context.Context, input *schema.Message, opts_ ...unreachableOption) (output T, err error) {
		return p.Parse(ctx, input)
	}

	opts = append([]LambdaOpt{WithLambdaType("MessageParse")}, opts...)

	return anyLambda(i, nil, nil, nil, opts...)
}
