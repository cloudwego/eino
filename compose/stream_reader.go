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
	"reflect"

	"github.com/cloudwego/eino/internal/generic"
	"github.com/cloudwego/eino/schema"
)

type streamReader interface {
	copy(n int) []streamReader
	getType() reflect.Type
	getChunkType() reflect.Type
	merge([]streamReader) streamReader
	withKey(string) streamReader
	close()
	toAnyStreamReader() *schema.StreamReader[any]
	mergeWithNames([]streamReader, []string) streamReader
}

type streamReaderPacker[T any] struct {
	sr *schema.StreamReader[T]
}

func (srp streamReaderPacker[T]) close() {
	srp.sr.Close()
}

func (srp streamReaderPacker[T]) copy(n int) []streamReader {
	ret := make([]streamReader, n)
	srs := srp.sr.Copy(n)

	for i := 0; i < n; i++ {
		ret[i] = streamReaderPacker[T]{srs[i]}
	}

	return ret
}

func (srp streamReaderPacker[T]) getType() reflect.Type {
	return reflect.TypeOf(srp.sr)
}

func (srp streamReaderPacker[T]) getChunkType() reflect.Type {
	return generic.TypeOf[T]()
}

func (srp streamReaderPacker[T]) toStreamReaders(srs []streamReader) []*schema.StreamReader[T] {
	ret := make([]*schema.StreamReader[T], len(srs)+1)
	ret[0] = srp.sr
	for i := 1; i < len(ret); i++ {
		sr, ok := unpackStreamReader[T](srs[i-1])
		if !ok {
			return nil
		}

		ret[i] = sr
	}

	return ret
}

func (srp streamReaderPacker[T]) merge(isrs []streamReader) streamReader {
	srs := srp.toStreamReaders(isrs)

	sr := schema.MergeStreamReaders(srs)

	return packStreamReader(sr)
}

func (srp streamReaderPacker[T]) mergeWithNames(isrs []streamReader, names []string) streamReader {
	srs := srp.toStreamReaders(isrs)

	sr := schema.InternalMergeNamedStreamReaders(srs, names)

	return packStreamReader(sr)
}

func (srp streamReaderPacker[T]) withKey(key string) streamReader {
	cvt := func(v T) (map[string]any, error) {
		return map[string]any{key: v}, nil
	}

	ret := schema.StreamReaderWithConvert[T, map[string]any](srp.sr, cvt)

	return packStreamReader(ret)
}

func (srp streamReaderPacker[T]) toAnyStreamReader() *schema.StreamReader[any] {
	return schema.StreamReaderWithConvert(srp.sr, func(t T) (any, error) {
		return t, nil
	})
}

func packStreamReader[T any](sr *schema.StreamReader[T]) streamReader {
	return streamReaderPacker[T]{sr}
}

func unpackStreamReader[T any](isr streamReader) (*schema.StreamReader[T], bool) {
	c, ok := isr.(streamReaderPacker[T])
	if ok {
		return c.sr, true
	}

	typ := generic.TypeOf[T]()
	if typ.Kind() == reflect.Interface {
		return schema.StreamReaderWithConvert(isr.toAnyStreamReader(), func(t any) (T, error) {
			return t.(T), nil
		}), true
	}

	return nil, false
}
