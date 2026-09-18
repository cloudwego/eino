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

package checkpoint

import (
	"bytes"
	"encoding"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/eino-contrib/jsonschema"
	"github.com/stretchr/testify/require"
	orderedmap "github.com/wk8/go-ordered-map/v2"

	"github.com/cloudwego/eino/schema"
)

type canonicalGobValue struct {
	Value    string
	behavior string
	cached   []byte
}

var canonicalGobCalls int32

func (v *canonicalGobValue) GobEncode() ([]byte, error) {
	atomic.AddInt32(&canonicalGobCalls, 1)
	switch v.behavior {
	case "panic":
		panic("encode panic")
	case "lazy":
		if v.cached == nil {
			v.cached = []byte(v.Value)
		}
		return append([]byte(nil), v.cached...), nil
	}
	return []byte(v.Value), nil
}

type canonicalBinaryValue struct {
	Value    string
	behavior string
	cached   []byte
}

var canonicalBinaryCalls int32

func (v *canonicalBinaryValue) MarshalBinary() ([]byte, error) {
	atomic.AddInt32(&canonicalBinaryCalls, 1)
	switch v.behavior {
	case "panic":
		panic("encode panic")
	case "lazy":
		if v.cached == nil {
			v.cached = []byte(v.Value)
		}
		return append([]byte(nil), v.cached...), nil
	}
	return []byte(v.Value), nil
}

type canonicalJSONValue struct {
	Value    string
	behavior string
}

var canonicalJSONCalls int32

func (v *canonicalJSONValue) MarshalJSON() ([]byte, error) {
	call := atomic.AddInt32(&canonicalJSONCalls, 1)
	switch v.behavior {
	case "panic":
		panic("JSON marshaler must not be called")
	case "nondeterministic":
		return []byte(fmt.Sprintf(`{"call":%d}`, call)), nil
	default:
		return []byte(`{"value":"stable"}`), nil
	}
}

type canonicalTextValue struct {
	Value string
}

var canonicalTextCalls int32

func (v *canonicalTextValue) MarshalText() ([]byte, error) {
	atomic.AddInt32(&canonicalTextCalls, 1)
	return []byte(v.Value), nil
}

type canonicalNormalizationNested struct {
	Slice        []int
	Float32      float32
	Float64      float64
	Complex64    complex64
	Complex128   complex128
	Zero         *int
	IndirectZero **int
}

type canonicalNormalizationValue struct {
	Direct    []int
	Nested    canonicalNormalizationNested
	Interface any
	Map       map[string]canonicalNormalizationNested
	Array     [1]canonicalNormalizationNested
}

type canonicalNormalizationInterfaceValue struct {
	Value any
}

type canonicalNormalizationMapKey struct {
	Zero *int
}

type canonicalNormalizationMap map[string]string

type canonicalJSONStringKey string

type canonicalJSONIntKey int16

type canonicalJSONUintKey uint32

type canonicalNormalizationMapContexts struct {
	Omitted   canonicalNormalizationMap
	Interface any
	Slice     []canonicalNormalizationMap
	Array     [1]canonicalNormalizationMap
	Map       map[string]canonicalNormalizationMap
	Pointer   *canonicalNormalizationMap
}

func init() {
	gob.Register([]int{})
	gob.Register(canonicalNormalizationMap{})
}

func canonicalNormalizationFixture() canonicalNormalizationNested {
	zero := 0
	zeroPointer := &zero
	return canonicalNormalizationNested{
		Slice:        []int{},
		Float32:      math.Float32frombits(1 << 31),
		Float64:      math.Copysign(0, -1),
		Complex64:    complex(math.Float32frombits(1<<31), math.Float32frombits(1<<31)),
		Complex128:   complex(math.Copysign(0, -1), math.Copysign(0, -1)),
		Zero:         &zero,
		IndirectZero: &zeroPointer,
	}
}

func canonicalGobRoundTrip[T any](t *testing.T, source T) T {
	t.Helper()
	var data bytes.Buffer
	require.NoError(t, gob.NewEncoder(&data).Encode(source))
	var restored T
	require.NoError(t, gob.NewDecoder(&data).Decode(&restored))
	return restored
}

func TestSemanticDigestGobNormalizationRoundTrip(t *testing.T) {
	directSource := []int{}
	_, directSourceDigest, directSourceOK := SemanticDigest(directSource)
	require.True(t, directSourceOK)
	directRestored := canonicalGobRoundTrip(t, directSource)
	require.Nil(t, directRestored)
	_, directRestoredDigest, directRestoredOK := SemanticDigest(directRestored)
	require.True(t, directRestoredOK)
	require.Equal(t, directSourceDigest, directRestoredDigest)

	source := canonicalNormalizationValue{
		Direct:    []int{},
		Nested:    canonicalNormalizationFixture(),
		Interface: []int{},
		Map: map[string]canonicalNormalizationNested{
			"value": canonicalNormalizationFixture(),
		},
		Array: [1]canonicalNormalizationNested{canonicalNormalizationFixture()},
	}

	sourceType, sourceDigest, sourceOK := SemanticDigest(source)
	require.True(t, sourceOK)
	restored := canonicalGobRoundTrip(t, source)
	restoredType, restoredDigest, restoredOK := SemanticDigest(restored)
	require.True(t, restoredOK)
	require.Equal(t, sourceType, restoredType)
	require.Equal(t, sourceDigest, restoredDigest)

	require.Nil(t, restored.Direct)
	require.Nil(t, restored.Nested.Slice)
	require.Nil(t, restored.Interface.([]int))
	for _, normalized := range []canonicalNormalizationNested{
		restored.Nested,
		restored.Map["value"],
		restored.Array[0],
	} {
		require.False(t, math.Signbit(float64(normalized.Float32)))
		require.False(t, math.Signbit(normalized.Float64))
		require.False(t, math.Signbit(float64(real(normalized.Complex64))))
		require.False(t, math.Signbit(float64(imag(normalized.Complex64))))
		require.False(t, math.Signbit(real(normalized.Complex128)))
		require.False(t, math.Signbit(imag(normalized.Complex128)))
		require.Nil(t, normalized.Zero)
		require.Nil(t, normalized.IndirectZero)
	}
}

