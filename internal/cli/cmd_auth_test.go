package cli

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/avitsrimer/jcli/internal/config"
	"github.com/avitsrimer/jcli/internal/jenkins"
)

// fakeCreds is a recording credsClient: it captures set/delete calls and can be primed to return an
// error so the auth commands' error paths are exercised.
type fakeCreds struct {
	setCalls    []string // "profile=token" pairs recorded by SetToken
	deleteCalls []string // profile names recorded by DeleteToken
	setErr      error
	deleteErr   error
}

func (f *fakeCreds) Token(string) (string, error) { return "", nil }

func (f *fakeCreds) SetToken(profile, token string) error {
	f.setCalls = append(f.setCalls, profile+"="+token)
	return f.setErr
}

func (f *fakeCreds) DeleteToken(profile string) error {
	f.deleteCalls = append(f.deleteCalls, profile)
	return f.deleteErr
}

func (f *fakeCreds) Flush() error { return nil }

// scriptedPrompter feeds canned responses for the login prompts. line answers promptLine in order,
// secret answers promptSecret; err, if set, fails the first prompt.
type scriptedPrompter struct {
	lines  []string
	line   int
	secret string
	err    error
}

func (p *scriptedPrompter) promptLine(string) (string, error) {
	if p.err != nil {
		return "", p.err
	}
	if p.line >= len(p.lines) {
		return "", nil
	}
	v := p.lines[p.line]
	p.line++
	return v, nil
}

func (p *scriptedPrompter) promptSecret(string) (string, error) {
	if p.err != nil {
		return "", p.err
	}
	return p.secret, nil
}

// authTestApp builds an app wired with a temp config dir, the given creds + jenkins mock, and a
// scripted prompter. It returns the app plus its output buffers.
func authTestApp(t *testing.T, cfg *config.Config, cr credsClient, jc *jenkinsClientMock, pr prompter) (*app, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("JCLI_PROFILE", "")
	var out, errBuf bytes.Buffer
	a := &app{
		cfg:    cfg,
		creds:  cr,
		stdout: &out,
		stderr: &errBuf,
		global: &globalOpts{},
		factory: func(_, _, _ string) jenkinsClient {
			return jc
		},
		promptFactory: func() prompter { return pr },
	}
	return a, &out, &errBuf
}

func okWhoAmI() *jenkinsClientMock {
	return &jenkinsClientMock{
		WhoAmIFunc: func(context.Context) (jenkins.Identity, error) {
			return jenkins.Identity{Name: "alice"}, nil
		},
	}
}

