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

package serialization

import (
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/bytedance/sonic"

	"github.com/cloudwego/eino/schema"
)

var m = map[string]reflect.Type{}
var rm = map[reflect.Type]string{}

func init() {
	_ = GenericRegister[int]("_eino_int")
	_ = GenericRegister[int8]("_eino_int8")
	_ = GenericRegister[int16]("_eino_int16")
	_ = GenericRegister[int32]("_eino_int32")
	_ = GenericRegister[int64]("_eino_int64")
	_ = GenericRegister[uint]("_eino_uint")
	_ = GenericRegister[uint8]("_eino_uint8")
	_ = GenericRegister[uint16]("_eino_uint16")
	_ = GenericRegister[uint32]("_eino_uint32")
	_ = GenericRegister[uint64]("_eino_uint64")
	_ = GenericRegister[float32]("_eino_float32")
	_ = GenericRegister[float64]("_eino_float64")
	_ = GenericRegister[complex64]("_eino_complex64")
	_ = GenericRegister[complex128]("_eino_complex128")
	_ = GenericRegister[uintptr]("_eino_uintptr")
	_ = GenericRegister[bool]("_eino_bool")
	_ = GenericRegister[string]("_eino_string")
	_ = GenericRegister[any]("_eino_any")

	_ = GenericRegister[schema.Message]("_eino_message")
	_ = GenericRegister[schema.Document]("_eino_document")
	_ = GenericRegister[schema.RoleType]("_eino_role_type")
	_ = GenericRegister[schema.ToolCall]("_eino_tool_call")
	_ = GenericRegister[schema.FunctionCall]("_eino_function_call")
	_ = GenericRegister[schema.ResponseMeta]("_eino_response_meta")
	_ = GenericRegister[schema.TokenUsage]("_eino_token_usage")
	_ = GenericRegister[schema.LogProbs]("_eino_log_probs")
	_ = GenericRegister[schema.ChatMessagePart]("_eino_chat_message_part")
	_ = GenericRegister[schema.ChatMessagePartType]("_eino_chat_message_type")
	_ = GenericRegister[schema.ChatMessageImageURL]("_eino_chat_message_image_url")
	_ = GenericRegister[schema.ChatMessageAudioURL]("_eino_chat_message_audio_url")
	_ = GenericRegister[schema.ChatMessageVideoURL]("_eino_chat_message_video_url")
	_ = GenericRegister[schema.ChatMessageFileURL]("_eino_chat_message_file_url")
	_ = GenericRegister[schema.ImageURLDetail]("_eino_image_url_detail")
	_ = GenericRegister[schema.PromptTokenDetails]("_eino_prompt_token_details")
}

func GenericRegister[T any](key string) error {
	t := reflect.TypeOf((*T)(nil)).Elem()
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if nt, ok := m[key]; ok {
		return fmt.Errorf("key[%s] already registered to %s", key, nt.String())
	}
	if nk, ok := rm[t]; ok {
		return fmt.Errorf("type[%s] already registered to %s", t.String(), nk)
	}
	m[key] = t
	rm[t] = key
	return nil
}

type InternalSerializer struct{}

func (i *InternalSerializer) Marshal(v interface{}) ([]byte, error) {
	is, err := internalMarshal(v, nil)
	if err != nil {
		return nil, err
	}

	return sonic.Marshal(is)
}

