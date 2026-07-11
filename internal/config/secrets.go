package config

import (
	"errors"
	"fmt"
	"regexp"
)

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

// Secret reads a secret from the platform secret store.
// Returns ("", ErrSecretNotFound) if the secret is not stored.
func Secret(name string) (string, error) {
	if err := validateSecretName(name); err != nil {
		return "", err
	}
	return platformSecret(name)
}

// SetSecret writes a secret to the platform secret store (creates or updates).
func SetSecret(name, value string) error {
	if err := validateSecretName(name); err != nil {
		return err
	}
	return platformSetSecret(name, value)
}

// DeleteSecret removes a secret from the platform secret store.
// Returns nil if not present.
func DeleteSecret(name string) error {
	if err := validateSecretName(name); err != nil {
		return err
	}
	return platformDeleteSecret(name)
}

// SecretStoreName names this platform's secret store ("macOS Keychain",
// "Windows Credential Manager"), or "" when the platform has none.
func SecretStoreName() string { return secretStore }

// ErrSecretNotFound is returned when a stored secret does not exist.
var ErrSecretNotFound = errors.New("secret not found in the platform secret store")

// ErrSecretsUnsupported is returned by every secrets call on platforms with
// no supported secret store (anything other than macOS and Windows). Secrets
// are never written to disk or env as a fallback.
var ErrSecretsUnsupported = errors.New("no secret store on this platform (supported: macOS Keychain, Windows Credential Manager)")
