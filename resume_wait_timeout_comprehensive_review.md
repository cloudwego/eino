# Comprehensive Review: ResumeWaitTimeout (uncommitted changes)

## Pre-Flight
- Files in scope: `adk/turn_loop.go` (+~250 LOC), `adk/turn_loop_test.go` (+~600 LOC)
- Baseline: `go build ./...` OK; `go test ./adk/ -run TestTurnLoop` OK; new tests pass under `-race`.
- Feature: a new `ResumeWaitTimeout` config that bounds how long a managed business
  interrupt (`TurnLoopInterruptWaitsForExplicitResume`) waits for `Resume(...)`.
  On expiry the loop persists the runner checkpoint and exits with `*InterruptError`.
  Also adds: pre-load `Resume()` buffering, `InterruptContexts` carried in the
  checkpoint, and a restored-session watcher.

---

## Stage 1: Design Review

### Iteration 1 — Scorecard

| # | Dimension | Rating | Notes |
|---|-----------|--------|-------|
| 1 | Concept coherence | ⭐⭐⭐⭐⭐ | `ResumeWaitTimeout` reads naturally beside `InterruptMode`; "bounded wait → persist + InterruptError" is a clean concept. |
| 2 | API usability | ⭐⭐⭐⭐⭐ | Single `time.Duration` field, zero = unbounded (matches Go idiom). Doc comment states the no-op-unless-managed precondition. |
| 3 | Minimum API surface | ⭐⭐⭐⭐⭐ | Only one new public field. All other machinery is unexported. |
| 4 | Backward compatibility | ⭐⭐⭐⭐ | Zero value preserves old unbounded behavior. New `InterruptContexts` checkpoint field decodes to nil on old data. See F1 (gob risk). |
| 5 | Module separation | ⭐⭐⭐⭐⭐ | All within turn_loop.go; no layer leakage. |
| 6 | Cohesion vs tension | ⭐⭐⭐⭐ | Watcher↔cleanup↔takePendingResume coordination via `timerCancel`/`timedOut` is inherently distributed but well-commented. See F2. |
| 7 | Elegance vs complexity | ⭐⭐⭐⭐ | The pre-load Resume adoption defer + 3-case switch is the most accidental-feeling complexity. Justified but dense. See F3. |
| 8 | Naming | ⭐⭐⭐⭐⭐ | `interruptCtxSnapshot` deliberately distinct from `interrupted` and `l.interruptContexts`; `timerCancel`, `timedOut`, `closeTimerCancelLocked` all clear. |
| 9 | Readability | ⭐⭐⭐⭐ | Watcher double-check race handling is subtle but heavily commented. |
| 10 | Duplication | ⭐⭐⭐ | The arm-watcher block is duplicated verbatim between Phase 2 (run) and `armRestoredManagedWatcherIfNeeded`. See F4. |
| 11 | Public API docs | ⭐⭐⭐⭐⭐ | `ResumeWaitTimeout` doc covers expiry behavior, push-no-reset, zero-default, precondition. |
| 12 | Internal comments | ⭐⭐⭐⭐⭐ | Exceptionally thorough on the concurrency-sensitive paths. |

### Findings

- **F1 (gob durability of `InterruptContexts`)** — `nice-to-have/doc`: `turnLoopCheckpoint.InterruptContexts []*InterruptCtx` is gob-encoded. `InterruptCtx.Info` is `any`. If a real interrupt carries a non-gob-registered concrete type in `Info`, `saveTurnLoopCheckpoint` fails → surfaces as `CheckpointErr`. The runner checkpoint (`resumeBytes`) already encodes the same interrupt info, so this is partially redundant. Verdict pending.
- **F2 (watcher commitStop ordering)** — verify in Stage 2 (attack): watcher releases `resumeMu` then calls `commitStop()`; a `Resume()` racing in between. Move to attack tests rather than design fix.
- **F3 (pre-load adoption switch)** — `nice-to-have`: dense but each branch is commented and tested. Counter-argue likely "won't fix".
- **F4 (duplicated arm-watcher block)** — candidate fix: extract a small `armResumeWaitWatcherLocked` helper used by both the Phase 2 site and `armRestoredManagedWatcherIfNeeded`.

### 1.2 Validate & Counter-Argue

