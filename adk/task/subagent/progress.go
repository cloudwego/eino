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
	"fmt"
	"strings"

	"github.com/bytedance/sonic"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/task/background"
)

const (
	maxProgressRecords = 100
	maxProgressBytes   = 64 << 10
)

// ReadProgress projects a bounded child-session transcript without exposing
// the Controller's session store. It returns an empty string for tasks owned by
// another executor. format converts one materialized child-session message to
// one transcript record; an empty result skips the message. It may be called
// concurrently and must not mutate the message.
func (r *Controller[M]) ReadProgress(
	ctx context.Context,
	task *background.TaskSnapshot,
	format func(context.Context, string, M) (string, error),
) (string, error) {
	if task == nil || task.Spec.ExecutorKey != ExecutorKey {
		return "", nil
	}
	if r == nil {
		return "", errors.New("task/subagent: controller is required to read progress")
	}
	if format == nil {
		return "", errors.New("task/subagent: progress formatter is required")
	}
	payload, err := validateSpecPayload(task.Spec)
	if err != nil {
		return "", err
	}
	sessionStore, err := r.sessionStoreFor(
		ctx, task.Spec.ID, task.Spec.RootSessionID, payload.ChildSessionID,
		task, false,
	)
	if err != nil {
		return "", fmt.Errorf(
			"task/subagent: construct progress session store: %w",
			err,
		)
	}
	return readProgress(
		ctx,
		sessionStore,
		task,
		payload.ChildSessionID,
		payload.SubAgentName,
		format,
	)
}

func readProgress[M adk.MessageType](
	ctx context.Context,
	store adk.SessionEventStore[M],
	task *background.TaskSnapshot,
	sessionID string,
	agentName string,
	format func(context.Context, string, M) (string, error),
) (string, error) {
	inputEventID, err := firstTaskMessageEventID(
		ctx, store, sessionID, task.Spec.ID,
	)
	if err != nil {
		return "", err
	}

	recent, moreRecent, err := loadTaskEventsReverse(
		ctx,
		store,
		sessionID,
		task.Spec.ID,
		maxProgressRecords+1,
		[]adk.SessionEventKind{
			adk.SessionEventMessage,
			adk.SessionEventMessageStreamIncomplete,
		},
	)
	if err != nil {
		return "", err
	}

	lines := make([]string, 0, len(recent))
	usedBytes := 0
	truncated := moreRecent
	for _, event := range recent {
		if event.EventID == inputEventID {
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
	if task.Status == background.StatusWaitingInput {
		interrupts, _, loadErr := loadTaskEventsReverse(
			ctx,
			store,
			sessionID,
			task.Spec.ID,
			1,
			[]adk.SessionEventKind{adk.SessionEventInterrupt},
		)
		if loadErr != nil {
			return "", loadErr
		}
		if len(interrupts) > 0 && interrupts[0].Interrupt != nil {
			data, marshalErr := sonic.Marshal(interrupts[0].Interrupt.Contexts)
			if marshalErr != nil {
				return "", marshalErr
			}
			sections = append(sections, "Input required:\n"+string(data))
		}
	}
	return strings.Join(sections, "\n"), nil
}

func firstTaskMessageEventID[M adk.MessageType](
	ctx context.Context,
	store adk.SessionEventStore[M],
	sessionID,
	taskID string,
) (string, error) {
	cursor := ""
	for {
		page, err := store.LoadEvents(ctx, sessionID, &adk.LoadSessionEventsRequest{
			After: cursor, Limit: maxProgressRecords,
			Kinds: []adk.SessionEventKind{adk.SessionEventMessage},
		})
		if err != nil {
			return "", err
		}
		for _, event := range page.Events {
			if eventBelongsToTask(event, taskID) {
				return event.EventID, nil
			}
		}
		if page.Next == "" {
			return "", nil
		}
		cursor = page.Next
	}
}

func loadTaskEventsReverse[M adk.MessageType](
	ctx context.Context,
	store adk.SessionEventStore[M],
	sessionID,
	taskID string,
	limit int,
	kinds []adk.SessionEventKind,
) ([]*adk.SessionEvent[M], bool, error) {
	cursor := ""
	result := make([]*adk.SessionEvent[M], 0, limit)
	for {
		page, err := store.LoadEvents(ctx, sessionID, &adk.LoadSessionEventsRequest{
			After: cursor, Limit: maxProgressRecords, Reverse: true, Kinds: kinds,
		})
		if err != nil {
			return nil, false, err
		}
		for _, event := range page.Events {
			if !eventBelongsToTask(event, taskID) {
				continue
			}
			if len(result) == limit {
				return result, true, nil
			}
			result = append(result, event)
		}
		if page.Next == "" {
			return result, false, nil
		}
		cursor = page.Next
	}
}

func eventBelongsToTask[M adk.MessageType](
	event *adk.SessionEvent[M],
	taskID string,
) bool {
	if event == nil || event.Extra == nil {
		return false
	}
	value, ok := event.Extra[taskIDEventExtraKey].(string)
	return ok && value == taskID
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
