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
    "reflect"
    "strings"
    "testing"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

// ===== Mock type replacing schema.ToolInfo (pointer-receiver MarshalJSON) =====

type hrMockToolInfo struct {
    Name   string
    Desc   string
    params map[string]string // unexported — only via MarshalJSON
}

type hrMockToolInfoJSON struct {
    Name      string            `json:"name"`
    Desc      string            `json:"desc"`
    HasParams bool              `json:"has_params"`
    Params    map[string]string `json:"params,omitempty"`
}

func (t *hrMockToolInfo) MarshalJSON() ([]byte, error) {
    tmp := &hrMockToolInfoJSON{Name: t.Name, Desc: t.Desc}
    if t.params != nil {
        tmp.HasParams = true
        tmp.Params = t.params
    }
    return json.Marshal(tmp)
}

func (t *hrMockToolInfo) UnmarshalJSON(data []byte) error {
    tmp := &hrMockToolInfoJSON{}
    if err := json.Unmarshal(data, tmp); err != nil {
        return err
    }
    t.Name = tmp.Name
    t.Desc = tmp.Desc
    if tmp.HasParams {
        t.params = tmp.Params
    }
    return nil
}

func newMockToolInfo(name, desc string, params map[string]string) *hrMockToolInfo {
    return &hrMockToolInfo{Name: name, Desc: desc, params: params}
}

// Holder types for position tests.
type hrMockToolInfoConcreteHolder struct {
    T *hrMockToolInfo `json:"t"`
}
type hrMockToolInfoInterfaceHolder struct {
    V any `json:"v"`
}
type hrMockToolInfoSliceHolder struct {
    S []*hrMockToolInfo `json:"s"`
}
type hrMockToolInfoMapHolder struct {
    M map[string]*hrMockToolInfo `json:"m"`
}

// ===== Existing fixture types =====

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

type hrZeroValueStruct struct {
    S string `json:"s"`
    I int    `json:"i"`
    B bool   `json:"b"`
}

// ===== Edge-case fixture types =====

type hrEdgeArrayHolder struct {
    A [3]int    `json:"a"`
    B [2]string `json:"b"`
    I any       `json:"i"`
}

type hrEdgeIntKeyMap struct {
    M map[int]string `json:"m"`
}

type hrEdgeStructKeyMap struct {
    M map[hrEdgeKey]string `json:"m"`
}

type hrEdgeKey struct {
    K1 string `json:"k1"`
    K2 int    `json:"k2"`
}

type hrEdgePtrLevels struct {
    P *int   `json:"p"`
    Q **int  `json:"q"`
    R ***int `json:"r"`
}

type hrEdgeNestedSlicePtr struct {
    S []*hrEdgeAtom          `json:"s"`
    M map[string]*hrEdgeAtom `json:"m"`
}

type hrEdgeAtom struct {
    N int `json:"n"`
}

type hrEdgeNumericConvert struct {
    I8  int8    `json:"i8"`
    I16 int16   `json:"i16"`
    I32 int32   `json:"i32"`
    U8  uint8   `json:"u8"`
    U16 uint16  `json:"u16"`
    U32 uint32  `json:"u32"`
    F32 float32 `json:"f32"`
}

type hrEdgeFancyJSON struct {
    V hrJSONMarshaler `json:"v"`
}

type hrJSONMarshaler struct {
    Inner string
}

func (m hrJSONMarshaler) MarshalJSON() ([]byte, error) {
    return []byte(`"prefix:` + m.Inner + `"`), nil
}

func (m *hrJSONMarshaler) UnmarshalJSON(data []byte) error {
    s := strings.Trim(string(data), `"`)
    m.Inner = strings.TrimPrefix(s, "prefix:")
    return nil
}

type hrEdgeIgnoreField struct {
    A string `json:"a"`
    B string `json:"-"`
    C string
    d string //nolint:unused // intentional: unexported field probes filtering
}

type hrEdgeAnyContainer struct {
    V any `json:"v"`
}

type hrEdgeUnregisteredField struct {
    V hrUnregisteredInner `json:"v"`
}

// hrUnregisteredInner is intentionally NOT passed to GenericRegister so we can
// observe how the serializer treats concrete-typed (non-interface) fields whose
// type isn't registered.
type hrUnregisteredInner struct {
    N int `json:"n"`
}

// hrUnregisteredHere is intentionally never registered. Used only in
// TestHumanReadableSerializer_MarshalErrors.
type hrUnregisteredHere struct {
    X int
}

// ===== init: type registrations =====

