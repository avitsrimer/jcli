# jcli — Switch keychain to ACL-only (drop Touch ID / data-protection keychain)

## Overview

Replace the credential agent's keychain backing so the token is stored as a
**plain generic-password item in the file-based login keychain**, authorized by
the keychain's **trusted-application ACL** (bound to the signed `jcli` binary's
designated requirement), instead of a `kSecAttrAccessControl` `.userPresence`
item in the **data-protection keychain**.

**Problem it solves.** On real hardware, the current design fails: storing an
item with `SecAccessControlCreateWithFlags(..., kSecAccessControlUserPresence)`
forces it into the macOS **data-protection keychain**, which requires a
`keychain-access-groups` entitlement. A self-signed binary with **no Apple Team
ID / provisioning profile** cannot carry that entitlement — `SecItemAdd` returns
`OSStatus -34018 (errSecMissingEntitlement)`, and signing the binary *with* the
entitlement gets it **SIGKILLed on launch** (verified: `rc=137`). So `jcli login`
can never persist a token under the current design without a paid Apple Developer
identity.

**Decision (made with the user).** Drop the Touch ID gesture. Use the file-based
login keychain with the default trusted-app ACL: the signed `jcli` (specifically
the agent process that creates the item) is automatically trusted to read it, and
a binary with a *different* code signature / DR triggers the standard keychain
"Allow / Always Allow" authorization prompt. No biometrics, no entitlements, works
with the existing self-signed identity.

**Integration.** The change is confined to the darwin cgo keychain backing
(`internal/agent/keychain_darwin.go`). The platform-neutral `keychainStore`
interface **signatures** (`Set`/`Get`/`Delete`) are unchanged, so the agent socket
protocol, in-memory TTL cache, `creds` client, and `cli` commands are untouched —
but `keychain.go`'s doc comments (which currently describe Touch ID / user-presence)
must be reworded. The non-darwin stub is unchanged, so `make cross-build` stays
green.

### Acceptance Criteria

- [ ] `keychainStore.Set` stores a plain generic-password item (no
  `kSecAttrAccessControl`, no `kSecAttrAccessible` data-protection attribute) in
  the file-based login keychain; a signed `jcli` writes the token with **no
  `-34018`** (pending manual — Post-Completion; behavioral, needs hardware).
- [ ] `keychainStore.Get` reads the item with no `LAContext` / no Touch ID; the
  agent process that created the item reads it back **without a prompt** (same
  signing identity / DR) (pending manual — Post-Completion; behavioral, needs hardware).
- [x] No `LocalAuthentication` dependency remains: the cgo preamble no longer
  includes `<LocalAuthentication/...>` and `#cgo LDFLAGS` no longer links
  `-framework LocalAuthentication`; the long-lived `LAContext` and its
  `newAuthContext`/`releaseAuthContext`/`Close()` plumbing are removed.
- [x] The `keychainStore` interface and the `agent`/`creds`/`cli` layers compile
  unchanged (no signature ripple); the agent's now-purposeless optional
  `store.Close()` call is removed.
- [x] `go test -race ./...` is green; `GOOS=linux CGO_ENABLED=0 go build/vet ./...`
  stays green via the unchanged stub.
- [x] `DESIGN.md`, `CLAUDE.md`, and `README.md` describe ACL-based authorization
  (not Touch ID / `.userPresence` / data-protection keychain).

## Context (from discovery)

- **Failing code:** `internal/agent/keychain_darwin.go` — C preamble `setItem`
  (lines ~36-77) sets `kSecAttrAccessControl` via
  `SecAccessControlCreateWithFlags(kSecAttrAccessibleWhenUnlockedThisDeviceOnly,
  kSecAccessControlUserPresence, …)`; `getItem` (~79-118) reads via
  `kSecUseAuthenticationContext` with the long-lived `LAContext`;
  `newAuthContext`/`releaseAuthContext` (~12-24) create/free that context;
  `#cgo LDFLAGS` (line 7) links `-framework LocalAuthentication`; Go side has the
  `darwinKeychain.authCtx` field, `newDarwinKeychain` (calls `C.newAuthContext()`),
  and `Close()` (calls `C.releaseAuthContext`).