func TestLogin(t *testing.T) {
	t.Run("success persists profile and stores token", func(t *testing.T) {
		cr := &fakeCreds{}
		pr := &scriptedPrompter{lines: []string{"https://jenkins.example.com", "alice"}, secret: "s3cret"}
		a, out, _ := authTestApp(t, &config.Config{}, cr, okWhoAmI(), pr)
		a.global.Profile = "work"

		code := a.run([]string{"login"})
		require.Equal(t, exitOK, code)

		p, err := a.cfg.Get("work")
		require.NoError(t, err)
		assert.Equal(t, "https://jenkins.example.com", p.URL)
		assert.Equal(t, "alice", p.Username)
		assert.Equal(t, []string{"work=s3cret"}, cr.setCalls)
		assert.Contains(t, out.String(), "logged in")

		// config is persisted to disk: a fresh Load sees the profile.
		reloaded, err := config.Load()
		require.NoError(t, err)
		_, err = reloaded.Get("work")
		require.NoError(t, err)
	})

	t.Run("verify failure with bad token is auth exit and persists nothing", func(t *testing.T) {
		cr := &fakeCreds{}
		jc := &jenkinsClientMock{
			WhoAmIFunc: func(context.Context) (jenkins.Identity, error) {
				return jenkins.Identity{}, jenkins.ErrAuth
			},
		}
		pr := &scriptedPrompter{lines: []string{"https://jenkins.example.com", "alice"}, secret: "bad"}
		a, _, errBuf := authTestApp(t, &config.Config{}, cr, jc, pr)
		a.global.Profile = "work"

		code := a.run([]string{"login"})
		assert.Equal(t, exitAuth, code)
		assert.Empty(t, cr.setCalls, "token must not be stored on verify failure")
		_, err := a.cfg.Get("work")
		require.ErrorIs(t, err, config.ErrNotFound, "profile must not be persisted on verify failure")
		assert.NotEmpty(t, errBuf.String())
	})

	t.Run("re-login updates the existing profile", func(t *testing.T) {
		cfg := &config.Config{Profiles: []config.Profile{{Name: "work", URL: "https://old.example.com", Username: "bob"}}}
		cr := &fakeCreds{}
		pr := &scriptedPrompter{lines: []string{"https://new.example.com", "alice"}, secret: "newtok"}
		a, _, _ := authTestApp(t, cfg, cr, okWhoAmI(), pr)
		a.global.Profile = "work"

		code := a.run([]string{"login"})
		require.Equal(t, exitOK, code)

		assert.Len(t, a.cfg.Profiles, 1, "re-login must not duplicate the profile")
		p, err := a.cfg.Get("work")
		require.NoError(t, err)
		assert.Equal(t, "https://new.example.com", p.URL)
		assert.Equal(t, "alice", p.Username)
		assert.Equal(t, []string{"work=newtok"}, cr.setCalls)
	})

	t.Run("no profile name falls back to default and marks it the config default", func(t *testing.T) {
		cr := &fakeCreds{}
		pr := &scriptedPrompter{lines: []string{"https://jenkins.example.com", "alice"}, secret: "s3cret"}
		a, out, _ := authTestApp(t, &config.Config{}, cr, okWhoAmI(), pr)
		// no --profile flag, no JCLI_PROFILE, fresh config with no default.

		code := a.run([]string{"login"})
		require.Equal(t, exitOK, code)

		p, err := a.cfg.Get(config.DefaultProfileName)
		require.NoError(t, err)
		assert.Equal(t, "https://jenkins.example.com", p.URL)
		assert.Equal(t, []string{config.DefaultProfileName + "=s3cret"}, cr.setCalls)
		assert.Equal(t, config.DefaultProfileName, a.cfg.Default, "first profile must become the config default")
		assert.Contains(t, out.String(), "logged in")

		// persisted to disk: a fresh Load sees both the profile and the default.
		reloaded, err := config.Load()
		require.NoError(t, err)
		assert.Equal(t, config.DefaultProfileName, reloaded.Default)
		_, err = reloaded.Get(config.DefaultProfileName)
		require.NoError(t, err)
	})

	t.Run("login without flag resolves the existing default and does not clobber it", func(t *testing.T) {
		cfg := &config.Config{
			Default:  "work",
			Profiles: []config.Profile{{Name: "work", URL: "https://old.example.com", Username: "bob"}},
		}
		cr := &fakeCreds{}
		pr := &scriptedPrompter{lines: []string{"https://new.example.com", "alice"}, secret: "newtok"}
		a, _, _ := authTestApp(t, cfg, cr, okWhoAmI(), pr)
		// no --profile flag: Resolve must return the stored default "work".

		code := a.run([]string{"login"})
		require.Equal(t, exitOK, code)

		assert.Equal(t, "work", a.cfg.Default, "existing default must be preserved")
		assert.Len(t, a.cfg.Profiles, 1, "must update the resolved default profile, not create 'default'")
		p, err := a.cfg.Get("work")
		require.NoError(t, err)
		assert.Equal(t, "https://new.example.com", p.URL)
		assert.Equal(t, []string{"work=newtok"}, cr.setCalls)
	})

	t.Run("empty url is a usage error", func(t *testing.T) {
		pr := &scriptedPrompter{lines: []string{"", "alice"}, secret: "tok"}
		a, _, _ := authTestApp(t, &config.Config{}, &fakeCreds{}, okWhoAmI(), pr)
		a.global.Profile = "work"
		code := a.run([]string{"login"})
		assert.Equal(t, exitUsage, code)
	})

	t.Run("--url and --username flags skip both line prompts", func(t *testing.T) {
		cr := &fakeCreds{}
		// no lines scripted: a line prompt would return "" and fail the required-field check.
		pr := &scriptedPrompter{secret: "s3cret"}
		a, _, _ := authTestApp(t, &config.Config{}, cr, okWhoAmI(), pr)
		a.global.Profile = "work"

		code := a.run([]string{"login", "--url", "https://jenkins.example.com", "--username", "alice"})
		require.Equal(t, exitOK, code)
		assert.Equal(t, 0, pr.line, "no interactive line prompts when --url and --username are given")

		p, err := a.cfg.Get("work")
		require.NoError(t, err)
		assert.Equal(t, "https://jenkins.example.com", p.URL)
		assert.Equal(t, "alice", p.Username)
		assert.Equal(t, []string{"work=s3cret"}, cr.setCalls)
	})

	t.Run("--username flag still prompts for the url", func(t *testing.T) {
		cr := &fakeCreds{}
		pr := &scriptedPrompter{lines: []string{"https://jenkins.example.com"}, secret: "s3cret"}
		a, _, _ := authTestApp(t, &config.Config{}, cr, okWhoAmI(), pr)
		a.global.Profile = "work"

		code := a.run([]string{"login", "--username", "alice"})
		require.Equal(t, exitOK, code)
		assert.Equal(t, 1, pr.line, "only the url is prompted when --username is given")

		p, err := a.cfg.Get("work")
		require.NoError(t, err)
		assert.Equal(t, "https://jenkins.example.com", p.URL)
		assert.Equal(t, "alice", p.Username)
	})

	t.Run("token is still verified before persisting even with flags", func(t *testing.T) {
		cr := &fakeCreds{}
		jc := &jenkinsClientMock{
			WhoAmIFunc: func(context.Context) (jenkins.Identity, error) {
				return jenkins.Identity{}, jenkins.ErrAuth
			},
		}
		pr := &scriptedPrompter{secret: "bad"}
		a, _, _ := authTestApp(t, &config.Config{}, cr, jc, pr)
		a.global.Profile = "work"

		code := a.run([]string{"login", "--url", "https://jenkins.example.com", "--username", "alice"})
		assert.Equal(t, exitAuth, code)
		assert.Empty(t, cr.setCalls, "token must not be stored on verify failure")
		_, err := a.cfg.Get("work")
		assert.ErrorIs(t, err, config.ErrNotFound)
	})
}