func init() {
    // Basic fixture types.
    _ = GenericRegister[hrTestStruct]("hr_test_struct")
    _ = GenericRegister[hrTestStructWithExtra]("hr_test_struct_with_extra")
    _ = GenericRegister[hrStructWithInterface]("hr_struct_with_interface")
    _ = GenericRegister[hrWrapper]("hr_wrapper")
    _ = GenericRegister[hrLargeIntegerStruct]("hr_large_integer_struct")
    _ = GenericRegister[hrReservedTypeStruct]("hr_reserved_type_struct")
    _ = GenericRegister[hrZeroValueStruct]("hr_zero_value_struct")

    // Edge-case types.
    _ = GenericRegister[hrEdgeArrayHolder]("hr_edge_array_holder")
    _ = GenericRegister[[3]int]("hr_edge_array_3_int")
    _ = GenericRegister[[2]string]("hr_edge_array_2_string")
    _ = GenericRegister[hrEdgeIntKeyMap]("hr_edge_int_key_map")
    _ = GenericRegister[hrEdgeStructKeyMap]("hr_edge_struct_key_map")
    _ = GenericRegister[hrEdgeKey]("hr_edge_key")
    _ = GenericRegister[hrEdgePtrLevels]("hr_edge_ptr_levels")
    _ = GenericRegister[hrEdgeNestedSlicePtr]("hr_edge_nested_slice_ptr")
    _ = GenericRegister[hrEdgeAtom]("hr_edge_atom")
    _ = GenericRegister[hrEdgeNumericConvert]("hr_edge_numeric_convert")
    _ = GenericRegister[hrEdgeFancyJSON]("hr_edge_fancy_json")
    _ = GenericRegister[hrJSONMarshaler]("hr_edge_json_marshaler")
    _ = GenericRegister[hrEdgeIgnoreField]("hr_edge_ignore_field")
    _ = GenericRegister[hrEdgeAnyContainer]("hr_edge_any_container")

    // Mock ToolInfo types.
    _ = GenericRegister[hrMockToolInfo]("hr_mock_tool_info")
    _ = GenericRegister[hrMockToolInfoConcreteHolder]("hr_mock_tool_info_concrete_holder")
    _ = GenericRegister[hrMockToolInfoInterfaceHolder]("hr_mock_tool_info_interface_holder")
    _ = GenericRegister[hrMockToolInfoSliceHolder]("hr_mock_tool_info_slice_holder")
    _ = GenericRegister[hrMockToolInfoMapHolder]("hr_mock_tool_info_map_holder")
}

// =============================================================================
// Section: Basic serialization behavior
// =============================================================================

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

