---
description: Fast-commit the working tree. Claude analyses the dirty diff, splits it into 1+ Conventional-Commits-style commits, and applies autonomously. No TUI. Falls back to /commit-compose when the tree is clean.
argument-hint: 'optional: free-text hint, e.g. "keep tests separate"'
allowed-tools: [Bash, Read, Write]
---

# /cc-commit

You are helping the user turn their **uncommitted working-tree changes**
into one or more Conventional-Commits-style commits, without a TUI.
Claude (you) reads the diff, decides on 1+ commit groups, writes the
proposal, and the binary autonomously stages + commits each group.

This is the fast-action sibling of `/commit-compose`. There is **no
picker TUI**, **no review TUI**, **no chat y/N**. Just analyse and
commit.

## Commit-message rules

Every commit message you propose MUST follow the **Commit-message
rules** documented at the top of
`${CLAUDE_PLUGIN_ROOT}/.claude-plugin/commands/commit-compose.md` -
Conventional Commits format, specific feature/module/service scope (not
generic technology scopes), imperative lowercase summary, no trailing
period, max 72 characters, kebab-case scope when multi-word.

The scope-selection cheatsheet is the load-bearing part: ask "what is
this change ABOUT?" and use that as the scope. The technology is just
the tool the change happens to use.

## Argument

`$1` is an **optional free-text hint** that steers Claude's grouping
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
diagnostic bash commands (env dumps, version checks, list-clients,
etc.). Do NOT narrate "let me check ...". Do NOT print the full diff
in chat. Read the artifacts on disk, write the proposal JSON, apply.

## 1. Pre-flight + branch on tree state

Run **exactly one** bash block to detect the working tree state and
locate the binary:

```bash
set -e
git rev-parse --git-dir >/dev/null 2>&1 || { echo "not in a git repository" >&2; exit 1; }

DIRTY="$(git status --porcelain)"
if [ -z "$DIRTY" ]; then
  echo "DIRTY=no"
else
  echo "DIRTY=yes"
fi

BIN="$(command -v commit-composer 2>/dev/null || true)"
[ -z "$BIN" ] && BIN="${CLAUDE_PLUGIN_ROOT}/.claude-plugin/bin/commit-composer"
echo "BIN=$BIN"
```

If the block exits non-zero (not in a git repo), surface the error and
stop.

### 1a. Clean-tree fallback

If `DIRTY=no`, the working tree has nothing to fast-commit. **Switch
to the `/commit-compose` flow**: read the file at
`${CLAUDE_PLUGIN_ROOT}/.claude-plugin/commands/commit-compose.md`
(the path the plugin install resolved `commit-compose.md` to) and
follow it from step 1 onward, passing the user's `$1` through as the
range argument (it may or may not be a valid range; the binary's
launcher will error cleanly if it isn't). Do NOT duplicate the
commit-compose flow inline here - read the file and follow it.

Print one line of chat acknowledging the fallback (e.g. "working tree
clean, opening the commit-compose TUI instead") and continue.

## 2. Synthesize the minimal plan + run __split-prepare

If `DIRTY=yes`, run one more bash block to write the plan and prepare
the analysis artifacts:

```bash
set -e
PLAN_FILE="$(mktemp -t cc-commit-plan-XXXXXX)"
SPLITS_DIR="$(mktemp -d -t cc-commit-splits-XXXXXX)"
cat > "$PLAN_FILE" <<'EOF'
## commit-composer plan v1
base: WORKING
ops:
- claude-recompose WORKING
EOF
echo "PLAN_FILE=$PLAN_FILE"
echo "SPLITS_DIR=$SPLITS_DIR"

"$BIN" __split-prepare --plan="$PLAN_FILE" --out="$SPLITS_DIR"
```

`__split-prepare` writes:

- `$SPLITS_DIR/WORKING.files.txt`   - name-status lines for every
  file in the dirty tree (staged + unstaged + untracked).
- `$SPLITS_DIR/WORKING.diff`        - the unified diff of the dirty
  tree vs HEAD.
- `$SPLITS_DIR/WORKING.hunks.json`  - parsed hunks (not needed for
  file-level grouping; ignore unless you want hunk-level splits).
- `$SPLITS_DIR/manifest.json`       - structured list with one entry
  for `WORKING`.

## 3. Propose groups

Read `$SPLITS_DIR/WORKING.files.txt`. **Start with filenames**: if the
file paths make the topical boundary obvious (e.g. a CI workflow file
+ a Dockerfile + an unrelated README change), you can group without
reading the diff at all. Reading the full diff is the single biggest
token sink in this workflow.

Only read `$SPLITS_DIR/WORKING.diff` when:

- One file mixes two clearly different concerns and you need to know
  *what* changed to decide grouping.
- Filenames are generic (`utils.go`, `helpers.py`) and don't reveal
  the topic.
- The user's `$1` hint requires understanding the content.

**Decide on 1+ groups.** Output count is YOUR judgment - 1, 2, 5, or
more are all valid. Decide from the diff, not from a fixed number:

- One feature spread across many files → 1 group.
- Two unrelated topics in the dirty tree → 2 groups.
- An unrelated docs tweak alongside a feature → split it out.

Factor in the user's `$1` hint when non-empty.

**Every file** the dirty tree touches must appear in **exactly one**
group's `files` array. The binary fails the apply if anything is left
uncommitted afterward (`executeUncommittedRecompose` clean check).

Write the proposal to `$SPLITS_DIR/WORKING.split.json`:

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

## 4. Apply

```bash
"$BIN" --apply --plan="$PLAN_FILE" --splits="$SPLITS_DIR"
```

The binary will:

1. Verify the working tree matches the plan (dirty + WORKING op).
2. `git reset` to clear the index.
3. For each group, in order: `git add <files>` + `git commit -m <msg>`.
4. Verify the working tree is clean afterward (every dirty file was
   placed in exactly one group).

If apply exits non-zero, surface the error and stop. **Do not** run
`git reset --hard` or any cleanup; the user inspects what happened.

## 5. Brief summary

After a successful apply, print one short chat line per created
commit, e.g.:

```
Committed:
  abc1234 feat(auth): add token refresh helper
  def5678 docs(auth): document the refresh flow
```

You can use `git log -<N> --oneline HEAD` where N = number of groups
to fetch the short SHAs. Keep the output to N+1 lines total.

## 6. No cleanup of temp files

Leave `$PLAN_FILE` and `$SPLITS_DIR` on disk. The user has explicitly
forbidden auto-rm of these artifacts (they like to inspect them after
runs).

## Failure modes (one-line surface, then stop)

- Not in a git repo → step 1 errors with `"not in a git repository"`.
- Binary missing → `$BIN` not executable; surface the path and tell
  the user to run `./scripts/install.sh`.
- `__split-prepare` fails → surface the binary's stderr.
- Apply fails → surface the binary's stderr. The working tree may be
  partially committed (some groups landed, later ones did not); do
  not attempt recovery, let the user inspect.

In every error case, **do not** discard the user's uncommitted work.