- **Consumer (signatures unchanged, comments stale):** `internal/agent/keychain.go`
  declares the platform-neutral `keychainStore` interface (`Set`/`Get`/`Delete`) +
  `ErrNoToken`. The signatures stay, but its package comment (line 4), interface
  comment, and `Get` comment (lines ~18-23) describe "a single Touch ID /
  user-presence prompt" and must be reworded.
- **Agent wiring:** `internal/agent/agent.go` — `Server.Close()` type-asserts the
  store for an optional `Close()` (added in review) to release the `LAContext`;
  becomes dead once `Close()` is gone. The in-memory TTL cache, peer-UID check,
  and screen-lock flush are unaffected.
- **Stub (unchanged):** `internal/agent/keychain_other.go` (`!darwin`).
- **Reproduction evidence:** a freshly spawned agent from the *signed* binary
  returns `set token: keychain set … failed: OSStatus -34018`; re-signing with a
  `keychain-access-groups` entitlement → binary SIGKILLed on launch (`rc=137`).
- **No data migration:** because `Set` always failed with `-34018`, no
  data-protection keychain items were ever created — nothing to migrate.

## Development Approach

- **Testing approach:** Regular (code first, then tests within the same task) —
  matches the existing project.
- complete each task fully before moving to the next; small, focused changes.
- **CRITICAL: every task MUST include new/updated tests** — except the docs-only
  task, which has no code. The real cgo keychain path cannot be unit-tested
  (covered manually in Post-Completion); the pure-Go `osStatusHint` decoder and
  the agent/keychain wiring stay covered by existing tests.
- **CRITICAL: all tests must pass before starting the next task.**
- **CRITICAL: update this plan file if scope changes during implementation.**
- run `go test -race ./...` (native cgo) and the linux cross-build after each
  change; keep `README.md`/`CLAUDE.md`/`DESIGN.md` in sync; use `GOTOOLCHAIN=local`.

## Testing Strategy

- **unit tests:** the cgo Set/Get/Delete against the real Keychain remain
  manual-only (no headless biometrics/keychain); the `keychainStore` contract is
  exercised through the agent with the moq mock in `agent_test.go` (unchanged).
  Keep the `osStatusHint` table test in `keychain_darwin_test.go`.
- **build guards:** native `go build`/`go test -race ./...` plus
  `GOOS=linux CGO_ENABLED=0 go build/vet ./...` (stub) after the cgo edit.
- **e2e tests:** none (no UI). The end-to-end login→list→read round-trip against
  a real Keychain is a manual smoke test (Post-Completion).

## Progress Tracking

- mark completed items with `[x]` immediately when done.
- add newly discovered tasks with ➕ prefix; document blockers with ⚠️ prefix.
- update plan if implementation deviates from original scope.

## Solution Overview

`SecItemAdd` of a generic-password item **without** `kSecAttrAccessControl` and
**without** a data-protection accessibility attribute lands in the file-based
login keychain. The creating process (the agent, identified by its code
signature) is added to the item's ACL by default, so the same signed binary reads
it back silently; a binary whose signature/DR differs hits the standard keychain
authorization prompt. This removes the `-34018` failure mode entirely and needs no
entitlement. Touch ID is dropped — user-presence is no longer enforced by the
item; the keychain ACL (bound to the signed binary) is the access boundary, which
is the accepted pattern for a local, self-signed CLI.

