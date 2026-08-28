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

package backgroundtask_test

import (
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/backgroundtask"
)

func TestForegroundTimeoutError(t *testing.T) {
	err := error(&backgroundtask.ForegroundTimeoutError{
		Timeout: 250 * time.Millisecond,
		TaskID:  "task_123",
	})

	require.Equal(
		t,
		`backgroundtask: foreground task "task_123" timed out after 250ms`,
		err.Error(),
	)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	var timeoutErr *backgroundtask.ForegroundTimeoutError
	require.True(t, errors.As(err, &timeoutErr))
	require.Equal(t, 250*time.Millisecond, timeoutErr.Timeout)
	require.Equal(t, "task_123", timeoutErr.TaskID)
}

func TestForegroundTimeoutErrorAgentEventGobRoundTrip(t *testing.T) {
	original := &adk.AgentEvent{Err: &backgroundtask.ForegroundTimeoutError{
		Timeout: 3 * time.Second, TaskID: "task_checkpoint",
	}}
	var buf bytes.Buffer
	require.NoError(t, gob.NewEncoder(&buf).Encode(original))

	var decoded adk.AgentEvent
	require.NoError(t, gob.NewDecoder(&buf).Decode(&decoded))
	require.ErrorIs(t, decoded.Err, context.DeadlineExceeded)
	var timeoutErr *backgroundtask.ForegroundTimeoutError
	require.ErrorAs(t, decoded.Err, &timeoutErr)
	require.Equal(t, 3*time.Second, timeoutErr.Timeout)
	require.Equal(t, "task_checkpoint", timeoutErr.TaskID)
}
