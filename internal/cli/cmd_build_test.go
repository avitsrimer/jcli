package cli

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/avitsrimer/jcli/internal/cache"
	"github.com/avitsrimer/jcli/internal/jenkins"
)

// buildParams is the parameter set used across build tests: a Choice and a String with defaults.
func buildParamDefs() []jenkins.Param {
	return []jenkins.Param{
		{Name: "ENV", Type: "Choice", Choices: []string{"uat1", "uat2"}, Default: "uat1"},
		{Name: "BRANCH", Type: "String", Default: "master"},
	}
}

// warmBuildCache primes a warm cache with deploy-app carrying live param defs so the build path
// validates against cached defs without a JobParams read. Set fetched to false to leave params
// unfetched (forcing a live read).
func warmBuildCache(t *testing.T, fetched bool) {
	t.Helper()
	job := cache.Job{Path: "/job/deploy-app", Class: "WorkflowJob", Buildable: true, Params: buildParamDefs()}
	if fetched {
		job.ParamsFetchedAt = time.Now().UTC()
	} else {
		job.Params = nil
	}
	m := &cache.Map{FetchedAt: time.Now().UTC(), URL: "https://jenkins.example.com", Jobs: map[string]cache.Job{
		"deploy-app": job,
	}}
	require.NoError(t, m.Save("work"))
}

