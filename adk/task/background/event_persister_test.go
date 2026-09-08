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

package background

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/schema"
)

type testStreamEvent struct {
	Name string
}

type testStreamEventPersister struct{}

type taskEventRuntimeStub struct {
	ExecutionRuntime
	scope  TaskEventScope
	writer TaskEventWriter
}

func (r taskEventRuntimeStub) NewTaskEventWriter(string) (TaskEventScope, TaskEventWriter) {
	return r.scope, r.writer
}

type taskEventWriterFunc func(
	context.Context,
	*TaskEventPartInput,
) (*AppendTaskEventResult, error)

func (f taskEventWriterFunc) Append(
	ctx context.Context,
	input *TaskEventPartInput,
) (*AppendTaskEventResult, error) {
	return f(ctx, input)
}

func (testStreamEventPersister) Persist(
	ctx context.Context,
	_ TaskEventScope,
	input *TaskEventEnvelope[testStreamEvent, string],
	writer TaskEventWriter,
) error {
	_, err := writer.Append(ctx, &TaskEventPartInput{
		PartID: "header", Data: []byte(input.Event.Name),
	})
	if err != nil {
		return err
	}
	index := 0
	for {
		chunk, recvErr := input.Stream.Recv()
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			return recvErr
		}
		_, appendErr := writer.Append(ctx, &TaskEventPartInput{
			PartID: fmt.Sprintf("chunk-%d", index),
			Data:   []byte(chunk),
		})
		if appendErr != nil {
			return appendErr
		}
		index++
	}
	_, err = writer.Append(ctx, &TaskEventPartInput{
		PartID: "end", Final: true,
	})
	return err
}

func TestPersistTaskEventPassesTypedEventAndStream(t *testing.T) {
	store := NewInMemoryStore(nil)
	started := createAndStart(t, store, "typed-stream-event")
	runtime := newTaskRuntime(
		store,
		store,
		started.Spec.ID,
		started.Attempt,
		started.Version,
		nil,
	)
	persist := func(chunks ...string) (*TaskEventPersistResult, error) {
		return PersistTaskEvent[testStreamEvent, string](
			context.Background(),
			runtime,
			"logical-event",
			&TaskEventEnvelope[testStreamEvent, string]{
				Event:  testStreamEvent{Name: "raw-event"},
				Stream: schema.StreamReaderFromArray(chunks),
			},
			testStreamEventPersister{},
		)
	}

	result, err := persist("one", "two")
	require.NoError(t, err)
	require.Equal(t, TaskEventScope{
		TaskID: started.Spec.ID, Attempt: started.Attempt,
		EventID: "logical-event",
	}, result.Scope)
	require.Len(t, result.Appends, 4)
	for _, part := range result.Appends {
		require.True(t, part.Inserted)
	}

	page, err := store.ListTaskEvents(
		context.Background(),
		&ListTaskEventsRequest{TaskID: started.Spec.ID},
	)
	require.NoError(t, err)
	require.Equal(t, []string{
		"header", "chunk-0", "chunk-1", "end",
	}, taskEventPartIDs(page.Parts))
	require.Equal(t, []string{
		"raw-event", "one", "two", "",
	}, taskEventData(page.Parts))
	require.True(t, page.Parts[3].Final)

	replayed, err := persist("one", "two")
	require.NoError(t, err)
	for _, part := range replayed.Appends {
		require.False(t, part.Inserted)
	}
}

