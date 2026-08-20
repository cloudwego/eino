# Comprehensive Review Summary: foreground-background-notification-ownership

## Overview
- Total iterations: Stage 1: 2, Stage 2: 1, Stage 3: 1
- Files modified: 43
- Lines changed: +1872 / -1357 before this report
- Scope: current uncommitted diff implementing `.trae/documents/plan-foreground-background-notification-ownership.md`

## Stage 1: Design Review Changes

### Findings Resolved
| # | Dimension | Finding | Fix Applied | Files |
|---|-----------|---------|-------------|-------|
| 1 | Backward Compatibility | `Manager.Submit` originally wrapped both `ErrTaskCreatedEventUndelivered` and the sender error with two `%w` verbs, relying on newer Go multi-error behavior. This repository targets Go 1.18. | Replaced double `%w` with `taskCreatedEventUndeliveredError`, using `Is` for the sentinel and `Unwrap` for the cause. | `adk/backgroundtask/store.go`, `adk/backgroundtask/executor.go` |
| 2 | Module Separation | Durable sub-agent auto-background still reused the old manager-backed foreground coordinator, which pre-created tasks before ownership transfer. | Removed the old coordinator execution API, routed normal foreground through `AgentTool`, made explicit background submit/execute directly, and rejected unsafe durable auto-background configuration until checkpoint handoff support exists. | `adk/internal/foreground/coordinator.go`, `adk/middlewares/subagent/agent_tool.go` |
| 3 | API Usability | Foreground managed-tool waiting-input was rendered as a foreground result, leaving no parent-level resume bridge. | Added `foregroundToolInterruptState`, parent `StatefulInterrupt`, and `Resume(Attempt: 0)` bridge with no TaskStore record. | `adk/backgroundtask/tool/managed_tool.go`, `adk/backgroundtask/tool/managed_tool_test.go` |
| 4 | Capability Model | Recoverable shell did not advertise foreground handoff despite being able to adopt the same process-local run. | Implemented `ForegroundHandoffTool` for the shell adapter and made recoverable executor recover started checkpoint attempts without re-running `Start`. | `adk/backgroundtask/shell/shell.go`, `adk/backgroundtask/tool/executor.go` |

### Design Scorecard (Final)
| Dimension | Rating | Notes |
|-----------|--------|-------|
| Concept Coherence | 5/5 | Store records now mean background ownership only. |
| API Usability | 4/5 | `SubmitRequest` makes checkpoint-at-create explicit; durable auto-background is rejected until safe handoff exists. |
| Minimum API Surface | 4/5 | Removed `MarkBackgrounded`; added only `SubmitRequest`, `ErrTaskCreatedEventUndelivered`, `ForegroundHandoffTool`, and `InterruptSignalFromEvent`. |
| Backward Compatibility | 4/5 | Intentional API break in `Manager.Submit`; Go 1.18 compatibility verified. |
| Module Separation | 4/5 | Foreground behavior moved to adapters; background core no longer knows foreground mode. |
| Cohesion | 4/5 | Managed-tool foreground handling is cohesive but sizeable. |
| Complexity | 4/5 | Complexity is concentrated in ownership transition and interrupt/resume paths. |
| Naming | 5/5 | New public names describe ownership and event delivery semantics directly. |
| Readability | 4/5 | `managed_tool.go` remains dense; tests document the critical cases. |
| Duplication | 4/5 | Foreground update drain has streaming/invoke variants to preserve behavior. |
| Public Docs | 5/5 | Exported APIs and prompt semantics updated. |
| Internal Comments | 4/5 | Non-obvious adopted-run and checkpoint paths are covered by focused tests. |

## Stage 2: Attack Review Changes

### Bugs Fixed
| # | Severity | Bug | Fix | Test |
|---|----------|-----|-----|------|
| 1 | High | Foreground managed-tool waiting-input could not be resumed without creating a durable task. | Added parent `StatefulInterrupt` bridge and `Resume(Attempt: 0)`. | `TestManagedToolForegroundWaitingInputResumesWithoutTask` |
| 2 | High | Durable sub-agent auto-background still used unsafe pre-created foreground tasks. | Disabled configuration until checkpoint handoff support exists. | `TestDurableAutoBackgroundRequiresHandoffSupport` |
| 3 | Medium | Recoverable shell auto-handoff could re-run start if adopted handle was unavailable on attempt 1. | Treat any started recoverable checkpoint as recoverable, including attempt 1. | `TestNewRegistrationAndAdapter`, managed-tool handoff tests |

### Attack Test Results
- `go test ./... -run TestAttack_ -count=1`: passed
- Existing attack tests were updated where foreground no longer creates task records.

## Stage 3: Test Audit Changes

### Improvements Applied
| # | Category | Change | LOC Impact |
|---|----------|--------|------------|
| 1 | Assertion Quality | Foreground tests now assert `ErrNotFound` for preallocated IDs that never became tasks. | Moderate |
| 2 | Coverage Gap | Added foreground waiting-input resume test covering stable TaskID, RequestID, checkpoint, and `Attempt: 0`. | +106 LOC |
| 3 | Semantic Value | Removed deferred-created marker tests that contradicted the new model. | -123 LOC |

### Coverage
- `go test -coverprofile=cover.out ./adk/... && go tool cover -func=cover.out`: total 85.5%.
- Package-level changed areas with lower package totals still have focused tests for changed contracts; `adk/internal/foreground` now contains only simple data/projection helpers.

## Verification
- `go test ./...`
- `go test ./... -run TestAttack_ -count=1`
- `golangci-lint run --new-from-rev=alpha/10 ./...`
- `go test -race ./...`
- `go test -coverprofile=cover.out ./adk/... && go tool cover -func=cover.out`
- `git diff --check`
- Sensitive data scan: only matched the test phrase `secret task`; no credential material found.

## Remaining Items
- Durable sub-agent auto-background is intentionally disabled until a checkpoint handoff seam is implemented end to end.