- **F1**: Real but low-severity. The pre-existing `cancel.go` path (`InterruptError` already carries `[]*InterruptCtx`) and the runner checkpoint already rely on the same `Info any` being serializable in practice, so this introduces no *new* class of failure beyond what resumable interrupts already require. Adding the contexts to the TurnLoop checkpoint is what lets a restored session re-synthesize the error (Test #13). **Verdict: Won't Fix** (consistent with existing serialization assumptions); no code change.
- **F2**: Not a design issue — defer to Stage 2 attack tests. **Verdict: Defer to Stage 2.**
- **F3**: Extracting would scatter the tightly-coupled branch logic across functions and hurt readability; it is exercised by Tests #9/#10/#12. **Verdict: Won't Fix.**
- **F4**: Genuine duplication of a 6-line block with identical guard semantics. Extracting a `*Locked` helper removes the duplication without changing behavior and centralizes the arming invariant. **Verdict: Fix.**

### 1.3 Fix — F4

Extracted `armResumeWaitWatcherLocked(pr) (shouldArm bool)` (caller holds `resumeMu`), used by both the Phase 2 interrupt site and `armRestoredManagedWatcherIfNeeded`.

### 1.5 Loop decision: all dimensions >= 4/5, single fix applied. Proceed to Stage 2.

---

## Stage 2: Attack Review

### Iteration 1 — attack tests (`adk/turn_loop_attack_test.go`)

| # | Severity | Probe | Test | Result |
|---|----------|-------|------|--------|
| 1 | green | Resume vs timeout watcher race (50 iters) | `TestAttack_ResumeRacesTimeoutWatcher` | Always nil OR *InterruptError |
| 2 | green | Resume after timeout committed Stop | `TestAttack_ResumeAfterTimeoutFired` | Returns `ErrTurnLoopStopped` |
| 3 | green | Watcher goroutine leak (Stop/Resume/timeout) | `TestAttack_NoWatcherGoroutineLeak` | No leak |
| 4 | green | Concurrent pre-load Resume (16 goroutines) | `TestAttack_ConcurrentPreLoadResume` | Exactly 1 accepted, 15 in-progress |
| 5 | green | Pre-load Resume vs checkpoint accepted resume | `TestAttack_PreLoadResumeLosesToAcceptedCheckpointResume` | Checkpoint wins |
| 6 | green | Timeout still persists checkpoint | `TestAttack_TimeoutWithNilInterruptContexts` | Checkpoint attempted, no err |
| 7 | green | Stop vs timeout race (40 iters) | `TestAttack_StopAndTimeoutRace` | Deterministic, checkpoint persisted |
| 8 | green | Context cancel during resume wait | `TestAttack_ContextCancelDuringWait` | Prompt exit |

All probes PASS under `-race`. Zero confirmed bugs. No production fixes required.

F2 (watcher sets timedOut, releases lock, then Resume races before commitStop)
is resolved by the existing design: cleanup gates the synthesized error on
`pending.timedOut && !pending.resumeSubmitted`, so a Resume that wins sets
resumeSubmitted and suppresses the timeout error. Verified by probe #1.

### 2.6 Loop decision: zero confirmed bugs. Proceed to Stage 3.


---

## Stage 3: Test Audit

### Findings (PR tests in turn_loop_test.go)

| Priority | Issue | Verdict | Action |
|----------|-------|---------|--------|
| High | Test #11 `NonManagedRestore_PreRunPushStillLegacy` re-invokes an existing test under a new name (no new coverage, misleading name) | Fix | Deleted |
| Medium | drain-then-stop `OnAgentEvents` + fresh `PrepareAgent` duplicated across #8/#9/#10/#12 | Fix | Extracted `freshStopPrepareAgent()` and `drainAndStop` helpers |
| Low | #4 uses `assert.GreaterOrEqual(elapsed, timeout/2)` loose lower bound | Won't Fix | Intentional timing tolerance under -race |
| Low | #6 vs #8 both assert "parked → Resume releases" | Won't Fix | Distinct paths (live vs restore); intentional pair |

### Coverage (new production code, via TestTurnLoop + attack tests)

| Function | Coverage |
|----------|----------|
| armResumeWaitWatcherLocked | 100% |
| armRestoredManagedWatcherIfNeeded | 100% |
| closeTimerCancelLocked | 100% |
| cleanup | 100% |
| tryLoadCheckpoint | 93.4% |
| Resume | 90.5% |
| watchResumeWait | 85.7% (remaining = nondeterministic post-lock race re-check) |

Diff coverage exceeds the 85% target on meaningful paths. Full `go test ./adk/`
passes (33.9s); TurnLoop subset passes under `-race`.

### 3.5 Loop decision: no High findings remain. Proceed to Stage 4.

---

## Stage 4: Final Summary

### Overview
- Iterations: Stage 1: 1, Stage 2: 1, Stage 3: 1 (no safety valves triggered)
- Production files modified: 1 (`adk/turn_loop.go`)
- Test files modified: 1 (`adk/turn_loop_test.go`)
- Net change vs baseline of the PR: +1104 / -9

### Stage 1 (Design) — change applied
| # | Dimension | Finding | Fix | File |
|---|-----------|---------|-----|------|
| F4 | Duplication | Arm-watcher 6-line block duplicated between Phase 2 and restored-watcher | Extracted `armResumeWaitWatcherLocked(pr) bool` helper; both sites call it | `adk/turn_loop.go` |

F1/F2/F3 examined and resolved as Won't-Fix / Defer with recorded rationale.

### Stage 2 (Attack) — bugs found
Zero confirmed bugs. 9 adversarial tests written; all pass under `-race`.
Per user decision, all 9 were merged into `adk/turn_loop_test.go` as durable
regression tests (concurrency hardening section).

### Stage 3 (Test Audit) — changes applied
| # | Category | Change | LOC |
|---|----------|--------|-----|
| 1 | Semantic value | Deleted Test #11 (re-invoked an existing test under a new name) | -6 |
| 2 | Boilerplate | Extracted `freshStopPrepareAgent()` + `drainAndStop`; applied to #8/#9/#10/#12 | net negative inline |

### Cumulative file change list
| File | Stage(s) | Summary |
|------|----------|---------|
| `adk/turn_loop.go` | 1 | Added `armResumeWaitWatcherLocked` helper; Phase 2 + restored-watcher now share it. No behavior change. |
| `adk/turn_loop_test.go` | 3 | Deleted noise test; extracted 2 test helpers; merged 9 race-hardening attack tests. |

### Verification (final)
- `go build ./...` OK
- `gofmt -l` clean on both files
- `go test ./adk/` full suite OK (33.8s)
- TurnLoop + attack subset OK under `-race`
- New-code coverage: all new functions 85–100%

### Remaining items
None. No safety valves triggered.
