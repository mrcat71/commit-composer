---
description: Fast-commit the working tree. Claude analyses the dirty diff, splits it into 1+ Conventional-Commits-style commits, and applies autonomously. No TUI. Falls back to /commit-compose when the tree is clean.
when_to_use: The user wants their current uncommitted changes committed for them, quickly and without a TUI - "commit my changes", "commit this", "fast commit", "just commit it", "commit the working tree", "split my changes into commits". Use for uncommitted work; for rewriting commits that already exist use commit-compose instead.
argument-hint: 'optional: free-text hint, e.g. "keep tests separate"'
model: sonnet
# This is the fast action: the user picked it over /commit-compose precisely to
# trade some grouping/scope deliberation for latency.
effort: low
allowed-tools:
  - Bash(commit-composer *)
  - Bash(${CLAUDE_PLUGIN_ROOT}/bin/commit-composer *)
  - Bash(git *)
  - Read
  - Write
---

# /cc-commit

You are helping the user turn their **uncommitted working-tree changes**
into one or more Conventional-Commits-style commits, without a TUI.
Claude (you) reads the diff, decides on 1+ commit groups, writes the
proposal, and the binary autonomously stages + commits each group.

This is the fast-action sibling of `/commit-compose`. There is **no
picker TUI**, **no review TUI**, **no chat y/N**. Just analyse and
commit.

## The binary

Every step is a single `commit-composer ...` invocation - no `mktemp`,
no `$(...)` command substitution, no `sh -c`, no editor pop. The binary
does all the shell glue internally so this flow issues clean, one-line
commands that a `Bash(commit-composer *)` allow rule covers.

`commit-composer` must be on `$PATH` (Homebrew installs it there; from
source, `./scripts/install.sh` installs it via `go install`). If it is
not found, tell the user to run `./scripts/install.sh` and stop.

## Commit-message rules

Every commit message you propose MUST follow the rules in
`${CLAUDE_PLUGIN_ROOT}/skills/commit-compose/references/commit-message-rules.md` -
Conventional Commits format, specific feature/module/service scope (not
generic technology scopes), imperative lowercase summary, no trailing
period, max 72 characters, kebab-case scope when multi-word.

Read that file before writing any message. The scope-selection
cheatsheet at the end of it is the load-bearing part: ask "what is this
change ABOUT?" and use the answer as the scope. The technology is just
the tool the change happens to use.

## Argument

`$ARGUMENTS` is an **optional free-text hint** that steers Claude's grouping
or message style. Examples:

- `/cc-commit` - pure auto.
- `/cc-commit keep test files in their own commit` - bias toward a
  dedicated test commit.
- `/cc-commit one commit only` - bias toward folding everything into
  a single commit.
- `/cc-commit scope all under platform` - bias scope choice.

Treat the hint as guidance, not a hard contract. If the diff genuinely
needs a different shape (e.g. user asked for 1 commit but two clearly
unrelated topics are touched), follow the diff and note the deviation
in a single chat line.

## Speed rule (do not violate)

The user has explicitly asked for this to be a fast action. Do NOT run
diagnostic bash commands (env dumps, version checks, `git status`,
etc.). Do NOT narrate "let me check ...". Do NOT print the full diff
in chat.

The prepare step already ran at load time (section 1), so the intended
shape is **two tool calls total**: one `Write` of the proposal JSON,
one `Bash` apply. Anything more than that is you re-fetching something
you already have.

## 1. Pre-flight + prepare (already done)

The prepare step ran while this skill was loading. Its output:

```!
commit-composer __cc-prepare || true
```

Do **not** run `__cc-prepare` again - everything it produces is in the
block above. Parse it:

- `DIRTY=no` - the working tree is clean; **switch to the
  `/commit-compose` flow** (see 1a). Nothing else is printed.
- `DIRTY=yes` - followed by:
  - `PLAN_FILE=<path>`  - the synthesized WORKING plan.
  - `SPLITS_DIR=<path>` - directory holding the analysis artifacts.
  - `FILES=<path>`      - name-status lines for every dirty file
    (staged + unstaged + untracked).
  - `--- BEGIN FILES ---` / `--- END FILES ---` - those name-status
    lines inline. When the markers are present you have the full file
    list already; do **not** `Read` the `FILES` path. Only if
    `FILES_TRUNCATED=<n>` appears instead (more than 300 dirty files)
    do you read `FILES` yourself.

`__cc-prepare` also wrote, under `SPLITS_DIR`:

