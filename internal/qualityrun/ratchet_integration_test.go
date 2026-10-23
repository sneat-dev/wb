package qualityrun

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/testenv"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type ratchetFixtureRepo struct {
	t          *testing.T
	dir        string
	modulePath string
}

func newRatchetFixtureRepo(t *testing.T) *ratchetFixtureRepo {
	t.Helper()
	dir := t.TempDir()
	repo := &ratchetFixtureRepo{t: t, dir: dir, modulePath: "fixture.test/cliapp"}
	runCommand(t, dir, "git", "init", "-q", "--initial-branch=main")
	runCommand(t, dir, "git", "config", "user.email", "fixture@example.com")
	runCommand(t, dir, "git", "config", "user.name", "Fixture")
	repo.writeFile("go.mod", "module "+repo.modulePath+"\n\ngo 1.27\n")
	return repo
}

func (r *ratchetFixtureRepo) writeFile(relativePath, contents string) {
	r.t.Helper()
	full := filepath.Join(r.dir, relativePath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(contents), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *ratchetFixtureRepo) commitAll(message string) string {
	r.t.Helper()
	runCommand(r.t, r.dir, "git", "add", "-A")
	runCommand(r.t, r.dir, "git", "-c", "user.email=fixture@example.com", "-c", "user.name=Fixture", "commit", "-qm", message)
	return runCommand(r.t, r.dir, "git", "rev-parse", "HEAD")
}

const ratchetFixtureBaseSource = `package app

func Add(a, b int) int {
	return a + b
}

func Uncovered(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
}
`

const ratchetFixtureTestSource = `package app

import "testing"

func TestAdd(t *testing.T) {
	if Add(1, 2) != 3 {
		t.Fatal("Add(1, 2) != 3")
	}
}
`

func changedFailureDetail(result ChangedResult, err error, stderr *bytes.Buffer) string {
	text := stderr.String() + result.Findings
	if err != nil {
		text += err.Error()
	}
	return text
}
func TestCoverageChangedRejectsUnknownTarget(t *testing.T) {
	t.Parallel()
	repo := newRatchetFixtureRepo(t)
	repo.writeFile("app.go", ratchetFixtureBaseSource)
	repo.writeFile("app_test.go", ratchetFixtureTestSource)
	repo.commitAll("base")

	var stderr bytes.Buffer
	result, err := ChangedCoverage(t.Context(), ChangedRequest{Path: repo.dir, Minimum: -1, BaselineTimeout: 5 * time.Minute, Diagnostics: &stderr, Target: "does-not-exist"})
	if err == nil && result.Findings == "" {
		t.Fatal("code = 0, want nonzero for an unresolvable --target")
	}
}

func TestCoverageChangedUsesPublishedBaselineFileWhenPresent(t *testing.T) {
	t.Parallel()
	repo := newRatchetFixtureRepo(t)
	repo.writeFile("app.go", ratchetFixtureBaseSource)
	repo.writeFile("app_test.go", ratchetFixtureTestSource)
	baseSHA := repo.commitAll("base")

	runCommand(t, repo.dir, "git", "checkout", "-q", "-b", "feature")
	repo.writeFile("app.go", ratchetFixtureBaseSource+"\nfunc NewlyAdded(a, b int) int {\n\treturn a * b\n}\n")
	repo.commitAll("add NewlyAdded, untested")

	// A baseline claiming a much higher already-uncovered count must not by
	// itself make the run pass: the changed-line rule still fires.
	baselinePath := filepath.Join(t.TempDir(), "baseline.json")
	baseline := quality.PackageBaseline{SchemaVersion: 2, SHA: baseSHA, Packages: map[string]int{".": 100}}
	if err := quality.WriteBaseline(baselinePath, baseline); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	result, err := ChangedCoverage(t.Context(), ChangedRequest{Path: repo.dir, Minimum: -1, BaselineTimeout: 5 * time.Minute, Diagnostics: &stderr, Target: baseSHA, BaselineFile: baselinePath})
	if err == nil && result.Findings == "" {
		t.Fatal("code = 0, want nonzero: the changed-line rule fires regardless of a generous baseline")
	}
	if len(result.Report.Packages) != 1 || result.Report.Packages[0].BaselineUncovered != 100 {
		t.Fatalf("stdout = %q, want it to reflect the published baseline (100), not a freshly measured one", fmt.Sprintf("%+v", result.Report))
	}
}

func TestCoverageChangedRejectsMalformedBaselineFile(t *testing.T) {
	t.Parallel()
	repo := newRatchetFixtureRepo(t)
	repo.writeFile("app.go", ratchetFixtureBaseSource)
	repo.writeFile("app_test.go", ratchetFixtureTestSource)
	baseSHA := repo.commitAll("base")

	baselinePath := filepath.Join(t.TempDir(), "baseline.json")
	if err := os.WriteFile(baselinePath, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	result, err := ChangedCoverage(t.Context(), ChangedRequest{Path: repo.dir, Minimum: -1, BaselineTimeout: 5 * time.Minute, Diagnostics: &stderr, Target: baseSHA, BaselineFile: baselinePath})
	if err == nil && result.Findings == "" {
		t.Fatal("code = 0, want nonzero for a malformed --baseline-file")
	}
	if !strings.Contains(changedFailureDetail(result, err, &stderr), baselinePath) {
		t.Fatalf("stderr = %q, want it to name the malformed baseline file", changedFailureDetail(result, err, &stderr))
	}
}

func TestCoverageChangedFailsWhenGoTestFails(t *testing.T) {
	t.Parallel()
	repo := newRatchetFixtureRepo(t)
	repo.writeFile("app.go", "package app\n\nfunc Broken() int {\n") // syntax error
	baseSHA := repo.commitAll("broken")

	// --report-dir exercises the same quality.CoverWithOptions failure path
	// the sharded coverage-diagnostics manifest uses (review item 5);
	// runChangedCoverage never surfaces that manifest, since --changed
	// cannot combine with --test-shards (validateCoverageExecutionOptions),
	// so no manifest is ever written for it to find.
	reportDir := t.TempDir()
	var stderr bytes.Buffer
	result, err := ChangedCoverage(t.Context(), ChangedRequest{Path: repo.dir, Minimum: -1, BaselineTimeout: 5 * time.Minute, Diagnostics: &stderr, Target: baseSHA, ReportDir: reportDir})
	if err == nil && result.Findings == "" {
		t.Fatal("code = 0, want nonzero when go test fails to build")
	}
	if !strings.Contains(changedFailureDetail(result, err, &stderr), "coverage could not be measured") {
		t.Fatalf("stderr = %q, want it to report the measurement failure", changedFailureDetail(result, err, &stderr))
	}
}

func TestCoverageChangedEnforcesMinimumBackstop(t *testing.T) {
	t.Parallel()
	repo := newRatchetFixtureRepo(t)
	repo.writeFile("app.go", ratchetFixtureBaseSource)
	repo.writeFile("app_test.go", ratchetFixtureTestSource)
	baseSHA := repo.commitAll("base")

	runCommand(t, repo.dir, "git", "checkout", "-q", "-b", "feature")
	// No source changes at all: the ratchet itself passes (nothing changed,
	// nothing rose), but the aggregate is well under a 100% --minimum.
	repo.writeFile("README.md", "unrelated\n")
	repo.commitAll("unrelated doc change")

	baselinePath := filepath.Join(t.TempDir(), "baseline.json")
	if err := quality.WriteBaseline(baselinePath, quality.PackageBaseline{SchemaVersion: 2, Packages: map[string]int{".": 3}}); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	result, err := ChangedCoverage(t.Context(), ChangedRequest{Path: repo.dir, Minimum: 100, BaselineTimeout: 5 * time.Minute, Diagnostics: &stderr, Target: baseSHA, BaselineFile: baselinePath})
	if err == nil && result.Findings == "" {
		t.Fatal("code = 0, want nonzero: the --minimum backstop still applies under --changed")
	}
	if !strings.Contains(changedFailureDetail(result, err, &stderr), "is below required") {
		t.Fatalf("stderr = %q, want the --minimum backstop message", changedFailureDetail(result, err, &stderr))
	}
}

func TestCoverageChangedRejectsMissingGoModAtRepoPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	runCommand(t, dir, "git", "init", "-q", "--initial-branch=main")
	runCommand(t, dir, "git", "config", "user.email", "fixture@example.com")
	runCommand(t, dir, "git", "config", "user.name", "Fixture")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCommand(t, dir, "git", "add", "-A")
	runCommand(t, dir, "git", "commit", "-qm", "no go.mod here")

	var stderr bytes.Buffer
	result, err := ChangedCoverage(t.Context(), ChangedRequest{Path: dir, Minimum: -1, BaselineTimeout: 5 * time.Minute, Diagnostics: &stderr, Target: "main"})
	if err == nil && result.Findings == "" {
		t.Fatal("code = 0, want nonzero when the repository has no go.mod")
	}
	if !strings.Contains(changedFailureDetail(result, err, &stderr), "requires exactly one Go module") {
		t.Fatalf("stderr = %q, want it to explain the missing module", changedFailureDetail(result, err, &stderr))
	}
}

func TestCoverageChangedFallsBackToMeasuringMergeBaseWhenBaselineFileIsMissing(t *testing.T) {
	t.Parallel()
	repo := newRatchetFixtureRepo(t)
	repo.writeFile("app.go", ratchetFixtureBaseSource)
	repo.writeFile("app_test.go", ratchetFixtureTestSource)
	baseSHA := repo.commitAll("base")

	runCommand(t, repo.dir, "git", "checkout", "-q", "-b", "feature")
	moved := `package app

func Uncovered(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
}

func Add(a, b int) int {
	return a + b
}
`
	repo.writeFile("app.go", moved)
	repo.commitAll("move Uncovered above Add")

	missingBaseline := filepath.Join(t.TempDir(), "no-such-baseline.json")
	var stderr bytes.Buffer
	result, err := ChangedCoverage(t.Context(), ChangedRequest{Path: repo.dir, Minimum: -1, BaselineTimeout: 5 * time.Minute, Diagnostics: &stderr, Target: baseSHA, BaselineFile: missingBaseline})
	if err != nil || result.Findings != "" {
		t.Fatalf("operation error = %v, want 0\nstdout:\n%s\nstderr:\n%s", err, fmt.Sprintf("%+v", result.Report), changedFailureDetail(result, err, &stderr))
	}
	if !strings.Contains(changedFailureDetail(result, err, &stderr), "measuring merge base") {
		t.Fatalf("stderr = %q, want it to report falling back to measuring the merge base", changedFailureDetail(result, err, &stderr))
	}
}

func TestCoverageChangedPassesUnderALenientMinimum(t *testing.T) {
	t.Parallel()
	repo := newRatchetFixtureRepo(t)
	repo.writeFile("app.go", ratchetFixtureBaseSource)
	repo.writeFile("app_test.go", ratchetFixtureTestSource)
	baseSHA := repo.commitAll("base")

	runCommand(t, repo.dir, "git", "checkout", "-q", "-b", "feature")
	repo.writeFile("README.md", "unrelated\n")
	repo.commitAll("unrelated doc change")

	baselinePath := filepath.Join(t.TempDir(), "baseline.json")
	if err := quality.WriteBaseline(baselinePath, quality.PackageBaseline{SchemaVersion: 2, Packages: map[string]int{".": 3}}); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	result, err := ChangedCoverage(t.Context(), ChangedRequest{Path: repo.dir, Minimum: 0, BaselineTimeout: 5 * time.Minute, Diagnostics: &stderr, Target: baseSHA, BaselineFile: baselinePath})
	if err != nil || result.Findings != "" {
		t.Fatalf("operation error = %v, want 0 under a --minimum of 0\nstderr:\n%s", err, changedFailureDetail(result, err, &stderr))
	}
}

func TestCoverageChangedFailsClosedWhenReportDirIsBlocked(t *testing.T) {
	t.Parallel()
	repo := newRatchetFixtureRepo(t)
	repo.writeFile("app.go", ratchetFixtureBaseSource)
	repo.writeFile("app_test.go", ratchetFixtureTestSource)
	baseSHA := repo.commitAll("base")

	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	reportDir := filepath.Join(blocker, "reports")

	var stderr bytes.Buffer
	result, err := ChangedCoverage(t.Context(), ChangedRequest{Path: repo.dir, Minimum: -1, BaselineTimeout: 5 * time.Minute, Diagnostics: &stderr, Target: baseSHA, ReportDir: reportDir})
	if err == nil && result.Findings == "" {
		t.Fatal("code = 0, want nonzero when --report-dir cannot be created")
	}
}

func TestCoverageChangedFailsClosedOnMalformedRepositoryQualityPolicy(t *testing.T) {
	t.Parallel()
	repo := newRatchetFixtureRepo(t)
	repo.writeFile("app.go", ratchetFixtureBaseSource)
	repo.writeFile("app_test.go", ratchetFixtureTestSource)
	repo.writeFile(".wb/quality.yaml", "version: 2\n")
	baseSHA := repo.commitAll("base")

	var stderr bytes.Buffer
	result, err := ChangedCoverage(t.Context(), ChangedRequest{Path: repo.dir, Minimum: -1, BaselineTimeout: 5 * time.Minute, Diagnostics: &stderr, Target: baseSHA})
	if err == nil && result.Findings == "" {
		t.Fatal("code = 0, want nonzero for a malformed .wb/quality.yaml policy")
	}
	if !strings.Contains(changedFailureDetail(result, err, &stderr), "quality.yaml") {
		t.Fatalf("stderr = %q, want it to name the malformed policy file", changedFailureDetail(result, err, &stderr))
	}
}

//nolint:paralleltest // Process-wide environment changes in TestCoverageChangedFailsClosedWhenCoverageProfileIsMalformed; these rows share their parent environment and remain sequential.
func TestCoverageChangedFailsClosedWhenCoverageProfileIsMalformed(t *testing.T) {
	repo := newRatchetFixtureRepo(t)
	repo.writeFile("app.go", ratchetFixtureBaseSource)
	repo.writeFile("app_test.go", ratchetFixtureTestSource)
	baseSHA := repo.commitAll("base")
	runCommand(t, repo.dir, "git", "checkout", "-q", "-b", "feature")
	repo.writeFile("README.md", "x\n")
	repo.commitAll("doc change")

	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = test ]; then\n" +
		"  for a in \"$@\"; do\n" +
		"    case \"$a\" in -coverprofile=*) p=\"${a#-coverprofile=}\";; esac\n" +
		"  done\n" +
		"  printf 'mode: set\\nbadformat 1 1\\n' > \"$p\"\n" +
		"  exit 0\n" +
		"fi\n" +
		"exec " + realGo + " \"$@\"\n"
	shimDir := t.TempDir()
	if err := testenv.WriteExecutableFile(filepath.Join(shimDir, "go"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var stderr bytes.Buffer
	result, err := ChangedCoverage(t.Context(), ChangedRequest{Path: repo.dir, Minimum: -1, BaselineTimeout: 5 * time.Minute, Diagnostics: &stderr, Target: baseSHA})
	if err == nil && result.Findings == "" {
		t.Fatal("code = 0, want nonzero when the coverage profile fails the ratchet's stricter parse")
	}
	if !strings.Contains(changedFailureDetail(result, err, &stderr), "coverage could not be measured: invalid coverage profile") ||
		!strings.Contains(changedFailureDetail(result, err, &stderr), "missing file:range separator") {
		t.Fatalf("stderr = %q, want the malformed profile and its structural parse failure", changedFailureDetail(result, err, &stderr))
	}
}

func TestCoverageChangedFailsClosedOnUnusableBaselineArtifacts(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		contents string
	}{
		{name: "empty object", contents: "{}"},
		{name: "wrong schema version and sha", contents: `{"schema_version":2,"sha":"deadbeef","packages":{".":0}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			repo := newRatchetFixtureRepo(t)
			repo.writeFile("app.go", ratchetFixtureBaseSource)
			repo.writeFile("app_test.go", ratchetFixtureTestSource)
			baseSHA := repo.commitAll("base")

			runCommand(t, repo.dir, "git", "checkout", "-q", "-b", "feature")
			// Delete the package's only test: the uncovered count goes from
			// 0 to 1 and no non-test line changes, so only the baseline
			// count rule can catch it.
			if err := os.Remove(filepath.Join(repo.dir, "app_test.go")); err != nil {
				t.Fatal(err)
			}
			repo.commitAll("delete the only test")

			baselinePath := filepath.Join(t.TempDir(), "baseline.json")
			if err := os.WriteFile(baselinePath, []byte(c.contents), 0o644); err != nil {
				t.Fatal(err)
			}

			var stderr bytes.Buffer
			result, err := ChangedCoverage(t.Context(), ChangedRequest{Path: repo.dir, Minimum: -1, BaselineTimeout: 5 * time.Minute, Diagnostics: &stderr, Target: baseSHA, BaselineFile: baselinePath})
			if err == nil && result.Findings == "" {
				t.Fatalf("code = 0, want nonzero: an unusable baseline (%s) must not silently pass a rising uncovered count\nstdout:\n%s\nstderr:\n%s", c.name, fmt.Sprintf("%+v", result.Report), changedFailureDetail(result, err, &stderr))
			}
			if !strings.Contains(changedFailureDetail(result, err, &stderr), "rose above baseline") {
				t.Fatalf("stderr = %q, want it to report the uncovered count rising once the unusable baseline is discarded", changedFailureDetail(result, err, &stderr))
			}
		})
	}
}

func TestChangedCoverageRejectsUnreadableSelectedPackage(t *testing.T) {
	t.Parallel()
	repo := newRatchetFixtureRepo(t)
	repo.writeFile("app.go", "package app\n")
	base := repo.commitAll("base")
	profile := filepath.Join(t.TempDir(), "coverage.out")
	var output bytes.Buffer
	// Public --changed --package remains forbidden. This exercises the
	// resolved service boundary and verifies unreadable packages do not become empty.
	result, err := ChangedCoverage(t.Context(), ChangedRequest{Path: repo.dir, Target: base, Run: quality.RunOptions{GoTestPackages: []string{"./go.mod"}, CoverageProfile: profile}, Diagnostics: &output})
	if result.HasReport {
		t.Fatal("unreadable package produced a success report")
	}
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
