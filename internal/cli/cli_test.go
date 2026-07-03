package cli

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/avitsrimer/jcli/internal/config"
	"github.com/avitsrimer/jcli/internal/creds"
	"github.com/avitsrimer/jcli/internal/jenkins"
)

// stubCreds is a no-op credsClient for dispatch/wiring tests; command bodies (Task 9+) exercise it.
type stubCreds struct{}

func (stubCreds) Token(string) (string, error)  { return "", nil }
func (stubCreds) SetToken(string, string) error { return nil }
func (stubCreds) DeleteToken(string) error      { return nil }
func (stubCreds) Flush() error                  { return nil }

// newTestApp builds an app with injected collaborators and buffers for output, bypassing newApp so
// no real config/creds/HTTP is touched.
func newTestApp(cfg *config.Config) (*app, *bytes.Buffer, *bytes.Buffer) {
	var out, errBuf bytes.Buffer
	a := &app{
		cfg:    cfg,
		creds:  stubCreds{},
		stdout: &out,
		stderr: &errBuf,
		global: &globalOpts{},
		factory: func(_, _, _ string) jenkinsClient {
			return &jenkinsClientMock{}
		},
	}
	return a, &out, &errBuf
}

func TestExitCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "nil is ok", err: nil, want: exitOK},
		{name: "jenkins auth", err: fmt.Errorf("whoami: %w", jenkins.ErrAuth), want: exitAuth},
		{name: "creds auth", err: fmt.Errorf("get token: %w", creds.ErrAuth), want: exitAuth},
		{name: "jenkins not found", err: fmt.Errorf("job: %w", jenkins.ErrNotFound), want: exitNotFound},
		{name: "build failed", err: fmt.Errorf("result UNSTABLE: %w", errBuildFailed), want: exitBuildFail},
		{name: "jenkins permission is other", err: jenkins.ErrPermission, want: exitUsage},
		{name: "plain error is usage", err: errors.New("boom"), want: exitUsage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, exitCode(tt.err))
		})
	}
}

func TestResolveProfile(t *testing.T) {
	cfg := &config.Config{
		Default: "work",
		Profiles: []config.Profile{
			{Name: "work", URL: "https://jenkins.example.com", Username: "alice"},
			{Name: "staging", URL: "https://staging.example.com", Username: "bob"},
		},
	}

	t.Run("flag overrides default", func(t *testing.T) {
		a, _, _ := newTestApp(cfg)
		a.global.Profile = "staging"
		p, err := a.resolveProfile()
		require.NoError(t, err)
		assert.Equal(t, "staging", p.Name)
	})

	t.Run("env over default", func(t *testing.T) {
		t.Setenv("JCLI_PROFILE", "staging")
		a, _, _ := newTestApp(cfg)
		p, err := a.resolveProfile()
		require.NoError(t, err)
		assert.Equal(t, "staging", p.Name)
	})

	t.Run("falls back to default", func(t *testing.T) {
		t.Setenv("JCLI_PROFILE", "")
		a, _, _ := newTestApp(cfg)
		p, err := a.resolveProfile()
		require.NoError(t, err)
		assert.Equal(t, "work", p.Name)
	})

	t.Run("no profile selected is an error", func(t *testing.T) {
		t.Setenv("JCLI_PROFILE", "")
		a, _, _ := newTestApp(&config.Config{})
		_, err := a.resolveProfile()
		require.Error(t, err)
	})

	t.Run("unknown profile is not-found", func(t *testing.T) {
		a, _, _ := newTestApp(cfg)
		a.global.Profile = "ghost"
		_, err := a.resolveProfile()
		require.ErrorIs(t, err, config.ErrNotFound)
	})
}

func TestRunDispatch(t *testing.T) {
	t.Run("known command dispatches and records exit", func(t *testing.T) {
		a, _, errBuf := newTestApp(&config.Config{})
		// build with an empty config has no profile to resolve → usage exit recorded by the command.
		code := a.run([]string{"build", "Logistics"})
		assert.Equal(t, exitUsage, code)
		assert.Contains(t, errBuf.String(), "no profile selected")
	})

	t.Run("unknown command is a parse/usage error", func(t *testing.T) {
		a, _, errBuf := newTestApp(&config.Config{})
		code := a.run([]string{"frobnicate"})
		assert.Equal(t, exitUsage, code)
		assert.NotEmpty(t, errBuf.String())
	})

	t.Run("global flags parse before the command", func(t *testing.T) {
		a, _, _ := newTestApp(&config.Config{})
		code := a.run([]string{"--profile", "work", "--json", "-v", "build", "Logistics"})
		assert.Equal(t, exitNotFound, code) // unknown profile "work" in empty config → not-found
		assert.Equal(t, "work", a.global.Profile)
		assert.True(t, a.global.JSON)
		assert.True(t, a.global.Verbose)
	})

	t.Run("param pre-parse lifts params before go-flags", func(t *testing.T) {
		a, _, _ := newTestApp(&config.Config{})
		// build is a known command with a --wait flag; the --param-* args must not reach go-flags
		// (which would reject them as unknown), and must land in app.buildParams.
		code := a.run([]string{"build", "Logistics", "--param-branch=main", "--wait", "--param-env=uat1"})
		assert.Equal(t, exitUsage, code) // empty config → no profile → usage exit
		assert.Equal(t, map[string]string{"branch": "main", "env": "uat1"}, a.buildParams)
	})

	t.Run("help is a clean exit", func(t *testing.T) {
		a, out, _ := newTestApp(&config.Config{})
		code := a.run([]string{"--help"})
		assert.Equal(t, exitOK, code)
		assert.NotEmpty(t, out.String())
	})

	t.Run("version prints the version and exits ok with no subcommand", func(t *testing.T) {
		a, out, errBuf := newTestApp(&config.Config{})
		code := a.run([]string{"--version"})
		// the pre-parse scan must handle --version without a subcommand, so it never hits the
		// flags.ErrCommandRequired/exit-1 path (which the naive post-parse check would).
		assert.Equal(t, exitOK, code)
		assert.Contains(t, out.String(), version)
		assert.Empty(t, errBuf.String())
	})
}
