# CI helper scripts

CI-only helpers. Not part of the shipped app — this is a separate, tiny npm
project so its one dependency doesn't touch the frontend or the Go module.

## `check-commit-message.js`

Fails a PR whose **squash commit** release-please would be unable to parse.

release-please parses every squash commit with `@conventional-commits/parser`.
That parser throws on certain bodies — most infamously a parenthesis `( ... )`
that wraps across a line break — and then silently skips the commit
(`Considering: 0 commits` → no release PR). A `feat:`/`fix:` PR merges clean but
no release is ever cut. That happened once (PR #196 → the 2.2.0 release never
fired), and the body can't be fixed after squash-merge because `main` is
protected + linear-history. So this guard runs at PR time, on the reconstructed
squash message (PR title + body), and fails while the author can still edit the
description. It re-runs on every PR edit.

Run by the `commit-message` job in `.github/workflows/pr.yml`.

```sh
cd scripts && npm ci
PR_TITLE='feat: something' PR_BODY='body...' npm run check-commit-message
```

### Keeping the parser in lockstep with release-please

`@conventional-commits/parser` is pinned (exact version, no `^`) to the version
release-please depends on, so this guard fails in exactly the cases the real
release run would. When bumping release-please, check its `@conventional-commits/parser`
dependency and update `package.json` here to match.
