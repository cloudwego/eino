/*
 * Copyright 2026 CloudWeGo Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     https://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package schema

import (
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStreamReaderWithConvertNilChunks(t *testing.T) {
	chunks := []any{nil, "next", (*int)(nil), nil}
	for _, source := range []string{"array", "pipe", "copy"} {
		t.Run(source, func(t *testing.T) {
			sr := StreamReaderFromArray(chunks)
			if source != "array" {
				var sw *StreamWriter[any]
				sr, sw = Pipe[any](len(chunks))
				for _, chunk := range chunks {
					sw.Send(chunk, nil)
				}
				sw.Close()
			}
			if source == "copy" {
				readers := sr.Copy(2)
				sr = readers[0]
				defer readers[1].Close()
			}

			var received []any
			converted := StreamReaderWithConvert(sr, func(chunk any) (any, error) {
				received = append(received, chunk)
				return chunk, nil
			})
			defer converted.Close()

			for _, want := range chunks {
				got, err := converted.Recv()
				assert.NoError(t, err)
				assert.Equal(t, want, got)
			}
			_, err := converted.Recv()
			assert.ErrorIs(t, err, io.EOF)
			assert.Equal(t, chunks, received)
		})
	}
}

func TestStreamReaderWithConvertNilNonEmptyInterface(t *testing.T) {
	sr := StreamReaderFromArray([]fmt.Stringer{nil})
	converted := StreamReaderWithConvert(sr, func(chunk fmt.Stringer) (string, error) {
		if chunk == nil {
			return "empty", nil
		}
		return chunk.String(), nil
	})
	defer converted.Close()

	got, err := converted.Recv()
	assert.NoError(t, err)
	assert.Equal(t, "empty", got)
	_, err = converted.Recv()
	assert.ErrorIs(t, err, io.EOF)
}

func TestStreamReaderWithConvertNilChunkErrors(t *testing.T) {
	convertErr := errors.New("cannot convert nil")
	for _, err := range []error{ErrNoValue, convertErr} {
		t.Run(err.Error(), func(t *testing.T) {
			sr := StreamReaderFromArray([]any{nil, "next"})
			converted := StreamReaderWithConvert(sr, func(chunk any) (string, error) {
				if chunk == nil {
					return "", err
				}
				return chunk.(string), nil
			})
			defer converted.Close()

			if err != ErrNoValue {
				_, gotErr := converted.Recv()
				assert.ErrorIs(t, gotErr, convertErr)
			}
			got, gotErr := converted.Recv()
			assert.NoError(t, gotErr)
			assert.Equal(t, "next", got)
			_, gotErr = converted.Recv()
			assert.ErrorIs(t, gotErr, io.EOF)
		})
	}
}
