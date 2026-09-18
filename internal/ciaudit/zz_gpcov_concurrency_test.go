package ciaudit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const gpCovCancellingWorkflow = `name: CI
on:
  pull_request:
  push:
    branches: [main]
concurrency:
  group: ci-${{ github.workflow }}-${{ github.ref }}
  cancel-in-progress: true
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: echo build
`

// TestGpCovStreamConcurrencyReportsUnreadableWorkflowInputs pins that an
// unreadable workflows directory and an unreadable or malformed workflow are
// errors, never an empty report a caller could read as "nothing to cancel".
func TestGpCovStreamConcurrencyReportsUnreadableWorkflowInputs(t *testing.T) {
	t.Parallel()
	t.Run("the workflows path is not a directory", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		write(t, root, filepath.Join(".github", "workflows"), "not a directory\n")

		reports, err := StreamConcurrency(root)
		if err == nil {
			t.Fatalf("reports = %#v, want an error", reports)
		}
		if !strings.Contains(err.Error(), "read workflows in") {
			t.Fatalf("error %q does not name the workflows directory", err)
		}
	})

	t.Run("a workflow file cannot be read", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeWorkflow(t, root, "ci.yml", gpCovCancellingWorkflow)
		gpCovMakeUnreadable(t, filepath.Join(root, ".github", "workflows", "ci.yml"))

		reports, err := StreamConcurrency(root)
		if err == nil {
			t.Fatalf("reports = %#v, want an error", reports)
		}
		if !strings.Contains(err.Error(), "ci.yml") {
			t.Fatalf("error %q does not name the unreadable workflow", err)
		}
	})

	t.Run("a workflow file is not YAML", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeWorkflow(t, root, "ci.yml", "name: [unterminated\n")

		reports, err := StreamConcurrency(root)
		if err == nil {
			t.Fatalf("reports = %#v, want an error", reports)
		}
		if !strings.Contains(err.Error(), "parse workflow") {
			t.Fatalf("error %q does not name the parse failure", err)
		}
	})
}

// TestGpCovStreamConcurrencyReportsEveryWorkflowInPathOrder pins that
// subdirectories and non-YAML entries are not workflows, and that the reports
// come back sorted by workflow path.
func TestGpCovStreamConcurrencyReportsEveryWorkflowInPathOrder(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeWorkflow(t, root, "b.yml", "name: B\non: [push]\n")
	writeWorkflow(t, root, "a.yaml", "name: A\non: [push]\n")
	writeWorkflow(t, root, "notes.md", "not a workflow\n")
	write(t, root, filepath.Join(".github", "workflows", "nested", "other.yml"), gpCovCancellingWorkflow)

	reports, err := StreamConcurrency(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 2 {
		t.Fatalf("reports = %#v, want the two top-level workflow files", reports)
	}
	if reports[0].Workflow != ".github/workflows/a.yaml" || reports[0].Name != "A" {
		t.Fatalf("reports[0] = %#v, want a.yaml first", reports[0])
	}
	if reports[1].Workflow != ".github/workflows/b.yml" || reports[1].Name != "B" {
		t.Fatalf("reports[1] = %#v, want b.yml second", reports[1])
	}
}

// TestGpCovStreamConcurrencyReadsScalarAndAbsentTriggers covers the two shapes
// a workflow's `on` can take beyond a sequence and a mapping.
func TestGpCovStreamConcurrencyReadsScalarAndAbsentTriggers(t *testing.T) {
	t.Parallel()
	t.Run("a scalar on", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeWorkflow(t, root, "ci.yml", "name: CI\non: push\njobs:\n  b:\n    runs-on: ubuntu-latest\n")

		reports, err := StreamConcurrency(root)
		if err != nil {
			t.Fatal(err)
		}
		if !reports[0].Push || reports[0].PullRequest {
			t.Fatalf("scalar on: push = %#v; want push only", reports[0])
		}
	})

	t.Run("no on key at all", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeWorkflow(t, root, "ci.yml", "name: CI\njobs:\n  b:\n    runs-on: ubuntu-latest\n")

		reports, err := StreamConcurrency(root)
		if err != nil {
			t.Fatal(err)
		}
		if reports[0].Push || reports[0].PullRequest {
			t.Fatalf("a workflow with no on key reported triggers: %#v", reports[0])
		}
	})
}

