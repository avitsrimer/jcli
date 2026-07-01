package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/avitsrimer/jcli/internal/cache"
	"github.com/avitsrimer/jcli/internal/config"
	"github.com/avitsrimer/jcli/internal/jenkins"
)

// readTestApp builds an app wired to a temp cache dir (XDG_CACHE_HOME), a single "work" profile, a
// creds stub serving a fixed token, and the given jenkins mock. It returns the app plus its buffers.
func readTestApp(t *testing.T, jc *jenkinsClientMock) (*app, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("JCLI_PROFILE", "")
	var out, errBuf bytes.Buffer
	a := &app{
		cfg: &config.Config{
			Default:  "work",
			Profiles: []config.Profile{{Name: "work", URL: "https://jenkins.example.com", Username: "alice"}},
		},
		creds:  &fakeCreds{},
		stdout: &out,
		stderr: &errBuf,
		global: &globalOpts{},
		factory: func(_, _, _ string) jenkinsClient {
			return jc
		},
	}
	return a, &out, &errBuf
}

// sampleJobs is the crawl tree returned by mocks: a top-level buildable job and a folder with a
// nested child, so folder-aware flat keying ("Folder/Child") is exercised.
func sampleJobs() []jenkins.Job {
	return []jenkins.Job{
		{Name: "deploy-app", Class: "WorkflowJob", Buildable: true},
		{Name: "Folder", Class: "Folder", Jobs: []jenkins.Job{
			{Name: "child-job", Class: "WorkflowJob", Buildable: true},
		}},
	}
}

func TestList(t *testing.T) {
	t.Run("cold cache crawls and lists all", func(t *testing.T) {
		jc := &jenkinsClientMock{JobsFunc: func(context.Context) ([]jenkins.Job, error) { return sampleJobs(), nil }}
		a, out, _ := readTestApp(t, jc)

		code := a.run([]string{"list"})
		require.Equal(t, exitOK, code)
		assert.Len(t, jc.JobsCalls(), 1, "cold cache must crawl exactly once")
		s := out.String()
		assert.Contains(t, s, "deploy-app")
		assert.Contains(t, s, "Folder/child-job")

		// cache was persisted: a fresh load sees the jobs.
		m, err := cache.Load("work")
		require.NoError(t, err)
		assert.Len(t, m.Jobs, 3)
	})

	t.Run("filter by substring", func(t *testing.T) {
		jc := &jenkinsClientMock{JobsFunc: func(context.Context) ([]jenkins.Job, error) { return sampleJobs(), nil }}
		a, out, _ := readTestApp(t, jc)

		code := a.run([]string{"list", "app"})
		require.Equal(t, exitOK, code)
		s := out.String()
		assert.Contains(t, s, "deploy-app")
		assert.NotContains(t, s, "child-job")
	})

	t.Run("filter by glob", func(t *testing.T) {
		jc := &jenkinsClientMock{JobsFunc: func(context.Context) ([]jenkins.Job, error) { return sampleJobs(), nil }}
		a, out, _ := readTestApp(t, jc)

		code := a.run([]string{"list", "Folder/*"})
		require.Equal(t, exitOK, code)
		s := out.String()
		assert.Contains(t, s, "Folder/child-job")
		assert.NotContains(t, s, "deploy-app")
	})

	t.Run("json output", func(t *testing.T) {
		jc := &jenkinsClientMock{JobsFunc: func(context.Context) ([]jenkins.Job, error) { return sampleJobs(), nil }}
		a, out, _ := readTestApp(t, jc)

		code := a.run([]string{"--json", "list", "app"})
		require.Equal(t, exitOK, code)
		var names []string
		require.NoError(t, json.Unmarshal(out.Bytes(), &names))
		assert.Equal(t, []string{"deploy-app"}, names)
	})

	t.Run("refresh forces a crawl over a warm cache", func(t *testing.T) {
		jc := &jenkinsClientMock{JobsFunc: func(context.Context) ([]jenkins.Job, error) { return sampleJobs(), nil }}
		a, _, _ := readTestApp(t, jc)

		// prime a warm cache so a plain list would not crawl.
		m := &cache.Map{FetchedAt: time.Now().UTC(), URL: "https://jenkins.example.com", Jobs: map[string]cache.Job{
			"old-job": {Path: "/job/old-job"},
		}}
		require.NoError(t, m.Save("work"))

		code := a.run([]string{"list", "--refresh"})
		require.Equal(t, exitOK, code)
		assert.Len(t, jc.JobsCalls(), 1, "--refresh must crawl even with a warm cache")
	})

	t.Run("stale warm cache prints a non-blocking hint and does not crawl", func(t *testing.T) {
		jc := &jenkinsClientMock{} // JobsFunc unset: a crawl would panic, proving none happens.
		a, out, errBuf := readTestApp(t, jc)

		stale := &cache.Map{FetchedAt: time.Now().Add(-48 * time.Hour), URL: "https://jenkins.example.com", Jobs: map[string]cache.Job{
			"deploy-app": {Path: "/job/deploy-app", Buildable: true},
		}}
		require.NoError(t, stale.Save("work"))

		code := a.run([]string{"list"})
		require.Equal(t, exitOK, code)
		assert.Contains(t, errBuf.String(), "--refresh")
		assert.Contains(t, out.String(), "deploy-app")
	})
}