func TestHumanReadableSerializer_NonOmitEmptyZeroValuesAreScalars(t *testing.T) {
    s := &HumanReadableSerializer{}
    input := hrZeroValueStruct{}

    data, err := s.Marshal(input)
    require.NoError(t, err)

    var raw map[string]any
    err = json.Unmarshal(data, &raw)
    require.NoError(t, err)
    assert.Equal(t, "", raw["s"])
    assert.Equal(t, float64(0), raw["i"])
    assert.Equal(t, false, raw["b"])

    var result hrZeroValueStruct
    err = s.Unmarshal(data, &result)
    require.NoError(t, err)
    assert.Equal(t, input, result)
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

// =============================================================================
// Section: Type annotations ($type envelope)
// =============================================================================

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

    t.Run("struct field with registered value", func(t *testing.T) {
        input := hrReservedTypeStruct{
            Type: "_eino_string",
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

// =============================================================================
// Section: Pointer-receiver MarshalJSON (mock ToolInfo regression)
// =============================================================================

func TestHumanReadableSerializer_PtrReceiverMarshalJSON_TopLevel(t *testing.T) {
    s := &HumanReadableSerializer{}

    original := newMockToolInfo("search", "search the docs", map[string]string{"q": "query"})

    data, err := s.Marshal(original)
    require.NoError(t, err)

    // The wire format must come from MarshalJSON (lowercase tags from hrMockToolInfoJSON).
    var raw map[string]any
    require.NoError(t, json.Unmarshal(data, &raw))
    assert.Equal(t, "search", raw["name"], "must use MarshalJSON's lowercase 'name' tag")
    assert.Equal(t, "search the docs", raw["desc"])
    assert.Equal(t, true, raw["has_params"], "MarshalJSON must record has_params=true")
    require.Contains(t, raw, "params", "MarshalJSON must include params")

    var got hrMockToolInfo
    require.NoError(t, s.Unmarshal(data, &got))
    assert.Equal(t, original.Name, got.Name)
    assert.Equal(t, original.Desc, got.Desc)
    assert.Equal(t, original.params, got.params, "unexported params must round-trip via MarshalJSON/UnmarshalJSON")
}

func TestHumanReadableSerializer_PtrReceiverMarshalJSON_ValueType(t *testing.T) {
    s := &HumanReadableSerializer{}

    // Pass by value (not pointer) — exercises the addressability shim in hrMarshalStruct.
    tiVal := hrMockToolInfo{
        Name:   "value-type",
        Desc:   "no pointer",
        params: map[string]string{"x": "y"},
    }

    data, err := s.Marshal(tiVal)
    require.NoError(t, err)

    // The wire format must come from MarshalJSON (lowercase tags).
    var raw map[string]any
    require.NoError(t, json.Unmarshal(data, &raw))
    assert.Equal(t, "value-type", raw["name"],
        "value-type hrMockToolInfo must still go through pointer-receiver MarshalJSON via the addressability shim")
    assert.Equal(t, true, raw["has_params"])

    // Round-trip into a value target.
    var got hrMockToolInfo
    require.NoError(t, s.Unmarshal(data, &got))
    assert.Equal(t, "value-type", got.Name)
    assert.Equal(t, map[string]string{"x": "y"}, got.params)
}

func TestHumanReadableSerializer_PtrReceiverMarshalJSON_NoParams(t *testing.T) {
    s := &HumanReadableSerializer{}

    original := newMockToolInfo("ping", "no-arg tool", nil)

    data, err := s.Marshal(original)
    require.NoError(t, err)

    var raw map[string]any
    require.NoError(t, json.Unmarshal(data, &raw))
    assert.Equal(t, false, raw["has_params"], "nil params → has_params=false")
    _, hasParams := raw["params"]
    assert.False(t, hasParams, "nil params → params field must be absent (omitempty)")

    var got hrMockToolInfo
    require.NoError(t, s.Unmarshal(data, &got))
    assert.Equal(t, "ping", got.Name)
    assert.Equal(t, "no-arg tool", got.Desc)
    assert.Nil(t, got.params, "absent params must remain nil")
}

func TestHumanReadableSerializer_PtrReceiverMarshalJSON_InConcreteField(t *testing.T) {
    s := &HumanReadableSerializer{}
    holder := hrMockToolInfoConcreteHolder{
        T: newMockToolInfo("search", "search docs", map[string]string{"q": "query"}),
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

    var got hrMockToolInfoConcreteHolder
    require.NoError(t, s.Unmarshal(data, &got))
    require.NotNil(t, got.T)
    assert.Equal(t, holder.T.Name, got.T.Name)
    assert.Equal(t, holder.T.params, got.T.params)
}

func TestHumanReadableSerializer_PtrReceiverMarshalJSON_InInterfaceField(t *testing.T) {
    s := &HumanReadableSerializer{}
    holder := hrMockToolInfoInterfaceHolder{
        V: newMockToolInfo("search", "in interface", map[string]string{"q": "query"}),
    }

    data, err := s.Marshal(holder)
    require.NoError(t, err)

    // Interface fields must include the $type envelope.
    var raw map[string]any
    require.NoError(t, json.Unmarshal(data, &raw))
    vMap, ok := raw["v"].(map[string]any)
    require.True(t, ok)
    assert.Equal(t, "*hr_mock_tool_info", vMap["$type"],
        "interface field with *hrMockToolInfo must carry the registered type tag")

    var got hrMockToolInfoInterfaceHolder
    require.NoError(t, s.Unmarshal(data, &got))

    gotTI, ok := got.V.(*hrMockToolInfo)
    require.True(t, ok, "interface field must reconstruct as *hrMockToolInfo, got %T", got.V)
    assert.Equal(t, "search", gotTI.Name)
    assert.Equal(t, map[string]string{"q": "query"}, gotTI.params, "params must round-trip through interface field")
}

func TestHumanReadableSerializer_PtrReceiverMarshalJSON_InSlice(t *testing.T) {
    s := &HumanReadableSerializer{}
    holder := hrMockToolInfoSliceHolder{
        S: []*hrMockToolInfo{
            newMockToolInfo("t1", "first", nil),
            newMockToolInfo("t2", "second", map[string]string{"x": "1"}),
            nil, // nil pointer in slice — must round-trip as nil.
        },
    }

    data, err := s.Marshal(holder)
    require.NoError(t, err)

    var got hrMockToolInfoSliceHolder
    require.NoError(t, s.Unmarshal(data, &got))
    require.Len(t, got.S, 3)
    require.NotNil(t, got.S[0])
    assert.Equal(t, "t1", got.S[0].Name)
    assert.Nil(t, got.S[0].params)
    require.NotNil(t, got.S[1])
    assert.Equal(t, map[string]string{"x": "1"}, got.S[1].params)
    assert.Nil(t, got.S[2], "nil entry in slice must round-trip as nil")
}

func TestHumanReadableSerializer_PtrReceiverMarshalJSON_InMap(t *testing.T) {
    s := &HumanReadableSerializer{}
    holder := hrMockToolInfoMapHolder{
        M: map[string]*hrMockToolInfo{
            "alpha":    newMockToolInfo("alpha", "first", nil),
            "beta":     newMockToolInfo("beta", "second", map[string]string{"y": "2"}),
            "nilEntry": nil,
        },
    }

    data, err := s.Marshal(holder)
    require.NoError(t, err)

    var got hrMockToolInfoMapHolder
    require.NoError(t, s.Unmarshal(data, &got))
    require.Len(t, got.M, 3)
    require.NotNil(t, got.M["alpha"])
    assert.Equal(t, "alpha", got.M["alpha"].Name)
    require.NotNil(t, got.M["beta"])
    assert.Equal(t, map[string]string{"y": "2"}, got.M["beta"].params)
    assert.Nil(t, got.M["nilEntry"])
}

func TestHumanReadableSerializer_PtrReceiverMarshalJSON_NilPointer(t *testing.T) {
    s := &HumanReadableSerializer{}
    var nilTI *hrMockToolInfo

    data, err := s.Marshal(nilTI)
    require.NoError(t, err)
    assert.Equal(t, "null", string(data), "nil pointer must marshal to JSON null")

    var got *hrMockToolInfo
    require.NoError(t, s.Unmarshal(data, &got))
    assert.Nil(t, got)
}

// =============================================================================
// Section: Fixed-size arrays
// =============================================================================

func TestHumanReadableSerializer_FixedSizeArrayRoundTrip(t *testing.T) {
    s := &HumanReadableSerializer{}
    input := hrEdgeArrayHolder{
        A: [3]int{10, 20, 30},
        B: [2]string{"x", "y"},
    }

    data, err := s.Marshal(input)
    require.NoError(t, err)

    var got hrEdgeArrayHolder
    require.NoError(t, s.Unmarshal(data, &got))
    assert.Equal(t, input, got)
}

func TestHumanReadableSerializer_ArrayInInterfaceField(t *testing.T) {
    s := &HumanReadableSerializer{}
    input := hrEdgeArrayHolder{
        I: [3]int{1, 2, 3},
    }

    data, err := s.Marshal(input)
    require.NoError(t, err)

    // Verify $type annotation includes the array shape.
    var raw map[string]any
    require.NoError(t, json.Unmarshal(data, &raw))
    iMap, ok := raw["i"].(map[string]any)
    require.True(t, ok, "interface field must serialize with type envelope")
    require.Contains(t, iMap, "$type")
    assert.Contains(t, iMap["$type"], "[3]")

    var got hrEdgeArrayHolder
    require.NoError(t, s.Unmarshal(data, &got))
    assert.Equal(t, input.I, got.I)
}

func TestHumanReadableSerializer_ArrayWithExtraJSONElementsTruncates(t *testing.T) {
    s := &HumanReadableSerializer{}

    original := hrEdgeArrayHolder{A: [3]int{1, 2, 3}}
    data, err := s.Marshal(original)
    require.NoError(t, err)

    var raw map[string]any
    require.NoError(t, json.Unmarshal(data, &raw))
    raw["a"] = []any{json.Number("11"), json.Number("22"), json.Number("33"), json.Number("44"), json.Number("55")}

    tampered, err := json.Marshal(raw)
    require.NoError(t, err)

    var got hrEdgeArrayHolder
    require.NoError(t, s.Unmarshal(tampered, &got))
    assert.Equal(t, [3]int{11, 22, 33}, got.A,
        "extra JSON elements beyond array length must be silently dropped")
}

// =============================================================================
// Section: Non-string map keys
// =============================================================================

func TestHumanReadableSerializer_IntegerMapKeys(t *testing.T) {
    s := &HumanReadableSerializer{}
    input := hrEdgeIntKeyMap{M: map[int]string{1: "one", 2: "two", 42: "forty-two"}}

    data, err := s.Marshal(input)
    require.NoError(t, err)

    var got hrEdgeIntKeyMap
    require.NoError(t, s.Unmarshal(data, &got))
    assert.Equal(t, input, got)
}

func TestHumanReadableSerializer_StructMapKeys(t *testing.T) {
    s := &HumanReadableSerializer{}
    input := hrEdgeStructKeyMap{
        M: map[hrEdgeKey]string{
            {K1: "alpha", K2: 1}: "first",
            {K1: "beta", K2: 2}:  "second",
        },
    }

    data, err := s.Marshal(input)
    require.NoError(t, err)

    var got hrEdgeStructKeyMap
    require.NoError(t, s.Unmarshal(data, &got))
    assert.Equal(t, input, got)
}

// =============================================================================
// Section: Pointer indirection
// =============================================================================

func TestHumanReadableSerializer_MultiLevelPointers(t *testing.T) {
    s := &HumanReadableSerializer{}
    v1 := 7
    pv1 := &v1
    ppv1 := &pv1
    input := hrEdgePtrLevels{P: &v1, Q: &pv1, R: &ppv1}

    data, err := s.Marshal(input)
    require.NoError(t, err)

    var got hrEdgePtrLevels
    require.NoError(t, s.Unmarshal(data, &got))
    require.NotNil(t, got.P)
    require.NotNil(t, got.Q)
    require.NotNil(t, got.R)
    assert.Equal(t, 7, *got.P)
    assert.Equal(t, 7, **got.Q)
    assert.Equal(t, 7, ***got.R)
}

func TestHumanReadableSerializer_NilPointerFieldIsAbsent(t *testing.T) {
    s := &HumanReadableSerializer{}
    input := hrEdgePtrLevels{P: nil, Q: nil, R: nil}

    data, err := s.Marshal(input)
    require.NoError(t, err)

    var got hrEdgePtrLevels
    require.NoError(t, s.Unmarshal(data, &got))
    assert.Nil(t, got.P)
    assert.Nil(t, got.Q)
    assert.Nil(t, got.R)
}

func TestHumanReadableSerializer_SliceAndMapOfPointers(t *testing.T) {
    s := &HumanReadableSerializer{}
    input := hrEdgeNestedSlicePtr{
        S: []*hrEdgeAtom{{N: 1}, nil, {N: 3}},
        M: map[string]*hrEdgeAtom{
            "a": {N: 10},
            "b": nil,
        },
    }

    data, err := s.Marshal(input)
    require.NoError(t, err)

    var got hrEdgeNestedSlicePtr
    require.NoError(t, s.Unmarshal(data, &got))
    require.Equal(t, len(input.S), len(got.S))
    for i := range input.S {
        if input.S[i] == nil {
            assert.Nil(t, got.S[i], "nil slice element[%d] must round-trip as nil", i)
        } else {
            require.NotNil(t, got.S[i])
            assert.Equal(t, *input.S[i], *got.S[i])
        }
    }
    require.Equal(t, len(input.M), len(got.M))
    require.NotNil(t, got.M["a"])
    assert.Equal(t, 10, got.M["a"].N)
    assert.Nil(t, got.M["b"], "nil map value must round-trip as nil")
}

// =============================================================================
// Section: Numeric types
// =============================================================================

func TestHumanReadableSerializer_IntegerExtremes(t *testing.T) {
    type holder struct {
        MinI64 int64  `json:"min_i64"`
        MaxI64 int64  `json:"max_i64"`
        MaxU64 uint64 `json:"max_u64"`
        AnyI64 any    `json:"any_i64"`
        AnyU64 any    `json:"any_u64"`
    }
    _ = GenericRegister[holder]("hr_edge_int_extremes_holder")

    s := &HumanReadableSerializer{}
    input := holder{
        MinI64: -1 << 63,
        MaxI64: 1<<63 - 1,
        MaxU64: ^uint64(0),
        AnyI64: int64(-1 << 62),
        AnyU64: uint64(1<<63 + 1),
    }

    data, err := s.Marshal(input)
    require.NoError(t, err)

    var got holder
    require.NoError(t, s.Unmarshal(data, &got))
    assert.Equal(t, input.MinI64, got.MinI64)
    assert.Equal(t, input.MaxI64, got.MaxI64)
    assert.Equal(t, input.MaxU64, got.MaxU64)
    assert.Equal(t, input.AnyI64, got.AnyI64)
    assert.Equal(t, input.AnyU64, got.AnyU64)
}

func TestHumanReadableSerializer_NumericFieldTypesRoundTrip(t *testing.T) {
    s := &HumanReadableSerializer{}
    input := hrEdgeNumericConvert{
        I8:  -8,
        I16: -16,
        I32: -32,
        U8:  8,
        U16: 16,
        U32: 32,
        F32: 1.5,
    }

    data, err := s.Marshal(input)
    require.NoError(t, err)

    var got hrEdgeNumericConvert
    require.NoError(t, s.Unmarshal(data, &got))
    assert.Equal(t, input, got)
}

func TestHumanReadableSerializer_NumericOverflowDecodeError(t *testing.T) {
    s := &HumanReadableSerializer{}

    // 200 doesn't fit in int8 (-128..127).
    tampered := []byte(`{"i8":200,"i16":0,"i32":0,"u8":0,"u16":0,"u32":0,"f32":0}`)

    var got hrEdgeNumericConvert
    err := s.Unmarshal(tampered, &got)
    require.Error(t, err, "must reject numeric overflow rather than silently truncating")
    assert.Contains(t, err.Error(), "I8")
    assert.Contains(t, err.Error(), "200")
}

func TestHumanReadableSerializer_HighPrecisionFloats(t *testing.T) {
    type holder struct {
        F  float64 `json:"f"`
        F2 float64 `json:"f2"`
        A  any     `json:"a"`
    }
    _ = GenericRegister[holder]("hr_edge_precision_floats_holder")

    s := &HumanReadableSerializer{}
    input := holder{
        F:  1.7976931348623157e+308, // near math.MaxFloat64
        F2: 5.0e-324,                // near smallest positive subnormal
        A:  float64(3.141592653589793),
    }

    data, err := s.Marshal(input)
    require.NoError(t, err)

    var got holder
    require.NoError(t, s.Unmarshal(data, &got))
    assert.Equal(t, input.F, got.F)
    assert.Equal(t, input.F2, got.F2)
    assert.Equal(t, input.A, got.A)
}

// =============================================================================
// Section: Interface field primitives
// =============================================================================

func TestHumanReadableSerializer_AnyFieldPrimitives(t *testing.T) {
    s := &HumanReadableSerializer{}

    cases := []struct {
        name     string
        raw      string
        expected any
    }{
        {"int via json.Number", `{"v":42}`, int(42)},
        {"float via json.Number", `{"v":3.14}`, 3.14},
        {"exponent float", `{"v":1e2}`, 100.0},
        {"large uint via json.Number", `{"v":18446744073709551610}`, uint64(18446744073709551610)},
        {"string", `{"v":"hello"}`, "hello"},
        {"bool true", `{"v":true}`, true},
        {"bool false", `{"v":false}`, false},
    }

    for _, c := range cases {
        t.Run(c.name, func(t *testing.T) {
            var got hrEdgeAnyContainer
            require.NoError(t, s.Unmarshal([]byte(c.raw), &got))
            assert.Equal(t, c.expected, got.V, "raw=%s", c.raw)
        })
    }
}

func TestHumanReadableSerializer_ConvertJSONPrimitive_DefaultBranch(t *testing.T) {
    // A bool reaches convertJSONPrimitive's default arm.
    assert.Equal(t, true, convertJSONPrimitive(true))
    assert.Equal(t, "abc", convertJSONPrimitive("abc"))
    // A nil reaches the default arm too.
    assert.Equal(t, nil, convertJSONPrimitive(nil))
    // A non-integer float64 must round-trip as float64.
    assert.Equal(t, 3.5, convertJSONPrimitive(float64(3.5)))
    // An integer-valued float64 collapses to int.
    assert.Equal(t, int(7), convertJSONPrimitive(float64(7)))
}

// =============================================================================
// Section: Custom MarshalJSON (simple value-type marshaler)
// =============================================================================

func TestHumanReadableSerializer_CustomJSONMarshaler(t *testing.T) {
    s := &HumanReadableSerializer{}
    input := hrEdgeFancyJSON{V: hrJSONMarshaler{Inner: "hello"}}

    data, err := s.Marshal(input)
    require.NoError(t, err)

    // The inner value should serialize as the marshaler's chosen output.
    assert.Contains(t, string(data), "prefix:hello")

    var got hrEdgeFancyJSON
    require.NoError(t, s.Unmarshal(data, &got))
    assert.Equal(t, input, got)
}

// =============================================================================
// Section: Field handling
// =============================================================================

func TestHumanReadableSerializer_JSONDashAndUnexportedFields(t *testing.T) {
    s := &HumanReadableSerializer{}
    input := hrEdgeIgnoreField{A: "shown", B: "hidden", C: "default"}

    data, err := s.Marshal(input)
    require.NoError(t, err)

    var raw map[string]any
    require.NoError(t, json.Unmarshal(data, &raw))
    assert.Equal(t, "shown", raw["a"])
    _, hasB := raw["B"]
    assert.False(t, hasB, `json:"-" field must not be serialized`)
    assert.Equal(t, "default", raw["C"])

    var got hrEdgeIgnoreField
    require.NoError(t, s.Unmarshal(data, &got))
    assert.Equal(t, "shown", got.A)
    assert.Equal(t, "", got.B, `json:"-" field must remain zero on decode`)
    assert.Equal(t, "default", got.C)
}

func TestHumanReadableSerializer_StructFieldFallbackToFieldName(t *testing.T) {
    s := &HumanReadableSerializer{}

    // Hand-craft JSON using the Go field name (no tag). hrUnmarshalStruct should
    // look up `data[fieldName]` first, then fall back to `data[field.Name]`.
    raw := []byte(`{"Name":"x","value":99}`)
    var got hrTestStruct
    require.NoError(t, s.Unmarshal(raw, &got))
    assert.Equal(t, "x", got.Name)
    assert.Equal(t, 99, got.Value)
}

func TestHumanReadableSerializer_ConcreteFieldDoesNotRequireRegistration(t *testing.T) {
    _ = GenericRegister[hrEdgeUnregisteredField]("hr_edge_unregistered_field")

    s := &HumanReadableSerializer{}
    input := hrEdgeUnregisteredField{V: hrUnregisteredInner{N: 7}}

    data, err := s.Marshal(input)
    require.NoError(t, err, "concrete struct field shouldn't require its element type to be registered")

    var got hrEdgeUnregisteredField
    require.NoError(t, s.Unmarshal(data, &got))
    assert.Equal(t, input, got)
}

// =============================================================================
// Section: Error paths
// =============================================================================

func TestHumanReadableSerializer_UnmarshalErrors(t *testing.T) {
    s := &HumanReadableSerializer{}

    t.Run("corrupt JSON", func(t *testing.T) {
        var got hrTestStruct
        err := s.Unmarshal([]byte(`{"name":`), &got)
        require.Error(t, err)
        assert.Contains(t, err.Error(), "unmarshal JSON")
    })

    t.Run("nil pointer target", func(t *testing.T) {
        var ptr *hrTestStruct
        err := s.Unmarshal([]byte(`{}`), ptr)
        require.Error(t, err)
        assert.Contains(t, err.Error(), "non-nil pointer")
    })

    t.Run("non-pointer target", func(t *testing.T) {
        var v hrTestStruct
        err := s.Unmarshal([]byte(`{}`), v)
        require.Error(t, err)
        assert.Contains(t, err.Error(), "non-nil pointer")
    })

    t.Run("unknown $type", func(t *testing.T) {
        var got hrEdgeAnyContainer
        err := s.Unmarshal([]byte(`{"v":{"$type":"this_type_is_not_registered","value":1}}`), &got)
        // shouldTreatAsTypeEnvelope returns false for unknown type names, so the
        // payload is passed through as a plain map[string]any.
        require.NoError(t, err)
        m, ok := got.V.(map[string]any)
        require.True(t, ok)
        assert.Equal(t, "this_type_is_not_registered", m["$type"])
    })

    t.Run("typed envelope with bad inner data", func(t *testing.T) {
        var got hrEdgeAnyContainer
        // `_eino_int` expects a numeric value; a JSON object cannot decode into int.
        err := s.Unmarshal([]byte(`{"v":{"$type":"_eino_int","value":{"oops":1}}}`), &got)
        require.Error(t, err)
    })

    t.Run("array on a non-slice/array target", func(t *testing.T) {
        var got hrTestStruct
        err := s.Unmarshal([]byte(`[1,2,3]`), &got)
        require.Error(t, err)
        assert.Contains(t, err.Error(), "cannot unmarshal slice")
    })

    t.Run("object on a non-map/struct target", func(t *testing.T) {
        var got int
        err := s.Unmarshal([]byte(`{"a":1}`), &got)
        require.Error(t, err)
    })
}

func TestHumanReadableSerializer_MarshalErrors(t *testing.T) {
    s := &HumanReadableSerializer{}

    t.Run("unregistered type via interface field", func(t *testing.T) {
        input := hrEdgeAnyContainer{V: hrUnregisteredHere{X: 1}}
        _, err := s.Marshal(input)
        require.Error(t, err)
        assert.Contains(t, err.Error(), "unknown type")
    })

    t.Run("array of unregistered element via interface field", func(t *testing.T) {
        input := hrEdgeAnyContainer{V: [2]hrUnregisteredHere{{X: 1}, {X: 2}}}
        _, err := s.Marshal(input)
        require.Error(t, err)
    })
}

// =============================================================================
// Section: Top-level primitives
// =============================================================================

func TestHumanReadableSerializer_TopLevelPrimitives(t *testing.T) {
    s := &HumanReadableSerializer{}

    t.Run("int", func(t *testing.T) {
        data, err := s.Marshal(int(42))
        require.NoError(t, err)
        var got int
        require.NoError(t, s.Unmarshal(data, &got))
        assert.Equal(t, 42, got)
    })

    t.Run("float", func(t *testing.T) {
        data, err := s.Marshal(3.14)
        require.NoError(t, err)
        var got float64
        require.NoError(t, s.Unmarshal(data, &got))
        assert.InDelta(t, 3.14, got, 1e-9)
    })

    t.Run("string", func(t *testing.T) {
        data, err := s.Marshal("hello")
        require.NoError(t, err)
        var got string
        require.NoError(t, s.Unmarshal(data, &got))
        assert.Equal(t, "hello", got)
    })

    t.Run("[]int", func(t *testing.T) {
        data, err := s.Marshal([]int{1, 2, 3})
        require.NoError(t, err)
        var got []int
        require.NoError(t, s.Unmarshal(data, &got))
        assert.Equal(t, []int{1, 2, 3}, got)
    })

    t.Run("map[string]int", func(t *testing.T) {
        data, err := s.Marshal(map[string]int{"a": 1, "b": 2})
        require.NoError(t, err)
        var got map[string]int
        require.NoError(t, s.Unmarshal(data, &got))
        assert.Equal(t, map[string]int{"a": 1, "b": 2}, got)
    })
}

// =============================================================================
// Section: Internal helpers
// =============================================================================

func TestIsEmptyValue_AllKinds(t *testing.T) {
    cases := []struct {
        name string
        v    any
        want bool
    }{
        {"empty string", "", true},
        {"non-empty string", "x", false},
        {"empty slice", []int{}, true},
        {"non-empty slice", []int{1}, false},
        {"nil slice", []int(nil), true},
        {"empty map", map[string]int{}, true},
        {"non-empty map", map[string]int{"a": 1}, false},
        {"empty array", [0]int{}, true},
        {"non-empty array", [3]int{1, 2, 3}, false},
        {"false bool", false, true},
        {"true bool", true, false},
        {"int 0", int(0), true},
        {"int non-zero", int(5), false},
        {"int8 0", int8(0), true},
        {"uint 0", uint(0), true},
        {"uint64 0", uint64(0), true},
        {"float64 0", float64(0), true},
        {"float64 non-zero", float64(0.5), false},
        {"nil pointer", (*int)(nil), true},
        {"non-nil pointer", func() any { v := 1; return &v }(), false},
        {"nil interface", any(nil), true},
        // Channel hits the `default: return false` branch.
        {"channel (default branch)", make(chan int), false},
        {"func (default branch)", func() {}, false},
    }
    for _, c := range cases {
        t.Run(c.name, func(t *testing.T) {
            rv := reflect.ValueOf(c.v)
            if !rv.IsValid() {
                assert.Equal(t, c.want, true)
                return
            }
            got := isEmptyValue(rv)
            assert.Equal(t, c.want, got)
        })
    }
}

func TestSetValueWithConversion_AllPaths(t *testing.T) {
    t.Run("invalid source sets zero", func(t *testing.T) {
        var dst int = 99
        target := reflect.ValueOf(&dst).Elem()
        ok := setValueWithConversion(target, reflect.Value{})
        assert.True(t, ok)
        assert.Equal(t, 0, dst, "invalid source must zero the target")
    })

    t.Run("ptr target nil → allocates", func(t *testing.T) {
        var p *int
        target := reflect.ValueOf(&p).Elem()
        ok := setValueWithConversion(target, reflect.ValueOf(42))
        assert.True(t, ok)
        require.NotNil(t, p)
        assert.Equal(t, 42, *p)
    })

    t.Run("ptr source nil → target zeroed", func(t *testing.T) {
        var src *int
        var dst int = 99
        target := reflect.ValueOf(&dst).Elem()
        ok := setValueWithConversion(target, reflect.ValueOf(src))
        assert.True(t, ok)
        assert.Equal(t, 0, dst)
    })

    t.Run("ptr source non-nil → deref then set", func(t *testing.T) {
        v := 7
        var dst int
        target := reflect.ValueOf(&dst).Elem()
        ok := setValueWithConversion(target, reflect.ValueOf(&v))
        assert.True(t, ok)
        assert.Equal(t, 7, dst)
    })

    t.Run("convertible types", func(t *testing.T) {
        var dst int32
        target := reflect.ValueOf(&dst).Elem()
        ok := setValueWithConversion(target, reflect.ValueOf(int64(100)))
        assert.True(t, ok)
        assert.Equal(t, int32(100), dst)
    })

    t.Run("float64 → int", func(t *testing.T) {
        var dst int
        target := reflect.ValueOf(&dst).Elem()
        ok := setValueWithConversion(target, reflect.ValueOf(float64(7.0)))
        assert.True(t, ok)
        assert.Equal(t, 7, dst)
    })

    t.Run("int → int (different bit widths)", func(t *testing.T) {
        var dst int64
        target := reflect.ValueOf(&dst).Elem()
        ok := setValueWithConversion(target, reflect.ValueOf(int(42)))
        assert.True(t, ok)
        assert.Equal(t, int64(42), dst)
    })

    t.Run("float64 → uint", func(t *testing.T) {
        var dst uint
        target := reflect.ValueOf(&dst).Elem()
        ok := setValueWithConversion(target, reflect.ValueOf(float64(8)))
        assert.True(t, ok)
        assert.Equal(t, uint(8), dst)
    })

    t.Run("int → float", func(t *testing.T) {
        var dst float64
        target := reflect.ValueOf(&dst).Elem()
        ok := setValueWithConversion(target, reflect.ValueOf(int(12)))
        assert.True(t, ok)
        assert.Equal(t, float64(12), dst)
    })

    t.Run("incompatible types return false", func(t *testing.T) {
        var dst struct{ A int }
        target := reflect.ValueOf(&dst).Elem()
        ok := setValueWithConversion(target, reflect.ValueOf("not a struct"))
        assert.False(t, ok)
    })
}

func TestGetJSONFieldName_Variants(t *testing.T) {
    assert.Equal(t, "Name", getJSONFieldName("Name", ""))
    assert.Equal(t, "alias", getJSONFieldName("Name", "alias"))
    assert.Equal(t, "alias", getJSONFieldName("Name", "alias,omitempty"))
    // Empty primary part (only ",omitempty") falls back to field name.
    assert.Equal(t, "Name", getJSONFieldName("Name", ",omitempty"))
}

func TestParseTypeName_Errors(t *testing.T) {
    t.Run("unknown plain type", func(t *testing.T) {
        _, _, err := parseTypeName("not_registered")
        require.Error(t, err)
    })

    t.Run("malformed array missing close bracket", func(t *testing.T) {
        _, _, err := parseTypeName("[3 _eino_int")
        require.Error(t, err)
        assert.Contains(t, err.Error(), "invalid array")
    })

    t.Run("array with bad size", func(t *testing.T) {
        _, _, err := parseTypeName("[abc]_eino_int")
        require.Error(t, err)
    })

    t.Run("array with unknown elem", func(t *testing.T) {
        _, _, err := parseTypeName("[3]not_registered")
        require.Error(t, err)
    })

    t.Run("slice with unknown elem", func(t *testing.T) {
        _, _, err := parseTypeName("[]not_registered")
        require.Error(t, err)
    })

    t.Run("map with unknown key type", func(t *testing.T) {
        _, _, err := parseTypeName("map[not_registered]_eino_string")
        require.Error(t, err)
        assert.Contains(t, err.Error(), "key")
    })

    t.Run("map with unknown value type", func(t *testing.T) {
        _, _, err := parseTypeName("map[_eino_string]not_registered")
        require.Error(t, err)
        assert.Contains(t, err.Error(), "value")
    })

    t.Run("nested map with pointer key/value", func(t *testing.T) {
        rt, ptr, err := parseTypeName("map[*_eino_string]*_eino_int")
        require.NoError(t, err)
        assert.Equal(t, uint32(0), ptr)
        assert.Equal(t, reflect.Map, rt.Kind())
        assert.Equal(t, reflect.Ptr, rt.Key().Kind())
        assert.Equal(t, reflect.Ptr, rt.Elem().Kind())
    })

    t.Run("pointer to slice", func(t *testing.T) {
        rt, ptr, err := parseTypeName("*[]_eino_int")
        require.NoError(t, err)
        assert.Equal(t, uint32(1), ptr)
        assert.Equal(t, reflect.Slice, rt.Kind())
    })
}

func TestGetTypeName_AllShapes(t *testing.T) {
    // Plain registered.
    n, err := getTypeName(reflect.TypeOf(int(0)))
    require.NoError(t, err)
    assert.Equal(t, "_eino_int", n)

    // Pointer.
    n, err = getTypeName(reflect.TypeOf((*int)(nil)))
    require.NoError(t, err)
    assert.Equal(t, "*_eino_int", n)

    // Slice.
    n, err = getTypeName(reflect.TypeOf([]int{}))
    require.NoError(t, err)
    assert.Equal(t, "[]_eino_int", n)

    // Array.
    n, err = getTypeName(reflect.TypeOf([3]int{}))
    require.NoError(t, err)
    assert.Equal(t, "[3]_eino_int", n)

    // Map.
    n, err = getTypeName(reflect.TypeOf(map[string]int{}))
    require.NoError(t, err)
    assert.Equal(t, "map[_eino_string]_eino_int", n)

    // Unregistered.
    type unreg struct{}
    _, err = getTypeName(reflect.TypeOf(unreg{}))
    require.Error(t, err)

    // Slice with unregistered elem.
    _, err = getTypeName(reflect.TypeOf([]unreg{}))
    require.Error(t, err)

    // Array with unregistered elem.
    _, err = getTypeName(reflect.TypeOf([3]unreg{}))
    require.Error(t, err)

    // Map with unregistered key.
    _, err = getTypeName(reflect.TypeOf(map[unreg]int{}))
    require.Error(t, err)

    // Map with unregistered value.
    _, err = getTypeName(reflect.TypeOf(map[string]unreg{}))
    require.Error(t, err)
}

// =============================================================================
// Section: Protocol safety
// =============================================================================

func TestHumanReadableSerializer_NilSliceField(t *testing.T) {
    type holder struct {
        S []int `json:"s"`
    }
    _ = GenericRegister[holder]("hr_edge_nil_slice_holder")

    s := &HumanReadableSerializer{}
    input := holder{S: nil}

    data, err := s.Marshal(input)
    require.NoError(t, err)

    var raw map[string]any
    require.NoError(t, json.Unmarshal(data, &raw))
    assert.Nil(t, raw["s"], "nil slice must serialize as JSON null")

    var got holder
    require.NoError(t, s.Unmarshal(data, &got))
    assert.Nil(t, got.S, "JSON null must decode back to nil slice")
}

func TestHumanReadableSerializer_NilMapField(t *testing.T) {
    type holder struct {
        M map[string]int `json:"m"`
    }
    _ = GenericRegister[holder]("hr_edge_nil_map_holder")

    s := &HumanReadableSerializer{}
    input := holder{M: nil}

    data, err := s.Marshal(input)
    require.NoError(t, err)

    var got holder
    require.NoError(t, s.Unmarshal(data, &got))
    assert.Nil(t, got.M)
}

func TestHumanReadableSerializer_MapWithUnregisteredValueInInterface(t *testing.T) {
    type unregValue struct{ N int }
    type holder struct {
        V any `json:"v"`
    }
    _ = GenericRegister[holder]("hr_edge_unreg_map_value_holder")

    s := &HumanReadableSerializer{}
    input := holder{V: map[string]unregValue{"a": {N: 1}}}

    _, err := s.Marshal(input)
    require.Error(t, err, "map with unregistered value type in interface field must error")
}

func TestHumanReadableSerializer_StringWithSpecialCharacters(t *testing.T) {
    type holder struct {
        S string `json:"s"`
        A any    `json:"a"`
    }
    _ = GenericRegister[holder]("hr_edge_special_chars_holder")

    cases := []string{
        `"quotes"`,
        `back\slash`,
        "newline\nand\ttab",
        "unicode 你好 🚀",
        "control\x01\x02\x03",
        "",
        "$type:should-not-confuse-parser",
    }
    s := &HumanReadableSerializer{}
    for _, c := range cases {
        t.Run(c, func(t *testing.T) {
            input := holder{S: c, A: c}
            data, err := s.Marshal(input)
            require.NoError(t, err)
            var got holder
            require.NoError(t, s.Unmarshal(data, &got))
            assert.Equal(t, input.S, got.S)
            assert.Equal(t, input.A, got.A)
        })
    }
}

func TestHumanReadableSerializer_DeepRecursion(t *testing.T) {
    type node struct {
        V    int   `json:"v"`
        Next *node `json:"next,omitempty"`
    }
    _ = GenericRegister[node]("hr_edge_deep_node")

    s := &HumanReadableSerializer{}

    // Build a chain of 50 nodes.
    const depth = 50
    root := &node{V: 0}
    cur := root
    for i := 1; i < depth; i++ {
        cur.Next = &node{V: i}
        cur = cur.Next
    }

    data, err := s.Marshal(root)
    require.NoError(t, err)

    var got node
    require.NoError(t, s.Unmarshal(data, &got))

    // Walk and verify all values.
    cur = &got
    for i := 0; i < depth; i++ {
        require.NotNil(t, cur, "node at depth %d", i)
        assert.Equal(t, i, cur.V)
        cur = cur.Next
    }
}
