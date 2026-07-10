package cli

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/avitsrimer/jcli/internal/jenkins"
)

// failPrompter fails the test if any prompt is invoked; it proves the confirmation gate is skipped
// for an already-finished build and when --yes is set.
type failPrompter struct{ t *testing.T }

func (p failPrompter) promptLine(string) (string, error) {
	p.t.Helper()
	p.t.Fatal("promptLine must not be called")
	return "", nil
}

func (p failPrompter) promptSecret(string) (string, error) {
	p.t.Helper()
	p.t.Fatal("promptSecret must not be called")
	return "", nil
}

// cancelApp builds a read test app (temp cache dir, "work" profile) wired to the given mock, with
// the prompter injected when non-nil. It executes the cancel command directly (the command is not
// yet registered with the parser) so the tests drive Execute and inspect the recorded exit code.
func cancelApp(t *testing.T, jc *jenkinsClientMock, pr prompter) (*app, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	a, out, errBuf := readTestApp(t, jc)
	if pr != nil {
		a.promptFactory = func() prompter { return pr }
	}
	return a, out, errBuf
}

// runCancelCmd executes a cancelCmd with the given args and returns the recorded exit code.
func runCancelCmd(t *testing.T, a *app, yes bool, args ...string) int {
	t.Helper()
	c := &cancelCmd{app: a, Yes: yes}
	require.NoError(t, c.Execute(args), "Execute swallows errors via fail and returns nil")
	return a.lastExit
}

// runningBuild is a mock returning a still-building status for the numbered build URL.
func runningBuild(t *testing.T) *jenkinsClientMock {
	t.Helper()
	return &jenkinsClientMock{
		BuildStatusFunc: func(_ context.Context, buildURL string) (jenkins.Build, error) {
			assert.Equal(t, "https://jenkins.example.com/job/deploy-app/42/", buildURL)
			return jenkins.Build{Number: 42, URL: buildURL, Building: true, Timestamp: 5}, nil
		},
		StopFunc: func(_ context.Context, buildURL string) error {
			assert.Equal(t, "https://jenkins.example.com/job/deploy-app/42/", buildURL)
			return nil
		},
	}
}

func TestCancel_Success(t *testing.T) {
	t.Run("--yes stops without prompting", func(t *testing.T) {
		jc := runningBuild(t)
		a, out, _ := cancelApp(t, jc, failPrompter{t})
		warmStatusCache(t)

		code := runCancelCmd(t, a, true, "deploy-app", "42")
		require.Equal(t, exitOK, code)
		assert.Contains(t, out.String(), "canceled build #42 of deploy-app")
		assert.Len(t, jc.StopCalls(), 1)
	})

	t.Run("y at the prompt stops the build", func(t *testing.T) {
		jc := runningBuild(t)
		pr := &scriptedPrompter{lines: []string{"y"}}
		a, out, _ := cancelApp(t, jc, pr)
		warmStatusCache(t)

		code := runCancelCmd(t, a, false, "deploy-app", "42")
		require.Equal(t, exitOK, code)
		assert.Contains(t, out.String(), "canceled build #42 of deploy-app")
		assert.Len(t, jc.StopCalls(), 1)
		assert.Equal(t, 1, pr.line, "the confirmation prompt was read")
	})
}

func TestCancel_Decline(t *testing.T) {
	jc := &jenkinsClientMock{
		BuildStatusFunc: func(_ context.Context, buildURL string) (jenkins.Build, error) {
			return jenkins.Build{Number: 42, URL: buildURL, Building: true, Timestamp: 5}, nil
		},
	}
	pr := &scriptedPrompter{lines: []string{"n"}}
	a, out, _ := cancelApp(t, jc, pr)
	warmStatusCache(t)

	code := runCancelCmd(t, a, false, "deploy-app", "42")
	require.Equal(t, exitOK, code)
	assert.Contains(t, out.String(), "aborted")
	assert.Empty(t, jc.StopCalls(), "decline must not POST /stop")
}

func TestCancel_AlreadyFinished(t *testing.T) {
	jc := &jenkinsClientMock{
		BuildStatusFunc: func(_ context.Context, buildURL string) (jenkins.Build, error) {
			return jenkins.Build{Number: 42, URL: buildURL, Building: false, Result: "SUCCESS"}, nil
		},
	}
	// failPrompter proves the confirmation gate is skipped for a finished build.
	a, out, _ := cancelApp(t, jc, failPrompter{t})
	warmStatusCache(t)

	code := runCancelCmd(t, a, false, "deploy-app", "42")
	require.Equal(t, exitOK, code)
	assert.Contains(t, out.String(), "build #42 of deploy-app is not running (SUCCESS)")
	assert.Empty(t, jc.StopCalls(), "a finished build must not be stopped")
}

