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
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type hrPlainValue struct {
	Value int `json:"value"`
}

type hrValueOnly struct {
	Value any    `json:"value"`
	Name  string `json:"name,omitempty"`
}
type hrReservedValue struct {
	Type  string `json:"$type,omitempty"`
	Value int    `json:"value"`
}
type hrNamedKey string
type hrNamedString string
type hrNamedBool bool
type hrNamedFloat float64
type hrNamedSlice []int
type hrNamedMap map[string]int
type hrNamedArray [2]int
type hrArrayJSON struct{ Values []int }

func (v hrArrayJSON) MarshalJSON() ([]byte, error)     { return json.Marshal(v.Values) }
func (v *hrArrayJSON) UnmarshalJSON(data []byte) error { return json.Unmarshal(data, &v.Values) }

type hrValueJSON struct {
	Value int `json:"value"`
}

func (v hrValueJSON) MarshalJSON() ([]byte, error) {
	type plain hrValueJSON
	return json.Marshal(plain(v))
}
func (v *hrValueJSON) UnmarshalJSON(data []byte) error {
	type plain hrValueJSON
	return json.Unmarshal(data, (*plain)(v))
}

func init() {
	registrations := []error{
		GenericRegister[hrPlainValue]("hr_plain_value"),
		GenericRegister[hrValueOnly]("hr_value_only"),
		GenericRegister[hrEdgeFancyArray]("hr_edge_fancy_array"),
		GenericRegister[hrReservedValue]("hr_reserved_value"),
		GenericRegister[hrNamedKey]("hr_named_key"),
		GenericRegister[hrNamedString]("hr_named_string"),
		GenericRegister[hrNamedBool]("hr_named_bool"),
		GenericRegister[hrNamedFloat]("hr_named_float"),
		GenericRegister[hrNamedSlice]("hr_named_slice"),
		GenericRegister[hrNamedMap]("hr_named_map"),
		GenericRegister[hrNamedArray]("hr_named_array"),
		GenericRegister[hrArrayJSON]("hr_array_json"),
		GenericRegister[hrValueJSON]("hr_value_json"),
		GenericRegister[hrOpaqueJSON]("hr_opaque_json"),
		GenericRegister[hrOpaqueJSONHolder]("hr_opaque_json_holder"),
		GenericRegister[hrJSONSlice]("hr_json_slice"),
		GenericRegister[hrMarshalOnly]("hr_marshal_only"),
		GenericRegister[hrPointerMarshalOnly]("hr_pointer_marshal_only"),
		GenericRegister[hrUnmarshalOnly]("hr_unmarshal_only"),
	}
	for _, err := range registrations {
		if err != nil {
			panic(err)
		}
	}
}

