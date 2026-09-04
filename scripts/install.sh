#!/usr/bin/env bash
# install.sh - build the commit-composer binary into ./bin so the
# Claude plugin launcher can find it.
#
# Use this for local development. Production deployments should publish the
# binary alongside the plugin (under bin/) so users don't need a Go toolchain.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

if ! command -v go >/dev/null 2>&1; then
  printf 'install.sh: go toolchain not on PATH\n' >&2
  exit 1
fi

OUT_DIR="${OUT_DIR:-$REPO_ROOT/bin}"
mkdir -p "$OUT_DIR"

printf 'building commit-composer -> %s\n' "$OUT_DIR/commit-composer"
go build -trimpath -o "$OUT_DIR/commit-composer" ./cmd/commit-composer

chmod +x "$OUT_DIR/commit-composer"
printf 'built bundled binary: %s\n' "$OUT_DIR/commit-composer"

# Also put commit-composer on $PATH. The plugin's slash commands invoke the
# bare `commit-composer` name (so a single `Bash(commit-composer *)` allow rule
# covers them and no absolute paths leak into the command strings). Under
# Homebrew that name is already on $PATH; in a source checkout we install it
# with `go install`, which writes to `$(go env GOBIN)` or `$(go env GOPATH)/bin`.
# The overlay launcher still prefers the bundled binary above, so the TUI keeps
# using this fresh build.
printf 'installing commit-composer onto $PATH via go install\n'
go install -trimpath ./cmd/commit-composer

BIN_DIR="$(go env GOBIN)"
[ -n "$BIN_DIR" ] || BIN_DIR="$(go env GOPATH)/bin"
case ":$PATH:" in
  *":$BIN_DIR:"*) : ;;
  *) printf 'NOTE: %s is not on your $PATH - add it so `commit-composer` resolves for the slash commands.\n' "$BIN_DIR" >&2 ;;
esac
printf 'done. bundled: %s ; on PATH: %s/commit-composer\n' "$OUT_DIR/commit-composer" "$BIN_DIR"
