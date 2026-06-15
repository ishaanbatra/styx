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
		WantPipeline string `json:"want_pipeline"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatalf("parse fixtures: %v", err)
	}

	model := os.Getenv("STYX_BRAIN_MODEL")
	if model == "" {
		model = "llama3.2:3b"
	}
	b := &Ollama{BaseURL: "http://localhost:11434", Model: model}

	correct := 0
	for _, c := range cases {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		a, err := b.Decide(ctx, Turn{Utterance: c.Utterance})
		cancel()
		if err != nil {
			t.Logf("MISS (error) %q: %v", c.Utterance, err)
			continue
		}
		ok := string(a.Action) == c.WantAction
		if ok && c.WantThread != "" {
			ok = len(a.Dispatches) > 0 && a.Dispatches[0].Thread == c.WantThread
		}
		if ok && c.WantPipeline != "" {
			ok = a.Pipeline == c.WantPipeline
		}
		if ok {
			correct++
		} else {
			t.Logf("MISS %q: got action=%s dispatches=%+v pipeline=%s", c.Utterance, a.Action, a.Dispatches, a.Pipeline)
		}
	}
	accuracy := float64(correct) / float64(len(cases))
	t.Logf("routing accuracy: %d/%d = %.0f%%", correct, len(cases), accuracy*100)
	if accuracy < 0.8 {
		t.Errorf("routing accuracy %.0f%% below 80%% threshold - the 4b brain (or the prompt) needs work", accuracy*100)
	}
}
