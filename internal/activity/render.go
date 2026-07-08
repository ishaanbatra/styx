package activity

import (
	"fmt"
	"time"
)

// DefaultStall is the idle duration past which an agent is flagged ⚠.
const DefaultStall = 90 * time.Second

// Render paints one line per agent plus an optional watcher note. now is
// injected so output is deterministic. A live agent shows "▸ <last> <idle> ago";
// past the stall threshold it flips to "⚠ idle <idle>"; a finished agent shows
// "✓ done (<elapsed>)".
func Render(states []AgentState, note string, stall time.Duration, now time.Time) []string {
	out := make([]string, 0, len(states)+1)
	for _, s := range states {
		if s.Done {
			out = append(out, fmt.Sprintf("%-8s ✓ done (%s)", s.Label, short(s.Elapsed)))
			continue
		}
		idle := now.Sub(s.LastAt)
		if idle > stall {
			out = append(out, fmt.Sprintf("%-8s ⚠ idle %-6s (last: %s)", s.Label, short(idle), s.Last))
			continue
		}
		out = append(out, fmt.Sprintf("%-8s ▸ %-30s %s ago", s.Label, s.Last, short(idle)))
	}
	if note != "" {
		out = append(out, "watch (ollama): "+note)
	}
	return out
}

// short renders a duration as 2s / 4m03s / 1h12m.
func short(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
}
