# `jcli status` — job / build status command

## Overview

Add a new `status` subcommand that reports Jenkins run state, with three shapes
driven by its positional args:

- `jcli status` (no args) — a short list of only the **currently running** builds
  across all nodes; if none, print a plain "no jobs currently running".
- `jcli status <job>` — resolve the job, report whether its latest build is
  **running or not**, and when running show the running build's **stage info**
  (pipeline stages + overall build state).
- `jcli status <job> <number>` — show the **stage info** for that specific build
  number of the job.

`--wait` (valid only with a job/build target) follows the stage info, polling to
terminal state and re-rendering on change — reusing the exact poll/stage
machinery that `build --wait` already uses.

**Problem it solves:** today jcli can trigger and wait on a *freshly triggered*
build, but has no way to inspect an already-running build, check "is this job
running right now?", or get a fleet-wide "what's running" snapshot without the
Jenkins web UI.

**Integration:** it slots in beside `get`/`build` in the `cli` package, adds a
small read-only surface to the `jenkins` client, and reuses the existing
`StageView`, `humanizeDuration`, `stageGlyphs`, and `--wait` poll helpers.

## Context (from discovery)

- **Command wiring:** `internal/cli/commands.go` (`commands()` slice + one
  `xxxCmd` struct per command implementing `flags.Commander` via `Execute`).
  Dispatch/exit-code mapping in `internal/cli/cli.go`.
- **Client surface consumed by cli:** the `jenkinsClient` interface in
  `internal/cli/cli.go` (moq-generated `internal/cli/jenkins_mock.go` via the
  `go:generate` on line 26). The concrete client is `internal/jenkins/client.go`
  with wire types in `internal/jenkins/types.go`.
- **Reusable poll/stage machinery (from `build --wait`, `internal/cli/cmd_build.go`):**
  - `humanizeDuration(ms int64) string` — package-level, reusable as-is.
  - `stageGlyphs` map — package-level, reusable as-is.
  - `sleepPoll`, `pollEvery`, `waitEvery` — poll-interval/timeout helpers
    (`pollEvery`/`waitEvery` are on `*app`; `sleepPoll` is on `*buildCmd` but
    uses only `app` fields → will be lifted to `*app` for sharing).
  - `client.StageView(ctx, buildURL)` — already in the `jenkinsClient` interface;
    404 (freestyle/non-pipeline) surfaces as `jenkins.ErrNotFound` and is
    swallowed as "no stage data".
- **Existing client methods relevant here:** `Jobs`, `JobParams`,
  `BuildResult(ctx, buildURL)` (Building+Result only — no number/timestamp),
  `StageView`. Build/queue URLs are handled as absolute URLs
  (`getJSONURL`); base-relative reads use `getJSON`.
- **Job resolution pattern:** `resolveJob` (cmd_build.go) / `runGet`
  (cmd_get.go) — cache `Lookup`, one crawl-then-retry on miss, `suggestNames`
  on a still-missing job → `jenkins.ErrNotFound` (exit 3). `cache.Job.Path` is
  the job's Jenkins path (e.g. `/job/Folder/job/Child`); `prof.URL` is the base.
- **Exit codes** (`exitCode` in cli.go): 0 ok, 1 usage, 2 auth, 3 not-found,
  4 build-failed. `status` is informational → never returns exit 4; a failed
  build is reported as normal output with exit 0.
- **Output:** every command honors global `--json`; human output goes to
  `stdout`, diagnostics to `stderr` via `verbosef`.
- **Cross-build:** `internal/jenkins` is pure Go (no cgo/build tags) — new
  methods need no `_darwin`/`_other` split; `make cross-build` stays green.

## Development Approach

- **testing approach: Regular** (code first, then table-driven `testify` tests +
  moq mock — matches `cmd_build_test.go` / `cmd_get_test.go` / `client_test.go`).
- Complete each task fully before the next; small, focused changes.
- **Every task includes new/updated tests** (success + error/edge cases); all
  tests pass before starting the next task.
- Run `make test` (`go test -race ./...`) after each change; keep
  `golangci-lint run`, `gofmt -s`, `goimports` clean; honor `GOTOOLCHAIN=local`
  and max 140-char lines / lowercase in-code comments.
- Never hand-edit `jenkins_mock.go` — regenerate via `go generate ./internal/cli/`.
- Maintain backward compatibility: existing commands and `BuildResult` untouched.

## Testing Strategy

