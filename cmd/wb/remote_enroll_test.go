package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRemoteEnrollKeepsCredentialOutOfOutputAndConfig(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config", "wb.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("parallel: 3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	const token = "one-time-opaque-token"
	verified := false
	restarted := false
	deps := remoteEnrollDeps{
		configPath: func() string { return configPath },
		verify: func(_ context.Context, hubURL, machine, got string) error {
			verified = hubURL == defaultRemoteHubURL && machine == "studio-mac" && got == token
			return nil
		},
		restart: func(context.Context, string) error { restarted = true; return nil },
	}
	var stdout bytes.Buffer
	if err := runRemoteEnroll(context.Background(), deps, root, "studio-mac", defaultRemoteHubURL, "", true, true, true, strings.NewReader(token+"\n"), &stdout); err != nil {
		t.Fatal(err)
	}
	if !verified || !restarted {
		t.Fatalf("verified=%t restarted=%t", verified, restarted)
	}
	if strings.Contains(stdout.String(), token) {
		t.Fatal("credential leaked to stdout")
	}
	var result remoteEnrollResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	credential, err := os.ReadFile(result.TokenFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(credential)) != token {
		t.Fatal("private credential file does not contain the supplied token")
	}
	config, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(config), token) || !strings.Contains(string(config), "parallel: 3") {
		t.Fatalf("config leaked token or lost unrelated settings:\n%s", config)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(result.TokenFile)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("credential mode = %o, want 600", info.Mode().Perm())
		}
	}
}

func TestRemoteEnrollRequiresExplicitStdinAndDoesNotPersistUnverifiedToken(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "wb.yaml")
	deps := remoteEnrollDeps{
		configPath: func() string { return configPath },
		verify:     func(context.Context, string, string, string) error { return context.Canceled },
		restart:    func(context.Context, string) error { t.Fatal("restart called"); return nil },
	}
	if err := runRemoteEnroll(context.Background(), deps, root, "vm", defaultRemoteHubURL, "", false, false, false, strings.NewReader("secret"), ioDiscard{}); err == nil || !strings.Contains(err.Error(), "--token-stdin") {
		t.Fatalf("missing explicit stdin error = %v", err)
	}
	if err := runRemoteEnroll(context.Background(), deps, root, "vm", defaultRemoteHubURL, "", true, false, false, strings.NewReader("secret"), ioDiscard{}); err == nil || !strings.Contains(err.Error(), "verify hub credential") {
		t.Fatalf("verification error = %v", err)
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("config exists after failed verification: %v", err)
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(payload []byte) (int, error) { return len(payload), nil }