func TestPersistTaskEventTracksWriterResults(t *testing.T) {
	newRuntime := func(t *testing.T, taskID string) ExecutionRuntime {
		t.Helper()
		store := NewInMemoryStore(nil)
		started := createAndStart(t, store, taskID)
		return newTaskRuntime(
			store,
			store,
			started.Spec.ID,
			started.Attempt,
			started.Version,
			nil,
		)
	}
	appendPart := func(
		ctx context.Context,
		writer TaskEventWriter,
		partID string,
		data string,
	) error {
		_, err := writer.Append(ctx, &TaskEventPartInput{
			PartID: partID, Data: []byte(data),
		})
		return err
	}

	t.Run("collects append omitted by persister", func(t *testing.T) {
		result, err := PersistTaskEvent[string, string](
			context.Background(),
			newRuntime(t, "omitted-result"),
			"event",
			&TaskEventEnvelope[string, string]{Event: "value"},
			TaskEventPersisterFunc[string, string](func(
				ctx context.Context,
				_ TaskEventScope,
				_ *TaskEventEnvelope[string, string],
				writer TaskEventWriter,
			) error {
				require.NoError(t, appendPart(ctx, writer, "one", "value"))
				return nil
			}),
		)

		require.NoError(t, err)
		require.NotNil(t, result)
		require.Len(t, result.Appends, 1)
		require.Equal(t, "one", result.Appends[0].Part.PartID)
	})

	t.Run("returns persisted prefix with persister error", func(t *testing.T) {
		wantErr := errors.New("persister failed")
		result, err := PersistTaskEvent[string, string](
			context.Background(),
			newRuntime(t, "persister-error"),
			"event",
			&TaskEventEnvelope[string, string]{Event: "value"},
			TaskEventPersisterFunc[string, string](func(
				ctx context.Context,
				_ TaskEventScope,
				_ *TaskEventEnvelope[string, string],
				writer TaskEventWriter,
			) error {
				require.NoError(t, appendPart(ctx, writer, "one", "value"))
				return wantErr
			}),
		)

		require.ErrorIs(t, err, wantErr)
		require.NotNil(t, result)
		require.Len(t, result.Appends, 1)
		require.Equal(t, "one", result.Appends[0].Part.PartID)
	})

	t.Run("returns persisted prefix with append error", func(t *testing.T) {
		result, err := PersistTaskEvent[string, string](
			context.Background(),
			newRuntime(t, "append-error"),
			"event",
			&TaskEventEnvelope[string, string]{Event: "value"},
			TaskEventPersisterFunc[string, string](func(
				ctx context.Context,
				_ TaskEventScope,
				_ *TaskEventEnvelope[string, string],
				writer TaskEventWriter,
			) error {
				require.NoError(t, appendPart(ctx, writer, "one", "value"))
				return appendPart(ctx, writer, "one", "changed")
			}),
		)

		require.ErrorIs(t, err, ErrTaskEventPartConflict)
		require.NotNil(t, result)
		require.Len(t, result.Appends, 1)
		require.Equal(t, "one", result.Appends[0].Part.PartID)
	})

	t.Run("rejects malformed writer result even when persister ignores it", func(t *testing.T) {
		scope := TaskEventScope{TaskID: "task", Attempt: 1, EventID: "event"}
		runtime := taskEventRuntimeStub{
			scope: scope,
			writer: taskEventWriterFunc(func(
				_ context.Context,
				input *TaskEventPartInput,
			) (*AppendTaskEventResult, error) {
				return &AppendTaskEventResult{
					Part: &TaskEventPart{
						TaskID:  "different-task",
						EventID: scope.EventID,
						PartID:  input.PartID,
						Data:    append([]byte(nil), input.Data...),
						Final:   input.Final,
					},
				}, nil
			}),
		}
		result, err := PersistTaskEvent[string, string](
			context.Background(),
			runtime,
			scope.EventID,
			&TaskEventEnvelope[string, string]{Event: "value"},
			TaskEventPersisterFunc[string, string](func(
				ctx context.Context,
				_ TaskEventScope,
				_ *TaskEventEnvelope[string, string],
				writer TaskEventWriter,
			) error {
				_, _ = writer.Append(ctx, &TaskEventPartInput{
					PartID: "one", Data: []byte("value"),
				})
				return nil
			}),
		)

		require.ErrorContains(t, err, "incomplete append result")
		require.NotNil(t, result)
		require.Empty(t, result.Appends)
	})
}

func TestTaskEventWriterFencesEveryStreamPart(t *testing.T) {
	store := NewInMemoryStore(nil)
	started := createAndStart(t, store, "stream-fence")
	runtime := newTaskRuntime(
		store,
		store,
		started.Spec.ID,
		started.Attempt,
		started.Version,
		nil,
	)
	_, writer := runtime.NewTaskEventWriter("event")
	_, err := writer.Append(context.Background(), &TaskEventPartInput{
		PartID: "chunk-0", Data: []byte("one"),
	})
	require.NoError(t, err)
	_, err = store.Yield(context.Background(), &YieldTaskRequest{
		TaskID: started.Spec.ID, ExpectedVersion: started.Version,
	})
	require.NoError(t, err)

	_, err = writer.Append(context.Background(), &TaskEventPartInput{
		PartID: "chunk-1", Data: []byte("two"), Final: true,
	})
	require.ErrorIs(t, err, ErrLeaseLost)
}

