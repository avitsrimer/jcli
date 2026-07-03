package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/avitsrimer/jcli/internal/cache"
	"github.com/avitsrimer/jcli/internal/config"
	"github.com/avitsrimer/jcli/internal/jenkins"
)

// defaultPollInterval is how often --wait polls the queue/build state. It is a field on app
// (pollInterval) so tests can drive the queue→build→result transitions without sleeping on
// wall-clock time; a zero field falls back to this value.
const defaultPollInterval = 2 * time.Second

// defaultWaitTimeout bounds --wait so a build stuck in the queue (no available executor) or a hung
// run does not make jcli hang indefinitely; it is overridable via app.waitTimeout (tests set a tiny
// value). On expiry waitForBuild returns a clear timeout error mapped to a usage exit code.
const defaultWaitTimeout = 30 * time.Minute

// pollEvery returns the effective poll interval for --wait, honoring an injected app.pollInterval
// (used by tests) and falling back to defaultPollInterval in production.
func (a *app) pollEvery() time.Duration {
	if a.pollInterval > 0 {
		return a.pollInterval
	}
	return defaultPollInterval
}

// waitEvery returns the effective overall --wait timeout, honoring an injected app.waitTimeout
// (used by tests) and falling back to defaultWaitTimeout in production.
func (a *app) waitEvery() time.Duration {
	if a.waitTimeout > 0 {
		return a.waitTimeout
	}
	return defaultWaitTimeout
}

// runBuild resolves the job and its parameter definitions, validates the user-supplied
// --param-<name>=val pairs against those defs (rejecting unknown names and out-of-range Choice
// values, filling defaults for omitted params), then triggers a parameterized build. By default
// it is fire-and-forget: it prints the queue/monitor URL and returns. With --wait it resolves the
// queue item to a build number and polls the build to completion, returning errBuildFailed
// (exit 4) on a non-SUCCESS result.
func (c *buildCmd) runBuild(name string) error {
	if name == "" {
		return errors.New("build: missing job name")
	}
	// --logs needs the resolved build URL, which only exists once the run starts, so it implies --wait.
	if c.Logs {
		c.Wait = true
	}
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

	defs, err := c.paramDefs(client, m, prof, name, job)
	if err != nil {
		return err
	}

	params, err := validateParams(defs, c.app.buildParams)
	if err != nil {
		return err
	}

	loc, err := client.Build(context.Background(), job.Path, params)
	if err != nil {
		return fmt.Errorf("trigger build %q: %w", name, err)
	}

	if !c.Wait {
		if loc == "" {
			fmt.Fprintf(c.app.stdout, "triggered build of %q\n", name)
		} else {
			fmt.Fprintf(c.app.stdout, "triggered build of %q: %s\n", name, loc)
		}
		return nil
	}
	return c.waitForBuild(client, name, loc)
}

// resolveJob looks up the job in the cache, doing exactly one crawl-then-retry on a miss (so a
// freshly created job resolves without --refresh), mirroring get. A still-absent job is a
// not-found error (exit 3) with close-name suggestions. It lives on app so build and status share
// one resolution path.
func (a *app) resolveJob(client jenkinsClient, m *cache.Map, prof config.Profile, name string) (cache.Job, error) {
	if job, ok := m.Lookup(name); ok {
		return job, nil
	}
	if err := a.crawlAndSave(m, client, prof); err != nil {
		return cache.Job{}, err
	}
	job, ok := m.Lookup(name)
	if !ok {
		sugg := suggestNames(m, name)
		if len(sugg) == 0 {
			return cache.Job{}, fmt.Errorf("job %q: %w", name, jenkins.ErrNotFound)
		}
		return cache.Job{}, fmt.Errorf("job %q: %w (did you mean: %s)", name, jenkins.ErrNotFound, strings.Join(sugg, ", "))
	}
	return job, nil
}

// paramDefs returns the parameter definitions to validate against: cached defs when a live read
// has already populated them, otherwise a fresh JobParams read which is written back into the
// cache (the "from cache, live-read on miss" rule).
func (c *buildCmd) paramDefs(client jenkinsClient, m *cache.Map, prof config.Profile, name string, job cache.Job) ([]jenkins.Param, error) {
	if !job.ParamsFetchedAt.IsZero() {
		return job.Params, nil
	}
	defs, err := client.JobParams(context.Background(), job.Path)
	if err != nil {
		return nil, fmt.Errorf("read params for %q: %w", name, err)
	}
	m.UpsertJobParams(name, defs)
	if err := m.Save(prof.Name); err != nil {
		return nil, fmt.Errorf("save cache: %w", err)
	}
	return defs, nil
}

