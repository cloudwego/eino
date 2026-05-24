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

package session_test

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/session"
	"github.com/cloudwego/eino/schema"
)

func TestFileStoreConformance(t *testing.T) {
	session.RunConformanceTests(t, func(t testing.TB) adk.SessionStore {
		store, err := session.NewFileStore(t.TempDir())
		require.NoError(t, err)
		return store
	})
}

func TestFileStorePersistsAcrossInstances(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := session.NewFileStore(dir)
	require.NoError(t, err)

	first := []byte(`{"event_id":"persist-1","payload":"first"}`)
	second := []byte(`{"event_id":"persist-2","payload":"second"}`)
	require.NoError(t, store.AppendEvents(ctx, "s", [][]byte{first, second}))

	reopened, err := session.NewFileStore(dir)
	require.NoError(t, err)
	res, err := reopened.LoadEvents(ctx, "s", &adk.LoadEventsRequest{})
	require.NoError(t, err)
	require.Equal(t, [][]byte{first, second}, res.Events)
}

func TestFileStoreWritesOneJSONLinePerEvent(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := session.NewFileStore(dir)
	require.NoError(t, err)

	first := []byte(`{"event_id":"line-1","payload":"first"}`)
	second := []byte(`{"event_id":"line-2","payload":"second"}`)
	require.NoError(t, store.AppendEvents(ctx, "s", [][]byte{first, second}))

	data, err := os.ReadFile(filepath.Join(dir, url.PathEscape("s")+".jsonl"))
	require.NoError(t, err)
	assert.Equal(t, string(first)+"\n"+string(second)+"\n", string(data))
}

func TestFileStoreRejectsInvalidDir(t *testing.T) {
	store, err := session.NewFileStore("")
	require.Error(t, err)
	assert.Nil(t, store)
}

func TestFileStoreRejectsRawLineDelimiters(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := session.NewFileStore(dir)
	require.NoError(t, err)

	initial := []byte(`{"event_id":"line-ok","payload":"ok"}`)
	require.NoError(t, store.AppendEvents(ctx, "s", [][]byte{initial}))

	for _, payload := range [][]byte{
		[]byte("{\"event_id\":\"line-bad\n\",\"payload\":\"bad\"}"),
		[]byte("{\"event_id\":\"line-bad\r\",\"payload\":\"bad\"}"),
	} {
		err = store.AppendEvents(ctx, "s", [][]byte{payload})
		require.Error(t, err)
		assert.True(t, errors.Is(err, adk.ErrInvalidEventID))
	}

	res, err := store.LoadEvents(ctx, "s", &adk.LoadEventsRequest{})
	require.NoError(t, err)
	require.Equal(t, [][]byte{initial}, res.Events)
}

func TestAttack_FileStoreAcceptsEscapedLineDelimiters(t *testing.T) {
	ctx := context.Background()
	store, err := session.NewFileStore(t.TempDir())
	require.NoError(t, err)

	payload := []byte(`{"event_id":"escaped-line","payload":"first\nsecond\rthird"}`)
	require.NoError(t, store.AppendEvents(ctx, "s", [][]byte{payload}))

	res, err := store.LoadEvents(ctx, "s", &adk.LoadEventsRequest{})
	require.NoError(t, err)
	require.Equal(t, [][]byte{payload}, res.Events)
}

func TestFileStoreDuplicateEventIDWithinBatchFirstWriteWins(t *testing.T) {
	ctx := context.Background()
	store, err := session.NewFileStore(t.TempDir())
	require.NoError(t, err)

	first := []byte(`{"event_id":"dup-batch","payload":"first"}`)
	dup := []byte(`{"event_id":"dup-batch","payload":"second"}`)
	require.NoError(t, store.AppendEvents(ctx, "s", [][]byte{first, dup}))

	res, err := store.LoadEvents(ctx, "s", &adk.LoadEventsRequest{})
	require.NoError(t, err)
	require.Equal(t, [][]byte{first}, res.Events)
}

type fileStoreRunnerAgent struct {
	name   string
	inputs [][]*schema.Message
}

func (a *fileStoreRunnerAgent) Name(_ context.Context) string {
	return a.name
}

