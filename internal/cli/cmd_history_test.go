package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/avitsrimer/jcli/internal/jenkins"
)

func TestHumanizeSince(t *testing.T) {
	// fixed reference point so all deltas are deterministic; buckets are <60s just now, <60m Nm ago,
	// <24h Nh ago, else Nd ago, with zero/future timestamps also rendering just now.
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	ms := func(d time.Duration) int64 { return now.Add(-d).UnixMilli() }

	tests := []struct {
		name    string
		tsMilli int64
		want    string
	}{
		{name: "zero timestamp", tsMilli: 0, want: "just now"},
		{name: "negative timestamp", tsMilli: -1, want: "just now"},
		{name: "future timestamp", tsMilli: now.Add(time.Hour).UnixMilli(), want: "just now"},
		{name: "just started", tsMilli: ms(0), want: "just now"},
		{name: "30s ago", tsMilli: ms(30 * time.Second), want: "just now"},
		{name: "59s ago", tsMilli: ms(59 * time.Second), want: "just now"},
		{name: "60s ago", tsMilli: ms(60 * time.Second), want: "1m ago"},
		{name: "5m ago", tsMilli: ms(5 * time.Minute), want: "5m ago"},
		{name: "59m ago", tsMilli: ms(59 * time.Minute), want: "59m ago"},
		{name: "60m ago", tsMilli: ms(60 * time.Minute), want: "1h ago"},
		{name: "2h ago", tsMilli: ms(2 * time.Hour), want: "2h ago"},
		{name: "23h ago", tsMilli: ms(23 * time.Hour), want: "23h ago"},
		{name: "24h ago", tsMilli: ms(24 * time.Hour), want: "1d ago"},
		{name: "3d ago", tsMilli: ms(3 * 24 * time.Hour), want: "3d ago"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, humanizeSince(now, tc.tsMilli))
		})
	}
}

func TestHistory_ArgValidation(t *testing.T) {
	t.Run("missing job arg is a usage error", func(t *testing.T) {
		a, _, errBuf := readTestApp(t, &jenkinsClientMock{})
		code := a.run([]string{"history"})
		assert.Equal(t, exitUsage, code)
		assert.Contains(t, errBuf.String(), "job name required")
	})

	t.Run("count of zero is a usage error", func(t *testing.T) {
		a, _, errBuf := readTestApp(t, &jenkinsClientMock{})
		warmStatusCache(t)
		code := a.run([]string{"history", "deploy-app", "-n", "0"})
		assert.Equal(t, exitUsage, code)
		assert.Contains(t, errBuf.String(), "count")
	})

	t.Run("negative count is a usage error", func(t *testing.T) {
		a, _, errBuf := readTestApp(t, &jenkinsClientMock{})
		warmStatusCache(t)
		code := a.run([]string{"history", "deploy-app", "--count", "-3"})
		assert.Equal(t, exitUsage, code)
		assert.Contains(t, errBuf.String(), "count")
	})
}

func TestHistory_Limit(t *testing.T) {
	t.Run("defaults to 10 builds", func(t *testing.T) {
		var gotLimit int
		jc := &jenkinsClientMock{
			BuildsFunc: func(_ context.Context, jobPath string, limit int) ([]jenkins.Build, error) {
				assert.Equal(t, "/job/deploy-app", jobPath)
				gotLimit = limit
				return []jenkins.Build{{Number: 1, Result: "SUCCESS"}}, nil
			},
		}
		a, _, _ := readTestApp(t, jc)
		warmStatusCache(t)
		code := a.run([]string{"history", "deploy-app"})
		require.Equal(t, exitOK, code)
		assert.Equal(t, 10, gotLimit)
	})

	t.Run("-n overrides the count", func(t *testing.T) {
		var gotLimit int
		jc := &jenkinsClientMock{
			BuildsFunc: func(_ context.Context, _ string, limit int) ([]jenkins.Build, error) {
				gotLimit = limit
				return []jenkins.Build{{Number: 1, Result: "SUCCESS"}}, nil
			},
		}
		a, _, _ := readTestApp(t, jc)
		warmStatusCache(t)
		code := a.run([]string{"history", "deploy-app", "-n", "3"})
		require.Equal(t, exitOK, code)
		assert.Equal(t, 3, gotLimit)
	})

	t.Run("--count overrides the count", func(t *testing.T) {
		var gotLimit int
		jc := &jenkinsClientMock{
			BuildsFunc: func(_ context.Context, _ string, limit int) ([]jenkins.Build, error) {
				gotLimit = limit
				return []jenkins.Build{{Number: 1, Result: "SUCCESS"}}, nil
			},
		}
		a, _, _ := readTestApp(t, jc)
		warmStatusCache(t)
		code := a.run([]string{"history", "deploy-app", "--count", "5"})
		require.Equal(t, exitOK, code)
		assert.Equal(t, 5, gotLimit)
	})

	t.Run("count larger than available renders all rows without error", func(t *testing.T) {
		jc := &jenkinsClientMock{
			BuildsFunc: func(_ context.Context, _ string, _ int) ([]jenkins.Build, error) {
				return []jenkins.Build{
					{Number: 2, Result: "SUCCESS", Duration: 1000},
					{Number: 1, Result: "FAILURE", Duration: 2000},
				}, nil
			},
		}
		a, out, _ := readTestApp(t, jc)
		warmStatusCache(t)
		code := a.run([]string{"history", "deploy-app", "-n", "50"})
		require.Equal(t, exitOK, code)
		s := out.String()
		assert.Contains(t, s, "#2")
		assert.Contains(t, s, "#1")
	})
}

