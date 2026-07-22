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
	session.RunConformanceTests[*schema.Message](t, func(t testing.TB) adk.SessionEventStore[*schema.Message] {
		store, err := session.NewFileStore[*schema.Message](t.TempDir(), nil)
		require.NoError(t, err)
		return store
	}, func(content string) *schema.Message {
		return schema.UserMessage(content)
	})
	session.RunSerializerConformanceTests[*schema.Message](t, func(t testing.TB, serializer schema.Serializer) adk.SessionEventStore[*schema.Message] {
		store, err := session.NewFileStore[*schema.Message](t.TempDir(), &session.FileStoreConfig{EventSerializer: serializer})
		require.NoError(t, err)
		return store
	}, func(content string) *schema.Message {
		return schema.UserMessage(content)
	})
}

func TestFileStorePersistsAcrossInstances(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := session.NewFileStore[*schema.Message](dir, nil)
	require.NoError(t, err)

	first := testMessageEvent("persist-1", "first")
	second := testCommittedIdleEvent("persist-2", "turn-1")
	err = store.AppendEvents(ctx, "s", []*adk.SessionEvent[*schema.Message]{first, second})
	require.NoError(t, err)

	reopened, err := session.NewFileStore[*schema.Message](dir, nil)
	require.NoError(t, err)
	res, err := reopened.LoadEvents(ctx, "s", &adk.LoadSessionEventsRequest{})
	require.NoError(t, err)
	require.Len(t, res.Events, 2)
	assert.Equal(t, "persist-1", res.Events[0].EventID)
	assert.Equal(t, "persist-2", res.Events[1].EventID)
}

func TestFileStoreWritesHumanReadableEvlogLines(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := session.NewFileStore[*schema.Message](dir, nil)
	require.NoError(t, err)

	first := testMessageEvent("line-1", "first")
	second := testCommittedIdleEvent("line-2", "turn-1")
	err = store.AppendEvents(ctx, "s", []*adk.SessionEvent[*schema.Message]{first, second})
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(dir, url.PathEscape("s")+".evlog"))
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	require.Len(t, lines, 2)

	parts0 := strings.SplitN(lines[0], "\t", 3)
	require.Len(t, parts0, 3)
	assert.Equal(t, "line-1", parts0[0])
	assert.Equal(t, "message", parts0[1])
	assert.Contains(t, parts0[2], "first")

	parts1 := strings.SplitN(lines[1], "\t", 3)
	require.Len(t, parts1, 3)
	assert.Equal(t, "line-2", parts1[0])
	assert.Equal(t, "session.status_idle", parts1[1])
}

func TestFileStoreSessionEventExtraRoundTripKeepsThreeColumnFormat(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := session.NewFileStore[*schema.Message](dir, nil)
	require.NoError(t, err)

	event := testMessageEvent("extra-1", "one")
	event.Extra = map[string]any{
		"reason": "audit",
		"nested": map[string]any{"items": []any{"a", "b"}},
	}
	require.NoError(t, store.AppendEvents(ctx, "s", []*adk.SessionEvent[*schema.Message]{event}))

	reopened, err := session.NewFileStore[*schema.Message](dir, nil)
	require.NoError(t, err)
	res, err := reopened.LoadEvents(ctx, "s", &adk.LoadSessionEventsRequest{})
	require.NoError(t, err)
	require.Len(t, res.Events, 1)
	assert.Equal(t, "audit", res.Events[0].Extra["reason"])
	assert.Equal(t, "a", res.Events[0].Extra["nested"].(map[string]any)["items"].([]any)[0])

	data, err := os.ReadFile(filepath.Join(dir, url.PathEscape("s")+".evlog"))
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	require.Len(t, lines, 1)
	parts := strings.SplitN(lines[0], "\t", 4)
	require.Len(t, parts, 3)
	assert.Equal(t, "extra-1", parts[0])
	assert.Equal(t, "message", parts[1])
	assert.Contains(t, parts[2], "extra")
}

