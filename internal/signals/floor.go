package signals

import "strings"

// Tier is a coarse capability rank for a routing target. Higher is more capable
// (and generally more expensive). It is the vocabulary the router's capability
// floor is expressed in.
type Tier int

const (
	TierLocal  Tier = iota // ollama / local models — weakest
	TierHaiku              // small cloud tier (claude haiku-class)
	TierSonnet             // general capable cloud (sonnet / codex / agy)
	TierOpus               // top cloud tier (opus / fable)
)

// String renders the tier as its lowercase keyword, matching the [tiers] vocab
// in routing.toml (plus "local" for ollama).
func (t Tier) String() string {
	switch t {
	case TierOpus:
		return "opus"
	case TierSonnet:
		return "sonnet"
	case TierHaiku:
		return "haiku"
	default:
		return "local"
	}
}

// TierOf ranks a routing target by its channel and model string. The mapping is
// hand-curated (styx routing is a transparent table, not an LLM) and biased
// toward inclusion: an unknown cloud channel is treated as sonnet-class so it is
// never wrongly excluded from a capable-tier floor. Only ollama is below-floor.
func TierOf(channel, model string) Tier {
	switch channel {
	case "ollama":
		return TierLocal
	case "claude":
		m := strings.ToLower(model)
		switch {
		case strings.Contains(m, "opus"), strings.Contains(m, "fable"):
			return TierOpus
		case strings.Contains(m, "haiku"):
			return TierHaiku
		default: // "sonnet", "interactive", or unspecified claude
			return TierSonnet
		}
	default: // codex, agy, gemini, or any other cloud channel
		return TierSonnet
	}
}

// signalFloor maps a classification signal to the minimum capability tier a task
// carrying that signal requires. Signals with no entry impose no floor. Kept
// beside the signal definitions so the map cannot drift from what Extract emits.
var signalFloor = map[string]Tier{
	SigComplex: TierSonnet,
	SigDeep:    TierSonnet,
}

// Floor returns the highest minimum tier required by any of the given signals,
// or TierLocal when no signal imposes a floor.
func Floor(sigs []string) Tier {
	floor := TierLocal
	for _, s := range sigs {
		if t, ok := signalFloor[s]; ok && t > floor {
			floor = t
		}
	}
	return floor
}
