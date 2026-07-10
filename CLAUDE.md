# CLAUDE.md

Guidance for Claude Code when working in this repository.

## What this is

`jcli` — a general-purpose macOS Jenkins CLI in Go. Multi-profile support,
Keychain-backed credentials authorized by the login-keychain trusted-app ACL
(bound to the binary's ad-hoc code identity) and served through an on-demand
in-memory agent, a per-profile cached job/param map, and commands to
list/inspect/trigger Jenkins jobs. See **`DESIGN.md`** (repo root) for the full,
brainstorm-validated architecture — it is the source of truth for all design
decisions.

Single binary, two modes: the `jcli` CLI and the hidden `jcli __agent`
credential agent. Config in `~/.config/jcli/config.json` (no secrets); cache in
`~/.cache/jcli/<profile>/jobs.json`; the token only ever lives in the macOS
Keychain or the running agent's memory.

The legacy single-purpose Python deploy script it replaces is in `.legacy/`
(gitignored) and is a reference for validated Jenkins endpoint shapes.

## Layout

```
cmd/jcli/main.go     # flag parsing, command dispatch, agent-mode entry
internal/cli/        # command handlers (login, list, get, build, dump, profile)
internal/config/     # profiles file read/write (atomic, 0600)
internal/cache/      # job map load/store/refresh
internal/jenkins/    # REST client + types; consumer-side interface lives in cli/
internal/creds/      # client side: talk to agent over socket; spawn if absent
internal/agent/      # agent mode: keychain read (ACL-authorized), in-memory TTL, socket server
skill/               # embedded Claude skill (jenkins-cli) + embed.go shim (//go:embed)
```

The `install-skill` command writes the embedded skill (always overwriting) to
`<claude>/skills/jenkins-cli` (`--to` defaults to `~/.claude`), so the installed
binary is self-contained and re-installs are idempotent.

Implementation plans live in `docs/plans/`, but `.gitignore` tracks only
`docs/plans/completed/` — `docs/plans/*` is otherwise ignored. In-progress plans
are local-only; a plan is committed only once moved into `completed/`. Editing a
plan's checkboxes mid-work therefore never shows up in `git status`.

## Build / test / lint

```bash
make build       # stop the stale agent (see below), then go build -o jcli ./cmd/jcli
make test        # go test -race ./...
make lint        # GOTOOLCHAIN=local golangci-lint run (config: .golangci.yml)
make fmt         # gofmt -s -w . && goimports -w .
make cross-build # GOOS=linux CGO_ENABLED=0 go build/vet ./... (keychain stub)
make install     # build + install to ~/bin (override INSTALL_DIR=/usr/local/bin)
```

The credential agent is a long-lived detached process spawned from the binary
(via `os.Executable()`, an absolute path) on first credential use. It holds the
old binary's code and the token in memory, so a rebuilt binary must replace it —
otherwise the next CLI reconnects to the stale agent still running the old code.
`build` therefore depends
on a `stop-agent` target that `pkill`s the agent at this binary's absolute path
(an installed copy at a different path is left alone); `install` additionally
stops the agent at `$(INSTALL_DIR)/jcli`. A fresh agent spawns on demand at the
next command. `stop-agent` is a no-op when none is running and never fails the
build.

## CI & lint config

GitHub Actions (`.github/workflows/ci.yml`) runs three jobs: `build` on
`macos-latest` (`make test` + `golangci-lint`, because the cgo keychain code only
compiles on darwin), `cross-build` on `ubuntu-latest` (`make cross-build`), and
`shellcheck` over any repo shell scripts.

`.github/workflows/release.yml` runs GoReleaser (`.goreleaser.yml`) on `macos-latest`
when a `v*` tag is pushed, publishing a darwin/arm64 tar.gz archive + checksums to
GitHub Releases via the built-in `GITHUB_TOKEN` (same-repo, no PAT). The version is
stamped through `-X github.com/avitsrimer/jcli/internal/cli.version` (the same symbol
`make build` stamps with `git describe`). Released binaries are unsigned/ad-hoc, so a
downloaded archive is Gatekeeper-quarantined — users run
`xattr -d com.apple.quarantine jcli` after extracting.

The release also publishes a Homebrew **cask** to the `avitsrimer/homebrew-apps` tap
(`brew install avitsrimer/apps/jcli`) via `homebrew_casks` in `.goreleaser.yml` —
`brews` is deprecated (removed in goreleaser v2.16). The cross-repo push uses a
fine-grained PAT in the `HOMEBREW_TAP_TOKEN` secret (the built-in `GITHUB_TOKEN`
can't push to another repo). The cask's post-install hook runs `xattr -dr` so
brew-installed binaries skip the Gatekeeper quarantine automatically.

`.golangci.yml` (golangci-lint v2, derived from `umputun/revdiff`) drives both CI
and `make lint`; the target sets `GOTOOLCHAIN=local` so local lint matches CI's
pinned toolchain. Deliberate config choices — don't undo them without reason:

- `issues.max-{same,per-linter}-issues: 0` — report every finding, not a capped
  sample, so `make lint` output is honest (the defaults hid the true counts).
- `errcheck.exclude-functions: [fmt.Fprint, fmt.Fprintf, fmt.Fprintln]` — CLI
  diagnostic writes to stdout/stderr never need their error checked.
- `misspell.ignore-rules: [cancelled]` — Jenkins wire term (`json:"cancelled"`),
  kept for API compatibility. **Never let a misspell autofix rename that JSON tag.**
- `gosec` is excluded on `_test.go` (test-controlled paths/perms are not a
  security surface). The remaining production `G304` reads (`config`, `cache`,
  agent `lock`) carry inline `//nolint:gosec` — the paths are jcli's own, not user
  input.
- The cgo `import "C"` / `C.getItem` `//nolint:gocritic` directives suppress
  `dupImport`/`dupSubExpr` false positives from the cgo pseudo-import.

## Keychain trust (ad-hoc identity)

There is no managed code-signing certificate. `jcli` relies on the default ad-hoc
code identity that `go build` produces on Apple Silicon. The Keychain item's
trusted-app ACL — which authorizes silent reads — is bound to the ad-hoc code
identity of the binary that created the item. This is an ACL trust, **not** an
entitlement (the item lives in the file-based login keychain, so no
`keychain-access-groups` entitlement is involved).

The ad-hoc cdhash changes on every rebuild, so a freshly built/installed binary no
longer matches the item's ACL: the first token read hits the keychain
"Allow / Always Allow" authorization prompt (which names "Jenkins CLI", the item's
service) instead of being silent. Click **Always Allow** once — or run
`jcli logout && jcli login` to recreate the item under the new identity. This
one-prompt-per-rebuild behavior is the accepted trade-off for dropping cert
management.

## Go conventions (from umputun/tg-spam)

- Go 1.24+. Run `go build ./...`, `go test -race ./...`, `golangci-lint run`,
  `gofmt -s -w`, `goimports -w` before declaring work done.
- Max 140-char lines. CamelCase vars, PascalCase exported; doc comments on all
  exported symbols.
- In-code comments are lowercase and describe current purpose — never history or
  what changed.
- Return errors with context (`fmt.Errorf("…: %w", err)`); aggregate with
  multierror where crawling many jobs.
- Define interfaces in the **consumer** package, not the provider (e.g. `cli`
  owns the Jenkins client interface; `agent` owns the `keychainStore` interface).
- Table-driven tests with `testify`; mocks via `matryer/moq` through
  `go:generate` — never hand-edit generated mocks.
- CLI flags via `github.com/jessevdk/go-flags`. Diagnostics go to stderr via
  plain `fmt` (the `--verbose`-gated `app.verbosef` helper) — no logging
  framework, intentional for this single-binary CLI.
- Keep `README.md` in sync with `--help`. No AI-attribution or "Test plan"
  sections in commit messages.

## Implementation notes (Go 1.24 target / cgo / tests)

These are constraints discovered while building against the Go 1.24 target —
honor them when touching deps, the cgo files, or the agent tests.

- **Dependency pins forced by Go 1.24.** `golang.org/x/term` is pinned to
  `v0.31.0` and `golang.org/x/sys` to `v0.33.0` — newer releases require Go
  ≥1.25. `matryer/moq` is run via `go run github.com/matryer/moq@v0.5.3` in the
  `//go:generate` directives (moq `@latest`/v0.7+ requires Go 1.26). Use
  `GOTOOLCHAIN=local` for `go build`/`go mod`/`go test` so the toolchain does not
  auto-upgrade past 1.24.
- **Darwin cgo conventions.** The Security preamble and the screen-lock preamble
  are compiled as Objective-C (`#cgo CFLAGS: -x objective-c -fno-objc-arc`). cgo
  forbids `//export` in a file whose preamble defines C functions, so the
  screen-lock `//export goScreenLocked` bridge lives in its own file
  (`screenlock_export_darwin.go`), separate from the preamble in
  `screenlock_darwin.go`. moq cannot load a package whose cgo preamble is
  Objective-C, so `internal/agent/keychain_mock.go` is hand-written to moq's
  shape (with a regenerate TODO) rather than generated.
- **Regenerating `internal/cli/jenkins_mock.go` after an interface change.** When
  you add a method to the `jenkinsClient` interface, `go generate ./internal/cli/...`
  fails: moq can't load the package because the *stale* mock's
  `var _ jenkinsClient = &jenkinsClientMock{}` compile-time assertion no longer
  satisfies the enlarged interface (chicken-and-egg). Delete `jenkins_mock.go`
  first, then run the moq command — it regenerates fresh with the assertion
  restored. The file is fully generated; never hand-edit it.
- **Build-tag split for darwin vs stub.** Each cgo surface has a `_darwin.go`
  implementation plus a `_other.go` (`!darwin`) stub that returns a clear
  "unsupported platform" error / no-op: `keychain`, `peercred`, `screenlock`.
  This keeps `GOOS=linux CGO_ENABLED=0 go build/vet ./...` green
  (`make cross-build`).
- **Socket paths in agent/creds tests.** Unix `sockaddr_un` caps the path at 104
  bytes, and `t.TempDir()` paths can exceed that. Tests use a short
  `os.TempDir()`-based directory for the agent socket instead of `t.TempDir()`.
- **The keychain item is plain (ACL-authorized), not data-protection.** The token
  is a generic-password item in the default/login keychain with no
  `kSecAttrAccessControl` and no `kSecAttrAccessible` (it defaults to
  `kSecAttrAccessibleWhenUnlocked`); the ad-hoc code identity of the binary that
  created the item, recorded in the item's trusted-app ACL, is the access boundary.
  With the file-based keychain, `-34018` (`errSecMissingEntitlement`) should not
  occur — the `osStatusHint` decoder remains as a harmless safety net.
- **Peer-UID rejection, the keychain ACL authorization prompt, and screen-lock
  flush** cannot be exercised headlessly (single-UID test env, real keychain UI,
  CFRunLoop) — they are verified manually (see the plan's Post-Completion). The ACL
  prompt only appears when the binary's ad-hoc code identity changes (e.g. after a
  rebuild); the binary that created the item reads silently. Unit tests cover only
  the accept/extraction path and the `flush` mechanism the lock callback reuses.
- **No connection deadline across the blocking keychain read.** A `net.Conn`
  deadline cannot preempt the cgo `SecItemCopyMatching` read, and a short one armed
  before it tears down the socket mid-prompt — surfacing `read response: EOF` at the
  ACL dialog. The agent bounds only the request decode (`reqReadTimeout`) and the
  response write (`writeTimeout`, a write-only deadline armed after dispatch) — never
  the keychain read between them; the client's `requestTimeout` (2 min) is the sole
  bound that fires on a genuinely hung read, and must outlast a person answering the
  prompt.

## Exit codes

`0` ok, `1` usage, `2` auth, `3` not-found, `4` build-failed (with `--wait`).
