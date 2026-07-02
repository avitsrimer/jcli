package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/avitsrimer/jcli/internal/cache"
	"github.com/avitsrimer/jcli/internal/config"
	"github.com/avitsrimer/jcli/internal/jenkins"
)

// runStatus dispatches on the positional args: none lists the builds running right now; one is a
// job name (report running/not-running and, when running, the running build's stages); two is a
// job name plus a build number (that build's stage status). --wait follows a running/target build
// to terminal state and is only meaningful with a target.
func (c *statusCmd) runStatus(args []string) error {
	if len(args) > 2 {
		return fmt.Errorf("status: too many arguments (expected [job [number]])")
	}
	if c.Wait && len(args) == 0 {
		return fmt.Errorf("status: --wait requires a job (optionally a build number)")
	}

	switch len(args) {
	case 0:
		return c.runningList()
	case 1:
		return c.jobStatus(args[0])
	default:
		number, err := strconv.Atoi(args[1])
		if err != nil || number <= 0 {
			return fmt.Errorf("status: invalid build number %q", args[1])
		}
		return c.buildByNumber(args[0], number)
	}
}

// runningList reports every build currently executing across all nodes via the /computer executor
// snapshot, sorted by display name. With --json it emits an array; otherwise a short block, or a
// plain line when nothing is running.
func (c *statusCmd) runningList() error {
	_, client, err := c.app.clientFor()
	if err != nil {
		return err
	}
	builds, err := client.RunningBuilds(context.Background())
	if err != nil {
		return fmt.Errorf("list running builds: %w", err)
	}
	sort.Slice(builds, func(i, j int) bool { return builds[i].Name < builds[j].Name })

	if c.app.global.JSON {
		return c.printRunningJSON(builds)
	}
	if len(builds) == 0 {
		fmt.Fprintln(c.app.stdout, "no jobs currently running")
		return nil
	}
	fmt.Fprintln(c.app.stdout, "Running:")
	for _, b := range builds {
		// b.Name already carries "#<number>"; only append the elapsed time.
		fmt.Fprintf(c.app.stdout, "  %s  (%s)\n", b.Name, c.elapsedOf(b.Timestamp))
	}
	return nil
}

// jobStatus resolves a job (crawling once on a cache miss) and reports its latest build: running →
// render its stage status (following to completion under --wait); not running or never built →
// a one-line report. Reporting a finished/absent run is informational and always exits 0.
func (c *statusCmd) jobStatus(name string) error {
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

	b, ok, err := client.LastBuild(context.Background(), job.Path)
	if err != nil {
		return fmt.Errorf("last build for %q: %w", name, err)
	}
	if !ok {
		return c.renderNotRunning(name, jenkins.Build{}, false)
	}
	if !b.Building {
		return c.renderNotRunning(name, b, true)
	}
	return c.showBuild(client, name, b)
}

// buildByNumber resolves the job, addresses the specific build, and reports its stage status. A
// missing build number surfaces as ErrNotFound (exit 3). A still-building target is followed under
// --wait; an already-terminal target is simply rendered once.
func (c *statusCmd) buildByNumber(name string, number int) error {
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

	b, err := client.BuildStatus(context.Background(), buildURLFor(prof, job, number))
	if err != nil {
		return fmt.Errorf("build #%d of %q: %w", number, name, err)
	}
	return c.showBuild(client, name, b)
}

// showBuild follows a building target to terminal state under --wait, otherwise renders the
// current stage snapshot once. An already-terminal target under --wait renders once (not an error).
func (c *statusCmd) showBuild(client jenkinsClient, name string, b jenkins.Build) error {
	if c.Wait && b.Building {
		return c.followBuild(client, name, b.URL)
	}
	stages := c.stagesFor(context.Background(), client, b.URL)
	return c.renderBuild(name, b, stages)
}

// followBuild polls the build's status and stage view, streaming stage transition lines to stderr
// (mirroring build --wait), until the run finishes; it then renders the final snapshot to stdout.
// With --json intermediate output is suppressed and only the final document is emitted. The wait is
// bounded by waitEvery so a hung run surfaces a timeout instead of blocking forever.
func (c *statusCmd) followBuild(client jenkinsClient, name, buildURL string) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.app.waitEvery())
	defer cancel()

	seen := map[string]string{}
	var lastStageErr string
	for {
		b, err := client.BuildStatus(ctx, buildURL)
		if err != nil {
			return fmt.Errorf("poll build status for %q: %w", name, err)
		}
		if !c.app.global.JSON {
			c.logStageTransitions(ctx, client, buildURL, seen, &lastStageErr)
		}
		if !b.Building {
			return c.renderBuild(name, b, c.stagesFor(ctx, client, buildURL))
		}
		if err := c.app.sleepPoll(ctx); err != nil {
			return fmt.Errorf("waiting for %q to finish: %w", name, err)
		}
	}
}

// logStageTransitions fetches the stage view and prints changed-stage lines to stderr, swallowing a
// 404 (freestyle/non-pipeline) and deduping any other error under --verbose. It reuses the shared
// printStageTransitions renderer so build and status stream stages identically.
func (c *statusCmd) logStageTransitions(ctx context.Context, client jenkinsClient, buildURL string,
	seen map[string]string, lastStageErr *string) {
	stages, err := client.StageView(ctx, buildURL)
	if err != nil {
		if !errors.Is(err, jenkins.ErrNotFound) && err.Error() != *lastStageErr {
			*lastStageErr = err.Error()
			c.app.verbosef("stage view for %s: %v", buildURL, err)
		}
		return
	}
	*lastStageErr = ""
	printStageTransitions(c.app.stderr, stages, seen)
}

