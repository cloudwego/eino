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
	"crypto/sha256"
	"encoding"
	"encoding/gob"
	"encoding/hex"
	"encoding/json"
	"math"
	"math/big"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/eino-contrib/jsonschema"
	orderedmap "github.com/wk8/go-ordered-map/v2"

	"github.com/cloudwego/eino/schema"
)

// canonicalValue is internal framing for SemanticDigest. Its JSON encoding
// never invokes user JSON marshalers.
type canonicalValue struct {
	Kind    string           `json:"k"`
	Type    string           `json:"t,omitempty"`
	Value   string           `json:"v,omitempty"`
	IsNil   bool             `json:"n,omitempty"`
	Fields  []canonicalField `json:"f,omitempty"`
	Entries []canonicalEntry `json:"e,omitempty"`
}

type canonicalField struct {
	Name  string         `json:"n"`
	Value canonicalValue `json:"v"`
}

type canonicalEntry struct {
	Key   canonicalValue `json:"k"`
	Value canonicalValue `json:"v"`
}

type canonicalVisit struct {
	typ     reflect.Type
	pointer uintptr
}

type canonicalToolInfo struct {
	Name      string
	Desc      string
	Extra     map[string]any
	HasParams bool
	Params    canonicalValue
}

var (
	gobEncoderType      = reflect.TypeOf((*gob.GobEncoder)(nil)).Elem()
	binaryMarshalerType = reflect.TypeOf((*encoding.BinaryMarshaler)(nil)).Elem()
	jsonMarshalerType   = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	textMarshalerType   = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
	jsonSchemaType      = reflect.TypeOf(jsonschema.Schema{})
	jsonSchemaPtrType   = reflect.TypeOf((*jsonschema.Schema)(nil))
	toolInfoType        = reflect.TypeOf(schema.ToolInfo{})
	toolInfoPointerType = reflect.TypeOf((*schema.ToolInfo)(nil))
)

// SemanticDigest returns a panic-safe deterministic digest that follows Gob's
// persistence-visible semantics. It never invokes custom marshalers. Values
// that cannot be summarized safely or stably return ok=false.
func SemanticDigest(value any) (typeName, digest string, ok bool) {
	defer func() {
		if recover() != nil {
			typeName = ""
			digest = ""
			ok = false
		}
	}()

	typeName = "<nil>"
	if value != nil {
		typeName = canonicalTypeName(reflect.TypeOf(value))
	}
	data, ok := semanticCanonicalData(value)
	if !ok {
		return "", "", false
	}
	sum := sha256.Sum256(data)
	return typeName, hex.EncodeToString(sum[:]), true
}

// SemanticEqual reports whether two values have identical deterministic
// Gob-semantic canonical forms. It returns ok=false when either value cannot
// be canonicalized without invoking custom marshalers.
func SemanticEqual(left, right any) (equal, ok bool) {
	defer func() {
		if recover() != nil {
			equal = false
			ok = false
		}
	}()

	leftData, ok := semanticCanonicalData(left)
	if !ok {
		return false, false
	}
	rightData, ok := semanticCanonicalData(right)
	if !ok {
		return false, false
	}
	return bytes.Equal(leftData, rightData), true
}

func semanticCanonicalData(value any) ([]byte, bool) {
	canonical, ok := semanticCanonicalValueRoot(reflect.ValueOf(value))
	if !ok {
		return nil, false
	}
	data, err := json.Marshal(canonical)
	if err != nil {
		return nil, false
	}
	return data, true
}

func semanticCanonicalValueRoot(value reflect.Value) (canonicalValue, bool) {
	return semanticCanonicalValue(value, make(map[canonicalVisit]struct{}), true)
}

