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
	// A nil interface value carries no reflect type, and reflect.Value.Type
	// panics on the zero Value it produces. Locate the first value that does
	// carry a type to select the merge strategy; nil values are forwarded to
	// the merge function as typed zeros for it to interpret.
	primaryIdx := -1
	for i, v := range vs {
		if v != nil {
			primaryIdx = i
			break
		}
	}
	if primaryIdx < 0 {
		return nil, fmt.Errorf("(mergeValues) unsupported values: " +
			"every fan-in value is nil, so no merge type can be determined")
	}
	t0 := reflect.TypeOf(vs[primaryIdx])

	if fn := internal.GetMergeFunc(t0); fn != nil {
		return fn(vs)
	}

	// merge StreamReaders
	if s, ok := vs[primaryIdx].(streamReader); ok {
		t := s.getChunkType()
		if internal.GetMergeFunc(t) == nil {
			return nil, fmt.Errorf("(mergeValues | stream type)"+
				" unsupported chunk type: %v", t)
		}

		ss := make([]streamReader, 0, len(vs)-1)
		for i, v := range vs {
			if i == primaryIdx {
				continue
			}
			sri, ok_ := v.(streamReader)
			if !ok_ {
				return nil, fmt.Errorf("(mergeStream) unexpected type. "+
					"expect: %v, got: %v", t0, reflect.TypeOf(v))
			}

			if st := sri.getChunkType(); st != t {
				return nil, fmt.Errorf("(mergeStream) chunk type mismatch. "+
					"expect: %v, got: %v", t, st)
			}

			ss = append(ss, sri)
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