func TestBuild_Validation(t *testing.T) {
	t.Run("unknown param name is rejected", func(t *testing.T) {
		jc := &jenkinsClientMock{
			BuildFunc: func(context.Context, string, map[string]string) (string, error) {
				t.Fatal("Build must not be called when validation fails")
				return "", nil
			},
		}
		a, _, errBuf := readTestApp(t, jc)
		warmBuildCache(t, true)

		code := a.run([]string{"build", "deploy-app", "--param-NOPE=x"})
		assert.Equal(t, exitUsage, code)
		assert.Empty(t, jc.BuildCalls())
		assert.Contains(t, errBuf.String(), "unknown parameter")
	})

	t.Run("bad choice value is rejected", func(t *testing.T) {
		jc := &jenkinsClientMock{
			BuildFunc: func(context.Context, string, map[string]string) (string, error) {
				t.Fatal("Build must not be called when validation fails")
				return "", nil
			},
		}
		a, _, errBuf := readTestApp(t, jc)
		warmBuildCache(t, true)

		code := a.run([]string{"build", "deploy-app", "--param-ENV=prod"})
		assert.Equal(t, exitUsage, code)
		assert.Empty(t, jc.BuildCalls())
		assert.Contains(t, errBuf.String(), "choice parameter")
	})

	t.Run("defaults fill omitted params", func(t *testing.T) {
		var sent map[string]string
		jc := &jenkinsClientMock{
			BuildFunc: func(_ context.Context, _ string, params map[string]string) (string, error) {
				sent = params
				return "https://jenkins.example.com/queue/item/1/", nil
			},
		}
		a, _, _ := readTestApp(t, jc)
		warmBuildCache(t, true)

		// BRANCH omitted → default master.
		code := a.run([]string{"build", "deploy-app", "--param-ENV=uat2"})
		require.Equal(t, exitOK, code)
		require.Len(t, jc.BuildCalls(), 1)
		assert.Equal(t, "uat2", sent["ENV"])
		assert.Equal(t, "master", sent["BRANCH"], "omitted param filled from default")
	})

	t.Run("omitted choice fills valid default without error", func(t *testing.T) {
		var sent map[string]string
		jc := &jenkinsClientMock{
			BuildFunc: func(_ context.Context, _ string, params map[string]string) (string, error) {
				sent = params
				return "https://jenkins.example.com/queue/item/1/", nil
			},
		}
		a, _, _ := readTestApp(t, jc)
		warmBuildCache(t, true)

		// ENV (a Choice) omitted → filled from its default uat1, which is within the choice set.
		code := a.run([]string{"build", "deploy-app"})
		require.Equal(t, exitOK, code)
		require.Len(t, jc.BuildCalls(), 1)
		assert.Equal(t, "uat1", sent["ENV"], "omitted choice filled from valid default")
	})

	t.Run("choice default outside choices is rejected", func(t *testing.T) {
		jc := &jenkinsClientMock{
			JobParamsFunc: func(context.Context, string) ([]jenkins.Param, error) {
				// a misconfigured job whose Choice default is not among its choices.
				return []jenkins.Param{{Name: "ENV", Type: "Choice", Choices: []string{"uat1", "uat2"}, Default: "bogus"}}, nil
			},
			BuildFunc: func(context.Context, string, map[string]string) (string, error) {
				t.Fatal("Build must not run when a defaulted choice is invalid")
				return "", nil
			},
		}
		a, _, errBuf := readTestApp(t, jc)
		warmBuildCache(t, false) // force a live param read returning the bad default.

		code := a.run([]string{"build", "deploy-app"})
		assert.Equal(t, exitUsage, code)
		assert.Empty(t, jc.BuildCalls())
		assert.Contains(t, errBuf.String(), "choice parameter")
	})

	t.Run("job params read error surfaces", func(t *testing.T) {
		jc := &jenkinsClientMock{
			JobParamsFunc: func(context.Context, string) ([]jenkins.Param, error) {
				return nil, jenkins.ErrAuth
			},
			BuildFunc: func(context.Context, string, map[string]string) (string, error) {
				t.Fatal("Build must not run when reading params fails")
				return "", nil
			},
		}
		a, _, errBuf := readTestApp(t, jc)
		warmBuildCache(t, false) // unfetched params force a live read, which errors.

		code := a.run([]string{"build", "deploy-app"})
		assert.Equal(t, exitAuth, code, "JobParams auth error maps to exit 2")
		assert.Len(t, jc.JobParamsCalls(), 1)
		assert.Empty(t, jc.BuildCalls())
		assert.Contains(t, errBuf.String(), "read params")
	})

	t.Run("live param read on cache miss", func(t *testing.T) {
		jc := &jenkinsClientMock{
			JobParamsFunc: func(context.Context, string) ([]jenkins.Param, error) { return buildParamDefs(), nil },
			BuildFunc: func(context.Context, string, map[string]string) (string, error) {
				return "https://jenkins.example.com/queue/item/1/", nil
			},
		}
		a, _, _ := readTestApp(t, jc)
		warmBuildCache(t, false) // params present in cache shape but ParamsFetchedAt zero → live read.

		code := a.run([]string{"build", "deploy-app"})
		require.Equal(t, exitOK, code)
		assert.Len(t, jc.JobParamsCalls(), 1, "unfetched params trigger a live read")
		assert.Len(t, jc.BuildCalls(), 1)
	})
}

func TestBuild_FireAndForget(t *testing.T) {
	jc := &jenkinsClientMock{
		BuildFunc: func(context.Context, string, map[string]string) (string, error) {
			return "https://jenkins.example.com/queue/item/9/", nil
		},
		QueueItemFunc: func(context.Context, string) (jenkins.QueueItem, error) {
			t.Fatal("fire-and-forget must not poll the queue")
			return jenkins.QueueItem{}, nil
		},
	}
	a, out, _ := readTestApp(t, jc)
	warmBuildCache(t, true)

	code := a.run([]string{"build", "deploy-app"})
	require.Equal(t, exitOK, code)
	assert.Len(t, jc.BuildCalls(), 1)
	assert.Empty(t, jc.QueueItemCalls(), "no polling without --wait")
	assert.Contains(t, out.String(), "queue/item/9")
}

