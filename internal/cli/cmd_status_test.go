package cli

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/avitsrimer/jcli/internal/cache"
	"github.com/avitsrimer/jcli/internal/jenkins"
)

// fixedClock returns a func usable as app.now so status elapsed is deterministic.
func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

// warmStatusCache primes a warm cache with a single pipeline job so resolveJob answers from disk
// without a crawl.
func warmStatusCache(t *testing.T) {
	t.Helper()
	m := &cache.Map{FetchedAt: time.Now().UTC(), URL: "https://jenkins.example.com", Jobs: map[string]cache.Job{
		"deploy-app": {Path: "/job/deploy-app", Class: "WorkflowJob", Buildable: true},
	}}
	require.NoError(t, m.Save("work"))
}

func TestStatus_ArgValidation(t *testing.T) {
	t.Run("too many args is a usage error", func(t *testing.T) {
		a, _, errBuf := readTestApp(t, &jenkinsClientMock{})
		code := a.run([]string{"status", "a", "1", "extra"})
		assert.Equal(t, exitUsage, code)
		assert.Contains(t, errBuf.String(), "too many arguments")
	})

	t.Run("--wait without a target is a usage error", func(t *testing.T) {
		a, _, errBuf := readTestApp(t, &jenkinsClientMock{})
		code := a.run([]string{"status", "--wait"})
		assert.Equal(t, exitUsage, code)
		assert.Contains(t, errBuf.String(), "--wait requires a job")
	})

	t.Run("non-numeric build number is a usage error", func(t *testing.T) {
		a, _, errBuf := readTestApp(t, &jenkinsClientMock{})
		warmStatusCache(t)
		code := a.run([]string{"status", "deploy-app", "notanumber"})
		assert.Equal(t, exitUsage, code)
		assert.Contains(t, errBuf.String(), "invalid build number")
	})
}

func TestStatus_RunningList(t *testing.T) {
	now := time.UnixMilli(1_000_000)

	t.Run("lists running builds sorted with elapsed", func(t *testing.T) {
		jc := &jenkinsClientMock{
			RunningBuildsFunc: func(context.Context) ([]jenkins.RunningBuild, error) {
				return []jenkins.RunningBuild{
					{Name: "Sales #17", Number: 17, URL: "https://jenkins.example.com/job/Sales/17/", Timestamp: now.Add(-45 * time.Second).UnixMilli()},
					{Name: "Logistics #42", Number: 42, URL: "https://jenkins.example.com/job/Logistics/42/", Timestamp: now.Add(-3 * time.Minute).UnixMilli()},
				}, nil
			},
		}
		a, out, _ := readTestApp(t, jc)
		a.now = fixedClock(now)

		code := a.run([]string{"status"})
		require.Equal(t, exitOK, code)
		s := out.String()
		assert.Contains(t, s, "Running:")
		assert.Contains(t, s, "Logistics #42")
		assert.Contains(t, s, "Sales #17")
		assert.Contains(t, s, "45.0s")
		// sorted by name → Logistics before Sales.
		assert.Less(t, indexOf(s, "Logistics"), indexOf(s, "Sales"))
		// name already carries the number; it must not be doubled.
		assert.NotContains(t, s, "#42 #42")
	})

	t.Run("nothing running says so", func(t *testing.T) {
		jc := &jenkinsClientMock{
			RunningBuildsFunc: func(context.Context) ([]jenkins.RunningBuild, error) { return nil, nil },
		}
		a, out, _ := readTestApp(t, jc)
		code := a.run([]string{"status"})
		require.Equal(t, exitOK, code)
		assert.Contains(t, out.String(), "no jobs currently running")
	})

	t.Run("json emits an array", func(t *testing.T) {
		jc := &jenkinsClientMock{
			RunningBuildsFunc: func(context.Context) ([]jenkins.RunningBuild, error) {
				return []jenkins.RunningBuild{{Name: "Logistics #42", Number: 42, URL: "u", Timestamp: 5}}, nil
			},
		}
		a, out, _ := readTestApp(t, jc)
		a.global.JSON = true
		code := a.run([]string{"status"})
		require.Equal(t, exitOK, code)

		var got []runningJSON
		require.NoError(t, json.Unmarshal(out.Bytes(), &got))
		require.Len(t, got, 1)
		assert.Equal(t, 42, got[0].Number)
		assert.Equal(t, "Logistics #42", got[0].Name)
	})
}

