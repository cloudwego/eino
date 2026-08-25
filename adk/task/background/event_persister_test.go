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

func (testStreamEventPersister) Persist(
	ctx context.Context,
	_ TaskEventScope,
	input *TaskEventEnvelope[testStreamEvent, string],
	writer TaskEventWriter,
) ([]*AppendTaskEventResult, error) {
	var results []*AppendTaskEventResult
	header, err := writer.Append(ctx, &TaskEventPart{
		PartID: "header", Data: []byte(input.Event.Name),
	})
	if err != nil {
		return nil, err
	}
	results = append(results, header)
	index := 0
	for {
		chunk, recvErr := input.Stream.Recv()
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			return nil, recvErr
		}
		result, appendErr := writer.Append(ctx, &TaskEventPart{
			PartID: fmt.Sprintf("chunk-%d", index),
			Data:   []byte(chunk),
		})
		if appendErr != nil {
			return nil, appendErr
		}
		results = append(results, result)
		index++
	}
	final, err := writer.Append(ctx, &TaskEventPart{
		PartID: "end", Final: true,
	})
	if err != nil {
		return nil, err
	}
	return append(results, final), nil
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
	require.Len(t, result.Parts, 4)
	for _, part := range result.Parts {
		require.True(t, part.Inserted)
	}

	page, err := store.ListTaskEvents(
		context.Background(),
		&ListTaskEventsRequest{TaskID: started.Spec.ID},
	)
	require.NoError(t, err)
	require.Equal(t, []string{
		"header", "chunk-0", "chunk-1", "end",
	}, taskEventPartIDs(page.Events))
	require.Equal(t, []string{
		"raw-event", "one", "two", "",
	}, taskEventData(page.Events))
	require.True(t, page.Events[3].Final)

	replayed, err := persist("one", "two")
	require.NoError(t, err)
	for _, part := range replayed.Parts {
		require.False(t, part.Inserted)
	}
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
	_, err := writer.Append(context.Background(), &TaskEventPart{
		PartID: "chunk-0", Data: []byte("one"),
	})
	require.NoError(t, err)
	_, err = store.Yield(context.Background(), &YieldTaskRequest{
		TaskID: started.Spec.ID, ExpectedVersion: started.Version,
	})
	require.NoError(t, err)

	_, err = writer.Append(context.Background(), &TaskEventPart{
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
	require.Equal(t, []string{"header", "chunk-0"}, taskEventPartIDs(page.Events))
	require.False(t, page.Events[1].Final)

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
	require.Len(t, replayed.Parts, 4)
	require.False(t, replayed.Parts[0].Inserted)
	require.False(t, replayed.Parts[1].Inserted)
	require.True(t, replayed.Parts[2].Inserted)
	require.True(t, replayed.Parts[3].Inserted)
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
	require.Len(t, page.Events, 1)
	require.True(t, page.Events[0].Final)
}

func taskEventPartIDs(events []*TaskEvent) []string {
	result := make([]string, len(events))
	for index, event := range events {
		result[index] = event.PartID
	}
	return result
}

func taskEventData(events []*TaskEvent) []string {
	result := make([]string, len(events))
	for index, event := range events {
		result[index] = string(event.Data)
	}
	return result
}
