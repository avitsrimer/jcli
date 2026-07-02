package cli

// command bundles the metadata go-flags needs to register a subcommand with its data struct (which
// carries the command's flags/positionals and implements flags.Commander via Execute).
type command struct {
	name  string
	short string
	long  string
	data  any
}

// commands returns the registered subcommand set, wiring each command's data struct to the app.
func (a *app) commands() []command {
	return []command{
		{name: "login", short: "authenticate and store a profile", data: &loginCmd{app: a}},
		{name: "profile", short: "list/use/rm profiles", data: &profileCmd{app: a}},
		{name: "logout", short: "remove stored credentials", data: &logoutCmd{app: a}},
		{name: "list", short: "list jobs from the cached map", data: &listCmd{app: a}},
		{name: "get", short: "show job details and params", data: &getCmd{app: a}},
		{name: "build", short: "trigger a parameterized build", data: &buildCmd{app: a}},
		{name: "status", short: "show running jobs or a build's stage status", data: &statusCmd{app: a}},
		{name: "dump", short: "emit the full cached job map as JSON", data: &dumpCmd{app: a}},
		{name: "install-skill", short: "install the jcli Claude skill", data: &installSkillCmd{app: a}},
	}
}

// loginCmd authenticates and stores a profile (body: Task 9). --url and --username skip their
// interactive prompts; the API token is always read no-echo from the TTY (never a flag) so it
// never lands in argv or shell history.
type loginCmd struct {
	app      *app
	URL      string `long:"url" description:"Jenkins base URL (skips the interactive URL prompt)"`
	Username string `long:"username" description:"Jenkins username (skips the interactive username prompt)"`
}

// Execute implements flags.Commander.
func (c *loginCmd) Execute(_ []string) error { return c.app.fail(c.runLogin()) }

// profileCmd lists/uses/removes profiles. The action (list|use|rm) and target profile name arrive
// as positional args, parsed in runProfile.
type profileCmd struct {
	app *app
}

// Execute implements flags.Commander.
func (c *profileCmd) Execute(args []string) error { return c.app.fail(c.runProfile(args)) }

// logoutCmd removes stored credentials (body: Task 9).
type logoutCmd struct {
	app   *app
	Purge bool `long:"purge" description:"also remove the profile from config"`
}

// Execute implements flags.Commander.
func (c *logoutCmd) Execute(_ []string) error { return c.app.fail(c.runLogout()) }

// listCmd lists cached jobs. The optional positional is a glob/substring filter applied to job
// names; --refresh forces a fresh crawl before listing.
type listCmd struct {
	app     *app
	Refresh bool `long:"refresh" description:"force a fresh crawl before listing"`
}

// Execute implements flags.Commander. The first positional, if any, is the name filter.
func (c *listCmd) Execute(args []string) error {
	var pattern string
	if len(args) > 0 {
		pattern = args[0]
	}
	return c.app.fail(c.runList(pattern))
}

// getCmd shows a single job's details and params. The job name arrives as the first positional.
type getCmd struct {
	app *app
}

// Execute implements flags.Commander. The first positional is the job name (required).
func (c *getCmd) Execute(args []string) error {
	var name string
	if len(args) > 0 {
		name = args[0]
	}
	return c.app.fail(c.runGet(name))
}

// buildCmd triggers a parameterized build (body: Task 11). The dynamic --param-<name>=val pairs are
// lifted out of argv by the pre-parse pass and read from app.buildParams, not from struct tags.
type buildCmd struct {
	app      *app
	Wait     bool `long:"wait" description:"poll the build to completion and exit by its result"`
	NoStages bool `long:"no-stages" description:"suppress pipeline stage-view progress lines during --wait"`
}

// Execute implements flags.Commander. The first positional is the job name (required).
func (c *buildCmd) Execute(args []string) error {
	var name string
	if len(args) > 0 {
		name = args[0]
	}
	return c.app.fail(c.runBuild(name))
}

// statusCmd shows run state. With no positional it lists currently running builds; with a job
// name it reports whether the job is running (and the running build's stages); with a job name and
// a build number it shows that build's stage status. --wait follows a running/target build to
// terminal state.
type statusCmd struct {
	app  *app
	Wait bool `long:"wait" description:"follow the target build's stage status to completion"`
}

// Execute implements flags.Commander. Positionals are [job [number]]; none means "running now".
func (c *statusCmd) Execute(args []string) error { return c.app.fail(c.runStatus(args)) }

// dumpCmd emits the full cached job map as formatted JSON; --refresh rebuilds via a crawl first.
type dumpCmd struct {
	app     *app
	Refresh bool `long:"refresh" description:"force a fresh crawl before dumping"`
}

// Execute implements flags.Commander.
func (c *dumpCmd) Execute(_ []string) error { return c.app.fail(c.runDump()) }

// installSkillCmd writes the embedded jenkins-cli Claude skill into a .claude folder; --to overrides
// the default ~/.claude target.
type installSkillCmd struct {
	app *app
	To  string `long:"to" description:"path to a .claude folder (default ~/.claude)"`
}

// Execute implements flags.Commander.
func (c *installSkillCmd) Execute(_ []string) error { return c.app.fail(c.runInstallSkill()) }
