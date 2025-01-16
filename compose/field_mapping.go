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
	"errors"
	"fmt"
	"reflect"

	"github.com/cloudwego/eino/schema"
	"github.com/cloudwego/eino/utils/generic"
)

func takeOne(input any, from string) (any, error) {
	if len(from) == 0 {
		return input, nil
	}

	inputValue := reflect.ValueOf(input)

	f, err := checkAndExtractFromField(from, inputValue)
	if err != nil {
		return nil, err
	}

	return f.Interface(), nil
}

func assignOne[T any](dest T, taken any, to string) (T, error) {
	destValue := reflect.ValueOf(dest)

	if !destValue.CanAddr() {
		destValue = reflect.ValueOf(&dest).Elem()
	}

	if len(to) == 0 { // assign to output directly
		toSet := reflect.ValueOf(taken)
		if !toSet.Type().AssignableTo(destValue.Type()) {
			return dest, fmt.Errorf("mapping entire value has a mismatched type. from=%v, to=%v", toSet.Type(), destValue.Type())
		}

		destValue.Set(toSet)

		return destValue.Interface().(T), nil
	}

	toSet := reflect.ValueOf(taken)

	field, err := checkAndExtractToField(to, destValue, toSet)
	if err != nil {
		return dest, err
	}

	field.Set(toSet)
	return destValue.Interface().(T), nil

}

func convertTo[T any](mappings map[Mapping]any, mustSucceed bool) (T, error) {
	t := generic.NewInstance[T]()

	var (
		err          error
		field2Values = make(map[string][]any)
	)

	for m, taken := range mappings {
		field2Values[m.to] = append(field2Values[m.to], taken)
	}

	for fieldName, values := range field2Values {
		taken := values[0]
		if len(values) > 1 {
			taken, err = mergeValues(values)
			if err != nil {
				return t, fmt.Errorf("convertTo %T failed when merge multiple values for field %s, %w", t, fieldName, err)
			}
		}

		t, err = assignOne(t, taken, fieldName)
		if err != nil {
			if mustSucceed {
				panic(fmt.Errorf("convertTo failed when must succeed, %w", err))
			}
			return t, err
		}
	}

	return t, nil
}

type fieldMapFn func(any) (map[Mapping]any, error)
type streamFieldMapFn func(streamReader) streamReader

func mappingAssign[T any](in map[Mapping]any, mustSucceed bool) (any, error) {
	return convertTo[T](in, mustSucceed)
}

func mappingStreamAssign[T any](in streamReader, mustSucceed bool) streamReader {
	s, ok := unpackStreamReader[map[Mapping]any](in)
	if !ok {
		panic("mappingStreamAssign incoming streamReader chunk type not map[string]any")
	}

	return packStreamReader(schema.StreamReaderWithConvert(s, func(v map[Mapping]any) (T, error) {
		return convertTo[T](v, mustSucceed)
	}))
}

func fieldMap(mappings []*Mapping) fieldMapFn {
	return func(input any) (map[Mapping]any, error) {
		result := make(map[Mapping]any, len(mappings))
		for _, mapping := range mappings {
			taken, err := takeOne(input, mapping.from)
			if err != nil {
				return nil, err
			}

			result[*mapping] = taken
		}

		return result, nil
	}
}

func streamFieldMap(mappings []*Mapping) streamFieldMapFn {
	return func(input streamReader) streamReader {
		return packStreamReader(schema.StreamReaderWithConvert(input.toAnyStreamReader(), fieldMap(mappings)))
	}
}

var anyType = reflect.TypeOf((*any)(nil)).Elem()

func checkAndExtractFromField(fromField string, input reflect.Value) (reflect.Value, error) {
	if input.Kind() == reflect.Ptr {
		input = input.Elem()
	}

	if input.Kind() != reflect.Struct {
		return reflect.Value{}, fmt.Errorf("mapping has from but input is not struct or struct ptr, type= %v", input.Type())
	}

	f := input.FieldByName(fromField)
	if !f.IsValid() {
		return reflect.Value{}, fmt.Errorf("mapping has from not found. field=%v, inputType=%v", fromField, input.Type())
	}

	if !f.CanInterface() {
		return reflect.Value{}, fmt.Errorf("mapping has from not exported. field= %v, inputType=%v", fromField, input.Type())
	}

	return f, nil
}

func checkAndExtractToField(toField string, output, toSet reflect.Value) (reflect.Value, error) {
	if output.Kind() == reflect.Ptr {
		output = output.Elem()
	}

	if output.Kind() != reflect.Struct {
		return reflect.Value{}, fmt.Errorf("mapping has to but output is not a struct, type=%v", output.Type())
	}

	field := output.FieldByName(toField)
	if !field.IsValid() {
		return reflect.Value{}, fmt.Errorf("mapping has to not found. field=%v, outputType=%v", toField, output.Type())
	}

	if !field.CanSet() {
		return reflect.Value{}, fmt.Errorf("mapping has to not exported. field=%v, outputType=%v", toField, output.Type())
	}

	if !toSet.Type().AssignableTo(field.Type()) {
		return reflect.Value{}, fmt.Errorf("mapping to has a mismatched type. field=%s, from=%v, to=%v", toField, toSet.Type(), field.Type())
	}

	return field, nil
}

func checkAndExtractFieldType(field string, typ reflect.Type) (reflect.Type, error) {
	if typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}

	if typ.Kind() != reflect.Struct {
		return nil, fmt.Errorf("type[%v] is not a struct", typ)
	}

	f, ok := typ.FieldByName(field)
	if !ok {
		return nil, fmt.Errorf("type[%v] has no field[%s]", typ, field)
	}

	if !f.IsExported() {
		return nil, fmt.Errorf("type[%v] has an unexported field[%s]", typ.String(), field)
	}

	return f.Type, nil
}

func checkMappingGroup(mappings []*Mapping) error {
	if len(mappings) <= 1 {
		return nil
	}

	var toMap = make(map[string]bool, len(mappings))

	for _, mapping := range mappings {
		if mapping.empty() {
			return errors.New("multiple mappings have an empty mapping")
		}

		if len(mapping.to) == 0 {
			return fmt.Errorf("multiple mappings have a mapping to entire output, mapping= %s", mapping)
		}

		if _, ok := toMap[mapping.to]; ok {
			return fmt.Errorf("multiple mappings have the same To = %s, mappings=%v", mapping.to, mappings)
		}

		toMap[mapping.to] = true
	}

	return nil
}
