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

const (
	ComponentOfUnknown     = internal.ComponentOfUnknown
	ComponentOfGraph       = internal.ComponentOfGraph
	ComponentOfChain       = internal.ComponentOfChain
	ComponentOfPassthrough = internal.ComponentOfPassthrough
	ComponentOfToolsNode   = internal.ComponentOfToolsNode
	ComponentOfLambda      = internal.ComponentOfLambda
)

// NodeTriggerMode controls the triggering mode of graph nodes.
type NodeTriggerMode internal.NodeTriggerMode

const (
	// AnyPredecessor means that the current node will be triggered as long as any of its predecessor nodes has finished running.
	// Note that actual implementation organizes node execution in batches.
	// In this context, 'any predecessor finishes' would mean the other nodes of the same batch need to be finished too.
	AnyPredecessor = NodeTriggerMode(internal.AnyPredecessor)
	// AllPredecessor means that the current node will only be triggered when all of its predecessor nodes have finished running.
	AllPredecessor = NodeTriggerMode(internal.AllPredecessor)
)

// Runnable is the interface for an executable object. Graph, Chain can be compiled into Runnable.
// runnable is the core conception of eino, we do downgrade compatibility for four data flow patterns,
// and can automatically connect components that only implement one or more methods.
// eg, if a component only implements Stream() method, you can still call Invoke() to convert stream output to invoke output.
type Runnable[I, O any] interface {
	Invoke(ctx context.Context, input I, opts ...Option) (output O, err error)
	Stream(ctx context.Context, input I, opts ...Option) (output *schema.StreamReader[O], err error)
	Collect(ctx context.Context, input *schema.StreamReader[I], opts ...Option) (output O, err error)
	Transform(ctx context.Context, input *schema.StreamReader[I], opts ...Option) (output *schema.StreamReader[O], err error)
}

type graphRunner[I, O any] struct {
	i func(ctx context.Context, input I, opts ...Option) (output O, err error)
	s func(ctx context.Context, input I, opts ...Option) (output *schema.StreamReader[O], err error)
	c func(ctx context.Context, input *schema.StreamReader[I], opts ...Option) (output O, err error)
	t func(ctx context.Context, input *schema.StreamReader[I], opts ...Option) (output *schema.StreamReader[O], err error)
}

func (g *graphRunner[I, O]) Invoke(ctx context.Context, input I, opts ...Option) (output O, err error) {
	return g.i(ctx, input, opts...)
}

func (g *graphRunner[I, O]) Stream(ctx context.Context, input I, opts ...Option) (output *schema.StreamReader[O], err error) {
	return g.s(ctx, input, opts...)
}

func (g *graphRunner[I, O]) Collect(ctx context.Context, input *schema.StreamReader[I], opts ...Option) (output O, err error) {
	return g.c(ctx, input, opts...)
}

func (g *graphRunner[I, O]) Transform(ctx context.Context, input *schema.StreamReader[I], opts ...Option) (output *schema.StreamReader[O], err error) {
	return g.t(ctx, input, opts...)
}

func convertRunnable[I, O any](r internal.Runnable[I, O]) Runnable[I, O] {
	convertOptList := func(opts ...Option) []internal.Option {
		options := make([]internal.Option, len(opts))
		for i, o := range opts {
			options[i] = internal.Option(o)
		}
		return options
	}
	return &graphRunner[I, O]{
		i: func(ctx context.Context, input I, opts ...Option) (output O, err error) {
			return r.Invoke(ctx, input, convertOptList(opts...)...)
		},
		s: func(ctx context.Context, input I, opts ...Option) (output *schema.StreamReader[O], err error) {
			return r.Stream(ctx, input, convertOptList(opts...)...)
		},
		c: func(ctx context.Context, input *schema.StreamReader[I], opts ...Option) (output O, err error) {
			return r.Collect(ctx, input, convertOptList(opts...)...)
		},
		t: func(ctx context.Context, input *schema.StreamReader[I], opts ...Option) (output *schema.StreamReader[O], err error) {
			return r.Transform(ctx, input, convertOptList(opts...)...)
		},
	}
}