func (i *InternalSerializer) Unmarshal(data []byte, v any) error {
	val, err := unmarshal(data, reflect.TypeOf(v))
	if err != nil {
		return fmt.Errorf("failed to unmarshal: %w", err)
	}

	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		return fmt.Errorf("failed to unmarshal: value must be a non-nil pointer")
	}

	target := rv.Elem()
	if !target.CanSet() {
		return fmt.Errorf("failed to unmarshal: output value must be settable")
	}

	if val == nil {
		target.Set(reflect.Zero(target.Type()))
		return nil
	}

	source := reflect.ValueOf(val)

	var set func(target, source reflect.Value) bool
	set = func(target, source reflect.Value) bool {
		if !source.IsValid() {
			target.Set(reflect.Zero(target.Type()))
			return true
		}
		if source.Type().AssignableTo(target.Type()) {
			target.Set(source)
			return true
		}

		if target.Kind() == reflect.Ptr {
			if target.IsNil() {
				if !target.CanSet() {
					return false
				}
				target.Set(reflect.New(target.Type().Elem()))
			}
			return set(target.Elem(), source)
		}

		if source.Kind() == reflect.Ptr {
			if source.IsNil() {
				target.Set(reflect.Zero(target.Type()))
				return true
			}
			return set(target, source.Elem())
		}

		if source.Type().ConvertibleTo(target.Type()) {
			target.Set(source.Convert(target.Type()))
			return true
		}

		return false
	}

	if set(target, source) {
		return nil
	}

	return fmt.Errorf("failed to unmarshal: cannot assign %s to %s", reflect.TypeOf(val), target.Type())
}

func unmarshal(data []byte, t reflect.Type) (any, error) {
	is := &internalStruct{}
	err := sonic.Unmarshal(data, is)
	if err != nil {
		return nil, err
	}
	return internalUnmarshal(is, t)
}

type internalStruct struct {
	Type *valueType `json:",omitempty"`

	JSONValue json.RawMessage `json:",omitempty"`

	// map or struct
	// in map, the key is the serialized map key anyway todo: if key is string, don't serialize
	// in struct, the key is the original field name
	MapValues map[string]*internalStruct `json:",omitempty"`

	// slice
	SliceValues []*internalStruct `json:",omitempty"`
}

type valueType struct {
	PointerNum uint32 `json:",omitempty"`

	SimpleType string `json:",omitempty"`

	StructType string `json:",omitempty"`

	MapKeyType   *valueType `json:",omitempty"`
	MapValueType *valueType `json:",omitempty"`

	SliceValueType *valueType `json:",omitempty"`
}

func extractType(t reflect.Type) (*valueType, error) {
	ret := &valueType{}
	for t.Kind() == reflect.Ptr {
		ret.PointerNum += 1
		t = t.Elem()
	}
	var err error
	if t.Kind() == reflect.Map {
		ret.MapKeyType, err = extractType(t.Key())
		if err != nil {
			return nil, err
		}
		ret.MapValueType, err = extractType(t.Elem())
		if err != nil {
			return nil, err
		}
	} else if t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
		ret.SliceValueType, err = extractType(t.Elem())
		if err != nil {
			return nil, err
		}
	} else {
		key, ok := rm[t]
		if !ok {
			return ret, fmt.Errorf("unknown type: %s", t.String())
		}
		ret.SimpleType = key
	}
	return ret, nil
}

func restoreType(vt *valueType) (reflect.Type, error) {
	if vt.SimpleType != "" {
		rt, ok := m[vt.SimpleType]
		if !ok {
			return nil, fmt.Errorf("unknown type: %s", vt.SimpleType)
		}
		return resolvePointerNum(vt.PointerNum, rt), nil
	}
	if vt.StructType != "" {
		rt, ok := m[vt.StructType]
		if !ok {
			return nil, fmt.Errorf("unknown type: %s", vt.StructType)
		}
		return resolvePointerNum(vt.PointerNum, rt), nil
	}
	if vt.MapKeyType != nil {
		rkt, err := restoreType(vt.MapKeyType)
		if err != nil {
			return nil, err
		}
		rvt, err := restoreType(vt.MapValueType)
		if err != nil {
			return nil, err
		}
		return resolvePointerNum(vt.PointerNum, reflect.MapOf(rkt, rvt)), nil
	}
	if vt.SliceValueType != nil {
		rt, err := restoreType(vt.SliceValueType)
		if err != nil {
			return nil, err
		}
		return resolvePointerNum(vt.PointerNum, reflect.SliceOf(rt)), nil
	}
	return nil, fmt.Errorf("empty value")
}

