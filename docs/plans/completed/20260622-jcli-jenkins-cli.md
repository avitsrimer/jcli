# jcli — Jenkins CLI Implementation Plan

## Overview

Build `jcli`, a general-purpose macOS Jenkins CLI in Go, per the validated design
in `DESIGN.md`. It provides multi-profile support, macOS Keychain-backed
credentials unlocked via Touch ID through an on-demand in-memory agent, a cached
per-profile job/param map, and commands to list/inspect/trigger Jenkins jobs.

**Problem it solves:** replaces the legacy single-purpose Python deploy script
(now in `.legacy/`) with a hardened, reusable, server-agnostic tool that keeps
tokens out of plaintext/shell history and avoids prompting on every action.

**Integration:** single self-signed binary, two modes (`jcli` CLI + hidden
`jcli __agent`). Config in `~/.config/jcli/config.json` (no secrets); cache in
`~/.cache/jcli/<profile>/jobs.json`; token only ever in the Keychain / agent
memory.

### Acceptance Criteria

- [x] `jcli login/profile/logout` manage profiles in `~/.config/jcli/config.json` (no secrets on disk) and store/delete tokens via the agent.
- [x] `jcli list/get/dump` read from a per-profile cached job map, crawl on cold/`--refresh`, and resolve a job miss with one crawl-then-retry → else not-found (exit 3) with suggestions.
- [x] `jcli build` validates `--param-<name>=val` against cached param defs (unknown name / bad Choice rejected, defaults filled); fire-and-forget by default; `--wait` polls to completion with exit 0/4 by result.
- [x] Agent unlocks the per-profile Keychain item once via Touch ID and serves the token from memory over a `0600` unix socket with verified peer UID; second command within the 15-min TTL does not re-prompt; agent self-exits on idle. (Automated tests cover socket serve, in-memory cache-hit avoids 2nd keychain call, TTL refetch, peer-UID accept + reject branch, idle exit; the real cgo Touch ID unlock is manual-only — Post-Completion.)
- [x] Exit codes: 0 ok / 1 usage / 2 auth / 3 not-found / 4 build-failed.
- [x] Repo cross-builds (`go vet ./...`, `go build ./...`) on non-darwin via the keychain stub; `go test -race ./...` is green. (`golangci-lint` not installed on this host — skipped.)

## Context (from discovery)

- **Source of truth:** `DESIGN.md` (repo root) — all architecture decisions validated via brainstorm.
- **Reference conventions:** umputun/tg-spam `CLAUDE.md` (Go 1.24+, go-flags, go-pkgz/lgr, testify, matryer/moq, interfaces in consumer packages, gofmt -s, 140-col lines, errors with context, lowercase purpose-only comments, no AI-attribution in commits).
- **Legacy reference:** `.legacy/bin/jenkins.py` (validated endpoint shapes), `.legacy/bin/jenkins-research.py` (whoAmI / `/api/json` tree / per-job param defs / build 201). `.legacy/` is gitignored.
- **Repo state:** fresh git repo on `main`, only `README.md` + `.gitignore` + `DESIGN.md` present; no Go code yet.
- **Platform:** macOS; keychain/LAContext via cgo against Security.framework / LocalAuthentication.framework.

## Development Approach

- **Testing approach:** Regular (code first, then tests within the same task).
- complete each task fully before moving to the next.
- make small, focused changes; build pure-Go testable layers before the cgo/agent glue.
- **CRITICAL: every task MUST include new/updated tests** — unit tests for new and modified functions, covering success and error scenarios. Tests are required, not optional.
- **CRITICAL: all tests must pass before starting the next task.**
- **CRITICAL: update this plan file when scope changes during implementation.**
- run `go test -race ./...` and `golangci-lint run` after each change; keep README/CLAUDE.md in sync.

## Testing Strategy

- **unit tests:** required for every task.
  - `jenkins/`: `httptest.Server` fixtures mirroring real shapes captured in research (whoAmI, recursive job tree, per-job param defs, build → 201 + Location).
  - `config/` and `cache/`: `t.TempDir()` — atomic write, `0600` perms, round-trip, staleness, miss-then-crawl.
  - `creds`↔`agent`: real unix socket in tests; peer-UID path exercised; keychain/LAContext behind a `keychainStore` interface mocked with `moq`.
  - param validation: unknown name, choice violation, default fill.
- **e2e tests:** none (no UI). The cgo Touch ID path cannot be unit-tested — covered by a manual verification task.
- treat all unit tests with full rigor: must pass before the next task.

## Progress Tracking

- mark completed items with `[x]` immediately when done.
- add newly discovered tasks with ➕ prefix; document blockers with ⚠️ prefix.
- update plan if implementation deviates from original scope.

## Solution Overview

Layered, dependency-ordered build: scaffold → pure-Go domain packages
(`config`, `jenkins`, `cache`) → cgo `keychainStore` → `agent` (socket + TTL) →
`creds` client (spawn + socket) → CLI dispatch & flags → commands
(`login`/`profile`/`logout`, then `list`/`get`/`dump`, then `build`) → signing &
manual keychain verification → docs. Interfaces are declared in their consumer
packages (`cli` consumes a Jenkins client interface; `agent` consumes a
`keychainStore`) so they mock cleanly with `moq`.

## Technical Details

