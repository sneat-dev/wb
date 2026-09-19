package herdr

import (
	"errors"
	"testing"
)

func TestValidatePromptText(t *testing.T) {
	cases := []struct {
		name    string
		text    string
		wantErr bool
	}{
		{name: "ordinary sentence", text: "checks failed: 1 required check (Lint)", wantErr: false},
		{name: "empty", text: "", wantErr: true},
		{name: "trailing newline", text: "hello\n", wantErr: true},
		{name: "embedded newline", text: "line one\nline two", wantErr: true},
		{name: "carriage return", text: "hello\r", wantErr: true},
		{name: "tab is a control character", text: "hello\tworld", wantErr: true},
		{name: "escape byte", text: "hello\x1bworld", wantErr: true},
		{name: "unicode text without control chars", text: "sneat-dev/wb#598 — checks failed", wantErr: false},
		{name: "line separator U+2028", text: "hello" + string(rune(0x2028)) + "world", wantErr: true},
		{name: "paragraph separator U+2029", text: "hello" + string(rune(0x2029)) + "world", wantErr: true},
		{name: "right-to-left override U+202E", text: "hello" + string(rune(0x202e)) + "world", wantErr: true},
		{name: "zero width space U+200B", text: "hello" + string(rune(0x200b)) + "world", wantErr: true},
		{name: "leading hyphen", text: "-x", wantErr: true},
		{name: "hyphen not leading", text: "checks-failed", wantErr: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePromptText(tc.text)
			if tc.wantErr && !errors.Is(err, ErrInvalidPromptText) {
				t.Fatalf("ValidatePromptText(%q) error = %v, want ErrInvalidPromptText", tc.text, err)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidatePromptText(%q) error = %v, want nil", tc.text, err)
			}
		})
	}
}

func TestValidateKeyName(t *testing.T) {
	cases := []struct {
		name    string
		key     string
		wantErr bool
	}{
		{name: "simple", key: "esc", wantErr: false},
		{name: "chord", key: "ctrl+c", wantErr: false},
		{name: "multi-chord", key: "ctrl+shift+p", wantErr: false},
		{name: "empty", key: "", wantErr: true},
		{name: "space", key: "not a key", wantErr: true},
		{name: "shell metacharacter", key: "esc; rm -rf /", wantErr: true},
		{name: "leading plus", key: "+esc", wantErr: true},
		{name: "trailing plus", key: "esc+", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateKeyName(tc.key)
			if tc.wantErr && !errors.Is(err, ErrInvalidKeyName) {
				t.Fatalf("ValidateKeyName(%q) error = %v, want ErrInvalidKeyName", tc.key, err)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateKeyName(%q) error = %v, want nil", tc.key, err)
			}
		})
	}
}
