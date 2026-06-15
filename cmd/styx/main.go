// Styx is a personal multi-model dev orchestration CLI.
package main

import (
	"fmt"
	"os"
)

// parseGlobalFlags strips --quiet and --verbose from argv (long form only),
// returning the remaining tokens plus the two bools.
func parseGlobalFlags(argv []string) (rest []string, quiet, verbose bool) {
	for _, a := range argv {
		switch a {
		case "--quiet":
			quiet = true
		case "--verbose":
			verbose = true
		default:
			rest = append(rest, a)
		}
	}
	return rest, quiet, verbose
}

func main() {
	rest, quiet, verbose := parseGlobalFlags(os.Args[1:])

	if len(rest) == 0 {
		// Bare `styx` opens the REPL in the current project.
		globalQuiet = quiet
		globalVerbose = verbose
		if err := ensureFirstRun(); err != nil {
			fmt.Fprintf(os.Stderr, "styx: setup error: %v\n", err)
			os.Exit(1)
		}
		a, err := loadApp()
		if err != nil {
			fmt.Fprintf(os.Stderr, "styx: %v\n", err)
			os.Exit(1)
		}
		defer a.tracker.Close()
		if err := cmdREPL(a); err != nil {
			fmt.Fprintf(os.Stderr, "styx: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Store parsed globals so dispatch and verb handlers can read them.
	globalQuiet = quiet
	globalVerbose = verbose

	verb := rest[0]
	args := rest[1:]

	if err := ensureFirstRun(); err != nil {
		fmt.Fprintf(os.Stderr, "styx: setup error: %v\n", err)
		os.Exit(1)
	}

	if err := dispatch(verb, args); err != nil {
		fmt.Fprintf(os.Stderr, "styx: %v\n", err)
		os.Exit(1)
	}
}
