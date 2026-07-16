package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ishaanbatra/styx/internal/brief"
	"github.com/ishaanbatra/styx/internal/budget"
	"github.com/ishaanbatra/styx/internal/channel"
	"github.com/ishaanbatra/styx/internal/config"
	ferddebug "github.com/ishaanbatra/styx/internal/debug"
	"github.com/ishaanbatra/styx/internal/pipeline"
	"github.com/ishaanbatra/styx/internal/progress"
	"github.com/ishaanbatra/styx/internal/router"
	"github.com/ishaanbatra/styx/internal/signals"
)

type debugCLIArgs struct {
	bug        string
	testName   string
	logPaths   []string
	fileHints  []string
	reviewOnly string
}

// cmdDebug runs ultraFerdDebug: a read-only repository sweep followed by
// scaled independent review and a deterministic verdict.
func cmdDebug(ctx context.Context, a *app, prog *progress.Tracker, args []string) error {
	parsed, err := parseDebugArgs(args)
	if err != nil {
		return err
	}
	if prog == nil {
		prog = a.progress
	}
	if prog == nil {
		prog = progress.Quiet()
	}

	proj, err := resolveGlobalTarget("")
	if err != nil {
		return fmt.Errorf("resolve debug project: %w", err)
	}

	parsed.logPaths, err = resolveDebugLogPaths(parsed.logPaths)
	if err != nil {
		return err
	}
	presetBrief := ""
	if parsed.reviewOnly != "" {
		b, err := os.ReadFile(parsed.reviewOnly)
		if err != nil {
			return fmt.Errorf("read review-only brief %s: %w", parsed.reviewOnly, err)
		}
		presetBrief = string(b)
	}

	sigs := signals.Extract("debug", args, proj)
	runID := pipeline.NewRunID("debug-" + parsed.bug)
	projectID := config.ProjectID(proj.Path)

	var sweepAdapter *debugChannelAdapter
	sweepName := "review-only"
	if presetBrief == "" {
		dec, err := a.router.Route(ctx, router.Request{Verb: "debug.sweep", Args: []string{parsed.bug}, Signals: sigs})
		if err != nil {
			return fmt.Errorf("route debug.sweep: %w", err)
		}
		if dec.BlockedByBudget {
			return fmt.Errorf("debug sweep blocked by budget or circuit state; recommended target %s once available; free budget or pass --review-only <existing brief>", debugDecisionLabel(dec))
		}
		ch, ok := a.channels[dec.Channel]
		if !ok {
			return fmt.Errorf("unknown debug sweep channel %q", dec.Channel)
		}
		sweepName = debugDecisionLabel(dec)
		sweepAdapter = newDebugChannelAdapter(a, rawChannel(ch), dec, "debug.sweep", projectID, runID, proj.Path)
		sweepAdapter.attachments = debugLogAttachments(parsed.logPaths)
		sweepAdapter.extraRoots = debugLogRoots(proj.Path, parsed.logPaths)
		if dec.Degraded {
			logStatus("debug sweep degraded to %s: %s", sweepName, dec.Reason)
		}
	}

	codexAdapter, codexName, err := routeDebugReviewer(ctx, a, "debug.review.codex", parsed.bug, sigs, projectID, runID)
	if err != nil {
		return err
	}
	var claudeAdapter *debugChannelAdapter
	claudeName := ""
	if len(parsed.logPaths) == 0 {
		claudeAdapter, claudeName, err = routeDebugReviewer(ctx, a, "debug.review.claude", parsed.bug, sigs, projectID, runID)
		if err != nil {
			return err
		}
	}

	debugDir := proj.DebugDir
	if debugDir == "" {
		debugDir = "styx/debug"
	}
	artifactTime := time.Now().UTC()
	input := ferddebug.Input{
		Bug:         parsed.bug,
		TestName:    parsed.testName,
		LogPaths:    append([]string(nil), parsed.logPaths...),
		FileHints:   append([]string(nil), parsed.fileHints...),
		ProjectPath: proj.Path,
	}
	// Tree guard: agy's headless CLI auto-approves file writes (no read-only
	// flag exists), so "diagnosis only" cannot be enforced upstream. Snapshot
	// the working tree before the sweep and compare right after it (inside
	// PersistBrief, which Run calls between sweep and reviews).
	preTree, treeErr := gitTreeState(ctx, proj.Path)
	var sweepDirtied []string
	start := time.Now()
	rep, err := ferddebug.Run(ctx, ferddebug.Options{
		Input:         input,
		Sweeper:       sweepAdapter,
		Codex:         codexAdapter,
		Claude:        claudeAdapter,
		Prog:          prog,
		SweepChannel:  sweepName,
		CodexChannel:  codexName,
		ClaudeChannel: claudeName,
		PresetBrief:   presetBrief,
		PersistBrief: func(body string) (string, error) {
			if treeErr == nil {
				if postTree, err := gitTreeState(ctx, proj.Path); err == nil {
					sweepDirtied = treeStateDiff(preTree, postTree)
				}
			}
			if len(sweepDirtied) > 0 {
				logStatus("⚠ %s sweep modified the working tree (%s) — review and revert; treating brief with suspicion", ferddebug.PipelineName, strings.Join(sweepDirtied, ", "))
			}
			return brief.WriteBrief(brief.WriteOpts{
				ProjectPath: proj.Path,
				SubDir:      debugDir,
				Query:       parsed.bug,
				Body:        body,
				Now:         artifactTime,
			})
		},
	})
	if err != nil {
		return fmt.Errorf("run %s: %w", ferddebug.PipelineName, err)
	}
	rep.SweepDirtied = sweepDirtied

	out, err := brief.WriteReport(brief.WriteOpts{
		ProjectPath: proj.Path,
		SubDir:      debugDir,
		Query:       parsed.bug,
		Body:        ferddebug.RenderReport(rep),
		Now:         artifactTime,
	})
	if err != nil {
		return fmt.Errorf("write debug report: %w", err)
	}
	rel, _ := filepath.Rel(proj.Path, out)
	logStatus("✓ %s report saved: %s", ferddebug.PipelineName, rel)
	logStatus("  Verdict: %s confidence; confirmed=%t", rep.Verdict.Confidence, rep.Verdict.Confirmed)

	var tokensIn, tokensOut int
	for _, adapter := range []*debugChannelAdapter{sweepAdapter, codexAdapter, claudeAdapter} {
		if adapter != nil {
			tokensIn += adapter.response.EstTokensIn
			tokensOut += adapter.response.EstTokensOut
		}
	}
	if a.tracker != nil {
		_ = a.tracker.RecordOutcome(ctx, budget.Outcome{
			Project: projectID, CLI: ferddebug.PipelineName,
			Signals: strings.Join(sigs, ","), Risk: "read",
			DurationS: time.Since(start).Seconds(), TokensIn: tokensIn, TokensOut: tokensOut,
		})
	}
	return nil
}

