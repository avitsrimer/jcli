// Package cli is the command layer: it wires the pure-Go config, cache, and credential-agent
// clients to a Jenkins REST client and dispatches the jcli subcommands. The Jenkins client is
// consumed through an interface declared here (the umputun "interface in the consumer package"
// convention) so commands can be tested against a generated mock; the concrete client lives in
// internal/jenkins, which must not import this package.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	flags "github.com/jessevdk/go-flags"

	"github.com/avitsrimer/jcli/internal/cache"
	"github.com/avitsrimer/jcli/internal/config"
	"github.com/avitsrimer/jcli/internal/creds"
	"github.com/avitsrimer/jcli/internal/jenkins"
)

// moq is pinned to v0.5.3: v0.7+ requires Go 1.26 while this module targets Go 1.24.
//go:generate go run github.com/matryer/moq@v0.5.3 -out jenkins_mock.go . jenkinsClient

// exit codes per the design contract; the CLI maps every command outcome onto one of these.
const (
	exitOK        = 0
	exitUsage     = 1
	exitAuth      = 2
	exitNotFound  = 3
	exitBuildFail = 4
)

// jenkinsClient is the consumer-side view of internal/jenkins.Client: the exact surface the cli
// commands need. Declaring it here (not in the jenkins package) keeps the dependency arrow pointing
// inward and lets commands run against a moq-generated mock.
type jenkinsClient interface {
	WhoAmI(ctx context.Context) (jenkins.Identity, error)
	Jobs(ctx context.Context) ([]jenkins.Job, error)
	JobParams(ctx context.Context, jobPath string) ([]jenkins.Param, error)
	Build(ctx context.Context, jobPath string, params map[string]string) (string, error)
	QueueItem(ctx context.Context, queueURL string) (jenkins.QueueItem, error)
	BuildResult(ctx context.Context, buildURL string) (jenkins.BuildResult, error)
	StageView(ctx context.Context, buildURL string) ([]jenkins.Stage, error)
	LastBuild(ctx context.Context, jobPath string) (jenkins.Build, bool, error)
	BuildStatus(ctx context.Context, buildURL string) (jenkins.Build, error)
	RunningBuilds(ctx context.Context) ([]jenkins.RunningBuild, error)
	ConsoleText(ctx context.Context, buildURL string) (string, error)
	ConsoleProgressive(ctx context.Context, buildURL string, start int64) (jenkins.ConsoleChunk, error)
}

// clientFactory builds a jenkinsClient for a resolved profile's url/username/token. It is a field
// on app so tests can inject a factory returning a mock instead of a live HTTP client.
type clientFactory func(url, username, token string) jenkinsClient

// credsClient is the consumer-side view of internal/creds.Client used by the auth commands; kept
// minimal so it mocks cleanly.
type credsClient interface {
	Token(profile string) (string, error)
	SetToken(profile, token string) error
	DeleteToken(profile string) error
	Flush() error
}

// app holds the wiring shared by every command: the loaded config, a credential-agent client, a
// Jenkins client factory, the resolved output streams, and the global flags.
type app struct {
	cfg     *config.Config
	creds   credsClient
	factory clientFactory
	stdout  io.Writer
	stderr  io.Writer
	global  *globalOpts

	// buildParams holds the --param-<name>=val pairs lifted out of argv by the pre-parse pass; only
	// the build command reads them. lastExit is the exit code recorded by the dispatched command.
	buildParams map[string]string
	lastExit    int

	// pollInterval overrides the --wait poll interval; tests set a tiny value so they don't sleep on
	// wall-clock time. A zero value falls back to defaultPollInterval.
	pollInterval time.Duration

	// waitTimeout bounds how long --wait polls before giving up on a build stuck in the queue (no
	// executor) or hung mid-run; tests set a tiny value. A zero value falls back to defaultWaitTimeout.
	waitTimeout time.Duration

	// promptFactory builds the interactive prompter the login command uses; it is injectable so tests
	// can feed URL/username/token deterministically without a real TTY. nil falls back to a ttyPrompter
	// over stdin/stderr.
	promptFactory func() prompter

	// now returns the current time for status elapsed calculations; it is injectable so tests get a
	// deterministic clock. A nil field falls back to time.Now via clock().
	now func() time.Time
}

// clock returns the app's current-time source, defaulting to time.Now when unset so status can
// compute a build's elapsed duration deterministically under test.
func (a *app) clock() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

// prompter returns the app's interactive prompter, defaulting to a ttyPrompter reading echoed lines
// from stdin and secrets no-echo from the controlling terminal.
func (a *app) prompter() prompter {
	if a.promptFactory != nil {
		return a.promptFactory()
	}
	return ttyPrompter{in: os.Stdin, out: a.stderr}
}

// globalOpts holds the flags shared by every subcommand; go-flags binds them as top-level options.
type globalOpts struct {
	Profile string `long:"profile" description:"profile name to use (overrides JCLI_PROFILE and default)"`
	JSON    bool   `long:"json" description:"emit machine-readable JSON output"`
	Verbose bool   `short:"v" long:"verbose" description:"verbose logging"`
}

// newApp constructs an app with the real config + creds wiring and a factory that builds live
// jenkins.Client instances. Tests bypass this and build app directly with injected collaborators.
func newApp(stdout, stderr io.Writer) (*app, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	cr, err := creds.New()
	if err != nil {
		return nil, fmt.Errorf("init credential agent client: %w", err)
	}
	return &app{
		cfg:   cfg,
		creds: cr,
		factory: func(url, username, token string) jenkinsClient {
			return jenkins.New(url, username, token, &http.Client{})
		},
		stdout: stdout,
		stderr: stderr,
		global: &globalOpts{},
	}, nil
}

