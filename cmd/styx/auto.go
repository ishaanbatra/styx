package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ishaanbatra/styx/internal/brief"
	"github.com/ishaanbatra/styx/internal/channel"
	"github.com/ishaanbatra/styx/internal/execute"
	"github.com/ishaanbatra/styx/internal/intel"
	"github.com/ishaanbatra/styx/internal/pipeline"
	"github.com/ishaanbatra/styx/internal/project"
	"github.com/ishaanbatra/styx/internal/research"
	"github.com/ishaanbatra/styx/internal/router"
	"github.com/ishaanbatra/styx/internal/signals"
)

func cmdAuto(a *app, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: styx auto [--deep] [--no-pr] [--no-push] [--resume <run-id>] <goal>")
	}
	var (
		deep     bool
		noPR     bool
		noPush   bool
		resumeID string
		goal     []string
	)
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--deep":
			deep = true
		case "--no-pr":
			noPR = true
		case "--no-push":
			noPush = true
		case "--resume":
			if i+1 >= len(args) {
				return fmt.Errorf("--resume requires a run-id")
			}
			resumeID = args[i+1]
			i++
		default:
			goal = append(goal, args[i])
		}
	}

	proj, err := project.Current()
	if err != nil {
		return err
	}

	var runID string
	if resumeID != "" {
		runID = resumeID
	} else {
		if len(goal) == 0 {
			return fmt.Errorf("goal is required (or pass --resume <id>)")
		}
		runID = pipeline.NewRunID(strings.Join(goal, " "))
	}

	r := buildRunner(a, proj, runID, strings.Join(goal, " "), deep, noPR, noPush)

	ctx := context.Background()
	if resumeID != "" {
		fmt.Fprintf(os.Stderr, "[styx] resuming run %s\n", runID)
		return pipeline.Resume(ctx, r)
	}
	fmt.Fprintf(os.Stderr, "[styx] starting run %s\n", runID)
	// Create branch.
	if err := gitCheckoutNewBranch(proj.Path, r.State.Branch); err != nil {
		return fmt.Errorf("create branch: %w", err)
	}
	return pipeline.Run(ctx, r)
}

