package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ishaanbatra/styx/internal/attribution"
	"github.com/ishaanbatra/styx/internal/graph"
	"github.com/ishaanbatra/styx/internal/guidance"
	"github.com/ishaanbatra/styx/internal/launcher"
	"github.com/ishaanbatra/styx/internal/memory"
	"github.com/ishaanbatra/styx/internal/paths"
	"github.com/ishaanbatra/styx/internal/project"
	"github.com/ishaanbatra/styx/internal/target"
)

// cmdLaunch is the conductor front door: `styx` / `styx <repos...>`. The
// first repo (if any) resolves the focus project exactly like the classic
// REPL's newREPLSession; extra repos are both added to the Claude Code
// session directly (the launcher passes --add-dir per repo) and noted in
// the guidance so the brain also passes them as the MCP dispatch tool's
// extra_roots, giving dispatched agent threads the same access.
func cmdLaunch(a *app, repos ...string) error {
	return launchConductor(a, repos, nil)
}

// cmdResume relaunches the conductor resuming an existing Claude Code session:
// with a session ID it passes --resume <id>, without one --continue (Claude
// Code picks the directory's most recent session). Either way the full
// toolbelt is rewired — a plain `claude --resume` would restore the
// conversation but lose the styx MCP server and guidance, since those are
// per-invocation flags. Focus resolution is cwd-anchored like bare `styx`
// (sessions are per-directory); extra repos are out of scope.
func cmdResume(a *app, sessionID string) error {
	extraArgs := []string{"--continue"}
	if sessionID != "" {
		extraArgs = []string{"--resume", sessionID}
	}
	return launchConductor(a, nil, extraArgs)
}

// ensureGraphsFresh fires a background graphify build for every stale bound
// repo. All narration happens HERE, before the host owns the TTY — the
// goroutines are silent (build output goes to state/graph/<id>/build.log)
// because a stderr write mid-session would corrupt the Claude Code TUI.
// The returned channel closes when all builds finish; launchConductor ignores
// it (builds race the session and die with the process — meta is only written
// on success, so an interrupted build simply retries next launch).
func ensureGraphsFresh(bin string, projs []project.Project) <-chan struct{} {
	done := make(chan struct{})
	var wg sync.WaitGroup
	for _, p := range projs {
		stale, reason := graph.IsStale(p)
		if !stale {
			continue
		}
		logPath, lerr := graph.LogPath(p)
		if lerr != nil {
			logStatus("graph build skipped for %s: %v", p.Name, lerr)
			continue
		}
		logStatus("knowledge graph for %s is stale (%s) — rebuilding in background (log: %s)", p.Name, reason, logPath)
		wg.Add(1)
		go func(p project.Project) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), graph.BuildTimeout)
			defer cancel()
			// Errors land in build.log via Build; nothing to stderr from here.
			_ = graph.Build(ctx, p, bin)
		}(p)
	}
	go func() { wg.Wait(); close(done) }()
	return done
}

// launchConductor is the shared guidance-assembly + host-launch path behind
// cmdLaunch and cmdResume; extraArgs are passed through to the host CLI after
// its standard flags.
func launchConductor(a *app, repos []string, extraArgs []string) error {
	if err := ensureInteractiveTTY(); err != nil {
		return err
	}
	p, err := resolveLaunchTarget(repos)
	if err != nil {
		return fmt.Errorf("resolve launch project: %w", err)
	}
	var extras []string
	var extraNote strings.Builder
	var extraTail []string
	if len(repos) > 1 {
		extraTail = repos[1:]
	}
	graphProjects := []project.Project{p}
	for _, name := range extraTail {
		ep, rerr := target.Resolve(target.Spec{Alias: name})
		if rerr != nil {
			return fmt.Errorf("resolve extra repo %q: %w", name, rerr)
		}
		extras = append(extras, ep.Path)
		fmt.Fprintf(&extraNote, "- %s: %s (pass in dispatch extra_roots to give threads access)\n", ep.Name, ep.Path)
		graphProjects = append(graphProjects, ep)
	}
	if bin, ok := graph.Available(); ok {
		ensureGraphsFresh(bin, graphProjects) // background; dies with the session
	}
	guide, err := guidance.Load(p.Path)
	if err != nil {
		return fmt.Errorf("load guidance: %w", err)
	}
	guide = conductorGuidance(guide, p.Name, extraNote.String(), recallRoutingPrefs(), recallUserPrefs())
	styxBin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate styx binary: %w", err)
	}
	host := &launcher.ClaudeHost{}
	logStatus("launching " + host.Name() + " conductor in " + p.Name)
	return host.Launch(context.Background(), launcher.Opts{
		ProjectPath: p.Path, StyxBin: styxBin, Guidance: guide, ExtraRepos: extras,
		RouteGate: a.routing.Conductor.RouteGate,
		ExtraArgs: extraArgs,
	})
}

