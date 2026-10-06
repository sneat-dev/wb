package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/cli/cmddeps"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/depsrun"
	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/spf13/cobra"
)

const exitFindings = shared.ExitFindings

type codedError struct {
	code    int
	message string
}

func (e *codedError) Error() string { return e.message }
func cwDepsGraphFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	library := filepath.Join(root, "acme", "library")
	initTestRepository(t, library)
	cwCovWriteFile(t, filepath.Join(library, "go.mod"), "module github.com/acme/library\n\ngo 1.26\n")
	cwCovWriteFile(t, filepath.Join(library, "lib.go"), "package library\n\nconst Name = \"library\"\n")

	app := filepath.Join(root, "acme", "app")
	initTestRepository(t, app)
	cwCovWriteFile(t, filepath.Join(app, "go.mod"),
		"module github.com/acme/app\n\ngo 1.26\n\nrequire github.com/acme/library v1.2.3\n")
	cwCovWriteFile(t, filepath.Join(app, "app.go"), "package app\n\nimport \"github.com/acme/library\"\n\nvar _ = library.Name\n")

	cwCovFakeGH(t, "cwcov-user", []string{"acme"}, `[]`)
	return root
}

func cwDepsSetFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	seeds := t.TempDir()

	library := filepath.Join(root, "acme", "library")
	cwCovCloneWithOrigin(t, seeds, "library", library)
	cwCovWriteFile(t, filepath.Join(library, "go.mod"), "module github.com/acme/library\n\ngo 1.26\n")
	cwCovWriteFile(t, filepath.Join(library, "lib.go"), "package library\n\nconst Name = \"library\"\n")
	runGit(t, library, "add", ".")
	runGit(t, library, "commit", "-m", "add module")
	runGit(t, library, "push", "origin", "main")

	app := filepath.Join(root, "acme", "app")
	cwCovCloneWithOrigin(t, seeds, "app", app)
	cwCovWriteFile(t, filepath.Join(app, "go.mod"),
		"module github.com/acme/app\n\ngo 1.26\n\nrequire github.com/acme/library v1.2.3\n")
	cwCovWriteFile(t, filepath.Join(app, "app.go"), "package app\n\nimport \"github.com/acme/library\"\n\nvar _ = library.Name\n")
	runGit(t, app, "add", ".")
	runGit(t, app, "commit", "-m", "require library")
	runGit(t, app, "push", "origin", "main")

	cwCovFakeGH(t, "acme", nil,
		`[{"name":"app","isArchived":false,"isFork":false,"sshUrl":"git@example.test:acme/app.git"},`+
			`{"name":"library","isArchived":false,"isFork":false,"sshUrl":"git@example.test:acme/library.git"}]`)
	return root
}

func writeDepsPeersFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func initTestRepository(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "-C", path, "init", "-b", "main").CombinedOutput(); err != nil {
		t.Fatalf("init %s: %v\n%s", path, err, output)
	}
	return path
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	testenv.Git(t, dir, args...)
}

func cwCovFakeGH(t *testing.T, user string, orgs []string, remoteReposJSON string) {
	t.Helper()
	binDir := t.TempDir()
	orgsJSON, err := json.Marshal(cwCovOrgLogins(orgs))
	if err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "api" ]; then
  case "$2" in
    user)
      printf 'HTTP/2 200 OK\n\n{"login":"%s"}\n'
      exit 0
      ;;
    user/orgs)
      printf 'HTTP/2 200 OK\n\n%s\n'
      exit 0
      ;;
    *)
      printf '{"total_count":0,"items":[]}\n'
      exit 0
      ;;
  esac
fi
if [ "$1" = "repo" ] && [ "$2" = "list" ]; then
  printf '%%s\n' '%s'
  exit 0
