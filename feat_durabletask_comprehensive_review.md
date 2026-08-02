# Comprehensive Review Summary: feat/durabletask

## Pre-Flight

- **Branch**: `feat/durabletask`
- **Target branch**: `alpha/10`
- **PR scope base**: `alpha/10...HEAD`
- **Merge base**: `12c3d1b9ce23e8f8678b49e938dd5f8682ba7034`
- **Initial scope**: 38 files changed, 7350 insertions, 2532 deletions
- **Correction note**: The initial review pass used `origin/main...HEAD`; after target-branch clarification, scope was corrected to `alpha/10...HEAD`.
- **Baseline command**: `go test ./...`
- **Baseline status**: passed
- **Baseline result**: `go test ./...` passed. Slowest observed package: `github.com/cloudwego/eino/adk` at 48.838s.

## Stage 1: Design Review

### Iteration 1 Review

#### Summary Scorecard

| Dimension | Rating | Notes |
|---|---:|---|
| Concept Coherence | 4/5 | `backgroundtask.Manager`, `Store`, `Executor`, and Runner environment form a coherent lifecycle model. Durable `Submit`/`Execute` and process-local `Run` share coordination but remain distinct through `Spec.ExecutorKey`. |
| API Usability and Intuitiveness | 4/5 | Public entry points are explicit: `AllocateTaskID`, `Submit`, `Execute`, `RunSubmitted`, `ResumeTask`, and control tools. `RunInput.ForegroundTimeoutMs` nil vs `<=0` is subtle but documented. |
| Minimum API Surface | 4/5 | Store/notification/session activation SPIs are broad but justified by durable multi-process requirements. `State` aliases intentionally preserve compatibility. |
| Backward Compatibility | 4/5 | Existing runner and turn-loop APIs are extended, not removed. Alpha-era public additions are acceptable in this PR scope. |
| Module Separation and Layering | 4/5 | Durable lifecycle stays under `adk/backgroundtask`; domain executor lives in `adk/backgroundtask/subagent`; middleware only injects tools and prompt. Runner environment avoids executor importing app-specific state. |
| Cohesion vs. Tension | 4/5 | `Manager` is large but single-purpose: lifecycle coordination. Store remains the ownership/state boundary. |
| Elegance vs. Complexity | 4/5 | Complexity mostly follows from durable execution, foreground projection, lease, and checkpointing. No accidental cross-layer dependency found in the reviewed core. |
| Naming | 4/5 | New public names are mostly precise. `RunInput.Type` was avoided in durable `Spec` in favor of `Kind`/`ExecutorKey`, consistent with current direction. |
| Readability | 4/5 | Hardest areas are `Manager.RunStream` projection, `TypedRunner` session persistence loop, and `TurnLoop` resume bookkeeping. They have useful local comments. |
| Duplication | 4/5 | `execute` and `executeStarted` are parallel but serve durable-vs-local start paths; no clear low-risk consolidation. |
| Public API Documentation | 4/5 | Most public types/functions added here have comments. Edge cases on foreground timeout and streaming windows are well documented. |
| Internal Comments | 4/5 | Non-obvious checkpoint ordering and stream splitting have comments at the critical points. |

#### Public Name Assessment

| Name | Assessment |
|---|---|
| `backgroundtask.Status`, `State` aliases, status constants | Clear; aliases preserve source compatibility. |
| `backgroundtask.Task`, `Spec`, `Notification`, request/result types | Verbose but domain-appropriate. |
| `backgroundtask.Manager`, `Config`, `RunInput`, `NoticeInfo` | Clear. `ForegroundTimeoutMs` nil/zero behavior is documented. |
| `backgroundtask.Executor`, `ExecutorRegistry`, `ExecutionRuntime`, `ExecutionResult` | Good separation between durable intent and worker execution. |
| `backgroundtask.Store`, `NotificationOutbox` | Appropriate provider-facing SPI names. |
| `sessionnotify.Sink`, `MemoryInbox`, `TurnLoopActivator` | Clear but provisional; package name scopes them well. |
| `subagent.Executor`, `Submit`, `SubmitRequest`, `ResumeMode` | Clear domain executor API. |
| `middlewares/backgroundtask.Config`, `ToolConfig`, `NewTyped` | Consistent with middleware package conventions. |
| `adk.SessionEventStore`, `SessionEvent`, `SessionConfig`, `RollbackSession` | Large public model, but names match event-log semantics. |
| `adk.TypedRunnerEnvironment`, `ExecuteBackgroundTask` | Clear bridge between Runner and durable execution. |

#### Top Actionable Recommendations

