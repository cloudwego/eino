# PR #1234 Comprehensive Review - Round 3

## Result

**APPROVE.** A complete independent review of the current effective change from
merge base `60e1d992` through `HEAD` plus the existing Round 2 worktree
hardening found no new actionable issue or improvement.

- Scope reviewed: 38 files, +10,296/-42 lines, excluding this report.
- Round 3 iterations: design 1, attack 1, test audit 1.
- Remaining items: none.
- Production, existing tests, and checklist files were not modified.

## Fresh-Round Reset Audit Trail

| Round | Reset evidence | Retained result |
|---|---|---|
| 1 | The parent of `5b61af62` has no report; `5b61af62` first adds the report after the completed design, attack, and test-audit loops. | Only the cumulative validated fixes and clear result entered history. |
| 2 | Completed Tasks 2-4 each record clearing the previous report before a fresh full-scope discipline review. The resulting hardening is the Round 2 worktree change later committed by `3aebf546`; no intermediate Round 2 report is retained. | Only accepted production and test changes feed the cumulative summary below. |
| 3 | Completed Task 5 records another clear before the final full review. `git diff 5b61af62..3aebf546 -- pr_1234_comprehensive_review.md` shows the prior report removed and replaced by this Round 3 report. | One design, one attack, and one test-audit pass found no new actionable item; only this final clear round remains. |

## Cumulative Change Summary

The current diff:

1. Freezes 14 legacy checkpoint fixtures and validates current-reader resume,
   legacy-reader compatibility, fixture hashes, targeted resume, cancellation,
   streaming, nested AgentTools, parallel children, and representative payload
   fields.
2. Introduces a versioned sparse Compose checkpoint layout in which interrupt
   state has one checkpoint-tree owner while routing addresses remain a separate
   root index. Ownership is derived from the checkpoint tree, never reconstructed
   from filtered `Address` segments.
3. Persists versioned compact ToolsNode state, references unique tool-call
   sources when profitable, deep-clones hydrated calls, validates exact
   executed/rerun partitions, and preserves legacy state reads.
4. Adds deterministic checkpoint value walking and transformation APIs so ADK
   can inspect and rewrite logical values without exposing Compose wire structs.
5. Projects duplicate ADK messages and tool results onto canonical Compose
   values, validates sentinels, versions, coordinates, counts, digests, kinds,
   and target conflicts before hydration, and writes projected bytes only when
   they are smaller.
6. Preserves unsupported-forward-format failure semantics while keeping legacy
   and current-version logical state available to migration callbacks.

## Accepted Review-Commit Design Changes

Review commit `3aebf546` introduced the following accepted production design
changes. Comment-only checkpoint-schema annotations and test-only changes are
excluded.

