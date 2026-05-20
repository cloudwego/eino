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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ----------------- Type fixtures for edge-case tests -----------------

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
// type isn't registered. Concrete fields shouldn't need registration.
type hrUnregisteredInner struct {
	N int `json:"n"`
}

func init() {
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
}

// ===== parseArrayType / hrUnmarshalSlice (array path) / getTypeName (array) =====

// TestHumanReadableSerializer_FixedSizeArrayRoundTrip exercises the array path
// across the entire pipeline: marshal embeds the [3]int into the wire format
// (covering getTypeName's array branch and hrMarshalSlice for arrays), and
// unmarshal must drive parseArrayType and hrUnmarshalSlice's array branch.
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

// TestHumanReadableSerializer_ArrayInInterfaceField forces the typed-envelope
// path for arrays. The concrete value is `[3]int` stored in an `any` field, so
// marshal must emit `$type:"[3]_eino_int"` and unmarshal must drive
// parseArrayType + array-path of hrUnmarshalSlice.
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

// TestHumanReadableSerializer_ArrayWithExtraJSONElementsTruncates verifies the
// "if i >= dResult.Len() { break }" guard in hrUnmarshalSlice's array path: a
// shorter array target must safely truncate extra JSON elements rather than
// panicking on an out-of-bounds index write.
func TestHumanReadableSerializer_ArrayWithExtraJSONElementsTruncates(t *testing.T) {
	s := &HumanReadableSerializer{}

	// Marshal a [3]int holder, then craft a payload that has 5 elements for "a".
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

// ===== Non-string map keys =====

// TestHumanReadableSerializer_IntegerMapKeys covers hrMarshalMap's non-string
// key branch (sonic.Marshal of the key) and hrUnmarshalMapValue's non-string
// keyType path that calls sonic.UnmarshalString for the key.
func TestHumanReadableSerializer_IntegerMapKeys(t *testing.T) {
	s := &HumanReadableSerializer{}
	input := hrEdgeIntKeyMap{M: map[int]string{1: "one", 2: "two", 42: "forty-two"}}

	data, err := s.Marshal(input)
	require.NoError(t, err)

	var got hrEdgeIntKeyMap
	require.NoError(t, s.Unmarshal(data, &got))
	assert.Equal(t, input, got)
}

// TestHumanReadableSerializer_StructMapKeys covers the JSON-marshaled struct
// key path (composite keys with their own fields).
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

// ===== Pointer indirection =====

// TestHumanReadableSerializer_MultiLevelPointers covers wrapPointers across
// multiple indirections (*int, **int, ***int) in both directions.
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

// TestHumanReadableSerializer_NilPointerFieldIsAbsent covers the
// "rv.IsNil() inside pointer-deref loop" early return in hrMarshal.
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

// TestHumanReadableSerializer_SliceAndMapOfPointers covers concrete-typed
// (non-interface) collections of pointer elements — common in production code.
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

// ===== Numeric type conversions in hrUnmarshalPrimitive =====

// TestHumanReadableSerializer_NumericFieldTypesRoundTrip exercises every
// integer/float subtype that goes through hrUnmarshalPrimitive's specific
// json.Number branches and the float64-fallback conversion paths.
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

// TestHumanReadableSerializer_NumericOverflowDecodeError verifies that decoding
// a JSON number that doesn't fit the destination type produces a typed error
// (not silent truncation) — critical for serialization protocol safety.
func TestHumanReadableSerializer_NumericOverflowDecodeError(t *testing.T) {
	s := &HumanReadableSerializer{}

	// 200 doesn't fit in int8 (-128..127).
	tampered := []byte(`{"i8":200,"i16":0,"i32":0,"u8":0,"u16":0,"u32":0,"f32":0}`)

	var got hrEdgeNumericConvert
	err := s.Unmarshal(tampered, &got)
	require.Error(t, err, "must reject numeric overflow rather than silently truncating")
	// The error wraps the Go field name (I8) and the failing source value (200).
	assert.Contains(t, err.Error(), "I8")
	assert.Contains(t, err.Error(), "200")
}

// ===== Interface{} field with primitives =====

// TestHumanReadableSerializer_AnyFieldPrimitives covers hrUnmarshalPrimitive's
// interface-target path that calls convertJSONPrimitive — including json.Number
// disambiguation between int / uint / float.
func TestHumanReadableSerializer_AnyFieldPrimitives(t *testing.T) {
	s := &HumanReadableSerializer{}

	// Marshal a map[string]any directly — these reach convertJSONPrimitive on decode.
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

// TestHumanReadableSerializer_ConvertJSONPrimitive_DefaultBranch covers the
// `default` arm of convertJSONPrimitive (non-number, non-float64 goes through
// unchanged).
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

// ===== Custom MarshalJSON / UnmarshalJSON =====

// TestHumanReadableSerializer_CustomJSONMarshaler covers the checkMarshaler
// branches in both hrMarshalStruct and hrUnmarshalStruct.
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

// ===== Field-handling edge cases =====

// TestHumanReadableSerializer_JSONDashAndUnexportedFields verifies that
// json:"-" fields are excluded from output AND ignored on input; unexported
// fields are not serialized at all.
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

// TestHumanReadableSerializer_StructFieldFallbackToFieldName verifies the
// fallback in hrUnmarshalStruct: when JSON has "Name" but tag says "name", or
// vice versa.
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

// ===== Concrete (unregistered) struct fields =====

// TestHumanReadableSerializer_ConcreteFieldDoesNotRequireRegistration verifies
// that concrete-typed struct fields (not interface{}) round-trip even if their
// element type isn't in the registry. Only interface fields need registration.
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

// ===== Error paths — corrupted input, type mismatch =====

// TestHumanReadableSerializer_UnmarshalErrors enumerates failure modes that
// should produce typed errors (not panics) — critical for protocol safety.
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
		// payload is passed through as a plain map[string]any. This is the
		// documented best-effort behavior.
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

// TestHumanReadableSerializer_MarshalErrors verifies that marshaling
// unsupported values fails with a clean error.
func TestHumanReadableSerializer_MarshalErrors(t *testing.T) {
	s := &HumanReadableSerializer{}

	t.Run("unregistered type via interface field", func(t *testing.T) {
		// hrUnregisteredInner is intentionally unregistered, but it appears here
		// in an `any` field, which forces the typed-envelope path that needs
		// the type registered.
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

// hrUnregisteredHere is intentionally never registered. Used only in
// TestHumanReadableSerializer_MarshalErrors.
type hrUnregisteredHere struct {
	X int
}

// ===== Top-level non-struct values =====

// TestHumanReadableSerializer_TopLevelPrimitives covers Marshal/Unmarshal of
// non-struct top-level values (ints, strings, slices, maps).
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

// ===== isEmptyValue exercise =====

// TestIsEmptyValue_AllKinds locks down the semantics of isEmptyValue across
// every reflect.Kind it inspects, including the `default: return false` arm.
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

// ===== setValueWithConversion exercise =====

// TestSetValueWithConversion_AllPaths drives the conversion routine directly to
// reach branches not normally hit through normal Unmarshal flow (pointer
// allocation in target, pointer source dereferencing, numeric conversions).
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

// ===== getJSONFieldName edge cases =====

func TestGetJSONFieldName_Variants(t *testing.T) {
	assert.Equal(t, "Name", getJSONFieldName("Name", ""))
	assert.Equal(t, "alias", getJSONFieldName("Name", "alias"))
	assert.Equal(t, "alias", getJSONFieldName("Name", "alias,omitempty"))
	// Empty primary part (only ",omitempty") falls back to field name.
	assert.Equal(t, "Name", getJSONFieldName("Name", ",omitempty"))
}

// ===== parseTypeName error and recursion paths =====

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

// ===== getTypeName error/branch coverage =====

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

// ===== Additional protocol-safety edge cases =====

// TestHumanReadableSerializer_NilSliceField covers the nil-slice early return
// in hrMarshalSlice (line ~208). A nil slice in a non-omitempty struct field
// must serialize as JSON null and round-trip back to a nil slice.
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

// TestHumanReadableSerializer_NilMapField covers the nil-map early return in
// hrMarshalMap.
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

// TestHumanReadableSerializer_MapWithUnregisteredValueInInterface covers
// wrapMapWithType's getTypeName error path when the map value type isn't
// registered AND the map sits in an interface field that triggers type-envelope
// emission.
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

// TestHumanReadableSerializer_HighPrecisionFloats verifies that very small and
// very large float values survive round-trip with full precision (a common
// silent-corruption hazard in serialization protocols).
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

// TestHumanReadableSerializer_IntegerExtremes verifies that the boundary values
// for integer types survive round-trip in BOTH typed-field and any-field
// scenarios (any-fields go through json.Number → strconv parse).
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

// TestHumanReadableSerializer_StringWithSpecialCharacters verifies escaping
// for strings that contain JSON-significant characters (quotes, backslashes,
// control chars, multi-byte UTF-8). Round-trip must preserve byte-for-byte.
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

// TestHumanReadableSerializer_DeepRecursion exercises deeply nested structures
// to confirm there's no recursion-depth pathology and the wire format remains
// well-formed.
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