// resolveProfile resolves the active profile name (flag → JCLI_PROFILE → default) and returns its
// stored config. An unset or unknown profile is a usage error.
func (a *app) resolveProfile() (config.Profile, error) {
	name := a.cfg.Resolve(a.global.Profile)
	if name == "" {
		return config.Profile{}, errors.New("no profile selected: pass --profile, set JCLI_PROFILE, or run 'jcli login'")
	}
	return a.cfg.Get(name)
}

// clientFor resolves the active profile and builds a Jenkins client for it using the token from the
// credential agent. It returns the resolved profile so callers can persist the cache under its name.
// A creds auth failure surfaces as creds.ErrAuth (exit 2).
func (a *app) clientFor() (config.Profile, jenkinsClient, error) {
	prof, err := a.resolveProfile()
	if err != nil {
		return config.Profile{}, nil, err
	}
	token, err := a.creds.Token(prof.Name)
	if err != nil {
		return config.Profile{}, nil, fmt.Errorf("get token for %q: %w", prof.Name, err)
	}
	return prof, a.factory(prof.URL, prof.Username, token), nil
}

// crawlAndSave rebuilds the job map from a fresh Jenkins crawl and persists it under the profile's
// name. It is the shared "refresh the cache" step used by list/get/build/dump on a cold cache, a
// --refresh flag, or a lookup miss. The Rebuild error is returned as-is (it already carries
// context); a Save failure is wrapped.
func (a *app) crawlAndSave(m *cache.Map, client jenkinsClient, prof config.Profile) error {
	a.verbosef("crawling jobs for profile %q from %s", prof.Name, prof.URL)
	if err := m.Rebuild(prof.URL, func() ([]jenkins.Job, error) {
		return client.Jobs(context.Background())
	}); err != nil {
		return err
	}
	if err := m.Save(prof.Name); err != nil {
		return fmt.Errorf("save cache: %w", err)
	}
	return nil
}

// verbosef writes a diagnostic line to stderr when --verbose is set, otherwise it is a no-op. It
// gives the CLI a single gate for non-essential progress output without pulling in a logging
// framework: plain fmt-to-stderr is the intentional logging approach for jcli.
func (a *app) verbosef(format string, args ...any) {
	if a.global == nil || !a.global.Verbose {
		return
	}
	fmt.Fprintf(a.stderr, "jcli: "+format+"\n", args...)
}

// exitCode maps an error to a process exit code per the design contract. nil → 0; auth errors
// (jenkins.ErrAuth or creds.ErrAuth) → 2; not-found errors (jenkins.ErrNotFound for missing jobs,
// config.ErrNotFound for missing profiles) → 3; errBuildFailed → 4; everything else is a usage/other
// error → 1.
func exitCode(err error) int {
	switch {
	case err == nil:
		return exitOK
	case errors.Is(err, jenkins.ErrAuth), errors.Is(err, creds.ErrAuth):
		return exitAuth
	case errors.Is(err, jenkins.ErrNotFound), errors.Is(err, config.ErrNotFound):
		return exitNotFound
	case errors.Is(err, errBuildFailed):
		return exitBuildFail
	default:
		return exitUsage
	}
}

// errBuildFailed marks a build that completed with a non-success result under --wait; it maps to
// exit code 4. Commands wrap it with %w to add context.
var errBuildFailed = errors.New("build failed")

// Main is the package entry point invoked by cmd/jcli after the hidden __agent mode is handled in
// main.go. It runs the parser over args and returns the process exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	a, err := newApp(stdout, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "jcli: %v\n", err)
		return exitUsage
	}
	return a.run(args)
}

// run builds the go-flags parser, lifts --param-* out of argv, parses the rest, and dispatches to
// the selected subcommand. It returns the process exit code.
func (a *app) run(args []string) int {
	// pre-parse pass: lift --param-<name>=val out before go-flags sees argv (build consumes these).
	params, rest := extractParams(args)
	a.buildParams = params

	parser := a.newParser()
	if _, err := parser.ParseArgs(rest); err != nil {
		return a.handleParseError(err)
	}
	return a.lastExit
}

// newParser builds a go-flags parser bound to the global options with every subcommand registered.
// Each command's Execute method records its outcome on the app via setExit; go-flags itself only
// reports parse/usage errors.
func (a *app) newParser() *flags.Parser {
	parser := flags.NewParser(a.global, flags.HelpFlag|flags.PassDoubleDash)
	parser.Name = "jcli"
	for _, c := range a.commands() {
		if _, err := parser.AddCommand(c.name, c.short, c.long, c.data); err != nil {
			// AddCommand only fails on programmer error (duplicate name / bad tags); surface loudly.
			panic(fmt.Sprintf("register command %q: %v", c.name, err))
		}
	}
	return parser
}

// handleParseError turns a go-flags parse error into an exit code: --help is a clean exit, anything
// else is a usage error printed to stderr.
func (a *app) handleParseError(err error) int {
	var fe *flags.Error
	if errors.As(err, &fe) && fe.Type == flags.ErrHelp {
		fmt.Fprintln(a.stdout, err)
		return exitOK
	}
	fmt.Fprintf(a.stderr, "jcli: %v\n", err)
	return exitUsage
}

// setExit records the exit code produced by a command's Execute and is what run returns. Commands
// call it (typically via fail) instead of os.Exit so the parser stays in control of the process.
func (a *app) setExit(code int) {
	a.lastExit = code
}

// fail records err's mapped exit code and prints it to stderr, returning nil so go-flags does not
// also print the error. Commands return fail(err) from Execute.
func (a *app) fail(err error) error {
	if err == nil {
		return nil
	}
	fmt.Fprintf(a.stderr, "jcli: %v\n", err)
	a.setExit(exitCode(err))
	return nil
}
