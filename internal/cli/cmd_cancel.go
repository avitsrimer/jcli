package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/avitsrimer/jcli/internal/cache"
)

// cancelCmd stops a running build. Both positionals (job name + build number) are required; --yes
// skips the interactive confirmation for scripted/skill use.
type cancelCmd struct {
	app *app
	Yes bool `short:"y" long:"yes" description:"skip the confirmation prompt"`
}

// Execute implements flags.Commander. Positionals are <job> <number> (both required).
func (c *cancelCmd) Execute(args []string) error {
	name, number, err := parseCancelArgs(args)
	if err != nil {
		return c.app.fail(err)
	}
	return c.app.fail(c.runCancel(name, number))
}

// runCancel resolves the job, addresses the numbered build, and stops it if it is running. It
// pre-checks the build's status so an already-finished build reports "not running" and exits 0
// without prompting or POSTing. Unless --yes is set, a running build is confirmed interactively
// (anything other than y/yes aborts). A missing job or build surfaces as ErrNotFound (exit 3); an
// abort-permission denial surfaces as ErrPermission (exit 1 by contract).
func (c *cancelCmd) runCancel(name string, number int) error {
	prof, client, err := c.app.clientFor()
	if err != nil {
		return err
	}
	m, err := cache.Load(prof.Name)
	if err != nil {
		return fmt.Errorf("load cache: %w", err)
	}
	job, err := c.app.resolveJob(client, m, prof, name)
	if err != nil {
		return err
	}

	buildURL := buildURLFor(prof, job, number)
	b, err := client.BuildStatus(context.Background(), buildURL)
	if err != nil {
		return fmt.Errorf("build #%d of %q: %w", number, name, err)
	}
	// already finished (or never running): reporting is informational and exits 0, no prompt/POST.
	if !b.Building {
		fmt.Fprintf(c.app.stdout, "build #%d of %s is not running (%s)\n", number, name, buildResult(b))
		return nil
	}

	if !c.Yes {
		answer, err := c.app.prompter().promptLine(fmt.Sprintf("cancel build #%d of %s? [y/N] ", number, name))
		if err != nil {
			return fmt.Errorf("read confirmation: %w", err)
		}
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "y", "yes":
		default:
			fmt.Fprintln(c.app.stdout, "aborted")
			return nil
		}
	}

	if err := client.Stop(context.Background(), buildURL); err != nil {
		return fmt.Errorf("stop build #%d of %q: %w", number, name, err)
	}
	fmt.Fprintf(c.app.stdout, "canceled build #%d of %s\n", number, name)
	return nil
}

// parseCancelArgs validates the positional arguments: exactly a job name and a positive integer
// build number. It rejects too few/too many arguments and non-numeric or non-positive numbers with
// clear usage errors (exit 1).
func parseCancelArgs(args []string) (name string, number int, err error) {
	if len(args) < 2 {
		return "", 0, errors.New("cancel: expected a job and a build number (usage: cancel <job> <number>)")
	}
	if len(args) > 2 {
		return "", 0, errors.New("cancel: too many arguments (expected <job> <number>)")
	}
	n, err := strconv.Atoi(args[1])
	if err != nil || n <= 0 {
		return "", 0, fmt.Errorf("cancel: invalid build number %q", args[1])
	}
	return args[0], n, nil
}