func TestStatus_Job(t *testing.T) {
	now := time.UnixMilli(10_000_000)

	t.Run("running job renders build and stages", func(t *testing.T) {
		jc := &jenkinsClientMock{
			LastBuildFunc: func(_ context.Context, jobPath string) (jenkins.Build, bool, error) {
				assert.Equal(t, "/job/deploy-app", jobPath)
				return jenkins.Build{Number: 42, URL: "https://jenkins.example.com/job/deploy-app/42/", Building: true, Timestamp: now.Add(-2 * time.Minute).UnixMilli()}, true, nil
			},
			StageViewFunc: func(context.Context, string) ([]jenkins.Stage, error) {
				return []jenkins.Stage{
					{Name: "Build", Status: "SUCCESS", DurationMillis: 64000},
					{Name: "Deploy", Status: "IN_PROGRESS"},
				}, nil
			},
		}
		a, out, _ := readTestApp(t, jc)
		a.now = fixedClock(now)
		warmStatusCache(t)

		code := a.run([]string{"status", "deploy-app"})
		require.Equal(t, exitOK, code)
		s := out.String()
		assert.Contains(t, s, "deploy-app #42  RUNNING  (elapsed 2m0s)")
		assert.Contains(t, s, "✓ Build (1m4s)")
		assert.Contains(t, s, "▶ Deploy")
	})

	t.Run("not-running job reports last result", func(t *testing.T) {
		jc := &jenkinsClientMock{
			LastBuildFunc: func(context.Context, string) (jenkins.Build, bool, error) {
				return jenkins.Build{Number: 41, Building: false, Result: "SUCCESS"}, true, nil
			},
			StageViewFunc: func(context.Context, string) ([]jenkins.Stage, error) {
				t.Fatal("StageView must not be called for a non-running job")
				return nil, nil
			},
		}
		a, out, _ := readTestApp(t, jc)
		warmStatusCache(t)

		code := a.run([]string{"status", "deploy-app"})
		require.Equal(t, exitOK, code)
		assert.Contains(t, out.String(), "deploy-app: not running (last build #41 SUCCESS)")
	})

	t.Run("never-built job says so", func(t *testing.T) {
		jc := &jenkinsClientMock{
			LastBuildFunc: func(context.Context, string) (jenkins.Build, bool, error) {
				return jenkins.Build{}, false, nil
			},
		}
		a, out, _ := readTestApp(t, jc)
		warmStatusCache(t)

		code := a.run([]string{"status", "deploy-app"})
		require.Equal(t, exitOK, code)
		assert.Contains(t, out.String(), "deploy-app: never built")
	})

	t.Run("unknown job is not-found", func(t *testing.T) {
		jc := &jenkinsClientMock{
			JobsFunc: func(context.Context) ([]jenkins.Job, error) { return sampleJobs(), nil },
		}
		a, _, errBuf := readTestApp(t, jc)
		code := a.run([]string{"status", "ghost"})
		assert.Equal(t, exitNotFound, code)
		assert.Contains(t, errBuf.String(), "not found")
	})
}

