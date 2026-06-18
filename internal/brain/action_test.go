package brain

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestActionUnmarshal(t *testing.T) {
	raw := `{
		"action": "dispatch",
		"dispatches": [{
			"thread": "claude",
			"model": "sonnet",
			"message": "design the session loader refactor across the auth and storage layers",
			"cli_options": ["--add-dir", "../other"],
			"rationale": "ambiguous multi-file architecture work"
		}],
		"confidence": 0.9
	}`
	var a Action
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if a.Action != ActionDispatch {
		t.Errorf("action = %q", a.Action)
	}
	if len(a.Dispatches) != 1 || a.Dispatches[0].Thread != "claude" || a.Dispatches[0].Model != "sonnet" {
		t.Errorf("dispatches = %+v", a.Dispatches)
	}
	if a.Confidence != 0.9 {
		t.Errorf("confidence = %v", a.Confidence)
	}
}

func TestActionValid(t *testing.T) {
	tests := []struct {
		name string
		a    Action
		want bool
	}{
		{"reply ok", Action{Action: ActionReply, Reply: "hi", Confidence: 0.8}, true},
		{"reply missing text", Action{Action: ActionReply, Confidence: 0.8}, false},
		{"dispatch ok", Action{Action: ActionDispatch, Confidence: 0.7,
			Dispatches: []Dispatch{{Thread: "codex", Message: "do it"}}}, true},
		{"dispatch empty", Action{Action: ActionDispatch, Confidence: 0.7}, false},
		{"dispatch bad thread", Action{Action: ActionDispatch, Confidence: 0.7,
			Dispatches: []Dispatch{{Thread: "gpt9", Message: "x"}}}, false},
		{"pipeline ok", Action{Action: ActionPipeline, Pipeline: "research", Confidence: 0.9}, true},
		{"pipeline bad name", Action{Action: ActionPipeline, Pipeline: "destroy", Confidence: 0.9}, false},
		{"remember ok", Action{Action: ActionRemember, Remember: "fact", Confidence: 1}, true},
		{"remember empty", Action{Action: ActionRemember, Confidence: 1}, false},
		{"handoff ok", Action{Action: ActionHandoff, Confidence: 0.9}, true},
		{"escalate ok", Action{Action: ActionEscalate, Confidence: 0.2}, true},
		{"unknown action", Action{Action: "fly", Confidence: 1}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.a.Valid(); got != tt.want {
				t.Errorf("Valid() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestActionSchemaIsValidJSON(t *testing.T) {
	var v map[string]any
	if err := json.Unmarshal(ActionSchema, &v); err != nil {
		t.Fatalf("ActionSchema is not valid JSON: %v", err)
	}
	if v["type"] != "object" {
		t.Errorf("schema root type = %v", v["type"])
	}
}

func TestEffectiveRisk(t *testing.T) {
	tests := []struct {
		name string
		a    Action
		want RiskLevel
	}{
		{"default edit when unset", Action{Action: ActionDispatch, Dispatches: []Dispatch{{Thread: "claude", Message: "x"}}}, RiskEdit},
		{"explicit read", Action{Action: ActionDispatch, Dispatches: []Dispatch{{Thread: "claude", Message: "x", Risk: RiskRead}}}, RiskRead},
		{"max across dispatches", Action{Action: ActionParallelDispatch, Dispatches: []Dispatch{{Thread: "claude", Message: "a", Risk: RiskRead}, {Thread: "codex", Message: "b", Risk: RiskShip}}}, RiskShip},
		{"auto pipeline is ship", Action{Action: ActionPipeline, Pipeline: "auto"}, RiskShip},
		{"research pipeline defaults edit", Action{Action: ActionPipeline, Pipeline: "research"}, RiskEdit},
		{"action-level ship", Action{Action: ActionHandoff, Risk: RiskShip}, RiskShip},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.a.EffectiveRisk(); got != tt.want {
				t.Errorf("EffectiveRisk() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestActionValidRisk(t *testing.T) {
	good := Action{Action: ActionDispatch, Confidence: 0.7, Dispatches: []Dispatch{{Thread: "claude", Message: "x", Risk: RiskShip}}}
	if !good.Valid() {
		t.Error("valid risk rejected")
	}
	bad := Action{Action: ActionDispatch, Confidence: 0.7, Dispatches: []Dispatch{{Thread: "claude", Message: "x", Risk: "nuke"}}}
	if bad.Valid() {
		t.Error("invalid risk accepted")
	}
}

func TestRiskInSchema(t *testing.T) {
	if !bytes.Contains(ActionSchema, []byte(`"risk"`)) {
		t.Error("ActionSchema missing risk property")
	}
}

func TestDispatchProjectAndExtraRootsRoundTrip(t *testing.T) {
	raw := `{"action":"dispatch","confidence":0.9,"dispatches":[
		{"thread":"claude","message":"trace the upload","project":"ai-ta-teacher-ui","extra_roots":["/repos/ai-ta-backend"]}
	]}`
	var a Action
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		t.Fatal(err)
	}
	d := a.Dispatches[0]
	if d.Project != "ai-ta-teacher-ui" {
		t.Errorf("Project = %q", d.Project)
	}
	if len(d.ExtraRoots) != 1 || d.ExtraRoots[0] != "/repos/ai-ta-backend" {
		t.Errorf("ExtraRoots = %v", d.ExtraRoots)
	}
}

func TestProjectAndExtraRootsInSchema(t *testing.T) {
	for _, want := range []string{`"project"`, `"extra_roots"`} {
		if !bytes.Contains(ActionSchema, []byte(want)) {
			t.Errorf("ActionSchema missing %s", want)
		}
	}
}