| Finding/change | Exposed API or conceptual cost | Checkpoint-size impact | Compatibility impact | Counterargument | Why accepted |
|---|---|---|---|---|---|
| Persist a Runner projection only when its encoded bytes are smaller than the unprojected form. | No API growth; privately encodes two candidates, adding save-time CPU and temporary memory. | Prevents projection metadata from enlarging small checkpoints; retained projection measured 393,621 bytes at 320 KiB and 1,114,521 bytes at 1 MiB. | Current readers already accept projected and unprojected forms; logical resume is unchanged. | Always projecting is simpler and encodes once. | The measured small-payload regression made a size-profitability gate necessary, while large payloads retain the linear-size benefit. |
| Validate all Projection V1 target coordinates and exactly-one-of source/inline/nil payload forms before hydration, using typed coordinate keys for duplicate tool-result targets. | No API growth; adds private validators and stricter V1 invariants. | Byte-neutral. | Valid V1 and legacy data are unchanged; malformed or ambiguous V1 data now fails before partial mutation. | Ignoring unused fields can appear more forward-compatible. | Those fields select hydration targets; accepting contradictory forms or aliased coordinates can silently restore data to the wrong location. |
| Keep Compose agentic-message slices containing nil elements inline instead of projecting them. | No API growth; adds one private projection eligibility rule. | Such slices forgo deduplication and may be larger, but remain linear and use the existing inline form. | Preserves nil elements and Gob round trips; non-nil slices still project. | Add another placeholder encoding so nil-bearing slices can also deduplicate. | The extra wire concept was not justified for a case Gob cannot safely re-encode after hydration; inline fallback is simpler and lossless. |
| Preflight checkpoint-tree metadata and ToolsNode state versions before migration, walk, or transform callbacks. | Public signatures are unchanged; callbacks now have one explicit fail-before-callback format gate. | Byte-neutral. | Valid legacy/current checkpoints still expose hydrated logical values; nil or unsupported forward state fails deterministically before callbacks run. | Migration callbacks could be allowed to inspect unknown versions. | Unknown compact state cannot be hydrated safely, and the documented compatibility policy requires unsupported forward formats to fail loudly. |
| Require every compact ToolsNode call to belong to exactly one executed or rerun partition, with no unknown result or rerun IDs. | No API growth; tightens one private persisted-state invariant. | Byte-neutral. | Valid legacy and V1 resumes are unchanged; corrupt or incomplete V1 state is rejected instead of being inferred. | Missing classifications could default to rerun. | Inference can duplicate an already executed side effect or omit required work; exact partition evidence is required for safe resume. |
| Deep-clone ToolCalls hydrated from their canonical graph-state message. | No API growth; adds serializer work and a possible explicit clone error. | Persisted bytes are unchanged. | Logical values are unchanged, but callback/runtime mutation can no longer alias canonical checkpoint state. | A slice copy is faster if nested ToolCall data is treated as immutable. | ToolCalls contain nested mutable values; the alias attack proved a shallow copy does not preserve checkpoint ownership. |
| Route a single restored ToolsNode task through the same executed-task guard as multi-task parallel dispatch. | No API or wire growth; removes a special case. | Byte-neutral. | Fresh execution is unchanged; a restored single executed tool is no longer invoked again in Stream mode. | The one-task fast path avoids scheduler bookkeeping. | The fast path bypassed the restored `executed` check; removing it is simpler and prevents duplicate tool side effects. |

## Design Review

| Dimension | Rating | Round 3 conclusion |
|---|---:|---|
| Concept coherence | 5/5 | Layout ownership, routing, compact state, and projection have distinct roles. |
| API usability | 4/5 | Traversal callbacks are explicit, deterministic, nil-safe for serializers, and return original bytes on no-op transforms. |
| Minimum API surface | 4/5 | External growth is limited to two functions, two types, and five kind constants needed for the ADK-to-Compose boundary. |
| Backward compatibility | 5/5 | Frozen V0 fixtures resume; V1 types fail loudly in the legacy reader; legacy wire fields remain readable. |
| Module separation | 4/5 | Compose owns checkpoint traversal and layout; ADK owns projection; shared wire-only ToolsNode types remain under `internal`. |
| Cohesion vs. tension | 4/5 | Sparse ownership and projection cooperate without coupling ownership to runtime address filtering. |
| Elegance vs. complexity | 4/5 | Projection is complex, but remains private and is gated by measured byte profitability. |
| Naming | 5/5 | Public names consistently identify checkpoint values, locations, walking, and transformation. |
| Readability | 4/5 | Projection validation/hydration is the main hotspot; typed targets and focused helpers keep invariants reviewable. |
| Duplication | 4/5 | Typed/untyped and Invoke/Stream parallels are intentional compatibility paths with parity tests. |
| Public API documentation | 5/5 | Ordering, metadata exclusion, read-only inputs, unknown kinds, nil serializers, and no-op behavior are documented. |
| Internal comments | 4/5 | Persisted schemas and non-obvious projection/ownership invariants are marked at their implementation points. |

### Public API Assessment

| Name | Assessment |
|---|---|
| `CheckpointValueKind` | Necessary extensible discriminator. |
| `CheckpointValueState` | Clear graph-state kind. |
| `CheckpointValueInput` | Clear persisted-input kind. |
| `CheckpointValueChannel` | Clear channel-value kind. |
| `CheckpointValueInterruptState` | Clear component-state kind. |
| `CheckpointValueInterruptLayerPayload` | Precise despite length. |
| `CheckpointValueLocation` | Minimal coordinates for all supported values. |
| `WalkCheckpointValues` | Read-only deterministic traversal with logical hydration. |
| `TransformCheckpointValues` | Explicit replacement contract and no-op byte preservation. |

Top actionable recommendations: none.

## Adversarial Review