func TestStatus_BuildByNumber(t *testing.T) {
	t.Run("renders a finished build with stages", func(t *testing.T) {
		jc := &jenkinsClientMock{
			BuildStatusFunc: func(_ context.Context, buildURL string) (jenkins.Build, error) {
				assert.Equal(t, "https://jenkins.example.com/job/deploy-app/42/", buildURL)
				return jenkins.Build{Number: 42, URL: buildURL, Building: false, Result: "FAILURE", Timestamp: 5}, nil
			},
			StageViewFunc: func(context.Context, string) ([]jenkins.Stage, error) {
				return []jenkins.Stage{{Name: "Build", Status: "FAILED", DurationMillis: 2000}}, nil
			},
		}
		a, out, _ := readTestApp(t, jc)
		warmStatusCache(t)

		code := a.run([]string{"status", "deploy-app", "42"})
		require.Equal(t, exitOK, code, "status is informational; a FAILURE build still exits 0")
		s := out.String()
		assert.Contains(t, s, "deploy-app #42  FAILURE")
		assert.Contains(t, s, "✗ Build (2.0s)")
	})

	t.Run("freestyle build with no stages renders just the build line", func(t *testing.T) {
		jc := &jenkinsClientMock{
			BuildStatusFunc: func(_ context.Context, buildURL string) (jenkins.Build, error) {
				return jenkins.Build{Number: 3, URL: buildURL, Building: false, Result: "SUCCESS"}, nil
			},
			StageViewFunc: func(context.Context, string) ([]jenkins.Stage, error) { return nil, jenkins.ErrNotFound },
		}
		a, out, _ := readTestApp(t, jc)
		warmStatusCache(t)

		code := a.run([]string{"status", "deploy-app", "3"})
		require.Equal(t, exitOK, code)
		assert.Contains(t, out.String(), "deploy-app #3  SUCCESS")
	})

	t.Run("missing build number is not-found", func(t *testing.T) {
		jc := &jenkinsClientMock{
			BuildStatusFunc: func(context.Context, string) (jenkins.Build, error) {
				return jenkins.Build{}, jenkins.ErrNotFound
			},
		}
		a, _, errBuf := readTestApp(t, jc)
		warmStatusCache(t)

		code := a.run([]string{"status", "deploy-app", "999"})
		assert.Equal(t, exitNotFound, code)
		assert.Contains(t, errBuf.String(), "not found")
	})

	t.Run("json emits the build document", func(t *testing.T) {
		jc := &jenkinsClientMock{
			BuildStatusFunc: func(_ context.Context, buildURL string) (jenkins.Build, error) {
				return jenkins.Build{Number: 42, URL: buildURL, Building: false, Result: "SUCCESS", Timestamp: 5}, nil
			},
			StageViewFunc: func(context.Context, string) ([]jenkins.Stage, error) {
				return []jenkins.Stage{{Name: "Build", Status: "SUCCESS", DurationMillis: 2000}}, nil
			},
		}
		a, out, _ := readTestApp(t, jc)
		a.global.JSON = true
		warmStatusCache(t)

		code := a.run([]string{"status", "deploy-app", "42"})
		require.Equal(t, exitOK, code)

		var got statusJSON
		require.NoError(t, json.Unmarshal(out.Bytes(), &got))
		assert.Equal(t, "deploy-app", got.Job)
		assert.False(t, got.Running)
		require.NotNil(t, got.Build)
		assert.Equal(t, 42, got.Build.Number)
		require.Len(t, got.Stages, 1)
		assert.Equal(t, "Build", got.Stages[0].Name)
	})
}

func TestStatus_Logs(t *testing.T) {
	t.Run("--logs at job+number level dumps the console", func(t *testing.T) {
		jc := &jenkinsClientMock{
			ConsoleTextFunc: func(_ context.Context, buildURL string) (string, error) {
				assert.Equal(t, "https://jenkins.example.com/job/deploy-app/42/", buildURL)
				return "console for build 42\n", nil
			},
			StageViewFunc: func(context.Context, string) ([]jenkins.Stage, error) {
				t.Fatal("StageView must not be called with --logs")
				return nil, nil
			},
		}
		a, out, _ := readTestApp(t, jc)
		warmStatusCache(t)

		code := a.run([]string{"status", "deploy-app", "42", "--logs"})
		require.Equal(t, exitOK, code)
		assert.Contains(t, out.String(), "console for build 42")
	})

	t.Run("--logs with --wait follows the console", func(t *testing.T) {
		var polls atomic.Int32
		jc := &jenkinsClientMock{
			ConsoleProgressiveFunc: func(_ context.Context, _ string, start int64) (jenkins.ConsoleChunk, error) {
				if polls.Add(1) < 2 {
					return jenkins.ConsoleChunk{Text: "a\n", Size: 2, More: true}, nil
				}
				return jenkins.ConsoleChunk{Text: "b\n", Size: 4, More: false}, nil
			},
		}
		a, out, _ := readTestApp(t, jc)
		a.pollInterval = time.Millisecond
		warmStatusCache(t)

		code := a.run([]string{"status", "deploy-app", "42", "--wait", "--logs"})
		require.Equal(t, exitOK, code)
		assert.Equal(t, "a\nb\n", out.String())
	})

	t.Run("--logs with job only is a usage error", func(t *testing.T) {
		a, _, errBuf := readTestApp(t, &jenkinsClientMock{})
		warmStatusCache(t)
		code := a.run([]string{"status", "deploy-app", "--logs"})
		assert.Equal(t, exitUsage, code)
		assert.Contains(t, errBuf.String(), "--logs requires a job and build number")
	})

	t.Run("--logs with no args is a usage error", func(t *testing.T) {
		a, _, errBuf := readTestApp(t, &jenkinsClientMock{})
		code := a.run([]string{"status", "--logs"})
		assert.Equal(t, exitUsage, code)
		assert.Contains(t, errBuf.String(), "--logs requires a job and build number")
	})
}