// waitForBuild resolves the queue item at loc into a build number (polling until Jenkins
// populates the queue item's executable) and then polls that build to completion. It returns
// nil on SUCCESS and errBuildFailed (exit 4) on any other terminal result.
func (c *buildCmd) waitForBuild(client jenkinsClient, name, loc string) error {
	if loc == "" {
		return fmt.Errorf("build %q: no queue location returned, cannot wait", name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.app.waitEvery())
	defer cancel()

	// phase 1: queue item → build URL (executable transition).
	var buildURL string
	for {
		item, err := client.QueueItem(ctx, loc)
		if err != nil {
			return fmt.Errorf("poll queue item for %q: %w", name, err)
		}
		if item.Cancelled {
			return fmt.Errorf("build %q was canceled in the queue: %w", name, errBuildFailed)
		}
		if item.Executable != nil && item.Executable.URL != "" {
			buildURL = item.Executable.URL
			fmt.Fprintf(c.app.stderr, "build %q started: %s\n", name, buildURL)
			break
		}
		if err := c.app.sleepPoll(ctx); err != nil {
			return fmt.Errorf("waiting for build %q to start: %w", name, err)
		}
	}

	// phase 2: build → terminal result. seen tracks each stage's last-logged status so only
	// changed stages emit a transition line; lastStageErr dedupes the swallowed stage-view error
	// diagnostic the same way, so a persistent error logs once per distinct message instead of on
	// every poll. stage view is purely informational and never affects the terminal outcome, which
	// BuildResult alone decides.
	seen := map[string]string{}
	var lastStageErr string
	var consoleStart int64
	for {
		res, err := client.BuildResult(ctx, buildURL)
		if err != nil {
			return fmt.Errorf("poll build result for %q: %w", name, err)
		}
		if c.Logs {
			// --logs streams the console (to stdout) in place of the stage lines.
			next, _, cerr := c.app.streamConsoleChunk(ctx, client, buildURL, consoleStart)
			if cerr != nil {
				return fmt.Errorf("streaming console for %q: %w", name, cerr)
			}
			consoleStart = next
		} else {
			c.logStages(ctx, client, buildURL, seen, &lastStageErr)
		}
		if !res.Building && res.Result != "" {
			if c.Logs {
				if err := c.drainConsole(ctx, client, name, buildURL, consoleStart); err != nil {
					return err
				}
			}
			return c.reportResult(name, buildURL, res.Result)
		}
		if err := c.app.sleepPoll(ctx); err != nil {
			return fmt.Errorf("waiting for build %q to finish: %w", name, err)
		}
	}
}

// drainConsole flushes any console output produced after the build reached its terminal result,
// pacing each fetch with sleepPoll (bounded by waitEvery) so it never busy-spins while Jenkins
// finishes writing the log tail.
func (c *buildCmd) drainConsole(ctx context.Context, client jenkinsClient, name, buildURL string, start int64) error {
	for {
		next, more, err := c.app.streamConsoleChunk(ctx, client, buildURL, start)
		if err != nil {
			return fmt.Errorf("draining console for %q: %w", name, err)
		}
		start = next
		if !more {
			return nil
		}
		if err := c.app.sleepPoll(ctx); err != nil {
			return fmt.Errorf("draining console for %q: %w", name, err)
		}
	}
}

// stageGlyphs maps each stage status to its transition glyph. NOT_EXECUTED and PAUSED have no
// glyph: a not-yet-run or paused stage produces no line until it advances to a real status.
var stageGlyphs = map[string]string{
	"IN_PROGRESS": "▶",
	"SUCCESS":     "✓",
	"FAILED":      "✗",
	"UNSTABLE":    "⚠",
	"ABORTED":     "⊘",
}

// logStages fetches the pipeline stage view and prints one stderr line per stage whose status
// changed since the last poll. It is skipped when --no-stages is set, and any stage-view error
// (including a 404 ErrNotFound for freestyle/non-pipeline jobs) is swallowed: stage data is
// informational and must never fail or interrupt the wait. A non-404 error is logged under
// --verbose but deduped via lastStageErr, so a persistent error emits one diagnostic per distinct
// message rather than once per poll.
func (c *buildCmd) logStages(ctx context.Context, client jenkinsClient, buildURL string, seen map[string]string, lastStageErr *string) {
	if c.NoStages {
		return
	}
	stages, err := client.StageView(ctx, buildURL)
	if err != nil {
		if !errors.Is(err, jenkins.ErrNotFound) && err.Error() != *lastStageErr {
			*lastStageErr = err.Error()
			c.app.verbosef("stage view for %s: %v", buildURL, err)
		}
		return
	}
	// a later successful read clears the deduped error so a subsequent failure logs again.
	*lastStageErr = ""
	printStageTransitions(c.app.stderr, stages, seen)
}

// printStageTransitions writes one line per stage whose status changed since the last poll,
// updating seen in place. NOT_EXECUTED/PAUSED/unknown statuses have no glyph and emit no line; the
// still-running IN_PROGRESS line omits a duration while every terminal status carries one. Shared
// by build --wait and status --wait so both stream stage progress identically.
func printStageTransitions(w io.Writer, stages []jenkins.Stage, seen map[string]string) {
	for _, st := range stages {
		if seen[st.Name] == st.Status {
			continue
		}
		seen[st.Name] = st.Status
		glyph, ok := stageGlyphs[st.Status]
		if !ok {
			continue
		}
		if st.Status == "IN_PROGRESS" {
			fmt.Fprintf(w, "%s %s\n", glyph, st.Name)
			continue
		}
		fmt.Fprintf(w, "%s %s (%s)\n", glyph, st.Name, humanizeDuration(st.DurationMillis))
	}
}

// humanizeDuration renders a stage's durationMillis as a compact human string (e.g. 1m23s, 4.2s,
// 850ms) for the completion transition line.
func humanizeDuration(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", ms)
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	default:
		return d.Round(time.Second).String()
	}
}