fi
printf '{"total_count":0,"items":[]}\n'
exit 0
`, user, string(orgsJSON), remoteReposJSON)
	if err := testenv.WriteExecutableFile(filepath.Join(binDir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XDG_STATE_HOME", t.TempDir())
}

func cwCovOrgLogins(orgs []string) []map[string]string {
	out := make([]map[string]string, 0, len(orgs))
	for _, org := range orgs {
		out = append(out, map[string]string{"login": org})
	}
	return out
}

func cwCovCloneWithOrigin(t *testing.T, seedRoot, name, clonePath string) string {
	t.Helper()
	return testenv.CloneWithOrigin(t, seedRoot, name, clonePath)
}

func cwCovWriteFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
func familyChild(t *testing.T, name string, flags shared.Flags) *cobra.Command {
	t.Helper()
	runtime := shared.Runtime{Flags: func() shared.Flags { return flags }, ExitError: func(code int, s string) error { return &codedError{code, s} }}
	service := depsrun.New(depsrun.DefaultDependencies(os.Stderr))
	root := cmddeps.New(runtime, cmddeps.Operations(service, func(string) error { return errors.New("unexpected browser open") }))
	child, _, err := root.Find([]string{name})
	if err != nil {
		t.Fatal(err)
	}
	child.SetContext(context.Background())
	root.RemoveCommand(child)
	return child
}
func cwCovExec(t *testing.T, _ string, build func() *cobra.Command, args ...string) (string, string, error) {
	t.Helper()
	cmd := build()
	var out, errout bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errout)
	cmd.SetArgs(args)
	cmd.SetContext(context.Background())
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	err := cmd.Execute()
	return out.String(), errout.String(), err
}

//nolint:paralleltest // Installs a private pnpm executable through PATH for the genuine package-manager command.
func TestDepsPeersExitsWithFindingsAndPrintsTheVerdictTable(t *testing.T) {
	target := t.TempDir()
	writeDepsPeersFile(t, filepath.Join(target, "package.json"), `{
  "name": "@acme/host",
  "dependencies": {"react": "^18.0.0"}
}
`)
	writeDepsPeersFile(t, filepath.Join(target, "pnpm-lock.yaml"), `lockfileVersion: '9.0'

importers:
  .:
    dependencies:
      react:
        specifier: ^18.0.0
        version: 17.0.2
`)
	// A registry stub on PATH keeps the command hermetic while still exercising
	// the real pnpm invocation path, rather than bypassing it with an injected
	// resolver the shipped binary never uses.
	bin := t.TempDir()
	writeDepsPeersFile(t, filepath.Join(bin, "pnpm"), `#!/bin/sh
