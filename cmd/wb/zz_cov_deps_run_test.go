package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/recipe"
	"github.com/sneat-dev/wb/internal/runlog"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// cwDepsRecipeConfig writes a recipe config with one always-applicable
// template-section recipe, one gated on a go.mod, and one whose applies_if is
// invalid so the error bucket is reachable.
func cwDepsRecipeConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	readmeTemplate := filepath.Join(dir, "readme.tmpl")
	gatedTemplate := filepath.Join(dir, "gated.tmpl")
	brokenTemplate := filepath.Join(dir, "broken.tmpl")
	cwCovWriteFile(t, readmeTemplate, "<!-- cw-deps:v1 -->\ncw-deps hello\n<!-- /cw-deps -->\n")
	cwCovWriteFile(t, gatedTemplate, "<!-- cw-deps-gated:v1 -->\ncw-deps gated\n<!-- /cw-deps-gated -->\n")
	cwCovWriteFile(t, brokenTemplate, "<!-- cw-deps-broken:v1 -->\ncw-deps broken\n<!-- /cw-deps-broken -->\n")
	path := filepath.Join(dir, "wb.yaml")
	cwCovWriteFile(t, path, `
recipes:
  readme:
    type: template-section
    applies_if: always
    target: README.md
    template: `+readmeTemplate+`
    marker: cw-deps
  gated:
    type: template-section
    applies_if: has_file:go.mod
    target: README.md
    template: `+gatedTemplate+`
    marker: cw-deps-gated
  broken:
    type: template-section
    applies_if: nonsense
    target: README.md
    template: `+brokenTemplate+`
    marker: cw-deps-broken
`)
	return path
}

// cwDepsRunFixture builds a projects root whose repositories exercise every
// runRun bucket, plus a hermetic gh that classifies the remote side.
func cwDepsRunFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	seeds := t.TempDir()
	// A real clone with an origin, so recipe preview can fetch and inspect it.
	cwCovCloneWithOrigin(t, seeds, "app", filepath.Join(root, "acme", "app"))
	// A clone of a repository GitHub does not list for this owner: local-only.
	cwCovCloneWithOrigin(t, seeds, "localonly", filepath.Join(root, "acme", "localonly"))
	// A fork and an archived repository are listed remotely.
	cwCovCloneWithOrigin(t, seeds, "fork", filepath.Join(root, "acme", "fork"))
	cwCovCloneWithOrigin(t, seeds, "archived", filepath.Join(root, "acme", "archived"))
	// The fake login is the same owner as the extra org, so fleetOwners
	// resolves exactly one owner and every local repository is classified.
	cwCovFakeGH(t, "acme", nil,
		`[{"name":"app","isArchived":false,"isFork":false,"sshUrl":"git@example.test:acme/app.git"},`+
			`{"name":"fork","isArchived":false,"isFork":true,"sshUrl":"git@example.test:acme/fork.git"},`+
			`{"name":"archived","isArchived":true,"isFork":false,"sshUrl":"git@example.test:acme/archived.git"},`+
			`{"name":"remoteonly","isArchived":false,"isFork":false,"sshUrl":"git@example.test:acme/remoteonly.git"}]`)
	t.Setenv(wbhome.EnvOverride, t.TempDir())
	return root
}

func TestCwDepsRunRunDryRunClassifiesEveryRepositoryBucket(t *testing.T) {
	root := cwDepsRunFixture(t)
	configPath := cwDepsRecipeConfig(t)

	previousRoot, previousOrgs := projectsRoot, extraOrgs
	projectsRoot, extraOrgs = root, []string{"acme"}
	t.Cleanup(func() { projectsRoot, extraOrgs = previousRoot, previousOrgs })

	var code int
	out, errOut := cwDepsCaptureStderr(t, func() { code = runRun(root, "", []string{"acme"}, configPath, "readme", false, false) })
	if code != 1 {
		t.Fatalf("dry-run exit = %d, want findings for the drift the recipe would land\n%s", code, out)
	}
	for _, want := range []string{
		"Summary", "Updated", "Skipped", "Forks", "Archived", "Errors",
		"✓ acme/app — would",
		"⑂ acme/fork",
		"▪ acme/archived",
		"– acme/localonly — local-only (not under your GitHub orgs)",
		"– acme/remoteonly — remote-only (clone to evaluate)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run report missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(errOut, "dry-run: reporting only") {
		t.Errorf("dry-run intent was not announced on stderr:\n%s", errOut)
	}
}

