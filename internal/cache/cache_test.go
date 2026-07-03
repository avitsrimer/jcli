package cache

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/avitsrimer/jcli/internal/jenkins"
)

func TestPath(t *testing.T) {
	tests := []struct {
		name    string
		xdg     string
		profile string
		want    string
	}{
		{name: "xdg set", xdg: "/tmp/xdg", profile: "work", want: "/tmp/xdg/jcli/work/jobs.json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("XDG_CACHE_HOME", tt.xdg)
			got, err := Path(tt.profile)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestPathDefaultHome(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "")
	got, err := Path("work")
	require.NoError(t, err)
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, ".cache", "jcli", "work", "jobs.json"), got)
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "jobs.json")
	m := &Map{
		FetchedAt: time.Date(2026, 6, 22, 13, 0, 0, 0, time.UTC),
		URL:       "https://jenkins.example.com",
		Jobs: map[string]Job{
			"Logistics": {
				Path:      "/job/Logistics",
				Class:     "WorkflowJob",
				Buildable: true,
				Params: []jenkins.Param{
					{Name: "service", Type: "Choice", Choices: []string{"a", "b"}},
				},
				ParamsFetchedAt: time.Date(2026, 6, 22, 13, 5, 0, 0, time.UTC),
			},
		},
	}
	require.NoError(t, m.saveTo(path))

	got, err := loadFrom(path)
	require.NoError(t, err)
	assert.Equal(t, m.URL, got.URL)
	assert.True(t, m.FetchedAt.Equal(got.FetchedAt))
	require.Contains(t, got.Jobs, "Logistics")
	assert.Equal(t, m.Jobs["Logistics"], got.Jobs["Logistics"])
}

func TestSavePerms(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "jobs.json")
	m := &Map{Jobs: map[string]Job{}}
	require.NoError(t, m.saveTo(path))

	fi, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())

	di, err := os.Stat(filepath.Dir(path))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), di.Mode().Perm())
}

func TestSaveAtomicNoTempLeft(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.json")
	m := &Map{Jobs: map[string]Job{"x": {Path: "/job/x"}}}
	require.NoError(t, m.saveTo(path))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "jobs.json", entries[0].Name())
}

func TestLookup(t *testing.T) {
	m := &Map{Jobs: map[string]Job{"Logistics": {Path: "/job/Logistics", Buildable: true}}}

	got, ok := m.Lookup("Logistics")
	assert.True(t, ok)
	assert.Equal(t, "/job/Logistics", got.Path)

	_, ok = m.Lookup("Missing")
	assert.False(t, ok)
}

func TestUpsertJobParams(t *testing.T) {
	t.Run("updates existing and stamps timestamp", func(t *testing.T) {
		m := &Map{Jobs: map[string]Job{"Logistics": {Path: "/job/Logistics", Class: "WorkflowJob"}}}
		params := []jenkins.Param{{Name: "stage", Type: "Choice", Choices: []string{"uat1"}}}

		before := time.Now().UTC()
		ok := m.UpsertJobParams("Logistics", params)
		require.True(t, ok)

		job := m.Jobs["Logistics"]
		assert.Equal(t, params, job.Params)
		assert.Equal(t, "WorkflowJob", job.Class, "preserves other fields")
		assert.False(t, job.ParamsFetchedAt.Before(before), "timestamp moved forward")
	})

	t.Run("absent job returns false", func(t *testing.T) {
		m := &Map{Jobs: map[string]Job{}}
		ok := m.UpsertJobParams("Nope", nil)
		assert.False(t, ok)
		assert.NotContains(t, m.Jobs, "Nope")
	})
}

func TestIsStale(t *testing.T) {
	ttl := 24 * time.Hour
	tests := []struct {
		name      string
		fetchedAt time.Time
		want      bool
	}{
		{name: "never crawled", fetchedAt: time.Time{}, want: true},
		{name: "fresh", fetchedAt: time.Now(), want: false},
		{name: "just inside ttl", fetchedAt: time.Now().Add(-ttl + time.Minute), want: false},
		{name: "just past ttl", fetchedAt: time.Now().Add(-ttl - time.Minute), want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &Map{FetchedAt: tt.fetchedAt}
			assert.Equal(t, tt.want, m.IsStale(ttl))
		})
	}
}

