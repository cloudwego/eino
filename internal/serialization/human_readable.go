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

package serialization

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/bytedance/sonic"
)

const typeFieldName = "$type"

type HumanReadableSerializer struct{}

func (h *HumanReadableSerializer) Marshal(v any) ([]byte, error) {
	result, err := hrMarshal(v, nil)
	if err != nil {
		return nil, err
	}
	return sonic.Marshal(result)
}

func (h *HumanReadableSerializer) Unmarshal(data []byte, v any) error {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		return fmt.Errorf("failed to unmarshal: value must be a non-nil pointer")
	}

	var raw any
	if err := sonic.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("failed to unmarshal JSON: %w", err)
	}

	result, err := hrUnmarshal(raw, rv.Elem().Type())
	if err != nil {
		return fmt.Errorf("failed to unmarshal: %w", err)
	}

	target := rv.Elem()
	if !target.CanSet() {
		return fmt.Errorf("failed to unmarshal: output value must be settable")
	}

	if result == nil {
		target.Set(reflect.Zero(target.Type()))
		return nil
	}

	source := reflect.ValueOf(result)
	if !setValueWithConversion(target, source) {
		return fmt.Errorf("failed to unmarshal: cannot assign %s to %s", reflect.TypeOf(result), target.Type())
	}

	return nil
}

func hrMarshal(v any, fieldType reflect.Type) (any, error) {
	if v == nil {
		return nil, nil
	}

	rv := reflect.ValueOf(v)
	if !rv.IsValid() {
		return nil, nil
	}

	if rv.Kind() == reflect.Invalid {
		return nil, nil
	}

	if rv.IsZero() && fieldType != nil && fieldType.Kind() != reflect.Interface {
		return nil, nil
	}

	rt := rv.Type()
	typeUnspecific := fieldType == nil || fieldType.Kind() == reflect.Interface

	var pointerNum uint32
	for rt.Kind() == reflect.Ptr {
		pointerNum++
		if rv.IsNil() {
			return nil, nil
		}
		rv = rv.Elem()
		rt = rt.Elem()
	}

	switch rt.Kind() {
	case reflect.Struct:
		return hrMarshalStruct(rv, rt, typeUnspecific, pointerNum)
	case reflect.Map:
		return hrMarshalMap(rv, rt, typeUnspecific, pointerNum)
	case reflect.Slice, reflect.Array:
		return hrMarshalSlice(rv, rt, typeUnspecific, pointerNum)
	default:
		return hrMarshalPrimitive(rv, rt, typeUnspecific, pointerNum)
	}
}

func hrMarshalStruct(rv reflect.Value, rt reflect.Type, typeUnspecific bool, pointerNum uint32) (any, error) {
	if checkMarshaler(rt) {
		jsonBytes, err := json.Marshal(rv.Interface())
		if err != nil {
			return nil, err
		}
		var result any
		if err := sonic.Unmarshal(jsonBytes, &result); err != nil {
			return nil, err
		}
		if typeUnspecific {
			_, isMap := result.(map[string]any)
			return wrapWithType(result, rt, pointerNum, !isMap)
		}
		return result, nil
	}

	result := make(map[string]any)

	for i := 0; i < rt.NumField(); i++ {
		field := rt.Field(i)
		if field.PkgPath != "" {
			continue
		}

		fieldValue := rv.Field(i)
		jsonTag := field.Tag.Get("json")
		fieldName := getJSONFieldName(field.Name, jsonTag)

		if fieldName == "-" {
			continue
		}

		if hasOmitempty(jsonTag) && isEmptyValue(fieldValue) {
			continue
		}

		marshaledValue, err := hrMarshal(fieldValue.Interface(), field.Type)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal field %s: %w", field.Name, err)
		}

		if marshaledValue != nil || !hasOmitempty(jsonTag) {
			result[fieldName] = marshaledValue
		}
	}

	if typeUnspecific {
		return wrapWithType(result, rt, pointerNum, false)
	}

	return result, nil
}

