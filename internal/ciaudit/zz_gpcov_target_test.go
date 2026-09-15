package ciaudit

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestGpCovCompareCoverageFloorsReportsTheGitCommandThatFailed pins that a
// comparison the package cannot make fails loudly instead of reporting "no
// lowered floors".
func TestGpCovCompareCoverageFloorsReportsTheGitCommandThatFailed(t *testing.T) {
	t.Run("the root is not a git repository", func(t *testing.T) {
		root := t.TempDir()
		// Keep discovery from escaping into a repository that happens to
		// contain the temporary directory.
		t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(root))

		findings, err := CompareCoverageFloors(root, "main")
		if err == nil {
			t.Fatalf("findings = %+v, want an error", findings)
		}
		if !strings.Contains(err.Error(), "determine the current branch") {
			t.Fatalf("error %q does not name the failed step", err)
		}
	})

	t.Run("the target cannot be fetched", func(t *testing.T) {
		root := t.TempDir()
		targetGit(t, root, "init", "-q", "-b", "main")
		targetGit(t, root, "config", "user.email", "audit@example.test")
		targetGit(t, root, "config", "user.name", "audit")
		write(t, root, "README.md", "fixture\n")
		targetGit(t, root, "add", "-A")
		targetGit(t, root, "commit", "-qm", "init")
		targetGit(t, root, "checkout", "-qb", "feature/x")

		findings, err := CompareCoverageFloors(root, "main")
		if err == nil {
			t.Fatalf("findings = %+v, want an error", findings)
		}
		if !strings.Contains(err.Error(), "fetch origin/main") {
			t.Fatalf("error %q does not name the failed fetch", err)
		}
	})
}

// TestGpCovCompareCoverageFloorsReportsAWorkflowsPathThatIsNotADirectory pins
// that a local read failure is propagated rather than read as "no floors".
func TestGpCovCompareCoverageFloorsReportsAWorkflowsPathThatIsNotADirectory(t *testing.T) {
	root := gpCovRepo(t, nil)
	targetGit(t, root, "checkout", "-qb", "feature/x")
	write(t, root, filepath.Join(".github", "workflows"), "not a directory\n")

	findings, err := CompareCoverageFloors(root, "main")
	if err == nil {
		t.Fatalf("findings = %+v, want an error", findings)
	}
	if !strings.Contains(err.Error(), "workflows") {
		t.Fatalf("error %q does not name the workflows path", err)
	}
}

// TestGpCovCompareCoverageFloorsWithoutAWorkflowsDirectoryIsAValidNoOp pins
// that a branch carrying no workflows at all has nothing to lower.
func TestGpCovCompareCoverageFloorsWithoutAWorkflowsDirectoryIsAValidNoOp(t *testing.T) {
	root := gpCovRepo(t, nil)
	targetGit(t, root, "checkout", "-qb", "feature/x")
	write(t, root, "backend/main.go", "package main\n")

	findings, err := CompareCoverageFloors(root, "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("a branch with no workflows produced findings: %+v", findings)
	}
}

