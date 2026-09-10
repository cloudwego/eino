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

package core

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExecutionGateContext(t *testing.T) {
	require.NoError(t, WaitExecutionGate(context.Background()))
	require.NoError(t, WaitExecutionGate(nil))

	want := errors.New("blocked")
	ctx := WithExecutionGate(context.Background(), func(context.Context) error {
		return want
	})
	require.ErrorIs(t, WaitExecutionGate(ctx), want)
	require.Same(t, ctx, WithExecutionGate(ctx, nil))
	require.Nil(t, WithExecutionGate(nil, func(context.Context) error { return nil }))
}
