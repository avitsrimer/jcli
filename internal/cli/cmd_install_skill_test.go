package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/avitsrimer/jcli/internal/config"
	"github.com/avitsrimer/jcli/skill"
)

func TestInstallSkill(t *testing.T) {
	embedded, err := skill.Files.ReadFile("jenkins-cli/SKILL.md")
	require.NoError(t, err)

	t.Run("--to writes the embedded skill", func(t *testing.T) {
		tmp := t.TempDir()
		a, out, _ := newTestApp(&config.Config{})
		cmd := &installSkillCmd{app: a, To: tmp}

		require.NoError(t, cmd.runInstallSkill())

		dest := filepath.Join(tmp, "skills", "jenkins-cli")
		skillPath := filepath.Join(dest, "SKILL.md")
		got, err := os.ReadFile(skillPath)
		require.NoError(t, err)
		assert.Equal(t, embedded, got, "installed SKILL.md should equal the embedded bytes")
		assert.Contains(t, out.String(), "installed jenkins-cli skill to "+dest)

		dirInfo, err := os.Stat(dest)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o755), dirInfo.Mode().Perm(), "skill dir should be 0o755")
		fileInfo, err := os.Stat(skillPath)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o644), fileInfo.Mode().Perm(), "skill file should be 0o644")
	})

	t.Run("overwrites a stale destination", func(t *testing.T) {
		tmp := t.TempDir()
		dest := filepath.Join(tmp, "skills", "jenkins-cli")
		require.NoError(t, os.MkdirAll(dest, 0o755))
		stale := filepath.Join(dest, "stale.txt")
		require.NoError(t, os.WriteFile(stale, []byte("old"), 0o644))
		// a stale SKILL.md with different bytes must be replaced by the embedded content
		skillPath := filepath.Join(dest, "SKILL.md")
		require.NoError(t, os.WriteFile(skillPath, []byte("stale skill body"), 0o644))

		a, _, _ := newTestApp(&config.Config{})
		cmd := &installSkillCmd{app: a, To: tmp}
		require.NoError(t, cmd.runInstallSkill())

		_, err := os.Stat(stale)
		assert.True(t, os.IsNotExist(err), "stale file should be removed by os.RemoveAll")
		got, err := os.ReadFile(skillPath)
		require.NoError(t, err)
		assert.Equal(t, embedded, got, "stale SKILL.md should be replaced with the embedded bytes")
	})

	t.Run("returns a wrapped error when the destination is unwritable", func(t *testing.T) {
		tmp := t.TempDir()
		// a regular file in the parent chain makes os.MkdirAll fail with ENOTDIR
		blocker := filepath.Join(tmp, "blocker")
		require.NoError(t, os.WriteFile(blocker, []byte("not a dir"), 0o644))

		a, _, _ := newTestApp(&config.Config{})
		cmd := &installSkillCmd{app: a, To: blocker}
		err := cmd.runInstallSkill()
		require.Error(t, err)
		// the error is wrapped with context (%w) and surfaces the offending path
		assert.Contains(t, err.Error(), "skill dir", "error should be wrapped with install context")
		assert.ErrorContains(t, err, "blocker", "error should name the offending destination path")
	})

	t.Run("Execute records the failure exit code on install error", func(t *testing.T) {
		tmp := t.TempDir()
		blocker := filepath.Join(tmp, "blocker")
		require.NoError(t, os.WriteFile(blocker, []byte("not a dir"), 0o644))

		a, _, errBuf := newTestApp(&config.Config{})
		cmd := &installSkillCmd{app: a, To: blocker}
		// Execute returns nil by design (app.fail swallows the error after recording it) and records
		// the failure exit code while printing the error to stderr.
		require.NoError(t, cmd.Execute(nil))
		assert.Equal(t, exitUsage, a.lastExit)
		assert.NotEmpty(t, errBuf.String())
	})

	t.Run("defaults to ~/.claude", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		a, _, _ := newTestApp(&config.Config{})
		cmd := &installSkillCmd{app: a}

		require.NoError(t, cmd.runInstallSkill())

		assert.FileExists(t, filepath.Join(home, ".claude", "skills", "jenkins-cli", "SKILL.md"))
	})

	t.Run("Execute writes to stdout and records exit ok", func(t *testing.T) {
		tmp := t.TempDir()
		a, out, errBuf := newTestApp(&config.Config{})
		cmd := &installSkillCmd{app: a, To: tmp}

		require.NoError(t, cmd.Execute(nil))

		assert.Equal(t, exitOK, a.lastExit)
		assert.Empty(t, errBuf.String())
		assert.Contains(t, out.String(), "installed jenkins-cli skill to ")
		assert.FileExists(t, filepath.Join(tmp, "skills", "jenkins-cli", "SKILL.md"))
	})
}
