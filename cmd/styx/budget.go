package main

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/ishaanbatra/styx/internal/budget"
	"github.com/ishaanbatra/styx/internal/config"
)

func cmdBudget(args []string) error {
	tr, err := budget.Default()
	if err != nil {
		return err
	}
	defer tr.Close()

	// Load routing to get configured message limits; fall back to builtins
	// gracefully if no config exists yet.
	r, err := config.LoadRouting()
	if err != nil {
		// No config yet — builtins will be used (zero Routing{} triggers fallback).
		r = config.Routing{}
	}
	seedMessageLimits(tr, r)

	ctx := context.Background()
	for _, ch := range []string{"claude", "codex", "agy", "ollama", "mlx"} {
		st, err := tr.State(ctx, ch)
		if err != nil {
			fmt.Printf("%-8s  error: %v\n", ch, err)
			continue
		}

		cooldown := ""
		if !st.CooldownUntil.IsZero() {
			cooldown = fmt.Sprintf(" (cooldown until %s)", st.CooldownUntil.Format(time.RFC3339))
		}

		// Unlimited channels (no limits configured).
		if st.SessionLimit == 0 && st.WeeklyLimit == 0 {
			fmt.Printf("%-8s  unlimited (local)%s\n", ch, cooldown)
			continue
		}

		// Compute max(SessionPct, WeeklyPct) and label which window it came from.
		usedPct := st.SessionPct
		window := "(5h)"
		if st.WeeklyPct >= st.SessionPct {
			usedPct = st.WeeklyPct
			window = "(week)"
		}
		usedRounded := int(math.Round(usedPct))

		fmt.Printf("%-8s  %d/%d session-5h   %d/%d weekly   used %d%% %s%s\n",
			ch,
			st.SessionCount, st.SessionLimit,
			st.WeeklyCount, st.WeeklyLimit,
			usedRounded, window,
			cooldown,
		)
	}
	return nil
}