func TestStatus_Wait(t *testing.T) {
	t.Run("follows a running build to terminal and renders final snapshot", func(t *testing.T) {
		var polls atomic.Int32
		jc := &jenkinsClientMock{
			BuildStatusFunc: func(_ context.Context, buildURL string) (jenkins.Build, error) {
				// building for the first two polls, then done.
				if polls.Add(1) < 3 {
					return jenkins.Build{Number: 42, URL: buildURL, Building: true, Timestamp: 5}, nil
				}
				return jenkins.Build{Number: 42, URL: buildURL, Building: false, Result: "SUCCESS", Timestamp: 5}, nil
			},
			StageViewFunc: func(context.Context, string) ([]jenkins.Stage, error) {
				i := int(polls.Load())
				if i < 3 {
					return []jenkins.Stage{{Name: "Build", Status: "IN_PROGRESS"}}, nil
				}
				return []jenkins.Stage{{Name: "Build", Status: "SUCCESS", DurationMillis: 3000}}, nil
			},
		}
		a, out, errBuf := readTestApp(t, jc)
		a.pollInterval = time.Millisecond
		warmStatusCache(t)

		// target is already building (LastBuild reports building), so --wait follows it.
		jc.LastBuildFunc = func(context.Context, string) (jenkins.Build, bool, error) {
			return jenkins.Build{Number: 42, URL: "https://jenkins.example.com/job/deploy-app/42/", Building: true, Timestamp: 5}, true, nil
		}

		code := a.run([]string{"status", "deploy-app", "--wait"})
		require.Equal(t, exitOK, code)
		// stage transitions streamed to stderr; final snapshot to stdout.
		assert.Contains(t, errBuf.String(), "▶ Build")
		assert.Contains(t, out.String(), "deploy-app #42  SUCCESS")
		assert.GreaterOrEqual(t, int(polls.Load()), 3)
	})

	t.Run("already-terminal target renders once, not an error", func(t *testing.T) {
		jc := &jenkinsClientMock{
			BuildStatusFunc: func(_ context.Context, buildURL string) (jenkins.Build, error) {
				return jenkins.Build{Number: 42, URL: buildURL, Building: false, Result: "SUCCESS", Timestamp: 5}, nil
			},
			StageViewFunc: func(context.Context, string) ([]jenkins.Stage, error) { return nil, jenkins.ErrNotFound },
		}
		a, out, _ := readTestApp(t, jc)
		a.pollInterval = time.Millisecond
		warmStatusCache(t)

		code := a.run([]string{"status", "deploy-app", "42", "--wait"})
		require.Equal(t, exitOK, code)
		assert.Contains(t, out.String(), "deploy-app #42  SUCCESS")
	})

	t.Run("json wait emits a single final document", func(t *testing.T) {
		var polls atomic.Int32
		jc := &jenkinsClientMock{
			BuildStatusFunc: func(_ context.Context, buildURL string) (jenkins.Build, error) {
				if polls.Add(1) < 2 {
					return jenkins.Build{Number: 42, URL: buildURL, Building: true, Timestamp: 5}, nil
				}
				return jenkins.Build{Number: 42, URL: buildURL, Building: false, Result: "SUCCESS", Timestamp: 5}, nil
			},
			StageViewFunc: func(context.Context, string) ([]jenkins.Stage, error) { return nil, jenkins.ErrNotFound },
			LastBuildFunc: func(context.Context, string) (jenkins.Build, bool, error) {
				return jenkins.Build{Number: 42, URL: "https://jenkins.example.com/job/deploy-app/42/", Building: true, Timestamp: 5}, true, nil
			},
		}
		a, out, _ := readTestApp(t, jc)
		a.pollInterval = time.Millisecond
		a.global.JSON = true
		warmStatusCache(t)

		code := a.run([]string{"status", "deploy-app", "--wait"})
		require.Equal(t, exitOK, code)

		// exactly one JSON document — a decoder reading the buffer sees EOF after the first value.
		dec := json.NewDecoder(out)
		var doc statusJSON
		require.NoError(t, dec.Decode(&doc))
		assert.False(t, doc.Running)
		_, err := dec.Token()
		assert.Error(t, err, "no second JSON document should be emitted")
	})
}

