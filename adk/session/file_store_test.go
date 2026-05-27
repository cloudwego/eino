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
	"strings"
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

	first := adk.SessionEventPayload{EventID: "persist-1", Kind: adk.SessionEventMessage, Data: []byte(`{"payload":"first"}`)}
	second := adk.SessionEventPayload{EventID: "persist-2", Kind: adk.SessionEventTurnEnd, Data: []byte(`{"payload":"second"}`)}
	require.NoError(t, store.AppendEvents(ctx, "s", []adk.SessionEventPayload{first, second}))

	reopened, err := session.NewFileStore(dir)
	require.NoError(t, err)
	res, err := reopened.LoadEvents(ctx, "s", &adk.LoadEventsRequest{})
	require.NoError(t, err)
	require.Equal(t, []adk.SessionEventPayload{first, second}, res.Events)
}

func TestFileStoreWritesOneEvlogLinePerEvent(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := session.NewFileStore(dir)
	require.NoError(t, err)

	first := adk.SessionEventPayload{EventID: "line-1", Kind: adk.SessionEventMessage, Data: []byte(`{"payload":"first"}`)}
	second := adk.SessionEventPayload{EventID: "line-2", Kind: adk.SessionEventTurnEnd, Data: []byte(`{"payload":"second"}`)}
	require.NoError(t, store.AppendEvents(ctx, "s", []adk.SessionEventPayload{first, second}))

	data, err := os.ReadFile(filepath.Join(dir, url.PathEscape("s")+".evlog"))
	require.NoError(t, err)

	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	require.Len(t, lines, 2)

	// Each line is: <EventID>\t<Kind>\t<raw Data>
	parts0 := strings.SplitN(lines[0], "\t", 3)
	require.Len(t, parts0, 3)
	assert.Equal(t, "line-1", parts0[0])
	assert.Equal(t, "message", parts0[1])
	assert.Equal(t, `{"payload":"first"}`, parts0[2])

	parts1 := strings.SplitN(lines[1], "\t", 3)
	require.Len(t, parts1, 3)
	assert.Equal(t, "line-2", parts1[0])
	assert.Equal(t, "turn_end", parts1[1])
	assert.Equal(t, `{"payload":"second"}`, parts1[2])
}

func TestFileStoreRejectsInvalidDir(t *testing.T) {
	store, err := session.NewFileStore("")
	require.Error(t, err)
	assert.Nil(t, store)
}