- **unit tests: required per task.**
  - `internal/jenkins`: `httptest.Server` fixtures asserting request path +
    query `tree`, decoding into the new types, and status→sentinel mapping
    (404→`ErrNotFound`), mirroring existing `client_test.go` tests.
  - `internal/cli`: table-driven tests with the moq `JenkinsClientMock`, asserting
    rendered stdout, JSON output shape, exit codes, and `--wait` transitions
    driven by a stubbed sequence of `BuildStatus`/`StageView` returns and an
    injected clock + tiny `pollInterval` (no wall-clock sleeps).
- **e2e tests:** none — this is a CLI with no UI-based e2e harness.

## Progress Tracking

- Mark completed items `[x]` immediately when done.
- Add newly discovered tasks with ➕ prefix; blockers with ⚠️ prefix.
- Update this plan if scope changes during implementation.

## Solution Overview

Three input shapes funnel into two render paths:

```
args=0            → RunningBuilds()            → render running list (or "none")
args=1 (job)      → resolve job → LastBuild()  → running? render build+stages : "not running"
args=2 (job,num)  → resolve job → BuildStatus(buildURLFor(job,num)) → render build+stages
```

- **Running list** uses a single `/computer/api/json` call (all executors across
  all nodes) — efficient, cache-independent, sees exactly what is executing now.
- **Job / build render** = overall build line (`#N STATE (elapsed …)`) + the
  pipeline stage snapshot via `StageView`; a `StageView` 404 (freestyle) is
  swallowed so only the build line shows.
- **`--wait`** reuses `sleepPoll`/`pollEvery`/`waitEvery`: while the target is
  building, poll `BuildStatus`+`StageView`, emit stage **transition lines to
  stderr** (same streaming model as `build --wait`), and on terminal
  (`Building==false`) print the final build+stage **snapshot to stdout**, exit 0.
  - `--wait` with **no target** (args=0) is a usage error (exit 1).
  - `--wait <job> <number>` (or `<job>`) whose target is **already terminal** is
    NOT an error: render the final snapshot once and exit 0.
  - with `--json`, `--wait` suppresses intermediate output and emits a **single**
    JSON document at terminal state (never concatenated JSON).
  - `--wait <job>` whose last build is not building and never starts is bounded by
    `waitEvery` (no infinite block).

Key design decisions:

- **Build addressing by two positionals** (`<job> <number>`), not `name#number`
  or a URL — one job-path resolution path, build URL computed as
  `TrimRight(prof.URL,"/") + job.Path + "/" + number + "/"`.
- **Informational exit semantics:** `status` returns exit 0 regardless of a
  build's SUCCESS/FAILURE result (it queries, it does not build). Not-found
  (missing job or build number → 404) → exit 3; auth → 2. Note: `/computer`
  requires Overall/Read — a 403 maps to `jenkins.ErrPermission`, which
  `exitCode`'s default branch currently renders as exit 1 (not 2); acceptable,
  but the no-arg error message should stay clear.
- **Running list shows only *executing* builds**, not queued-but-not-started
  items (`/computer` reports executors, not the queue) — matches "currently
  running"; state this in help/README so it isn't read as "everything pending".
- **Injected clock** (`app.now func() time.Time`): the `clock()` helper falls back
  to `time.Now` when the field is nil (mirrors `pollEvery`/`prompter` — **no**
  wiring in `newApp`), so "elapsed" is deterministic in tests.
- **`LastBuild` returns `(Build, bool, error)`** — the bool distinguishes a job
  that has never built from an error.

## Technical Details

### New wire types (`internal/jenkins/types.go`)

```go
// Build is a build's status detail read from <buildURL>/api/json or a job's lastBuild.
type Build struct {
    Number    int    `json:"number"`
    URL       string `json:"url"`
    Building  bool   `json:"building"`
    Result    string `json:"result"`
    Timestamp int64  `json:"timestamp"` // build start, epoch millis
}

// jobLastBuild is the shape of GET <job>/api/json?tree=lastBuild[...]; LastBuild is
// nil for a job that has never run.
type jobLastBuild struct {
    LastBuild *Build `json:"lastBuild"`
}

// RunningBuild is one currently-executing build reported by /computer.
// Name is fullDisplayName, which ALREADY includes the build number
// (e.g. "Folder » MyJob #42") — render it verbatim, do not also print Number.
type RunningBuild struct {
    Name      string `json:"fullDisplayName"`
    Number    int    `json:"number"`
    URL       string `json:"url"`
    Timestamp int64  `json:"timestamp"`
}

// computerResponse / executor shapes for /computer/api/json currentExecutable.
```

### New client methods (`internal/jenkins/client.go`)

