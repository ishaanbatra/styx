package main

import (
	"fmt"

	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/paths"
)

func cmdUpgrade() error {
	p, err := paths.RoutingPath()
	if err != nil {
		return err
	}
	n, injected, fableRestored, taskCapInjected, watchInjected, debugInjected, err := config.UpgradeRoutingFile(p)
	if err != nil {
		return err
	}
	if n == 0 && !injected && !fableRestored && !taskCapInjected && !watchInjected && !debugInjected {
		fmt.Println("routing.toml already up to date (agy + implement/debug verbs + fable tier + conductor task cap + watch config present).")
		return nil
	}
	if n > 0 {
		fmt.Printf("Migrated %d rule reference(s) from gemini-cli to agy.\n", n)
	}
	if injected {
		fmt.Println("Added the implement verb (codex implements from a plan, claude fallback).")
	}
	if fableRestored {
		fmt.Println("Restored the fable tier (suspension lifted; fable now maps to fable).")
	}
	if taskCapInjected {
		fmt.Println("Seeded [conductor] max_background_tasks = 4.")
	}
	if watchInjected {
		fmt.Println("Seeded [watch] stall_threshold_seconds = 90, interval_seconds = 15, ollama_enabled = true.")
	}
	if debugInjected {
		fmt.Println("Added ultraFerdDebug routing (agy sweep, codex + claude reviews).")
	}
	fmt.Printf("Backup saved to %s/routing.v0.1.toml.bak\n", "~/.config/styx")
	return nil
}
