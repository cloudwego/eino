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
	durablesubagent "github.com/cloudwego/eino/adk/backgroundtask/subagent"
)

const (
	maxTaskProgressRecords = 100
	maxTaskProgressBytes   = 64 << 10
)

// NewDurableTaskProgressHook returns a task_output progress callback backed by
// the durable sub-agent's child session.
func NewDurableTaskProgressHook[M adk.MessageType](
	store adk.SessionEventStore[M],
	format TranscriptFormat[M],
) func(context.Context, *backgroundtask.Task) (string, error) {
	if format == nil {
		format = defaultTranscriptFormat[M]
	}
	return func(ctx context.Context, task *backgroundtask.Task) (string, error) {
		if task == nil || task.Spec.ExecutorKey != durablesubagent.ExecutorKey ||
			task.Spec.Kind != TaskTypeSubagent {
			return "", nil
		}
		if store == nil {
			return "", errors.New("subagent: session store is required to read task progress")
		}
		agentName := NameFromTask(task)
		if agentName == "" {
			return "", errors.New("subagent: task payload does not contain a valid agent name")
		}
		return readDurableTaskProgress(
			ctx, store, task, agentName, format,
		)
	}
}

func readDurableTaskProgress[M adk.MessageType](
	ctx context.Context,
	store adk.SessionEventStore[M],
	task *backgroundtask.Task,
	agentName string,
	format TranscriptFormat[M],
) (string, error) {
	sessionID := task.Spec.ID + "/session"
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
		Limit:   maxTaskProgressRecords + 1,
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
		if nilTranscriptMessage(message) {
			continue
		}
		line, formatErr := format(ctx, agentName, message)
		if formatErr != nil {
			return "", formatErr
		}
		if line == "" {
			continue
		}
		if len(lines) >= maxTaskProgressRecords ||
			usedBytes+len(line)+1 > maxTaskProgressBytes {
			truncated = true
			break
		}
		lines = append(lines, line)
		usedBytes += len(line) + 1
	}
	reverseStrings(lines)

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

func reverseStrings(values []string) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}

func nilTranscriptMessage[M adk.MessageType](message M) bool {
	switch typed := any(message).(type) {
	case adk.Message:
		return typed == nil
	case adk.AgenticMessage:
		return typed == nil
	default:
		return true
	}
}
