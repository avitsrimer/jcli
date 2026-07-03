# GitHub release pipeline (macOS arm64, GoReleaser) + version stamping

## Overview
- Add a tag-driven GitHub Release for `jcli`: version stamping + a `--version`
  flag, a `.goreleaser.yml` (macOS **arm64 only**, cgo-enabled), and a
  `release.yml` workflow that runs GoReleaser on `macos-latest` when a `v*` tag is
  pushed. **No Homebrew tap** (deferred — needs a separate repo + PAT).
- Problem it solves: today there are no tags, no `--version`, and no way to get a
  built binary without cloning + `make install`. This produces a downloadable,
  checksummed archive per tagged version and lets `jcli --version` report the
  build.
- Integration: reuses the existing `.github/workflows/ci.yml` conventions
  (`setup-go` via `go-version-file`, `GOTOOLCHAIN=local`, `golangci-lint-action`).
  The release job uses the **built-in `GITHUB_TOKEN`** (same-repo release — no PAT
  needed). Builds run on a macOS runner because the keychain code is cgo/darwin.

## Context (from discovery)
- `cmd/jcli/main.go`: `run()` intercepts `__agent`, else delegates to
  `cli.Main(args, stdout, stderr)`.
- `internal/cli/cli.go`: `globalOpts` struct (:120, fields `Profile`/`JSON`/
  `Verbose`), `Main` (:229) → `run` → `newParser` (:255) using
  `flags.NewParser(a.global, flags.HelpFlag|flags.PassDoubleDash)` (go-flags).
- No `version`/`revision` var anywhere; `git tag -l` is empty; no `.goreleaser.yml`.
- `Makefile` `build:` (post drop-signing) is `$(GO) build -o $(BINARY) $(PKG)` with
  `GO ?= GOTOOLCHAIN=local go`; `install: build`.
- revdiff reference (`.goreleaser.yml`): `version: 2`, `builds` with
  `ldflags: -s -w -X main.revision=...`, `archives` with `name_template`,
  `release.yml` = `goreleaser/goreleaser-action@v7` `release --clean` on `v*` tags
  with `permissions: contents: write`.
- **Decisions (this planning):** arm64-only; GoReleaser (not hand-rolled).
- **Branch:** this work edits `Makefile` (which the unmerged `keychain-drop-signing`
  branch already changed) — so it must be **stacked on `keychain-drop-signing`**,
  not branched from `main` (else a stale Makefile + merge conflicts). Chain:
  `main → ci/github-actions-and-lint-clean → keychain-drop-signing → <this>`.

## Development Approach
- **Testing approach:** Regular. Task 1 (the `--version` flag) gets a real unit
  test in `internal/cli`. The `.goreleaser.yml` and `release.yml` are config —
  "tested" by `goreleaser check` + a local `goreleaser build --snapshot` (proves
  the cgo arm64 build succeeds) and `actionlint`, not Go unit tests. This is a
  documented, justified deviation from the test-per-task template for the config
  tasks.
- Complete each task fully; run `GOTOOLCHAIN=local go test -race ./...` after any
  code change — must pass before the next task.
- **CRITICAL: update this plan file if scope changes during implementation.**
- Keep `README.md`/`--help` in sync.

## Testing Strategy
- **Unit tests:** existing suite stays green; Task 1 adds a `--version` test
  (prints the version var, exits 0, works with no subcommand).
- **GoReleaser validation:** `goreleaser check` (config valid) and
  `goreleaser build --snapshot --clean --single-target` (actually builds the
  cgo arm64 binary locally — the real proof the release will build). Requires
  `brew install goreleaser`.
- **Lint:** `GOTOOLCHAIN=local golangci-lint run` stays 0 issues.
- **Workflow:** `actionlint .github/workflows/release.yml` clean.
- **No e2e.** The actual GitHub Release (tag push → Actions) is verified manually
  in Post-Completion (it needs a pushed tag and the hosted runner).

## Progress Tracking
- Mark `[x]` immediately; new tasks `➕`; blockers `⚠️`.

## Solution Overview
- **Version var location:** `internal/cli` (`var version = "unknown"`), set via
  ldflags `-X github.com/avitsrimer/jcli/internal/cli.version=<v>`. Placing it in
  `cli` (not `main`) lets the `--version` flag live naturally in `globalOpts` and
  print the same var. `-X` can set an unexported package var by full symbol path.
