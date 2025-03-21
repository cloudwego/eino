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
}

func Register(key string, value any) error {
	t := reflect.TypeOf(value)
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

func Marshal(v interface{}) ([]byte, error) {
	is, err := internalMarshal(v)
	if err != nil {
		return nil, err
	}
	return sonic.Marshal(is)
}

func Unmarshal(data []byte) (any, error) {
	is := &internalStruct{}
	err := sonic.Unmarshal(data, is)
	if err != nil {
		return nil, err
	}
	return internalUnmarshal(is)
}

type internalStruct struct {
	PointerNum uint32 `json:",omitempty"`

	// based type
	Type      string          `json:",omitempty"`
	JSONValue json.RawMessage `json:",omitempty"`

	// struct type
	StructType string `json:",omitempty"`
	// map key type
	MapKeyPointerNum uint32 `json:",omitempty"`
	MapKeyType       string `json:",omitempty"`
	// map value type
	MapValuePointerNum uint32 `json:",omitempty"`
	MapValueType       string `json:",omitempty"`
	// map or struct
	// in map, the key is the serialized map key anyway todo: if key is string, don't serialize
	// in struct, the key is the original field name
	MapValues map[string]*internalStruct `json:",omitempty"`

	// slice value type
	SliceValuePointerNum uint32            `json:",omitempty"`
	SliceValueType       string            `json:",omitempty"`
	SliceValues          []*internalStruct `json:",omitempty"`
}

func internalMarshal(v any) (*internalStruct, error) {
	if v == nil {
		return nil, nil // 这里表示没有值，空指针不等于没有值
	}

	ret := &internalStruct{}
	rv := reflect.ValueOf(v)
	rt := rv.Type()

	// 计算指针层数
	for rt.Kind() == reflect.Ptr {
		ret.PointerNum++
		if rv.IsNil() {
			for rt.Kind() == reflect.Ptr {
				rt = rt.Elem()
			}
			key, ok := rm[rt]
			if !ok {
				return nil, fmt.Errorf("unknown type: %v", rt)
			}
			ret.Type = key
			ret.JSONValue = json.RawMessage("null")
			return ret, nil
		}
		rv = rv.Elem()
		rt = rt.Elem()
	}

	switch rt.Kind() {
	case reflect.Struct:
		// 处理struct，复用map部分处理
		key, ok := rm[rt]
		if !ok {
			return nil, fmt.Errorf("unknown type: %v", rt)
		}
		ret.StructType = key

		ret.MapValues = make(map[string]*internalStruct)

		for i := 0; i < rt.NumField(); i++ {
			field := rt.Field(i)
			// 只处理可导出的字段
			if field.PkgPath == "" {
				k := field.Name
				v := rv.Field(i) // 使用Field(i)而不是FieldByName，更高效

				internalValue, err := internalMarshal(v.Interface())
				if err != nil {
					return nil, err
				}

				ret.MapValues[k] = internalValue
			}
		}

		return ret, nil
	case reflect.Map:
		// 处理Map类型
		// map key类型
		rkt := rt.Key()
		for rkt.Kind() == reflect.Ptr {
			ret.MapKeyPointerNum++
			rkt = rkt.Elem()
		}
		key, ok := rm[rkt]
		if !ok {
			return nil, fmt.Errorf("unknown type: %v", rkt)
		}
		ret.MapKeyType = key
		// map value类型
		rvt := rt.Elem()
		for rvt.Kind() == reflect.Ptr {
			ret.MapValuePointerNum++
			rvt = rvt.Elem()
		}
		key, ok = rm[rvt]
		if !ok {
			return nil, fmt.Errorf("unknown type: %v", rvt)
		}
		ret.MapValueType = key

		ret.MapValues = make(map[string]*internalStruct)

		iter := rv.MapRange()
		for iter.Next() {
			k := iter.Key()
			v := iter.Value()

			internalValue, err := internalMarshal(v.Interface())
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
		// 处理切片和数组类型
		rvt := rt.Elem()
		for rvt.Kind() == reflect.Ptr {
			ret.SliceValuePointerNum++
			rvt = rvt.Elem()
		}
		key, ok := rm[rvt]
		if !ok {
			return nil, fmt.Errorf("unknown type: %v", rvt)
		}
		ret.SliceValueType = key

		length := rv.Len()
		ret.SliceValues = make([]*internalStruct, length)

		for i := 0; i < length; i++ {
			internalValue, err := internalMarshal(rv.Index(i).Interface())
			if err != nil {
				return nil, err
			}
			ret.SliceValues[i] = internalValue
		}

		return ret, nil

	default:
		// 处理基本类型
		key, ok := rm[rv.Type()]
		if !ok {
			return nil, fmt.Errorf("unknown type: %v", rt)
		}
		ret.Type = key

		jsonBytes, err := json.Marshal(rv.Interface())
		if err != nil {
			return nil, err
		}
		ret.JSONValue = jsonBytes
		return ret, nil
	}
}

func internalUnmarshal(v *internalStruct) (any, error) {
	if v == nil {
		return nil, nil
	}

	if len(v.Type) != 0 {
		// based type
		t, ok := m[v.Type]
		if !ok {
			return nil, fmt.Errorf("unknown type key: %v", v.Type)
		}
		pResult := reflect.New(resolvePointerNum(v.PointerNum, t))
		err := sonic.Unmarshal(v.JSONValue, pResult.Interface())
		if err != nil {
			return nil, fmt.Errorf("unmarshal type[%s] fail: %v, data: %s", v.Type, err, string(v.JSONValue))
		}
		return pResult.Elem().Interface(), nil
	}

	if len(v.StructType) > 0 {
		// struct
		rt, ok := m[v.StructType]
		if !ok {
			return nil, fmt.Errorf("unknown type key: %v", v.StructType)
		}
		result, dResult := createValueFromType(resolvePointerNum(v.PointerNum, rt))

		// todo: if all fields are based, can use unmarshal instead of internalUnmarshal
		for k, internalValue := range v.MapValues {
			value, err := internalUnmarshal(internalValue)
			if err != nil {
				return nil, fmt.Errorf("unmarshal map field[%v] fail: %v", k, err)
			}
			field := dResult.FieldByName(k)
			if !field.CanSet() {
				return nil, fmt.Errorf("unmarshal map fail, can not set field %v", k)
			}
			if value == nil {
				rft, ok := rt.FieldByName(k)
				if !ok {
					return nil, fmt.Errorf("unmarshal map fail, cannot find field: %v", k)
				}
				field.Set(reflect.New(rft.Type).Elem())
			} else {
				field.Set(reflect.ValueOf(value))
			}
		}

		return result.Interface(), nil
	}

	if len(v.MapKeyType) > 0 {
		// map
		rkt, ok := m[v.MapKeyType]
		if !ok {
			return nil, fmt.Errorf("unknown type: %v", v.MapKeyType)
		}
		rkt = resolvePointerNum(v.MapKeyPointerNum, rkt)
		rvt, ok := m[v.MapValueType]
		if !ok {
			return nil, fmt.Errorf("unknown type: %v", v.MapValueType)
		}
		rvt = resolvePointerNum(v.MapValuePointerNum, rvt)

		// todo: if all values are based, can use unmarshal instead of internalUnmarshal
		result, dResult := createValueFromType(reflect.MapOf(rkt, rvt))
		for marshaledMapKey, internalValue := range v.MapValues {
			prkv := reflect.New(rkt)
			err := sonic.UnmarshalString(marshaledMapKey, prkv.Interface())
			if err != nil {
				return nil, fmt.Errorf("unmarshal map key[%v] to type[%s] fail: %v", marshaledMapKey, v.MapKeyType, err)
			}

			value, err := internalUnmarshal(internalValue)
			if err != nil {
				return nil, fmt.Errorf("unmarshal map value fail: %v", err)
			}
			if value == nil {
				dResult.SetMapIndex(prkv.Elem(), reflect.New(rvt).Elem())
			} else {
				dResult.SetMapIndex(prkv.Elem(), reflect.ValueOf(value))
			}
		}
		return result.Interface(), nil
	}

	// slice
	rvt, ok := m[v.SliceValueType]
	if !ok {
		return nil, fmt.Errorf("unknown type: %v", v.SliceValueType)
	}
	rvt = resolvePointerNum(v.SliceValuePointerNum, rvt)

	// todo: if all slice values are based, can use unmarshal instead of internalUnmarshal
	result, dResult := createValueFromType(reflect.SliceOf(rvt))
	for _, internalValue := range v.SliceValues {
		value, err := internalUnmarshal(internalValue)
		if err != nil {
			return nil, fmt.Errorf("unmarshal slice[%s] fail: %v", v.SliceValueType, err)
		}
		if value == nil {
			// empty value
			dResult.Set(reflect.Append(dResult, reflect.New(rvt).Elem()))
		} else {
			dResult.Set(reflect.Append(dResult, reflect.ValueOf(value)))
		}
	}
	return result.Interface(), nil
}

func resolvePointerNum(pointerNum uint32, t reflect.Type) reflect.Type {
	for i := uint32(0); i < pointerNum; i++ {
		t = reflect.PointerTo(t)
	}
	return t
}

func createValueFromType(t reflect.Type) (value reflect.Value, derefValue reflect.Value) {
	// 创建新值
	value = reflect.New(t).Elem()

	// 获取解引用后的值
	derefValue = value
	for derefValue.Kind() == reflect.Ptr {
		// 如果是nil指针，需要先初始化
		if derefValue.IsNil() {
			derefValue.Set(reflect.New(derefValue.Type().Elem()))
		}
		derefValue = derefValue.Elem()
	}

	// 如果是map类型，需要初始化
	if derefValue.Kind() == reflect.Map && derefValue.IsNil() {
		derefValue.Set(reflect.MakeMap(derefValue.Type()))
	}

	return value, derefValue
}