func TestCwDepsRunRunGatedRecipeSkipsNonApplicableRepositories(t *testing.T) {
	root := cwDepsRunFixture(t)
	configPath := cwDepsRecipeConfig(t)
	previousRoot := projectsRoot
	projectsRoot = root
	t.Cleanup(func() { projectsRoot = previousRoot })

	var code int
	out := cwCovCaptureStdout(t, func() { code = runRun(root, "", []string{"acme"}, configPath, "gated", false, false) })
	if code != 0 {
		t.Fatalf("gated dry-run exit = %d, want no drift\n%s", code, out)
	}
	if !strings.Contains(out, "recipe does not apply") {
		t.Errorf("gated recipe did not report non-applicable repositories:\n%s", out)
	}
}

func TestCwDepsRunRunReportsInvalidAppliesIfAsAnError(t *testing.T) {
	root := cwDepsRunFixture(t)
	configPath := cwDepsRecipeConfig(t)
	previousRoot := projectsRoot
	projectsRoot = root
	t.Cleanup(func() { projectsRoot = previousRoot })

	var code int
	out := cwCovCaptureStdout(t, func() { code = runRun(root, "", []string{"acme"}, configPath, "broken", false, false) })
	if code != 1 {
		t.Fatalf("broken recipe exit = %d, want findings\n%s", code, out)
	}
	if !strings.Contains(out, "unknown applies_if") {
		t.Errorf("broken recipe did not name the invalid predicate:\n%s", out)
	}
}

func TestCwDepsRunRunListAndUnknownRecipeAndBadConfig(t *testing.T) {
	root := cwDepsRunFixture(t)
	configPath := cwDepsRecipeConfig(t)

	// --list names every configured recipe in sorted order and reports no drift.
	out := cwCovCaptureStdout(t, func() {
		if code := runRun(root, "", nil, configPath, "", true, false); code != 0 {
			t.Fatalf("--list exit = %d", code)
		}
	})
	if !strings.Contains(out, "broken") || !strings.Contains(out, "gated") || !strings.Contains(out, "readme") {
		t.Errorf("--list output = %q", out)
	}
	if strings.Index(out, "broken") > strings.Index(out, "gated") {
		t.Errorf("--list is not sorted: %q", out)
	}
	// No recipe name behaves like --list rather than failing.
	out = cwCovCaptureStdout(t, func() {
		if code := runRun(root, "", nil, configPath, "", false, false); code != 0 {
			t.Fatalf("empty recipe exit = %d", code)
		}
	})
	if !strings.Contains(out, "readme") {
		t.Errorf("empty recipe did not list: %q", out)
	}
	// An unknown recipe is a named error.
	_, errOut := cwDepsCaptureStderr(t, func() {
		if code := runRun(root, "", nil, configPath, "absent", false, false); code != 1 {
			t.Fatalf("unknown recipe exit = %d", code)
		}
	})
	if !strings.Contains(errOut, `unknown recipe "absent"`) {
		t.Errorf("unknown recipe stderr = %q", errOut)
	}
	// An unreadable config is reported rather than treated as no recipes.
	_, errOut = cwDepsCaptureStderr(t, func() {
		if code := runRun(root, "", nil, filepath.Join(t.TempDir(), "absent.yaml"), "readme", false, false); code != 1 {
			t.Fatalf("missing config exit = %d", code)
		}
	})
	if !strings.Contains(errOut, "read config") {
		t.Errorf("missing config stderr = %q", errOut)
	}
}