func TestSemanticDigestGobNilMapNormalizationContexts(t *testing.T) {
	var nilMap canonicalNormalizationMap
	emptyMap := canonicalNormalizationMap{}

	_, nilRootDigest, ok := SemanticDigest(nilMap)
	require.True(t, ok)
	_, emptyRootDigest, ok := SemanticDigest(emptyMap)
	require.True(t, ok)
	require.Equal(t, emptyRootDigest, nilRootDigest)

	restoredRoot := canonicalGobRoundTrip(t, nilMap)
	require.NotNil(t, restoredRoot)
	_, restoredRootDigest, ok := SemanticDigest(restoredRoot)
	require.True(t, ok)
	require.Equal(t, nilRootDigest, restoredRootDigest)

	source := canonicalNormalizationMapContexts{
		Interface: any(nilMap),
		Slice:     []canonicalNormalizationMap{nil},
		Array:     [1]canonicalNormalizationMap{nil},
		Map:       map[string]canonicalNormalizationMap{"nil": nil},
		Pointer:   &nilMap,
	}
	sourceType, sourceDigest, ok := SemanticDigest(source)
	require.True(t, ok)

	restored := canonicalGobRoundTrip(t, source)
	require.Nil(t, restored.Omitted)
	require.NotNil(t, restored.Interface.(canonicalNormalizationMap))
	require.NotNil(t, restored.Slice[0])
	require.NotNil(t, restored.Array[0])
	require.NotNil(t, restored.Map["nil"])
	require.Nil(t, restored.Pointer)

	restoredType, restoredDigest, ok := SemanticDigest(restored)
	require.True(t, ok)
	require.Equal(t, sourceType, restoredType)
	require.Equal(t, sourceDigest, restoredDigest)

	omitted := canonicalNormalizationMapContexts{}
	transmitted := canonicalNormalizationMapContexts{
		Omitted: canonicalNormalizationMap{},
	}
	_, omittedDigest, ok := SemanticDigest(omitted)
	require.True(t, ok)
	_, transmittedDigest, ok := SemanticDigest(transmitted)
	require.True(t, ok)
	require.NotEqual(t, omittedDigest, transmittedDigest)

	restoredOmitted := canonicalGobRoundTrip(t, omitted)
	restoredTransmitted := canonicalGobRoundTrip(t, transmitted)
	require.Nil(t, restoredOmitted.Omitted)
	require.NotNil(t, restoredTransmitted.Omitted)
	_, restoredOmittedDigest, ok := SemanticDigest(restoredOmitted)
	require.True(t, ok)
	_, restoredTransmittedDigest, ok := SemanticDigest(restoredTransmitted)
	require.True(t, ok)
	require.Equal(t, omittedDigest, restoredOmittedDigest)
	require.Equal(t, transmittedDigest, restoredTransmittedDigest)
}

func TestSemanticDigestGobInterfacePointerNormalizationFailsClosed(t *testing.T) {
	zero := 0
	zeroPointer := &zero
	for _, source := range []canonicalNormalizationInterfaceValue{
		{Value: &zero},
		{Value: &zeroPointer},
	} {
		restored := canonicalGobRoundTrip(t, source)
		require.IsType(t, int(0), restored.Value)
		require.Zero(t, restored.Value)

		_, _, ok := SemanticDigest(source)
		require.False(t, ok)
	}
}

func TestSemanticDigestGobNormalizedMapKeyCollisionFailsClosed(t *testing.T) {
	zero := 0
	source := map[canonicalNormalizationMapKey]string{
		{}:            "nil",
		{Zero: &zero}: "zero",
	}
	require.Len(t, source, 2)

	restored := canonicalGobRoundTrip(t, source)
	require.Len(t, restored, 1)
	_, _, ok := SemanticDigest(source)
	require.False(t, ok)
}

func TestSemanticDigestMapKeySignedZeroSemantics(t *testing.T) {
	type nestedKey struct {
		Float   float32
		Complex complex128
		Array   [1]complex64
	}

	negativeFloat32 := math.Float32frombits(1 << 31)
	negativeFloat64 := math.Float64frombits(1 << 63)
	positiveFloat32 := float32(0)
	positiveFloat64 := float64(0)
	negativePointer := &negativeFloat64
	positivePointer := &positiveFloat64
	tests := []struct {
		name     string
		negative any
		positive any
	}{
		{
			name:     "float32",
			negative: map[float32]string{negativeFloat32: "value"},
			positive: map[float32]string{positiveFloat32: "value"},
		},
		{
			name:     "float64",
			negative: map[float64]string{negativeFloat64: "value"},
			positive: map[float64]string{positiveFloat64: "value"},
		},
		{
			name:     "complex64_real",
			negative: map[complex64]string{complex(negativeFloat32, 1): "value"},
			positive: map[complex64]string{complex(positiveFloat32, 1): "value"},
		},
		{
			name:     "complex64_imaginary",
			negative: map[complex64]string{complex(1, negativeFloat32): "value"},
			positive: map[complex64]string{complex(1, positiveFloat32): "value"},
		},
		{
			name:     "complex128_real",
			negative: map[complex128]string{complex(negativeFloat64, 1): "value"},
			positive: map[complex128]string{complex(positiveFloat64, 1): "value"},
		},
		{
			name:     "complex128_imaginary",
			negative: map[complex128]string{complex(1, negativeFloat64): "value"},
			positive: map[complex128]string{complex(1, positiveFloat64): "value"},
		},
		{
			name: "array_and_struct",
			negative: map[nestedKey]string{{
				Float:   negativeFloat32,
				Complex: complex(1, negativeFloat64),
				Array:   [1]complex64{complex(negativeFloat32, 1)},
			}: "value"},
			positive: map[nestedKey]string{{
				Float:   positiveFloat32,
				Complex: complex(1, positiveFloat64),
				Array:   [1]complex64{complex(positiveFloat32, 1)},
			}: "value"},
		},
		{
			name:     "interface",
			negative: map[any]string{negativeFloat64: "value"},
			positive: map[any]string{positiveFloat64: "value"},
		},
		{
			name:     "pointer",
			negative: map[*float64]string{negativePointer: "value"},
			positive: map[*float64]string{positivePointer: "value"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			negativeType, negativeDigest, ok := SemanticDigest(tt.negative)
			require.True(t, ok)
			positiveType, positiveDigest, ok := SemanticDigest(tt.positive)
			require.True(t, ok)
			require.Equal(t, positiveType, negativeType)
			require.Equal(t, positiveDigest, negativeDigest)

			equal, ok := SemanticEqual(tt.negative, tt.positive)
			require.True(t, ok)
			require.True(t, equal)
		})
	}

	t.Run("nan_payloads_remain_distinct", func(t *testing.T) {
		nan32 := math.Float32frombits(0x7fc00001)
		otherNaN32 := math.Float32frombits(0x7fc00002)
		nan64 := math.Float64frombits(0x7ff8000000000001)
		otherNaN64 := math.Float64frombits(0x7ff8000000000002)
		for _, pair := range [][2]any{
			{
				map[float32]string{nan32: "value"},
				map[float32]string{otherNaN32: "value"},
			},
			{
				map[float64]string{nan64: "value"},
				map[float64]string{otherNaN64: "value"},
			},
			{
				map[complex64]string{complex(nan32, 1): "value"},
				map[complex64]string{complex(otherNaN32, 1): "value"},
			},
			{
				map[complex128]string{complex(1, nan64): "value"},
				map[complex128]string{complex(1, otherNaN64): "value"},
			},
		} {
			_, leftDigest, ok := SemanticDigest(pair[0])
			require.True(t, ok)
			_, rightDigest, ok := SemanticDigest(pair[1])
			require.True(t, ok)
			require.NotEqual(t, leftDigest, rightDigest)
		}
	})

	t.Run("external_marshaler_fails_closed_without_calls", func(t *testing.T) {
		atomic.StoreInt32(&canonicalGobCalls, 0)
		for _, value := range []any{
			map[*canonicalGobValue]string{{Value: "key", behavior: "panic"}: "value"},
			map[any]string{&canonicalGobValue{Value: "key", behavior: "panic"}: "value"},
		} {
			require.NotPanics(t, func() {
				_, _, ok := SemanticDigest(value)
				require.False(t, ok)
			})
		}
		require.Zero(t, atomic.LoadInt32(&canonicalGobCalls))
	})
}