func hrMarshalMap(rv reflect.Value, rt reflect.Type, typeUnspecific bool, pointerNum uint32) (any, error) {
	if rv.IsNil() {
		return nil, nil
	}

	result := make(map[string]any)
	iter := rv.MapRange()

	for iter.Next() {
		k := iter.Key()
		v := iter.Value()

		var keyStr string
		if k.Kind() == reflect.String {
			keyStr = k.String()
		} else {
			keyBytes, err := sonic.Marshal(k.Interface())
			if err != nil {
				return nil, fmt.Errorf("failed to marshal map key: %w", err)
			}
			keyStr = string(keyBytes)
		}

		marshaledValue, err := hrMarshal(v.Interface(), rt.Elem())
		if err != nil {
			return nil, fmt.Errorf("failed to marshal map value for key %s: %w", keyStr, err)
		}

		result[keyStr] = marshaledValue
	}

	if typeUnspecific {
		return wrapMapWithType(result, rt, pointerNum)
	}

	return result, nil
}

func hrMarshalSlice(rv reflect.Value, rt reflect.Type, typeUnspecific bool, pointerNum uint32) (any, error) {
	if rv.Kind() == reflect.Slice && rv.IsNil() {
		return nil, nil
	}

	length := rv.Len()
	result := make([]any, length)

	for i := 0; i < length; i++ {
		elem := rv.Index(i)
		marshaledElem, err := hrMarshal(elem.Interface(), rt.Elem())
		if err != nil {
			return nil, fmt.Errorf("failed to marshal slice element %d: %w", i, err)
		}
		result[i] = marshaledElem
	}

	if typeUnspecific {
		return wrapSliceWithType(result, rt, pointerNum)
	}

	return result, nil
}

func hrMarshalPrimitive(rv reflect.Value, rt reflect.Type, typeUnspecific bool, pointerNum uint32) (any, error) {
	if !typeUnspecific {
		return rv.Interface(), nil
	}

	if isPrimitiveJSONType(rt) && pointerNum == 0 {
		return rv.Interface(), nil
	}

	return wrapWithType(rv.Interface(), rt, pointerNum, true)
}

func wrapWithType(value any, rt reflect.Type, pointerNum uint32, isSimple bool) (any, error) {
	key, ok := rm[rt]
	if !ok {
		return nil, fmt.Errorf("unknown type: %v (not registered)", rt)
	}

	typeName := key
	if pointerNum > 0 {
		typeName = strings.Repeat("*", int(pointerNum)) + key
	}

	if isSimple {
		return map[string]any{
			typeFieldName: typeName,
			"value":       value,
		}, nil
	}

	if m, ok := value.(map[string]any); ok {
		m[typeFieldName] = typeName
		return m, nil
	}

	return map[string]any{
		typeFieldName: typeName,
		"value":       value,
	}, nil
}

func wrapMapWithType(value map[string]any, rt reflect.Type, pointerNum uint32) (any, error) {
	keyType := rt.Key()
	elemType := rt.Elem()

	keyTypeName, err := getTypeName(keyType)
	if err != nil {
		return nil, err
	}
	elemTypeName, err := getTypeName(elemType)
	if err != nil {
		return nil, err
	}

	typeName := fmt.Sprintf("map[%s]%s", keyTypeName, elemTypeName)
	if pointerNum > 0 {
		typeName = strings.Repeat("*", int(pointerNum)) + typeName
	}

	value[typeFieldName] = typeName
	return value, nil
}

func wrapSliceWithType(value []any, rt reflect.Type, pointerNum uint32) (any, error) {
	elemType := rt.Elem()
	elemTypeName, err := getTypeName(elemType)
	if err != nil {
		return nil, err
	}

	typeName := fmt.Sprintf("[]%s", elemTypeName)
	if pointerNum > 0 {
		typeName = strings.Repeat("*", int(pointerNum)) + typeName
	}

	return map[string]any{
		typeFieldName: typeName,
		"value":       value,
	}, nil
}