func TestHumanReadableSerializer_RegressionRoundTrips(t *testing.T) {
	s := &HumanReadableSerializer{}
	cases := []struct {
		name  string
		value any
	}{
		{"business value scalar", hrValueOnly{Value: 7}},
		{"concrete business value", hrPlainValue{Value: 7}},
		{"business value map", hrValueOnly{Value: map[string]any{"value": 7}}},
		{"business value struct", hrValueOnly{Value: hrValueOnly{Value: "nested"}}},
		{"reserved fields", hrReservedValue{Type: "_eino_string", Value: 7}},
		{"omitted reserved field", hrReservedValue{Value: 7}},
		{"concrete map with reserved keys", hrTestStructWithExtra{Extra: map[string]any{"$type": "_eino_string", "value": "hello"}}},
		{"nested maps with reserved keys", map[string]map[string]any{"nested": {"$type": "_eino_int", "value": 7}}},
		{"named string map keys", map[hrNamedKey]int{"a": 1}},
		{"float32", float32(1.5)},
		{"integral float64", float64(1)},
		{"named string", hrNamedString("hello")},
		{"named bool", hrNamedBool(true)},
		{"named float", hrNamedFloat(1.5)},
		{"named slice", hrNamedSlice{1, 2}},
		{"named map", hrNamedMap{"a": 1}},
		{"named array", hrNamedArray{1, 2}},
		{"nested named collections", []hrNamedMap{{"a": 1}}},
		{"custom JSON array", hrArrayJSON{Values: []int{7}}},
		{"custom JSON single value", hrValueJSON{Value: 7}},
		{"concrete custom JSON array", hrEdgeFancyArray{Array: hrArrayJSON{Values: []int{7}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := hrEdgeAnyContainer{V: tc.value}
			data, err := s.Marshal(input)
			require.NoError(t, err)
			var got hrEdgeAnyContainer
			require.NotPanics(t, func() { err = s.Unmarshal(data, &got) })
			require.NoError(t, err)
			assert.Equal(t, input, got)
		})
	}
}

func TestHumanReadableSerializer_IntegerWireCompatibility(t *testing.T) {
	s := &HumanReadableSerializer{}
	for _, tc := range []struct {
		value    any
		typeName string
	}{
		{int(42), "_eino_int"}, {int8(42), "_eino_int8"},
		{int16(42), "_eino_int16"}, {int32(42), "_eino_int32"},
		{int64(42), "_eino_int64"}, {uint(42), "_eino_uint"},
		{uint8(42), "_eino_uint8"}, {uint16(42), "_eino_uint16"},
		{uint32(42), "_eino_uint32"}, {uint64(42), "_eino_uint64"},
		{uintptr(42), "_eino_uintptr"},
	} {
		t.Run(tc.typeName, func(t *testing.T) {
			primitiveWire := `{"$type":"` + tc.typeName + `","value":42}`
			slice := reflect.MakeSlice(reflect.SliceOf(reflect.TypeOf(tc.value)), 1, 1)
			slice.Index(0).Set(reflect.ValueOf(tc.value))
			for _, position := range []struct {
				name  string
				value any
				wire  string
			}{
				{"root", tc.value, primitiveWire},
				{"interface field", hrEdgeAnyContainer{V: tc.value}, `{"$type":"hr_edge_any_container","v":` + primitiveWire + `}`},
				{"concrete element", slice.Interface(), `{"$type":"[]` + tc.typeName + `","value":[42]}`},
			} {
				t.Run(position.name, func(t *testing.T) {
					data, err := s.Marshal(position.value)
					require.NoError(t, err)
					assert.JSONEq(t, position.wire, string(data))
					got := reflect.New(reflect.TypeOf(position.value))
					require.NoError(t, s.Unmarshal(data, got.Interface()))
					assert.Equal(t, position.value, got.Elem().Interface())
				})
			}
		})
	}
}

type hrEdgeFancyArray struct{ Array hrArrayJSON }

func TestHumanReadableSerializer_ValueFieldLegacyFormat(t *testing.T) {
	s := &HumanReadableSerializer{}
	// An ordinary struct's value field stays inline, even when it is the only
	// field. This also makes previously ambiguous checkpoints readable.
	data, err := s.Marshal(hrPlainValue{Value: 7})
	require.NoError(t, err)
	assert.JSONEq(t, `{"$type":"hr_plain_value","value":7}`, string(data))
	var concrete hrPlainValue
	require.NoError(t, s.Unmarshal(data, &concrete))
	assert.Equal(t, hrPlainValue{Value: 7}, concrete)
	for _, wire := range []string{
		`{"$type":"hr_value_only","value":{"$type":"_eino_int","value":7}}`,
		`{"value":{"$type":"_eino_int","value":7}}`,
	} {
		var got hrValueOnly
		require.NoError(t, s.Unmarshal([]byte(wire), &got))
		assert.Equal(t, hrValueOnly{Value: 7}, got)
	}
	input := hrValueOnly{Value: 7}
	data, err = s.Marshal(input)
	require.NoError(t, err)
	var got hrValueOnly
	require.NoError(t, s.Unmarshal(data, &got))
	assert.Equal(t, input, got)
}

func TestHumanReadableSerializer_NamedCollectionPointers(t *testing.T) {
	s := &HumanReadableSerializer{}
	values := []any{hrNamedSlice{1, 2}, hrNamedMap{"a": 1}, hrNamedArray{1, 2}}
	for _, value := range values {
		ptr := reflect.New(reflect.TypeOf(value))
		ptr.Elem().Set(reflect.ValueOf(value))
		input := hrEdgeAnyContainer{V: ptr.Interface()}
		data, err := s.Marshal(input)
		require.NoError(t, err)
		var got hrEdgeAnyContainer
		require.NoError(t, s.Unmarshal(data, &got))
		assert.Equal(t, input, got)
	}
}

// The private payload must be handled by the JSON methods, not field reflection.
type hrOpaqueJSON struct{ payload json.RawMessage }

var errHROpaqueJSON = errors.New("custom JSON error")

func (v *hrOpaqueJSON) MarshalJSON() ([]byte, error) {
	if string(v.payload) == "marshal-error" {
		return nil, errHROpaqueJSON
	}
	return v.payload, nil
}

func (v *hrOpaqueJSON) UnmarshalJSON(data []byte) error {
	if string(data) == `"unmarshal-error"` {
		return errHROpaqueJSON
	}
	v.payload = append(v.payload[:0], data...)
	return nil
}

type hrOpaqueJSONHolder struct {
	Concrete hrOpaqueJSON            `json:"concrete"`
	Pointer  *hrOpaqueJSON           `json:"pointer"`
	Any      any                     `json:"any"`
	Map      map[string]hrOpaqueJSON `json:"map"`
	Slice    []*hrOpaqueJSON         `json:"slice"`
}

func assertHRJSONContent(t *testing.T, expected, actual []byte) {
	t.Helper()
	decode := func(data []byte) any {
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.UseNumber() // JSONEq's float64 comparison can hide large integer loss.
		var value any
		require.NoError(t, dec.Decode(&value))
		return value
	}
	assert.Equal(t, decode(expected), decode(actual))
}

func TestHumanReadableSerializer_CustomJSONPayloadContract(t *testing.T) {
	s := &HumanReadableSerializer{}
	for _, payload := range []string{
		`{"name":"test","value":7}`,
		`{"value":7}`,
		`{"$type":"_eino_int","value":7}`,
		`{"nested":{"$type":"_eino_string","value":"business"}}`,
		`[{"$type":"_eino_int","value":7},null]`,
		`"escaped \\ \" 中文 <>&"`, `true`, `9223372036854775807`, `18446744073709551615`,
		`1e1000`, `1.0`, `-0`, `-0.0`, `{}`, `[]`,
	} {
		t.Run(payload, func(t *testing.T) {
			value := hrOpaqueJSON{payload: json.RawMessage(payload)}
			for _, input := range []any{value, &value} {
				data, err := s.Marshal(input)
				require.NoError(t, err)
				target := reflect.New(reflect.TypeOf(input))
				require.NoError(t, s.Unmarshal(data, target.Interface()))
				got := target.Elem()
				if got.Kind() == reflect.Ptr {
					require.False(t, got.IsNil())
					got = got.Elem()
				}
				assertHRJSONContent(t, value.payload, got.Interface().(hrOpaqueJSON).payload)
			}
			input := hrOpaqueJSONHolder{Concrete: value, Pointer: &value, Any: &value,
				Map: map[string]hrOpaqueJSON{"item": value}, Slice: []*hrOpaqueJSON{&value, nil}}
			data, err := s.Marshal(input)
			require.NoError(t, err)
			var fields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(data, &fields))
			assertHRJSONContent(t, value.payload, fields["concrete"])
			var got hrOpaqueJSONHolder
			require.NoError(t, s.Unmarshal(data, &got))
			require.IsType(t, &hrOpaqueJSON{}, got.Any)
			require.NotNil(t, got.Pointer)
			require.Len(t, got.Map, 1)
			require.Len(t, got.Slice, 2)
			require.NotNil(t, got.Slice[0])
			assert.Nil(t, got.Slice[1])
			mapValue := got.Map["item"]
			for _, custom := range []*hrOpaqueJSON{&got.Concrete, got.Pointer, got.Any.(*hrOpaqueJSON), &mapValue, got.Slice[0]} {
				assertHRJSONContent(t, value.payload, custom.payload)
			}
		})
	}
}

