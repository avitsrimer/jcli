package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPath(t *testing.T) {
	t.Run("honors XDG_CONFIG_HOME", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
		p, err := Path()
		require.NoError(t, err)
		assert.Equal(t, "/tmp/xdg/jcli/config.json", p)
	})

	t.Run("falls back to home .config", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "")
		home, err := os.UserHomeDir()
		require.NoError(t, err)
		p, err := Path()
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(home, ".config", "jcli", "config.json"), p)
	})
}

func TestLoadSaveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	want := &Config{
		Default: "work",
		Profiles: []Profile{
			{Name: "work", URL: "https://ci.example.com", Username: "alice"},
			{Name: "home", URL: "https://home.example.com", Username: "bob"},
		},
	}
	require.NoError(t, want.Save())

	got, err := Load()
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestSavePermsAndDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	cfg := &Config{Default: "work", Profiles: []Profile{{Name: "work", URL: "u", Username: "a"}}}
	require.NoError(t, cfg.Save())

	path, err := Path()
	require.NoError(t, err)

	fi, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm(), "config file must be 0600")

	di, err := os.Stat(filepath.Dir(path))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), di.Mode().Perm(), "config dir must be 0700")
}

func TestSaveLeavesNoTempBehind(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	cfg := &Config{Default: "work", Profiles: []Profile{{Name: "work"}}}
	require.NoError(t, cfg.Save())

	path, err := Path()
	require.NoError(t, err)
	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "only config.json should remain, no temp file")
	assert.Equal(t, "config.json", entries[0].Name())
}

func TestLoadMissingFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, &Config{}, cfg, "missing file yields empty config")
}

func TestLoadMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(path, []byte("{not json"), 0o600))

	_, err := loadFrom(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse config")
}

func TestResolvePrecedence(t *testing.T) {
	cfg := &Config{Default: "default-p"}

	t.Run("flag wins", func(t *testing.T) {
		t.Setenv(envProfile, "env-p")
		assert.Equal(t, "flag-p", cfg.Resolve("flag-p"))
	})

	t.Run("env beats default", func(t *testing.T) {
		t.Setenv(envProfile, "env-p")
		assert.Equal(t, "env-p", cfg.Resolve(""))
	})

	t.Run("default when nothing set", func(t *testing.T) {
		t.Setenv(envProfile, "")
		assert.Equal(t, "default-p", cfg.Resolve(""))
	})

	t.Run("empty when all unset", func(t *testing.T) {
		t.Setenv(envProfile, "")
		empty := &Config{}
		assert.Equal(t, "", empty.Resolve(""))
	})
}

func TestGet(t *testing.T) {
	cfg := &Config{Profiles: []Profile{{Name: "work", URL: "u", Username: "a"}}}

	t.Run("found", func(t *testing.T) {
		p, err := cfg.Get("work")
		require.NoError(t, err)
		assert.Equal(t, "work", p.Name)
	})

	t.Run("not found", func(t *testing.T) {
		_, err := cfg.Get("missing")
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrNotFound)
	})
}

func TestUpsert(t *testing.T) {
	cfg := &Config{}

	t.Run("insert", func(t *testing.T) {
		cfg.Upsert(Profile{Name: "work", URL: "u1", Username: "a"})
		require.Len(t, cfg.Profiles, 1)
		assert.Equal(t, "u1", cfg.Profiles[0].URL)
	})

	t.Run("update in place", func(t *testing.T) {
		cfg.Upsert(Profile{Name: "work", URL: "u2", Username: "b"})
		require.Len(t, cfg.Profiles, 1, "update must not append")
		assert.Equal(t, "u2", cfg.Profiles[0].URL)
		assert.Equal(t, "b", cfg.Profiles[0].Username)
	})
}

func TestRemove(t *testing.T) {
	t.Run("removes profile", func(t *testing.T) {
		cfg := &Config{Profiles: []Profile{{Name: "a"}, {Name: "b"}}}
		require.NoError(t, cfg.Remove("a"))
		require.Len(t, cfg.Profiles, 1)
		assert.Equal(t, "b", cfg.Profiles[0].Name)
	})

	t.Run("clears default when removed", func(t *testing.T) {
		cfg := &Config{Default: "a", Profiles: []Profile{{Name: "a"}}}
		require.NoError(t, cfg.Remove("a"))
		assert.Empty(t, cfg.Default)
	})

	t.Run("keeps default when other removed", func(t *testing.T) {
		cfg := &Config{Default: "a", Profiles: []Profile{{Name: "a"}, {Name: "b"}}}
		require.NoError(t, cfg.Remove("b"))
		assert.Equal(t, "a", cfg.Default)
	})

	t.Run("unknown profile", func(t *testing.T) {
		cfg := &Config{}
		err := cfg.Remove("missing")
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrNotFound)
	})
}

func TestSetDefault(t *testing.T) {
	t.Run("sets existing", func(t *testing.T) {
		cfg := &Config{Profiles: []Profile{{Name: "work"}}}
		require.NoError(t, cfg.SetDefault("work"))
		assert.Equal(t, "work", cfg.Default)
	})

	t.Run("rejects unknown", func(t *testing.T) {
		cfg := &Config{}
		err := cfg.SetDefault("missing")
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrNotFound)
	})
}

func TestSaveDiskFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	cfg := &Config{Default: "work", Profiles: []Profile{{Name: "work", URL: "u", Username: "a"}}}
	require.NoError(t, cfg.saveTo(path))

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	assert.Equal(t, "work", raw["default"])
	assert.Contains(t, string(data), "\n  ", "expected indented JSON")
}

func TestSaveToBadDir(t *testing.T) {
	// a path under a file (not a dir) cannot have a parent dir created.
	dir := t.TempDir()
	notADir := filepath.Join(dir, "file")
	require.NoError(t, os.WriteFile(notADir, []byte("x"), 0o600))

	cfg := &Config{}
	err := cfg.saveTo(filepath.Join(notADir, "sub", "config.json"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create config dir")
}