func TestHistory_Render(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)

	t.Run("finished rows render result, duration and relative time", func(t *testing.T) {
		jc := &jenkinsClientMock{
			BuildsFunc: func(context.Context, string, int) ([]jenkins.Build, error) {
				return []jenkins.Build{
					{Number: 42, Result: "SUCCESS", Duration: 64000, Timestamp: now.Add(-5 * time.Minute).UnixMilli()},
					{Number: 41, Result: "FAILURE", Duration: 2000, Timestamp: now.Add(-2 * time.Hour).UnixMilli()},
				}, nil
			},
		}
		a, out, _ := readTestApp(t, jc)
		a.now = fixedClock(now)
		warmStatusCache(t)

		code := a.run([]string{"history", "deploy-app"})
		require.Equal(t, exitOK, code)
		s := out.String()
		// header line names the job.
		assert.Contains(t, s, "deploy-app")
		assert.Contains(t, s, "#42")
		assert.Contains(t, s, "SUCCESS")
		assert.Contains(t, s, "1m4s")
		assert.Contains(t, s, "5m ago")
		assert.Contains(t, s, "#41")
		assert.Contains(t, s, "FAILURE")
		assert.Contains(t, s, "2.0s")
		assert.Contains(t, s, "2h ago")
	})

	t.Run("running build renders RUNNING and an em-dash for duration", func(t *testing.T) {
		jc := &jenkinsClientMock{
			BuildsFunc: func(context.Context, string, int) ([]jenkins.Build, error) {
				return []jenkins.Build{
					{Number: 43, Building: true, Timestamp: now.Add(-30 * time.Second).UnixMilli()},
				}, nil
			},
		}
		a, out, _ := readTestApp(t, jc)
		a.now = fixedClock(now)
		warmStatusCache(t)

		code := a.run([]string{"history", "deploy-app"})
		require.Equal(t, exitOK, code)
		s := out.String()
		assert.Contains(t, s, "#43")
		assert.Contains(t, s, "RUNNING")
		assert.Contains(t, s, "—")
		assert.Contains(t, s, "just now")
	})

	t.Run("columns align across differing build-number widths", func(t *testing.T) {
		jc := &jenkinsClientMock{
			BuildsFunc: func(context.Context, string, int) ([]jenkins.Build, error) {
				return []jenkins.Build{
					{Number: 10, Result: "SUCCESS", Duration: 64000, Timestamp: now.Add(-5 * time.Minute).UnixMilli()},
					{Number: 9, Result: "FAILURE", Duration: 2000, Timestamp: now.Add(-2 * time.Hour).UnixMilli()},
				}, nil
			},
		}
		a, out, _ := readTestApp(t, jc)
		a.now = fixedClock(now)
		warmStatusCache(t)

		code := a.run([]string{"history", "deploy-app"})
		require.Equal(t, exitOK, code)
		// #9 is padded to the width of #10 so the result/duration/time columns line up exactly.
		lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
		require.Equal(t, []string{
			"deploy-app",
			"  #10  SUCCESS  1m4s  5m ago",
			"  #9   FAILURE  2.0s  2h ago",
		}, lines)
	})

	t.Run("running em-dash pads by rune width so the time column stays aligned", func(t *testing.T) {
		jc := &jenkinsClientMock{
			BuildsFunc: func(context.Context, string, int) ([]jenkins.Build, error) {
				return []jenkins.Build{
					{Number: 10, Building: true, Timestamp: now.Add(-30 * time.Second).UnixMilli()},
					{Number: 9, Result: "SUCCESS", Duration: 2000, Timestamp: now.Add(-2 * time.Hour).UnixMilli()},
				}, nil
			},
		}
		a, out, _ := readTestApp(t, jc)
		a.now = fixedClock(now)
		warmStatusCache(t)

		code := a.run([]string{"history", "deploy-app"})
		require.Equal(t, exitOK, code)
		lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
		require.Equal(t, []string{
			"deploy-app",
			"  #10  RUNNING  —     just now",
			"  #9   SUCCESS  2.0s  2h ago",
		}, lines)
	})

	t.Run("empty history renders a no-builds line", func(t *testing.T) {
		jc := &jenkinsClientMock{
			BuildsFunc: func(context.Context, string, int) ([]jenkins.Build, error) {
				return []jenkins.Build{}, nil
			},
		}
		a, out, _ := readTestApp(t, jc)
		warmStatusCache(t)

		code := a.run([]string{"history", "deploy-app"})
		require.Equal(t, exitOK, code)
		s := out.String()
		assert.Contains(t, s, "deploy-app")
		assert.Contains(t, s, "no builds")
	})
}

