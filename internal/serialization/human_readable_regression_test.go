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
	"encoding/json"
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