func TestHumanReadableSerializer_CustomJSONErrors(t *testing.T) {
	s := &HumanReadableSerializer{}
	_, err := s.Marshal(&hrOpaqueJSON{payload: json.RawMessage("marshal-error")})
	require.ErrorIs(t, err, errHROpaqueJSON)
	_, err = s.Marshal(&hrOpaqueJSON{payload: json.RawMessage(`{"incomplete"`)})
	require.Error(t, err)
	var root hrOpaqueJSON
	err = s.Unmarshal([]byte(`{"$type":"hr_opaque_json","value":"unmarshal-error"}`), &root)
	require.ErrorIs(t, err, errHROpaqueJSON)
	for _, wire := range []string{
		`{"concrete":"unmarshal-error"}`, `{"map":{"item":"unmarshal-error"}}`,
		`{"slice":["unmarshal-error"]}`,
	} {
		var got hrOpaqueJSONHolder
		require.ErrorIs(t, s.Unmarshal([]byte(wire), &got), errHROpaqueJSON)
	}
}

// A named collection can choose a JSON shape different from its underlying kind.
type hrJSONSlice []int

func (v hrJSONSlice) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct{ Items []int }{Items: []int(v)})
}

func (v *hrJSONSlice) UnmarshalJSON(data []byte) error {
	var projection struct{ Items []int }
	if err := json.Unmarshal(data, &projection); err != nil {
		return err
	}
	*v = projection.Items
	return nil
}

