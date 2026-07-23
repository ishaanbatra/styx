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
	result, err := config.UpgradeRoutingFile(p)
	if err != nil {
		return err
	}
	if !result.Changed() {
		fmt.Println("routing.toml already up to date (agy model pin + implement/debug/dead-code/map-impact/cross-repo/PR-drafting verbs + fable tier + conductor host/task cap + watch config present).")
		return nil
	}
	if result.GeminiRewrites > 0 {
		fmt.Printf("Migrated %d rule reference(s) from gemini-cli to agy.\n", result.GeminiRewrites)
	}
	if result.OllamaRewrites > 0 {
		fmt.Printf("Migrated %d seeded Ollama target(s) from qwen2.5-coder:14b to qwen2.5-coder:7b.\n", result.OllamaRewrites)
	}
	if result.ImplementInjected {
		fmt.Println("Added the implement verb (codex implements from a plan, claude fallback).")
	}
	if result.FableRestored {
		fmt.Println("Restored the fable tier (suspension lifted; fable now maps to fable).")
	}
	if result.TaskCapInjected {
		fmt.Println("Seeded [conductor] max_background_tasks = 4.")
	}
	if result.HostInjected {
		fmt.Println("Seeded [conductor] host = \"claude\".")
	}
	if result.WatchInjected {
		fmt.Println("Seeded [watch] stall_threshold_seconds = 90, interval_seconds = 15, ollama_enabled = true.")
	}
	if result.DebugInjected {
		fmt.Println("Added ultraFerdDebug routing (agy sweep, codex + claude reviews).")
	}
	if result.DeadCodeInjected {
		fmt.Println("Added dead-code routing (agy sweep, claude/codex fallback).")
	}
	if result.MapImpactInjected {
		fmt.Println("Added map-impact routing (agy sweep, claude/codex fallback).")
	}
	if result.CrossRepoInjected {
		fmt.Println("Added cross-repo routing (agy multi-root sweep, claude/codex fallback).")
	}
	if result.PRDraftInjected {
		fmt.Println("Added local PR title/body drafting with a haiku fallback.")
	}
	if result.AgyPinned {
		fmt.Println("Pinned agy routes to Gemini 3.1 Pro (High).")
	}
	fmt.Printf("Backup saved to %s/routing.v0.1.toml.bak\n", "~/.config/styx")
	return nil
}
