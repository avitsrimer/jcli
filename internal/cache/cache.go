// Package cache persists a per-profile Jenkins job map to ~/.cache/jcli/<profile>/jobs.json.
// the map records each job's path, class, buildability, and (lazily filled) parameter
// definitions so list/get/build can answer from disk and avoid re-crawling. all writes are
// atomic (temp+rename) with 0600 perms and a 0700 parent directory, mirroring the config
// package; param defs reuse jenkins.Param so the wire and cache shapes stay aligned.
package cache

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/avitsrimer/jcli/internal/jenkins"
)

// Job is one cached entry. Path is the Jenkins URL path used to address the job (e.g.
// "/job/Folder/job/Child"); Class is the raw Jenkins _class; Params is the lazily filled
// parameter set and ParamsFetchedAt is its zero value until a live read populates it.
type Job struct {
	Path            string          `json:"path"`
	Class           string          `json:"class"`
	Buildable       bool            `json:"buildable"`
	Params          []jenkins.Param `json:"params,omitempty"`
	ParamsFetchedAt time.Time       `json:"params_fetched_at,omitempty"`
}

// Map is the on-disk document: when the list was last crawled, the server URL it came from,
// and the jobs keyed by their full folder-aware name.
type Map struct {
	FetchedAt time.Time      `json:"fetched_at"`
	URL       string         `json:"url"`
	Jobs      map[string]Job `json:"jobs"`
}

// Path returns the cache file location for a profile, honoring XDG_CACHE_HOME and falling
// back to ~/.cache/jcli/<profile>/jobs.json.
func Path(profile string) (string, error) {
	base := os.Getenv("XDG_CACHE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home dir: %w", err)
		}
		base = filepath.Join(home, ".cache")
	}
	return filepath.Join(base, "jcli", profile, "jobs.json"), nil
}

// Load reads and parses the cache for a profile. A missing file yields an empty Map (not an
// error) so a cold cache behaves like an empty one without special-casing.
func Load(profile string) (*Map, error) {
	path, err := Path(profile)
	if err != nil {
		return nil, err
	}
	return loadFrom(path)
}

// loadFrom is the testable core of Load, reading an explicit path.
func loadFrom(path string) (*Map, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Map{Jobs: map[string]Job{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read cache %s: %w", path, err)
	}

	var m Map
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse cache %s: %w", path, err)
	}
	if m.Jobs == nil {
		m.Jobs = map[string]Job{}
	}
	return &m, nil
}

// Save atomically persists the cache for a profile, creating the parent dir with 0700.
func (m *Map) Save(profile string) error {
	path, err := Path(profile)
	if err != nil {
		return err
	}
	return m.saveTo(path)
}

// saveTo is the testable core of Save, writing to an explicit path via temp+rename.
func (m *Map) saveTo(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create cache dir %s: %w", dir, err)
	}

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode cache: %w", err)
	}

	tmp, err := os.CreateTemp(dir, "jobs-*.json.tmp")
	if err != nil {
		return fmt.Errorf("create temp cache: %w", err)
	}
	tmpName := tmp.Name()
	// best-effort cleanup if we bail before the rename succeeds.
	defer func() { _ = os.Remove(tmpName) }()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp cache: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp cache: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp cache: %w", err)
	}

	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename temp cache to %s: %w", path, err)
	}
	return nil
}

// Lookup returns the cached job by its full name and whether it was present.
func (m *Map) Lookup(name string) (Job, bool) {
	job, ok := m.Jobs[name]
	return job, ok
}

// UpsertJobParams stores the live parameter set for a job and stamps ParamsFetchedAt to now.
// A missing job is created with just the params; callers that know the path/class should set
// those via Rebuild first. It returns false when the named job is absent so the caller can
// decide whether to crawl.
func (m *Map) UpsertJobParams(name string, params []jenkins.Param) bool {
	job, ok := m.Jobs[name]
	if !ok {
		return false
	}
	job.Params = params
	job.ParamsFetchedAt = time.Now().UTC()
	if m.Jobs == nil {
		m.Jobs = map[string]Job{}
	}
	m.Jobs[name] = job
	return true
}

// IsStale reports whether the list crawl is older than ttl. A zero FetchedAt (never crawled)
// is always stale.
func (m *Map) IsStale(ttl time.Duration) bool {
	if m.FetchedAt.IsZero() {
		return true
	}
	return time.Since(m.FetchedAt) > ttl
}

// crawlFunc enumerates the live job tree; Rebuild calls it to repopulate the map.
type crawlFunc func() ([]jenkins.Job, error)

// Rebuild replaces the job list from a fresh crawl, preserving previously fetched param sets
// for jobs that still exist (so a list refresh does not discard live param reads). It stamps
// FetchedAt to now and records the server URL.
func (m *Map) Rebuild(url string, crawl crawlFunc) error {
	tree, err := crawl()
	if err != nil {
		return fmt.Errorf("crawl jobs: %w", err)
	}

	prev := m.Jobs
	jobs := map[string]Job{}
	flatten(url, "", "", tree, jobs)
	// carry forward params/timestamps for jobs that survived the crawl.
	for name, job := range jobs {
		if old, ok := prev[name]; ok {
			job.Params = old.Params
			job.ParamsFetchedAt = old.ParamsFetchedAt
			jobs[name] = job
		}
	}

	m.Jobs = jobs
	m.URL = url
	m.FetchedAt = time.Now().UTC()
	return nil
}

// flatten walks the recursive job tree into the flat, folder-aware map. namePrefix builds the
// full job name ("Folder/Child"); the addressable Jenkins path is derived from the job's own URL
// (stripping baseURL) so it preserves Jenkins' URL-encoding of names with spaces or special
// characters, falling back to a reconstructed "<pathPrefix>/job/<name>" only when the URL is
// absent or unparseable.
func flatten(baseURL, namePrefix, pathPrefix string, jobs []jenkins.Job, out map[string]Job) {
	for _, j := range jobs {
		name := j.Name
		if namePrefix != "" {
			name = namePrefix + "/" + j.Name
		}
		path := jobPath(baseURL, j.URL)
		if path == "" {
			path = pathPrefix + "/job/" + j.Name
		}

		out[name] = Job{Path: path, Class: j.Class, Buildable: j.Buildable}
		if len(j.Jobs) > 0 {
			flatten(baseURL, name, path, j.Jobs, out)
		}
	}
}

// jobPath derives the baseURL-relative, trailing-slash-trimmed path from a job's absolute URL.
// It returns "" when jobURL is empty or not under baseURL so the caller can fall back to a
// reconstructed path.
func jobPath(baseURL, jobURL string) string {
	if jobURL == "" {
		return ""
	}
	rel := strings.TrimPrefix(jobURL, strings.TrimRight(baseURL, "/"))
	if rel == jobURL {
		// jobURL was not under baseURL; signal a fallback rather than emit a wrong path.
		return ""
	}
	return "/" + strings.Trim(rel, "/")
}
