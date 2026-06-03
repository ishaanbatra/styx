package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ishaanbatra/styx/internal/budget"
	"github.com/ishaanbatra/styx/internal/channel"
	"github.com/ishaanbatra/styx/internal/channel/agy"
	"github.com/ishaanbatra/styx/internal/channel/claude"
	"github.com/ishaanbatra/styx/internal/channel/codex"
	"github.com/ishaanbatra/styx/internal/channel/ollama"
	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/paths"
	"github.com/ishaanbatra/styx/internal/progress"
	"github.com/ishaanbatra/styx/internal/project"
	"github.com/ishaanbatra/styx/internal/router"
)

// globalQuiet and globalVerbose are set by main() after parseGlobalFlags.
var (
	globalQuiet   bool
	globalVerbose bool
)

// newProgress builds a progress tracker from the parsed global flags.
func newProgress() *progress.Tracker {
	return progress.New(os.Stderr, globalQuiet, globalVerbose)
}

// app bundles the long-lived dependencies shared by every verb.
type app struct {
	routing  config.Routing
	tracker  *budget.Tracker
	router   *router.Router
	channels map[string]channel.Channel
	progress *progress.Tracker
}

// builtinMsgLimits is the fallback used when routing.toml doesn't specify message limits
// (e.g. upgraded users whose config predates the B2 migration).
var builtinMsgLimits = map[string][2]int{
	"claude": {45, 225},
	"codex":  {50, 250},
	"agy":    {100, 500},
	// ollama omitted on purpose: unlimited (0/0)
}

// channelCapEntry pairs a channel name with its parsed ChannelCap.
type channelCapEntry struct {
	name string
	cap  config.ChannelCap
}

// seedMessageLimits applies per-channel message limits to t, preferring
// routing.toml values and falling back to builtins for unset channels.
func seedMessageLimits(t *budget.Tracker, r config.Routing) {
	caps := []channelCapEntry{
		{"claude", r.Budget.Claude},
		{"codex", r.Budget.Codex},
		{"agy", r.Budget.Agy},
	}
	for _, entry := range caps {
		builtin := builtinMsgLimits[entry.name]
		s5h, sWeek := builtin[0], builtin[1]
		if entry.cap.MessagesPer5h > 0 {
			s5h = entry.cap.MessagesPer5h
		}
		if entry.cap.MessagesPerWeek > 0 {
			sWeek = entry.cap.MessagesPerWeek
		}
		t.SetMessageLimits(entry.name, s5h, sWeek)
	}
}

func loadApp() (*app, error) {
	r, err := config.LoadRouting()
	if err != nil {
		return nil, fmt.Errorf("load routing: %w", err)
	}
	t, err := budget.Default()
	if err != nil {
		return nil, fmt.Errorf("open budget tracker: %w", err)
	}

	// Seed message limits from routing.toml, falling back to builtins for
	// upgraded users whose config may not yet have message-limit keys.
	seedMessageLimits(t, r)

	// Create the progress tracker once and share it with both app.progress and
	// defaultChannels so the decorator narrates the same tracker output.
	p := newProgress()
	rt := router.FromConfig(r, &budgetSource{t: t})
	return &app{
		routing:  r,
		tracker:  t,
		router:   rt,
		channels: defaultChannels(p),
		progress: p,
	}, nil
}

func defaultChannels(prog *progress.Tracker) map[string]channel.Channel {
	a := agy.New()
	raw := map[string]channel.Channel{
		"claude": claude.New(),
		"codex":  codex.New(),
		"agy":    a,
		"gemini": a, // alias for backward-compatible routing rules
		"ollama": ollama.New(),
	}
	wrapped := make(map[string]channel.Channel, len(raw))
	for name, ch := range raw {
		wrapped[name] = &channel.WithProgress{Inner: ch, Tracker: prog, Label: name}
	}
	return wrapped
}

// budgetSource adapts *budget.Tracker to router.BudgetSource.
type budgetSource struct{ t *budget.Tracker }

func (b *budgetSource) UsedPct(ctx context.Context, ch string) (float64, error) {
	return b.t.UsedPct(ctx, ch)
}

func dispatch(verb string, args []string) error {
	switch verb {
	case "help", "-h", "--help":
		printHelp()
		return nil
	case "migrate-secrets":
		return cmdMigrateSecrets()
	case "upgrade":
		return cmdUpgrade()
	case "project":
		return cmdProject(args)
	case "route":
		return cmdRoute(args)
	case "budget":
		return cmdBudget(args)
	case "check":
		return cmdCheck(args)
	case "deep-research":
		return cmdDeepResearch(args)
	case "context":
		return cmdContext(args)
	case "execute":
		return cmdExecuteVerb(args)
	case "runs":
		return cmdRuns(args)
	}

	a, err := loadApp()
	if err != nil {
		return err
	}
	defer a.tracker.Close()

	switch verb {
	case "research":
		return cmdResearch(a, args)
	case "plan":
		return cmdPlan(a, args)
	case "build":
		return cmdBuild(a, args)
	case "review":
		return cmdReview(a, args)
	case "intel":
		return cmdIntel(a, args)
	case "auto":
		return cmdAuto(a, args)
	case "grunt", "think", "explain", "summarize", "critique":
		return cmdOneShot(a, verb, args)
	}
	return fmt.Errorf("unknown verb %q (run `styx help`)", verb)
}

// ensureFirstRun creates the config dir and seeds routing.toml on first run.
func ensureFirstRun() error {
	cfg, err := paths.ConfigDir()
	if err != nil {
		return err
	}
	if err := paths.EnsureDir(cfg); err != nil {
		return err
	}
	if err := paths.EnsureDir(filepath.Join(cfg, "state")); err != nil {
		return err
	}
	routingPath, err := paths.RoutingPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(routingPath); os.IsNotExist(err) {
		if err := os.WriteFile(routingPath, []byte(defaultRoutingTOML), 0o644); err != nil {
			return fmt.Errorf("seed default routing.toml: %w", err)
		}
		fmt.Fprintf(os.Stderr, "[styx] wrote default routing.toml to %s\n", routingPath)
	}
	// v0.2 auto-upgrade: rewrite gemini:* -> agy:default if present.
	if n, err := config.UpgradeRoutingFile(routingPath); err != nil {
		fmt.Fprintf(os.Stderr, "[styx] upgrade check failed: %v\n", err)
	} else if n > 0 {
		fmt.Fprintf(os.Stderr, "[styx] auto-upgraded %d gemini reference(s) to agy (backup at routing.v0.1.toml.bak)\n", n)
	}
	return nil
}

// resolveTarget converts a "backend|student|teacher|<alias>" arg into a Project.
// Empty arg means the project for the current working directory.
func resolveTarget(arg string) (project.Project, error) {
	if arg == "" {
		return project.Current()
	}
	if p, err := project.Resolve(arg); err == nil {
		return p, nil
	}
	return project.Current()
}