// buildRunner wires the production adapters around the pipeline.Runner struct.
func buildRunner(a *app, proj project.Project, runID, goal string, deep, noPR, noPush bool) *pipeline.Runner {
	st := pipeline.NewState(runID, goal)
	r := &pipeline.Runner{
		State:       st,
		StateDir:    pipeline.RunDir(proj.Path, runID),
		ProjectPath: proj.Path,
		Goal:        goal,
		Deep:        deep,
		NoPR:        noPR,
		NoPush:      noPush,
	}

	r.RunResearch = func(ctx context.Context, rr *pipeline.Runner) (string, error) {
		g := rr.State.Goal
		drafter := routeChannel(a, "research", []string{g})
		critic := routeChannel(a, "research.critic", []string{g})
		drafter.projectPath = proj.Path
		b, err := research.Loop(ctx, g, drafter, critic)
		if err != nil {
			return "", err
		}
		b.DrafterChannel = drafter.id
		b.CriticChannel = critic.id
		if deep {
			urls := research.ExtractURLs(b.Drafts[len(b.Drafts)-1])
			if len(urls) > 0 {
				sources, _ := research.ChaseSources(ctx, urls, research.AgySummarizer(drafter))
				b.Sources = sources
			}
		}
		subDir := proj.ResearchDir
		if subDir == "" {
			subDir = "styx/research"
		}
		path, err := brief.WriteBrief(brief.WriteOpts{
			ProjectPath: proj.Path,
			SubDir:      subDir,
			Query:       g,
			Body:        research.RenderBrief(b),
			Now:         b.StartedAt,
		})
		if err != nil {
			return "", err
		}
		rel, _ := filepath.Rel(proj.Path, path)
		return rel, nil
	}

	r.EnsureIntel = func(ctx context.Context, rr *pipeline.Runner) (bool, string, error) {
		stale, reason, err := intel.IsStale(proj)
		if err != nil {
			return false, "", err
		}
		if !stale {
			return true, "intel fresh", nil
		}
		ag, ok := a.channels["agy"]
		if !ok {
			return false, "", fmt.Errorf("agy not registered")
		}
		if _, err := intel.Build(ctx, proj, &agyAdapter{ch: ag}); err != nil {
			return false, "", err
		}
		idx, err := intel.Load(proj)
		if err != nil {
			return false, "", err
		}
		_, err = intel.WriteContextMD(proj.Path, intel.ToMarkdown(idx))
		return false, reason, err
	}

	r.RunPlan = func(ctx context.Context, rr *pipeline.Runner) (string, error) {
		g := rr.State.Goal
		idx, err := intel.Load(proj)
		if err != nil {
			return "", err
		}
		ctxMD := intel.ToMarkdown(idx)
		subDirResearch := proj.ResearchDir
		if subDirResearch == "" {
			subDirResearch = "styx/research"
		}
		latest, _ := brief.LoadLatest(filepath.Join(proj.Path, subDirResearch))
		var briefBody string
		if latest != "" {
			b, _ := os.ReadFile(latest)
			briefBody = string(b)
		}
		prompt := fmt.Sprintf(`Create a detailed implementation plan for: %s

--- PROJECT CONTEXT ---
%s

--- RESEARCH BRIEF ---
%s
`, g, ctxMD, briefBody)
		sigs := signals.Extract("plan", []string{g}, proj)
		resp, _, err := sendWithFallback(a, ctx,
			router.Request{Verb: "plan", Args: []string{g}, Signals: sigs},
			channel.Request{Prompt: prompt, WorkingDir: proj.Path})
		if err != nil {
			return "", err
		}
		subDirPlans := proj.PlansDir
		if subDirPlans == "" {
			subDirPlans = "styx/plans"
		}
		path, err := brief.WritePlan(brief.WriteOpts{
			ProjectPath: proj.Path,
			SubDir:      subDirPlans,
			Query:       g,
			Body:        resp.Text,
		})
		if err != nil {
			return "", err
		}
		rel, _ := filepath.Rel(proj.Path, path)
		return rel, nil
	}

	r.RunExecute = func(ctx context.Context, rr *pipeline.Runner) ([]string, error) {
		// Read the most recent plan written by RunPlan.
		subDirPlans := proj.PlansDir
		if subDirPlans == "" {
			subDirPlans = "styx/plans"
		}
		latest, err := brief.LoadLatest(filepath.Join(proj.Path, subDirPlans))
		if err != nil || latest == "" {
			return nil, fmt.Errorf("no plan to execute")
		}
		planContent, err := os.ReadFile(latest)
		if err != nil {
			return nil, err
		}
		// Snapshot HEAD so we can list exactly the commits Apply added.
		preHead, _ := gitRevParse(proj.Path, "HEAD")
		_, err = execute.Apply(ctx, execute.Options{
			PlanContent: string(planContent),
			ProjectPath: proj.Path,
		})
		if err != nil {
			return nil, err
		}
		if preHead == "" {
			return nil, nil
		}
		return gitCommitsSince(proj.Path, preHead+"..HEAD"), nil
	}

	r.RunTest = func(ctx context.Context, rr *pipeline.Runner) (bool, string, error) {
		idx, err := intel.Load(proj)
		if err != nil {
			return false, "", err
		}
		argv := execute.DetectTestCommand(idx.Conventions.TestFramework)
		res, err := execute.RunTests(ctx, proj.Path, argv)
		if err != nil {
			return false, "", err
		}
		if res.Skipped {
			fmt.Fprintln(os.Stderr, "[styx] no test framework detected; skipping test stage")
			return true, "", nil
		}
		return res.Passed, res.Output, nil
	}

	r.RunFixTests = func(ctx context.Context, rr *pipeline.Runner, output string, attempt int) error {
		fixPrompt := fmt.Sprintf("Tests failed (attempt %d). Diagnose and fix. Commit fixes as separate commits.\n\n--- TEST OUTPUT ---\n%s", attempt, output)
		_, err := execute.Apply(ctx, execute.Options{
			PlanContent: fixPrompt,
			ProjectPath: proj.Path,
		})
		return err
	}

	r.RunReview = func(ctx context.Context, rr *pipeline.Runner) (int, int, string, error) {
		diff, err := gitDiffBase(proj.Path)
		if err != nil {
			return 0, 0, "", err
		}
		text, err := runReviewSynthesized(a, ctx, proj.Path, diff)
		if err != nil {
			return 0, 0, "", err
		}
		c, _ := research.Parse(text)
		return len(c.Blocking), len(c.Important), text, nil
	}

	r.RunFixReview = func(ctx context.Context, rr *pipeline.Runner, findings string, attempt int) error {
		fixPrompt := fmt.Sprintf("Review findings (attempt %d). Fix every BLOCKING and IMPORTANT item. Commit fixes.\n\n--- FINDINGS ---\n%s", attempt, findings)
		_, err := execute.Apply(ctx, execute.Options{
			PlanContent: fixPrompt,
			ProjectPath: proj.Path,
		})
		return err
	}

	r.RunShip = func(ctx context.Context, rr *pipeline.Runner) (string, bool, error) {
		res, err := execute.Ship(ctx, execute.ShipOptions{
			ProjectPath: proj.Path,
			Branch:      rr.State.Branch,
			NoPR:        noPR,
			NoPush:      noPush,
			Goal:        rr.State.Goal,
		})
		if err != nil {
			return "", false, err
		}
		return res.PRURL, res.Pushed, nil
	}

	return r
}

