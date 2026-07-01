# Stage View progress during `--wait`

## Overview
- While `build --wait` polls a run to completion (phase 2 of `waitForBuild`), also fetch the
  Pipeline Stage View Plugin's `wfapi/describe` data and log stage state transitions to stderr.
- Solves the "blind wait" problem: today `--wait` prints only `started` then the terminal result;
  the user gets no visibility into which stage is running or where a long pipeline is.
- Auto-on whenever stage data is available; silently degrades for freestyle / non-pipeline jobs
  (where `wfapi` returns 404). A `--no-stages` flag opts out entirely.

## Context (from discovery)
- Files/components involved:
  - `internal/jenkins/client.go` / `types.go` — REST client + types (new `StageView` method).
  - `internal/cli/cmd_build.go` — `waitForBuild` phase-2 loop is the integration point.
  - `internal/cli/commands.go` — `buildCmd` flag struct (`Wait` lives here; add `NoStages`).
  - `internal/cli/cli.go` — `jenkinsClient` interface the cli package owns.
  - `internal/cli/jenkins_mock.go` — moq mock to regenerate after the interface grows.
- Related patterns found:
  - `BuildResult`/`QueueItem` are the template: absolute-URL GET via `getJSONURL`, subset structs,
    typed sentinel errors (`ErrNotFound` from a 404 in `statusError`).
  - Progress lines already go to `c.app.stderr` (e.g. `build %q started`); terminal result to stdout.
- Dependencies identified: Pipeline Stage View Plugin endpoint `<buildURL>/wfapi/describe`. Stage
  status enum: `NOT_EXECUTED`, `IN_PROGRESS`, `SUCCESS`, `FAILED`, `ABORTED`, `UNSTABLE`, `PAUSED`.

## Development Approach
- **Testing approach**: Regular (code first, then table-driven tests with `httptest` + moq).
- Complete each task fully (incl. tests passing) before the next.
- `wfapi` absence is never an error: `StageView` maps 404 to a sentinel the loop treats as "no data".
- Stage view is purely informational — `BuildResult` (`Building`/`Result`) stays the sole authority
  for terminal exit and exit codes. Backward compatible: default piped output gains stderr lines only.

## Testing Strategy
- **Unit tests**: required per task. `internal/jenkins` uses `httptest.Server`; `internal/cli` uses
  the regenerated moq mock to drive stage snapshots across poll iterations.
- No e2e/UI suite in this repo. Live `wfapi` against real Jenkins is manual (Post-Completion).

## Progress Tracking
- Mark `[x]` immediately when done; ➕ for new tasks; ⚠️ for blockers. Keep plan in sync.

## Solution Overview
- New `Client.StageView(ctx, buildURL) ([]Stage, error)` hitting `<buildURL>/wfapi/describe`,
  returning a 404 as `ErrNotFound` (caller swallows it).
- `waitForBuild` keeps a `map[stageName]status` of last-seen statuses. Each poll, after the existing
  `BuildResult` call, it calls `StageView` (skipped when `--no-stages`); for every stage whose status
  changed since last poll it appends one transition line to stderr.
- Transition glyphs: `▶` IN_PROGRESS, `✓` SUCCESS, `✗` FAILED, `⚠` UNSTABLE, `⊘` ABORTED. Lines
  carry the stage name and, on completion, `durationMillis` humanized.

## Technical Details
- `wfapi/describe` JSON subset: top-level `stages[]`, each `{ name, status, durationMillis }`.
- New types in `types.go`: `Stage struct { Name, Status string; DurationMillis int64 }`
  (+ a private `stageViewResponse` wrapper).
- Ordering: log stages in the order `wfapi` returns them (pipeline order) for deterministic output
  and stable tests.
- A stage may appear mid-run; first sighting of a non-`NOT_EXECUTED` status logs a transition.

## What Goes Where
- **Implementation Steps**: client method + types, flag, wait-loop integration, mock regen, tests.
- **Post-Completion**: live verification against a real production pipeline + a freestyle job.

## Implementation Steps

### Task 1: Add `StageView` client method and stage types

**Files:**
- Modify: `internal/jenkins/types.go`
- Modify: `internal/jenkins/client.go`
- Modify: `internal/jenkins/client_test.go`

