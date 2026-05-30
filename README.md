# commit-composer

A TUI for marking git commits with rebase actions and applying them via
`git rebase -i`. Ships as a Claude Code plugin with two slash commands:

- `/commit-composer:commit-compose` - the full TUI flow for reshaping
  committed history (interactive picker + review TUI).
- `/commit-composer:cc-commit` - fast-action sibling that turns the
  current **uncommitted** working tree into 1+ Conventional-Commits-
  style commits autonomously, no TUI.

Both have model-invoked skill aliases (`commit-composer:commit-composer`
and `commit-composer:cc-commit`).

```
+- Commits (5) ---------------+- Commit a1b2c3d -----------+
| > a1b2c3 [pick        ] feat| SHA:    a1b2c3d...         |
|   d4e5f6 [squash      ] cach| Author: jane <jane@ex.com> |
|   789abc [reword      ] refa| Date:   2026-05-14 10:12   |
|   0fedcb [drop        ] wip | Message:                   |
|   1234567 [claude-split] mix|   chore: misc tidy         |
|                             | Split:  logical            |
|                             | Files:  M auth.go A test.go|
|                             |  diff --git a/auth.go ...  |
+-----------------------------+----------------------------+
 [p] pick  [r] reword  [s] squash  [f] fixup  [d] drop  [e] edit  [c] claude-split  ⏎ apply  q cancel  ? help
```

## Install

### Homebrew (recommended)

Works on macOS (Homebrew) and Linux (Linuxbrew).

```bash
brew tap mrcat71/tap
brew install commit-composer
```

Then in a Claude Code session, one-time setup:

```
/plugin marketplace add $(brew --prefix)/share/commit-composer
/plugin install commit-composer@mrcat71
/reload-plugins
```

`$(brew --prefix)` resolves to `/opt/homebrew` on Apple Silicon,
`/usr/local` on Intel Macs, and `/home/linuxbrew/.linuxbrew` on Linux.
Run it once in a shell to see your actual path if you'd rather hard-code
it in the slash command.

Future upgrades are just `brew upgrade commit-composer` - the marketplace
path stays stable, so no re-adding is needed. Run `/plugin marketplace update`
or restart Claude Code to pick up plugin-file changes.

### From source

```bash
git clone https://github.com/mrcat71/commit-composer
cd commit-composer
./scripts/install.sh
```

Then, inside a Claude Code session:

```
/plugin marketplace add /absolute/path/to/commit-composer
/plugin install commit-composer@mrcat71
/reload-plugins
```

Requires Go 1.24+ and a recent `git` on `$PATH`.