- **`--version` handling (revised per review — the naive approach fails):** with
  subcommands registered, `parser.ParseArgs` returns `flags.ErrCommandRequired` for
  `jcli --version` *before* any post-parse check runs, and `handleParseError`
  (cli.go:269) only treats `flags.ErrHelp` as a clean exit — so a bare
  `jcli --version` would print an error and exit 1. **Detect it with a pre-parse
  scan** mirroring `extractParams` (cli.go:242): in `run`, before building the
  parser, if the args contain `--version`, write `version` to stdout and return
  `exitOK`. Keep `Version bool \`long:"version"\`` in `globalOpts` **only** for
  `--help` discoverability — the pre-scan is the actual behavior path, so it works
  with no subcommand. (Do NOT switch to `parser.SubcommandsOptional = true` — that
  would regress bare `jcli` from a usage error to a silent success.)
- Also stamp `make build` (via `git describe`) so local/dev builds self-report.
- **GoReleaser:** single `builds` entry, `goos: [darwin]`, `goarch: [arm64]`,
  `env: [CGO_ENABLED=1]`, `main: ./cmd/jcli`, the `-X …cli.version={{.Version}}`
  ldflag; `archives` (tar.gz, `jcli_{{.Version}}_{{.Os}}_{{.Arch}}`, bundling
  `README.md` + `LICENSE` if present); default checksum + release notes.
- **release.yml:** `on: push: tags: ['v*']`, `permissions: contents: write`,
  `macos-latest`, `setup-go` (`go-version-file: go.mod`), `GOTOOLCHAIN=local`,
  `goreleaser-action@v7 release --clean`, `GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}`.

## Technical Details
- **ldflags symbol:** `-X github.com/avitsrimer/jcli/internal/cli.version=…`.
- **Makefile:** add `REV := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)`
  and build with `-ldflags "-X github.com/avitsrimer/jcli/internal/cli.version=$(REV)"`.
