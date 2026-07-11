package config

import (
	"runtime"
	"strings"
	"testing"
)

func TestSecretName_Validation(t *testing.T) {
	cases := []struct {
		in      string
		wantErr bool
	}{
		{"gemini_api_key", false},
		{"openai-token", false},
		{"a", false},
		{"", true},
		{"has spaces", true},
		{"has;semicolon", true},
		{strings.Repeat("a", 256), true},
	}
	for _, c := range cases {
		err := validateSecretName(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("validateSecretName(%q): got err=%v, wantErr=%v", c.in, err, c.wantErr)
		}
	}
}

func TestSecretStoreName_MatchesPlatform(t *testing.T) {
	got := SecretStoreName()
	switch runtime.GOOS {
	case "darwin":
		if got != "macOS Keychain" {
			t.Errorf("darwin store = %q, want macOS Keychain", got)
		}
	case "windows":
		if got != "Windows Credential Manager" {
			t.Errorf("windows store = %q, want Windows Credential Manager", got)
		}
	default:
		if got != "" {
			t.Errorf("unsupported platform store = %q, want empty", got)
		}
	}
}

func TestSecret_InvalidNameRejectedBeforeBackend(t *testing.T) {
	// Validation must run in the portable front on every platform.
	if _, err := Secret("has spaces"); err == nil {
		t.Error("Secret: invalid name must error")
	}
	if err := SetSecret("has spaces", "v"); err == nil {
		t.Error("SetSecret: invalid name must error")
	}
	if err := DeleteSecret("has spaces"); err == nil {
		t.Error("DeleteSecret: invalid name must error")
	}
}
