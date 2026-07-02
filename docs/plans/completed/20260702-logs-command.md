# `jcli logs` — console output command + `--logs` on build/status

## Overview

Add console-log access to jcli in three places:

- **`jcli logs <job> [<number>]`** — print a build's Jenkins console output.
  `logs <job>` targets the job's latest build; `logs <job> <number>` a specific
  build. No positional is a usage error (logs need a build target). Without
  `--wait` it dumps the full console once; with `--wait` it streams the console
  progressively until the build finishes.
- **`jcli build <job> --logs`** — stream the triggered build's console while it
  runs. `--logs` implies `--wait` (a fire-and-forget build has no build URL yet),
  and suppresses the stage-view lines (the console already shows everything). The
  terminal result and exit code are unchanged (exit 4 on non-success).
- **`jcli status <job> <number> --logs`** — show that build's console instead of
  the stage snapshot. `--logs` on `status` is valid **only** at the build-id
  level (a job name *and* number); using it with `status <job>` or no args is a
  usage error. Without `--wait` it dumps once; with `--wait` it follows live.
  (Intentional asymmetry: `status <job> --logs` is rejected — use `logs <job>`
  for the latest build's console. The `--help`/docs must convey this.)

**Problem it solves:** today jcli can trigger, wait on, and report stage status,
but there is no way to read a build's actual console output — the thing you need
when a build fails. This closes that gap and reuses the `status`/`build --wait`
machinery (job resolution, `buildURLFor`, `sleepPoll`, the `--wait` timeout).

## Context (from discovery)

- **Jenkins console endpoints:**
  - `<buildURL>/consoleText` — full plain-text console (one shot).
  - `<buildURL>/logText/progressiveText?start=<n>` — the incremental chunk from
    byte offset `n`; response headers `X-Text-Size` (next offset) and
    `X-More-Data` (`true` while the build is still producing output). Polling this
    with the returned offset is how you follow a live build; for a finished build
    it returns the remainder once with `X-More-Data` absent/false.
- **Client (`internal/jenkins/client.go`):** all current reads are JSON via
  `getJSON`/`getJSONURL`; there is **no plain-text GET helper yet**. `statusError`
  maps 401/403/404 to `ErrAuth`/`ErrPermission`/`ErrNotFound`. Build/queue URLs
  are handled as absolute URLs.
- **Consumer interface + mock:** `jenkinsClient` in `internal/cli/cli.go`, mock
  regenerated via `go generate ./internal/cli/` (moq v0.5.3, `GOTOOLCHAIN=local`).
- **Reusable CLI machinery (added with `status`):**
  - `(*app).resolveJob(client, m, prof, name)` — cache lookup + one crawl-retry.
  - `buildURLFor(prof, job, number)` — absolute build URL from job path + number.
  - `client.LastBuild(ctx, jobPath) (Build, bool, error)` — latest build (ok=false
    when never built); `Build` carries `Number`, `URL`, `Building`, `Result`.
  - `(*app).sleepPoll`, `pollEvery`, `waitEvery` — the `--wait` poll cadence/timeout.
  - `buildCmd.waitForBuild` (`internal/cli/cmd_build.go`) — phase 1 queue→buildURL,
    phase 2 poll `BuildResult` + `logStages` to terminal; `reportResult` maps
    SUCCESS→0 / else→`errBuildFailed` (exit 4).
  - `statusCmd.buildByNumber` / `showBuild` (`internal/cli/cmd_status.go`).
- **Exit codes** (`exitCode`): 0 ok, 1 usage, 2 auth, 3 not-found, 4 build-failed.
  `logs` and `status --logs` are informational → exit 0 regardless of build
  result; `build --logs` keeps build's exit-4-on-failure semantics. Missing
  build → exit 3, auth → exit 2.
- **Output streams:** console text goes to **stdout** (raw passthrough); the
  "build started"/diagnostic lines go to stderr. `--json` does not apply to raw
  console output — it is ignored when `--logs` (or the `logs` command) is active.

## Development Approach

- **testing approach: Regular** (code first, then table-driven `testify` + moq,
  matching `cmd_build_test.go` / `cmd_status_test.go` / `client_test.go`).
- Complete each task fully before the next; small, focused changes.
- **Every task includes new/updated tests** (success + error/edge). All tests
  pass before the next task.
- Run `make test` (`go test -race ./...`) after each change; keep
  `golangci-lint run`, `gofmt -s`, `goimports` clean; `GOTOOLCHAIN=local`, max
  140-char lines, lowercase in-code comments.
- Never hand-edit `jenkins_mock.go` — regenerate via `go generate ./internal/cli/`.
- Maintain backward compatibility: `build`/`status` behavior is unchanged unless
  `--logs` is passed.

## Testing Strategy

- **unit tests: required per task.**
  - `internal/jenkins`: `httptest.Server` fixtures asserting the request path
    (`/consoleText`, `/logText/progressiveText`) and `start` query, returning body
    + `X-Text-Size`/`X-More-Data` headers; assert chunk parsing, the
    header-absent fallback, and 404→`ErrNotFound`.
  - `internal/cli`: table-driven tests with `jenkinsClientMock`, asserting stdout
    console text, dump vs follow (progressive sequence with tiny `pollInterval`),
    `build --logs` implies `--wait` + still exits by result, `status --logs`
    validation (job-id level only), and exit codes.
- **e2e tests:** none (CLI, no UI harness).

## Progress Tracking

- Mark completed items `[x]` immediately when done.
- Add newly discovered tasks with ➕ prefix; blockers with ⚠️ prefix.
- Update this plan if scope changes during implementation.

## Solution Overview

A single console-streaming primitive backs all three surfaces:

```
one-shot:  client.ConsoleText(buildURL)            → print full text once
follow:    loop client.ConsoleProgressive(buildURL, start) → print chunk;
           start = chunk.Size; stop when !chunk.More; sleepPoll between polls
```

- **`logs` command** resolves the build URL (LastBuild for job-only,
  `buildURLFor` for job+number) then calls `dumpConsole` (no `--wait`) or
  `followConsole` (`--wait`).
- **`build --logs`** sets `Wait=true`, and in `waitForBuild` phase 2 streams a
  console chunk each poll (in place of stage lines), then after the build reaches
  terminal **drains the tail** — a loop that keeps fetching while `More==true`
  but **paces each iteration with `sleepPoll(ctx)`** (still bounded by
  `waitEvery`) so it never busy-spins — then reports the result (unchanged exit
  code).
- **`status <job> <number> --logs`** delegates to the same `dumpConsole`/
  `followConsole` helpers instead of rendering the stage snapshot.

Key design decisions:

- **Shared helpers on `*app`** (`dumpConsole`, `followConsole`, and the
  lower-level `streamConsoleChunk`) so `logs`, `build`, and `status` reuse one
  implementation — mirrors how `sleepPoll`/`resolveJob` were lifted for `status`.
- **`--logs` implies `--wait` on build** (chosen): a fire-and-forget build has no
  build URL, so `--logs` alone would have nothing to stream.
- **`--logs` suppresses stage output** on build/status (the console already
  contains stage progress) — no double reporting.
- **Raw passthrough to stdout, `--json` ignored** for console output (it is not
  structured data).
- **Progressive follow naturally terminates** for an already-finished build
  (single chunk, `More=false`), so `logs <job> --wait` on a completed build dumps
  once and exits — no special "already terminal" branch needed.
- **Informational exit for `logs`/`status --logs`** (exit 0); `build --logs`
  keeps exit 4 on a failed build.

## Technical Details

### New client type + methods (`internal/jenkins`)

```go
// ConsoleChunk is one progressive slice of a build's console output. Size is the next start
// offset to request; More is true while the build is still producing output.
type ConsoleChunk struct {
    Text string
    Size int64
    More bool
}

// getText performs an authenticated GET against an absolute endpoint and returns the body as a
// string plus the response headers, mapping status codes via the SAME statusError as the JSON
// path. Unlike getJSONURL it sets NO `Accept: application/json` header (console endpoints return
// text/plain) and reads the full body into a string.
func (c *Client) getText(ctx, endpoint string, query url.Values) (string, http.Header, error)

// ConsoleText returns a build's full console output (<buildURL>/consoleText).
func (c *Client) ConsoleText(ctx, buildURL string) (string, error)

// ConsoleProgressive returns the console chunk from byte offset start
// (<buildURL>/logText/progressiveText?start=N), parsing X-Text-Size / X-More-Data. When the
// size header is absent it falls back to start+len(text) so the caller still advances.
func (c *Client) ConsoleProgressive(ctx, buildURL string, start int64) (ConsoleChunk, error)
```

### Interface additions (`internal/cli/cli.go`)

```go
ConsoleText(ctx context.Context, buildURL string) (string, error)
ConsoleProgressive(ctx context.Context, buildURL string, start int64) (jenkins.ConsoleChunk, error)
```

### Shared console helpers (`internal/cli/cmd_logs.go`, on `*app`)

```go
// dumpConsole prints a build's full console once, under a bounded context (WithTimeout on
// jenkins.defaultTimeout-equivalent) so a large/slow console body cannot hang indefinitely.
func (a *app) dumpConsole(client jenkinsClient, buildURL string) error

// followConsole streams a build's console to stdout until Jenkins reports no more data, bounded
// by waitEvery and paced by sleepPoll.
func (a *app) followConsole(client jenkinsClient, buildURL string) error

// streamConsoleChunk fetches the console since start, prints it, and returns the next offset and
// whether more output is expected. Reused by followConsole and build --logs' wait loop.
func (a *app) streamConsoleChunk(ctx context.Context, client jenkinsClient, buildURL string, start int64) (int64, bool, error)
```

### Flag additions

- `buildCmd`: `Logs bool \`long:"logs" description:"stream the build's console output (implies --wait)"\``
- `statusCmd`: `Logs bool \`long:"logs" description:"show the build's console output (requires a job and build number)"\``
- `logsCmd`: `app *app; Wait bool \`long:"wait" description:"follow the console output until the build finishes"\``

### Processing flow

- `runLogs(args)`: 0 args → usage error; >2 → usage error; 1 → resolve job +
  `LastBuild` (never-built → error) → dump/follow its `URL`; 2 → parse number
  (invalid → usage error) → `buildURLFor` → dump/follow.
- `runBuild`: `if c.Logs { c.Wait = true }`; phase-2 loop streams console when
  `Logs`, else `logStages`; drains the console tail before `reportResult`.
- `runStatus`: `if c.Logs && len(args) != 2` → usage error; when `args==2 &&
  Logs`, resolve build URL and dump/follow instead of the stage render.

## What Goes Where

- **Implementation Steps** (`[ ]`): client methods, interface/mock, logs command
  + shared helpers, build `--logs`, status `--logs`, verify, docs.
- **Post-Completion** (no checkboxes): live-Jenkins smoke checks (follow a real
  running build's console, dump a finished build, freestyle vs pipeline).

## Implementation Steps

### Task 1: Add `jenkins` console type and client methods

**Files:**
- Modify: `internal/jenkins/types.go`
- Modify: `internal/jenkins/client.go`
- Modify: `internal/jenkins/client_test.go`

- [x] add `ConsoleChunk` to `types.go` with a doc comment
- [x] add the `getText` plain-text GET helper to `client.go` (absolute endpoint,
      optional query, `statusError` mapping, reads full body)
- [x] add `ConsoleText(ctx, buildURL)` hitting `<buildURL>/consoleText`
- [x] add `ConsoleProgressive(ctx, buildURL, start)` hitting
      `<buildURL>/logText/progressiveText?start=N`, parsing `X-Text-Size` /
      `X-More-Data` with the size-absent fallback
- [x] write `client_test.go` cases: full console dump, progressive chunk with
      headers, `More=true`→`false` sequence, header-absent fallback, and status
      mapping (404→`ErrNotFound`, 401→`ErrAuth`) — assert no JSON `Accept` header
      is sent
- [x] run `make test` + `make cross-build` — must pass before Task 2

### Task 2: Extend `jenkinsClient` interface and regenerate the mock

**Files:**
- Modify: `internal/cli/cli.go`
- Modify: `internal/cli/jenkins_mock.go` (generated — do not hand-edit)

- [x] add `ConsoleText` and `ConsoleProgressive` to the `jenkinsClient` interface
- [x] regenerate: `go generate ./internal/cli/` (remove the stale mock first if
      moq's type-check blocks on it, as during the status work)
- [x] confirm existing `internal/cli` tests compile/pass against the wider mock
- [x] run `make test` — must pass before Task 3

### Task 3: `logs` command + shared console helpers

**Files:**
- Create: `internal/cli/cmd_logs.go`
- Create: `internal/cli/cmd_logs_test.go`
- Modify: `internal/cli/commands.go`

- [x] add `dumpConsole`, `followConsole`, `streamConsoleChunk` on `*app` in
      `cmd_logs.go` (raw text to stdout; follow bounded by `waitEvery`/`sleepPoll`)
- [x] add `logsCmd` struct (`app`, `Wait bool`) with `Execute` → `runLogs(args)`
- [x] implement `runLogs`: arg validation (0 → missing job, >2 → too many),
      job-only → `resolveJob` + `LastBuild` (never-built error), job+number →
      `buildURLFor`; dump when not `--wait`, follow when `--wait`
- [x] register `{name: "logs", ...}` in `commands()`
- [x] write tests: dump (job-only latest, job+number), follow sequence (tiny
      `pollInterval`), never-built error, missing-build not-found, arg validation,
      and `--json --logs` still emits raw console (no JSON wrapping)
- [x] run `make test` — must pass before Task 4

### Task 4: `--logs` on `build`

**Files:**
- Modify: `internal/cli/commands.go`
- Modify: `internal/cli/cmd_build.go`
- Modify: `internal/cli/cmd_build_test.go`

- [x] add `Logs bool \`long:"logs"\`` to `buildCmd`
- [x] in `runBuild`, set `c.Wait = true` when `c.Logs`
- [x] in `waitForBuild` phase 2, stream a console chunk per poll via
      `streamConsoleChunk` when `Logs` (skip `logStages`), tracking the offset;
      drain the tail after terminal, then `reportResult` (unchanged exit code)
- [x] write tests: `build --logs` implies `--wait`, streams console to stdout,
      suppresses stage lines, emits the tail chunk after the terminal
      `BuildResult`, and still returns exit 4 on a FAILURE result
- [x] run `make test` — must pass before Task 5

### Task 5: `--logs` on `status` (build-id level only)

**Files:**
- Modify: `internal/cli/commands.go`
- Modify: `internal/cli/cmd_status.go`
- Modify: `internal/cli/cmd_status_test.go`

- [x] add `Logs bool \`long:"logs"\`` to `statusCmd`
- [x] in `runStatus`, reject `--logs` unless exactly two positionals (job +
      number) with a clear usage error
- [x] when `args==2 && Logs`, resolve `buildURLFor` and dump/follow the console
      (via the shared helpers) instead of rendering the stage snapshot
- [x] write tests: `status <job> <number> --logs` dump + `--wait` follow;
      `--logs` with job-only or no args → usage error; console goes to stdout
- [x] run `make test` — must pass before Task 6

### Task 6: Verify acceptance criteria

- [x] verify all three surfaces from Overview behave as specified
- [x] verify edge cases: never-built job, missing build number (exit 3), auth
      (exit 2), `logs` no-arg / too many args, `status --logs` at wrong level,
      `build --logs` exit-4-on-failure, follow timeout bounded by `waitEvery`
- [x] run full suite: `make test`
- [x] run `make lint` and `make cross-build` — must be clean (no new findings)

### Task 7: [Final] Update documentation

**Files:**
- Modify: `README.md`
- Modify: `skill/jenkins-cli/SKILL.md`
- Modify: `CLAUDE.md` (only if a new pattern emerged)

- [x] document `logs` and the `--logs` flags (all three surfaces) in `README.md`,
      keeping it in sync with `--help`; add to the commands table
- [x] add `logs` / `--logs` usage to `skill/jenkins-cli/SKILL.md`
- [x] update `CLAUDE.md` only if warranted
- [x] move this plan to `docs/plans/completed/`

## Review (implementation notes)

- Implemented exactly as planned; all review refinements folded in (paced
  tail-drain, `getText` sans JSON `Accept` + 401 test, bounded `dumpConsole`,
  `--json`-ignored-with-`--logs` test, asymmetry note).
- Full suite (`go test -race ./...`), `go vet`, and `make cross-build` pass;
  changed files are `golangci-lint`-clean (only the 9 pre-existing repo findings
  remain, none in new code). Smoke-tested `logs`/`--logs` help + validation paths.

## Post-Completion

*Manual verification against a live Jenkins — cannot run headlessly:*

- `jcli logs <job>` on a finished build → full console dumps once.
- `jcli logs <job> --wait` / `jcli build <job> --logs` on a running build →
  console streams live and stops when the build finishes; `build --logs` exits by
  result (0 / 4).
- `jcli status <job> <number> --logs` → console for that build; `--logs` with
  `status <job>` or no args → usage error.
- Confirm a freestyle (non-pipeline) build's console streams the same way (console
  endpoints are not pipeline-specific).
