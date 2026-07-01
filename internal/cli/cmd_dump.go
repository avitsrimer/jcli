package cli

import (
	"encoding/json"
	"fmt"

	"github.com/avitsrimer/jcli/internal/cache"
)

// runDump emits the full cached job map for the active profile as formatted JSON. With --refresh it
// rebuilds the map from a fresh crawl (and persists it) before dumping. An empty/cold cache dumps a
// valid Map document with an empty jobs object.
func (c *dumpCmd) runDump() error {
	prof, client, err := c.app.clientFor()
	if err != nil {
		return err
	}
	m, err := cache.Load(prof.Name)
	if err != nil {
		return fmt.Errorf("load cache: %w", err)
	}

	if c.Refresh {
		if err := c.app.crawlAndSave(m, client, prof); err != nil {
			return err
		}
	}

	enc := json.NewEncoder(c.app.stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return fmt.Errorf("encode cache: %w", err)
	}
	return nil
}
