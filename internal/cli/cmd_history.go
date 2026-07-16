package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/avitsrimer/jcli/internal/cache"
	"github.com/avitsrimer/jcli/internal/jenkins"
)

// runHistory resolves the job (crawling once on a cache miss) and lists its most recent builds. It
// runs the same pipeline as the other read commands: clientFor → cache.Load → resolveJob → live
// fetch → render. A missing job name or a non-positive count is a usage error; an unknown job
// surfaces jenkins.ErrNotFound (exit 3).
func (c *historyCmd) runHistory(name string) error {
	if name == "" {
		return errors.New("history: job name required")
	}
	if c.Count <= 0 {
		return fmt.Errorf("history: --count must be a positive number (got %d)", c.Count)
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
	builds, err := client.Builds(context.Background(), job.Path, c.Count)
	if err != nil {
		return fmt.Errorf("builds for %q: %w", name, err)
	}
	return c.renderHistory(name, builds)
}

// renderHistory writes the job name header followed by one aligned row per build: build number,
// result (or RUNNING for an in-progress build), wall-clock duration (an em-dash while running),
// and relative "time ago". A never-built job renders a clear "no builds" line. The number, result,
// and duration columns are each padded to their widest value so the trailing time column lines up;
// duration is padded by rune count so the multibyte em-dash does not skew the alignment.
func (c *historyCmd) renderHistory(name string, builds []jenkins.Build) error {
	if c.app.global.JSON {
		return c.printHistoryJSON(builds)
	}
	w := c.app.stdout
	fmt.Fprintln(w, name)
	if len(builds) == 0 {
		fmt.Fprintln(w, "  no builds")
		return nil
	}

	type row struct{ num, result, duration, since string }
	rows := make([]row, len(builds))
	numWidth, resultWidth, durWidth := 0, 0, 0
	for i, b := range builds {
		r := row{num: "#" + strconv.Itoa(b.Number), since: humanizeSince(c.app.clock(), b.Timestamp)}
		if b.Building {
			r.result, r.duration = "RUNNING", "—"
		} else {
			r.result, r.duration = buildResult(b), humanizeDuration(b.Duration)
		}
		if len(r.num) > numWidth {
			numWidth = len(r.num)
		}
		if len(r.result) > resultWidth {
			resultWidth = len(r.result)
		}
		if dw := utf8.RuneCountInString(r.duration); dw > durWidth {
			durWidth = dw
		}
		rows[i] = r
	}
	for _, r := range rows {
		durPad := strings.Repeat(" ", durWidth-utf8.RuneCountInString(r.duration))
		fmt.Fprintf(w, "  %-*s  %-*s  %s%s  %s\n", numWidth, r.num, resultWidth, r.result, r.duration, durPad, r.since)
	}
	return nil
}

// printHistoryJSON emits the builds as a JSON array in the same order as the human output (newest
// first). Each element reuses buildJSON via newBuildJSON, so a finished build carries its duration
// while a running/zero-duration build omits it (omitempty).
func (c *historyCmd) printHistoryJSON(builds []jenkins.Build) error {
	out := make([]*buildJSON, 0, len(builds))
	for _, b := range builds {
		out = append(out, newBuildJSON(b))
	}
	return c.app.encodeJSON(out)
}

// humanizeSince renders the time between tsMillis (a build's start, in epoch millis) and now as a
// compact relative string: "just now" (<60s), "Nm ago" (<60m), "Nh ago" (<24h), else "Nd ago". A
// zero/negative or future timestamp renders as "just now". Pure so callers pass app.clock() for
// deterministic tests.
func humanizeSince(now time.Time, tsMillis int64) string {
	if tsMillis <= 0 {
		return "just now"
	}
	d := now.Sub(time.UnixMilli(tsMillis))
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
