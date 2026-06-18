package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ishaanbatra/styx/internal/budget"
	"github.com/ishaanbatra/styx/internal/channel"
	"github.com/ishaanbatra/styx/internal/channel/agy"
	"github.com/ishaanbatra/styx/internal/channel/claude"
	"github.com/ishaanbatra/styx/internal/channel/codex"
	"github.com/ishaanbatra/styx/internal/channel/ollama"
	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/memory"
	"github.com/ishaanbatra/styx/internal/modelsync"
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

// logStatus writes a "[styx] " status line to stderr unless --quiet is set.
// Final results (printed to stdout) are never suppressed by --quiet.
func logStatus(format string, args ...any) {
	if globalQuiet {
		return
	}
	fmt.Fprintf(os.Stderr, "[styx] "+format+"\n", args...)
}

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
	if rp, perr := paths.RoutingPath(); perr == nil {
		if cp, perr := paths.ModelsCachePath(); perr == nil {
			opener := func() (*memory.Store, memory.Embedder, func()) {
				return globalCorrectionStore(r.Brain.EmbedModel)
			}
			refreshed, rerr := maybeRefreshModels(rp, cp, r.Models.RefreshIntervalHours, time.Now(), opener)
			if rerr != nil {
				fmt.Fprintf(os.Stderr, "[styx] model refresh skipped: %v\n", rerr)
			} else if refreshed {
				// Only re-read routing when a migration actually rewrote it.
				r2, rerr := config.LoadRouting()
				if rerr == nil {
					r = r2
				}
			}
		}
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
	bs := &budgetSource{t: t}
	rt := router.FromConfig(r, bs)
	rt.Breaker = bs
	return &app{
		routing:  r,
		tracker:  t,
		router:   rt,
		channels: defaultChannels(p, r),
		progress: p,
	}, nil
}

// correctionStoreOpener lazily opens the global memory store + embedder used to
// record routing de-pin corrections. It returns a closer the caller always
// defers (a no-op on failure). A nil opener means "do not record corrections"
// (used by tests and any path without a memory store).
type correctionStoreOpener func() (*memory.Store, memory.Embedder, func())

// globalCorrectionStore opens the shared global.db and an ollama embedder so a
// de-pin migration is recorded as a routing-preference memory. Best-effort: any
// failure yields a nil store (recording is skipped) rather than blocking the
// refresh, and the embedder is only contacted when there is a change to record.
func globalCorrectionStore(embedModel string) (*memory.Store, memory.Embedder, func()) {
	noop := func() {}
	memDir, err := paths.MemoryDir()
	if err != nil {
		return nil, nil, noop
	}
	if err := paths.EnsureDir(memDir); err != nil {
		return nil, nil, noop
	}
	store, err := memory.Open(filepath.Join(memDir, "global.db"))
	if err != nil {
		return nil, nil, noop
	}
	emb := memory.NewOllamaEmbedder("http://localhost:11434", embedModel)
	return store, emb, func() { store.Close() }
}

func maybeRefreshModels(routingPath, cachePath string, intervalHours int, now time.Time, openStore correctionStoreOpener) (bool, error) {
	c, err := modelsync.LoadCache(cachePath)
	if err != nil {
		return false, err
	}
	if !c.IsStale(now, time.Duration(intervalHours)*time.Hour) {
		return false, nil
	}
	// Open the correction store only on the stale path so the common
	// fresh-cache case stays free of any sqlite/embedder setup.
	var store *memory.Store
	var emb memory.Embedder
	if openStore != nil {
		var closeStore func()
		store, emb, closeStore = openStore()
		defer closeStore()
	}
	err = modelsync.Refresh(context.Background(), modelsync.Options{
		RoutingPath: routingPath,
		CachePath:   cachePath,
		Now:         now,
		Discoverers: []modelsync.Discoverer{
			modelsync.CodexDiscoverer{},
			modelsync.ClaudeDiscoverer{},
		},
		Store: store,
		Embed: emb,
	})
	return err == nil, err
}

func defaultChannels(prog *progress.Tracker, r config.Routing) map[string]channel.Channel {
	a := agy.New()
	raw := map[string]channel.Channel{
		"claude": claude.New(),
		"codex":  codex.New(),
		"agy":    a,
		"gemini": a, // alias for backward-compatible routing rules
		"ollama": ollama.New(),
	}
	timeouts := map[string]int{
		"claude": r.Budget.Claude.TimeoutMinutes,
		"codex":  r.Budget.Codex.TimeoutMinutes,
		"agy":    r.Budget.Agy.TimeoutMinutes,
		"gemini": r.Budget.Agy.TimeoutMinutes,
	}
	wrapped := make(map[string]channel.Channel, len(raw))
	for name, ch := range raw {
		inner := ch
		if mins, ok := timeouts[name]; ok {
			if mins <= 0 {
				mins = 10 // claude/codex previously had no timeout at all
			}
			inner = &channel.WithTimeout{Inner: inner, D: time.Duration(mins) * time.Minute}
		}
		wrapped[name] = &channel.WithProgress{Inner: inner, Tracker: prog, Label: name}
	}
	return wrapped
}

// budgetSource adapts *budget.Tracker to router.BudgetSource.
type budgetSource struct{ t *budget.Tracker }

func (b *budgetSource) UsedPct(ctx context.Context, ch string) (float64, error) {
	return b.t.UsedPct(ctx, ch)
}

func (b *budgetSource) Broken(ctx context.Context, ch string) bool {
	broken, err := b.t.ShouldCircuitBreak(ctx, ch, 3, 10*time.Minute)
	return err == nil && broken
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
	case "doctor":
		return cmdDoctor(args)
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
	// Anything that isn't a verb is an utterance: `styx "fix the flaky test"`
	// runs one brain turn and exits.
	utterance := strings.TrimSpace(strings.Join(append([]string{verb}, args...), " "))
	return cmdBrainTurn(a, utterance)
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
		logStatus("wrote default routing.toml to %s", routingPath)
	}
	// Auto-upgrade: v0.2 rewrites gemini:* -> agy:default; v0.3 injects the
	// `implement` verb rules. Both back up to routing.v0.1.toml.bak.
	if n, injected, err := config.UpgradeRoutingFile(routingPath); err != nil {
		logStatus("upgrade check failed: %v", err)
	} else {
		if n > 0 {
			logStatus("auto-upgraded %d gemini reference(s) to agy (backup at routing.v0.1.toml.bak)", n)
		}
		if injected {
			logStatus("auto-upgraded routing.toml with the implement verb (codex implements, claude fallback)")
		}
	}
	return nil
}

// rawChannel unwraps a progress-decorated channel back to its inner channel,
// so orchestration verbs that narrate their own progress don't double-narrate.
func rawChannel(ch channel.Channel) channel.Channel {
	if w, ok := ch.(*channel.WithProgress); ok {
		return w.Inner
	}
	return ch
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
