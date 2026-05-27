package config

import (
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
