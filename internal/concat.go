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
	"strings"

	"github.com/cloudwego/eino/utils/generic"
)

var (
	concatFuncs = map[reflect.Type]any{
		generic.TypeOf[string](): concatStrings,
	}
)

func concatStrings(ss []string) (string, error) {
	var n int
	for _, s := range ss {
		n += len(s)
	}

	var b strings.Builder
	b.Grow(n)
	for _, s := range ss {
		_, err := b.WriteString(s)
		if err != nil {
			return "", err
		}
	}

	return b.String(), nil
}

func RegisterStreamChunkConcatFunc[T any](fn func([]T) (T, error)) {
	concatFuncs[generic.TypeOf[T]()] = fn
}

func GetConcatFunc(tpe reflect.Type) func(reflect.Value) (reflect.Value, error) {
	if fn, ok := concatFuncs[tpe]; ok {
		return func(a reflect.Value) (reflect.Value, error) {
			rvs := reflect.ValueOf(fn).Call([]reflect.Value{a})
			var err error
			if !rvs[1].IsNil() {
				err = rvs[1].Interface().(error)
			}
			return rvs[0], err
		}
	}

	return nil
}

// ConcatItems the caller should ensure len(items) > 1
func ConcatItems[T any](items []T) (T, error) {
	typ := generic.TypeOf[T]()
	v := reflect.ValueOf(items)

	var cv reflect.Value
	var err error

	// handle map kind
	if typ.Kind() == reflect.Map {
		cv, err = concatMaps(v)
	} else {
		cv, err = concatSliceValue(v)
	}

	if err != nil {
		var t T
		return t, err
	}

	return cv.Interface().(T), nil
}

func concatMaps(ms reflect.Value) (reflect.Value, error) {
	typ := ms.Type().Elem()

	rms := reflect.MakeMap(reflect.MapOf(typ.Key(), generic.TypeOf[[]any]()))
	ret := reflect.MakeMap(typ)

	n := ms.Len()
	for i := 0; i < n; i++ {
		m := ms.Index(i)

		for _, key := range m.MapKeys() {
			vals := rms.MapIndex(key)
			if !vals.IsValid() {
				var s []any
				vals = reflect.ValueOf(s)
			}

			val := m.MapIndex(key)
			vals = reflect.Append(vals, val)
			rms.SetMapIndex(key, vals)
		}
	}

	for _, key := range rms.MapKeys() {
		vals := rms.MapIndex(key)

		anyVals := vals.Interface().([]any)
		v, err := toSliceValue(anyVals)
		if err != nil {
			return reflect.Value{}, err
		}

		var cv reflect.Value

		if v.Type().Elem().Kind() == reflect.Map {
			cv, err = concatMaps(v)
		} else {
			cv, err = concatSliceValue(v)
		}

		if err != nil {
			return reflect.Value{}, err
		}

		ret.SetMapIndex(key, cv)
	}

	return ret, nil
}

func concatSliceValue(val reflect.Value) (reflect.Value, error) {
	elmType := val.Type().Elem()

	if val.Len() == 1 {
		return val.Index(0), nil
	}

	f := GetConcatFunc(elmType)
	if f != nil {
		return f(val)
	}

	var (
		structType  reflect.Type
		isStructPtr bool
	)

	if elmType.Kind() == reflect.Struct {
		structType = elmType
	} else if elmType.Kind() == reflect.Pointer && elmType.Elem().Kind() == reflect.Struct {
		isStructPtr = true
		structType = elmType.Elem()
	}

	if structType != nil {
		maps := make([]map[string]any, 0, val.Len())
		for i := 0; i < val.Len(); i++ {
			sliceElem := val.Index(i)
			m, err := structToMap(sliceElem)
			if err != nil {
				return reflect.Value{}, err
			}

			maps = append(maps, m)
		}

		result, err := concatMaps(reflect.ValueOf(maps))
		if err != nil {
			return reflect.Value{}, err
		}

		return mapToStruct(result.Interface().(map[string]any), structType, isStructPtr), nil
	}

	var filtered reflect.Value
	for i := 0; i < val.Len(); i++ {
		oneVal := val.Index(i)
		if !oneVal.IsZero() {
			if filtered.IsValid() {
				return reflect.Value{}, fmt.Errorf("cannot concat multiple non-zero value of type %s", elmType)
			}

			filtered = oneVal
		}
	}

	return filtered, nil
}

func structToMap(s reflect.Value) (map[string]any, error) {
	if s.Kind() == reflect.Ptr {
		s = s.Elem()
	}

	ret := make(map[string]any, s.NumField())
	for i := 0; i < s.NumField(); i++ {
		fieldType := s.Type().Field(i)
		if !fieldType.IsExported() {
			return nil, fmt.Errorf("structToMap: field %s is not exported", fieldType.Name)
		}

		ret[fieldType.Name] = s.Field(i).Interface()
	}

	return ret, nil
}

func mapToStruct(m map[string]any, t reflect.Type, toPtr bool) reflect.Value {
	ret := reflect.New(t).Elem()
	for k, v := range m {
		field := ret.FieldByName(k)
		field.Set(reflect.ValueOf(v))
	}

	if toPtr {
		ret = ret.Addr()
	}

	return ret
}

func toSliceValue(vs []any) (reflect.Value, error) {
	typ := reflect.TypeOf(vs[0])

	ret := reflect.MakeSlice(reflect.SliceOf(typ), len(vs), len(vs))
	ret.Index(0).Set(reflect.ValueOf(vs[0]))

	for i := 1; i < len(vs); i++ {
		v := vs[i]
		vt := reflect.TypeOf(v)
		if typ != vt {
			return reflect.Value{}, fmt.Errorf("unexpected slice element type. Got %v, expected %v", typ, vt)
		}

		ret.Index(i).Set(reflect.ValueOf(v))
	}

	return ret, nil
}