func TestSemanticDigestPreservesGobSentZeroIdentity(t *testing.T) {
	negativeFloat32 := math.Float32frombits(1 << 31)
	negativeFloat64 := math.Float64frombits(1 << 63)
	nan32 := math.Float32frombits(0x7fc00001)
	otherNaN32 := math.Float32frombits(0x7fc00002)
	nan64 := math.Float64frombits(0x7ff8000000000001)
	otherNaN64 := math.Float64frombits(0x7ff8000000000002)
	tests := []struct {
		name  string
		left  any
		right any
	}{
		{name: "float32_signed_zero", left: negativeFloat32, right: float32(0)},
		{name: "float64_signed_zero", left: negativeFloat64, right: float64(0)},
		{name: "complex64_real_signed_zero",
			left: complex(negativeFloat32, 1), right: complex(float32(0), float32(1))},
		{name: "complex64_imaginary_signed_zero",
			left: complex(1, negativeFloat32), right: complex(float32(1), float32(0))},
		{name: "complex128_real_signed_zero",
			left: complex(negativeFloat64, 1), right: complex(float64(0), float64(1))},
		{name: "complex128_imaginary_signed_zero",
			left: complex(1, negativeFloat64), right: complex(float64(1), float64(0))},
		{name: "array_value_signed_zero",
			left: [1]float64{negativeFloat64}, right: [1]float64{0}},
		{name: "map_value_signed_zero",
			left:  map[string]float64{"value": negativeFloat64},
			right: map[string]float64{"value": 0}},
		{name: "float32_nan_payload", left: nan32, right: otherNaN32},
		{name: "float64_nan_payload", left: nan64, right: otherNaN64},
		{name: "complex64_nan_payload",
			left: complex(nan32, 1), right: complex(otherNaN32, 1)},
		{name: "complex128_nan_payload",
			left: complex(1, nan64), right: complex(1, otherNaN64)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, leftDigest, ok := SemanticDigest(tt.left)
			require.True(t, ok)
			_, rightDigest, ok := SemanticDigest(tt.right)
			require.True(t, ok)
			require.NotEqual(t, leftDigest, rightDigest)
		})
	}
}

func TestSemanticEqualPreservesGobSentZeroIdentity(t *testing.T) {
	negativeFloat32 := math.Float32frombits(1 << 31)
	negativeFloat64 := math.Float64frombits(1 << 63)
	tests := []struct {
		name     string
		negative any
		positive any
	}{
		{name: "float32", negative: negativeFloat32, positive: float32(0)},
		{name: "float64", negative: negativeFloat64, positive: float64(0)},
		{
			name:     "complex64",
			negative: complex(negativeFloat32, negativeFloat32),
			positive: complex64(0),
		},
		{
			name:     "complex128",
			negative: complex(negativeFloat64, negativeFloat64),
			positive: complex128(0),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			equal, ok := SemanticEqual(tt.negative, tt.negative)
			require.True(t, ok)
			require.True(t, equal)

			equal, ok = SemanticEqual(tt.negative, tt.positive)
			require.True(t, ok)
			require.False(t, equal)
		})
	}

	atomic.StoreInt32(&canonicalGobCalls, 0)
	equal, ok := SemanticEqual(
		&canonicalGobValue{Value: "left", behavior: "panic"},
		&canonicalGobValue{Value: "right", behavior: "panic"},
	)
	require.False(t, ok)
	require.False(t, equal)
	require.Zero(t, atomic.LoadInt32(&canonicalGobCalls))
}

func TestSemanticDigestDeterministicAndJSONSafe(t *testing.T) {
	atomic.StoreInt32(&canonicalJSONCalls, 0)
	firstType, firstDigest, ok := SemanticDigest(map[string]any{
		"b": 2,
		"a": &canonicalJSONValue{Value: "persisted"},
	})
	require.True(t, ok)
	secondType, secondDigest, ok := SemanticDigest(map[string]any{
		"a": &canonicalJSONValue{Value: "persisted"},
		"b": 2,
	})
	require.True(t, ok)
	require.Equal(t, firstType, secondType)
	require.Equal(t, firstDigest, secondDigest)
	require.NotEmpty(t, firstDigest)
	require.Equal(t, int32(0), atomic.LoadInt32(&canonicalJSONCalls))
}

func TestSemanticCanonicalValueKinds(t *testing.T) {
	interfaceValue := any("interface")
	values := []any{
		nil,
		true,
		int64(-1),
		uint64(1),
		float32(1.25),
		float64(1.25),
		complex64(1 + 2i),
		complex128(1 + 2i),
		"value",
		&interfaceValue,
		[]int{1, 2},
		[2]int{1, 2},
		struct{ Exported string }{Exported: "value"},
		map[string]int{"b": 2, "a": 1},
	}
	for _, value := range values {
		_, _, ok := SemanticDigest(value)
		require.True(t, ok, "%T", value)
	}

	cyclic := map[string]any{}
	cyclic["self"] = cyclic
	_, _, ok := SemanticDigest(cyclic)
	require.False(t, ok)

	_, _, ok = SemanticDigest(make(chan int))
	require.False(t, ok)
}

func TestSemanticDigestRejectsExternalMarshalersWithoutCalling(t *testing.T) {
	tests := []struct {
		name  string
		value any
		calls *int32
	}{
		{name: "gob_stable", value: &canonicalGobValue{Value: "stable"}, calls: &canonicalGobCalls},
		{name: "gob_panic", value: &canonicalGobValue{behavior: "panic"}, calls: &canonicalGobCalls},
		{name: "gob_lazy", value: &canonicalGobValue{
			Value: "lazy", behavior: "lazy",
		}, calls: &canonicalGobCalls},
		{name: "gob_nil", value: (*canonicalGobValue)(nil), calls: &canonicalGobCalls},
		{name: "binary_stable", value: &canonicalBinaryValue{
			Value: "stable",
		}, calls: &canonicalBinaryCalls},
		{name: "binary_panic", value: &canonicalBinaryValue{
			behavior: "panic",
		}, calls: &canonicalBinaryCalls},
		{name: "binary_lazy", value: &canonicalBinaryValue{
			Value: "lazy", behavior: "lazy",
		}, calls: &canonicalBinaryCalls},
		{name: "binary_nil", value: (*canonicalBinaryValue)(nil), calls: &canonicalBinaryCalls},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			atomic.StoreInt32(tt.calls, 0)
			for _, candidate := range []any{
				tt.value,
				map[string]any{"value": tt.value},
			} {
				require.NotPanics(t, func() {
					_, _, ok := SemanticDigest(candidate)
					require.False(t, ok)
				})
			}
			require.Zero(t, atomic.LoadInt32(tt.calls))
			switch value := tt.value.(type) {
			case *canonicalGobValue:
				if value != nil {
					require.Nil(t, value.cached)
				}
			case *canonicalBinaryValue:
				if value != nil {
					require.Nil(t, value.cached)
				}
			}
		})
	}
}