func semanticCanonicalValue(value reflect.Value,
	visiting map[canonicalVisit]struct{}, sendZero bool) (canonicalValue, bool) {
	if !value.IsValid() {
		return canonicalValue{Kind: "invalid"}, true
	}
	typ := value.Type()
	canonical := canonicalValue{
		Kind: value.Kind().String(),
		Type: canonicalTypeName(typ),
	}
	if known, handled, ok := semanticKnownGobValue(value, canonical, visiting); handled {
		return known, ok
	}
	if hasGobExternalEncoding(typ) {
		return canonicalValue{}, false
	}
	if !sendZero {
		omitted, ok := gobFieldValueOmitted(value)
		if !ok {
			return canonicalValue{}, false
		}
		if omitted {
			if typ.Kind() == reflect.Map {
				canonical.IsNil = true
				return canonical, true
			}
			return semanticCanonicalValue(reflect.Zero(typ), visiting, true)
		}
	}
	if value.Kind() == reflect.Pointer && value.IsNil() {
		canonical.IsNil = true
		return canonical, true
	}
	if isNilableKind(value.Kind()) && value.IsNil() &&
		(value.Kind() != reflect.Map || !sendZero) {
		canonical.IsNil = true
		return canonical, true
	}

	switch value.Kind() {
	case reflect.Bool:
		canonical.Value = strconv.FormatBool(value.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		canonical.Value = strconv.FormatInt(value.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		canonical.Value = strconv.FormatUint(value.Uint(), 10)
	case reflect.Float32:
		canonical.Value = strconv.FormatUint(
			uint64(math.Float32bits(float32(value.Float()))), 16)
	case reflect.Float64:
		canonical.Value = strconv.FormatUint(math.Float64bits(value.Float()), 16)
	case reflect.Complex64:
		number := complex64(value.Complex())
		canonical.Value = strconv.FormatUint(uint64(math.Float32bits(real(number))), 16) +
			":" + strconv.FormatUint(uint64(math.Float32bits(imag(number))), 16)
	case reflect.Complex128:
		number := value.Complex()
		canonical.Value = strconv.FormatUint(math.Float64bits(real(number)), 16) +
			":" + strconv.FormatUint(math.Float64bits(imag(number)), 16)
	case reflect.String:
		canonical.Value = value.String()
	case reflect.Interface:
		if gobInterfacePointerIndirectionUnprovable(value.Elem()) {
			return canonicalValue{}, false
		}
		child, ok := semanticCanonicalValue(value.Elem(), visiting, true)
		if !ok {
			return canonicalValue{}, false
		}
		canonical.Entries = []canonicalEntry{{Value: child}}
	case reflect.Pointer:
		return semanticPointer(value, canonical, visiting, sendZero)
	case reflect.Slice:
		return semanticSequence(value, canonical, visiting, true)
	case reflect.Array:
		return semanticSequence(value, canonical, visiting, false)
	case reflect.Map:
		return semanticMap(value, canonical, visiting)
	case reflect.Struct:
		for i := 0; i < value.NumField(); i++ {
			field := typ.Field(i)
			if field.PkgPath != "" || field.Type.Kind() == reflect.Func ||
				field.Type.Kind() == reflect.Chan {
				continue
			}
			child, ok := semanticCanonicalValue(value.Field(i), visiting, false)
			if !ok {
				return canonicalValue{}, false
			}
			canonical.Fields = append(canonical.Fields, canonicalField{
				Name:  field.Name,
				Value: child,
			})
		}
	default:
		return canonicalValue{}, false
	}
	return canonical, true
}

func semanticKnownGobValue(value reflect.Value, canonical canonicalValue,
	visiting map[canonicalVisit]struct{}) (canonicalValue, bool, bool) {
	if value.Type() != toolInfoType && value.Type() != toolInfoPointerType {
		return canonicalValue{}, false, false
	}
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			canonical.IsNil = true
			return canonical, true, true
		}
		value = value.Elem()
	}
	toolInfo := value.Interface().(schema.ToolInfo)
	summary := canonicalToolInfo{
		Name:      toolInfo.Name,
		Desc:      toolInfo.Desc,
		Extra:     toolInfo.Extra,
		HasParams: toolInfo.ParamsOneOf != nil,
	}
	if toolInfo.ParamsOneOf != nil {
		params, err := toolInfo.ParamsOneOf.ToJSONSchema()
		if err != nil {
			return canonicalValue{}, true, false
		}
		paramsCanonical, ok := semanticJSONValue(
			reflect.ValueOf(params), make(map[canonicalVisit]struct{}))
		if !ok {
			return canonicalValue{}, true, false
		}
		summary.Params = paramsCanonical
	}
	child, ok := semanticCanonicalValue(reflect.ValueOf(summary), visiting, true)
	if !ok {
		return canonicalValue{}, true, false
	}
	canonical.Entries = []canonicalEntry{{Value: child}}
	return canonical, true, true
}

