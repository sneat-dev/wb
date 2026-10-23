package integration

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/cli/cmdwait"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/waitregistry"
	"github.com/sneat-dev/wb/internal/waitrun"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/spf13/cobra"
)

func TestWaitListShowsLiveAndStaleRecordsThenPrunesOnlyStale(t *testing.T) {
	t.Parallel()
	projects := t.TempDir()
	home, err := wbhome.EnsureRoot(projects)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, time.September, 27, 12, 0, 0, 0, time.UTC)
	for _, record := range []waitregistry.Record{
		{ID: "live", PID: os.Getpid(), Kind: "pr", Targets: []string{"acme/app#9"}, Until: "checks-settled", WBSessionID: "session-live", StartedAt: started},
		{ID: "dead", PID: 1 << 30, Kind: "agent", Targets: []string{"job-7"}, Until: "terminal", WBSessionID: "session-dead", StartedAt: started},
	} {
		if _, err := waitregistry.Register(home, record); err != nil {
			t.Fatal(err)
		}
	}
	cmd := listCommand(projects)
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"waiting pr acme/app#9 until checks-settled for session-live", "stale agent job-7 until terminal for session-dead", started.Format(time.RFC3339)} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("list output %q lacks %q", out.String(), want)
		}
	}
	prune := listCommand(projects)
	var pruned bytes.Buffer
	prune.SetOut(&pruned)
	prune.SetArgs([]string{"--prune"})
	if err := prune.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pruned.String(), "pruned 1 stale wait(s)") {
		t.Fatalf("prune output = %q", pruned.String())
	}
	records, err := waitregistry.List(home, waitregistry.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].ID != "live" {
		t.Fatalf("records after prune = %+v", records)
	}
}

// These default-suite tests exercise real writable metadata and native PID liveness.
func listCommand(projects string) *cobra.Command {
	registry := waitrun.DefaultRegistry()
	return cmdwait.NewList(shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: projects} }}, cmdwait.Dependencies{Inspect: registry.Inspect, Discovery: func(*cobra.Command, string) {}})
}
func TestWaitListTellsAQuietSessionApartFromAStoppedOne(t *testing.T) {
	t.Parallel()
	cmd := listCommand(t.TempDir())
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil || !strings.Contains(out.String(), "no outstanding waits") {
		t.Fatal(out.String(), err)
	}
}
func TestWaitListReportsAStaleWaiterInJSON(t *testing.T) {
	t.Parallel()
	projects := t.TempDir()
	home, err := wbhome.EnsureRoot(projects)
	if err != nil {
		t.Fatal(err)
	}
	release, err := waitregistry.Register(home, waitregistry.Record{ID: "x", PID: 1 << 30, Kind: "pr", Targets: []string{"acme/app#9"}, Until: "checks-settled", StartedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	cmd := listCommand(projects)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Waits []waitregistry.Record `json:"waits"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil || len(payload.Waits) != 1 || !payload.Waits[0].Stale {
		t.Fatal(out.String(), payload, err)
	}
}
