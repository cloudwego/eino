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

package compose

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/utils/generic"
)

func mapFrom[T any](input any, mappings []*Mapping) (T, error) {
	f := fieldMap(mappings)
	m, err := f(input)
	if err != nil {
		var t T
		return t, err
	}

	a, err := mappingAssign[T](m, false)
	if err != nil {
		var t T
		return t, err
	}

	return a.(T), nil
}

func TestFieldMapping(t *testing.T) {
	t.Run("whole mapped to whole", func(t *testing.T) {
		m := []*Mapping{NewMapping("1")}

		out, err := mapFrom[string]("correct input", m)
		assert.NoError(t, err)
		assert.Equal(t, "correct input", out)

		out, err = mapFrom[string]("", m)
		assert.NoError(t, err)
		assert.Equal(t, "", out)

		_, err = mapFrom[string](1, m)
		assert.ErrorContains(t, err, "mismatched type")

		out1, err := mapFrom[any]("correct input", m)
		assert.NoError(t, err)
		assert.Equal(t, "correct input", out1)
	})

	t.Run("field mapped to whole", func(t *testing.T) {
		type up struct {
			F1 string
			f2 string
			F3 int
		}

		m := []*Mapping{NewMapping("1").From("F1")}

		out, err := mapFrom[string](&up{F1: "field1"}, m)
		assert.NoError(t, err)
		assert.Equal(t, "field1", out)

		out, err = mapFrom[string](&up{F1: ""}, m)
		assert.NoError(t, err)
		assert.Equal(t, "", out)

		out, err = mapFrom[string](up{F1: "field1"}, m)
		assert.NoError(t, err)
		assert.Equal(t, "field1", out)

		m[0].from = "f2"
		_, err = mapFrom[string](&up{f2: "f2"}, m)
		assert.ErrorContains(t, err, "not exported")

		m[0].from = "field3"
		_, err = mapFrom[string](&up{F3: 3}, m)
		assert.ErrorContains(t, err, "from not found")

		m[0].from = "F3"
		_, err = mapFrom[string](&up{F3: 3}, m)
		assert.ErrorContains(t, err, "mismatched type")

		m = []*Mapping{NewMapping("1").From("F1")}
		out1, err := mapFrom[any](&up{F1: "field1"}, m)
		assert.NoError(t, err)
		assert.Equal(t, "field1", out1)
	})

	t.Run("whole mapped to field", func(t *testing.T) {
		type down struct {
			F1 string
			f3 string
		}

		m := []*Mapping{NewMapping("1").To("F1")}

		out, err := mapFrom[down]("from", m)
		assert.NoError(t, err)
		assert.Equal(t, down{F1: "from"}, out)

		out, err = mapFrom[down](1, m)
		assert.ErrorContains(t, err, "mismatched type")

		m[0].to = "f2"
		_, err = mapFrom[down]("from", m)
		assert.ErrorContains(t, err, "to not found")

		m[0].to = "f3"
		_, err = mapFrom[down]("from", m)
		assert.ErrorContains(t, err, "not exported")

		m = []*Mapping{NewMapping("1").To("F1")}
		out1, err := mapFrom[*down]("from", m)
		assert.NoError(t, err)
		assert.Equal(t, &down{F1: "from"}, out1)
	})

	t.Run("field mapped to field", func(t *testing.T) {
		type inner struct {
			in string
		}

		type up struct {
			F1 *inner
		}

		type down struct {
			F1 *inner
		}

		m := []*Mapping{NewMapping("1").From("F1").To("F1")}

		out, err := mapFrom[*down](&up{F1: &inner{in: "in"}}, m)
		assert.NoError(t, err)
		assert.Equal(t, &down{F1: &inner{in: "in"}}, out)
	})

	t.Run("multiple mappings", func(t *testing.T) {
		type up struct {
			F1 string
			F2 int
		}

		type down struct {
			F1 string
			F2 int
		}

		m := []*Mapping{
			NewMapping("1").From("F1").To("F1"),
			NewMapping("1").From("F2").To("F2"),
		}

		out, err := mapFrom[*down](&up{F1: "v1", F2: 2}, m)
		assert.NoError(t, err)
		assert.Equal(t, &down{F1: "v1", F2: 2}, out)

		m[0].fromNodeKey = "1"
		m[0].to = ""
		out, err = mapFrom[*down](&up{F1: "v1", F2: 2}, m)
		assert.ErrorContains(t, err, "mapping entire value has a mismatched type")
	})

	t.Run("invalid mapping", func(t *testing.T) {
		m := []*Mapping{NewMapping("1").From("F1")}
		_, err := mapFrom[string](generic.PtrOf("input"), m)
		assert.ErrorContains(t, err, "mapping has from but input is not struct or struct ptr")

		m = []*Mapping{NewMapping("1").To("F1")}
		_, err = mapFrom[string]("input", m)
		assert.ErrorContains(t, err, "mapping has to but output is not a struct")
	})
}
