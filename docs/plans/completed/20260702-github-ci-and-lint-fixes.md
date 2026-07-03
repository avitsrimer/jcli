# GitHub CI + golangci-lint clean-up

## Overview
- Add a GitHub Actions CI workflow (adapted from `umputun/revdiff`) and the
  golangci-lint v2 config it depends on, then drive the linter to **0 findings**.
- Problem it solves: the repo has no CI and no lint config; `make lint` runs with
  golangci defaults that disagree with the intended (revdiff-style) linter set.
  Adding both gives every push/PR an automated build + test + lint gate.
- Integration: CI runs `go test -race` and lint on `macos-latest` (the cgo
  keychain/screen-lock code only compiles on darwin), a `cross-build` job on
  `ubuntu-latest` mirroring `make cross-build` (linux stub), and a `shellcheck`
  job for `scripts/*.sh`. Code signing / release stay manual and out of CI.

## Context (from discovery)
- Repo remote: `github.com:avitsrimer/jcli` — GitHub Actions applies. No `.github/`
  yet. `.golangci.yml` (revdiff's v2 config) is already written at repo root.
- `golangci-lint v2.12.2` installed locally (via brew); config `version: "2"`.
- Baseline run: **69 findings** across 25 files. By linter:
  - auto-fixable (~30): `misspell` 8, `modernize` 11, `intrange` 2, `perfsprint` 3,
    `testifylint` 6. (No `whitespace` findings in the live run; a few
    `testifylint`/`modernize` items may need a hand-fix if `--fix` can't rewrite them.)
  - `errcheck` 8 — unchecked `fmt.Fprint*` to std streams.
  - `wrapcheck` 8 — unwrapped errors from stdlib/other packages.
  - `gosec` 9 — dir/file perms (G301/G306) + file-inclusion (G304), most intentional.
  - `noctx` 4 — `net.Listen` / `net.DialTimeout` / `exec.Command` in agent/creds.
  - `gocritic` 6 — `httpNoBody` ×3, cgo `dupImport` ×2, `dupSubExpr` ×1.
  - `govet` (shadow) 4.
- Decisions taken with the user:
  - **CI scope:** `ci.yml` with build/test/lint (macos) + cross-build (ubuntu) +
    shellcheck (ubuntu). No bun, no coveralls, no goreleaser/release.
  - **Fix strategy:** fix everything in code where possible (tighten perms, convert
    noctx to Context-aware APIs). `//nolint` (with the config-required explanation)
    only for the two cgo false positives and the two internal-path G304s.
- jcli constraints (from CLAUDE.md): Go 1.24 target, `GOTOOLCHAIN=local`; darwin cgo
  is Objective-C; keychain-ACL/screen-lock/peercred behaviors are verified manually
  and are NOT exercised by `go test` (unit tests use the hand-written mock / accept
  path), so a headless `go test -race ./...` passes on a macOS runner.

## Development Approach
- **Testing approach:** Regular (code first). This is a lint/CI clean-up on an
  existing, tested codebase — no new product behavior. The per-task "test" is:
  (a) the targeted lint category drops to 0, and (b) `GOTOOLCHAIN=local go test
  -race ./...` still passes. Add/adjust unit tests only where a fix changes a
  code path (none expected — fixes are mechanical/annotation-level).
- Complete each task fully before the next; make small, focused changes.
- **CRITICAL: after every task, run `GOTOOLCHAIN=local go test -race ./...` — it
  must pass before starting the next task.**
- **CRITICAL: update this plan file if scope changes during implementation.**
- Maintain backward compatibility (no behavior change intended anywhere).

## Testing Strategy
- **Unit tests:** existing suite must stay green after each task
  (`GOTOOLCHAIN=local go test -race ./...`). No new features → no new test files
  expected; if a fix alters a code path, update that package's `_test.go` in the
  same task.
- **Lint gate:** after each fix task, `golangci-lint run` shows the category
  eliminated; final task requires exit code 0.
- **Cross-build:** `make cross-build` (GOOS=linux CGO_ENABLED=0) must stay green.
- **No e2e:** project has no UI/e2e suite.
- **Workflow YAML:** validate with `actionlint` if available (else visual review);
  true verification is the first push (Post-Completion).

## Progress Tracking
- Mark completed items `[x]` immediately when done.
- Add newly discovered tasks with ➕ prefix; blockers with ⚠️ prefix.
- Keep the plan in sync with actual work.

## Solution Overview
- **CI:** one workflow `.github/workflows/ci.yml`, three jobs. Reuse Makefile
  targets where sensible (`make test`, `make cross-build`) so CI and local agree;
  use `golangci/golangci-lint-action` for the lint job (pinned golangci version to
  match local, `2.12.2`). `setup-go` uses `go-version-file: go.mod` so the pinned
  1.24.2 toolchain is used with `GOTOOLCHAIN=local`.
- **Toolchain alignment (from review):** local `make lint` is bare
  `golangci-lint run`, which on this machine resolves to Go 1.26.x — a *different*
  toolchain than CI's pinned 1.24.2, so "0 findings locally" could diverge from CI.
  Fix by making `make lint` use `GOTOOLCHAIN=local` (the Makefile already defines
  `GO ?= GOTOOLCHAIN=local go`) and giving the CI lint job `env: GOTOOLCHAIN=local`,
  so the acceptance gate (Task 8) runs under the same 1.24.2 as CI.
- **Lint fixes:** ordered by blast radius — auto-fix first, then errcheck →
  wrapcheck → gosec → noctx → gocritic/govet — so each category is isolated and
  verifiable, and re-running the linter between tasks shows monotonic progress.

## Technical Details
- **`.golangci.yml`:** already present; add one exclusion rule so `gosec` does not
  flag test files (test-controlled paths/perms are not a security surface):
  ```yaml
  - linters: [gosec]
    path: _test\.go$
  ```
- **CI `setup-go`:** `go-version-file: go.mod`; every go step gets `env:
  GOTOOLCHAIN=local`.
- **noctx conversions:**
  - `internal/agent/agent.go:131` `net.Listen("unix", sockPath)` →
    `(&net.ListenConfig{}).Listen(ctx, "unix", sockPath)` — use a `ctx` in scope, or
    `context.Background()` if none (the agent listener is process-lifetime).
  - `internal/creds/creds.go:143,177` `net.DialTimeout("unix", …, dialTimeout)` →
    `(&net.Dialer{Timeout: dialTimeout}).DialContext(ctx, "unix", c.sockPath)`.
  - `internal/creds/creds.go:161` `exec.Command(c.self, "__agent")` →
    `exec.CommandContext(context.Background(), c.self, "__agent")` — keep the
    existing `//nolint:gosec` (detached agent must outlive the CLI, so Background is
    intentional, not a cancellable ctx).
- **gosec:**
  - `cmd_install_skill.go:39` `0o755` → `0o750` (G301); `:48` `0o644` → `0o600` (G306).
  - `internal/agent/lock.go:15` and `internal/cache/cache.go:65` (G304): keep the
    `os.OpenFile`/`os.ReadFile` but add
    `//nolint:gosec // path is an internal agent/cache path, not user input`.
- **gocritic (cgo false positives, `internal/agent/keychain_darwin.go`):**
  - line 113/117 `dupImport` (`import "C"` vs `"unsafe"`) and line 187 `dupSubExpr`
    on `C.getItem(…)` are cgo-parser artifacts → `//nolint:gocritic // cgo import
    "C" / cgo call — false positive` on the relevant lines.
  - `internal/jenkins/client.go` `httpNoBody` ×3: pass `http.NoBody` instead of
    `nil` request body — a real, clean fix.
- **govet shadow ×4:** locate with `golangci-lint run --enable-only govet` and
  rename the shadowing locals.
- **errcheck ×8:** prefix diagnostic writes with `_, _ =`
  (`main.go`, `cli.go`, `cmd_get.go`, `cmd_login.go`).
- **wrapcheck ×8:** wrap the returned error with `fmt.Errorf("…: %w", err)` at each
  site (`cli.go` ×3, `cmd_get.go`, `cmd_list.go`, `cmd_logs.go` ×2, `cmd_status.go`,
  `lock.go`). Keep messages lowercase per CLAUDE.md.

## What Goes Where
- **Implementation Steps** (checkboxes): config, workflow file, all in-repo code
  fixes, and verification.
- **Post-Completion** (no checkboxes): pushing to GitHub and confirming the Actions
  run is green on the hosted macOS runner; manual keychain/lock behaviors unchanged.

## Implementation Steps

### Task 1: Add golangci test-file exclusion + CI workflow

**Files:**
- Modify: `.golangci.yml`, `Makefile`
- Create: `.github/workflows/ci.yml`

- [ ] add the `gosec` `_test.go` exclusion rule to `.golangci.yml` (see Technical Details)
- [ ] change the `Makefile` `lint:` target to `GOTOOLCHAIN=local golangci-lint run`
  so local lint uses the same 1.24.2 toolchain as CI (toolchain-alignment fix)
- [ ] create `.github/workflows/ci.yml` with three jobs:
  - `build` (`macos-latest`): checkout → `setup-go@v6` (`go-version-file: go.mod`) →
    `make test` (env `GOTOOLCHAIN=local`) → `golangci/golangci-lint-action@v9`
    (`version: v2.12.2`, step `env: GOTOOLCHAIN=local` so it lints under 1.24.2)
  - `cross-build` (`ubuntu-latest`): checkout → `setup-go@v6` → `make cross-build`
    (optional: also `GOOS=linux golangci-lint run` so the `!darwin` stub files —
    `keychain_other.go`/`peercred_other.go`/`screenlock_other.go` — get a lint gate;
    the macOS lint job only sees darwin-tagged files)
  - `shellcheck` (`ubuntu-latest`): checkout → run `shellcheck` over `scripts/*.sh`
  - triggers: `push` (branches + tags) and `pull_request`; `permissions: contents: read`;
    `persist-credentials: false` on checkout
- [ ] validate the workflow YAML with `actionlint` if installed; otherwise review by eye
- [ ] confirm `golangci-lint run` now excludes test-file gosec noise (findings drop from 69)
- [ ] run `GOTOOLCHAIN=local go test -race ./...` — must pass before next task

### Task 2: Auto-fix mechanical findings

**Files:**
- Modify: various (driven by `--fix`) — `misspell`, `modernize`, `intrange`,
  `perfsprint`, `testifylint`, `whitespace` sites

- [ ] run `GOTOOLCHAIN=local golangci-lint run --fix`
- [ ] review the diff — especially `modernize` (`atomic.Int32` in
  `cmd_status_test.go`) and `testifylint` rewrites — for correctness
- [ ] **⚠️ revert the `modernize` `omitzero` hunk on `internal/cache/cache.go:28`**
  (`ParamsFetchedAt time.Time` tag) — `--fix` rewrites `omitempty`→`omitzero`, which
  changes the on-disk `jobs.json` serialization (zero times would drop out). Keep the
  format byte-identical; re-check that `cache.go:28` no longer appears only after the
  hunk is reverted (the finding is a no-op tag, not a bug)
- [ ] hand-fix any `testifylint`/`modernize` items `--fix` could not rewrite
- [ ] run `gofmt -s -w . && goimports -w .` to normalize
- [ ] confirm `misspell`/`modernize`/`intrange`/`perfsprint`/`testifylint`
  are gone from `golangci-lint run` (except the intentionally-reverted `cache.go:28`,
  which must be silenced with a targeted `//nolint:modernize // omitzero would change
  on-disk cache format` on that field)
- [ ] run `GOTOOLCHAIN=local go test -race ./...` — must pass before next task

### Task 3: errcheck — check diagnostic writes

**Files:**
- Modify: `cmd/jcli/main.go`, `internal/cli/cli.go`, `internal/cli/cmd_get.go`,
  `internal/cli/cmd_login.go`

- [ ] prefix each unchecked `fmt.Fprint*` with `_, _ =` (8 sites)
- [ ] confirm `errcheck` findings are 0
- [ ] run `GOTOOLCHAIN=local go test -race ./...` — must pass before next task

### Task 4: wrapcheck — wrap returned errors

**Files:**
- Modify: `internal/cli/cli.go`, `internal/cli/cmd_get.go`, `internal/cli/cmd_list.go`,
  `internal/cli/cmd_logs.go`, `internal/cli/cmd_status.go`, `internal/agent/lock.go`

- [ ] wrap each flagged return with `fmt.Errorf("<lowercase context>: %w", err)` (8 sites)
- [ ] confirm `wrapcheck` findings are 0
- [ ] run `GOTOOLCHAIN=local go test -race ./...` — must pass before next task

### Task 5: gosec — tighten perms, annotate internal-path reads

**Files:**
- Modify: `internal/cli/cmd_install_skill.go`, `internal/agent/lock.go`,
  `internal/cache/cache.go`

- [ ] `cmd_install_skill.go`: `MkdirAll` `0o755`→`0o750`, `WriteFile` `0o644`→`0o600`
- [ ] add `//nolint:gosec // <reason: internal path, not user input>` to
  `lock.go` `os.OpenFile` and `cache.go` `os.ReadFile` (G304)
- [ ] update `cmd_install_skill_test.go` expectations if any assert the old perms
- [ ] confirm `gosec` findings are 0 (prod fixed/annotated, tests excluded via Task 1)
- [ ] run `GOTOOLCHAIN=local go test -race ./...` — must pass before next task

### Task 6: noctx — Context-aware socket + exec

**Files:**
- Modify: `internal/agent/agent.go`, `internal/creds/creds.go`

- [ ] `agent.go`: `net.Listen` → `(&net.ListenConfig{}).Listen(ctx, …)` (ctx in scope
  or `context.Background()`)
- [ ] `creds.go`: both `net.DialTimeout` → `(&net.Dialer{Timeout: dialTimeout}).DialContext(ctx, …)`
- [ ] `creds.go`: `exec.Command` → `exec.CommandContext(context.Background(), …)`,
  retaining the existing `//nolint:gosec`
- [ ] confirm `noctx` findings are 0
- [ ] run `GOTOOLCHAIN=local go test -race ./...` — must pass (agent/creds tests) before next task

### Task 7: gocritic + govet remainder

**Files:**
- Modify: `internal/jenkins/client.go`, `internal/agent/keychain_darwin.go`,
  plus the 4 `govet` shadow sites (located during the task)

- [ ] `client.go`: pass `http.NoBody` instead of `nil` at the 3 request sites
- [ ] `keychain_darwin.go`: `//nolint:gocritic // <reason>` on the `dupImport` and
  `dupSubExpr` cgo false positives
- [ ] rename the shadowing locals at the 4 `govet` shadow sites — two in production
  (`agent.go:126` `if err := os.Remove(...)`, `cmd_get.go:37`) and **two in tests**
  (`creds_test.go:205`, `:223` — the `_test.go` exclusions do NOT cover `govet`
  shadow, so these renames are required). Rename (e.g. `rmErr`); do not disable shadow
- [ ] confirm `gocritic` and `govet` findings are 0
- [ ] run `GOTOOLCHAIN=local go test -race ./...` — must pass before next task

### Task 8: Verify acceptance criteria
- [ ] `GOTOOLCHAIN=local golangci-lint run` exits 0 (zero findings)
- [ ] `GOTOOLCHAIN=local go test -race ./...` fully green
- [ ] `make cross-build` green (linux stub still compiles)
- [ ] `make build` succeeds (stops stale agent, builds binary)
- [ ] workflow YAML validated (`actionlint` if present)

### Task 9: Documentation + close-out
- [ ] update `CLAUDE.md`: note CI workflow exists and `.golangci.yml` drives
  `make lint` (revdiff-derived v2 config); add golangci-lint as a dev prerequisite
- [ ] update `README.md` if it documents build/lint steps or should show a CI badge
- [ ] move this plan to `docs/plans/completed/`

## Post-Completion
*Informational — external actions, no checkboxes.*

**External system updates:**
- Push the branch and open a PR; confirm the Actions run is green — the hosted
  `macos-latest` runner is the real test of the cgo build/test + lint on darwin
  (only reproducible locally on this machine before push).
- Consider adding a CI status badge to `README.md` once the workflow has run once.

**Manual verification (unchanged by this work, but worth a sanity check):**
- Keychain ACL silent read, peer-UID rejection, and screen-lock flush remain
  manually verified (per CLAUDE.md) — none of the lint fixes touch that behavior,
  but re-run a real `jcli` command locally after signing to confirm the agent still
  spawns/reads cleanly after the `noctx`/socket changes.
