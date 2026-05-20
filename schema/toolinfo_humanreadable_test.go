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

package schema_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/cloudwego/eino/internal/serialization"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	jsonschemalib "github.com/eino-contrib/jsonschema"
)

// holder fixtures used to exercise ToolInfo in different positions.
type concreteToolInfoHolder struct {
	T *schema.ToolInfo `json:"t"`
}

type interfaceToolInfoHolder struct {
	V any `json:"v"`
}

type sliceToolInfoHolder struct {
	S []*schema.ToolInfo `json:"s"`
}

type mapToolInfoHolder struct {
	M map[string]*schema.ToolInfo `json:"m"`
}

func init() {
	_ = serialization.GenericRegister[concreteToolInfoHolder]("concrete_tool_info_holder")
	_ = serialization.GenericRegister[interfaceToolInfoHolder]("interface_tool_info_holder")
	_ = serialization.GenericRegister[sliceToolInfoHolder]("slice_tool_info_holder")
	_ = serialization.GenericRegister[mapToolInfoHolder]("map_tool_info_holder")
}

// TestToolInfoHRS_TopLevelPtr_RoundTrip is the regression test for the
// pointer-receiver MarshalJSON bug: hrMarshalStruct must use the addressable
// form so (*ToolInfo).MarshalJSON is invoked. Without the fix, ParamsOneOf's
// unexported `params` and `jsonschema` fields are silently lost on round-trip.
func TestToolInfoHRS_TopLevelPtr_RoundTrip(t *testing.T) {
	s := &schema.HumanReadableSerializer{}

	original := &schema.ToolInfo{
		Name:  "search",
		Desc:  "search the docs",
		Extra: map[string]any{"hint": "use keywords"},
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"q": {Type: schema.String, Desc: "query", Required: true},
		}),
	}

	data, err := s.Marshal(original)
	require.NoError(t, err)

	// The wire format must come from MarshalJSON (lowercase tags), not from
	// reflection (which would emit "Name"/"Desc" with capitals and drop ParamsOneOf).
	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	assert.Equal(t, "search", raw["name"], "must use MarshalJSON's lowercase 'name' tag")
	assert.Equal(t, true, raw["has_params_one_of"], "MarshalJSON must record HasParamsOneOf=true")
	require.Contains(t, raw, "params", "MarshalJSON must include params")

	var got schema.ToolInfo
	require.NoError(t, s.Unmarshal(data, &got))
	assert.Equal(t, original.Name, got.Name)
	assert.Equal(t, original.Desc, got.Desc)
	assert.Equal(t, original.Extra, got.Extra)
	require.NotNil(t, got.ParamsOneOf, "ParamsOneOf must not be nil after round-trip")

	originalJS, err := original.ParamsOneOf.ToJSONSchema()
	require.NoError(t, err)
	gotJS, err := got.ParamsOneOf.ToJSONSchema()
	require.NoError(t, err)
	assert.True(t, reflect.DeepEqual(originalJS, gotJS),
		"ParamsOneOf must produce a byte-identical JSON schema after round-trip")
}

// TestToolInfoHRS_NoParams covers the simplest case: a tool with no parameters.
func TestToolInfoHRS_NoParams(t *testing.T) {
	s := &schema.HumanReadableSerializer{}
	original := &schema.ToolInfo{Name: "ping", Desc: "no-arg tool"}
	data, err := s.Marshal(original)
	require.NoError(t, err)
	var got schema.ToolInfo
	require.NoError(t, s.Unmarshal(data, &got))
	assert.Equal(t, "ping", got.Name)
	assert.Equal(t, "no-arg tool", got.Desc)
	assert.Nil(t, got.ParamsOneOf, "absent ParamsOneOf must remain nil")
}

// TestToolInfoHRS_JSONSchema covers the alternate ParamsOneOf representation.
func TestToolInfoHRS_JSONSchema(t *testing.T) {
	s := &schema.HumanReadableSerializer{}

	src := &jsonschemalib.Schema{
		Type:       "object",
		Properties: jsonschemalib.NewProperties(),
	}
	src.Properties.Set("name", &jsonschemalib.Schema{Type: "string"})
	src.Required = []string{"name"}

	original := &schema.ToolInfo{
		Name:        "create",
		Desc:        "create a thing",
		ParamsOneOf: schema.NewParamsOneOfByJSONSchema(src),
	}

	data, err := s.Marshal(original)
	require.NoError(t, err)

	var got schema.ToolInfo
	require.NoError(t, s.Unmarshal(data, &got))
	require.NotNil(t, got.ParamsOneOf)

	gotJS, err := got.ParamsOneOf.ToJSONSchema()
	require.NoError(t, err)
	assert.True(t, reflect.DeepEqual(src, gotJS),
		"json-schema-based ParamsOneOf must round-trip byte-identically")
}