// sleepPoll waits one poll interval or returns the context error if the overall --wait deadline
// elapses first, so a build stuck in the queue or hung mid-run surfaces a timeout instead of
// hanging forever. It lives on app so both build and status --wait share one poll cadence.
func (a *app) sleepPoll(ctx context.Context) error {
	t := time.NewTimer(a.pollEvery())
	defer t.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("--wait timed out after %s: %w", a.waitEvery(), ctx.Err())
	case <-t.C:
		return nil
	}
}

// reportResult prints the terminal build outcome and returns nil for SUCCESS or errBuildFailed
// (exit 4) for any other result (FAILURE/UNSTABLE/ABORTED).
func (c *buildCmd) reportResult(name, buildURL, result string) error {
	if result == "SUCCESS" {
		fmt.Fprintf(c.app.stdout, "build %q SUCCESS: %s\n", name, buildURL)
		return nil
	}
	return fmt.Errorf("build %q %s: %s: %w", name, result, buildURL, errBuildFailed)
}

// validateParams checks the user-supplied params against the job's parameter definitions: every
// supplied name must be defined, Choice params' values must be within their choices, and any
// defined param the user omitted is filled from its default. The returned map is what gets sent
// to buildWithParameters.
func validateParams(defs []jenkins.Param, supplied map[string]string) (map[string]string, error) {
	byName := make(map[string]jenkins.Param, len(defs))
	for _, d := range defs {
		byName[d.Name] = d
	}

	// reject unknown names so a typo'd --param-foo never silently does nothing.
	for name := range supplied {
		if _, ok := byName[name]; !ok {
			return nil, fmt.Errorf("unknown parameter %q (valid: %s)", name, strings.Join(paramNames(defs), ", "))
		}
	}

	out := make(map[string]string, len(defs))
	for _, d := range defs {
		val, set := supplied[d.Name]
		if !set {
			val = d.Default
		}
		// validate Choice values whether supplied or defaulted, so a default outside the choice set
		// (a misconfigured job) is caught instead of being silently sent.
		if d.Type == "Choice" && len(d.Choices) > 0 {
			if !contains(d.Choices, val) {
				return nil, fmt.Errorf("invalid value %q for choice parameter %q (choices: %s)",
					val, d.Name, strings.Join(d.Choices, ", "))
			}
		}
		out[d.Name] = val
	}
	return out, nil
}

// paramNames returns the sorted defined parameter names for error messages.
func paramNames(defs []jenkins.Param) []string {
	names := make([]string, 0, len(defs))
	for _, d := range defs {
		names = append(names, d.Name)
	}
	sort.Strings(names)
	return names
}

// contains reports whether s is in list.
func contains(list []string, s string) bool {
	return slices.Contains(list, s)
}
