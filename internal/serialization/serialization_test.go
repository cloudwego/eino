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
	Register("myStruct", myStruct{})
	Register("myStruct2", myStruct2{})
	GenericRegister[myInterface]("myInterface")
	ms := myStruct{A: "test"}
	pms := &ms
	ppms := &pms
	values := []any{
		10,
		"test",
		ms,
		pms,
		ppms,
		myInterface(pms),
		[]int{1, 2, 3},
		[]any{1, "test"},
		[]myInterface{nil, &myStruct{A: "1"}, &myStruct{A: "2"}},
		map[string]string{"123": "123", "abc": "abc"},
		map[string]myInterface{"1": nil, "2": pms},
		map[string]any{"123": 1, "abc": &myStruct{A: "1"}, "bcd": nil},
		map[myStruct]any{myStruct{A: "1"}: 1, myStruct{A: "2"}: &myStruct{A: "2"}, myStruct{A: "3"}: nil, myStruct{A: "4"}: []any{1, ppms, "123", &myStruct{A: "1"}, nil, map[myStruct]any{myStruct{A: "1"}: 1, myStruct{A: "2"}: nil}}},
		myStruct2{
			A: "123",
			B: &myStruct{A: "test"},
			C: map[string]**myStruct{
				"a": ppms,
			},
			D: map[myStruct]any{{"a"}: 1},
			E: []any{1, "2", 3},
			f: "",
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
