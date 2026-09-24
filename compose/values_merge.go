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
	"fmt"
	"reflect"

	"github.com/cloudwego/eino/internal"
)

// RegisterValuesMergeFunc registers a function to merge outputs from multiple nodes when fan-in.
// It's used to define how to merge for a specific type.
// For maps that already have a default merge function, you don't need to register a new one unless you want to customize the merge logic.
func RegisterValuesMergeFunc[T any](fn func([]T) (T, error)) {
	internal.RegisterValuesMergeFunc(fn)
}

type mergeOptions struct {
	streamMergeWithSourceEOF bool
	names                    []string
}

// the caller should ensure len(vs) > 1
func mergeValues(vs []any, opts *mergeOptions) (any, error) {
	// An untyped nil output carries no value: a fan-in predecessor returning
	// (nil, nil) contributes nothing to the merge. Drop untyped nils so the
	// outcome is deterministic — previously a nil landing at vs[0] panicked in
	// reflect.Value.Type, while the same values in another order failed inside
	// the type-specific merge func, and channel iteration order is random.
	var nilFiltered bool
	vs, opts, nilFiltered = dropNilMergeValues(vs, opts)
	if nilFiltered {
		if len(vs) == 0 {
			return nil, nil
		}
		if len(vs) == 1 {
			return vs[0], nil
		}
	}

	v0 := reflect.ValueOf(vs[0])
	t0 := v0.Type()

	if fn := internal.GetMergeFunc(t0); fn != nil {
		return fn(vs)
	}

	// merge StreamReaders
	if s, ok := vs[0].(streamReader); ok {
		t := s.getChunkType()
		if internal.GetMergeFunc(t) == nil {
			return nil, fmt.Errorf("(mergeValues | stream type)"+
				" unsupported chunk type: %v", t)
		}

		ss := make([]streamReader, len(vs)-1)
		for i := 0; i < len(ss); i++ {
			sri, ok_ := vs[i+1].(streamReader)
			if !ok_ {
				return nil, fmt.Errorf("(mergeStream) unexpected type. "+
					"expect: %v, got: %v", t0, reflect.TypeOf(vs[i]))
			}

			if st := sri.getChunkType(); st != t {
				return nil, fmt.Errorf("(mergeStream) chunk type mismatch. "+
					"expect: %v, got: %v", t, st)
			}

			ss[i] = sri
		}

		if opts != nil && opts.streamMergeWithSourceEOF {
			ms := s.mergeWithNames(ss, opts.names)
			return ms, nil
		}

		ms := s.merge(ss)

		return ms, nil
	}

	return nil, fmt.Errorf("(mergeValues) unsupported type: %v", t0)
}

// dropNilMergeValues removes untyped nil values before merging, keeping
// opts.names (when set) aligned with the surviving values. It reports whether
// any value was dropped, so the caller can short-circuit empty/singleton
// results without changing its contract for nil-free inputs.
func dropNilMergeValues(vs []any, opts *mergeOptions) ([]any, *mergeOptions, bool) {
	hasNil := false
	for _, v := range vs {
		if v == nil {
			hasNil = true
			break
		}
	}
	if !hasNil {
		return vs, opts, false
	}

	filtered := make([]any, 0, len(vs))
	var filteredNames []string
	if opts != nil && opts.names != nil {
		filteredNames = make([]string, 0, len(opts.names))
	}
	for i, v := range vs {
		if v == nil {
			continue
		}
		filtered = append(filtered, v)
		if filteredNames != nil && i < len(opts.names) {
			filteredNames = append(filteredNames, opts.names[i])
		}
	}
	if opts != nil {
		opts.names = filteredNames
	}
	return filtered, opts, true
}
