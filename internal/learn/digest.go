package learn

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Candidate is one memory the digest model proposes. The evidence guard
// (FilterByEvidence) decides whether it survives.
type Candidate struct {
	Kind       string  `json:"kind"` // routing-preference | user-preference
	Text       string  `json:"text"`
	Confidence float64 `json:"confidence"`
	Evidence   string  `json:"evidence"` // "scorecard:<cli>/<signal>" or "retro:<id>"
}

// RetroNote is one unconsumed retrospective offered to the digest.
type RetroNote struct {
	ID   int64
	Text string
}

// maxCandidates caps what one digest run may propose — a hallucination
// bound: at worst 5 bad sentences, each still evidence-checked and printed.
const maxCandidates = 5

// candidateSchema is the ollama structured-output format for Propose.
var candidateSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"candidates": {
			"type": "array",
			"items": {
				"type": "object",
				"properties": {
					"kind": {"type": "string", "enum": ["routing-preference", "user-preference"]},
					"text": {"type": "string"},
					"confidence": {"type": "number"},
					"evidence": {"type": "string"}
				},
				"required": ["kind", "text", "confidence", "evidence"]
			}
		}
	},
	"required": ["candidates"]
}`)

const digestSystem = `You are styx's learning digester. From a dispatch
scorecard and session notes, propose at most 5 durable memories that would
improve future routing or match how the user works. Kinds:
- routing-preference: which CLI suits which kind of work, grounded in the scorecard.
- user-preference: how the user likes to work, grounded in retrospectives or rating notes.
Each candidate needs:
- text: ONE standalone plain sentence (it will be injected into future guidance verbatim).
- confidence: 0 to 1.
- evidence: EXACTLY one citation — "scorecard:<cli>/<signal>" naming a scorecard line, or "retro:<id>" naming a retrospective id shown to you.
Propose nothing when the data is thin; fewer, stronger memories beat many weak ones.`

// Digester proposes candidate memories via the local ollama brain model.
type Digester struct {
	BaseURL string // e.g. http://localhost:11434
	Model   string // e.g. qwen2.5-coder:7b

	client *http.Client
}

func (d *Digester) httpClient() *http.Client {
	if d.client == nil {
		d.client = &http.Client{Timeout: 120 * time.Second}
	}
	return d.client
}

type digestChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type digestChatRequest struct {
	Model     string              `json:"model"`
	Stream    bool                `json:"stream"`
	Think     bool                `json:"think"`
	Format    json.RawMessage     `json:"format"`
	KeepAlive string              `json:"keep_alive,omitempty"`
	Options   map[string]any      `json:"options"`
	Messages  []digestChatMessage `json:"messages"`
}

type digestChatResponse struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
}

// Propose asks the local model for candidate memories. Fails loudly when
// ollama is unreachable or emits garbage — the caller writes nothing then.
func (d *Digester) Propose(ctx context.Context, scorecard string, retros []RetroNote, ratingNotes []string) ([]Candidate, error) {
	var u strings.Builder
	u.WriteString("Scorecard (ground truth):\n")
	u.WriteString(scorecard)
	u.WriteString("\nUnconsumed retrospectives:\n")
	if len(retros) == 0 {
		u.WriteString("  (none)\n")
	}
	for _, r := range retros {
		fmt.Fprintf(&u, "  retro:%d: %s\n", r.ID, r.Text)
	}
	u.WriteString("Rating notes (30d):\n")
	if len(ratingNotes) == 0 {
		u.WriteString("  (none)\n")
	}
	for _, n := range ratingNotes {
		fmt.Fprintf(&u, "  %s\n", n)
	}
	user := u.String()

	opts := map[string]any{"temperature": 0}
	if est := (len(digestSystem) + len(user)) / 4; est+1024 > 4096 {
		// Ollama defaults num_ctx to 4096 and silently truncates beyond it.
		opts["num_ctx"] = est + 2048
	}
	body, err := json.Marshal(digestChatRequest{
		Model:     d.Model,
		Stream:    false,
		Think:     false, // classification-shaped task; reasoning bleed breaks structured output
		Format:    candidateSchema,
		KeepAlive: "30m",
		Options:   opts,
		Messages: []digestChatMessage{
			{Role: "system", Content: digestSystem},
			{Role: "user", Content: user},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal digest request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.BaseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build digest request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("digest call (is ollama up?): %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read digest response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("ollama digest %d: %s", resp.StatusCode, string(raw))
	}
	var cr digestChatResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		return nil, fmt.Errorf("parse digest response envelope: %w", err)
	}
	var out struct {
		Candidates []Candidate `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(cr.Message.Content), &out); err != nil {
		return nil, fmt.Errorf("digest model emitted invalid JSON: %w", err)
	}
	return out.Candidates, nil
}

// FilterByEvidence is the mechanical hallucination guard: candidates whose
// citation does not name a real scorecard cell or a gathered retrospective —
// or whose kind/text/confidence is malformed — are dropped before anything
// is written. Returns the survivors (at most maxCandidates) and one
// human-readable reason per drop.
func FilterByEvidence(cands []Candidate, sc Scorecard, retros []RetroNote) (kept []Candidate, dropped []string) {
	retroIDs := map[int64]bool{}
	for _, r := range retros {
		retroIDs[r.ID] = true
	}
	for _, c := range cands {
		reason := ""
		switch {
		case c.Kind != "routing-preference" && c.Kind != "user-preference":
			reason = fmt.Sprintf("kind %q is not learnable", c.Kind)
		case strings.TrimSpace(c.Text) == "":
			reason = "empty text"
		case c.Confidence <= 0 || c.Confidence > 1:
			reason = fmt.Sprintf("confidence %.2f out of (0,1]", c.Confidence)
		case strings.HasPrefix(c.Evidence, "scorecard:"):
			parts := strings.SplitN(strings.TrimPrefix(c.Evidence, "scorecard:"), "/", 2)
			if len(parts) != 2 || !sc.HasCell(parts[0], parts[1]) {
				reason = fmt.Sprintf("citation %q matches no scorecard line", c.Evidence)
			}
		case strings.HasPrefix(c.Evidence, "retro:"):
			id, err := strconv.ParseInt(strings.TrimPrefix(c.Evidence, "retro:"), 10, 64)
			if err != nil || !retroIDs[id] {
				reason = fmt.Sprintf("citation %q matches no retrospective", c.Evidence)
			}
		default:
			reason = fmt.Sprintf("citation %q is neither scorecard:<cli>/<signal> nor retro:<id>", c.Evidence)
		}
		if reason != "" {
			dropped = append(dropped, fmt.Sprintf("%q — %s", c.Text, reason))
			continue
		}
		if len(kept) >= maxCandidates {
			dropped = append(dropped, fmt.Sprintf("%q — over the %d-candidate cap", c.Text, maxCandidates))
			continue
		}
		kept = append(kept, c)
	}
	return kept, dropped
}