1. **Attack-test lifecycle race/duplication boundaries**: no design blocker, but outbox ack/redelivery and explicit background stream startup have enough concurrency surface to deserve adversarial tests.
2. **Audit test quality on new durable task tests**: many tests are valuable but long; check for duplicated helper setup and weak assertions.
3. **Keep Store SPI provisional in docs/release notes**: already stated in package docs; no code change needed.

#### Validate & Counter-Argue

| Finding | Validation | Counter-argument | Verdict |
|---|---|---|---|
| `Manager` has a large public surface and two execution modes. | Re-read `manager.go`, `executor.go`, and middleware/subagent integration. It is large, but public APIs map to distinct durable lifecycle actions. | Splitting now would either duplicate state coordination or expose more intermediate concepts. Existing comments already mark SPIs provisional. | Won't Fix |
| `RunInput.ForegroundTimeoutMs` nil vs `<=0` is subtle. | The behavior is documented in the field and method comments. Tests cover per-run override/disable. | Adding a custom duration type or extra bool would expand API surface without clear correctness gain. | Won't Fix |
| `execute`/`executeStarted` duplicate terminal commit flow. | They share heartbeat, executeClaim, and commit logic but differ in claim creation path (`Start` vs pre-started local record). | Consolidation would likely obscure the Store ownership boundary; current duplication is small and testable. | Won't Fix |

### Iteration 1 Re-Review

No Stage 1 code changes were made. All 12 dimensions remain >= 4/5 with no unresolved blockers. Proceeding to Stage 2.

## Stage 2: Attack Review

### Iteration 1 Attack Tests

| # | Severity | Issue | Test Name | Initial Status |
|---|---|---|---|---|
| 1 | Critical | `sessionnotify.MemoryInbox` did not deep-copy `Notification.Target.Metadata`, allowing caller-owned maps and returned pending items to mutate inbox state. | `TestAttack_MemoryInboxDeepCopiesNotificationTargetMetadata` | Confirmed failing |

#### Validate & Counter-Argue

| Bug | Validation | Counter-argument | Verdict |
|---|---|---|---|
| `Notification.Target.Metadata` aliases through `MemoryInbox.Enqueue` / `ListPending`. | The attack test failed both after mutating the original map and after mutating the map returned by `ListPending`. Existing tests covered `Task.Spec.Notify.Metadata` but not `Notification.Target.Metadata`. | `MemoryInbox` is process-local, but it is still the reference implementation of `SessionNotificationInbox`; all other task snapshots are cloned, so exposing this one map is inconsistent and can corrupt pending routing metadata. | Fix |

#### Fix Applied

| File | Change |
|---|---|
| `adk/backgroundtask/sessionnotify/sessionnotify.go` | `cloneNotification` now deep-copies `notification.Target.Metadata` before cloning the task snapshot. |
| `adk/backgroundtask/sessionnotify/sessionnotify_test.go` | Added `TestAttack_MemoryInboxDeepCopiesNotificationTargetMetadata`. |

#### Verification

- `go test ./adk/backgroundtask/sessionnotify -run 'TestAttack_' -v -count=1`: passed after fix.
- `go build ./...`: passed.
- `go test ./...`: passed.
- `go test ./... -run 'TestAttack_' -v -count=1`: passed.

### Iteration 1 Re-Attack

All attack tests pass. No new red bugs found.

## Stage 3: Test Audit

### Iteration 1 Audit

| Priority | Issue | Count | Estimated LOC Impact |
|---|---|---:|---:|
| Medium | `sessionnotify` package coverage was 83.3%, below the 85% target for the package touched by the Stage 2 fix. `Sink.Accept` and `AcceptTarget` validation paths were untested. | 1 | +20 LOC |
| Low | `adk/backgroundtask/subagent` package coverage is 63.1% in the broader `adk/backgroundtask/...` sweep. This is outside the Stage 2 fix surface and mostly concentrated in foreground observer/control-error paths. | 1 | Deferred |

#### Validate & Counter-Argue

| Finding | Validation | Counter-argument | Verdict |
|---|---|---|---|
| Add focused tests for `Sink.Accept` and `AcceptTarget` validation paths. | Function coverage showed `Accept` at 0.0% and `AcceptTarget` at 66.7%; these are public entry points in the package touched by the fix. | These are simple guard paths and not high behavioral risk, but small tests improve package coverage without obscuring intent. | Fix |
| Raise `subagent` package coverage above 70%. | `go test -coverprofile=cover.out ./adk/backgroundtask/...` showed `subagent` at 63.1%. | This package is large durable executor integration code with existing focused tests; raising it substantially would require broader executor harness work beyond the Stage 2 fix. No high-priority test quality defect was found there during this loop. | Defer |

#### Fix Applied

