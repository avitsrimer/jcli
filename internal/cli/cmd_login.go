package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/avitsrimer/jcli/internal/config"
)

// prompter abstracts the interactive prompts the login command makes so tests can drive them
// without a real TTY. promptLine reads a single echoed line (URL, username); promptSecret reads a
// secret with echo suppressed (the API token, read no-echo from the TTY, never from argv/env).
type prompter interface {
	promptLine(label string) (string, error)
	promptSecret(label string) (string, error)
}

// ttyPrompter is the production prompter. It writes labels to out, reads echoed lines from in, and
// reads secrets no-echo from the controlling terminal (/dev/tty, falling back to stdin) via
// golang.org/x/term so the token never appears on screen or in shell history.
type ttyPrompter struct {
	in  io.Reader
	out io.Writer
}

// promptLine writes the label and reads one trimmed line of echoed input.
func (p ttyPrompter) promptLine(label string) (string, error) {
	fmt.Fprint(p.out, label)
	r := bufio.NewReader(p.in)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("read %s: %w", strings.TrimSpace(label), err)
	}
	return strings.TrimSpace(line), nil
}

// promptSecret writes the label and reads a secret with terminal echo disabled. It prefers
// /dev/tty so the secret is read from the terminal even when stdin is redirected; if that cannot be
// opened it falls back to stdin's fd. The trailing newline the user types is not echoed, so we emit
// one ourselves to keep subsequent output on its own line.
func (p ttyPrompter) promptSecret(label string) (string, error) {
	fmt.Fprint(p.out, label)
	defer fmt.Fprintln(p.out)

	fd := -1
	if tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
		defer func() { _ = tty.Close() }()
		fd = int(tty.Fd())
	} else if f, ok := p.in.(*os.File); ok {
		fd = int(f.Fd())
	}
	if fd < 0 || !term.IsTerminal(fd) {
		return "", fmt.Errorf("read %s: no terminal available for no-echo input", strings.TrimSpace(label))
	}

	secret, err := term.ReadPassword(fd)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", strings.TrimSpace(label), err)
	}
	return strings.TrimSpace(string(secret)), nil
}

// runLogin prompts for URL, username, and token, verifies the token against Jenkins via WhoAmI,
// then persists the profile to config and the token to the agent. Re-running with the same profile
// name updates the existing entry. The token is read no-echo and never logged.
func (c *loginCmd) runLogin() error {
	// login bootstraps the tool, so an unresolved profile falls back to the well-known
	// "default" name rather than erroring — `jcli login` with no arguments must work.
	name := c.app.cfg.Resolve(c.app.global.Profile)
	if name == "" {
		name = config.DefaultProfileName
	}

	p := c.app.prompter()

	// --url / --username skip their prompt; only fields left empty are asked for interactively.
	var err error
	url := strings.TrimSpace(c.URL)
	if url == "" {
		if url, err = p.promptLine("Jenkins URL: "); err != nil {
			return err
		}
	}
	if url == "" {
		return errors.New("a Jenkins URL is required")
	}
	username := strings.TrimSpace(c.Username)
	if username == "" {
		if username, err = p.promptLine("Username: "); err != nil {
			return err
		}
	}
	if username == "" {
		return errors.New("username is required")
	}
	token, err := p.promptSecret("API token: ")
	if err != nil {
		return err
	}
	if token == "" {
		return errors.New("API token is required")
	}

	// verify the token before persisting anything: a bad token must fail with the auth exit code and
	// leave config/keychain untouched.
	client := c.app.factory(url, username, token)
	if _, err := client.WhoAmI(context.Background()); err != nil {
		return fmt.Errorf("verify token: %w", err)
	}

	c.app.cfg.Upsert(config.Profile{Name: name, URL: url, Username: username})
	// the first profile logged in (when no default is set yet) becomes the default so
	// list/get/build resolve a profile without requiring --profile on every call.
	if c.app.cfg.Default == "" {
		c.app.cfg.Default = name
	}
	if err := c.app.cfg.Save(); err != nil {
		return fmt.Errorf("save profile %q: %w", name, err)
	}
	if err := c.app.creds.SetToken(name, token); err != nil {
		return fmt.Errorf("store token for %q: %w", name, err)
	}

	fmt.Fprintf(c.app.stdout, "logged in to profile %q as %s\n", name, username)
	return nil
}
