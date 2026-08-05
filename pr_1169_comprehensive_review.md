# Comprehensive Review Tracking: PR #1169

## Scope And Baseline

- PR: `#1169 feat(adk): add durable background task execution`
- Focus commit: `fd060d58 refactor(adk): simplify task progress events`
- Focus range: `3138bf74..fd060d58`
- Focus size: 25 files, +383/-340
- Baseline `go build ./...`: pass
- Baseline `go test ./...`: pass
- Baseline `git diff --check`: pass

## Stage 1: Design Review

### Iteration 1 Scorecard

| Dimension | Rating | Notes |
| --- | --- | --- |
| Concept coherence | 5/5 | Task lifecycle snapshots and append-ordered progress events remain independent domains. |
| API usability | 4/5 | The unified append API is direct, but plain live updates discard the resolved generated EventID. |
| Minimum API surface | 5/5 | The split append APIs and unused replay cursor are removed without replacement. |
| Backward compatibility | 5/5 | The affected progress contracts are unreleased; lifecycle, notification, and SessionEvent APIs are unchanged. |
| Module separation | 5/5 | Runtime owns generation, Store owns fencing/deduplication, and materializers own derived output. |
| Cohesion | 5/5 | Event identity and replay semantics are centralized without cross-store ordering. |
| Elegance/complexity | 5/5 | One idempotent append path replaces keyed/unkeyed branching and public sequence state. |
| Naming | 5/5 | Public names consistently use TaskEvent and EventID vocabulary. |
| Readability | 4/5 | `persistUpdate`, Store append authorization, and recovery materialization are the densest sections. |
| Duplication | 5/5 | Raw runtime callers share the same empty-ID generation path. |
| Public API documentation | 4/5 | Core contracts are documented, including opaque EventID and stable replay order. |
| Internal comments | 5/5 | Non-obvious materialization and ordering constraints are recorded at their ownership boundary. |

### New Public Name Assessment

| Public name(s) | Assessment |
| --- | --- |
| `TaskEvent` | Precisely names progress-only task events without implying lifecycle authority. |
| `AppendTaskEventRequest`, `AppendTaskEventResult` | Clearly separates write authorization from persisted event data and reports replay insertion. |
| `ReadRecentTaskEventsRequest`, `ReadRecentTaskEventsResult` | Explicitly bounded presentation API with no replay-cursor implication. |
| `ErrTaskEventConflict` | Accurately identifies same-EventID/different-bytes corruption prevention. |
| `Store.AppendTaskEvent`, `Store.ReadRecentTaskEvents` | Minimal Store surface aligned with the target event model. |
| `ExecutionRuntime.AppendTaskEvent` | Correct single generation boundary for managed and raw callers. |
| `Manager.ReadRecentTaskEvents` | Direct delegation without exposing Store or cursor internals. |
| `Update.EventID` | Consistent stable identity for recoverable update sources. |
| `MaterializeOutputRequest.EventID` | Correct idempotency key for derived output. |

### Iteration 1 Findings And Verdicts

| # | Severity | Dimension | Finding | Validation And Counterargument | Verdict |
| --- | --- | --- | --- | --- | --- |
| D1 | Medium | API usability | `adk/backgroundtask/tool/executor.go:417-437` receives a resolved generated EventID but projects the original plain `Update` with an empty EventID. | Persistence readers can obtain identity from `TaskEvent`, but the plan requires callers to receive generated IDs and makes runtime return the resolved event for that purpose. Projecting a clone with the resolved ID preserves caller-supplied materialization gating. | Fix |

### Top Recommendations

1. Preserve the resolved EventID at every caller-visible projection boundary.
2. Keep attempt fencing before EventID replay lookup in every Store implementation.
3. Retain deterministic source replay order as an explicit materializer precondition.

### Iteration 1 Fix Log

- D1: Live projection now clones the update and assigns the resolved
  `TaskEvent.EventID`. The pre-call `callerSuppliedEventID` flag remains the
  materialization gate, so generated IDs do not make plain updates materializable.
- D1 verification: `go build ./...`, `go test ./...`, and `git diff --check` pass.

### Iteration 1 Re-Review

D1 is resolved. Re-review of API usability, materialization gating, projection
aliasing, and replay behavior found no new concerns.

| Dimension | Final Rating |
| --- | --- |
| Concept coherence | 5/5 |
| API usability | 5/5 |
| Minimum API surface | 5/5 |
| Backward compatibility | 5/5 |
| Module separation | 5/5 |
| Cohesion | 5/5 |
| Elegance/complexity | 5/5 |
| Naming | 5/5 |
| Readability | 4/5 |
| Duplication | 5/5 |
| Public API documentation | 4/5 |
| Internal comments | 5/5 |