func internalMarshal(v any, fieldType reflect.Type) (*internalStruct, error) {
	if v == nil ||
		(reflect.ValueOf(v).IsZero() && fieldType != nil && fieldType.Kind() != reflect.Interface) {
		return nil, nil
	}

	ret := &internalStruct{}
	rv := reflect.ValueOf(v)
	rt := rv.Type()
	typeUnspecific := fieldType == nil || fieldType.Kind() == reflect.Interface

	var pointerNum uint32
	for rt.Kind() == reflect.Ptr {
		pointerNum++
		if !rv.IsNil() {
			rv = rv.Elem()
			rt = rt.Elem()
			continue
		}
		for rt.Kind() == reflect.Ptr {
			rt = rt.Elem()
		}
		if typeUnspecific {
			// need type registered
			key, ok := rm[rt]
			if !ok {
				return nil, fmt.Errorf("unknown type: %v", rt)
			}
			ret.Type = &valueType{
				PointerNum: pointerNum,
				SimpleType: key,
			}
		}
		ret.JSONValue = json.RawMessage("null")
		return ret, nil
	}

	switch rt.Kind() {
	case reflect.Struct:
		if typeUnspecific {
			// need type registered
			key, ok := rm[rt]
			if !ok {
				return nil, fmt.Errorf("unknown type: %v", rt)
			}

			if checkMarshaler(rt) {
				ret.Type = &valueType{
					PointerNum: pointerNum,
					SimpleType: key,
				}
			} else {
				ret.Type = &valueType{
					PointerNum: pointerNum,
					StructType: key,
				}
			}
		}

		if checkMarshaler(rt) {
			jsonBytes, err := json.Marshal(rv.Interface())
			if err != nil {
				return nil, err
			}
			ret.JSONValue = jsonBytes
			return ret, nil
		}

		ret.MapValues = make(map[string]*internalStruct)

		for i := 0; i < rt.NumField(); i++ {
			field := rt.Field(i)
			// only handle exported fields
			if field.PkgPath == "" {
				k := field.Name
				v := rv.Field(i)

				internalValue, err := internalMarshal(v.Interface(), field.Type)
				if err != nil {
					return nil, err
				}

				ret.MapValues[k] = internalValue
			}
		}

		return ret, nil
	case reflect.Map:
		if typeUnspecific {
			var err error
			ret.Type = &valueType{
				PointerNum: pointerNum,
			}
			// map key type
			ret.Type.MapKeyType, err = extractType(rt.Key())
			if err != nil {
				return nil, err
			}

			// map value type
			ret.Type.MapValueType, err = extractType(rt.Elem())
			if err != nil {
				return nil, err
			}
		}

		ret.MapValues = make(map[string]*internalStruct)

		iter := rv.MapRange()
		for iter.Next() {
			k := iter.Key()
			v := iter.Value()

			internalValue, err := internalMarshal(v.Interface(), rt.Elem())
			if err != nil {
				return nil, err
			}

			keyStr, err := sonic.MarshalString(k.Interface())
			if err != nil {
				return nil, fmt.Errorf("marshaling map key[%v] fail: %v", k.Interface(), err)
			}
			ret.MapValues[keyStr] = internalValue
		}

		return ret, nil
	case reflect.Slice, reflect.Array:
		if typeUnspecific {
			var err error
			ret.Type = &valueType{PointerNum: pointerNum}
			ret.Type.SliceValueType, err = extractType(rt.Elem())
			if err != nil {
				return nil, err
			}
		}

		length := rv.Len()
		ret.SliceValues = make([]*internalStruct, length)

		for i := 0; i < length; i++ {
			internalValue, err := internalMarshal(rv.Index(i).Interface(), rt.Elem())
			if err != nil {
				return nil, err
			}
			ret.SliceValues[i] = internalValue
		}

		return ret, nil

	default:
		if typeUnspecific {
			key, ok := rm[rv.Type()]
			if !ok {
				return nil, fmt.Errorf("unknown type: %v", rt)
			}
			ret.Type = &valueType{
				PointerNum: pointerNum,
				SimpleType: key,
			}
		}

		jsonBytes, err := json.Marshal(rv.Interface())
		if err != nil {
			return nil, err
		}
		ret.JSONValue = jsonBytes
		return ret, nil
	}
}