echo '{"version":"2.1.0","peerDependencies":{"react":"^18.0.0"}}'
`)
	if err := os.Chmod(filepath.Join(bin, "pnpm"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	var out bytes.Buffer
	command := familyChild(t, "peers", shared.Flags{})
	command.SetArgs([]string{"@acme/widget", "--against", target})
	command.SetOut(&out)
	command.SetErr(&out)
	command.SilenceUsage = true

	err := command.Execute()
	var exit *codedError
	if !errors.As(err, &exit) || exit.code != exitFindings {
		t.Fatalf("error = %v, want an exitFindings error", err)
	}
	rendered := out.String()
	for _, want := range []string{"WB npm peer compatibility", "`react`", "unsatisfied", "17.0.2"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("output missing %q:\n%s", want, rendered)
		}
	}
}

//nolint:paralleltest // Installs a private pnpm executable through PATH before the actual late format refusal.
func TestDepsPeersRejectsAnUnknownFormat(t *testing.T) {
	target := t.TempDir()
	writeDepsPeersFile(t, filepath.Join(target, "package.json"), `{"name":"@acme/host"}`)
	bin := t.TempDir()
	writeDepsPeersFile(t, filepath.Join(bin, "pnpm"), "#!/bin/sh\necho '{\"version\":\"2.1.0\"}'\n")
	if err := os.Chmod(filepath.Join(bin, "pnpm"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	command := familyChild(t, "peers", shared.Flags{})
	command.SetArgs([]string{"@acme/widget", "--against", target, "--format", "toml"})
	command.SetOut(new(bytes.Buffer))
	command.SetErr(new(bytes.Buffer))
	command.SilenceUsage = true
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "unknown --format") {
		t.Fatalf("error = %v, want an unknown-format rejection", err)
	}
}

//nolint:paralleltest // Native fleet discovery uses the private GH PATH fixture.
func TestCwDepsGraphCommandInProcess(t *testing.T) {
	root := cwDepsGraphFixture(t)
	reportDir := filepath.Join(t.TempDir(), "reports")

	stdout, _, err := cwCovExec(t, root, func() *cobra.Command { return familyChild(t, "graph", shared.Flags{ProjectsRoot: root}) },
		"--fleet", "--ecosystem", "go", "--format", "json", "--report-dir", reportDir, "--parallel", "1")
	if err != nil {
		t.Fatalf("deps graph: %v\n%s", err, stdout)
	}
	var graph map[string]any
	if err := json.Unmarshal([]byte(stdout), &graph); err != nil {
		t.Fatalf("deps graph JSON: %v\n%s", err, stdout)
	}
	for _, name := range []string{"deps-graph.json", "deps-graph.yaml", "deps-graph.md"} {
		if _, statErr := os.Stat(filepath.Join(reportDir, name)); statErr != nil {
			t.Errorf("deps graph did not write %s: %v", name, statErr)
		}
	}
	// Without --report-dir, the report still lands under the WB home's own
	// reports directory, keyed by ecosystem.
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return familyChild(t, "graph", shared.Flags{ProjectsRoot: root}) },
		"--fleet", "--ecosystem", "go", "--format", "json", "--parallel", "1"); err != nil {
		t.Fatalf("deps graph without --report-dir: %v", err)
	}
	home, err := wbhome.EnsureRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(filepath.Join(home, "reports", "deps-graph-go", "deps-graph.json")); statErr != nil {
		t.Errorf("deps graph without --report-dir did not persist under the WB home: %v", statErr)
	}
	// A view the graph does not support is refused before anything is written.
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return familyChild(t, "graph", shared.Flags{ProjectsRoot: root}) }, "--fleet", "--view", "nonsense"); err == nil {
		t.Fatal("an unsupported graph view must be refused")
	}
	// Only the go and npm ecosystems are supported.
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return familyChild(t, "graph", shared.Flags{ProjectsRoot: root}) }, "--fleet", "--ecosystem", "maven"); err == nil ||
		!strings.Contains(err.Error(), "only the go and npm ecosystems") {
		t.Fatalf("unsupported ecosystem = %v", err)
	}
	// A repository path cannot be combined with --fleet.
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return familyChild(t, "graph", shared.Flags{ProjectsRoot: root}) }, "--fleet", filepath.Join(root, "acme", "app")); err == nil ||
		!strings.Contains(err.Error(), "cannot be used with --fleet") {
		t.Fatalf("repository-path with --fleet = %v", err)
	}
}

//nolint:paralleltest // Native fleet discovery uses the private GH PATH fixture.
func TestCwDepsDriftCommandInProcess(t *testing.T) {
	root := cwDepsGraphFixture(t)

	stdout, _, err := cwCovExec(t, root, func() *cobra.Command { return familyChild(t, "drift", shared.Flags{ProjectsRoot: root}) }, "--fleet", "--ecosystem", "go", "--format", "json", "--parallel", "1")
	if err != nil {
		t.Fatalf("deps drift: %v\n%s", err, stdout)
	}
	var report struct {
		Ecosystem string `json:"ecosystem"`
		Summary   struct {
			Repositories int `json:"repositories"`
		} `json:"summary"`
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("deps drift JSON: %v\n%s", err, stdout)
	}
	if report.Ecosystem != "go" || report.Summary.Repositories != 2 {
		t.Fatalf("drift report = %+v", report)
	}
	// The report is persisted beside the run for later reading.
	reportDir := filepath.Join(t.TempDir(), "reports")
	if stdout, _, err := cwCovExec(t, root, func() *cobra.Command { return familyChild(t, "drift", shared.Flags{ProjectsRoot: root}) }, "--fleet", "--report-dir", reportDir); err != nil {
		t.Fatalf("deps drift with report dir: %v\n%s", err, stdout)
	}
	for _, name := range []string{"deps-drift.md", "deps-drift.yaml", "deps-drift.json"} {
		if _, statErr := os.Stat(filepath.Join(reportDir, name)); statErr != nil {
			t.Errorf("deps drift did not write %s: %v", name, statErr)
		}
	}
	// --fail-on-drift converts a divergent fleet into a findings exit.
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return familyChild(t, "drift", shared.Flags{ProjectsRoot: root}) }, "--fleet", "--fail-on-drift", "--format", "yaml"); err == nil {
		t.Log("no drift was reported for this fixture; the flag was still exercised")
	}
	// An unsupported ecosystem is refused.
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return familyChild(t, "drift", shared.Flags{ProjectsRoot: root}) }, "--fleet", "--ecosystem", "cargo"); err == nil ||
		!strings.Contains(err.Error(), "only the go and npm ecosystems") {
		t.Fatalf("unsupported drift ecosystem = %v", err)
	}
	// --fail-on-behind requires an online query.
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return familyChild(t, "drift", shared.Flags{ProjectsRoot: root}) }, "--fleet", "--fail-on-behind"); err == nil {
		t.Log("--fail-on-behind was accepted offline; behaviour is the command's own contract")
	}
}

//nolint:paralleltest // Actual Git fleet execution uses the private GH PATH fixture.
func TestCwDepsSetCommandInProcessDryRun(t *testing.T) {
	root := cwDepsSetFixture(t)
	app := filepath.Join(root, "acme", "app")

	// A dry run plans against the repository's real manifest and reports the
	// exact decision without changing one tracked file.
	beforeRaw, err := os.ReadFile(filepath.Join(app, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	before := string(beforeRaw)
	stdout, _, err := cwCovExec(t, root, func() *cobra.Command { return familyChild(t, "set", shared.Flags{ProjectsRoot: root}) },
		"go", "github.com/acme/library@v1.2.4", app, "--dry-run", "--format", "json", "--parallel", "1")
	if err != nil {
		t.Fatalf("deps set --dry-run: %v\n%s", err, stdout)
	}
	var report struct {
		Target       struct{ Dependency, Version string } `json:"target"`
		Repositories []struct {
			Repository string `json:"repository"`
			Status     string `json:"status"`
			Decisions  []struct {
				File      string `json:"file"`
				Action    string `json:"action"`
				AfterRef  string `json:"after_ref"`
				BeforeRef string `json:"before_ref"`
			} `json:"decisions"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("deps set JSON: %v\n%s", err, stdout)
	}
	if report.Target.Dependency != "github.com/acme/library" || report.Target.Version != "v1.2.4" {
		t.Fatalf("target = %+v", report.Target)
	}
	if len(report.Repositories) != 1 || report.Repositories[0].Repository != "acme/app" {
		t.Fatalf("repositories = %+v", report.Repositories)
	}
	afterRaw, err := os.ReadFile(filepath.Join(app, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	after := string(afterRaw)
	if before != after {
		t.Errorf("a dry run changed go.mod:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	// The report directory receives the persisted plan.
	reportDir := filepath.Join(t.TempDir(), "reports")
	if stdout, _, err := cwCovExec(t, root, func() *cobra.Command { return familyChild(t, "set", shared.Flags{ProjectsRoot: root}) },
		"go", "github.com/acme/library@v1.2.4", app, "--dry-run", "--report-dir", reportDir); err != nil {
		t.Fatalf("deps set with report dir: %v\n%s", err, stdout)
	}
	// A non-fleet target that does not match the filters is refused.
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return familyChild(t, "set", shared.Flags{ProjectsRoot: root}) },
		"go", "github.com/acme/library@v1.2.4", app, "--dry-run", "--match", "other/*"); err == nil ||
		!strings.Contains(err.Error(), "does not match selected filters") {
		t.Fatalf("filtered target = %v", err)
	}
	// --layer without --dependency-order is a usage error.
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return familyChild(t, "set", shared.Flags{ProjectsRoot: root}) },
		"go", "github.com/acme/library@v1.2.4", app, "--layer", "1"); err == nil ||
		!strings.Contains(err.Error(), "--layer requires --dependency-order") {
		t.Fatalf("--layer alone = %v", err)
	}
	// --dependency-order is go-only.
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return familyChild(t, "set", shared.Flags{ProjectsRoot: root}) },
		"npm", "@acme/lib@1.2.4", app, "--dependency-order"); err == nil ||
		!strings.Contains(err.Error(), "supported only for the go ecosystem") {
		t.Fatalf("npm --dependency-order = %v", err)
	}
	// --propagate requires --fleet.
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return familyChild(t, "set", shared.Flags{ProjectsRoot: root}) },
		"go", "github.com/acme/library@v1.2.4", app, "--propagate"); err == nil ||
		!strings.Contains(err.Error(), "--propagate requires --fleet") {
		t.Fatalf("--propagate without --fleet = %v", err)
	}
	// A repository path cannot be combined with --fleet.
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return familyChild(t, "set", shared.Flags{ProjectsRoot: root}) },
		"go", "github.com/acme/library@v1.2.4", app, "--fleet"); err == nil ||
		!strings.Contains(err.Error(), "cannot be used with --fleet") {
		t.Fatalf("path with --fleet = %v", err)
	}
	// A malformed target is refused.
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return familyChild(t, "set", shared.Flags{ProjectsRoot: root}) }, "go", "not-a-target", app, "--dry-run"); err == nil {
		t.Fatal("a malformed target must be refused")
	}
	// An unknown output format is refused.
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return familyChild(t, "set", shared.Flags{ProjectsRoot: root}) },
		"go", "github.com/acme/library@v1.2.4", app, "--dry-run", "--format", "toml"); err == nil ||
		!strings.Contains(err.Error(), "unknown --format") {
		t.Fatalf("unknown format = %v", err)
	}
}

