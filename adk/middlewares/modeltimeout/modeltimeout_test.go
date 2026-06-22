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

package modeltimeout

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type blockingChatModel struct{}

func (m *blockingChatModel) Generate(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (m *blockingChatModel) Stream(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestMiddlewareWrapModel(t *testing.T) {
	mw := New(&Config{CallTimeout: 10 * time.Millisecond})
	wrapped, err := mw.WrapModel(context.Background(), &blockingChatModel{}, nil)
	require.NoError(t, err)

	_, err = wrapped.Generate(context.Background(), []*schema.Message{schema.UserMessage("hi")})
	require.ErrorIs(t, err, ErrModelTimeout)

	timeoutErr, ok := AsModelTimeout(err)
	require.True(t, ok)
	require.Equal(t, PhaseCall, timeoutErr.Phase)
}

func TestInactiveMiddlewareDelegates(t *testing.T) {
	m := &blockingChatModel{}

	mw := New(&Config{})
	wrapped, err := mw.WrapModel(context.Background(), m, nil)
	require.NoError(t, err)
	require.Same(t, m, wrapped)
}
