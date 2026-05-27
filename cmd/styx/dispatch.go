package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ishaanbatra/styx/internal/budget"
	"github.com/ishaanbatra/styx/internal/channel"
	"github.com/ishaanbatra/styx/internal/channel/claude"
	"github.com/ishaanbatra/styx/internal/channel/codex"
	"github.com/ishaanbatra/styx/internal/channel/gemini"
	"github.com/ishaanbatra/styx/internal/channel/ollama"
	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/paths"
	"github.com/ishaanbatra/styx/internal/project"
	"github.com/ishaanbatra/styx/internal/router"
)

// app bundles the long-lived dependencies shared by every verb.
type app struct {
	routing  config.Routing
	tracker  *budget.Tracker
	router   *router.Router
	channels map[string]channel.Channel
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
	rt := router.FromConfig(r, &budgetSource{t: t})
	return &app{
		routing:  r,
		tracker:  t,
		router:   rt,
		channels: defaultChannels(),
	}, nil
}

func defaultChannels() map[string]channel.Channel {
	return map[string]channel.Channel{
		"claude": claude.New(),
		"codex":  codex.New(),
		"gemini": gemini.New(),
		"ollama": ollama.New(),
	}
}

// budgetSource adapts *budget.Tracker to router.BudgetSource.
type budgetSource struct{ t *budget.Tracker }

func (b *budgetSource) UsedPct(ctx context.Context, ch string) (float64, error) {
	st, err := b.t.State(ctx, ch)
	if err != nil {
		return 0, err
	}
	return st.UsedPct, nil
}

func dispatch(verb string, args []string) error {
	switch verb {
	case "help", "-h", "--help":
		printHelp()
		return nil
	case "migrate-secrets":
		return cmdMigrateSecrets()
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

