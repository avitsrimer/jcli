# history command — recent builds for a job

## Overview
- Add a `jcli history <job>` command that lists a job's most recent builds as a
  rich, aligned table: build number, result (`SUCCESS`/`FAILURE`/`ABORTED`/…, or
  `RUNNING` for an in-progress build), wall-clock duration, and a relative
  "time ago" column.
- Fills the gap between `status` (running-now / single-build stage view) and
  `get` (job params): there is currently no quick "what happened lately" view.
- Defaults to the last **10** builds; `--count`/`-n <N>` overrides. Honors the
  global `--json` flag by emitting an array of build documents.
- Integrates through the same pipeline every read command uses:
  `clientFor` → `cache.Load` → `resolveJob` → live Jenkins fetch → render.

## Context (from discovery)
- **Files/components involved:**
  - `internal/jenkins/client.go`, `internal/jenkins/types.go` — REST client;
    needs a new `Builds` method and a `Duration` field on `Build`.
  - `internal/cli/commands.go` — `historyCmd` struct + registration in
    `commands()`.
  - `internal/cli/cli.go` — the `jenkinsClient` consumer interface (declared
    here, not in `commands.go`) and the moq `//go:generate` directive.
  - `internal/cli/cmd_history.go` (new) — handler + rendering.
  - `internal/cli/jenkins_mock.go` — regenerated moq mock (delete-then-generate).
  - `internal/cli/cmd_status.go` — reuse `buildURLFor`, `buildResult`,
    `humanizeDuration`, `buildJSON`/`newBuildJSON`, `encodeJSON` patterns.
- **Related patterns found:**
  - Every command is a `*Cmd` struct with an `Execute(args []string) error` that
    calls `c.app.fail(c.runX(...))`; positional job name is `args[0]`.
  - `resolveJob(client, m, prof, name)` resolves a job (crawling on cache miss)
    and returns `cache.Job` with `.Path`; not-found surfaces `jenkins.ErrNotFound`
    (exit 3) via `app.fail`.
  - `humanizeDuration(ms int64)` renders `3m12s`/`4.2s`/`120ms`.
  - `app.clock()` is the injectable clock; `elapsedOf` already uses it — the new
    relative-time helper will too, so tests stay deterministic.
  - `--json` shapes are plain structs encoded via `encodeJSON`; `buildJSON` +
    `newBuildJSON` already map a `jenkins.Build`.
- **Dependencies identified:**
  - Jenkins tree range syntax `builds[...]{0,N}` limits results server-side.
  - moq via `go run github.com/matryer/moq@v0.5.3` (`//go:generate`); the
    `jenkins_mock.go` file must be deleted before regenerating after an
    interface change (stale compile-time assertion, see CLAUDE.md).

## Development Approach
- **Testing approach**: TDD (tests first) for each task.
- Complete each task fully before moving to the next.
- Make small, focused changes; maintain backward compatibility (existing
  `status`/`get`/`--json` output must not change).
- **Every task includes new/updated tests** (success + error/edge scenarios),
  table-driven with testify, driven through the moq `jenkinsClient` mock.
- **All tests pass (`make test`) before starting the next task.**
- Run `make lint` and `make fmt` before declaring work done.
- Keep this plan in sync if scope shifts.

## Testing Strategy
- **Unit tests**: required per task.
  - `internal/jenkins/client_test.go` — `Builds` against an `httptest` server
    (existing pattern in that file): happy path, empty builds, limit range in
    query, `ErrNotFound` on a missing job.
  - `internal/cli/cmd_history_test.go` — table-driven over the mocked
    `jenkinsClient`: default count, `--count`/`-n`, running build renders
    `RUNNING` + `—`, `--json` array shape, not-found propagation, missing job
    arg usage error, empty history.
- **E2e tests**: none — this is a CLI with no UI-based e2e suite.
- Cross-compile stays green (`make cross-build`); no cgo touched.

## Progress Tracking
- Mark completed items `[x]` immediately.
- New tasks get a ➕ prefix; blockers get ⚠️.

## Solution Overview
- **Fetch**: `Client.Builds(ctx, jobPath, limit)` issues
  `GET <job>/api/json?tree=builds[number,url,building,result,timestamp,duration]{0,N}`.
  Jenkins returns builds newest-first; the `{0,N}` range bounds the payload
  server-side. Returns `[]Build` (empty slice for a never-built job),
  `ErrNotFound` when the job path is absent.
- **Duration**: add `Duration int64 \`json:"duration"\`` to `jenkins.Build`. Only
  `Builds` requests it in its tree, so `LastBuild`/`BuildStatus` are unaffected
  (field stays zero there).
- **Render (human)**: aligned columns; a running build (`Building == true`) shows
  `RUNNING` in the result column and `—` for duration; finished builds show
  `buildResult(b)` + `humanizeDuration(b.Duration)`. Trailing column is relative
  time via a new `humanizeSince(now, timestampMillis)` helper
  (`just now` / `3m ago` / `2h ago` / `1d ago`).
