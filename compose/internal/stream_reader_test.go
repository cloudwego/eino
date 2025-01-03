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

package internal

import (
	"io"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/schema"
)

func TestArrayStreamMerge(t *testing.T) {

	t.Run("unpack_to_equal_type", func(t *testing.T) {
		a1 := []int{1, 2, 3}
		a2 := []int{4, 5, 6}
		a3 := []int{7, 8, 9}
		s1 := schema.StreamReaderFromArray(a1)
		s2 := schema.StreamReaderFromArray(a2)
		s3 := schema.StreamReaderFromArray(a3)

		sp1 := streamReaderPacker[int]{sr: s1}
		sp2 := streamReaderPacker[int]{sr: s2}
		sp3 := streamReaderPacker[int]{sr: s3}

		sp := sp1.merge([]streamReader{sp2, sp3})

		sr, ok := unpackStreamReader[int](sp)
		if !ok {
			t.Fatal("unexpected")
		}

		defer sr.Close()

		var result []int
		for {
			chunk, err := sr.Recv()
			if err == io.EOF {
				break
			}
			assert.Nil(t, err)
			result = append(result, chunk)
		}
		if !reflect.DeepEqual(result, append(append(a1, a2...), a3...)) {
			t.Fatalf("result: %v error", result)
		}
	})

	t.Run("unpack_to_father_type", func(t *testing.T) {
		a1 := []*doctor{{say: "a"}, {say: "b"}, {say: "c"}}
		a2 := []*doctor{{say: "d"}, {say: "e"}, {say: "f"}}
		a3 := []*doctor{{say: "g"}, {say: "h"}, {say: "i"}}
		s1 := schema.StreamReaderFromArray(a1)
		s2 := schema.StreamReaderFromArray(a2)
		s3 := schema.StreamReaderFromArray(a3)

		sp1 := streamReaderPacker[*doctor]{sr: s1}
		sp2 := streamReaderPacker[*doctor]{sr: s2}
		sp3 := streamReaderPacker[*doctor]{sr: s3}

		sp := sp1.merge([]streamReader{sp2, sp3})

		sr, ok := unpackStreamReader[person](sp)
		assert.True(t, ok)

		defer sr.Close()

		var result []person
		for {
			chunk, err := sr.Recv()
			if err == io.EOF {
				break
			}
			assert.Nil(t, err)
			result = append(result, chunk)
		}

		baseline := append(append(a1, a2...), a3...)

		assert.Len(t, result, len(baseline))

		for idx := range result {
			assert.Equal(t, baseline[idx].say, result[idx].Say())
		}
	})
}

type doctor struct {
	say string
}

func (d *doctor) Say() string {
	return d.say
}

type person interface {
	Say() string
}
