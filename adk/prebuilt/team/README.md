# team

`team` is a prebuilt, multi-agent **team** orchestrator for eino's ADK. A single
*leader* agent coordinates a dynamic set of background *teammate* agents that
communicate through file-backed mailboxes and collaborate on a shared task list.

## What you get

`NewRunner` builds a leader `adk.ChatModelAgent` and automatically injects:

- **Team tools** — `Agent` (spawn a teammate), `SendMessage`
  (DM / broadcast / shutdown request+response), and `TeamDelete`.
- **A team-aware plantask middleware** — a shared task list (`TaskCreate`,
  `TaskList`, `TaskGet`, `TaskUpdate`) stored under a per-team directory so the
  leader and all teammates see the same tasks. Unlike the single-agent plantask
  "scratch pad", the team task list is **not** auto-cleared when everything is
  completed (tasks are removed explicitly via `status: "deleted"`).

Teammates are spawned in the background, stay addressable by name via
`SendMessage` across assistant turns, and are torn down explicitly (shutdown_request /
Runner shutdown).

## Minimal usage

```go
ctx := context.Background()

teamConf := &team.Config{
    Backend: myBackend, // see "Backend contract" below
    BaseDir: "/team-data",
}

agentConf := &adk.ChatModelAgentConfig{
    Name:        "leader",
    Description: "coordinates the team and delegates work to teammates",
    Model:       myChatModel, // a live model.Model
}

runner, err := team.NewRunner(ctx, &team.RunnerConfig{
    AgentConfig: agentConf,
    TeamConfig:  teamConf,

    // Decide which buffered items to process this turn.
    GenInput: func(_ context.Context, loop *adk.TurnLoop[team.TurnInput, adk.Message], items []team.TurnInput) (*adk.GenInputResult[team.TurnInput, adk.Message], error) {
        return &adk.GenInputResult[team.TurnInput, adk.Message]{Consumed: items}, nil
    },

    // Drain the agent event stream (required, even if you ignore the events).
    OnAgentEvents: func(_ context.Context, _ *adk.TurnContext[team.TurnInput, adk.Message], events *adk.AsyncIterator[*adk.AgentEvent]) error {
        for {
            if _, ok := events.Next(); !ok {
                return nil
            }
        }
    },
})
if err != nil { /* handle */ }

runner.Push(team.TurnInput{Messages: []string{"Build a small web service."}})
runner.Run(ctx)
exit := runner.Wait()
```

A complete, runnable version (with a stub model and an in-memory backend, so it
needs no API key) lives in `ExampleNewRunner` in `example_test.go`.

## Required callbacks

`NewRunner` returns an error if either callback is nil.

- **`GenInput`** — invoked each turn with all buffered `TurnInput` items; returns
  which to `Consumed` now versus keep for later. Consuming everything is a fine
  default. You can also call `loop.Stop()` from here to end the run.
- **`OnAgentEvents`** — **must** consume the agent event stream to completion;
  not draining it can stall the `TurnLoop`. Supply a no-op drain if you do not
  need the events.

## Routing input

`TurnInput.TargetAgent` selects the recipient loop. Leave it empty (or set it to
the leader name) to address the leader; the team layer routes teammate-bound
items internally.

## Backend contract

`Backend` (see `backend.go`) extends `plantask.Backend` with `Exists` and
`Mkdir`. Key points:

- **Atomic writes**: `Write` must replace a file atomically (temp file + rename).
  A torn `config.json` or inbox file permanently breaks routing/broadcast.
- **Single process**: the package serializes access with in-process locks only.
  Sharing one `BaseDir` across processes is unsupported unless the Backend adds
  cross-process coordination.

The package does not ship a concrete Backend; provide a filesystem-backed (or
other persistent) implementation. The tests use an in-memory backend.

## Lifecycle notes

- `Run(ctx)` is non-blocking; the `ctx` you pass is captured as the team runtime
  root context, so background teammates survive across turns and are cancelled
  when it is cancelled.
- `Wait()` / `WaitContext(ctx)` block until the loop exits, then perform teammate
  shutdown and leader-mailbox cleanup. Use `WaitContext` to bound teardown with
  an external deadline.
- `Config` carries lazily-initialized shared state and a `sync.Once`; **pass it
  by pointer and do not copy it** after handing it to `NewRunner`.