// stagesFor reads the build's pipeline stages, returning nil when there are none (freestyle jobs
// answer 404 → ErrNotFound, which is swallowed since stage data is informational).
func (c *statusCmd) stagesFor(ctx context.Context, client jenkinsClient, buildURL string) []jenkins.Stage {
	stages, err := client.StageView(ctx, buildURL)
	if err != nil {
		if !errors.Is(err, jenkins.ErrNotFound) {
			c.app.verbosef("stage view for %s: %v", buildURL, err)
		}
		return nil
	}
	return stages
}

// renderBuild writes the build's overall state line plus a per-stage snapshot (--json emits the
// structured document instead). A running build's line carries its elapsed time; a finished one
// carries its terminal result.
func (c *statusCmd) renderBuild(name string, b jenkins.Build, stages []jenkins.Stage) error {
	if c.app.global.JSON {
		return c.printBuildJSON(name, b, stages)
	}
	w := c.app.stdout
	if b.Building {
		fmt.Fprintf(w, "%s #%d  RUNNING  (elapsed %s)\n", name, b.Number, c.elapsedOf(b.Timestamp))
	} else {
		fmt.Fprintf(w, "%s #%d  %s\n", name, b.Number, buildResult(b))
	}
	for _, st := range stages {
		glyph, ok := stageGlyphs[st.Status]
		if !ok {
			// NOT_EXECUTED, PAUSED, or an unknown status: a neutral marker so the stage still shows.
			glyph = "·"
		}
		if st.Status == "IN_PROGRESS" {
			fmt.Fprintf(w, "  %s %s\n", glyph, st.Name)
			continue
		}
		fmt.Fprintf(w, "  %s %s (%s)\n", glyph, st.Name, humanizeDuration(st.DurationMillis))
	}
	return nil
}

// renderNotRunning reports a job whose latest build is finished (built=true) or that has never run
// (built=false). --json emits the structured document with running=false.
func (c *statusCmd) renderNotRunning(name string, b jenkins.Build, built bool) error {
	if c.app.global.JSON {
		if !built {
			return c.printBuildJSON(name, jenkins.Build{}, nil)
		}
		return c.printBuildJSON(name, b, nil)
	}
	if !built {
		fmt.Fprintf(c.app.stdout, "%s: never built\n", name)
		return nil
	}
	fmt.Fprintf(c.app.stdout, "%s: not running (last build #%d %s)\n", name, b.Number, buildResult(b))
	return nil
}

// buildURLFor computes a build's absolute URL from the profile base and the job's Jenkins path
// (e.g. base + "/job/Folder/job/Child" + "/42/").
func buildURLFor(prof config.Profile, job cache.Job, number int) string {
	return strings.TrimRight(prof.URL, "/") + job.Path + "/" + strconv.Itoa(number) + "/"
}

// buildResult renders a finished build's terminal outcome, falling back to UNKNOWN when Jenkins
// reports neither building nor a result (a transient state).
func buildResult(b jenkins.Build) string {
	if b.Result != "" {
		return b.Result
	}
	return "UNKNOWN"
}

// elapsedOf renders the time since a build's start (epoch millis) using the injectable clock, so
// tests get a deterministic value. A zero or future timestamp renders as 0ms.
func (c *statusCmd) elapsedOf(timestamp int64) string {
	if timestamp <= 0 {
		return humanizeDuration(0)
	}
	d := c.app.clock().Sub(time.UnixMilli(timestamp))
	if d < 0 {
		d = 0
	}
	return humanizeDuration(d.Milliseconds())
}

// runningJSON is the --json shape for one currently-running build.
type runningJSON struct {
	Name      string `json:"name"`
	Number    int    `json:"number"`
	URL       string `json:"url"`
	Timestamp int64  `json:"timestamp"`
}

// buildJSON is the --json shape for a build's status.
type buildJSON struct {
	Number    int    `json:"number"`
	URL       string `json:"url"`
	Building  bool   `json:"building"`
	Result    string `json:"result,omitempty"`
	Timestamp int64  `json:"timestamp"`
}

// stageJSON is the --json shape for one pipeline stage.
type stageJSON struct {
	Name           string `json:"name"`
	Status         string `json:"status"`
	DurationMillis int64  `json:"duration_millis"`
}

// statusJSON is the --json document for a job/build target.
type statusJSON struct {
	Job     string      `json:"job"`
	Running bool        `json:"running"`
	Build   *buildJSON  `json:"build"`
	Stages  []stageJSON `json:"stages,omitempty"`
}

// printRunningJSON emits the running-list JSON array.
func (c *statusCmd) printRunningJSON(builds []jenkins.RunningBuild) error {
	out := make([]runningJSON, 0, len(builds))
	for _, b := range builds {
		out = append(out, runningJSON{Name: b.Name, Number: b.Number, URL: b.URL, Timestamp: b.Timestamp})
	}
	return c.encodeJSON(out)
}

// printBuildJSON emits the job/build status JSON document. A zero build (never built) yields a null
// build and running=false.
func (c *statusCmd) printBuildJSON(name string, b jenkins.Build, stages []jenkins.Stage) error {
	doc := statusJSON{Job: name, Running: b.Building}
	if b.URL != "" || b.Number != 0 {
		doc.Build = &buildJSON{
			Number:    b.Number,
			URL:       b.URL,
			Building:  b.Building,
			Result:    b.Result,
			Timestamp: b.Timestamp,
		}
	}
	for _, st := range stages {
		doc.Stages = append(doc.Stages, stageJSON{Name: st.Name, Status: st.Status, DurationMillis: st.DurationMillis})
	}
	return c.encodeJSON(doc)
}

// encodeJSON writes v as indented JSON to stdout.
func (c *statusCmd) encodeJSON(v any) error {
	enc := json.NewEncoder(c.app.stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
