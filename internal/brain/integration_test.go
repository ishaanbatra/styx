package brain

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// TestRoutingAccuracy runs the real local ollama brain against the labeled
// fixture set. Gated behind STYX_BRAIN_IT=1 because it needs ollama running
// with the brain model pulled. This is the regression net for "is the 4b
// brain good enough."
func TestRoutingAccuracy(t *testing.T) {
	if os.Getenv("STYX_BRAIN_IT") != "1" {
		t.Skip("set STYX_BRAIN_IT=1 (and run ollama) to run the brain integration suite")
	}
	raw, err := os.ReadFile("../../testdata/brain/utterances.json")
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	var cases []struct {
		Utterance    string `json:"utterance"`
		WantAction   string `json:"want_action"`
		WantThread   string `json:"want_thread"`
		WantRisk     string `json:"want_risk"`
		WantPipeline string `json:"want_pipeline"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatalf("parse fixtures: %v", err)
	}

	model := os.Getenv("STYX_BRAIN_MODEL")
	if model == "" {
		model = "qwen2.5-coder:7b"
	}
	b := &Ollama{BaseURL: "http://localhost:11434", Model: model}

	correct := 0        // routing AND (risk, when the fixture labels it)
	routingCorrect := 0 // routing only (action/thread/pipeline) — comparable across history
	riskTotal, riskCorrect := 0, 0
	for _, c := range cases {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		a, err := b.Decide(ctx, Turn{Utterance: c.Utterance})
		cancel()
		if err != nil {
			t.Logf("MISS (error) %q: %v", c.Utterance, err)
			continue
		}
		routeOK := string(a.Action) == c.WantAction
		if routeOK && c.WantThread != "" {
			routeOK = len(a.Dispatches) > 0 && a.Dispatches[0].Thread == c.WantThread
		}
		if routeOK && c.WantPipeline != "" {
			routeOK = a.Pipeline == c.WantPipeline
		}
		if routeOK {
			routingCorrect++
		}
		// Risk is scored only on labeled fixtures. An empty/omitted risk counts
		// as "edit" (EffectiveRisk's default), mirroring eval/promptfoo/gate.js.
		riskOK := true
		if c.WantRisk != "" {
			riskTotal++
			gotRisk := "edit"
			if len(a.Dispatches) > 0 && a.Dispatches[0].Risk != "" {
				gotRisk = string(a.Dispatches[0].Risk)
			}
			riskOK = gotRisk == c.WantRisk
			if riskOK {
				riskCorrect++
			}
		}
		if routeOK && riskOK {
			correct++
		} else {
			t.Logf("MISS %q: got action=%s dispatches=%+v pipeline=%s", c.Utterance, a.Action, a.Dispatches, a.Pipeline)
		}
	}
	n := len(cases)
	t.Logf("routing accuracy: %d/%d = %.0f%%", routingCorrect, n, float64(routingCorrect)/float64(n)*100)
	if riskTotal > 0 {
		t.Logf("risk accuracy: %d/%d = %.0f%%", riskCorrect, riskTotal, float64(riskCorrect)/float64(riskTotal)*100)
	}
	gate := float64(correct) / float64(n)
	t.Logf("gate accuracy (routing+risk): %d/%d = %.0f%%", correct, n, gate*100)
	if gate < 0.8 {
		t.Errorf("gate accuracy %.0f%% below 80%% threshold - the brain (or the prompt) needs work", gate*100)
	}
}