func TestSemanticDigestCanonicalizesFrameworkToolInfo(t *testing.T) {
	source := &schema.ToolInfo{
		Name:  "tool",
		Desc:  "description",
		Extra: map[string]any{"stable": "value"},
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"input": {Type: schema.String, Required: true},
		}),
	}
	sourceType, sourceDigest, ok := SemanticDigest(source)
	require.True(t, ok)

	restored := canonicalGobRoundTrip(t, source)
	restoredType, restoredDigest, ok := SemanticDigest(restored)
	require.True(t, ok)
	require.Equal(t, sourceType, restoredType)
	require.Equal(t, sourceDigest, restoredDigest)

	atomic.StoreInt32(&canonicalGobCalls, 0)
	source.Extra["unsafe"] = &canonicalGobValue{Value: "must not run"}
	_, _, ok = SemanticDigest(source)
	require.False(t, ok)
	require.Zero(t, atomic.LoadInt32(&canonicalGobCalls))

	tests := []struct {
		name     string
		newValue func() any
		calls    *int32
	}{
		{
			name: "json_panics",
			newValue: func() any {
				return &canonicalJSONValue{behavior: "panic"}
			},
			calls: &canonicalJSONCalls,
		},
		{
			name: "json_is_nondeterministic",
			newValue: func() any {
				return &canonicalJSONValue{behavior: "nondeterministic"}
			},
			calls: &canonicalJSONCalls,
		},
		{
			name: "text",
			newValue: func() any {
				return &canonicalTextValue{Value: "unsafe"}
			},
			calls: &canonicalTextCalls,
		},
		{
			name: "gob",
			newValue: func() any {
				return &canonicalGobValue{Value: "unsafe"}
			},
			calls: &canonicalGobCalls,
		},
		{
			name: "binary",
			newValue: func() any {
				return &canonicalBinaryValue{Value: "unsafe"}
			},
			calls: &canonicalBinaryCalls,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			atomic.StoreInt32(tt.calls, 0)
			toolInfo := &schema.ToolInfo{
				Name: "unsafe-default",
				ParamsOneOf: schema.NewParamsOneOfByJSONSchema(&jsonschema.Schema{
					Type: "object",
					Default: map[string]any{
						"nested": []any{tt.newValue()},
					},
				}),
			}
			require.NotPanics(t, func() {
				_, _, ok := SemanticDigest(toolInfo)
				require.False(t, ok)
			})
			require.Zero(t, atomic.LoadInt32(tt.calls))
		})
	}

	safeDefault := &schema.ToolInfo{
		Name: "safe-default",
		ParamsOneOf: schema.NewParamsOneOfByJSONSchema(&jsonschema.Schema{
			Type: "object",
			Default: map[string]any{
				"nested": []any{int64(1), "value", true},
			},
		}),
	}
	safeType, safeDigest, ok := SemanticDigest(safeDefault)
	require.True(t, ok)
	restoredSafeDefault := canonicalGobRoundTrip(t, safeDefault)
	restoredSafeType, restoredSafeDigest, ok := SemanticDigest(restoredSafeDefault)
	require.True(t, ok)
	require.Equal(t, safeType, restoredSafeType)
	require.Equal(t, safeDigest, restoredSafeDigest)
}

