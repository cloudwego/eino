# ADK 会话事件日志（event log）

[English](session_event_log.md) | 中文

配置 `SessionID` 和 `SessionStore` 后，同一会话的每次写入都通过 `AppendEvents` 追加到日志末尾。按追加顺序读，可以看到会话依次发生过什么。要得到某个位置可用于下一轮的历史消息，还要应用回滚、替换及其他消息变更。例如，一条较早的消息即使已从模型上下文删除，仍能在会话日志里查到。

## 一条事件记录了什么

`adk.SessionEventStore[M]` 按 `SessionID` 隔离日志。写入时传入会话 ID 和一批有序的 `SessionEvent[M]`，读取时仍然指定同一个会话。包 `adk` 定义事件和 Runner 的重放规则，子包 `adk/session` 提供用于本地实验的内存和文件存储，业务也可以实现同一接口。[接口与事件类型](session.go) · [InMemoryStore](session/in_memory_store.go) · [FileStore](session/file_store.go)

| 字段 | 在日志中的含义 |
| --- | --- |
| `SessionID`、`EventID` | 会话 ID 作为 Store 方法参数传入。两者合起来定位唯一记录，例如“会话 S 的那条 idle”。`EventID` 是不透明字符串，默认由 Runner 分配 UUID，也可以通过 `SessionConfig.EventIDGenerator` 生成。 |
| `TurnID` | 关联同一逻辑回合的事件，可以串起一次中断前后的两段执行。旧记录或回合外的事件可以没有它。 |
| `Kind` 和对应的 payload | 每条事件有一种语义。例如 `message` 携带一条消息，`rollback` 携带两个事件 ID，`session.status_idle` 携带生命周期状态和结束原因。 |
| `MessageID` | 标识 payload 中的一条逻辑消息。更新已有消息会另写一条有新 `EventID` 的事件，通过 `MessageID` 指回被修改的消息。 |
| `Timestamp`、`Extra` | 时间戳表示事件发生时间，回放顺序由追加位置决定。业务可在事件的 `Extra` 里携带元数据，例如请求编号。ADK 重建消息时忽略这些元数据。 |

同一条日志有多种读法。展示执行过程可以读取 `session.status_*`、`span.model_request_*`、`span.tool_call_*`、`interrupt`、`cancel`、`session.error` 和业务扩展 `x.*`。构建模型输入则沿 rollback 指定的历史，只把消息及其变更应用到消息列表。一次失败请求的 span 因而可以出现在历史详情中，而它的错误信息不会自动成为下一轮的消息。

## 一条时间线上的位置

下表用 `kind 简写-追加序号` 表示示例中的 `EventID`。例如 `msg_replace-07` 是第 7 条事件，实际 kind 为 `messages_replaced`。编号只用于阅读示例。消息列表里的 `m1`、`m2` 等是 `MessageID`，`m2′` 表示内容已更新，ID 仍为 `m2`。

| EventID | 追加的事件 | 到这个位置时 |
| --- | --- | --- |
| `running-01` | `session.status_running` | 第一个回合开始，`TurnID=running-01` |
| `msg-02` | `message(m1)` | 消息为 `[m1]` |
| `msg-03` | `message(m2)` | 消息为 `[m1, m2]` |
| `idle-04` | `session.status_idle(end_turn)` | 第一个可回滚的提交点，消息已在此前写入日志 |
| `running-05` | `session.status_running` | 第二个回合开始 |
| `msg-06` | `message(m3)` | 消息为 `[m1, m2, m3]`，最近的提交点仍是 `idle-04` |
| `msg_replace-07` | `messages_replaced([s, m2, m3])` | 这次全量替换保留了 `m2`、`m3`，移除了 `m1`。重放可以从这里开始 |
| `msg_update-08` | `message_updated(m2, 新内容)` | 消息为 `[s, m2′, m3]`。`m2` 首次出现于 `msg-03`，经过 `msg_replace-07` 仍然可见 |
| `msg_insert-09` | `message_inserted(x, BeforeMessageID=m3)` | 消息为 `[s, m2′, x, m3]` |
| `idle-10` | `session.status_idle(end_turn)` | 第二个提交点，重建它的消息从 `msg_replace-07` 读起即可 |
| `rollback-11` | `rollback(FromEventID=idle-10, ToEventID=idle-04)` | 会话回到 `idle-04`，消息恢复为 `[m1, m2]`。`msg_replace-07` 和 `idle-10` 仍在日志中 |
| `running-12` | `session.status_running` | 从 `idle-04` 开始新回合 |
| `msg-13` | `message(m4)` | 消息为 `[m1, m2, m4]` |
| `idle-14` | `session.status_idle(end_turn)` | 第三个提交点，消息为 `[m1, m2, m4]` |
| `rollback-15` | `rollback(FromEventID=idle-14, ToEventID=idle-10)` | 恢复 `idle-10` 当时的 `[s, m2′, x, m3]`。若此刻再回到 `idle-04`，下一条 rollback 的起点是 `idle-10` |

