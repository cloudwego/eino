# ADK session event log

English | [中文](session_event_log.zh_CN.md)

When a Runner has a `SessionID` and a `SessionStore`, it writes events to the end of that session's log with `AppendEvents`. Reading in append order shows what happened. To recover the message history at a particular point, the Runner follows rollbacks and applies message changes. A message removed from the model context can still be found in the log.

## What an event records

An `adk.SessionEventStore[M]` holds a separate log for each `SessionID`. A writer supplies the session ID and an ordered batch of `SessionEvent[M]` values. A reader supplies the same session ID. The `adk` package defines the events and their replay semantics. The `adk/session` subpackage provides in-memory and file stores for local experiments. Applications can implement the same interface for their own storage. [Store interface and event types](session.go) · [InMemoryStore](session/in_memory_store.go) · [FileStore](session/file_store.go)

| Field | Meaning |
| --- | --- |
| `SessionID` and `EventID` | Store methods take the session ID as an argument. Together, the two IDs identify one record, such as a particular idle event in session S. The Runner assigns an opaque UUID by default. Applications can supply a `SessionConfig.EventIDGenerator`. |
| `TurnID` | Groups events from one logical turn, including executions on either side of an interrupt. Older records and events written outside a turn can have an empty `TurnID`. |
| `Kind` and its payload | Each event records one kind of change or observation. For example, `message` carries a message, `rollback` carries two event IDs, and `session.status_idle` carries the lifecycle state and stop reason. |
| `MessageID` | Identifies a logical message in a payload. Updating a message appends an event with a new `EventID` that refers to the message by its `MessageID`. |
| `Timestamp` and `Extra` | The timestamp records when the event occurred. Append position determines replay order. Applications can attach metadata such as a request number in the event's `Extra` field. ADK ignores this metadata when rebuilding messages. |

An execution timeline can display `session.status_*`, `span.model_request_*`, `span.tool_call_*`, `interrupt`, `cancel`, `session.error`, and application extensions under `x.*`. To build model input, the Runner follows the history selected by rollbacks and applies the events that change the message list. A failed model request can therefore appear as a span in the timeline without adding its error text to the next turn's messages.

## Reading positions on a timeline

The example IDs use a short kind name and an append position. For instance, `msg_replace-07` is the seventh event and its kind is `messages_replaced`. The numbering is for this example. In the message lists, `m1` and `m2` are message IDs. The notation `m2′` means that the content has changed and the ID is still `m2`.

| EventID | Event appended | State at this position |
| --- | --- | --- |
| `running-01` | `session.status_running` | The first turn starts with `TurnID=running-01`. |
| `msg-02` | `message(m1)` | Messages are `[m1]`. |
| `msg-03` | `message(m2)` | Messages are `[m1, m2]`. |
| `idle-04` | `session.status_idle(end_turn)` | The first commit point eligible for rollback. Both messages are already in the log. |
| `running-05` | `session.status_running` | The second turn starts. |
| `msg-06` | `message(m3)` | Messages are `[m1, m2, m3]`. The latest commit point is still `idle-04`. |
| `msg_replace-07` | `messages_replaced([s, m2, m3])` | The full replacement retains `m2` and `m3` and drops `m1`. Replay can start here. |
| `msg_update-08` | `message_updated(m2, new content)` | Messages are `[s, m2′, m3]`. The replacement at `msg_replace-07` retained `m2`, first recorded at `msg-03`. |
| `msg_insert-09` | `message_inserted(x, BeforeMessageID=m3)` | Messages are `[s, m2′, x, m3]`. |
| `idle-10` | `session.status_idle(end_turn)` | The second commit point. To rebuild its messages, start at `msg_replace-07`. |
| `rollback-11` | `rollback(FromEventID=idle-10, ToEventID=idle-04)` | The session returns to `idle-04` with `[m1, m2]`. The log still contains `msg_replace-07` and `idle-10`. |
| `running-12` | `session.status_running` | A new turn starts from `idle-04`. |
| `msg-13` | `message(m4)` | Messages are `[m1, m2, m4]`. |
| `idle-14` | `session.status_idle(end_turn)` | The third commit point has messages `[m1, m2, m4]`. |
| `rollback-15` | `rollback(FromEventID=idle-14, ToEventID=idle-10)` | Restores `[s, m2′, x, m3]` as of `idle-10`. A subsequent rollback to `idle-04` would start from `idle-10`. |

