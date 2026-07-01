---
name: jenkins-cli
description: Trigger, inspect, and list Jenkins CI jobs and builds from the command line via the jcli CLI. Use when the user wants to run a Jenkins build, list or search Jenkins jobs, view job parameters, or trigger a parameterized build.
---

# jenkins-cli

Drive Jenkins from the terminal with `jcli`. Requires `jcli` installed (`make install`) and a logged-in profile first.

## Core flow

- `jcli login` — authenticate a profile (stores token in Keychain).
- `jcli list [filter]` — list cached jobs, optional name filter.
- `jcli get <job>` — show a job's details and parameters.
- `jcli build <job> --param-<name>=val [--wait]` — trigger a parameterized build; `--wait` polls to completion.
- `jcli profile` — list / use / rm profiles.

## Exit codes

`0` ok, `1` usage, `2` auth, `3` not-found, `4` build-failed (with `--wait`).
