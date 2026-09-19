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
	previous := nonInteractive
	nonInteractive = false
	t.Cleanup(func() { nonInteractive = previous })
	var opened string
	command := newDashboardCmdWithDependencies(dashboardCommandDependencies{
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

func TestDashboardJSONDoesNotOpenBrowser(t *testing.T) {
	previous := nonInteractive
	nonInteractive = false
	t.Cleanup(func() { nonInteractive = previous })
	opened := false
	command := newDashboardCmdWithDependencies(dashboardCommandDependencies{
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
	previous := nonInteractive
	nonInteractive = false
	t.Cleanup(func() { nonInteractive = previous })
	var opened, root string
	command := newDashboardCmdWithDependencies(dashboardCommandDependencies{
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
	if root != projectsRoot || opened != "http://127.0.0.1:9000/" {
		t.Fatalf("root = %q, opened = %q", root, opened)
	}
}

// wb dashboard --local must not silently drop the item-9 warning an implicit
// Start returns when it found a live, healthy, supervised daemon under a
// different binary than this invocation's own (sneat-dev/wb#622 review
// round 3, item M1).
func TestDashboardLocalPrintsAProvenanceWarningToStderr(t *testing.T) {
	previous := nonInteractive
	nonInteractive = false
	t.Cleanup(func() { nonInteractive = previous })
	command := newDashboardCmdWithDependencies(dashboardCommandDependencies{
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
