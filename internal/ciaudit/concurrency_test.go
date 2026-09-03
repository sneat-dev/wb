package ciaudit

import (
	"os"
	"path/filepath"
	"testing"
)

func writeWorkflow(t *testing.T, root, name, contents string) {
	t.Helper()
	directory := filepath.Join(root, ".github", "workflows")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, name), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStreamConcurrencyRecognizesACancellingRefKeyedGroup(t *testing.T) {
	root := t.TempDir()
	writeWorkflow(t, root, "ci.yml", `name: CI
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
`)
	reports, err := StreamConcurrency(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 {
		t.Fatalf("reports = %#v, want one", reports)
	}
	report := reports[0]
	if !report.PullRequest || !report.Push {
		t.Errorf("triggers = %#v, want both pull_request and push", report)
	}
	if !report.Cancels() {
		t.Errorf("report = %#v, want a cancelling ref-keyed group", report)
	}
}

// A group that names only the workflow serializes the whole repository. That
// queues an unrelated branch behind the stream instead of cancelling the
// stream's own superseded run, so it must not count as cancellation.
func TestStreamConcurrencyRejectsAGroupThatIsNotRefKeyed(t *testing.T) {
	root := t.TempDir()
	writeWorkflow(t, root, "ci.yml", `name: CI
on: [pull_request]
concurrency:
  group: ci
  cancel-in-progress: true
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: echo build
`)
	reports, err := StreamConcurrency(root)
	if err != nil {
		t.Fatal(err)
	}
	if reports[0].Cancels() {
		t.Errorf("a repository-wide group counted as per-branch cancellation: %#v", reports[0])
	}
	if !reports[0].Declared {
		t.Errorf("the declared group was not recorded: %#v", reports[0])
	}
}

func TestStreamConcurrencyRejectsAGroupWithoutCancelInProgress(t *testing.T) {
	root := t.TempDir()
	writeWorkflow(t, root, "ci.yml", `name: CI
on: [pull_request]
concurrency:
  group: ci-${{ github.ref }}
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: echo build
`)
	reports, err := StreamConcurrency(root)
	if err != nil {
		t.Fatal(err)
	}
	if reports[0].Cancels() || reports[0].CancelInProgress {
		t.Errorf("a group without cancel-in-progress counted as cancelling: %#v", reports[0])
	}
}

// A workflow declaring `concurrency` for one job only, with none at the top
// level, must not read as repository-wide cancellation. A text match would
// report the nested value as present.
func TestStreamConcurrencyIgnoresAJobLevelDeclarationAtTheTopLevel(t *testing.T) {
	root := t.TempDir()
	writeWorkflow(t, root, "ci.yml", `name: CI
on: [pull_request]
jobs:
  build:
    runs-on: ubuntu-latest
    concurrency:
      group: build-${{ github.ref }}
      cancel-in-progress: true
    steps:
      - run: echo build
`)
	reports, err := StreamConcurrency(root)
	if err != nil {
		t.Fatal(err)
	}
	if reports[0].Declared {
		t.Errorf("a job-level concurrency block was read as the workflow's own: %#v", reports[0])
	}
}

// YAML 1.1 folds a bare `on:` key to the boolean true; the trigger read must
// survive that rather than silently reporting no triggers.
func TestStreamConcurrencyReadsTriggersDespiteTheYAMLOnKeyFolding(t *testing.T) {
	root := t.TempDir()
	writeWorkflow(t, root, "ci.yaml", "name: CI\non:\n  pull_request:\n    branches: [main]\njobs:\n  b:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo\n")
	reports, err := StreamConcurrency(root)
	if err != nil {
		t.Fatal(err)
	}
	if !reports[0].PullRequest {
		t.Fatalf("pull_request trigger was not read: %#v", reports[0])
	}
}

func TestStreamConcurrencyOnARepositoryWithNoWorkflowsIsEmpty(t *testing.T) {
	reports, err := StreamConcurrency(t.TempDir())
	if err != nil || len(reports) != 0 {
		t.Fatalf("reports = %#v, err = %v; want an empty result", reports, err)
	}
}

func TestStreamConcurrencyAcceptsAScalarConcurrencyDeclaration(t *testing.T) {
	root := t.TempDir()
	writeWorkflow(t, root, "ci.yml", "name: CI\non: [pull_request]\nconcurrency: ci-${{ github.ref }}\njobs:\n  b:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo\n")
	reports, err := StreamConcurrency(root)
	if err != nil {
		t.Fatal(err)
	}
	if !reports[0].Declared || reports[0].Cancels() {
		t.Errorf("scalar concurrency = %#v; declared but never cancelling", reports[0])
	}
}

// The evidence behind "CI owns it" must be the invocation, not the word: a
// mechanism named in a comment or a job name is not proof that it runs.
func TestWorkflowMechanismsMatchInvocationsNotMentions(t *testing.T) {
	root := t.TempDir()
	writeWorkflow(t, root, "ci.yml", `name: CI
on: [pull_request]
env:
  CI: "1"
jobs:
  race:
    runs-on: ubuntu-latest
    steps:
      - run: go test -race -count=1 ./...
      - run: go vet ./...
`)
	mechanisms, err := WorkflowMechanisms(root, ".github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"-race", "go vet", "-count=1", "CI=1"} {
		if !mechanisms[want] {
			t.Errorf("mechanism %q not detected in %#v", want, mechanisms)
		}
	}

	// A workflow that only mentions the words carries none of them.
	mentions := t.TempDir()
	writeWorkflow(t, mentions, "ci.yml", `name: race and vet notes
on: [pull_request]
jobs:
  build:
    # we should add -race and go vet here one day
    runs-on: ubuntu-latest
    steps:
      - run: go build ./...
`)
	none, err := WorkflowMechanisms(mentions, ".github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	if none["-race"] || none["go vet"] {
		t.Fatalf("mechanisms = %#v; a mention is not evidence that CI runs it", none)
	}
}

func TestWorkflowMechanismsOnAMissingWorkflowIsEmpty(t *testing.T) {
	mechanisms, err := WorkflowMechanisms(t.TempDir(), ".github/workflows/absent.yml")
	if err != nil || len(mechanisms) != 0 {
		t.Fatalf("mechanisms = %#v, err = %v", mechanisms, err)
	}
}
