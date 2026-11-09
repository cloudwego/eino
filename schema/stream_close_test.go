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

package schema_test

import (
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/schema"
)

func TestStreamReaderWithOnClose(t *testing.T) {
	calls := 0
	underlying, writer := schema.Pipe[int](0)
	reader := schema.StreamReaderWithConvert(underlying, func(value int) (int, error) { return value, nil }, schema.WithOnClose(func() { calls++ }))
	copies := reader.Copy(2)
	copies[0].Close()
	require.Zero(t, calls)
	copies[1].Close()
	require.Equal(t, 1, calls)
	require.True(t, writer.Send(1, nil))
	writer.Close()
}

func TestStreamReaderWithOnCloseAfterEOF(t *testing.T) {
	calls := 0
	reader := schema.StreamReaderWithConvert(schema.StreamReaderFromArray([]int{1}), func(value int) (int, error) { return value, nil }, schema.WithOnClose(func() { calls++ }))
	value, err := reader.Recv()
	require.NoError(t, err)
	require.Equal(t, 1, value)
	_, err = reader.Recv()
	require.ErrorIs(t, err, io.EOF)
	require.Zero(t, calls)
	reader.Close()
	reader.Close()
	require.Equal(t, 1, calls)
}
