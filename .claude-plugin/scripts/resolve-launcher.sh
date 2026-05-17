#!/usr/bin/env bash
# resolve-launcher.sh <launcher-name> [data-dir]
#
# Resolves the absolute path to a launcher script with a two-layer override:
#   1. If $CLAUDE_PLUGIN_DATA/scripts/<launcher-name> exists and is executable,
#      use that (user override).
#   2. Otherwise use the bundled script next to this resolver.
#
# Prints the resolved path. Exits 1 if neither exists.
#
# Mirrors revdiff's resolver shape. Note we do NOT add a project-layer
# override (security): only the user's home-scoped CLAUDE_PLUGIN_DATA may
# override the bundled launcher.

set -euo pipefail

if [ "$#" -lt 1 ]; then
  printf 'usage: %s <launcher-name> [data-dir]\n' "$0" >&2
  exit 2
fi

LAUNCHER_NAME="$1"
DATA_DIR="${2:-${CLAUDE_PLUGIN_DATA:-}}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [ -n "$DATA_DIR" ] && [ -x "$DATA_DIR/scripts/$LAUNCHER_NAME" ]; then
  printf '%s\n' "$DATA_DIR/scripts/$LAUNCHER_NAME"
  exit 0
fi

if [ -x "$SCRIPT_DIR/$LAUNCHER_NAME" ]; then
  printf '%s\n' "$SCRIPT_DIR/$LAUNCHER_NAME"
  exit 0
fi

printf 'commit-composer: cannot locate launcher %q\n' "$LAUNCHER_NAME" >&2
exit 1
