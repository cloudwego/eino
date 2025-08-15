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
	"errors"
	"io"

	"github.com/cloudwego/eino/internal"
	"github.com/cloudwego/eino/schema"
)

// RegisterStreamChunkConcatFunc registers a function to concat stream chunks.
// It's required when you want to concat stream chunks of a specific type.
// for example you call Invoke() but node only implements Stream().
// call at process init
// not thread safe
// eg.
//
//	type testStruct struct {
//		field1 string
//		field2 int
//	}
//	compose.RegisterStreamChunkConcatFunc(func(items []testStruct) (testStruct, error) {
//		return testStruct{
//			field1: items[1].field1, // may implement inplace logic by your scenario
//			field2: items[0].field2 + items[1].field2,
//		}, nil
//	})
func RegisterStreamChunkConcatFunc[T any](fn func([]T) (T, error)) {
	internal.RegisterStreamChunkConcatFunc(fn)
}

var emptyStreamConcatErr = errors.New("stream reader is empty, concat fail")

func concatStreamReader[T any](sr *schema.StreamReader[T]) (T, error) {
	defer sr.Close()

	var items []T

	for {
		chunk, err := sr.Recv()
		if err != nil {
			if err == io.EOF {
				break
			}

			if _, ok := schema.GetSourceName(err); ok {
				continue
			}

			var t T
			return t, newStreamReadError(err)
		}

		items = append(items, chunk)
	}

	if len(items) == 0 {
		var t T
		return t, emptyStreamConcatErr
	}

	if len(items) == 1 {
		return items[0], nil
	}

	res, err := internal.ConcatItems(items)
	if err != nil {
		var t T
		return t, err
	}
	return res, nil
}
