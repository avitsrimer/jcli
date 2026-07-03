package cli

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/avitsrimer/jcli/skill"
)

// runInstallSkill writes the embedded jenkins-cli Claude skill to <to>/skills/jenkins-cli, defaulting
// to ~/.claude when --to is unset. It always overwrites any existing destination so re-installs are
// idempotent.
func (c *installSkillCmd) runInstallSkill() error {
	to := c.To
	if to == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("resolve home dir: %w", err)
		}
		to = filepath.Join(home, ".claude")
	}
	dest := filepath.Join(to, "skills", "jenkins-cli")
	c.app.verbosef("installing skill to %s", dest)

	if err := os.RemoveAll(dest); err != nil {
		return fmt.Errorf("remove existing skill dir %s: %w", dest, err)
	}

	const root = "jenkins-cli"
	err := fs.WalkDir(skill.Files, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := path[len(root):]
		target := filepath.Join(dest, filepath.FromSlash(rel))
		if d.IsDir() {
			if mkErr := os.MkdirAll(target, 0o750); mkErr != nil {
				return fmt.Errorf("create dir %s: %w", target, mkErr)
			}
			return nil
		}
		data, err := skill.Files.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read embedded %s: %w", path, err)
		}
		if err := os.WriteFile(target, data, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", target, err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("install skill: %w", err)
	}

	fmt.Fprintf(c.app.stdout, "installed jenkins-cli skill to %s\n", dest)
	return nil
}
