# Working in this repo

This file teaches an agent how to make changes here without breaking the CI/release pipeline. Read alongside `README.md` (which is the user-facing doc).

## Branch & commit flow

- **`main` is protected.** No direct pushes. All changes land via PR, squash-merged. Linear history is enforced.
- **PR titles are Conventional Commits.** `pr.yml` blocks merge if the title doesn't match. Allowed types: `feat`, `fix`, `chore`, `docs`, `refactor`, `test`, `perf`, `build`, `ci`, `revert`.
- **The PR title becomes the squash commit on `main`** — and that's what release-please reads. So the title is load-bearing:
  - `feat: ...` → minor bump on next release.
  - `fix: ...` → patch bump.
  - `chore:` / `docs:` / `ci:` / `refactor:` / `test:` / `build:` → hidden from changelog, no version bump.
  - **Never write `BREAKING CHANGE:` in the body** unless you actually want a major bump.
- One PR = one logical change. Don't bundle a refactor into a `feat:` PR — the changelog will lie.

## Pre-flight checks before opening / pushing a PR

`pr.yml`'s `verify` job runs all of these. Run them locally first; a failed CI round-trip is wasted minutes.

```
gofmt -l .                     # must print nothing
go vet ./...
golangci-lint run              # errcheck, staticcheck, unused, ineffassign, gosec
go test -race -count=1 ./...
govulncheck ./...
```

Or just `task test` for the test portion.

## Build

`task build` → `dist/SerialHop.exe` (default GOOS=windows, GOARCH=amd64). Override via `task build GOOS=linux GOARCH=arm64`. `jq` is required on the build host.

Generated artifacts (gitignored — **never commit**):
- `assets/manifest.xml` (rendered from `assets/manifest.template.xml` by `tools/render-manifest`)
- `cmd/serialhop/resource_windows.syso`
- `dist/`, `*.exe`

## Tooling rule: Go programs, not shell

`tools/render-manifest` and `tools/buildcmd` exist because Task's embedded shell (`mvdan.cc/sh`) mangles single-quoted args on Windows — `sed`/`awk` pipelines that work on macOS silently break on the Windows release runner. **If a build step needs more than one command or any quoting, write it as a Go program under `tools/`, not as shell in `Taskfile.yaml`.**

## Releases — what NOT to touch

The release flow is fully automated by `release-please.yml`. Don't:

- Hand-edit `.release-please-manifest.json` (release-please owns it).
- Hand-edit the version strings in `assets/version.json` (release-please bumps them on the release PR).
- Create git tags or GitHub releases manually. Tags are created by the release-please action when its PR merges.
- Add the `FixedFileInfo.*.{Major,Minor,Patch}` integer fields to `extra-files` in `release-please-config.json`. The `json` updater is string-only and will silently no-op. The release-build job derives them from the string field via a jq step — that's the *only* sanctioned mechanism.

To ship a release: merge `feat:` / `fix:` PRs → wait for the "chore(main): release X.Y.Z" PR to appear (or update) → squash-merge it → `release-build` publishes `SerialHop-vX.Y.Z.exe` + `SHA256SUMS.txt` + Sigstore attestation. That's it.

If a release-please PR is missing CI checks, the GitHub App token has likely expired/rotated — see the design notes in `docs/superpowers/specs/2026-05-01-ci-design.md`. Closing-and-reopening the PR is the manual workaround.

## Cross-platform testing

Tests must pass on **both macOS and Windows**. Windows-only code (service worker, real SCM client, walk panel, UAC helpers) lives in `_windows.go` files; their logic is covered by fakes that compile and run on macOS/Linux. When adding Windows-only code, add the equivalent fake so coverage doesn't regress on non-Windows runners.

## Dependabot

Weekly bumps land as `chore(deps): ...` / `chore(ci): ...` PRs (Mondays 06:00 UTC). Review and merge like any other PR. **Don't auto-merge `actions/checkout` v6+** — it's incompatible with `golang/govulncheck-action@v1` (`Duplicate header: "Authorization"`). Stay on v4 until the upstream issue resolves.

## Where to look

- Design rationale for CI/release: `docs/superpowers/specs/2026-05-01-ci-design.md`.
- Implementation plan: `docs/superpowers/plans/2026-05-01-ci.md`.
- App / device design: `docs/superpowers/specs/2026-04-26-lab-devices-client-design.md`.
- Log streaming: `docs/superpowers/specs/2026-04-28-log-streaming-design.md`.