func semanticJSONValue(value reflect.Value,
	visiting map[canonicalVisit]struct{}) (canonicalValue, bool) {
	if !value.IsValid() {
		return canonicalValue{Kind: "json_null"}, true
	}
	if value.Kind() == reflect.Interface {
		if value.IsNil() {
			return canonicalValue{Kind: "json_null"}, true
		}
		return semanticJSONValue(value.Elem(), visiting)
	}
	if value.Type() == jsonSchemaType || value.Type() == jsonSchemaPtrType {
		return semanticJSONSchema(value, visiting)
	}
	if hasCustomMarshaler(value.Type()) {
		return canonicalValue{}, false
	}

	switch value.Kind() {
	case reflect.Pointer:
		if value.IsNil() {
			return canonicalValue{Kind: "json_null"}, true
		}
		visit := canonicalVisit{typ: value.Type(), pointer: value.Pointer()}
		if _, exists := visiting[visit]; exists {
			return canonicalValue{}, false
		}
		visiting[visit] = struct{}{}
		defer delete(visiting, visit)
		return semanticJSONValue(value.Elem(), visiting)
	case reflect.Bool:
		return canonicalValue{
			Kind:  "json_bool",
			Value: strconv.FormatBool(value.Bool()),
		}, true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return semanticJSONDecimal(strconv.FormatInt(value.Int(), 10)), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return semanticJSONDecimal(strconv.FormatUint(value.Uint(), 10)), true
	case reflect.Float32:
		return semanticJSONNumber(strconv.FormatFloat(value.Float(), 'g', -1, 32))
	case reflect.Float64:
		return semanticJSONNumber(strconv.FormatFloat(value.Float(), 'g', -1, 64))
	case reflect.String:
		if value.Type() == reflect.TypeOf(json.Number("")) {
			return semanticJSONNumber(value.String())
		}
		return canonicalValue{Kind: "json_string", Value: value.String()}, true
	case reflect.Slice:
		if value.IsNil() {
			return canonicalValue{Kind: "json_null"}, true
		}
		if value.Type().Elem().Kind() == reflect.Uint8 {
			return canonicalValue{}, false
		}
		return semanticJSONArray(value, visiting, true)
	case reflect.Array:
		return semanticJSONArray(value, visiting, false)
	case reflect.Map:
		return semanticJSONObject(value, visiting)
	case reflect.Struct:
		return semanticJSONStruct(value, visiting)
	default:
		return canonicalValue{}, false
	}
}

