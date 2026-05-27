package config

import (
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// keychainService is the macOS Keychain "service" name for all Styx secrets.
const keychainService = "styx"

var secretNameRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func validateSecretName(name string) error {
	if name == "" {
		return errors.New("secret name is empty")
	}
	if !secretNameRE.MatchString(name) {
		return fmt.Errorf("invalid secret name %q (allowed: A-Z a-z 0-9 _ -, length 1-128)", name)
	}
	return nil
}

// Secret reads a secret from the macOS Keychain.
// Returns ("", ErrSecretNotFound) if the secret is not stored.
func Secret(name string) (string, error) {
	if err := validateSecretName(name); err != nil {
		return "", err
	}
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

// SetSecret writes a secret to the macOS Keychain (creates or updates).
func SetSecret(name, value string) error {
	if err := validateSecretName(name); err != nil {
		return err
	}
	cmd := exec.Command("security", "add-generic-password",
		"-U", "-s", keychainService, "-a", name, "-w", value)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("write keychain secret %q: %w (output: %s)", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// DeleteSecret removes a secret from the Keychain. Returns nil if not present.
func DeleteSecret(name string) error {
	if err := validateSecretName(name); err != nil {
		return err
	}
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

// ErrSecretNotFound is returned when a Keychain item does not exist.
var ErrSecretNotFound = errors.New("secret not found in Keychain")