## Turns, commit points, and rollbacks

A fresh `Run` or `Query` first rebuilds the message history, then passes `session.status_running` followed by all new input messages to a single `AppendEvents` call. The agent starts after the call succeeds. Model messages, tool results, message changes, and observations can follow. When execution ends, the Runner writes `session.status_idle` with a `StopReason` describing the outcome.

If the store persists only a prefix before returning an error, subsequent checkpoint validation uses the events that reached the log.

- **Execution boundaries.** A `running` event opens an execution and an `idle` event closes it. On a fresh turn, the opening event's ID becomes the `TurnID`. After an interrupt, `Resume` writes another running event and carries forward the turn ID from the checkpoint. One logical turn can span several executions.
- **Commit points.** An `idle` event with `StopReason.Type == end_turn` marks a commit point, such as `idle-04` or `idle-10`. There is no separate commit-start event. Messages can reach storage earlier. For example, the history at `msg-06` already includes `m3`. Messages written before an interrupt or failure can likewise contribute to the next Run if they still belong to its history. An interrupt, cancellation, or failure also produces an idle outcome. Only a normally completed turn supplies a rollback target.
- **Rollback endpoints.** Each `rollback` event records the session's current commit point in `FromEventID` and the requested commit point in `ToEventID`. Both refer to idle events in the same session. An application can select any commit point already in the log, including `idle-10` after `rollback-11` skipped it. The Runner determines From and follows To when it rebuilds the history. The stored JSON key for From is `from_event_id`.
- **Rollback and fork.** A rollback keeps the same `SessionID`. New executions continue from the chosen point and append to the same log. A fork starts a separate session at a historical position so the two sessions can advance independently. `RollbackSession` operates on the supplied session.

In the example, `(idle-04, idle-10]` covers the records appended during the second turn. Its execution starts at `running-05`. Rebuilding the messages at `idle-10` starts at `msg_replace-07`. A rollback's From and To record a change of position within the session. Earlier records retain their append positions.

After `rollback-15`, the last record in the log is `rollback-15`, the most recently appended normal idle is `idle-14`, the current commit point is `idle-10`, and message reconstruction starts at `msg_replace-07`. [Runner write path](runner.go) · [Rollback examples and tests](session_test.go)

## Rebuilding the message history for the next turn

1. Read pages backward from the end of the log. On reaching a rollback on the history being followed, find and validate its `ToEventID` among earlier records. Skip all intervening events, including any rollbacks or replacements. Continue backward through the history as it stood at the target.
2. Stop when that history reaches its most recent `messages_replaced` event, and include the event in replay. Its payload is the **complete, ordered message list**. New replacements must contain at least one message. An explicit empty list in an older record still clears the history at that point. If there is no full replacement, read back to the start of the session and start with an empty list.
3. Apply the selected message events in forward order, then append the input for the new Run. Results for recognized parallel tool calls are ordered to match the calls in the preceding assistant message. Model and tool spans, ordinary observation events, and partial output from an incomplete stream do not contribute messages.

Starting at `rollback-11` takes replay back to `idle-04`. That jump skips `msg_replace-07`, so the reader continues to the beginning of the session. Starting at `rollback-15` takes replay to `idle-10`, where reading back as far as `msg_replace-07` is sufficient. This stopping rule applies to **message reconstruction**. When handling `RollbackSession`, the Runner can search past `msg_replace-07` for the requested target. It stops paging once it has confirmed both endpoints.

Both `Run` history replay and `RollbackSession` read up to 300 pages by default, with at most 100 matching events per page. Set `SessionConfig.MaxReplayLoadPages` for replay or `WithRollbackSessionMaxLoadPages` for a rollback call. A scan succeeds if it reaches its stopping point on the last allowed page. If it needs another page, it returns `ErrSessionScanLimitExceeded`. A repeated continuation cursor or an empty page with a continuation cursor also returns an error. The scanner checks for cancellation before each read. On scan failure, `Run` stops before calling the agent and `RollbackSession` stops before appending its marker. [Reverse traversal and forward replay](session.go)

## Which messages can an update, insertion, or deletion reference?

Resolve each reference against the message list **immediately before applying the event in forward order**. Messages can come from the most recent full replacement on this history or from appends and insertions since then. Without a replacement, messages accumulate from the start of the session. References can cross turn boundaries. Each full replacement resets both the list and any insertion positions left by deleted messages.

