// Command jcli is a general-purpose macOS Jenkins CLI with multi-profile support,
// Keychain-backed credentials, and a cached job/param map. See DESIGN.md.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/avitsrimer/jcli/internal/agent"
	"github.com/avitsrimer/jcli/internal/cli"
)

// exitUsage is the exit code for the hidden agent-boot failure path; the rest of the exit-code
// contract (0 ok / 1 usage / 2 auth / 3 not-found / 4 build-failed) is owned by internal/cli.
const exitUsage = 1

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run handles the hidden agent mode and otherwise delegates to internal/cli for dispatch, flag
// parsing, and exit-code mapping. It is split out from main so tests can drive it with explicit
// args and writers.
func run(args []string, stdout, stderr io.Writer) int {
	// hidden agent mode: `jcli __agent` boots the credential-agent socket server. It is intercepted
	// here, before any flag parsing, so it never appears in CLI help output.
	if len(args) > 0 && args[0] == "__agent" {
		if err := agent.Run(); err != nil {
			fmt.Fprintf(stderr, "jcli: agent: %v\n", err)
			return exitUsage
		}
		return 0
	}

	return cli.Main(args, stdout, stderr)
}