func getTypeName(t reflect.Type) (string, error) {
	var pointerPrefix string
	for t.Kind() == reflect.Ptr {
		pointerPrefix += "*"
		t = t.Elem()
	}

	if t.Kind() == reflect.Map {
		keyName, err := getTypeName(t.Key())
		if err != nil {
			return "", err
		}
		elemName, err := getTypeName(t.Elem())
		if err != nil {
			return "", err
		}
		return pointerPrefix + fmt.Sprintf("map[%s]%s", keyName, elemName), nil
	}

	if t.Kind() == reflect.Slice {
		elemName, err := getTypeName(t.Elem())
		if err != nil {
			return "", err
		}
		return pointerPrefix + fmt.Sprintf("[]%s", elemName), nil
	}

	if t.Kind() == reflect.Array {
		elemName, err := getTypeName(t.Elem())
		if err != nil {
			return "", err
		}
		return pointerPrefix + fmt.Sprintf("[%d]%s", t.Len(), elemName), nil
	}

	key, ok := rm[t]
	if !ok {
		return "", fmt.Errorf("unknown type: %v", t)
	}
	return pointerPrefix + key, nil
}

func hrUnmarshal(data any, targetType reflect.Type) (any, error) {
	if data == nil {
		return nil, nil
	}

	ptrNum, baseType := derefPointerNum(targetType)

	switch v := data.(type) {
	case map[string]any:
		return hrUnmarshalMap(v, targetType, baseType, ptrNum)
	case []any:
		return hrUnmarshalSlice(v, targetType, baseType, ptrNum)
	default:
		return hrUnmarshalPrimitive(data, targetType, baseType, ptrNum)
	}
}

func hrUnmarshalMap(data map[string]any, targetType, baseType reflect.Type, ptrNum uint32) (any, error) {
	if typeStr, hasType := data[typeFieldName].(string); hasType {
		return hrUnmarshalTyped(data, typeStr)
	}

	if baseType.Kind() == reflect.Struct {
		return hrUnmarshalStruct(data, targetType, baseType, ptrNum)
	}

	if baseType.Kind() == reflect.Map {
		return hrUnmarshalMapValue(data, targetType, baseType, ptrNum)
	}

	if baseType.Kind() == reflect.Interface {
		result := make(map[string]any)
		for k, v := range data {
			unmarshaled, err := hrUnmarshal(v, reflect.TypeOf((*any)(nil)).Elem())
			if err != nil {
				return nil, err
			}
			result[k] = unmarshaled
		}
		return result, nil
	}

	return nil, fmt.Errorf("cannot unmarshal map to %v", targetType)
}

func hrUnmarshalTyped(data map[string]any, typeStr string) (any, error) {
	actualType, ptrNum, err := parseTypeName(typeStr)
	if err != nil {
		return nil, err
	}

	value, hasValue := data["value"]
	if hasValue && len(data) == 2 {
		result, err := hrUnmarshal(value, actualType)
		if err != nil {
			return nil, err
		}
		return wrapPointers(result, ptrNum), nil
	}

	dataCopy := make(map[string]any)
	for k, v := range data {
		if k != typeFieldName {
			dataCopy[k] = v
		}
	}

	result, err := hrUnmarshal(dataCopy, actualType)
	if err != nil {
		return nil, err
	}
	return wrapPointers(result, ptrNum), nil
}

func hrUnmarshalStruct(data map[string]any, targetType, baseType reflect.Type, ptrNum uint32) (any, error) {
	if checkMarshaler(baseType) {
		jsonBytes, err := sonic.Marshal(data)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal data for custom unmarshaler: %w", err)
		}
		result := reflect.New(baseType)
		if err := json.Unmarshal(jsonBytes, result.Interface()); err != nil {
			return nil, fmt.Errorf("failed to unmarshal with custom unmarshaler: %w", err)
		}
		return wrapPointers(result.Elem().Interface(), ptrNum), nil
	}

	result, dResult := createValueFromType(targetType)

	for i := 0; i < baseType.NumField(); i++ {
		field := baseType.Field(i)
		if field.PkgPath != "" {
			continue
		}

		jsonTag := field.Tag.Get("json")
		fieldName := getJSONFieldName(field.Name, jsonTag)
		if fieldName == "-" {
			continue
		}

		fieldData, ok := data[fieldName]
		if !ok {
			fieldData, ok = data[field.Name]
		}
		if !ok {
			continue
		}

		fieldValue, err := hrUnmarshal(fieldData, field.Type)
		if err != nil {
			return nil, fmt.Errorf("failed to unmarshal field %s: %w", field.Name, err)
		}

		if fieldValue != nil {
			fieldRef := dResult.FieldByName(field.Name)
			if fieldRef.CanSet() {
				if !setValueWithConversion(fieldRef, reflect.ValueOf(fieldValue)) {
					return nil, fmt.Errorf("cannot set field %s: type mismatch", field.Name)
				}
			}
		}
	}

	return result.Interface(), nil
}

