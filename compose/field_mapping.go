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

	sb.WriteString(m.fromNodeKey)

	if m.to != "" {
		sb.WriteString(" to ")
		sb.WriteString(m.to)
		sb.WriteString("(field)")
	}

	sb.WriteString("; ")
	return sb.String()
}

const pathSeparator = "."

// FromField creates a FieldMapping that maps a single predecessor field to the entire successor input.
// This is an exclusive mapping - once set, no other field mappings can be added since the successor input
// has already been fully mapped.
// Field: either the field of a struct, or the key of a map.
func FromField(from string) *FieldMapping {
	return &FieldMapping{
		from: from,
	}
}

// ToField creates a FieldMapping that maps the entire predecessor output to a single successor field.
// Field: either the field of a struct, or the key of a map.
func ToField(to string) *FieldMapping {
	return &FieldMapping{
		to: to,
	}
}

// MapFields creates a FieldMapping that maps a single predecessor field to a single successor field.
// Field: either the field of a struct, or the key of a map.
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

	var (
		toPaths           = strings.Split(to, pathSeparator)
		originalDestValue = destValue
	)

	for {
		path := toPaths[0]
		toPaths = toPaths[1:]
		if len(toPaths) == 0 {
			toSet := reflect.ValueOf(taken)

			if destValue.Kind() == reflect.Map {
				key, err := checkAndExtractToMapKey(path, destValue, toSet)
				if err != nil {
					return dest, err
				}

				destValue.SetMapIndex(key, toSet)

				return originalDestValue.Interface().(T), nil
			}

			field, err := checkAndExtractToField(path, destValue, toSet)
			if err != nil {
				return dest, err
			}

			field.Set(toSet)

			return originalDestValue.Interface().(T), nil
		}

		if destValue.Kind() == reflect.Map {
			if !reflect.TypeOf(path).AssignableTo(destValue.Type().Key()) {
				return dest, fmt.Errorf("field mapping to a map key but output is not a map with string key, type=%v", destValue.Type())
			}

			keyValue := reflect.ValueOf(path)
			valueValue := destValue.MapIndex(keyValue)
			if !valueValue.IsValid() {
				valueValue = newInstanceByType(destValue.Type().Elem())
				destValue.SetMapIndex(keyValue, valueValue)
			}

			destValue = valueValue

			continue
		}

		for destValue.Kind() == reflect.Ptr {
			destValue = destValue.Elem()
		}

		if destValue.Kind() != reflect.Struct {
			return dest, fmt.Errorf("field mapping to a struct field but output is not a struct, type=%v", destValue.Type())
		}

		field := destValue.FieldByName(path)
		if !field.IsValid() {
			return dest, fmt.Errorf("field mapping to a struct field, but field not found. field=%v, outputType=%v", path, destValue.Type())
		}

		if !field.CanSet() {
			return dest, fmt.Errorf("field mapping to a struct field, but field not exported. field=%v, outputType=%v", path, destValue.Type())
		}

		instantiateIfNeeded(field)

		destValue = field
	}
}

func instantiateIfNeeded(field reflect.Value) {
	if field.Kind() == reflect.Ptr {
		if field.IsNil() {
			field.Set(reflect.New(field.Type().Elem()))
		}
	}
}

func newInstanceByType(typ reflect.Type) reflect.Value {
	switch typ.Kind() {
	case reflect.Map:
		return reflect.MakeMap(typ)
	case reflect.Slice, reflect.Array:
		return reflect.MakeSlice(typ, 0, 0)
	case reflect.Ptr:
		typ = typ.Elem()
		origin := reflect.New(typ)
		inst := origin

		for typ.Kind() == reflect.Ptr {
			typ = typ.Elem()
			inst = inst.Elem()
			inst.Set(reflect.New(typ))
		}

		return origin
	default:
		return reflect.New(typ).Elem()
	}
}

func checkAndExtractFromField(fromField string, input reflect.Value) (reflect.Value, error) {
	f := input.FieldByName(fromField)
	if !f.IsValid() {
		return reflect.Value{}, fmt.Errorf("field mapping from a struct field, but field not found. field=%v, inputType=%v", fromField, input.Type())
	}

	if !f.CanInterface() {
		return reflect.Value{}, fmt.Errorf("field mapping from a struct field, but field not exported. field= %v, inputType=%v", fromField, input.Type())
	}

	return f, nil
}

type errMapKeyNotFound struct {
	mapKey string
}

func (e *errMapKeyNotFound) Error() string {
	return fmt.Sprintf("key=%s", e.mapKey)
}

