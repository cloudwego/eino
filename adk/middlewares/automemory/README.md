# AutoMemory and single-run Dream

AutoMemory reads and extracts memories around normal agent runs. Dream performs
one explicit consolidation of the same memory files: it merges topics, corrects
stale information, and maintains the index. A Dream invocation may make multiple
model and tool calls, but it is one synchronous task.

Dream has two entry points in the `automemory` package:

- `memory.RunDream(ctx, request)` uses the environment already bound by `New`.
- `automemory.RunDream(ctx, config, request)` runs independently of AutoMemory.

Neither entry point schedules tasks or runs automatically after the main agent.

## Use Dream with AutoMemory

The examples assume the application has initialized `ctx`, `backend`, and `cm`,
where `cm` implements `model.BaseModel[*schema.Message]`.

```go
memory, err := automemory.New(ctx, &automemory.Config[*schema.Message]{
    MemoryDirectory: "./memory",
    MemoryBackend:   backend,
    Model:           cm,
    Dream:           &automemory.DreamOptions[*schema.Message]{},
})
if err != nil {
    return err
}

agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
    Name:     "assistant",
    Model:    cm,
    Handlers: []adk.ChatModelAgentMiddleware{memory},
})
if err != nil {
    return err
}
// Run the main agent through an ADK Runner as usual.
_ = agent

// Explicitly run consolidation at a time chosen by the application.
if err := memory.RunDream(ctx, nil); err != nil {
    return err
}

// A method value can be handed to application-owned task scheduling.
runDream := memory.RunDream
_ = runDream
```

`New` returns `*Middleware[M]`, which directly implements the ADK middleware
interface. Keep this concrete value to access `RunDream`; assigning it to the
interface hides that method.

`Config.Dream == nil` leaves Dream unconfigured. Calling the bound method then
returns `ErrDreamNotConfigured`. A non-nil `Dream` explicitly enables it and
validates its options during `New`.

The bound method reuses the Backend, resolved directory, and
`Read.Index.FileName`. It does not resolve relative paths again on each run.
`Dream.Model` overrides `Config.Model`; if absent, only `Config.Model` is used.
`Read.Model` and `Write.Model` are not Dream fallbacks. Configuration structs
and the handler slice are copied during initialization; the model, Backend,
Store, callbacks, and individual handlers remain shared dependencies.

## Run independently

```go
err := automemory.RunDream(ctx, &automemory.DreamConfig[*schema.Message]{
    MemoryDirectory: "./memory",
    MemoryBackend:   backend,
    IndexFileName:   "MEMORY.md",
    Options: &automemory.DreamOptions[*schema.Message]{
        Model: cm,
        OnEvent: func(ctx context.Context, event *adk.AgentEvent) error {
            // Observe materialized messages and progress.
            return nil
        },
    },
}, nil)
```

The standalone entry point requires a directory, Backend, Options, and Model.
It resolves relative directories when called. `IndexFileName` defaults to
`MEMORY.md` and must be a file name within the memory directory.

Both entry points support `*schema.Message` and `*schema.AgenticMessage`.
`MaxIterations` defaults to 12 model calls; a negative value is invalid.
Each invocation creates a fresh consolidation agent and a fresh nonstreaming
ADK Runner. It does not alter AutoMemory extraction progress.

## Search selected session histories

The session store is optional. Provide it once in `DreamOptions` and select the
search scope on each request:

```go
memory, err := automemory.New(ctx, &automemory.Config[*schema.Message]{
    MemoryDirectory: "./memory",
    MemoryBackend:   backend,
    Model:           cm,
    Dream: &automemory.DreamOptions[*schema.Message]{
        SessionStore: sessionStore,
    },
})
if err != nil {
    return err
}
err = memory.RunDream(ctx, &automemory.DreamRequest{
    SessionIDs: []string{"session-a", "session-b"},
})
```

IDs are trimmed and deduplicated while preserving order. Blank IDs are rejected.
A nonempty list requires a SessionStore. A nil or empty request only consolidates
files and exposes no session-history search tool, even if a store is configured.
Dream does not enumerate sessions, exclude a current session, or infer a scope
from a parent Runner.

The historical SessionStore is a read dependency for `grep_session_history`.
It is not attached to Dream's Runner: consolidation events are neither appended
to those sessions nor replayed from them.

Search reads raw message events rather than the final replayed conversation, so
replaced or rolled-back messages may still appear. Results default to 50 matching
lines, searched in request order; they have no global time ordering across sessions.
The result limit does not cap how many events are scanned when a term is absent.
Use a narrow session scope and a context timeout to control query cost.

## Completion, errors, and extensions

`RunDream` blocks until its event iterator is drained. Pass a context with the
application's timeout or cancellation policy. On the first execution, stream,
interrupt, or observer error, Dream cancels its execution and drains the
remaining events, closing unused message streams. Models, tools, and handlers
must honor context cancellation; a callback runs synchronously and must return
for execution to finish.

`OnEvent` observes successful events in order. Execution errors are checked
before the callback, and returning nil cannot erase an execution failure.
Returning an error from the callback cancels the task and returns that error.
Streams injected by handlers are materialized before observation. An interrupt
returns `ErrDreamInterrupted`; this API does not save checkpoints or resume.

Additional `Handlers` apply only to the internal consolidation agent, after the
built-in memory filesystem handler. Reduction and summarization can be included
here. Do not add AutoMemory itself: that would reintroduce injection/extraction
side effects within consolidation. Custom handlers are trusted extensions and
can change tool behavior beyond the built-in memory filesystem boundaries.

## Application-owned scheduling and write coordination

Use cron, a task service, or an application-owned timer to decide when to call
`RunDream`. The application chooses session IDs, retry rules, and whether an
overlapping scheduled run is skipped or queued.

Serialize consolidation tasks that share the same memory storage. For multiple
instances, use a lock key that includes the tenant, actual Backend namespace,
and directory. Coordinate with main-agent writes and outstanding extraction
tasks as well. An extraction Coordinator lock is session-scoped and does not
lock the whole memory directory.

LocalBackend's temporary-file rename and same-path Write/Edit serialization
protect individual file operations. They do not protect the whole
`read → model reasoning → write` sequence from another task's updates.
Dream provides no directory transaction, global writer lock, or extraction
flush barrier. Concurrent writers need application coordination or a Backend
that detects conflicting versions.

Failure or cancellation may leave partial changes: updating a topic and its
index is not atomic, and completed writes are not rolled back. A retry can
repair inconsistencies but is not an exactly-once or strictly idempotent task.

## Alpha/10 migration

The `automemory/dream` subpackage has been removed. Replace `dream.Run` with
the package-level `automemory.RunDream`, or configure `Config.Dream` and call
the method returned by `automemory.New`.

Remove `dream.New` from main-agent handlers. `ScheduleConfig`, scheduling
Store/LocalStore, touch tracking, interval/threshold fields, run locks,
`SessionID`, `OnError`, and `HandleIterator` are no longer Dream APIs.
Pass explicit `DreamRequest.SessionIDs`, observe with `OnEvent`, and handle
the returned error. The application now owns scheduling and task coordination.
