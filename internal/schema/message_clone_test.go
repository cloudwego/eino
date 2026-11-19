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

package schema

import (
	"reflect"
	"sync"
	"testing"

	"github.com/eino-contrib/jsonschema"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/schema"
)

// Populate all known fields, including mutually exclusive payloads. Clone must
// preserve them, not normalize an event or silently discard a newly added field.
func TestCloneMessageKnownFields(t *testing.T) {
	t.Run("Message", func(t *testing.T) {
		source := populatedMessageValue(reflect.TypeOf((*schema.Message)(nil))).Interface().(*schema.Message)
		cloned := CloneMessage(source)
		require.Equal(t, source, cloned)
		assertMessageCloneOwnership(t, reflect.ValueOf(source), reflect.ValueOf(cloned), "Message")
	})
	t.Run("AgenticMessage", func(t *testing.T) {
		source := populatedMessageValue(reflect.TypeOf((*schema.AgenticMessage)(nil))).Interface().(*schema.AgenticMessage)
		cloned := CloneMessage(source)
		require.Equal(t, source, cloned)
		assertMessageCloneOwnership(t, reflect.ValueOf(source), reflect.ValueOf(cloned), "AgenticMessage")
	})
}

var sharedParamsType = reflect.TypeOf((*schema.ParamsOneOf)(nil))
var sharedJSONSchemaType = reflect.TypeOf((*jsonschema.Schema)(nil))

func populatedMessageValue(typ reflect.Type) reflect.Value {
	if typ == sharedParamsType {
		param := &schema.ParameterInfo{}
		param.ElemInfo = param // Shared definitions may contain recursive references.
		return reflect.ValueOf(schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{"p": param}))
	}
	if typ == sharedJSONSchemaType {
		value := &jsonschema.Schema{Type: "object"}
		value.Items = value
		return reflect.ValueOf(value)
	}
	v := reflect.New(typ).Elem()
	switch typ.Kind() {
	case reflect.Ptr:
		v.Set(reflect.New(typ.Elem()))
		v.Elem().Set(populatedMessageValue(typ.Elem()))
	case reflect.Struct:
		for i := 0; i < typ.NumField(); i++ {
			v.Field(i).Set(populatedMessageValue(typ.Field(i).Type))
		}
	case reflect.Slice:
		v.Set(reflect.MakeSlice(typ, 2, 2))
		v.Index(0).Set(populatedMessageValue(typ.Elem()))
		// The second pointer element stays nil, exercising nil elements too.
	case reflect.Map:
		v.Set(reflect.MakeMap(typ))
		v.SetMapIndex(reflect.ValueOf("entry"), populatedMessageValue(typ.Elem()))
	case reflect.Interface:
		v.Set(reflect.ValueOf(&struct{ Value string }{Value: "opaque"}))
	case reflect.String:
		v.SetString("value")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(7)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(7)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(0.75)
	default:
		panic("unhandled fixture type: " + typ.String())
	}
	return v
}

func assertMessageCloneOwnership(t *testing.T, source, cloned reflect.Value, path string) {
	t.Helper()
	switch source.Kind() {
	case reflect.Ptr, reflect.Slice, reflect.Map, reflect.Interface:
		if source.IsNil() {
			require.True(t, cloned.IsNil(), path)
			return
		}
	}
	if source.Type() == sharedParamsType || source.Type() == sharedJSONSchemaType {
		require.Equal(t, source.Pointer(), cloned.Pointer(), path+" must remain shared")
		return
	}
	switch source.Kind() {
	case reflect.Interface:
		// Opaque values remain exactly the same objects; do not traverse their graph.
		require.Equal(t, source.Elem().Pointer(), cloned.Elem().Pointer(), path)
	case reflect.Ptr:
		if source.IsNil() {
			require.True(t, cloned.IsNil(), path)
			return
		}
		require.NotEqual(t, source.Pointer(), cloned.Pointer(), path+" must be isolated")
		assertMessageCloneOwnership(t, source.Elem(), cloned.Elem(), path+".*")
	case reflect.Struct:
		for i := 0; i < source.NumField(); i++ {
			assertMessageCloneOwnership(t, source.Field(i), cloned.Field(i), path+"."+source.Type().Field(i).Name)
		}
	case reflect.Slice:
		require.NotEqual(t, source.Pointer(), cloned.Pointer(), path+" backing array must be isolated")
		for i := 0; i < source.Len(); i++ {
			assertMessageCloneOwnership(t, source.Index(i), cloned.Index(i), path+"[]")
		}
	case reflect.Map:
		for _, k := range source.MapKeys() {
			assertMessageCloneOwnership(t, source.MapIndex(k), cloned.MapIndex(k), path+"["+k.String()+"]")
		}
		// Replacing a map value must affect only that map, even though its previous
		// opaque value was shared.
		key := reflect.ValueOf("entry")
		source.SetMapIndex(key, reflect.ValueOf("changed"))
		require.NotEqual(t, source.MapIndex(key).Interface(), cloned.MapIndex(key).Interface(), path)
	}
}

func TestCloneMessagePreservesNilAndEmpty(t *testing.T) {
	require.Nil(t, CloneMessage((*schema.Message)(nil)))
	require.Nil(t, CloneMessage((*schema.AgenticMessage)(nil)))
	require.Nil(t, CloneMessages([]*schema.Message(nil)))
	require.NotNil(t, CloneMessages([]*schema.Message{}))
	require.Equal(t, []*schema.AgenticMessage{nil}, CloneMessages([]*schema.AgenticMessage{nil}))
	for _, msg := range []*schema.Message{
		{},
		{Extra: map[string]any{}, ToolCalls: []schema.ToolCall{}, UserInputMultiContent: []schema.MessageInputPart{}},
	} {
		require.Equal(t, msg, CloneMessage(msg))
	}
	for _, msg := range []*schema.AgenticMessage{
		{},
		{Extra: map[string]any{}, ContentBlocks: []*schema.ContentBlock{}},
	} {
		require.Equal(t, msg, CloneMessage(msg))
	}
}