func TestBuild_Wait(t *testing.T) {
	const queueLoc = "https://jenkins.example.com/queue/item/9/"
	const buildURL = "https://jenkins.example.com/job/deploy-app/7/"

	t.Run("queue executable transition then SUCCESS exits 0", func(t *testing.T) {
		var queuePolls int32
		jc := &jenkinsClientMock{
			BuildFunc: func(context.Context, string, map[string]string) (string, error) { return queueLoc, nil },
			QueueItemFunc: func(context.Context, string) (jenkins.QueueItem, error) {
				// first poll: still pending (no executable); second: build number assigned.
				if atomic.AddInt32(&queuePolls, 1) < 2 {
					return jenkins.QueueItem{}, nil
				}
				return jenkins.QueueItem{Executable: &jenkins.Executable{Number: 7, URL: buildURL}}, nil
			},
			BuildResultFunc: func(context.Context, string) (jenkins.BuildResult, error) {
				return jenkins.BuildResult{Building: false, Result: "SUCCESS"}, nil
			},
			StageViewFunc: func(context.Context, string) ([]jenkins.Stage, error) { return nil, jenkins.ErrNotFound },
		}
		a, out, _ := readTestApp(t, jc)
		a.pollInterval = time.Millisecond
		warmBuildCache(t, true)

		code := a.run([]string{"build", "deploy-app", "--wait"})
		require.Equal(t, exitOK, code)
		assert.GreaterOrEqual(t, len(jc.QueueItemCalls()), 2, "polled the queue until executable populated")
		assert.Len(t, jc.BuildResultCalls(), 1)
		assert.Contains(t, out.String(), "SUCCESS")
	})

	t.Run("building then FAILURE exits 4", func(t *testing.T) {
		var resultPolls int32
		jc := &jenkinsClientMock{
			BuildFunc: func(context.Context, string, map[string]string) (string, error) { return queueLoc, nil },
			QueueItemFunc: func(context.Context, string) (jenkins.QueueItem, error) {
				return jenkins.QueueItem{Executable: &jenkins.Executable{Number: 7, URL: buildURL}}, nil
			},
			BuildResultFunc: func(context.Context, string) (jenkins.BuildResult, error) {
				// first poll building, second poll terminal FAILURE.
				if atomic.AddInt32(&resultPolls, 1) < 2 {
					return jenkins.BuildResult{Building: true}, nil
				}
				return jenkins.BuildResult{Building: false, Result: "FAILURE"}, nil
			},
			StageViewFunc: func(context.Context, string) ([]jenkins.Stage, error) { return nil, jenkins.ErrNotFound },
		}
		a, _, errBuf := readTestApp(t, jc)
		a.pollInterval = time.Millisecond
		warmBuildCache(t, true)

		code := a.run([]string{"build", "deploy-app", "--wait"})
		assert.Equal(t, exitBuildFail, code)
		assert.GreaterOrEqual(t, len(jc.BuildResultCalls()), 2, "polled until the build finished")
		assert.Contains(t, errBuf.String(), "FAILURE")
	})

	t.Run("UNSTABLE exits 4", func(t *testing.T) {
		jc := &jenkinsClientMock{
			BuildFunc: func(context.Context, string, map[string]string) (string, error) { return queueLoc, nil },
			QueueItemFunc: func(context.Context, string) (jenkins.QueueItem, error) {
				return jenkins.QueueItem{Executable: &jenkins.Executable{Number: 7, URL: buildURL}}, nil
			},
			BuildResultFunc: func(context.Context, string) (jenkins.BuildResult, error) {
				return jenkins.BuildResult{Building: false, Result: "UNSTABLE"}, nil
			},
			StageViewFunc: func(context.Context, string) ([]jenkins.Stage, error) { return nil, jenkins.ErrNotFound },
		}
		a, _, _ := readTestApp(t, jc)
		a.pollInterval = time.Millisecond
		warmBuildCache(t, true)

		code := a.run([]string{"build", "deploy-app", "--wait"})
		assert.Equal(t, exitBuildFail, code)
	})
}

