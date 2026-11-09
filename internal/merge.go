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

package internal

import (
	"fmt"
	"reflect"

	"github.com/cloudwego/eino/internal/generic"
)

var mergeFuncs = map[reflect.Type]any{}

func RegisterValuesMergeFunc[T any](fn func([]T) (T, error)) {
	mergeFuncs[generic.TypeOf[T]()] = fn
}

func GetMergeFunc(typ reflect.Type) func([]any) (any, error) {
	if fn, ok := mergeFuncs[typ]; ok {
		return func(vs []any) (any, error) {
			rvs := reflect.MakeSlice(reflect.SliceOf(typ), 0, len(vs))
			for _, v := range vs {
				if v == nil {
					// A nil interface value has no reflect type of its own. It is
					// only representable when the merge type can hold nil, where it
					// becomes a typed zero for the merge function to interpret.
					if !typeHoldsNil(typ) {
						return nil, fmt.Errorf(
							"(values merge) field type mismatch. expected: '%v', got: '<nil>'", typ)
					}
					rvs = reflect.Append(rvs, reflect.Zero(typ))
					continue
				}
				if t := reflect.TypeOf(v); t != typ {
					return nil, fmt.Errorf(
						"(values merge) field type mismatch. expected: '%v', got: '%v'", typ, t)
				}
				rvs = reflect.Append(rvs, reflect.ValueOf(v))
			}

			rets := reflect.ValueOf(fn).Call([]reflect.Value{rvs})
			var err error
			if !rets[1].IsNil() {
				err = rets[1].Interface().(error)
			}
			return rets[0].Interface(), err
		}
	}

	if typ.Kind() == reflect.Map {
		return func(vs []any) (any, error) {
			return mergeMap(typ, vs)
		}
	}

	return nil
}

// typeHoldsNil reports whether values of typ can be nil, i.e. whether a nil
// interface value is representable as a zero of this type.
func typeHoldsNil(typ reflect.Type) bool {
	switch typ.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func:
		return true
	}
	return false
}

func mergeMap(typ reflect.Type, vs []any) (any, error) {
	merged := reflect.MakeMap(typ)
	for _, v := range vs {
		if v == nil {
			// A nil map contributes no entries; skip it rather than failing on
			// reflect.ValueOf(nil), which has no type to check or iterate.
			continue
		}
		if t := reflect.TypeOf(v); t != typ {
			return nil, fmt.Errorf(
				"(values merge map) field type mismatch. expected: '%v', got: '%v'", typ, t)
		}

		iter := reflect.ValueOf(v).MapRange()
		for iter.Next() {
			key, val := iter.Key(), iter.Value()
			if merged.MapIndex(key).IsValid() {
				return nil, fmt.Errorf("(values merge map) duplicated key ('%v') found", key.Interface())
			}
			merged.SetMapIndex(key, val)
		}
	}

	return merged.Interface(), nil
}
