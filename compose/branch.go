/*
 * Copyright 2025 CloudWeGo Authors
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

	"github.com/cloudwego/eino/internal/generic"
	"github.com/cloudwego/eino/schema"
)

// GraphBranchCondition is the condition type for the branch.
type GraphBranchCondition[T any] func(ctx context.Context, in T) (endNode string, err error)

// StreamGraphBranchCondition is the condition type for the stream branch.
type StreamGraphBranchCondition[T any] func(ctx context.Context, in *schema.StreamReader[T]) (endNode string, err error)

// GraphMultiBranchCondition is the condition type for the multi choice branch.
type GraphMultiBranchCondition[T any] func(ctx context.Context, in T) (endNode map[string]bool, err error)

// StreamGraphMultiBranchCondition is the condition type for the stream multi choice branch.
type StreamGraphMultiBranchCondition[T any] func(ctx context.Context, in *schema.StreamReader[T]) (endNodes map[string]bool, err error)

// GraphBranch is the branch type for the graph.
// It is used to determine the next node based on the condition.
type GraphBranch struct {
	invoke    func(ctx context.Context, input any) (output []string, err error)
	collect   func(ctx context.Context, input streamReader) (output []string, err error)
	inputType reflect.Type
	*genericHelper
	endNodes   map[string]bool
	idx        int // used to distinguish branches in parallel
	noDataFlow bool
}

// GetEndNode returns the all end nodes of the branch.
func (gb *GraphBranch) GetEndNode() map[string]bool {
	return gb.endNodes
}

func newGraphBranch[T any](r *runnablePacker[T, []string, any], endNodes map[string]bool) *GraphBranch {
	return &GraphBranch{
		invoke: func(ctx context.Context, input any) (output []string, err error) {
			in, ok := input.(T)
			if !ok {
				// When a nil is passed as an 'any' type, its original type information is lost,
				// becoming an untyped nil. This would cause type assertions to fail.
				// So if the input is nil and the target type T is an interface, we need to explicitly create a nil of type T.
				if input == nil && generic.TypeOf[T]().Kind() == reflect.Interface {
					var i T
					in = i
				} else {
					panic(newUnexpectedInputTypeErr(generic.TypeOf[T](), reflect.TypeOf(input)))
				}
			}
			return r.Invoke(ctx, in)
		},
		collect: func(ctx context.Context, input streamReader) (output []string, err error) {
			in, ok := unpackStreamReader[T](input)
			if !ok {
				panic(newUnexpectedInputTypeErr(generic.TypeOf[T](), input.getType()))
			}
			return r.Collect(ctx, in)
		},
		inputType:     generic.TypeOf[T](),
		genericHelper: newGenericHelper[T, T](),
		endNodes:      endNodes,
	}
}

// NewGraphMultiBranch creates a branch for graphs where a condition selects
// multiple end nodes; only keys present in endNodes are allowed.
func NewGraphMultiBranch[T any](condition GraphMultiBranchCondition[T], endNodes map[string]bool) *GraphBranch {
	condRun := func(ctx context.Context, in T, opts ...any) ([]string, error) {
		ends, err := condition(ctx, in)
		if err != nil {
			return nil, err
		}
		ret := make([]string, 0, len(ends))
		for end := range ends {
			if !endNodes[end] {
				return nil, fmt.Errorf("branch invocation returns unintended end node: %s", end)
			}
			ret = append(ret, end)
		}

		return ret, nil
	}

	return newGraphBranch(newRunnablePacker(condRun, nil, nil, nil, false), endNodes)
}

// NewStreamGraphMultiBranch creates a streaming branch where a condition on
// the input stream selects multiple end nodes.
func NewStreamGraphMultiBranch[T any](condition StreamGraphMultiBranchCondition[T],
	endNodes map[string]bool) *GraphBranch {

	condRun := func(ctx context.Context, in *schema.StreamReader[T], opts ...any) ([]string, error) {
		ends, err := condition(ctx, in)
		if err != nil {
			return nil, err
		}

		ret := make([]string, 0, len(ends))
		for end := range ends {
			if !endNodes[end] {
				return nil, fmt.Errorf("branch invocation returns unintended end node: %s", end)
			}
			ret = append(ret, end)
		}
		return ret, nil
	}

	return newGraphBranch(newRunnablePacker(nil, nil, condRun, nil, false), endNodes)
}

// NewGraphBranch creates a new graph branch.
// It is used to determine the next node based on the condition.
// e.g.
//
//	condition := func(ctx context.Context, in string) (string, error) {
//		// logic to determine the next node
//		return "next_node_key", nil
//	}
//	endNodes := map[string]bool{"path01": true, "path02": true}
//	branch := compose.NewGraphBranch(condition, endNodes)
//
//	graph.AddBranch("key_of_node_before_branch", branch)
func NewGraphBranch[T any](condition GraphBranchCondition[T], endNodes map[string]bool) *GraphBranch {
	return NewGraphMultiBranch(func(ctx context.Context, in T) (endNode map[string]bool, err error) {
		ret, err := condition(ctx, in)
		if err != nil {
			return nil, err
		}
		return map[string]bool{ret: true}, nil
	}, endNodes)
}

// NewStreamGraphBranch creates a new stream graph branch.
// It is used to determine the next node based on the condition of stream input.
// e.g.
//
//	condition := func(ctx context.Context, in *schema.StreamReader[T]) (string, error) {
//		// logic to determine the next node.
//		// to use the feature of stream, you can use the first chunk to determine the next node.
//		return "next_node_key", nil
//	}
//	endNodes := map[string]bool{"path01": true, "path02": true}
//	branch := compose.NewStreamGraphBranch(condition, endNodes)
//
//	graph.AddBranch("key_of_node_before_branch", branch)
func NewStreamGraphBranch[T any](condition StreamGraphBranchCondition[T], endNodes map[string]bool) *GraphBranch {
	return NewStreamGraphMultiBranch(func(ctx context.Context, in *schema.StreamReader[T]) (endNode map[string]bool, err error) {
		ret, err := condition(ctx, in)
		if err != nil {
			return nil, err
		}
		return map[string]bool{ret: true}, nil
	}, endNodes)
}
