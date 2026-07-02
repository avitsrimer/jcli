---
name: jenkins-cli
description: Trigger, inspect, and list Jenkins CI jobs and builds from the command line via the jcli CLI. Use when the user wants to run a Jenkins build, list or search Jenkins jobs, view job parameters, trigger a parameterized build, or check what is running / a build's status.
---

# jenkins-cli

Drive Jenkins from the terminal with `jcli`. Requires `jcli` installed (`make install`) and a logged-in profile first.

## Core flow

- `jcli login` — authenticate a profile (stores token in Keychain).
- `jcli list [filter]` — list cached jobs, optional name filter.
- `jcli get <job>` — show a job's details and parameters.
- `jcli build <job> --param-<name>=val [--wait]` — trigger a parameterized build; `--wait` polls to completion.
- `jcli status [job [number]] [--wait]` — no args: list builds running right now; `<job>`: is it running (and its running build's stages); `<job> <number>`: that build's stage status; `--wait` follows a running build.
- `jcli profile` — list / use / rm profiles.

## Exit codes

`0` ok, `1` usage, `2` auth, `3` not-found, `4` build-failed (with `build --wait`).

`status` is informational — it reports a failed build as normal output and exits `0`; only a missing job/build (`3`) or auth failure (`2`) is non-zero.
