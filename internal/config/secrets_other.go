//go:build !darwin && !windows

package config

import "fmt"

const secretStore = ""

func platformSecret(name string) (string, error) {
	return "", fmt.Errorf("read secret %q: %w", name, ErrSecretsUnsupported)
}

func platformSetSecret(name, _ string) error {
	return fmt.Errorf("write secret %q: %w", name, ErrSecretsUnsupported)
}

func platformDeleteSecret(name string) error {
	return fmt.Errorf("delete secret %q: %w", name, ErrSecretsUnsupported)
}
