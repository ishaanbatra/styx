//go:build darwin

package config

import (
	"fmt"
	"os/exec"
	"strings"
)

// keychainService is the macOS Keychain "service" name for all Styx secrets.
const keychainService = "styx"

const secretStore = "macOS Keychain"

func platformSecret(name string) (string, error) {
	out, err := exec.Command("security", "find-generic-password",
		"-s", keychainService, "-a", name, "-w").Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 44 {
			return "", ErrSecretNotFound
		}
		return "", fmt.Errorf("read keychain secret %q: %w", name, err)
	}
	return strings.TrimRight(string(out), "\n"), nil
}

func platformSetSecret(name, value string) error {
	cmd := exec.Command("security", "add-generic-password",
		"-U", "-s", keychainService, "-a", name, "-w", value)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("write keychain secret %q: %w (output: %s)", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func platformDeleteSecret(name string) error {
	cmd := exec.Command("security", "delete-generic-password",
		"-s", keychainService, "-a", name)
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 44 {
			return nil
		}
		return fmt.Errorf("delete keychain secret %q: %w", name, err)
	}
	return nil
}