func TestBuild_WaitStages(t *testing.T) {
	const queueLoc = "https://jenkins.example.com/queue/item/9/"
	const buildURL = "https://jenkins.example.com/job/deploy-app/7/"

	t.Run("evolving stage snapshots emit transitions without dupes", func(t *testing.T) {
		var poll int32
		// three poll snapshots: build first IN_PROGRESS, then build done + test IN_PROGRESS,
		// then both done. BuildResult goes terminal on the third poll.
		snapshots := [][]jenkins.Stage{
			{{Name: "build", Status: "IN_PROGRESS"}, {Name: "test", Status: "NOT_EXECUTED"}},
			{{Name: "build", Status: "SUCCESS", DurationMillis: 1500}, {Name: "test", Status: "IN_PROGRESS"}},
			{{Name: "build", Status: "SUCCESS", DurationMillis: 1500}, {Name: "test", Status: "SUCCESS", DurationMillis: 62000}},
		}
		jc := &jenkinsClientMock{
			BuildFunc: func(context.Context, string, map[string]string) (string, error) { return queueLoc, nil },
			QueueItemFunc: func(context.Context, string) (jenkins.QueueItem, error) {
				return jenkins.QueueItem{Executable: &jenkins.Executable{Number: 7, URL: buildURL}}, nil
			},
			BuildResultFunc: func(context.Context, string) (jenkins.BuildResult, error) {
				if atomic.AddInt32(&poll, 1) < 3 {
					return jenkins.BuildResult{Building: true}, nil
				}
				return jenkins.BuildResult{Building: false, Result: "SUCCESS"}, nil
			},
			StageViewFunc: func(context.Context, string) ([]jenkins.Stage, error) {
				// align stage snapshot to the current build poll (1-indexed, capped at last).
				i := int(atomic.LoadInt32(&poll)) - 1
				if i >= len(snapshots) {
					i = len(snapshots) - 1
				}
				return snapshots[i], nil
			},
		}
		a, _, errBuf := readTestApp(t, jc)
		a.pollInterval = time.Millisecond
		warmBuildCache(t, true)

		code := a.run([]string{"build", "deploy-app", "--wait"})
		require.Equal(t, exitOK, code)

		out := errBuf.String()
		// exact transition lines in pipeline order, each status logged once.
		assert.Contains(t, out, "▶ build\n")
		assert.Contains(t, out, "✓ build (1.5s)\n")
		assert.Contains(t, out, "▶ test\n")
		assert.Contains(t, out, "✓ test (1m2s)\n")
		// NOT_EXECUTED never produces a transition line: test only logs its IN_PROGRESS then SUCCESS.
		assert.Equal(t, 1, strings.Count(out, "▶ test\n"), "test IN_PROGRESS logged once, never for NOT_EXECUTED")
		assert.Equal(t, 1, strings.Count(out, "✓ test (1m2s)\n"), "test SUCCESS logged once with its duration")
		assert.Equal(t, 1, strings.Count(out, "▶ build\n"), "no duplicate IN_PROGRESS line for build")
		assert.Equal(t, 1, strings.Count(out, "✓ build (1.5s)\n"), "no duplicate SUCCESS line for build")
	})

	t.Run("stage view ErrNotFound is swallowed, wait completes with no stage lines", func(t *testing.T) {
		jc := &jenkinsClientMock{
			BuildFunc: func(context.Context, string, map[string]string) (string, error) { return queueLoc, nil },
			QueueItemFunc: func(context.Context, string) (jenkins.QueueItem, error) {
				return jenkins.QueueItem{Executable: &jenkins.Executable{Number: 7, URL: buildURL}}, nil
			},
			BuildResultFunc: func(context.Context, string) (jenkins.BuildResult, error) {
				return jenkins.BuildResult{Building: false, Result: "SUCCESS"}, nil
			},
			StageViewFunc: func(context.Context, string) ([]jenkins.Stage, error) {
				return nil, jenkins.ErrNotFound
			},
		}
		a, out, errBuf := readTestApp(t, jc)
		a.pollInterval = time.Millisecond
		warmBuildCache(t, true)

		code := a.run([]string{"build", "deploy-app", "--wait"})
		require.Equal(t, exitOK, code, "404 stage view never fails the wait")
		assert.GreaterOrEqual(t, len(jc.StageViewCalls()), 1)
		assert.Contains(t, out.String(), "SUCCESS")
		assert.NotContains(t, errBuf.String(), "▶")
		assert.NotContains(t, errBuf.String(), "✓ ")
	})

	t.Run("no-stages flag never calls StageView", func(t *testing.T) {
		jc := &jenkinsClientMock{
			BuildFunc: func(context.Context, string, map[string]string) (string, error) { return queueLoc, nil },
			QueueItemFunc: func(context.Context, string) (jenkins.QueueItem, error) {
				return jenkins.QueueItem{Executable: &jenkins.Executable{Number: 7, URL: buildURL}}, nil
			},
			BuildResultFunc: func(context.Context, string) (jenkins.BuildResult, error) {
				return jenkins.BuildResult{Building: false, Result: "SUCCESS"}, nil
			},
			StageViewFunc: func(context.Context, string) ([]jenkins.Stage, error) {
				t.Fatal("StageView must not be called with --no-stages")
				return nil, nil
			},
		}
		a, _, _ := readTestApp(t, jc)
		a.pollInterval = time.Millisecond
		warmBuildCache(t, true)

		code := a.run([]string{"build", "deploy-app", "--wait", "--no-stages"})
		require.Equal(t, exitOK, code)
		assert.Equal(t, 0, len(jc.StageViewCalls()), "--no-stages suppresses all stage fetching")
	})

	// terminalGlyphs exercises each non-IN_PROGRESS terminal status glyph and its duration rendering:
	// a stage transitions IN_PROGRESS → <terminal> across two polls, and the second line must carry
	// the humanized duration. BuildResult stays SUCCESS throughout so the wait exits 0 regardless of
	// the stage's (purely informational) status.
	terminalGlyphs := []struct {
		name       string
		status     string
		durationMS int64
		wantDoneLn string
	}{
		{name: "failed", status: "FAILED", durationMS: 3661000, wantDoneLn: "✗ deploy (1h1m1s)\n"},
		{name: "unstable", status: "UNSTABLE", durationMS: 850, wantDoneLn: "⚠ deploy (850ms)\n"},
		{name: "aborted", status: "ABORTED", durationMS: 60000, wantDoneLn: "⊘ deploy (1m0s)\n"},
	}
	for _, tc := range terminalGlyphs {
		t.Run(tc.name+" stage glyph and duration", func(t *testing.T) {
			var poll int32
			snapshots := [][]jenkins.Stage{
				{{Name: "deploy", Status: "IN_PROGRESS"}},
				{{Name: "deploy", Status: tc.status, DurationMillis: tc.durationMS}},
			}
			jc := &jenkinsClientMock{
				BuildFunc: func(context.Context, string, map[string]string) (string, error) { return queueLoc, nil },
				QueueItemFunc: func(context.Context, string) (jenkins.QueueItem, error) {
					return jenkins.QueueItem{Executable: &jenkins.Executable{Number: 7, URL: buildURL}}, nil
				},
				BuildResultFunc: func(context.Context, string) (jenkins.BuildResult, error) {
					// stay building for the first poll, terminal SUCCESS on the second.
					if atomic.AddInt32(&poll, 1) < 2 {
						return jenkins.BuildResult{Building: true}, nil
					}
					return jenkins.BuildResult{Building: false, Result: "SUCCESS"}, nil
				},
				StageViewFunc: func(context.Context, string) ([]jenkins.Stage, error) {
					i := int(atomic.LoadInt32(&poll)) - 1
					if i >= len(snapshots) {
						i = len(snapshots) - 1
					}
					return snapshots[i], nil
				},
			}
			a, _, errBuf := readTestApp(t, jc)
			a.pollInterval = time.Millisecond
			warmBuildCache(t, true)

			code := a.run([]string{"build", "deploy-app", "--wait"})
			require.Equal(t, exitOK, code, "stage status is informational and never affects exit")

			out := errBuf.String()
			assert.Equal(t, 1, strings.Count(out, "▶ deploy\n"), "IN_PROGRESS line logged once")
			assert.Equal(t, 1, strings.Count(out, tc.wantDoneLn), "terminal glyph line with humanized duration")
		})
	}

	t.Run("persistent non-404 stage view error is swallowed and logged once across polls", func(t *testing.T) {
		var poll int32
		jc := &jenkinsClientMock{
			BuildFunc: func(context.Context, string, map[string]string) (string, error) { return queueLoc, nil },
			QueueItemFunc: func(context.Context, string) (jenkins.QueueItem, error) {
				return jenkins.QueueItem{Executable: &jenkins.Executable{Number: 7, URL: buildURL}}, nil
			},
			BuildResultFunc: func(context.Context, string) (jenkins.BuildResult, error) {
				// stay building for several polls so logStages runs repeatedly, then go terminal.
				if atomic.AddInt32(&poll, 1) < 4 {
					return jenkins.BuildResult{Building: true}, nil
				}
				return jenkins.BuildResult{Building: false, Result: "SUCCESS"}, nil
			},
			StageViewFunc: func(context.Context, string) ([]jenkins.Stage, error) {
				// a persistent non-404 error (e.g. auth) must not fail the wait nor emit any transition line.
				return nil, jenkins.ErrAuth
			},
		}
		a, out, errBuf := readTestApp(t, jc)
		a.pollInterval = time.Millisecond
		warmBuildCache(t, true)

		// --verbose routes the swallowed diagnostic to stderr; the wait still exits 0.
		code := a.run([]string{"build", "deploy-app", "--wait", "--verbose"})
		require.Equal(t, exitOK, code, "a non-404 stage view error never fails the wait")
		assert.GreaterOrEqual(t, len(jc.StageViewCalls()), 4, "logStages ran on every build poll")
		assert.Contains(t, out.String(), "SUCCESS")
		assert.NotContains(t, errBuf.String(), "▶")
		assert.NotContains(t, errBuf.String(), "✓ ")
		assert.Equal(t, 1, strings.Count(errBuf.String(), "stage view for"),
			"the same persistent diagnostic is deduped to one line across polls")
	})
}