// TestToolInfoHRS_NestedParams covers ParameterInfo with nested SubParams,
// arrays via ElemInfo, and Enum constraints — exercises the full ParameterInfo
// surface through MarshalJSON.
func TestToolInfoHRS_NestedParams(t *testing.T) {
	s := &schema.HumanReadableSerializer{}
	original := &schema.ToolInfo{
		Name: "complex",
		Desc: "tool with nested parameters",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"filter": {
				Type: schema.Object,
				Desc: "filter object",
				SubParams: map[string]*schema.ParameterInfo{
					"status": {Type: schema.String, Enum: []string{"ok", "fail"}, Required: true},
					"limit":  {Type: schema.Integer, Required: false},
				},
				Required: true,
			},
			"tags": {
				Type:     schema.Array,
				ElemInfo: &schema.ParameterInfo{Type: schema.String},
				Required: false,
			},
		}),
	}

	data, err := s.Marshal(original)
	require.NoError(t, err)

	var got schema.ToolInfo
	require.NoError(t, s.Unmarshal(data, &got))

	originalJS, err := original.ParamsOneOf.ToJSONSchema()
	require.NoError(t, err)
	gotJS, err := got.ParamsOneOf.ToJSONSchema()
	require.NoError(t, err)
	assert.True(t, reflect.DeepEqual(originalJS, gotJS),
		"nested ParameterInfo must round-trip byte-identically through ToJSONSchema")
}

// TestToolInfoHRS_InConcreteField verifies ToolInfo as a non-interface struct
// field (the most common position).
func TestToolInfoHRS_InConcreteField(t *testing.T) {
	s := &schema.HumanReadableSerializer{}
	holder := concreteToolInfoHolder{
		T: &schema.ToolInfo{
			Name: "search",
			Desc: "search docs",
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"q": {Type: schema.String, Required: true},
			}),
		},
	}

	data, err := s.Marshal(holder)
	require.NoError(t, err)

	// Concrete fields don't carry a $type envelope.
	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	tMap, ok := raw["t"].(map[string]any)
	require.True(t, ok)
	_, hasType := tMap["$type"]
	assert.False(t, hasType, "concrete pointer field should not carry a $type envelope")
	assert.Equal(t, "search", tMap["name"], "must still go through MarshalJSON")

	var got concreteToolInfoHolder
	require.NoError(t, s.Unmarshal(data, &got))
	require.NotNil(t, got.T)
	assert.Equal(t, holder.T.Name, got.T.Name)
	require.NotNil(t, got.T.ParamsOneOf)
}

// TestToolInfoHRS_InInterfaceField verifies the typed-envelope path: a
// *ToolInfo placed in an `any` field must serialize with $type and resolve
// back through hrUnmarshalTyped on decode.
func TestToolInfoHRS_InInterfaceField(t *testing.T) {
	s := &schema.HumanReadableSerializer{}
	holder := interfaceToolInfoHolder{
		V: &schema.ToolInfo{
			Name: "search",
			Desc: "in interface",
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"q": {Type: schema.String, Required: true},
			}),
		},
	}

	data, err := s.Marshal(holder)
	require.NoError(t, err)

	// Interface fields must include the $type envelope so the decoder reconstructs the concrete type.
	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	vMap, ok := raw["v"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "*_eino_tool_info", vMap["$type"],
		"interface field with *ToolInfo must carry the registered type tag")

	var got interfaceToolInfoHolder
	require.NoError(t, s.Unmarshal(data, &got))

	// V must come back as *schema.ToolInfo with all fields preserved.
	gotTI, ok := got.V.(*schema.ToolInfo)
	require.True(t, ok, "interface field must reconstruct as *schema.ToolInfo, got %T", got.V)
	assert.Equal(t, "search", gotTI.Name)
	require.NotNil(t, gotTI.ParamsOneOf)
}

// TestToolInfoHRS_InSlice verifies a slice of ToolInfo round-trips.
func TestToolInfoHRS_InSlice(t *testing.T) {
	s := &schema.HumanReadableSerializer{}
	holder := sliceToolInfoHolder{
		S: []*schema.ToolInfo{
			{Name: "t1", Desc: "first"},
			{Name: "t2", Desc: "second", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"x": {Type: schema.String, Required: true},
			})},
			nil, // nil pointer in slice — must round-trip as nil.
		},
	}

	data, err := s.Marshal(holder)
	require.NoError(t, err)

	var got sliceToolInfoHolder
	require.NoError(t, s.Unmarshal(data, &got))
	require.Len(t, got.S, 3)
	require.NotNil(t, got.S[0])
	assert.Equal(t, "t1", got.S[0].Name)
	require.NotNil(t, got.S[1])
	require.NotNil(t, got.S[1].ParamsOneOf)
	assert.Nil(t, got.S[2], "nil entry in slice must round-trip as nil")
}

