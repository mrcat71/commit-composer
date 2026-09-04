---
description: Mark and recompose git commits in a TUI, then apply via git rebase -i. Claude-recompose pools let Claude redesign multiple commits and the user reviews the proposal in the TUI.
when_to_use: The user wants to rewrite, clean up, reorder, squash, split, or reword existing git commits - "recompose commits", "clean up my history", "rewrite these commits", "squash these", "split this commit", "reword the last N commits", "interactive rebase", "tidy the branch before a PR". Use for commits that are already committed; for uncommitted working-tree changes use cc-commit instead.
argument-hint: 'optional: <rev> or <base>..<head> (default: upstream..HEAD)'
model: sonnet
allowed-tools:
  - Bash(commit-composer *)
  - Bash(${CLAUDE_PLUGIN_ROOT}/bin/commit-composer *)
  - Bash(git *)
  - Read
  - Write
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

## Commit-message rules (applies to claude-recompose AND claude-reword)

Every commit message you propose - whether for a claude-recompose group
or a claude-reword commit - MUST follow the rules in
`${CLAUDE_PLUGIN_ROOT}/skills/commit-compose/references/commit-message-rules.md`.
Read that file before writing any message. The reword and recompose
flows are the two surfaces where you get to write commit messages; treat
both consistently.

The scope-selection cheatsheet at the end of that file is the
load-bearing part: ask "what is this change ABOUT?" and use the answer
as the scope. The technology is just the tool the change happens to use.

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

Argument: `$ARGUMENTS` (optional). **Do not ask the user to confirm a range** -
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
etc.). Do NOT narrate "let me check ...". Every step below is a single
`commit-composer ...` command - no `mktemp`, no `$(...)`, no `sh -c`.
The binary does the shell glue (temp files, terminal detection, the
protected-branch check) internally, which keeps the commands clean
enough for a single `Bash(commit-composer *)` allow rule.

`commit-composer` must be on `$PATH` (Homebrew installs it there; from
source, `./scripts/install.sh`). If it is missing, tell the user to
run `./scripts/install.sh` and stop.

**Token rule:** treat large artifacts as last-resort context. The full
pool diff can be thousands of lines; reading it always is expensive
and rarely necessary. Step 2 spells out when to read it and when not.
Do NOT re-verify the apply with `go build` / test suites - the binary
already asserts the working tree is clean. Do NOT grep the binary's
source to confirm its semantics; trust the documented behaviour below.

Launch the picker TUI:

```bash
commit-composer __launch --plugin-root="${CLAUDE_PLUGIN_ROOT}" -- "$ARGUMENTS"
```

A dirty tree is fine - the binary auto-detects it and adds a synthetic
"(uncommitted changes)" row the user can mark with 'c' to have Claude
recompose the dirty tree into coherent commits. `__launch` runs the
overlay (tmux popup / Zellij floating / kitty overlay / etc.) and,
once the TUI closes, prints:

- `PLAN_FILE=<path>`  - the captured structured plan.
- `CANCELLED=0|1`     - `1` means the user quit the TUI.
- `SHARED_REF=<ref>`  - a protected branch the range already contains,
  or empty (used for the heads-up in step 3).

If the command exits non-zero, surface the error and stop. If
`CANCELLED=1`, say "cancelled" and stop. That's the only command you
run before the TUI appears - no diagnostics, no `--version`.

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

The review TUI may rewrite `pool_size` to 1 in the accepted JSON; ignore
that. The binary's WORKING code path (`executeUncommittedRecompose`)
never reads `pool_size` - it only does `git reset` (index, not HEAD)
followed by per-group `git add` + `git commit`. No history is touched.

Applying a WORKING op stages + commits each group on top of HEAD without
running rebase, so it cannot conflict with existing history. If the plan
also has commit-level claude-recompose ops, the binary will stash the
dirty tree, run the rebase, pop the stash, then commit the WORKING groups.

## 2. If the plan has claude-recompose ops, pre-analyse pools

Check whether any line in `$PLAN_FILE` starts with `- claude-recompose`
(or the legacy `- claude-split`). If none, skip to step 3.

Consecutive `claude-recompose` rows are **pooled**: their combined diff
is analysed as one batch and Claude proposes a fresh sequence of
commits. Prepare the analysis artifacts (substitute the `PLAN_FILE`
value the launch step printed):

```bash
commit-composer __split-prepare --plan=<PLAN_FILE>
```

With `--out` omitted, `__split-prepare` creates the artifacts directory
itself and prints `SPLITS_DIR=<path>` (followed by the manifest path).
Use that `SPLITS_DIR` for the rest of the flow.

`__split-prepare` writes, for each pool:

- `$SPLITS_DIR/<lastSHA>.diff` - unified diff covering the whole pool
- `$SPLITS_DIR/<lastSHA>.files.txt` - name-status lines for every file
  touched by any commit in the pool
- `$SPLITS_DIR/<lastSHA>.commits.txt` - `<sha> <subject>` per commit
- `$SPLITS_DIR/manifest.json` - structured list of pools with
  `last_sha`, `pool_size`, `commits`, file paths

For each pool, **start with `<lastSHA>.files.txt` and `<lastSHA>.commits.txt`** -
the file paths and commit subjects are usually enough to decide grouping
(a deps bump, a refactor concentrated in one module, a docs-only change,
etc.). **Only read `<lastSHA>.diff`** when the grouping requires
inspecting *what* changed - e.g. one file mixes two concerns and you
need to know if hunks can be split, or filenames don't make the topical
boundary obvious. The diff can be thousands of lines; reading it
defensively is the single biggest token sink in this workflow.

