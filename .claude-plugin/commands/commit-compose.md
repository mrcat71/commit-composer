---
description: Mark and recompose git commits in a TUI, then apply via git rebase -i. Claude-recompose pools let Claude redesign multiple commits and the user reviews the proposal in the TUI.
argument-hint: 'optional: <rev> or <base>..<head> (default: upstream..HEAD)'
allowed-tools: [Bash, Read, Write]
---

# /commit-compose

You are helping the user recompose a slice of git commits using the
`commit-composer` plugin's TUI binary. The TUI lets the user mark each
commit with one of: pick / reword / squash / fixup / drop / edit /
**claude-recompose**, plus reorder. On confirmation it emits a structured
plan; you then pre-analyse `claude-recompose` commits and open the
**review TUI** so the user reviews / edits / comments on the proposed
commits visually. After the user accepts in the TUI, apply via
`git rebase -i`.

## ABSOLUTE RULE - read this before doing anything else

**Do NOT print the plan, the proposed groups, file lists, or any
"Apply? (y/N)" prompt as text in chat.** The user has explicitly asked
for the review to happen in the TUI, not in chat. Between running the
first TUI and applying, the only chat output you may produce is:

  - The one-line protected-branch heads-up (step 3 below), and
  - A brief "Opening review TUI..." sentence if you want a status line.

If you find yourself drafting bullet points like "Group 1: feat(...) -
files: ..." in chat, STOP. Write the proposal to `<sha>.split.json`
and launch `__review-proposal`. The user sees everything inside the TUI.

The review TUI is **the** confirmation point. There is no fallback
chat confirmation. If the review TUI fails to open, surface the error
and stop - do not paper over it with a chat prompt.

## Range argument

Argument: `$1` (optional). **Do not ask the user to confirm a range** -
the TUI is the place where they pick which commits to recompose. Pass
whatever the user supplied (or empty) straight to the launcher.

How the binary resolves an empty argument:

- Try `@{upstream}..HEAD` first; use it if it has at least one commit.
- Otherwise fall back to the last 10 commits (`HEAD~10..HEAD`).
- Errors only if the repo has fewer than 2 commits total.

Other accepted forms:

- `<rev>` (no `..`): treated as `<rev>..HEAD`.
- `<base>..<head>`: used as-is. Triple-dot ranges are rejected.

## Speed rule (do not violate)

The user has explicitly asked for the TUI to launch fast. Do NOT run
diagnostic bash commands (env dumps, version checks, list-clients,
etc.). Do NOT narrate "let me check ...". Run **exactly one** bash
block to do pre-flight + launch, in this order:

```bash
set -e
git rev-parse --git-dir >/dev/null 2>&1 || { echo "not in a git repository" >&2; exit 1; }
# Dirty tree is now allowed - the binary auto-detects it and adds a
# synthetic "(uncommitted changes)" row at the top of the TUI. The user
# can mark that row with 'c' to have Claude recompose the dirty tree into
# coherent commits.

BIN="$(command -v commit-composer 2>/dev/null || true)"
[ -z "$BIN" ] && BIN="${CLAUDE_PLUGIN_ROOT}/.claude-plugin/bin/commit-composer"
LAUNCHER="${CLAUDE_PLUGIN_DATA:+$CLAUDE_PLUGIN_DATA/scripts/launch-commit-composer.sh}"
[ -x "$LAUNCHER" ] || LAUNCHER="${CLAUDE_PLUGIN_ROOT}/.claude-plugin/scripts/launch-commit-composer.sh"

PLAN_FILE="$(mktemp -t commit-composer-plan-XXXXXX)"
SPLITS_DIR="$(mktemp -d -t commit-composer-splits-XXXXXX)"
echo "PLAN_FILE=$PLAN_FILE"
echo "SPLITS_DIR=$SPLITS_DIR"
echo "BIN=$BIN"

"$LAUNCHER" "$1" >"$PLAN_FILE"
echo "--- PLAN ---"
cat "$PLAN_FILE"
```

If that bash block exits non-zero, surface the error and stop. If
`$PLAN_FILE` is empty after a clean exit, the user cancelled in the
TUI - say "cancelled" and stop.

That's the only bash you should run before the TUI appears. No
diagnostics, no `--help`, no `--version`, no `tmux list-clients`. The
launcher handles terminal detection itself.

## 1. Capture the plan

Done by the bash block above. If empty plan or non-zero exit: stop.

## 2a. Uncommitted-changes row

If `$PLAN_FILE` contains a line `- claude-recompose WORKING`, that's the
synthetic uncommitted-changes pool. Treat it like any other pool when
proposing groups: `__split-prepare` writes the diff to
`$SPLITS_DIR/WORKING.diff` and the file list to
`$SPLITS_DIR/WORKING.files.txt`. Your proposal goes to
`$SPLITS_DIR/WORKING.split.json` with `"sha": "WORKING"` and `"pool_size": 0`
(0 marks "not a commit pool - just commit on top of HEAD").

Applying a WORKING op stages + commits each group on top of HEAD without
running rebase, so it cannot conflict with existing history. If the plan
also has commit-level claude-recompose ops, the binary will stash the
dirty tree, run the rebase, pop the stash, then commit the WORKING groups.

## 2. If the plan has claude-recompose ops, pre-analyse pools

Check whether any line in `$PLAN_FILE` starts with `- claude-recompose`
(or the legacy `- claude-split`). If none, skip to step 3.

Consecutive `claude-recompose` rows are **pooled**: their combined diff
is analysed as one batch and Claude proposes a fresh sequence of
commits. Prepare the analysis artifacts (re-using `$SPLITS_DIR` from
step 0):

```bash
"$BIN" __split-prepare --plan="$PLAN_FILE" --out="$SPLITS_DIR"
```

