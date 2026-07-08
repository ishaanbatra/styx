package main

// styx learn — the self-improvement digest verb (spec
// docs/superpowers/specs/2026-07-07-styx-self-improvement-design.md).
// Manual only: no daemons, no background learning. All learning lands as
// plain-text memories with provenance in the global memory store — never
// code changes, never routing.toml edits.

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ishaanbatra/styx/internal/learn"
	"github.com/ishaanbatra/styx/internal/memory"
	"github.com/ishaanbatra/styx/internal/paths"
)

// scorecardWindow is the trailing outcome window styx learn aggregates.
const scorecardWindow = 30 * 24 * time.Hour

func cmdLearn(a *app, args []string) error {
	var scorecardOnly, dryRun, list bool
	var forgetID int64
	var forget bool
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--scorecard":
			scorecardOnly = true
		case "--dry-run":
			dryRun = true
		case "--list":
			list = true
		case "--forget":
			i++
			if i >= len(args) {
				return fmt.Errorf("--forget needs a memory id (styx learn --list shows ids)")
			}
			id, err := strconv.ParseInt(args[i], 10, 64)
			if err != nil {
				return fmt.Errorf("--forget: bad id %q: %w", args[i], err)
			}
			forgetID, forget = id, true
		default:
			return fmt.Errorf("unknown flag %q (usage: styx learn [--scorecard|--dry-run|--list|--forget <id>])", args[i])
		}
	}
	ctx := context.Background()
	if scorecardOnly {
		out, err := learnScorecard(ctx, a)
		if err != nil {
			return err
		}
		fmt.Print(out)
		return nil
	}
	store, err := openGlobalMemory()
	if err != nil {
		return err
	}
	defer store.Close()
	switch {
	case list:
		out, err := learnList(ctx, store)
		if err != nil {
			return err
		}
		fmt.Print(out)
		return nil
	case forget:
		out, err := learnForget(ctx, store, forgetID)
		if err != nil {
			return err
		}
		fmt.Print(out)
		return nil
	}
	return runLearn(ctx, a, store, dryRun)
}

// dedupeSimilarity is the same-kind cosine threshold above which a candidate
// refreshes an existing memory instead of adding a new row.
const dedupeSimilarity = 0.9

// runLearn wires the production digester (local ollama brain model) and
// embedder into runLearnDigest.
func runLearn(ctx context.Context, a *app, store *memory.Store, dryRun bool) error {
	emb := memory.NewOllamaEmbedder("http://localhost:11434", a.routing.Brain.EmbedModel)
	dig := &learn.Digester{BaseURL: "http://localhost:11434", Model: a.routing.Brain.Model}
	out, err := runLearnDigest(ctx, a, store, emb, dig, dryRun)
	if err != nil {
		return err
	}
	fmt.Print(out)
	return nil
}

