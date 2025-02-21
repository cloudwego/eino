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
	"fmt"
	"reflect"
	"runtime/debug"
	"strings"

	"github.com/cloudwego/eino/internal/generic"
	"github.com/cloudwego/eino/internal/safe"
	"github.com/cloudwego/eino/schema"
)

type FieldMapping struct {
	fromNodeKey string
	from        string
	to          string
}

func (m *FieldMapping) empty() bool {
	return len(m.from) == 0 && len(m.to) == 0
}

// String returns the string representation of the FieldMapping.
func (m *FieldMapping) String() string {
	var sb strings.Builder
	sb.WriteString("from ")

	if m.from != "" {
		sb.WriteString(m.from)
		sb.WriteString("(field) of ")
	}

	if m.to != "" {
		sb.WriteString(" to ")
		sb.WriteString(m.to)
		sb.WriteString("(field)")
	}

	sb.WriteString("; ")
	return sb.String()
}

// FromField creates a FieldMapping that maps a single predecessor field to the entire successor input.
// This is an exclusive mapping - once set, no other field mappings can be added since the successor input
// has already been fully mapped.
func FromField(from string) *FieldMapping {
	return &FieldMapping{
		from: from,
	}
}

// ToField creates a FieldMapping that maps the entire predecessor output to a single successor field
func ToField(to string) *FieldMapping {
	return &FieldMapping{
		to: to,
	}
}

// MapFields creates a FieldMapping that maps a single predecessor field to a single successor field
func MapFields(from, to string) *FieldMapping {
	return &FieldMapping{
		from: from,
		to:   to,
	}
}

func buildFieldMappingConverter[I any]() func(input any) (any, error) {
	return func(input any) (any, error) {
		in, ok := input.(map[string]any)
		if !ok {
			panic(newUnexpectedInputTypeErr(reflect.TypeOf(map[string]any{}), reflect.TypeOf(input)))
		}

		return convertTo[I](in)
	}
}

func buildStreamFieldMappingConverter[I any]() func(input streamReader) streamReader {
	return func(input streamReader) streamReader {
		s, ok := unpackStreamReader[map[string]any](input)
		if !ok {
			panic("mappingStreamAssign incoming streamReader chunk type not map[string]any")
		}

		return packStreamReader(schema.StreamReaderWithConvert(s, func(v map[string]any) (I, error) {
			return convertTo[I](v)
		}))
	}
}

func convertTo[T any](mappings map[string]any) (T, error) {
	if _, ok := mappings[""]; ok {
		// to the entire successor input
		return mappings[""].(T), nil
	}

	t := generic.NewInstance[T]()

	var (
		err          error
		field2Values = make(map[string][]any)
	)

	for to, taken := range mappings {
		field2Values[to] = append(field2Values[to], taken)
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
			panic(fmt.Errorf("convertTo failed when must succeed, %w", err))
		}
	}

	return t, nil
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

func checkAndExtractFieldType(field string, typ reflect.Type) (reflect.Type, error) {
	if len(field) == 0 {
		return typ, nil
	}
	for typ.Kind() == reflect.Ptr {
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

func checkAndExtractToField(toField string, output, toSet reflect.Value) (reflect.Value, error) {
	for output.Kind() == reflect.Ptr {
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

func fieldMap(mappings []*FieldMapping) func(any) (map[string]any, error) {
	return func(input any) (map[string]any, error) {
		result := make(map[string]any, len(mappings))
		for _, mapping := range mappings {
			taken, err := takeOne(input, mapping.from)
			if err != nil {
				panic(safe.NewPanicErr(err, debug.Stack()))
			}

			result[mapping.to] = taken
		}

		return result, nil
	}
}

func streamFieldMap(mappings []*FieldMapping) func(streamReader) streamReader {
	return func(input streamReader) streamReader {
		return packStreamReader(schema.StreamReaderWithConvert(input.toAnyStreamReader(), fieldMap(mappings)))
	}
}

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

func isFromAll(mappings []*FieldMapping) bool {
	for _, mapping := range mappings {
		if len(mapping.from) == 0 {
			return true
		}
	}
	return false
}

func isToAll(mappings []*FieldMapping) bool {
	for _, mapping := range mappings {
		if len(mapping.to) == 0 {
			return true
		}
	}
	return false
}

func validateStruct(t reflect.Type) bool {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t.Kind() != reflect.Struct
}

func validateFieldMapping(predecessorType reflect.Type, successorType reflect.Type, mappings []*FieldMapping) (*handlerPair, error) {
	var fieldCheckers = make(map[string]handlerPair)

	// check if mapping is legal
	if isFromAll(mappings) && isToAll(mappings) {
		return nil, fmt.Errorf("invalid field mappings: from all fields to all, use common edge instead")
	} else if !isToAll(mappings) && validateStruct(successorType) {
		// if user has not provided a specific struct type, graph cannot construct any struct in the runtime
		return nil, fmt.Errorf("static check fail: upstream input type should be struct, actual: %v", successorType)
	} else if !isFromAll(mappings) && validateStruct(predecessorType) {
		// TODO: should forbid?
		return nil, fmt.Errorf("static check fail: downstream output type should be struct, actual: %v", predecessorType)
	}

	for _, mapping := range mappings {
		predecessorFieldType, err := checkAndExtractFieldType(mapping.from, predecessorType)
		if err != nil {
			return nil, fmt.Errorf("static check failed for mapping %s: %w", mapping, err)
		}
		successorFieldType, err := checkAndExtractFieldType(mapping.to, successorType)
		if err != nil {
			return nil, fmt.Errorf("static check failed for mapping %s: %w", mapping, err)
		}

		at := checkAssignable(predecessorFieldType, successorFieldType)
		if at == assignableTypeMustNot {
			return nil, fmt.Errorf("static check failed for mapping %s, field[%v]-[%v] must not be assignable", mapping, predecessorFieldType, successorFieldType)
		} else if at == assignableTypeMay {
			checker := func(a any) (any, error) {
				trueInType := reflect.TypeOf(a)
				if !trueInType.AssignableTo(successorFieldType) {
					return nil, fmt.Errorf("runtime check failed for mapping %s, field[%v]-[%v] must not be assignable", mapping, trueInType, successorFieldType)
				}
				return a, nil
			}
			fieldCheckers[mapping.to] = handlerPair{
				invoke: checker,
				transform: func(input streamReader) streamReader {
					return packStreamReader(schema.StreamReaderWithConvert(input.toAnyStreamReader(), checker))
				},
			}
		}
	}

	if len(fieldCheckers) == 0 {
		return nil, nil
	}

	checker := func(value any) (any, error) {
		mValue := value.(map[string]any)
		var err error
		for k, v := range fieldCheckers {
			if _, ok := mValue[k]; ok {
				mValue[k], err = v.invoke(mValue[k])
				if err != nil {
					return nil, err
				}
			}
		}
		return mValue, nil
	}
	return &handlerPair{
		invoke: checker,
		transform: func(input streamReader) streamReader {
			return packStreamReader(schema.StreamReaderWithConvert(input.toAnyStreamReader(), checker))
		},
	}, nil
}
