# Fix "read response: EOF" during the interactive Keychain prompt

## Overview
- When the macOS Keychain ACL authorization dialog appears (the "Jenkins CLI wants to
  use… Allow / Always Allow" prompt, which can also ask for the login password), the token
  read **blocks** until the user finishes interacting. But the client↔agent socket is under a
  connection deadline that was armed *before* that blocking read, so it expires mid-prompt,
  the socket is torn down, and the CLI surfaces `read response: EOF`.
- This only bites after a rebuild/identity change (when the ACL prompt appears); a silent
  keychain read returns in milliseconds and never trips the deadline — matching the
  CLAUDE.md note on ad-hoc identity re-prompts.
- Fix removes the deadline from the blocking keychain read on both ends:
  - **Agent**: bound only the *request read*, then set a generous deadline covering the
    blocking `dispatch` + response write.
  - **Client**: bump the single connection deadline from 10s to a generous value that
    tolerates a person interacting with the dialog (user's chosen approach).

## Context (from discovery)
- **`internal/agent/agent.go:219`** — `handle` sets `conn.SetDeadline(now+5s)` on the whole
  connection, *before* decoding the request and *before* `dispatch → getToken → store.Get()`
  (agent.go:274), which is where the Keychain prompt blocks. The response write
  (`writeResponse`, agent.go:316-318) then fails silently (`_ =`) and the deferred
  `conn.Close()` drops the socket.
- **`internal/creds/creds.go:32,129`** — `requestDeadline = 10s`, applied via
  `conn.SetDeadline` in `exchange`; the client then blocks in `json.Decode`, which returns
  EOF when the agent closes the socket (creds.go:134-135 → "read response: %w").
- Test conventions: `internal/agent/agent_test.go` uses `newTestServer(t, store, cfg...)`
  with a `keychainStoreMock{GetFunc: ...}` and overrides `Server` fields like `s.ttl`.
  `internal/creds/creds_test.go` stands up a fake agent via `net.Listen("unix", …)` and
  constructs `&Client{...}` directly with injectable fields (spawnTimeout, pollInterval).
- Both timeouts are private constants/fields; tests can drive the fix deterministically by
  making the read-bound value an overridable field (no multi-second sleeps needed).

## Development Approach
- **testing approach**: TDD (tests first) — write regression tests that reproduce the EOF
  under a deadline shorter than a simulated slow `store.Get`, watch them fail, then apply the
  fix to make them pass.
- complete each task fully before moving to the next; small, focused changes.
- **every task includes new/updated tests** as separate checklist items.
- **all tests must pass before starting the next task.**
- run `make test` (`go test -race ./...`) after each change; keep backward compatibility of
  the wire protocol (no struct/JSON changes).

## Testing Strategy
- **unit tests**: required per task. Agent test injects a `store.Get` that blocks slightly
  longer than a *tiny* configured request-read deadline and asserts the client still receives
  the token (proving the blocking read is no longer under the request deadline). Client test
  stands up a fake agent that delays its response beyond a *tiny* configured connection
  deadline and asserts the response is still read.
- **e2e tests**: none in this repo (CLI, no UI e2e harness). Manual Keychain-prompt
  verification is listed under Post-Completion (cannot be exercised headlessly — real
  keychain UI, per CLAUDE.md).

## Progress Tracking
- mark completed items `[x]` immediately; add ➕ for new tasks, ⚠️ for blockers.
- keep this file in sync if scope changes.

## Solution Overview
- **Agent** (`handle`): replace the single 5s connection deadline with:
  1. `SetReadDeadline(now + reqReadTimeout)` bounding only the request decode;
  2. after a successful decode, clear/reset with `SetDeadline(now + keychainOpTimeout)` before
     `dispatch`.
  Crucially, `keychainOpTimeout` bounds **only the response write** — a `net.Conn` deadline
  cannot preempt a blocking cgo `store.Get()` (`SecItemCopyMatching`). If the keychain read
  itself genuinely hangs, `dispatch` never returns, `writeResponse` is never reached, and this
  deadline is never consulted; the agent goroutine stays blocked in cgo. That is acceptable:
  the goroutine is per-connection and the accept loop keeps serving. The point of the change is
  simply that the blocking read is no longer under the (short) request deadline, so the normal
  interactive-prompt case completes instead of tearing down the socket mid-prompt.
  Set `reqReadTimeout` directly in the `newServer` struct literal (exactly like `ttl`/`idle`)
  and let `newTestServer`'s cfg func override it — no zero-value branch needed.
- **Client** (`exchange`): bump `requestDeadline` 10s → 2 min, and make it an overridable
  `Client` field (`requestTimeout`) resolved with a zero-value fallback (the client's tests
  construct `&Client{...}` directly, bypassing `New`, so the fallback is genuinely needed —
  unlike the agent). The client deadline is the **effective overall protection**: it is the
  only bound that will actually fire if the keychain read hangs (the client's `Decode` times
  out and the CLI returns an error). After such a client timeout the agent may still complete
  the read and cache the token, so an immediate retry succeeds from cache.
- 2 min is chosen to comfortably outlast a person reading and answering the dialog (incl.
  typing a login password) while still surfacing a truly hung read within a bounded time.

## Technical Details
- New constants:
  - `internal/agent/agent.go`: `requestReadTimeout = 5 * time.Second` (bounds request read),
    `keychainOpTimeout = 2 * time.Minute` (bounds the response *write* only — it cannot
    preempt the blocking cgo keychain read; see Solution Overview).
  - `internal/creds/creds.go`: `requestDeadline` value changed 10s → `2 * time.Minute`
    (keep the name/comment, update the comment to explain the interactive-prompt rationale).
- New overridable fields:
  - `Server.reqReadTimeout time.Duration` — set in the `newServer` struct literal to
    `requestReadTimeout` (like `ttl: defaultTTL`); `newTestServer`'s cfg func overrides it. No
    zero-value branch.
  - `Client.requestTimeout time.Duration` — resolved with a zero-value fallback in `do` (the
    caller of the free function `exchange`): use `c.requestTimeout` when non-zero, else the
    `requestDeadline` constant, and pass the resolved duration into `exchange`. Tests construct
    `&Client{...}` directly, so the fallback is required here.
- No wire-format changes; `request`/`response` structs untouched. Exit-code behavior
  unchanged.

## What Goes Where
- **Implementation Steps** (`[ ]`): agent + client code and their unit tests.
- **Post-Completion** (no checkboxes): manual Keychain-prompt verification after a rebuild.

## Implementation Steps

### Task 1: Agent — bound the request read, not the blocking keychain read

**Files:**
- Modify: `internal/agent/agent.go`
- Modify: `internal/agent/agent_test.go`

- [x] add `reqReadTimeout time.Duration` field to `Server` and set it in the `newServer`
      struct literal to the `requestReadTimeout` constant (exactly like `ttl: defaultTTL`,
      agent.go:148) — no zero-value branch; `newTestServer`'s cfg func overrides it
- [x] add constants `requestReadTimeout = 5 * time.Second` and `keychainOpTimeout = 2 * time.Minute`
      with doc comments: the former bounds the request decode; the latter bounds only the
      response write and explicitly does NOT preempt the blocking cgo keychain read
- [x] write a regression test in `agent_test.go`
      (`TestServer_GetToken_SlowKeychainReadNotBoundedByRequestDeadline`): configure a
      `Server` with a tiny `reqReadTimeout` (e.g. 30ms) via `newTestServer`'s cfg func, and a
      `keychainStoreMock.GetFunc` that sleeps longer (e.g. 80ms) before returning a token;
      assert the client connection reads back the token with no error. Red state is a compile
      error until the field exists, then a runtime failure on the pre-fix `handle` (the single
      short deadline covers the sleeping read); it passes once `handle` stops bounding the read
- [x] in `handle`, replace `conn.SetDeadline(now+5s)` with `SetReadDeadline(now + s.reqReadTimeout)`
      before decoding the request; after a successful decode, `SetDeadline(now + keychainOpTimeout)`
      before `dispatch`/`writeResponse`
- [x] run `make test` — the new test and all existing agent tests must pass before Task 2

### Task 2: Client — tolerate a slow response during the keychain prompt

**Files:**
- Modify: `internal/creds/creds.go`
- Modify: `internal/creds/creds_test.go`

- [x] add `requestTimeout time.Duration` field to `Client`; resolve it in `do` (the caller of
      the free function `exchange`) — use `c.requestTimeout` when non-zero, else the
      `requestDeadline` constant — and pass the resolved duration into `exchange`
- [x] change the `requestDeadline` constant 10s → `2 * time.Minute`; update its doc comment to
      explain it must outlast an interactive Keychain prompt (login-password entry), not just
      a fast silent read, and that it is the only bound that fires if the keychain read hangs
- [x] leave `New` unchanged (zero `requestTimeout` → constant fallback), so production uses
      the 2-min constant; no wiring needed
- [x] write a regression test in `creds_test.go`
      (`TestClient_Token_HonorsRequestTimeoutField`): stand up a fake agent listener that reads
      the request then sleeps ~80ms before writing the response. Two sub-cases against the
      injected field:
      (a) `requestTimeout = 30ms` → expect a deadline/timeout error — this is the fail-first
      discriminator: pre-fix code ignores the field and uses the 10s constant, so it does NOT
      time out and the assertion fails until the field is wired in;
      (b) `requestTimeout = 1s` → expect the token returned — a positive-path guard that passes
      both before and after the fix (not itself a regression proof)
- [x] run `make test` — new + existing creds tests must pass before Task 3

### Task 3: Verify acceptance criteria
- [x] confirm agent's blocking keychain read is no longer under the request deadline and the
      response write is under `keychainOpTimeout`
- [x] confirm client `requestDeadline` is 2 min and overridable via `requestTimeout`
- [x] run full suite: `make test`
- [x] run `make lint`
- [x] run `make cross-build` (deadline changes are platform-agnostic; confirm the `!darwin`
      stubs still compile)
- [x] no e2e harness in this repo — N/A (no e2e harness)

### Task 4: [Final] Update documentation
- [x] update `CLAUDE.md` only if a new durable pattern emerged (likely a one-line note that
      the agent must not hold a deadline across the blocking keychain read); skip if it
      duplicates code-level comments
- [x] `README.md` needs no change (no user-facing flag/behavior change) — verified: only
      timeout mention is the 15-min TTL, unrelated to the request/connection deadline
- [x] move this plan to `docs/plans/completed/`

## Post-Completion
*Items requiring manual intervention — no checkboxes, informational only.*

**Manual verification** (cannot be exercised headlessly — real Keychain UI, per CLAUDE.md
"Peer-UID rejection, the keychain ACL authorization prompt… verified manually"):
- Rebuild the binary (`make build`) to force a new ad-hoc identity, then run a command that
  reads the token (e.g. `jcli list`). Confirm the Keychain "Allow / Always Allow" prompt
  appears, take >10s (previously fatal) to click **Always Allow** / enter the login password,
  and confirm the command completes successfully with no `read response: EOF`.
- Confirm a subsequent silent read (same binary identity) still returns immediately.
