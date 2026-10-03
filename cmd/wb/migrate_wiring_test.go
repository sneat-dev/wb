package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A real root journey retains hierarchical dispatch, legacy exit identity and
// the positive filesystem proof that parsed ProjectsRoot supplies GitHubDir.
func TestMigrateWiringUsesParsedProjectsRootAndLegacyUsageIdentity(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("WB_HOME", t.TempDir())
	spec := filepath.Join(t.TempDir(), "migration.hcl")
	if err := os.WriteFile(spec, []byte(`format = "https://sneat.dev/workbench/formats/migration/v1"
migration "wiring" {
 scope { languages = ["go"] }
 text_replace "go" {
 from = "OLDNAME"
 to = "NEWNAME"
 }
}`), 0600); err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "go.mod"), []byte("module github.com/acme/sample\n\ngo 1.26\n"), 0600); err != nil {
		t.Fatal(err)
	}
	root := newRootCmdFor(&invocation{})
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SilenceErrors = true
	root.SilenceUsage = true
	root.SetArgs([]string{"migrate", spec, source, "--hierarchical", "--projects-root", projects, "--format=json"})
	err := root.Execute()
	var coded *exitError
	if !errors.As(err, &coded) || coded.code != exitUsage {
		t.Fatalf("error=%v stderr=%s", err, errOut.String())
	}
	if _, err := os.Stat(filepath.Join(projects, ".wb")); err != nil {
		t.Fatalf("github-dir did not default to parsed projectsRoot: %v stderr=%s", err, errOut.String())
	}
	out.Reset()
	errOut.Reset()
	command := newMigrateCmd(&invocation{})
	command.SetOut(&out)
	command.SetErr(&errOut)
	command.SilenceErrors = true
	command.SilenceUsage = true
	command.SetArgs([]string{spec, source, "--hierarchical", "--cleanup", "--github-dir", projects})
	err = command.Execute()
	if !errors.As(err, &coded) || coded.code != exitUsage {
		t.Fatalf("hierarchical cleanup dispatch error=%v", err)
	}
	if !strings.Contains(errOut.String(), "--cleanup does not take a source root") || out.Len() != 0 {
		t.Fatalf("cleanup dispatch output=%q stderr=%q", out.String(), errOut.String())
	}
}
