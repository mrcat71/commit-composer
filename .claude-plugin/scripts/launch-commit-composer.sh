#!/usr/bin/env bash
# launch-commit-composer.sh
#
# Detect the host terminal and spawn the commit-composer TUI in an overlay
# pane / popup so the launching shell (and the calling Claude conversation)
# is never blocked on stdin/stdout fighting with the TUI.
#
# The TUI's structured plan is written to a temp file via `--output=$FILE`,
# and after the overlay process exits we `cat $FILE` so the plan ends up on
# our own stdout. This is the pattern revdiff uses; we mirror it because
# every overlay backend (tmux popup, kitty overlay, etc.) steals stdout
# from a directly-spawned binary.
#
# Probe order (first matching env var wins):
#   1. tmux        (TMUX)
#   2. Zellij      (ZELLIJ)
#   3. kitty       (KITTY_LISTEN_ON)
#   4. wezterm     (WEZTERM_PANE)
#   5. ghostty     (TERM_PROGRAM=ghostty)
#   6. iTerm2      (ITERM_SESSION_ID)
#   7. Emacs vterm (INSIDE_EMACS=vterm)
#   8. fallback    (run inline)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PLUGIN_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

# Locate the commit-composer binary. Preference order:
#   1. $COMMIT_COMPOSER_BIN (explicit override)
#   2. <plugin-root>/bin/commit-composer (bundled - copied here by install.sh)
#   3. $PATH lookup
#   4. go run from <plugin-root>/.. (dev fallback - requires go toolchain)
#
# We check the plugin-bundled binary BEFORE $PATH so the launcher always uses
# the binary that ships with the current plugin install rather than a stale
# `go install`ed one. The bundled binary is updated by ./scripts/install.sh
# in the dev tree; Claude Code's plugin cache also copies it on
# `/plugin marketplace update`.
resolve_bin() {
  if [ -n "${COMMIT_COMPOSER_BIN:-}" ] && [ -x "${COMMIT_COMPOSER_BIN}" ]; then
    printf '%s' "${COMMIT_COMPOSER_BIN}"
    return
  fi
  if [ -x "${PLUGIN_ROOT}/bin/commit-composer" ]; then
    printf '%s' "${PLUGIN_ROOT}/bin/commit-composer"
    return
  fi
  if command -v commit-composer >/dev/null 2>&1; then
    command -v commit-composer
    return
  fi
  printf '__dev_go_run__'
}

BIN="$(resolve_bin)"

# If we fell through to `__dev_go_run__` AND there's no go.mod at the
# plugin's parent dir, surface a clear error instead of silently failing
# inside an overlay where the user can't see it.
if [ "$BIN" = "__dev_go_run__" ] && [ ! -f "${PLUGIN_ROOT}/../go.mod" ]; then
  cat >&2 <<EOF
commit-composer: cannot locate the binary.