| Event | Replay rule |
| --- | --- |
| `message` | Append the message to the list. Later events can refer to it by `MessageID`. |
| `messages_replaced` | Start with the complete list in the payload. An older message can still be referenced if the new list includes it with the same ID. |
| `message_updated` | Find the visible target by `MessageID` and replace it in place. A missing or deleted target produces an error. A replacement with no ID inherits the target ID. An explicit ID must match the target. |
| `message_inserted` | An empty `BeforeMessageID` appends to the end. Otherwise insert before the specified ID. A deleted message's position remains a valid anchor. An unknown anchor produces an error. |
| `messages_deleted` | All named targets must be visible. Remove them from the output and retain their positions as insertion anchors until the next full replacement. |

In the timeline, `msg_update-08` can update `m2` because `msg_replace-07` retained it. An update to `m1` at the same position would fail. If `m3` were deleted after the replacement, `x` could still be inserted before its old position. A subsequent `messages_replaced([s])` would clear that anchor. If `m1` had been updated before `idle-04`, returning to `idle-04` would replay that update. Restoring `idle-10` uses the full list from `msg_replace-07`.

## Common use cases

### Continuing a conversation and compacting its context

For the next user question in a session, call `runner.Query(ctx, question)`, or pass multiple new messages to `Run`. The Runner rebuilds the prior messages and adds the new input. Passing the conversation history again would append those messages a second time. In the example, continuing from `idle-04` requires only `m3`. Summarization middleware can record compacted context with a full replacement like `msg_replace-07`. A later turn can still update messages retained in that replacement. [Summarization middleware](middlewares/summarization/summarization.go)

### Updating old tool messages with reduction clear

Suppose a file search returned a long text result. The assistant message `m_call` issued tool call `call_1`. The tool message `m_result` carries the same call ID and the full search result. Another round of tool calls has since completed. Before the next model request, the earlier result becomes eligible for clearing if the token count reaches `MaxTokensForClear` and only the most recent tool-call round is retained. This example uses the default clear handler, English placeholder text, and no external storage. The IDs below number the two updates in their local append order.

| EventID | Event appended | Updated message |
| --- | --- | --- |
| `msg_update-01` | `message_updated(MessageID=m_result)` | The tool result text becomes `[Old tool result content cleared]`. It still refers to `call_1`. |
| `msg_update-02` | `message_updated(MessageID=m_call)` | The assistant message keeps the original call ID and arguments and gains a flag marking it as cleared for subsequent passes. A custom `ClearHandler` can also change the call arguments in this update. |

The call ID `call_1` pairs the call with its result. Replay uses the two **message IDs** to replace each complete message in its original position, so the model still sees the assistant call before the tool result. Both original `message` events remain in the log. These updates are local to the two messages. The next turn still rebuilds history from the applicable `messages_replaced` event or from the start of the session. If the tokens saved fall short of a configured `ClearAtLeastTokens`, neither update is written. [Clear implementation](middlewares/reduction/reduction.go) · [Placeholder and token threshold tests](middlewares/reduction/reduction_test.go)

### Resuming a tool after human approval

When a tool requests approval, read the `InterruptID` for each interrupted point from the event's `Interrupt.Contexts`. Put the decision for each ID in `ResumeParams.Targets` and call `ResumeWithParams` to resume those points. Use `Resume` when execution can continue without passing a decision to a specific point.

With both a session store and a checkpoint store configured, the Runner records the persisted idle event's ID in the checkpoint after an interrupt or recoverable cancellation. On resume it reads and validates the checkpoint, decodes the execution state, appends `x.resume.request_started`, then appends `session.status_running` and invokes the agent. A stale checkpoint yields `adk.ErrCheckpointStale`. Invalid execution state or a missing run session also returns an error before either the log or the checkpoint is modified.

The checkpoint's `IdleEventID` is fixed when the checkpoint is written. Validation reads backward for the single latest relevant event and compares its ID with the saved ID. A new execution, a message change, or a rollback can advance the version. Ordinary observations and `x.resume.request_started` leave the version intact. For example, if the request marker is written but writing the running event fails, the caller can retry the same checkpoint. With a checkpoint store alone, the Runner resumes from the supplied checkpoint ID. For older session checkpoints, the Runner checks the stored `SessionID` even when `IdleEventID` is absent. Checkpoints predating both fields can resume without a log read. A checkpoint with an `IdleEventID` must also identify its session. [Runner resume path](runner.go)

