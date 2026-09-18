package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/buildinfo"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// AC: cli-install#req:version-json-side-effect-free ("MUST NOT perform
// network I/O, write or create files, start daemons, run update checks, or
// emit telemetry") — review B1: `wb version` (with or without --json) MUST
// NOT record a heartbeat or an invoked-command on the worktree the caller
// happens to be standing in. A fleet CLI's install/upgrade status probe
// execs `<host> version --json` from the CALLER's own cwd, which may itself
// be a WB worktree; recording activity there on every probe would make a
// lane a prober merely glanced at look busy.
//
// The fixture below mirrors internal/worktrees' own heartbeat contract
// (TestWTCoreCovHeartbeatScopedToCurrentDirectory): a worktree is anything
// with a `.wb/local/manifest.yaml` file above the current directory: only
// the file's presence is checked, not its contents, so a placeholder is
// enough — no real Git worktree or EnsureManifest call is required.
func TestVersionCommand_DoesNotTouchWorktreeHeartbeatOrInvokedCommand(t *testing.T) {
	dir := t.TempDir()
	journalDir := filepath.Join(dir, ".wb", "local")
	if err := os.MkdirAll(journalDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(journalDir, "manifest.yaml"), []byte("version: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Seed a known heartbeat and invoked-command first, so "unchanged" is a
	// real assertion (equal to a specific prior value), not merely "still
	// zero" — which an untouched fixture would also report even if the fix
	// silently regressed to a no-op for every command.
	worktrees.TouchHeartbeat(dir, "prior command")
	worktrees.SetInvokedCommand("prior command")
	heartbeatPath := filepath.Join(journalDir, "heartbeat.json")
	before, err := os.ReadFile(heartbeatPath)
	if err != nil {
		t.Fatalf("read seeded heartbeat: %v", err)
	}

	t.Chdir(dir)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"version", "--json"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("wb version --json exit = %d, stderr = %s", code, stderr.String())
	}

	after, err := os.ReadFile(heartbeatPath)
	if err != nil {
		t.Fatalf("read heartbeat after version --json: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("heartbeat file changed after `wb version --json`:\nbefore: %s\nafter:  %s", before, after)
	}
	if got := worktrees.InvokedCommand(); got != "prior command" {
		t.Errorf("InvokedCommand() = %q after `wb version --json`, want the seeded %q unchanged", got, "prior command")
	}

	// Plain `wb version` (no --json) gets the identical exemption — the
	// review asked to prefer skipping every version invocation, not only
	// --json.
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"version"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("wb version exit = %d, stderr = %s", code, stderr.String())
	}
	after, err = os.ReadFile(heartbeatPath)
	if err != nil {
		t.Fatalf("read heartbeat after version: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("heartbeat file changed after plain `wb version`:\nbefore: %s\nafter:  %s", before, after)
	}
}

// AC: cli-install#req:version-json-contract — review M2: every catalog CLI's
// `version --json` MUST print exactly the four fleet-wide contract keys
// (name, version, commit, date, date_source), never omitted even when
// empty, and `name` MUST equal the catalog id ("wb").
func TestVersionJSON_ContractKeys(t *testing.T) {
	buildinfo.Set("1.2.3")
	t.Cleanup(func() { buildinfo.Set("") })

	var stdout, stderr bytes.Buffer
	if code := run([]string{"version", "--json"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("wb version --json exit = %d, stderr = %s", code, stderr.String())
	}

	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("decode %s: %v", stdout.String(), err)
	}
	for _, key := range []string{"name", "version", "commit", "date", "date_source"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("version --json is missing contract key %q (cli-install#req:version-json-contract): %s", key, stdout.String())
		}
	}
	var name, version string
	_ = json.Unmarshal(decoded["name"], &name)
	_ = json.Unmarshal(decoded["version"], &version)
	if name != "wb" {
		t.Errorf("name = %q, want %q", name, "wb")
	}
	if version != "1.2.3" {
		t.Errorf("version = %q, want the overridden 1.2.3 unchanged", version)
	}
}
