package main

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"time"

	"github.com/ishaanbatra/styx/internal/activity"
	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/paths"
	"github.com/ishaanbatra/styx/internal/target"
)

// watchProjectID resolves the project ID that `styx watch <args>` should read
// the mirror for. It MUST agree with newREPLSession's seed resolution
// (repl.go) for both forms:
//   - an explicit repo/alias arg resolves via target.Resolve(Spec{Alias:
//     arg}) — the exact call newREPLSession makes when repos is non-empty —
//     so `styx watch otherRepo` matches a writer started with `styx repl
//     otherRepo`, regardless of the reader's own cwd.
//   - no args falls back to resolveGlobalTarget(""), the same cwd-based
//     resolution the no-arg REPL and the `styx mcp` conductor use at their
//     own launch time.
//
// Note: resolveGlobalTarget(arg) for a non-empty arg is equivalent to
// target.Resolve(Spec{Alias: arg}) too — target.Resolve's Alias branch
// ignores Dir/Cwd whenever Alias != "" — but we call target.Resolve directly
// here to make that equivalence structural rather than incidental.
func watchProjectID(args []string) (string, error) {
	if len(args) > 0 && args[0] != "" {
		p, err := target.Resolve(target.Spec{Alias: args[0]})
		if err != nil {
			return "", err
		}
		return p.ID, nil
	}
	p, err := resolveGlobalTarget("")
	if err != nil {
		return "", err
	}
	return p.ID, nil
}

// watchMirrorPath resolves the on-disk mirror file `styx watch <args>` should
// read: <StateDir>/watch/<projectID>.json, using watchProjectID above.
func watchMirrorPath(args []string) (string, error) {
	id, err := watchProjectID(args)
	if err != nil {
		return "", err
	}
	stateDir, err := paths.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(stateDir, "watch", id+".json"), nil
}

// cmdWatch renders the live dispatch board written by a running styx session
// (REPL or `styx mcp` conductor) in the current project, or in an optional
// explicitly named repo (`styx watch <repo>`, matching `styx repl <repo>`).
// It reads the on-disk mirror only — no app/routing/budget wiring — so it is
// registered in dispatch.go's pre-loadApp switch, next to `runs`.
//
// The mirror path MUST match what the writer (repl.go / mcp_conductor.go)
// computes: <StateDir>/watch/<projectID>.json. See watchProjectID above and
// docs/ARCHITECTURE.md's Activity section.
func cmdWatch(args []string) error {
	path, err := watchMirrorPath(args)
	if err != nil {
		return err
	}
	stall := watchStallThreshold()

	for {
		states, note, err := activity.ReadMirror(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				fmt.Println("(no live activity — no styx session is dispatching in this project)")
				return nil
			}
			return err
		}
		fmt.Print("\033[H\033[2J") // clear screen
		for _, line := range activity.Render(states, note, stall, time.Now()) {
			fmt.Println(line)
		}
		time.Sleep(time.Second)
	}
}

// watchStallThreshold returns the configured stall threshold
// (routing.toml's [watch] stall_threshold_seconds) so a cross-process `styx
// watch` flags stalls at the same duration as the in-process REPL /watch and
// inline LiveRenderer, which both read routing.Watch.StallThreshold(). This is
// a pure TOML parse (config.LoadRouting) — no loadApp()/sqlite wiring — so
// `watch` stays registered in dispatch.go's pre-loadApp switch. A missing or
// broken config falls back to activity.DefaultStall (90s) silently: watch is
// a read-only viewer and must keep working even when routing.toml is absent.
func watchStallThreshold() time.Duration {
	routing, err := config.LoadRouting()
	if err != nil {
		return activity.DefaultStall
	}
	return routing.Watch.StallThreshold()
}