func semanticJSONSchema(value reflect.Value,
	visiting map[canonicalVisit]struct{}) (canonicalValue, bool) {
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return canonicalValue{Kind: "json_null"}, true
		}
		visit := canonicalVisit{typ: value.Type(), pointer: value.Pointer()}
		if _, exists := visiting[visit]; exists {
			return canonicalValue{}, false
		}
		visiting[visit] = struct{}{}
		defer delete(visiting, visit)
		value = value.Elem()
	}

	boolean := value.FieldByName("boolean")
	if boolean.IsValid() && !boolean.IsNil() {
		return canonicalValue{
			Kind:  "json_bool",
			Value: strconv.FormatBool(boolean.Elem().Bool()),
		}, true
	}

	fields := make(map[string]canonicalValue)
	typ := value.Type()
	for i := 0; i < value.NumField(); i++ {
		fieldType := typ.Field(i)
		if fieldType.PkgPath != "" ||
			fieldType.Name == "Type" ||
			fieldType.Name == "TypeEnhanced" ||
			fieldType.Name == "Extras" {
			continue
		}
		name, omitEmpty, include, supported := jsonFieldName(fieldType)
		if !supported {
			return canonicalValue{}, false
		}
		if !include || omitEmpty && isJSONEmptyValue(value.Field(i)) {
			continue
		}
		var (
			child canonicalValue
			ok    bool
		)
		if fieldType.Name == "Properties" {
			child, ok = semanticJSONSchemaProperties(
				value.Field(i).Interface().(*orderedmap.OrderedMap[string, *jsonschema.Schema]),
				visiting)
		} else {
			child, ok = semanticJSONValue(value.Field(i), visiting)
		}
		if !ok {
			return canonicalValue{}, false
		}
		fields[name] = child
	}

	typeValue := value.FieldByName("Type").String()
	typeEnhanced := value.FieldByName("TypeEnhanced")
	if typeValue != "" && !typeEnhanced.IsNil() {
		return canonicalValue{}, false
	}
	if typeValue != "" {
		fields["type"] = canonicalValue{Kind: "json_string", Value: typeValue}
	} else if typeEnhanced.Len() > 0 {
		child, ok := semanticJSONArray(typeEnhanced, visiting, false)
		if !ok {
			return canonicalValue{}, false
		}
		fields["type"] = child
	}

	extras := value.FieldByName("Extras")
	if !extras.IsNil() {
		iterator := extras.MapRange()
		for iterator.Next() {
			child, ok := semanticJSONValue(iterator.Value(), visiting)
			if !ok {
				return canonicalValue{}, false
			}
			fields[iterator.Key().String()] = child
		}
	}
	if len(fields) == 0 {
		return canonicalValue{Kind: "json_bool", Value: "true"}, true
	}
	return canonicalJSONObjectFields(fields), true
}

func semanticJSONSchemaProperties(
	properties *orderedmap.OrderedMap[string, *jsonschema.Schema],
	visiting map[canonicalVisit]struct{}) (canonicalValue, bool) {
	if properties == nil {
		return canonicalValue{Kind: "json_null"}, true
	}
	fields := make(map[string]canonicalValue, properties.Len())
	for pair := properties.Oldest(); pair != nil; pair = pair.Next() {
		child, ok := semanticJSONValue(reflect.ValueOf(pair.Value), visiting)
		if !ok {
			return canonicalValue{}, false
		}
		fields[pair.Key] = child
	}
	return canonicalJSONObjectFields(fields), true
}

func semanticJSONNumber(raw string) (canonicalValue, bool) {
	var parsed json.Number
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil || parsed.String() != raw {
		return canonicalValue{}, false
	}
	if !strings.ContainsAny(raw, ".eE") {
		if _, ok := new(big.Int).SetString(raw, 10); !ok {
			return canonicalValue{}, false
		}
		return semanticJSONDecimal(raw), true
	}

	number, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
		return canonicalValue{}, false
	}
	normalized, ok := normalizeSemanticJSONDecimal(raw)
	if !ok {
		return canonicalValue{}, false
	}
	return semanticJSONDecimal(normalized), true
}

func semanticJSONDecimal(value string) canonicalValue {
	return canonicalValue{
		Kind:  "json_number",
		Value: value,
	}
}

