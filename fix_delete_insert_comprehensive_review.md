# Comprehensive Review: Tombstone-Aware Message Replay

## Pre-Flight

- Branch: `fix/delete_insert`
- Target: `origin/alpha/10`
- Initial scope: `adk/session.go`, `adk/session_test.go`
- Baseline commit: `4397b3df`
- Baseline test: `go test ./...` passed

## Stage 1: Design Review

### Iteration 1

| Dimension | Rating | Notes |
|-----------|--------|-------|
| Concept coherence | 5/5 | A replay-only tombstone directly models the required ordering semantics. |
| API usability | 5/5 | No exported API changes. |
| Minimum API surface | 5/5 | All new types and methods are private. |
| Backward compatibility | 5/5 | Persisted events and visible reconstructed state are unchanged. |
| Module separation | 5/5 | Replay behavior remains in `adk/session.go`. |
| Cohesion | 5/5 | `messageReplay` owns only ordered replay projection. |
| Elegance | 4/5 | One entry list handles visible messages and tombstones without parallel indexes. |
| Naming | 5/5 | No public names; `messageReplay` and `messageReplayEntry` describe their roles. |
| Readability | 4/5 | The asymmetric tombstone rules needed an explicit invariant comment. |
| Duplication | 5/5 | `applySessionEvent` and durable replay share one event application engine. |
| Public documentation | 5/5 | No public surface changed. |
| Internal comments | 4/5 | Tombstone visibility rules were initially implicit. |

New public names: none.

Top recommendations:

1. Document that tombstones are insertion anchors but not update or repeated-delete targets.
2. Keep tombstones private to replay rather than changing `SessionEvent` or reconstructed state.
3. Preserve `MessagesReplaced` as a hard replay boundary.

### Findings and Verdicts

| # | Finding | Reference | Validation and Counter-Argument | Verdict |
|---|---------|-----------|---------------------------------|---------|
| D1 | Tombstone target asymmetry was not explained. | `adk/session.go`, `messageReplay` | The conditions are correct but non-obvious; one concise invariant comment prevents future weakening. | Fix |
| D2 | Deleted entries retain the full message during replay. | `adk/session.go`, `messageReplayEntry` | Retaining only the ID looks cheaper, but the loaded event slice already owns the message payload for the replay duration and a second ID field adds state to synchronize. | Won't Fix |

### Fix and Re-Review

- Added the tombstone visibility invariant to `messageReplay`.
- Re-review: readability and internal comments are 5/5; no new concerns.
- Verification: `go build ./...` and `go test ./...` passed.

## Stage 2: Attack Review

### Iteration 1

| # | Severity | Attack | Test | Result |
|---|----------|--------|------|--------|
| A1 | OK | Multiple insertions against one tombstone preserve event order. | `TestAttack_MessageReplayMultipleInsertionsBeforeDeletedAnchor` | Pass |
| A2 | OK | Tombstones do not become valid update or repeated-delete targets. | `TestAttack_MessageReplayDeletedTargetRemainsMutationInvalid` | Pass |
| A3 | OK | Generic replay behavior is identical for `AgenticMessage`. | `TestAttack_AgenticMessageReplayInsertsBeforeDeletedAnchor` | Pass |

The initial Agentic attack test used an invalid struct literal; validation showed this
was a test-construction mistake, not a production defect. It was corrected to use
`schema.UserAgenticMessage` and `schema.SystemAgenticMessage`.

No confirmed production bugs were found. The complete `TestAttack_` set passes.

## Stage 3: Test Audit

### Iteration 1

| Priority | Issue | Count | Estimated LOC Impact |
|----------|-------|-------|----------------------|
| High | Identity-mismatch attack test logged either result without asserting failure. | 1 | +1 |
| Medium | Similar message setup appears in multiple replay tests. | 2 sites | 0 |

### Findings and Verdicts

| # | Category | Validation and Counter-Argument | Verdict |
|---|----------|---------------------------------|---------|
| T1 | Assertion quality | `TestAttack_ApplySessionEventMessageUpdatedIdentityMismatch` passed even if the operation unexpectedly succeeded. Strong assertions test the contract without coupling to internals. | Fix |
| T2 | Boilerplate | Two setup sites do not meet the three-site extraction threshold, and a helper would hide each event sequence. | Won't Fix |
| T3 | Duplicates | The end-to-end store replay, repeated-tombstone insertion, strict mutation, and Agentic parity tests exercise distinct contracts. | Won't Fix |
| T4 | Logical grouping | Separate top-level tests provide useful failure isolation across reconstruction, direct replay, and generic parity. | Won't Fix |

### Improvements and Re-Audit

- Strengthened the identity-mismatch attack test to require the error, check its
  category, and verify the original message slice remains unchanged.
- `go test ./...` passed after the fix.
- `adk` package statement coverage: 88.8%.
- Modified function coverage:
  - `applySessionEvent`: 85.7%
  - `newMessageReplay`: 100.0%
  - `messageReplay.apply`: 100.0%
  - `messageReplay.visibleMessages`: 100.0%
- No modified function is below the 70% hard floor; all meet the 85% target.

## Final Summary

### Overview

- Total iterations: Stage 1: 1, Stage 2: 1, Stage 3: 1
- Production and test files modified: 2
- Review findings fixed: 2
- Confirmed production bugs found during attack review: 0

### Findings Resolved

| Stage | Finding | Fix | File |
|-------|---------|-----|------|
| Design | Tombstone mutation asymmetry was implicit. | Documented insertion-anchor versus mutation-target rules. | `adk/session.go` |
| Test audit | Identity-mismatch attack test had no required assertion. | Required the error and unchanged state. | `adk/session_test.go` |

### Attack Results

- New attack tests: 3 top-level tests, 4 scenarios
- Final result: all passing
- Verified ordering, mutation strictness, and `AgenticMessage` parity

### Cumulative File Changes

| File | Stages | Summary |
|------|--------|---------|
| `adk/session.go` | 1 | Replaced visible-only replay with ordered tombstone entries and documented strict mutation behavior. |
| `adk/session_test.go` | 2, 3 | Added reconstruction and adversarial replay coverage; strengthened an existing assertion. |
| `fix_delete_insert_comprehensive_review.md` | 1-4 | Recorded findings, verdicts, fixes, and verification. |

### Final Verification

- `go build ./...`: pass
- `go test ./...`: pass
- `go test ./adk -run 'TestAttack_' -v -count=1`: pass
- `go test -coverprofile=adk.cover.out ./adk`: pass, 88.8%
- `git diff --check`: pass

### Remaining Items

None.