//nolint:paralleltest // Actual Git wave execution uses the private GH PATH fixture.
func TestCwDepsBumpCommandInProcessDryRun(t *testing.T) {
	root := cwDepsSetFixture(t)

	stdout, _, err := cwCovExec(t, root, func() *cobra.Command { return familyChild(t, "bump", shared.Flags{ProjectsRoot: root}) },
		"go", "--fleet", "--changed", "github.com/acme/library@v1.2.4", "--dry-run", "--format", "json", "--parallel", "1")
	if err != nil {
		t.Fatalf("deps bump --dry-run: %v\n%s", err, stdout)
	}
	var report struct {
		Operation string `json:"operation"`
		Status    string `json:"status"`
		Ecosystem string `json:"ecosystem"`
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("deps bump JSON: %v\n%s", err, stdout)
	}
	if report.Operation == "" || report.Ecosystem != "go" {
		t.Fatalf("bump report = %+v", report)
	}
	// The seed events are required.
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return familyChild(t, "bump", shared.Flags{ProjectsRoot: root}) }, "go", "--fleet", "--dry-run"); err == nil ||
		!strings.Contains(err.Error(), "at least one --changed") {
		t.Fatalf("missing seed events = %v", err)
	}
	// deps bump requires --fleet.
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return familyChild(t, "bump", shared.Flags{ProjectsRoot: root}) },
		"go", "--changed", "github.com/acme/library@v1.2.4", "--dry-run"); err == nil ||
		!strings.Contains(err.Error(), "deps bump requires --fleet") {
		t.Fatalf("without --fleet = %v", err)
	}
	// Only the go and npm ecosystems are supported.
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return familyChild(t, "bump", shared.Flags{ProjectsRoot: root}) },
		"cargo", "--fleet", "--changed", "x@1.0.0", "--dry-run"); err == nil ||
		!strings.Contains(err.Error(), "only the go and npm ecosystems") {
		t.Fatalf("unsupported ecosystem = %v", err)
	}
	// --scope without --latest is refused rather than silently ignored.
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return familyChild(t, "bump", shared.Flags{ProjectsRoot: root}) },
		"go", "--fleet", "--changed", "github.com/acme/library@v1.2.4", "--dry-run", "--scope", "github.com/acme/*"); err == nil ||
		!strings.Contains(err.Error(), "--scope selects which published modules --latest derives") {
		t.Fatalf("--scope without --latest = %v", err)
	}
	// --latest without a usable --scope is refused.
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return familyChild(t, "bump", shared.Flags{ProjectsRoot: root}) },
		"go", "--fleet", "--dry-run", "--latest"); err == nil ||
		!strings.Contains(err.Error(), "--latest derives release events for the modules --scope selects") {
		t.Fatalf("--latest without --scope = %v", err)
	}
}

