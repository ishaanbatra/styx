package shipgate

import (
	"strings"
	"testing"
	"time"
)

func TestHandshake(t *testing.T) {
	g := New(ModeHandshake)

	t.Run("first call denied with token", func(t *testing.T) {
		r, err := g.Check("dispatch:codex", "")
		if err != nil {
			t.Fatalf("Check: %v", err)
		}
		if r.Allowed || r.Token == "" {
			t.Fatalf("want denied+token, got %+v", r)
		}
		if !strings.Contains(r.Message, r.Token) {
			t.Fatalf("message must carry the token for the brain to relay: %+v", r)
		}
	})

	t.Run("valid token allows once", func(t *testing.T) {
		r1, _ := g.Check("pipeline:auto", "")
		r2, err := g.Check("pipeline:auto", r1.Token)
		if err != nil || !r2.Allowed {
			t.Fatalf("want allowed, got %+v err=%v", r2, err)
		}
		r3, _ := g.Check("pipeline:auto", r1.Token) // single-use
		if r3.Allowed {
			t.Fatal("token reuse must be denied")
		}
	})

	t.Run("token bound to action", func(t *testing.T) {
		r1, _ := g.Check("dispatch:claude", "")
		r2, _ := g.Check("pipeline:auto", r1.Token)
		if r2.Allowed {
			t.Fatal("token for one action must not unlock another")
		}
	})

	t.Run("expired token denied", func(t *testing.T) {
		g := New(ModeHandshake)
		base := time.Now()
		g.now = func() time.Time { return base }
		r1, _ := g.Check("dispatch:codex", "")
		g.now = func() time.Time { return base.Add(11 * time.Minute) }
		r2, _ := g.Check("dispatch:codex", r1.Token)
		if r2.Allowed {
			t.Fatal("expired token must be denied")
		}
	})
}

func TestModes(t *testing.T) {
	t.Run("off always allows", func(t *testing.T) {
		r, err := New(ModeOff).Check("dispatch:codex", "")
		if err != nil || !r.Allowed {
			t.Fatalf("want allowed, got %+v err=%v", r, err)
		}
	})
	t.Run("tty uses injected confirm", func(t *testing.T) {
		g := New(ModeTTY)
		g.ConfirmTTY = func(action string) (bool, error) { return action == "yes:please", nil }
		if r, _ := g.Check("yes:please", ""); !r.Allowed {
			t.Fatal("tty-confirmed action must be allowed")
		}
		if r, _ := g.Check("no:way", ""); r.Allowed {
			t.Fatal("tty-refused action must be denied")
		}
	})
}
