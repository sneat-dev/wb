package deps

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGoAdapterUsesGoToolingForExactExistingRequirement(t *testing.T) {
	dependency := filepath.Join(t.TempDir(), "model")
	writeTestFile(t, filepath.Join(dependency, "go.mod"), "module example.com/model\n\ngo 1.24\n")
	writeTestFile(t, filepath.Join(dependency, "model.go"), "package model\n\nconst Name = \"model\"\n")
	repository := t.TempDir()
	writeTestFile(t, filepath.Join(repository, "go.mod"), "module example.com/app\n\ngo 1.24\n\nrequire example.com/model v0.1.0\n\nreplace example.com/model => "+filepath.ToSlash(dependency)+"\n")
	writeTestFile(t, filepath.Join(repository, "app.go"), "package app\n\nimport \"example.com/model\"\n\nvar Name = model.Name\n")
	target := Target{Ecosystem: EcosystemGo, Dependency: "example.com/model", Version: "v0.2.0", Resolved: "v0.2.0"}
	decisions, err := (goAdapter{}).apply(context.Background(), repository, target, Options{Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 1 || decisions[0].Action != "updated" || decisions[0].AfterVersion != "v0.2.0" {
		t.Fatalf("decisions = %+v", decisions)
	}
	contents, err := os.ReadFile(filepath.Join(repository, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "example.com/model v0.2.0") {
		t.Fatalf("go.mod was not updated by Go tooling:\n%s", contents)
	}
}

func TestValidatePublishableGoManifestsRejectsLocalReplace(t *testing.T) {
	t.Parallel()
	repository := t.TempDir()
	writeTestFile(t, filepath.Join(repository, "go.mod"), "module example.com/app\n\ngo 1.24\n\nrequire example.com/model v0.2.0\n\nreplace example.com/model => ../model\n")
	err := validatePublishableGoManifests(repository)
	if err == nil || !strings.Contains(err.Error(), "example.com/model => ../model") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidatePublishableGoManifestsAllowsVersionedReplace(t *testing.T) {
	t.Parallel()
	repository := t.TempDir()
	writeTestFile(t, filepath.Join(repository, "go.mod"), "module example.com/app\n\ngo 1.24\n\nrequire example.com/model v0.2.0\n\nreplace example.com/model => example.com/fork/model v0.2.1\n")
	if err := validatePublishableGoManifests(repository); err != nil {
		t.Fatal(err)
	}
}

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