## 回合、提交点和回滚

一次新的 `Run` 或 `Query` 先重建历史消息，再调用一次 `AppendEvents`，按顺序写入 `session.status_running` 和本次所有输入消息。写入成功后才执行 agent。随后可以陆续追加模型消息、工具结果、消息变更和观测事件。执行收尾写入 `session.status_idle`，其 `StopReason` 说明结束原因。

如果存储在返回错误前只写入了批次的前几条事件，后续恢复会根据实际落盘的事件校验 checkpoint。

- **执行的起点和终点**是一对 `running` 和 `idle`。新回合以开头的 running 事件 ID 作为 `TurnID`。中断后 `Resume` 会产生新的 running，并沿用 checkpoint 中的回合 ID，所以一个逻辑回合可以跨越多段执行。
- **提交点（commit）**是 `idle` 且 `StopReason.Type == end_turn` 的那个位置，例如 `idle-04` 和 `idle-10`。日志里没有单独的 commit-start 事件。回合里的消息在到达提交点以前就可以持久化。例如在 `msg-06` 处重建，已经能读到 `m3`。中断或失败前已写入、仍然有效的消息也会进入下一次 Run 的历史。中断、取消或失败产生的 idle 记录执行结局，只有正常结束的 idle 可以作为回滚目标。
- **回滚的起点和终点**由新追加的 `rollback` 事件描述。`FromEventID` 是回滚前会话所在的最近正常提交点，`ToEventID` 是要恢复到的正常提交点，两者都引用同一 session 的 idle 事件。业务可以指定这条 rollback 之前已存在的任一正常提交点作为 To，包括曾被 `rollback-11` 跳过的 `idle-10`。Runner 确定 From，重建时沿 To 查历史。From 在持久化 JSON 中对应的键是 `from_event_id`。
- **Rollback 与 fork** 的区别在于会话身份。Rollback 保留原 `SessionID`，把目标之后的新执行继续追加在同一份日志末尾。Fork 的语义是从历史位置派生另一个可独立推进的 session。调用 `RollbackSession` 只操作传入的会话。

示例中，`(idle-04, idle-10]` 表示第二个回合追加的记录。`running-05` 是这次执行的起点，`msg_replace-07` 是重建 `idle-10` 消息的起点。回滚的 From 和 To 则描述会话位置的一次跳转，历史记录始终留在原处。

追加 `rollback-15` 后，日志末尾是 `rollback-15`，最近写入的正常 idle 是 `idle-14`，会话当前的提交点是 `idle-10`，重建消息从 `msg_replace-07` 开始。[Runner 追加流程](runner.go) · [回滚实现和用例](session_test.go)

## 如何恢复下一轮的历史消息

1. 从日志尾部倒序分页。遇到生效的 rollback，就在更早记录中找到并校验它的 `ToEventID`，跳过中间整个区间，其中的其他 rollback 和替换也随之跳过。到达目标后，沿目标当时的历史继续倒序读取。
2. 遇到这条历史上最近一次生效的 `messages_replaced`，收下它并停止向前翻页。它的 payload 是**完整的有序消息列表**，新写入的替换事件必须至少包含一条消息。旧记录中显式保存的空列表在回放时仍会清空此前的历史。如果一直没有全量替换，就读到会话开头，从空列表开始。
3. 按正序应用选出的消息事件，得到可见消息列表，再拼上本次 Run 的输入。已识别的并行工具结果会按前一条 assistant 消息里的工具调用顺序排列。模型和工具 span、普通观测扩展、未完整的流前缀不会加入消息列表。

从 `rollback-11` 倒序读，先跳到 `idle-04`。这一步跳过了 `msg_replace-07`，所以要继续读到会话开头。从 `rollback-15` 倒序读，先跳到 `idle-10`，再向前读到 `msg_replace-07` 即可停止。这里的停止条件仅用于**重建消息**。执行 `RollbackSession` 时，查找目标可以越过 `msg_replace-07`。目标和起点均已确认就停止翻页。

`Run` 重建消息和 `RollbackSession` 查找回滚目标默认都最多读 300 页，每页最多 100 条符合筛选条件的事件。通过 `SessionConfig.MaxReplayLoadPages` 调整重建上限，通过 `WithRollbackSessionMaxLoadPages` 调整单次回滚的上限。在最后一页满足停止条件仍然成功。如果还需要下一页，返回 `ErrSessionScanLimitExceeded`。游标重复，或空页仍返回后续游标，也会报错。每次读取前都会检查 context 是否已取消。扫描失败时，`Run` 在调用 agent 前退出，`RollbackSession` 在追加回滚事件前退出。[倒序读取与正序重放](session.go)

