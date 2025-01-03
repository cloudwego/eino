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

	"github.com/cloudwego/eino/compose/internal"
	"github.com/cloudwego/eino/schema"
)

type GenLocalState[S any] func(ctx context.Context) (state S)

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

// GetState gets the state from the context.
// When using this method to read or write state in custom nodes, it may lead to data race because other nodes may concurrently access the state.
// You need to be aware of and resolve this situation, typically by adding a mutex.
// It's recommended to only READ the returned state. If you want to WRITE to state, consider using StatePreHandler / StatePostHandler because they are concurrency safe out of the box.
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
	return internal.GetState[S](ctx)
}