func TestHistory_JSON(t *testing.T) {
	t.Run("emits the build array with duration for finished builds, omitted for running", func(t *testing.T) {
		jc := &jenkinsClientMock{
			BuildsFunc: func(context.Context, string, int) ([]jenkins.Build, error) {
				return []jenkins.Build{
					{Number: 43, Building: true, Timestamp: 300},
					{Number: 42, Result: "SUCCESS", Duration: 64000, Timestamp: 200},
					{Number: 41, Result: "FAILURE", Duration: 0, Timestamp: 100},
				}, nil
			},
		}
		a, out, _ := readTestApp(t, jc)
		a.global.JSON = true
		warmStatusCache(t)

		code := a.run([]string{"history", "deploy-app"})
		require.Equal(t, exitOK, code)

		var got []buildJSON
		require.NoError(t, json.Unmarshal(out.Bytes(), &got))
		require.Len(t, got, 3)
		// order matches human output (newest first).
		assert.Equal(t, 43, got[0].Number)
		assert.Equal(t, 42, got[1].Number)
		assert.Equal(t, 41, got[2].Number)
		// finished build carries its duration.
		assert.Equal(t, int64(64000), got[1].Duration)

		// duration is omitted (0) for the running build and the zero-duration finished build.
		var raw []map[string]any
		require.NoError(t, json.Unmarshal(out.Bytes(), &raw))
		_, running := raw[0]["duration"]
		assert.False(t, running, "running build must omit duration")
		_, zero := raw[2]["duration"]
		assert.False(t, zero, "zero-duration build must omit duration")
	})
}

// TestStatus_JSONOmitsDuration is a regression guard: the status JSON tree never fetches a build's
// duration, so the buildJSON.Duration field (added for history) must stay omitted there.
func TestStatus_JSONOmitsDuration(t *testing.T) {
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

	var raw map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &raw))
	build, ok := raw["build"].(map[string]any)
	require.True(t, ok)
	_, hasDuration := build["duration"]
	assert.False(t, hasDuration, "status --json must omit duration")
}

func TestHistory_NotFound(t *testing.T) {
	jc := &jenkinsClientMock{
		JobsFunc: func(context.Context) ([]jenkins.Job, error) { return sampleJobs(), nil },
	}
	a, _, errBuf := readTestApp(t, jc)
	code := a.run([]string{"history", "ghost"})
	assert.Equal(t, exitNotFound, code)
	assert.Contains(t, errBuf.String(), "not found")
}

func TestHistory_BuildsError(t *testing.T) {
	t.Run("auth failure from Builds maps to exit 2 and wraps the job name", func(t *testing.T) {
		jc := &jenkinsClientMock{
			BuildsFunc: func(context.Context, string, int) ([]jenkins.Build, error) {
				return nil, jenkins.ErrAuth
			},
		}
		a, _, errBuf := readTestApp(t, jc)
		warmStatusCache(t)
		code := a.run([]string{"history", "deploy-app"})
		assert.Equal(t, exitAuth, code)
		assert.Contains(t, errBuf.String(), `builds for "deploy-app"`)
	})

	t.Run("generic failure from Builds maps to exit 1", func(t *testing.T) {
		jc := &jenkinsClientMock{
			BuildsFunc: func(context.Context, string, int) ([]jenkins.Build, error) {
				return nil, errors.New("connection reset")
			},
		}
		a, _, errBuf := readTestApp(t, jc)
		warmStatusCache(t)
		code := a.run([]string{"history", "deploy-app"})
		assert.Equal(t, exitUsage, code)
		assert.Contains(t, errBuf.String(), `builds for "deploy-app"`)
		assert.Contains(t, errBuf.String(), "connection reset")
	})
}
