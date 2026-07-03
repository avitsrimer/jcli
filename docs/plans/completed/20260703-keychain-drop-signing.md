# Keychain without code signing (drop the self-signed identity)

## Overview
- Remove jcli's self-signed code-signing machinery while **keeping** the macOS
  Keychain as the token store. The token stays encrypted-at-rest in the login
  keychain; jcli simply relies on its default **ad-hoc** code identity (what
  `go build` already produces on Apple Silicon) instead of a managed self-signed
  cert.
- Problem it solves: deletes the cert lifecycle (`make cert`/`sign`/`show-dr`,
  `scripts/make-cert.sh`, the "never regenerate the cert" constraint, the
  stable-DR bookkeeping) — a real maintenance burden — in exchange for one macOS
  **"Allow / Always Allow"** keychain prompt after each rebuild/reinstall (the
  ad-hoc cdhash changes, so the item's trusted-app ACL no longer matches until
  re-authorized). **This trade-off is accepted** (decided during planning).
- Integration: the cgo keychain layer, the in-memory agent, peer-UID checks, and
  screen-lock flush all stay. Only the *signing identity* goes away; the keychain
  ACL trust now anchors on the ad-hoc identity rather than a self-signed DR.

## Context (from discovery)
- Repo: `github.com/avitsrimer/jcli`. No functional signing logic in Go — signing
  is entirely build-time (`codesign`) plus doc comments.
- **Signing surface to remove/reword** (from a full grep):
  - `Makefile`: `SIGN_ID` var + header comment; `cert`, `sign`, `show-dr` targets;
    `install: sign` dependency; `.PHONY` list.
  - `scripts/make-cert.sh`: delete entirely.
  - Go doc comments referencing "signed binary / DR": `internal/agent/keychain.go`
    (:18, :23), `internal/agent/keychain_darwin.go` (:23, :121, :143, :177), and
    the `errSecMissingEntitlement` hint (:135) that says "run `make sign`".
  - Tests referencing the hint/DR: `keychain_darwin_test.go:14` (asserts the hint
    string), `keychain_test.go:10` (comment).
  - Docs: `README.md` ("Building & code signing" section + :110), `CLAUDE.md`
    (intro :9/:14, build list :47-49, stale-agent note :55-56, CI note :71, the
    whole "Code signing (stable identity…)" section :65-116, :168/:175),
    `DESIGN.md` (rows :12-13, :32-34, :39, :57, :68, :81, :152, :175-177).
  - CI: `.github/workflows/ci.yml` never signed — but its `shellcheck` job globs
    `scripts/*.sh`, which becomes **empty** once `make-cert.sh` is deleted (the
    only shell script), so that step must be made no-file-safe.
- Key facts established earlier (dialectic + code verification):
  - The item is a plain generic-password with **no** `kSecAttrAccessControl` /
    `kSecAttrAccessible` (`keychain_darwin.go` `setItem`) — no biometrics.
  - `-34018` "should no longer occur with the file-based keychain" per
    `CLAUDE.md:170-172`; a different code identity yields the **Allow/Always Allow**
    prompt, not a hard rejection. So an unsigned binary is functionally workable.
  - `stop-agent` is still needed after this change — not for DR reasons, but
    because the long-lived agent holds the **old binary's code + the token** in
    memory; a rebuild must replace it to take effect.

## Development Approach
- **Testing approach:** Regular (code/config/doc changes on an existing, tested
  codebase). This is primarily *removal* + docs; the only code touched is doc
  comments and one error-hint string (with its test). New unit tests aren't
  warranted — a documented, justified deviation from the test-per-task template.
- Per-task verification is: the relevant `make`/lint/test command still passes and
  the signing surface is gone. Run `GOTOOLCHAIN=local go test -race ./...` after
  any code-touching task; it must pass before the next.
- **CRITICAL: `DESIGN.md` is the documented source of truth** — its auth-model
  description must end consistent with the new reality, not left describing signing.
- Make small, focused changes; keep `README.md`/`CLAUDE.md`/`--help` in sync.

## Testing Strategy
- **Unit tests:** existing suite stays green (`GOTOOLCHAIN=local go test -race
  ./...`); Task 4 updates the one assertion on the reworded `-34018` hint string.
- **Lint:** `GOTOOLCHAIN=local golangci-lint run` stays at 0 issues.
- **Build/install:** `make build` and `make install` must work with **no cert
  present** (this is the whole point). `make cross-build` stays green.
- **Workflow:** `actionlint .github/workflows/ci.yml` clean after the shellcheck
  step change.
- **No e2e.** The real keychain prompt behavior is manual (Task 1 spike +
  Post-Completion) — it cannot be exercised headlessly.

## Progress Tracking
- Mark completed items `[x]` immediately; new tasks with ➕; blockers with ⚠️.
- Record the Task 1 spike observation inline (⚠️ note) since it's informational.

