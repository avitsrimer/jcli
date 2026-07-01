package cli

import (
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/avitsrimer/jcli/internal/cache"
)

// listStaleTTL is the age past which the list command prints a hint suggesting --refresh; it never
// blocks, it just nudges. 24h matches the design's staleness window.
const listStaleTTL = 24 * time.Hour

// runList loads the per-profile cached job map, crawling it fresh when the cache is cold (never
// crawled / empty) or when --refresh is set, then prints the job names matching pattern. With
// --json it emits a JSON array of names; otherwise one name per line. A stale (but non-empty) cache
// prints a refresh hint to stderr without blocking.
func (c *listCmd) runList(pattern string) error {
	prof, client, err := c.app.clientFor()
	if err != nil {
		return err
	}
	m, err := cache.Load(prof.Name)
	if err != nil {
		return fmt.Errorf("load cache: %w", err)
	}

	cold := len(m.Jobs) == 0 || m.FetchedAt.IsZero()
	if c.Refresh || cold {
		if err := c.app.crawlAndSave(m, client, prof); err != nil {
			return err
		}
	} else if m.IsStale(listStaleTTL) {
		// non-blocking hint: the cached list is older than the TTL.
		fmt.Fprintln(c.app.stderr, "jcli: cached job list is stale; run with --refresh to update")
	}

	names := filterJobs(m, pattern)
	return c.printList(names)
}

// printList writes the matched names as a JSON array (--json) or one per line.
func (c *listCmd) printList(names []string) error {
	if c.app.global.JSON {
		enc := json.NewEncoder(c.app.stdout)
		enc.SetIndent("", "  ")
		if names == nil {
			names = []string{}
		}
		return enc.Encode(names)
	}
	for _, n := range names {
		fmt.Fprintln(c.app.stdout, n)
	}
	return nil
}

// filterJobs returns the sorted job names matching pattern. An empty pattern matches everything. A
// pattern containing glob metacharacters is matched with path.Match semantics against the job name;
// otherwise it is a case-insensitive substring match. Glob patterns that fail to match (or are
// malformed) fall back to substring so a stray bracket never silently drops everything.
func filterJobs(m *cache.Map, pattern string) []string {
	names := make([]string, 0, len(m.Jobs))
	for name := range m.Jobs {
		if matchJob(name, pattern) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// matchJob reports whether name matches pattern (see filterJobs for the rules).
func matchJob(name, pattern string) bool {
	if pattern == "" {
		return true
	}
	if strings.ContainsAny(pattern, "*?[") {
		if ok, err := path.Match(pattern, name); err == nil && ok {
			return true
		}
	}
	return strings.Contains(strings.ToLower(name), strings.ToLower(pattern))
}
