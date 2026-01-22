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
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type Serializer interface {
	Marshal(v any) ([]byte, error)
	Unmarshal(data []byte, v any) error
}

func getSerializers() map[string]Serializer {
	return map[string]Serializer{
		"InternalSerializer":      &InternalSerializer{},
		"HumanReadableSerializer": &HumanReadableSerializer{},
	}
}

type myInterface interface {
	Method()
}
type myStruct struct {
	A string
}

func (m *myStruct) Method() {}

type myStruct2 struct {
	A any
	B myInterface
	C map[string]**myStruct
	D map[myStruct]any
	E []any
	f string
	G myStruct3
	H *myStruct4
	I []*myStruct3
	J map[string]myStruct3
	K myStruct4
	L []*myStruct4
	M map[string]myStruct4
}

type myStruct3 struct {
	FieldA string
}

type myStruct4 struct {
	FieldA string
}

func (m *myStruct4) UnmarshalJSON(bytes []byte) error {
	m.FieldA = string(bytes)
	return nil
}

func (m myStruct4) MarshalJSON() ([]byte, error) {
	return []byte(m.FieldA), nil
}

type myStruct5 struct {
	FieldA string
}

func (m *myStruct5) UnmarshalJSON(bytes []byte) error {
	m.FieldA = "FieldA"
	return nil
}

func (m myStruct5) MarshalJSON() ([]byte, error) {
	return []byte("1"), nil
}

type unmarshalTestStruct struct {
	Foo string
	Bar int
}

func init() {
	_ = GenericRegister[myStruct]("myStruct")
	_ = GenericRegister[myStruct2]("myStruct2")
	_ = GenericRegister[myStruct3]("myStruct3")
	_ = GenericRegister[myStruct4]("myStruct4")
	_ = GenericRegister[myStruct5]("myStruct5")
	_ = GenericRegister[myInterface]("myInterface")
	_ = GenericRegister[unmarshalTestStruct]("unmarshalTestStruct")
}

func TestSerialization_RoundTrip(t *testing.T) {
	ms := myStruct{A: "test"}
	pms := &ms
	pointerOfPointerOfMyStruct := &pms

	ms1 := myStruct{A: "1"}
	ms2 := myStruct{A: "2"}
	ms3 := myStruct{A: "3"}
	ms4 := myStruct{A: "4"}

	testCases := []struct {
		name  string
		value any
	}{
		{"int", 10},
		{"string", "test"},
		{"struct", ms},
		{"pointer to struct", pms},
		{"pointer to pointer of struct", pointerOfPointerOfMyStruct},
		{"interface", myInterface(pms)},
		{"slice of int", []int{1, 2, 3}},
		{"slice of any", []any{1, "test"}},
		{"slice of interface with nil", []myInterface{nil, &myStruct{A: "1"}, &myStruct{A: "2"}}},
		{"map string to string", map[string]string{"123": "123", "abc": "abc"}},
		{"map string to interface with nil", map[string]myInterface{"1": nil, "2": pms}},
		{"map string to any with nil", map[string]any{"123": 1, "abc": &myStruct{A: "1"}, "bcd": nil}},
		{"map struct to any complex", map[myStruct]any{
			ms1: 1,
			ms2: &myStruct{A: "2"},
			ms3: nil,
			ms4: []any{
				1,
				pointerOfPointerOfMyStruct,
				"123",
				&myStruct{A: "1"},
				nil,
				map[myStruct]any{
					ms1: 1,
					ms2: nil,
				},
			},
		}},
		{"complex struct", myStruct2{
			A: "123",
			B: &myStruct{A: "test"},
			C: map[string]**myStruct{"a": pointerOfPointerOfMyStruct},
			D: map[myStruct]any{{"a"}: 1},
			E: []any{1, "2", 3},
			f: "",
			G: myStruct3{FieldA: "1"},
			H: nil,
			I: []*myStruct3{{FieldA: "2"}, {FieldA: "3"}},
			J: map[string]myStruct3{"1": {FieldA: "4"}, "2": {FieldA: "5"}},
			K: myStruct4{FieldA: "1"},
			L: []*myStruct4{{FieldA: "2"}, {FieldA: "3"}},
			M: map[string]myStruct4{"1": {FieldA: "4"}, "2": {FieldA: "5"}},
		}},
		{"deeply nested map", map[string]map[string][]map[string][][]string{
			"1": {
				"a": []map[string][][]string{
					{"b": {{"c"}, {"d"}}},
				},
			},
		}},
		{"empty slice of pointers", []*myStruct{}},
		{"empty struct pointer", &myStruct{}},
	}

	for serializerName, s := range getSerializers() {
		t.Run(serializerName, func(t *testing.T) {
			for _, tc := range testCases {
				t.Run(tc.name, func(t *testing.T) {
					data, err := s.Marshal(tc.value)
					require.NoError(t, err, "marshal failed")

					result := reflect.New(reflect.TypeOf(tc.value)).Interface()
					err = s.Unmarshal(data, result)
					require.NoError(t, err, "unmarshal failed")

					assert.Equal(t, tc.value, reflect.ValueOf(result).Elem().Interface())
				})
			}
		})
	}
}

