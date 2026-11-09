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

package adk

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestStreamConcatErrorsReachRecovery(t *testing.T) {
	t.Run("message", func(t *testing.T) {
		testStreamConcatErrors(t, []*schema.Message{
			schema.AssistantMessage("prefix", nil), schema.UserMessage("incompatible role"),
		}, schema.AssistantMessage("recovered", nil), schema.ConcatMessages)
	})
	t.Run("agentic", func(t *testing.T) {
		testStreamConcatErrors(t, []*schema.AgenticMessage{
			{Role: schema.AgenticRoleTypeAssistant}, {Role: schema.AgenticRoleTypeUser},
		}, &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant,
			ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.AssistantGenText{Text: "recovered"})},
		}, schema.ConcatAgenticMessages)
	})
}

func testStreamConcatErrors[M MessageType](t *testing.T, broken []M, healthy M, concat func([]M) (M, error)) {
	t.Helper()
	_, concatErr := concat(broken)
	require.Error(t, concatErr, "the fixture must fail the actual schema concatenator")
	t.Run("consume", func(t *testing.T) {
		_, err := typedConsumeStream(schema.StreamReaderFromArray(broken))
		require.EqualError(t, err, concatErr.Error())
	})
	t.Run("read_error_keeps_precedence", func(t *testing.T) {
		readErr := errors.New("provider stream failed")
		sr, sw := schema.Pipe[M](len(broken) + 1)
		for _, chunk := range broken {
			sw.Send(chunk, nil)
		}
		var zero M
		sw.Send(zero, readErr)
		sw.Close()
		_, err := typedConsumeStream(sr)
		require.ErrorIs(t, err, readErr)
	})
	t.Run("empty_is_still_accepted", func(t *testing.T) {
		want, wantErr := concat(nil)
		got, err := typedConsumeStream(schema.StreamReaderFromArray([]M{}))
		require.Equal(t, wantErr, err)
		require.Equal(t, want, got)
	})
	t.Run("healthy_is_accepted", func(t *testing.T) {
		got, err := typedConsumeStream(schema.StreamReaderFromArray([]M{healthy}))
		require.NoError(t, err)
		require.Equal(t, healthy, got)
	})
	t.Run("retry", func(t *testing.T) {
		inner := &concatErrorModel[M]{first: broken, healthy: healthy}
		rejected := 0
		wrapper := newTypedRetryModelWrapper[M](inner, &TypedModelRetryConfig[M]{
			MaxRetries: 1,
			BackoffFunc: func(context.Context, int) time.Duration {
				return 0
			},
			ShouldRetry: func(_ context.Context, input *TypedRetryContext[M]) *TypedRetryDecision[M] {
				if input.Err != nil {
					rejected++
					require.EqualError(t, input.Err, concatErr.Error())
				}
				return &TypedRetryDecision[M]{Retry: input.Err != nil}
			},
		})
		sr, err := wrapper.Stream(context.Background(), nil)
		t.Logf("model calls=%d, concat-error retry decisions=%d", inner.calls, rejected)
		require.NoError(t, err)
		got, err := concatReturnedStream(sr, concat)
		require.NoError(t, err)
		require.Equal(t, healthy, got)
		require.Equal(t, 2, inner.calls)
		require.Equal(t, 1, rejected)
	})
	t.Run("retry_panic_does_not_strand_failover_event", func(t *testing.T) {
		iter, generator := NewAsyncIteratorPair[*TypedAgentEvent[M]]()
		defer generator.Close()
		ctx := withTypedChatModelAgentExecCtx(context.Background(), &typedChatModelAgentExecCtx[M]{
			generator:                generator,
			failoverLastSuccessModel: &concatErrorModel[M]{first: []M{healthy}, healthy: healthy},
		})
		config := &ModelFailoverConfig[M]{MaxRetries: 1,
			GetFailoverModel: func(context.Context, *FailoverContext[M]) (model.BaseModel[M], []M, error) {
				return nil, nil, errors.New("unexpected failover")
			},
		}
		retryConfig := &TypedModelRetryConfig[M]{MaxRetries: 1,
			ShouldRetry: func(context.Context, *TypedRetryContext[M]) *TypedRetryDecision[M] { panic("policy panic") },
		}
		inner := &typedEventSenderModel[M]{inner: &typedFailoverProxyModel[M]{}, modelFailoverConfig: config, modelRetryConfig: retryConfig}
		retry := newTypedRetryModelWrapper[M](inner, retryConfig)
		wrapper := newFailoverModelWrapper[M](retry, config)
		require.PanicsWithValue(t, "policy panic", func() { _, _ = wrapper.Stream(ctx, nil) })
		event, ok := iter.Next()
		require.True(t, ok)
		finished := make(chan error, 1)
		go func() {
			_, err := concatReturnedStream(event.Output.MessageOutput.MessageStream, concat)
			finished <- err
		}()
		select {
		case err := <-finished:
			require.Error(t, err)
		case <-time.After(time.Second):
			t.Fatal("event EOF remained blocked after policy panic")
		}
	})
	for _, cached := range []bool{true, false} {
		for _, readError := range []bool{false, true} {
			t.Run(fmt.Sprintf("retry_declines_failover_accepts/cached=%v/read_error=%v", cached, readError), func(t *testing.T) {
				primary := &concatErrorModel[M]{first: broken, healthy: healthy}
				backup := &concatErrorModel[M]{first: []M{healthy}, healthy: healthy}
				wantErr := concatErr
				if readError {
					wantErr = errors.New("provider read failed")
					primary.terminal = wantErr
				}
				iter, generator := NewAsyncIteratorPair[*TypedAgentEvent[M]]()
				defer generator.Close()
				execCtx := &typedChatModelAgentExecCtx[M]{generator: generator}
				if cached {
					execCtx.failoverLastSuccessModel = primary
				}
				ctx := withTypedChatModelAgentExecCtx(context.Background(), execCtx)
				config := &ModelFailoverConfig[M]{MaxRetries: 2,
					ShouldFailover: func(_ context.Context, _ M, err error) bool {
						require.EqualError(t, err, wantErr.Error())
						return true
					},
					GetFailoverModel: func(_ context.Context, input *FailoverContext[M]) (model.BaseModel[M], []M, error) {
						if !cached && input.FailoverAttempt == 1 {
							return primary, nil, nil
						}
						return backup, nil, nil
					},
				}
				retryConfig := &TypedModelRetryConfig[M]{ShouldRetry: func(context.Context, *TypedRetryContext[M]) *TypedRetryDecision[M] { return &TypedRetryDecision[M]{} }}
				inner := &typedEventSenderModel[M]{inner: &typedFailoverProxyModel[M]{}, modelFailoverConfig: config, modelRetryConfig: retryConfig}
				retry := newTypedRetryModelWrapper[M](inner, retryConfig)
				sr, err := newFailoverModelWrapper[M](retry, config).Stream(ctx, nil)
				require.NoError(t, err)
				got, err := concatReturnedStream(sr, concat)
				require.NoError(t, err)
				require.Equal(t, healthy, got)
				require.Equal(t, 1, primary.calls)
				require.Equal(t, 1, backup.calls)
				event, ok := iter.Next()
				require.True(t, ok)
				_, eventErr := concatReturnedStream(event.Output.MessageOutput.MessageStream, concat)
				var willRetry *WillRetryError
				require.ErrorAs(t, eventErr, &willRetry, "outer failover approved recovery despite inner retry declining")
				event, ok = iter.Next()
				require.True(t, ok)
				got, eventErr = concatReturnedStream(event.Output.MessageOutput.MessageStream, concat)
				require.NoError(t, eventErr)
				require.Equal(t, healthy, got)
			})
		}
	}
	for _, cached := range []bool{true, false} {
		for _, readError := range []bool{false, true} {
			t.Run(fmt.Sprintf("declined/cached=%v/read_error=%v", cached, readError), func(t *testing.T) {
				primary := &concatErrorModel[M]{first: broken, healthy: healthy}
				wantErr := concatErr
				if readError {
					wantErr = errors.New("provider read failed")
					primary.terminal = wantErr
				}
				iter, generator := NewAsyncIteratorPair[*TypedAgentEvent[M]]()
				defer generator.Close()
				execCtx := &typedChatModelAgentExecCtx[M]{generator: generator}
				if cached {
					execCtx.failoverLastSuccessModel = primary
				}
				ctx := withTypedChatModelAgentExecCtx(context.Background(), execCtx)
				config := &ModelFailoverConfig[M]{MaxRetries: 2,
					ShouldFailover: func(_ context.Context, _ M, err error) bool {
						require.EqualError(t, err, wantErr.Error())
						return false
					},
					GetFailoverModel: func(_ context.Context, input *FailoverContext[M]) (model.BaseModel[M], []M, error) {
						require.False(t, cached)
						require.Equal(t, uint(1), input.FailoverAttempt)
						return primary, nil, nil
					},
				}
				inner := &typedEventSenderModel[M]{inner: &typedFailoverProxyModel[M]{}, modelFailoverConfig: config}
				_, err := newFailoverModelWrapper[M](inner, config).Stream(ctx, nil)
				require.EqualError(t, err, wantErr.Error())
				require.Equal(t, 1, primary.calls)
				event, ok := iter.Next()
				require.True(t, ok)
				_, eventErr := concatReturnedStream(event.Output.MessageOutput.MessageStream, concat)
				require.EqualError(t, eventErr, wantErr.Error())
				var willRetry *WillRetryError
				require.False(t, errors.As(eventErr, &willRetry), "declined failure must remain terminal to history consumers")
			})
		}
	}
	for _, cached := range []bool{true, false} {
		for _, backupFails := range []bool{false, true} {
			t.Run(fmt.Sprintf("failover/cached=%v/backup_fails=%v", cached, backupFails), func(t *testing.T) {
				primary := &concatErrorModel[M]{first: broken, healthy: healthy}
				backupChunks := []M{healthy}
				if backupFails {
					backupChunks = broken
				}
				backup := &concatErrorModel[M]{first: backupChunks, healthy: healthy}
				iter, generator := NewAsyncIteratorPair[*TypedAgentEvent[M]]()
				defer generator.Close()
				execCtx := &typedChatModelAgentExecCtx[M]{generator: generator}
				attempts := uint(2)
				if cached {
					execCtx.failoverLastSuccessModel = primary
					attempts = 1
				}
				ctx := withTypedChatModelAgentExecCtx(context.Background(), execCtx)
				config := &ModelFailoverConfig[M]{
					MaxRetries: attempts,
					ShouldFailover: func(_ context.Context, _ M, err error) bool {
						require.EqualError(t, err, concatErr.Error())
						return true
					},
					GetFailoverModel: func(_ context.Context, input *FailoverContext[M]) (model.BaseModel[M], []M, error) {
						if !cached && input.FailoverAttempt == 1 {
							return primary, nil, nil
						}
						require.EqualError(t, input.LastErr, concatErr.Error())
						return backup, nil, nil
					},
				}
				inner := &typedEventSenderModel[M]{inner: &typedFailoverProxyModel[M]{}, modelFailoverConfig: config}
				wrapper := newFailoverModelWrapper[M](inner, config)
				sr, err := wrapper.Stream(ctx, nil)
				if backupFails {
					require.EqualError(t, err, concatErr.Error())
				} else {
					require.NoError(t, err)
					got, readErr := concatReturnedStream(sr, concat)
					require.NoError(t, readErr)
					require.Equal(t, healthy, got)
					require.Same(t, backup, typedGetFailoverLastSuccessModel[M](ctx))
				}
				require.Equal(t, 1, primary.calls)
				require.Equal(t, 1, backup.calls)
				event, ok := iter.Next()
				require.True(t, ok)
				_, eventErr := concatReturnedStream(event.Output.MessageOutput.MessageStream, concat)
				var willRetry *WillRetryError
				require.ErrorAs(t, eventErr, &willRetry, "failed event must be skippable by flow history")
				event, ok = iter.Next()
				require.True(t, ok)
				got, eventErr := concatReturnedStream(event.Output.MessageOutput.MessageStream, concat)
				if backupFails {
					require.EqualError(t, eventErr, concatErr.Error())
					require.False(t, errors.As(eventErr, &willRetry), "exhausted error must remain terminal")
				} else {
					require.NoError(t, eventErr)
					require.Equal(t, healthy, got)
				}
			})
		}
	}

}

type concatErrorModel[M MessageType] struct {
	first    []M
	healthy  M
	calls    int
	terminal error
}

func (m *concatErrorModel[M]) Generate(context.Context, []M, ...model.Option) (M, error) {
	var zero M
	return zero, errors.New("unused Generate")
}

func (m *concatErrorModel[M]) Stream(context.Context, []M, ...model.Option) (*schema.StreamReader[M], error) {
	m.calls++
	if m.terminal != nil {
		sr, sw := schema.Pipe[M](len(m.first) + 1)
		for _, chunk := range m.first {
			sw.Send(chunk, nil)
		}
		var zero M
		sw.Send(zero, m.terminal)
		sw.Close()
		return sr, nil
	}
	if m.calls == 1 {
		return schema.StreamReaderFromArray(m.first), nil
	}
	return schema.StreamReaderFromArray([]M{m.healthy}), nil
}

func concatReturnedStream[M MessageType](sr *schema.StreamReader[M], concat func([]M) (M, error)) (M, error) {
	defer sr.Close()
	var chunks []M
	for {
		chunk, err := sr.Recv()
		if err == io.EOF {
			return concat(chunks)
		}
		if err != nil {
			var zero M
			return zero, err
		}
		chunks = append(chunks, chunk)
	}
}
