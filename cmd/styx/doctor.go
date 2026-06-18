package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/ishaanbatra/styx/internal/brain"
	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/memory"
	"github.com/ishaanbatra/styx/internal/modelsync"
	"github.com/ishaanbatra/styx/internal/paths"
)

// cmdDoctor preflights the orchestrator: CLI presence and versions,
// capability-card drift (--help vs ExpectedFlags), resume support, claude tier
// callability, and ollama model availability. `styx doctor --fix` pulls
// missing ollama models.
func cmdDoctor(args []string) error {
	fix := false
	for _, a := range args {
		if a == "--fix" {
			fix = true
		}
	}
	r, err := config.LoadRouting()
	if err != nil {
		return fmt.Errorf("load routing: %w", err)
	}
	healthy := true

	if rp, err := paths.RoutingPath(); err == nil {
		if cp, err := paths.ModelsCachePath(); err == nil {
			opener := func() (*memory.Store, memory.Embedder, func()) {
				return globalCorrectionStore(r.Brain.EmbedModel)
			}
			if err := runModelRefresh(rp, cp, time.Now(), opener); err != nil {
				fmt.Printf("! model refresh skipped: %v\n", err)
			} else {
				fmt.Println("ok models refreshed (defer-to-latest)")
			}
		}
	}

	for _, card := range brain.Cards {
		if card.Bin == "" {
			continue // ollama is probed via HTTP below.
		}
		if _, err := exec.LookPath(card.Bin); err != nil {
			fmt.Printf("x %s not found on PATH\n", card.Bin)
			healthy = false
			continue
		}
		version := probeOutput(card.Bin, "--version")
		help := probeOutput(card.Bin, "--help")
		missing := missingFlags(help, card)
		mode := "native resume"
		if card.ResumeProbe == "" || !strings.Contains(help, card.ResumeProbe) {
			mode = "styx-maintained continuity"
		}
		if len(missing) > 0 {
			fmt.Printf("! %s %s - knowledge stale: --help missing %v (CLI updated? refresh internal/brain/cards.go) - %s\n",
				card.Bin, firstLine(version), missing, mode)
			healthy = false
		} else {
			fmt.Printf("ok %s %s - card current - %s\n", card.Bin, firstLine(version), mode)
		}
	}

	required := []string{r.Brain.Model, r.Brain.EmbedModel}
	tags, err := fetchOllamaTags("http://localhost:11434")
	if err != nil {
		fmt.Printf("x ollama unreachable: %v (REPL will degrade to ask-the-user routing)\n", err)
		return reportDoctor(false)
	}
	missing := ollamaModelsMissing(tags, required)
	if len(missing) == 0 {
		fmt.Printf("ok ollama up - models present: %s\n", strings.Join(required, ", "))
	} else if fix {
		for _, m := range missing {
			fmt.Printf("... pulling %s\n", m)
			cmd := exec.Command("ollama", "pull", m)
			cmd.Stdout, cmd.Stderr = childOutputWriter(), childOutputWriter()
			if err := cmd.Run(); err != nil {
				fmt.Printf("x pull %s failed: %v\n", m, err)
				healthy = false
			} else {
				fmt.Printf("ok pulled %s\n", m)
			}
		}
	} else {
		fmt.Printf("! ollama up but missing models %v - run `styx doctor --fix` or `ollama pull <model>`\n", missing)
		healthy = false
	}

	if !checkTiers(r.Tiers) {
		healthy = false
	}
	return reportDoctor(healthy)
}

func runModelRefresh(routingPath, cachePath string, now time.Time, openStore correctionStoreOpener) error {
	var store *memory.Store
	var emb memory.Embedder
	if openStore != nil {
		var closeStore func()
		store, emb, closeStore = openStore()
		defer closeStore()
	}
	return modelsync.Refresh(context.Background(), modelsync.Options{
		RoutingPath: routingPath,
		CachePath:   cachePath,
		Now:         now,
		Discoverers: []modelsync.Discoverer{
			modelsync.CodexDiscoverer{},
			modelsync.ClaudeDiscoverer{},
		},
		Store: store,
		Embed: emb,
		Log:   func(f string, a ...any) { fmt.Printf("  "+f+"\n", a...) },
	})
}

func reportDoctor(healthy bool) error {
	if healthy {
		fmt.Println("doctor: all clear")
		return nil
	}
	return fmt.Errorf("doctor found issues (see above)")
}

// probeOutput runs `bin arg` with a short timeout and returns combined output
// ("" on failure; absence of output is handled by the card checks).
func probeOutput(bin, arg string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, bin, arg).CombinedOutput()
	return string(out)
}

// missingFlags returns card.ExpectedFlags absent from the CLI's help text.
func missingFlags(help string, card brain.Card) []string {
	var missing []string
	for _, f := range card.ExpectedFlags {
		if !strings.Contains(help, f) {
			missing = append(missing, f)
		}
	}
	return missing
}

func fetchOllamaTags(baseURL string) (string, error) {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(baseURL + "/api/tags")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("ollama tags HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ollamaModelsMissing parses /api/tags JSON and returns required models not
// present. "name" or "name:tag" both satisfy a required bare name.
func ollamaModelsMissing(tagsJSON string, required []string) []string {
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	_ = json.Unmarshal([]byte(tagsJSON), &tags)
	have := map[string]bool{}
	for _, m := range tags.Models {
		have[m.Name] = true
		if i := strings.Index(m.Name, ":"); i > 0 {
			have[m.Name[:i]] = true
		}
	}
	var missing []string
	for _, r := range required {
		if !have[r] {
			missing = append(missing, r)
		}
	}
	return missing
}

// claudeModelOK reports whether `claude --model <alias>` can serve a trivial
// request. This catches aliases that are known to the card but not currently
// callable for this user or region.
func claudeModelOK(alias string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "claude", "-p", "ok",
		"--model", alias, "--dangerously-skip-permissions")
	return cmd.Run() == nil
}

// checkTiers probes each distinct tier->alias mapping for a callable claude
// model. Returns false if any mapped alias is unavailable.
func checkTiers(tiers map[string]string) bool {
	if _, err := exec.LookPath("claude"); err != nil {
		return true // claude absence is already reported by the card loop.
	}
	return checkTiersWithProbe(tiers, claudeModelOK)
}

func checkTiersWithProbe(tiers map[string]string, probe func(alias string) bool) bool {
	names := make([]string, 0, len(tiers))
	for tier := range tiers {
		names = append(names, tier)
	}
	sort.Strings(names)

	seen := map[string]bool{}
	ok := true
	for _, tier := range names {
		alias := tiers[tier]
		if seen[alias] {
			continue
		}
		seen[alias] = true
		if probe(alias) {
			fmt.Printf("ok tier %s -> claude --model %s - callable\n", tier, alias)
		} else {
			fmt.Printf("x tier %s -> claude --model %s - NOT callable (suspended/deprecated/not on your plan); remap it in ~/.config/styx/routing.toml [tiers]\n", tier, alias)
			ok = false
		}
	}
	return ok
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func childOutputWriter() io.Writer {
	if globalQuiet {
		return io.Discard
	}
	return os.Stdout
}