Then **propose groups**. Group files **by feature / logical topic**,
not mechanically by file. The output you must produce is a JSON file at
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
- **Commit messages must be non-empty** and MUST follow the
  **Commit-message rules** section at the top of this file. The scope
  guidance is the load-bearing part - prefer feature/module/service/
  chart scopes over generic technology scopes like `terraform`,
  `helm`, `k8s`.
- 1 group, same-count, or more-than-input - all valid. Pick what
  makes the new history readable.

## 2c. If the plan has claude-reword ops, ask Claude to propose new messages

Check whether any line in `$PLAN_FILE` starts with `- claude-reword`.
If none, skip to step 3.

`claude-reword` is the per-commit reword path: the user pressed `r` in
the TUI and chose `c` (ask Claude). Each marked commit needs (a) a
Claude-proposed message under the **Commit-message rules**, and (b) a
user-review pass in `$EDITOR`. The existing manual reword path (which
produces normal `- reword <sha> :: <msg>` lines) is unaffected.

Prepare per-commit artifacts (substitute the `PLAN_FILE` value from the
launch step):

```bash
commit-composer __reword-prepare --plan=<PLAN_FILE>
```

With `--out` omitted it creates the directory itself and prints
`REWORDS_DIR=<path>` (followed by the manifest path). Use that
`REWORDS_DIR` below.

> Note: the `$EDITOR` review loop below is the one remaining step that
> shells out interactively (it opens your editor per commit), so it may
> prompt under a strict shell-approval setup. It only runs when you
> actually pick claude-reword in the TUI.

`__reword-prepare` writes for each claude-reword op:

- `$REWORDS_DIR/<sha>.reword.msg.txt` - the current commit message
- `$REWORDS_DIR/<sha>.reword.diff`    - the commit's unified diff
- `$REWORDS_DIR/reword-manifest.json` - structured list of entries

For each entry in `reword-manifest.json`:

1. Read `<sha>.reword.msg.txt` and `<sha>.reword.diff`.
2. Compose a new commit message that:
   - Accurately reflects what the diff does.
   - Follows the **Commit-message rules** section at the top of this
     file (Conventional Commits format; explicit, non-generic scope;
     imperative lowercase summary; no trailing period; kebab-case
     scope when multi-word).
3. Write the proposal to `$REWORDS_DIR/<sha>.reword.proposed.txt`.
   Single line if the message is one-line; subject + blank line + body
   if the original had a body and you preserved or extended it.

Do NOT print proposals into chat. The user reviews them in `$EDITOR`,
not in the conversation.

Then, for each commit, open `$EDITOR` so the user can review and edit
the proposal before it lands in the plan:

```bash
for f in "$REWORDS_DIR"/*.reword.proposed.txt; do
  [ -e "$f" ] || continue
  cp "$f" "${f%.proposed.txt}.draft.txt"
  ${EDITOR:-vi} "${f%.proposed.txt}.draft.txt"
  cp "${f%.proposed.txt}.draft.txt" "${f%.proposed.txt}.final.txt"
done
```

If the user empties a file, `__reword-apply` will error in the next
step - that's the signal that the user cancelled that specific reword.
Surface the error and stop; do not auto-fall-back to the original
message.

Finally, fold the accepted messages back into the plan (substitute the
`PLAN_FILE` and `REWORDS_DIR` values):

```bash
commit-composer __reword-apply --plan=<PLAN_FILE> --rewords-dir=<REWORDS_DIR>
```

After this step, every `- claude-reword <sha>` line in `$PLAN_FILE` has
been rewritten to `- reword <sha> :: <accepted-message>` (multi-line
messages use the `b64::` encoding automatically). The apply step (5)
then proceeds with regular reword ops only.

## 3. Brief protected-branch heads-up (one line)

No command needed - `__launch` already did the overlap check and printed
`SHARED_REF=<ref>` in step 0. If it is non-empty, the range already lives
on a protected branch; tell the user in ONE chat line, e.g.:

> Heads-up: these commits are on `origin/master`. Applying will require
> `git push --force-with-lease` afterwards. Review the plan in the TUI.

If `SHARED_REF` was empty, say nothing here. Do NOT dump the full plan
as text. The next step opens it in the TUI.

## 4. Open the review TUI (this is the single confirmation point)

Launch the second-pass review TUI - it shows the proposed commits as
virtual rows that the user can edit or comment on, and is the place
where they say "yes apply" / "no cancel":

```bash
commit-composer __launch --plugin-root="${CLAUDE_PLUGIN_ROOT}" -- __review-proposal --splits=<SPLITS_DIR>
```

`__launch` runs the review overlay and streams the outcome JSON to
stdout - read it directly from the command output (no temp file).

The TUI keys (mentioned briefly in chat or via `?` inside the TUI):
- `r` reword (edit message in $EDITOR)
- `s` squash into previous group within the same pool
- `d` drop (fold files into previous, discard message)
- `m` leave a comment for Claude on this group
- `⏎` submit (apply), `q` cancel

If the command errors or prints nothing, treat as cancelled.

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

Substitute the `PLAN_FILE` and `SPLITS_DIR` values:

```bash
commit-composer --apply --plan=<PLAN_FILE> --splits=<SPLITS_DIR>
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

## 7. No cleanup of temp files

Leave `PLAN_FILE`, `SPLITS_DIR`, and `REWORDS_DIR` on disk on both
success and cancellation. The user has explicitly forbidden auto-rm of
these artifacts (they inspect them after runs), and recursive `rm` is
denied by their shell-approval hook anyway. The OS reaps the temp
directory on its own schedule.