func TestAttack_FileStoreRejectsRawLineDelimitersInData(t *testing.T) {
	ctx := context.Background()
	store, err := session.NewFileStore(t.TempDir())
	require.NoError(t, err)

	// Data containing \n must be rejected.
	err = store.AppendEvents(ctx, "s", []adk.SessionEventPayload{
		{EventID: "bad-lf", Data: []byte("first\nsecond")},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "without raw CR/LF")

	// Data containing \r must be rejected.
	err = store.AppendEvents(ctx, "s", []adk.SessionEventPayload{
		{EventID: "bad-cr", Data: []byte("first\rsecond")},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "without raw CR/LF")

	// Valid JSON (no raw newlines) should succeed.
	err = store.AppendEvents(ctx, "s", []adk.SessionEventPayload{
		{EventID: "good", Data: []byte(`{"msg":"hello\\nworld"}`)},
	})
	require.NoError(t, err)
}

func TestFileStoreDuplicateEventIDWithinBatchFirstWriteWins(t *testing.T) {
	ctx := context.Background()
	store, err := session.NewFileStore(t.TempDir())
	require.NoError(t, err)

	first := adk.SessionEventPayload{EventID: "dup-batch", Kind: adk.SessionEventMessage, Data: []byte(`{"payload":"first"}`)}
	dup := adk.SessionEventPayload{EventID: "dup-batch", Kind: adk.SessionEventMessage, Data: []byte(`{"payload":"second"}`)}
	require.NoError(t, store.AppendEvents(ctx, "s", []adk.SessionEventPayload{first, dup}))

	res, err := store.LoadEvents(ctx, "s", &adk.LoadEventsRequest{})
	require.NoError(t, err)
	require.Equal(t, []adk.SessionEventPayload{first}, res.Events)
}

func TestFileStoreExtensionEventCompactPayloadAndFilter(t *testing.T) {
	ctx := context.Background()
	store, err := session.NewFileStore(t.TempDir())
	require.NoError(t, err)

	extensionKind := adk.SessionEventKind("x.outcome.grading")
	se := &adk.SessionEvent[*schema.Message]{
		EventID: "extension-1",
		Kind:    extensionKind,
		Extension: &adk.SessionExtensionEvent{
			Data: []byte("{\n  \"outcome_name\": \"code_review\",\n  \"attempt\": 1\n}"),
		},
	}
	require.NoError(t, adk.NormalizeSessionEventKind(se))
	require.Equal(t, []byte(`{"outcome_name":"code_review","attempt":1}`), []byte(se.Extension.Data))

	data, err := (&schema.HumanReadableSerializer{}).Marshal(se)
	require.NoError(t, err)
	require.NotContains(t, string(data), "\n")
	payload := adk.SessionEventPayload{EventID: se.EventID, Kind: se.Kind, Data: data}
	require.NoError(t, store.AppendEvents(ctx, "s", []adk.SessionEventPayload{
		{EventID: "message-1", Kind: adk.SessionEventMessage, Data: []byte(`{"message":1}`)},
		payload,
	}))

	res, err := store.LoadEvents(ctx, "s", &adk.LoadEventsRequest{
		Kinds: []adk.SessionEventKind{extensionKind},
	})
	require.NoError(t, err)
	require.Equal(t, []adk.SessionEventPayload{payload}, res.Events)

	var decoded adk.SessionEvent[*schema.Message]
	require.NoError(t, (&schema.HumanReadableSerializer{}).Unmarshal(res.Events[0].Data, &decoded))
	require.NoError(t, adk.NormalizeSessionEventKind(&decoded))
	require.NotNil(t, decoded.Extension)
	assert.Equal(t, []byte(`{"outcome_name":"code_review","attempt":1}`), []byte(decoded.Extension.Data))
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
		Agent:        firstAgent,
		SessionID:    "runner-jsonl",
		SessionStore: store,
		Session:      &adk.SessionConfig{EventFlushBatchSize: 1},
	})
	drainFileStoreRunnerEvents(t, first.Query(ctx, "hello"))

	reopened, err := session.NewFileStore(dir)
	require.NoError(t, err)
	secondAgent := &fileStoreRunnerAgent{name: "second"}
	second := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:        secondAgent,
		SessionID:    "runner-jsonl",
		SessionStore: reopened,
		Session:      &adk.SessionConfig{EventFlushBatchSize: 1},
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

	// Write a corrupted evlog line (missing tab separator).
	path := filepath.Join(dir, url.PathEscape("s")+".evlog")
	require.NoError(t, os.WriteFile(path, []byte("corrupted-no-tab\n"), 0o644))

	err = store.AppendEvents(ctx, "s", []adk.SessionEventPayload{{EventID: "new", Data: []byte(`{"payload":"new"}`)}})
	require.Error(t, err)
	assert.True(t, errors.Is(err, adk.ErrInvalidEventID))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "corrupted-no-tab\n", string(data))
}

func TestFileStoreRejectsEmptySessionID(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := session.NewFileStore(dir)
	require.NoError(t, err)

	err = store.AppendEvents(ctx, "", []adk.SessionEventPayload{{EventID: "empty-session", Data: []byte(`{}`)}})
	require.Error(t, err)

	_, err = store.LoadEvents(ctx, "", &adk.LoadEventsRequest{})
	require.Error(t, err)

	_, statErr := os.Stat(filepath.Join(dir, ".evlog"))
	assert.True(t, os.IsNotExist(statErr))
}

func TestFileStoreEscapedSessionIDPath(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := session.NewFileStore(dir)
	require.NoError(t, err)

	sessionID := "a/b %雪"
	payload := adk.SessionEventPayload{EventID: "escaped", Kind: adk.SessionEventMessage, Data: []byte(`{"payload":"ok"}`)}
	require.NoError(t, store.AppendEvents(ctx, sessionID, []adk.SessionEventPayload{payload}))

	res, err := store.LoadEvents(ctx, sessionID, &adk.LoadEventsRequest{})
	require.NoError(t, err)
	require.Equal(t, []adk.SessionEventPayload{payload}, res.Events)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, url.PathEscape(sessionID)+".evlog", entries[0].Name())
}