func routeDebugReviewer(ctx context.Context, a *app, verb, bug string, sigs []string, projectID, runID string) (*debugChannelAdapter, string, error) {
	dec, err := a.router.Route(ctx, router.Request{Verb: verb, Args: []string{bug}, Signals: sigs})
	if err != nil {
		return nil, "", fmt.Errorf("route %s: %w", verb, err)
	}
	name := debugDecisionLabel(dec)
	if dec.BlockedByBudget {
		return &debugChannelAdapter{blockedErr: fmt.Errorf("%s blocked by budget or circuit state (recommended %s)", verb, name)}, name, nil
	}
	ch, ok := a.channels[dec.Channel]
	if !ok {
		return &debugChannelAdapter{blockedErr: fmt.Errorf("unknown review channel %q", dec.Channel)}, name, nil
	}
	return newDebugChannelAdapter(a, rawChannel(ch), dec, verb, projectID, runID, ""), name, nil
}

func debugDecisionLabel(dec router.Decision) string {
	if dec.Model == "" {
		return dec.Channel
	}
	return dec.Channel + ":" + dec.Model
}

type debugChannelAdapter struct {
	ch          channel.Channel
	tracker     *budget.Tracker
	channelName string
	model       string
	effort      string
	role        string
	projectID   string
	runID       string
	projectPath string
	attachments []channel.Attachment
	extraRoots  []string
	blockedErr  error
	response    channel.Response
}

func newDebugChannelAdapter(a *app, ch channel.Channel, dec router.Decision, role, projectID, runID, projectPath string) *debugChannelAdapter {
	return &debugChannelAdapter{
		ch: ch, tracker: a.tracker, channelName: dec.Channel, model: dec.Model, effort: dec.Effort,
		role: role, projectID: projectID, runID: runID, projectPath: projectPath,
	}
}

func (a *debugChannelAdapter) Send(ctx context.Context, prompt string) (string, error) {
	if a.blockedErr != nil {
		return "", a.blockedErr
	}
	if a.ch == nil {
		return "", errors.New("debug channel is unavailable")
	}
	resp, err := a.ch.Send(ctx, channel.Request{
		Model: a.model, Effort: a.effort, Prompt: prompt, WorkingDir: a.projectPath,
		Attachments: append([]channel.Attachment(nil), a.attachments...),
		ExtraRoots:  append([]string(nil), a.extraRoots...),
	})
	a.response = resp
	if a.tracker != nil {
		_ = a.tracker.Record(ctx, budget.Event{
			Channel: a.channelName, Verb: a.role, Model: a.model,
			TokensIn: resp.EstTokensIn, TokensOut: resp.EstTokensOut,
			Success: err == nil, ErrorKind: errorKindOf(err),
			Project: a.projectID, RunID: a.runID,
		})
	}
	return resp.Text, err
}

