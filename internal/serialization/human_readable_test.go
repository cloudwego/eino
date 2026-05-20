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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type hrTestStruct struct {
	Name  string `json:"name"`
	Value int    `json:"value"`
}

type hrTestStructWithExtra struct {
	Name  string         `json:"name"`
	Extra map[string]any `json:"extra,omitempty"`
}

type hrStructWithInterface struct {
	A any
	B any
	C map[string]any
}

type hrWrapper struct {
	Inner hrTestStruct `json:"inner"`
}

type hrLargeIntegerStruct struct {
	I int64  `json:"i"`
	U uint64 `json:"u"`
	A any    `json:"a"`
}

type hrReservedTypeStruct struct {
	Type string `json:"$type"`
	Name string `json:"name"`
}

func init() {
	_ = GenericRegister[hrTestStruct]("hr_test_struct")
	_ = GenericRegister[hrTestStructWithExtra]("hr_test_struct_with_extra")
	_ = GenericRegister[hrStructWithInterface]("hr_struct_with_interface")
	_ = GenericRegister[hrWrapper]("hr_wrapper")
	_ = GenericRegister[hrLargeIntegerStruct]("hr_large_integer_struct")
	_ = GenericRegister[hrReservedTypeStruct]("hr_reserved_type_struct")
}

func TestHumanReadableSerializer_OmitemptyBehavior(t *testing.T) {
	s := &HumanReadableSerializer{}

	input := hrTestStructWithExtra{
		Name:  "test",
		Extra: nil,
	}

	data, err := s.Marshal(input)
	require.NoError(t, err)

	var jsonMap map[string]any
	err = json.Unmarshal(data, &jsonMap)
	require.NoError(t, err)

	_, hasExtra := jsonMap["extra"]
	assert.False(t, hasExtra, "omitempty field should not be present when nil")
}

func TestHumanReadableSerializer_TypeAnnotationForCustomTypes(t *testing.T) {
	s := &HumanReadableSerializer{}

	input := hrStructWithInterface{
		A: hrTestStruct{Name: "typed", Value: 100},
	}

	data, err := s.Marshal(input)
	require.NoError(t, err)

	var jsonMap map[string]any
	err = json.Unmarshal(data, &jsonMap)
	require.NoError(t, err)

	aMap := jsonMap["A"].(map[string]any)
	assert.Equal(t, "hr_test_struct", aMap["$type"])

	var result hrStructWithInterface
	err = s.Unmarshal(data, &result)
	require.NoError(t, err)
	assert.Equal(t, input.A, result.A)
}

func TestHumanReadableSerializer_CompareWithInternalSerializer(t *testing.T) {
	hr := &HumanReadableSerializer{}
	is := &InternalSerializer{}

	input := hrStructWithInterface{
		A: "string",
		B: hrTestStruct{Name: "test", Value: 42},
		C: map[string]any{
			"key1": "value1",
			"key2": 123,
		},
	}

	hrData, err := hr.Marshal(input)
	require.NoError(t, err)

	isData, err := is.Marshal(input)
	require.NoError(t, err)

	t.Logf("HumanReadable output size: %d bytes", len(hrData))
	t.Logf("Internal output size: %d bytes", len(isData))
	t.Logf("HumanReadable output:\n%s", string(hrData))

	assert.Less(t, len(hrData), len(isData), "HumanReadable should produce smaller output")

	var hrResult hrStructWithInterface
	err = hr.Unmarshal(hrData, &hrResult)
	require.NoError(t, err)

	var isResult hrStructWithInterface
	err = is.Unmarshal(isData, &isResult)
	require.NoError(t, err)

	assert.Equal(t, hrResult.A, isResult.A)
	assert.Equal(t, hrResult.B, isResult.B)
}

func TestHumanReadableSerializer_JSONFieldNames(t *testing.T) {
	s := &HumanReadableSerializer{}

	input := hrTestStruct{
		Name:  "test",
		Value: 123,
	}

	data, err := s.Marshal(input)
	require.NoError(t, err)

	var jsonMap map[string]any
	err = json.Unmarshal(data, &jsonMap)
	require.NoError(t, err)

	assert.Equal(t, "test", jsonMap["name"])
	assert.Equal(t, float64(123), jsonMap["value"])
	_, hasName := jsonMap["Name"]
	assert.False(t, hasName, "should use json tag name, not struct field name")
}

func TestHumanReadableSerializer_TypeAnnotationOnlyForInterfaceFields(t *testing.T) {
	s := &HumanReadableSerializer{}

	t.Run("concrete struct field has no $type", func(t *testing.T) {
		input := hrWrapper{
			Inner: hrTestStruct{Name: "test", Value: 123},
		}

		data, err := s.Marshal(input)
		require.NoError(t, err)

		var jsonMap map[string]any
		err = json.Unmarshal(data, &jsonMap)
		require.NoError(t, err)

		innerMap := jsonMap["inner"].(map[string]any)
		_, hasType := innerMap["$type"]
		assert.False(t, hasType, "concrete struct field should not have $type annotation")
	})

	t.Run("interface field has $type", func(t *testing.T) {
		input := hrStructWithInterface{
			A: hrTestStruct{Name: "test", Value: 123},
		}

		data, err := s.Marshal(input)
		require.NoError(t, err)

		var jsonMap map[string]any
		err = json.Unmarshal(data, &jsonMap)
		require.NoError(t, err)

		aMap := jsonMap["A"].(map[string]any)
		_, hasType := aMap["$type"]
		assert.True(t, hasType, "interface field should have $type annotation")
	})
}

func TestHumanReadableSerializer_PreservesLargeIntegers(t *testing.T) {
	s := &HumanReadableSerializer{}
	input := hrLargeIntegerStruct{
		I: 9007199254740993,
		U: 1<<63 + 123,
		A: int64(9007199254740993),
	}

	data, err := s.Marshal(input)
	require.NoError(t, err)

	var result hrLargeIntegerStruct
	err = s.Unmarshal(data, &result)
	require.NoError(t, err)
	assert.Equal(t, input, result)
}

func TestHumanReadableSerializer_PreservesUserTypeKey(t *testing.T) {
	s := &HumanReadableSerializer{}

	t.Run("map key", func(t *testing.T) {
		input := map[string]any{
			"$type": "user-controlled",
			"value": int64(7),
		}

		data, err := s.Marshal(input)
		require.NoError(t, err)

		var result map[string]any
		err = s.Unmarshal(data, &result)
		require.NoError(t, err)
		assert.Equal(t, input, result)
	})

	t.Run("struct field", func(t *testing.T) {
		input := hrReservedTypeStruct{
			Type: "user-controlled",
			Name: "kept",
		}

		data, err := s.Marshal(input)
		require.NoError(t, err)

		var result hrReservedTypeStruct
		err = s.Unmarshal(data, &result)
		require.NoError(t, err)
		assert.Equal(t, input, result)
	})
}