func TestHumanReadableSerializer_NestedTypeIdentity(t *testing.T) {
	s := &HumanReadableSerializer{}
	values := []any{
		hrNamedSlice{}, hrNamedMap{}, hrNamedArray{}, hrJSONSlice{1, 2},
		map[hrNamedKey]any{"item": hrValueOnly{Value: hrNamedFloat(1)}},
		[]any{hrNamedSlice{1}, map[string]any{"item": hrValueJSON{Value: 7}}},
		[2]any{hrReservedValue{Type: "_eino_string", Value: 7}, hrArrayJSON{Values: []int{2}}},
		int64(math.MaxInt64), uint64(math.MaxUint64),
		float32(math.SmallestNonzeroFloat32), float32(math.MaxFloat32),
		float64(math.SmallestNonzeroFloat64), float64(math.MaxFloat64),
		float32(math.Copysign(0, -1)), math.Copysign(0, -1),
	}
	for _, value := range values {
		t.Run(reflect.TypeOf(value).String(), func(t *testing.T) {
			for _, input := range []any{value, hrEdgeAnyContainer{V: value}} {
				data, err := s.Marshal(input)
				require.NoError(t, err)
				target := reflect.New(reflect.TypeOf(input))
				require.NoError(t, s.Unmarshal(data, target.Interface()))
				got := target.Elem().Interface()
				assert.Equal(t, input, got)
				if holder, ok := got.(hrEdgeAnyContainer); ok {
					got = holder.V
				}
				switch want := value.(type) {
				case float32:
					require.IsType(t, want, got)
					assert.Equal(t, math.Float32bits(want), math.Float32bits(got.(float32)))
				case float64:
					require.IsType(t, want, got)
					assert.Equal(t, math.Float64bits(want), math.Float64bits(got.(float64)))
				}
			}
		})
	}
}

