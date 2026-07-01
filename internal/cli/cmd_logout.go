package cli

import "fmt"

// runLogout deletes the stored token for the resolved profile and, with --purge, also removes the
// profile from config. The profile is resolved via the standard flag → JCLI_PROFILE → default path;
// an unset/unknown profile is a usage/not-found error.
func (c *logoutCmd) runLogout() error {
	prof, err := c.app.resolveProfile()
	if err != nil {
		return err
	}
	if err := c.app.creds.DeleteToken(prof.Name); err != nil {
		return fmt.Errorf("delete token for %q: %w", prof.Name, err)
	}
	if c.Purge {
		if err := c.app.cfg.Remove(prof.Name); err != nil {
			return fmt.Errorf("remove profile %q: %w", prof.Name, err)
		}
		if err := c.app.cfg.Save(); err != nil {
			return fmt.Errorf("save config: %w", err)
		}
		fmt.Fprintf(c.app.stdout, "logged out and removed profile %q\n", prof.Name)
		return nil
	}
	fmt.Fprintf(c.app.stdout, "logged out of profile %q\n", prof.Name)
	return nil
}
