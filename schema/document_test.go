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
	"encoding/json"
	"testing"

	"github.com/smartystreets/goconvey/convey"
	"github.com/stretchr/testify/require"
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

func TestDocumentTypedMetadataAfterJSONRoundTrip(t *testing.T) {
	subIndexes := []string{"public", "archive"}
	dense := []float64{0.25, 1.5}
	sparse := map[int]float64{0: 0.75, 42: -1.25}
	original := (&Document{
		ID:       "document-1",
		Content:  "indexed content",
		MetaData: map[string]any{"source": "notes.md"},
	}).WithSubIndexes(subIndexes).WithDenseVector(dense).WithSparseVector(sparse)

	encoded, err := json.Marshal(original)
	require.NoError(t, err)
	var decoded Document
	require.NoError(t, json.Unmarshal(encoded, &decoded))

	assertions := []struct {
		name string
		want any
		got  any
	}{
		{"sub indexes", subIndexes, decoded.SubIndexes()},
		{"dense vector", dense, decoded.DenseVector()},
		{"sparse vector", sparse, decoded.SparseVector()},
		{"custom metadata", "notes.md", decoded.MetaData["source"]},
	}
	for _, assertion := range assertions {
		t.Run(assertion.name, func(t *testing.T) {
			require.Equal(t, assertion.want, assertion.got)
		})
	}
}
