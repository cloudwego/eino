/*
 * Copyright 2025 CloudWeGo Authors
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
	"fmt"
	"reflect"

	"github.com/cloudwego/eino/internal/generic"
	"github.com/cloudwego/eino/schema"
)

func newGenericHelper[I, O any]() *genericHelper {
	return &genericHelper{
		inputStreamFilter:  defaultStreamMapFilter[I],
		outputStreamFilter: defaultStreamMapFilter[O],
		inputConverter: handlerPair{
			invoke:    defaultValueChecker[I],
			transform: defaultStreamConverter[I],
		},
		outputConverter: handlerPair{
			invoke:    defaultValueChecker[O],
			transform: defaultStreamConverter[O],
		},
		inputFieldMappingConverter: handlerPair{
			invoke:    buildFieldMappingConverter[I](),
			transform: buildStreamFieldMappingConverter[I](),
		},
		outputFieldMappingConverter: handlerPair{
			invoke:    buildFieldMappingConverter[O](),
			transform: buildStreamFieldMappingConverter[O](),
		},
		inputStreamConvertPair:  defaultStreamConvertPair[I](),
		outputStreamConvertPair: defaultStreamConvertPair[O](),
		inputZeroValue:          zeroValueFromGeneric[I],
		outputZeroValue:         zeroValueFromGeneric[O],
		inputEmptyStream:        emptyStreamFromGeneric[I],
		outputEmptyStream:       emptyStreamFromGeneric[O],
	}
}

type genericHelper struct {
	// when set input key, use this method to convert input from map[string]any to T
	inputStreamFilter, outputStreamFilter streamMapFilter
	// when predecessor's output is assignableTypeMay to current node's input, validate and convert(if needed) types using the following two methods
	inputConverter, outputConverter handlerPair
	// when current node enable field mapping, convert map input to expected struct using the following two methods
	inputFieldMappingConverter, outputFieldMappingConverter handlerPair
	// can convert input/output from stream to non-stream or non-stream to stream, used for checkpoint
	inputStreamConvertPair, outputStreamConvertPair streamConvertPair

	inputZeroValue, outputZeroValue     func() any
	inputEmptyStream, outputEmptyStream func() streamReader
}

func (g *genericHelper) forMapInput() *genericHelper {
	return &genericHelper{
		outputStreamFilter:          g.outputStreamFilter,
		outputConverter:             g.outputConverter,
		outputFieldMappingConverter: g.outputFieldMappingConverter,
		outputStreamConvertPair:     g.outputStreamConvertPair,
		outputZeroValue:             g.outputZeroValue,
		outputEmptyStream:           g.outputEmptyStream,

		inputStreamFilter: defaultStreamMapFilter[map[string]any],
		inputConverter: handlerPair{
			invoke:    defaultValueChecker[map[string]any],
			transform: defaultStreamConverter[map[string]any],
		},
		inputFieldMappingConverter: handlerPair{
			invoke:    buildFieldMappingConverter[map[string]any](),
			transform: buildStreamFieldMappingConverter[map[string]any](),
		},
		inputStreamConvertPair: defaultStreamConvertPair[map[string]any](),
		inputZeroValue:         zeroValueFromGeneric[map[string]any],
		inputEmptyStream:       emptyStreamFromGeneric[map[string]any],
	}
}

func (g *genericHelper) forMapOutput() *genericHelper {
	return &genericHelper{
		inputStreamFilter:          g.inputStreamFilter,
		inputConverter:             g.inputConverter,
		inputFieldMappingConverter: g.inputFieldMappingConverter,
		inputStreamConvertPair:     g.inputStreamConvertPair,
		inputZeroValue:             g.inputZeroValue,
		inputEmptyStream:           g.inputEmptyStream,

		outputStreamFilter: defaultStreamMapFilter[map[string]any],
		outputConverter: handlerPair{
			invoke:    defaultValueChecker[map[string]any],
			transform: defaultStreamConverter[map[string]any],
		},
		outputFieldMappingConverter: handlerPair{
			invoke:    buildFieldMappingConverter[map[string]any](),
			transform: buildStreamFieldMappingConverter[map[string]any](),
		},
		outputStreamConvertPair: defaultStreamConvertPair[map[string]any](),
		outputZeroValue:         zeroValueFromGeneric[map[string]any],
		outputEmptyStream:       emptyStreamFromGeneric[map[string]any],
	}
}

func (g *genericHelper) forPredecessorPassthrough() *genericHelper {
	return &genericHelper{
		inputStreamFilter:           g.inputStreamFilter,
		outputStreamFilter:          g.inputStreamFilter,
		inputConverter:              g.inputConverter,
		outputConverter:             g.inputConverter,
		inputFieldMappingConverter:  g.inputFieldMappingConverter,
		outputFieldMappingConverter: g.inputFieldMappingConverter,
		inputStreamConvertPair:      g.inputStreamConvertPair,
		outputStreamConvertPair:     g.inputStreamConvertPair,
		inputZeroValue:              g.inputZeroValue,
		outputZeroValue:             g.inputZeroValue,
		inputEmptyStream:            g.inputEmptyStream,
		outputEmptyStream:           g.inputEmptyStream,
	}
}

func (g *genericHelper) forSuccessorPassthrough() *genericHelper {
	return &genericHelper{
		inputStreamFilter:           g.outputStreamFilter,
		outputStreamFilter:          g.outputStreamFilter,
		inputConverter:              g.outputConverter,
		outputConverter:             g.outputConverter,
		inputFieldMappingConverter:  g.outputFieldMappingConverter,
		outputFieldMappingConverter: g.outputFieldMappingConverter,
		inputStreamConvertPair:      g.outputStreamConvertPair,
		outputStreamConvertPair:     g.outputStreamConvertPair,
		inputZeroValue:              g.outputZeroValue,
		outputZeroValue:             g.outputZeroValue,
		inputEmptyStream:            g.outputEmptyStream,
		outputEmptyStream:           g.outputEmptyStream,
	}
}

type streamMapFilter func(key string, isr streamReader) (streamReader, bool)

type valueHandler func(value any) (any, error)
type streamHandler func(streamReader) streamReader

type handlerPair struct {
	invoke    valueHandler
	transform streamHandler
}

type streamConvertPair struct {
	concatStream  func(sr streamReader) (any, error)
	restoreStream func(any) (streamReader, error)
}

func defaultStreamConvertPair[T any]() streamConvertPair {
	var t T
	return streamConvertPair{
		concatStream: func(sr streamReader) (any, error) {
			tsr, ok := unpackStreamReader[T](sr)
			if !ok {
				return nil, fmt.Errorf("cannot convert sr to streamReader[%T]", t)
			}
			value, err := concatStreamReader(tsr)
			if err != nil {
				if errors.Is(err, emptyStreamConcatErr) {
					return nil, nil
				}
				return nil, err
			}
			return value, nil
		},
		restoreStream: func(a any) (streamReader, error) {
			if a == nil {
				return packStreamReader(schema.StreamReaderFromArray([]T{})), nil
			}
			value, ok := a.(T)
			if !ok {
				return nil, fmt.Errorf("cannot convert value[%T] to streamReader[%T]", a, t)
			}
			return packStreamReader(schema.StreamReaderFromArray([]T{value})), nil
		},
	}
}

func defaultStreamMapFilter[T any](key string, isr streamReader) (streamReader, bool) {
	sr, ok := unpackStreamReader[map[string]any](isr)
	if !ok {
		return nil, false
	}

	cvt := func(m map[string]any) (T, error) {
		var t T
		v, ok_ := m[key]
		if !ok_ {
			return t, schema.ErrNoValue
		}
		vv, ok_ := v.(T)
		if !ok_ {
			return t, fmt.Errorf(
				"[defaultStreamMapFilter]fail, key[%s]'s value type[%s] isn't expected type[%s]",
				key, reflect.TypeOf(v).String(),
				generic.TypeOf[T]().String())
		}
		return vv, nil
	}

	ret := schema.StreamReaderWithConvert[map[string]any, T](sr, cvt)

	return packStreamReader(ret), true
}

func defaultStreamConverter[T any](reader streamReader) streamReader {
	return packStreamReader(schema.StreamReaderWithConvert(reader.toAnyStreamReader(), func(v any) (T, error) {
		vv, ok := v.(T)
		if !ok {
			var t T
			return t, fmt.Errorf("runtime type check fail, expected type: %T, actual type: %T", t, v)
		}
		return vv, nil
	}))
}

func defaultValueChecker[T any](v any) (any, error) {
	nValue, ok := v.(T)
	if !ok {
		var t T
		return nil, fmt.Errorf("runtime type check fail, expected type: %T, actual type: %T", t, v)
	}
	return nValue, nil
}

func zeroValueFromGeneric[T any]() any {
	var t T
	return t
}

func emptyStreamFromGeneric[T any]() streamReader {
	var t T
	sr, sw := schema.Pipe[T](1)
	sw.Send(t, nil)
	sw.Close()
	return packStreamReader(sr)
}