type errInterfaceNotValidForFieldMapping struct {
	interfaceType reflect.Type
	actualType    reflect.Type
}

func (e *errInterfaceNotValidForFieldMapping) Error() string {
	return fmt.Sprintf("field mapping from an interface type, but actual type is not struct, struct ptr or map. InterfaceType= %v, ActualType= %v", e.interfaceType, e.actualType)
}

func checkAndExtractFromMapKey(fromMapKey string, input reflect.Value) (reflect.Value, error) {
	if !reflect.TypeOf(fromMapKey).AssignableTo(input.Type().Key()) {
		return reflect.Value{}, fmt.Errorf("field mapping from a map key, but input is not a map with string key, type=%v", input.Type())
	}

	v := input.MapIndex(reflect.ValueOf(fromMapKey))
	if !v.IsValid() {
		return reflect.Value{}, fmt.Errorf("field mapping from a map key, but key not found in input. %w", &errMapKeyNotFound{mapKey: fromMapKey})
	}

	return v, nil
}

func checkAndExtractFieldType(paths []string, typ reflect.Type) (extracted reflect.Type, intermediateInterface bool, err error) {
	if len(paths) == 1 && len(paths[0]) == 0 {
		return typ, false, nil
	}

	extracted = typ
	for i, field := range paths {
		if extracted.Kind() == reflect.Map {
			if extracted.Key() != strType {
				return nil, false, fmt.Errorf("type[%v] is not a map with string key", extracted)
			}

			extracted = extracted.Elem()
			continue
		}

		for extracted.Kind() == reflect.Ptr {
			extracted = extracted.Elem()
		}

		if extracted.Kind() == reflect.Struct {
			f, ok := extracted.FieldByName(field)
			if !ok {
				return nil, false, fmt.Errorf("type[%v] has no field[%s]", extracted, field)
			}

			if !f.IsExported() {
				return nil, false, fmt.Errorf("type[%v] has an unexported field[%s]", extracted.String(), field)
			}

			extracted = f.Type
			continue
		}

		if i < len(paths)-1 {
			if extracted.Kind() == reflect.Interface {
				return extracted, true, nil
			}

			return nil, false, fmt.Errorf("intermediate type[%v] is not valid", extracted)
		}
	}

	return extracted, false, nil
}

var strType = reflect.TypeOf("")

func checkAndExtractToField(toField string, output, toSet reflect.Value) (field reflect.Value, err error) {
	originalOutput := output
	if output.Kind() == reflect.Ptr {
		if output.IsNil() {
			originalOutput.Set(reflect.New(output.Type().Elem()))
		}

		output = output.Elem()
	}

	if output.Kind() == reflect.Ptr {
		return reflect.Value{}, fmt.Errorf("field mapping to a struct field, but it's a nested pointer, type= %v", originalOutput.Type())
	}

	if output.Kind() != reflect.Struct {
		return reflect.Value{}, fmt.Errorf("field mapping to a struct field but output is not a struct, type=%v", output.Type())
	}

	field = output.FieldByName(toField)
	if !field.IsValid() {
		return reflect.Value{}, fmt.Errorf("field mapping to a struct field, but field not found. field=%v, outputType=%v", toField, output.Type())
	}

	if !field.CanSet() {
		return reflect.Value{}, fmt.Errorf("field mapping to a struct field, but field not exported. field=%v, outputType=%v", toField, output.Type())
	}

	if !toSet.Type().AssignableTo(field.Type()) {
		return reflect.Value{}, fmt.Errorf("field mapping to a struct field, but field has a mismatched type. field=%s, from=%v, to=%v", toField, toSet.Type(), field.Type())
	}

	return field, nil
}

func checkAndExtractToMapKey(toMapKey string, output, toSet reflect.Value) (key reflect.Value, err error) {
	if output.Kind() != reflect.Map {
		return reflect.Value{}, fmt.Errorf("field mapping to a map key but output is not a map, type=%v", output.Type())
	}

	if !reflect.TypeOf(toMapKey).AssignableTo(output.Type().Key()) {
		return reflect.Value{}, fmt.Errorf("field mapping to a map key but output is not a map with string key, type=%v", output.Type())
	}

	if !toSet.Type().AssignableTo(output.Type().Elem()) {
		return reflect.Value{}, fmt.Errorf("field mapping to a map key but map value has a mismatched type. key=%s, from=%v, to=%v", toMapKey, toSet.Type(), output.Type().Elem())
	}

	if output.IsNil() {
		output.Set(reflect.MakeMap(output.Type()))
	}

	return reflect.ValueOf(toMapKey), nil
}

