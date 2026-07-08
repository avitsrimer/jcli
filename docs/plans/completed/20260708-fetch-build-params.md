# Fetch parameters used for a specific build (`status <job> <number> --params`)

## Overview
- Add a `--params` flag to the `status` command so `jcli status <job> <number> --params`
  reports the **actual parameter values a specific build ran with**, read straight from
  the build record (`<buildURL>/api/json?tree=actions[parameters[name,value]]`).
- **Problem it solves:** today jcli can only read a job's parameter *definitions*
  (`get <job>` → defaults/choices). There is no way to see what a past build was actually
  triggered with, so an agent has to grep the console log to recover e.g. the branch. This
  is the exact pain in the feature request (Dave, Slack `1783520986.899019`): *"it's combing
  through the output logs to find the branch when they should be fetchable from the build."*
- **Integration:** `status` already resolves a job + build number and renders a build's
  header line + stages. `--params` reuses that resolution and the `buildURLFor` helper,
  swapping the stage block for a parameter block (the same way `--logs` swaps in the console).

## Context (from discovery)
- Files/components involved:
  - `internal/jenkins/client.go` — URL-addressed build reads (`BuildStatus`, `StageView`);
    `stringifyValue` (client.go:365) already turns a Boolean/String value into text.
  - `internal/jenkins/types.go` — build/param wire types.
  - `internal/cli/cli.go` — `jenkinsClient` consumer interface (add a method here).
  - `internal/cli/commands.go` — `statusCmd` flag struct.
  - `internal/cli/cmd_status.go` — dispatch, build resolution, rendering, JSON docs.
  - `internal/cli/jenkins_mock.go` — moq-generated mock (regenerated, never hand-edited).
  - `README.md`, `skill/jenkins-cli/SKILL.md` — user docs kept in sync with `--help`.
- Related patterns found:
  - `--logs` in `status` is the template: it requires a job + build number
    (`c.Logs && len(args) != 2` guard, cmd_status.go:30) and routes the `len(args)==2`
    branch to a dedicated handler (`buildLogs`) instead of the stage path.
  - `buildURLFor(prof, job, number)` (cmd_status.go:270) builds the numbered build URL.
  - `resolveJob` (cmd_build.go:104) resolves a job from cache with one crawl-retry.
  - `BuildStatus` (client.go:174) is the model for a new URL-addressed `getJSONURL` method;
    a 404 already maps to `jenkins.ErrNotFound` (exit 3) via `statusError`.
  - JSON output goes through per-command doc structs + `encodeJSON` (cmd_status.go:302–361).
- Dependencies identified: none new. Reuses `stringifyValue`, `getJSONURL`, `buildURLFor`,
  `resolveJob`, `encodeJSON`.

## Development Approach
- **Testing approach: TDD (tests first).** Write the client test against the
  `actions[parameters[...]]` wire shape and the CLI tests (mock-driven) before implementing,
  then make them pass.
- Complete each task fully (impl + tests green) before starting the next.
- Small, focused changes; run `make test` after each change.
- **Every task includes new/updated tests** — success and error/edge scenarios.
- Maintain backward compatibility: `status <job>` and `status <job> <number>` (no `--params`)
  are unchanged; `get <job>` is untouched.

## Testing Strategy
- **Unit tests (required each task):**
  - Client: `TestClient_BuildParams` — table/subtests with an `httptest` server returning a
    realistic `actions` array (a `ParametersAction` mixed with empty action objects; a String
    value and a Boolean value); assert flattened `[]BuildParam` in order with stringified
    values, that the request carries the `actions[parameters[name,value]]` tree, empty-params
    build → empty slice, and 404 → `jenkins.ErrNotFound`.
  - CLI (`cmd_status_test.go`, moq mock): `--params` human output, `--params --json` shape,
    validation errors (missing number, `--params` + `--logs` together), build-not-found →
    exit 3, and a build with no params → `(none)`.
- **E2e tests:** none — this is a CLI/library with no UI-based e2e harness. Manual smoke
  against the real Jenkins is listed under Post-Completion.

## Progress Tracking
- Mark completed items `[x]` immediately.
- New tasks get a ➕ prefix; blockers a ⚠️ prefix.
- Keep this file in sync if scope shifts during implementation.

## Solution Overview
- **New client method** `Client.BuildParams(ctx, buildURL) ([]BuildParam, error)` reads the
  build's `ParametersAction` and returns ordered `{Name, Value}` pairs (value stringified via
  the existing `stringifyValue`). One GET, mirrors `BuildStatus`/`StageView` exactly.
- **`--params` flag** on `status`, gated to a job + build number (same rule as `--logs`) and
  mutually exclusive with `--logs`. It fetches the build's status (for the `#n RESULT` header
  line) and its params, then renders a header + aligned `name = value` block — or `(none)`.
