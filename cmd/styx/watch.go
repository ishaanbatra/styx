package main

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"time"

	"github.com/ishaanbatra/styx/internal/activity"
	"github.com/ishaanbatra/styx/internal/paths"
)

// cmdWatch renders the live dispatch board written by a running styx session
// (REPL or `styx mcp` conductor) in the current project. It reads the
// on-disk mirror only — no app/routing/budget wiring — so it is registered in
// dispatch.go's pre-loadApp switch, next to `runs`.
//
// The mirror path MUST match what the writer (repl.go / mcp_conductor.go)
// computes: <StateDir>/watch/<projectID>.json, where projectID comes from
// resolveGlobalTarget("") — the same cwd-based resolution the writer uses at
// its own launch time. See docs/ARCHITECTURE.md's Activity section.
func cmdWatch(args []string) error {
	proj, err := resolveGlobalTarget("")
	if err != nil {
		return err
	}
	stateDir, err := paths.StateDir()
	if err != nil {
		return err
	}
	path := filepath.Join(stateDir, "watch", proj.ID+".json")

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
		for _, line := range activity.Render(states, note, activity.DefaultStall, time.Now()) {
			fmt.Println(line)
		}
		time.Sleep(time.Second)
	}
}
