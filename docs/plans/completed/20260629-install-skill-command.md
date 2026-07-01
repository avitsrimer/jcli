# Add `install-skill` command with an embedded jcli skill

## Overview
- Create a short Claude Code **skill** describing how to use `jcli`, stored at `skill/jenkins-cli/SKILL.md`.
- **Embed** the skill into the `jcli` binary so the installed binary (run from `~/bin`, not the
  repo) is self-contained and can install the skill from any directory.
- Add a new `jcli install-skill` command that writes the embedded skill to a Claude skills
  directory: `<to>/skills/jenkins-cli/`. `--to` defaults to `~/.claude`; pass `--to <path>` to
  target another `.claude` folder.
- On install, **always overwrite** any existing target (the embedded copy is the source of truth),
  so re-installs are idempotent.

### Design pivot (recorded)
The original request said "make a symlink to the skill from this project." During planning we
switched to **embedding the skill into the CLI binary** instead of symlinking. Rationale: the
binary is installed to `~/bin/jcli` and invoked from anywhere, so a symlink would have to point at
a repo checkout that may move or disappear, leaving a dangling link. Embedding makes the binary
self-contained and the install reproducible from any cwd. The command therefore *writes files*, not
a symlink.

## Context (from discovery)
- **Command pattern**: each subcommand is a `*Cmd` struct in `internal/cli/cmd_*.go`, registered in
  `internal/cli/commands.go` (`commands()` slice), dispatched via a `flags.Commander` `Execute` that
  returns `c.app.fail(c.run...())`. Simplest existing example: `cmd_dump.go` / `cmd_profile.go`.
- **App helpers**: `app.fail(err)` maps errors to exit codes and prints to stderr; `app.verbosef` is
  the `--verbose`-gated diagnostic; `app.stdout`/`app.stderr` are the output streams. `install-skill`
  needs **none** of the Jenkins/creds wiring (`clientFor`) — it is a purely local file operation.
- **Skills convention** (confirmed via `~/.claude/skills/`): a skill is a directory
  `<claude>/skills/<name>/` containing `SKILL.md` (existing entries are dirs or symlinks to dirs).
  Target for this skill: `<to>/skills/jenkins-cli/`.
- **`//go:embed` constraint**: embed patterns cannot reference parent directories. To keep the
  skill at repo-root `./skill/jenkins-cli/`, make `./skill/` its own Go package (`skill/embed.go`,
  `package skill`) that embeds the `jenkins-cli` subtree as an `embed.FS`. `internal/cli` imports it.
- **Module path**: `github.com/avitsrimer/jcli` (from `go.mod`). New package import path:
  `github.com/avitsrimer/jcli/skill`.
- **Cross-build**: `make cross-build` (linux, `CGO_ENABLED=0`) must stay green — `embed` is pure Go
  and works under that build, so no build-tag split is needed.
- **Exit codes**: install failures fall through to exit 1 (usage/other) via the default arm of
  `exitCode`; no new code needed.

