//go:build unix

package channel

import (
	"errors"
	"os/exec"
	"testing"
)

func TestKilledBySignal(t *testing.T) {
	cases := []struct {
		name string
		run  func() error
		want bool
	}{
		{"sigkill", func() error {
			cmd := exec.Command("sleep", "30")
			if err := cmd.Start(); err != nil {
				return err
			}
			_ = cmd.Process.Kill() // sends SIGKILL
			return cmd.Wait()
		}, true},
		{"plain nonzero exit", func() error {
			return exec.Command("false").Run()
		}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.run()
			var ee *exec.ExitError
			if !errors.As(err, &ee) {
				t.Fatalf("expected *exec.ExitError, got %v", err)
			}
			if got := KilledBySignal(ee); got != c.want {
				t.Errorf("KilledBySignal = %v, want %v", got, c.want)
			}
		})
	}
}
