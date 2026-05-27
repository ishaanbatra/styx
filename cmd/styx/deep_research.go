package main

import (
	"fmt"
	"os"
)

func cmdDeepResearch(args []string) error {
	fmt.Fprintln(os.Stderr, "[styx] 'deep-research' is now 'research --deep' in v0.2 — forwarding")
	a, err := loadApp()
	if err != nil {
		return err
	}
	defer a.tracker.Close()
	forwarded := append([]string{"--deep"}, args...)
	return cmdResearch(a, forwarded)
}