func TestSemanticJSONValue(t *testing.T) {
	var nilInterface any
	number := 7
	var nilPointer *int
	var nilSlice []string
	var nilMap map[string]int

	tests := []struct {
		name     string
		value    reflect.Value
		expected canonicalValue
	}{
		{
			name:     "invalid",
			value:    reflect.Value{},
			expected: canonicalValue{Kind: "json_null"},
		},
		{
			name:     "nil_interface",
			value:    reflect.ValueOf(&nilInterface).Elem(),
			expected: canonicalValue{Kind: "json_null"},
		},
		{
			name:     "bool",
			value:    reflect.ValueOf(true),
			expected: canonicalValue{Kind: "json_bool", Value: "true"},
		},
		{
			name:     "signed_integer",
			value:    reflect.ValueOf(int8(-8)),
			expected: canonicalValue{Kind: "json_number", Value: "-8"},
		},
		{
			name:     "unsigned_integer",
			value:    reflect.ValueOf(uint16(16)),
			expected: canonicalValue{Kind: "json_number", Value: "16"},
		},
		{
			name:     "float32",
			value:    reflect.ValueOf(float32(1.25)),
			expected: canonicalValue{Kind: "json_number", Value: "1.25"},
		},
		{
			name:     "float64",
			value:    reflect.ValueOf(2.5),
			expected: canonicalValue{Kind: "json_number", Value: "2.5"},
		},
		{
			name:     "string",
			value:    reflect.ValueOf("value"),
			expected: canonicalValue{Kind: "json_string", Value: "value"},
		},
		{
			name:     "json_number",
			value:    reflect.ValueOf(json.Number("1.250")),
			expected: canonicalValue{Kind: "json_number", Value: "1.25"},
		},
		{
			name:     "pointer",
			value:    reflect.ValueOf(&number),
			expected: canonicalValue{Kind: "json_number", Value: "7"},
		},
		{
			name:     "nil_pointer",
			value:    reflect.ValueOf(nilPointer),
			expected: canonicalValue{Kind: "json_null"},
		},
		{
			name:     "nil_slice",
			value:    reflect.ValueOf(nilSlice),
			expected: canonicalValue{Kind: "json_null"},
		},
		{
			name:     "nil_map",
			value:    reflect.ValueOf(nilMap),
			expected: canonicalValue{Kind: "json_null"},
		},
		{
			name:  "array",
			value: reflect.ValueOf([2]bool{true, false}),
			expected: canonicalValue{
				Kind: "json_array",
				Entries: []canonicalEntry{
					{Value: canonicalValue{Kind: "json_bool", Value: "true"}},
					{Value: canonicalValue{Kind: "json_bool", Value: "false"}},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual, ok := semanticJSONValue(tt.value, make(map[canonicalVisit]struct{}))
			require.True(t, ok)
			require.Equal(t, tt.expected, actual)
		})
	}

	t.Run("safe_nested_typed_map", func(t *testing.T) {
		value := map[canonicalJSONIntKey]any{
			10: []any{"ten", nil},
			-2: struct {
				Enabled bool `json:"enabled"`
			}{Enabled: true},
		}
		actual, ok := semanticJSONValue(reflect.ValueOf(value), make(map[canonicalVisit]struct{}))
		require.True(t, ok)
		require.Equal(t, []string{"-2", "10"}, canonicalJSONKeys(t, actual))

		fields := canonicalJSONFields(t, actual)
		require.Equal(t, canonicalValue{
			Kind: "json_object",
			Entries: []canonicalEntry{{
				Key:   canonicalValue{Kind: "json_string", Value: "enabled"},
				Value: canonicalValue{Kind: "json_bool", Value: "true"},
			}},
		}, fields["-2"])
		require.Equal(t, canonicalValue{
			Kind: "json_array",
			Entries: []canonicalEntry{
				{Value: canonicalValue{Kind: "json_string", Value: "ten"}},
				{Value: canonicalValue{Kind: "json_null"}},
			},
		}, fields["10"])
	})

	t.Run("unsupported_values_fail_closed", func(t *testing.T) {
		for _, value := range []any{
			json.Number("not-a-number"),
			json.Number("NaN"),
			[]byte("binary"),
			complex(1, 2),
			make(chan int),
		} {
			actual, ok := semanticJSONValue(
				reflect.ValueOf(value), make(map[canonicalVisit]struct{}))
			require.False(t, ok, "%T", value)
			require.Equal(t, canonicalValue{}, actual)
		}
	})

	t.Run("cycles_fail_closed", func(t *testing.T) {
		type node struct {
			Next *node `json:"next"`
		}
		pointerCycle := &node{}
		pointerCycle.Next = pointerCycle

		mapCycle := map[string]any{}
		mapCycle["self"] = mapCycle

		sliceCycle := make([]any, 1)
		sliceCycle[0] = sliceCycle

		for _, value := range []any{pointerCycle, mapCycle, sliceCycle} {
			actual, ok := semanticJSONValue(
				reflect.ValueOf(value), make(map[canonicalVisit]struct{}))
			require.False(t, ok)
			require.Equal(t, canonicalValue{}, actual)
		}
	})

	t.Run("duplicate_canonical_map_keys_fail_closed", func(t *testing.T) {
		value := map[any]string{
			canonicalJSONStringKey("1"): "typed string",
			"1":                         "string",
		}
		require.Len(t, value, 2)

		actual, ok := semanticJSONValue(
			reflect.ValueOf(value), make(map[canonicalVisit]struct{}))
		require.False(t, ok)
		require.Equal(t, canonicalValue{}, actual)
	})

	t.Run("nested_custom_marshalers_fail_closed_without_calls", func(t *testing.T) {
		type wrapper struct {
			Nested any `json:"nested"`
		}
		wrappers := []struct {
			name string
			wrap func(any) reflect.Value
		}{
			{
				name: "schema",
				wrap: func(value any) reflect.Value {
					return reflect.ValueOf(&jsonschema.Schema{Default: value})
				},
			},
			{
				name: "struct",
				wrap: func(value any) reflect.Value {
					return reflect.ValueOf(wrapper{Nested: value})
				},
			},
			{
				name: "map",
				wrap: func(value any) reflect.Value {
					return reflect.ValueOf(map[string]any{"nested": value})
				},
			},
			{
				name: "slice",
				wrap: func(value any) reflect.Value {
					return reflect.ValueOf([]any{value})
				},
			},
			{
				name: "array",
				wrap: func(value any) reflect.Value {
					return reflect.ValueOf([1]any{value})
				},
			},
			{
				name: "pointer",
				wrap: func(value any) reflect.Value {
					return reflect.ValueOf(&wrapper{Nested: value})
				},
			},
			{
				name: "interface",
				wrap: func(value any) reflect.Value {
					var nested any = value
					return reflect.ValueOf(&nested).Elem()
				},
			},
		}
		marshalers := []struct {
			name  string
			value any
			calls *int32
		}{
			{name: "json", value: &canonicalJSONValue{behavior: "panic"}, calls: &canonicalJSONCalls},
			{name: "text", value: &canonicalTextValue{Value: "text"}, calls: &canonicalTextCalls},
			{name: "gob", value: &canonicalGobValue{behavior: "panic"}, calls: &canonicalGobCalls},
			{name: "binary", value: &canonicalBinaryValue{behavior: "panic"}, calls: &canonicalBinaryCalls},
		}
		for _, marshaler := range marshalers {
			for _, wrapped := range wrappers {
				t.Run(marshaler.name+"/"+wrapped.name, func(t *testing.T) {
					atomic.StoreInt32(marshaler.calls, 0)
					actual, ok := semanticJSONValue(
						wrapped.wrap(marshaler.value), make(map[canonicalVisit]struct{}))
					require.False(t, ok)
					require.Equal(t, canonicalValue{}, actual)
					require.Zero(t, atomic.LoadInt32(marshaler.calls))
				})
			}
		}
	})
}

func TestSemanticJSONIntegerCanonicalizationExact(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  string
	}{
		{name: "int_min", value: -int(^uint(0)>>1) - 1,
			want: strconv.FormatInt(int64(-int(^uint(0)>>1)-1), 10)},
		{name: "int_max", value: int(^uint(0) >> 1),
			want: strconv.FormatInt(int64(^uint(0)>>1), 10)},
		{name: "int8_min", value: int8(math.MinInt8), want: "-128"},
		{name: "int8_max", value: int8(math.MaxInt8), want: "127"},
		{name: "int16_min", value: int16(math.MinInt16), want: "-32768"},
		{name: "int16_max", value: int16(math.MaxInt16), want: "32767"},
		{name: "int32_min", value: int32(math.MinInt32), want: "-2147483648"},
		{name: "int32_max", value: int32(math.MaxInt32), want: "2147483647"},
		{name: "int64_min", value: int64(math.MinInt64), want: "-9223372036854775808"},
		{name: "int64_max", value: int64(math.MaxInt64), want: "9223372036854775807"},
		{name: "uint_max", value: ^uint(0), want: strconv.FormatUint(uint64(^uint(0)), 10)},
		{name: "uint8_max", value: uint8(math.MaxUint8), want: "255"},
		{name: "uint16_max", value: uint16(math.MaxUint16), want: "65535"},
		{name: "uint32_max", value: uint32(math.MaxUint32), want: "4294967295"},
		{name: "uint64_max", value: uint64(math.MaxUint64), want: "18446744073709551615"},
		{name: "uintptr_max", value: ^uintptr(0),
			want: strconv.FormatUint(uint64(^uintptr(0)), 10)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual, ok := semanticJSONValue(
				reflect.ValueOf(tt.value), make(map[canonicalVisit]struct{}))
			require.True(t, ok)
			require.Equal(t, canonicalValue{Kind: "json_number", Value: tt.want}, actual)
		})
	}

	for _, values := range []struct {
		name   string
		first  any
		second any
	}{
		{
			name:   "signed_adjacent_above_2_to_53",
			first:  int64(1 << 53),
			second: int64(1<<53 + 1),
		},
		{
			name:   "unsigned_adjacent_above_2_to_53",
			first:  uint64(1 << 53),
			second: uint64(1<<53 + 1),
		},
		{
			name:   "signed_and_unsigned_extrema",
			first:  int64(math.MaxInt64),
			second: uint64(math.MaxUint64),
		},
	} {
		t.Run(values.name, func(t *testing.T) {
			first, ok := semanticJSONValue(
				reflect.ValueOf(values.first), make(map[canonicalVisit]struct{}))
			require.True(t, ok)
			second, ok := semanticJSONValue(
				reflect.ValueOf(values.second), make(map[canonicalVisit]struct{}))
			require.True(t, ok)
			require.NotEqual(t, first, second)
		})
	}
}