func normalizeSemanticJSONDecimal(raw string) (string, bool) {
	negative := strings.HasPrefix(raw, "-")
	unsigned := strings.TrimPrefix(raw, "-")
	mantissa := unsigned
	exponentText := ""
	if index := strings.IndexAny(unsigned, "eE"); index >= 0 {
		mantissa = unsigned[:index]
		exponentText = unsigned[index+1:]
	}

	fractionDigits := 0
	digits := mantissa
	if index := strings.IndexByte(mantissa, '.'); index >= 0 {
		fractionDigits = len(mantissa) - index - 1
		digits = mantissa[:index] + mantissa[index+1:]
	}
	digits = strings.TrimLeft(digits, "0")
	if digits == "" {
		if negative {
			return "-0", true
		}
		return "0", true
	}

	var exponent int64
	var err error
	if exponentText != "" {
		exponent, err = strconv.ParseInt(exponentText, 10, 64)
		if err != nil {
			return "", false
		}
	}
	if exponent < math.MinInt64+int64(fractionDigits) {
		return "", false
	}
	exponent -= int64(fractionDigits)

	trimmed := strings.TrimRight(digits, "0")
	trailingZeros := len(digits) - len(trimmed)
	if exponent > math.MaxInt64-int64(trailingZeros) {
		return "", false
	}
	digits = trimmed
	exponent += int64(trailingZeros)

	digitCount := int64(len(digits))
	if exponent > math.MaxInt64-digitCount {
		return "", false
	}
	decimalPoint := digitCount + exponent
	sign := ""
	if negative {
		sign = "-"
	}
	if decimalPoint <= 0 {
		if decimalPoint == math.MinInt64 {
			return "", false
		}
		zeroCount := -decimalPoint
		if zeroCount > int64(maxInt()-len(sign)-2-len(digits)) {
			return "", false
		}
		return sign + "0." + strings.Repeat("0", int(zeroCount)) + digits, true
	}
	if decimalPoint >= digitCount {
		zeroCount := decimalPoint - digitCount
		if zeroCount > int64(maxInt()-len(sign)-len(digits)) {
			return "", false
		}
		return sign + digits + strings.Repeat("0", int(zeroCount)), true
	}
	return sign + digits[:int(decimalPoint)] + "." + digits[int(decimalPoint):], true
}

func maxInt() int {
	return int(^uint(0) >> 1)
}

func semanticJSONArray(value reflect.Value, visiting map[canonicalVisit]struct{},
	trackCycle bool) (canonicalValue, bool) {
	if trackCycle {
		visit := canonicalVisit{typ: value.Type(), pointer: value.Pointer()}
		if _, exists := visiting[visit]; exists {
			return canonicalValue{}, false
		}
		visiting[visit] = struct{}{}
		defer delete(visiting, visit)
	}
	canonical := canonicalValue{
		Kind:    "json_array",
		Entries: make([]canonicalEntry, value.Len()),
	}
	for i := 0; i < value.Len(); i++ {
		child, ok := semanticJSONValue(value.Index(i), visiting)
		if !ok {
			return canonicalValue{}, false
		}
		canonical.Entries[i].Value = child
	}
	return canonical, true
}

func semanticJSONObject(value reflect.Value,
	visiting map[canonicalVisit]struct{}) (canonicalValue, bool) {
	if value.IsNil() {
		return canonicalValue{Kind: "json_null"}, true
	}
	visit := canonicalVisit{typ: value.Type(), pointer: value.Pointer()}
	if _, exists := visiting[visit]; exists {
		return canonicalValue{}, false
	}
	visiting[visit] = struct{}{}
	defer delete(visiting, visit)

	fields := make(map[string]canonicalValue, value.Len())
	iterator := value.MapRange()
	for iterator.Next() {
		key, ok := semanticJSONMapKey(iterator.Key())
		if !ok {
			return canonicalValue{}, false
		}
		if _, duplicate := fields[key]; duplicate {
			return canonicalValue{}, false
		}
		child, ok := semanticJSONValue(iterator.Value(), visiting)
		if !ok {
			return canonicalValue{}, false
		}
		fields[key] = child
	}
	return canonicalJSONObjectFields(fields), true
}

func semanticJSONMapKey(value reflect.Value) (string, bool) {
	if hasCustomMarshaler(value.Type()) {
		return "", false
	}
	switch value.Kind() {
	case reflect.String:
		return value.String(), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(value.Int(), 10), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return strconv.FormatUint(value.Uint(), 10), true
	default:
		return "", false
	}
}

