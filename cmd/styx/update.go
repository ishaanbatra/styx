package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	styxupdate "github.com/ishaanbatra/styx/internal/update"
)

const selfUpdateTimeout = 5 * time.Minute

func cmdUpdate(args []string) error {
	if len(args) == 1 && args[0] == "--check-only" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return styxupdate.Check(ctx)
	}
	if len(args) != 0 {
		return fmt.Errorf("usage: styx update")
	}

	oldVersion := styxDisplayVersion()
	ctx, cancel := context.WithTimeout(context.Background(), selfUpdateTimeout)
	defer cancel()
	newVersion, err := styxupdate.SelfUpdate(ctx, oldVersion)
	if err != nil {
		return err
	}
	oldVersion = releaseDisplayVersion(oldVersion)
	if oldVersion == newVersion {
		fmt.Printf("styx is already up to date (%s).\n", newVersion)
		return nil
	}
	fmt.Printf("styx updated: %s → %s\n", oldVersion, newVersion)
	return nil
}

func releaseDisplayVersion(version string) string {
	version = strings.TrimSpace(version)
	if strings.HasPrefix(version, "v") {
		return version
	}
	return "v" + version
}