func TestSemanticJSONNumberCanonicalization(t *testing.T) {
	valid := []struct {
		raw  string
		want string
	}{
		{raw: "0", want: "0"},
		{raw: "-0", want: "-0"},
		{raw: "9007199254740992", want: "9007199254740992"},
		{raw: "9007199254740993", want: "9007199254740993"},
		{raw: "-9223372036854775809", want: "-9223372036854775809"},
		{raw: "-9223372036854775808", want: "-9223372036854775808"},
		{raw: "9223372036854775807", want: "9223372036854775807"},
		{raw: "9223372036854775808", want: "9223372036854775808"},
		{raw: "18446744073709551615", want: "18446744073709551615"},
		{raw: "18446744073709551616", want: "18446744073709551616"},
		{raw: "0.00125", want: "0.00125"},
		{raw: "-0.00125", want: "-0.00125"},
		{raw: "1.250", want: "1.25"},
		{raw: "125e-2", want: "1.25"},
		{raw: "0.0012500e3", want: "1.25"},
		{raw: "-123.4500e-2", want: "-1.2345"},
		{raw: "1e-3", want: "0.001"},
		{raw: "1e3", want: "1000"},
		{raw: "1.0e3", want: "1000"},
		{raw: "123000e-3", want: "123"},
		{raw: "1e0003", want: "1000"},
		{raw: "1E+0", want: "1"},
		{raw: "9007199254740993.0", want: "9007199254740993"},
		{raw: "9007199254740993e0", want: "9007199254740993"},
		{raw: "-0.0", want: "-0"},
		{raw: "-0e0", want: "-0"},
	}
	for _, tt := range valid {
		t.Run("valid_"+tt.raw, func(t *testing.T) {
			actual, ok := semanticJSONNumber(tt.raw)
			require.True(t, ok)
			require.Equal(t, canonicalValue{Kind: "json_number", Value: tt.want}, actual)
		})
	}

	for _, raw := range []string{
		"",
		"+1",
		"01",
		"-01",
		"1.",
		".1",
		"1e",
		" 1",
		"1 ",
		"NaN",
		"Inf",
		"-Inf",
		"Infinity",
		"-Infinity",
		"0x1",
		"1e10000",
	} {
		t.Run("invalid_"+raw, func(t *testing.T) {
			actual, ok := semanticJSONNumber(raw)
			require.False(t, ok)
			require.Equal(t, canonicalValue{}, actual)
		})
	}
}

func TestSemanticJSONFloatNormalization(t *testing.T) {
	negativeZero := math.Copysign(0, -1)
	tests := []struct {
		name  string
		value any
		want  string
	}{
		{name: "float32", value: float32(1.25), want: "1.25"},
		{name: "float64", value: 1.25, want: "1.25"},
		{name: "float32_negative_zero", value: math.Float32frombits(1 << 31), want: "-0"},
		{name: "float64_negative_zero", value: negativeZero, want: "-0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual, ok := semanticJSONValue(
				reflect.ValueOf(tt.value), make(map[canonicalVisit]struct{}))
			require.True(t, ok)
			require.Equal(t, canonicalValue{Kind: "json_number", Value: tt.want}, actual)
		})
	}

	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		actual, ok := semanticJSONValue(
			reflect.ValueOf(value), make(map[canonicalVisit]struct{}))
		require.False(t, ok)
		require.Equal(t, canonicalValue{}, actual)
	}

	source := &schema.ToolInfo{
		Name: "negative-zero",
		ParamsOneOf: schema.NewParamsOneOfByJSONSchema(&jsonschema.Schema{
			Default: negativeZero,
			Maximum: json.Number("-0"),
		}),
	}
	_, sourceDigest, ok := SemanticDigest(source)
	require.True(t, ok)
	restored := canonicalGobRoundTrip(t, source)
	restoredSchema, err := restored.ParamsOneOf.ToJSONSchema()
	require.NoError(t, err)
	restoredDefault, ok := restoredSchema.Default.(float64)
	require.True(t, ok)
	require.True(t, math.Signbit(restoredDefault))
	require.Equal(t, json.Number("-0"), restoredSchema.Maximum)
	_, restoredDigest, ok := SemanticDigest(restored)
	require.True(t, ok)
	require.Equal(t, sourceDigest, restoredDigest)
}

func TestSemanticJSONNumberDigestDistinguishesAdjacentLargeIntegers(t *testing.T) {
	digest := func(raw string) string {
		t.Helper()
		_, digest, ok := SemanticDigest(&schema.ToolInfo{
			Name: "large-integer",
			ParamsOneOf: schema.NewParamsOneOfByJSONSchema(&jsonschema.Schema{
				Maximum: json.Number(raw),
			}),
		})
		require.True(t, ok)
		return digest
	}

	require.NotEqual(t, digest("9007199254740992"), digest("9007199254740993"))
	require.NotEqual(t, digest("9007199254740992.0"), digest("9007199254740993.0"))
	require.NotEqual(t, digest("9007199254740992e0"), digest("9007199254740993e0"))
	require.NotEqual(t, digest("9223372036854775807"), digest("18446744073709551615"))
	require.NotEqual(t, digest("18446744073709551616"), digest("18446744073709551617"))

	for _, schemaValue := range []*jsonschema.Schema{
		{Maximum: json.Number("+1")},
		{Default: math.NaN()},
		{Default: math.Inf(1)},
	} {
		_, digest, ok := SemanticDigest(&schema.ToolInfo{
			Name:        "invalid-number",
			ParamsOneOf: schema.NewParamsOneOfByJSONSchema(schemaValue),
		})
		require.False(t, ok)
		require.Empty(t, digest)
	}
}

func TestSemanticJSONSchema(t *testing.T) {
	t.Run("nil_and_boolean_schemas", func(t *testing.T) {
		var nilSchema *jsonschema.Schema
		tests := []struct {
			name     string
			schema   *jsonschema.Schema
			expected canonicalValue
		}{
			{
				name:     "nil",
				schema:   nilSchema,
				expected: canonicalValue{Kind: "json_null"},
			},
			{
				name:     "true",
				schema:   jsonschema.TrueSchema,
				expected: canonicalValue{Kind: "json_bool", Value: "true"},
			},
			{
				name:     "false",
				schema:   jsonschema.FalseSchema,
				expected: canonicalValue{Kind: "json_bool", Value: "false"},
			},
			{
				name:     "empty_object_is_true",
				schema:   &jsonschema.Schema{},
				expected: canonicalValue{Kind: "json_bool", Value: "true"},
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				actual, ok := semanticJSONSchema(
					reflect.ValueOf(tt.schema), make(map[canonicalVisit]struct{}))
				require.True(t, ok)
				require.Equal(t, tt.expected, actual)
			})
		}
	})

	t.Run("safe_nested_schema_is_canonical", func(t *testing.T) {
		properties := orderedmap.New[string, *jsonschema.Schema]()
		properties.Set("z", nil)
		properties.Set("a", &jsonschema.Schema{Type: "string"})
		zero, one := uint64(0), uint64(1)
		value := &jsonschema.Schema{
			Type:       "object",
			Properties: properties,
			MaxLength:  &zero,
			MinLength:  &one,
			Title:      "root",
			Default: map[canonicalJSONIntKey]string{
				10: "ten",
				-2: "negative",
			},
			Extras: map[string]any{
				"x-extra": []any{nil, true},
			},
		}

		actual, ok := semanticJSONSchema(
			reflect.ValueOf(value), make(map[canonicalVisit]struct{}))
		require.True(t, ok)
		require.Equal(t, []string{
			"default", "maxLength", "minLength", "properties", "title", "type", "x-extra",
		}, canonicalJSONKeys(t, actual))

		fields := canonicalJSONFields(t, actual)
		require.Equal(t, []string{"-2", "10"}, canonicalJSONKeys(t, fields["default"]))
		require.Equal(t, canonicalValue{Kind: "json_number", Value: "0"}, fields["maxLength"])
		require.Equal(t, canonicalValue{Kind: "json_number", Value: "1"}, fields["minLength"])
		require.Equal(t, []string{"a", "z"}, canonicalJSONKeys(t, fields["properties"]))
		propertiesFields := canonicalJSONFields(t, fields["properties"])
		require.Equal(t, canonicalValue{
			Kind: "json_object",
			Entries: []canonicalEntry{{
				Key:   canonicalValue{Kind: "json_string", Value: "type"},
				Value: canonicalValue{Kind: "json_string", Value: "string"},
			}},
		}, propertiesFields["a"])
		require.Equal(t, canonicalValue{Kind: "json_null"}, propertiesFields["z"])
		require.Equal(t, canonicalValue{Kind: "json_string", Value: "root"}, fields["title"])
		require.Equal(t, canonicalValue{Kind: "json_string", Value: "object"}, fields["type"])
		require.Equal(t, canonicalValue{
			Kind: "json_array",
			Entries: []canonicalEntry{
				{Value: canonicalValue{Kind: "json_null"}},
				{Value: canonicalValue{Kind: "json_bool", Value: "true"}},
			},
		}, fields["x-extra"])
	})

	t.Run("enhanced_type", func(t *testing.T) {
		actual, ok := semanticJSONSchema(reflect.ValueOf(&jsonschema.Schema{
			TypeEnhanced: []string{"string", "null"},
		}), make(map[canonicalVisit]struct{}))
		require.True(t, ok)
		require.Equal(t, canonicalValue{
			Kind: "json_object",
			Entries: []canonicalEntry{{
				Key: canonicalValue{Kind: "json_string", Value: "type"},
				Value: canonicalValue{
					Kind: "json_array",
					Entries: []canonicalEntry{
						{Value: canonicalValue{Kind: "json_string", Value: "string"}},
						{Value: canonicalValue{Kind: "json_string", Value: "null"}},
					},
				},
			}},
		}, actual)
	})

	t.Run("invalid_schema_fails_closed", func(t *testing.T) {
		conflictingType := &jsonschema.Schema{
			Type:         "string",
			TypeEnhanced: []string{"number"},
		}
		cyclic := &jsonschema.Schema{}
		cyclic.Items = cyclic
		invalidProperties := orderedmap.New[string, *jsonschema.Schema]()
		invalidProperties.Set("invalid", &jsonschema.Schema{
			Default: []byte("unsupported"),
		})
		invalidChild := &jsonschema.Schema{Properties: invalidProperties}

		for _, value := range []*jsonschema.Schema{conflictingType, cyclic, invalidChild} {
			actual, ok := semanticJSONSchema(
				reflect.ValueOf(value), make(map[canonicalVisit]struct{}))
			require.False(t, ok)
			require.Equal(t, canonicalValue{}, actual)
		}
	})
}

