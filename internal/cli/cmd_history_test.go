package cli

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestHumanizeSince(t *testing.T) {
	// fixed reference point so all deltas are deterministic; buckets are <60s just now, <60m Nm ago,
	// <24h Nh ago, else Nd ago, with zero/future timestamps also rendering just now.
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	ms := func(d time.Duration) int64 { return now.Add(-d).UnixMilli() }

	tests := []struct {
		name    string
		tsMilli int64
		want    string
	}{
		{name: "zero timestamp", tsMilli: 0, want: "just now"},
		{name: "negative timestamp", tsMilli: -1, want: "just now"},
		{name: "future timestamp", tsMilli: now.Add(time.Hour).UnixMilli(), want: "just now"},
		{name: "just started", tsMilli: ms(0), want: "just now"},
		{name: "30s ago", tsMilli: ms(30 * time.Second), want: "just now"},
		{name: "59s ago", tsMilli: ms(59 * time.Second), want: "just now"},
		{name: "60s ago", tsMilli: ms(60 * time.Second), want: "1m ago"},
		{name: "5m ago", tsMilli: ms(5 * time.Minute), want: "5m ago"},
		{name: "59m ago", tsMilli: ms(59 * time.Minute), want: "59m ago"},
		{name: "60m ago", tsMilli: ms(60 * time.Minute), want: "1h ago"},
		{name: "2h ago", tsMilli: ms(2 * time.Hour), want: "2h ago"},
		{name: "23h ago", tsMilli: ms(23 * time.Hour), want: "23h ago"},
		{name: "24h ago", tsMilli: ms(24 * time.Hour), want: "1d ago"},
		{name: "3d ago", tsMilli: ms(3 * 24 * time.Hour), want: "3d ago"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, humanizeSince(now, tc.tsMilli))
		})
	}
}