func parseDebugArgs(args []string) (debugCLIArgs, error) {
	var out debugCLIArgs
	var positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		value := func(name string) (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s requires a value", name)
			}
			i++
			return args[i], nil
		}
		switch {
		case arg == "--":
			positional = append(positional, args[i+1:]...)
			i = len(args)
		case arg == "--test":
			out.testName, _ = value("--test")
			if out.testName == "" {
				return debugCLIArgs{}, fmt.Errorf("--test requires a value")
			}
		case strings.HasPrefix(arg, "--test="):
			out.testName = strings.TrimPrefix(arg, "--test=")
		case arg == "--log":
			v, err := value("--log")
			if err != nil || v == "" || strings.HasPrefix(v, "-") {
				return debugCLIArgs{}, fmt.Errorf("--log requires a value")
			}
			out.logPaths = append(out.logPaths, v)
			// In entry mode, one --log accepts a corpus. A following flag ends
			// the corpus; use -- before an optional free-form description.
			for i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				out.logPaths = append(out.logPaths, args[i])
			}
		case strings.HasPrefix(arg, "--log="):
			v := strings.TrimPrefix(arg, "--log=")
			if v == "" {
				return debugCLIArgs{}, fmt.Errorf("--log requires a value")
			}
			out.logPaths = append(out.logPaths, v)
		case arg == "--file":
			v, err := value("--file")
			if err != nil || v == "" {
				return debugCLIArgs{}, fmt.Errorf("--file requires a value")
			}
			out.fileHints = append(out.fileHints, v)
		case strings.HasPrefix(arg, "--file="):
			out.fileHints = append(out.fileHints, strings.TrimPrefix(arg, "--file="))
		case arg == "--review-only":
			out.reviewOnly, _ = value("--review-only")
			if out.reviewOnly == "" {
				return debugCLIArgs{}, fmt.Errorf("--review-only requires a value")
			}
		case strings.HasPrefix(arg, "--review-only="):
			out.reviewOnly = strings.TrimPrefix(arg, "--review-only=")
		case strings.HasPrefix(arg, "-"):
			return debugCLIArgs{}, fmt.Errorf("unknown debug flag %q", arg)
		default:
			positional = append(positional, arg)
		}
	}
	out.bug = strings.TrimSpace(strings.Join(positional, " "))
	if len(out.logPaths) > 0 && out.reviewOnly != "" {
		return debugCLIArgs{}, fmt.Errorf("--log and --review-only cannot be combined")
	}
	if out.bug == "" && out.reviewOnly != "" {
		out.bug = "review-only debug diagnosis"
	}
	if out.bug == "" && len(out.logPaths) > 0 {
		out.bug = "failure triage from provided logs"
	}
	if out.bug == "" {
		return debugCLIArgs{}, fmt.Errorf("usage: styx debug <bug description> | styx debug --log <file...> [-- <failure description>] | styx debug --review-only <brief-path> [bug]")
	}
	return out, nil
}

func resolveDebugLogPaths(paths []string) ([]string, error) {
	resolved := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("resolve debug log %s: %w", path, err)
		}
		info, err := os.Stat(abs)
		if err != nil {
			return nil, fmt.Errorf("stat debug log %s: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("debug log %s is not a regular file", path)
		}
		if _, ok := seen[abs]; ok {
			continue
		}
		seen[abs] = struct{}{}
		resolved = append(resolved, abs)
	}
	return resolved, nil
}

func debugLogAttachments(paths []string) []channel.Attachment {
	attachments := make([]channel.Attachment, 0, len(paths))
	for _, path := range paths {
		attachments = append(attachments, channel.Attachment{Path: path, Mime: "text/plain"})
	}
	return attachments
}

func debugLogRoots(projectPath string, paths []string) []string {
	projectPath, _ = filepath.Abs(projectPath)
	seen := map[string]struct{}{}
	var roots []string
	for _, path := range paths {
		if pathWithin(projectPath, path) {
			continue
		}
		root := filepath.Dir(path)
		if _, ok := seen[root]; ok {
			continue
		}
		seen[root] = struct{}{}
		roots = append(roots, root)
	}
	return roots
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// gitTreeState returns the porcelain status of dir's working tree, bounded so
// a hung git can never stall the pathway. A non-git dir (or missing git)
// returns an error and the caller skips the tree guard.
func gitTreeState(ctx context.Context, dir string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git status in %s: %w", dir, err)
	}
	return string(out), nil
}

// treeStateDiff returns the porcelain lines present after the sweep but not
// before it — the paths the sweep itself touched.
func treeStateDiff(pre, post string) []string {
	if pre == post {
		return nil
	}
	before := map[string]struct{}{}
	for _, line := range strings.Split(pre, "\n") {
		if line != "" {
			before[line] = struct{}{}
		}
	}
	var dirtied []string
	for _, line := range strings.Split(post, "\n") {
		if line == "" {
			continue
		}
		if _, ok := before[line]; !ok {
			dirtied = append(dirtied, strings.TrimSpace(line))
		}
	}
	return dirtied
}
