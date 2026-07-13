package onboard

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/mattn/go-isatty"
)

const (
	ollamaURL       = "https://ollama.com/download"
	agyInstallerURL = "https://antigravity.google/cli/install.sh"
)

type detection struct {
	installed map[Subscription]bool
	selected  []Subscription
}

// SeedRouting runs the terminal wizard when it is safe to interact, otherwise
// it atomically writes the unchanged default routing configuration.
func SeedRouting(path, defaultContent string) error {
	if !wizardAllowed() {
		return atomicWrite(path, []byte(defaultContent), 0o644)
	}

	detected := detectChannels()
	selected := append([]Subscription(nil), detected.selected...)
	if err := huh.NewForm(huh.NewGroup(
		huh.NewMultiSelect[Subscription]().
			Title("Which subscriptions do you have?").
			Description("Detected local tools are selected. Space toggles; Enter continues.").
			Options(
				huh.NewOption("Claude Pro / Max", Claude),
				huh.NewOption("ChatGPT Plus / Pro (Codex)", Codex),
				huh.NewOption("Google / Antigravity", Agy),
				huh.NewOption("Local Ollama", Ollama),
			).
			Value(&selected),
	)).Run(); err != nil {
		return fmt.Errorf("run first-run wizard: %w", err)
	}

	active := make(Subscriptions)
	for _, subscription := range selected {
		if detected.installed[subscription] {
			active[subscription] = true
			continue
		}
		if subscription == Ollama {
			fmt.Fprintf(os.Stderr, "Ollama is not reachable. Install or start it from %s\n", ollamaURL)
			continue
		}
		if confirmInstall(subscription) && runOfficialInstaller(subscription) {
			active[subscription] = true
		}
	}

	tailored, err := TailorRouting(defaultContent, active)
	if err != nil {
		return err
	}
	if err := atomicWrite(path, []byte(tailored), 0o644); err != nil {
		return fmt.Errorf("write tailored routing config: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Routing saved to %s. Next, run `styx doctor`.\n", path)
	return nil
}

func wizardAllowed() bool {
	if _, ok := os.LookupEnv("CI"); ok {
		return false
	}
	if _, ok := os.LookupEnv("STYX_NO_WIZARD"); ok {
		return false
	}
	return isTerminal(os.Stdin) && isTerminal(os.Stdout) && isTerminal(os.Stderr)
}

func isTerminal(file *os.File) bool {
	fd := file.Fd()
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}

func detectChannels() detection {
	installed := make(map[Subscription]bool, 4)
	var selected []Subscription
	for _, subscription := range []Subscription{Claude, Codex, Agy} {
		_, err := exec.LookPath(string(subscription))
		installed[subscription] = err == nil
		if err == nil {
			selected = append(selected, subscription)
		}
	}
	installed[Ollama] = ollamaReachable()
	if installed[Ollama] {
		selected = append(selected, Ollama)
	}
	return detection{installed: installed, selected: selected}
}

func ollamaReachable() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost:11434/api/tags", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode < http.StatusBadRequest
}

func confirmInstall(subscription Subscription) bool {
	install := false
	title := fmt.Sprintf("Install %s now?", subscription)
	if err := huh.NewConfirm().
		Title(title).
		Description(installerDescription(subscription)).
		Affirmative("Install").
		Negative("Skip").
		Value(&install).
		Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Skipping %s installation: %v\n", subscription, err)
		return false
	}
	return install
}

func installerDescription(subscription Subscription) string {
	switch subscription {
	case Claude:
		return "Runs: npm install -g @anthropic-ai/claude-code"
	case Codex:
		return "Runs: npm install -g @openai/codex"
	case Agy:
		return "Downloads and runs the official Antigravity installer"
	default:
		return ""
	}
}

func runOfficialInstaller(subscription Subscription) bool {
	var err error
	switch subscription {
	case Claude:
		err = runCommand(5*time.Minute, "npm", "install", "-g", "@anthropic-ai/claude-code")
	case Codex:
		err = runCommand(5*time.Minute, "npm", "install", "-g", "@openai/codex")
	case Agy:
		err = runAgyInstaller()
	default:
		return false
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s installation failed: %v\n", subscription, err)
		return false
	}
	fmt.Fprintf(os.Stderr, "%s installed.\n", subscription)
	return true
}

func runAgyInstaller() error {
	tmp, err := os.CreateTemp("", "styx-agy-install-*.sh")
	if err != nil {
		return err
	}
	path := tmp.Name()
	if err := tmp.Close(); err != nil {
		os.Remove(path)
		return err
	}
	defer os.Remove(path)
	if err := runCommand(2*time.Minute, "curl", "-fsSL", "-o", path, agyInstallerURL); err != nil {
		return fmt.Errorf("download installer: %w", err)
	}
	if err := runCommand(10*time.Minute, "bash", path); err != nil {
		return fmt.Errorf("run installer: %w", err)
	}
	return nil
}

func runCommand(timeout time.Duration, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("%s timed out after %s: %w", name, timeout, ctx.Err())
		}
		return err
	}
	return nil
}

func atomicWrite(path string, content []byte, mode os.FileMode) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".routing.toml.tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		tmp.Close()
		if err != nil {
			os.Remove(tmpPath)
		}
	}()
	if err = tmp.Chmod(mode); err != nil {
		return err
	}
	if _, err = tmp.Write(content); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