func TestCloneMessageOpaqueValuesAreNotCopied(t *testing.T) {
	opaque := &struct {
		mu   sync.Mutex
		data map[string]string
	}{data: map[string]string{"key": "value"}}
	// A used/locked mutex and opaque cycles must never be traversed or copied.
	opaque.mu.Lock()
	defer opaque.mu.Unlock()
	extra := map[string]any{"payload": opaque}
	extra["self"] = extra
	msg := &schema.AgenticMessage{
		Extra:        extra,
		ResponseMeta: &schema.AgenticResponseMeta{Extension: opaque},
		ContentBlocks: []*schema.ContentBlock{{
			AssistantGenText: &schema.AssistantGenText{Extension: opaque},
			ServerToolCall:   &schema.ServerToolCall{Arguments: opaque},
			ServerToolResult: &schema.ServerToolResult{Content: opaque},
		}},
	}
	cloned := CloneMessage(msg)
	require.Same(t, opaque, cloned.Extra["payload"])
	require.Same(t, opaque, cloned.ResponseMeta.Extension)
	require.Same(t, opaque, cloned.ContentBlocks[0].AssistantGenText.Extension)
	require.Same(t, opaque, cloned.ContentBlocks[0].ServerToolCall.Arguments)
	require.Same(t, opaque, cloned.ContentBlocks[0].ServerToolResult.Content)
	delete(cloned.Extra, "payload")
	require.Contains(t, msg.Extra, "payload")
	require.Contains(t, cloned.Extra["self"], "payload", "opaque map values still refer to the original map")
}

// Exercise the boundary between mutable tool metadata and the tool's shared
// parameter definitions through both supported message representations.
func TestCloneMessageToolSearchResult(t *testing.T) {
	check := func(t *testing.T, cloneResult func(*schema.ToolSearchResult) *schema.ToolSearchResult) {
		t.Helper()
		definition := &jsonschema.Schema{Type: "object"}
		definition.Items = definition
		type annotation struct{ Label string }
		labels := []annotation{{Label: "original"}}
		tool := &schema.ToolInfo{Name: "search", Desc: "original", ParamsOneOf: schema.NewParamsOneOfByJSONSchema(definition),
			Extra: map[string]any{"labels": labels, "optional": (*annotation)(nil)},
		}
		original := &schema.ToolSearchResult{Tools: []*schema.ToolInfo{nil, tool}}
		cloned := cloneResult(original)
		require.Equal(t, original, cloned)
		require.NotSame(t, original, cloned)
		require.NotSame(t, &original.Tools[0], &cloned.Tools[0])
		copiedTool := cloned.Tools[1]
		require.NotSame(t, tool, copiedTool)
		require.Same(t, tool.ParamsOneOf, copiedTool.ParamsOneOf)
		require.Same(t, &labels[0], &copiedTool.Extra["labels"].([]annotation)[0], "opaque slice retains its backing array")
		require.IsType(t, (*annotation)(nil), copiedTool.Extra["optional"])
		require.Nil(t, copiedTool.Extra["optional"])

		cloned.Tools[0] = copiedTool
		copiedTool.Name, copiedTool.Desc = "other", "updated"
		delete(copiedTool.Extra, "labels")
		copiedTool.Extra["new"] = true
		require.Nil(t, original.Tools[0])
		require.Equal(t, "search", tool.Name)
		require.Equal(t, "original", tool.Desc)
		require.Contains(t, tool.Extra, "labels")
		require.NotContains(t, tool.Extra, "new")
	}
	t.Run("Message", func(t *testing.T) {
		check(t, func(result *schema.ToolSearchResult) *schema.ToolSearchResult {
			msg := &schema.Message{UserInputMultiContent: []schema.MessageInputPart{{ToolSearchResult: result}}}
			return CloneMessage(msg).UserInputMultiContent[0].ToolSearchResult
		})
	})
	t.Run("AgenticMessage", func(t *testing.T) {
		check(t, func(result *schema.ToolSearchResult) *schema.ToolSearchResult {
			msg := &schema.AgenticMessage{ContentBlocks: []*schema.ContentBlock{{ToolSearchFunctionToolResult: &schema.ToolSearchFunctionToolResult{Result: result}}}}
			return CloneMessage(msg).ContentBlocks[0].ToolSearchFunctionToolResult.Result
		})
	})
}

func TestCloneMessageConcurrentIndependentEdits(t *testing.T) {
	source := &schema.Message{
		Content: "original", Extra: map[string]any{"key": "original"},
		ToolCalls:                []schema.ToolCall{{Extra: map[string]any{"key": "original"}}},
		AssistantGenMultiContent: []schema.MessageOutputPart{{StreamingMeta: &schema.MessageStreamingMeta{Index: 1}}},
	}
	cloned := CloneMessage(source)
	var wg sync.WaitGroup
	for _, message := range []*schema.Message{source, cloned} {
		wg.Add(1)
		go func(m *schema.Message) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				m.Extra["key"] = i
				m.ToolCalls[0].Extra["key"] = i
				m.AssistantGenMultiContent[0].StreamingMeta.Index = i
			}
		}(message)
	}
	wg.Wait()
}