- **Config:** `~/.config/jcli/config.json` → `{ "default": "<name>", "profiles": [ {"name","url","username"} ] }`. Resolution: `--profile` → `JCLI_PROFILE` → `default`.
- **Keychain item:** generic-password, service `Jenkins CLI`, account `jcli:<profile>`; `kSecAttrAccessibleWhenUnlockedThisDeviceOnly` + `kSecAccessControl` `.userPresence`; ACL trusted-app = signed binary's designated requirement.
- **Agent:** unix socket `0600` in runtime dir; JSON requests `{op:"get-token"|"set-token"|"delete-token"|"flush", profile?, token?}` (`flush` with no `profile` = flush all buffers; with `profile` = that one). Peer UID verified via `getsockopt(SOL_LOCAL, LOCAL_PEERCRED)` → `xucred` reached through `conn.SyscallConn()` (darwin has no `SO_PEERCRED`). Single-instance: agent takes an exclusive bind/flock; a stale socket file is removed before bind; a second agent that loses the race exits and the CLI connects to the winner. First `get-token` for a profile triggers a single Touch ID unlock via a long-lived `LAContext` (held for the agent's lifetime; `set-token`/`delete-token` do not prompt). Token held in a locked buffer with 15-min refresh-on-use TTL + absolute idle exit; zero buffer on eviction/exit.
- **Cache map:** per-profile `jobs.json`, keyed by full folder-aware job name, `{fetched_at,url,jobs:{<name>:{path,class,buildable,params:[{name,type,choices,default}],params_fetched_at}}}`; atomic temp+rename, `0600`. Full crawl on cold/`--refresh`; live param read on `get`/`build`; miss → one crawl then retry → else not-found(3) with suggestions; 24h list staleness hint.
- **Params:** pre-parse pass lifts `--param-*` from argv before go-flags; validate names/choices against cached defs, fill defaults.
- **Exit codes:** 0 ok, 1 usage, 2 auth, 3 not-found, 4 build-failed (with `--wait`).
- **HTTP mapping:** 401→auth(2), 403→permission, 404→not-found(3), other→wrapped error w/ body snippet; `fmt.Errorf("%w")`, multierror on crawl.

## What Goes Where

- **Implementation Steps** (`[ ]`): all Go code, tests, Makefile, docs — buildable here.
- **Post-Completion** (no checkboxes): manual Touch ID verification on a physical Mac, self-signed cert creation on the user's machine, real-Jenkins smoke test.

## Implementation Steps

### Task 1: Scaffold project, Makefile, and conventions

**Files:**
- Create: `go.mod`
- Create: `Makefile`
- Create: `cmd/jcli/main.go`
- Create: `CLAUDE.md`
- Modify: `.gitignore`

- [x] `go mod init` (module path e.g. `github.com/avitsrimer/jcli`), set Go 1.24, add deps: `go-flags`, `go-pkgz/lgr`, `stretchr/testify`.
- [x] minimal `cmd/jcli/main.go` that parses global flags (`--profile`, `--json`, `-v/--verbose`) and dispatches to a stubbed command set (prints usage, exit code 1 on unknown).
- [x] `Makefile` targets: `build`, `test` (`go test -race ./...`), `lint` (`golangci-lint run`), `fmt` (`gofmt -s -w` + `goimports`), `sign`, `install`.
- [x] write `CLAUDE.md` capturing the umputun-derived Go conventions + a pointer to `DESIGN.md`; add Go build artifacts (`/jcli`, `coverage.out`) to `.gitignore`.
- [x] write test asserting global-flag parsing + unknown-command exit code; run `go build ./...` and `go test -race ./...` — must pass before Task 2.

### Task 2: Config package (profiles)

**Files:**
- Create: `internal/config/config.go`
- Create: `internal/config/config_test.go`

- [x] define `Profile{Name,URL,Username}` and `Config{Default string, Profiles []Profile}` with json tags; locate file at `~/.config/jcli/config.json` (honor `XDG_CONFIG_HOME`).
- [x] implement `Load`, `Save` (atomic temp+rename, `0600`, mkdir `0700`), `Resolve(flag, env)` (flag → `JCLI_PROFILE` → default), `Upsert`, `Remove`, `SetDefault`.
- [x] write tests with `t.TempDir()`: round-trip, perms `0600`, resolution precedence, upsert/update, remove, set-default, missing-file behavior.
- [x] write error-case tests: malformed JSON, unknown profile, empty config.
- [x] run `go test -race ./...` — must pass before Task 3.

### Task 3: Jenkins REST client

**Files:**
- Create: `internal/jenkins/client.go`
- Create: `internal/jenkins/types.go`
- Create: `internal/jenkins/client_test.go`

- [x] define typed `Client{baseURL, username, token, http}` with `WhoAmI`, `Jobs` (recursive `/api/json?tree=jobs[...,jobs[...]]`), `JobParams(name)` (parameterDefinitions), `Build(name, params, waitable)` → returns queue/build location.
- [x] map status codes to typed errors: 401→`ErrAuth`, 403→`ErrPermission`, 404→`ErrNotFound`, other→wrapped error with body snippet; wrap with `fmt.Errorf("%w")`.
- [x] declare nothing global — keep concrete client here; the consumer interface lives in `cli` (Task 8).
- [x] write tests against `httptest.Server` fixtures mirroring research shapes (whoAmI authorities, nested job tree, Choice/String/Boolean param defs, build → 201 + `Location`).
- [x] write error-case tests for 401/403/404/500 mapping; run `go test -race ./...` — must pass before Task 4.

### Task 4: Cache / job map package

**Files:**
- Create: `internal/cache/cache.go`
- Create: `internal/cache/cache_test.go`

- [x] define `Map{FetchedAt,URL,Jobs map[string]Job}` and `Job{Path,Class,Buildable,Params []Param,ParamsFetchedAt}`; path `~/.cache/jcli/<profile>/jobs.json` (honor `XDG_CACHE_HOME`).
- [x] implement `Load`, `Save` (atomic, `0600`, mkdir `0700`), `Lookup(name)`, `UpsertJobParams(name, params)`, `IsStale(ttl)`, and a `Rebuild` hook that accepts a crawl function (job lister) to repopulate the list.
- [x] implement folder-aware flat keying for nested jobs; preserve `ParamsFetchedAt` per job.
- [x] write tests with `t.TempDir()`: round-trip, perms, lookup hit/miss, param upsert updates timestamp, staleness boundary, atomic-write survives partial failure (temp not left behind).
- [x] write error-case tests: corrupt cache file, empty cache; run `go test -race ./...` — must pass before Task 5.

### Task 5: Keychain store (cgo, isolated)

> ⚠️ **De-risking spike first.** Before wiring the full store, validate the core macOS assumptions on hardware, because the design depends on them: (a) does the Touch ID / Keychain dialog actually display the **service** string ("Jenkins CLI") and/or the `localizedReason`; (b) how do a trusted-app ACL (designated requirement) and a `.userPresence` `kSecAccessControl` interact — with biometry set, access is gated by user-presence and the legacy app-identity ACL is largely bypassed. Capture findings in the task notes and adjust the keychain attributes accordingly. This spike is throwaway cgo and is *not* unit-tested.

**Files:**
- Create: `internal/agent/keychain.go`
- Create: `internal/agent/keychain_darwin.go`
- Create: `internal/agent/keychain_other.go` (non-darwin stub)
- Create: `internal/agent/keychain_test.go`

- [x] spike (deferred to manual hardware verification — see Post-Completion). The de-risking spike requires driving the real macOS Keychain/Touch ID dialog, which cannot be exercised non-interactively in an automated agent. ASSUMPTIONS recorded and baked into the implementation: (a) the dialog name is driven by the `kSecAttrService` string (`Jenkins CLI`) and/or the `LAContext`'s `localizedReason`; (b) with a `kSecAccessControl` set to `.userPresence` (`kSecAccessControlUserPresence`), access is gated by user-presence (Touch ID, password fallback) and the legacy trusted-app (designated-requirement) ACL is largely bypassed — so we rely on `.userPresence` for the read gate rather than the app-identity ACL. Final item attributes chosen: generic-password, service `Jenkins CLI`, account `jcli:<profile>`, `kSecAttrAccessibleWhenUnlockedThisDeviceOnly`, `kSecAccessControl` = `.userPresence`. To be confirmed on hardware during Post-Completion manual verification.
- [x] declare `keychainStore` interface (consumer-side, in `agent`), **platform-neutral**: `Set(profile, token) error`, `Get(profile) (string, error)`, `Delete(profile) error` — no `LAContext`/cgo types in the signatures (so the non-darwin stub and the moq mock stay clean). (`internal/agent/keychain.go`; also defines `ErrNoToken`.)
- [x] implement darwin cgo backing against Security.framework: generic-password item, service `Jenkins CLI`, account `jcli:<profile>`, `kSecAttrAccessibleWhenUnlockedThisDeviceOnly` + `kSecAccessControl` `.userPresence`; the long-lived `LAContext` is **created and owned inside `keychain_darwin.go`** and reused internally across `Get` calls via `kSecUseAuthenticationContext`; `Set`/`Delete` do not prompt. (Preamble compiled as Objective-C via `#cgo CFLAGS: -x objective-c -fno-objc-arc`; links Security/LocalAuthentication/Foundation/CoreFoundation. Compiles natively on this Apple Silicon Mac with `CGO_ENABLED=1`.)
- [x] add the non-darwin build-tag stub returning a clear "unsupported platform" error so `go vet`/cross-build stays green (`internal/agent/keychain_other.go`; verified via `GOOS=linux CGO_ENABLED=0 go build/vet ./...`).
- [x] generate a `moq` mock of `keychainStore` via `go:generate` for use by agent tests. NOTE: moq's package loader cannot compile the Objective-C cgo preamble, so it fails to load this package; the `//go:generate go run github.com/matryer/moq@latest ...` directive is in place and a faithful moq-shaped mock is hand-written (`internal/agent/keychain_mock.go`) with a TODO to regenerate once moq can load the cgo package.
- [x] write tests against the mock (Set/Get/Delete success + error propagation); document that the real cgo Touch ID path is manually verified (Post-Completion) (`internal/agent/keychain_test.go`). Run `go test -race ./...` — passes (agent package green; benign cgo linker `LC_DYSYMTAB` warning only). golangci-lint not installed on this host (noted). Before Task 6.

### Task 6: Credential agent (socket server + in-memory TTL)

**Files:**
- Create: `internal/agent/agent.go`
- Create: `internal/agent/peercred_darwin.go`
- Create: `internal/agent/peercred_other.go`
- Create: `internal/agent/agent_test.go`
- Modify: `cmd/jcli/main.go`

- [x] single-instance startup: remove any stale socket file, then exclusive `net.Listen("unix", …)` (`0600`) guarded by a lockfile/flock; if another agent already holds it, exit cleanly (the racing CLI will connect to the winner). (`acquireLock`/`releaseLock` in `lock.go` via `unix.Flock LOCK_EX|LOCK_NB`; `newServer` removes the stale socket then binds + `chmod 0600`; lost race → `errAlreadyRunning` which `Run` treats as a clean exit.)
- [x] implement the request loop handling `{op:"get-token"|"set-token"|"delete-token"|"flush", profile, token?}`; `set/delete` proxy to `keychainStore`; `get` serves from memory or fetches from `keychainStore` on miss (the single Touch ID); hold one long-lived `LAContext` for the agent's lifetime. (`Server.handle`/`dispatch` in `agent.go`; the long-lived `LAContext` lives inside the darwin `keychainStore` from Task 5 — the agent reuses the single `store` instance for its whole lifetime, so reads share that context.)
- [x] implement peer-UID verification in `peercred_darwin.go` via `conn.SyscallConn()` + `unix.GetsockoptXucred(fd, SOL_LOCAL, LOCAL_PEERCRED)`; reject UID ≠ server UID. Non-darwin stub returns "unsupported". (`peercred_darwin.go` extracts `Xucred.Uid` through `raw.Control`; `handle` rejects `uid != os.Getuid()`; `peercred_other.go` always errors.)
- [x] implement in-memory token cache keyed by profile with 15-min refresh-on-use TTL + absolute idle exit; zero buffers on eviction/exit. (`entry.token` is a `[]byte` wiped by `zero`; `getToken` refreshes `expires` on hit; `watchIdle` closes the listener after the absolute idle window, ending `Serve` which flushes all buffers; `evict`/`flushAll` zero on delete/flush.)
- [x] add hidden `jcli __agent` entry in `main.go` that boots the server. (intercepted before flag parsing, calls `agent.Run`; kept out of `knownCommands`/`printUsage` so it stays hidden.)
- [x] write tests over a real unix socket with a mocked `keychainStore`: cache hit avoids second keychain call, TTL expiry forces refetch, set/delete proxy correctly, flush clears, idle exit. (Peer-UID *rejection* is structurally untestable in a single-UID env → verified manually in Post-Completion; unit-test only the extraction/accept path.) Run `go test -race ./...` — must pass before Task 7. (`agent_test.go` over real sockets; accept path exercised via real `peerUID` (same-UID), and the reject *branch* via an injected `peerUID` returning a foreign uid. NOTE: socket paths use a short `os.TempDir` dir, not `t.TempDir`, to stay under the 104-byte `sockaddr_un` limit. `go test -race ./...` green (benign cgo `LC_DYSYMTAB` linker warning only); linux cross-build `GOOS=linux CGO_ENABLED=0 go build/vet ./...` green; `golangci-lint` not installed on this host.)

### Task 7: Creds client (spawn + talk to agent)

**Files:**
- Create: `internal/creds/creds.go`
- Create: `internal/creds/creds_test.go`

- [x] implement client: connect to agent socket; on connection-refused spawn the agent **detached** via `exec.Command(self, "__agent")` with `SysProcAttr{Setsid:true}` and detached stdio (no controlling terminal, not reaped on parent exit), then wait (bounded) for the socket to appear and retry. (`internal/creds/creds.go`: `connect`→`spawnAgent` sets `Setsid:true`, nil stdio, `Process.Release()` so the child is reparented to init and never reaped by the CLI; `waitConnect` polls every 25ms up to a 3s `spawnTimeout`. `self` = `os.Executable()` in `New`. Reuses the exported `agent.SocketPath()` to match the agent's path resolution; mirrors the agent's unexported `{op,profile,token}`/`{token,error,auth}` wire types byte-for-byte with a sync note.)
- [x] handle the spawn race idempotently: the loser of the agent's exclusive bind (Task 6) exits, so `creds` must treat "another agent already listening" as success and connect — never fatal on a benign bind/refused-then-present transition. (`isRefused` treats ECONNREFUSED + ENOENT/ErrNotExist all as "no agent — spawn/wait"; `waitConnect` keeps polling on refused until the winner's socket accepts, so a refused-then-present transition is never fatal. Verified by `TestClient_ConcurrentSpawnRace` (8 racing callers, stub takes an exclusive flock so losers exit — all 8 still succeed via the winner).)
- [x] implement `Token(profile)`, `SetToken(profile, token)`, `DeleteToken(profile)`, and `Flush()`; surface auth failures as `ErrAuth` (exit 2); never log the token, zero local copies after use. (All four methods present; a creds-local `ErrAuth` sentinel wraps any `resp.Auth` error via `fmt.Errorf("%w")` — exit-code mapping deferred to `cli` per plan. Token never passed to any log call; the request copy's `Token` field is cleared after the exchange in `do`.)
- [x] write tests against a stub agent socket: happy path, spawn-when-absent (point `self` at a fake agent binary/script), bounded-wait timeout, concurrent callers racing to spawn. (`creds_test.go`: happy-path get/set/delete/flush against an in-process stub listener; `TestClient_SpawnWhenAbsent` points `self` at a real binary compiled from `testdata/stubagent` that binds the socket **synchronously** before serving — deterministic, not wall-clock-dependent; `TestClient_SpawnTimeout` points `self` at `/usr/bin/true`; `TestClient_ConcurrentSpawnRace` for the racing herd. Detached stubs record their pid beside the socket and are killed in `t.Cleanup` — verified no leaked processes after `-race` runs.)
- [x] write error-case tests: socket gone mid-request, malformed response, spawn timeout. Run `go test -race ./...` — must pass before Task 8. (`TestClient_SocketGoneMidRequest`, `TestClient_MalformedResponse`, `TestClient_SpawnTimeout`, plus auth/non-auth error mapping. `go build/vet ./...`, `gofmt -s -l .`, `go test -race ./...` all green (benign cgo `LC_DYSYMTAB` linker warning only); `GOOS=linux CGO_ENABLED=0 go build ./...` green; golangci-lint not installed on this host — skipped.)

### Task 8: CLI dispatch, flags, and --param pre-parse

**Files:**
- Create: `internal/cli/cli.go`
- Create: `internal/cli/params.go`
- Create: `internal/cli/cli_test.go`
- Create: `internal/cli/params_test.go`
- Modify: `cmd/jcli/main.go`

- [x] declare the consumer-side `jenkinsClient` interface in `cli` (matching Task 3 methods: `WhoAmI`/`Jobs`/`JobParams`/`Build`, all taking `context.Context`) and a small `app` struct wiring config + cache (`loadCache`) + creds (consumer-side `credsClient` iface) + a `clientFactory` (url/username/token → `jenkinsClient`, injectable for tests); centralize exit-code mapping in `exitCode(err)` (0 ok / 1 usage / 2 auth via `jenkins.ErrAuth`+`creds.ErrAuth` / 3 `jenkins.ErrNotFound` / 4 `errBuildFailed`). `internal/jenkins` does NOT import `cli`; `cli` imports `creds` (pure-Go, no cgo). (`internal/cli/cli.go`)
- [x] implement the `--param-<name>=val` pre-parse pass (`extractParams`/`splitParam` in `internal/cli/params.go`): scans argv, lifts matching args into a `map[string]string` (split on FIRST `=`, so `=` inside the value survives), returns the remaining argv for go-flags; bare `--param-x` (no `=`) and `--param-=v` (empty name) pass through untouched; later duplicate wins. Stored on `app.buildParams` for the build command (Task 11).
- [x] register subcommand skeletons (`login`, `profile`, `logout`, `list`, `get`, `build`, `dump`) with go-flags (`jessevdk/go-flags` v1.6.1) in `internal/cli/commands.go`; global `--profile`/`--json`/`-v` bound on `globalOpts`; `--help` is a clean exit (0). Skeleton `Execute` methods record a usage exit via `notImplemented` until bodies land (Tasks 9-11). Hidden `__agent` path stays intercepted in `main.go` before flag parsing.
- [x] generate a `moq` mock of `jenkinsClient` (`internal/cli/jenkins_mock.go`). NOTE: `moq@latest` (v0.7+) requires Go 1.26; this module targets Go 1.24, so the `//go:generate` directive is pinned to `go run github.com/matryer/moq@v0.5.3`, which loads `internal/cli` cleanly (no cgo in this package) and generated the committed mock.
- [x] write tests: param pre-parse (multiple params, quoted values, `=` in value, empty value, no params, bare-prefix/empty-name/non-param pass-through, later-dup-wins, empty argv — `params_test.go`); exit-code mapping (each error class — `cli_test.go`); profile resolution wiring (flag/env/default/unknown via `config.Resolve`+`Get` — `cli_test.go`); plus dispatch tests (known/unknown command, global-flag parse, param pre-parse seam, `--help`). `go test -race ./...` green; `go build`/`go vet`/`gofmt -s -l .` clean; `GOOS=linux CGO_ENABLED=0 go build ./...` green. `golangci-lint` not installed on this host (skipped, as in prior tasks).

### Task 9: Auth commands — login / profile / logout

**Files:**
- Create: `internal/cli/cmd_login.go`
- Create: `internal/cli/cmd_profile.go`
- Create: `internal/cli/cmd_logout.go`
- Create: `internal/cli/cmd_auth_test.go`

- [x] `login [--profile]`: prompts URL + username + token; the token is read **no-echo** from the TTY (`golang.org/x/term.ReadPassword` on `/dev/tty`, falling back to stdin's fd — never from argv/env). Prompting is injectable via an `app.promptFactory func() prompter` seam (`ttyPrompter` in production; a scripted prompter in tests). The token is verified via `WhoAmI` *before* persisting anything (bad token → `jenkins.ErrAuth` → exit 2, leaving config/keychain untouched); on success `config.Upsert` + `Save` then `creds.SetToken`. Re-running the same profile name updates the existing entry (no duplicate). The profile name is the resolved `--profile`/`JCLI_PROFILE`. (`internal/cli/cmd_login.go`.) NOTE: pinned `golang.org/x/term@v0.31.0` — v0.44 requires Go ≥1.25 while this module targets Go 1.24.
- [x] `profile list|use <p>|rm <p>`: action via the first positional (bare `profile` ⇒ `list`); `list` prints each profile marking the default with `*`; `use` = `config.SetDefault`+`Save`; `rm` = `config.Remove`+`Save`+`creds.DeleteToken` for that profile. Unknown profile on `use`/`rm` → `config.ErrNotFound` → exit 3; missing/unknown action → usage exit 1. (`internal/cli/cmd_profile.go`.)
- [x] `logout [--profile]`: resolves the profile, `creds.DeleteToken`; `--purge` additionally `config.Remove`+`Save`. Unknown profile → exit 3. (`internal/cli/cmd_logout.go`.)
- [x] tests with mocked `jenkinsClient` (`WhoAmI`) + a recording fake `credsClient` (`fakeCreds` set/delete) + `t.TempDir()` config (`XDG_CONFIG_HOME`): login success (persists profile to disk + stores token), verify-fail leaves nothing persisted, duplicate-login update (no dup), profile list/use/rm, logout deletes token, `--purge` removes profile. Prompting fed deterministically via `scriptedPrompter`. (`internal/cli/cmd_auth_test.go`.)
- [x] error-case tests: login bad token → exit 2 (and persists nothing); `profile use/rm` unknown → exit 3; missing-name / unknown-action / empty-url → exit 1; logout delete-token error → exit 1. Also extended `exitCode` to map `config.ErrNotFound` → 3 (was jenkins-only). `go build`/`go vet`/`gofmt -s -l .`/`go test -race ./...` all green (benign cgo `LC_DYSYMTAB` linker warning only); `GOOS=linux CGO_ENABLED=0 go build ./...` green; golangci-lint not installed on this host (skipped, as prior tasks).

### Task 10: Read commands — list / get / dump

**Files:**
- Create: `internal/cli/cmd_list.go`
- Create: `internal/cli/cmd_get.go`
- Create: `internal/cli/cmd_dump.go`
- Create: `internal/cli/cmd_read_test.go`

- [x] `list [pattern]`: load cached map (crawl if cold or `--refresh`), filter by glob/substring, print names (+ `--json`); 24h staleness hint. (`internal/cli/cmd_list.go`: `runList` loads via `app.loadCache`, crawls + `Rebuild`+`Save` when cold (`len(Jobs)==0 || FetchedAt zero`) or `--refresh`; otherwise an `IsStale(24h)` hint to stderr (never blocks). `filterJobs`/`matchJob`: glob via `path.Match` when the pattern has `*?[`, else case-insensitive substring (glob falls back to substring on no-match/malformed). `--json` emits a sorted JSON array. The list pattern arrives as the first positional in `listCmd.Execute`.)
- [x] `get <job>`: live param read, update cache; on miss → one crawl then retry → else not-found(3) with close-name suggestions; human + `--json` output. (`internal/cli/cmd_get.go`: `runGet` looks up the job, and on a miss does exactly one `Rebuild`+`Save` then retries; still-absent → `fmt.Errorf("%w", jenkins.ErrNotFound)` (exit 3) with `suggestNames` (bidirectional case-insensitive substring, capped at 5). On hit it calls `client.JobParams(job.Path)`, `UpsertJobParams`+`Save`, then prints human or `--json`. Empty job name → usage exit 1.)
- [x] `dump`: emit full cached map as formatted JSON; `--refresh` rebuilds via crawl first. (`internal/cli/cmd_dump.go`: `runDump` loads the cache, on `--refresh` does `Rebuild`+`Save`, then `json.Encoder` with `SetIndent` over the whole `cache.Map`. Cold cache emits a valid Map with a non-nil empty `jobs` object because `cache.Load` normalizes `Jobs` to a non-nil map.)
- [x] write tests with mocked client + temp cache: list filter + cold-crawl + staleness hint, get live-read updates cache, get miss→crawl→retry, dump JSON shape, `--refresh` path. (`internal/cli/cmd_read_test.go`: `readTestApp` wires `XDG_CACHE_HOME=t.TempDir()`, a one-profile config, `fakeCreds`, and the jenkins mock. `TestList` covers cold-crawl (one `Jobs` call + persisted cache), substring + glob filters, `--json` array, `--refresh` over a warm cache, and the stale-cache hint with a deliberately nil `JobsFunc` to prove no crawl happens. `TestGet` covers live-read-updates-cache (params + `ParamsFetchedAt` persisted), miss→crawl→retry (exactly one `Jobs` + one `JobParams`), and `--json`. `TestDump` covers indented JSON shape with no crawl and the `--refresh` rebuild.)
- [x] write error-case tests: get unknown job after crawl (exit 3 + suggestions), empty map dump. Run `go test -race ./...` — must pass before Task 11. (`TestGet` "unknown job after crawl" asserts exit 3, one crawl, no `JobParams` call, and `did you mean ... deploy-app` in stderr; "missing job name" → usage exit 1. `TestDump` "empty cache" asserts a valid non-nil empty `jobs` object. Also updated `cli_test.go` dispatch tests to use `build` (still a skeleton) instead of the now-implemented `list`/`dump`. `GOTOOLCHAIN=local go build/vet ./...`, `gofmt -s -l .`, `go test -race ./...` all green (benign cgo `LC_DYSYMTAB` linker warning only); `GOOS=linux CGO_ENABLED=0 go build ./...` green (exit 0); golangci-lint not installed on this host — skipped, as prior tasks.)

### Task 11: Build command (validate + trigger + --wait)

**Files:**
- Create: `internal/cli/cmd_build.go`
- Create: `internal/cli/cmd_build_test.go`

- [x] `build <job> [--param-<name>=val ...] [--wait]`: resolve params from cache (live-read on miss), validate names + Choice values, fill defaults for omitted params, reject unknown names. (`internal/cli/cmd_build.go`: `runBuild` resolves the job via `resolveJob` (one crawl-then-retry on a cache miss, mirroring get), then `paramDefs` returns cached defs when `ParamsFetchedAt` is set else does a live `JobParams` read written back to the cache. `validateParams` rejects unknown supplied names (usage error listing valid names), rejects out-of-range Choice values, and fills defaults for omitted params before sending to `Build`.)
- [x] trigger `buildWithParameters`; fire-and-forget default prints monitor URL; `--wait` resolves queue→build number, polls to completion, sets exit code 0/4 by result. (Default prints the queue/monitor URL and returns 0 without polling. `--wait` → `waitForBuild`: phase 1 polls `QueueItem` until `executable` populates (the queue→build transition), phase 2 polls `BuildResult` until `!building && result != ""`; SUCCESS→0, any other terminal result (FAILURE/UNSTABLE/ABORTED/cancelled)→`errBuildFailed` (exit 4). Poll interval is injectable via `app.pollInterval` (defaults to `defaultPollInterval` 2s; tests set 1ms so they never sleep on wall-clock).)
- [x] surface auth/not-found/permission via the shared exit-code mapping. (All errors flow through `app.fail`→`exitCode`: `jenkins.ErrAuth`/`creds.ErrAuth`→2, `jenkins.ErrNotFound`→3, `errBuildFailed`→4, `jenkins.ErrPermission`/other→1.)
- [x] write tests with mocked client: param validation (unknown name, bad choice, default fill), fire-and-forget happy path, `--wait` success (exit 0) and failure (exit 4); fixtures must cover the queue-item `executable` transition (pending → build number assigned) before terminal status, not just the terminal result. (`internal/cli/cmd_build_test.go`: `TestBuild_Validation` (unknown-name→1, bad-choice→1, default-fill asserts BRANCH=master in the sent params, live-read-on-miss); `TestBuild_FireAndForget` (Build called, exit 0, `QueueItemCalls` empty — no polling); `TestBuild_Wait` "queue executable transition then SUCCESS" returns a pending queue item on the first poll and an executable on the second before terminal SUCCESS→0; "building then FAILURE" polls BuildResult twice (building→terminal)→4; "UNSTABLE"→4. NOTE: `--param-*` are passed in argv (not by setting `a.buildParams` directly) because `app.run` re-runs the pre-parse pass.)
- [x] write error-case tests: build unknown job (exit 3), auth failure (exit 2). Run `go test -race ./...` — must pass before Task 12. (`TestBuild_Errors`: unknown job after one crawl→exit 3 with no Build call; `Build` returning `jenkins.ErrAuth`→exit 2; missing job name→usage exit 1.

  **Scope additions (within Task 3's client):** added typed `QueueItem`/`BuildResult` methods + `QueueItem`/`Executable`/`BuildResult` types to `internal/jenkins` (with httptest tests covering the pending→executable and building→terminal transitions), since polling to completion needs to read the queue `executable` field and the build result; extended the consumer-side `jenkinsClient` interface in `internal/cli` with both and regenerated the moq mock (`go run github.com/matryer/moq@v0.5.3`). Removed the now-dead `notImplemented` skeleton helper and updated the dispatch tests in `cli_test.go` (build is no longer a skeleton). `GOTOOLCHAIN=local go build/vet ./...`, `gofmt -s -l .`, `go test -race ./...` all green (benign cgo `LC_DYSYMTAB` linker warning only); `GOOS=linux CGO_ENABLED=0 go build ./...` exit 0; golangci-lint not installed on this host — skipped, as prior tasks.)

### Task 12: Signing target and build wiring

**Files:**
- Modify: `Makefile`
- Create: `scripts/make-cert.sh`

- [x] `scripts/make-cert.sh`: idempotently create/reuse a self-signed code-signing certificate in the login keychain (skip if present); **never regenerate** — a new cert changes the designated requirement (DR) and breaks existing Keychain ACL trust. (`scripts/make-cert.sh`: CN `jcli Code Signing`; `security find-certificate -c` guard exits 0 + reuses when present; otherwise `openssl req -x509` (rsa:2048, EKU=codeSigning) → `openssl pkcs12 -export` (random transient passphrase) → `security import -T /usr/bin/codesign`; optional `add-trusted-cert` step printed (needs sudo) rather than failed-hard. Header warns NEVER to regenerate. Made executable; **not run** — verified by `bash -n` syntax check only; actual cert creation is manual — see Post-Completion.)
- [x] `make sign`: build then `codesign` the binary with the self-signed identity; `make install` signs + installs to `~/bin` or `/usr/local/bin`. (`Makefile`: `SIGN_ID ?= jcli Code Signing` matches the cert CN; `sign: build` → `codesign --force --options runtime --sign "$(SIGN_ID)"` then `show-dr`; `install: sign` → `install -m 0755` to `$(INSTALL_DIR)` (default `$(HOME)/bin`, override to `/usr/local/bin`). Also added `make cert`. **codesign not run** — needs a real signing identity; manual — see Post-Completion.)
- [x] capture and assert the DR with `codesign -d -r-` so rebuild+re-sign produces a stable, verifiable requirement; record the expected DR string. (`make show-dr` runs `codesign -d -r-`; `sign` invokes it after signing so the DR is printed every time. Expected DR recorded in CLAUDE.md/README: `identifier "jcli" and certificate leaf[subject.CN] = "jcli Code Signing"` — exact string to be pinned on hardware after the first real sign (Post-Completion). Verified the target runs on the current ad-hoc-signed binary.)
- [x] document in `CLAUDE.md`/README that the keychain ACL + dialog name depend on a stable signing identity and the cert must not be regenerated. (`CLAUDE.md` "Code signing" section + `README.md` "Building & code signing" section both state the DR/ACL/dialog-name binding and the never-regenerate rule.)
- [x] add a test/CI guard that `make build` and `go vet ./...` pass on non-darwin (via the keychain stub) so the repo stays cross-buildable. (`make cross-build`: `GOOS=linux CGO_ENABLED=0 go build ./...` + `go vet ./...` — verified green.)
- [x] run `go build ./... && go test -race ./... && golangci-lint run` — must pass before Task 13. (`GOTOOLCHAIN=local go build/vet ./...`, `gofmt -s -l .`, `go test -race ./...` all green — all packages `ok` (benign cgo `LC_DYSYMTAB` linker warning only). `golangci-lint` not installed on this host — skipped, as prior tasks.)

### Task 13: Screen-lock flush (optional hardening — deferrable)

> The absolute idle-exit TTL (Task 6) already bounds token exposure, so this is *additive* hardening, not a correctness requirement. It adds a second, independent cgo surface (a CFRunLoop for distributed notifications) and cannot be unit-tested — defer if it threatens the schedule. Listed separately so it never bloats the core agent.

**Files:**
- Create: `internal/agent/screenlock_darwin.go`
- Create: `internal/agent/screenlock_other.go`

- [x] subscribe to `com.apple.screenIsLocked` via `CFNotificationCenterGetDistributedCenter` on a dedicated goroutine/run-loop that does not block the socket-accept loop; non-darwin stub is a no-op. (`internal/agent/screenlock_darwin.go`: a C `screenLockObserver` trampoline is registered on the distributed center with `CFNotificationSuspensionBehaviorDeliverImmediately`, then `CFRunLoopRun` runs on a goroutine pinned via `runtime.LockOSThread` — never returns, never touches the accept loop. The `//export goScreenLocked` bridge lives in a separate file `screenlock_export_darwin.go` because cgo forbids `//export` in a file whose preamble defines C functions; the preamble declares `extern void goScreenLocked(void);`. `internal/agent/screenlock_other.go` is a `!darwin` no-op `watchScreenLock(_ func())`.)
- [x] on lock, signal the agent to zero all in-memory token buffers (reuse the `flush` path). (`Server.Serve` calls `watchScreenLock(s.flushAll)`, wiring the lock callback directly to the existing flush-all mechanism — `goScreenLocked` invokes the registered Go callback under a mutex from the run-loop thread.)
- [x] no unit test (cgo run-loop); add a note that lock-flush is verified manually (Post-Completion). Run `go test -race ./...` — must pass before Task 14. (No unit test added — the CFRunLoop/distributed-notification path cannot be driven headlessly; a comment in `agent.go` and the file doc comments mark it manual-verify per Post-Completion. `GOTOOLCHAIN=local go build/vet ./...` (native cgo), `gofmt -s -l .`, `go test -race ./...` all green — all packages `ok` (benign cgo `LC_DYSYMTAB` linker warning only); `GOOS=linux CGO_ENABLED=0 go build ./...` exit 0 (stub keeps cross-build green); golangci-lint not installed on this host — skipped, as prior tasks.)

### Task 14: Verify acceptance criteria
- [x] verify every item in the Overview **Acceptance Criteria** checklist is implemented. (All 6 ACs traced to implementing code + passing tests — see Overview, now all `[x]`. No gaps found; cgo Touch ID unlock and peer-UID rejection on a foreign UID and screen-lock flush are manual-only, the rest is automated.)
- [x] verify edge cases: cold cache crawl, job miss→crawl→retry, param validation failures, auth failure paths. (Tests present from Tasks 10-11: `TestList`/"cold cache crawls"; `TestGet`/"miss triggers one crawl then retry"; `TestBuild_Validation` unknown-name/bad-choice/default-fill; `TestBuild_Errors` auth→2; `jenkins.TestClient_StatusMapping`/`TestClient_Build_ErrorMapping` 401/403/404/500; `agent.TestServer_GetToken_MissingMapsToAuthError`. No additions needed.)
- [x] run full suite: `go test -race ./...`. (Green, exit 0 — all packages `ok`; only the benign cgo `LC_DYSYMTAB` linker warning.)
- [x] run `golangci-lint run` and `gofmt -s -l .` (no diffs); confirm non-darwin `go vet ./...` is green. (`gofmt -s -l .` empty; `go vet ./...` darwin exit 0; `GOOS=linux CGO_ENABLED=0 go vet ./...` exit 0. `golangci-lint` not installed on this host — skipped, install not required per task.)
- [x] verify coverage on pure-Go packages (`config`, `jenkins`, `cache`, `cli`, `creds`) is reasonable; note cgo + peer-UID-rejection + screen-lock paths are manual-only. (Per-package: config 83.3%, jenkins 85.6%, cache 76.7%, cli 79.8%, creds 85.2% (agent 67.1% — cgo keychain/Touch ID, peer-UID-rejection on foreign UID, CFRunLoop screen-lock are manual-only and excluded). Total 77.3%. Reasonable; domain packages well-covered.)

### Task 15: [Final] Update documentation
- [x] update `README.md` so usage/commands/flags match `--help` exactly (replace the placeholder examples with real `jcli` syntax). (Rewrote README: real command surface (login/profile/logout/list/get/build/dump) verified against `./jcli --help` and per-command `--help`; global `--profile`/`--json`/`-v`, dynamic `--param-<name>=val`, `--wait`, `--purge`, `--refresh`; Keychain+Touch ID agent and profiles sections accurate; exit-code table; kept the Task 12 signing section. No production/internal names; placeholders (`auth login`/`params`/`--param branch=`) removed.)
- [x] update `CLAUDE.md` with any new patterns discovered during implementation. (Added "Implementation notes (Go 1.24 target / cgo / tests)": dep pins (x/term v0.31.0, x/sys v0.33.0, moq @v0.5.3, GOTOOLCHAIN=local); darwin cgo Objective-C CFLAGS + //export split into screenlock_export_darwin.go + hand-written keychain_mock.go; the `_darwin.go`/`_other.go` build-tag split for keychain/peercred/screenlock; short os.TempDir socket paths due to the 104-byte sockaddr_un limit; manual-only paths.)
- [x] move this plan to `docs/plans/completed/`. (harness moves the plan after all phases finish)

## Post-Completion
*Items requiring manual intervention or external systems — informational only*

**Manual verification:**
- On a physical Mac with Touch ID: run `make sign`, `jcli login`, confirm the Keychain/Touch ID dialog reads **"Jenkins CLI"**, that "Always Allow"/ACL trust survives a rebuild + re-sign, and that a second command within the TTL does **not** re-prompt.
- Confirm the agent flushes the token on screen-lock (if Task 13 done) and self-exits after the idle timeout.
- Confirm peer-UID rejection: a connection from a different UID to the agent socket is refused (cannot be exercised in the single-UID test env).
- Smoke-test against a real Jenkins: `login` → `list` → `get <job>` → `build <job> --param-… --wait`.

**External system updates:**
- Create the self-signed code-signing certificate on the user's machine (via `scripts/make-cert.sh`) before relying on the keychain UX.
- Optional later: switch the signing identity to an Apple Developer ID for distribution to other machines (config is already a build-time Makefile var).