// TestGpCovCompareCoverageFloorsSkipsWorkflowsItCannotCompare pins each skip
// in the local read: a target workflow with no floor, a non-YAML entry, a
// subdirectory, and an unreadable file.
func TestGpCovCompareCoverageFloorsSkipsWorkflowsItCannotCompare(t *testing.T) {
	t.Run("the target has no floor for the workflow", func(t *testing.T) {
		root := gpCovRepo(t, map[string]string{
			".github/workflows/ci.yml": "jobs:\n  build:\n    with:\n      other: 1\n",
		})
		targetGit(t, root, "checkout", "-qb", "feature/x")
		write(t, root, ".github/workflows/ci.yml", "jobs:\n  build:\n    with:\n      min_test_coverage_percent: 70\n")
		targetGit(t, root, "add", "-A")
		targetGit(t, root, "commit", "-qm", "add a floor")

		findings, err := CompareCoverageFloors(root, "main")
		if err != nil {
			t.Fatal(err)
		}
		if len(findings) != 0 {
			t.Fatalf("a workflow with no target floor produced findings: %+v", findings)
		}
	})

	// A lowered floor is committed to a README.md inside the workflows
	// directory. Only the extension filter keeps it from being compared, so a
	// finding here would mean the filter is gone.
	t.Run("a non-YAML entry is not a workflow", func(t *testing.T) {
		const targetNote = "jobs:\n  build:\n    with:\n      min_test_coverage_percent: 85\n"
		root := gpCovRepo(t, map[string]string{".github/workflows/README.md": targetNote})
		targetGit(t, root, "checkout", "-qb", "feature/x")
		write(t, root, ".github/workflows/README.md", "jobs:\n  build:\n    with:\n      min_test_coverage_percent: 10\n")
		targetGit(t, root, "add", "-A")
		targetGit(t, root, "commit", "-qm", "lower a note")

		findings, err := CompareCoverageFloors(root, "main")
		if err != nil {
			t.Fatal(err)
		}
		if len(findings) != 0 {
			t.Fatalf("a non-YAML entry was compared as a workflow: %+v", findings)
		}
	})

	// Reading a directory as a file fails; only the IsDir skip keeps this call
	// from returning an error.
	t.Run("a subdirectory is skipped", func(t *testing.T) {
		root := gpCovRepo(t, nil)
		targetGit(t, root, "checkout", "-qb", "feature/x")
		write(t, root, ".github/workflows/nested/keep.yml", "min_test_coverage_percent: 10\n")

		findings, err := CompareCoverageFloors(root, "main")
		if err != nil {
			t.Fatalf("a subdirectory under the workflows path produced an error: %v", err)
		}
		if len(findings) != 0 {
			t.Fatalf("a subdirectory produced findings: %+v", findings)
		}
	})

	t.Run("an unreadable workflow file", func(t *testing.T) {
		root := gpCovRepo(t, nil)
		targetGit(t, root, "checkout", "-qb", "feature/x")
		write(t, root, ".github/workflows/ci.yml", "min_test_coverage_percent: 70\n")
		gpCovMakeUnreadable(t, filepath.Join(root, ".github", "workflows", "ci.yml"))

		findings, err := CompareCoverageFloors(root, "main")
		if err == nil {
			t.Fatalf("findings = %+v, want an error", findings)
		}
		if !strings.Contains(err.Error(), "ci.yml") {
			t.Fatalf("error %q does not name the unreadable workflow", err)
		}
	})
}

// TestGpCovMinTestCoveragePercentRejectsUnusableValues pins the extractor's
// own boundary: no floor at all, and a floor that cannot be represented.
func TestGpCovMinTestCoveragePercentRejectsUnusableValues(t *testing.T) {
	if value, ok := minTestCoveragePercent("jobs:\n  with:\n    min_test_coverage_percent: 85.5\n"); !ok || value != 85.5 {
		t.Fatalf("value = %v, ok = %v; want the declared 85.5", value, ok)
	}

	overflow := "min_test_coverage_percent: " + strings.Repeat("9", 400) + "\n"
	if value, ok := minTestCoveragePercent(overflow); ok {
		t.Fatalf("a value that overflows float64 was accepted as %v", value)
	}

	if value, ok := minTestCoveragePercent("jobs:\n  build:\n    runs-on: ubuntu-latest\n"); ok {
		t.Fatalf("content with no floor was accepted as %v", value)
	}
}

// TestGpCovGitOutputReportsAFailedExecutableLookup covers the failure that is
// not an exit status — the one a caller must not read as "git said nothing".
func TestGpCovGitOutputReportsAFailedExecutableLookup(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	output, err := gitOutput(t.TempDir(), "rev-parse", "--abbrev-ref", "HEAD")
	if err == nil {
		t.Fatalf("output = %q, want an error", output)
	}
	if !strings.Contains(err.Error(), "git rev-parse --abbrev-ref HEAD") {
		t.Fatalf("error %q does not name the command that failed", err)
	}
}