func TestProfile(t *testing.T) {
	baseCfg := func() *config.Config {
		return &config.Config{
			Default: "work",
			Profiles: []config.Profile{
				{Name: "work", URL: "https://work.example.com", Username: "alice"},
				{Name: "staging", URL: "https://staging.example.com", Username: "bob"},
			},
		}
	}

	t.Run("list marks the default", func(t *testing.T) {
		a, out, _ := authTestApp(t, baseCfg(), &fakeCreds{}, okWhoAmI(), &scriptedPrompter{})
		code := a.run([]string{"profile", "list"})
		require.Equal(t, exitOK, code)
		s := out.String()
		assert.Contains(t, s, "* work")
		assert.Contains(t, s, "  staging")
	})

	t.Run("bare profile defaults to list", func(t *testing.T) {
		a, out, _ := authTestApp(t, baseCfg(), &fakeCreds{}, okWhoAmI(), &scriptedPrompter{})
		code := a.run([]string{"profile"})
		require.Equal(t, exitOK, code)
		assert.Contains(t, out.String(), "* work")
	})

	t.Run("use sets the default and persists", func(t *testing.T) {
		a, _, _ := authTestApp(t, baseCfg(), &fakeCreds{}, okWhoAmI(), &scriptedPrompter{})
		code := a.run([]string{"profile", "use", "staging"})
		require.Equal(t, exitOK, code)
		assert.Equal(t, "staging", a.cfg.Default)

		reloaded, err := config.Load()
		require.NoError(t, err)
		assert.Equal(t, "staging", reloaded.Default)
	})

	t.Run("rm removes profile and deletes token", func(t *testing.T) {
		cr := &fakeCreds{}
		a, _, _ := authTestApp(t, baseCfg(), cr, okWhoAmI(), &scriptedPrompter{})
		code := a.run([]string{"profile", "rm", "staging"})
		require.Equal(t, exitOK, code)
		_, err := a.cfg.Get("staging")
		require.ErrorIs(t, err, config.ErrNotFound)
		assert.Equal(t, []string{"staging"}, cr.deleteCalls)
	})

	t.Run("use unknown profile is not-found", func(t *testing.T) {
		a, _, errBuf := authTestApp(t, baseCfg(), &fakeCreds{}, okWhoAmI(), &scriptedPrompter{})
		code := a.run([]string{"profile", "use", "ghost"})
		assert.Equal(t, exitNotFound, code)
		assert.NotEmpty(t, errBuf.String())
	})

	t.Run("rm unknown profile is not-found", func(t *testing.T) {
		cr := &fakeCreds{}
		a, _, _ := authTestApp(t, baseCfg(), cr, okWhoAmI(), &scriptedPrompter{})
		code := a.run([]string{"profile", "rm", "ghost"})
		assert.Equal(t, exitNotFound, code)
		assert.Empty(t, cr.deleteCalls, "no token delete when the profile does not exist")
	})

	t.Run("unknown action is a usage error", func(t *testing.T) {
		a, _, _ := authTestApp(t, baseCfg(), &fakeCreds{}, okWhoAmI(), &scriptedPrompter{})
		code := a.run([]string{"profile", "frob"})
		assert.Equal(t, exitUsage, code)
	})

	t.Run("use without name is a usage error", func(t *testing.T) {
		a, _, _ := authTestApp(t, baseCfg(), &fakeCreds{}, okWhoAmI(), &scriptedPrompter{})
		code := a.run([]string{"profile", "use"})
		assert.Equal(t, exitUsage, code)
	})
}