// TestCwDepsRunRunApplyLandsTheRecipe covers the mutating half: a real clone
// with a real origin, so gitops.Land can create its worktree and commit.
func TestCwDepsRunRunApplyLandsTheRecipe(t *testing.T) {
	root := t.TempDir()
	seeds := t.TempDir()
	clone := filepath.Join(root, "acme", "app")
	remote := cwCovCloneWithOrigin(t, seeds, "app", clone)
	runGit(t, clone, "config", "user.email", "wb@example.test")
	runGit(t, clone, "config", "user.name", "WB Test")
	// A hermetic gh lists exactly this clone for the selected owner, so the
	// recipe is applied to the scratch checkout and never to a real one.
	cwCovFakeGH(t, "acme", nil,
		`[{"name":"app","isArchived":false,"isFork":false,"sshUrl":"git@example.test:acme/app.git"}]`)
	configPath := cwDepsRecipeConfig(t)
	previousRoot := projectsRoot
	projectsRoot = root
	t.Cleanup(func() { projectsRoot = previousRoot })

	var applyCode int
	out := cwCovCaptureStdout(t, func() {
		applyCode = runRun(root, "", []string{"acme"}, configPath, "readme", false, true)
	})
	if applyCode != 0 {
		t.Fatalf("apply exit = %d\n%s", applyCode, out)
	}
	if !strings.Contains(out, "✓ acme/app") {
		t.Errorf("apply did not report the landed repository:\n%s", out)
	}
	if !strings.Contains(out, "pushed to main") {
		t.Errorf("apply did not report where the change landed:\n%s", out)
	}
	// The change reached the default branch of the real origin, carrying the
	// recipe's marker block.
	published, err := exec.Command("git", "--git-dir", remote, "show", "main:README.md").Output()
	if err != nil || !strings.Contains(string(published), "cw-deps hello") {
		t.Fatalf("apply did not publish the recipe block: %q, %v\n%s", published, err, out)
	}
	// A second apply still reports the repository: either it is a fresh
	// no-op or the already-created branch is a recorded refusal, and both are
	// visible outcomes rather than silence.
	out = cwCovCaptureStdout(t, func() {
		applyCode = runRun(root, "", []string{"acme"}, configPath, "readme", false, true)
	})
	if !strings.Contains(out, "acme/app") {
		t.Errorf("second apply reported nothing for the repository (exit %d):\n%s", applyCode, out)
	}
}

