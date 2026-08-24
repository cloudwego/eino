# Comprehensive Review Summary: task-ownership-runtime

## Overview

- Base: `alpha/10` (`9ce88298`)
- Branch: `feat/task-ownership-runtime`
- Review iterations: design 3, attack 3, test audit 3
- Scope: hard migration from `adk/backgroundtask` to `adk/task`

## Final Architecture

- `task.Handle` is an owner-neutral operation handle, not a lifecycle record.
- Foreground lifecycle belongs only to the parent runtime.
- Background lifecycle belongs only to `background.Manager`.
- Both owners share a durable mailbox and stable TaskID.
- `ChildSessionID` is persistent conversation identity; TaskID is one finite execution.
- Background providers implement one atomic `LifecycleStore` contract covering
  lifecycle, mailbox, handoff, input boundaries, and parent notification writes.
- Sub-agent execution has one TurnLoop payload and one Controller path.
- Queue and Preempt are durable input intents; the active TurnLoop decides safe points.
- Nested tasks route to their direct `ParentTaskID`; only roots emit session events.

## Design Findings Resolved

| # | Finding | Resolution |
|---|---|---|
| 1 | Foreground records created a second lifecycle owner. | Foreground state stays parent-owned; background records appear only at launch or atomic handoff. |
| 2 | `PendingResume` duplicated mailbox input durability. | Removed `PendingResume`, `Manager.Resume`, and the sub-agent v4 path; all background input uses TaskMailbox. |
| 3 | Store capabilities could be split across non-atomic providers. | Added mandatory `LifecycleStore` combining lifecycle, mailbox, adoption, input transitions, and parent notification writes. |
| 4 | Foreground terminal candidate could be replayed as a new turn after a crash before seal. | Replay now restores the write-ahead candidate and seals if idle; racing input resumes execution. |
| 5 | Foreground failure/cancel could leave the child session permanently active. | Added durable failure replay and `Abandon` to seal failed/canceled foreground mailboxes. |
| 6 | Cancel racing with foreground handoff could miss the new background owner. | Post-adoption cancellation check durably cancels the transferred task. |
| 7 | Input racing with an idle transition could leave a Pending task without a worker. | Manager redispatches tasks returned to Pending by atomic input-boundary transitions. |
| 8 | Managed-tool resume had a cursor/checkpoint crash gap. | Resume establishment commits mailbox cursor and recovery checkpoint atomically. |
| 9 | Nested task root scope drifted to the immediate child session. | RootSessionID now propagates from `ExecutionContext`; direct parent and root identities remain distinct. |
| 10 | Nested notification replay could report success after a failed parent append. | Replay metadata is committed only after parent mailbox append; delivery intent participates in conflict detection. |
| 11 | Handoff reused `task_created`. | Added `task_backgrounded` so creation and ownership transfer are distinguishable. |
| 12 | Exposing `Handle.Mode()` implied ownership was stable for the lifetime of a handle. | Removed `Mode()`; ownership can transfer without changing the handle or TaskID. |

## API Surface Simplification

- `ExecutorRegistry` is private to `background.Manager`; local tasks, managed
  tools, and sub-agents register executors through the manager.
- `ExecutionResult` has one `ExecutionAction` discriminator instead of
  independently configurable status and directive fields.
- Task input uses `SendInput`; sub-agent conversation reuse uses
  `Continue` with explicit `IfIdle` start options.
- `Input` is a caller-owned send intent; `InputRecord` adds persisted routing,
  sequence, and timestamp fields.
- Sub-agent `Handle` keeps identity private and is restored through
  `Controller.Handle`, so serialized state stores IDs rather than inert handles.
- Initial placement uses `StartMode`; active authority uses
  `Owner` plus `Generation`. Background specs name their root scope explicitly
  as `RootSessionID`.
- Deep Agent derives its shared Manager from the durable Sub-agent Controller
  when omitted and rejects conflicting Manager instances.
- Business terminal states are returned through one `OutcomeStatus` field.
- Lifecycle transitions are exposed only through mailbox-aware atomic store
  operations; the old split transition and capability interfaces are removed.

## Attack Review

- All repository `TestAttack_*` tests pass.
- Added focused attacks for inactive foreground notification replay, terminal
  write-ahead recovery, cancel/handoff races, ambiguous resume input, nested
  root routing, parent notification replay, terminal sealing, and redispatch.
- Managed-tool tests cover invalid wake input, rejected resume, accepted
  multi-step resume, and worker handoff after resume commit.

## Test Audit

- Full ADK coverage with `-coverpkg=./adk/...`: **85.6%**.
- `adk/task/background`: **76.8%** package coverage.
- `adk/task/subagent`: **74.0%** package coverage.
- A local statement-block approximation reports **76.9% changed-code
  coverage**. This is below the plan's 85% diff target; no repository-supported
  diff coverage tool is present, so this number is not a Codecov result.
- Critical orchestration functions (`Start`, `runActivation`, mailbox
  registration/input paths) are above the 70% function floor.

## Verification

- `go test ./...`
- `go test -race` across task runtime, middleware, and Deep Agent packages
- `go test ./... -run 'TestAttack_' -count=1`
- `go vet ./...`
- `golangci-lint run ./adk/task/... ./adk/middlewares/task/... ./adk/middlewares/subagent/... ./adk/prebuilt/deep/...`
- `git diff --check`
- Package migration checks confirm old directories and old sub-agent APIs are removed.

## Remaining Item

- The strict 85% changed-code coverage target is not demonstrated. Overall ADK
  coverage exceeds 85%, and critical new packages/functions exceed the 70%
  floor, but the conservative local diff estimate is 76.9%.