func TestSerialization_CustomMarshaler(t *testing.T) {
	for serializerName, s := range getSerializers() {
		t.Run(serializerName, func(t *testing.T) {
			t.Run("struct with custom marshaler", func(t *testing.T) {
				input := myStruct5{FieldA: "1"}
				data, err := s.Marshal(input)
				require.NoError(t, err)

				result := &myStruct5{}
				err = s.Unmarshal(data, result)
				require.NoError(t, err)
				assert.Equal(t, myStruct5{FieldA: "FieldA"}, *result)
			})

			t.Run("custom marshaler in map[string]any", func(t *testing.T) {
				input := map[string]any{
					"1": myStruct5{FieldA: "1"},
				}
				data, err := s.Marshal(input)
				require.NoError(t, err)

				result := map[string]any{}
				err = s.Unmarshal(data, &result)
				require.NoError(t, err)
				assert.Equal(t, map[string]any{
					"1": myStruct5{FieldA: "FieldA"},
				}, result)
			})
		})
	}
}

func TestSerialization_Unmarshal(t *testing.T) {
	ptr := func(i int) *int { return &i }

	successCases := []struct {
		name        string
		inputValue  any
		outputPtr   func() any
		expectedVal any
	}{
		{
			name:        "simple type",
			inputValue:  123,
			outputPtr:   func() any { return new(int) },
			expectedVal: 123,
		},
		{
			name:        "struct type",
			inputValue:  unmarshalTestStruct{Foo: "hello", Bar: 42},
			outputPtr:   func() any { return new(unmarshalTestStruct) },
			expectedVal: unmarshalTestStruct{Foo: "hello", Bar: 42},
		},
		{
			name:        "pointer to struct",
			inputValue:  &unmarshalTestStruct{Foo: "world", Bar: 99},
			outputPtr:   func() any { return new(*unmarshalTestStruct) },
			expectedVal: &unmarshalTestStruct{Foo: "world", Bar: 99},
		},
		{
			name:        "unmarshal pointer to value",
			inputValue:  &unmarshalTestStruct{Foo: "p2v", Bar: 1},
			outputPtr:   func() any { return new(unmarshalTestStruct) },
			expectedVal: unmarshalTestStruct{Foo: "p2v", Bar: 1},
		},
		{
			name:        "unmarshal value to pointer",
			inputValue:  unmarshalTestStruct{Foo: "v2p", Bar: 2},
			outputPtr:   func() any { return new(*unmarshalTestStruct) },
			expectedVal: &unmarshalTestStruct{Foo: "v2p", Bar: 2},
		},
		{
			name:        "convertible types",
			inputValue:  int32(42),
			outputPtr:   func() any { return new(int64) },
			expectedVal: int64(42),
		},
		{
			name:        "pointer to pointer destination",
			inputValue:  12345,
			outputPtr:   func() any { return new(*int) },
			expectedVal: ptr(12345),
		},
		{
			name:        "unmarshal to any",
			inputValue:  unmarshalTestStruct{Foo: "any", Bar: 101},
			outputPtr:   func() any { return new(any) },
			expectedVal: unmarshalTestStruct{Foo: "any", Bar: 101},
		},
	}

	for serializerName, s := range getSerializers() {
		t.Run(serializerName, func(t *testing.T) {
			t.Run("success cases", func(t *testing.T) {
				for _, tc := range successCases {
					t.Run(tc.name, func(t *testing.T) {
						data, err := s.Marshal(tc.inputValue)
						require.NoError(t, err)

						outputPtr := tc.outputPtr()
						err = s.Unmarshal(data, outputPtr)
						require.NoError(t, err)

						actualVal := reflect.ValueOf(outputPtr).Elem().Interface()
						assert.Equal(t, tc.expectedVal, actualVal)
					})
				}
			})

			t.Run("unmarshal nil pointer", func(t *testing.T) {
				data, err := s.Marshal((*unmarshalTestStruct)(nil))
				require.NoError(t, err)

				var result *unmarshalTestStruct = &unmarshalTestStruct{}
				err = s.Unmarshal(data, &result)
				require.NoError(t, err)
				assert.Nil(t, result)
			})

			t.Run("error cases", func(t *testing.T) {
				data, err := s.Marshal(123)
				require.NoError(t, err)

				t.Run("destination not a pointer", func(t *testing.T) {
					var output int
					err := s.Unmarshal(data, output)
					require.Error(t, err)
					assert.Contains(t, err.Error(), "non-nil pointer")
				})

				t.Run("destination is a nil pointer", func(t *testing.T) {
					var output *int
					err := s.Unmarshal(data, output)
					require.Error(t, err)
					assert.Contains(t, err.Error(), "non-nil pointer")
				})

				t.Run("type mismatch", func(t *testing.T) {
					strData, mErr := s.Marshal("i am a string")
					require.NoError(t, mErr)

					var output int
					err := s.Unmarshal(strData, &output)
					require.Error(t, err)
				})

				t.Run("unconvertible types", func(t *testing.T) {
					intData, mErr := s.Marshal(123)
					require.NoError(t, mErr)

					var output bool
					err := s.Unmarshal(intData, &output)
					require.Error(t, err)
					assert.Contains(t, err.Error(), "cannot assign")
				})
			})
		})
	}
}