func TestRebuildFlattensNestedJobs(t *testing.T) {
	m := &Map{Jobs: map[string]Job{}}
	crawl := func() ([]jenkins.Job, error) {
		return []jenkins.Job{
			{Name: "Logistics", Class: "WorkflowJob", Buildable: true},
			{
				Name:  "Folder",
				Class: "Folder",
				Jobs: []jenkins.Job{
					{Name: "Child", Class: "WorkflowJob", Buildable: true},
				},
			},
		}, nil
	}

	require.NoError(t, m.Rebuild("https://jenkins.example.com", crawl))

	assert.Equal(t, "https://jenkins.example.com", m.URL)
	assert.False(t, m.FetchedAt.IsZero())
	require.Contains(t, m.Jobs, "Logistics")
	assert.Equal(t, "/job/Logistics", m.Jobs["Logistics"].Path)
	require.Contains(t, m.Jobs, "Folder/Child")
	assert.Equal(t, "/job/Folder/job/Child", m.Jobs["Folder/Child"].Path)
	assert.True(t, m.Jobs["Folder/Child"].Buildable)
}

func TestRebuildDerivesPathFromURL(t *testing.T) {
	// when the crawl returns the job's own URL, Path is derived from it (stripping baseURL) so it
	// preserves Jenkins' URL-encoding of names with spaces rather than reconstructing from the raw name.
	m := &Map{Jobs: map[string]Job{}}
	crawl := func() ([]jenkins.Job, error) {
		return []jenkins.Job{
			{
				Name: "My Folder",
				URL:  "https://jenkins.example.com/job/My%20Folder/",
				Jobs: []jenkins.Job{
					{Name: "Deploy App", URL: "https://jenkins.example.com/job/My%20Folder/job/Deploy%20App/", Buildable: true},
				},
			},
		}, nil
	}

	require.NoError(t, m.Rebuild("https://jenkins.example.com", crawl))

	require.Contains(t, m.Jobs, "My Folder")
	assert.Equal(t, "/job/My%20Folder", m.Jobs["My Folder"].Path)
	require.Contains(t, m.Jobs, "My Folder/Deploy App")
	assert.Equal(t, "/job/My%20Folder/job/Deploy%20App", m.Jobs["My Folder/Deploy App"].Path,
		"path is taken from the encoded URL, not reconstructed from the raw name")
}

func TestRebuildPreservesParams(t *testing.T) {
	stamp := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m := &Map{Jobs: map[string]Job{
		"Logistics": {
			Path:            "/job/Logistics",
			Params:          []jenkins.Param{{Name: "stage", Type: "Choice"}},
			ParamsFetchedAt: stamp,
		},
		"Gone": {Path: "/job/Gone"},
	}}
	crawl := func() ([]jenkins.Job, error) {
		return []jenkins.Job{{Name: "Logistics", Class: "WorkflowJob", Buildable: true}}, nil
	}

	require.NoError(t, m.Rebuild("https://jenkins.example.com", crawl))

	job := m.Jobs["Logistics"]
	require.Len(t, job.Params, 1)
	assert.Equal(t, "stage", job.Params[0].Name)
	assert.True(t, job.ParamsFetchedAt.Equal(stamp), "carried timestamp forward")
	assert.NotContains(t, m.Jobs, "Gone", "stale job dropped")
}

func TestRebuildCrawlError(t *testing.T) {
	m := &Map{Jobs: map[string]Job{"keep": {Path: "/job/keep"}}}
	crawl := func() ([]jenkins.Job, error) {
		return nil, assert.AnError
	}

	err := m.Rebuild("https://jenkins.example.com", crawl)
	require.Error(t, err)
	require.ErrorIs(t, err, assert.AnError)
	assert.Contains(t, m.Jobs, "keep", "map left intact on crawl failure")
}

func TestLoadCorruptCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	require.NoError(t, os.WriteFile(path, []byte("{not json"), 0o600))

	_, err := loadFrom(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse cache")
}

func TestLoadMissingFileEmptyMap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.json")
	m, err := loadFrom(path)
	require.NoError(t, err)
	require.NotNil(t, m.Jobs)
	assert.Empty(t, m.Jobs)
	assert.True(t, m.IsStale(24*time.Hour), "cold cache is stale")
}

func TestLoadEmptyJobsNormalized(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"url":"https://x","jobs":null}`), 0o600))

	m, err := loadFrom(path)
	require.NoError(t, err)
	require.NotNil(t, m.Jobs, "nil jobs normalized to empty map")
	_, ok := m.Lookup("anything")
	assert.False(t, ok)
}
