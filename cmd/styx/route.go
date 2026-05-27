package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/ishaanbatra/styx/internal/project"
	"github.com/ishaanbatra/styx/internal/router"
	"github.com/ishaanbatra/styx/internal/signals"
)

func cmdRoute(args []string) error {
	if len(args) < 2 || args[0] != "--explain" {
		return fmt.Errorf("usage: styx route --explain <verb> \"<text>\"")
	}
	verb := args[1]
	text := strings.Join(args[2:], " ")
	a, err := loadApp()
	if err != nil {
		return err
	}
	defer a.tracker.Close()
	proj, _ := project.Current()
	sigs := signals.Extract(verb, []string{text}, proj)
	fmt.Print(a.router.Explain(context.Background(), router.Request{Verb: verb, Args: []string{text}, Signals: sigs}))
	return nil
}
