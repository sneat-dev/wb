package quality

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDqCovSingleWorkerNodeEnvStatesTheRequiredEnvironment(t *testing.T) {
	t.Parallel()
	got := strings.Join(SingleWorkerNodeEnv(), " ")
	if got != "CI=1 NX_DAEMON=false NX_SKIP_NX_CACHE=true" {
		t.Fatalf("SingleWorkerNodeEnv = %q, want the documented environment", got)
	}
}

func TestDqCovSortVerificationReportsOrdersByRepository(t *testing.T) {
	t.Parallel()
	reports := []VerificationReport{
		{Repository: "zulu/repo"},
		{Repository: "alpha/repo"},
		{Repository: "mike/repo"},
	}
	SortVerificationReports(reports)
	got := make([]string, len(reports))
	for index, report := range reports {
		got[index] = report.Repository
	}
	if strings.Join(got, ",") != "alpha/repo,mike/repo,zulu/repo" {
		t.Fatalf("sorted repositories = %v, want deterministic name order", got)
	}
}

func TestDqCovParseChecksDefaultsDeduplicatesAndRejects(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"", "   "} {
		checks, err := ParseChecks(value)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(checkStrings(checks), ",") != "lint,test,build" {
			t.Fatalf("ParseChecks(%q) = %v, want the conventional default", value, checks)
		}
	}
	checks, err := ParseChecks(" spec , spec ")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(checkStrings(checks), ",") != "spec" {
		t.Fatalf("checks = %v, want one deduplicated spec check", checks)
	}
	for _, value := range []string{"nope", "lint,"} {
		if _, err := ParseChecks(value); err == nil {
			t.Fatalf("ParseChecks(%q) was accepted", value)
		}
	}
}

func TestDqCovTruncateCommandDetailToBoundsEveryShape(t *testing.T) {
	t.Parallel()
	if got := truncateCommandDetailTo("anything", 0); got != "" {
		t.Fatalf("truncate(max=0) = %q, want empty", got)
	}
	if got := truncateCommandDetailTo("anything", -5); got != "" {
		t.Fatalf("truncate(max<0) = %q, want empty", got)
	}

	detail := strings.Repeat("head", 1000) + "TAIL"
	capped := truncateCommandDetailTo(detail, 2000)
	if !strings.HasPrefix(capped, detail[:250]) {
		t.Fatalf("large max did not cap the retained head: %q", capped[:60])
	}
	if !strings.Contains(capped, "TAIL") || !strings.Contains(capped, "truncated") {
		t.Fatalf("large max lost the tail or the notice: %q", capped)
	}

	tiny := truncateCommandDetailTo(detail, 48)
	if !strings.HasPrefix(tiny, detail[:12]) || !strings.Contains(tiny, "final 0 bytes") {
		t.Fatalf("degenerate max = %q, want a labelled bounded excerpt", tiny)
	}
}

func TestDqCovGoCommandAndNodeCheckCommandVariants(t *testing.T) {
	t.Parallel()
	if got := goCommand(CheckSpec, false, 0); got != nil {
		t.Fatalf("goCommand(spec) = %v, want no command", got)
	}
	if got := strings.Join(goCommand(CheckLint, false, 0), " "); got != "go vet ./..." {
		t.Fatalf("goCommand(lint) = %q", got)
	}
	if got := strings.Join(goCommand(CheckBuild, false, 0), " "); got != "go build ./..." {
		t.Fatalf("goCommand(build) = %q", got)
	}
	if got := strings.Join(nodeCheckCommand("npm", CheckTest, false, false), " "); got != "npm run test" {
		t.Fatalf("unbounded script command = %q", got)
	}
	if got := strings.Join(nodeCheckCommand("npm", CheckTest, false, true), " "); got != "npm run test --parallel=1 -- --maxWorkers=1" {
		t.Fatalf("single-worker script command = %q", got)
	}
}

func TestDqCovRunVerificationSkipsAnEmptyCommand(t *testing.T) {
	t.Parallel()
	entry := runVerification(context.Background(), RunOptions{}, "go", ".", CheckBuild, t.TempDir())
	if entry.Status != StatusSkipped || entry.Detail != "unsupported check" || entry.Command != "" {
		t.Fatalf("entry = %+v, want an explicit skip", entry)
	}
}

func TestDqCovRunShardedVerificationFailsWithoutTemporaryRoot(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	if _, _, err := runShardedVerification(context.Background(), RunOptions{}, t.TempDir()); err == nil {
		t.Fatal("an unusable temporary root was accepted")
	}
}

