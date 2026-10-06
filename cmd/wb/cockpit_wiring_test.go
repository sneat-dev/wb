package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/sneat-dev/wb/internal/cockpitrun"
	"github.com/sneat-dev/wb/internal/daemonruntime"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCockpitRootHostedConfigurationUsesRealValidation(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(path, []byte("cockpit:\n  hosted_url: https://cockpit.example.test/wb/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	url, err := loadHostedCockpitURL(path)
	if err != nil || url != "https://cockpit.example.test/wb/" {
		t.Fatalf("url=%q err=%v", url, err)
	}
	if err := os.WriteFile(path, []byte("cockpit:\n  hosted_url: not a url\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if url, err := loadHostedCockpitURL(path); err == nil || !strings.Contains(err.Error(), "hosted_url") {
		t.Fatalf("url=%q err=%v", url, err)
	}
}

func TestCockpitRootDefaultLocalRefusesWithoutStartingDaemon(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	deps := newCockpitDependencies()
	_, err := deps.Local(context.Background(), cockpitrun.LocalRequest{Root: root, Listen: "not-an-address"})
	var coded *exitError
	if !errors.As(err, &coded) || coded.code != exitUsage {
		t.Fatalf("actual default refusal=%v", err)
	}
	// NewController resolves its store before Start acquires the lifecycle lock;
	// Start validates the address only after that bookkeeping. Refusal must leave
	// no lifecycle record or socket, and release its temporary process ownership.
	location, err := daemonruntime.ResolveLocation(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{location.StatePath, location.SocketPath} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("refusal left daemon state or endpoint at %s: %v", path, err)
		}
	}
	owner, err := os.ReadFile(filepath.Join(location.RuntimeDir, "daemon.lifecycle.owner"))
	if err != nil || string(owner) != "pid=0\n" {
		t.Fatalf("refusal did not release lifecycle ownership: %q %v", owner, err)
	}
}
func TestCockpitRootHostedUsesActualConfigurationDefaults(t *testing.T) {
	t.Parallel()
	command := newCockpitCmd(testInvocation(t, t.TempDir()))
	command.SetArgs([]string{"--hosted", "--json"})
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	var result struct {
		URL   string `json:"url"`
		Scope string `json:"scope"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.URL == "" || result.Scope != "hosted" {
		t.Fatalf("stdout=%q err=%v", stdout.String(), err)
	}
}