TurnLoop retains the runner checkpoint and pending inputs until resume consumes it. If the running write has an unknown outcome, the next resume checks the same IdleEventID against the log. If resume fails after the owning session has advanced, TurnLoop returns the error and saves any new interrupt checkpoint. If there is no new checkpoint, it keeps the inputs queued for later turns. The next loop processes them through GenInput and Run using the session history. The optional patchtoolcalls middleware can insert placeholders for missing tool results before the model call. [TurnLoop checkpoint handling](turn_loop_checkpoint.go) · [patchtoolcalls](middlewares/patchtoolcalls/patchtoolcalls.go)

### Returning to an earlier answer and asking a new question

A “Return to this answer” action can store the `EventID` of the turn's `idle(end_turn)` event. Suppose the user is at `idle-14` and selects `idle-10`. The application can pass both the target and the commit point the user saw. If the current commit point has changed, the call returns `adk.ErrSessionHeadChanged`. After a successful rollback, call `Query` to continue from the history at `idle-10`.

```go
err := adk.RollbackSession[adk.Message](ctx, store, sessionID, "idle-10",
	adk.WithRollbackSessionFromEventID[adk.Message]("idle-14"),
	adk.WithRollbackSessionCheckPointStore[adk.Message](checkpoints),
)
```

When the checkpoint store supports deletion, Eino appends the rollback event and then deletes the session-derived checkpoint. A `*adk.RollbackCheckpointCleanupError` means the rollback to `idle-10` already took effect. To retry just the deletion, pass the ID of the appended rollback event and the same stores to `RetryRollbackCheckpointCleanup`.

```go
var cleanupErr *adk.RollbackCheckpointCleanupError
if errors.As(err, &cleanupErr) {
	err = adk.RetryRollbackCheckpointCleanup[adk.Message](ctx, store, sessionID, cleanupErr.RollbackEventID, checkpoints)
}
```

The cleanup can be retried while that rollback is the latest checkpoint-relevant event. An already absent checkpoint counts as success. If the session has advanced, cleanup returns `adk.ErrSessionHeadChanged` and preserves the current checkpoint, even if a later rollback chose the same destination. As with Run and Resume, callers using separate store instances or processes must serialize operations on the same session.

Rollback checks both idle endpoints. The next `Query` reads farther if it needs an earlier `messages_replaced` event or the start of the log to rebuild the conversation. Its replay page limit applies independently.

### Displaying an execution timeline and investigating slow tools

Read the log in forward order to show the complete execution timeline, including executions that a later rollback skipped. The function below reads events after the caller's last processed position. Save the returned `EventID` to resume polling after that event. The result's `Next` cursor indicates whether another page is available in the current direction. It is empty on the last page.

```go
func readTimeline(ctx context.Context, store adk.SessionEventStore[adk.Message],
	sessionID, after string, visit func(*adk.SessionEvent[adk.Message]) error) (string, error) {
	req := &adk.LoadSessionEventsRequest{After: after, Limit: 50}
	for {
		page, err := store.LoadEvents(ctx, sessionID, req)
		if err != nil {
			return after, err
		}
		if page == nil {
			return after, nil
		}
		for _, event := range page.Events {
			if err := visit(event); err != nil {
				return after, err
			}
			after = event.EventID
		}
		if page.Next == "" {
			return after, nil
		}
		req.After = page.Next
	}
}
```

To display the newest page first, set `Reverse: true`. To page further back, keep the same direction and `Kinds` filter, and pass the previous page's `Next` as `After`. Both directions exclude the cursor event. To investigate a slow tool, filter for `span.tool_call_start` and `span.tool_call_end`, pair them by `SpanID`, and compute duration from `StartedAt` and `EndedAt` on the end event. With parallel calls, events from other calls can appear between the two. [Pagination contract and span fields](session.go)

A live UI can pass `adk.WithTimelineEvents()` to receive session events through the Runner's iterator. For a streaming message, `SessionEventVariant.MessageStreamRef` supplies a reserved `EventID` while the UI displays incoming content. A `message` or `message_stream_incomplete` event with the same ID can then update that entry. An empty stream may yield only the reference. The UI can display a recorded prefix from a failed stream. Message reconstruction skips it on the next turn. [Live events and stream references](session.go)
