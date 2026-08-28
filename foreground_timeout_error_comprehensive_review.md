# Comprehensive Review: Structured Foreground Timeout Error

## Scope

- Base: `alpha/10`
- Branch: `fix/adk-foreground-timeout-error`
- Production packages: `adk/backgroundtask`, `adk/backgroundtask/local`, `adk/backgroundtask/tool`
- Review stages: design review, adversarial tests, test audit

## Pre-Flight

- Focused tests: passed
- Initial full repository test: one obsolete filesystem string assertion failed
- Final full repository test: passed

## Stage 1: Design Review

### Findings

| # | Dimension | Finding | Validation and Verdict | Resolution |
|---|---|---|---|---|
| 1 | API consistency | Foreground timeout used raw errors or model-facing strings across local and managed-tool paths. | Fix. Callers need one machine-readable classification without coupling to presentation text. | Added `backgroundtask.ForegroundTimeoutError` and returned it from buffered and streaming paths. |
| 2 | Determinism | `InvocationTimeoutMs` was evaluated once to create the timer and again to format the result. | Fix. A dynamic callback could report a timeout different from the one that fired. | `foregroundTimeout` now returns the timer channel and the single evaluated duration. |
| 3 | Backward compatibility | Existing callers may still use `errors.Is(err, context.DeadlineExceeded)`. | Fix. This compatibility is an explicit requirement. | `ForegroundTimeoutError.Unwrap` returns `context.DeadlineExceeded`. |

### Final Scorecard

| Dimension | Rating | Notes |
|---|---:|---|
| Concept coherence | 5/5 | One error type represents one framework-level timeout concept. |
| API usability | 5/5 | `errors.As` exposes fields; `errors.Is` preserves the old classification. |
| Minimum API surface | 5/5 | One type with `Timeout` and `TaskID`; no constructor or presentation API. |
| Backward compatibility | 4/5 | Error text and result shape intentionally change, while deadline matching remains compatible. |
| Module separation | 5/5 | The shared error lives in `adk/backgroundtask`; local and tool only construct it. |
| Cohesion | 5/5 | All production changes implement the same foreground-timeout contract. |
| Elegance | 5/5 | No new state machine or configuration was introduced. |
| Naming | 5/5 | `ForegroundTimeoutError`, `Timeout`, and `TaskID` are direct and consistent. |
| Readability | 4/5 | Timeout construction is repeated at four explicit exit points. |
| Duplication | 4/5 | Small construction duplication keeps divergent sync/stream control flow local. |
| Public documentation | 5/5 | Type, fields, compatibility, and non-persisted TaskID semantics are documented. |
| Internal comments | 4/5 | Existing control-flow comments remain sufficient; no new hidden protocol was added. |

No unresolved design findings remain.

## Stage 2: Attack Review

### Results

| # | Severity | Scenario | Test | Result |
|---|---|---|---|---|
| 1 | High | Dynamic timeout callback returns different values across calls. | `TestAttack_ForegroundTimeoutUsesOnePolicySnapshot` | Passed; callback is evaluated once. |
| 2 | High | Caller deadline is misclassified as a foreground timeout. | `TestAttack_ForegroundTimeoutDoesNotMaskCallerDeadline` in local and tool packages | Passed; raw caller error is preserved. |
| 3 | Medium | Invoke and stream paths expose different timeout metadata. | `TestManagedToolForegroundTimeoutReturnsTypedError` | Passed for both paths. |

All existing attack tests in the affected packages also passed. No confirmed bugs
remain.

## Stage 3: Test Audit

### Findings

| # | Category | Finding | Verdict | Resolution |
|---|---|---|---|---|
| 1 | Logical grouping | Managed-tool invoke and stream assertions were sequential in one test body. | Fix. They are intentional parity cases and benefit from separate failure names. | Split them into `invoke` and `stream` subtests with one strong typed-error assertion helper. |
| 2 | Assertion quality | Filesystem integration was coupled to `"timed out after 1000ms"`. | Fix. Exact structured fields are the contract. | Asserted `errors.Is`, `errors.As`, `Timeout`, and non-empty `TaskID`. |
| 3 | Semantic value | Direct `Error`/`Unwrap` test could appear trivial. | Keep. It protects the public compatibility contract independently of wrappers. | Retained `TestForegroundTimeoutError`. |

Coverage run:

- `adk/backgroundtask`: 84.8%
- `adk/backgroundtask/local`: 78.4%
- `adk/backgroundtask/tool`: 76.7%
- `adk/middlewares/filesystem`: 88.8%
- Combined selected packages: 82.4%
- All new error methods and all four changed timeout exits were covered.
- The only uncovered changed helper branch is the inherited non-positive timeout
  disable path; the helper remains above the 70% function floor at 75%.

No high-priority test findings remain.

## Final Summary

- Review iterations: design 1, attack 1, test audit 1
- Public API: added `backgroundtask.ForegroundTimeoutError`
- Compatibility: `errors.Is(err, context.DeadlineExceeded)` remains true
- Structured metadata: configured `Timeout` and pre-allocated `TaskID`
- Behavioral parity: local buffered, local streaming, managed invoke, and managed
  stream use the same timeout error
- Presentation: no model-facing timeout sentence is generated by these layers
- Verification: focused tests, all attack tests, coverage run, and `go test ./...`
  passed after updating obsolete assertions
- Remaining items: none
