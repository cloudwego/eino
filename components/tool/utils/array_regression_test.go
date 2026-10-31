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

package utils

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
)

func TestToolsWithArrayInput(t *testing.T) {
	ctx := context.Background()
	info := &schema.ToolInfo{Name: "sum"}
	t.Run("invoke", func(t *testing.T) {
		fn := NewTool(info, func(_ context.Context, input [2]int) (int, error) {
			return input[0] + input[1], nil
		})
		require.NotPanics(t, func() {
			result, err := fn.InvokableRun(ctx, "[2,3]")
			require.NoError(t, err)
			require.Equal(t, "5", result)
		})
	})
	t.Run("stream", func(t *testing.T) {
		fn := NewStreamTool(info, func(_ context.Context, input [2]int) (*schema.StreamReader[int], error) {
			return schema.StreamReaderFromArray([]int{input[0] + input[1]}), nil
		})
		require.NotPanics(t, func() {
			result, err := fn.StreamableRun(ctx, "[2,3]")
			require.NoError(t, err)
			defer result.Close()
			value, err := result.Recv()
			require.NoError(t, err)
			require.Equal(t, "5", value)
		})
	})
}