func TestHumanReadableSerializer_NegativeZeroInConcreteCollections(t *testing.T) {
	s := &HumanReadableSerializer{}
	input := map[string]float64{"zero": math.Copysign(0, -1)}
	data, err := s.Marshal(input)
	require.NoError(t, err)
	var got map[string]float64
	require.NoError(t, s.Unmarshal(data, &got))
	assert.Equal(t, math.Float64bits(input["zero"]), math.Float64bits(got["zero"]))
	input32 := []float32{float32(math.Copysign(0, -1))}
	data, err = s.Marshal(input32)
	require.NoError(t, err)
	var got32 []float32
	require.NoError(t, s.Unmarshal(data, &got32))
	require.Len(t, got32, 1)
	assert.Equal(t, math.Float32bits(input32[0]), math.Float32bits(got32[0]))
}

type hrMarshalOnly struct{ Value int }

func (v hrMarshalOnly) MarshalJSON() ([]byte, error) { return json.Marshal(v.Value) }

type hrPointerMarshalOnly hrMarshalOnly

func (v *hrPointerMarshalOnly) MarshalJSON() ([]byte, error) { return json.Marshal(v.Value) }

type hrUnmarshalOnly hrMarshalOnly

func (v *hrUnmarshalOnly) UnmarshalJSON(data []byte) error { return json.Unmarshal(data, &v.Value) }

func TestHumanReadableSerializer_IncompleteJSONMethodPair(t *testing.T) {
	s := &HumanReadableSerializer{}
	for _, tc := range []struct {
		value   any
		name    string
		missing string
	}{
		{hrMarshalOnly{Value: 7}, "hr_marshal_only", "json.Unmarshaler"},
		{hrPointerMarshalOnly{Value: 7}, "hr_pointer_marshal_only", "json.Unmarshaler"},
		{hrUnmarshalOnly{Value: 7}, "hr_unmarshal_only", "json.Marshaler"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := reflect.TypeOf(tc.value)
			pointer := reflect.New(rt)
			pointer.Elem().Set(reflect.ValueOf(tc.value))
			holder := reflect.New(reflect.StructOf([]reflect.StructField{{Name: "Value", Type: rt, Tag: `json:"value"`}})).Elem()
			holder.Field(0).Set(reflect.ValueOf(tc.value))
			envelope := `{"$type":"` + tc.name + `","Value":7}`
			for _, position := range []struct {
				name  string
				input any
				wire  string
			}{
				{"root", tc.value, `{"Value":7}`},
				{"root pointer", pointer.Interface(), `{"Value":7}`},
				{"concrete field", holder.Interface(), `{"value":{"Value":7}}`},
				{"interface field", hrEdgeAnyContainer{V: tc.value}, `{"v":` + envelope + `}`},
				{"map element", map[string]any{"item": tc.value}, `{"item":` + envelope + `}`},
				{"slice element", []any{tc.value}, `[` + envelope + `]`},
			} {
				t.Run(position.name, func(t *testing.T) {
					_, err := s.Marshal(position.input)
					require.ErrorContains(t, err, "missing "+tc.missing)
					assert.Contains(t, err.Error(), rt.String())
					target := reflect.New(reflect.TypeOf(position.input))
					err = s.Unmarshal([]byte(position.wire), target.Interface())
					require.ErrorContains(t, err, "missing "+tc.missing)
					assert.Contains(t, err.Error(), rt.String())
				})
			}
		})
	}
}

func TestHumanReadableSerializer_DecodedStringsOwnTheirMemory(t *testing.T) {
	data := []byte(`"retained value"`)
	var got string
	require.NoError(t, (&HumanReadableSerializer{}).Unmarshal(data, &got))
	for i := range data {
		data[i] = 'x'
	}
	assert.Equal(t, "retained value", got)
}

func TestHumanReadableSerializer_RejectsMalformedJSON(t *testing.T) {
	s := &HumanReadableSerializer{}
	for _, data := range [][]byte{
		[]byte(`{"value":1} trailing`), []byte(`1 2`), []byte("\"unescaped\nnewline\""),
	} {
		var got any
		require.Error(t, s.Unmarshal(data, &got))
	}
}
