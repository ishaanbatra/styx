package main

import (
	"context"
	"fmt"
	"os"

	"github.com/ishaanbatra/styx/internal/channel"
	"github.com/ishaanbatra/styx/internal/router"
	"github.com/ishaanbatra/styx/internal/signals"
)

func cmdBuild(a *app, args []string) error {
	target := ""
	if len(args) > 0 {
		target = args[0]
	}
	proj, err := resolveTarget(target)
	if err != nil {
		return err
	}

	sigs := signals.Extract("build", args, proj)
	dec, err := a.router.Route(context.Background(), router.Request{Verb: "build", Args: args, Signals: sigs})
	if err != nil {
		return err
	}
	ch, ok := a.channels[dec.Channel]
	if !ok {
		return fmt.Errorf("unknown channel %q for build", dec.Channel)
	}
	fmt.Fprintf(os.Stderr, "[styx] -> %s (%s:%s)\n", proj.Path, dec.Channel, dec.Model)
	_, err = ch.Send(context.Background(), channel.Request{
		Model:       dec.Model,
		Interactive: true,
		WorkingDir:  proj.Path,
	})
	return err
}