## Solution Overview
- **No new code path** — jcli already reads the keychain with a plain
  `SecItemCopyMatching`; whether the calling binary is self-signed or ad-hoc only
  changes *which* code identity the item's ACL trusts. Dropping signing means we
  stop managing that identity and accept re-authorization on identity change.
- **Order:** an informational spike first (observe real behavior, no gate), then
  remove the build-time signing (Makefile + script), then reword the code
  comments/hint + test, then the docs, then verify and close out. Removing the
  Makefile targets before the docs keeps each task independently verifiable.

## Technical Details
- **Ad-hoc identity:** on arm64, `go build` emits an ad-hoc signature (cdhash);
  no explicit `codesign` step is needed for the binary to run or to be added to
  the item's ACL. No `codesign` invocation replaces `make sign`.
- **Makefile:** delete `SIGN_ID`, `cert`, `sign`, `show-dr`; `install: build`
  (keep the stale-agent `pkill` line). Keep `stop-agent` and its dependency on
  `build`; reword its comment to drop DR framing.
- **CI shellcheck step:** change `find … -print0 | xargs -0 shellcheck` to
  `xargs -0 -r shellcheck` (GNU `-r` = no-run-if-empty; ubuntu runner has GNU
  xargs) so the job is a safe no-op when `scripts/` has no shell scripts.
- **`-34018` hint:** keep the constant as a safety net but reword the text away
  from "run `make sign`" (e.g. note it is unexpected for the file-based keychain);
  update `keychain_darwin_test.go` to match.
- **Migration:** the currently-installed **signed** binary created the keychain
  item with an ACL trusting the self-signed DR. After switching to the unsigned
  binary, the first read prompts once ("Allow / Always Allow") — or run
  `jcli logout` then `jcli login` to recreate the item under the ad-hoc identity.
  This is manual (Post-Completion).

## What Goes Where
- **Implementation Steps** (checkboxes): Makefile, script deletion, CI tweak, Go
  comment/hint + test, doc rewrites, verification.
- **Post-Completion** (no checkboxes): the manual keychain re-authorization, the
  spike's real-UI observation, and the accepted per-rebuild prompt.

## Implementation Steps

### Task 1: Spike — observe unsigned keychain behavior (informational, no gate)

**Files:** none (observation only)

- [x] `GOTOOLCHAIN=local go build -o /tmp/jcli-unsigned ./cmd/jcli` (ad-hoc signed)
- [x] confirm the ad-hoc signature: `codesign -dv /tmp/jcli-unsigned 2>&1 | grep -i 'Signature=adhoc'`
- [x] stop the current agent, then run a token-reading command with the unsigned
  binary (e.g. `/tmp/jcli-unsigned status`) and **observe** (deferred to
  Post-Completion — interactive, needs a human at the keychain prompt; headless
  spike confirmed Signature=adhoc)
- [x] record the observation in this plan as a ⚠️ note (informational — we proceed
  regardless per the planning decision)
- [x] remove `/tmp/jcli-unsigned`; no repo changes in this task

### Task 2: Strip signing from the Makefile

**Files:**
- Modify: `Makefile`

- [x] delete the `SIGN_ID` var and its header comment (lines referencing the cert CN)
- [x] delete the `cert`, `sign`, and `show-dr` targets
- [x] change `install: sign` to `install: build`
- [x] update `.PHONY` to drop `cert sign show-dr`
- [x] reword the `stop-agent` comment to drop DR framing (agent holds old code + token)
- [x] verify: `make build` and `make install` succeed with **no cert on the machine**;
  `make lint`, `make test`, `make cross-build` unaffected
- [x] run `GOTOOLCHAIN=local go test -race ./...` — must pass before next task

### Task 3: Delete the cert script and make CI shellcheck no-file-safe

**Files:**
- Delete: `scripts/make-cert.sh`
- Modify: `.github/workflows/ci.yml`

- [x] `rm scripts/make-cert.sh`
- [x] change the shellcheck step to `find . -name '*.sh' -not -path './.git/*'
  -print0 | xargs -0 -r shellcheck` so it no-ops when no scripts remain
- [x] `actionlint .github/workflows/ci.yml` — clean
- [x] confirm no other `*.sh` exist in the repo (grep) so the empty-scripts case is real
- [x] (no Go tests — build/CI change)

### Task 4: Reword keychain doc comments + the -34018 hint (and its test)

**Files:**
- Modify: `internal/agent/keychain.go`
- Modify: `internal/agent/keychain_darwin.go`
- Modify: `internal/agent/keychain_darwin_test.go`
- Modify: `internal/agent/keychain_test.go`

- [x] `keychain.go` (:18, :23): reword "signed binary / signed binary's ACL trust"
  to "the binary that created the item (its ad-hoc code identity)"
- [x] `keychain_darwin.go` (:23, :55-57, :121, :143, :177): reword "signed binary /
  signed / DR" comments to describe the ad-hoc identity and the Allow prompt on
  identity change (`:55-57` is the `getItem` C-function doc comment)
