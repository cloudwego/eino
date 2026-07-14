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

package backgroundtask

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/schema"
)

// drainStringStream reads a string stream to EOF and returns the concatenation.
func drainStringStream(t *testing.T, sr *schema.StreamReader[string]) string {
	t.Helper()
	defer sr.Close()
	var b strings.Builder
	for {
		chunk, err := sr.Recv()
		if errors.Is(err, io.EOF) {
			return b.String()
		}
		require.NoError(t, err)
		b.WriteString(chunk)
	}
}

// streamWorkChunks returns a StreamWorkFunc that emits the given chunks, optionally
// pausing before each so a deadline can fire mid-stream.
func streamWorkChunks(pause time.Duration, chunks ...string) StreamWorkFunc {
	return func(ctx context.Context, _ TaskInfo) (*schema.StreamReader[string], error) {
		sr, sw := schema.Pipe[string](len(chunks))
		go func() {
			defer sw.Close()
			for _, c := range chunks {
				if pause > 0 {
					select {
					case <-time.After(pause):
					case <-ctx.Done():
						return
					}
				}
				if sw.Send(c, nil) {
					return
				}
			}
		}()
		return sr, nil
	}
}

// TestRunStream_ForegroundStreamsAndCompletes: every chunk is forwarded live and
// the accumulated text becomes the task's final Result.
func TestRunStream_ForegroundStreamsAndCompletes(t *testing.T) {
	m := New(context.Background(), &Config{ForegroundTimeoutMs: intPtr(0)})
	defer closeWithTimeout(m)

	sr, err := m.RunStream(context.Background(), &RunInput{Description: "stream"},
		streamWorkChunks(0, "a", "b", "c"))
	require.NoError(t, err)

	got := drainStringStream(t, sr)
	assert.Equal(t, "abc", got)

	tasks := m.List()
	require.Len(t, tasks, 1)
	task := waitTask(t, m, tasks[0].ID)
	assert.Equal(t, StatusCompleted, task.Status)
	assert.Equal(t, "abc", task.Result)
}

// TestRunStream_AutoBackground: a run that outlives its budget is moved to the
// background; the caller's stream is capped with a notice and the remaining chunks
// are drained into the task Result.
func TestRunStream_AutoBackground(t *testing.T) {
	m := New(context.Background(), &Config{
		ForegroundTimeoutMs:  intPtr(40),
		ShouldAutoBackground: func(context.Context, *Task) bool { return true },
	})
	defer closeWithTimeout(m)

	// 4 chunks, ~25ms apart; budget 40ms → ~1-2 chunks stream before background.
	sr, err := m.RunStream(context.Background(), &RunInput{Description: "slow", Type: "bash"},
		streamWorkChunks(25*time.Millisecond, "1", "2", "3", "4"))
	require.NoError(t, err)

	got := drainStringStream(t, sr)
	assert.Contains(t, got, "moved to the background")
	assert.Contains(t, got, "(bash)")

	tasks := m.List()
	require.Len(t, tasks, 1)
	task := waitTask(t, m, tasks[0].ID)
	assert.Equal(t, StatusCompleted, task.Status)
	assert.True(t, task.RunInBackground)
	// All four chunks land in the final result even though only some were streamed.
	assert.Equal(t, "1234", task.Result)
}

// TestRunStream_ExplicitBackground: no execution chunks reach the caller, only the
// notice; the work runs detached and its output becomes the task Result.
func TestRunStream_ExplicitBackground(t *testing.T) {
	m := New(context.Background(), &Config{})
	defer closeWithTimeout(m)

	sr, err := m.RunStream(context.Background(),
		&RunInput{Description: "bg", Type: "bash", RunInBackground: true},
		streamWorkChunks(0, "chunk-1", "chunk-2"))
	require.NoError(t, err)

	got := drainStringStream(t, sr)
	assert.Contains(t, got, "is running in the background")
	assert.NotContains(t, got, "moved to the background")
	assert.NotContains(t, got, "chunk-")

	tasks := m.List()
	require.Len(t, tasks, 1)
	task := waitTask(t, m, tasks[0].ID)
	assert.Equal(t, StatusCompleted, task.Status)
	assert.Equal(t, "chunk-1chunk-2", task.Result)
}

// TestRunStream_ExplicitBackgroundStartupPreview forwards launch-time chunks for
// the configured preview window, then emits the normal background notice and
// drains later output into the task result without forwarding it to the caller.
func TestRunStream_ExplicitBackgroundStartupPreview(t *testing.T) {
	m := New(context.Background(), &Config{})
	defer closeWithTimeout(m)

	release := make(chan struct{})
	work := func(ctx context.Context, _ TaskInfo) (*schema.StreamReader[string], error) {
		sr, sw := schema.Pipe[string](2)
		sw.Send("authenticate at https://example.com/oauth\n", nil)
		go func() {
			defer sw.Close()
			select {
			case <-release:
				sw.Send("authenticated\n", nil)
			case <-ctx.Done():
			}
		}()
		return sr, nil
	}

	sr, err := m.RunStream(context.Background(), &RunInput{
		Description:                "oauth",
		Type:                       "bash",
		RunInBackground:            true,
		BackgroundStartupPreviewMs: 500,
	}, work)
	require.NoError(t, err)

	got := drainStringStream(t, sr)
	assert.Contains(t, got, "https://example.com/oauth")
	assert.Contains(t, got, "is running in the background")
	assert.NotContains(t, got, "authenticated")

	close(release)
	tasks := m.List()
	require.Len(t, tasks, 1)
	task := waitTask(t, m, tasks[0].ID)
	assert.True(t, task.RunInBackground)
	assert.Equal(t, StatusCompleted, task.Status)
	assert.Equal(t, "authenticate at https://example.com/oauth\nauthenticated\n", task.Result)
}

// TestRunStream_WorkError: an error from the stream finalizes the task as failed
// and surfaces on the caller's stream.
func TestRunStream_WorkError(t *testing.T) {
	m := New(context.Background(), &Config{ForegroundTimeoutMs: intPtr(0)})
	defer closeWithTimeout(m)

	wantErr := errors.New("boom")
	work := func(ctx context.Context, _ TaskInfo) (*schema.StreamReader[string], error) {
		sr, sw := schema.Pipe[string](2)
		go func() {
			defer sw.Close()
			sw.Send("partial", nil)
			sw.Send("", wantErr)
		}()
		return sr, nil
	}

	sr, err := m.RunStream(context.Background(), &RunInput{Description: "err"}, work)
	require.NoError(t, err)

	defer sr.Close()
	var sawErr error
	for {
		_, recvErr := sr.Recv()
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			sawErr = recvErr
			break
		}
	}
	require.Error(t, sawErr)
	assert.Contains(t, sawErr.Error(), "boom")

	tasks := m.List()
	require.Len(t, tasks, 1)
	task := waitTask(t, m, tasks[0].ID)
	assert.Equal(t, StatusFailed, task.Status)
}