func TestFileStoreRollbackPreservesPhysicalAuditLog(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := session.NewFileStore[*schema.Message](dir, nil)
	require.NoError(t, err)
	sessionID := "rollback-audit"

	err = store.AppendEvents(ctx, sessionID, []*adk.SessionEvent[*schema.Message]{
		testMessageEvent("msg-1", "Q1"),
		testCommittedIdleEvent("end-1", "turn-1"),
		testMessageEvent("msg-2", "Q2"),
		testCommittedIdleEvent("end-2", "turn-2"),
	})
	require.NoError(t, err)

	require.NoError(t, adk.RollbackSession[*schema.Message](ctx, store, sessionID, "end-1"))

	res, err := store.LoadEvents(ctx, sessionID, &adk.LoadSessionEventsRequest{})
	require.NoError(t, err)
	require.Len(t, res.Events, 5)
	assert.Equal(t, "msg-2", res.Events[2].EventID)
	assert.Equal(t, "end-2", res.Events[3].EventID)
	assert.Equal(t, adk.SessionEventRollback, res.Events[4].Kind)

	data, err := os.ReadFile(filepath.Join(dir, url.PathEscape(sessionID)+".evlog"))
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	require.Len(t, lines, 5)
	assert.Contains(t, lines[4], "\trollback\t")
}

func TestFileStoreRejectsInvalidDir(t *testing.T) {
	store, err := session.NewFileStore[*schema.Message]("", nil)
	require.Error(t, err)
	assert.Nil(t, store)
}

func TestFileStoreRejectsSerializerRawLineDelimiters(t *testing.T) {
	ctx := context.Background()
	store, err := session.NewFileStore[*schema.Message](t.TempDir(), &session.FileStoreConfig{
		EventSerializer: newlineSerializer{},
	})
	require.NoError(t, err)

	err = store.AppendEvents(ctx, "s", []*adk.SessionEvent[*schema.Message]{testMessageEvent("bad", "bad")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "without raw CR/LF")
}

func TestFileStoreAppendFailsOnCorruptedExistingLog(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := session.NewFileStore[*schema.Message](dir, nil)
	require.NoError(t, err)

	path := filepath.Join(dir, url.PathEscape("s")+".evlog")
	require.NoError(t, os.WriteFile(path, []byte("corrupted-no-tab\n"), 0o644))

	err = store.AppendEvents(ctx, "s", []*adk.SessionEvent[*schema.Message]{testMessageEvent("new", "new")})
	require.Error(t, err)
	assert.True(t, errors.Is(err, adk.ErrInvalidEventID))
}

func TestFileStoreEscapedSessionIDPath(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := session.NewFileStore[*schema.Message](dir, nil)
	require.NoError(t, err)

	sessionID := "a/b %snow"
	err = store.AppendEvents(ctx, sessionID, []*adk.SessionEvent[*schema.Message]{testMessageEvent("escaped", "ok")})
	require.NoError(t, err)

	res, err := store.LoadEvents(ctx, sessionID, &adk.LoadSessionEventsRequest{})
	require.NoError(t, err)
	require.Len(t, res.Events, 1)
	assert.Equal(t, "escaped", res.Events[0].EventID)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, url.PathEscape(sessionID)+".evlog", entries[0].Name())
}