func TestFileStorePersistenceFormatWithTabInData(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := session.NewFileStore(dir)
	require.NoError(t, err)

	first := adk.SessionEventPayload{EventID: "tab-1", Kind: adk.SessionEventMessage, Data: []byte("hello\tworld")}
	second := adk.SessionEventPayload{EventID: "tab-2", Kind: adk.SessionEventTurnEnd, Data: []byte("end")}
	require.NoError(t, store.AppendEvents(ctx, "s", []adk.SessionEventPayload{first, second}))

	// Reopen the store.
	reopened, err := session.NewFileStore(dir)
	require.NoError(t, err)

	res, err := reopened.LoadEvents(ctx, "s", &adk.LoadEventsRequest{})
	require.NoError(t, err)
	require.Len(t, res.Events, 2)

	assert.Equal(t, "tab-1", res.Events[0].EventID)
	assert.Equal(t, adk.SessionEventMessage, res.Events[0].Kind)
	assert.Equal(t, []byte("hello\tworld"), res.Events[0].Data)

	assert.Equal(t, "tab-2", res.Events[1].EventID)
	assert.Equal(t, adk.SessionEventTurnEnd, res.Events[1].Kind)
	assert.Equal(t, []byte("end"), res.Events[1].Data)

	// Read the raw file and verify line format.
	data, err := os.ReadFile(filepath.Join(dir, url.PathEscape("s")+".evlog"))
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	require.Len(t, lines, 2)

	// Each line: <EventID>\t<Kind>\t<Data> — split by first 2 tabs only.
	parts0 := strings.SplitN(lines[0], "\t", 3)
	require.Len(t, parts0, 3)
	// The Data part should contain the tab byte.
	assert.Contains(t, parts0[2], "\t")

	parts1 := strings.SplitN(lines[1], "\t", 3)
	require.Len(t, parts1, 3)
}

func TestFileStoreKindFilter(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := session.NewFileStore(dir)
	require.NoError(t, err)

	e1 := adk.SessionEventPayload{EventID: "e1", Kind: adk.SessionEventMessage, Data: []byte(`{"m":1}`)}
	e2 := adk.SessionEventPayload{EventID: "e2", Kind: adk.SessionEventSpanModelRequestStart, Data: []byte(`{"s":1}`)}
	e3 := adk.SessionEventPayload{EventID: "e3", Kind: adk.SessionEventTurnEnd, Data: []byte(`{"t":1}`)}
	e4 := adk.SessionEventPayload{EventID: "e4", Kind: adk.SessionEventMessage, Data: []byte(`{"m":2}`)}
	require.NoError(t, store.AppendEvents(ctx, "s", []adk.SessionEventPayload{e1, e2, e3, e4}))

	// Load with kind filter: message + turn_end only.
	kinds := []adk.SessionEventKind{adk.SessionEventMessage, adk.SessionEventTurnEnd}
	res, err := store.LoadEvents(ctx, "s", &adk.LoadEventsRequest{Kinds: kinds})
	require.NoError(t, err)
	require.Len(t, res.Events, 3)
	assert.Equal(t, "e1", res.Events[0].EventID)
	assert.Equal(t, "e3", res.Events[1].EventID)
	assert.Equal(t, "e4", res.Events[2].EventID)

	// Load with Limit=1 and same Kinds.
	res, err = store.LoadEvents(ctx, "s", &adk.LoadEventsRequest{Kinds: kinds, Limit: 1})
	require.NoError(t, err)
	require.Len(t, res.Events, 1)
	assert.Equal(t, "e1", res.Events[0].EventID)
	assert.Equal(t, "e1", res.Next)

	// Load with After="e1", Limit=1, same Kinds.
	res, err = store.LoadEvents(ctx, "s", &adk.LoadEventsRequest{After: "e1", Kinds: kinds, Limit: 1})
	require.NoError(t, err)
	require.Len(t, res.Events, 1)
	assert.Equal(t, "e3", res.Events[0].EventID)
	assert.Equal(t, "e3", res.Next)
}
