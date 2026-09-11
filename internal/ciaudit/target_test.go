package ciaudit

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// targetFixture builds a real Git repository with a main branch carrying one
// coverage floor and a feature branch this test can lower it on.
type targetFixture struct {
	Root string
}

func newTargetFixture(t *testing.T, mainFloor string) targetFixture {
	t.Helper()
	root := t.TempDir()
	targetGit(t, root, "init", "-q", "-b", "main")
	targetGit(t, root, "config", "user.email", "audit@example.test")
	targetGit(t, root, "config", "user.name", "audit")
	write(t, root, ".github/workflows/ci.yml", `
jobs:
  build:
    with:
      min_test_coverage_percent: `+mainFloor+`
`)
	targetGit(t, root, "add", "-A")
	targetGit(t, root, "commit", "-qm", "init")

	// CompareCoverageFloors reads "origin/<target>", not a local branch, so
	// the fixture needs a real remote to fetch from.
	remote := filepath.Join(t.TempDir(), "remote.git")
	targetGit(t, root, "init", "-q", "--bare", remote)
	targetGit(t, root, "remote", "add", "origin", remote)
	targetGit(t, root, "push", "-q", "origin", "main")

	return targetFixture{Root: root}
}

func targetGit(t *testing.T, dir string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s in %s: %v\n%s", arguments, dir, err, output)
	}
}

// TestCompareCoverageFloorsReportsALoweredFloor pins
// lesson:l10-coverage-floors-are-raised-with-real-tests-never-lowered-to-fit.
func TestCompareCoverageFloorsReportsALoweredFloor(t *testing.T) {
	fixture := newTargetFixture(t, "85")
	targetGit(t, fixture.Root, "checkout", "-qb", "feature/x")
	write(t, fixture.Root, ".github/workflows/ci.yml", `
jobs:
  build:
    with:
      min_test_coverage_percent: 70
`)
	targetGit(t, fixture.Root, "add", "-A")
	targetGit(t, fixture.Root, "commit", "-qm", "lower the floor")

	findings, err := CompareCoverageFloors(fixture.Root, "main")
	if err != nil {
		t.Fatalf("CompareCoverageFloors: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("expected exactly one finding, got %+v", findings)
	}
	if findings[0].Code != "coverage-floor-lowered" {
		t.Fatalf("unexpected finding code: %+v", findings[0])
	}
	if findings[0].File != ".github/workflows/ci.yml" {
		t.Fatalf("unexpected finding file: %+v", findings[0])
	}
}

// TestCompareCoverageFloorsFalsePositives pins the shapes that must not
// report a finding: a raised floor, an unchanged floor, the current branch
// already being the target, and a workflow that is new on this branch (so
// the target has nothing to compare it to).
func TestCompareCoverageFloorsFalsePositives(t *testing.T) {
	t.Run("a raised floor is not a lowered one", func(t *testing.T) {
		fixture := newTargetFixture(t, "70")
		targetGit(t, fixture.Root, "checkout", "-qb", "feature/x")
		write(t, fixture.Root, ".github/workflows/ci.yml", `
jobs:
  build:
    with:
      min_test_coverage_percent: 85
`)
		targetGit(t, fixture.Root, "add", "-A")
		targetGit(t, fixture.Root, "commit", "-qm", "raise the floor")

		findings, err := CompareCoverageFloors(fixture.Root, "main")
		if err != nil {
			t.Fatalf("CompareCoverageFloors: %v", err)
		}
		if len(findings) != 0 {
			t.Fatalf("a raised floor produced findings: %+v", findings)
		}
	})

	t.Run("an unchanged floor", func(t *testing.T) {
		fixture := newTargetFixture(t, "85")
		targetGit(t, fixture.Root, "checkout", "-qb", "feature/x")
		write(t, fixture.Root, "README.md", "unrelated change\n")
		targetGit(t, fixture.Root, "add", "-A")
		targetGit(t, fixture.Root, "commit", "-qm", "unrelated")

		findings, err := CompareCoverageFloors(fixture.Root, "main")
		if err != nil {
			t.Fatalf("CompareCoverageFloors: %v", err)
		}
		if len(findings) != 0 {
			t.Fatalf("an unchanged floor produced findings: %+v", findings)
		}
	})

	t.Run("the current branch already is the target", func(t *testing.T) {
		fixture := newTargetFixture(t, "85")
		findings, err := CompareCoverageFloors(fixture.Root, "main")
		if err != nil {
			t.Fatalf("CompareCoverageFloors: %v", err)
		}
		if len(findings) != 0 {
			t.Fatalf("auditing main against itself produced findings: %+v", findings)
		}
	})

	t.Run("a workflow file that is new on this branch", func(t *testing.T) {
		fixture := newTargetFixture(t, "85")
		targetGit(t, fixture.Root, "checkout", "-qb", "feature/x")
		write(t, fixture.Root, ".github/workflows/new.yml", `
jobs:
  build:
    with:
      min_test_coverage_percent: 10
`)
		targetGit(t, fixture.Root, "add", "-A")
		targetGit(t, fixture.Root, "commit", "-qm", "add a new workflow")

		findings, err := CompareCoverageFloors(fixture.Root, "main")
		if err != nil {
			t.Fatalf("CompareCoverageFloors: %v", err)
		}
		if len(findings) != 0 {
			t.Fatalf("a workflow absent from the target produced findings: %+v", findings)
		}
	})

	t.Run("an empty target is a no-op", func(t *testing.T) {
		fixture := newTargetFixture(t, "85")
		findings, err := CompareCoverageFloors(fixture.Root, "")
		if err != nil {
			t.Fatalf("CompareCoverageFloors: %v", err)
		}
		if len(findings) != 0 {
			t.Fatalf("an empty target produced findings: %+v", findings)
		}
	})
}
