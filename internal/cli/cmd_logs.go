package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/avitsrimer/jcli/internal/cache"
	"github.com/avitsrimer/jcli/internal/config"
)

// runLogs prints a build's Jenkins console output. logs <job> targets the job's latest build;
// logs <job> <number> a specific build. Without --wait it dumps the full console once; with --wait
// it streams the console progressively until the build finishes. Console text is raw and always
// goes to stdout, so --json has no effect here.
func (c *logsCmd) runLogs(args []string) error {
	switch len(args) {
	case 0:
		return errors.New("logs: missing job name")
	case 1, 2:
		// handled below
	default:
		return errors.New("logs: too many arguments (expected job [number])")
	}

	prof, client, err := c.app.clientFor()
	if err != nil {
		return err
	}
	m, err := cache.Load(prof.Name)
	if err != nil {
		return fmt.Errorf("load cache: %w", err)
	}
	job, err := c.app.resolveJob(client, m, prof, args[0])
	if err != nil {
		return err
	}

	buildURL, err := c.resolveBuildURL(client, prof, job, args)
	if err != nil {
		return err
	}
	if c.Wait {
		return c.app.followConsole(client, buildURL)
	}
	return c.app.dumpConsole(client, buildURL)
}

// resolveBuildURL picks the build to read: a specific numbered build (logs <job> <number>) or the
// job's latest build (logs <job>). A never-built job is an error.
func (c *logsCmd) resolveBuildURL(client jenkinsClient, prof config.Profile, job cache.Job, args []string) (string, error) {
	if len(args) == 2 {
		number, err := strconv.Atoi(args[1])
		if err != nil || number <= 0 {
			return "", fmt.Errorf("logs: invalid build number %q", args[1])
		}
		return buildURLFor(prof, job, number), nil
	}
	b, ok, err := client.LastBuild(context.Background(), job.Path)
	if err != nil {
		return "", fmt.Errorf("last build for %q: %w", args[0], err)
	}
	if !ok {
		return "", fmt.Errorf("%s: never built", args[0])
	}
	return b.URL, nil
}

// dumpConsole prints a build's full console once, bounded by waitEvery so a large or slow console
// body cannot hang indefinitely (the underlying HTTP client has no timeout of its own).
func (a *app) dumpConsole(client jenkinsClient, buildURL string) error {
	ctx, cancel := context.WithTimeout(context.Background(), a.waitEvery())
	defer cancel()
	text, err := client.ConsoleText(ctx, buildURL)
	if err != nil {
		return fmt.Errorf("read console: %w", err)
	}
	if _, err = fmt.Fprint(a.stdout, text); err != nil {
		return fmt.Errorf("write console: %w", err)
	}
	return nil
}

// followConsole streams a build's console to stdout until Jenkins reports no more data, bounded by
// waitEvery and paced by sleepPoll. A finished build returns a single chunk with More=false, so
// this dumps once and exits without a special case.
func (a *app) followConsole(client jenkinsClient, buildURL string) error {
	ctx, cancel := context.WithTimeout(context.Background(), a.waitEvery())
	defer cancel()
	var start int64
	for {
		next, more, err := a.streamConsoleChunk(ctx, client, buildURL, start)
		if err != nil {
			return err
		}
		start = next
		if !more {
			return nil
		}
		if err := a.sleepPoll(ctx); err != nil {
			return err
		}
	}
}

// streamConsoleChunk fetches the console since start, prints any new text to stdout, and returns
// the next offset and whether more output is expected. Shared by followConsole and build --logs.
func (a *app) streamConsoleChunk(ctx context.Context, client jenkinsClient, buildURL string, start int64) (int64, bool, error) {
	chunk, err := client.ConsoleProgressive(ctx, buildURL, start)
	if err != nil {
		return start, false, fmt.Errorf("read console: %w", err)
	}
	if chunk.Text != "" {
		if _, werr := fmt.Fprint(a.stdout, chunk.Text); werr != nil {
			return chunk.Size, chunk.More, fmt.Errorf("write console: %w", werr)
		}
	}
	return chunk.Size, chunk.More, nil
}