func TestGet(t *testing.T) {
	params := []jenkins.Param{
		{Name: "BRANCH", Type: "String", Default: "master"},
		{Name: "ENV", Type: "Choice", Choices: []string{"uat1", "uat2"}, Default: "uat1"},
	}

	t.Run("live read updates the cache", func(t *testing.T) {
		jc := &jenkinsClientMock{
			JobParamsFunc: func(_ context.Context, jobPath string) ([]jenkins.Param, error) {
				assert.Equal(t, "/job/deploy-app", jobPath)
				return params, nil
			},
		}
		a, out, _ := readTestApp(t, jc)
		// warm cache so no crawl is needed.
		m := &cache.Map{FetchedAt: time.Now().UTC(), Jobs: map[string]cache.Job{
			"deploy-app": {Path: "/job/deploy-app", Class: "WorkflowJob", Buildable: true},
		}}
		require.NoError(t, m.Save("work"))

		code := a.run([]string{"get", "deploy-app"})
		require.Equal(t, exitOK, code)
		assert.Len(t, jc.JobParamsCalls(), 1)
		assert.Contains(t, out.String(), "BRANCH")
		assert.Contains(t, out.String(), "choices=[uat1, uat2]")

		// params persisted to cache with a fetch timestamp.
		reloaded, err := cache.Load("work")
		require.NoError(t, err)
		j, ok := reloaded.Lookup("deploy-app")
		require.True(t, ok)
		assert.Len(t, j.Params, 2)
		assert.False(t, j.ParamsFetchedAt.IsZero())
	})

	t.Run("miss triggers one crawl then retry succeeds", func(t *testing.T) {
		jc := &jenkinsClientMock{
			JobsFunc:      func(context.Context) ([]jenkins.Job, error) { return sampleJobs(), nil },
			JobParamsFunc: func(context.Context, string) ([]jenkins.Param, error) { return params, nil },
		}
		a, out, _ := readTestApp(t, jc) // cold cache → lookup miss → crawl → retry hit.

		code := a.run([]string{"get", "deploy-app"})
		require.Equal(t, exitOK, code)
		assert.Len(t, jc.JobsCalls(), 1, "exactly one crawl on the miss")
		assert.Len(t, jc.JobParamsCalls(), 1)
		assert.Contains(t, out.String(), "deploy-app")
	})

	t.Run("json output", func(t *testing.T) {
		jc := &jenkinsClientMock{
			JobParamsFunc: func(context.Context, string) ([]jenkins.Param, error) { return params, nil },
		}
		a, out, _ := readTestApp(t, jc)
		m := &cache.Map{FetchedAt: time.Now().UTC(), Jobs: map[string]cache.Job{
			"deploy-app": {Path: "/job/deploy-app", Class: "WorkflowJob", Buildable: true},
		}}
		require.NoError(t, m.Save("work"))

		code := a.run([]string{"--json", "get", "deploy-app"})
		require.Equal(t, exitOK, code)
		var got map[string]any
		require.NoError(t, json.Unmarshal(out.Bytes(), &got))
		assert.Equal(t, "deploy-app", got["name"])
		assert.Equal(t, "/job/deploy-app", got["path"])
	})

	t.Run("unknown job after crawl is not-found with suggestions", func(t *testing.T) {
		jc := &jenkinsClientMock{
			JobsFunc: func(context.Context) ([]jenkins.Job, error) { return sampleJobs(), nil },
		}
		a, _, errBuf := readTestApp(t, jc)

		// "deploy" is a substring of "deploy-app" → that name is suggested.
		code := a.run([]string{"get", "deploy"})
		assert.Equal(t, exitNotFound, code)
		assert.Len(t, jc.JobsCalls(), 1, "one crawl before giving up")
		assert.Empty(t, jc.JobParamsCalls(), "no param read for a missing job")
		s := errBuf.String()
		assert.Contains(t, s, "did you mean")
		assert.Contains(t, s, "deploy-app")
	})

	t.Run("missing job name is a usage error", func(t *testing.T) {
		a, _, _ := readTestApp(t, &jenkinsClientMock{})
		code := a.run([]string{"get"})
		assert.Equal(t, exitUsage, code)
	})
}