func fieldMap(mappings []*FieldMapping) func(any) (map[string]any, error) {
	return func(input any) (result map[string]any, err error) {
		result = make(map[string]any, len(mappings))
		var inputValue reflect.Value
		for _, mapping := range mappings {
			if len(mapping.from) == 0 {
				result[mapping.to] = input
				continue
			}

			fromPath := strings.Split(mapping.from, pathSeparator)

			if !inputValue.IsValid() {
				inputValue = reflect.ValueOf(input)
			}

			var (
				pathInputValue = inputValue
				pathInputType  = inputValue.Type()
				taken          = input
			)

			for i, path := range fromPath {
				taken, pathInputType, err = takeOne(pathInputValue, pathInputType, path)
				if err != nil {
					// we deferred check from Compile time to request time for interface types, so we won't panic here
					var interfaceNotValidErr *errInterfaceNotValidForFieldMapping
					if errors.As(err, &interfaceNotValidErr) {
						return nil, err
					}

					// map key not found can only be a request time error, so we won't panic here
					var mapKeyNotFoundErr *errMapKeyNotFound
					if errors.As(err, &mapKeyNotFoundErr) {
						return nil, err
					}

					panic(safe.NewPanicErr(err, debug.Stack()))
				}

				if i < len(fromPath)-1 {
					pathInputValue = reflect.ValueOf(taken)
				}
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

func takeOne(inputValue reflect.Value, inputType reflect.Type, from string) (taken any, takenType reflect.Type, err error) {
	var f reflect.Value
	switch inputValue.Kind() {
	case reflect.Map:
		f, err = checkAndExtractFromMapKey(from, inputValue)
		if err != nil {
			return nil, nil, err
		}

		return f.Interface(), f.Type(), nil
	case reflect.Ptr, reflect.Interface:
		inputValue = inputValue.Elem()
		fallthrough
	case reflect.Struct:
		f, err = checkAndExtractFromField(from, inputValue)
		if err != nil {
			return nil, nil, err
		}

		return f.Interface(), f.Type(), nil
	default:
		if inputType.Kind() == reflect.Interface {
			return nil, nil, &errInterfaceNotValidForFieldMapping{
				interfaceType: inputType,
				actualType:    inputValue.Type(),
			}
		}

		return reflect.Value{}, nil, fmt.Errorf("field mapping from a field, but input is not struct, struct ptr or map, type= %v", inputValue.Type())
	}
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

func validateStructOrMap(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Map:
		return true
	case reflect.Ptr:
		t = t.Elem()
		fallthrough
	case reflect.Struct:
		return true
	default:
		return false
	}
}

func validateFieldMapping(predecessorType reflect.Type, successorType reflect.Type, mappings []*FieldMapping) (*handlerPair, error) {
	var fieldCheckers = make(map[string]handlerPair)

	// check if mapping is legal
	if isFromAll(mappings) && isToAll(mappings) {
		return nil, fmt.Errorf("invalid field mappings: from all fields to all, use common edge instead")
	} else if !isToAll(mappings) && !validateStructOrMap(successorType) {
		// if user has not provided a specific struct type, graph cannot construct any struct in the runtime
		return nil, fmt.Errorf("static check fail: successor input type should be struct or map, actual: %v", successorType)
	} else if !isFromAll(mappings) && !validateStructOrMap(predecessorType) {
		// TODO: should forbid?
		return nil, fmt.Errorf("static check fail: predecessor output type should be struct or map, actual: %v", predecessorType)
	}

	var (
		predecessorFieldType, successorFieldType                         reflect.Type
		err                                                              error
		predecessorIntermediateInterface, successorIntermediateInterface bool
	)

	for _, mapping := range mappings {
		predecessorFieldType, predecessorIntermediateInterface, err = checkAndExtractFieldType(strings.Split(mapping.from, pathSeparator), predecessorType)
		if err != nil {
			return nil, fmt.Errorf("static check failed for mapping %s: %w", mapping, err)
		}

		successorFieldType, successorIntermediateInterface, err = checkAndExtractFieldType(strings.Split(mapping.to, pathSeparator), successorType)
		if err != nil {
			return nil, fmt.Errorf("static check failed for mapping %s: %w", mapping, err)
		}

		if successorIntermediateInterface {
			return nil, fmt.Errorf("static check failed for mapping %s, the successor has intermediate interface type %v", mapping, successorFieldType)
		}

		if predecessorIntermediateInterface {
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
			continue
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