Checked (in order):
  \$COMMIT_COMPOSER_BIN  = ${COMMIT_COMPOSER_BIN:-(unset)}
  ${PLUGIN_ROOT}/bin/commit-composer
  \`command -v commit-composer\`
  \`go run\` fallback at ${PLUGIN_ROOT}/.. (no go.mod found)

To fix:
  1. cd /path/to/commit-composer && ./scripts/install.sh
  2. In Claude Code: /plugin marketplace update commit-composer
  3. /reload-plugins

Or set COMMIT_COMPOSER_BIN to the absolute path of the built binary.
EOF
  exit 2
fi

OUTPUT_FILE="$(mktemp -t commit-composer-output-XXXXXX)"
trap 'rm -f "$OUTPUT_FILE"' EXIT

# Build the actual command line that runs inside the overlay.
#
# Two argument layouts depending on the first non-empty arg:
#
#   subcommand mode  (first arg starts with `__`):
#     <bin> __subcmd <subcmd-flags...> --output=<out>
#     The binary's main() must see os.Args[1] = "__subcmd", so subcommand
#     stays first, --output goes last.
#
#   tui mode (first arg is a positional range or no args):
#     <bin> --output=<out> [<range>]
#     --output goes FIRST so an empty/positional range cannot short-circuit
#     Go's flag.Parse (which stops at the first non-flag).
#
# Empty args are skipped throughout - passing `""` would otherwise terminate
# flag parsing and lose the --output flag.
build_cmd() {
  local out="$1"
  shift
  # Filter out empty args. Under `set -u`, expanding "${array[@]}" of an
  # empty array errors with "unbound variable", so guard every expansion
  # behind a length check.
  local args=()
  local a
  for a in "$@"; do
    [ -n "$a" ] && args+=("$a")
  done
  local n=${#args[@]}

  local binexpr
  if [ "$BIN" = "__dev_go_run__" ]; then
    local repo_root
    repo_root="$(cd "${PLUGIN_ROOT}/.." && pwd)"
    binexpr=$(printf 'cd %q && go run ./cmd/commit-composer' "$repo_root")
  else
    binexpr=$(printf '%q' "$BIN")
  fi

  local first=""
  if [ "$n" -gt 0 ]; then
    first="${args[0]}"
  fi

  if [[ "$first" == __* ]]; then
    # Subcommand mode: subcommand + its flags first, --output last.
    printf '%s' "$binexpr"
    if [ "$n" -gt 0 ]; then
      for a in "${args[@]}"; do
        printf ' %q' "$a"
      done
    fi
    printf ' --output=%q' "$out"
  else
    # TUI mode: --output first, then the optional range.
    printf '%s --output=%q' "$binexpr" "$out"
    if [ "$n" -gt 0 ]; then
      for a in "${args[@]}"; do
        printf ' %q' "$a"
      done
    fi
  fi
}

CMD="$(build_cmd "$OUTPUT_FILE" "$@")"
TITLE="commit-composer"
CWD="$(pwd)"

# Sentinel pattern for non-blocking backends: launcher polls until the
# sentinel file appears, signalling that the spawned binary has exited.
sentinel_wait() {
  local sentinel="$1"
  local timeout_s="${2:-600}"
  local waited=0
  while [ ! -f "$sentinel" ]; do
    sleep 0.3
    waited=$((waited + 1))
    if [ "$waited" -gt $((timeout_s * 4)) ]; then
      printf 'commit-composer: timed out waiting for overlay\n' >&2
      return 1
    fi
  done
  rm -f "$sentinel"
}

run_inline() {
  # No overlay - run directly. stdout already points where the caller
  # expects, so write straight to OUTPUT_FILE and then cat at the end like
  # the other backends.
  sh -c "$CMD" </dev/tty >/dev/tty 2>&1
}

run_tmux() {
  # tmux display-popup -E is blocking; no sentinel needed. The overlay
  # gets its own stdio so we use --output=FILE for IPC.
  local w h
  w=${TMUX_POPUP_WIDTH:-80%}
  h=${TMUX_POPUP_HEIGHT:-80%}
  tmux display-popup -E -w "$w" -h "$h" -T " $TITLE " -d "$CWD" -- sh -c "$CMD"
}

run_zellij() {
  local sentinel
  sentinel="$(mktemp -t commit-composer-done-XXXXXX)"
  rm -f "$sentinel"
  zellij run --floating --close-on-exit \
    --name "$TITLE" --cwd "$CWD" -- \
    sh -c "$CMD; touch $(printf %q "$sentinel")"
  sentinel_wait "$sentinel"
}

run_kitty() {
  local sentinel
  sentinel="$(mktemp -t commit-composer-done-XXXXXX)"
  rm -f "$sentinel"
  kitty @ --to "${KITTY_LISTEN_ON}" launch \
    --type=overlay --title "$TITLE" --cwd=current \
    sh -c "$CMD; touch $(printf %q "$sentinel")"
  sentinel_wait "$sentinel"
}

run_wezterm() {
  local sentinel
  sentinel="$(mktemp -t commit-composer-done-XXXXXX)"
  rm -f "$sentinel"
  wezterm cli split-pane --bottom --percent 80 --cwd "$CWD" -- \
    sh -c "$CMD; touch $(printf %q "$sentinel")"
  sentinel_wait "$sentinel"
}

run_ghostty() {
  # Ghostty doesn't expose a CLI; we ask AppleScript to send keystrokes.
  # Fall through to inline if we're not on macOS.
  if ! command -v osascript >/dev/null 2>&1; then
    run_inline
    return
  fi
  local sentinel
  sentinel="$(mktemp -t commit-composer-done-XXXXXX)"
  rm -f "$sentinel"
  local payload
  payload="cd $(printf %q "$CWD") && $CMD; touch $(printf %q "$sentinel"); exit"
  osascript \
    -e 'tell application "System Events" to tell process "ghostty" to keystroke "d" using {command down}' \
    -e "delay 0.4" \
    -e "tell application \"System Events\" to tell process \"ghostty\" to keystroke \"${payload//\"/\\\"}\"" \
    -e 'tell application "System Events" to tell process "ghostty" to key code 36' \
    >/dev/null
  sentinel_wait "$sentinel"
}

run_iterm() {
  if ! command -v osascript >/dev/null 2>&1; then
    run_inline
    return
  fi
  local sentinel
  sentinel="$(mktemp -t commit-composer-done-XXXXXX)"
  rm -f "$sentinel"
  local payload
  payload="cd $(printf %q "$CWD") && $CMD; touch $(printf %q "$sentinel"); exit"
  osascript <<EOF >/dev/null
tell application "iTerm2"
  tell current session of current window
    set newSession to (split horizontally with same profile)
    tell newSession
      write text "${payload//\"/\\\"}"
    end tell
  end tell
end tell
EOF
  sentinel_wait "$sentinel"
}

run_emacs_vterm() {
  local sentinel
  sentinel="$(mktemp -t commit-composer-done-XXXXXX)"
  rm -f "$sentinel"
  emacsclient --eval "(progn (let ((default-directory \"$CWD\")) (vterm \"*commit-composer*\")) (vterm-send-string \"$CMD; touch $sentinel\n\"))" >/dev/null 2>&1
  sentinel_wait "$sentinel"
}

dispatch() {
  if [ -n "${TMUX:-}" ]; then
    run_tmux
  elif [ -n "${ZELLIJ:-}" ]; then
    run_zellij
  elif [ -n "${KITTY_LISTEN_ON:-}" ]; then
    run_kitty
  elif [ -n "${WEZTERM_PANE:-}" ]; then
    run_wezterm
  elif [ "${TERM_PROGRAM:-}" = "ghostty" ]; then
    run_ghostty
  elif [ -n "${ITERM_SESSION_ID:-}" ]; then
    run_iterm
  elif [ "${INSIDE_EMACS:-}" = "vterm" ]; then
    run_emacs_vterm
  else
    run_inline
  fi
}

dispatch

# Emit the captured plan to our own stdout. Empty file = user cancelled.
if [ -s "$OUTPUT_FILE" ]; then
  cat "$OUTPUT_FILE"
fi
