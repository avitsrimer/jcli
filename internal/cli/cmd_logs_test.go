package cli

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/avitsrimer/jcli/internal/jenkins"
)

func TestLogs_ArgValidation(t *testing.T) {
	t.Run("no args is a usage error", func(t *testing.T) {
		a, _, errBuf := readTestApp(t, &jenkinsClientMock{})
		code := a.run([]string{"logs"})
		assert.Equal(t, exitUsage, code)
		assert.Contains(t, errBuf.String(), "missing job name")
	})

	t.Run("too many args is a usage error", func(t *testing.T) {
		a, _, errBuf := readTestApp(t, &jenkinsClientMock{})
		code := a.run([]string{"logs", "deploy-app", "1", "extra"})
		assert.Equal(t, exitUsage, code)
		assert.Contains(t, errBuf.String(), "too many arguments")
	})

	t.Run("non-numeric build number is a usage error", func(t *testing.T) {
		a, _, errBuf := readTestApp(t, &jenkinsClientMock{})
		warmStatusCache(t)
		code := a.run([]string{"logs", "deploy-app", "notanumber"})
		assert.Equal(t, exitUsage, code)
		assert.Contains(t, errBuf.String(), "invalid build number")
	})
}

func TestLogs_Dump(t *testing.T) {
	t.Run("job-only dumps the latest build console", func(t *testing.T) {
		jc := &jenkinsClientMock{
			LastBuildFunc: func(_ context.Context, jobPath string) (jenkins.Build, bool, error) {
				assert.Equal(t, "/job/deploy-app", jobPath)
				return jenkins.Build{Number: 42, URL: "https://jenkins.example.com/job/deploy-app/42/"}, true, nil
			},
			ConsoleTextFunc: func(_ context.Context, buildURL string) (string, error) {
				assert.Equal(t, "https://jenkins.example.com/job/deploy-app/42/", buildURL)
				return "Started\nFinished: SUCCESS\n", nil
			},
		}
		a, out, _ := readTestApp(t, jc)
		warmStatusCache(t)

		code := a.run([]string{"logs", "deploy-app"})
		require.Equal(t, exitOK, code)
		assert.Contains(t, out.String(), "Finished: SUCCESS")
		assert.Empty(t, jc.ConsoleProgressiveCalls(), "dump must not use progressive")
	})

	t.Run("job+number dumps that build console", func(t *testing.T) {
		jc := &jenkinsClientMock{
			ConsoleTextFunc: func(_ context.Context, buildURL string) (string, error) {
				assert.Equal(t, "https://jenkins.example.com/job/deploy-app/7/", buildURL)
				return "build 7 log", nil
			},
		}
		a, out, _ := readTestApp(t, jc)
		warmStatusCache(t)

		code := a.run([]string{"logs", "deploy-app", "7"})
		require.Equal(t, exitOK, code)
		assert.Contains(t, out.String(), "build 7 log")
		assert.Empty(t, jc.LastBuildCalls(), "explicit number must not query LastBuild")
	})

	t.Run("never-built job errors", func(t *testing.T) {
		jc := &jenkinsClientMock{
			LastBuildFunc: func(context.Context, string) (jenkins.Build, bool, error) {
				return jenkins.Build{}, false, nil
			},
		}
		a, _, errBuf := readTestApp(t, jc)
		warmStatusCache(t)

		code := a.run([]string{"logs", "deploy-app"})
		assert.Equal(t, exitUsage, code)
		assert.Contains(t, errBuf.String(), "never built")
	})

	t.Run("missing build surfaces not-found", func(t *testing.T) {
		jc := &jenkinsClientMock{
			ConsoleTextFunc: func(context.Context, string) (string, error) {
				return "", jenkins.ErrNotFound
			},
		}
		a, _, errBuf := readTestApp(t, jc)
		warmStatusCache(t)

		code := a.run([]string{"logs", "deploy-app", "999"})
		assert.Equal(t, exitNotFound, code)
		assert.Contains(t, errBuf.String(), "not found")
	})

	t.Run("--json still emits raw console, not a json document", func(t *testing.T) {
		jc := &jenkinsClientMock{
			ConsoleTextFunc: func(context.Context, string) (string, error) { return "plain log line\n", nil },
		}
		a, out, _ := readTestApp(t, jc)
		a.global.JSON = true
		warmStatusCache(t)

		code := a.run([]string{"logs", "deploy-app", "5"})
		require.Equal(t, exitOK, code)
		assert.Equal(t, "plain log line\n", out.String(), "console is raw passthrough even with --json")
	})
}

func TestLogs_Follow(t *testing.T) {
	t.Run("streams progressive chunks until no more data", func(t *testing.T) {
		var polls int32
		jc := &jenkinsClientMock{
			ConsoleProgressiveFunc: func(_ context.Context, _ string, start int64) (jenkins.ConsoleChunk, error) {
				switch atomic.AddInt32(&polls, 1) {
				case 1:
					assert.Equal(t, int64(0), start)
					return jenkins.ConsoleChunk{Text: "line 1\n", Size: 7, More: true}, nil
				case 2:
					assert.Equal(t, int64(7), start)
					return jenkins.ConsoleChunk{Text: "line 2\n", Size: 14, More: true}, nil
				default:
					assert.Equal(t, int64(14), start)
					return jenkins.ConsoleChunk{Text: "done\n", Size: 19, More: false}, nil
				}
			},
		}
		a, out, _ := readTestApp(t, jc)
		a.pollInterval = time.Millisecond
		warmStatusCache(t)

		code := a.run([]string{"logs", "deploy-app", "42", "--wait"})
		require.Equal(t, exitOK, code)
		assert.Equal(t, "line 1\nline 2\ndone\n", out.String())
		assert.Equal(t, int32(3), atomic.LoadInt32(&polls))
	})
}
