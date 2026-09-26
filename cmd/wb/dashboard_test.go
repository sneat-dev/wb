package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestDashboardOpensHostedURL(t *testing.T) {
	var opened string
	command := newDashboardCmdWithDependencies(&invocation{}, dashboardCommandDependencies{
		open: func(target string) error { opened = target; return nil },
		localURL: func(context.Context, string) (string, string, error) {
			return "", "", errors.New("unexpected local lookup")
		},
	})
	var output bytes.Buffer
	command.SetOut(&output)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if opened != hostedDashboardURL || !strings.Contains(output.String(), hostedDashboardURL) {
		t.Fatalf("opened = %q, output = %q", opened, output.String())
	}
}

// TestDashboardNonInteractiveDoesNotOpenBrowser proves that an invocation
// carrying nonInteractive: true (as --non-interactive sets it) stops "wb
// dashboard" from opening a browser even in text format, where an
// interactive invocation would: a mutation that swapped inv for a fresh
// &invocation{} (sneat-dev/wb#733 PR-3 review, finding N1) would make this
// test's open dependency fire, which it must not.
func TestDashboardNonInteractiveDoesNotOpenBrowser(t *testing.T) {
	opened := false
	command := newDashboardCmdWithDependencies(&invocation{nonInteractive: true}, dashboardCommandDependencies{
		open: func(string) error { opened = true; return nil },
		localURL: func(context.Context, string) (string, string, error) {
			return "", "", errors.New("unexpected local lookup")
		},
	})
	var output bytes.Buffer
	command.SetOut(&output)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if opened || !strings.Contains(output.String(), hostedDashboardURL) {
		t.Fatalf("opened = %t, output = %q, want the URL printed but not opened", opened, output.String())
	}
}

func TestDashboardJSONDoesNotOpenBrowser(t *testing.T) {
	opened := false
	command := newDashboardCmdWithDependencies(&invocation{}, dashboardCommandDependencies{
		open: func(string) error { opened = true; return nil },
		localURL: func(context.Context, string) (string, string, error) {
			return "", "", errors.New("unexpected local lookup")
		},
	})
	command.SetArgs([]string{"--format=json"})
	var output bytes.Buffer
	command.SetOut(&output)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	var result dashboardOpenResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if opened || result.Opened || result.Scope != "hosted" || result.URL != hostedDashboardURL {
		t.Fatalf("opened = %t, result = %+v", opened, result)
	}
}

func TestDashboardLocalStartsDaemonAndOpensItsURL(t *testing.T) {
	wantRoot := t.TempDir()
	var opened, root string
	command := newDashboardCmdWithDependencies(&invocation{projectsRoot: wantRoot}, dashboardCommandDependencies{
		open: func(target string) error { opened = target; return nil },
		localURL: func(_ context.Context, projectsRoot string) (string, string, error) {
			root = projectsRoot
			return "http://127.0.0.1:9000/", "", nil
		},
	})
	command.SetArgs([]string{"--local"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if root != wantRoot || opened != "http://127.0.0.1:9000/" {
		t.Fatalf("root = %q, opened = %q, want root = %q", root, opened, wantRoot)
	}
}

// wb dashboard --local must not silently drop the item-9 warning an implicit
// Start returns when it found a live, healthy, supervised daemon under a
// different binary than this invocation's own (sneat-dev/wb#622 review
// round 3, item M1).
func TestDashboardLocalPrintsAProvenanceWarningToStderr(t *testing.T) {
	command := newDashboardCmdWithDependencies(&invocation{}, dashboardCommandDependencies{
		open: func(string) error { return nil },
		localURL: func(context.Context, string) (string, string, error) {
			return "http://127.0.0.1:9000/", "the running supervised daemon's executable does not match this invocation's own binary", nil
		},
	})
	command.SetArgs([]string{"--local"})
	var stderr bytes.Buffer
	command.SetErr(&stderr)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "does not match this invocation's own binary") {
		t.Fatalf("stderr = %q, want the warning printed", stderr.String())
	}
}

func TestDashboardMetricsAndCoverageFlags(t *testing.T) {
	var opened string
	command := newDashboardCmdWithDependencies(&invocation{}, dashboardCommandDependencies{
		open: func(target string) error { opened = target; return nil },
		localURL: func(_ context.Context, _ string) (string, string, error) {
			return "http://127.0.0.1:9000/", "", nil
		},
	})
	command.SetArgs([]string{"--metrics"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if opened != "http://127.0.0.1:9000/metrics" {
		t.Fatalf("opened = %q, want http://127.0.0.1:9000/metrics", opened)
	}

	command = newDashboardCmdWithDependencies(&invocation{}, dashboardCommandDependencies{
		open: func(target string) error { opened = target; return nil },
		localURL: func(_ context.Context, _ string) (string, string, error) {
			return "http://127.0.0.1:9000/", "", nil
		},
	})
	command.SetArgs([]string{"--coverage"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if opened != "http://127.0.0.1:9000/coverage" {
		t.Fatalf("opened = %q, want http://127.0.0.1:9000/coverage", opened)
	}
}

func TestNewDashboardCmd(t *testing.T) {
	cmd := newDashboardCmd(&invocation{})
	if cmd == nil || cmd.Use != "dashboard" {
		t.Fatalf("expected dashboard command, got %v", cmd)
	}
}