func TestCwDepsApplyRecipeSurfacesTheFetchFailure(t *testing.T) {
	notARepository := t.TempDir()
	rep := &report{}
	config, err := recipe.LoadConfig(cwDepsRecipeConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	err = applyRecipe(config.Recipes["readme"], discover.Repo{Org: "acme", Name: "app", Path: notARepository}, rep)
	if err == nil {
		t.Fatal("applying a recipe to a non-repository must fail")
	}
	if len(rep.errors) != 0 || len(rep.updated) != 0 {
		t.Errorf("a failed apply must not record an outcome: %+v", rep)
	}
}

func TestCwDepsPrintRunHistoryReadsAManagedWorktree(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "init", "-b", "main")
	manifest := worktrees.Manifest{
		Version: 1, EffortID: "cw-deps", EffortKind: worktrees.EffortKindFeature,
		Repository: "acme/app", Worktree: root, Branch: "cw-deps", Base: "main",
		BaseSHA: strings.Repeat("a", 40), CreatedAt: time.Now().UTC(),
		RunID: "run-1", ClaimID: strings.Repeat("b", 64), Provenance: worktrees.ProvenanceCreated,
	}
	if err := worktrees.WriteManifest(root, manifest); err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC().Add(-time.Hour)
	events := []runlog.Event{
		{SchemaVersion: runlog.EventSchemaVersion, Timestamp: started, OperationID: "wbo-1", State: "requested", Kind: "go test"},
		{SchemaVersion: runlog.EventSchemaVersion, Timestamp: started.Add(time.Minute), OperationID: "wbo-1", State: "succeeded",
			Kind: "go test", DurationMS: 1000, UserCPUMS: 10, SystemCPUMS: 5},
		{SchemaVersion: runlog.EventSchemaVersion, Timestamp: started.Add(2 * time.Minute), OperationID: "wbo-2", State: "failed", Kind: "go build"},
	}
	var lines bytes.Buffer
	for _, event := range events {
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		lines.Write(raw)
		lines.WriteByte('\n')
	}
	cwCovWriteFile(t, filepath.Join(root, ".wb", "local", "run", "events.jsonl"), lines.String())

	restore := cwDepsChdir(t, root)
	defer restore()

	var out bytes.Buffer
	if err := printRunHistory(cwDepsNewOutCommand(&out), 14, false); err != nil {
		t.Fatalf("printRunHistory text: %v", err)
	}
	for _, want := range []string{"Governed commands · 14 days ·", "operations 2 · failed 1", "go test"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("history text missing %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	if err := printRunHistory(cwDepsNewOutCommand(&out), 14, true); err != nil {
		t.Fatalf("printRunHistory json: %v", err)
	}
	var summary runlog.Summary
	if err := json.Unmarshal(out.Bytes(), &summary); err != nil {
		t.Fatalf("history JSON: %v\n%s", err, out.String())
	}
	if summary.Operations != 2 || summary.Failed != 1 {
		t.Errorf("summary = %+v", summary)
	}

	// A window below one day is refused before anything is read.
	restore()
	if err := printRunHistory(cwDepsNewOutCommand(&bytes.Buffer{}), 0, false); err == nil ||
		!strings.Contains(err.Error(), "--days must be at least 1") {
		t.Fatalf("zero-day window = %v", err)
	}
	// Outside a managed worktree the telemetry source is named as missing.
	plain := t.TempDir()
	restore = cwDepsChdir(t, plain)
	defer restore()
	if err := printRunHistory(cwDepsNewOutCommand(&bytes.Buffer{}), 7, false); err == nil ||
		!strings.Contains(err.Error(), "not inside a managed WB worktree") {
		t.Fatalf("unmanaged cwd = %v", err)
	}
}

func TestCwDepsRunCommandUsageRefusalsInProcess(t *testing.T) {
	cwCovFakeGH(t, "cwcov-user", nil, `[]`)
	t.Setenv(wbhome.EnvOverride, t.TempDir())
	tests := map[string][]string{
		"queue with history":    {"run", "--queue", "--history"},
		"history with list":     {"run", "--history", "--list"},
		"worker without async":  {"run", "--worker", "w1"},
		"idempotency alone":     {"run", "--idempotency-key", "k"},
		"async recipe mode":     {"run", "--async"},
		"saturated recipe mode": {"run", "--allow-saturated-host"},
		"quiet recipe mode":     {"run", "--quiet"},
		"days without history":  {"run", "--days", "3"},
	}
	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			_, stderr, code := cwCovRun(t, args...)
			if code != exitUsage {
				t.Fatalf("%v exit = %d, want usage\nstderr: %s", args, code, stderr)
			}
		})
	}
}

// cwDepsCaptureStderr captures os.Stderr while fn runs: runRun prints its
// dry-run notice and errors with fmt.Fprintln(os.Stderr, ...).
func cwDepsCaptureStderr(t *testing.T, fn func()) (string, string) {
	t.Helper()
	previous := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = writer
	restore := func() { os.Stderr = previous }
	defer restore()
	done := make(chan string, 1)
	go func() {
		var buffer bytes.Buffer
		_, _ = buffer.ReadFrom(reader)
		done <- buffer.String()
	}()
	out := cwCovCaptureStdout(t, fn)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	restore()
	captured := <-done
	_ = reader.Close()
	return out, captured
}

// cwDepsChdir moves the process working directory and returns a function that
// restores it, so a test that changes directory always restores before its
// temporary directory is cleaned up.
func cwDepsChdir(t *testing.T, dir string) func() {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	restored := false
	restore := func() {
		if restored {
			return
		}
		restored = true
		if err := os.Chdir(previous); err != nil {
			t.Fatalf("restore working directory: %v", err)
		}
	}
	t.Cleanup(restore)
	return restore
}