- **JSON** (`--json`) emits a `{job, build, params}` doc reusing the existing `buildJSON`
  build sub-object.
- **`--wait` is silently ignored with `--params`** (params are fixed at trigger time and never
  change over a build's life, unlike `--logs`). Render once regardless of `--wait`; add a test
  asserting `--params --wait` produces the same single-shot output.

### Key design decisions & rationale
- **Why a `status` flag, not a new command or `get <job> <n>`:** the number-addressed build
  already lives in `status`; `--params` slots in beside `--logs` as "another view of a build"
  with zero new command surface and reuses all of status' resolution/JSON plumbing.
- **Two client calls (`BuildStatus` + `BuildParams`), not one merged tree:** keeps each client
  method single-purpose and independently testable, matching the existing method boundaries.
  The extra round-trip is negligible for a CLI. (Merging into one richer `tree=` is a possible
  later optimization, not worth the coupling now.)
- **JSON `params` as an object map `name→value`:** friendliest for `jq`/agents ("what was
  `raven_branch`?") and mirrors how `--param-<name>=val` is supplied to `build`. Jenkins param
  names are unique per job, so the map loses nothing; human output preserves Jenkins' order.

## Technical Details
- **Wire shape** (`GET <buildURL>/api/json?tree=actions[parameters[name,value]]`):
  ```json
  { "actions": [
      {},
      { "_class": "hudson.model.ParametersAction",
        "parameters": [
          { "name": "raven_branch",    "value": "master" },
          { "name": "where_to_deploy", "value": "uat-2"  } ] },
      {} ] }
  ```
  Most action entries have no `parameters` key → decode to an empty slice and are skipped.
  `value` is decoded as `any` (a Boolean value arrives as `true`, a String as `"master"`),
  then run through `stringifyValue`. String/Boolean are the params in scope; an exotic type
  (file/credentials, decoded as a nested object) renders via `stringifyValue`'s `%v` fallback
  rather than erroring — acceptable, and the tests cover only String/Boolean.
- **New types** (`internal/jenkins/types.go`):
  ```go
  type BuildParam struct { Name string `json:"name"`; Value string `json:"value"` }
  type buildParamsResponse struct {
      Actions []struct{ Parameters []rawBuildParam `json:"parameters"` } `json:"actions"`
  }
  type rawBuildParam struct { Name string `json:"name"`; Value any `json:"value"` }
  ```
- **Human output:**
  ```
  UAT3-Raven-Deployment-pipeline #1828  SUCCESS
  params:
    raven_branch    = master
    where_to_deploy = uat-2
  ```
  (running target → `#<n>  RUNNING  (elapsed …)`; no params → `params:    (none)`).
  Alignment: compute the max param-name width in `renderBuildParams` and pad with
  `%-*s` (inline; `printGet` in cmd_get.go does not align, so there is no existing helper to
  reuse). Assert the alignment in the CLI test.
- **JSON output** (`buildParamsJSON` doc). Note: `params` is a `map[string]string`, so
  `encoding/json` emits keys in **alphabetical** order (deterministic — good for stable test
  assertions), *not* Jenkins insertion order. Only human output preserves Jenkins' order.
  ```json
  { "job": "UAT3-Raven-Deployment-pipeline",
    "build": { "number": 1828, "url": "…", "building": false,
               "result": "SUCCESS", "timestamp": 1783520986000 },
    "params": { "raven_branch": "master", "where_to_deploy": "uat-2" } }
  ```
- **Exit codes:** unchanged — missing build → `3` (`ErrNotFound`), auth → `2`, else `0`.

## What Goes Where
- **Implementation Steps** (`[ ]`): client method + types, CLI flag/dispatch/render, mock
  regen, tests, docs.
- **Post-Completion** (no checkboxes): manual smoke against real Jenkins, cross-build check.

## Implementation Steps

### Task 1: `jenkins.BuildParams` client method + types

**Files:**
- Modify: `internal/jenkins/types.go`
- Modify: `internal/jenkins/client.go`
- Modify: `internal/jenkins/client_test.go`

- [x] write `TestClient_BuildParams` in `client_test.go` first (TDD): httptest server
      asserting the request path/`tree` query and returning an `actions` array with a
      `ParametersAction` (String + Boolean value) interleaved with empty action objects;
      subtests for ordered flatten+stringify, empty-params build → empty slice, and
      404 → `jenkins.ErrNotFound`
- [x] add `BuildParam`, `buildParamsResponse`, `rawBuildParam` to `types.go` with doc comments
- [x] add `func (c *Client) BuildParams(ctx context.Context, buildURL string) ([]BuildParam, error)`
      to `client.go` — build `<buildURL>/api/json?tree=actions[parameters[name,value]]`, call
      `getJSONURL`, flatten actions, stringify values via `stringifyValue`, wrap errors with context
- [x] run `go test -race ./internal/jenkins/...` — must pass before Task 2

### Task 2: Add `BuildParams` to the `jenkinsClient` interface + regenerate the mock

**Files:**
- Modify: `internal/cli/cli.go`
- Modify: `internal/cli/jenkins_mock.go` (regenerated)

- [x] add `BuildParams(ctx context.Context, buildURL string) ([]jenkins.BuildParam, error)`
      to the `jenkinsClient` interface in `cli.go`
- [x] regenerate the mock: `GOTOOLCHAIN=local go generate ./internal/cli/...`
      (never hand-edit `jenkins_mock.go`); confirm the new `BuildParamsFunc` appears
- [x] run `GOTOOLCHAIN=local go build ./...` — must compile before Task 3

### Task 3: `--params` flag, dispatch, and rendering in `status`

**Files:**
- Modify: `internal/cli/commands.go`
- Modify: `internal/cli/cmd_status.go`
- Modify: `internal/cli/cmd_status_test.go`

- [x] write the CLI tests first (TDD) in `cmd_status_test.go`: `--params` human output
      (header line + aligned `name = value` block, asserting the name-column padding),
      `--params --json` doc shape, `(none)` for a param-less build, `--params --wait`
      producing the same single-shot output, and error cases — `--params` without a build
      number and `--params` combined with `--logs` — plus build-not-found → exit 3
- [x] add `Params bool` flag to `statusCmd` in `commands.go`
      (`long:"params" description:"show the parameters a specific build ran with (requires a job and build number)"`)
- [x] in `runStatus` (`cmd_status.go`): add guards mirroring `--logs` — evaluate
      `c.Params && c.Logs` (mutually exclusive) **first** so that combination reports the
      exclusion error rather than an arity error, then `c.Params && len(args) != 2` — and route
      the `len(args)==2` branch to `c.buildParams(args[0], number)` when `c.Params` is set
- [x] add `buildParams(name string, number int) error`: `clientFor` → `cache.Load` →
      `resolveJob` → `buildURLFor` → `client.BuildStatus` (header) + `client.BuildParams`,
      then render
- [x] add `renderBuildParams(name, b, params)` (human: reuse the `#n RESULT`/`RUNNING` header
      logic, then an aligned `name = value` block or `params:    (none)`) and a
      `buildParamsJSON` doc emitted via `encodeJSON` under `--json`
- [x] run `go test -race ./internal/cli/...` — must pass before Task 4

### Task 4: Verify acceptance criteria
- [x] `status <job>` and `status <job> <number>` (no `--params`) behave exactly as before — runStatus dispatches len==1→jobStatus, len==2→buildByNumber unchanged; existing TestStatus_Job/BuildByNumber pass
- [x] `--params` requires a job + number, rejects combination with `--logs`, and 404s → exit 3 — guards verified (mutual-exclusion evaluated first, then arity); TestStatus_Params covers job-only usage error, --params+--logs exclusion, and missing build → exitNotFound (3)
- [x] String and Boolean param values both render correctly; param-less build → `(none)` — client TestClient_BuildParams asserts String "master" + Boolean "true"; CLI subtest asserts `params:    (none)`
- [x] run full suite: `make test` — all 8 packages OK (go test -race ./...)
- [x] run `make lint` — 0 issues
- [x] run `make cross-build` (the new code is platform-agnostic; keep the linux stub build green) — linux build + vet green

### Task 5: [Final] Docs + close-out

**Files:**
- Modify: `README.md`
- Modify: `skill/jenkins-cli/SKILL.md`

- [x] document `--params` in the `status` section + commands table of `README.md`
      (kept in sync with `--help`)
- [x] add the `--params` view to the `status` line in `skill/jenkins-cli/SKILL.md`
- [x] update `CLAUDE.md` only if a new convention emerged (likely not) — nothing new; left unchanged
- [x] move this plan to `docs/plans/completed/` (deferred to finalize step — review phases still read the plan)

## Post-Completion
*Items requiring manual intervention or external systems — informational only.*

**Manual verification:**
- Smoke against real Jenkins (e.g. the UAT3 profile from the request):
  `jcli status UAT3-Raven-Deployment-pipeline 1828 --params` and confirm it matches the
  build's `…/1828/parameters/` page Dave linked. Repeat with `--json | jq .params`.
- Sanity-check a freestyle/non-parameterized build renders `(none)` rather than erroring.

**Possible follow-ups (out of scope, note only):**
- Allowing `status <job> --params` (latest build, no number) if users ask — deliberately
  omitted to keep the flag's arity identical to `--logs`.