func TestStatus_Params(t *testing.T) {
	t.Run("human output renders header and aligned name = value block", func(t *testing.T) {
		jc := &jenkinsClientMock{
			BuildStatusFunc: func(_ context.Context, buildURL string) (jenkins.Build, error) {
				assert.Equal(t, "https://jenkins.example.com/job/deploy-app/42/", buildURL)
				return jenkins.Build{Number: 42, URL: buildURL, Building: false, Result: "SUCCESS", Timestamp: 5}, nil
			},
			BuildParamsFunc: func(_ context.Context, buildURL string) ([]jenkins.BuildParam, error) {
				assert.Equal(t, "https://jenkins.example.com/job/deploy-app/42/", buildURL)
				return []jenkins.BuildParam{
					{Name: "raven_branch", Value: "master"},
					{Name: "where_to_deploy", Value: "uat-2"},
				}, nil
			},
		}
		a, out, _ := readTestApp(t, jc)
		warmStatusCache(t)

		code := a.run([]string{"status", "deploy-app", "42", "--params"})
		require.Equal(t, exitOK, code)
		s := out.String()
		assert.Contains(t, s, "deploy-app #42  SUCCESS")
		assert.Contains(t, s, "params:")
		// the shorter name is padded to the width of the longest so the '=' columns align.
		assert.Contains(t, s, "  raven_branch    = master")
		assert.Contains(t, s, "  where_to_deploy = uat-2")
	})

	t.Run("json emits the {job, build, params} document", func(t *testing.T) {
		jc := &jenkinsClientMock{
			BuildStatusFunc: func(_ context.Context, buildURL string) (jenkins.Build, error) {
				return jenkins.Build{Number: 42, URL: buildURL, Building: false, Result: "SUCCESS", Timestamp: 5}, nil
			},
			BuildParamsFunc: func(context.Context, string) ([]jenkins.BuildParam, error) {
				return []jenkins.BuildParam{
					{Name: "raven_branch", Value: "master"},
					{Name: "where_to_deploy", Value: "uat-2"},
				}, nil
			},
		}
		a, out, _ := readTestApp(t, jc)
		a.global.JSON = true
		warmStatusCache(t)

		code := a.run([]string{"status", "deploy-app", "42", "--params"})
		require.Equal(t, exitOK, code)

		var got buildParamsJSON
		require.NoError(t, json.Unmarshal(out.Bytes(), &got))
		assert.Equal(t, "deploy-app", got.Job)
		require.NotNil(t, got.Build)
		assert.Equal(t, 42, got.Build.Number)
		assert.False(t, got.Build.Building)
		assert.Equal(t, "SUCCESS", got.Build.Result)
		assert.Equal(t, "https://jenkins.example.com/job/deploy-app/42/", got.Build.URL)
		assert.Equal(t, int64(5), got.Build.Timestamp)
		assert.Equal(t, map[string]string{"raven_branch": "master", "where_to_deploy": "uat-2"}, got.Params)
	})

	t.Run("param-less build renders (none)", func(t *testing.T) {
		jc := &jenkinsClientMock{
			BuildStatusFunc: func(_ context.Context, buildURL string) (jenkins.Build, error) {
				return jenkins.Build{Number: 3, URL: buildURL, Building: false, Result: "SUCCESS"}, nil
			},
			BuildParamsFunc: func(context.Context, string) ([]jenkins.BuildParam, error) { return nil, nil },
		}
		a, out, _ := readTestApp(t, jc)
		warmStatusCache(t)

		code := a.run([]string{"status", "deploy-app", "3", "--params"})
		require.Equal(t, exitOK, code)
		s := out.String()
		assert.Contains(t, s, "deploy-app #3  SUCCESS")
		assert.Contains(t, s, "params:    (none)")
	})

	t.Run("--wait is ignored: same single-shot output", func(t *testing.T) {
		var buildStatusCalls atomic.Int32
		jc := &jenkinsClientMock{
			BuildStatusFunc: func(_ context.Context, buildURL string) (jenkins.Build, error) {
				buildStatusCalls.Add(1)
				return jenkins.Build{Number: 42, URL: buildURL, Building: false, Result: "SUCCESS", Timestamp: 5}, nil
			},
			BuildParamsFunc: func(context.Context, string) ([]jenkins.BuildParam, error) {
				return []jenkins.BuildParam{{Name: "raven_branch", Value: "master"}}, nil
			},
		}
		a, out, _ := readTestApp(t, jc)
		a.pollInterval = time.Millisecond
		warmStatusCache(t)

		code := a.run([]string{"status", "deploy-app", "42", "--params", "--wait"})
		require.Equal(t, exitOK, code)
		s := out.String()
		assert.Contains(t, s, "deploy-app #42  SUCCESS")
		assert.Contains(t, s, "  raven_branch = master")
		// rendered once: BuildStatus fetched a single time, no polling loop.
		assert.Equal(t, int32(1), buildStatusCalls.Load())
	})

	t.Run("--params with job only is a usage error", func(t *testing.T) {
		a, _, errBuf := readTestApp(t, &jenkinsClientMock{})
		warmStatusCache(t)
		code := a.run([]string{"status", "deploy-app", "--params"})
		assert.Equal(t, exitUsage, code)
		assert.Contains(t, errBuf.String(), "--params requires a job and build number")
	})

	t.Run("--params combined with --logs is a usage error", func(t *testing.T) {
		a, _, errBuf := readTestApp(t, &jenkinsClientMock{})
		warmStatusCache(t)
		code := a.run([]string{"status", "deploy-app", "42", "--params", "--logs"})
		assert.Equal(t, exitUsage, code)
		assert.Contains(t, errBuf.String(), "--params and --logs are mutually exclusive")
	})

	t.Run("exclusion is checked before arity: job-only --params --logs still reports exclusion", func(t *testing.T) {
		// job-only (len==1) would pass the arity guard for either flag; the exclusion error proves
		// the mutual-exclusion guard runs first, not the arity guard.
		a, _, errBuf := readTestApp(t, &jenkinsClientMock{})
		warmStatusCache(t)
		code := a.run([]string{"status", "deploy-app", "--params", "--logs"})
		assert.Equal(t, exitUsage, code)
		assert.Contains(t, errBuf.String(), "--params and --logs are mutually exclusive")
		assert.NotContains(t, errBuf.String(), "requires a job and build number")
	})

	t.Run("running build renders a RUNNING header alongside params", func(t *testing.T) {
		now := time.UnixMilli(10_000_000)
		jc := &jenkinsClientMock{
			BuildStatusFunc: func(_ context.Context, buildURL string) (jenkins.Build, error) {
				return jenkins.Build{Number: 42, URL: buildURL, Building: true, Timestamp: now.Add(-2 * time.Minute).UnixMilli()}, nil
			},
			BuildParamsFunc: func(context.Context, string) ([]jenkins.BuildParam, error) {
				return []jenkins.BuildParam{{Name: "raven_branch", Value: "master"}}, nil
			},
		}
		a, out, _ := readTestApp(t, jc)
		a.now = fixedClock(now)
		warmStatusCache(t)

		code := a.run([]string{"status", "deploy-app", "42", "--params"})
		require.Equal(t, exitOK, code)
		s := out.String()
		assert.Contains(t, s, "deploy-app #42  RUNNING  (elapsed 2m0s)")
		assert.Contains(t, s, "  raven_branch = master")
	})

	t.Run("json for a param-less build emits a non-nil empty params map", func(t *testing.T) {
		jc := &jenkinsClientMock{
			BuildStatusFunc: func(_ context.Context, buildURL string) (jenkins.Build, error) {
				return jenkins.Build{Number: 3, URL: buildURL, Building: false, Result: "SUCCESS"}, nil
			},
			BuildParamsFunc: func(context.Context, string) ([]jenkins.BuildParam, error) { return nil, nil },
		}
		a, out, _ := readTestApp(t, jc)
		a.global.JSON = true
		warmStatusCache(t)

		code := a.run([]string{"status", "deploy-app", "3", "--params"})
		require.Equal(t, exitOK, code)
		// the JSON must carry "params": {} (empty object), never null, so consumers can index it.
		assert.Contains(t, out.String(), `"params": {}`)

		var got buildParamsJSON
		require.NoError(t, json.Unmarshal(out.Bytes(), &got))
		assert.NotNil(t, got.Params)
		assert.Empty(t, got.Params)
	})

	t.Run("missing build is not-found (exit 3)", func(t *testing.T) {
		jc := &jenkinsClientMock{
			BuildStatusFunc: func(context.Context, string) (jenkins.Build, error) {
				return jenkins.Build{}, jenkins.ErrNotFound
			},
		}
		a, _, errBuf := readTestApp(t, jc)
		warmStatusCache(t)

		code := a.run([]string{"status", "deploy-app", "999", "--params"})
		assert.Equal(t, exitNotFound, code)
		assert.Contains(t, errBuf.String(), "not found")
	})
}

// indexOf is a tiny helper for ordering assertions in rendered output.
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
