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
	"reflect"

	"github.com/cloudwego/eino/schema"
	"github.com/cloudwego/eino/utils/generic"
)

// GenLocalState is a function that generates the state.
type GenLocalState[S any] func(ctx context.Context) (state S)

type stateKey struct{}

type internalState struct {
	state     any
	forbidden bool
}

// StatePreHandler is a function that is called before the node is executed.
// Notice: if user called Stream but with StatePreHandler, the StatePreHandler will read all stream chunks and merge them into a single object.
type StatePreHandler[I, S any] func(ctx context.Context, in I, state S) (I, error)

// StatePostHandler is a function that is called after the node is executed.
// Notice: if user called Stream but with StatePostHandler, the StatePostHandler will read all stream chunks and merge them into a single object.
type StatePostHandler[O, S any] func(ctx context.Context, out O, state S) (O, error)

// StreamStatePreHandler is a function that is called before the node is executed with stream input and output.
type StreamStatePreHandler[I, S any] func(ctx context.Context, in *schema.StreamReader[I], state S) (*schema.StreamReader[I], error)

// StreamStatePostHandler is a function that is called after the node is executed with stream input and output.
type StreamStatePostHandler[O, S any] func(ctx context.Context, out *schema.StreamReader[O], state S) (*schema.StreamReader[O], error)

func convertPreHandler[I, S any](handler StatePreHandler[I, S]) *composableRunnable {
	rf := func(ctx context.Context, in I, opts ...any) (I, error) {
		cState, err := getState[S](ctx)
		if err != nil {
			return in, err
		}

		return handler(ctx, in, cState)
	}

	return runnableLambda[I, I](rf, nil, nil, nil, false)
}

func convertPostHandler[O, S any](handler StatePostHandler[O, S]) *composableRunnable {
	rf := func(ctx context.Context, out O, opts ...any) (O, error) {
		cState, err := getState[S](ctx)
		if err != nil {
			return out, err
		}

		return handler(ctx, out, cState)
	}

	return runnableLambda[O, O](rf, nil, nil, nil, false)
}

func streamConvertPreHandler[I, S any](handler StreamStatePreHandler[I, S]) *composableRunnable {
	rf := func(ctx context.Context, in *schema.StreamReader[I], opts ...any) (*schema.StreamReader[I], error) {
		cState, err := getState[S](ctx)
		if err != nil {
			return in, err
		}

		return handler(ctx, in, cState)
	}

	return runnableLambda[I, I](nil, nil, nil, rf, false)
}

func streamConvertPostHandler[O, S any](handler StreamStatePostHandler[O, S]) *composableRunnable {
	rf := func(ctx context.Context, out *schema.StreamReader[O], opts ...any) (*schema.StreamReader[O], error) {
		cState, err := getState[S](ctx)
		if err != nil {
			return out, err
		}

		return handler(ctx, out, cState)
	}

	return runnableLambda[O, O](nil, nil, nil, rf, false)
}

// GetState gets the state from the context.
// When using this method to read or write state in custom nodes, it may lead to data race because other nodes may concurrently access the state.
// You need to be aware of and resolve this situation, typically by adding a mutex.
// It's recommended to only READ the returned state. If you want to WRITE to state, consider using StatePreHandler / StatePostHandler because they are concurrency safe out of the box.
// note: this method will report error
// e.g.
//
//	lambdaFunc := func(ctx context.Context, in string, opts ...any) (string, error) {
//		state, err := compose.GetState[*testState](ctx)
//		if err != nil {
//			return "", err
//		}
//		// do something with state
//		return in, nil
//	}
//
//	stateGraph := compose.NewStateGraph[string, string, testState](genStateFunc)
//	stateGraph.AddNode("node1", lambdaFunc)
func GetState[S any](ctx context.Context) (S, error) {
	state := ctx.Value(stateKey{})

	iState := state.(*internalState)
	if iState.forbidden {
		var s S
		return s, fmt.Errorf("GetState in node is forbidden in Workflow because of the race of state, if you have handled concurrent state access safety, you can add WithGetStateEnable option at graph compile")
	}
	cState, ok := iState.state.(S)
	if !ok {
		var s S
		return s, fmt.Errorf("unexpected state type. expected: %v, got: %v",
			generic.TypeOf[S](), reflect.TypeOf(iState.state))
	}

	return cState, nil
}

func getState[S any](ctx context.Context) (S, error) {
	state := ctx.Value(stateKey{})

	cState, ok := state.(*internalState).state.(S)
	if !ok {
		var s S
		return s, fmt.Errorf("unexpected state type. expected: %v, got: %v",
			generic.TypeOf[S](), reflect.TypeOf(state))
	}

	return cState, nil
}
