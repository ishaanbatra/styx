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
	n, err := config.UpgradeRoutingFile(p)
	if err != nil {
		return err
	}
	if n == 0 {
		fmt.Println("No legacy gemini:* references found. Already on agy.")
		return nil
	}
	fmt.Printf("Migrated %d rule reference(s) from gemini-cli to agy.\n", n)
	fmt.Printf("Backup saved to %s/routing.v0.1.toml.bak\n", "~/.config/styx")
	return nil
}