## Stage 2: Attack Review

### Iteration 1 Results

| # | Severity | Issue | Test | Status |
| --- | --- | --- | --- | --- |
| A1 | OK | Byte-identical replay must not duplicate Store or live projection but must retry materialization. | `TestAttack_ReplayedEventProjectsOnceMaterializesTwice` | Verified |
| A2 | OK | Same EventID with different bytes must fail deterministically. | `TestAttack_ConflictingEventIDFailsTask` | Verified |
| A3 | OK | Recoverable updates without stable identity must fail. | `TestAttack_RecoverableUpdateRequiresEventID` | Verified |
| A4 | OK | A persisted replay must repair missing materialization even when `Inserted=false`. | `TestAttack_PersistedReplayRepairsMissingMaterialization` | Verified |
| A5 | OK | Materialization must preserve source replay order instead of sorting EventID. | `TestAttack_MaterializationPreservesStableReplayOrder` | Verified |
| A6 | OK | Plain updates must receive generated IDs without becoming materializable. | `TestAttack_PlainUpdateGeneratedEventIDNotMaterialized` | Verified |
| A7 | OK | Identical EventIDs in different tasks must not conflict. | `TestAttack_EventIDIsTaskLocal` | Verified |
| A8 | OK | Recent reads must preserve append order independent of lexical EventID order. | `TestAttack_RecentTaskEventsIgnoreEventIDLexicalOrder` | Verified |
| A9 | OK | Stale attempts must receive `ErrLeaseLost` before replay success. | `TestAttack_TaskEventReplayFencesStaleAttempt` | Verified |
| A10 | OK | Append order must span attempts without exposing attempt in events. | `TestAttack_TaskEventOrderSpansAttemptsWithoutExposingAttempt` | Verified |

Validation confirmed each expectation follows the plan's identity, fencing,
ordering, or materialization contract. Counterarguments based on Store-internal
position or generated-ID materialization would violate the intentionally minimal
public model, so none justified weakening the tests.

Final command:

```text
go test ./adk/backgroundtask/... ./adk/middlewares/filesystem ./adk/middlewares/subagent -run 'TestAttack_' -v -count=1
```

Result: all scoped attack tests pass, zero confirmed bugs, Stage 2 complete in one iteration.

## Stage 3: Test Audit

### Iteration 1 Findings

| Priority | Category | Finding | Count | Estimated LOC Impact | Verdict |
| --- | --- | --- | --- | --- | --- |
| Medium | Assertion quality | New result-pointer assertions could panic before identifying a nil contract violation, and one validation test accepted any error. | 16 sites | +15 LOC | Fix |
| None | Duplicates | Event ordering, replay, task-local identity, and materialization tests have distinct semantic contracts. | 0 | 0 | Won't Fix |
| None | Boilerplate | Repeated setup remains below the three-occurrence extraction threshold or uses existing helpers. | 0 | 0 | Won't Fix |
| None | Logical grouping | Separate attack names preserve independent failure isolation. | 0 | 0 | Won't Fix |
| None | Semantic value | Every new test protects an explicit plan requirement or review finding. | 0 | 0 | Won't Fix |
| None | Coverage gaps | Production diff coverage is 89.7%; every changed function is at least 70% covered. | 0 | 0 | Won't Fix |

Validation confirmed that explicit `require.NotNil` and exact error matching improve
failure diagnosis without coupling tests to implementation details. The counterargument
that a panic still fails the test was rejected because it obscures the violated contract.

### Iteration 1 Fixes

- Added nil guards before dereferencing returned `TaskEvent`, append results, and
  projected updates.
- Replaced the generic empty-EventID error assertion with the expected validation text.
- Replaced a non-empty event-list assertion with the exact expected length.

### Iteration 1 Re-Audit

- Exact production diff coverage: 89.7% (61/68 changed executable lines).
- Combined scoped package coverage: 86.1%.
- Changed functions below 70%: none.
- High-priority findings remaining: none.
- Duplicate, boilerplate, grouping, semantic-value, and assertion re-audit: clear.
- `go test ./...`: pass.

# Comprehensive Review Summary: PR #1169

## Overview

- **Total iterations**: Stage 1: 1, Stage 2: 1, Stage 3: 1
- **Focus implementation**: `3138bf74..fd060d58`
- **Files modified**: 25 Go files plus this review report
- **Reviewed Go delta**: +440 / -342 (net +98)
- **Review fixes after implementation commit**: 7 Go files, +65 / -10

## Stage 1: Design Review Changes

### Findings Resolved

