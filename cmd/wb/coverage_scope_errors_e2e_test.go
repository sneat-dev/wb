//go:build e2e

package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestE2ECoverageChangedAffectedScopeRejectsBrokenGraph(t *testing.T) {
	t.Parallel()
	repo := newRatchetFixtureRepo(t)
	repo.writeFile("app.go", "package app\n")
	base := repo.commitAll("valid base")
	// The head graph cannot resolve this package declaration. Coverage must
	// report the graph failure rather than treating the selected scope as empty.
	repo.writeFile("app.go", "package\n")
	profile := filepath.Join(t.TempDir(), "coverage.out")
	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", repo.dir, "--changed", "--affected-packages", "--target", base,
		"--coverage-profile", profile, "--non-interactive"}, &stdout, &stderr)
	if code == 0 || !strings.Contains(stderr.String(), "read coverage scope graph") {
		t.Fatalf("code=%d stderr=%q, want a graph-resolution failure", code, stderr.String())
	}
	if _, err := os.Stat(profile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed graph produced a coverage profile: %v", err)
	}
}

func TestE2ECoverageChangedRejectsUnreadableSelectedPackage(t *testing.T) {
	t.Parallel()
	repo := newRatchetFixtureRepo(t)
	repo.writeFile("app.go", "package app\n")
	base := repo.commitAll("base")
	profile := filepath.Join(t.TempDir(), "coverage.out")
	var output bytes.Buffer
	command := &cobra.Command{}
	command.SetContext(t.Context())
	command.SetOut(&output)
	// Exercise the internal resolved-selection boundary directly: public
	// --changed --package remains forbidden. A regular file cannot be read as
	// a package directory and must not be mistaken for an absent package.
	err := runChangedCoverage(command, repo.dir, qualityOptions{
		target: base, packagePatterns: []string{"./go.mod"}, explicitGoTestPackages: true,
		coverageProfile: profile, format: "json",
	})
	var pathError *os.PathError
	if !errors.As(err, &pathError) || !strings.Contains(err.Error(), "resolve coverage package ./go.mod") {
		t.Fatalf("error=%v, want the package directory read failure", err)
	}
	if output.Len() != 0 {
		t.Fatalf("unreadable selected package produced a success report: %s", &output)
	}
	if _, err := os.Stat(profile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unreadable selected package produced a coverage profile: %v", err)
	}
}
