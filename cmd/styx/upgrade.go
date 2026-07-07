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
	n, injected, fableRestored, taskCapInjected, err := config.UpgradeRoutingFile(p)
	if err != nil {
		return err
	}
	if n == 0 && !injected && !fableRestored && !taskCapInjected {
		fmt.Println("routing.toml already up to date (agy + implement verb + fable tier + conductor task cap present).")
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
	fmt.Printf("Backup saved to %s/routing.v0.1.toml.bak\n", "~/.config/styx")
	return nil
}
