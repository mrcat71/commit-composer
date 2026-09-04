# Installing commit-composer

`commit-composer` ships as a Claude Code plugin - two skills
(`commit-compose` and `cc-commit`), each also callable as a slash
command - backed by a Go TUI binary. Claude Code installs plugins
through a *marketplace*, so even for a personal local install you
register the repo as a marketplace and then install the plugin from it.

## Prerequisites

- Recent `git` on `$PATH`
- Claude Code CLI (for the `/plugin` slash commands)
- A terminal that supports one of the overlay backends commit-composer
  knows about, or a regular terminal (it falls back to inline)
- Go 1.27+ only if installing from source

## Option A: Homebrew (recommended)

Works on macOS (Homebrew) and Linux (Linuxbrew).

```bash
brew tap mrcat71/tap
brew install commit-composer
```

This installs:

- the `commit-composer` binary to `$(brew --prefix)/bin/`
- the plugin files to `$(brew --prefix)/share/commit-composer/`:
  `.claude-plugin/` (manifests), `skills/`, and `scripts/` (launcher)

`$(brew --prefix)` is:

| Platform                 | Prefix                       |
|--------------------------|------------------------------|
| Apple Silicon macOS      | `/opt/homebrew`              |
| Intel macOS              | `/usr/local`                 |
| Linux (Linuxbrew)        | `/home/linuxbrew/.linuxbrew` |

Then in a Claude Code session, one-time setup:

```
/plugin marketplace add $(brew --prefix)/share/commit-composer
/plugin install commit-composer@mrcat71
/reload-plugins
```

`commit-composer` is the plugin name; `mrcat71` is the
marketplace name (declared in `marketplace.json`).

Future upgrades:

```bash
brew upgrade commit-composer
```

The marketplace path is stable across versions, so no re-registration is
needed. Run `/plugin marketplace update` or restart Claude Code to pick
up plugin-file changes.

## Option B: From source

```bash
git clone https://github.com/mrcat71/commit-composer
cd commit-composer
./scripts/install.sh
```

This produces `bin/commit-composer`. The bash launcher prefers that
bundled binary over any `commit-composer` on `$PATH`. To also install
the binary system-wide:

```bash
go install ./cmd/commit-composer
# Binary lands in $(go env GOPATH)/bin/commit-composer
```

In a Claude Code session, run:

```
/plugin marketplace add /absolute/path/to/commit-composer
/plugin install commit-composer@mrcat71
/reload-plugins
```

You should now see both entry points listed under `/help` and in the
skills index:

- `/commit-composer:commit-compose` - picker + review TUI, applies via
  `git rebase -i`
- `/commit-composer:cc-commit` - fast no-TUI commit of the working tree

## Verify it works

In a git repo with at least a couple of commits:

```
/commit-composer:commit-compose HEAD~3
```

The TUI should launch in an overlay (tmux popup / Zellij floating /
kitty overlay / wezterm split / iTerm split / inline fallback), let you
mark each commit, and on Enter print the plan back to the session for
confirmation.

## Known issues

- **Local-directory marketplaces**: there is a known Claude Code bug
  ([anthropics/claude-code#14929](https://github.com/anthropics/claude-code/issues/14929))
  where components from a *directory-based* local marketplace are
  sometimes not discovered. If neither
  `/commit-composer:commit-compose` nor `/commit-composer:cc-commit`
  shows up after install, the workaround is to push the repo to git and
  use `/plugin marketplace add <user>/<repo>` instead of the local
  path.

- **Desktop app plugin UI** currently only supports marketplace-based
  installs, not local-directory registration. Use the CLI for the
  local-path workflow above.

- **Cache invalidation**: after editing plugin files in-tree, run
  `/reload-plugins` to pick changes up without restarting. If a change
  still does not show up:
  ```bash
  rm -rf ~/.claude/plugins/cache
  ```
  then re-run `/reload-plugins`.

## Uninstall

```
/plugin uninstall commit-composer
/plugin marketplace remove mrcat71
```

If installed via Homebrew:

```bash
brew uninstall commit-composer
brew untap mrcat71/tap   # optional, only if you have no other plugins from the tap
```

To rebuild the binary after a code change:

```bash
./scripts/install.sh   # or: go install ./cmd/commit-composer
```

No `/reload-plugins` is needed for binary-only changes - the launcher
re-executes the binary on every invocation.

## Override the binary location

By default the launcher looks up the binary in this order:

1. `$COMMIT_COMPOSER_BIN` (if set and executable)
2. `<plugin-root>/bin/commit-composer` (bundled build - checked before
   `$PATH` so a stale `go install`ed binary never shadows the one that
   ships with the current plugin install)
3. `commit-composer` on `$PATH`
4. `go run ./cmd/commit-composer` from the cloned repo (dev fallback)

Setting `COMMIT_COMPOSER_BIN=/path/to/your/binary` is useful when
testing a development build without overwriting the installed one.

## Override the launcher script

The slash commands run the terminal-overlay launcher through
`commit-composer __launch`, which resolves `launch-commit-composer.sh`
in this order:

1. `$COMMIT_COMPOSER_LAUNCHER` (if set and executable)
2. `$CLAUDE_PLUGIN_DATA/scripts/launch-commit-composer.sh` (user override)
3. `<root>/scripts/launch-commit-composer.sh` for each of two roots in
   turn: the `--plugin-root` the skill passes, then `$CLAUDE_PLUGIN_ROOT`
4. `<root>/.claude-plugin/scripts/launch-commit-composer.sh` for the same
   two roots (pre-0.4 layout, for a stale plugin cache)
5. `<binary-dir>/../scripts/launch-commit-composer.sh` (source checkout)

To customise terminal-overlay behaviour without forking the plugin,
either point `COMMIT_COMPOSER_LAUNCHER` at your script or drop a
replacement into your Claude data directory:

```
$CLAUDE_PLUGIN_DATA/scripts/launch-commit-composer.sh
```
