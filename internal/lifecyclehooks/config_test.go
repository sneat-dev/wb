package lifecyclehooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadReadsOnlyStrictHooksSectionFromWBConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wb.yaml")
	raw := `remote:
  provider: git
hooks:
  version: 1
  executors:
    code-index:
      run: /opt/tools/code-index
      args: [sync, .]
      cwd: repository
      mode: coalesced
      timeout: 2m
      failure: warn
  bindings:
    - on: [checkout-updated]
      match:
        repositories:
          include: [github.com/sneat-co/*]
          exclude: [github.com/sneat-co/legacy-*]
      execute: [code-index]
`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, found, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !found || len(cfg.Executors) != 1 || len(cfg.Bindings) != 1 {
		t.Fatalf("config = %+v, found=%t", cfg, found)
	}
	matching := Event{Name: EventCheckoutUpdated, Repository: "github.com/SNEAT-CO/WB"}
	if !cfg.Bindings[0].matches(matching) {
		t.Fatal("canonical repository should match case-insensitively")
	}
	matching.Repository = "github.com/sneat-co/legacy-api"
	if cfg.Bindings[0].matches(matching) {
		t.Fatal("exclude must win")
	}
}

func TestLoadRejectsUnknownLifecycleHookField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wb.yaml")
	raw := `hooks:
  version: 1
  surprise: true
  executors: {}
  bindings: []
`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(path); err == nil || !strings.Contains(err.Error(), "surprise") {
		t.Fatalf("error = %v, want unknown field", err)
	}
}

func TestLoadWithoutHooksIsNotConfigured(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(path, []byte("remote:\n  provider: git\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, found, err := Load(path)
	if err != nil || found {
		t.Fatalf("found=%t err=%v", found, err)
	}
}