func semanticJSONStruct(value reflect.Value,
	visiting map[canonicalVisit]struct{}) (canonicalValue, bool) {
	fields := make(map[string]canonicalValue)
	typ := value.Type()
	for i := 0; i < value.NumField(); i++ {
		fieldType := typ.Field(i)
		if fieldType.PkgPath != "" {
			continue
		}
		if fieldType.Anonymous {
			return canonicalValue{}, false
		}
		name, omitEmpty, include, supported := jsonFieldName(fieldType)
		if !supported {
			return canonicalValue{}, false
		}
		if !include || omitEmpty && isJSONEmptyValue(value.Field(i)) {
			continue
		}
		if _, duplicate := fields[name]; duplicate {
			return canonicalValue{}, false
		}
		child, ok := semanticJSONValue(value.Field(i), visiting)
		if !ok {
			return canonicalValue{}, false
		}
		fields[name] = child
	}
	return canonicalJSONObjectFields(fields), true
}

func canonicalJSONObjectFields(fields map[string]canonicalValue) canonicalValue {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	canonical := canonicalValue{
		Kind:    "json_object",
		Entries: make([]canonicalEntry, len(keys)),
	}
	for i, key := range keys {
		canonical.Entries[i] = canonicalEntry{
			Key:   canonicalValue{Kind: "json_string", Value: key},
			Value: fields[key],
		}
	}
	return canonical
}

func jsonFieldName(field reflect.StructField) (
	name string, omitEmpty, include, supported bool,
) {
	tag := field.Tag.Get("json")
	if tag == "-" {
		return "", false, false, true
	}
	parts := strings.Split(tag, ",")
	name = parts[0]
	if name == "" {
		name = field.Name
	} else if !isValidJSONTagName(name) {
		return "", false, false, false
	}
	for _, option := range parts[1:] {
		if option == "omitempty" {
			omitEmpty = true
		}
		if option == "string" {
			return "", false, false, false
		}
	}
	return name, omitEmpty, true, true
}

func isValidJSONTagName(name string) bool {
	if name == "" {
		return false
	}
	for _, character := range name {
		switch {
		case strings.ContainsRune("!#$%&()*+-./:;<=>?@[]^_{|}~ ", character):
		case !unicode.IsLetter(character) && !unicode.IsDigit(character):
			return false
		}
	}
	return true
}

func isJSONEmptyValue(value reflect.Value) bool {
	switch value.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return value.Len() == 0
	case reflect.Bool:
		return !value.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return value.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32,
		reflect.Uint64, reflect.Uintptr:
		return value.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return value.Float() == 0
	case reflect.Interface, reflect.Pointer:
		return value.IsNil()
	default:
		return false
	}
}

func hasCustomMarshaler(typ reflect.Type) bool {
	return typeImplementsEncoding(typ, jsonMarshalerType) ||
		typeImplementsEncoding(typ, textMarshalerType) ||
		typeImplementsEncoding(typ, gobEncoderType) ||
		typeImplementsEncoding(typ, binaryMarshalerType)
}

func typeImplementsEncoding(typ, encodingType reflect.Type) bool {
	if typ.Implements(encodingType) {
		return true
	}
	return typ.Kind() != reflect.Pointer && reflect.PointerTo(typ).Implements(encodingType)
}

func semanticPointer(value reflect.Value, canonical canonicalValue,
	visiting map[canonicalVisit]struct{}, sendZero bool) (canonicalValue, bool) {
	visit := canonicalVisit{typ: value.Type(), pointer: value.Pointer()}
	if _, exists := visiting[visit]; exists {
		return canonicalValue{}, false
	}
	visiting[visit] = struct{}{}
	defer delete(visiting, visit)
	child, ok := semanticCanonicalValue(value.Elem(), visiting, sendZero)
	if !ok {
		return canonicalValue{}, false
	}
	canonical.Entries = []canonicalEntry{{Value: child}}
	return canonical, true
}

