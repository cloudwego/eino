# Comprehensive Review: PR #1213

## Scope

- Base: `alpha/10`
- Head: `fix/streaming-nested-checkpoint-resume`
- Purpose: preserve nested checkpoints when an interrupt is raised from a terminal stream error during resume.

## Stage 1: Design Review

### Findings

No blocking design findings.

1. **Concept coherence (5/5)**: interrupt control metadata remains separate from materialized stream data.
2. **API usability (5/5)**: no public API changes.
3. **Minimum API surface (5/5)**: all new helpers are package-private.
4. **Backward compatibility (4/5)**: ordinary rerun behavior remains unchanged; malformed subgraph checkpoints now fail closed.
5. **Layering (5/5)**: compose owns checkpoint integrity and ADK only adds a defensive input check.
6. **Cohesion (5/5)**: all production changes serve interrupt/checkpoint recovery.
7. **Complexity (4/5)**: stream error filtering is necessary but requires careful ID matching.
8. **Naming (5/5)**: no public names were added; internal names describe their contracts.
9. **Readability (4/5)**: interrupt attribution is non-trivial and has a rationale comment.
10. **Duplication (5/5)**: shared stream conversion remains centralized.
11. **Public documentation (5/5)**: no exported surface changed.
12. **Internal comments (4/5)**: the two non-obvious control-flow decisions are documented.

### Validation

- The address-prefix lookup is scoped to the current graph and falls back to the observing node when ownership cannot be proven.
- Checkpoint stream conversion ignores only an interrupt ID already present in that checkpoint; unrelated errors still fail.
- The subgraph integrity check uses existing component metadata and does not reject ordinary rerun nodes.

### Recommendations

1. Attack-test a stream that emits data before its terminal interrupt.
2. Attack-test repeated node names across nested graph boundaries.
3. Keep the exact middleware and TurnLoop matrix as the primary regression test.

All recommendations are verification work; no design change is required before Stage 2.

## Stage 2: Attack Review

### Attack Results

| Severity | Scenario | Test | Result |
|----------|----------|------|--------|
| High | A stream emits data before its terminal interrupt | `TestAgenticReact_LateStreamInterruptResume` | Pass |
| High | An unrecorded interrupt must not be swallowed during checkpoint conversion | `TestAttack_CheckpointStreamConversionRejectsUnrecordedInterrupt` | Pass |
| Medium | Reused node names across nested graph boundaries must not change ownership | `TestAttack_InterruptOriginUsesCurrentGraphBoundary` | Pass |

The initial attack test had a compile-only assertion error because `IsInterruptRerunError` returns two values. The test was corrected without changing production code, then all attack tests passed.

No confirmed bugs remain after the first attack iteration.

## Stage 3: Test Audit

### Findings and Fixes

| Priority | Finding | Resolution |
|----------|---------|------------|
| Medium | The Agentic regression allowed either persisted middleware state on either resume | Assert the exact state-to-resume-data transition |
| Medium | The late-error fixture emitted no data before the terminal error | Emit a partial chunk before the error |
| Medium | The nested `subGraphInterruptError` filter branch was uncovered | Add a typed nested-interrupt conversion case |
| Medium | Address fallback behavior and repeated nested node names needed explicit coverage | Add boundary and fallback attack subtests |

No true duplicate or coverage-only tests were found. The direct Runner and TurnLoop cases are intentionally paired, as are streaming and non-streaming modes.

### Coverage

- Combined `adk` and `compose` statement coverage: 88.1%.
- Changed critical functions range from 83.3% to 100%.
- Every changed function with important branching logic is above the 70% hard floor.
- `interruptOriginNodeKey` increased from 81.8% to 90.9%.

## Final Summary

## Overview

- **Iterations**: Design 1, Attack 1, Test Audit 1
- **Production files modified**: 5
- **Test files modified**: 2
- **Production and test diff**: +620 / -14 before this report

## Changes

| Stage | Result |
|-------|--------|
| Design | No blocking findings; no public API changes |
| Attack | Three high-value edge cases verified; no confirmed bugs |
| Test Audit | Strengthened middleware state assertions and boundary coverage |

The final test matrix covers direct Runner and TurnLoop execution in both streaming and non-streaming modes. The streaming case uses a named tool-call middleware, a successful stream return, a partial chunk, an ordinary terminal error, and `schema.WithErrWrapper` conversion to a second `tool.StatefulInterrupt`.

## Verification

- `go test ./... -count=1`
- `go test -race ./...`
- Focused race tests for the exact Agentic and compose checkpoint paths
- `golangci-lint run --new-from-rev=alpha/10 ./...`

All commands passed after the review fixes. No items remain deferred.
