# PR #1234 Comprehensive Review - Round 3

## Result

**APPROVE.** A complete independent review of the current effective change from
merge base `60e1d992` through `HEAD` plus the existing Round 2 worktree
hardening found no new actionable issue or improvement.

- Scope reviewed: 38 files, +10,296/-42 lines, excluding this report.
- Round 3 iterations: design 1, attack 1, test audit 1.
- Remaining items: none.
- Production, existing tests, and checklist files were not modified.

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