func TestLogout(t *testing.T) {
	baseCfg := func() *config.Config {
		return &config.Config{
			Default:  "work",
			Profiles: []config.Profile{{Name: "work", URL: "https://work.example.com", Username: "alice"}},
		}
	}

	t.Run("deletes token for resolved profile", func(t *testing.T) {
		cr := &fakeCreds{}
		a, out, _ := authTestApp(t, baseCfg(), cr, okWhoAmI(), &scriptedPrompter{})
		code := a.run([]string{"logout"})
		require.Equal(t, exitOK, code)
		assert.Equal(t, []string{"work"}, cr.deleteCalls)
		// profile is retained without --purge.
		_, err := a.cfg.Get("work")
		require.NoError(t, err)
		assert.Contains(t, out.String(), "logged out")
	})

	t.Run("purge also removes the profile", func(t *testing.T) {
		cr := &fakeCreds{}
		a, _, _ := authTestApp(t, baseCfg(), cr, okWhoAmI(), &scriptedPrompter{})
		code := a.run([]string{"logout", "--purge"})
		require.Equal(t, exitOK, code)
		assert.Equal(t, []string{"work"}, cr.deleteCalls)
		_, err := a.cfg.Get("work")
		require.ErrorIs(t, err, config.ErrNotFound)

		reloaded, err := config.Load()
		require.NoError(t, err)
		_, err = reloaded.Get("work")
		assert.ErrorIs(t, err, config.ErrNotFound)
	})

	t.Run("unknown profile via flag is not-found", func(t *testing.T) {
		a, _, _ := authTestApp(t, baseCfg(), &fakeCreds{}, okWhoAmI(), &scriptedPrompter{})
		a.global.Profile = "ghost"
		code := a.run([]string{"--profile", "ghost", "logout"})
		assert.Equal(t, exitNotFound, code)
	})

	t.Run("delete-token error surfaces", func(t *testing.T) {
		cr := &fakeCreds{deleteErr: errors.New("agent down")}
		a, _, errBuf := authTestApp(t, baseCfg(), cr, okWhoAmI(), &scriptedPrompter{})
		code := a.run([]string{"logout"})
		assert.Equal(t, exitUsage, code)
		assert.NotEmpty(t, errBuf.String())
	})
}
