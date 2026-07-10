# Add `cancel` command to stop a running build

## Overview
- Add a `jcli cancel <job> <number>` subcommand that stops a currently-running
  Jenkins build via `POST <buildURL>/stop`.
- Solves the gap where `jcli build --wait` / `jcli status` can observe a build
  but nothing can stop one — today the only recourse is the Jenkins web UI.
- Integrates with the existing command layer: job resolution reuses
  `app.resolveJob`, the build URL comes from `buildURLFor`, and the new REST
  call sits beside `Build` in `internal/jenkins`. A confirmation prompt reuses
  the existing `prompter`, skippable with `--yes`.

## Context (from discovery)
- files/components involved:
  - `internal/jenkins/client.go` / `client_test.go` — add `Stop` REST method
    (POST, mirrors the existing `Build` POST plumbing).
  - `internal/cli/cli.go` — add `Stop` to the `jenkinsClient` interface.
  - `internal/cli/jenkins_mock.go` — regenerated (never hand-edited).
  - `internal/cli/commands.go` — register `cancelCmd` + its `Execute`.
  - `internal/cli/cmd_cancel.go` / `cmd_cancel_test.go` — new command handler.
  - `README.md`, `skill/jenkins-cli/SKILL.md` — document the command.
- related patterns found:
  - Commands are structs implementing `flags.Commander`; `Execute` returns
    `c.app.fail(c.runX(...))`. Positional args parsed inside `Execute`.
  - `statusCmd.buildByNumber` already resolves job → `buildURLFor(prof, job, n)`
    → `client.BuildStatus`; `cancel` follows the same shape.
  - `client.Build` is the POST template: `http.NewRequestWithContext(...POST...)`,
    `req.SetBasicAuth`, status check, `statusError` mapping.
  - `loginCmd` uses `app.prompter()` (`promptLine`) for interactive input; tests
    inject `promptFactory`.
  - Exit codes in `cli.go`: `exitCode` already maps `jenkins.ErrNotFound`→3,
    `ErrAuth`→2. No new code needed.
- dependencies identified: none new. Reuses `go-flags`, `testify`, moq mock.

## Decisions (from planning)
- **Scope: running build only.** `cancel <job> <number>` → `POST <buildURL>/stop`.
  No queue-item cancellation, no `/term`/`/kill` escalation (can be a later flag).
- **Confirm unless `--yes`/`-y`.** Destructive action gets an interactive
  `[y/N]` prompt by default; `--yes` skips it for scripted/skill use.
- **Testing: regular (code first, then table-driven tests).**
- **Already-finished build is not an error.** Pre-check with `BuildStatus`; if the
  target build is not `building`, print a clear "not running" line and exit 0
  without prompting or POSTing (avoids a pointless stop + confirmation).

## Development Approach
- **testing approach**: Regular (code first, then tests)
- complete each task fully before moving to the next
- make small, focused changes
- **CRITICAL: every task MUST include new/updated tests** for code changes in that task
  - unit tests for the new `Stop` client method (success + error status mapping)
  - table-driven tests for `cancelCmd` covering success, declined, already-finished,
    `--yes`, missing/invalid args, not-found, and auth error paths
  - tests cover both success and error scenarios
- **CRITICAL: all tests must pass before starting next task** — no exceptions
- run `make test` after each change; run `make lint` before declaring done
- maintain backward compatibility (purely additive command)

## Testing Strategy
- **unit tests**: required for every task (see above). Client test uses an
  `httptest.Server` like the existing `client_test.go`; command test uses the
  moq `jenkinsClientMock` + an injected fake `prompter`, like `cmd_auth_test.go`.
- **e2e tests**: none — this project has no UI/e2e harness. Manual verification
  against a live Jenkins is listed under Post-Completion.

## Progress Tracking
- mark completed items with `[x]` immediately when done
- add newly discovered tasks with ➕ prefix
- document issues/blockers with ⚠️ prefix
- keep this plan in sync with actual work