- **Gatekeeper reality (document, don't fix):** the released binary is unsigned/
  ad-hoc; a *downloaded* archive is quarantined and blocked by Gatekeeper. Users
  must `xattr -d com.apple.quarantine jcli` (or `xattr -c jcli`) after extracting.
  README must state this. (Notarization is explicitly out of scope.)
- **Self-contained:** the released binary embeds the skill, so post-download the
  user runs `jcli install-skill`. README note.

## What Goes Where
- **Implementation Steps** (checkboxes): version flag + test, Makefile stamping,
  `.goreleaser.yml`, `release.yml`, docs, verification.
- **Post-Completion** (no checkboxes): pushing the first `v*` tag to trigger the
  real release, verifying the artifact + `xattr` unquarantine, Homebrew (deferred).

## Implementation Steps

### Task 1: Add `version` var and `--version` flag

**Files:**
- Modify: `internal/cli/cli.go`
- Create/Modify: `internal/cli/cli_test.go` (or the existing cli test file)

- [x] add `var version = "unknown"` in `internal/cli` (package-level, for ldflags)
- [x] add `Version bool \`long:"version" description:"print version and exit"\`` to
  `globalOpts` (:120) — for `--help` discoverability only
- [x] in `cli.run`, add a **pre-parse scan** (before `newParser`, alongside the
  `extractParams` call at :242): if the incoming args contain `--version`, write
  `version` to stdout and return `exitOK` — so it works with **no subcommand**
  (a post-parse check cannot, because `ParseArgs` returns `ErrCommandRequired`
  first)
- [x] write a test: `Main([]string{"--version"}, &out, &err)` returns 0 (exitOK)
  and `out` contains the version string; assert nothing goes to stderr
- [x] write a test asserting `--version` with NO subcommand does not hit the
  `ErrCommandRequired`/exit-1 path (guards against the naive regression)
- [x] run `GOTOOLCHAIN=local go test -race ./...` and `golangci-lint run` — pass before next

### Task 2: Stamp the version into `make build`

**Files:**
- Modify: `Makefile`

- [x] add `REV := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)`
- [x] change `build:` to pass `-ldflags "-X github.com/avitsrimer/jcli/internal/cli.version=$(REV)"`
- [x] verify `make build && ./jcli --version` prints the git-derived revision
- [x] confirm `make install`, `make test`, `make lint`, `make cross-build` still work
- [x] (no unit test — build-system change; verified via `./jcli --version`)

### Task 3: Add `.goreleaser.yml` (macOS arm64, cgo) and validate locally

**Files:**
- Create: `.goreleaser.yml`

- [x] write `version: 2` config: one `builds` entry (`main: ./cmd/jcli`,
  `binary: jcli`, `goos: [darwin]`, `goarch: [arm64]`, `env: [CGO_ENABLED=1]`,
  ldflags `-s -w -X github.com/avitsrimer/jcli/internal/cli.version={{.Version}}`)
- [x] add `archives` (tar.gz, `name_template: jcli_{{.Version}}_{{.Os}}_{{.Arch}}`).
  **Do NOT add an explicit `files: [README.md, LICENSE]` list** — the repo has **no
  `LICENSE` file**, and a literal missing entry makes goreleaser fail
  ("globbing ... matches no files"). Rely on GoReleaser v2's default archive files
  (globs `README*`/`LICENSE*`/`CHANGELOG*`, silently skipping absent ones) — README
  is picked up automatically, LICENSE skipped. Keep default checksum + release notes
- [x] `brew install goreleaser`; run `goreleaser check` — config valid
- [x] run `goreleaser build --snapshot --clean --single-target` **(must run on an
  arm64 mac** — `--single-target` resolves to the runner's arch, and only
  `darwin/arm64` is in the matrix; on Intel it errors "no matching target"); the
  cgo arm64 binary builds; run `dist/**/jcli --version` to confirm the ldflag injects
- [x] (no unit test — config; validated by goreleaser check + snapshot build)

### Task 4: Add the release workflow

**Files:**
- Create: `.github/workflows/release.yml`

- [x] `on: push: tags: ['v*']`; `permissions: contents: write`
- [x] one job on `macos-latest`: checkout (fetch-depth 0, persist-credentials
  false) → `setup-go@v6` (`go-version-file: go.mod`) → `goreleaser-action@v7`
  with `args: release --clean`, `env: GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}`
  and `GOTOOLCHAIN: local`
- [x] `actionlint .github/workflows/release.yml` — clean
- [x] (no unit test — workflow; validated by actionlint + the manual tag push)

### Task 5: Document releases + version

**Files:**
- Modify: `README.md`
- Modify: `CLAUDE.md`

- [x] `README.md`: add a "Releases" / "Install from a release" note — download the
  `jcli_<v>_darwin_arm64.tar.gz` from GitHub Releases, extract, **`xattr -d
  com.apple.quarantine jcli`** (Gatekeeper: unsigned + quarantined), move to
  `~/bin`, then `jcli install-skill`; mention `jcli --version`
- [x] `CLAUDE.md`: add a short note under CI/release — `release.yml` builds a
  darwin/arm64 GoReleaser archive on `v*` tags via the built-in `GITHUB_TOKEN`;
  binaries are unsigned (Gatekeeper `xattr` needed); Homebrew deferred
- [x] (no unit test — docs)

### Task 6: Verify acceptance criteria
- [ ] `GOTOOLCHAIN=local go test -race ./...` green (incl. the new `--version` test)
- [ ] `GOTOOLCHAIN=local golangci-lint run` — 0 issues
- [ ] `make build && ./jcli --version` prints a real version
- [ ] `goreleaser check` passes and `goreleaser build --snapshot --single-target`
  produces a runnable arm64 binary that reports its version
- [ ] `actionlint .github/workflows/release.yml` clean; `make cross-build` green
- [ ] repo grep: version symbol path is consistent between Makefile, `.goreleaser.yml`,
  and `internal/cli`

### Task 7: [Final] Documentation close-out
- [ ] README/CLAUDE.md/`--help` agree on `--version` and the release flow
- [ ] move this plan to `docs/plans/completed/`

## Post-Completion
*Informational — manual/external, no checkboxes.*

**Trigger the first real release:**
- After merge, push a tag (e.g. `git tag v0.1.0 && git push origin v0.1.0`) to fire
  `release.yml`. Watch the Actions run; confirm the Release has the
  `jcli_0.1.0_darwin_arm64.tar.gz` archive + `checksums.txt`.
- Download-test on a Mac: extract, `xattr -d com.apple.quarantine jcli`, run
  `./jcli --version`, then `jcli install-skill`. Without the `xattr` step Gatekeeper
  blocks the unsigned binary — this is expected (notarization is out of scope).

**Deferred:**
- **Homebrew tap** — a source-build formula (compiles locally → no Gatekeeper
  quarantine) is the clean distribution channel, but needs a separate tap repo +
  a PAT secret for the cross-repo push. Plan separately when desired.
