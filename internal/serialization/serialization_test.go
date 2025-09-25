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

	"github.com/cloudwego/eino/schema"
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
		myStruct2{
			A: &schema.Message{}, // test empty fields
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
