#!/usr/bin/env bash
# eino_setup.sh — fetch eino framework source into your project for AI-assisted development.
#
# BACKGROUND
#   When building applications with eino, your AI coding assistant (Claude Code,
#   Cursor, Copilot, etc.) only sees your code. It cannot navigate into eino's
#   source to understand how components work, what patterns are idiomatic, or
#   how to wire things together correctly.
#
#   This script clones eino, eino-ext, and eino-examples into a _eino/ directory
#   inside your project. Your AI assistant can then browse the actual source,
#   examples, and extensions — giving it full context to help you build correctly.
#
# WHAT IT DOES
#   1. Clones eino        → _eino/eino/
#   2. Clones eino-ext    → _eino/eino-ext/
#   3. Clones eino-examples → _eino/eino-examples/
#   4. Adds _eino/ to .gitignore (read-only reference, never committed)
#   5. Writes a _eino/README.md explaining the directory to future readers
#
# RESULTING LAYOUT
#   your-project/
#   ├── _eino/
#   │   ├── eino/          ← github.com/cloudwego/eino (core framework)
#   │   ├── eino-ext/      ← github.com/cloudwego/eino-ext (components & integrations)
#   │   └── eino-examples/ ← github.com/cloudwego/eino-examples (patterns & recipes)
#   └── ... your code
#
# NOTE: _eino/ is read-only reference material. Do not edit files inside it.
#       Your go.mod is unchanged — eino remains a normal dependency.
#
# KEEPING UP TO DATE
#   bash eino_setup.sh --update    # pull latest on all three repos
#
# USAGE
#   bash eino_setup.sh             # first-time setup
#   bash eino_setup.sh --reset     # re-clone everything from scratch
#   bash eino_setup.sh --update    # pull latest without re-cloning
#
# SYSTEM PROMPT
#   After running this script, add the following to your AI assistant's project
#   instructions (CLAUDE.md, .cursorrules, .github/copilot-instructions.md, etc.):
#
#   ---
#   ## eino Framework Reference
#
#   This project uses the eino framework (github.com/cloudwego/eino).
#   The full framework source is available locally in `_eino/`:
#
#   - `_eino/eino/`          — core framework (components, graph, compose, callbacks)
#   - `_eino/eino-ext/`      — official components and integrations (models, tools, retrievers, etc.)
#   - `_eino/eino-examples/` — working examples and patterns
#
#   When answering questions about eino APIs, component wiring, graph construction,
#   callbacks, or any eino-specific patterns: explore `_eino/` first.
#   Prefer examples from `_eino/eino-examples/` as the canonical reference for
#   idiomatic usage.
#   ---

set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$PROJECT_ROOT"

EINO_DIR="_eino"
EINO_REPO="https://github.com/cloudwego/eino"
EINO_EXT_REPO="https://github.com/cloudwego/eino-ext"
EINO_EXAMPLES_REPO="https://github.com/cloudwego/eino-examples"

# Parse flags
RESET=false
UPDATE=false
for arg in "$@"; do
  case $arg in
    --reset)  RESET=true ;;
    --update) UPDATE=true ;;
  esac
done

echo "==> eino setup in: $PROJECT_ROOT"

# --reset: remove and re-clone
if [ "$RESET" = true ]; then
  echo "==> --reset: removing $EINO_DIR/"
  rm -rf "$EINO_DIR"
fi

# --update: pull latest on existing clones
if [ "$UPDATE" = true ]; then
  for repo in eino eino-ext eino-examples; do
    dir="$EINO_DIR/$repo"
    if [ -d "$dir/.git" ]; then
      echo "==> Updating $dir/"
      git -C "$dir" pull --ff-only
    else
      echo "==> $dir/ not found, skipping update (run without --update to clone)"
    fi
  done
  echo ""
  echo "Done. Run 'bash eino_setup.sh' to clone any missing repos."
  exit 0
fi

mkdir -p "$EINO_DIR"

# Clone repos (shallow — we only need source to read, not full history)
clone_if_missing() {
  local repo_url="$1"
  local dest="$2"
  if [ ! -d "$dest/.git" ]; then
    echo "==> Cloning $(basename "$dest")/"
    git clone --depth=1 "$repo_url" "$dest"
  else
    echo "==> $dest/ already exists, skipping clone"
  fi
}

clone_if_missing "$EINO_REPO"          "$EINO_DIR/eino"
clone_if_missing "$EINO_EXT_REPO"      "$EINO_DIR/eino-ext"
clone_if_missing "$EINO_EXAMPLES_REPO" "$EINO_DIR/eino-examples"

# Add _eino/ to .gitignore
GITIGNORE=".gitignore"
if ! grep -qxF "$EINO_DIR/" "$GITIGNORE" 2>/dev/null; then
  echo "" >> "$GITIGNORE"
  echo "# eino framework source (AI coding reference — see eino_setup.sh)" >> "$GITIGNORE"
  echo "$EINO_DIR/" >> "$GITIGNORE"
  echo "==> Added '$EINO_DIR/' to $GITIGNORE"
fi

# Write a README so the directory is self-explanatory
cat > "$EINO_DIR/README.md" <<'EOF'
# _eino — eino framework source reference

This directory contains read-only clones of the eino framework repositories,
checked out for use by AI coding assistants (Claude Code, Cursor, Copilot, etc.).

| Directory      | Repository                              | Purpose                        |
|----------------|-----------------------------------------|--------------------------------|
| `eino/`        | github.com/cloudwego/eino               | Core framework source          |
| `eino-ext/`    | github.com/cloudwego/eino-ext           | Components and integrations    |
| `eino-examples/` | github.com/cloudwego/eino-examples    | Patterns, recipes, and samples |

**Do not edit files here.** This directory is in `.gitignore` and is never committed.

To update to the latest:

    bash eino_setup.sh --update

To re-clone from scratch:

    bash eino_setup.sh --reset
EOF

echo ""
echo "Done. Your AI assistant now has full eino context in $EINO_DIR/:"
echo "  $EINO_DIR/eino/          — core framework ($(find "$EINO_DIR/eino" -name "*.go" | wc -l | tr -d ' ') .go files)"
echo "  $EINO_DIR/eino-ext/      — components & integrations ($(find "$EINO_DIR/eino-ext" -name "*.go" | wc -l | tr -d ' ') .go files)"
echo "  $EINO_DIR/eino-examples/ — patterns & recipes ($(find "$EINO_DIR/eino-examples" -name "*.go" | wc -l | tr -d ' ') .go files)"
echo ""
echo "Add the following to your AI assistant's system prompt or project instructions"
echo "(e.g. CLAUDE.md, .cursorrules, .github/copilot-instructions.md):"
echo ""
echo "---"
cat <<'PROMPT'
## eino Framework Reference

This project uses the eino framework (github.com/cloudwego/eino).
The full framework source is available locally in `_eino/`:

- `_eino/eino/`          — core framework (components, graph, compose, callbacks)
- `_eino/eino-ext/`      — official components and integrations (models, tools, retrievers, etc.)
- `_eino/eino-examples/` — working examples and patterns

When answering questions about eino APIs, component wiring, graph construction,
callbacks, or any eino-specific patterns: explore `_eino/` first.
Prefer examples from `_eino/eino-examples/` as the canonical reference for
idiomatic usage.
PROMPT
echo "---"