func TestSemanticJSONMapKey(t *testing.T) {
	tests := []struct {
		name     string
		value    any
		expected string
	}{
		{name: "typed_string", value: canonicalJSONStringKey("key"), expected: "key"},
		{name: "string", value: string("string"), expected: "string"},
		{name: "int", value: int(-1), expected: "-1"},
		{name: "int8", value: int8(-8), expected: "-8"},
		{name: "typed_signed_integer", value: canonicalJSONIntKey(-12), expected: "-12"},
		{name: "int32", value: int32(-32), expected: "-32"},
		{name: "int64", value: int64(-64), expected: "-64"},
		{name: "uint", value: uint(1), expected: "1"},
		{name: "uint8", value: uint8(8), expected: "8"},
		{name: "uint16", value: uint16(16), expected: "16"},
		{name: "typed_unsigned_integer", value: canonicalJSONUintKey(34), expected: "34"},
		{name: "uint64", value: uint64(64), expected: "64"},
		{name: "uintptr", value: uintptr(128), expected: "128"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual, ok := semanticJSONMapKey(reflect.ValueOf(tt.value))
			require.True(t, ok)
			require.Equal(t, tt.expected, actual)
		})
	}

	t.Run("unsupported_key_kinds", func(t *testing.T) {
		for _, value := range []any{true, 1.25, [1]int{1}} {
			actual, ok := semanticJSONMapKey(reflect.ValueOf(value))
			require.False(t, ok)
			require.Empty(t, actual)
		}
	})

	t.Run("custom_marshaler_keys_are_not_called", func(t *testing.T) {
		tests := []struct {
			name  string
			value any
			calls *int32
		}{
			{name: "json", value: canonicalJSONValue{Value: "json"}, calls: &canonicalJSONCalls},
			{name: "text", value: canonicalTextValue{Value: "text"}, calls: &canonicalTextCalls},
			{name: "gob", value: canonicalGobValue{Value: "gob"}, calls: &canonicalGobCalls},
			{name: "binary", value: canonicalBinaryValue{Value: "binary"}, calls: &canonicalBinaryCalls},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				atomic.StoreInt32(tt.calls, 0)
				actual, ok := semanticJSONMapKey(reflect.ValueOf(tt.value))
				require.False(t, ok)
				require.Empty(t, actual)
				require.Zero(t, atomic.LoadInt32(tt.calls))
			})
		}
	})
}

func TestSemanticJSONStruct(t *testing.T) {
	t.Run("tags_omitempty_and_nested_values", func(t *testing.T) {
		type value struct {
			EmptyString string         `json:"empty_string,omitempty"`
			ZeroNumber  int            `json:"zero_number,omitempty"`
			False       bool           `json:"false,omitempty"`
			EmptyArray  [0]int         `json:"empty_array,omitempty"`
			EmptySlice  []string       `json:"empty_slice,omitempty"`
			EmptyMap    map[string]int `json:"empty_map,omitempty"`
			NilPointer  *int           `json:"nil_pointer,omitempty"`
			NilAny      any            `json:"nil_any,omitempty"`
			KeptZero    int            `json:"kept_zero"`
			NonZero     int            `json:"renamed,omitempty"`
			DefaultName string
			Ignored     string `json:"-"`
			unexported  string
		}
		actual, ok := semanticJSONStruct(reflect.ValueOf(value{
			KeptZero:    0,
			NonZero:     9,
			DefaultName: "kept",
			Ignored:     "ignored",
			unexported:  "hidden",
		}), make(map[canonicalVisit]struct{}))
		require.True(t, ok)
		require.Equal(t, []string{"DefaultName", "kept_zero", "renamed"},
			canonicalJSONKeys(t, actual))
		require.Equal(t, map[string]canonicalValue{
			"DefaultName": {Kind: "json_string", Value: "kept"},
			"kept_zero":   {Kind: "json_number", Value: "0"},
			"renamed":     {Kind: "json_number", Value: "9"},
		}, canonicalJSONFields(t, actual))
	})

	t.Run("nonempty_values_are_not_omitted", func(t *testing.T) {
		number := 1
		value := struct {
			Array     [1]int         `json:"array,omitempty"`
			Slice     []string       `json:"slice,omitempty"`
			Map       map[string]int `json:"map,omitempty"`
			Pointer   *int           `json:"pointer,omitempty"`
			Interface any            `json:"interface,omitempty"`
		}{
			Array:     [1]int{1},
			Slice:     []string{"value"},
			Map:       map[string]int{"key": 2},
			Pointer:   &number,
			Interface: "",
		}
		actual, ok := semanticJSONStruct(
			reflect.ValueOf(value), make(map[canonicalVisit]struct{}))
		require.True(t, ok)
		require.Equal(t, []string{"array", "interface", "map", "pointer", "slice"},
			canonicalJSONKeys(t, actual))
		require.Equal(t, canonicalValue{Kind: "json_string"}, canonicalJSONFields(t, actual)["interface"])
	})

	t.Run("ambiguous_fields_fail_closed", func(t *testing.T) {
		type Embedded struct {
			Value string
		}
		type anonymous struct {
			Embedded
		}
		duplicate := reflect.New(reflect.StructOf([]reflect.StructField{
			{Name: "First", Type: reflect.TypeOf(""), Tag: `json:"same"`},
			{Name: "Second", Type: reflect.TypeOf(""), Tag: `json:"same"`},
		})).Elem()
		duplicate.Field(0).SetString("first")
		duplicate.Field(1).SetString("second")
		for _, value := range []reflect.Value{
			reflect.ValueOf(anonymous{Embedded: Embedded{Value: "value"}}),
			duplicate,
		} {
			actual, ok := semanticJSONStruct(
				value, make(map[canonicalVisit]struct{}))
			require.False(t, ok)
			require.Equal(t, canonicalValue{}, actual)
		}
	})

	t.Run("string_option_fails_closed", func(t *testing.T) {
		value := struct {
			Count int `json:"count,string"`
		}{Count: 1}
		actual, ok := semanticJSONStruct(
			reflect.ValueOf(value), make(map[canonicalVisit]struct{}))
		require.False(t, ok)
		require.Equal(t, canonicalValue{}, actual)
	})

	t.Run("invalid_name_fails_closed", func(t *testing.T) {
		value := reflect.New(reflect.StructOf([]reflect.StructField{{
			Name: "Count",
			Type: reflect.TypeOf(0),
			Tag:  `json:"bad\\name"`,
		}})).Elem()
		value.Field(0).SetInt(1)
		actual, ok := semanticJSONStruct(value, make(map[canonicalVisit]struct{}))
		require.False(t, ok)
		require.Equal(t, canonicalValue{}, actual)
	})

}