- [x] add `Stage` (exported: `Name`, `Status`, `DurationMillis`) and private `stageViewResponse` to `types.go`
- [x] add `Client.StageView(ctx, buildURL)` in `client.go`: GET `<buildURL>/wfapi/describe` via `getJSONURL`, return `[]Stage`
- [x] confirm a 404 surfaces as `ErrNotFound` (existing `statusError`); document that callers swallow it
- [x] write tests: successful describe decode (multiple stages, mixed statuses) via `httptest`
- [x] write tests: 404 → `errors.Is(err, ErrNotFound)`; malformed body → decode error
- [x] run `GOTOOLCHAIN=local go test -race ./internal/jenkins/...` — must pass before next task

### Task 2: Add `--no-stages` flag and grow the client interface

**Files:**
- Modify: `internal/cli/commands.go`
- Modify: `internal/cli/cli.go`
- Modify: `internal/cli/jenkins_mock.go`

- [x] add `NoStages bool` with `long:"no-stages"` to the `buildCmd`/build flag struct in `commands.go`
- [x] add `StageView(ctx context.Context, buildURL string) ([]jenkins.Stage, error)` to the `jenkinsClient` interface in `cli.go`
- [x] regenerate the moq mock (`go:generate`) so `jenkins_mock.go` gains `StageViewFunc`; do not hand-edit beyond regen
- [x] run `GOTOOLCHAIN=local go build ./...` — interface + mock compile before next task

### Task 3: Emit stage transitions in the wait loop

**Files:**
- Modify: `internal/cli/cmd_build.go`
- Modify: `internal/cli/cmd_build_test.go`

- [x] in `waitForBuild` phase 2, keep `seen := map[string]string{}`; after each `BuildResult`, unless `NoStages`, call `StageView`
- [x] swallow `ErrNotFound` (and any stage-view error) — never fail the wait on stage data; continue polling
- [x] add a small helper to diff `[]Stage` against `seen` and print one stderr line per changed status (glyph + name + humanized duration on completion)
- [x] write tests: mock returns evolving stage snapshots across polls → assert exact stderr transition lines, no dupes
- [x] write tests: `StageView` returns `ErrNotFound` → wait still completes on `BuildResult`, no stage lines; `--no-stages` → `StageView` never called (assert `len(StageViewCalls())==0`)
- [x] run `GOTOOLCHAIN=local go test -race ./internal/cli/...` — must pass before next task

### Task 4: Verify acceptance criteria
- [x] stage transitions appear during `--wait` for pipeline jobs; absent silently for freestyle — covered by `TestBuild_WaitStages` (evolving snapshots emit `▶`/`✓` transitions; `ErrNotFound` 404 path emits no stage lines and still completes). Live-verified against real pipeline/freestyle jobs in Post-Completion.
- [x] `--no-stages` fully suppresses stage fetching; exit codes (0/4) unchanged from today — `TestBuild_WaitStages` "no-stages" subtest asserts `len(StageViewCalls())==0`; exit 0 (SUCCESS) and exit 4 (FAILURE/UNSTABLE/cancelled) verified unchanged in `TestBuild_Wait`/`TestBuild_WaitErrors` (stage view is purely informational, never affects exit).
- [x] run full suite: `GOTOOLCHAIN=local make test` — all packages `ok` (`-race`); ld `LC_DYSYMTAB` warnings are benign linker noise.
- [x] run `make lint` and `make cross-build` (stage view path must stay green on the `!darwin` stub) — `golangci-lint` not installed locally; fell back to `GOTOOLCHAIN=local go vet ./...` (clean). `make cross-build` (`GOOS=linux CGO_ENABLED=0` build+vet) passes green with the keychain stub.

### Task 5: [Final] Update documentation
- [x] document `--no-stages` and stage-view behavior in `README.md`; keep in sync with `--help`
- [x] note the `wfapi`/Stage View Plugin dependency where build/wait is described
- [x] move this plan to `docs/plans/completed/`

## Post-Completion
*Informational — external/manual, no checkboxes.*

**Manual verification:**
- Trigger a real production **pipeline** job with `--wait` and confirm live stage transitions match the Jenkins Stage View UI.
- Trigger a **freestyle** job (e.g. a non-pipeline `deploy-*`) with `--wait` and confirm it silently shows no stages and still exits on result.
- Confirm against a Jenkins **without** the Stage View Plugin (or a job that predates it) that the 404 path degrades cleanly.