```go
// LastBuild reads a job's most recent build summary; ok is false when the job has
// never built. tree=lastBuild[number,url,building,result,timestamp].
func (c *Client) LastBuild(ctx, jobPath) (Build, bool, error)

// BuildStatus reads a single build's status by absolute URL.
// tree=number,url,building,result,timestamp.
func (c *Client) BuildStatus(ctx, buildURL) (Build, error)

// RunningBuilds lists all currently-executing builds across every node via
// /computer/api/json (executors + oneOffExecutors currentExecutable), skipping
// idle (null) executors and deduping by URL.
func (c *Client) RunningBuilds(ctx) ([]RunningBuild, error)
```

### `jenkinsClient` interface additions (`internal/cli/cli.go`)

```go
LastBuild(ctx context.Context, jobPath string) (jenkins.Build, bool, error)
BuildStatus(ctx context.Context, buildURL string) (jenkins.Build, error)
RunningBuilds(ctx context.Context) ([]jenkins.RunningBuild, error)
```

### Processing flow (`internal/cli/cmd_status.go`)

- `runStatus(args)` dispatches on `len(args)` (0/1/2; >2 = usage error).
- `--wait` requires a target and a building state; else usage error.
- `buildURLFor(prof, job, number)` computes the absolute build URL.
- `renderBuild(w, name, b, stages)` prints the `#N STATE (elapsed …)` line
  (elapsed = `app.now() - b.Timestamp`, via `humanizeDuration`) then one line per
  stage using `stageGlyphs`/`humanizeDuration`.
- `--json` emits a struct `{job, build:{number,url,building,result,timestamp}, stages:[…]}`
  for the target case, and `[{name,number,url,elapsed_ms}]` for the running-list case.

## What Goes Where

- **Implementation Steps** (`[ ]`): client types+methods, interface+mock,
  command registration+parsing, command body+render+wait, tests, README/skill.
- **Post-Completion** (no checkboxes): manual live-Jenkins smoke checks
  (real running build, freestyle fallback, `--wait` follow) that can't run
  headlessly.

## Implementation Steps

### Task 1: Add `jenkins` client status types and methods

**Files:**
- Modify: `internal/jenkins/types.go`
- Modify: `internal/jenkins/client.go`
- Modify: `internal/jenkins/client_test.go`

- [ ] add `Build`, `jobLastBuild`, `RunningBuild`, and the `/computer` executor
      response types to `types.go` with doc comments
- [ ] add `LastBuild(ctx, jobPath) (Build, bool, error)` (tree
      `lastBuild[number,url,building,result,timestamp]`; `ok=false` when
      `lastBuild` is nil) to `client.go`
- [ ] add `BuildStatus(ctx, buildURL) (Build, error)` (absolute URL via
      `getJSONURL`; tree `number,url,building,result,timestamp`)
- [ ] add `RunningBuilds(ctx) ([]RunningBuild, error)` hitting
      `/computer/api/json` with the executors tree, skipping null
      `currentExecutable`, deduping by URL and preferring the `oneOffExecutors`
      (flyweight) record over a node-`executors` placeholder for the same URL
- [ ] write `client_test.go` cases for each (assert path + `tree` query,
      decode fixture, `ok` false path for never-built, 404→`ErrNotFound`,
      idle-executor skip + dedupe)
- [ ] run `make test` + `make cross-build` — must pass before Task 2

### Task 2: Extend `jenkinsClient` interface and regenerate the mock

**Files:**
- Modify: `internal/cli/cli.go`
- Modify: `internal/cli/jenkins_mock.go` (generated — do not hand-edit)

- [ ] add `LastBuild`, `BuildStatus`, `RunningBuilds` to the `jenkinsClient`
      interface in `cli.go`
- [ ] regenerate the mock: `go generate ./internal/cli/`
- [ ] confirm existing `internal/cli` tests still compile/pass (mock now
      satisfies the wider interface)
- [ ] run `make test` — must pass before Task 3

### Task 3: Register `status`, arg dispatch skeleton, and shared helpers

Creates a compiling `status` command whose `runStatus` performs only arg-count
and `--wait` validation (the render/poll bodies land in Task 4), plus the shared
`clock`/`sleepPoll` refactor. This keeps the tree compiling and Task 3's tests
runnable on their own.

**Files:**
- Create: `internal/cli/cmd_status.go`
- Create: `internal/cli/cmd_status_test.go`
- Modify: `internal/cli/commands.go`
- Modify: `internal/cli/cli.go`
- Modify: `internal/cli/cmd_build.go`

