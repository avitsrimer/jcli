package cli

import (
	"context"
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

func TestHistory_NotFound(t *testing.T) {
	jc := &jenkinsClientMock{
		JobsFunc: func(context.Context) ([]jenkins.Job, error) { return sampleJobs(), nil },
	}
	a, _, errBuf := readTestApp(t, jc)
	code := a.run([]string{"history", "ghost"})
	assert.Equal(t, exitNotFound, code)
	assert.Contains(t, errBuf.String(), "not found")
}