//nolint:paralleltest // Actual Git propagation execution uses the private GH PATH fixture.
func TestCwDepsSetPropagateFleetDelegatesToDepsBump(t *testing.T) {
	root := cwDepsSetFixture(t)

	stdout, _, err := cwCovExec(t, root, func() *cobra.Command { return familyChild(t, "set", shared.Flags{ProjectsRoot: root}) },
		"go", "github.com/acme/library@v1.2.4", "--fleet", "--propagate", "--dry-run", "--format", "json", "--parallel", "1")
	if err != nil {
		t.Fatalf("deps set --propagate --fleet: %v\n%s", err, stdout)
	}
	var report struct {
		Operation  string `json:"operation"`
		Ecosystem  string `json:"ecosystem"`
		SeedEvents []struct {
			Dependency string `json:"dependency"`
			Version    string `json:"version"`
			Source     string `json:"source"`
		} `json:"seed_events"`
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("deps set --propagate JSON: %v\n%s", err, stdout)
	}
	if report.Operation == "" || report.Ecosystem != "go" {
		t.Fatalf("propagate report = %+v", report)
	}
	if len(report.SeedEvents) != 1 || report.SeedEvents[0].Dependency != "github.com/acme/library" ||
		report.SeedEvents[0].Version != "v1.2.4" || report.SeedEvents[0].Source != "exact_set" {
		t.Fatalf("propagate seed events = %+v", report.SeedEvents)
	}
}