func TestDqCovVerifyWithOptionsFailsOnUnreadableWorkspace(t *testing.T) {
	t.Parallel()
	repository := t.TempDir()
	writeQualityFile(t, filepath.Join(repository, "go.work"), "this is not a go.work file\n")
	report := VerifyWithOptions(context.Background(), "example/broken", repository, []Check{CheckBuild}, RunOptions{})
	if report.Status != StatusFailed || len(report.Results) != 1 || report.Results[0].Language != "go" || !strings.Contains(report.Results[0].Detail, "parse go.work") {
		t.Fatalf("report = %+v, want one go discovery failure", report)
	}
}

// TestDqCovVerifyWithOptionsSkipsSpecInsideGoAndNodeLoops pins that a spec-
// only request never becomes a Go or Node command, while a Node project with a
// missing script is recorded as an explicit skip.
func TestDqCovVerifyWithOptionsSkipsSpecInsideGoAndNodeLoops(t *testing.T) {
	t.Parallel()
	t.Run("go module with spec check only", func(t *testing.T) {
		t.Parallel()
		repository := t.TempDir()
		writeQualityFile(t, filepath.Join(repository, "go.mod"), "module example.test/spec-only\n\ngo 1.24\n")
		report := VerifyWithOptions(context.Background(), "example/spec-only", repository, []Check{CheckSpec}, RunOptions{})
		if report.Status != StatusPassed || len(report.Results) != 1 {
			t.Fatalf("report = %+v, want only the spec skip", report)
		}
		if result := report.Results[0]; result.Language != "specscore" || result.Status != StatusSkipped {
			t.Fatalf("result = %+v, want the go loop to skip spec", result)
		}
	})

	t.Run("node project with a missing script", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("test shell helper is POSIX-only")
		}
		repository := t.TempDir()
		writeQualityFile(t, filepath.Join(repository, "package.json"), `{"scripts":{"lint":"x"}}`)
		writeQualityFile(t, filepath.Join(repository, "pnpm-lock.yaml"), "lockfileVersion: '9.0'\n")
		bin := filepath.Join(t.TempDir(), "bin")
		if err := os.MkdirAll(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		writeQualityFile(t, filepath.Join(bin, "pnpm"), "#!/bin/sh\nexit 0\n")
		if err := os.Chmod(filepath.Join(bin, "pnpm"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

		report := VerifyWithOptions(context.Background(), "example/node-gap", repository, []Check{CheckLint, CheckTest, CheckSpec}, RunOptions{})
		if report.Status != StatusPassed || len(report.Results) != 4 {
			t.Fatalf("report = %+v, want install, lint, test skip, spec skip", report)
		}
		statuses := map[Check]Status{}
		for _, result := range report.Results {
			if result.Language == "node" {
				statuses[result.Check] = result.Status
			}
		}
		if statuses[CheckLint] != StatusPassed || statuses[CheckTest] != StatusSkipped {
			t.Fatalf("node statuses = %v, want lint passed and test skipped", statuses)
		}
	})
}

func TestDqCovVerifyWithOptionsFailsOnMalformedNodeManifest(t *testing.T) {
	t.Parallel()
	repository := t.TempDir()
	writeQualityFile(t, filepath.Join(repository, "package.json"), "{not json")
	report := VerifyWithOptions(context.Background(), "example/node-broken", repository, []Check{CheckBuild}, RunOptions{})
	if report.Status != StatusFailed || len(report.Results) != 1 || report.Results[0].Language != "node" || !strings.Contains(report.Results[0].Detail, "parse package.json") {
		t.Fatalf("report = %+v, want one node discovery failure", report)
	}
}

func TestDqCovVerifyWithOptionsReturnsSkippedWhenNothingApplies(t *testing.T) {
	t.Parallel()
	repository := t.TempDir()
	report := VerifyWithOptions(context.Background(), "example/nothing", repository, []Check{CheckTest}, RunOptions{})
	if report.Status != StatusSkipped || len(report.Results) != 0 {
		t.Fatalf("report = %+v, want an empty skipped report", report)
	}
	if report.Repository != "example/nothing" || report.Path != repository {
		t.Fatalf("report identity = %+v, want the caller's repository and path", report)
	}
}

// TestDqCovSpecLintOrSkipInspectFailures covers the two inspection errors that
// force a failed spec check rather than a silent skip.
func TestDqCovSpecLintOrSkipInspectFailures(t *testing.T) {
	t.Parallel()
	t.Run("unreadable repository root", func(t *testing.T) {
		t.Parallel()
		repository := t.TempDir()
		dqCovChmod(t, repository, 0)
		entry := specLintOrSkip(context.Background(), RunOptions{}, repository, filepath.Join(repository, "spec"))
		if entry.Status != StatusFailed || !strings.Contains(entry.Detail, "inspect SpecScore config") {
			t.Fatalf("entry = %+v, want a config inspection failure", entry)
		}
	})

	t.Run("unreadable plans layout", func(t *testing.T) {
		t.Parallel()
		repository := t.TempDir()
		writeQualityFile(t, filepath.Join(repository, ".gitignore"), externalStoreLifecycleLockRule+"\n")
		entry := specLintOrSkip(context.Background(), RunOptions{}, repository, filepath.Join(repository, "spec"))
		if entry.Status != StatusFailed || !strings.Contains(entry.Detail, "inspect SpecScore Plans layout") {
			t.Fatalf("entry = %+v, want a layout inspection failure", entry)
		}
	})
}

func TestDqCovIsExternalPlansStoreSurfacesWalkFailure(t *testing.T) {
	t.Parallel()
	repository := t.TempDir()
	writeQualityFile(t, filepath.Join(repository, ".gitignore"), externalStoreLifecycleLockRule+"\n")
	writeQualityFile(t, filepath.Join(repository, "spec", "plans", "README.md"), "# Plans\n")
	denied := filepath.Join(repository, "spec", "denied")
	if err := os.Mkdir(denied, 0o755); err != nil {
		t.Fatal(err)
	}
	dqCovChmod(t, denied, 0)
	if external, err := isExternalPlansStore(repository, filepath.Join(repository, "spec")); err == nil || external {
		t.Fatalf("isExternalPlansStore = %v err=%v, want an unreadable tree to fail closed", external, err)
	}
}

// TestDqCovIgnoresLifecycleLockInspectErrors covers the inspection failures and
// the exact-line comparison, including a trailing CR and trailing spaces.
func TestDqCovIgnoresLifecycleLockInspectErrors(t *testing.T) {
	t.Parallel()
	t.Run("unreadable parent", func(t *testing.T) {
		t.Parallel()
		repository := t.TempDir()
		dqCovChmod(t, repository, 0)
		if _, err := ignoresLifecycleLock(filepath.Join(repository, ".gitignore")); err == nil {
			t.Fatal("an unreadable parent directory was accepted")
		}
	})

	t.Run("unreadable gitignore", func(t *testing.T) {
		t.Parallel()
		repository := t.TempDir()
		path := filepath.Join(repository, ".gitignore")
		writeQualityFile(t, path, externalStoreLifecycleLockRule+"\n")
		dqCovChmod(t, path, 0)
		if _, err := ignoresLifecycleLock(path); err == nil {
			t.Fatal("an unreadable .gitignore was accepted")
		}
	})

	t.Run("exact rule with git line endings", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), ".gitignore")
		writeQualityFile(t, path, "node_modules/\r\n"+externalStoreLifecycleLockRule+"  \r\n")
		ignored, err := ignoresLifecycleLock(path)
		if err != nil || !ignored {
			t.Fatalf("ignored=%v err=%v, want the anchored rule recognized", ignored, err)
		}
	})

	t.Run("unanchored rule is not the rule", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), ".gitignore")
		writeQualityFile(t, path, strings.TrimPrefix(externalStoreLifecycleLockRule, "/")+"\n")
		ignored, err := ignoresLifecycleLock(path)
		if err != nil || ignored {
			t.Fatalf("ignored=%v err=%v, want the unanchored rule rejected", ignored, err)
		}
	})
}

