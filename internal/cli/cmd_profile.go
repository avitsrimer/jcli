package cli

import (
	"errors"
	"fmt"
)

// runProfile dispatches the profile subcommand on its first positional arg: list (or no arg) shows
// all profiles marking the default, use <p> sets the default, rm <p> removes the profile from config
// and deletes its stored token. Unknown actions are usage errors.
func (c *profileCmd) runProfile(args []string) error {
	action := "list"
	if len(args) > 0 {
		action = args[0]
	}
	switch action {
	case "list":
		return c.list()
	case "use":
		if len(args) < 2 {
			return errors.New("profile use: missing profile name")
		}
		return c.use(args[1])
	case "rm":
		if len(args) < 2 {
			return errors.New("profile rm: missing profile name")
		}
		return c.rm(args[1])
	default:
		return fmt.Errorf("profile: unknown action %q (want list|use|rm)", action)
	}
}

// list prints every profile name, marking the default with an asterisk.
func (c *profileCmd) list() error {
	if len(c.app.cfg.Profiles) == 0 {
		fmt.Fprintln(c.app.stdout, "no profiles configured; run 'jcli login' to add one")
		return nil
	}
	for _, p := range c.app.cfg.Profiles {
		marker := " "
		if p.Name == c.app.cfg.Default {
			marker = "*"
		}
		fmt.Fprintf(c.app.stdout, "%s %s\t%s\t%s\n", marker, p.Name, p.URL, p.Username)
	}
	return nil
}

// use marks the named profile as the default. An unknown profile surfaces config.ErrNotFound.
func (c *profileCmd) use(name string) error {
	if err := c.app.cfg.SetDefault(name); err != nil {
		return fmt.Errorf("set default profile: %w", err)
	}
	if err := c.app.cfg.Save(); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	fmt.Fprintf(c.app.stdout, "default profile is now %q\n", name)
	return nil
}

// rm removes the named profile from config and deletes its stored token. An unknown profile
// surfaces config.ErrNotFound; the token delete is best-effort once config removal succeeds.
func (c *profileCmd) rm(name string) error {
	if err := c.app.cfg.Remove(name); err != nil {
		return fmt.Errorf("remove profile: %w", err)
	}
	if err := c.app.cfg.Save(); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	if err := c.app.creds.DeleteToken(name); err != nil {
		return fmt.Errorf("delete token for %q: %w", name, err)
	}
	fmt.Fprintf(c.app.stdout, "removed profile %q\n", name)
	return nil
}
