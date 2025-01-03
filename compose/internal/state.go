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
	"fmt"
	"reflect"

	"github.com/cloudwego/eino/schema"
	"github.com/cloudwego/eino/utils/generic"
)

type StateKey struct{}

func convertPreHandler[I, S any](handler func(ctx context.Context, in I, state S) (I, error)) *composableRunnable {
	rf := func(ctx context.Context, in I, opts ...any) (I, error) {
		cState, err := GetState[S](ctx)
		if err != nil {
			return in, err
		}

		return handler(ctx, in, cState)
	}

	return runnableLambda[I, I](rf, nil, nil, nil, false)
}

func convertPostHandler[O, S any](handler func(ctx context.Context, out O, state S) (O, error)) *composableRunnable {
	rf := func(ctx context.Context, out O, opts ...any) (O, error) {
		cState, err := GetState[S](ctx)
		if err != nil {
			return out, err
		}

		return handler(ctx, out, cState)
	}

	return runnableLambda[O, O](rf, nil, nil, nil, false)
}

func streamConvertPreHandler[I, S any](handler func(ctx context.Context, in *schema.StreamReader[I], state S) (*schema.StreamReader[I], error)) *composableRunnable {
	rf := func(ctx context.Context, in *schema.StreamReader[I], opts ...any) (*schema.StreamReader[I], error) {
		cState, err := GetState[S](ctx)
		if err != nil {
			return in, err
		}

		return handler(ctx, in, cState)
	}

	return runnableLambda[I, I](nil, nil, nil, rf, false)
}

func streamConvertPostHandler[O, S any](handler func(ctx context.Context, out *schema.StreamReader[O], state S) (*schema.StreamReader[O], error)) *composableRunnable {
	rf := func(ctx context.Context, out *schema.StreamReader[O], opts ...any) (*schema.StreamReader[O], error) {
		cState, err := GetState[S](ctx)
		if err != nil {
			return out, err
		}

		return handler(ctx, out, cState)
	}

	return runnableLambda[O, O](nil, nil, nil, rf, false)
}

func GetState[S any](ctx context.Context) (S, error) {
	state := ctx.Value(StateKey{})

	cState, ok := state.(S)
	if !ok {
		var s S
		return s, fmt.Errorf("unexpected state type. expected: %v, got: %v",
			generic.TypeOf[S](), reflect.TypeOf(state))
	}

	return cState, nil
}
