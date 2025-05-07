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
	"testing"

	"github.com/stretchr/testify/assert"
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
}

func TestSerialization(t *testing.T) {
	GenericRegister[myStruct]("myStruct")
	GenericRegister[myStruct2]("myStruct2")
	GenericRegister[myInterface]("myInterface")
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
	}

	for _, value := range values {
		data, err := Marshal(value)
		assert.NoError(t, err)
		nValue, err := Unmarshal(data)
		assert.NoError(t, err)
		assert.Equal(t, value, nValue)
	}
}

type myStruct3 struct {
	FieldA string
}

func (m *myStruct3) UnmarshalJSON(bytes []byte) error {
	m.FieldA = "FieldA"
	return nil
}

func (m myStruct3) MarshalJSON() ([]byte, error) {
	return []byte("1"), nil
}

func TestMarshalStruct(t *testing.T) {
	assert.NoError(t, GenericRegister[myStruct3]("myStruct3"))
	s := myStruct3{FieldA: "1"}
	data, err := Marshal(s)
	assert.NoError(t, err)
	result, err := Unmarshal(data)
	assert.NoError(t, err)
	assert.Equal(t, myStruct3{FieldA: "FieldA"}, result)

	ma := map[string]any{
		"1": s,
	}
	data, err = Marshal(ma)
	assert.NoError(t, err)
	result, err = Unmarshal(data)
	assert.NoError(t, err)
	assert.Equal(t, map[string]any{
		"1": myStruct3{FieldA: "FieldA"},
	}, result)
}