| File | Change |
|---|---|
| `adk/backgroundtask/sessionnotify/sessionnotify_test.go` | Added `TestSinkAcceptRejectsUnroutedNotification` and `TestSinkAcceptTargetValidatesDependenciesAndIdentity`. |

#### Verification

- `go test -coverprofile=cover_sessionnotify.out ./adk/backgroundtask/sessionnotify`: passed, coverage 86.5%.
- `go tool cover -func=cover_sessionnotify.out`: `cloneNotification` 100.0%, `Accept` 100.0%, `AcceptTarget` 88.9%, package total 86.5%.
- `go test ./...`: passed.

### Iteration 1 Re-Audit

The Stage 2 fix path is covered, `sessionnotify` package coverage meets the 85% target, and no high-priority duplicate/weak-assertion/semantic-value findings remain in the touched test surface.

## Post-Review Architecture Follow-Up: Durable Subagent Output

### Problem

The original implementation persisted durable subagent output twice:

- Runner persisted the canonical child timeline in `<taskID>/session`;
- the durable executor formatted the same live events into the background-task output
  feed and optional `OutputFile`.

This created a non-atomic dual-write boundary and allowed a duplicate output projection
to affect task correctness. Separately, `task_output` hid authoritative `ResultData`
whenever an output file was healthy.

### Final Design

| Concern | Owner |
|---|---|
| Task lifecycle and terminal result/error | `backgroundtask.Manager` and Store |
| Durable subagent progress | Child `SessionEventStore` |
| Local subagent and shell incremental output | Store output feed and optional `OutputFile` |
| Model-facing durable progress | Private child-session projection invoked by `task_output` |
| Local/Durable transcript customization | Shared message-level `TranscriptFormat` |

The follow-up deliberately did not add a public `SessionReader`,
`Manager.ReadProgress`, reader interface, or reader registry. The background-task
middleware accepts one optional `ReadTaskProgress` callback. DeepAgent wires the
built-in durable subagent callback automatically.

The private projection derives progress from existing invariants: a durable task owns a
dedicated `<taskID>/session`, its first message is the submitted query, and later message
events are progress. It excludes that first message, reads a bounded recent page,
restores chronological order, includes incomplete stream materialization, and reads the
latest interrupt separately. No `_eino_message_source` or `_eino_agent_name` metadata
was added.

Transferred or nested emitter names are intentionally displayed using the root
`subagent_name`. This avoids persistence weight for marginal presentation fidelity.

### API and Implementation Changes

| Area | Change |
|---|---|
| Durable executor | Removed `OutputStore`, `EventFormat`, output-file opening, and `AppendOutput` writes. |
| Durable submission | Removed `SubmitRequest.OutputFile`; new durable tasks do not persist an output path. |
| Subagent middleware | Replaced Local/Durable source-specific formatters with one `TranscriptFormat(ctx, agentName, message)`. |
| Task-control middleware | Added optional `ReadTaskProgress` callback; `task_output` always preserves terminal `ResultData`/`ResultError`. |
| DeepAgent | Shares one formatter, wires durable progress automatically, and keeps Durable `OutputDir` shell-only. |
| Session projection | Added a private bounded reader under `adk/middlewares/subagent`; no new public session contract. |

### Regression Coverage

- Default and custom transcript formatting.
- Local output feed/file behavior with the shared formatter.
- Durable child-session progress with initial-query exclusion.
- Standard messages, Agentic user-role tool results, and incomplete streams.
- Waiting-input interrupt rendering.
- Callback failure isolation from task lifecycle and terminal result.
- Resume across Manager reconstruction without durable subagent feed/file writes.
- DeepAgent formatter forwarding.

### Follow-Up Verification

- `go test ./adk/backgroundtask/... ./adk/middlewares/... ./adk/prebuilt/deep -count=1`: passed.
- `go test ./...`: passed.
- `go build ./...`: passed.
- Focused coverage:
  - `adk/backgroundtask/subagent`: 69.8%.
  - `adk/middlewares/subagent`: 73.2%.

## Final Summary

## Overview

- **Total iterations**: Stage 1: 1, Stage 2: 1, Stage 3: 1, architecture follow-up: 1
- **Review-start PR scope**: 38 files, 7350 insertions, 2532 deletions versus `alpha/10...HEAD`
- **Original review changes**: 2 code/test files
- **Architecture follow-up changes**: 11 code/test/design files plus this audit document
- **Temporary branches cleaned**: none; no `review/pr-*` branches existed

## Stage 1: Design Review Changes

### Findings Resolved

No production design changes were required. Findings were validated and either accepted as intentional design trade-offs or deferred to attack/test stages.

