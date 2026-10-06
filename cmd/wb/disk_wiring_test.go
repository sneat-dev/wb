package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/disk"
)

func TestDiskWiringUsesTheParsedProjectsRoot(t *testing.T) {
	t.Parallel()
	projects := t.TempDir()
	root := newRootCmdFor(&invocation{})
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs([]string{"disk", "--projects-root", projects, "--skip-sizes", "--format", "json"})
	err := root.Execute()
	// The real filesystem may report low headroom. Findings preserve root error
	// identity; this wiring check must not assume host capacity is sufficient.
	if err != nil {
		var coded *exitError
		if !errors.As(err, &coded) || coded.code != exitFindings {
			t.Fatalf("Execute error = %v; stderr = %s", err, errOut.String())
		}
	}
	var report disk.Report
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("JSON = %q: %v", out.String(), err)
	}
	if filepath.Clean(report.Filesystem.Path) != filepath.Clean(projects) {
		t.Fatalf("filesystem path = %q, want %q", report.Filesystem.Path, projects)
	}
	for _, category := range report.Categories {
		if category.ApparentBytes != 0 || category.UnsharedBytes != 0 {
			t.Fatalf("skip-sizes measured category %+v", category)
		}
	}
}

func TestDiskWiringPreservesRootUsageErrorIdentity(t *testing.T) {
	t.Parallel()
	command := newDiskCmd(&invocation{projectsRoot: t.TempDir()})
	command.SilenceErrors = true
	command.SilenceUsage = true
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&out)
	command.SetArgs([]string{"--format", "toml"})
	err := command.Execute()
	var coded *exitError
	if !errors.As(err, &coded) || coded.code != exitUsage {
		t.Fatalf("error = %#v", err)
	}
	if got := exitCodeFor(err, true); got != exitUsage {
		t.Fatalf("exit code = %d", got)
	}
	if out.Len() != 0 {
		t.Fatalf("unexpected report = %q", out.String())
	}
}
