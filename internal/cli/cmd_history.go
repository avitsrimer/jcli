package cli

import (
	"fmt"
	"time"
)

// humanizeSince renders the time between tsMillis (a build's start, in epoch millis) and now as a
// compact relative string: "just now" (<60s), "Nm ago" (<60m), "Nh ago" (<24h), else "Nd ago". A
// zero/negative or future timestamp renders as "just now". Pure so callers pass app.clock() for
// deterministic tests.
func humanizeSince(now time.Time, tsMillis int64) string {
	if tsMillis <= 0 {
		return "just now"
	}
	d := now.Sub(time.UnixMilli(tsMillis))
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
