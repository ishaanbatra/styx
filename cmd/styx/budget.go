package main

import (
	"context"
	"fmt"
	"time"

	"github.com/ishaanbatra/styx/internal/budget"
)

func cmdBudget(args []string) error {
	tr, err := budget.Default()
	if err != nil {
		return err
	}
	defer tr.Close()
	ctx := context.Background()
	for _, ch := range []string{"claude", "codex", "gemini", "gemini_paid", "gemini_free", "ollama"} {
		st, err := tr.State(ctx, ch)
		if err != nil {
			fmt.Printf("%-12s  error: %v\n", ch, err)
			continue
		}
		cooldown := ""
		if !st.CooldownUntil.IsZero() {
			cooldown = fmt.Sprintf(" (cooldown until %s)", st.CooldownUntil.Format(time.RFC3339))
		}
		fmt.Printf("%-12s  used %5.1f%%  window=%s%s\n", ch, st.UsedPct, st.Window, cooldown)
	}
	return nil
}
