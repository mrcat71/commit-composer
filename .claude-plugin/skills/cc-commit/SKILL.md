---
name: cc-commit
description: Fast-commit the working tree. Claude analyses the dirty diff, splits it into 1+ Conventional-Commits-style commits, and applies autonomously without a TUI. Activates on phrases like "commit my changes", "make commits for what I have", "auto-commit this", "split my dirty tree into commits", or "use cc-commit / fast commit". Falls back to /commit-compose when the working tree is clean.
argument-hint: 'optional: free-text hint, e.g. "keep tests separate"'
allowed-tools: [Bash, Read, Write]
---

# cc-commit (skill alias for /cc-commit)

This skill activates when the user wants their current working-tree
changes turned into commits without going through a TUI. It is a thin
wrapper around the `/cc-commit` slash command - call that command's
flow rather than re-implementing it.

## Activation phrases

- "commit my changes"
- "make commits for what I have"
- "auto-commit this"
- "split my dirty tree into commits"
- "just commit this"
- "use cc-commit / fast commit"
- "fast commit the working tree"

## Workflow (be fast)

The user has explicitly asked for this to be fast. Do NOT chat about
how to group the changes - read the artifacts and decide. Do NOT print
the diff in chat.

Delegate to `/cc-commit` immediately, passing the user's optional hint
as `$1`. The slash command:

1. Pre-flights (in a git repo? dirty tree?).
2. If the tree is clean, falls back to `/commit-compose`.
3. Otherwise synthesizes a WORKING-only plan, runs `__split-prepare`
   to get the diff + file list, has Claude write
   `WORKING.split.json`, and applies autonomously.
4. Prints one short chat line per created commit.

No TUI, no confirmation, no `$EDITOR` review. The whole point of this
skill is to be the one-shot fast path.
