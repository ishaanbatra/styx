package main

import (
	"context"
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
// REPL's newREPLSession; extra repos are noted in the guidance handed to the
// brain (bind them via the MCP dispatch tool's extra_roots, not this exec).
func cmdLaunch(a *app, repos ...string) error {
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
		return err
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
	})
}

// resolveLaunchTarget mirrors newREPLSession's seed resolution (repl.go): an
// explicit first repo resolves by alias; with none given, fall back to the
// global --project/--dir/cwd resolution so bare `styx` still targets the
// current directory's repo rather than erroring on an empty spec.
func resolveLaunchTarget(repos []string) (project.Project, error) {
	if len(repos) > 0 {
		return target.Resolve(target.Spec{Alias: repos[0]})
	}
	return resolveGlobalTarget("")
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