### Design Scorecard (Final)

| Dimension | Before | After |
|---|---:|---:|
| Concept Coherence | 4/5 | 4/5 |
| API Usability and Intuitiveness | 4/5 | 4/5 |
| Minimum API Surface | 4/5 | 4/5 |
| Backward Compatibility | 4/5 | 4/5 |
| Module Separation and Layering | 4/5 | 4/5 |
| Cohesion vs. Tension | 4/5 | 4/5 |
| Elegance vs. Complexity | 4/5 | 4/5 |
| Naming | 4/5 | 4/5 |
| Readability | 4/5 | 4/5 |
| Duplication | 4/5 | 4/5 |
| Public API Documentation | 4/5 | 4/5 |
| Internal Comments | 4/5 | 4/5 |

## Stage 2: Attack Review Changes

### Bugs Fixed

| # | Severity | Bug | Fix | Test |
|---|---|---|---|---|
| 1 | Critical | `sessionnotify.MemoryInbox` allowed `Notification.Target.Metadata` map aliasing across enqueue/list boundaries. | Deep-copy `Notification.Target.Metadata` in `cloneNotification`. | `TestAttack_MemoryInboxDeepCopiesNotificationTargetMetadata` |

### Attack Test Results (Final)

- **Total attack tests run**: all repository `TestAttack_` tests via `go test ./... -run 'TestAttack_' -v -count=1`
- **All passing**: yes

## Stage 3: Test Audit Changes

### Improvements Applied

| # | Category | Change | LOC Impact |
|---|---|---|---:|
| 1 | Coverage Gap | Added direct guard-path tests for `Sink.Accept` and `Sink.AcceptTarget`. | +20 |

### Coverage (Final)

- `adk/backgroundtask/sessionnotify`: 86.5% statements
- Functions below 70% in touched package: none on the Stage 2 fix path; `RequestTurn` remains 70.6%.
- Broader `adk/backgroundtask/...` sweep: 75.4% total; `subagent` remains below 70% and is deferred because it is outside the direct fix surface.

## Cumulative File Change List

| File | Stage(s) | Summary of Changes |
|---|---|---|
| `adk/backgroundtask/sessionnotify/sessionnotify.go` | 2 | Deep-copy `Notification.Target.Metadata` in `cloneNotification`. |
| `adk/backgroundtask/sessionnotify/sessionnotify_test.go` | 2, 3 | Added one attack regression test and two validation-path coverage tests. |
| `adk/backgroundtask/subagent/subagent.go` | Follow-up | Removed duplicate output feed/file persistence from durable execution. |
| `adk/backgroundtask/subagent/subagent_test.go` | Follow-up | Verified resume without durable output feed/file writes. |
| `adk/middlewares/backgroundtask/middleware.go` | Follow-up | Added progress callback integration and preserved authoritative terminal results. |
| `adk/middlewares/backgroundtask/middleware_test.go` | Follow-up | Covered progress/error composition and terminal-result authority. |
| `adk/middlewares/subagent/agent_tool.go` | Follow-up | Unified message-level transcript formatting and removed durable output-file submission. |
| `adk/middlewares/subagent/middleware.go` | Follow-up | Added shared `TranscriptFormat` and simplified Durable configuration. |
| `adk/middlewares/subagent/task_progress.go` | Follow-up | Added private bounded child-session projection. |
| `adk/middlewares/subagent/task_progress_test.go` | Follow-up | Covered standard, Agentic, incomplete, interrupt, and formatter semantics. |
| `adk/prebuilt/deep/deep.go` | Follow-up | Wired shared formatting and durable progress callback. |
| `adk/prebuilt/deep/deep_test.go` | Follow-up | Covered formatter forwarding. |
| `.trae/documents/unify_local_durable_subagent_background.md` | Follow-up | Documented executor-specific output authority and session-backed progress. |
| `feat_durabletask_comprehensive_review.md` | 1, 2, 3, Follow-up | Exported review record and architecture follow-up. |

## Remaining Items

- Deferred: broader `adk/backgroundtask/subagent` branch coverage. The architecture
  follow-up raised focused package coverage from 63.1% to 69.8%; remaining gaps are
  primarily foreground control and error branches outside the corrected output path.

## Final Verification

- `go test ./...`: passed
- `go build ./...`: passed
- `go test ./... -run 'TestAttack_' -v -count=1`: passed
- `go test -coverprofile=cover_sessionnotify.out ./adk/backgroundtask/sessionnotify`: passed, 86.5%
- `go test -coverprofile=<tmp>/subagent_coverage.out ./adk/backgroundtask/subagent ./adk/middlewares/subagent`: passed, 69.8% and 73.2%.
