#!/usr/bin/env bash
# dev_setup.sh — set up a local multi-module workspace for eino development.
#
# BACKGROUND
#   eino, eino-ext, and eino-examples live in separate GitHub repositories to
#   keep their Go modules, versioning, and maintenance independent. However,
#   working across them is inconvenient: editors and AI coding tools lack
#   cross-repo type information and can't navigate between them.
#
#   This script brings all three repos together locally so that a single
#   go.work file provides full cross-module LSP (go-to-definition, type
#   inference, autocomplete) across all ~83 modules — without touching any
#   remote repository.
#
# WHAT IT DOES
#   1. Clones eino-ext  → ext/
#   2. Clones eino-examples → examples/
#   3. Registers ext/ and examples/ in .git/info/exclude so eino's git
#      never sees them (local-only, never committed)
#   4. Creates go.work at the repo root covering eino + all modules in
#      ext/ and examples/ (go.work is already in .gitignore)
#
# RESULTING LAYOUT
#   eino/               ← you are here (github.com/cloudwego/eino)
#   eino/ext/           ← github.com/cloudwego/eino-ext  (full git repo)
#   eino/examples/      ← github.com/cloudwego/eino-examples  (full git repo)
#   eino/go.work        ← wires all modules together (gitignored)
#
# WORKING ACROSS REPOS
#   Each subdirectory is a full independent git repo tracking its own remote.
#   To contribute to eino-ext or eino-examples, work inside that directory:
#
#     cd ext
#     git checkout -b feat/my-feature
#     # make changes — editor has full cross-repo type info via go.work
#     git commit -m "feat: ..."
#     git push origin feat/my-feature   # pushes to cloudwego/eino-ext
#
# KEEPING REPOS UP TO DATE
#     git -C ext pull
#     git -C examples pull
#
# USAGE
#   bash scripts/dev_setup.sh           # first-time setup
#   bash scripts/dev_setup.sh --reset   # re-clone everything from scratch

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

EXT_DIR="ext"
EXAMPLES_DIR="examples"
EINO_EXT_REPO="https://github.com/cloudwego/eino-ext"
EINO_EXAMPLES_REPO="https://github.com/cloudwego/eino-examples"

# Parse flags
RESET=false
for arg in "$@"; do
  case $arg in
    --reset) RESET=true ;;
  esac
done

echo "==> Setting up eino dev workspace in: $REPO_ROOT"

# --reset: remove existing dirs
if [ "$RESET" = true ]; then
  echo "==> --reset: removing $EXT_DIR/ and $EXAMPLES_DIR/"
  rm -rf "$EXT_DIR" "$EXAMPLES_DIR" go.work go.work.sum
fi

# Clone repos if not already present
if [ ! -d "$EXT_DIR/.git" ]; then
  echo "==> Cloning eino-ext into $EXT_DIR/"
  git clone "$EINO_EXT_REPO" "$EXT_DIR"
else
  echo "==> $EXT_DIR/ already exists, skipping clone"
fi

if [ ! -d "$EXAMPLES_DIR/.git" ]; then
  echo "==> Cloning eino-examples into $EXAMPLES_DIR/"
  git clone "$EINO_EXAMPLES_REPO" "$EXAMPLES_DIR"
else
  echo "==> $EXAMPLES_DIR/ already exists, skipping clone"
fi

# Exclude dirs from eino's git tracking (local only, not committed)
EXCLUDE_FILE=".git/info/exclude"
add_exclude() {
  local entry="$1"
  if ! grep -qxF "$entry" "$EXCLUDE_FILE" 2>/dev/null; then
    echo "$entry" >> "$EXCLUDE_FILE"
    echo "==> Added '$entry' to $EXCLUDE_FILE"
  fi
}
add_exclude "$EXT_DIR/"
add_exclude "$EXAMPLES_DIR/"

# Build go.work covering eino root + every go.mod found in ext/ and examples/
if [ ! -f "go.work" ]; then
  echo "==> Creating go.work"
  go work init .

  # Collect all module directories (directories containing a go.mod)
  while IFS= read -r modfile; do
    dir="$(dirname "$modfile")"
    go work use "$dir"
  done < <(find "$EXT_DIR" "$EXAMPLES_DIR" -name "go.mod" | sort)

  echo "==> go.work created with $(grep -c '^\s\+\.' go.work || true) module(s)"
else
  echo "==> go.work already exists, skipping (use --reset to recreate)"
fi

echo ""
echo "Done. Your workspace includes:"
echo "  .             — github.com/cloudwego/eino"
echo "  $EXT_DIR/          — github.com/cloudwego/eino-ext ($(find "$EXT_DIR" -name "go.mod" | wc -l | tr -d ' ') modules)"
echo "  $EXAMPLES_DIR/  — github.com/cloudwego/eino-examples ($(find "$EXAMPLES_DIR" -name "go.mod" | wc -l | tr -d ' ') modules)"
echo ""
echo "Run 'go build ./...' or open this directory in your editor."