// runLearnDigest is one digest pass: scorecard → gather → propose → evidence
// guard → dedupe → write with provenance → mark retrospectives consumed.
// All embeds and dedupe decisions happen BEFORE any write, so an ollama
// failure mid-pass writes nothing partial. Dry-run stops after planning.
func runLearnDigest(ctx context.Context, a *app, store *memory.Store, emb memory.Embedder, dig *learn.Digester, dryRun bool) (string, error) {
	rows, err := a.tracker.OutcomesSince(ctx, time.Now().Add(-scorecardWindow))
	if err != nil {
		return "", fmt.Errorf("read outcomes: %w", err)
	}
	sc := learn.Build(rows, 30)
	retroItems, err := store.UnconsumedByKind(ctx, memory.KindRetrospective)
	if err != nil {
		return "", fmt.Errorf("gather retrospectives: %w", err)
	}
	retros := make([]learn.RetroNote, len(retroItems))
	for i, it := range retroItems {
		retros[i] = learn.RetroNote{ID: it.ID, Text: it.Text}
	}
	var notes []string
	for _, o := range rows {
		if o.Rating != "" && o.Note != "" {
			notes = append(notes, fmt.Sprintf("%s (%s): %s", o.Rating, o.CLI, o.Note))
		}
	}

	cands, err := dig.Propose(ctx, sc.Render(), retros, notes)
	if err != nil {
		return "", err // loud; nothing written
	}
	kept, dropped := learn.FilterByEvidence(cands, sc, retros)

	var b strings.Builder
	for _, d := range dropped {
		fmt.Fprintf(&b, "dropped: %s\n", d)
	}
	if len(kept) == 0 {
		b.WriteString("nothing to learn this round (no candidates survived the evidence guard)\n")
		return b.String(), nil
	}

	// Plan phase: embed + dedupe-check every survivor before writing anything.
	type plannedWrite struct {
		cand   learn.Candidate
		text   string
		vec    []float32
		dupeID int64 // >0: refresh this row instead of adding
	}
	date := time.Now().Format("2006-01-02")
	var plans []plannedWrite
	for _, c := range kept {
		vec, err := emb.Embed(ctx, c.Text)
		if err != nil {
			return "", fmt.Errorf("embed candidate (is ollama up?): %w", err)
		}
		p := plannedWrite{
			cand: c,
			text: fmt.Sprintf("%s [learned-by styx-learn %s; evidence: %s]", c.Text, date, c.Evidence),
			vec:  vec,
		}
		if it, sim, err := store.MostSimilar(ctx, memory.Kind(c.Kind), vec); err != nil {
			return "", fmt.Errorf("dedupe scan: %w", err)
		} else if sim >= dedupeSimilarity {
			p.dupeID = it.ID
		}
		plans = append(plans, p)
	}

	if dryRun {
		for _, p := range plans {
			verb := "would learn"
			if p.dupeID > 0 {
				verb = fmt.Sprintf("would refresh memory %d", p.dupeID)
			}
			fmt.Fprintf(&b, "%s [%s, conf %.2f]: %s\n", verb, p.cand.Kind, p.cand.Confidence, p.text)
		}
		b.WriteString("dry run: nothing written, retrospectives left unconsumed\n")
		return b.String(), nil
	}

	// Write phase.
	for _, p := range plans {
		if p.dupeID > 0 {
			if err := store.UpdateEvidence(ctx, p.dupeID, p.text); err != nil {
				return "", fmt.Errorf("refresh memory %d: %w", p.dupeID, err)
			}
			fmt.Fprintf(&b, "refreshed %d [%s]: %s\n", p.dupeID, p.cand.Kind, p.text)
			continue
		}
		id, err := store.Add(ctx, memory.Item{
			Kind: memory.Kind(p.cand.Kind), Text: p.text, Source: "styx-learn",
			Scope: "global", Confidence: p.cand.Confidence, Embedding: p.vec,
		})
		if err != nil {
			return "", fmt.Errorf("write memory: %w", err)
		}
		fmt.Fprintf(&b, "learned %d [%s]: %s\n", id, p.cand.Kind, p.text)
	}
	retroIDs := make([]int64, len(retros))
	for i, r := range retros {
		retroIDs[i] = r.ID
	}
	if err := store.MarkConsumed(ctx, retroIDs); err != nil {
		return "", fmt.Errorf("mark retrospectives consumed: %w", err)
	}
	return b.String(), nil
}

// openGlobalMemory opens ~/.config/styx/state/memory/global.db — where
// learned memories live (the store launch guidance injection reads).
func openGlobalMemory() (*memory.Store, error) {
	memDir, err := paths.MemoryDir()
	if err != nil {
		return nil, err
	}
	if err := paths.EnsureDir(memDir); err != nil {
		return nil, err
	}
	return memory.Open(filepath.Join(memDir, "global.db"))
}

// learnScorecard renders the deterministic cli × signal scorecard over the
// trailing 30 days of dispatch outcomes. No LLM involvement.
func learnScorecard(ctx context.Context, a *app) (string, error) {
	rows, err := a.tracker.OutcomesSince(ctx, time.Now().Add(-scorecardWindow))
	if err != nil {
		return "", fmt.Errorf("read outcomes: %w", err)
	}
	return learn.Build(rows, 30).Render(), nil
}

// learnList renders the learned set (routing + user preferences) with ids
// and provenance so --forget has addressable targets.
func learnList(ctx context.Context, store *memory.Store) (string, error) {
	var b strings.Builder
	total := 0
	for _, kind := range []memory.Kind{memory.KindRoutingPreference, memory.KindUserPreference} {
		items, err := store.TopByKind(ctx, kind, 100)
		if err != nil {
			return "", fmt.Errorf("list %s memories: %w", kind, err)
		}
		if len(items) == 0 {
			continue
		}
		fmt.Fprintf(&b, "%s:\n", kind)
		for _, it := range items {
			fmt.Fprintf(&b, "  [%d] %s (source %s, %s, conf %.2f)\n",
				it.ID, it.Text, it.Source, it.CreatedAt.Format("2006-01-02"), it.Confidence)
		}
		total += len(items)
	}
	if total == 0 {
		return "no learned memories yet — dispatch some work, then run styx learn\n", nil
	}
	return b.String(), nil
}

// learnForget hard-deletes one memory by id — the reversibility guarantee.
func learnForget(ctx context.Context, store *memory.Store, id int64) (string, error) {
	if err := store.Delete(ctx, id); err != nil {
		return "", err
	}
	return fmt.Sprintf("forgot memory %d\n", id), nil
}