func TestFileStoreValidationReplayAndReversePagination(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := session.NewFileStore[*schema.Message](dir, nil)
	require.NoError(t, err)

	_, err = session.NewFileStore[*schema.Message]("", nil)
	require.Error(t, err)

	service, err := session.NewFileStore[*schema.Message](filepath.Join(dir, "svc"), nil)
	require.NoError(t, err)
	assert.NotNil(t, service)

	require.Error(t, store.AppendEvents(ctx, "", nil))

	empty, err := store.LoadEvents(ctx, "empty", &adk.LoadSessionEventsRequest{Reverse: true})
	require.NoError(t, err)
	assert.Empty(t, empty.Events)

	events := []*adk.SessionEvent[*schema.Message]{
		testMessageEvent("e1", "one"),
		testSpanEvent("e2"),
		testCommittedIdleEvent("e3", "turn-1"),
	}
	err = store.AppendEvents(ctx, "s", events)
	require.NoError(t, err)

	err = store.AppendEvents(ctx, "s", []*adk.SessionEvent[*schema.Message]{testMessageEvent("e4", "four")})
	require.NoError(t, err)

	err = store.AppendEvents(ctx, "s", []*adk.SessionEvent[*schema.Message]{testMessageEvent("e1", "duplicate existing")})
	require.ErrorIs(t, err, adk.ErrDuplicateEventID)

	err = store.AppendEvents(ctx, "s2", []*adk.SessionEvent[*schema.Message]{
		testMessageEvent("dup", "one"),
		testMessageEvent("dup", "two"),
	})
	require.ErrorIs(t, err, adk.ErrDuplicateEventID)

	_, err = store.LoadEvents(ctx, "s", &adk.LoadSessionEventsRequest{After: "missing"})
	require.ErrorIs(t, err, adk.ErrEventIDOutOfRange)
	_, err = store.LoadEvents(ctx, "s", &adk.LoadSessionEventsRequest{Reverse: true, After: "missing"})
	require.ErrorIs(t, err, adk.ErrEventIDOutOfRange)

	forward, err := store.LoadEvents(ctx, "s", &adk.LoadSessionEventsRequest{
		After: "e1",
		Kinds: []adk.SessionEventKind{adk.SessionEventSessionStatusIdle, adk.SessionEventMessage},
		Limit: 1,
	})
	require.NoError(t, err)
	require.Len(t, forward.Events, 1)
	assert.Equal(t, "e3", forward.Events[0].EventID)
	assert.Equal(t, "e3", forward.Next)

	reverse, err := store.LoadEvents(ctx, "s", &adk.LoadSessionEventsRequest{
		Reverse: true,
		After:   "e4",
		Limit:   1,
	})
	require.NoError(t, err)
	require.Len(t, reverse.Events, 1)
	assert.Equal(t, "e3", reverse.Events[0].EventID)
	assert.Equal(t, "e3", reverse.Next)
}

func TestFileStoreRejectsCorruptedRecordsOnIndexRebuild(t *testing.T) {
	ctx := context.Background()
	cases := map[string]string{
		"missing newline":     "e1\tmessage\t{}",
		"empty event id":      "\tmessage\t{}\n",
		"missing kind tab":    "e1\tmessage-only\n",
		"duplicate event id":  "e1\tmessage\t{}\ne1\tmessage\t{}\n",
		"extra mismatches":    "e1\tturn_end\t{\"event_id\":\"e1\",\"kind\":\"message\",\"message\":{\"role\":\"user\",\"content\":\"x\"}}\n",
		"invalid event body":  "e1\tmessage\tnot-json\n",
		"invalid event shape": "e1\tmessage\t{\"event_id\":\"e1\",\"kind\":\"message\"}\n",
		"empty session id":    "",
	}

	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			store, err := session.NewFileStore[*schema.Message](dir, nil)
			require.NoError(t, err)

			if name == "empty session id" {
				_, err = store.LoadEvents(ctx, "", &adk.LoadSessionEventsRequest{})
				require.Error(t, err)
				return
			}

			path := filepath.Join(dir, url.PathEscape("s")+".evlog")
			require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
			_, err = store.LoadEvents(ctx, "s", &adk.LoadSessionEventsRequest{})
			require.Error(t, err)
		})
	}
}

type newlineSerializer struct{}

func (newlineSerializer) Marshal(any) ([]byte, error) {
	return []byte("bad\nline"), nil
}

func (newlineSerializer) Unmarshal([]byte, any) error {
	return nil
}