## 消息更新、插入和删除可以引用谁

判断引用时，看**正序应用该事件前**的消息列表。可引用的消息来自最近一次生效的全量替换，以及此后追加或插入的消息。没有替换时，从会话开头累计。这个范围可以跨多个回合。一次新的全量替换会重置消息列表以及已删除消息留下的位置。

| 事件 | 重放规则 |
| --- | --- |
| `message` | 将消息追加到列表末尾。后续事件通过它的 `MessageID` 引用它。 |
| `messages_replaced` | 用 payload 的完整列表重新开始。旧消息只要以同一个 ID 出现在新列表里，仍可被引用。 |
| `message_updated` | 按 `MessageID` 找当前可见的目标，原位替换。目标不存在或已删除会报错。新消息未带 ID 时继承目标 ID，显式指定的 ID 必须与目标一致。 |
| `message_inserted` | `BeforeMessageID` 为空时追加到末尾，非空时插到该 ID 的位置之前。已删除消息在本次重放中保留的位置仍可作锚点。找不到锚点会报错。 |
| `messages_deleted` | 所列目标都必须可见。删除后不再输出这些消息，其位置保留作插入锚点，直到下一次全量替换。 |

示例中的 `msg_update-08` 能更新 `m2`，因为 `msg_replace-07` 的列表保留了它。同一位置若更新 `m1` 会报找不到目标。若在这次全量替换后删除 `m3`，仍可以在它原来的位置前插入 `x`。若再追加一次 `messages_replaced([s])`，这个锚点也就消失。如果在 `idle-04` 之前更新过 `m1`，回到 `idle-04` 时会用到这次更新。恢复 `idle-10` 时，`msg_replace-07` 已提供完整的起始列表。

## 典型应用场景

### 多轮对话与上下文压缩

同一会话收到新的用户提问时，调用 `runner.Query(ctx, question)`，或者用 `Run` 传入本次新增的多条消息。Runner 从日志恢复已有消息再拼入新的输入。调用方把整段历史再次传入会重复追加那些消息。例如表中从 `idle-04` 续聊时，只需传入 `m3`。摘要中间件可以用一次全量替换写入压缩结果，如 `msg_replace-07`。下一轮仍可更新这次替换保留的消息。[摘要中间件](middlewares/summarization/summarization.go)

### Reduction clear 如何更新旧工具消息

假设一次文件搜索返回了很长的文本。日志中的 assistant 消息 `m_call` 发起工具调用 `call_1`，tool 消息 `m_result` 携带同一个 `call_1` 和完整搜索结果。后来又执行了一轮工具调用。下一次模型请求前，若 token 数达到 `MaxTokensForClear`，且只保留最近一轮工具调用，较早的搜索结果就可以清理。以下示例使用默认清理处理器和英文占位文本，未配置外部存储。两条更新按各自在这段示例中的追加顺序编号。

| EventID | 新的日志事件 | 更新后的消息 |
| --- | --- | --- |
| `msg_update-01` | `message_updated(MessageID=m_result)` | tool 消息的结果文本变为 `[Old tool result content cleared]`，仍关联 `call_1`。 |
| `msg_update-02` | `message_updated(MessageID=m_call)` | assistant 消息保留原调用 ID 和参数，并记上已清理标记，供后续 clear 识别。自定义 `ClearHandler` 也可以在这条更新中改写调用参数。 |

`call_1` 用于配对调用和结果。重放按两个 **MessageID** 在原位置各替换一条完整消息，因此模型看到的顺序依然是 assistant 调用在前、tool 结果在后。原始的两条 `message` 仍可在日志中查到。这次 clear 只追加局部更新，下一轮重建仍需从生效的 `messages_replaced` 或会话开头读起。若配置了 `ClearAtLeastTokens` 而本次节省量未达标，这两条更新都不会写入。[clear 的实现](middlewares/reduction/reduction.go) · [默认占位文本与清理阈值用例](middlewares/reduction/reduction_test.go)

### 人工审批后恢复工具执行

例如工具请求审批时，业务可从事件的 `Interrupt.Contexts` 取得各个 `InterruptID`，把审批结果放入 `ResumeParams.Targets`，用 `ResumeWithParams` 恢复对应位置。仅需继续执行时，也可以调用 `Resume`。

配置会话存储和 checkpoint 存储时，Runner 在中断或可恢复取消后，把落盘的 idle 事件 ID 记入 checkpoint。恢复时依次读取并校验 checkpoint、解码执行状态、追加 `x.resume.request_started`，再追加 `session.status_running` 并执行 agent。checkpoint 已过期会返回 `adk.ErrCheckpointStale`。执行状态损坏或缺少运行会话也会直接报错，此时日志和 checkpoint 都保持原状。