> Known issue: slash commands from directory-based local marketplaces
> can fail to register
> ([claude-code#14929](https://github.com/anthropics/claude-code/issues/14929)).
> The `commit-composer` skill alias is unaffected. If `/commit-composer:commit-compose`
> does not appear, push the repo to git and
> `/plugin marketplace add <user>/<repo>` instead.

## Usage

### `/commit-composer:commit-compose` (reshape committed history)

```
/commit-composer:commit-compose                # last <upstream>..HEAD (default), or HEAD~10..HEAD
/commit-composer:commit-compose HEAD~5         # the last 5 commits
/commit-composer:commit-compose <base>..<head> # explicit range
```

The slash command will:

1. Allow a dirty working tree (the TUI shows a synthetic "uncommitted"
   row you can opt into).
2. Refuse if any commit in the range is reachable from a protected
   branch (`origin/main`, `origin/master`, `upstream/main`,
   `upstream/master`).
3. Launch the TUI in an overlay (tmux popup / Zellij floating / kitty
   overlay / wezterm split / iTerm split / inline).
4. Capture your structured plan and show it back for confirmation.
5. Apply the plan via `git rebase -i <base>` driven non-interactively by
   `GIT_SEQUENCE_EDITOR` and `GIT_EDITOR` helpers.

### `/commit-composer:cc-commit` (fast-commit the dirty tree)

```
/commit-composer:cc-commit                          # auto-split + commit, no TUI
/commit-composer:cc-commit keep tests separate      # hint biases grouping
/commit-composer:cc-commit one commit only          # bias toward a single commit
```

The slash command will:

1. Refuse cleanly if the repo isn't a git repo.
2. If the working tree is clean, fall back to `/commit-compose` (the
   full TUI flow on already-committed history).
3. Otherwise: read the dirty diff, decide on 1+ Conventional-Commits-
   style groups, write each `git add` + `git commit` autonomously.

No picker TUI, no review TUI, no chat confirmation. Use this when you
just want your working tree turned into commits. Use `/commit-compose`
when you want to reshape history that is already committed.

You can also run the binary directly:

```bash
commit-composer HEAD~5                              # interactive TUI, prints plan to stdout
commit-composer --output=plan.txt HEAD~5            # write plan to a file
commit-composer --apply --plan=plan.txt             # apply (no claude-split)
commit-composer --apply --plan=plan.txt --splits=DIR # apply (with claude-split JSONs)
commit-composer __split-prepare --plan=FILE --out=DIR # extract diffs for Claude to analyse
commit-composer --list HEAD~5                       # print resolved commits and exit
```

## Keybindings

```
j / ↓         cursor down
k / ↑         cursor up
J             move highlighted commit DOWN
K             move highlighted commit UP
p             pick
r             reword  (choose: [e] $EDITOR  [c] ask Claude)
s             squash  (fold into previous, keep both messages)
f             fixup   (fold into previous, drop this message)
d             drop
e             edit    (rebase pauses on this commit)
c             claude-recompose (mark / unmark)
space         cycle action
ctrl+j / ctrl+k    scroll diff
ctrl+d / ctrl+u    diff page down / up
enter         confirm plan and exit
q / esc       cancel
?             toggle help overlay
```

### claude-recompose

Pressing `c` marks a commit so Claude will redesign its place in the
history during apply. **Consecutive marked commits get pooled into a
single analysis batch** — mark 3 in a row and Claude looks at the
combined diff, then proposes a fresh sequence of commits grouped by
feature/topic (not mechanically by file).

After the TUI confirmation, Claude shows the full combined plan
(rebase actions + per-pool proposed groups + protected-branch warning,
if any) and offers three options:

- `y` apply the plan
- `c` leave a comment in plain English ("merge groups 1 and 2",
  "give pool A three commits instead of two", "rename the first
  commit") - Claude revises the proposal and asks again
- `n` cancel

### claude-reword

Pressing `r` opens an inline chooser:

- `e` edit the message in `$EDITOR` (the existing manual flow)
- `c` ask Claude to propose a new message
- `esc` cancel

If you pick `c`, the commit is marked `claude-reword` in the plan.
After you confirm the TUI, the slash command extracts the current
message plus the commit diff, asks Claude to draft a replacement under
the project's commit-message rules, and opens `$EDITOR` per commit with
the proposal pre-filled so you can review or edit before it lands.
The final, user-approved message is what ends up in the rewritten
history.

### Commit-message style

`claude-recompose` and `claude-reword` both produce Conventional
Commits (`<type>(<scope>): <summary>`). The scope is required when one
is clear and should describe the affected feature/module/service/
chart/role/package rather than the implementation technology (so
`feat(gitlab-access-token): ...` rather than `feat(terraform): ...`).
The full ruleset lives in
[`.claude-plugin/commands/commit-compose.md`](.claude-plugin/commands/commit-compose.md)
under "Commit-message rules"; the binary has no validator, so the
rules are enforced by Claude's prompt and your `$EDITOR` review pass.

## Plan format

The binary emits a line-based v1 plan:

```
## commit-composer plan v1
base: <full-sha>
range: <base>..<head>
ops:
- pick   a1b2c3d
- squash d4e5f6a
- reword 789abc0 :: new commit message here
- drop   0fedcba
- pick   1234567
```

Multi-line reword messages are base64-encoded:

```
- reword 789abc0 :: b64::Zm9vCgpiYXIK
```

`claude-recompose` ops carry no extra metadata in the plan line itself;
consecutive marked commits form a pool:

```
- claude-recompose 1234567
- claude-recompose 89abcde
- claude-recompose fedcba0
```

`claude-reword` ops are per-commit and likewise carry no message in the
plan line emitted by the TUI - the slash command fills the message in
before apply by rewriting these lines to regular `reword` ops:

```
- claude-reword 1234567
```

The actual split decisions (file groups + new commit messages) live in
separate `<lastSHA>.split.json` files in the splits directory, keyed by
the LAST commit of each pool, with `pool_size` describing how many
commits to dissolve:

```json
{
  "sha": "fedcba0...",
  "pool_size": 3,
  "groups": [
    { "files": ["auth.go", "auth_test.go"], "message": "feat: add Auth" },
    { "files": ["docs/auth.md"],            "message": "docs: explain Auth" }
  ]
}
```

An empty stdout (or a non-zero exit) means "cancelled - do nothing".

## Safety

- The plugin refuses to start if the working tree is dirty.
- The plugin refuses to rewrite commits already on a protected branch
  (`origin/main`, `origin/master`, `upstream/main`, `upstream/master`).
- The plugin never runs `git push --force`. If you need to publish the
  rewritten history, it prints the suggested
  `git push --force-with-lease` command for you to run yourself.
- On rebase conflict, the plugin stops and surfaces the conflict; it
  never runs `git rebase --abort` automatically.

## Repo layout

```
commit-composer/
├── README.md
├── LICENSE                              # MIT
├── go.mod
├── cmd/commit-composer/main.go
├── internal/
│   ├── git/        # exec wrappers + rebase driver
│   ├── plan/       # data model + serialize / parse
│   └── tui/        # bubbletea Model / Update / View
├── .claude-plugin/
│   ├── plugin.json
│   ├── marketplace.json
│   ├── commands/
│   │   ├── commit-compose.md           # full TUI flow
│   │   └── cc-commit.md                # fast no-TUI working-tree commit
│   ├── skills/
│   │   ├── commit-composer/SKILL.md    # alias for /commit-compose
│   │   └── cc-commit/SKILL.md          # alias for /cc-commit
│   └── scripts/
│       ├── launch-commit-composer.sh   # terminal-overlay dispatcher
│       └── resolve-launcher.sh         # user-override resolver
├── scripts/install.sh
└── docs/
    ├── commit-composer-plan.md         # original design plan
    ├── install.md                      # full install + troubleshooting
    └── next-steps.md                   # remaining milestones and follow-ups
```

## Development

```bash
go test ./...                              # unit + integration tests
go build ./...                             # build everything
./scripts/install.sh                       # build into .claude-plugin/bin
go run ./cmd/commit-composer HEAD~5        # run against current repo
```

## Acknowledgements

The plugin manifest, skill-alias pattern, and bash-driven overlay
detection are adapted from
[umputun/revdiff](https://github.com/umputun/revdiff).

## License

MIT - see [LICENSE](LICENSE).