func TestSerialization_PrimitiveTypes(t *testing.T) {
	testCases := []struct {
		name  string
		value any
	}{
		{"int", int(42)},
		{"int8", int8(8)},
		{"int16", int16(16)},
		{"int32", int32(32)},
		{"int64", int64(64)},
		{"uint", uint(42)},
		{"uint8", uint8(8)},
		{"uint16", uint16(16)},
		{"uint32", uint32(32)},
		{"uint64", uint64(64)},
		{"float32", float32(3.14)},
		{"float64", float64(3.14159)},
		{"bool true", true},
		{"bool false", false},
		{"string", "hello world"},
		{"empty string", ""},
	}

	for serializerName, s := range getSerializers() {
		t.Run(serializerName, func(t *testing.T) {
			for _, tc := range testCases {
				t.Run(tc.name, func(t *testing.T) {
					data, err := s.Marshal(tc.value)
					require.NoError(t, err)

					result := reflect.New(reflect.TypeOf(tc.value)).Interface()
					err = s.Unmarshal(data, result)
					require.NoError(t, err)

					assert.Equal(t, tc.value, reflect.ValueOf(result).Elem().Interface())
				})
			}
		})
	}
}

func TestSerialization_NilValues(t *testing.T) {
	for serializerName, s := range getSerializers() {
		t.Run(serializerName, func(t *testing.T) {
			t.Run("nil pointer", func(t *testing.T) {
				var input *myStruct = nil
				data, err := s.Marshal(input)
				require.NoError(t, err)

				var result *myStruct
				err = s.Unmarshal(data, &result)
				require.NoError(t, err)
				assert.Nil(t, result)
			})

			t.Run("nil in slice", func(t *testing.T) {
				input := []any{1, nil, "three", nil, 5}
				data, err := s.Marshal(input)
				require.NoError(t, err)

				var result []any
				err = s.Unmarshal(data, &result)
				require.NoError(t, err)
				require.Len(t, result, 5)
				assert.Equal(t, 1, result[0])
				assert.Nil(t, result[1])
				assert.Equal(t, "three", result[2])
				assert.Nil(t, result[3])
				assert.Equal(t, 5, result[4])
			})

			t.Run("nil in map", func(t *testing.T) {
				input := map[string]any{
					"value": 123,
					"nil":   nil,
				}
				data, err := s.Marshal(input)
				require.NoError(t, err)

				var result map[string]any
				err = s.Unmarshal(data, &result)
				require.NoError(t, err)
				assert.Equal(t, 123, result["value"])
				assert.Nil(t, result["nil"])
			})
		})
	}
}

func TestSerialization_EmptyCollections(t *testing.T) {
	for serializerName, s := range getSerializers() {
		t.Run(serializerName, func(t *testing.T) {
			t.Run("empty slice", func(t *testing.T) {
				input := []int{}
				data, err := s.Marshal(input)
				require.NoError(t, err)

				var result []int
				err = s.Unmarshal(data, &result)
				require.NoError(t, err)
				assert.NotNil(t, result)
				assert.Len(t, result, 0)
			})

			t.Run("empty map", func(t *testing.T) {
				input := map[string]any{}
				data, err := s.Marshal(input)
				require.NoError(t, err)

				var result map[string]any
				err = s.Unmarshal(data, &result)
				require.NoError(t, err)
				assert.NotNil(t, result)
				assert.Len(t, result, 0)
			})

			t.Run("empty slice of pointers", func(t *testing.T) {
				input := []*myStruct{}
				data, err := s.Marshal(input)
				require.NoError(t, err)

				var result []*myStruct
				err = s.Unmarshal(data, &result)
				require.NoError(t, err)
				assert.NotNil(t, result)
				assert.Len(t, result, 0)
			})
		})
	}
}