func TestDqCovNodeProjectReadAndParseFailures(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if _, err := nodeProject(root, filepath.Join(root, "missing", "package.json"), false); err == nil {
		t.Fatal("a missing manifest was accepted")
	}
	broken := filepath.Join(root, "package.json")
	writeQualityFile(t, broken, "{not json")
	if _, err := nodeProject(root, broken, false); err == nil || !strings.Contains(err.Error(), "parse package.json") {
		t.Fatalf("error = %v, want a parse failure", err)
	}
	writeQualityFile(t, broken, `{"scripts":{"test":"x"}}`)
	if err := os.Mkdir(filepath.Join(root, "nx.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := nodeProject(root, broken, true)
	if err != nil || project.Nx || !project.Scripts["test"] || !project.Locked || project.Path != root {
		t.Fatalf("project = %+v err=%v, want a non-Nx locked project", project, err)
	}

	// The manifest itself is readable, but its directory can never hold
	// nx.json: the inspection error must be reported, not read as "no Nx".
	notADirectory := filepath.Join(root, "root-is-a-file")
	writeQualityFile(t, notADirectory, "x")
	if _, err := nodeProject(notADirectory, broken, false); err == nil || !strings.Contains(err.Error(), "inspect nx.json") {
		t.Fatalf("error = %v, want the nx.json inspection failure", err)
	}
}

// TestDqCovVerifyWithOptionsFailsOnUninspectableSpecRoot covers a spec/ entry
// that exists but cannot be resolved (a symlink loop). The check must fail
// closed rather than report lint as inapplicable.
func TestDqCovVerifyWithOptionsFailsOnUninspectableSpecRoot(t *testing.T) {
	t.Parallel()
	repository := t.TempDir()
	writeQualityFile(t, filepath.Join(repository, "go.mod"), "module example.test/spec-loop\n\ngo 1.24\n")
	if err := os.Symlink("spec", filepath.Join(repository, "spec")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	report := VerifyWithOptions(context.Background(), "example/spec-loop", repository, []Check{CheckSpec}, RunOptions{})
	if report.Status != StatusFailed || len(report.Results) != 1 {
		t.Fatalf("report = %+v, want one failed spec result", report)
	}
	result := report.Results[0]
	if result.Language != "specscore" || result.Status != StatusFailed || !strings.Contains(result.Detail, "inspect SpecScore root") {
		t.Fatalf("result = %+v, want an uninspectable-root failure", result)
	}
}

// TestDqCovNodeProjectsSelectsLockedScopesAndSurfacesErrors covers the
// lockfile-scope selection rules and both error paths.
func TestDqCovNodeProjectsSelectsLockedScopesAndSurfacesErrors(t *testing.T) {
	t.Parallel()
	t.Run("root manifest without lockfile", func(t *testing.T) {
		t.Parallel()
		repository := t.TempDir()
		writeQualityFile(t, filepath.Join(repository, "package.json"), `{"scripts":{"test":"x"}}`)
		projects, ok, err := nodeProjects(repository)
		if err != nil || !ok || len(projects) != 1 {
			t.Fatalf("projects = %+v ok=%v err=%v, want the unlocked root selected", projects, ok, err)
		}
		if projects[0].Locked || projects[0].Module != "." {
			t.Fatalf("project = %+v, want an unlocked root scope", projects[0])
		}
	})

	t.Run("independent locked scopes sorted", func(t *testing.T) {
		t.Parallel()
		repository := t.TempDir()
		writeQualityFile(t, filepath.Join(repository, "package.json"), `{"scripts":{"test":"x"}}`)
		writeQualityFile(t, filepath.Join(repository, "b", "package.json"), `{"scripts":{"test":"x"}}`)
		writeQualityFile(t, filepath.Join(repository, "b", "yarn.lock"), "")
		writeQualityFile(t, filepath.Join(repository, "a", "package.json"), `{"scripts":{"test":"x"}}`)
		writeQualityFile(t, filepath.Join(repository, "a", "pnpm-lock.yaml"), "")
		projects, ok, err := nodeProjects(repository)
		if err != nil || !ok || len(projects) != 3 {
			t.Fatalf("projects = %+v ok=%v err=%v", projects, ok, err)
		}
		if projects[0].Module != "." || projects[1].Module != "a" || projects[2].Module != "b" {
			t.Fatalf("modules = %q,%q,%q, want deterministic scope order", projects[0].Module, projects[1].Module, projects[2].Module)
		}
		if !projects[1].Locked || !projects[2].Locked || projects[0].Locked {
			t.Fatalf("locked flags = %v, want only the nested scopes locked", projects)
		}
	})

	t.Run("malformed nested manifest fails", func(t *testing.T) {
		t.Parallel()
		repository := t.TempDir()
		writeQualityFile(t, filepath.Join(repository, "package.json"), `{"scripts":{"test":"x"}}`)
		writeQualityFile(t, filepath.Join(repository, "nested", "package.json"), "{not json")
		writeQualityFile(t, filepath.Join(repository, "nested", "pnpm-lock.yaml"), "")
		if _, _, err := nodeProjects(repository); err == nil {
			t.Fatal("a malformed nested manifest was accepted")
		}
	})

	t.Run("unreadable subtree fails", func(t *testing.T) {
		t.Parallel()
		repository := t.TempDir()
		writeQualityFile(t, filepath.Join(repository, "package.json"), `{"scripts":{"test":"x"}}`)
		denied := filepath.Join(repository, "denied")
		if err := os.Mkdir(denied, 0o755); err != nil {
			t.Fatal(err)
		}
		dqCovChmod(t, denied, 0)
		if _, _, err := nodeProjects(repository); err == nil {
			t.Fatal("an unreadable subtree was accepted")
		}
	})
}

func TestDqCovDetectPackageManagerHonorsDeclarationThenLockfiles(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name               string
		declared, lockfile string
		want               string
	}{
		{name: "declared with version", declared: "pnpm@9.0.0", want: "pnpm"},
		{name: "declared plain", declared: "yarn", want: "yarn"},
		{name: "unknown declaration falls back to lockfile", declared: "corepack@1", lockfile: "bun.lock", want: "bun"},
		{name: "yarn lockfile", lockfile: "yarn.lock", want: "yarn"},
		{name: "no evidence defaults to npm", want: "npm"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if tc.lockfile != "" {
				writeQualityFile(t, filepath.Join(root, tc.lockfile), "")
			}
			if got := detectPackageManager(root, tc.declared); got != tc.want {
				t.Fatalf("detectPackageManager = %q, want %q", got, tc.want)
			}
		})
	}
}
