//go:build e2e

package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