`IdleEventID` 在 checkpoint 写入时固定。校验只需倒序取日志里最近的一条相关事件，比对两个 ID。新的执行、消息变更和回滚等会推进版本，普通观测和 `x.resume.request_started` 不推进版本。例如已写入请求标记但写 running 失败，仍可重试同一个 checkpoint。只有 checkpoint 存储、没有会话日志时，按指定 ID 从 checkpoint 恢复。旧会话 checkpoint 只要记录了 `SessionID`，就会校验归属。缺少 `IdleEventID` 时跳过版本查询。更早的格式若两者都没有，可直接恢复。带有 `IdleEventID` 的 checkpoint 必须同时标明所属会话。[Runner 的恢复流程](runner.go)

TurnLoop 会保留尚未消费的 Runner checkpoint 和待处理输入。若 running 写入结果未知，下次 Resume 会用同一 IdleEventID 再做版本校验。同一会话的版本确认已推进后，如果本次恢复失败，TurnLoop 会返回错误。有新的中断快照时保存新快照，否则保留后续回合的待处理输入，下次启动通过 GenInput 和 Run 从日志接续。模型调用前可以配置 patchtoolcalls 中间件给缺失的工具结果补占位消息。 [TurnLoop 的快照处理](turn_loop_checkpoint.go) · [patchtoolcalls](middlewares/patchtoolcalls/patchtoolcalls.go)

### 回到旧回答并重新提问

“回到这轮回答”按钮可以保存该回合的 `idle(end_turn).EventID`。例如用户在 `idle-14` 选择回到 `idle-10`，业务可以把目标和界面上看到的提交点一同传入。如果会话的提交点已变化，调用会返回 `adk.ErrSessionHeadChanged`。回滚成功后调用 `Query`，新问题便接在 `idle-10` 的历史后面。

```go
err := adk.RollbackSession[adk.Message](ctx, store, sessionID, "idle-10",
	adk.WithRollbackSessionFromEventID[adk.Message]("idle-14"),
	adk.WithRollbackSessionCheckPointStore[adk.Message](checkpoints),
)
```

如果 checkpoint 存储支持删除，Eino 会先追加回滚事件，再删除该会话默认键上的 checkpoint。收到 `*adk.RollbackCheckpointCleanupError` 时，会话已经回到了 `idle-10`。使用刚追加的回滚事件自身的 ID 和原来的两个存储，可以单独重试删除。

```go
var cleanupErr *adk.RollbackCheckpointCleanupError
if errors.As(err, &cleanupErr) {
	err = adk.RetryRollbackCheckpointCleanup[adk.Message](ctx, store, sessionID, cleanupErr.RollbackEventID, checkpoints)
}
```

只要这条回滚仍是最近一条影响 checkpoint 版本的事件，就可以重试清理。键已经不存在也算成功。若会话已经推进，清理返回 `adk.ErrSessionHeadChanged`，保留当前 checkpoint。即使后续回滚选中了同一目标，也按两次独立回滚处理。与 Run、Resume 一样，跨存储实例或跨进程访问时，调用方需要串行执行同一会话的操作。

回滚时只需确认起点和目标两个 idle。下一次 `Query` 为了重建消息，可能要继续读取更早的 `messages_replaced` 或日志开头，适用独立的回放页数上限。

### 展示执行时间线与排查慢工具

查看完整过程时，正序分页读取日志，包括后来被 rollback 越过的执行。下面的函数读取自上次处理位置以来的事件，调用方可以保存返回的 `EventID`，下次轮询从它之后继续。返回结果的 `Next` 只表示本次读取方向上是否还有下一页，读到末页时它为空。

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

历史面板若先展示最新一页，设置 `Reverse: true`，继续向前翻页时保持方向和 `Kinds` 过滤条件，把上一页的 `Next` 传给 `After`。两个方向都排除游标指向的那条事件。排查慢工具时，可以只选 `span.tool_call_start` 和 `span.tool_call_end`，用 `SpanID` 配对，用结束事件的 `StartedAt` 和 `EndedAt` 算耗时。并行执行的其他调用可能写在这对事件之间。[分页契约与 span 字段](session.go)

实时界面可以用 `adk.WithTimelineEvents()` 在 Runner 的迭代器中接收会话事件。流式消息先以 `SessionEventVariant.MessageStreamRef` 提供预留的 `EventID`，界面可以展示正在生成的内容，收到同 ID 的 `message` 或 `message_stream_incomplete` 时再更新这一项。空流可能只有引用，失败流已记录的前缀可供界面展示，下一轮重建会跳过它。[实时事件与流引用](session.go)