## Solution Overview
- One new REST method `Client.Stop(ctx, buildURL)` performing an authenticated
  POST to `<buildURL>/stop`, treating any `< 400` response (Jenkins answers 200
  or a 302 redirect that Go's client follows) as success and routing `>= 400`
  through the existing `statusError` sentinel mapper.
- One new command `cancelCmd`: resolve job → compute build URL → read
  `BuildStatus` (guards not-found and "already finished") → confirm (unless
  `--yes`) → `Stop`. Output and exit codes follow the established contract.

## Technical Details
- **Endpoint**: `POST <buildURL>/stop`. `buildURL` is
  `buildURLFor(prof, job, number)` = `<base><job.Path>/<number>/`. Jenkins
  `/stop` returns `302 Found` (redirect back to the build page); Go's default
  `http.Client` (used in production) follows it, so `Stop` normally observes a
  final `200` — it will only ever see the raw `302` if a non-following client is
  injected. `Stop` accepts `200` **and** `302` explicitly (matching the strict
  exact-status style of `Build`/`getJSONURL` rather than a `< 400` range) and
  maps every other status via `statusError` (401→ErrAuth, 403→ErrPermission,
  404→ErrNotFound, else wrapped snippet). The doc comment must state that `200`
  is the status seen in production and `302` is the defensive fallback.
- **Command signature**: `jcli cancel <job> <number>` (both positional,
  required). `--yes`/`-y` skips the confirmation prompt.
- **Confirmation**: `app.prompter().promptLine("cancel build #<n> of <job>? [y/N] ")`;
  accept `y`/`yes` (case-insensitive) — anything else prints `aborted` and
  returns nil (exit 0). Injected via `promptFactory` in tests.
- **Pre-check flow** in `runCancel`:
  1. `clientFor` → profile + client (auth error → exit 2).
  2. `cache.Load` + `resolveJob` (missing job → exit 3 with suggestions).
  3. `client.BuildStatus(buildURLFor(...))` (missing build → exit 3).
  4. if `!b.Building`: print `build #<n> of <job> is not running (<result>)` and
     return nil (exit 0) — no prompt, no POST.
  5. confirm unless `--yes`; on decline print `aborted`, return nil.
  6. `client.Stop(buildURL)`; on success print `canceled build #<n> of <job>`.
- **Exit codes**: success/declined/already-finished → 0; missing-or-invalid args
  → 1 (usage); auth (401/`ErrAuth`) → 2; unknown job or build (404/`ErrNotFound`)
  → 3. No new exit code, and **`exitCode()` must not be modified**. Note the
  permission case: a Jenkins Job/Cancel-permission denial returns 403 →
  `jenkins.ErrPermission`, which the existing contract maps to exit **1**
  (usage), the same as every other command (verified in
  `cli_test.go` `TestExitCode`). This is a likely path for cancel (abort
  permission is distinct from build/read), so `Stop` should surface a clear
  stderr message, but the exit code stays 1 by contract — do not remap it.
- **`--json` is intentionally not supported** for `cancel`, consistent with
  `build` (both are action commands with human-only output); the omission is a
  decision, not an oversight.

## What Goes Where
- **Implementation Steps** (`[ ]`): client method, interface + mock, command,
  registration, docs — all in-repo.
- **Post-Completion** (no checkboxes): manual stop against a live Jenkins run.

## Implementation Steps

### Task 1: Add `Stop` REST method to the Jenkins client

**Files:**
- Modify: `internal/jenkins/client.go`
- Modify: `internal/jenkins/client_test.go`

- [x] add `func (c *Client) Stop(ctx context.Context, buildURL string) error` that
      POSTs to `strings.TrimRight(buildURL, "/") + "/stop"` with basic auth, using
      the same request plumbing as `Build`
- [x] accept `resp.StatusCode == 200 || resp.StatusCode == 302` as success
      (matching `Build`'s exact-status style), route every other status through
      `statusError(resp)`; doc comment states 200 is the production status
      (default client follows the 302) and 302 is the defensive fallback, plus the
      401/403/404 sentinel mapping
- [x] write `client_test.go` cases: 200 success, 302→200 success (httptest handler
      returning `/stop` redirect), 404→`ErrNotFound`, 401→`ErrAuth`,
      403→`ErrPermission`, 500→wrapped error with snippet
- [x] run `make test` — must pass before task 2

### Task 2: Extend the `jenkinsClient` interface and regenerate the mock

**Files:**
- Modify: `internal/cli/cli.go`
- Modify: `internal/cli/jenkins_mock.go` (regenerated, do not hand-edit)

- [x] add `Stop(ctx context.Context, buildURL string) error` to the `jenkinsClient`
      interface in `cli.go`
- [x] delete `internal/cli/jenkins_mock.go`, then run
      `GOTOOLCHAIN=local go generate ./internal/cli/...` to regenerate it fresh
      (per CLAUDE.md: the stale mock's compile-time assertion blocks regeneration)
- [x] run `make test` — must pass (compiles with the enlarged interface) before task 3

### Task 3: Implement the `cancel` command handler

**Files:**
- Create: `internal/cli/cmd_cancel.go`
- Create: `internal/cli/cmd_cancel_test.go`

- [x] add `cancelCmd` struct (`app *app`, `Yes bool` with `short:"y" long:"yes"`)
      and its `Execute(args []string) error` that parses the two positionals
      (job name + build number) and returns `c.app.fail(c.runCancel(name, number))`
- [x] validate arity/number in `Execute` or `runCancel`: require exactly a job and a
      positive integer build number, reject `>2` positionals with a clear "too many
      arguments" usage error (mirroring `statusCmd.runStatus`), else a clear usage
      error (exit 1)
- [x] implement `runCancel`: `clientFor` → `cache.Load` → `resolveJob` →
      `buildURLFor` → `BuildStatus`; short-circuit with a "not running" line when the
      build is not `building`
- [x] add confirmation via `app.prompter().promptLine(...)` gated by `!c.Yes`;
      decline prints `aborted` and returns nil
- [x] call `client.Stop` and print `canceled build #<n> of <job>` on success
- [x] write table-driven `cmd_cancel_test.go` (mirror `cmd_status_test.go` setup;
      reuse the existing package-level `scriptedPrompter` from `cmd_auth_test.go`
      for the `[y/N]` line — no new prompter fake needed):
      success with `--yes`, success via `y` prompt, decline via `n` prompt,
      already-finished short-circuit, missing args, too-many args,
      non-numeric/zero number, unknown job (exit 3), build not found (exit 3),
      auth error (exit 2), permission-denied (exit 1), `Stop` returning an error
- [x] assert exit codes and stdout/stderr text in each case
- [x] **negative-call assertions** (prove the confirmation gate): on decline,
      assert `mock.StopCalls()` is empty; on already-finished, assert both
      `mock.StopCalls()` is empty and the prompter was never invoked; with
      `--yes`, assert the prompter was never invoked (a prompter that records or
      fails on call)
- [x] run `make test` — must pass before task 4

### Task 4: Register the command

**Files:**
- Modify: `internal/cli/commands.go`

- [x] add `{name: "cancel", short: "stop a running build", data: &cancelCmd{app: a}}`
      to the `commands()` slice (place it after `build`/`status` logically)
- [x] add a `long` description if the `--yes` semantics need spelling out
- [x] add/extend a test asserting `cancel` is registered and its `--help` renders
      (follow the existing command-registration test pattern in `cli_test.go`)
- [x] run `make test` — must pass before task 5

### Task 5: Documentation

**Files:**
- Modify: `README.md`
- Modify: `skill/jenkins-cli/SKILL.md`

- [x] add a `cancel` row to the command table in `README.md` and a short usage
      example (`jcli cancel my-job 42`, `--yes` note), keeping it in sync with `--help`
- [x] document `cancel` in `skill/jenkins-cli/SKILL.md` so the Claude skill can
      drive it (when to use, args, `--yes` for non-interactive use); verify
      `skill/embed_test.go` still passes
- [x] run `make test` — must pass before verification task

### Task 6: Verify acceptance criteria
- [x] verify `jcli cancel <job> <number>` stops a running build (covered by cmd_cancel_test.go `TestCancel_Success` + client_test.go `TestClient_Stop`; live check in Post-Completion)
- [x] verify already-finished build prints "not running" and exits 0 (covered by cmd_cancel_test.go `TestCancel_AlreadyFinished`; live check in Post-Completion)
- [x] verify decline prints `aborted` and does not POST (covered by cmd_cancel_test.go `TestCancel_Decline`, asserts `StopCalls()` empty; live check in Post-Completion)
- [x] verify unknown job → exit 3, auth failure → exit 2, bad args → exit 1 (covered by cmd_cancel_test.go `TestCancel_Errors` + `TestCancel_ArgValidation`)
- [x] run full suite: `make test` — all packages ok
- [x] run `make lint` (`GOTOOLCHAIN=local golangci-lint run`) — clean (0 issues)
- [x] run `make cross-build` — linux build still green (no darwin-only additions)

### Task 7: [Final] Finalize docs and archive plan
- [x] confirm `README.md` command table matches `jcli --help`
- [x] update `CLAUDE.md` only if a new pattern was discovered (likely none —
      the command reuses existing patterns)
- [x] move this plan to `docs/plans/completed/`

## Post-Completion
*Items requiring manual intervention or external systems — no checkboxes, informational only*

**Manual verification** (cannot be exercised headlessly — needs a live Jenkins):
- Trigger a long-running build (`jcli build <job>`), then in another shell run
  `jcli cancel <job> <number>` and confirm the run aborts in the Jenkins UI and
  `jcli status <job> <number>` shows `ABORTED`.
- Confirm the `[y/N]` prompt appears without `--yes` and that answering `n`
  leaves the build running.
- Confirm `jcli cancel <job> <finished-number>` reports "not running" and exits 0.
- Confirm behaviour against a freestyle (non-pipeline) job as well as a pipeline
  job, since `/stop` applies to both.

**External system updates** (if applicable):
- None. Purely additive CLI command; no consumers, config, or deployment changes.
