// Styx is a personal multi-model dev orchestration CLI.
package main

import (
	"fmt"
	"os"
	"runtime/debug"
	"strings"
)

// styxVersion is printed by `styx version`. Release builds stamp it via ldflags.
var styxVersion = "0.4.0-dev"

func styxDisplayVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return resolveStyxDisplayVersion(styxVersion, debug.BuildInfo{})
	}
	return resolveStyxDisplayVersion(styxVersion, *info)
}

func resolveStyxDisplayVersion(version string, info debug.BuildInfo) string {
	if !strings.HasSuffix(version, "-dev") {
		return version
	}

	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}

	var revision string
	modified := false
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}
	if revision == "" {
		return version
	}
	if len(revision) > 7 {
		revision = revision[:7]
	}
	if modified {
		revision += "-dirty"
	}
	return "dev-" + revision
}

// parseGlobalFlags strips global flags from argv (long form only), returning
// the remaining tokens plus the parsed values.
func parseGlobalFlags(argv []string) (rest []string, quiet, verbose bool, projectAlias, dirArg, host string) {
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "--quiet":
			quiet = true
		case a == "--verbose":
			verbose = true
		case a == "--project":
			if i+1 < len(argv) {
				projectAlias = argv[i+1]
				i++
			}
		case strings.HasPrefix(a, "--project="):
			projectAlias = strings.TrimPrefix(a, "--project=")
		case a == "--dir":
			if i+1 < len(argv) {
				dirArg = argv[i+1]
				i++
			}
		case strings.HasPrefix(a, "--dir="):
			dirArg = strings.TrimPrefix(a, "--dir=")
		case a == "--host":
			if i+1 < len(argv) {
				host = argv[i+1]
				i++
			}
		case strings.HasPrefix(a, "--host="):
			host = strings.TrimPrefix(a, "--host=")
		default:
			rest = append(rest, a)
		}
	}
	return rest, quiet, verbose, projectAlias, dirArg, host
}

func main() {
	rest, quiet, verbose, projectAlias, dirArg, host := parseGlobalFlags(os.Args[1:])

	if len(rest) == 0 {
		// Bare `styx` launches the configured conductor host with the styx MCP
		// toolbelt in the current project. `styx repl` still opens the
		// classic v0.2 REPL.
		globalQuiet = quiet
		globalVerbose = verbose
		globalProjectAlias = projectAlias
		globalDirArg = dirArg
		globalHost = host
		runLaunchUpdateChecks()
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
		if err := cmdLaunch(a); err != nil {
			fmt.Fprintf(os.Stderr, "styx: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Store parsed globals so dispatch and verb handlers can read them.
	globalQuiet = quiet
	globalVerbose = verbose
	globalProjectAlias = projectAlias
	globalDirArg = dirArg
	globalHost = host

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
