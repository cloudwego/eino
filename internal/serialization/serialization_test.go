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

func TestSerialization(t *testing.T) {
	_ = GenericRegister[myStruct]("myStruct")
	_ = GenericRegister[myStruct2]("myStruct2")
	_ = GenericRegister[myInterface]("myInterface")
	ms := myStruct{A: "test"}
	pms := &ms
	pointerOfPointerOfMyStruct := &pms

	ms1 := myStruct{A: "1"}
	ms2 := myStruct{A: "2"}
	ms3 := myStruct{A: "3"}
	ms4 := myStruct{A: "4"}
	values := []any{
		10,
		"test",
		ms,
		pms,
		pointerOfPointerOfMyStruct,
		myInterface(pms),
		[]int{1, 2, 3},
		[]any{1, "test"},
		[]myInterface{nil, &myStruct{A: "1"}, &myStruct{A: "2"}},
		map[string]string{"123": "123", "abc": "abc"},
		map[string]myInterface{"1": nil, "2": pms},
		map[string]any{"123": 1, "abc": &myStruct{A: "1"}, "bcd": nil},
		map[myStruct]any{
			ms1: 1,
			ms2: &myStruct{
				A: "2",
			},
			ms3: nil,
			ms4: []any{
				1,
				pointerOfPointerOfMyStruct,
				"123", &myStruct{
					A: "1",
				},
				nil,
				map[myStruct]any{
					ms1: 1,
					ms2: nil,
				},
			},
		},
		myStruct2{
			A: "123",
			B: &myStruct{
				A: "test",
			},
			C: map[string]**myStruct{
				"a": pointerOfPointerOfMyStruct,
			},
			D: map[myStruct]any{{"a"}: 1},
			E: []any{1, "2", 3},
			f: "",
			G: myStruct3{
				FieldA: "1",
			},
			H: nil,
			I: []*myStruct3{
				{FieldA: "2"}, {FieldA: "3"},
			},
			J: map[string]myStruct3{
				"1": {FieldA: "4"},
				"2": {FieldA: "5"},
			},
			K: myStruct4{
				FieldA: "1",
			},
			L: []*myStruct4{
				{FieldA: "2"}, {FieldA: "3"},
			},
			M: map[string]myStruct4{
				"1": {FieldA: "4"},
				"2": {FieldA: "5"},
			},
		},
		map[string]map[string][]map[string][][]string{
			"1": {
				"a": []map[string][][]string{
					{"b": {
						{"c"},
						{"d"},
					}},
				},
			},
		},
		[]*myStruct{},
		&myStruct{},
	}

	for _, value := range values {
		data, err := (&InternalSerializer{}).Marshal(value)
		assert.NoError(t, err)
		v := reflect.New(reflect.TypeOf(value)).Interface()
		err = (&InternalSerializer{}).Unmarshal(data, v)
		assert.NoError(t, err)
		assert.Equal(t, value, reflect.ValueOf(v).Elem().Interface())
	}
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

func TestMarshalStruct(t *testing.T) {
	assert.NoError(t, GenericRegister[myStruct5]("myStruct5"))
	s := myStruct5{FieldA: "1"}
	data, err := (&InternalSerializer{}).Marshal(s)
	assert.NoError(t, err)
	result := &myStruct5{}
	err = (&InternalSerializer{}).Unmarshal(data, result)
	assert.NoError(t, err)
	assert.Equal(t, myStruct5{FieldA: "FieldA"}, *result)

	ma := map[string]any{
		"1": s,
	}
	data, err = (&InternalSerializer{}).Marshal(ma)
	assert.NoError(t, err)
	result2 := map[string]any{}
	err = (&InternalSerializer{}).Unmarshal(data, &result2)
	assert.NoError(t, err)
	assert.Equal(t, map[string]any{
		"1": myStruct5{FieldA: "FieldA"},
	}, result2)
}

type unmarshalTestStruct struct {
	Foo string
	Bar int
}

func init() {
	// Register types for the serializer to work.
	// This is necessary for the serializer to know how to handle custom struct types.
	err := GenericRegister[unmarshalTestStruct]("unmarshalTestStruct")
	if err != nil {
		panic(err)
	}
}

func TestInternalSerializer_Unmarshal(t *testing.T) {
	s := InternalSerializer{}

	t.Run("success cases", func(t *testing.T) {
		// Helper to create a pointer to a value, needed for the expected value in one test case.
		ptr := func(i int) *int { return &i }

		testCases := []struct {
			name        string
			inputValue  any
			outputPtr   any
			expectedVal any
		}{
			{
				name:        "simple type",
				inputValue:  123,
				outputPtr:   new(int),
				expectedVal: 123,
			},
			{
				name:        "struct type",
				inputValue:  unmarshalTestStruct{Foo: "hello", Bar: 42},
				outputPtr:   new(unmarshalTestStruct),
				expectedVal: unmarshalTestStruct{Foo: "hello", Bar: 42},
			},
			{
				name:        "pointer to struct",
				inputValue:  &unmarshalTestStruct{Foo: "world", Bar: 99},
				outputPtr:   new(*unmarshalTestStruct),
				expectedVal: &unmarshalTestStruct{Foo: "world", Bar: 99},
			},
			{
				name:        "unmarshal pointer to value",
				inputValue:  &unmarshalTestStruct{Foo: "p2v", Bar: 1},
				outputPtr:   new(unmarshalTestStruct),
				expectedVal: unmarshalTestStruct{Foo: "p2v", Bar: 1},
			},
			{
				name:        "unmarshal value to pointer",
				inputValue:  unmarshalTestStruct{Foo: "v2p", Bar: 2},
				outputPtr:   new(*unmarshalTestStruct),
				expectedVal: &unmarshalTestStruct{Foo: "v2p", Bar: 2},
			},
			{
				name:        "unmarshal nil pointer",
				inputValue:  (*unmarshalTestStruct)(nil),
				outputPtr:   &struct{ v *unmarshalTestStruct }{v: &unmarshalTestStruct{}}, // placeholder to be replaced
				expectedVal: (*unmarshalTestStruct)(nil),
			},
			{
				name:        "convertible types",
				inputValue:  int32(42),
				outputPtr:   new(int64),
				expectedVal: int64(42),
			},
			{
				name:        "pointer to pointer destination",
				inputValue:  12345,
				outputPtr:   new(*int),
				expectedVal: ptr(12345),
			},
			{
				name:        "unmarshal to any",
				inputValue:  unmarshalTestStruct{Foo: "any", Bar: 101},
				outputPtr:   new(any),
				expectedVal: unmarshalTestStruct{Foo: "any", Bar: 101},
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				data, err := s.Marshal(tc.inputValue)
				require.NoError(t, err)

				// Special handling for the nil test case to correctly pass the pointer.
				if tc.name == "unmarshal nil pointer" {
					target := tc.outputPtr.(*struct{ v *unmarshalTestStruct })
					err = s.Unmarshal(data, &target.v)
					require.NoError(t, err)
					assert.Nil(t, target.v)
					return
				}

				err = s.Unmarshal(data, tc.outputPtr)
				require.NoError(t, err)

				// Dereference the pointer to get the actual value for comparison.
				actualVal := reflect.ValueOf(tc.outputPtr).Elem().Interface()
				assert.Equal(t, tc.expectedVal, actualVal)
			})
		}
	})

	t.Run("error cases", func(t *testing.T) {
		data, err := s.Marshal(123)
		require.NoError(t, err)

		t.Run("destination not a pointer", func(t *testing.T) {
			var output int
			err := s.Unmarshal(data, output)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "value must be a non-nil pointer")
		})

		t.Run("destination is a nil pointer", func(t *testing.T) {
			var output *int // nil
			err := s.Unmarshal(data, output)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "value must be a non-nil pointer")
		})

		t.Run("type mismatch", func(t *testing.T) {
			strData, mErr := s.Marshal("i am a string")
			require.NoError(t, mErr)

			var output int
			err := s.Unmarshal(strData, &output)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "cannot assign")
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
}