func semanticSequence(value reflect.Value, canonical canonicalValue,
	visiting map[canonicalVisit]struct{}, trackCycle bool) (canonicalValue, bool) {
	if trackCycle && value.Len() == 0 {
		// A fresh Gob destination decodes every zero-length ordinary slice as
		// nil, regardless of the source slice's nilness or capacity.
		canonical.IsNil = true
		return canonical, true
	}
	var visit canonicalVisit
	if trackCycle {
		visit = canonicalVisit{typ: value.Type(), pointer: value.Pointer()}
		if _, exists := visiting[visit]; exists {
			return canonicalValue{}, false
		}
		visiting[visit] = struct{}{}
		defer delete(visiting, visit)
	}
	canonical.Entries = make([]canonicalEntry, value.Len())
	for i := 0; i < value.Len(); i++ {
		child, ok := semanticCanonicalValue(value.Index(i), visiting, true)
		if !ok {
			return canonicalValue{}, false
		}
		canonical.Entries[i].Value = child
	}
	return canonical, true
}

func semanticMap(value reflect.Value, canonical canonicalValue,
	visiting map[canonicalVisit]struct{}) (canonicalValue, bool) {
	visit := canonicalVisit{typ: value.Type(), pointer: value.Pointer()}
	if _, exists := visiting[visit]; exists {
		return canonicalValue{}, false
	}
	visiting[visit] = struct{}{}
	defer delete(visiting, visit)

	type encodedEntry struct {
		data  []byte
		entry canonicalEntry
	}
	entries := make([]encodedEntry, 0, value.Len())
	keys := make(map[string]struct{}, value.Len())
	iterator := value.MapRange()
	for iterator.Next() {
		key, ok := semanticCanonicalMapKey(iterator.Key(), visiting)
		if !ok {
			return canonicalValue{}, false
		}
		keyData, err := json.Marshal(key)
		if err != nil {
			return canonicalValue{}, false
		}
		keyIdentity := string(keyData)
		if _, duplicate := keys[keyIdentity]; duplicate {
			return canonicalValue{}, false
		}
		keys[keyIdentity] = struct{}{}
		mapValue, ok := semanticCanonicalValue(iterator.Value(), visiting, true)
		if !ok {
			return canonicalValue{}, false
		}
		entry := canonicalEntry{Key: key, Value: mapValue}
		data, err := json.Marshal(entry)
		if err != nil {
			return canonicalValue{}, false
		}
		entries = append(entries, encodedEntry{data: data, entry: entry})
	}
	sort.Slice(entries, func(left, right int) bool {
		return bytes.Compare(entries[left].data, entries[right].data) < 0
	})
	canonical.Entries = make([]canonicalEntry, len(entries))
	for i := range entries {
		canonical.Entries[i] = entries[i].entry
	}
	return canonical, true
}

func semanticCanonicalMapKey(value reflect.Value,
	visiting map[canonicalVisit]struct{}) (canonicalValue, bool) {
	canonical, ok := semanticCanonicalValue(value, visiting, true)
	if !ok || !normalizeSemanticMapKeyZeros(value, &canonical) {
		return canonicalValue{}, false
	}
	return canonical, true
}