// routedChannel wraps channel.Channel with the model + id for use by research.Channel adapter.
type routedChannel struct {
	ch          channel.Channel
	model       string
	id          string
	projectPath string
}

func (rc *routedChannel) Send(ctx context.Context, prompt string) (string, error) {
	resp, err := rc.ch.Send(ctx, channel.Request{
		Model:      rc.model,
		Prompt:     prompt,
		WorkingDir: rc.projectPath,
	})
	if err != nil {
		return "", err
	}
	return resp.Text, nil
}

func routeChannel(a *app, verb string, args []string) *routedChannel {
	dec, err := a.router.Route(context.Background(), router.Request{Verb: verb, Args: args})
	if err != nil {
		// fall back to ollama-as-default
		return &routedChannel{ch: a.channels["ollama"], model: "qwen2.5-coder:14b", id: "ollama:qwen2.5-coder:14b"}
	}
	ch, ok := a.channels[dec.Channel]
	if !ok {
		return &routedChannel{ch: a.channels["ollama"], model: "qwen2.5-coder:14b", id: "ollama:qwen2.5-coder:14b"}
	}
	return &routedChannel{ch: ch, model: dec.Model, id: dec.Channel + ":" + dec.Model}
}

// gitCheckoutNewBranch creates and switches to a new branch from current HEAD.
func gitCheckoutNewBranch(repo, branch string) error {
	cmd := exec.Command("git", "checkout", "-b", branch)
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git checkout -b %s: %v (%s)", branch, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func gitCommitsSince(repo, rev string) []string {
	cmd := exec.Command("git", "rev-list", rev)
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var shas []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if len(line) >= 7 {
			shas = append(shas, line[:7])
		}
	}
	return shas
}

func gitRevParse(repo, ref string) (string, error) {
	cmd := exec.Command("git", "rev-parse", ref)
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// defaultBranch resolves the upstream default branch, falling back through
// origin/HEAD -> local main -> master -> trunk -> dev.
func defaultBranch(repo string) string {
	cmd := exec.Command("git", "symbolic-ref", "--short", "refs/remotes/origin/HEAD")
	cmd.Dir = repo
	if out, err := cmd.Output(); err == nil {
		s := strings.TrimSpace(string(out))
		if i := strings.Index(s, "/"); i >= 0 && i < len(s)-1 {
			return s[i+1:]
		}
	}
	for _, b := range []string{"main", "master", "trunk", "dev"} {
		c := exec.Command("git", "rev-parse", "--verify", "--quiet", b)
		c.Dir = repo
		if err := c.Run(); err == nil {
			return b
		}
	}
	return "main"
}

func gitDiffBase(repo string) (string, error) {
	base := defaultBranch(repo)
	cmd := exec.Command("git", "diff", base+"...HEAD")
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}
