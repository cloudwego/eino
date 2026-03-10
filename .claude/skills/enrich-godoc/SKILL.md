---
name: enrich-godoc
description: Enrich godoc comments for Go packages in the Eino project using cloudwego.io as the source of truth. Use when the user asks to enrich, improve, or write godoc for a package or set of files.
argument-hint: [package-path-or-file]
allowed-tools: Read, Grep, Glob, Bash, Write, Edit
---

# Godoc Enrichment Process

You are enriching godoc comments for the Eino project (`github.com/cloudwego/eino`).
The target is $ARGUMENTS (if empty, ask the user which package or file to enrich).

## Step 1 — Discover the official documentation URL

Check `llms.txt` in the repo root for the relevant doc URL(s):

```
Glob("llms.txt") then Read it, search for lines matching the package topic
```

Also check if `CLAUDE.md` or memory files mention doc URLs for this package.

## Step 2 — Fetch the official docs

For each relevant URL found:

```bash
curl -s "<URL>" | python3 -c "
import sys, re
html = sys.stdin.read()
html = re.sub(r'<script[^>]*>.*?</script>', '', html, flags=re.DOTALL)
html = re.sub(r'<style[^>]*>.*?</style>', '', html, flags=re.DOTALL)
text = re.sub(r'<[^>]+>', ' ', html)
text = re.sub(r'[ \t]+', ' ', text)
text = re.sub(r'\n\s*\n', '\n\n', text)
# Skip nav, find main content
start = max(text.find('##'), text.find('Overview'), 0)
print(text[start:start+12000])
"
```

Fetch in multiple chunks if needed (offset the start) to get all sections including best practices, pitfalls, and code examples.

## Step 3 — Read all target Go files

Read every `.go` file in the target package. Pay special attention to:
- `doc.go` — package-level overview
- `interface.go` — primary exported interfaces
- `option.go` — option types and helpers
- Any other files with exported symbols

## Step 4 — Map doc concepts to exported symbols

For each exported symbol (type, function, method, constant, variable), identify:
1. What the docs say about it (purpose, constraints, pitfalls, examples)
2. What is currently in the godoc (may be empty, minimal, or incorrect)
3. What should be added or changed

Prioritize:
- **Pitfalls and gotchas** — things that cause bugs or confusion (e.g. "must close", "not thread-safe", "read-once")
- **Non-obvious constraints** — e.g. "same model for index and retrieval", "no mutation in handlers"
- **When to use which** — when multiple options exist, explain the trade-offs
- **Concrete examples** — short, compilable snippets showing idiomatic use
- **Cross-references** — link related types with `[TypeName]` godoc links

## Step 5 — Write the enriched comments

Rules:
- **Do not change any code** — only modify comments
- **Preserve existing correct content** — add to it, don't replace good explanations
- **Fix incorrect content** — if the existing comment contradicts the docs, fix it
- **Use Go godoc style**: complete sentences, start with the symbol name, use `//` not `/* */` except for package doc
- **Use `#` headings** for multi-section package docs (rendered in pkg.go.dev)
- **Use `[SymbolName]` links** for cross-references within the same package
- **Keep examples short** — 5–15 lines, showing the single most important usage
- **Separate `jsonschema_description` from `jsonschema` tags** — remind in tool utility docs
- **Naming**: `CamelCase` for GetType() values per `components.Typer` convention

## Step 6 — Verify

```bash
go build ./path/to/package/...
```

Fix any compilation errors introduced (should be none for comment-only changes).

## Step 7 — Summary

Report what was changed and why, calling out:
- Any inaccuracies in the original comments that were corrected
- Key concepts that were missing and are now documented
- Any questions or ambiguities that could not be resolved from the docs alone

## Conventions established in prior enrichment passes

These patterns were confirmed correct in previous sessions:

- **StreamReader**: read-once, must close even after EOF, use `.Copy(n)` for fan-out
- **Fake stream**: T boxed into single-chunk StreamReader[T] by the framework when bridging paradigms — does NOT reduce latency
- **Callback stream handlers**: MUST close their StreamReader copy or the entire pipeline leaks
- **Handler context flow**: ctx flows between timings of the SAME handler (OnStart→OnEnd), NOT between different handlers; no guaranteed order between handlers
- **Input/Output mutation**: NEVER mutate CallbackInput/CallbackOutput — shared pointer, causes data races
- **AppendGlobalHandlers**: NOT thread-safe, init-only
- **RunInfo.Name**: empty string if not set by user; standalone components need InitCallbacks
- **jsonschema_description tag**: must be a separate tag from `jsonschema:"required"` to avoid comma-parsing bugs
- **WrapImplSpecificOptFn / GetImplSpecificOptions**: the standard pattern for per-component option extension
- **ScoreThreshold**: filters by value, does NOT sort — retriever returns results in its own order
- **Embedding model consistency**: same model must be used for both indexing and retrieval