// TestToolInfoHRS_InMap verifies a map[string]*ToolInfo round-trips.
func TestToolInfoHRS_InMap(t *testing.T) {
	s := &schema.HumanReadableSerializer{}
	holder := mapToolInfoHolder{
		M: map[string]*schema.ToolInfo{
			"alpha": {Name: "alpha", Desc: "first"},
			"beta": {Name: "beta", Desc: "second", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"y": {Type: schema.Integer, Required: false},
			})},
			"nilEntry": nil,
		},
	}

	data, err := s.Marshal(holder)
	require.NoError(t, err)

	var got mapToolInfoHolder
	require.NoError(t, s.Unmarshal(data, &got))
	require.Len(t, got.M, 3)
	require.NotNil(t, got.M["alpha"])
	assert.Equal(t, "alpha", got.M["alpha"].Name)
	require.NotNil(t, got.M["beta"].ParamsOneOf)
	assert.Nil(t, got.M["nilEntry"])
}

// TestToolInfoHRS_NilPointer verifies that a nil *ToolInfo at the top level
// short-circuits cleanly without invoking MarshalJSON on a nil receiver.
func TestToolInfoHRS_NilPointer(t *testing.T) {
	s := &schema.HumanReadableSerializer{}
	var nilTI *schema.ToolInfo

	data, err := s.Marshal(nilTI)
	require.NoError(t, err)
	assert.Equal(t, "null", string(data), "nil pointer must marshal to JSON null")

	var got *schema.ToolInfo
	require.NoError(t, s.Unmarshal(data, &got))
	assert.Nil(t, got)
}

// TestToolInfoHRS_ValueTypeStillUsesMarshalJSON verifies that even a ToolInfo
// passed by value (not pointer) goes through MarshalJSON. The fix uses
// reflect.New + Elem.Set when the value isn't addressable, which is exactly
// this case (a value type passed directly to Marshal).
func TestToolInfoHRS_ValueTypeStillUsesMarshalJSON(t *testing.T) {
	s := &schema.HumanReadableSerializer{}

	tiVal := schema.ToolInfo{
		Name: "value-type",
		Desc: "no pointer",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"x": {Type: schema.String, Required: true},
		}),
	}

	data, err := s.Marshal(tiVal)
	require.NoError(t, err)

	// The wire format must come from MarshalJSON (lowercase tags).
	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	assert.Equal(t, "value-type", raw["name"],
		"value-type ToolInfo must still go through pointer-receiver MarshalJSON via the addressability shim")
	assert.Equal(t, true, raw["has_params_one_of"])

	// Round-trip into a value target.
	var got schema.ToolInfo
	require.NoError(t, s.Unmarshal(data, &got))
	require.NotNil(t, got.ParamsOneOf)
	assert.Equal(t, "value-type", got.Name)
}

// TestToolInfoHRS_ExtraPreservesPrimitives — Extra is map[string]any. JSON's
// standard marshaling collapses int → float64 on decode. We document the
// observed round-trip behavior here so callers know what to expect when
// putting non-string primitives in Extra.
func TestToolInfoHRS_ExtraPreservesPrimitives(t *testing.T) {
	s := &schema.HumanReadableSerializer{}
	original := &schema.ToolInfo{
		Name: "extra-test",
		Extra: map[string]any{
			"str":  "hello",
			"bool": true,
			"int":  int(42),
			"flt":  3.14,
		},
	}
	data, err := s.Marshal(original)
	require.NoError(t, err)

	var got schema.ToolInfo
	require.NoError(t, s.Unmarshal(data, &got))

	// String, bool, and float survive byte-for-byte.
	assert.Equal(t, "hello", got.Extra["str"])
	assert.Equal(t, true, got.Extra["bool"])
	assert.Equal(t, 3.14, got.Extra["flt"])

	// Documented limitation: integers go through ToolInfo's standard json
	// MarshalJSON, which encodes them as JSON numbers. On decode, the standard
	// json package reads them as float64 by default. Callers that need exact
	// integer fidelity for Extra should use a typed wrapper rather than relying
	// on ToolInfo's Extra map.
	switch v := got.Extra["int"].(type) {
	case float64:
		assert.Equal(t, float64(42), v)
	case int:
		assert.Equal(t, 42, v)
	default:
		t.Fatalf("unexpected type for Extra[int]: %T", v)
	}
}