| Category | Evidence | Result |
|---|---|---|
| Data corruption | Deep-clone, alias, digest, missing-reference, and truncation attacks | Pass |
| Validation gaps | Sentinel, version, coordinate, payload-form, role, and partition attacks | Pass |
| Conflict detection | Duplicate owners, routes, targets, tool IDs, and result-kind conflicts | Pass |
| Boundary values | Nil values, empty call IDs, nested depth/width, and 1 MiB payloads | Pass |
| Type safety | Gob schema evolution, invalid concrete types, and schema/agentic kind separation | Pass |
| Feature interaction | Sparse state, nested AgentTools, compact ToolsNode references, targeted resume | Pass |
| Determinism | Repeated source-selection and first-error ordering tests | Pass |
| Error quality | Exact forward-version and corruption diagnostics | Pass |
| Streaming parity | ToolsNode and nested parallel Invoke/Stream paths | Pass |
| Runtime overrides | Temporary resume-with-`WithToolList` probe | Pass |
| Compatibility | 14 frozen fixtures plus old-reader acceptance/rejection checks | Pass |

Three temporary Round 3 probes confirmed current-version logical corruption
remains migratable, ownership is independent of filtered address segments, and
resume honors runtime tool overrides. All passed and the temporary file was
removed.

## Test Audit

| Dimension | Result |
|---|---|
| Duplicates | No true or near-duplicate test with removable semantic coverage. |
| Assertion quality | New assertions check exact values/errors where deterministic; size assertions intentionally enforce bounds. |
| Boilerplate | Repeated setup is already concentrated in fixture, serializer, and resume helpers. |
| Logical grouping | Feature-first table tests and Invoke/Stream subtests are coherent. |
| Semantic value | Every added test protects compatibility, integrity, size, or resume behavior. |
| Coverage gaps | No important changed branch lacks semantic coverage. |

Coverage from permanent tests:

| Scope | Coverage |
|---|---:|
| Added production statements | 92.6% (1,567/1,692) |
| `adk` package | 90.8% |
| `compose` package | 89.2% |
| `internal/core` package | 85.4% |

All changed functions with substantive branching meet the 70% hard floor.

## Verification

Local delivery verification completed on 2026-09-07 from branch
`fix/agenttool-checkpoint-linear-size` at `5b61af62`, with merge base
`60e1d9929cb65c8c4814b66fba2854e29b730114`.

- Scope inspection: 18 dirty tracked files, all within the existing PR
  production/test set plus this report; no untracked files, conflict markers,
  unintended files, or temporary artifacts.
- `gofmt -d` over all 22 existing changed Go files in the merge-base-to-worktree
  diff: no output.
- `git diff --check`: pass. `git diff origin/main --check`: pass.
- `go vet ./adk ./compose ./internal/core`: pass.
- `go test ./... -count=1`: pass (`adk` 64.130s, `compose` 35.023s).
- `go test -race ./adk/... ./compose/... ./internal/core -count=1`: pass
  (`adk` 80.960s, `compose` 30.862s, `internal/core` 1.707s).
- `GOTOOLCHAIN=go1.18.10 go test ./adk ./compose ./internal/core -count=1`:
  pass (`adk` 64.475s, `compose` 30.974s, `internal/core` 1.106s).
- `golangci-lint run --new-from-rev=origin/main ./...`: pass with `0 issues`.
  It emitted one non-fatal generated-file-filter warning for a deleted sibling
  worktree cache path.
- `go test ./... -run '^TestAttack_' -count=10`: pass; all 76 repository
  `TestAttack_*` tests were repeated ten times.
- Focused size and projection-profitability tests: pass. The 320 KiB
  AgentTool checkpoint was 393,621 bytes, the 1 MiB case was 1,114,521 bytes,
  and depth 0-3 measured 357,224, 393,623, 444,290, and 526,702 bytes.
- Focused compatibility tests: pass, including all 14 frozen fixtures with the
  current and legacy readers, Gob schema evolution, forward-format failures,
  layout metadata, migration, and rerun-input compatibility.
- Local API compatibility: pass. A temporary two-commit snapshot repository
  included all dirty tracked changes, ran `go mod tidy` with Go 1.22.12, and
  `go-apidiff` found no incompatible changes against merge base `60e1d992`.
  The temporary repository was removed.

## Remaining Items

None.
