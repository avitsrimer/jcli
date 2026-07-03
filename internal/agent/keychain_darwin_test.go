//go:build darwin

package agent

import "testing"

func TestOSStatusHint(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   string
	}{
		{"missing-entitlement status decodes to a safety-net hint", errSecMissingEntitlement,
			" (errSecMissingEntitlement: unexpected for the file-based login keychain)"},
		{"unknown status has no hint", -1, ""},
		{"success has no hint", 0, ""},
		{"item-not-found has no hint here", errSecItemNotFound, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := osStatusHint(tt.status); got != tt.want {
				t.Fatalf("osStatusHint(%d) = %q, want %q", tt.status, got, tt.want)
			}
		})
	}
}