func hrUnmarshalMapValue(data map[string]any, targetType, baseType reflect.Type, ptrNum uint32) (any, error) {
	result, dResult := createValueFromType(targetType)

	keyType := baseType.Key()
	elemType := baseType.Elem()

	for k, v := range data {
		var keyValue reflect.Value
		if keyType.Kind() == reflect.String {
			keyValue = reflect.ValueOf(k)
		} else {
			keyPtr := reflect.New(keyType)
			if err := sonic.UnmarshalString(k, keyPtr.Interface()); err != nil {
				return nil, fmt.Errorf("failed to unmarshal map key %s: %w", k, err)
			}
			keyValue = keyPtr.Elem()
		}

		elemValue, err := hrUnmarshal(v, elemType)
		if err != nil {
			return nil, fmt.Errorf("failed to unmarshal map value for key %s: %w", k, err)
		}

		if elemValue == nil {
			dResult.SetMapIndex(keyValue, reflect.Zero(elemType))
		} else {
			dResult.SetMapIndex(keyValue, reflect.ValueOf(elemValue))
		}
	}

	return result.Interface(), nil
}

func hrUnmarshalSlice(data []any, targetType, baseType reflect.Type, ptrNum uint32) (any, error) {
	if baseType.Kind() == reflect.Interface {
		result := make([]any, len(data))
		for i, elem := range data {
			unmarshaled, err := hrUnmarshal(elem, reflect.TypeOf((*any)(nil)).Elem())
			if err != nil {
				return nil, err
			}
			result[i] = unmarshaled
		}
		return result, nil
	}

	if baseType.Kind() != reflect.Slice && baseType.Kind() != reflect.Array {
		return nil, fmt.Errorf("cannot unmarshal slice to %v", targetType)
	}

	elemType := baseType.Elem()
	result, dResult := createValueFromType(targetType)

	if baseType.Kind() == reflect.Array {
		for i, elem := range data {
			if i >= dResult.Len() {
				break
			}
			elemValue, err := hrUnmarshal(elem, elemType)
			if err != nil {
				return nil, fmt.Errorf("failed to unmarshal array element %d: %w", i, err)
			}
			if elemValue == nil {
				dResult.Index(i).Set(reflect.Zero(elemType))
			} else {
				dResult.Index(i).Set(reflect.ValueOf(elemValue))
			}
		}
	} else {
		for i, elem := range data {
			elemValue, err := hrUnmarshal(elem, elemType)
			if err != nil {
				return nil, fmt.Errorf("failed to unmarshal slice element %d: %w", i, err)
			}
			if elemValue == nil {
				dResult.Set(reflect.Append(dResult, reflect.Zero(elemType)))
			} else {
				dResult.Set(reflect.Append(dResult, reflect.ValueOf(elemValue)))
			}
		}
	}

	return result.Interface(), nil
}

