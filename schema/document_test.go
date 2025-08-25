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

package schema

import (
	"testing"

	"github.com/smartystreets/goconvey/convey"
)

func TestDocument(t *testing.T) {
	convey.Convey("test document", t, func() {
		var (
			subIndexes = []string{"hello", "bye"}
			score      = 1.1
			extraInfo  = "asd"
			dslInfo    = map[string]any{"hello": true}
			vector     = []float64{1.1, 2.2}
		)

		d := &Document{
			ID:       "asd",
			Content:  "qwe",
			MetaData: nil,
		}

		d.WithSubIndexes(subIndexes).
			WithDenseVector(vector).
			WithScore(score).
			WithExtraInfo(extraInfo).
			WithDSLInfo(dslInfo)

		convey.So(d.SubIndexes(), convey.ShouldEqual, subIndexes)
		convey.So(d.Score(), convey.ShouldEqual, score)
		convey.So(d.ExtraInfo(), convey.ShouldEqual, extraInfo)
		convey.So(d.DSLInfo(), convey.ShouldEqual, dslInfo)
		convey.So(d.DenseVector(), convey.ShouldEqual, vector)
	})
}

func TestDocumentUtilityMethods(t *testing.T) {
	convey.Convey("test document utility methods", t, func() {
		convey.Convey("test Clone method", func() {
			original := &Document{
				ID:       "test-id",
				Content:  "test content",
				MetaData: map[string]any{"key1": "value1", "key2": 42},
			}

			clone := original.Clone()

			convey.So(clone, convey.ShouldNotBeNil)
			convey.So(clone.ID, convey.ShouldEqual, original.ID)
			convey.So(clone.Content, convey.ShouldEqual, original.Content)
			convey.So(clone.MetaData["key1"], convey.ShouldEqual, "value1")
			convey.So(clone.MetaData["key2"], convey.ShouldEqual, 42)

			// Test independence: modifying clone doesn't affect original
			clone.MetaData["key3"] = "new value"
			convey.So(original.MetaData["key3"], convey.ShouldBeNil)

			// Test edge cases
			var nilDoc *Document
			convey.So(nilDoc.Clone(), convey.ShouldBeNil)

			docWithNilMeta := &Document{ID: "test", Content: "test"}
			convey.So(docWithNilMeta.Clone().MetaData, convey.ShouldBeNil)
		})

		convey.Convey("test IsEmpty method", func() {
			convey.So((&Document{}).IsEmpty(), convey.ShouldBeTrue)
			convey.So((&Document{MetaData: map[string]any{}}).IsEmpty(), convey.ShouldBeTrue)

			convey.So((&Document{ID: "test"}).IsEmpty(), convey.ShouldBeFalse)
			convey.So((&Document{Content: "test"}).IsEmpty(), convey.ShouldBeFalse)
			convey.So((&Document{MetaData: map[string]any{"key": "value"}}).IsEmpty(), convey.ShouldBeFalse)
		})

		convey.Convey("test HasMetaData method", func() {
			doc := &Document{
				MetaData: map[string]any{"existing_key": "value", "nil_value": nil},
			}

			convey.So(doc.HasMetaData("existing_key"), convey.ShouldBeTrue)
			convey.So(doc.HasMetaData("nil_value"), convey.ShouldBeTrue) // nil value still counts as existing
			convey.So(doc.HasMetaData("non_existing"), convey.ShouldBeFalse)

			// Test with nil metadata
			convey.So((&Document{}).HasMetaData("any_key"), convey.ShouldBeFalse)
		})

		convey.Convey("test ClearMetaData method", func() {
			doc := &Document{
				ID:       "test-id",
				Content:  "test content",
				MetaData: map[string]any{"key1": "value1", "key2": "value2"},
			}

			result := doc.ClearMetaData()

			convey.So(result, convey.ShouldEqual, doc) // Should return same instance for chaining
			convey.So(doc.MetaData, convey.ShouldBeNil)
			convey.So(doc.ID, convey.ShouldEqual, "test-id") // Other fields unchanged
		})

		convey.Convey("test WithMetaData and GetMetaData methods", func() {
			doc := &Document{ID: "test-id"}

			// Test setting and getting metadata
			result := doc.WithMetaData("custom_key", "custom_value")
			convey.So(result, convey.ShouldEqual, doc) // Should return same instance
			convey.So(doc.MetaData, convey.ShouldNotBeNil)

			value, exists := doc.GetMetaData("custom_key")
			convey.So(exists, convey.ShouldBeTrue)
			convey.So(value, convey.ShouldEqual, "custom_value")

			// Test different value types
			doc.WithMetaData("int_key", 42).WithMetaData("bool_key", true)

			intValue, intExists := doc.GetMetaData("int_key")
			convey.So(intExists, convey.ShouldBeTrue)
			convey.So(intValue, convey.ShouldEqual, 42)

			// Test non-existing key
			_, nonExists := doc.GetMetaData("non_existing")
			convey.So(nonExists, convey.ShouldBeFalse)

			// Test GetMetaData with nil MetaData
			nilValue, nilExists := (&Document{}).GetMetaData("any_key")
			convey.So(nilExists, convey.ShouldBeFalse)
			convey.So(nilValue, convey.ShouldBeNil)
		})

		convey.Convey("test method chaining", func() {
			doc := &Document{}

			result := doc.WithMetaData("key1", "value1").
				WithScore(0.95).
				WithExtraInfo("test info")

			convey.So(result, convey.ShouldEqual, doc)
			convey.So(doc.Score(), convey.ShouldEqual, 0.95)
			convey.So(doc.ExtraInfo(), convey.ShouldEqual, "test info")

			value1, exists1 := doc.GetMetaData("key1")
			convey.So(exists1, convey.ShouldBeTrue)
			convey.So(value1, convey.ShouldEqual, "value1")
		})
	})
}
