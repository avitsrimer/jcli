# jcli — Design

A general-purpose macOS Jenkins CLI with multi-profile support, Keychain-backed
credentials (authorized by the login-keychain trusted-app ACL bound to the signed
binary), and a cached job/param map.

## Decisions (brainstorm summary)

| Decision | Choice |
|---|---|
| Language | Go 1.24+, following umputun/tg-spam conventions |
| Credential model | On-demand signed **agent** authorized by the keychain ACL (bound to the binary's DR), in-memory token with TTL, served over a unix socket |
| Code signing | Self-signed local code-signing certificate (stable designated requirement) |
| Job map | Cache job **list + params** per profile; live refresh; `dump` emits JSON |
| Build params | Dynamic `--param-<name>=val`, validated against cached param defs |
| Build behavior | Fire-and-forget by default; `--wait` polls to completion (result-based exit code) |

### Adopted Go conventions (from umputun/tg-spam)

- Go 1.24+. `go build`, `go test -race ./...`, `golangci-lint run`, `gofmt -s -w`, `goimports -w`.
- Max 140-char lines. CamelCase vars, PascalCase exported; doc comments on all exported symbols.
- In-code comments lowercase, describe current purpose — never history/changes.
- Errors returned with context (`fmt.Errorf("%w", …)`); aggregate with multierror.
- Define interfaces in the **consumer** package, not the provider.
- Table-driven tests with `testify`; mocks via `matryer/moq` through `go:generate` (never hand-edited).
- CLI flags: `github.com/jessevdk/go-flags`. Diagnostics: plain `fmt`-to-stderr (a `--verbose`-gated
  `app.verbosef` helper) — no logging framework, by design for a single-binary CLI.
- Keep README in sync with `--help`. No AI-attribution / "Test plan" sections in commits.

## Section 1: Process model & components

**Single signed binary, two modes.** One binary runs as the CLI *or* as the
credential agent (`jcli __agent`, hidden subcommand). One self-signed cert, one
designated requirement, one ACL entry — simpler to sign and trust.

**Components:**
1. **CLI front-end** — parses commands (`login`, `list`, `get`, `build`, `dump`, `profile`, `logout`), talks to Jenkins, renders output.
2. **Credential agent** — same binary in agent mode. Launched on-demand by the CLI if not running. Reads the token from the keychain (authorized once by the trusted-app ACL), holds it in memory with a TTL, serves it over a unix-domain socket.
3. **Keychain layer** — per-profile plain generic-password item in the default/login keychain, authorized by the trusted-app ACL bound to the signed binary's DR. No `kSecAttrAccessible` is set, so the item takes the default `kSecAttrAccessibleWhenUnlocked`.
4. **Config store** — `~/.config/jcli/config.json`: profiles (name → url, username, keychain account ref, default flag). **No secrets.**
5. **Cache store** — `~/.cache/jcli/<profile>/jobs.json`: job map (list + param defs).
6. **Jenkins REST client** — thin wrapper over whoAmI, `/api/json` tree, per-job param defs, `buildWithParameters`.

**Flow (command needing token):** CLI → connect to agent socket → (spawn agent if
absent) → agent reads the keychain item once (authorized by the trusted-app ACL),
caches in memory → returns token over socket → CLI calls Jenkins. Within the TTL,
later commands reuse the in-memory token with no further keychain reads. Socket is
`0600` in the user's runtime dir; agent verifies peer UID via
`SO_PEERCRED`/`LOCAL_PEERCRED`.

## Section 2: Keychain hardening & agent lifecycle

**Keychain item.** One plain generic-password item per profile, account =
`jcli:<profile>`, service = `Jenkins CLI` (this string is what the keychain
authorization prompt shows). Stored in the user's **default/login keychain** (the
file-based keychain), authorized by:
- the trusted-app **ACL** bound to the signed `jcli` binary's designated requirement (DR) — the creating signed binary is added to the item's ACL, so it reads the item back silently; a binary with a *different* DR triggers the standard keychain "Allow / Always Allow" prompt naming *Jenkins CLI*, and trust survives rebuilds with the same cert.

No `kSecAttrAccessControl` and no data-protection accessibility attribute are set,
so the item lands in the file-based keychain (not the data-protection keychain,
which would require a `keychain-access-groups` entitlement). With no
`kSecAttrAccessible` set, the item takes Apple's documented default
`kSecAttrAccessibleWhenUnlocked` — acceptable, since the token is only needed while
the Mac is unlocked.

**Agent lifecycle.**
- **Spawn:** CLI tries the socket; on connection-refused it `fork/exec`s `jcli __agent` (detached), waits for the socket, then proceeds.
- **Read:** on the first token request, the agent reads the keychain item (authorized by the trusted-app ACL — silent for the same signed binary). Token held in a locked memory buffer.
- **TTL & idle exit:** configurable TTL (default 15 min) refreshes on use; agent self-terminates when the TTL lapses or after an absolute idle timeout, zeroing the buffer. Re-arming re-reads the keychain.
- **Lock events:** agent subscribes to screen-lock/session events and flushes the token immediately on lock.
- **Scope:** one agent serves all profiles; each profile's token read independently, cached under its own key.

**Hardening details:** socket in runtime dir at `0600`; peer-UID check rejects
other users; token zeroed on free; no token ever written to disk, logs, argv, or
env; secrets never passed to log calls.

### Why the agent is necessary (not just a TTL in the CLI)

