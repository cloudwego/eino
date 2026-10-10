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

package schema

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/internal/serialization"
)

type persistenceExtra struct {
	ID int `json:"id"`
}
type persistenceHolder struct {
	Value any
	Tool  *ToolInfo
	Tools []*ToolInfo
}

func init() {
	Register[*persistenceExtra]()
	RegisterName[persistenceHolder]("test_persistence_holder")
}

func TestJSONSerializers_ToolInfoContract(t *testing.T) {
	tool := &ToolInfo{Name: "test", Extra: map[string]any{
		"custom": &persistenceExtra{ID: 4}, "number": int64(9007199254740993),
		"$type": "_eino_string", "value": "business",
	}}
	wire, err := json.Marshal(tool)
	require.NoError(t, err)
	var expected ToolInfo
	require.NoError(t, json.Unmarshal(wire, &expected))
	for _, serializer := range []Serializer{&HumanReadableSerializer{}, &serialization.InternalSerializer{}} {
		input := persistenceHolder{Value: tool, Tool: tool, Tools: []*ToolInfo{tool}}
		data, err := serializer.Marshal(input)
		require.NoError(t, err)
		var got persistenceHolder
		require.NoError(t, serializer.Unmarshal(data, &got))
		assert.Equal(t, persistenceHolder{Value: &expected, Tool: &expected, Tools: []*ToolInfo{&expected}}, got)
	}
}

func TestHumanReadableSerializer_RegisteredPointer(t *testing.T) {
	serializer := &HumanReadableSerializer{}
	input := persistenceHolder{Value: &persistenceExtra{ID: 4}}
	data, err := serializer.Marshal(input)
	require.NoError(t, err)
	var got persistenceHolder
	require.NoError(t, serializer.Unmarshal(data, &got))
	assert.Equal(t, input, got)
}

func TestToolInfo_JSONRemainsUnannotated(t *testing.T) {
	tool := &ToolInfo{Name: "test", Extra: map[string]any{"custom": &persistenceExtra{ID: 4}}}
	data, err := json.Marshal(tool)
	require.NoError(t, err)
	assert.JSONEq(t, `{"name":"test","extra":{"custom":{"id":4}}}`, string(data))
}

func TestSerializers_ToolInfoLegacyJSON(t *testing.T) {
	serializer := &HumanReadableSerializer{}
	wire := []byte(`{"$type":"*_eino_tool_info","name":"legacy","extra":{"source":"old"},"has_params_one_of":true}`)
	var got *ToolInfo
	require.NoError(t, serializer.Unmarshal(wire, &got))
	assert.Equal(t, &ToolInfo{Name: "legacy", Extra: map[string]any{"source": "old"}, ParamsOneOf: NewParamsOneOfByParams(map[string]*ParameterInfo{})}, got)
}

func TestGobSerializer_ConcreteAndRegisteredInterface(t *testing.T) {
	serializer := &GobSerializer{}
	cases := []any{int64(9007199254740993), "hello", true, &persistenceExtra{ID: 4}}
	for _, value := range cases {
		input := persistenceHolder{Value: value}
		data, err := serializer.Marshal(input)
		require.NoError(t, err)
		var got persistenceHolder
		require.NoError(t, serializer.Unmarshal(data, &got))
		assert.Equal(t, input, got)
	}
	input := persistenceExtra{ID: 7}
	data, err := serializer.Marshal(input)
	require.NoError(t, err)
	var got persistenceExtra
	require.NoError(t, serializer.Unmarshal(data, &got))
	assert.Equal(t, input, got)
}

func TestGobSerializer_Errors(t *testing.T) {
	serializer := &GobSerializer{}
	data, err := serializer.Marshal(42)
	require.NoError(t, err)
	for _, dest := range []any{nil, 42, (*int)(nil)} {
		require.Error(t, serializer.Unmarshal(data, dest))
	}
	var wrong string
	require.Error(t, serializer.Unmarshal(data, &wrong))
	var got int
	require.Error(t, serializer.Unmarshal([]byte("not gob"), &got))
	require.Error(t, serializer.Unmarshal(data[:len(data)-1], &got))
	type unregistered struct{ ID int }
	_, err = serializer.Marshal(persistenceHolder{Value: unregistered{ID: 1}})
	require.Error(t, err)
}

func TestInternalSerializer_ToolInfoLegacyJSON(t *testing.T) {
	serializer := &serialization.InternalSerializer{}
	data := []byte(`{"Type":{"SimpleType":"_eino_tool_info","PointerNum":1},"JSONValue":{"name":"legacy","extra":{"source":"old"},"has_params_one_of":true}}`)
	var got *ToolInfo
	require.NoError(t, serializer.Unmarshal(data, &got))
	assert.Equal(t, &ToolInfo{Name: "legacy", Extra: map[string]any{"source": "old"}, ParamsOneOf: NewParamsOneOfByParams(map[string]*ParameterInfo{})}, got)
}
