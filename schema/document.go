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

const (
	docMetaDataKeySubIndexes   = "_sub_indexes"
	docMetaDataKeyScore        = "_score"
	docMetaDataKeyExtraInfo    = "_extra_info"
	docMetaDataKeyDSL          = "_dsl"
	docMetaDataKeyDenseVector  = "_dense_vector"
	docMetaDataKeySparseVector = "_sparse_vector"
)

// Document is a piece of text with metadata.
type Document struct {
	// ID is the unique identifier of the document.
	ID string `json:"id"`
	// Content is the content of the document.
	Content string `json:"content"`
	// MetaData is the metadata of the document, can be used to store extra information.
	MetaData map[string]any `json:"meta_data"`
}

// String returns the content of the document.
func (d *Document) String() string {
	return d.Content
}

// WithSubIndexes sets the sub indexes of the document.
// can use doc.SubIndexes() to get the sub indexes, useful for search engine to use sub indexes to search.
func (d *Document) WithSubIndexes(indexes []string) *Document {
	if d.MetaData == nil {
		d.MetaData = make(map[string]any)
	}

	d.MetaData[docMetaDataKeySubIndexes] = indexes

	return d
}

// SubIndexes returns the sub indexes of the document.
// can use doc.WithSubIndexes() to set the sub indexes.
func (d *Document) SubIndexes() []string {
	if d.MetaData == nil {
		return nil
	}

	indexes, ok := d.MetaData[docMetaDataKeySubIndexes].([]string)
	if ok {
		return indexes
	}

	return nil
}

// WithScore sets the score of the document.
// can use doc.Score() to get the score.
func (d *Document) WithScore(score float64) *Document {
	if d.MetaData == nil {
		d.MetaData = make(map[string]any)
	}

	d.MetaData[docMetaDataKeyScore] = score

	return d
}

// Score returns the score of the document.
// can use doc.WithScore() to set the score.
func (d *Document) Score() float64 {
	if d.MetaData == nil {
		return 0
	}

	score, ok := d.MetaData[docMetaDataKeyScore].(float64)
	if ok {
		return score
	}

	return 0
}

// WithExtraInfo sets the extra info of the document.
// can use doc.ExtraInfo() to get the extra info.
func (d *Document) WithExtraInfo(extraInfo string) *Document {
	if d.MetaData == nil {
		d.MetaData = make(map[string]any)
	}

	d.MetaData[docMetaDataKeyExtraInfo] = extraInfo

	return d
}

// ExtraInfo returns the extra info of the document.
// can use doc.WithExtraInfo() to set the extra info.
func (d *Document) ExtraInfo() string {
	if d.MetaData == nil {
		return ""
	}

	extraInfo, ok := d.MetaData[docMetaDataKeyExtraInfo].(string)
	if ok {
		return extraInfo
	}

	return ""
}

// WithDSLInfo sets the dsl info of the document.
// can use doc.DSLInfo() to get the dsl info.
func (d *Document) WithDSLInfo(dslInfo map[string]any) *Document {
	if d.MetaData == nil {
		d.MetaData = make(map[string]any)
	}

	d.MetaData[docMetaDataKeyDSL] = dslInfo

	return d
}

// DSLInfo returns the dsl info of the document.
// can use doc.WithDSLInfo() to set the dsl info.
func (d *Document) DSLInfo() map[string]any {
	if d.MetaData == nil {
		return nil
	}

	dslInfo, ok := d.MetaData[docMetaDataKeyDSL].(map[string]any)
	if ok {
		return dslInfo
	}

	return nil
}

// WithDenseVector sets the dense vector of the document.
// can use doc.DenseVector() to get the dense vector.
func (d *Document) WithDenseVector(vector []float64) *Document {
	if d.MetaData == nil {
		d.MetaData = make(map[string]any)
	}

	d.MetaData[docMetaDataKeyDenseVector] = vector

	return d
}

// DenseVector returns the dense vector of the document.
// can use doc.WithDenseVector() to set the dense vector.
func (d *Document) DenseVector() []float64 {
	if d.MetaData == nil {
		return nil
	}

	vector, ok := d.MetaData[docMetaDataKeyDenseVector].([]float64)
	if ok {
		return vector
	}

	return nil
}

// WithSparseVector sets the sparse vector of the document, key indices -> value vector.
// can use doc.SparseVector() to get the sparse vector.
func (d *Document) WithSparseVector(sparse map[int]float64) *Document {
	if d.MetaData == nil {
		d.MetaData = make(map[string]any)
	}

	d.MetaData[docMetaDataKeySparseVector] = sparse

	return d
}

// SparseVector returns the sparse vector of the document, key indices -> value vector.
// can use doc.WithSparseVector() to set the sparse vector.
func (d *Document) SparseVector() map[int]float64 {
	if d.MetaData == nil {
		return nil
	}

	sparse, ok := d.MetaData[docMetaDataKeySparseVector].(map[int]float64)
	if ok {
		return sparse
	}

	return nil
}

// Clone creates a deep copy of the document.
// This is useful when you need to create a copy of the document without affecting the original.
func (d *Document) Clone() *Document {
	if d == nil {
		return nil
	}

	clone := &Document{
		ID:      d.ID,
		Content: d.Content,
	}

	if d.MetaData != nil {
		clone.MetaData = make(map[string]any, len(d.MetaData))
		for k, v := range d.MetaData {
			// For simplicity, we do a shallow copy of the values.
			// For deep copy of complex types, users can implement their own logic.
			clone.MetaData[k] = v
		}
	}

	return clone
}

// IsEmpty checks if the document is empty (no ID, no content, and no metadata).
func (d *Document) IsEmpty() bool {
	return d.ID == "" && d.Content == "" && len(d.MetaData) == 0
}

// HasMetaData checks if the document contains the specified metadata key.
func (d *Document) HasMetaData(key string) bool {
	if d.MetaData == nil {
		return false
	}
	_, exists := d.MetaData[key]
	return exists
}

// ClearMetaData removes all metadata from the document.
// Returns the document itself for method chaining.
func (d *Document) ClearMetaData() *Document {
	d.MetaData = nil
	return d
}

// WithMetaData sets custom metadata for the document.
// This is a generic method for setting any metadata key-value pair.
// Returns the document itself for method chaining.
func (d *Document) WithMetaData(key string, value any) *Document {
	if d.MetaData == nil {
		d.MetaData = make(map[string]any)
	}
	d.MetaData[key] = value
	return d
}

// GetMetaData retrieves custom metadata from the document.
// Returns the value and a boolean indicating whether the key exists.
func (d *Document) GetMetaData(key string) (any, bool) {
	if d.MetaData == nil {
		return nil, false
	}
	value, exists := d.MetaData[key]
	return value, exists
}
