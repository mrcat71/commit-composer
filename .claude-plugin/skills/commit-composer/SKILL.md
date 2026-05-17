---
name: commit-composer
description: Recompose a range of git commits in a TUI (pick / reword / squash / fixup / drop / edit / reorder). Activates on phrases like "reshape my commits", "clean up commit history", "interactive rebase with a UI", "squash these commits", or "commit composer".
argument-hint: 'optional: <rev> or <base>..<head>'
allowed-tools: [Bash, Read, Write]
---

# commit-composer (skill alias for /commit-compose)

This skill activates when the user wants to reshape recent git history but
prefers a guided UI to an `interactive rebase`. It is a thin wrapper around
the `/commit-compose` slash command - call that command's flow rather than
re-implementing it.

## Activation phrases

- "reshape my commits"
- "clean up my commit history"
- "squash my last N commits"
- "split this commit into smaller ones"
- "break up commit X into logical pieces"
- "interactive rebase with a UI"
- "let me pick which commits to keep"
- "use commit composer / commit-composer"

## Workflow (be fast)

The user has explicitly asked for the TUI to launch fast. Do NOT run
diagnostic commands. Do NOT chat about the range or ask which commits
to pick - the TUI itself is the place where they pick commits.

Just delegate to `/commit-compose` immediately, passing the user's
range argument if they supplied one (otherwise empty). The slash
command runs exactly one bash block to launch the TUI.

`/commit-compose` handles: silent pre-flight, launching the TUI
overlay, capturing the plan, pre-analysing claude-recompose pools
through Claude, presenting one combined plan with comment-to-refine
iteration, applying the rebase, executing pre-approved recompose
groups, and surfacing conflicts.