- [x] `keychain_darwin.go` (:127): **reword the `errSecMissingEntitlement` const
  comment** — it currently says "-34018 is returned when the calling binary is
  unsigned or ad-hoc signed", which directly contradicts the new model (ad-hoc
  works, gets the Allow prompt, not -34018); it would otherwise ship as a false
  statement sitting right above the reworded hint
- [x] `keychain_darwin.go` (:135): reword the `errSecMissingEntitlement` hint away
  from "run `make sign`" (note it is unexpected for the file-based login keychain)
- [x] `keychain_darwin_test.go` (:14): update the expected hint string to match;
  also reword the stale subtest name at `:13` ("…decodes to a sign hint")
- [x] `keychain_test.go` (:10): reword the comment about the signed binary's DR
- [x] grep Go sources for leftover signing language:
  `grep -rn -i 'signed\|designated requirement\|\bDR\b\|self-signed' internal/` —
  confirm every remaining hit is intentional
- [x] run `GOTOOLCHAIN=local go test -race ./internal/agent/` — must pass; then
  `GOTOOLCHAIN=local golangci-lint run` — 0 issues, before next task

### Task 5: Update README, CLAUDE.md, DESIGN.md

**Files:**
- Modify: `README.md`
- Modify: `CLAUDE.md`
- Modify: `DESIGN.md`

- [x] `README.md`: retitle "Building & code signing (macOS)" → "Building"; drop
  `cert`/`sign`/`show-dr` from the make list; replace the DR paragraphs with a
  short note that the token lives in the Keychain and macOS shows a one-time
  "Allow / Always Allow" prompt (re-shown after a rebuild); reword :44 ("builds,
  signs, and installs"), :48 (**"the Keychain UX depends on a stable signing
  identity" is now false** — there is none; reword to the prompt-per-rebuild
  reality), and :110
- [x] `CLAUDE.md`: reword the intro (:9, :14) and stale-agent note (:55-56, keep
  the "old code + token in memory" rationale, drop DR); remove the build-list
  `cert`/`sign`/`show-dr` (:47-49); replace the entire "Code signing (stable
  identity…)" section (header at :90, through :116) with a short "Keychain trust
  (ad-hoc identity)" note; reword the CI note (:71) and :168/:175
- [x] `DESIGN.md`: update the opening sentence (:4, "authorized by the login-keychain
  trusted-app ACL bound to the signed binary"), the summary rows (:12-13), the
  "single signed binary" framing (:32-34), and the auth-model / signing lines (:39,
  :57, :68, :81, :152, :175-177) to describe the ad-hoc, no-managed-cert model —
  this is the source of truth, make it fully consistent
- [x] grep the three docs for leftover `sign`/`cert`/`DR`/`self-signed` and confirm
  each remaining mention is intentional/historical
- [x] (no tests — docs)

### Task 6: Verify acceptance criteria
- [ ] `make build` and `make install` succeed with no cert present
- [ ] `GOTOOLCHAIN=local go test -race ./...` fully green
- [ ] `GOTOOLCHAIN=local golangci-lint run` exits 0
- [ ] `make cross-build` green
- [ ] `actionlint .github/workflows/ci.yml` clean
- [ ] repo-wide grep (excluding `docs/plans/completed/`) shows no stale
  `codesign`/`make sign`/`make cert`/`show-dr`/`SIGN_ID` references and no dangling
  link to `scripts/make-cert.sh`
- [ ] Go-source grep `grep -rn -i 'signed\|designated requirement\|\bDR\b\|self-signed'
  internal/` — every remaining hit is intentional (catches stale comments the
  keyword grep above would miss, e.g. the old `keychain_darwin.go:55`/`:127` text)

### Task 7: [Final] Documentation close-out
- [ ] final pass that `README.md`, `CLAUDE.md`, `DESIGN.md`, and `--help` agree
- [ ] move this plan to `docs/plans/completed/`

## Post-Completion
*Informational — manual/external, no checkboxes.*

**Manual verification:**
- After `make install`, the first token-reading command with the new unsigned
  binary will show a macOS **"Allow / Always Allow"** prompt for the *Jenkins CLI*
  item (the ad-hoc cdhash differs from the old self-signed DR). Click **Always
  Allow** once — or run `jcli logout && jcli login` to recreate the item cleanly
  under the ad-hoc identity.
- **Expected ongoing behavior (accepted):** each future rebuild/reinstall changes
  the cdhash, so the Allow prompt reappears once per rebuild. This is the accepted
  trade-off for dropping cert management.
- Keychain-unlock password prompts (login keychain locked) are unaffected by this
  change — they were never related to signing.

**Cleanup (optional):**
- The old self-signed `jcli Code Signing` certificate can be left in the login
  keychain harmlessly, or deleted via Keychain Access / `security delete-identity`
  once nothing references it.
