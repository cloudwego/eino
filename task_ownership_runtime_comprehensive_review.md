# Comprehensive Review Summary: Task Ownership Runtime

## Overview

- Base: `alpha/10` (`9ce88298`)
- Branch: `feat/task-ownership-runtime`
- Scope: unified foreground/background Task runtime, durable mailbox,
  Sub-agent continuation, nested Task authority, managed tools, and middleware
- Review iterations: design 1, attack 1, test audit 2
- Result: approved for PR submission

## Stage 1: Design Review

### Final Architecture

- `TaskID` identifies one finite execution.
- `ChildSessionID` identifies persistent Sub-agent conversation history.
- `task.Handle` is the owner-neutral operation surface.
- `background.TaskSnapshot` is the durable background lifecycle record.
- Foreground execution is parent-owned; background execution is Manager-owned.
- Both owners use the same durable Mailbox and retain the same TaskID across
  handoff.
- Nested Task creation is authorized by `ExecutionContext`; the Store derives
  `ParentTaskID` and `RootSessionID` from the authoritative parent Mailbox.
- `ExecutionAction` and `OutcomeStatus` are the single discriminators for
  executor results and caller-visible outcomes.

### Findings

| # | Dimension | Finding | Verdict | Resolution |
|---|---|---|---|---|
| 1 | Concept coherence | Task identity was coupled to background lifecycle. | Fix | Split `task.Handle` from `background.TaskSnapshot`. |
| 2 | API usability | Input send intent and persisted record were one type. | Fix | Split `Input` and `InputRecord`. |
| 3 | Minimum surface | Executor registry was threaded through every integration. | Fix | Registry is private to `Manager`; integrations self-register. |
| 4 | Valid states | Executor result used independent status/directive fields. | Fix | Added one `ExecutionAction` discriminator. |
| 5 | Valid states | Managed-tool outcome accepted background-only states. | Fix | `tool.Outcome` now uses `task.OutcomeStatus`. |
| 6 | Ownership | Parent/root scope could be supplied inconsistently. | Fix | Nested mailbox registration accepts only `ParentExecution`; Store derives scope. |
| 7 | Naming | Start selection and active ownership both used `Mode`. | Fix | Split `StartMode` from `Owner`; use `Generation` consistently. |
| 8 | API safety | Serialized Sub-agent handles lost operational closures. | Fix | Handle identity is private and restored through `Controller.Handle`. |
| 9 | Configuration | Deep Agent could receive conflicting Manager instances. | Fix | Derive Manager from Controller when omitted and reject conflicts. |
| 10 | Naming | `Complete`, `Wait`, `LifecycleHook`, and `EventToInput` were too broad. | Fix | Renamed to explicit completion, cancellation, and input policy terms. |
| 11 | Public surface | Manager exposes low-level mailbox forwarding methods. | Won't Fix | Required by sibling runtime packages; documented as advanced runtime API. |
| 12 | Complexity | Sub-agent recovery remains concentrated in `turn_loop.go`. | Defer | Complexity follows one state machine; splitting now would spread invariants. |

### Final Scorecard

| Dimension | Rating | Notes |
|---|---:|---|
| Concept coherence | 5/5 | Task, session, mailbox, owner, and snapshot are distinct. |
| API usability | 4/5 | Main paths are direct; storage SPI remains intentionally detailed. |
| Minimum API surface | 4/5 | Registries and duplicate state transitions were removed. |
| Compatibility | 4/5 | Deliberate hard migration; no partial compatibility layer. |
| Layering | 5/5 | Core communication, background lifecycle, and domain runtimes are separated. |
| Cohesion | 5/5 | Changes serve one ownership and durability model. |
| Elegance | 4/5 | Atomicity is explicit without a second lifecycle model. |
| Naming | 5/5 | Owner, StartMode, Generation, RootSessionID, and policy names align. |
| Readability | 4/5 | Public model is clear; recovery orchestration remains dense. |
| Duplication | 4/5 | Duplicate registries, outcomes, inputs, and parent scope are removed. |
| Public API docs | 5/5 | `adk/task/README.md` documents concepts, APIs, examples, and SPI. |
| Internal comments | 4/5 | Non-obvious transition and recovery boundaries are documented. |

## Stage 2: Attack Review

### Result

- Repository attack tests: 137
- Final result: all pass
- No new confirmed correctness bug was found.

### Verified Attack Surfaces

| Area | Evidence |
|---|---|
| Input races | Completion, waiting, and suspension transitions preserve late input. |
| Parent authority | Stale generation/attempt cannot create nested Task state. |
| Parent scope | Nested Mailbox scope is derived; conflicting explicit root scope is rejected. |
| Handoff | Foreground adoption retains TaskID and does not lose racing input. |
| Replay | Input, progress, and notification EventID conflicts are detected. |
| Cancellation | Cancel/handoff races preserve durable cancellation intent. |
| Recovery | Sub-agent and managed-tool checkpoints survive worker handoff. |
| Preemption | Durable preempt intent reaches TurnLoop safe-point selection. |

## Stage 3: Test Audit

### Findings

| # | Category | Finding | Verdict | Resolution |
|---|---|---|---|---|
| 1 | Coverage gap | `background.Handle.Wait` did not directly cover failed, canceled, and context-canceled paths. | Fix | Added a table-driven terminal-state test and wait cancellation case. |
| 2 | Duplication | Similar foreground/background tests exercise different ownership paths. | Won't Fix | Keep as intentional parity pairs. |
| 3 | Assertions | Timing assertions use lower bounds without strict upper bounds. | Won't Fix | They prove timeout activation without coupling tests to scheduler latency. |
| 4 | Boilerplate | Runtime fixtures repeat setup in several packages. | Defer | Cross-package helpers would hide domain-specific setup and increase coupling. |

### Coverage

- ADK aggregate coverage with `-coverpkg=./adk/...`: 85.8%.
- `adk/task/background` package coverage: 79.6%.
- `background.Handle.Wait`: 92.3%.
- Low-coverage trivial forwarding/context methods are excluded from the
  important-branch floor.

## Verification

- `go test ./...`
- `go test ./... -run 'TestAttack_' -count=1`
- `go test -race` across Task runtime, middleware, filesystem, and Deep Agent
- `go vet ./...`
- `golangci-lint` across changed Task and middleware packages
- `git diff --check`

## Remaining Items

No blocking design, correctness, or test-quality findings remain.
