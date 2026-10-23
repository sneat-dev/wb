package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/sneat-dev/wb/internal/layout"
)

func TestLayoutRootFactoryReadsParsedProjectsRoot(t *testing.T) {
	t.Parallel()
	inv := &invocation{projectsRoot: "before-parsing"}
	cmd := newLayoutCmd(inv)
	cmd.PersistentFlags().StringVar(&inv.projectsRoot, "projects-root", inv.projectsRoot, "")
	root := t.TempDir()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"audit", "--projects-root", root, "--format=json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var report layout.Report
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.ProjectsRoot != root {
		t.Fatalf("root=%q want %q", report.ProjectsRoot, root)
	}
}
func TestLayoutRootFactoryKeepsFindingsExitIdentity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	initOriginRepository(t, root+"/stray", "acme/app")
	cmd := newLayoutCmd(&invocation{projectsRoot: root})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"audit"})
	err := cmd.Execute()
	var coded *exitError
	if !errors.As(err, &coded) || coded.code != exitFindings || exitCodeFor(err, false) != exitFindings {
		t.Fatalf("error=%v exit=%d", err, exitCodeFor(err, false))
	}
}