// conductorGuidance assembles the final --append-system-prompt content:
// base guidance, the focus project's registry alias, extra-repo notes, and
// the two learned-preference sections (the entire application mechanism of
// styx learn — nothing else consumes learned state), and the commit-attribution
// instruction that replaces Claude Code's built-in co-author rule.
func conductorGuidance(base, focusName, extraNote, prefs, userPrefs string) string {
	g := base
	g += "\n\n## This session's project\n" +
		"Registry alias: `" + focusName + "`. Pass it as `project` on dispatch/" +
		"thread_status/memory_save (an empty project also resolves to this repo)."
	if extraNote != "" {
		g += "\n\n## Bound repos beyond " + focusName + "\n" + extraNote
	}
	if prefs != "" {
		g += "\n\n## Routing preferences (learned)\n" + prefs
	}
	if userPrefs != "" {
		g += "\n\n## User preferences (learned)\n" + userPrefs
	}
	g += "\n\n## Commit attribution\n" + attribution.CommitInstruction
	return g
}

// stdinIsTTY reports whether stdin is a character device. Var for tests.
var stdinIsTTY = func() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// ensureInteractiveTTY refuses a conductor launch when there is no terminal
// to hand to Claude Code — exec'ing claude on a pipe dies with a confusing
// "--print requires input" error instead of anything actionable.
func ensureInteractiveTTY() error {
	if stdinIsTTY() {
		return nil
	}
	return fmt.Errorf("the conductor needs an interactive terminal (stdin is not a TTY); use a verb like `styx research` or `styx \"<task>\"` for scripted runs")
}

// resolveLaunchTarget mirrors newREPLSession's seed resolution (repl.go): an
// explicit first repo resolves by alias; with none given, fall back to the
// global --project/--dir/cwd resolution so bare `styx` still targets the
// current directory's repo rather than erroring on an empty spec.
//
// Unlike every other verb, bare `styx` outside any git repository does not
// error: the conductor is useful in a plain directory (global tools, guidance,
// dispatch to registered projects), so the cwd itself becomes the focus. Only
// the implicit-cwd case is relaxed — an explicit repo argument or --project/
// --dir flag that fails still surfaces its error.
func resolveLaunchTarget(repos []string) (project.Project, error) {
	if len(repos) > 0 {
		return target.Resolve(target.Spec{Alias: repos[0]})
	}
	p, err := resolveGlobalTarget("")
	if err == nil {
		return p, nil
	}
	if errors.Is(err, project.ErrNotInGitRepo) && globalProjectAlias == "" && globalDirArg == "" {
		cwd, werr := os.Getwd()
		if werr != nil {
			return project.Project{}, fmt.Errorf("getwd: %w", werr)
		}
		logStatus("not a git repo — launching in plain directory %s (project-scoped tools need a registered repo)", cwd)
		return project.Project{Name: filepath.Base(cwd), Path: cwd}, nil
	}
	return project.Project{}, err
}

// topLearnedPrefs renders the global store's top-5 memories of kind, ranked
// by confidence × recency (TopByKind) — kind-exact and embedder-free, so the
// launch path works even with ollama down. A pure enhancement: any failure
// is narrated via logStatus and yields "".
func topLearnedPrefs(kind memory.Kind) string {
	memDir, err := paths.MemoryDir()
	if err != nil {
		logStatus("%s recall skipped: %v", kind, err)
		return ""
	}
	if err := paths.EnsureDir(memDir); err != nil {
		logStatus("%s recall skipped: %v", kind, err)
		return ""
	}
	glob, err := memory.Open(filepath.Join(memDir, "global.db"))
	if err != nil {
		logStatus("%s recall skipped: %v", kind, err)
		return ""
	}
	defer glob.Close()
	items, err := glob.TopByKind(context.Background(), kind, 5)
	if err != nil {
		logStatus("%s recall skipped: %v", kind, err)
		return ""
	}
	if len(items) == 0 {
		return ""
	}
	texts := make([]string, len(items))
	for i, it := range items {
		texts[i] = it.Text
	}
	return "- " + strings.Join(texts, "\n- ") + "\n"
}

// recallRoutingPrefs and recallUserPrefs feed the two learned guidance
// sections. Kind-exact TopByKind replaced the old embedding recall (which
// would cross-match user-preference texts against "routing preference").
func recallRoutingPrefs() string { return topLearnedPrefs(memory.KindRoutingPreference) }
func recallUserPrefs() string    { return topLearnedPrefs(memory.KindUserPreference) }
