package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestRunDelegatesToCLI confirms main.run hands non-agent args to internal/cli, which maps an
// unknown command to a usage exit. The full dispatch/flag/exit-code matrix is covered in
// internal/cli; here we only assert the delegation seam.
func TestRunDelegatesToCLI(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want int
	}{
		{name: "no command prints usage", args: nil, want: 1},
		{name: "unknown command", args: []string{"frobnicate"}, want: 1},
		{name: "known but unimplemented command", args: []string{"list"}, want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// keep config/cache lookups inside the test sandbox.
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			var out, errBuf bytes.Buffer
			assert.Equal(t, tt.want, run(tt.args, &out, &errBuf))
		})
	}
}