func TestHumanizeDuration(t *testing.T) {
	// locks in the current rendering: sub-second as %dms, sub-minute as %.1fs, and minute-or-more
	// as time.Duration's rounded String(). These are the exact strings the function produces today.
	tests := []struct {
		ms   int64
		want string
	}{
		{ms: 0, want: "0ms"},
		{ms: 850, want: "850ms"},
		{ms: 999, want: "999ms"},
		{ms: 1000, want: "1.0s"},
		{ms: 59000, want: "59.0s"},
		{ms: 60000, want: "1m0s"},
		{ms: 62000, want: "1m2s"},
		{ms: 3661000, want: "1h1m1s"},
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			assert.Equal(t, tc.want, humanizeDuration(tc.ms))
		})
	}
}

func TestBuild_WaitErrors(t *testing.T) {
	const queueLoc = "https://jenkins.example.com/queue/item/9/"
	const buildURL = "https://jenkins.example.com/job/deploy-app/7/"

	t.Run("cancelled queue item exits 4", func(t *testing.T) {
		jc := &jenkinsClientMock{
			BuildFunc: func(context.Context, string, map[string]string) (string, error) { return queueLoc, nil },
			QueueItemFunc: func(context.Context, string) (jenkins.QueueItem, error) {
				return jenkins.QueueItem{Cancelled: true}, nil
			},
			BuildResultFunc: func(context.Context, string) (jenkins.BuildResult, error) {
				t.Fatal("must not poll build result after a cancelled queue item")
				return jenkins.BuildResult{}, nil
			},
		}
		a, _, errBuf := readTestApp(t, jc)
		a.pollInterval = time.Millisecond
		warmBuildCache(t, true)

		code := a.run([]string{"build", "deploy-app", "--wait"})
		assert.Equal(t, exitBuildFail, code)
		assert.Contains(t, errBuf.String(), "cancelled")
		assert.Empty(t, jc.BuildResultCalls())
	})

	t.Run("queue poll error surfaces", func(t *testing.T) {
		jc := &jenkinsClientMock{
			BuildFunc: func(context.Context, string, map[string]string) (string, error) { return queueLoc, nil },
			QueueItemFunc: func(context.Context, string) (jenkins.QueueItem, error) {
				return jenkins.QueueItem{}, jenkins.ErrNotFound
			},
		}
		a, _, errBuf := readTestApp(t, jc)
		a.pollInterval = time.Millisecond
		warmBuildCache(t, true)

		code := a.run([]string{"build", "deploy-app", "--wait"})
		assert.Equal(t, exitNotFound, code, "a queue poll error maps through its typed error")
		assert.Contains(t, errBuf.String(), "poll queue item")
	})

	t.Run("build result poll error surfaces", func(t *testing.T) {
		jc := &jenkinsClientMock{
			BuildFunc: func(context.Context, string, map[string]string) (string, error) { return queueLoc, nil },
			QueueItemFunc: func(context.Context, string) (jenkins.QueueItem, error) {
				return jenkins.QueueItem{Executable: &jenkins.Executable{Number: 7, URL: buildURL}}, nil
			},
			BuildResultFunc: func(context.Context, string) (jenkins.BuildResult, error) {
				return jenkins.BuildResult{}, jenkins.ErrAuth
			},
		}
		a, _, errBuf := readTestApp(t, jc)
		a.pollInterval = time.Millisecond
		warmBuildCache(t, true)

		code := a.run([]string{"build", "deploy-app", "--wait"})
		assert.Equal(t, exitAuth, code)
		assert.Contains(t, errBuf.String(), "poll build result")
	})

	t.Run("wait times out on a build stuck in the queue", func(t *testing.T) {
		jc := &jenkinsClientMock{
			BuildFunc: func(context.Context, string, map[string]string) (string, error) { return queueLoc, nil },
			QueueItemFunc: func(context.Context, string) (jenkins.QueueItem, error) {
				// never gets an executor: always pending.
				return jenkins.QueueItem{}, nil
			},
		}
		a, _, errBuf := readTestApp(t, jc)
		a.pollInterval = time.Millisecond
		a.waitTimeout = 5 * time.Millisecond
		warmBuildCache(t, true)

		code := a.run([]string{"build", "deploy-app", "--wait"})
		assert.Equal(t, exitUsage, code, "an exceeded --wait deadline is a usage/operational error")
		assert.Contains(t, errBuf.String(), "timed out")
	})
}