`__split-prepare` writes, for each pool:

- `$SPLITS_DIR/<lastSHA>.diff` - unified diff covering the whole pool
- `$SPLITS_DIR/<lastSHA>.files.txt` - name-status lines for every file
  touched by any commit in the pool
- `$SPLITS_DIR/<lastSHA>.commits.txt` - `<sha> <subject>` per commit
- `$SPLITS_DIR/manifest.json` - structured list of pools with
  `last_sha`, `pool_size`, `commits`, file paths

For each pool, read the diff and the commit list, then **propose
groups**. Group files **by feature / logical topic**, not mechanically
by file. The output you must produce is a JSON file at
`$SPLITS_DIR/<lastSHA>.split.json` with this shape:

```json
{
  "sha": "<full-last-sha>",
  "pool_size": 3,
  "groups": [
    { "files": ["auth.go", "auth_test.go"], "message": "feat: add Auth helper" },
    { "files": ["docs/auth.md"],            "message": "docs: explain Auth" }
  ]
}
```

`pool_size` MUST match the `pool_size` from manifest.json (the binary
uses it to `git reset --mixed HEAD~<pool_size>` before applying the
new groups).

Rules when proposing groups:

- **Output count is YOUR judgment, not the input count.** If the user
  marks 2 commits but the code is really one feature, propose **1**
  group. If the user marks 2 commits but the code touches 4 unrelated
  things, propose **4**. Don't anchor on the pool size - decide from
  the diff. The user chose claude-recompose precisely because they
  want you to make this call.
- **Group by feature**, not by file. If `auth.go` and `auth_test.go`
  together implement one thing, keep them together. If `docs/auth.md`
  documents a different concern, separate it.
- **Every file the pool touches must be in exactly one group.** The
  binary rejects splits that leave the working tree dirty.
- **Commit messages must be non-empty** and follow Conventional
  Commits where it makes sense (feat/fix/docs/refactor/test/chore).
- 1 group, same-count, or more-than-input - all valid. Pick what
  makes the new history readable.

## 3. Brief protected-branch heads-up (one line)

Run the overlap check silently:

```bash
SHARED=""
for ref in origin/main origin/master upstream/main upstream/master; do
  git rev-parse --verify --quiet "$ref" >/dev/null 2>&1 || continue
  SHARED="$ref"; break
done
```

If `$SHARED` is set, the range overlaps a protected branch. Tell the
user in ONE chat line, e.g.:

> Heads-up: these commits are on `origin/master`. Applying will require
> `git push --force-with-lease` afterwards. Review the plan in the TUI.

Do NOT dump the full plan as text. The next step opens it in the TUI.

## 4. Open the review TUI (this is the single confirmation point)

Launch the second-pass review TUI - it shows the proposed commits as
virtual rows that the user can edit or comment on, and is the place
where they say "yes apply" / "no cancel":

```bash
OUTCOME_FILE="$(mktemp -t commit-composer-review-XXXXXX)"
"$LAUNCHER" __review-proposal --splits="$SPLITS_DIR" >"$OUTCOME_FILE"
```

The TUI keys (mentioned briefly in chat or via `?` inside the TUI):
- `r` reword (edit message in $EDITOR)
- `s` squash into previous group within the same pool
- `d` drop (fold files into previous, discard message)
- `m` leave a comment for Claude on this group
- `⏎` submit (apply), `q` cancel

If `$OUTCOME_FILE` is empty / non-zero exit, treat as cancelled.

Parse the JSON outcome:

```json
{
  "accept": true,
  "has_comments": true,
  "groups_changed": false,
  "pools": [ ... revised proposals (with any per-group comments) ... ]
}
```

Branch on the outcome:

- `accept: false` → user cancelled → say "cancelled" and clean up.
- `accept: true, has_comments: false` → go to step 5 (apply).
- `accept: true, has_comments: true` → revise:
  1. For each pool with commented groups, read the comments + the
     original pool diff at `$SPLITS_DIR/<lastSHA>.diff`
  2. Compose a revised proposal that addresses each comment
  3. Write the new proposal to `<lastSHA>.split.json` with all
     `comment` fields **cleared**
  4. Re-launch the review TUI (loop back to step 4)
  Cap iterations at **5** to prevent runaway loops.

## (No separate chat confirmation)

Skip the "render plan in chat, ask y/N" pattern. The review TUI IS
the confirmation. Going through chat to summarize the plan is
redundant and slow - the user can see everything in the TUI.

## 5. Apply

```bash
"$BIN" --apply --plan="$PLAN_FILE" --splits="$SPLITS_DIR"
```

(If there were no claude-recompose ops you can omit `--splits` AND
skip steps 2-4 entirely - go straight from the first TUI to apply.)

The binary will:

1. Run `git rebase -i <base>` driven by helpers (no editor pops up).
2. Pause at each claude-recompose pool end as an `edit` step.
3. Look up `<sha>.split.json` in `$SPLITS_DIR`.
4. Run `git reset --mixed HEAD^`, then for each group `git add <files>`
   + `git commit -m <message>`.
5. Verify the working tree is clean (no missed files).
6. `git rebase --continue`, repeat until done.

## 6. Handle conflicts

If the apply exits non-zero, the working tree is in a conflicted rebase
state. Surface the conflict to the user. Do NOT run `git rebase
--abort` automatically. Tell the user to resolve and run
`git rebase --continue` themselves.

After a successful rebase, remind the user that the local history has
diverged from any remote; if they need to push, they will need
`git push --force-with-lease`. **Do NOT run that yourself** - print the
suggested command.

## 7. Cleanup

```bash
rm -f "$PLAN_FILE"
rm -rf "$SPLITS_DIR"
```

Run cleanup on both success and cancellation.