## Development Approach
- **testing approach**: Regular (code first, then tests).
- Complete each task fully before the next; small, focused changes.
- **Every task includes new/updated tests** for its code changes (success + error/edge cases).
- **All tests must pass before starting the next task.**
- Run `make test` after each change; keep `make cross-build` green.
- Prefix manual `go build`/`go test`/`go mod` invocations with `GOTOOLCHAIN=local` (CLAUDE.md: pin
  to Go 1.24, don't auto-upgrade the toolchain).
- Keep `README.md` and `CLAUDE.md` in sync with `--help`.

## Testing Strategy
- **unit tests**: required per task. The `install-skill` command is filesystem-only and highly
  testable: drive it with `--to <tempdir>` and assert the written tree/content.
- **no e2e**: this is a CLI with no UI-based e2e harness; unit tests via the existing
  `internal/cli` table-driven style (testify) cover it.
- Verify the embedded FS content equals the on-disk `skill/jenkins-cli/SKILL.md` (guards against an
  empty/stale embed).

## Progress Tracking
- Mark completed items `[x]` immediately.
- New tasks get a ➕ prefix; blockers get ⚠️.
- Update this plan if scope changes.

## Solution Overview
- **Source of truth**: `skill/jenkins-cli/SKILL.md` (authored via the `skill-creator` skill, kept as
  short as possible).
- **Embed shim**: `skill/embed.go` exposes `var Files embed.FS` covering `jenkins-cli/**`.
- **Command**: `installSkillCmd{ app, To string }`. `runInstallSkill` resolves the destination
  (`--to` or `~/.claude` via `os.UserHomeDir`), computes `dest := filepath.Join(to, "skills",
  "jenkins-cli")`, removes any existing `dest`, then walks `skill.Files` recreating every file/dir
  under `dest` with `0755` dirs / `0644` files. Prints the install path to stdout.
- **Registration**: one line in `commands.go`'s `commands()` slice.

## Technical Details
- **Destination resolution**:
  - `to := c.To; if to == "" { home, err := os.UserHomeDir(); ... ; to = filepath.Join(home, ".claude") }`
  - `dest := filepath.Join(to, "skills", "jenkins-cli")`
- **Write (always overwrite)**: `os.RemoveAll(dest)` then `fs.WalkDir(skill.Files, "jenkins-cli", ...)`:
  - for a dir entry → `os.MkdirAll(targetPath, 0o755)`
  - for a file entry → read via `skill.Files.ReadFile(path)`, `os.WriteFile(targetPath, data, 0o644)`
  - `targetPath` maps the embed path `jenkins-cli/<rest>` onto `dest/<rest>`.
- Wrap every error with context (`fmt.Errorf("...: %w", err)`) per repo convention.
- `--verbose` logs the resolved dest via `app.verbosef`.

## What Goes Where
- **Implementation Steps** (`[ ]`): skill authoring, embed shim, command + registration, tests, docs.
- **Post-Completion** (no checkboxes): `make install` to rebuild/re-sign the binary, then run
  `jcli install-skill` and confirm Claude Code picks up the skill (manual, external to this repo).

## Implementation Steps

### Task 1: Author the jcli skill (via skill-creator)

**Files:**
- Create: `skill/jenkins-cli/SKILL.md`

- [x] invoke the `skill-creator` skill to draft `skill/jenkins-cli/SKILL.md` — keep it **as short as
      possible**: name `jenkins-cli`, a tight `description` that triggers on Jenkins/CI build tasks,
      and a minimal body covering the core flow (`jcli login`, `jcli list [filter]`, `jcli get <job>`,
      `jcli build <job> --param-<name>=val [--wait]`, `jcli profile`) and the exit-code contract.
- [x] ensure valid SKILL.md frontmatter (`name`, `description`) and that the body stays brief.
- [x] no automated test for prose; correctness is verified by the embed-content test in Task 4.

### Task 2: Add the embed shim package

**Files:**
- Create: `skill/embed.go`
- Create: `skill/embed_test.go`

- [x] create `skill/embed.go` (`package skill`) with `//go:embed jenkins-cli` and
      `var Files embed.FS` (doc comment on the exported var per repo convention).
- [x] write `skill/embed_test.go`: assert `Files` contains `jenkins-cli/SKILL.md` and that its
      bytes are non-empty and start with valid frontmatter (`---`) — guards against an empty embed.
- [x] run `make test` — must pass before next task.

### Task 3: Add the `install-skill` command and register it

**Files:**
- Create: `internal/cli/cmd_install_skill.go`
- Modify: `internal/cli/commands.go`

- [x] in `cmd_install_skill.go`: define `installSkillCmd{ app *app; To string `long:"to"
      description:"path to a .claude folder (default ~/.claude)"` }` (field tag on one logical line,
      ≤140 chars) with `Execute(_ []string) error { return c.app.fail(c.runInstallSkill()) }`. Add a
      lowercase doc comment on `installSkillCmd` and on `runInstallSkill` describing current purpose
      (CLAUDE.md convention) — no history.
- [x] implement `runInstallSkill`: resolve `to` (flag or `~/.claude` via `os.UserHomeDir`), compute
      `dest := filepath.Join(to, "skills", "jenkins-cli")`, `os.RemoveAll(dest)`, then
      `fs.WalkDir(skill.Files, "jenkins-cli", ...)` recreating dirs (`0o755`) and files (`0o644`)
      under `dest`; `app.verbosef` the resolved dest; print `installed jenkins-cli skill to <dest>`
      to `app.stdout`. Wrap errors with `%w`.
- [x] register `{name: "install-skill", short: "install the jcli Claude skill", data:
      &installSkillCmd{app: a}}` in `commands.go`'s `commands()` slice.
- [x] run `GOTOOLCHAIN=local go build ./...` — compile check before next task (avoids the
      `stop-agent` side effect that `make build` triggers).

### Task 4: Tests for `install-skill`

**Files:**
- Create: `internal/cli/cmd_install_skill_test.go`

- [x] table-driven test building an `app` with a buffer `stdout` (existing test pattern in
      `cli_test.go`); run the command with `--to <t.TempDir()>` and assert
      `<tmp>/skills/jenkins-cli/SKILL.md` exists with content equal to the embedded bytes.
- [x] test **overwrite**: pre-create `<tmp>/skills/jenkins-cli/` with a stale file, run install,
      assert the stale file is gone and `SKILL.md` is present (RemoveAll semantics).
- [x] test **default destination**: `t.Setenv("HOME", t.TempDir())` (the proven pattern in
      `internal/config/config_test.go` / `internal/cache/cache_test.go`; `os.UserHomeDir` honors
      `$HOME` on darwin/linux), run install with no `--to`, and assert
      `<tmphome>/.claude/skills/jenkins-cli/SKILL.md` exists — no extra resolver helper, no writes to
      the real `~/.claude`.
- [x] test the success message is written to stdout and exit code is 0.
- [x] run `make test` — must pass before next task.

### Task 5: Cross-build + docs

**Files:**
- Modify: `README.md`
- Modify: `CLAUDE.md`

- [x] run `make cross-build` — confirm the embed compiles under linux/`CGO_ENABLED=0`.
- [x] README: add an `install-skill` row to the Commands markdown table (`README.md:173-185`,
      `| `install-skill` | … |` format) and a short note under Installation
      ("`jcli install-skill` writes the bundled skill to `~/.claude/skills/jenkins-cli` (`--to` to
      override)").
- [x] CLAUDE.md: add `skill/` to the Layout section (embedded Claude skill) and note the
      `install-skill` command writes the embedded skill (overwrite) — no new test needed.

### Task 6: Verify acceptance criteria
- [x] all Overview requirements implemented: skill authored, embedded, `install-skill` writes it to
      `<to>/skills/jenkins-cli`, `--to` defaults to `~/.claude`, overwrites existing.
- [x] run full suite: `make test`
- [x] `make lint` (golangci-lint not installed — substituted `GOTOOLCHAIN=local go vet ./...`, exit 0)
      and `make fmt` clean (`gofmt -s -l .` empty, no diff; `make fmt` only errored on missing
      `goimports` binary, no formatting changes); `make cross-build` green.

### Task 7: [Final] Documentation + plan move
- [x] confirm README `--help` parity for the new command.
- [x] move this plan to `docs/plans/completed/` (deferred — orchestrator moves it after review phases)

## Post-Completion
*Manual / external — no checkboxes:*

**Manual verification:**
- `make install` to rebuild + re-sign the binary so `~/bin/jcli` carries the embedded skill.
- Run `jcli install-skill`; confirm `~/.claude/skills/jenkins-cli/SKILL.md` is written and that a new
  Claude Code session discovers the `jenkins-cli` skill.
- Optionally test `jcli install-skill --to <other>/.claude` against an alternate location.
