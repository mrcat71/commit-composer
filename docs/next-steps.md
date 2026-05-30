# Next steps

Snapshot of what is done vs. what is left, taken 2026-05-16 after the
M1-M4 implementation session.

## Status

| Milestone | Status      | Notes                                                                 |
|-----------|-------------|-----------------------------------------------------------------------|
| M1 viewer | done        | bubbletea split pane, vim nav, diff viewport                         |
| M2 actions| done        | p/r/s/f/d/e, space cycle, J/K reorder, plan emission, validation     |
| M3 rebase | done        | BuildTodo, GIT_SEQUENCE_EDITOR + GIT_EDITOR helpers, reword + fixup paths verified end-to-end |
| M4 plugin | done        | `.claude-plugin/` manifest + marketplace + slash command + skill alias + bash launcher |
| M5 reorder+reword polish | partial | reorder is wired; reword uses `tea.ExecProcess` + `$EDITOR` (no inline modal). Polish open. |
| M6 polish | not started | terminal detection backends untested end-to-end (only the inline fallback is exercised here) |
| M7 split  | partial     | Superseded by **M8 recompose** (see below). `claude-split` has been renamed to `claude-recompose` and made multi-commit. |
| M8 recompose | done        | `c` marks commits; consecutive marks pool; Claude analyses pooled diff and proposes feature-grouped commits; **phase 2 review TUI** lets the user reword/squash/drop proposed commits or leave per-group comments that drive a Claude revision loop (capped at 5 iterations); binary auto-executes the approved pool reset + groups during apply. |
| M9 cc-commit | done        | `/cc-commit` (skill alias `commit-composer:cc-commit`) is a fast-action sibling of `/commit-compose`: synthesizes a WORKING-only plan, runs `__split-prepare`, has Claude propose 1+ groups, applies autonomously via `executeUncommittedRecompose`. No picker TUI, no review TUI, no chat y/N. Accepts optional `$1` free-text hint. Falls back to `/commit-compose` on a clean tree. |

## Open work

### Verification (highest priority)

- [ ] Manual end-to-end inside each overlay backend: tmux popup,
  Zellij floating, kitty overlay, wezterm split, iTerm split, ghostty
  split (AppleScript), Emacs vterm. Inline fallback works.
- [ ] Verify the slash command flow under a real `/plugin install`. We
  have not loaded the plugin into a live Claude session yet.
- [ ] Verify the protected-branch refusal in the slash command picks up
  `origin/main` correctly in a repo where some commits are already
  published.
- [ ] Confirm `git rebase --continue` behaviour after a conflict: the
  staging dir is retained intentionally; verify the user can resolve
  and continue without re-running commit-composer.

### Polish

- [ ] **Reword UX**: optional inline modal as a fast path for one-line
  edits (currently every reword opens `$EDITOR`). Could be toggled by
  pressing `r` for inline, `R` for `$EDITOR`. Decide later.
- [ ] **Diff syntax highlighting**: today we colorize `+`/`-`/`@@`/file
  lines only. Pulling `alecthomas/chroma/v2` would give per-language
  highlighting like revdiff. Weigh against binary size.
- [ ] **Help overlay**: currently a static block; consider rendering
  bindings from `keymap` so additions stay in sync.
- [ ] **Status messages**: clear timer to drop stale `"set squash"`
  status after N seconds.
- [ ] **Footer ellipsis**: the help line truncates on narrow terminals.
  Add an "expand" hint or wrap.

### Features still owed by the plan

- [ ] **M5 split key (`x`)**: noop placeholder. Superseded by `c`
  (`claude-split`); the unused `x` placeholder can be removed.
- [x] **M7 file-level claude-split**: done. The TUI marks a commit with
  `c`, the slash command asks Claude to propose groups, the user
  approves once, and the binary auto-executes the splits during the
  rebase pauses.
- [ ] **M7 hunk-level split (real)**: when granularity is `by-hunk`,
  generate per-group unified diff patches and stage them via
  `git apply --cached`. Currently `by-hunk` is silently aliased to
  `logical`. The split spec format already supports it (granularity
  field) but `executeSplit` only handles file-level `git add`.
- [ ] **`golangci-lint run` clean**: not run yet (the user may not have
  it installed). Add a `.golangci.yml` with revdiff-compatible rules
  when there is appetite.

### Infrastructure / distribution

- [ ] Wire `scripts/install.sh` into `make install` for parity with the
  Go convention; keep the bash script for non-make users.
- [ ] Decide on prebuilt binaries: ship `.claude-plugin/bin/<os>/<arch>`
  via GitHub releases so users without a Go toolchain can install via
  marketplace alone. Currently install requires `go build`.
- [ ] Submit to a public Claude Code plugin marketplace once verified.
  Open decision from the original plan.
- [ ] Bug to track: `anthropics/claude-code#14929` (slash commands from
  directory-based local marketplaces sometimes not discovered). Watch
  for a fix so we can drop the "push to git first" workaround in
  `docs/install.md`.

### Testing gaps

- [ ] `teatest` snapshot tests for key flows (cursor move, action set,
  J/K reorder, validate failure). Pseudo-versioned dependency - pin
  carefully.
- [ ] End-to-end test using `--apply --plan=` from a temp plan file
  (currently only the in-process `Apply` is exercised).
- [ ] Test the conflict path: construct a rebase plan that we know will
  conflict, assert that `Apply` returns an error AND the staging dir
  remains on disk.
- [ ] Test `CommitEditorMain` directly with a synthetic
  `.git/rebase-merge/done` to make sure SHA extraction is robust to
  unusual subject lines (whitespace, leading dashes, etc.).

### Documentation

- [ ] Add a recorded asciinema of the TUI in action, link from README.
- [ ] Add a `CONTRIBUTING.md` once we know whether this is open to
  contributors.
- [ ] Expand `docs/install.md` with screenshots once the overlay
  backends are verified.
- [ ] Consider archiving the original `docs/commit-composer-plan.md`
  (kept as design history; current truth is this file + the README).

## Decisions still open

From the original plan:
1. **Distribution**: marketplace submission or local-only? Currently
   local-only; submission TBD after verification.
2. **Reword UX**: kept the `$EDITOR` choice from this session. Revisit
   if the inline modal becomes desired.
3. **`x` split**: defer to M7.