// TestGpCovStreamConcurrencyTreatsAnEmptyConcurrencyValueAsUndeclared pins
// that `concurrency:` with no value declares nothing, rather than a group
// named by the empty string.
func TestGpCovStreamConcurrencyTreatsAnEmptyConcurrencyValueAsUndeclared(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeWorkflow(t, root, "ci.yml", "name: CI\non: [pull_request]\nconcurrency:\njobs:\n  b:\n    runs-on: ubuntu-latest\n")

	reports, err := StreamConcurrency(root)
	if err != nil {
		t.Fatal(err)
	}
	if reports[0].Declared || reports[0].Group != "" || reports[0].Cancels() {
		t.Fatalf("an empty concurrency declaration was read as a declared group: %#v", reports[0])
	}
}

// TestGpCovWorkflowMechanismsReportsAnUnreadableWorkflow covers the read
// failure that is not "absent": a path that exists but cannot be read is an
// error, because reporting no mechanisms would claim CI runs nothing.
func TestGpCovWorkflowMechanismsReportsAnUnreadableWorkflow(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".github", "workflows", "ci.yml"), 0o755); err != nil {
		t.Fatal(err)
	}

	mechanisms, reusable, err := WorkflowMechanismsWithReuse(root, ".github/workflows/ci.yml")
	if err == nil {
		t.Fatalf("mechanisms = %#v, reusable = %v; want an error", mechanisms, reusable)
	}
	if !strings.Contains(err.Error(), "ci.yml") {
		t.Fatalf("error %q does not name the unreadable workflow", err)
	}
}

// TestGpCovWorkflowMechanismsKeepsInvocationsAfterAQuotedHash pins that a `#`
// inside a quoted shell fragment is not a YAML comment: truncating there would
// drop the invocation that is the whole point of the read.
func TestGpCovWorkflowMechanismsKeepsInvocationsAfterAQuotedHash(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		step      string
		mechanism string
	}{
		{
			name:      "double quoted hash",
			step:      `      - run: echo "a # b"; go test -race -count=1 ./...`,
			mechanism: "-race",
		},
		{
			name:      "single quoted hash",
			step:      `      - run: echo 'a # b'; go vet ./...`,
			mechanism: "go vet",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeWorkflow(t, root, "ci.yml",
				"name: CI\non: [pull_request]\njobs:\n  build:\n    runs-on: ubuntu-latest\n    steps:\n"+
					testCase.step+"\n")

			mechanisms, err := WorkflowMechanisms(root, ".github/workflows/ci.yml")
			if err != nil {
				t.Fatal(err)
			}
			if !mechanisms[testCase.mechanism] {
				t.Fatalf("mechanism %q was lost to a quoted #: %#v", testCase.mechanism, mechanisms)
			}
		})
	}
}

// TestGpCovWorkflowMechanismsWithReuseReportsTheReusableCallFlag pins that a
// job-level `uses:` into another repository is reported as opaque, so a caller
// never treats an unreadable callee as proof a mechanism is absent.
func TestGpCovWorkflowMechanismsWithReuseReportsTheReusableCallFlag(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeWorkflow(t, root, "ci.yml", `name: CI
on: [pull_request]
jobs:
  local:
    runs-on: ubuntu-latest
    steps:
      - run: go test -race ./...
  remote:
    uses: sneat-co/cicd/.github/workflows/go-ci.yml@main
`)

	mechanisms, reusable, err := WorkflowMechanismsWithReuse(root, ".github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	if !reusable {
		t.Fatalf("a job-level uses: was not reported as a reusable call: %#v", mechanisms)
	}
	if !mechanisms["-race"] {
		t.Fatalf("the locally invoked mechanism was not detected: %#v", mechanisms)
	}
}
