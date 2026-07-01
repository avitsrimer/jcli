# CLAUDE.md

Guidance for Claude Code when working in this repository.

## What this is

`jcli` — a general-purpose macOS Jenkins CLI in Go. Multi-profile support,
Keychain-backed credentials authorized by the login-keychain trusted-app ACL
(bound to the signed binary's DR) and served through an on-demand in-memory agent,
a per-profile cached job/param map, and commands to list/inspect/trigger Jenkins
jobs. See **`DESIGN.md`** (repo root) for the full, brainstorm-validated
architecture — it is the source of truth for all design decisions.

Single signed binary, two modes: the `jcli` CLI and the hidden `jcli __agent`
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

## Build / test / lint

```bash
make build       # stop the stale agent (see below), then go build -o jcli ./cmd/jcli
make test        # go test -race ./...
make lint        # golangci-lint run
make fmt         # gofmt -s -w . && goimports -w .
make cross-build # GOOS=linux CGO_ENABLED=0 go build/vet ./... (keychain stub)
make cert        # create/reuse the self-signed code-signing identity (idempotent)
make sign        # codesign --options runtime with the self-signed identity; prints the DR
make show-dr     # print the binary's designated requirement (codesign -d -r-)
make install     # sign + install to ~/bin (override INSTALL_DIR=/usr/local/bin)
```

The credential agent is a long-lived detached process spawned from the binary
(via `os.Executable()`, an absolute path) on first credential use. It holds the
old code and the token in memory, so a rebuilt/re-signed binary must replace it —
otherwise the next CLI reconnects to the stale agent whose DR no longer matches
the keychain ACL (e.g. an old agent built before this binary was signed). `build`
therefore depends
on a `stop-agent` target that `pkill`s the agent at this binary's absolute path
(an installed copy at a different path is left alone); `install` additionally
stops the agent at `$(INSTALL_DIR)/jcli`. A fresh agent spawns on demand at the
next command. `stop-agent` is a no-op when none is running and never fails the
build.

## Code signing (stable identity — do NOT regenerate the cert)

The Keychain item's trusted-app ACL — which authorizes the signed binary to read
the item silently — is bound to the signing identity's **designated requirement
(DR)**, which is derived from the self-signed certificate. This is an ACL trust,
**not** an entitlement (the item lives in the file-based keychain, so no
`keychain-access-groups` entitlement is involved). The cert common-name is
`jcli Code Signing` (the `SIGN_ID` Makefile var; it must match the CN created by
`scripts/make-cert.sh`).

- `scripts/make-cert.sh` is idempotent: if the cert already exists it exits 0 and
  reuses it. **Never regenerate it** — a new cert yields a new DR, which no longer
  matches the keychain ACL: reads from the new binary hit the keychain
  "Allow / Always Allow" authorization prompt instead of being silent, and any
  prior "Always Allow" trust is lost (the authorization prompt names "Jenkins CLI",
  the item's service).
- A rebuild + re-sign with the *same* cert must produce a **stable** DR. codesign
  derives a **leaf-certificate-hash** form for a self-signed identity (not the
  `certificate leaf[subject.CN]` form):
  `identifier jcli and certificate leaf = H"<sha1-of-the-cert>"`. The recorded DR
  for the current dev machine's identity (verified stable across rebuild + re-sign)
  is hash `700e7dfe165b8403e6e6c9e4d5690df71b93e794` — but this is **per-identity,
  not a shared constant**: anyone who runs `make cert` gets their own cert with its
  own hash. The hash is a public certificate fingerprint (safe to record); it stays
  stable while the same cert is reused and changes only if the cert is regenerated —
  which is why the cert must never be regenerated. Run `make show-dr` after the
  first sign to capture the DR for whatever identity is in use.

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
  `kSecAttrAccessibleWhenUnlocked`); the signed binary's DR in the item's
  trusted-app ACL is the access boundary. With the file-based keychain, `-34018`
  (`errSecMissingEntitlement`) should no longer occur — the `osStatusHint` decoder
  remains as a harmless safety net.
- **Peer-UID rejection, the keychain ACL authorization prompt, and screen-lock
  flush** cannot be exercised headlessly (single-UID test env, real keychain UI,
  CFRunLoop) — they are verified manually (see the plan's Post-Completion). The ACL
  prompt only appears when the signing identity / DR changes; the same signed
  binary reads silently. Unit tests cover only the accept/extraction path and the
  `flush` mechanism the lock callback reuses.

## Exit codes

`0` ok, `1` usage, `2` auth, `3` not-found, `4` build-failed (with `--wait`).