func hrUnmarshalPrimitive(data any, targetType, baseType reflect.Type, ptrNum uint32) (any, error) {
	if baseType.Kind() == reflect.Interface {
		return convertJSONPrimitive(data), nil
	}

	dataValue := reflect.ValueOf(data)
	if dataValue.Type().AssignableTo(baseType) {
		return wrapPointers(data, ptrNum), nil
	}

	if dataValue.Type().ConvertibleTo(baseType) {
		converted := dataValue.Convert(baseType)
		return wrapPointers(converted.Interface(), ptrNum), nil
	}

	if baseType.Kind() == reflect.Int || baseType.Kind() == reflect.Int8 ||
		baseType.Kind() == reflect.Int16 || baseType.Kind() == reflect.Int32 ||
		baseType.Kind() == reflect.Int64 {
		if f, ok := data.(float64); ok {
			result := reflect.New(baseType).Elem()
			result.SetInt(int64(f))
			return wrapPointers(result.Interface(), ptrNum), nil
		}
	}

	if baseType.Kind() == reflect.Uint || baseType.Kind() == reflect.Uint8 ||
		baseType.Kind() == reflect.Uint16 || baseType.Kind() == reflect.Uint32 ||
		baseType.Kind() == reflect.Uint64 {
		if f, ok := data.(float64); ok {
			result := reflect.New(baseType).Elem()
			result.SetUint(uint64(f))
			return wrapPointers(result.Interface(), ptrNum), nil
		}
	}

	if baseType.Kind() == reflect.Float32 {
		if f, ok := data.(float64); ok {
			return wrapPointers(float32(f), ptrNum), nil
		}
	}

	jsonBytes, err := sonic.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("failed to re-marshal data: %w", err)
	}

	result := reflect.New(baseType)
	if err := sonic.Unmarshal(jsonBytes, result.Interface()); err != nil {
		return nil, fmt.Errorf("failed to unmarshal to %v: %w", baseType, err)
	}

	return wrapPointers(result.Elem().Interface(), ptrNum), nil
}

func parseTypeName(typeStr string) (reflect.Type, uint32, error) {
	var ptrNum uint32
	for strings.HasPrefix(typeStr, "*") {
		ptrNum++
		typeStr = typeStr[1:]
	}

	if strings.HasPrefix(typeStr, "map[") {
		return parseMapType(typeStr, ptrNum)
	}

	if strings.HasPrefix(typeStr, "[]") {
		return parseSliceType(typeStr, ptrNum)
	}

	if strings.HasPrefix(typeStr, "[") {
		return parseArrayType(typeStr, ptrNum)
	}

	rt, ok := m[typeStr]
	if !ok {
		return nil, 0, fmt.Errorf("unknown type: %s", typeStr)
	}

	return rt, ptrNum, nil
}

func parseMapType(typeStr string, ptrNum uint32) (reflect.Type, uint32, error) {
	inner := typeStr[4:]
	bracketCount := 1
	keyEnd := 0
	for i, c := range inner {
		if c == '[' {
			bracketCount++
		} else if c == ']' {
			bracketCount--
			if bracketCount == 0 {
				keyEnd = i
				break
			}
		}
	}

	keyTypeStr := inner[:keyEnd]
	valueTypeStr := inner[keyEnd+1:]

	keyType, keyPtrNum, err := parseTypeName(keyTypeStr)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to parse map key type: %w", err)
	}

	finalKeyType := keyType
	for i := uint32(0); i < keyPtrNum; i++ {
		finalKeyType = reflect.PointerTo(finalKeyType)
	}

	valueType, valuePtrNum, err := parseTypeName(valueTypeStr)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to parse map value type: %w", err)
	}

	finalValueType := valueType
	for i := uint32(0); i < valuePtrNum; i++ {
		finalValueType = reflect.PointerTo(finalValueType)
	}

	return reflect.MapOf(finalKeyType, finalValueType), ptrNum, nil
}

func parseSliceType(typeStr string, ptrNum uint32) (reflect.Type, uint32, error) {
	elemTypeStr := typeStr[2:]
	elemType, elemPtrNum, err := parseTypeName(elemTypeStr)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to parse slice element type: %w", err)
	}

	finalElemType := elemType
	for i := uint32(0); i < elemPtrNum; i++ {
		finalElemType = reflect.PointerTo(finalElemType)
	}

	return reflect.SliceOf(finalElemType), ptrNum, nil
}