| # | Dimension | Finding | Fix Applied | Files |
| --- | --- | --- | --- | --- |
| D1 | API usability | Generated EventID was persisted but discarded from the live plain-update projection. | Project a cloned update carrying the resolved `TaskEvent.EventID` while retaining caller-supplied identity as the materialization gate. | `adk/backgroundtask/tool/executor.go` |

### Design Scorecard (Final)

| Dimension | Before | After |
| --- | --- | --- |
| API usability | 4/5 | 5/5 |
| All other dimensions | 4-5/5 | 4-5/5 |

## Stage 2: Attack Review Changes

### Bugs Fixed

No additional confirmed bug was found after the Stage 1 fix.

### Attack Test Results (Final)

- Task-event-specific attack tests: 10
- All passing: yes
- Confirmed bugs remaining: zero

## Stage 3: Test Audit Changes

### Improvements Applied

| # | Category | Change | LOC Impact |
| --- | --- | --- | --- |
| T1 | Assertion quality | Guard result pointers and projected updates before dereference. | +14 |
| T2 | Assertion quality | Require the specific empty-EventID validation error and exact event count. | +1 |

### Coverage (Final)

- Overall production diff coverage: 89.7%
- Combined scoped package coverage: 86.1%
- Changed functions below 70%: none

## Cumulative File Change List

| File or Area | Stage(s) | Summary of Changes |
| --- | --- | --- |
| `adk/backgroundtask/types.go` | Implementation | Replaced output records and cursor types with TaskEvent APIs. |
| `adk/backgroundtask/store.go` | Implementation | Unified append/read Store surface and conflict sentinel. |
| `adk/backgroundtask/in_memory_store.go` | Implementation | Added fenced task-wide EventID deduplication and bounded recent reads. |
| `adk/backgroundtask/executor.go` | Implementation | Centralized missing EventID generation and Manager delegation. |
| `adk/backgroundtask/conformance_test.go` | Implementation | Enforced the new public shape and removed cursor API. |
| `adk/backgroundtask/durable_store_test.go` | Implementation, 2, 3 | Covered replay, conflict, fencing, ordering, task locality, bounds, and cloning. |
| `adk/backgroundtask/manager_test.go` | Implementation, 3 | Covered generated and supplied runtime EventIDs. |
| `adk/backgroundtask/local/local.go` | Implementation | Routed raw stream chunks through runtime generation. |
| `adk/backgroundtask/local/local_test.go` | Implementation, 3 | Verified generated IDs for local raw callers. |
| `adk/backgroundtask/subagent/subagent_test.go` | Implementation | Migrated cursor-free progress assertions without changing checkpoint sequence. |
| `adk/backgroundtask/tool/types.go` | Implementation | Replaced Update.SourceID with stable EventID. |
| `adk/backgroundtask/tool/executor.go` | Implementation, 1 | Unified persistence and projected resolved generated identities. |
| `adk/backgroundtask/tool/materializer.go` | Implementation | Made EventID the durable derived-output idempotency key. |
| `adk/backgroundtask/tool/progress.go` | Implementation | Read bounded recent TaskEvents without exposing identity to task_output. |
| `adk/backgroundtask/tool/recovery_conformance.go` | Implementation | Compared stable EventID and bytes across recovery. |
| `adk/backgroundtask/tool/attack_test.go` | Implementation, 2 | Added replay, conflict, repair, order, and framing attacks. |
| `adk/backgroundtask/tool/managed_tool_test.go` | Implementation, 1, 2, 3 | Covered generation, projection, materialization gating, and recent reads. |
| `adk/backgroundtask/tool/progress_test.go` | Implementation | Covered bounded cursor-free rendering. |
| `adk/backgroundtask/tool/recovery_conformance_test.go` | Implementation | Migrated recovery identity assertions. |
| `adk/backgroundtask/tool/validation_test.go` | Implementation | Migrated TaskEvent formatting and projection helpers. |
| `adk/middlewares/filesystem/bash_run.go` | Implementation | Routed raw shell output through runtime generation. |
| `adk/middlewares/filesystem/bash_run_test.go` | Implementation, 3 | Verified generated shell event identity. |
| `adk/middlewares/filesystem/filesystem.go` | Implementation | Updated materializer contract documentation. |
| `adk/middlewares/subagent/agent_tool.go` | Implementation | Routed raw agent transcript events through runtime generation. |
| `adk/middlewares/subagent/middleware_test.go` | Implementation, 3 | Verified generated sub-agent event identity and cursor-free reads. |

## Final Verification

- `go build ./...`: pass.
- `go test ./...`: pass.
- Scoped `TestAttack_` suite: pass.
- Exact production diff coverage: 89.7%.
- Scoped package coverage: 86.1%.
- `git diff --check`: pass.

## Remaining Items

None.
