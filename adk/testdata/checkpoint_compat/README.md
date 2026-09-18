# Checkpoint Compatibility Fixtures

`main_60e1d992` is an immutable checkpoint-format-v0 fixture set produced by
Eino commit `60e1d9929cb65c8c4814b66fba2854e29b730114` (`v0.9.18`). The fixture
generator was added by its direct child commit
`3e3e994e7b10955c336ae38a610ddbff5e371521`; that commit changed only tests and
fixture data, not the producing runtime.

The original test generator persists random UUIDs and timestamps, so rerunning
it cannot reproduce the frozen hashes. The reproduction command instead
materializes the immutable files committed by the historical generator commit,
validates every decompressed fixture hash, and adds the format and provenance
metadata introduced later. Write the result to a separate directory:

```bash
go run ./adk/testdata/checkpoint_compat/regenerate -output /tmp/checkpoint-compat-v0
```

The command refuses to overwrite the frozen directory. Its output must match
the frozen set byte for byte. Introduce a new truthfully versioned fixture
directory when a new producer or checkpoint format needs coverage.

## Publication threat model

Regeneration is a local developer operation. It assumes that no concurrent
malicious process running as the same UID can mutate the writable output parent
or staging namespace while the command runs. On Linux and Darwin, publication
is confined away from the frozen fixture tree, later operations are anchored to
opened directories, cleanup removes only identity-matching objects, and the
final destination is installed with atomic no-replace semantics.

Directory ownership begins with the first successful entry-identity capture.
POSIX provides no portable operation that both creates a directory with
`mkdirat` and returns its descriptor, so replacement between `mkdirat` and that
first identity capture or `openat` is outside this tool's guarantees. Run
regeneration only in an output parent that is not concurrently mutable by an
untrusted same-UID process.

Other platforms fail before Git or filesystem work. Windows and FreeBSD are
verified by cross-compilation while that path remains a constant,
OS-independent unsupported error; nontrivial platform-specific behavior
requires runtime CI.
