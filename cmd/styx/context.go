package main

import (
	"errors"
	"fmt"

	"github.com/ishaanbatra/styx/internal/intel"
)

func cmdContext(args []string) error {
	if len(args) == 0 || args[0] != "show" {
		return errors.New("usage: styx context show")
	}
	proj, err := resolveGlobalTarget("")
	if err != nil {
		return err
	}
	idx, err := intel.Load(proj)
	if err != nil {
		return fmt.Errorf("load intel index for %s: %w (try: styx intel %s)", proj.Name, err, proj.Name)
	}
	fmt.Print(intel.ToMarkdown(idx))
	return nil
}
