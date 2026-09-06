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
		open:     func(target string) error { opened = target; return nil },
		localURL: func(context.Context, string) (string, error) { return "", errors.New("unexpected local lookup") },
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
		open:     func(string) error { opened = true; return nil },
		localURL: func(context.Context, string) (string, error) { return "", errors.New("unexpected local lookup") },
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
		localURL: func(_ context.Context, projectsRoot string) (string, error) {
			root = projectsRoot
			return "http://127.0.0.1:9000/", nil
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