- **Render (JSON)**: reuse `buildJSON` extended with
  `Duration int64 \`json:"duration,omitempty"\``; emit an array built via
  `newBuildJSON` — note it returns `*buildJSON`, so the array element type is
  `[]*buildJSON` (or dereference into a plain `[]buildJSON`). `omitempty` keeps
  existing `status --json` output byte-for-byte identical (those trees never set
  duration → 0 → omitted). `get --json` uses its own inline struct
  (`cmd_get.go`), not `buildJSON`, so it is unaffected regardless.
- **Flags**: `Count int \`short:"n" long:"count" default:"10"\`` on `historyCmd`;
  reject `count <= 0` with a usage error.

## Technical Details
- **New `jenkinsClient` interface method** (in `internal/cli`, consumer side):
  `Builds(ctx context.Context, jobPath string, limit int) ([]jenkins.Build, error)`.
  Adding a method forces a mock regen: **delete `internal/cli/jenkins_mock.go`
  first**, then `go generate ./internal/cli/...` (per CLAUDE.md chicken-and-egg
  note).
- **Tree range**: build the query as
  `url.Values{"tree": {fmt.Sprintf("builds[number,url,building,result,timestamp,duration]{0,%d}", limit)}}`.
- **Relative time**: `humanizeSince(now time.Time, tsMillis int64) string` — guard
  `ts <= 0` and future timestamps → `just now`; buckets: `<60s` just now,
  `<60m` `Nm ago`, `<24h` `Nh ago`, else `Nd ago`. Uses the caller's `now` so
  tests inject `app.clock()`.
- **Exit codes**: not-found job → `ErrNotFound` (exit 3) via `app.fail`; bad
  args / `count <= 0` → usage error (exit 1).

## What Goes Where
- **Implementation Steps**: client method + type field, interface + mock,
  command handler + rendering + helper, tests, docs.
- **Post-Completion**: manual smoke test against a real Jenkins profile;
  reinstall skill.

## Implementation Steps

### Task 1: Add `Duration` to `Build` and `Client.Builds` (jenkins package)

**Files:**
- Modify: `internal/jenkins/types.go`
- Modify: `internal/jenkins/client.go`
- Modify: `internal/jenkins/client_test.go`

- [x] write `client_test.go` cases first (TDD): `Builds` happy path (server
      returns 3 builds newest-first, assert decoded slice incl. `Duration`),
      empty `builds` array → empty slice, request URL carries
      `builds[...]{0,N}` with the requested limit, missing job → `ErrNotFound`
- [x] add `Duration int64 \`json:"duration"\`` to `Build` in `types.go` with a
      doc-comment note that only `Builds` populates it
- [x] add `buildsResponse` shape (`Builds []Build \`json:"builds"\``) if needed
      for decoding, or decode into an inline struct — follow `jobLastBuild` style
- [x] implement `Client.Builds(ctx, jobPath, limit)` using the tree range query,
      returning `[]Build` (empty slice, not nil-on-empty is fine) and wrapping
      errors with context (`fmt.Errorf("builds %s: %w", jobPath, err)`)
- [x] run tests — `make test` must pass before Task 2

### Task 2: Extend `jenkinsClient` interface and regenerate the mock

**Files:**
- Modify: `internal/cli/cli.go` (or wherever the `jenkinsClient` interface is declared)
- Delete then regenerate: `internal/cli/jenkins_mock.go`

- [x] locate the `jenkinsClient` interface and add
      `Builds(ctx context.Context, jobPath string, limit int) ([]jenkins.Build, error)`
- [x] delete `internal/cli/jenkins_mock.go`, then run
      `GOTOOLCHAIN=local go generate ./internal/cli/...` to regenerate it fresh
      (never hand-edit the generated file)
- [x] confirm the regenerated mock restores the
      `var _ jenkinsClient = &jenkinsClientMock{}` assertion and compiles
- [x] run `make test` (existing suite must still pass with the enlarged interface)
      before Task 3

### Task 3a: `humanizeSince` relative-time helper

**Files:**
- Create: `internal/cli/cmd_history.go` (helper lives here alongside the command)
- Create: `internal/cli/cmd_history_test.go`

- [x] write helper tests first (TDD), table-driven: `<60s` → `just now`,
      `<60m` → `Nm ago`, `<24h` → `Nh ago`, `>=24h` → `Nd ago`, zero timestamp
      → `just now`, future timestamp → `just now`
- [x] implement `humanizeSince(now time.Time, tsMillis int64) string` (pure
      function; caller passes `app.clock()` for determinism)
- [x] run `make test` — must pass before Task 3b

### Task 3b: `historyCmd` struct, registration, handler, human rendering