- `WORKING.diff`   - the unified diff of the dirty tree vs HEAD.
- `WORKING.hunks.json` - parsed hunks (ignore unless you want
  hunk-level splits).
- `manifest.json`  - structured pool list (one `WORKING` entry).

If the block shows a `commit-composer:` error instead (not a git repo,
binary missing), surface that one line and stop.

### 1a. Clean-tree fallback

On `DIRTY=no` the working tree has nothing to fast-commit. Read
`${CLAUDE_PLUGIN_ROOT}/skills/commit-compose/SKILL.md` and follow it
from its launch step onward, passing the user's `$ARGUMENTS` through as
the range argument. Do NOT duplicate that flow inline here.

Print one line of chat acknowledging the fallback (e.g. "working tree
clean, opening the commit-compose TUI instead") and continue.

## 2. Propose groups

**Start with the filenames from the injected `FILES` block**: if the
paths make the topical boundary obvious (e.g. a CI workflow file + a
Dockerfile + an unrelated README change), you can group without reading
anything else at all. Reading the full diff is the single biggest token
sink in this workflow.

Only read `SPLITS_DIR/WORKING.diff` when:

- One file mixes two clearly different concerns and you need to know
  *what* changed to decide grouping.
- Filenames are generic (`utils.go`, `helpers.py`) and don't reveal
  the topic.
- The user's `$ARGUMENTS` hint requires understanding the content.

**Decide on 1+ groups.** Output count is YOUR judgment - 1, 2, 5, or
more are all valid. Decide from the diff, not from a fixed number:

- One feature spread across many files -> 1 group.
- Two unrelated topics in the dirty tree -> 2 groups.
- An unrelated docs tweak alongside a feature -> split it out.

Factor in the user's `$ARGUMENTS` hint when non-empty.

**Every file** the dirty tree touches must appear in **exactly one**
group's `files` array. The binary fails the apply if anything is left
uncommitted afterward (`executeUncommittedRecompose` clean check).

Write the proposal to `SPLITS_DIR/WORKING.split.json`:

```json
{
  "sha": "WORKING",
  "pool_size": 0,
  "groups": [
    { "files": ["path/to/a.go", "path/to/a_test.go"], "message": "feat(scope): summary" },
    { "files": ["docs/scope.md"], "message": "docs(scope): explain new behaviour" }
  ]
}
```

- `sha` MUST be the literal string `"WORKING"`.
- `pool_size` MUST be `0` (marks the working-tree path; the binary's
  uncommitted code path ignores `pool_size`).
- Each group's `message` MUST be non-empty and MUST follow the
  **Commit-message rules** referenced above.
- Each group's `files` MUST list every path that should land in that
  commit.

Do NOT print the proposal in chat. The user does not review it
beforehand - this is the autonomous fast action.

## 3. Apply

Substitute the `PLAN_FILE` and `SPLITS_DIR` values from step 1:

```bash
commit-composer --apply --plan=<PLAN_FILE> --splits=<SPLITS_DIR>
```

The binary will:

1. Verify the working tree matches the plan (dirty + WORKING op).
2. `git reset` to clear the index.
3. For each group, in order: `git add <files>` + `git commit -m <msg>`.
4. Verify the working tree is clean afterward (every dirty file was
   placed in exactly one group).
5. Print one `COMMITTED <shortsha> <subject>` line per created commit.

If apply exits non-zero, surface the error and stop. **Do not** run
`git reset --hard` or any cleanup; the user inspects what happened.

## 4. Brief summary

Use the `COMMITTED` lines the apply step printed - no separate
`git log`. Echo them back, e.g.:

```
Committed:
  abc1234 feat(auth): add token refresh helper
  def5678 docs(auth): document the refresh flow
```

Keep the output to N+1 lines total.

## 5. No cleanup of temp files

Leave `PLAN_FILE` and `SPLITS_DIR` on disk. The user has explicitly
forbidden auto-rm of these artifacts (they like to inspect them after
runs).

## Failure modes (one-line surface, then stop)

- Not in a git repo -> `__cc-prepare` errors with `"not in a git
  repository"`.
- Binary missing -> `commit-composer` not on `$PATH`; tell the user to
  run `./scripts/install.sh`.
- `__cc-prepare` fails -> surface the binary's stderr.
- Apply fails -> surface the binary's stderr. The working tree may be
  partially committed (some groups landed, later ones did not); do
  not attempt recovery, let the user inspect.

In every error case, **do not** discard the user's uncommitted work.