func TestBuild_Errors(t *testing.T) {
	t.Run("unknown job after crawl exits 3", func(t *testing.T) {
		jc := &jenkinsClientMock{
			JobsFunc: func(context.Context) ([]jenkins.Job, error) { return sampleJobs(), nil },
			BuildFunc: func(context.Context, string, map[string]string) (string, error) {
				t.Fatal("Build must not run for a missing job")
				return "", nil
			},
		}
		a, _, errBuf := readTestApp(t, jc) // cold cache → miss → crawl → still absent.

		code := a.run([]string{"build", "does-not-exist"})
		assert.Equal(t, exitNotFound, code)
		assert.Len(t, jc.JobsCalls(), 1, "one crawl before giving up")
		assert.Empty(t, jc.BuildCalls())
		_ = errBuf
	})

	t.Run("auth failure exits 2", func(t *testing.T) {
		jc := &jenkinsClientMock{
			BuildFunc: func(context.Context, string, map[string]string) (string, error) {
				return "", jenkins.ErrAuth
			},
		}
		a, _, _ := readTestApp(t, jc)
		warmBuildCache(t, true)

		code := a.run([]string{"build", "deploy-app"})
		assert.Equal(t, exitAuth, code)
	})

	t.Run("missing job name is a usage error", func(t *testing.T) {
		a, _, _ := readTestApp(t, &jenkinsClientMock{})
		code := a.run([]string{"build"})
		assert.Equal(t, exitUsage, code)
	})
}