func TestSerialization_PointerTypes(t *testing.T) {
	for serializerName, s := range getSerializers() {
		t.Run(serializerName, func(t *testing.T) {
			t.Run("pointer to struct", func(t *testing.T) {
				input := &myStruct{A: "test"}
				data, err := s.Marshal(input)
				require.NoError(t, err)

				var result *myStruct
				err = s.Unmarshal(data, &result)
				require.NoError(t, err)
				require.NotNil(t, result)
				assert.Equal(t, "test", result.A)
			})

			t.Run("double pointer", func(t *testing.T) {
				value := &myStruct{A: "double"}
				input := &value
				data, err := s.Marshal(input)
				require.NoError(t, err)

				var result **myStruct
				err = s.Unmarshal(data, &result)
				require.NoError(t, err)
				require.NotNil(t, result)
				require.NotNil(t, *result)
				assert.Equal(t, "double", (*result).A)
			})

			t.Run("slice of pointers", func(t *testing.T) {
				input := []*myStruct{
					{A: "first"},
					{A: "second"},
				}
				data, err := s.Marshal(input)
				require.NoError(t, err)

				var result []*myStruct
				err = s.Unmarshal(data, &result)
				require.NoError(t, err)
				require.Len(t, result, 2)
				assert.Equal(t, "first", result[0].A)
				assert.Equal(t, "second", result[1].A)
			})

			t.Run("map with pointer to pointer values", func(t *testing.T) {
				v1 := &myStruct{A: "v1"}
				v2 := &myStruct{A: "v2"}
				input := map[string]**myStruct{
					"a": &v1,
					"b": &v2,
				}
				data, err := s.Marshal(input)
				require.NoError(t, err)

				var result map[string]**myStruct
				err = s.Unmarshal(data, &result)
				require.NoError(t, err)
				require.Len(t, result, 2)
				assert.Equal(t, "v1", (**result["a"]).A)
				assert.Equal(t, "v2", (**result["b"]).A)
			})
		})
	}
}

func TestSerialization_InterfaceTypes(t *testing.T) {
	for serializerName, s := range getSerializers() {
		t.Run(serializerName, func(t *testing.T) {
			t.Run("interface value", func(t *testing.T) {
				var input myInterface = &myStruct{A: "interface"}
				data, err := s.Marshal(input)
				require.NoError(t, err)

				var result myInterface
				err = s.Unmarshal(data, &result)
				require.NoError(t, err)
				require.NotNil(t, result)
				assert.Equal(t, "interface", result.(*myStruct).A)
			})

			t.Run("slice of interfaces with nil", func(t *testing.T) {
				input := []myInterface{
					nil,
					&myStruct{A: "first"},
					&myStruct{A: "second"},
				}
				data, err := s.Marshal(input)
				require.NoError(t, err)

				var result []myInterface
				err = s.Unmarshal(data, &result)
				require.NoError(t, err)
				require.Len(t, result, 3)
				assert.Nil(t, result[0])
				assert.Equal(t, "first", result[1].(*myStruct).A)
				assert.Equal(t, "second", result[2].(*myStruct).A)
			})

			t.Run("map with interface values and nil", func(t *testing.T) {
				input := map[string]myInterface{
					"nil":   nil,
					"value": &myStruct{A: "test"},
				}
				data, err := s.Marshal(input)
				require.NoError(t, err)

				var result map[string]myInterface
				err = s.Unmarshal(data, &result)
				require.NoError(t, err)
				require.Len(t, result, 2)
				assert.Nil(t, result["nil"])
				assert.Equal(t, "test", result["value"].(*myStruct).A)
			})
		})
	}
}

func TestSerialization_MapWithStructKeys(t *testing.T) {
	for serializerName, s := range getSerializers() {
		t.Run(serializerName, func(t *testing.T) {
			input := map[myStruct]int{
				{A: "key1"}: 100,
				{A: "key2"}: 200,
			}
			data, err := s.Marshal(input)
			require.NoError(t, err)

			var result map[myStruct]int
			err = s.Unmarshal(data, &result)
			require.NoError(t, err)
			assert.Equal(t, 100, result[myStruct{A: "key1"}])
			assert.Equal(t, 200, result[myStruct{A: "key2"}])
		})
	}
}
