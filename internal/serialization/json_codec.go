/*
 * Copyright 2026 CloudWeGo Authors
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
	"bytes"
	"encoding/json"
	"reflect"

	"github.com/bytedance/sonic"
)

type jsonCodec struct {
	marshal   func(reflect.Value) ([]byte, error)
	unmarshal func([]byte, reflect.Value) error
}

// Like the type registry, codecs must be registered during package initialization.
var jsonCodecs = map[reflect.Type]jsonCodec{}

// RegisterJSONCodec supplies a persistence representation for a type whose
// ordinary JSON methods cannot preserve interface values. It does not change
// that type's encoding/json behavior or its registered wire name.
func RegisterJSONCodec[T any](marshal func(*T) ([]byte, error), unmarshal func([]byte, *T) error) {
	typ := reflect.TypeOf((*T)(nil)).Elem()
	if _, exists := jsonCodecs[typ]; exists {
		panic("JSON codec already registered for " + typ.String())
	}
	jsonCodecs[typ] = jsonCodec{
		marshal: func(value reflect.Value) ([]byte, error) {
			tmp := reflect.New(typ)
			tmp.Elem().Set(value)
			return marshal(tmp.Interface().(*T))
		},
		unmarshal: func(data []byte, value reflect.Value) error {
			return unmarshal(data, value.Addr().Interface().(*T))
		},
	}
}

// MarshalJSONValue encodes a concrete projection without adding root metadata.
// Interface fields in the projection still retain their registered types.
func MarshalJSONValue(value any) ([]byte, error) {
	raw, err := hrMarshal(value, reflect.TypeOf(value))
	if err != nil {
		return nil, err
	}
	return sonic.Marshal(raw)
}

func marshalJSON(value any) ([]byte, error) {
	rv := reflect.ValueOf(value)
	for rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return json.Marshal(value)
		}
		rv = rv.Elem()
	}
	if codec, ok := jsonCodecs[rv.Type()]; ok {
		return codec.marshal(rv)
	}
	return json.Marshal(value)
}

func unmarshalJSON(data []byte, value any) error {
	target := reflect.ValueOf(value).Elem()
	_, baseType := derefPointerNum(target.Type())
	codec, ok := jsonCodecs[baseType]
	if !ok {
		return json.Unmarshal(data, value)
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		target.Set(reflect.Zero(target.Type()))
		return nil
	}
	result, base := createValueFromType(target.Type())
	if err := codec.unmarshal(data, base); err != nil {
		return err
	}
	target.Set(result)
	return nil
}