func internalUnmarshal(v *internalStruct, typ reflect.Type) (any, error) {
	if v == nil {
		return nil, nil
	}

	if v.Type == nil {
		// specific type
		if checkMarshaler(typ) {
			pv := reflect.New(typ)
			err := json.Unmarshal(v.JSONValue, pv.Interface())
			if err != nil {
				return nil, err
			}
			return pv.Elem().Interface(), nil
		}
		return internalSpecificTypeUnmarshal(v, typ)
	}

	if len(v.Type.SimpleType) != 0 {
		// based type
		t, ok := m[v.Type.SimpleType]
		if !ok {
			return nil, fmt.Errorf("unknown type key: %v", v.Type)
		}
		pResult := reflect.New(resolvePointerNum(v.Type.PointerNum, t))
		err := sonic.Unmarshal(v.JSONValue, pResult.Interface())
		if err != nil {
			return nil, fmt.Errorf("unmarshal type[%s] fail: %v, data: %s", t.String(), err, string(v.JSONValue))
		}
		return pResult.Elem().Interface(), nil
	}

	if len(v.Type.StructType) > 0 {
		// struct
		rt, ok := m[v.Type.StructType]
		if !ok {
			return nil, fmt.Errorf("unknown type key: %v", v.Type.StructType)
		}
		result, dResult := createValueFromType(resolvePointerNum(v.Type.PointerNum, rt))

		err := setStructFields(dResult, v.MapValues)
		if err != nil {
			return nil, err
		}

		return result.Interface(), nil
	}

	if v.Type.MapKeyType != nil {
		// map
		rkt, err := restoreType(v.Type.MapKeyType)
		if err != nil {
			return nil, err
		}
		rvt, err := restoreType(v.Type.MapValueType)
		if err != nil {
			return nil, err
		}

		result, dResult := createValueFromType(reflect.MapOf(rkt, rvt))
		err = setMapKVs(dResult, v.MapValues)
		if err != nil {
			return nil, err
		}
		return result.Interface(), nil
	}

	// slice
	rvt, err := restoreType(v.Type.SliceValueType)
	if err != nil {
		return nil, err
	}

	result, dResult := createValueFromType(reflect.SliceOf(rvt))
	err = setSliceElems(dResult, v.SliceValues)
	if err != nil {
		return nil, err
	}
	return result.Interface(), nil
}

func internalSpecificTypeUnmarshal(is *internalStruct, typ reflect.Type) (any, error) {
	_, dtyp := derefPointerNum(typ)
	result, dResult := createValueFromType(typ)

	if dtyp.Kind() == reflect.Struct {
		err := setStructFields(dResult, is.MapValues)
		if err != nil {
			return nil, err
		}
		return result.Interface(), nil
	} else if dtyp.Kind() == reflect.Map {
		err := setMapKVs(dResult, is.MapValues)
		if err != nil {
			return nil, err
		}
		return result.Interface(), nil
	} else if dtyp.Kind() == reflect.Array || dtyp.Kind() == reflect.Slice {
		err := setSliceElems(dResult, is.SliceValues)
		if err != nil {
			return nil, err
		}
		return result.Interface(), nil
	}
	// simple type
	v := reflect.New(typ)
	err := sonic.Unmarshal(is.JSONValue, v.Interface())
	if err != nil {
		return nil, fmt.Errorf("unmarshal type[%s] fail: %v", typ.String(), err)
	}
	return v.Elem().Interface(), nil
}

