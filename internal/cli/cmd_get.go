package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/avitsrimer/jcli/internal/cache"
	"github.com/avitsrimer/jcli/internal/jenkins"
)

// maxSuggestions caps the close-name hints printed when a job is not found.
const maxSuggestions = 5

// runGet does a live parameter read for a job: it resolves the job in the cache (crawling once on a
// miss), fetches its current parameter definitions, writes them back into the cache, and prints the
// job's details. A job that is still absent after a crawl is a not-found error (exit 3) carrying
// close-name suggestions.
func (c *getCmd) runGet(name string) error {
	if name == "" {
		return fmt.Errorf("get: missing job name")
	}
	prof, client, err := c.app.clientFor()
	if err != nil {
		return err
	}
	m, err := cache.Load(prof.Name)
	if err != nil {
		return fmt.Errorf("load cache: %w", err)
	}

	job, ok := m.Lookup(name)
	if !ok {
		// one crawl-then-retry before giving up, so a freshly created job resolves without --refresh.
		if err := c.app.crawlAndSave(m, client, prof); err != nil {
			return err
		}
		if job, ok = m.Lookup(name); !ok {
			return c.notFound(m, name)
		}
	}

	// live param read; update the cache so subsequent build/get see fresh defs.
	params, err := client.JobParams(context.Background(), job.Path)
	if err != nil {
		return fmt.Errorf("read params for %q: %w", name, err)
	}
	m.UpsertJobParams(name, params)
	if err := m.Save(prof.Name); err != nil {
		return fmt.Errorf("save cache: %w", err)
	}
	job, _ = m.Lookup(name)

	return c.printGet(name, job)
}

// notFound builds a jenkins.ErrNotFound (exit 3) for an absent job, appending up to maxSuggestions
// close-name hints when any cached names look similar.
func (c *getCmd) notFound(m *cache.Map, name string) error {
	sugg := suggestNames(m, name)
	if len(sugg) == 0 {
		return fmt.Errorf("job %q: %w", name, jenkins.ErrNotFound)
	}
	return fmt.Errorf("job %q: %w (did you mean: %s)", name, jenkins.ErrNotFound, strings.Join(sugg, ", "))
}

// printGet writes the job details as JSON (--json) or a human-readable block.
func (c *getCmd) printGet(name string, job cache.Job) error {
	if c.app.global.JSON {
		enc := json.NewEncoder(c.app.stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			Name string `json:"name"`
			cache.Job
		}{Name: name, Job: job})
	}

	w := c.app.stdout
	fmt.Fprintf(w, "name:      %s\n", name)
	fmt.Fprintf(w, "path:      %s\n", job.Path)
	fmt.Fprintf(w, "class:     %s\n", job.Class)
	fmt.Fprintf(w, "buildable: %t\n", job.Buildable)
	if len(job.Params) == 0 {
		fmt.Fprintln(w, "params:    (none)")
		return nil
	}
	fmt.Fprintln(w, "params:")
	for _, p := range job.Params {
		fmt.Fprintf(w, "  %s (%s)", p.Name, p.Type)
		if p.Default != "" {
			fmt.Fprintf(w, " default=%s", p.Default)
		}
		if len(p.Choices) > 0 {
			fmt.Fprintf(w, " choices=[%s]", strings.Join(p.Choices, ", "))
		}
		fmt.Fprintln(w)
	}
	return nil
}

// suggestNames returns up to maxSuggestions cached job names that look close to name: case-
// insensitive substring matches in either direction (the query inside a name, or a name inside the
// query), sorted for stable output.
func suggestNames(m *cache.Map, name string) []string {
	q := strings.ToLower(name)
	var out []string
	for cand := range m.Jobs {
		lc := strings.ToLower(cand)
		if strings.Contains(lc, q) || strings.Contains(q, lc) {
			out = append(out, cand)
		}
	}
	sort.Strings(out)
	if len(out) > maxSuggestions {
		out = out[:maxSuggestions]
	}
	return out
}
