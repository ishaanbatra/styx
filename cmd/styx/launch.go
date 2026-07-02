package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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

// launchConductor is the shared guidance-assembly + host-launch path behind
// cmdLaunch and cmdResume; extraArgs are passed through to the host CLI after
// its standard flags.
func launchConductor(a *app, repos []string, extraArgs []string) error {
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
	for _, name := range extraTail {
		ep, rerr := target.Resolve(target.Spec{Alias: name})
		if rerr != nil {
			return fmt.Errorf("resolve extra repo %q: %w", name, rerr)
		}
		extras = append(extras, ep.Path)
		fmt.Fprintf(&extraNote, "- %s: %s (pass in dispatch extra_roots to give threads access)\n", ep.Name, ep.Path)
	}
	guide, err := guidance.Load(p.Path)
	if err != nil {
		return fmt.Errorf("load guidance: %w", err)
	}
	if extraNote.Len() > 0 {
		guide += "\n\n## Bound repos beyond " + p.Name + "\n" + extraNote.String()
	}
	if prefs := recallRoutingPrefs(a); prefs != "" {
		guide += "\n\n## Routing preferences (learned)\n" + prefs
	}
	styxBin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate styx binary: %w", err)
	}
	host := &launcher.ClaudeHost{}
	logStatus("launching " + host.Name() + " conductor in " + p.Name)
	return host.Launch(context.Background(), launcher.Opts{
		ProjectPath: p.Path, StyxBin: styxBin, Guidance: guide, ExtraRepos: extras,
		ExtraArgs: extraArgs,
	})
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

// recallRoutingPrefs opens the global memory store + embedder exactly as
// newREPLSession does (repl.go) and recalls up to 5 routing-preference
// memories to fold into the launch guidance. Recall is an enhancement, never
// a blocker: any failure is narrated via logStatus and yields "".
func recallRoutingPrefs(a *app) string {
	memDir, err := paths.MemoryDir()
	if err != nil {
		logStatus("routing preference recall skipped: %v", err)
		return ""
	}
	if err := paths.EnsureDir(memDir); err != nil {
		logStatus("routing preference recall skipped: %v", err)
		return ""
	}
	glob, err := memory.Open(filepath.Join(memDir, "global.db"))
	if err != nil {
		logStatus("routing preference recall skipped: %v", err)
		return ""
	}
	defer glob.Close()

	emb := memory.NewOllamaEmbedder("http://localhost:11434", a.routing.Brain.EmbedModel)
	hits, err := memory.Recall(context.Background(), emb, "routing preference", 5, glob)
	if err != nil {
		logStatus("routing preference recall skipped: %v", err)
		return ""
	}
	if len(hits) == 0 {
		return ""
	}
	texts := make([]string, len(hits))
	for i, h := range hits {
		texts[i] = h.Item.Text
	}
	return "- " + strings.Join(texts, "\n- ")
}