func setSliceElems(dResult reflect.Value, values []*internalStruct) error {
	t := dResult.Type()
	for _, internalValue := range values {
		value, err := internalUnmarshal(internalValue, t.Elem())
		if err != nil {
			return fmt.Errorf("unmarshal slice[%s] fail: %v", t.Elem(), err)
		}
		if value == nil {
			// empty value
			dResult.Set(reflect.Append(dResult, reflect.New(t.Elem()).Elem()))
		} else {
			dResult.Set(reflect.Append(dResult, reflect.ValueOf(value)))
		}
	}
	return nil
}

func setMapKVs(dResult reflect.Value, values map[string]*internalStruct) error {
	t := dResult.Type()
	for marshaledMapKey, internalValue := range values {
		prkv := reflect.New(t.Key())
		err := sonic.UnmarshalString(marshaledMapKey, prkv.Interface())
		if err != nil {
			return fmt.Errorf("unmarshal map key[%v] to type[%s] fail: %v", marshaledMapKey, t.Key(), err)
		}

		value, err := internalUnmarshal(internalValue, t.Elem())
		if err != nil {
			return fmt.Errorf("unmarshal map value fail: %v", err)
		}
		if value == nil {
			dResult.SetMapIndex(prkv.Elem(), reflect.New(t.Elem()).Elem())
		} else {
			dResult.SetMapIndex(prkv.Elem(), reflect.ValueOf(value))
		}
	}
	return nil
}

func setStructFields(dResult reflect.Value, values map[string]*internalStruct) error {
	t := dResult.Type()
	for k, internalValue := range values {
		sf, ok := t.FieldByName(k)
		if !ok {
			continue
		}
		value, err := internalUnmarshal(internalValue, sf.Type)
		if err != nil {
			return fmt.Errorf("unmarshal map field[%v] fail: %v", k, err)
		}
		err = setStructField(t, dResult, k, value)
		if err != nil {
			return err
		}
	}
	return nil
}

func setStructField(t reflect.Type, s reflect.Value, fieldName string, val any) error {
	field := s.FieldByName(fieldName)
	if !field.CanSet() {
		return fmt.Errorf("unmarshal map fail, can not set field %v", fieldName)
	}
	if val == nil {
		rft, ok := t.FieldByName(fieldName)
		if !ok {
			return fmt.Errorf("unmarshal map fail, cannot find field: %v", fieldName)
		}
		field.Set(reflect.New(rft.Type).Elem())
	} else {
		field.Set(reflect.ValueOf(val))
	}
	return nil
}

func resolvePointerNum(pointerNum uint32, t reflect.Type) reflect.Type {
	for i := uint32(0); i < pointerNum; i++ {
		t = reflect.PointerTo(t)
	}
	return t
}

func derefPointerNum(t reflect.Type) (uint32, reflect.Type) {
	var ptrCount uint32 = 0

	for t != nil && t.Kind() == reflect.Ptr {
		t = t.Elem()
		ptrCount++
	}

	return ptrCount, t
}

func createValueFromType(t reflect.Type) (value reflect.Value, derefValue reflect.Value) {
	value = reflect.New(t).Elem()

	derefValue = value
	for derefValue.Kind() == reflect.Ptr {
		if derefValue.IsNil() {
			derefValue.Set(reflect.New(derefValue.Type().Elem()))
		}
		derefValue = derefValue.Elem()
	}

	if derefValue.Kind() == reflect.Map && derefValue.IsNil() {
		derefValue.Set(reflect.MakeMap(derefValue.Type()))
	}

	if (derefValue.Kind() == reflect.Slice || derefValue.Kind() == reflect.Array) && derefValue.IsNil() {
		derefValue.Set(reflect.MakeSlice(derefValue.Type(), 0, 0))
	}

	return value, derefValue
}

var marshalerType = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
var unmarshalerType = reflect.TypeOf((*json.Unmarshaler)(nil)).Elem()

func checkMarshaler(t reflect.Type) bool {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	if (t.Implements(marshalerType) || reflect.PointerTo(t).Implements(marshalerType)) &&
		(t.Implements(unmarshalerType) || reflect.PointerTo(t).Implements(unmarshalerType)) {
		return true
	}
	return false
}