func TestTaskEventStreamErrorCanReplayPersistedPrefix(t *testing.T) {
	store := NewInMemoryStore(nil)
	started := createAndStart(t, store, "stream-retry")
	runtime := newTaskRuntime(
		store,
		store,
		started.Spec.ID,
		started.Attempt,
		started.Version,
		nil,
	)
	stream, streamWriter := schema.Pipe[string](2)
	wantErr := fmt.Errorf("stream interrupted")
	streamWriter.Send("one", nil)
	streamWriter.Send("", wantErr)
	streamWriter.Close()
	_, err := PersistTaskEvent[testStreamEvent, string](
		context.Background(),
		runtime,
		"event",
		&TaskEventEnvelope[testStreamEvent, string]{
			Event: testStreamEvent{Name: "raw-event"}, Stream: stream,
		},
		testStreamEventPersister{},
	)
	require.ErrorIs(t, err, wantErr)
	page, err := store.ListTaskEvents(
		context.Background(),
		&ListTaskEventsRequest{TaskID: started.Spec.ID},
	)
	require.NoError(t, err)
	require.Equal(t, []string{"header", "chunk-0"}, taskEventPartIDs(page.Parts))
	require.False(t, page.Parts[1].Final)

	replayed, err := PersistTaskEvent[testStreamEvent, string](
		context.Background(),
		runtime,
		"event",
		&TaskEventEnvelope[testStreamEvent, string]{
			Event:  testStreamEvent{Name: "raw-event"},
			Stream: schema.StreamReaderFromArray([]string{"one", "two"}),
		},
		testStreamEventPersister{},
	)
	require.NoError(t, err)
	require.Len(t, replayed.Appends, 4)
	require.False(t, replayed.Appends[0].Inserted)
	require.False(t, replayed.Appends[1].Inserted)
	require.True(t, replayed.Appends[2].Inserted)
	require.True(t, replayed.Appends[3].Inserted)
}

func TestTaskEventFinalPartClosesLogicalEvent(t *testing.T) {
	store := NewInMemoryStore(nil)
	started := createAndStart(t, store, "stream-final")
	appendPart := func(partID, data string, final bool) error {
		_, err := store.AppendTaskEvent(
			context.Background(),
			&AppendTaskEventRequest{
				TaskID: started.Spec.ID, Attempt: started.Attempt,
				EventID: "event", PartID: partID,
				Data: []byte(data), Final: final,
			},
		)
		return err
	}
	require.NoError(t, appendPart("chunk-0", "one", false))
	require.NoError(t, appendPart("end", "done", true))
	require.NoError(t, appendPart("chunk-0", "one", false))
	require.ErrorIs(t, appendPart("chunk-0", "changed", false), ErrTaskEventPartConflict)
	require.ErrorIs(t, appendPart("chunk-0", "one", true), ErrTaskEventPartConflict)
	require.ErrorIs(t, appendPart("late", "late", false), ErrTaskEventClosed)
}

func TestAttack_ConcurrentFinalPartClosesEventOnce(t *testing.T) {
	store := NewInMemoryStore(nil)
	started := createAndStart(t, store, "concurrent-final")
	start := make(chan struct{})
	errs := make(chan error, 2)
	var group sync.WaitGroup
	for _, partID := range []string{"end-a", "end-b"} {
		partID := partID
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			_, err := store.AppendTaskEvent(
				context.Background(),
				&AppendTaskEventRequest{
					TaskID: started.Spec.ID, Attempt: started.Attempt,
					EventID: "event", PartID: partID,
					Data: []byte(partID), Final: true,
				},
			)
			errs <- err
		}()
	}
	close(start)
	group.Wait()
	close(errs)

	var succeeded, closed int
	for err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrTaskEventClosed):
			closed++
		default:
			require.NoError(t, err)
		}
	}
	require.Equal(t, 1, succeeded)
	require.Equal(t, 1, closed)
	page, err := store.ListTaskEvents(
		context.Background(),
		&ListTaskEventsRequest{TaskID: started.Spec.ID},
	)
	require.NoError(t, err)
	require.Len(t, page.Parts, 1)
	require.True(t, page.Parts[0].Final)
}

func taskEventPartIDs(parts []*TaskEventPart) []string {
	result := make([]string, len(parts))
	for index, part := range parts {
		result[index] = part.PartID
	}
	return result
}

func taskEventData(parts []*TaskEventPart) []string {
	result := make([]string, len(parts))
	for index, part := range parts {
		result[index] = string(part.Data)
	}
	return result
}