func (a *fileStoreRunnerAgent) Description(_ context.Context) string {
	return "file store runner agent"
}

func (a *fileStoreRunnerAgent) Run(_ context.Context, input *adk.AgentInput, _ ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
	iter, gen := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	a.inputs = append(a.inputs, append([]*schema.Message{}, input.Messages...))
	go func() {
		defer gen.Close()
		gen.Send(&adk.AgentEvent{
			AgentName: a.name,
			Output: &adk.AgentOutput{
				MessageOutput: &adk.MessageVariant{Message: schema.AssistantMessage("ok", nil), Role: schema.Assistant},
			},
		})
		gen.Send(&adk.AgentEvent{
			AgentName: a.name,
			SessionEvent: &adk.SessionEvent[*schema.Message]{
				Kind: adk.SessionEventTurnEnd,
				TurnEnd: &adk.TurnEndState[*schema.Message]{
					Messages: append([]*schema.Message{}, input.Messages...),
				},
			},
		})
	}()
	return iter
}

func drainFileStoreRunnerEvents(t *testing.T, iter *adk.AsyncIterator[*adk.AgentEvent]) {
	t.Helper()
	for {
		event, ok := iter.Next()
		if !ok {
			return
		}
		require.NoError(t, event.Err)
	}
}

func TestAttack_FileStoreSupportsRunnerDefaultSessionEncoding(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := session.NewFileStore(dir)
	require.NoError(t, err)

	firstAgent := &fileStoreRunnerAgent{name: "first"}
	first := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:              firstAgent,
		SessionID:          "runner-jsonl",
		SessionStore:       store,
		SessionPersistence: &adk.SessionPersistenceConfig{EventFlushBatchSize: 1},
	})
	drainFileStoreRunnerEvents(t, first.Query(ctx, "hello"))

	reopened, err := session.NewFileStore(dir)
	require.NoError(t, err)
	secondAgent := &fileStoreRunnerAgent{name: "second"}
	second := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:              secondAgent,
		SessionID:          "runner-jsonl",
		SessionStore:       reopened,
		SessionPersistence: &adk.SessionPersistenceConfig{EventFlushBatchSize: 1},
	})
	drainFileStoreRunnerEvents(t, second.Query(ctx, "again"))

	require.Len(t, secondAgent.inputs, 1)
	require.NotEmpty(t, secondAgent.inputs[0])
	assert.Equal(t, "hello", secondAgent.inputs[0][0].Content)
}

func TestFileStoreAppendFailsOnCorruptedExistingLog(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := session.NewFileStore(dir)
	require.NoError(t, err)

	path := filepath.Join(dir, url.PathEscape("s")+".jsonl")
	require.NoError(t, os.WriteFile(path, []byte("not-json\n"), 0o644))

	err = store.AppendEvents(ctx, "s", [][]byte{[]byte(`{"event_id":"new","payload":"new"}`)})
	require.Error(t, err)
	assert.True(t, errors.Is(err, adk.ErrInvalidEventID))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "not-json\n", string(data))
}

func TestFileStoreRejectsEmptySessionID(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := session.NewFileStore(dir)
	require.NoError(t, err)

	err = store.AppendEvents(ctx, "", [][]byte{[]byte(`{"event_id":"empty-session"}`)})
	require.Error(t, err)

	_, err = store.LoadEvents(ctx, "", &adk.LoadEventsRequest{})
	require.Error(t, err)

	_, statErr := os.Stat(filepath.Join(dir, ".jsonl"))
	assert.True(t, os.IsNotExist(statErr))
}

func TestFileStoreEscapedSessionIDPath(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := session.NewFileStore(dir)
	require.NoError(t, err)

	sessionID := "a/b %雪"
	payload := []byte(`{"event_id":"escaped","payload":"ok"}`)
	require.NoError(t, store.AppendEvents(ctx, sessionID, [][]byte{payload}))

	res, err := store.LoadEvents(ctx, sessionID, &adk.LoadEventsRequest{})
	require.NoError(t, err)
	require.Equal(t, [][]byte{payload}, res.Events)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, url.PathEscape(sessionID)+".jsonl", entries[0].Name())
}
