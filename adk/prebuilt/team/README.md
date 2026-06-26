# team

`team` is a prebuilt, multi-agent **team** orchestrator for eino's ADK. A single
*leader* agent coordinates a dynamic set of background *teammate* agents that
communicate through file-backed mailboxes and collaborate on a shared task list.

Teammates have their own context windows, message each other directly, and 
self-coordinate through a shared task list — as opposed to subagents that only 
report a result back to a single caller.

## What you get

`NewRunner` builds a leader `adk.ChatModelAgent`, **creates the team up front**
(directory layout, `config.json`, and the leader's inbox), and automatically
injects:

- **Team tools** — `Agent` (spawn a teammate) and `SendMessage`
  (DM / broadcast / shutdown_request + shutdown_response). There is no
  `TeamCreate` / `TeamDelete` tool: the team's lifecycle is tied to the Runner
  (created in `NewRunner`, removed on `Wait`), not driven by the model.
- **A team-aware plantask middleware** — a shared task list (`TaskCreate`,
  `TaskList`, `TaskGet`, `TaskUpdate`) stored under a per-team directory so the
  leader and all teammates see the same tasks. Unlike the single-agent plantask
  "scratch pad", the team task list is **not** auto-cleared when everything is
  completed (tasks are removed explicitly via `status: "deleted"`).

Teammates are spawned in the background, stay addressable by name via
`SendMessage` across assistant turns, and are torn down explicitly
(shutdown_request / Runner shutdown).

## Team lifecycle is automatic

The team is created and destroyed with the Runner — the model never manages it:

- **Create**: `NewRunner` writes the team directory, `config.json` (leader as the
  first member), and registers the leader's inbox. The team name comes from
  `Config.Name`, or is generated (`team-<uuid>`) when that is empty.
- **Destroy**: `Wait` / `WaitContext` shuts down teammates, stops the leader
  mailbox pump, and **removes the team's on-disk data** (config, inboxes, tasks).
  Set `Config.RetainDataOnExit = true` to keep that data after the run instead.

This means a clean exit deletes everything under `{BaseDir}/teams/{team}` and
`{BaseDir}/tasks/{team}`. If the process is killed (e.g. SIGINT) before `Wait`
returns, that cleanup never runs — see "Graceful shutdown" below.

## Teammate roles (`subagent_type`)

`RunnerConfig.TeammateRoles` declares reusable teammate roles. Each role has a
`Name` (the value the leader passes as the Agent tool's `subagent_type`), an
optional `Description` (rendered into the leader's instruction so it knows when to
pick the role — it is **not** added to the teammate's own context), and optional
`Instruction` / `Model` / `Tools` that are overlaid onto the leader's
`AgentConfig` when a teammate of that type is spawned.

```go
TeammateRoles: []team.TeammateRole{
    {Name: "geo-expert",  Description: "An experienced geography expert.", Instruction: "You are a geography expert."},
    {Name: "philosopher", Description: "A philosopher.",                   Instruction: "You are a philosopher."},
},
```

Key rules:

- **`subagent_type` is required and must match a declared role.** An empty or
  unknown value is rejected by the Agent tool; the error is returned to the model,
  which retries with a valid type on its next turn.
- **When `TeammateRoles` is empty**, the framework injects a single default
  `general-purpose` role (inheriting the leader's model and tools), so there is
  always exactly one valid `subagent_type`. Supplying your own roles replaces that
  default with the given set, treated as the exhaustive allowlist.
- A role's `Tools` only narrows the host-supplied business tools; `SendMessage`
  and the `Task*` tools are injected separately and stay available regardless.

## Minimal usage

```go
ctx := context.Background()

teamConf := &team.Config{
    Backend: myBackend, // see "Backend contract" below
    BaseDir: "/team-data",
    // Name:             "my-team", // optional; generated if empty
    // RetainDataOnExit: true,      // optional; default removes data on exit
}

agentConf := &adk.ChatModelAgentConfig{
    Name:        "team-lead",
    Description: "coordinates the team and delegates work to teammates",
    Model:       myChatModel, // a live model.Model
}

runner, err := team.NewRunner(ctx, &team.RunnerConfig{
    AgentConfig: agentConf,
    TeamConfig:  teamConf,

    // Optional reusable teammate roles selected via subagent_type.
    TeammateRoles: []team.TeammateRole{
        {Name: "researcher", Description: "Researches a topic.", Instruction: "You are a researcher."},
    },

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

runner.Push(team.TurnInput{TargetAgent: team.LeaderAgentName, Messages: []string{"Build a small web service."}})
runner.Run(ctx)
exit := runner.Wait()
```

A complete, runnable version (with a stub model and an in-memory backend, so it
needs no API key) lives in `ExampleNewRunner` in `example_test.go`. A fuller,
multi-teammate program against a real model and a filesystem backend lives in
`demo/` (see "Demo" below).

## Required callbacks

`NewRunner` returns an error if either callback is nil.

- **`GenInput`** — invoked each turn with all buffered `TurnInput` items; returns
  which to `Consumed` now versus keep for later. It also builds the
  `adk.AgentInput` (the message history) the agent runs on, so **per-agent
  conversation history is the host's responsibility** — key it by
  `items[0].TargetAgent` (all items in one call share the same target). You can
  call `loop.Stop()` from here to end the run.
- **`OnAgentEvents`** — **must** consume the agent event stream to completion;
  not draining it can stall the `TurnLoop`. The events belong to the agent named
  by `tc.Consumed[0].TargetAgent`; append produced messages back into that
  agent's history here. Supply a no-op drain if you do not need the events.

## Routing input

`TurnInput.TargetAgent` selects the recipient loop. Leave it empty (or set it to
`team.LeaderAgentName`) to address the leader; the team layer routes
teammate-bound items internally. Teammate names must start with an ASCII letter
or digit and contain only letters, digits, `.`, `_`, `-` (no spaces or CJK
characters); `team-lead` is reserved.

## Graceful shutdown

Cleanup (teammate teardown + on-disk data removal) happens inside `Wait` /
`WaitContext`, **after** the `TurnLoop` stops. If nothing ever stops the loop,
`Wait` blocks forever and a `Ctrl+C` kill skips cleanup, leaving the team and
task directories on disk. Drive a clean exit by either calling `loop.Stop()` from
`GenInput` when the work is done, or wiring a signal handler to `runner.Stop()`:

```go
sigCh := make(chan os.Signal, 1)
signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
go func() { <-sigCh; runner.Stop() }()

runner.Run(ctx)
runner.Wait() // now returns on SIGINT, runs cleanup
```

## Backend contract

`Backend` (see `backend.go`) extends `plantask.Backend` with `Exists` and
`Mkdir`. Key points:

- **Atomic writes**: `Write` must replace a file atomically (temp file + rename).
  A torn `config.json` or inbox file permanently breaks routing/broadcast. The
  `demo/` `fileBackend` is a reference implementation of this.
- **Single process**: the package serializes access with in-process locks only.
  Sharing one `BaseDir` across processes is unsupported unless the Backend adds
  cross-process coordination.

The package does not ship a concrete Backend; provide a filesystem-backed (or
other persistent) implementation. The tests use an in-memory backend; `demo/`
uses a filesystem one.

## Lifecycle notes

- `Run(ctx)` is non-blocking; the `ctx` you pass is captured as the team runtime
  root context, so background teammates survive across turns and are cancelled
  when it is cancelled. The leader's mailbox pump is also started here.
- `Wait()` / `WaitContext(ctx)` block until the loop exits, then perform teammate
  shutdown, leader-mailbox cleanup, and team-data removal (unless
  `RetainDataOnExit`). Use `WaitContext` to bound teardown with an external
  deadline.
- `Config` carries lazily-initialized shared state and a `sync.Once`; **pass it
  by pointer and do not copy it** after handing it to `NewRunner`.

## Demo

`demo/` is a runnable program: a leader coordinates teammates to answer several
questions, then synthesizes the answers. It illustrates the parts a host must
supply around the framework:

- **A real model + filesystem `Backend`** — `NewChatModel` (Ark/OpenAI via env
  vars) and `fileBackend`, an atomic-write reference Backend.
- **Per-agent history in `GenInput` / `OnAgentEvents`** — an `agentHistory` map
  keyed by `TargetAgent`; each agent runs on its own message history rather than a
  shared transcript.
- **`TeammateRoles`** — declares `geo-expert` and `philosopher`, which the leader
  selects via `subagent_type`.
- **Graceful shutdown** — a SIGINT/SIGTERM handler calls `runner.Stop()` so `Wait`
  returns and the team's on-disk data is cleaned up (the default, since
  `RetainDataOnExit` is unset).
- **A tool-call middleware** (`toolWrapMiddleware`) that turns a tool error into a
  normal string result, so a rejected call (e.g. an invalid `subagent_type` or
  teammate name) is fed back to the model to retry instead of aborting the turn.