func TestCancel_ArgValidation(t *testing.T) {
	t.Run("missing args is a usage error", func(t *testing.T) {
		a, _, errBuf := cancelApp(t, &jenkinsClientMock{}, nil)
		code := runCancelCmd(t, a, false)
		assert.Equal(t, exitUsage, code)
		assert.Contains(t, errBuf.String(), "expected a job and a build number")
	})

	t.Run("too many args is a usage error", func(t *testing.T) {
		a, _, errBuf := cancelApp(t, &jenkinsClientMock{}, nil)
		code := runCancelCmd(t, a, false, "deploy-app", "42", "extra")
		assert.Equal(t, exitUsage, code)
		assert.Contains(t, errBuf.String(), "too many arguments")
	})

	t.Run("non-numeric build number is a usage error", func(t *testing.T) {
		a, _, errBuf := cancelApp(t, &jenkinsClientMock{}, nil)
		code := runCancelCmd(t, a, false, "deploy-app", "notanumber")
		assert.Equal(t, exitUsage, code)
		assert.Contains(t, errBuf.String(), "invalid build number")
	})

	t.Run("zero build number is a usage error", func(t *testing.T) {
		a, _, errBuf := cancelApp(t, &jenkinsClientMock{}, nil)
		code := runCancelCmd(t, a, false, "deploy-app", "0")
		assert.Equal(t, exitUsage, code)
		assert.Contains(t, errBuf.String(), "invalid build number")
	})
}

func TestCancel_Errors(t *testing.T) {
	t.Run("unknown job is not-found", func(t *testing.T) {
		jc := &jenkinsClientMock{
			JobsFunc: func(context.Context) ([]jenkins.Job, error) { return sampleJobs(), nil },
		}
		a, _, errBuf := cancelApp(t, jc, nil)
		code := runCancelCmd(t, a, true, "ghost", "1")
		assert.Equal(t, exitNotFound, code)
		assert.Contains(t, errBuf.String(), "not found")
	})

	t.Run("missing build is not-found", func(t *testing.T) {
		jc := &jenkinsClientMock{
			BuildStatusFunc: func(context.Context, string) (jenkins.Build, error) {
				return jenkins.Build{}, jenkins.ErrNotFound
			},
		}
		a, _, errBuf := cancelApp(t, jc, nil)
		warmStatusCache(t)
		code := runCancelCmd(t, a, true, "deploy-app", "999")
		assert.Equal(t, exitNotFound, code)
		assert.Contains(t, errBuf.String(), "not found")
	})

	t.Run("auth failure is exit 2", func(t *testing.T) {
		jc := &jenkinsClientMock{
			BuildStatusFunc: func(context.Context, string) (jenkins.Build, error) {
				return jenkins.Build{}, jenkins.ErrAuth
			},
		}
		a, _, errBuf := cancelApp(t, jc, nil)
		warmStatusCache(t)
		code := runCancelCmd(t, a, true, "deploy-app", "42")
		assert.Equal(t, exitAuth, code)
		assert.NotEmpty(t, errBuf.String())
	})

	t.Run("permission denied on stop is exit 1 by contract", func(t *testing.T) {
		jc := &jenkinsClientMock{
			BuildStatusFunc: func(_ context.Context, buildURL string) (jenkins.Build, error) {
				return jenkins.Build{Number: 42, URL: buildURL, Building: true, Timestamp: 5}, nil
			},
			StopFunc: func(context.Context, string) error { return jenkins.ErrPermission },
		}
		a, _, errBuf := cancelApp(t, jc, nil)
		warmStatusCache(t)
		code := runCancelCmd(t, a, true, "deploy-app", "42")
		assert.Equal(t, exitUsage, code)
		assert.Contains(t, errBuf.String(), "permission denied")
		assert.Len(t, jc.StopCalls(), 1)
	})

	t.Run("stop error surfaces as a usage error", func(t *testing.T) {
		jc := &jenkinsClientMock{
			BuildStatusFunc: func(_ context.Context, buildURL string) (jenkins.Build, error) {
				return jenkins.Build{Number: 42, URL: buildURL, Building: true, Timestamp: 5}, nil
			},
			StopFunc: func(context.Context, string) error { return errors.New("boom") },
		}
		a, _, errBuf := cancelApp(t, jc, nil)
		warmStatusCache(t)
		code := runCancelCmd(t, a, true, "deploy-app", "42")
		assert.Equal(t, exitUsage, code)
		assert.Contains(t, errBuf.String(), "boom")
	})
}