func TestDump(t *testing.T) {
	t.Run("dumps the cached map as formatted JSON", func(t *testing.T) {
		jc := &jenkinsClientMock{} // no crawl without --refresh.
		a, out, _ := readTestApp(t, jc)
		m := &cache.Map{FetchedAt: time.Now().UTC(), URL: "https://jenkins.example.com", Jobs: map[string]cache.Job{
			"deploy-app": {Path: "/job/deploy-app", Class: "WorkflowJob", Buildable: true},
		}}
		require.NoError(t, m.Save("work"))

		code := a.run([]string{"dump"})
		require.Equal(t, exitOK, code)
		assert.Empty(t, jc.JobsCalls(), "dump without --refresh must not crawl")

		var got cache.Map
		require.NoError(t, json.Unmarshal(out.Bytes(), &got))
		assert.Equal(t, "https://jenkins.example.com", got.URL)
		require.Contains(t, got.Jobs, "deploy-app")
		assert.Contains(t, out.String(), "  ", "output is indented")
	})

	t.Run("refresh rebuilds via crawl first", func(t *testing.T) {
		jc := &jenkinsClientMock{JobsFunc: func(context.Context) ([]jenkins.Job, error) { return sampleJobs(), nil }}
		a, out, _ := readTestApp(t, jc)

		code := a.run([]string{"dump", "--refresh"})
		require.Equal(t, exitOK, code)
		assert.Len(t, jc.JobsCalls(), 1)
		var got cache.Map
		require.NoError(t, json.Unmarshal(out.Bytes(), &got))
		assert.Contains(t, got.Jobs, "deploy-app")
		assert.Contains(t, got.Jobs, "Folder/child-job")
	})

	t.Run("empty cache dumps a valid empty structure", func(t *testing.T) {
		jc := &jenkinsClientMock{} // cold cache, no --refresh → no crawl.
		a, out, _ := readTestApp(t, jc)

		code := a.run([]string{"dump"})
		require.Equal(t, exitOK, code)
		var got cache.Map
		require.NoError(t, json.Unmarshal(out.Bytes(), &got))
		assert.NotNil(t, got.Jobs)
		assert.Empty(t, got.Jobs)
	})
}
