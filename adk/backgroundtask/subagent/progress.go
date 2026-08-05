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

package subagent

import (
	"context"
	"errors"
	"strings"

	"github.com/bytedance/sonic"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/backgroundtask"
)

const (
	maxProgressRecords = 100
	maxProgressBytes   = 64 << 10
)

// ReadProgress projects a bounded child-session transcript without exposing
// the Executor's session store. It returns an empty string for tasks owned by
// another executor. format converts one materialized child-session message to
// one transcript record; an empty result skips the message. It may be called
// concurrently and must not mutate the message.
func (e *Executor[M]) ReadProgress(
	ctx context.Context,
	task *backgroundtask.Task,
	format func(context.Context, string, M) (string, error),
) (string, error) {
	if task == nil || task.Spec.ExecutorKey != ExecutorKey {
		return "", nil
	}
	if e == nil || e.sessionStore == nil {
		return "", errors.New("backgroundtask/subagent: executor is required to read progress")
	}
	if format == nil {
		return "", errors.New("backgroundtask/subagent: progress formatter is required")
	}
	payload, err := validateSpecPayload(task.Spec)
	if err != nil {
		return "", err
	}
	return readProgress(ctx, e.sessionStore, task, payload.SubAgentName, format)
}

func readProgress[M adk.MessageType](
	ctx context.Context,
	store adk.SessionEventStore[M],
	task *backgroundtask.Task,
	agentName string,
	format func(context.Context, string, M) (string, error),
) (string, error) {
	sessionID := childSessionID(task.Spec.ID)
	first, err := store.LoadEvents(ctx, sessionID, &adk.LoadSessionEventsRequest{
		Limit: 1,
		Kinds: []adk.SessionEventKind{adk.SessionEventMessage},
	})
	if err != nil {
		return "", err
	}
	var inputEventID string
	if len(first.Events) > 0 {
		inputEventID = first.Events[0].EventID
	}

	recent, err := store.LoadEvents(ctx, sessionID, &adk.LoadSessionEventsRequest{
		Limit:   maxProgressRecords + 1,
		Reverse: true,
		Kinds: []adk.SessionEventKind{
			adk.SessionEventMessage,
			adk.SessionEventMessageStreamIncomplete,
		},
	})
	if err != nil {
		return "", err
	}

	lines := make([]string, 0, len(recent.Events))
	usedBytes := 0
	truncated := recent.Next != ""
	for _, event := range recent.Events {
		if event == nil || event.EventID == inputEventID {
			continue
		}
		var message M
		switch event.Kind {
		case adk.SessionEventMessage:
			message = event.Message
		case adk.SessionEventMessageStreamIncomplete:
			if event.MessageStreamIncomplete != nil {
				message = event.MessageStreamIncomplete.Message
			}
		}
		if nilProgressMessage(message) {
			continue
		}
		line, formatErr := format(ctx, agentName, message)
		if formatErr != nil {
			return "", formatErr
		}
		if line == "" {
			continue
		}
		if len(lines) >= maxProgressRecords ||
			usedBytes+len(line)+1 > maxProgressBytes {
			truncated = true
			break
		}
		lines = append(lines, line)
		usedBytes += len(line) + 1
	}
	reverseProgress(lines)

	var sections []string
	if len(lines) > 0 || truncated {
		var transcript strings.Builder
		transcript.WriteString("Transcript:")
		if truncated {
			transcript.WriteString("\n[transcript records omitted due to display limits]")
		}
		if len(lines) > 0 {
			transcript.WriteByte('\n')
			transcript.WriteString(strings.Join(lines, "\n"))
		}
		sections = append(sections, transcript.String())
	}
	if task.Status == backgroundtask.StatusWaitingInput {
		interrupt, loadErr := store.LoadEvents(ctx, sessionID, &adk.LoadSessionEventsRequest{
			Limit:   1,
			Reverse: true,
			Kinds:   []adk.SessionEventKind{adk.SessionEventInterrupt},
		})
		if loadErr != nil {
			return "", loadErr
		}
		if len(interrupt.Events) > 0 && interrupt.Events[0].Interrupt != nil {
			data, marshalErr := sonic.Marshal(interrupt.Events[0].Interrupt.Contexts)
			if marshalErr != nil {
				return "", marshalErr
			}
			sections = append(sections, "Input required:\n"+string(data))
		}
	}
	return strings.Join(sections, "\n"), nil
}

func reverseProgress(values []string) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}

func nilProgressMessage[M adk.MessageType](message M) bool {
	switch typed := any(message).(type) {
	case adk.Message:
		return typed == nil
	case adk.AgenticMessage:
		return typed == nil
	default:
		return true
	}
}