func TestIsJSONEmptyValue(t *testing.T) {
	var nilInterface any
	nonNilInterface := any("")
	number := 1
	var nilPointer *int

	tests := []struct {
		name     string
		value    reflect.Value
		expected bool
	}{
		{name: "zero_length_array", value: reflect.ValueOf([0]int{}), expected: true},
		{name: "nonempty_array", value: reflect.ValueOf([1]int{}), expected: false},
		{name: "nil_map", value: reflect.ValueOf(map[string]int(nil)), expected: true},
		{name: "empty_map", value: reflect.ValueOf(map[string]int{}), expected: true},
		{name: "nonempty_map", value: reflect.ValueOf(map[string]int{"key": 0}), expected: false},
		{name: "nil_slice", value: reflect.ValueOf([]int(nil)), expected: true},
		{name: "empty_slice", value: reflect.ValueOf([]int{}), expected: true},
		{name: "nonempty_slice", value: reflect.ValueOf([]int{0}), expected: false},
		{name: "empty_string", value: reflect.ValueOf(""), expected: true},
		{name: "nonempty_string", value: reflect.ValueOf("value"), expected: false},
		{name: "false", value: reflect.ValueOf(false), expected: true},
		{name: "true", value: reflect.ValueOf(true), expected: false},
		{name: "zero_int", value: reflect.ValueOf(int(0)), expected: true},
		{name: "nonzero_int", value: reflect.ValueOf(int(-1)), expected: false},
		{name: "zero_int8", value: reflect.ValueOf(int8(0)), expected: true},
		{name: "nonzero_int8", value: reflect.ValueOf(int8(-1)), expected: false},
		{name: "zero_int16", value: reflect.ValueOf(int16(0)), expected: true},
		{name: "nonzero_int16", value: reflect.ValueOf(int16(-1)), expected: false},
		{name: "zero_int32", value: reflect.ValueOf(int32(0)), expected: true},
		{name: "nonzero_int32", value: reflect.ValueOf(int32(-1)), expected: false},
		{name: "zero_int64", value: reflect.ValueOf(int64(0)), expected: true},
		{name: "nonzero_int64", value: reflect.ValueOf(int64(-1)), expected: false},
		{name: "zero_uint", value: reflect.ValueOf(uint(0)), expected: true},
		{name: "nonzero_uint", value: reflect.ValueOf(uint(1)), expected: false},
		{name: "zero_uint8", value: reflect.ValueOf(uint8(0)), expected: true},
		{name: "nonzero_uint8", value: reflect.ValueOf(uint8(1)), expected: false},
		{name: "zero_uint16", value: reflect.ValueOf(uint16(0)), expected: true},
		{name: "nonzero_uint16", value: reflect.ValueOf(uint16(1)), expected: false},
		{name: "zero_uint32", value: reflect.ValueOf(uint32(0)), expected: true},
		{name: "nonzero_uint32", value: reflect.ValueOf(uint32(1)), expected: false},
		{name: "zero_uint64", value: reflect.ValueOf(uint64(0)), expected: true},
		{name: "nonzero_uint64", value: reflect.ValueOf(uint64(1)), expected: false},
		{name: "zero_uintptr", value: reflect.ValueOf(uintptr(0)), expected: true},
		{name: "nonzero_uintptr", value: reflect.ValueOf(uintptr(1)), expected: false},
		{name: "zero_float32", value: reflect.ValueOf(float32(0)), expected: true},
		{name: "nonzero_float32", value: reflect.ValueOf(float32(0.5)), expected: false},
		{name: "zero_float64", value: reflect.ValueOf(float64(0)), expected: true},
		{name: "nonzero_float64", value: reflect.ValueOf(float64(0.5)), expected: false},
		{
			name:     "nil_interface",
			value:    reflect.ValueOf(&nilInterface).Elem(),
			expected: true,
		},
		{
			name:     "non_nil_interface_with_empty_value",
			value:    reflect.ValueOf(&nonNilInterface).Elem(),
			expected: false,
		},
		{name: "nil_pointer", value: reflect.ValueOf(nilPointer), expected: true},
		{name: "non_nil_pointer", value: reflect.ValueOf(&number), expected: false},
		{name: "struct", value: reflect.ValueOf(struct{}{}), expected: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expected, isJSONEmptyValue(tt.value))
		})
	}
}

func canonicalJSONKeys(t *testing.T, value canonicalValue) []string {
	t.Helper()
	require.Equal(t, "json_object", value.Kind)
	keys := make([]string, len(value.Entries))
	for i, entry := range value.Entries {
		require.Equal(t, canonicalValue{Kind: "json_string", Value: entry.Key.Value}, entry.Key)
		keys[i] = entry.Key.Value
	}
	return keys
}

func canonicalJSONFields(t *testing.T, value canonicalValue) map[string]canonicalValue {
	t.Helper()
	fields := make(map[string]canonicalValue, len(value.Entries))
	for _, entry := range value.Entries {
		require.Equal(t, "json_string", entry.Key.Kind)
		require.NotContains(t, fields, entry.Key.Value)
		fields[entry.Key.Value] = entry.Value
	}
	return fields
}

func TestCanonicalHelpersFailClosed(t *testing.T) {
	require.Equal(t, "string", canonicalTypeName(reflect.TypeOf("")))
	require.Equal(t, "*github.com/cloudwego/eino/internal/checkpoint:canonicalGobValue",
		canonicalTypeName(reflect.TypeOf((*canonicalGobValue)(nil))))

	var _ gob.GobEncoder = (*canonicalGobValue)(nil)
	var _ encoding.BinaryMarshaler = (*canonicalBinaryValue)(nil)
}