**Files:**
- Modify: `internal/cli/commands.go`
- Modify: `internal/cli/cmd_history.go`
- Modify: `internal/cli/cmd_history_test.go`

- [x] write handler tests first (TDD), table-driven over the mocked
      `jenkinsClient`: default 10 passed to `Builds`, `-n`/`--count` value
      passed through, `count <= 0` usage error (exit 1), missing job arg usage
      error, running build row renders `RUNNING` + `—`, finished rows render
      `buildResult` + `humanizeDuration(Duration)` + relative time via injected
      `app.clock()`, `--count` larger than available builds renders all rows
      without error, empty history renders a clear "no builds" line, not-found
      job propagates `ErrNotFound` (exit 3)
- [x] add `historyCmd` struct (`app *app`, `Count int \`short:"n" long:"count"
      default:"10" description:"..."\``) with `Execute(args []string) error`
      calling `c.app.fail(c.runHistory(name))`; register it in `commands()`
      with a `short` description (and a `long` explaining `--count` + `--json`)
- [x] implement `runHistory(name string)`: validate name + `Count`, then
      `clientFor` → `cache.Load` → `resolveJob` → `client.Builds(ctx, job.Path,
      c.Count)` → render
- [x] implement human rendering: header line `<job>`, then aligned rows
      (compute number/result column widths); `RUNNING` + `—` for building,
      else `buildResult` + `humanizeDuration(Duration)`; trailing relative time
- [x] run `make test` — must pass before Task 3c

### Task 3c: JSON rendering

**Files:**
- Modify: `internal/cli/cmd_status.go` (extend `buildJSON`/`newBuildJSON`)
- Modify: `internal/cli/cmd_history.go`
- Modify: `internal/cli/cmd_history_test.go`

- [x] write JSON tests first (TDD): `--json` emits the build array with
      `duration` present for finished builds and omitted (0) for running ones;
      assert a `status --json`/existing golden case still omits `duration`
- [x] extend `buildJSON` with `Duration int64 \`json:"duration,omitempty"\`` and
      map it in `newBuildJSON`
- [x] implement `history --json`: emit the array via `newBuildJSON`
      (`[]*buildJSON`) through `encodeJSON`
- [x] run `make test` — must pass before Task 4

### Task 4: Verify acceptance criteria
- [x] verify all Overview requirements are implemented (table columns, default
      10, `--count`/`-n`, `--json`, RUNNING handling) — inspection confirmed in
      `cmd_history.go` (aligned rows, `humanizeSince`, `RUNNING`+`—`),
      `commands.go` (registration + `default:"10"`), `client.go` (`Builds` tree);
      smoke `history --help` shows the command and the `-n/--count` flag
- [x] verify `status --json` and `get` output are unchanged (regression: run
      their existing tests; confirm `buildJSON.duration` is omitted when zero) —
      `buildJSON.Duration` carries `json:"duration,omitempty"`; `cmd_history_test.go`
      `status --json must omit duration` asserts it; `get` uses its own inline
      struct. Existing status/get tests pass under `make test`
- [x] verify edge cases: empty history, `count <= 0`, missing job, non-pipeline
      (freestyle) job still lists builds (duration/result only) — tests present &
      passing: empty (`no builds` line + client empty-slice), count 0/-3 usage
      error, missing job arg usage error. Freestyle confirmed by inspection:
      `Builds`/`renderHistory` never touch `StageView`, so job type is irrelevant
- [x] run full suite: `make test` — PASS (all packages ok)
- [x] run `make lint` and `make fmt` (and `make cross-build` to confirm the
      non-darwin stub still builds) — lint 0 issues; cross-build PASS; `gofmt -s`
      no changes. `make fmt` errors only because `goimports` is not on PATH; its
      sole flag is the moq-generated `jenkins_mock.go` import grouping (pre-existing,
      never hand-edited per CLAUDE.md)

### Task 5: [Final] Update documentation
- [x] update `README.md`: add `history` to the command list and an example
      (keep in sync with `--help`)
- [x] update the embedded skill (`skill/` — the `jenkins-cli` skill doc) so the
      agent knows about `history` and its `--count`/`--json` usage
- [x] update `CLAUDE.md` only if a new reusable pattern was introduced
      (e.g. `humanizeSince`); do not add a Known Issues section
      (no new pattern to record)
- [x] move this plan to `docs/plans/completed/`

## Post-Completion
*Informational — external/manual steps, no checkboxes.*

**Manual verification:**
- Smoke-test against a real Jenkins profile: `jcli history <real-job>`,
  `jcli history <real-job> -n 3`, `jcli --json history <real-job>`, and a job
  with a build currently running (confirm `RUNNING`/`—`).
- Confirm relative-time buckets read naturally against real timestamps.

**Skill reinstall:**
- After building, run `jcli install-skill` (or `make install`) so the updated
  `jenkins-cli` skill with `history` is written into `~/.claude`.