A bare CLI exits in milliseconds, so a TTL has nowhere to persist across
invocations. The keychain authorizes reads by the binary's ACL: the same signed
binary reads silently, so the access boundary is the signing identity (the DR),
not a per-read gesture. The agent is the living process that makes the TTL real —
it reads the keychain **once** per arming window and serves the in-memory token to
subsequent commands, avoiding repeated keychain reads (and any ACL prompt if trust
has not yet been granted) within the TTL. The agent also bounds the token's
in-memory lifetime: it self-exits on idle/TTL lapse and flushes on screen lock,
which a long-lived "Always Allow" trust on a bare CLI could not provide.

## Section 3: Profiles & command surface

**Profiles.** `~/.config/jcli/config.json` holds a list of profiles and a
`default` pointer. Each profile: `name`, `url`, `username` — token lives only in
the keychain (account `jcli:<name>`). Resolution order: `--profile <name>` flag →
`JCLI_PROFILE` env → `default` in config.

**Subcommands:**
- **`jcli login [--profile p]`** — prompts for URL, username, token (no-echo TTY, never argv). Verifies via `/whoAmI`, then writes profile to config and token to keychain. Re-running updates.
- **`jcli profile list|use <p>|rm <p>`** — manage profiles / set default.
- **`jcli logout [--profile p]`** — delete keychain item (and optionally the profile).

**Resource commands** (all accept `--profile`, `--json`, `--refresh`):
- **`jcli list [pattern]`** — list jobs from cached map (glob/substring filter); `--refresh` rebuilds first.
- **`jcli get <job>`** — show job details + param defs (live read, updates cache). `--json` for machine output.
- **`jcli build <job> [--param-<name>=val ...] [--wait]`** — validate params against defs, fill defaults, trigger `buildWithParameters`; fire-and-forget by default, `--wait` polls to completion with a result-based exit code.
- **`jcli dump`** — emit full cached job map (list + params) as formatted JSON; `--refresh` to rebuild.

**Conventions:** `go-flags` for static flags + a pre-parse pass that lifts
`--param-*` out of argv before go-flags sees them. Global flags: `--profile`,
`--json`, `-v/--verbose`. Exit codes: `0` ok, `1` usage, `2` auth, `3` not-found,
`4` build-failed (with `--wait`).

## Section 4: Job map, caching & Jenkins client

**Map structure** — `~/.cache/jcli/<profile>/jobs.json`:
```json
{
  "fetched_at": "2026-06-22T13:00:00Z",
  "url": "https://jenkins…",
  "jobs": {
    "Logistics": {
      "path": "/job/Logistics",
      "class": "WorkflowJob",
      "buildable": true,
      "params": [
        {"name":"service","type":"Choice","choices":["supplier_stock","..."]},
        {"name":"stage","type":"Choice","choices":["uat1","uat2","uat3"]}
      ],
      "params_fetched_at": "2026-06-22T13:05:00Z"
    }
  }
}
```
Keyed by full job name (folder-aware: nested jobs stored with their full path).
File written atomically (temp + rename), `0600`.

**Refresh logic.**
- **Full crawl** (`--refresh`, or cold cache): recursively walk `/api/json?tree=jobs[name,url,...,jobs[...]]` to enumerate all jobs/folders. Param defs filled lazily (empty until a job is touched).
- **Per-job live read** (`get`/`build`): fetch that job's `parameterDefinitions` live, update its cache entry + `params_fetched_at`.
- **Miss handling**: requested job absent → one full crawl, then retry; still absent → `not-found` (exit 3) with close-name suggestions.
- **Staleness**: list TTL (24h default) prints a hint to `--refresh`; never blocks.

**Jenkins client.** Thin package over the validated endpoints using stdlib
`net/http` + a typed client. Interface defined in the **consumer** package so it's
mockable with `moq`. Explicit status handling: 401→auth(2), 403→permission,
404→not-found(3), others→wrapped error with body snippet. Errors carry context via
`fmt.Errorf("%w")`; multierror where crawling many jobs.

## Section 5: Project layout & testing

```
jenkins-cli/
├── Makefile                # build, sign (self-signed cert), lint, test, install
├── cmd/jcli/main.go        # flag parsing, command dispatch, agent-mode entry
├── internal/
│   ├── cli/                # command handlers (login, list, get, build, dump, profile)
│   ├── config/             # profiles file read/write (atomic, 0600)
│   ├── cache/              # job map load/store/refresh
│   ├── jenkins/            # REST client + types; consumer-side interface lives in cli/
│   ├── creds/              # client side: talk to agent over socket; spawn if absent
│   └── agent/              # agent mode: keychain read (ACL-authorized), in-memory TTL, socket server
├── README.md
└── CLAUDE.md               # umputun-derived Go conventions + this design
```
Keychain accessed via cgo against `Security.framework`, isolated entirely inside
`agent/` so the rest is pure Go and unit-testable.

**Testing.**
- Table-driven tests (`testify`) throughout.
- `jenkins/` tested against `httptest.Server` fixtures mirroring real shapes (whoAmI, job tree, param defs, build 201).
- `config/` and `cache/` tested with `t.TempDir()` — atomic write, perms, round-trip, staleness, miss-then-crawl.
- Param validation: unknown name, choice violation, default fill.
- `creds`↔`agent` over a real unix socket in tests; the keychain behind a `keychainStore` interface mocked with `moq` (the actual cgo keychain path verified manually).
- `go test -race ./...`, `golangci-lint run`, `gofmt -s` enforced in Makefile/CI.

**Signing.** `make sign` creates/reuses a self-signed code-signing cert and
`codesign`s the binary with a stable identity so the keychain ACL trust (bound to
the DR) + the authorization-prompt name hold across rebuilds.
