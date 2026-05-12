# SPEC: Fix TODO struct registration ordering in adk/prebuilt/deep/deep.go

## Problem
In `adk/prebuilt/deep/deep.go`, the `init()` function calls `schema.RegisterName[TODO](...)` and
`schema.RegisterName[[]TODO](...)` using the identifier `TODO` — but the `TODO` struct is defined
later in the same file. While Go allows forward references within a package, having `schema.RegisterName[TODO]`
at the top of the file before `type TODO struct` is defined is confusing for readers who don't know whether
`TODO` is the Go built-in placeholder keyword or an actual type.

Additionally, there is no comment explaining *why* these gob names are registered — this is important
for checkpoint compatibility (changing the name would break existing saved checkpoints).

## Acceptance criteria
- [ ] The `TODO` struct definition appears before the `init()` function that references it, or
      alternatively the `init()` is moved to the same location where `TODO` is defined.
- [ ] A short doc comment explains that the registered names are checkpoint-compatibility identifiers
      that must not be changed.
- [ ] All existing tests continue to pass (`go test ./adk/prebuilt/deep/...`).
- [ ] No lint errors introduced.

## Approach
1. Move `type TODO struct` and `type writeTodosArguments struct` to the top of the file (after imports),
   before the `init()` function, so the code reads in declaration order.
2. Add a comment above `init()` explaining the checkpoint gob names.

## Files likely touched
- `adk/prebuilt/deep/deep.go`

## Risk / blast radius
- Low. Pure code reordering + comment addition. No logic changes. Checkpoint names unchanged.

## Test plan
- `go vet ./adk/prebuilt/deep/...`
- `go build ./adk/prebuilt/deep/...`
- `go test ./adk/prebuilt/deep/...` (if tests exist)