func TestBuild_WaitLogs(t *testing.T) {
	const queueLoc = "https://jenkins.example.com/queue/item/9/"
	const buildURL = "https://jenkins.example.com/job/deploy-app/7/"

	t.Run("--logs implies --wait, streams console, suppresses stages, exits by result", func(t *testing.T) {
		var poll int32
		jc := &jenkinsClientMock{
			BuildFunc: func(context.Context, string, map[string]string) (string, error) { return queueLoc, nil },
			QueueItemFunc: func(context.Context, string) (jenkins.QueueItem, error) {
				return jenkins.QueueItem{Executable: &jenkins.Executable{Number: 7, URL: buildURL}}, nil
			},
			BuildResultFunc: func(context.Context, string) (jenkins.BuildResult, error) {
				// building on the first poll, terminal SUCCESS after.
				if atomic.AddInt32(&poll, 1) < 2 {
					return jenkins.BuildResult{Building: true}, nil
				}
				return jenkins.BuildResult{Building: false, Result: "SUCCESS"}, nil
			},
			ConsoleProgressiveFunc: func(_ context.Context, _ string, start int64) (jenkins.ConsoleChunk, error) {
				// stream two live chunks then a tail chunk after terminal, then no more.
				switch start {
				case 0:
					return jenkins.ConsoleChunk{Text: "starting\n", Size: 9, More: true}, nil
				case 9:
					return jenkins.ConsoleChunk{Text: "working\n", Size: 17, More: true}, nil
				default:
					return jenkins.ConsoleChunk{Text: "done\n", Size: 22, More: false}, nil
				}
			},
			StageViewFunc: func(context.Context, string) ([]jenkins.Stage, error) {
				t.Fatal("StageView must not be called with --logs")
				return nil, nil
			},
		}
		a, out, _ := readTestApp(t, jc)
		a.pollInterval = time.Millisecond
		warmBuildCache(t, true)

		code := a.run([]string{"build", "deploy-app", "--logs"})
		require.Equal(t, exitOK, code)
		s := out.String()
		assert.Contains(t, s, "starting\n")
		assert.Contains(t, s, "working\n")
		assert.Contains(t, s, "done\n", "tail chunk emitted after terminal BuildResult")
		assert.Contains(t, s, "SUCCESS")
		assert.Empty(t, jc.StageViewCalls())
	})

	t.Run("--logs still exits 4 on a FAILURE result", func(t *testing.T) {
		jc := &jenkinsClientMock{
			BuildFunc: func(context.Context, string, map[string]string) (string, error) { return queueLoc, nil },
			QueueItemFunc: func(context.Context, string) (jenkins.QueueItem, error) {
				return jenkins.QueueItem{Executable: &jenkins.Executable{Number: 7, URL: buildURL}}, nil
			},
			BuildResultFunc: func(context.Context, string) (jenkins.BuildResult, error) {
				return jenkins.BuildResult{Building: false, Result: "FAILURE"}, nil
			},
			ConsoleProgressiveFunc: func(context.Context, string, int64) (jenkins.ConsoleChunk, error) {
				return jenkins.ConsoleChunk{Text: "boom\n", Size: 5, More: false}, nil
			},
		}
		a, out, errBuf := readTestApp(t, jc)
		a.pollInterval = time.Millisecond
		warmBuildCache(t, true)

		code := a.run([]string{"build", "deploy-app", "--logs"})
		assert.Equal(t, exitBuildFail, code)
		assert.Contains(t, out.String(), "boom\n")
		assert.Contains(t, errBuf.String(), "FAILURE")
	})
}