func parseArrayType(typeStr string, ptrNum uint32) (reflect.Type, uint32, error) {
	closeBracket := strings.Index(typeStr, "]")
	if closeBracket == -1 {
		return nil, 0, fmt.Errorf("invalid array type: %s", typeStr)
	}

	sizeStr := typeStr[1:closeBracket]
	var size int
	if _, err := fmt.Sscanf(sizeStr, "%d", &size); err != nil {
		return nil, 0, fmt.Errorf("invalid array size: %s", sizeStr)
	}

	elemTypeStr := typeStr[closeBracket+1:]
	elemType, elemPtrNum, err := parseTypeName(elemTypeStr)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to parse array element type: %w", err)
	}

	finalElemType := elemType
	for i := uint32(0); i < elemPtrNum; i++ {
		finalElemType = reflect.PointerTo(finalElemType)
	}

	return reflect.ArrayOf(size, finalElemType), ptrNum, nil
}

func wrapPointers(value any, ptrNum uint32) any {
	if ptrNum == 0 || value == nil {
		return value
	}

	rv := reflect.ValueOf(value)
	for i := uint32(0); i < ptrNum; i++ {
		ptr := reflect.New(rv.Type())
		ptr.Elem().Set(rv)
		rv = ptr
	}
	return rv.Interface()
}

func convertJSONPrimitive(data any) any {
	switch v := data.(type) {
	case float64:
		if v == float64(int64(v)) {
			return int(v)
		}
		return v
	default:
		return data
	}
}

func setValueWithConversion(target, source reflect.Value) bool {
	if !source.IsValid() {
		target.Set(reflect.Zero(target.Type()))
		return true
	}

	if source.Type().AssignableTo(target.Type()) {
		target.Set(source)
		return true
	}

	if target.Kind() == reflect.Ptr {
		if target.IsNil() && target.CanSet() {
			target.Set(reflect.New(target.Type().Elem()))
		}
		return setValueWithConversion(target.Elem(), source)
	}

	if source.Kind() == reflect.Ptr {
		if source.IsNil() {
			target.Set(reflect.Zero(target.Type()))
			return true
		}
		return setValueWithConversion(target, source.Elem())
	}

	if source.Type().ConvertibleTo(target.Type()) {
		target.Set(source.Convert(target.Type()))
		return true
	}

	if target.Kind() == reflect.Int || target.Kind() == reflect.Int8 ||
		target.Kind() == reflect.Int16 || target.Kind() == reflect.Int32 ||
		target.Kind() == reflect.Int64 {
		if source.Kind() == reflect.Float64 {
			target.SetInt(int64(source.Float()))
			return true
		}
		if source.Kind() == reflect.Int {
			target.SetInt(int64(source.Int()))
			return true
		}
	}

	if target.Kind() == reflect.Uint || target.Kind() == reflect.Uint8 ||
		target.Kind() == reflect.Uint16 || target.Kind() == reflect.Uint32 ||
		target.Kind() == reflect.Uint64 {
		if source.Kind() == reflect.Float64 {
			target.SetUint(uint64(source.Float()))
			return true
		}
	}

	if target.Kind() == reflect.Float32 || target.Kind() == reflect.Float64 {
		if source.Kind() == reflect.Float64 {
			target.SetFloat(source.Float())
			return true
		}
		if source.Kind() == reflect.Int {
			target.SetFloat(float64(source.Int()))
			return true
		}
	}

	return false
}

func getJSONFieldName(fieldName, jsonTag string) string {
	if jsonTag == "" {
		return fieldName
	}
	parts := strings.Split(jsonTag, ",")
	if parts[0] == "" {
		return fieldName
	}
	return parts[0]
}

func hasOmitempty(jsonTag string) bool {
	return strings.Contains(jsonTag, "omitempty")
}

func isEmptyValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	case reflect.Bool:
		return !v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return v.Float() == 0
	case reflect.Interface, reflect.Ptr:
		return v.IsNil()
	}
	return false
}

func isPrimitiveJSONType(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Bool, reflect.String,
		reflect.Float32, reflect.Float64:
		return true
	default:
		return false
	}
}
