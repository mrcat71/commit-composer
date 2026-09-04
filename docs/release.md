# Releasing commit-composer to Homebrew

commit-composer ships as a Homebrew tap so users can install it with:

```bash
brew tap mrcat71/tap
brew install commit-composer
```

This document covers the **one-time setup** and the **per-release** workflow.

## One-time setup (do this once, ever)

### 1. Push the main repo to GitHub

```bash
cd /Users/andrei/Personal/commit-composer
git add -A
git commit -m "Initial public release of commit-composer"
git push origin main   # or whatever branch you use
```

The repo URL is already `https://github.com/mrcat71/commit-composer.git`.

### 2. Create the tap repository

Homebrew taps must live in a repo named `homebrew-<something>`. The shortest
useful name is just `homebrew-tap`:

1. On GitHub, create a new repo: `mrcat71/homebrew-tap`
2. Make it public
3. Initialize it with a README (or leave empty - goreleaser will create the
   first commit). The repo only needs to exist; goreleaser writes the
   Formula file on the first release.

The default branch must be `main` (or you must change `branch: main` in
`.goreleaser.yaml` to match).

### 3. Create a Personal Access Token for the tap

GitHub Actions' built-in `GITHUB_TOKEN` can only write to the repo that
triggered the workflow. To push the Formula to a *different* repo
(`homebrew-tap`), you need a separate Personal Access Token.

1. Go to https://github.com/settings/tokens (the classic-tokens page works,
   or fine-grained tokens with `Contents: read & write` on the `homebrew-tap`
   repo)
2. Generate a new token with `repo` scope
3. Copy the token (you won't see it again)

### 4. Add the token as a secret on the main repo

1. On `https://github.com/mrcat71/commit-composer`, go to
   `Settings -> Secrets and variables -> Actions`
2. Add a new repository secret:
   - Name: `HOMEBREW_TAP_TOKEN`
   - Value: the PAT you just copied

That's it. From now on the release flow is one command.

## Per-release workflow

```bash
# Make sure main is clean and pushed.
git checkout main && git pull

# Pick a version. Use semver.
TAG=v0.1.0

# Tag and push.
git tag -a "$TAG" -m "Release $TAG"
git push origin "$TAG"
```

The `release.yml` GitHub Actions workflow triggers on tags matching `v*`.
It will:

1. Check out the tagged commit
2. Install Go 1.27
3. Run `goreleaser release --clean`, which:
   - Builds `commit-composer` for `darwin-arm64` and `darwin-amd64` with
     `-X main.version=$TAG` baked in
   - Packages each as `commit-composer_<tag>_darwin_<arch>.tar.gz`
   - Computes SHA256 checksums (`checksums.txt`)
   - Creates a GitHub Release in this repo with the archives + checksums
     attached
   - Auto-generates a changelog from commits since the previous tag
     (filtering out `docs:`, `test:`, `chore:`, `ci:`, `style:` prefixes)
   - Pushes an updated `Formula/commit-composer.rb` to
     `mrcat71/homebrew-tap`

After the workflow finishes (usually ~3-5 minutes), users can install
the new version with:

```bash
brew update
brew upgrade commit-composer
```

First-time install (any user, anywhere):

```bash
brew tap mrcat71/tap
brew install commit-composer
```

## Local dry-run (optional, for testing)

Before tagging, you can dry-run goreleaser locally to catch config
problems without pushing anything:

```bash
# Install goreleaser via brew if you don't have it.
brew install goreleaser

# Dry run - builds binaries into ./dist/ but does NOT publish.
goreleaser release --snapshot --clean --skip=publish
ls dist/
```

The snapshot mode tags the version `0.0.0-next` and skips the Formula
push, so it's safe to run repeatedly. Check `dist/` for the binaries and
`dist/commit-composer_*.tar.gz` archives.

## Troubleshooting

### "HOMEBREW_TAP_TOKEN" missing

The CI workflow checks `${{ secrets.HOMEBREW_TAP_TOKEN }}`. If goreleaser
fails with a 401 or "could not find token", the secret is not set or has
the wrong scope. Verify it exists in
`Settings -> Secrets and variables -> Actions` of the
`commit-composer` repo, and that the PAT has `repo` scope on the
`homebrew-tap` repo.

### Tag already exists

If you accidentally pushed a tag and want to redo:

```bash
git tag -d v0.1.0           # delete locally
git push origin :v0.1.0     # delete on GitHub
```

Then re-tag and push as normal. Note: any GitHub Release attached to
the old tag is NOT auto-deleted - you may need to remove it via the
GitHub Releases UI.

### Skipping CI for non-release work

CI only triggers on tags matching `v*`. Normal pushes to `main` do not
trigger releases. If you want to add other CI checks later (lint, tests
on each PR), add separate workflows under `.github/workflows/`.

### Updating an existing install

Once published, end users update with:

```bash
brew update
brew upgrade commit-composer
```

If they hit a stale version, `brew untap mrcat71/tap && brew tap
mrcat71/tap` forces a fresh fetch of the Formula.
