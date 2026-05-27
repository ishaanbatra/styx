// Styx is a personal multi-model dev orchestration CLI.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		printHelp()
		return
	}
	verb := os.Args[1]
	args := os.Args[2:]

	if err := ensureFirstRun(); err != nil {
		fmt.Fprintf(os.Stderr, "styx: setup error: %v\n", err)
		os.Exit(1)
	}

	if err := dispatch(verb, args); err != nil {
		fmt.Fprintf(os.Stderr, "styx: %v\n", err)
		os.Exit(1)
	}
}
