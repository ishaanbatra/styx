//go:build windows

package config

import (
	"errors"
	"fmt"

	"github.com/danieljoos/wincred"
	"golang.org/x/sys/windows"
)

const secretStore = "Windows Credential Manager"

// credTarget namespaces styx secrets in the Credential Manager, mirroring
// the darwin Keychain service name.
func credTarget(name string) string { return "styx/" + name }

func platformSecret(name string) (string, error) {
	cred, err := wincred.GetGenericCredential(credTarget(name))
	if err != nil {
		if errors.Is(err, windows.ERROR_NOT_FOUND) {
			return "", ErrSecretNotFound
		}
		return "", fmt.Errorf("read credential %q: %w", name, err)
	}
	return string(cred.CredentialBlob), nil
}

func platformSetSecret(name, value string) error {
	cred := wincred.NewGenericCredential(credTarget(name))
	cred.CredentialBlob = []byte(value)
	if err := cred.Write(); err != nil {
		return fmt.Errorf("write credential %q: %w", name, err)
	}
	return nil
}

func platformDeleteSecret(name string) error {
	cred, err := wincred.GetGenericCredential(credTarget(name))
	if err != nil {
		if errors.Is(err, windows.ERROR_NOT_FOUND) {
			return nil
		}
		return fmt.Errorf("delete credential %q: %w", name, err)
	}
	if err := cred.Delete(); err != nil {
		return fmt.Errorf("delete credential %q: %w", name, err)
	}
	return nil
}