func normalizeSemanticMapKeyZeros(value reflect.Value, canonical *canonicalValue) bool {
	if !value.IsValid() {
		return canonical != nil && canonical.Kind == "invalid"
	}
	if canonical == nil || value.Type() == toolInfoType || value.Type() == toolInfoPointerType {
		return false
	}

	switch value.Kind() {
	case reflect.Float32:
		if float32(value.Float()) == 0 {
			canonical.Value = "0"
		}
	case reflect.Float64:
		if value.Float() == 0 {
			canonical.Value = "0"
		}
	case reflect.Complex64:
		number := complex64(value.Complex())
		realBits := math.Float32bits(real(number))
		imaginaryBits := math.Float32bits(imag(number))
		if real(number) == 0 {
			realBits = 0
		}
		if imag(number) == 0 {
			imaginaryBits = 0
		}
		canonical.Value = strconv.FormatUint(uint64(realBits), 16) +
			":" + strconv.FormatUint(uint64(imaginaryBits), 16)
	case reflect.Complex128:
		number := value.Complex()
		realBits := math.Float64bits(real(number))
		imaginaryBits := math.Float64bits(imag(number))
		if real(number) == 0 {
			realBits = 0
		}
		if imag(number) == 0 {
			imaginaryBits = 0
		}
		canonical.Value = strconv.FormatUint(realBits, 16) +
			":" + strconv.FormatUint(imaginaryBits, 16)
	case reflect.Interface, reflect.Pointer:
		if value.IsNil() {
			return true
		}
		if len(canonical.Entries) != 1 {
			return false
		}
		return normalizeSemanticMapKeyZeros(value.Elem(), &canonical.Entries[0].Value)
	case reflect.Array:
		if len(canonical.Entries) != value.Len() {
			return false
		}
		for i := 0; i < value.Len(); i++ {
			if !normalizeSemanticMapKeyZeros(
				value.Index(i), &canonical.Entries[i].Value) {
				return false
			}
		}
	case reflect.Struct:
		canonicalIndex := 0
		for i := 0; i < value.NumField(); i++ {
			field := value.Type().Field(i)
			if field.PkgPath != "" || field.Type.Kind() == reflect.Func ||
				field.Type.Kind() == reflect.Chan {
				continue
			}
			if canonicalIndex >= len(canonical.Fields) ||
				canonical.Fields[canonicalIndex].Name != field.Name ||
				!normalizeSemanticMapKeyZeros(
					value.Field(i), &canonical.Fields[canonicalIndex].Value) {
				return false
			}
			canonicalIndex++
		}
		return canonicalIndex == len(canonical.Fields)
	}
	return true
}

func gobInterfacePointerIndirectionUnprovable(value reflect.Value) bool {
	if !value.IsValid() || value.Kind() != reflect.Pointer {
		return false
	}
	for value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return true
		}
		if hasGobExternalEncoding(value.Type()) {
			return false
		}
		value = value.Elem()
	}
	return !hasGobExternalEncoding(value.Type()) && value.IsZero()
}

func gobFieldValueOmitted(value reflect.Value) (bool, bool) {
	if !value.IsValid() {
		return true, true
	}
	for value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return true, true
		}
		if hasGobExternalEncoding(value.Type()) {
			return false, true
		}
		value = value.Elem()
	}
	if hasGobExternalEncoding(value.Type()) {
		return false, true
	}

	switch value.Kind() {
	case reflect.Bool:
		return !value.Bool(), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return value.Int() == 0, true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32,
		reflect.Uint64, reflect.Uintptr:
		return value.Uint() == 0, true
	case reflect.Float32, reflect.Float64:
		return value.Float() == 0, true
	case reflect.Complex64, reflect.Complex128:
		return value.Complex() == 0, true
	case reflect.String:
		return value.Len() == 0, true
	case reflect.Slice:
		return value.Len() == 0, true
	case reflect.Map, reflect.Interface:
		return value.IsNil(), true
	case reflect.Array, reflect.Struct:
		return false, true
	default:
		return false, false
	}
}

func hasGobExternalEncoding(typ reflect.Type) bool {
	if typ.Implements(gobEncoderType) || typ.Implements(binaryMarshalerType) {
		return true
	}
	if typ.Kind() == reflect.Pointer {
		return false
	}
	pointerType := reflect.PointerTo(typ)
	return pointerType.Implements(gobEncoderType) ||
		pointerType.Implements(binaryMarshalerType)
}

func canonicalTypeName(typ reflect.Type) string {
	pointerPrefix := ""
	for typ.Kind() == reflect.Pointer {
		pointerPrefix += "*"
		typ = typ.Elem()
	}
	if typ.Name() == "" {
		return pointerPrefix + typ.String()
	}
	if typ.PkgPath() == "" {
		return pointerPrefix + typ.Name()
	}
	return pointerPrefix + typ.PkgPath() + ":" + typ.Name()
}

func isNilableKind(kind reflect.Kind) bool {
	switch kind {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return true
	default:
		return false
	}
}