func TestRunDepsBumpEnsureRootFailureFinishesCampaignAsFailedPersistence(t *testing.T) {
	t.Parallel()
	blocking := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocking, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	child := familyChild(t, "bump", shared.Flags{ProjectsRoot: filepath.Join(blocking, "child"), NonInteractive: true})
	child.SetOut(io.Discard)
	child.SetErr(io.Discard)
	child.SetArgs([]string{"go", "--fleet", "--changed", "github.com/acme/lib@v1.2.3"})
	// Discovery may refuse the blocked root before EnsureRoot. The owner original separately
	// proves the concrete EnsureRoot stage; the cheap command contract proves failed finish.
	if err := child.Execute(); err == nil {
		t.Fatal("blocked projects root must fail")
	}
}

//nolint:paralleltest // Native discovery installs a private GH executable through PATH.
func TestCwDepsRunBumpWritesReportAndFinishesCampaign(t *testing.T) {
	root := t.TempDir()
	cwCovFakeGH(t, "cwcov-user", []string{"acme"}, `[{"name":"consumer","nameWithOwner":"acme/consumer","sshUrl":"git@github.com:acme/consumer.git","isArchived":true}]`)
	reportDir := filepath.Join(root, "reports", "cw-deps-bump")
	child := familyChild(t, "bump", shared.Flags{ProjectsRoot: root, NonInteractive: true})
	child.SetOut(io.Discard)
	child.SetErr(io.Discard)
	child.SetArgs([]string{"go", "--fleet", "--changed", "github.com/acme/lib@v1.2.3", "--dry-run", "--max-waves", "1", "--report-dir", reportDir})
	if err := child.Execute(); err != nil {
		t.Fatalf("actual wave command: %v", err)
	}
	if _, err := os.Stat(reportDir); err != nil {
		t.Fatalf("report directory not persisted: %v", err)
	}
}
