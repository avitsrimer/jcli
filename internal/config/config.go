// Package config manages jcli profiles persisted to ~/.config/jcli/config.json.
// the file holds no secrets — only profile name, Jenkins URL, and username; tokens
// live in the Keychain via the agent. all writes are atomic (temp+rename) with 0600
// perms and a 0700 parent directory.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrNotFound is returned when a requested profile does not exist.
var ErrNotFound = errors.New("profile not found")

// envProfile is the environment variable consulted by Resolve after the explicit flag.
const envProfile = "JCLI_PROFILE"

// DefaultProfileName is the profile name login falls back to when no --profile flag,
// JCLI_PROFILE, or stored default is set, so the first login works with no arguments.
const DefaultProfileName = "default"

// Profile describes a single named Jenkins connection. It never carries a token.
type Profile struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	Username string `json:"username"`
}

// Config is the on-disk document: the default profile name plus all known profiles.
type Config struct {
	Default  string    `json:"default"`
	Profiles []Profile `json:"profiles"`
}

// Path returns the config file location, honoring XDG_CONFIG_HOME, falling back to
// ~/.config/jcli/config.json.
func Path() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home dir: %w", err)
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "jcli", "config.json"), nil
}

// Load reads and parses the config at Path. A missing file yields an empty Config
// (not an error) so first-run flows work without special-casing.
func Load() (*Config, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	return loadFrom(path)
}

// loadFrom is the testable core of Load, reading an explicit path.
func loadFrom(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	return &cfg, nil
}

// Save atomically persists the config to Path, creating the parent dir with 0700.
func (c *Config) Save() error {
	path, err := Path()
	if err != nil {
		return err
	}
	return c.saveTo(path)
}

// saveTo is the testable core of Save, writing to an explicit path via temp+rename.
func (c *Config) saveTo(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config dir %s: %w", dir, err)
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}

	tmp, err := os.CreateTemp(dir, "config-*.json.tmp")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpName := tmp.Name()
	// best-effort cleanup if we bail before the rename succeeds.
	defer func() { _ = os.Remove(tmpName) }()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp config: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}

	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename temp config to %s: %w", path, err)
	}
	return nil
}

// Resolve picks the active profile name: explicit flag → JCLI_PROFILE env → Default.
// it returns an empty string when none is set, leaving the caller to error appropriately.
func (c *Config) Resolve(flag string) string {
	if flag != "" {
		return flag
	}
	if env := os.Getenv(envProfile); env != "" {
		return env
	}
	return c.Default
}

// Get returns the named profile or ErrNotFound.
func (c *Config) Get(name string) (Profile, error) {
	for _, p := range c.Profiles {
		if p.Name == name {
			return p, nil
		}
	}
	return Profile{}, fmt.Errorf("%q: %w", name, ErrNotFound)
}

// Upsert inserts a new profile or replaces an existing one with the same name.
func (c *Config) Upsert(p Profile) {
	for i := range c.Profiles {
		if c.Profiles[i].Name == p.Name {
			c.Profiles[i] = p
			return
		}
	}
	c.Profiles = append(c.Profiles, p)
}

// Remove deletes the named profile. It also clears Default if it pointed at the removed
// profile. Removing an unknown profile returns ErrNotFound.
func (c *Config) Remove(name string) error {
	for i := range c.Profiles {
		if c.Profiles[i].Name == name {
			c.Profiles = append(c.Profiles[:i], c.Profiles[i+1:]...)
			if c.Default == name {
				c.Default = ""
			}
			return nil
		}
	}
	return fmt.Errorf("%q: %w", name, ErrNotFound)
}

// SetDefault marks the named profile as the default. The profile must already exist.
func (c *Config) SetDefault(name string) error {
	if _, err := c.Get(name); err != nil {
		return err
	}
	c.Default = name
	return nil
}