The agent's in-memory TTL cache is retained: it still avoids repeated keychain
reads (and any ACL prompt if trust hasn't been granted) across commands within the
15-minute window, and the screen-lock flush still zeroes buffers.

## Technical Details

- **Keychain item (new):** generic-password, service `Jenkins CLI`, account
  `jcli:<profile>`, value = token bytes. **No** `kSecAttrAccessControl`, **no**
  `kSecAttrAccessibleWhenUnlockedThisDeviceOnly`. With no `kSecAttrAccessible` set,
  the item takes Apple's documented default `kSecAttrAccessibleWhenUnlocked`
  (acceptable — token is only needed while the Mac is unlocked). With no
  `kSecUseKeychain`/`kSecMatchSearchList`, the item lands in the user's **default**
  keychain (normally the login keychain, but a custom default is possible — so docs
  should say "default/login keychain"). Default ACL ⇒ the creating signed app is
  trusted. `setItem` keeps its delete-then-add to overwrite cleanly.
- **Read:** `getItem` drops the `authCtx` parameter and the
  `kSecUseAuthenticationContext` entry; plain `SecItemCopyMatching` with
  `kSecReturnData`/`kSecMatchLimitOne`. `errSecItemNotFound` → `ErrNoToken`
  (unchanged mapping).
- **cgo surface:** remove `newAuthContext`/`releaseAuthContext`, the
  `<LocalAuthentication/LocalAuthentication.h>` include, and
  `-framework LocalAuthentication` from `#cgo LDFLAGS` (keep Security/Foundation/
  CoreFoundation). Preamble stays Objective-C (`-x objective-c -fno-objc-arc`).
- **Go surface:** `darwinKeychain` loses its `authCtx` field (becomes a
  field-less struct); `newDarwinKeychain` returns `&darwinKeychain{}`; `Close()` is
  removed; `Get` calls `C.getItem(svc, acct, &data, &n)`.
- **agent.go:** remove the optional `store.Close()` invocation in `Server.Close`
  and any comment referencing the long-lived `LAContext` lifetime.
- **osStatusHint:** keep the decoder and its test (harmless safety net); the
  `-34018` message stays accurate ("must be code-signed") even though the
  file-based keychain should no longer surface it.
- **Unchanged:** `keychainStore` interface, agent socket protocol + TTL + peer-UID
  + screen-lock, `creds`, `cli`, `keychain_other.go` stub.

## What Goes Where

- **Implementation Steps** (`[ ]`): the cgo rewrite, agent cleanup, test/doc
  updates — all buildable here.
- **Post-Completion** (no checkboxes): manual on-hardware smoke test (`make sign`
  → `login` → `list` round-trip, no `-34018`, silent read by the same signed
  binary, ACL prompt when the signing identity changes).

## Implementation Steps

### Task 1: Rewrite the darwin keychain backing to ACL-only

**Files:**
- Modify: `internal/agent/keychain_darwin.go`
- Modify: `internal/agent/keychain.go` (comment only — interface signatures stay)
- Modify: `internal/agent/agent.go`
- Modify: `internal/agent/keychain_test.go` (comment only)
- Modify: `internal/agent/keychain_darwin_test.go` (comment only, keep the test)

- [x] in the C preamble, change `setItem` to store a plain generic-password item:
  remove the `SecAccessControlCreateWithFlags(...)` call, the
  `kSecAttrAccessControl` dictionary entry, and the
  `kSecAttrAccessibleWhenUnlockedThisDeviceOnly` accessibility; keep the
  delete-then-`SecItemAdd` flow and the `service`/`account`/`kSecValueData` keys.
- [x] change `getItem` to drop the `authCtx` parameter and the
  `kSecUseAuthenticationContext` entry; keep `kSecReturnData` + `kSecMatchLimitOne`.
- [x] remove `newAuthContext`/`releaseAuthContext`, the
  `<LocalAuthentication/LocalAuthentication.h>` include, and
  `-framework LocalAuthentication` from `#cgo LDFLAGS` (retain Security/Foundation/
  CoreFoundation).
- [x] on the Go side, drop the `darwinKeychain.authCtx` field, simplify
  `newDarwinKeychain` to return `&darwinKeychain{}`, remove `Close()`, and update
  `Get` to call `C.getItem(svc, acct, &data, &n)`; refresh the type/func doc
  comments so they describe ACL-trusted access (no Touch ID / `LAContext`).
- [x] in `internal/agent/agent.go`, remove the optional `store.Close()` call in
  `Server.Close` (and the `LAContext`-lifetime comment) now that nothing needs
  closing.
- [x] reword the stale doc comments in `internal/agent/keychain.go` (package
  comment, the `keychainStore` interface comment, and the `Get` comment) so they
  describe ACL-trusted reads, not "a single Touch ID / user-presence prompt"
  (signatures unchanged).
- [x] refresh the remaining stale const comments in `keychain_darwin.go`: the
  `keychainService` comment (drop "Touch ID" — the service now only shows in the
  ACL authorization prompt for a different-DR binary) and the
  `errSecMissingEntitlement` comment (drop the ".userPresence access-control item"
  wording, per the no-history comment rule).
- [x] update the explanatory comments in `keychain_test.go` and
  `keychain_darwin_test.go` that mention "Touch ID unlock" to describe ACL-trusted
  reads; keep the `TestOSStatusHint` table test.
- [x] run `GOTOOLCHAIN=local go build ./... && GOTOOLCHAIN=local go vet ./...`
  (native cgo) and `GOTOOLCHAIN=local go test -race ./...` — must pass.
- [x] run `GOOS=linux CGO_ENABLED=0 GOTOOLCHAIN=local go build ./... && GOOS=linux
  CGO_ENABLED=0 GOTOOLCHAIN=local go vet ./...` and `gofmt -s -l .` (empty) — must
  pass before Task 2.

### Task 2: Update documentation to the ACL model

**Files:**
- Modify: `DESIGN.md`
- Modify: `CLAUDE.md`
- Modify: `README.md`

- [x] `DESIGN.md`: update the keychain-item description (drop `.userPresence` /
  `kSecAccessControl` / data-protection keychain; describe the file-based login
  keychain + trusted-app ACL bound to the binary's DR) and replace the agent's
  "single Touch ID unlock" rationale with "single keychain authorization; the
  in-memory TTL still avoids repeated keychain reads".
- [x] `CLAUDE.md`: update "What this is" and the implementation-notes keychain
  section (Touch ID → ACL); clarify the signing section now matters because the DR
  binds the **keychain ACL trust** (not an entitlement); note that with the
  file-based keychain `-34018` should no longer occur (the `osStatusHint` decoder
  stays as a harmless safety net); adjust the manual-only list (Touch ID unlock →
  ACL "Allow/Always Allow" prompt when the signing identity changes).
- [x] `README.md`: change the feature bullet and profiles section from "unlocked
  via Touch ID" to "authorized via the login-keychain ACL bound to the signed
  binary"; remove the Touch ID dialog claims; keep the agent/TTL and exit-code
  sections accurate.
- [x] verify docs match the code: `grep -ri "touch id\|userPresence\|LocalAuthentication\|data-protection\|kSecAccessControl" DESIGN.md CLAUDE.md README.md`
  returns only intentional historical/context mentions (ideally none) — before
  Task 3.

### Task 3: Verify acceptance criteria & finalize

- [x] verify every **static/compile** acceptance criterion (no `LocalAuthentication`
  linkage, interface/layers compile unchanged, tests green, cross-build green, docs
  updated) by tracing it to code/docs. **NOTE:** the two **behavioral** criteria —
  no `-34018` on `Set`, and a silent same-binary read — cannot be proven by the
  automated suite (no headless Keychain); they close **only** at Post-Completion
  manual verification, not here. Do not mark them done at Task 3.
- [x] confirm no `LocalAuthentication` linkage remains:
  `grep -rn "LocalAuthentication\|LAContext\|authCtx\|AuthContext\|kSecUseAuthenticationContext\|kSecAttrAccessControl\|SecAccessControl" internal/`
  returns nothing in production code. (one stale comment in keychain_mock.go reworded; guard now returns nothing.)
- [x] run the full suite: `GOTOOLCHAIN=local go test -race ./...` (green) and
  `gofmt -s -l .` (empty).
- [x] confirm the linux cross-build stays green:
  `GOOS=linux CGO_ENABLED=0 GOTOOLCHAIN=local go build ./... && … go vet ./...`.
- [x] move to completed/ (deferred until after review phases — harness)

## Post-Completion
*Items requiring manual intervention or external systems — informational only*

**Manual verification (on a physical Mac):**
- `make sign` (signs with the existing `jcli Code Signing` identity), then
  `jcli login --url … --username …`: confirm the token persists with **no
  `-34018`** and nothing is left unsaved on failure.
- `jcli list` / `jcli get <job>`: confirm the agent reads the token back **with no
  prompt** (same signed binary in the ACL); confirm a second command within the
  TTL does not re-read the keychain.
- Re-sign with a *different* identity (or run an unsigned/ad-hoc build) and confirm
  the keychain shows the **"Allow / Always Allow"** authorization prompt — proving
  the ACL is bound to the signing identity (the DR-stability rationale).
- Confirm the agent still self-exits after the idle timeout and flushes buffers on
  screen lock.

**Notes:**
- Security posture change: user-presence is no longer cryptographically enforced
  by the keychain item; the access boundary is the login-keychain ACL bound to the
  signed binary plus the screen-locked/idle flush. This is the accepted trade-off
  for a self-signed CLI without an Apple Team ID.
- If biometric enforcement is ever required, the path is an Apple Developer ID
  (Team ID) + `keychain-access-groups` entitlement + the data-protection keychain
  — a separate, larger change.