- [ ] add `statusCmd` struct (`app *app`, `Wait bool \`long:"wait" …\``) with
      `Execute(args []string)` calling `c.app.fail(c.runStatus(args))`
- [ ] register `{name: "status", short: "…", data: &statusCmd{app: a}}` in
      `commands()`
- [ ] create `cmd_status.go` with a `runStatus(args)` skeleton: validate arg
      count (>2 → usage error) and reject `--wait` with no target (args=0 → usage
      error); dispatch stubs for the 0/1/2-arg cases (filled in Task 4)
- [ ] add `now func() time.Time` field to `app` + a `clock()` helper that falls
      back to `time.Now` when nil (mirrors `pollEvery`/`prompter`; no `newApp`
      wiring)
- [ ] lift `sleepPoll` from `*buildCmd` to `*app`, update the `cmd_build.go`
      caller to `c.app.sleepPoll(ctx)` (shared by build + status; no behavior
      change; check `cmd_build_test.go` for any direct reference)
- [ ] write tests: command registered + dispatches; arg-count validation (>2 →
      exit 1); `--wait` without a target → exit 1; `sleepPoll` still honored by
      the build path (existing build tests stay green)
- [ ] run `make test` — must pass before Task 4

### Task 4: Implement `runStatus` bodies, rendering, and `--wait`

**Files:**
- Modify: `internal/cli/cmd_status.go`
- Modify: `internal/cli/cmd_status_test.go`

- [ ] implement running-list case (args=0): `RunningBuilds()` → sorted lines
      `<fullDisplayName>  <elapsed>` (name already carries `#number`; elapsed via
      `app.clock()` + `humanizeDuration`); empty → "no jobs currently running"
- [ ] implement job case (args=1): `resolveJob` + `LastBuild()`; if building →
      render build+stages; else → "<job> is not running (last build #N RESULT)"
      (or "never built")
- [ ] implement job+number case (args=2): `resolveJob` + `buildURLFor` +
      `BuildStatus()` + `StageView()` → render build+stages; 404 build →
      `jenkins.ErrNotFound` (exit 3)
- [ ] implement `renderBuild` (overall `#N STATE (elapsed …)` line + stage
      snapshot, `StageView` 404 swallowed) and `--json` output for both the
      target and running-list shapes
- [ ] implement `--wait`: for a specific/last building target, poll
      `BuildStatus`+`StageView` via `sleepPoll`/`waitEvery`, emit stage
      transition lines to stderr, print the final snapshot to stdout at terminal,
      exit 0; an **already-terminal** target renders once and exits 0 (not an
      error); with `--json` emit a single final JSON document only
- [ ] write tests: running list (some + none + JSON), job running/not-running/
      never-built, job+number success + not-found, freestyle stage-fallback,
      `--wait` transition sequence + already-terminal target + `--json` single-doc
      (injected clock + tiny `pollInterval`), exit-code assertions (informational
      → 0; not-found → 3; auth → 2)
- [ ] run `make test` — must pass before Task 5

### Task 5: Verify acceptance criteria

- [ ] verify all three input shapes from Overview behave as specified
- [ ] verify edge cases: no profile, cold cache (crawl-then-retry), freestyle
      job (no stages), never-built job, `--wait` timeout, `>2` args
- [ ] run full suite: `make test`
- [ ] run `make lint` and `make cross-build` — must be clean
- [ ] confirm no exit-4 leakage from `status` and existing `build`/`get` tests
      unaffected

### Task 6: [Final] Update documentation

**Files:**
- Modify: `README.md`
- Modify: `skill/jenkins-cli/SKILL.md`
- Modify: `CLAUDE.md` (only if a new pattern emerged, e.g. shared `sleepPoll`/clock)

- [ ] document `status` (all three shapes + `--wait`) in `README.md`, keeping it
      in sync with `--help`
- [ ] add `status` usage to `skill/jenkins-cli/SKILL.md`
- [ ] update `CLAUDE.md` only if warranted
- [ ] move this plan to `docs/plans/completed/`

## Post-Completion

*Manual verification against a live Jenkins — cannot run headlessly:*

- `jcli status` while at least one build runs → shows it with a growing elapsed;
  with nothing running → "no jobs currently running".
- `jcli status <pipeline-job>` mid-run → build line + live stage glyphs;
  `--wait` follows to terminal and re-renders on stage transitions.
- `jcli status <freestyle-job> <n>` → build line only (stage-view 404 swallowed).
- `jcli status <job> <bad-number>` → not-found (exit 3).
- Confirm `--json` shapes are stable for scripting.
