package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/waitregistry"
	"github.com/sneat-dev/wb/internal/wbhome"
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
	cmd := newWaitListCmd(&invocation{projectsRoot: projects})
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
	prune := newWaitListCmd(&invocation{projectsRoot: projects})
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
