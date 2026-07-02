# jcli

A small, general-purpose command-line tool for triggering and monitoring
[Jenkins](https://www.jenkins.io/) jobs from your terminal.

It is built to be safe and convenient on macOS: your Jenkins API token lives in
the **macOS Keychain**, authorized via the **login-keychain ACL bound to the
signed `jcli` binary** and served through an on-demand in-memory credential agent —
so it never lands in plaintext, shell history, or the process environment, and you
are not prompted on every command. Switch between multiple Jenkins servers using
named **profiles**.

## Features

- Trigger parameterized and non-parameterized Jenkins jobs from the CLI, with
  optional `--wait` to poll a build to completion.
- List jobs and inspect a job's parameter definitions before running them, from
  a per-profile cached job map (crawled on first use / `--refresh`).
- Check run state with `status` — list the builds running right now, see whether
  a job is running, or follow a build's pipeline stages to completion (`--wait`).
- **macOS Keychain integration** — API tokens are stored in and read from the
  login Keychain, authorized by the keychain ACL bound to the signed `jcli` binary
  and served through a short-lived in-memory agent so repeated commands do not
  re-read the keychain. Nothing sensitive is written to disk or shell history.
- **Profiles** — define multiple named Jenkins targets (e.g. `work`, `staging`,
  `home`), each with its own server URL, username, and Keychain-backed token.
- Sensible exit codes and clear error messages for scripting.

## Requirements

- macOS (the Keychain credential agent is macOS-only; the repo also cross-builds
  on other platforms with a stubbed keychain that returns an unsupported-platform
  error).
- Go 1.24+ to build.
- A Jenkins user with an API token.

## Installation

```bash
git clone <this-repo>
cd jenkins-cli
make install     # builds, signs, and installs jcli to ~/bin
```

See [Building & code signing](#building--code-signing-macos) for the signing
details (the Keychain UX depends on a stable signing identity).

After installing, run `jcli install-skill` to write the bundled skill to
`~/.claude/skills/jenkins-cli` (pass `--to <path>` to target another `.claude`
folder).

## Global options

These apply to every command:

| Flag           | Description                                                  |
| -------------- | ------------------------------------------------------------ |
| `--profile=<name>` | Profile to use (overrides `JCLI_PROFILE` and the default). |
| `--json`       | Emit machine-readable JSON output.                           |
| `-v, --verbose`| Verbose logging.                                             |
| `-h, --help`   | Show help (clean exit).                                      |

Profile resolution order: `--profile` → `JCLI_PROFILE` → the configured default.

## Authentication & profiles

A profile bundles a Jenkins server URL, a username, and a Keychain-backed token.
`jcli login` creates (or updates) one. The profile name is the resolved
`--profile` / `JCLI_PROFILE` value, falling back to `default` when none is set.

```bash
# create or update a profile; prompts for URL, username, and token
# (the token is read no-echo and verified before anything is persisted)
jcli login --profile work

# with no --profile, the profile is named "default" and becomes the default
jcli login

# pass URL and/or username as flags to skip those prompts — you are then
# only asked for the API token (the token is never a flag, so it stays out
# of argv and shell history)
jcli login --url https://jenkins.example.com --username alice

# list configured profiles (the default is marked with *)
jcli profile

# set the default profile
jcli profile use work

# remove a profile from config and delete its stored token
jcli profile rm work

# remove only the stored credentials for a profile (keep the profile config)
jcli logout --profile work

# remove the credentials AND the profile config entry
jcli logout --profile work --purge
```

`jcli login` verifies the token against Jenkins (`whoAmI`) **before** writing
anything, so a bad token fails (exit 2) and leaves your config and Keychain
untouched. Profile configuration lives in `~/.config/jcli/config.json` (no
secrets — only the URL, username, and the default selection are stored there).
Tokens live only in the macOS Keychain and the running agent's memory.

The first credential read reads the token from the Keychain once, authorized by
the keychain ACL bound to the signed `jcli` binary (silent for the installed
signed binary; a binary with a different signing identity triggers the standard
keychain "Allow / Always Allow" prompt). The in-memory agent then serves the token
to subsequent commands over a `0600` unix socket (peer-UID verified) on a 15-minute
refresh-on-use TTL, self-exiting after an idle window. Commands within the TTL do
not re-read the keychain.

## Usage

```bash
# list jobs visible to the current profile (optional glob/substring filter)
jcli list
jcli list 'deploy-*'

# show a job's details and parameter definitions (live read; updates the cache)
jcli get <job-name>

# trigger a build; pass parameters as --param-<name>=<value>
jcli build <job-name>
jcli build <job-name> --param-BRANCH=main --param-ENV=staging

# trigger and wait for completion (exit 0 on SUCCESS, 4 otherwise)
jcli build <job-name> --param-BRANCH=main --wait

# wait, but suppress the live pipeline stage-view progress lines
jcli build <job-name> --wait --no-stages

# show what is running right now across all nodes (or "no jobs currently running")
jcli status

# is a job running? if so, show its running build's stages
jcli status <job-name>

# show a specific build's stage status
jcli status <job-name> 42

# follow a running build's stages to completion
jcli status <job-name> --wait

# emit the full cached job map as formatted JSON
jcli dump

# run against a specific profile
jcli --profile staging list
```

### Cache & `--refresh`

`list`, `get`, and `dump` read from a per-profile cached job map in
`~/.cache/jcli/<profile>/jobs.json`. It is crawled on first use; `list`/`dump`
take a `--refresh` flag to force a fresh crawl, and `get`/`build` crawl once
automatically on a cache miss before reporting a job as not found. `list` prints
a hint to stderr when the cache is older than 24 hours.

### Build parameters

`build` accepts dynamic `--param-<name>=<value>` flags (one per parameter). The
value is split on the first `=`, so `=` may appear inside the value. Supplied
names are validated against the job's parameter definitions: unknown names and
out-of-range `Choice` values are rejected (exit 1), and omitted parameters are
filled with their defaults.

### Stage view during `--wait`

With `--wait`, `jcli` logs Pipeline Stage View transitions to stderr as the build
progresses, so a long pipeline is no longer a blind wait. Each stage prints one
line when its status changes:

| Glyph | Status        |
| ----- | ------------- |
| `▶`   | in-progress   |
| `✓`   | success       |
| `✗`   | failed        |
| `⚠`   | unstable      |
| `⊘`   | aborted       |

Completed stages also show a humanized duration (e.g. `1m23s`). Pass `--no-stages`
to opt out of stage fetching entirely (exit codes are unchanged either way).

Stage transitions require the Jenkins **Pipeline Stage View Plugin** and a
Pipeline job (the `wfapi/describe` endpoint). Freestyle jobs, or a Jenkins without
the plugin, simply show no stages — the fetch falls back silently, the wait still
works, and the build result (and exit code) is unaffected.

### `status`

`status` reports run state and takes three shapes:

- `jcli status` — a short list of the builds **currently executing** across all
  nodes (via the `/computer` executor snapshot), or `no jobs currently running`.
  Queued-but-not-yet-started items are not listed.
- `jcli status <job>` — resolves the job and reports whether its latest build is
  running; when running, it renders that build's stage snapshot.
- `jcli status <job> <number>` — renders the stage status of that specific build.

With `--wait` (valid only with a job/build target), `status` follows a running
build to completion, streaming the same stage transition lines to stderr as
`build --wait`, then prints the final snapshot. An already-finished target is
rendered once (not an error). `status` is informational: it reports a `FAILURE`
build as normal output and still exits `0`; only a missing job/build (exit `3`)
or auth failure (exit `2`) is non-zero. The same stage glyphs and stage-view
fallback described above apply, and `--json` emits a structured document.

### Commands

| Command          | Description                                                       |
| ---------------- | ----------------------------------------------------------------- |
| `login`          | Authenticate and store a profile (prompts for URL/user/token).    |
| `profile [list]` | List profiles (default action), marking the default with `*`.     |
| `profile use <p>`| Set the default profile.                                          |
| `profile rm <p>` | Remove a profile from config and delete its stored token.         |
| `logout`         | Remove stored credentials for a profile (`--purge` also removes the profile config). |
| `list [pattern]` | List cached jobs, optionally filtered (`--refresh` to re-crawl).  |
| `get <job>`      | Show a job's details and parameters (live read).                  |
| `build <job>`    | Trigger a build (`--param-<name>=val`, `--wait` to poll to completion, `--no-stages` to suppress stage-view lines). |
| `status [job [number]]` | Show running builds (no args), a job's run state, or a build's stage status (`--wait` to follow a running build). |
| `dump`           | Emit the full cached job map as formatted JSON (`--refresh` to re-crawl). |
| `install-skill`  | Install the bundled Claude skill to `~/.claude/skills/jenkins-cli` (`--to` to override). |

### Exit codes

| Code | Meaning                              |
| ---- | ------------------------------------ |
| `0`  | OK                                   |
| `1`  | Usage error                          |
| `2`  | Authentication failure               |
| `3`  | Job / profile not found              |
| `4`  | Build failed (with `--wait`)         |

## Building & code signing (macOS)

```bash
make build       # build ./jcli
make cert        # create/reuse the self-signed code-signing identity (idempotent)
make sign        # codesign --options runtime; prints the designated requirement
make install     # sign + install to ~/bin (INSTALL_DIR=/usr/local/bin to override)
make cross-build # prove the repo still builds on non-darwin (keychain stub)
```

The Keychain ACL trust — which lets the signed `jcli` read its token silently — is
bound to the signing identity's **designated requirement**, derived from the
self-signed certificate (`jcli Code Signing`). This is an ACL trust, not an
entitlement. `make cert` is idempotent and **must never regenerate** the
certificate — a new cert changes the designated requirement, so the rebuilt binary
no longer matches the ACL and reads hit the keychain "Allow / Always Allow"
authorization prompt (which names "Jenkins CLI") instead of being silent. A rebuild
+ re-sign with the same cert produces a stable requirement; verify it with
`make show-dr`.

The DR codesign derives for a self-signed identity is the leaf-certificate-hash
form, e.g.:

```
identifier jcli and certificate leaf = H"<sha1-of-your-cert>"
```

The hash is **specific to the certificate on your machine** — it is not a shared
constant. Whoever runs `make cert` gets their own self-signed cert with its own
hash, so this DR is per-identity. The hash is the certificate's (a public
fingerprint, safe to record), so the requirement stays stable as long as that
same cert is reused, and changes if the cert is regenerated. Run `make show-dr`
after your first sign to capture your own DR string for rebuild verification.

## License

MIT
