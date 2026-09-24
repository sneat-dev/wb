package quality

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

func TestCoverAggregatesGoStatements(t *testing.T) {
	t.Parallel()
	repository := t.TempDir()
	writeQualityFile(t, filepath.Join(repository, "go.mod"), "module example.test/coverage\n\ngo 1.26\n")
	writeQualityFile(t, filepath.Join(repository, "coverage.go"), "package coverage\n\nfunc Covered() int { return 1 }\nfunc Uncovered() int { return 2 }\n")
	writeQualityFile(t, filepath.Join(repository, "coverage_test.go"), "package coverage\n\nimport \"testing\"\n\nfunc TestCovered(t *testing.T) { if Covered() != 1 { t.Fatal(\"unexpected\") } }\n")

	var progress []Progress
	report := CoverWithOptions(context.Background(), "example/coverage", repository, RunOptions{Progress: func(event Progress) {
		progress = append(progress, event)
	}})
	if report.Status != StatusPassed {
		t.Fatalf("status = %s: %s", report.Status, report.Error)
	}
	if len(report.Modules) != 1 || report.Statements == 0 || report.Covered == 0 || report.Covered >= report.Statements {
		t.Fatalf("coverage = %+v", report)
	}
	combined := NewCoverageReport([]RepositoryCoverage{report})
	if combined.Statements != report.Statements || combined.Percentage != report.Percentage {
		t.Fatalf("combined report = %+v", combined)
	}
	if len(progress) != 2 || progress[0].State != ProgressStarted || progress[1].State != ProgressCompleted || progress[1].Status != StatusPassed {
		t.Fatalf("coverage progress = %+v", progress)
	}
}

func TestProfileTotals(t *testing.T) {
	t.Parallel()
	profile := filepath.Join(t.TempDir(), "coverage.out")
	writeQualityFile(t, profile, "mode: set\nexample.go:1.1,1.2 3 1\nexample.go:2.1,2.2 2 0\n")
	statements, covered, err := profileTotals(profile)
	if err != nil {
		t.Fatal(err)
	}
	if statements != 5 || covered != 3 || percent(covered, statements) != 60 {
		t.Fatalf("totals = %d/%d (%.2f%%)", covered, statements, percent(covered, statements))
	}
}

func TestVerifyRunsNodeScriptsWithDetectedPackageManager(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test shell helper is POSIX-only")
	}
	repository := t.TempDir()
	writeQualityFile(t, filepath.Join(repository, "package.json"), `{"scripts":{"lint":"x","test":"x","build":"x"}}`)
	writeQualityFile(t, filepath.Join(repository, "pnpm-lock.yaml"), "lockfileVersion: '9.0'\n")
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(repository, "commands.log")
	writeQualityFile(t, filepath.Join(bin, "pnpm"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \""+log+"\"\n")
	if err := os.Chmod(filepath.Join(bin, "pnpm"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	var progress []Progress
	report := VerifyWithOptions(context.Background(), "example/node", repository, []Check{CheckLint, CheckTest, CheckBuild}, RunOptions{Progress: func(event Progress) {
		progress = append(progress, event)
	}})
	if report.Status != StatusPassed || len(report.Results) != 4 {
		t.Fatalf("report = %+v", report)
	}
	contents, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(contents)), "install --frozen-lockfile\nrun lint\nrun test\nrun build"; got != want {
		t.Fatalf("commands = %q, want %q", got, want)
	}
	if len(progress) != 8 {
		t.Fatalf("verification progress events = %d, want 8: %+v", len(progress), progress)
	}
	for index, event := range progress {
		want := ProgressStarted
		if index%2 == 1 {
			want = ProgressCompleted
		}
		if event.State != want {
			t.Fatalf("verification progress event %d state = %s, want %s", index, event.State, want)
		}
	}
}

func TestVerifyRunsEveryConfiguredGoLintCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test shell helper is POSIX-only")
	}
	repository := t.TempDir()
	writeQualityFile(t, filepath.Join(repository, "go.mod"), "module example.test/lint\n\ngo 1.27\n")
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(repository, "commands.log")
	writeQualityFile(t, filepath.Join(bin, "go"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \""+log+"\"\n")
	if err := os.Chmod(filepath.Join(bin, "go"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	report := VerifyWithOptions(context.Background(), "example/lint", repository, []Check{CheckLint}, RunOptions{
		GoLintCommands: [][]string{{"go", "vet", "./..."}, {"go", "run", "example.test/linter@v1", "run", "./..."}},
	})
	if report.Status != StatusPassed || len(report.Results) != 2 {
		t.Fatalf("report = %+v", report)
	}
	contents, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(contents)), "vet ./...\nrun example.test/linter@v1 run ./..."; got != want {
		t.Fatalf("commands = %q, want %q", got, want)
	}
}

func TestVerifyRunsNxTargetsWhenRootScriptsAreAbsent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test shell helper is POSIX-only")
	}
	repository := t.TempDir()
	writeQualityFile(t, filepath.Join(repository, "package.json"), `{"devDependencies":{"nx":"22.0.0"}}`)
	writeQualityFile(t, filepath.Join(repository, "nx.json"), `{}`)
	writeQualityFile(t, filepath.Join(repository, "pnpm-lock.yaml"), "lockfileVersion: '9.0'\n")
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(repository, "commands.log")
	writeQualityFile(t, filepath.Join(bin, "pnpm"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \""+log+"\"\n")
	if err := os.Chmod(filepath.Join(bin, "pnpm"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeQualityFile(t, filepath.Join(bin, "node"), "#!/bin/sh\nprintf 'node %s\\n' \"$*\" >> \""+log+"\"\n")
	if err := os.Chmod(filepath.Join(bin, "node"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	report := Verify(context.Background(), "example/nx", repository, []Check{CheckLint, CheckTest, CheckBuild})
	if report.Status != StatusPassed || len(report.Results) != 4 {
		t.Fatalf("report = %+v", report)
	}
	for _, result := range report.Results {
		if result.Status != StatusPassed {
			t.Fatalf("Nx verification did not run: %+v", result)
		}
	}
	contents, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"install --frozen-lockfile",
		"node node_modules/nx/dist/bin/nx.js run-many --target=lint --all --skip-nx-cache",
		"node node_modules/nx/dist/bin/nx.js run-many --target=test --all --skip-nx-cache",
		"node node_modules/nx/dist/bin/nx.js run-many --target=build --all --skip-nx-cache",
	}, "\n")
	if got := strings.TrimSpace(string(contents)); got != want {
		t.Fatalf("commands = %q, want %q", got, want)
	}
}

// TestVerifyPreparesEveryIndependentNodeScopeBeforeScripts exercises the same
// verifier used by deps set and deps bump after they create an empty linked
// worktree. The shim refuses to run a script until its frozen install has
// created a project-local nx executable, so a passing result proves both
// preparation and local executable resolution rather than just command order.
func TestVerifyPreparesEveryIndependentNodeScopeBeforeScripts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test shell helper is POSIX-only")
	}
	repository := t.TempDir()
	for _, scope := range []string{"", "landings"} {
		writeQualityFile(t, filepath.Join(repository, scope, "package.json"), `{"scripts":{"lint":"nx lint","test":"nx test","build":"nx build"}}`)
		writeQualityFile(t, filepath.Join(repository, scope, "pnpm-lock.yaml"), "lockfileVersion: '9.0'\n")
	}
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(repository, "commands.log")
	writeQualityFile(t, filepath.Join(bin, "pnpm"), `#!/bin/sh
scope=$(pwd)
case "$1" in
  install)
    if [ -e node_modules ]; then echo "install found existing node_modules: $scope" >&2; exit 1; fi
    if [ "$2" != "--frozen-lockfile" ]; then echo "install was not frozen: $*" >&2; exit 1; fi
    mkdir -p node_modules/.bin
    printf '%s\n' '#!/bin/sh' 'printf "local nx %s %s\n" "$(pwd)" "$*" >> "`+log+`"' > node_modules/.bin/nx
    chmod +x node_modules/.bin/nx
    printf 'install %s\n' "$scope" >> "`+log+`"
    ;;
  run)
    if [ ! -x node_modules/.bin/nx ]; then echo "missing local nx: $scope" >&2; exit 1; fi
    node_modules/.bin/nx "$2"
    ;;
  *) echo "unexpected pnpm command: $*" >&2; exit 1 ;;
esac
`)
	if err := os.Chmod(filepath.Join(bin, "pnpm"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	report := Verify(context.Background(), "example/node-scopes", repository, []Check{CheckLint, CheckTest, CheckBuild})
	if report.Status != StatusPassed {
		t.Fatalf("report = %+v", report)
	}
	contents, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{repository, filepath.Join(repository, "landings")} {
		if !strings.Contains(string(contents), "install "+scope) {
			t.Fatalf("scope %s was not prepared from absent node_modules:\n%s", scope, contents)
		}
		for _, script := range []string{"lint", "test", "build"} {
			if !strings.Contains(string(contents), "local nx "+scope+" "+script) {
				t.Fatalf("scope %s did not run local nx %s:\n%s", scope, script, contents)
			}
		}
	}
}

// TestVerifyUsesGoWorkspaceModules verifies the verifier's production
// discovery and execution path. A template go.mod is deliberately valid but
// absent from go.work, whereas backend is an admitted workspace module.
func TestVerifyUsesGoWorkspaceModules(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test shell helper is POSIX-only")
	}
	repository := t.TempDir()
	writeQualityFile(t, filepath.Join(repository, "go.work"), "go 1.26\n\nuse ./backend\n")
	writeQualityFile(t, filepath.Join(repository, "backend", "go.mod"), "module example.test/backend\n\ngo 1.26\n")
	writeQualityFile(t, filepath.Join(repository, "tools", "contract-generator", "src", "generators", "contract", "files-go", "go.mod"), "module example.test/template\n\ngo 1.26\n")
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(repository, "go-commands.log")
	writeQualityFile(t, filepath.Join(bin, "go"), "#!/bin/sh\nprintf '%s %s\\n' \"$(pwd)\" \"$*\" >> \""+log+"\"\n")
	if err := os.Chmod(filepath.Join(bin, "go"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	report := Verify(context.Background(), "example/go-workspace", repository, []Check{CheckLint, CheckTest, CheckBuild})
	if report.Status != StatusPassed || len(report.Results) != 3 {
		t.Fatalf("report = %+v", report)
	}
	for _, result := range report.Results {
		if result.Module != "backend" || result.Status != StatusPassed {
			t.Fatalf("workspace verifier result = %+v, want admitted backend only", result)
		}
	}
	contents, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), "files-go") || strings.Count(strings.TrimSpace(string(contents)), "\n") != 2 {
		t.Fatalf("template fixture entered Go verification:\n%s", contents)
	}
}

// TestVerifyDiscoversStandaloneGoModulesWithoutWorkspace preserves verification
// for repositories that deliberately have no go.work. In that case every real
// standalone module remains an execution target.
func TestVerifyDiscoversStandaloneGoModulesWithoutWorkspace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test shell helper is POSIX-only")
	}
	repository := t.TempDir()
	writeQualityFile(t, filepath.Join(repository, "tool", "go.mod"), "module example.test/tool\n\ngo 1.26\n")
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeQualityFile(t, filepath.Join(bin, "go"), "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(filepath.Join(bin, "go"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	report := Verify(context.Background(), "example/standalone", repository, []Check{CheckLint, CheckTest, CheckBuild})
	if report.Status != StatusPassed || len(report.Results) != 3 {
		t.Fatalf("report = %+v", report)
	}
	for _, result := range report.Results {
		if result.Module != "tool" || result.Status != StatusPassed {
			t.Fatalf("standalone module was not verified: %+v", result)
		}
	}
}

func TestNodeInstallCommandUsesLockedPackageManagerSemantics(t *testing.T) {
	t.Parallel()
	for manager, want := range map[string]string{
		"npm":  "npm ci",
		"pnpm": "pnpm install --frozen-lockfile",
		"yarn": "yarn install --frozen-lockfile",
		"bun":  "bun install --frozen-lockfile",
	} {
		if got := strings.Join(nodeInstallCommand(manager), " "); got != want {
			t.Errorf("nodeInstallCommand(%q) = %q, want %q", manager, got, want)
		}
	}
}

func TestNodeCheckCommandBoundsNxWithoutForwardingExecutorSpecificFlags(t *testing.T) {
	t.Parallel()
	got := strings.Join(nodeCheckCommand("pnpm", CheckLint, true, true), " ")
	want := "node node_modules/nx/dist/bin/nx.js run-many --target=lint --all --skip-nx-cache --parallel=1"
	if got != want {
		t.Fatalf("single-worker Nx command = %q, want %q", got, want)
	}
}

func TestVerifySpecScoreConfiguration(t *testing.T) {
	t.Run("configured missing root fails closed", func(t *testing.T) {
		t.Parallel()
		repository := t.TempDir()
		writeQualityFile(t, filepath.Join(repository, "specscore.yaml"), "project:\n  slug: example\n")

		report := Verify(context.Background(), "example/configured-spec", repository, []Check{CheckSpec})
		if report.Status != StatusFailed || len(report.Results) != 1 {
			t.Fatalf("report = %+v", report)
		}
		result := report.Results[0]
		if result.Status != StatusFailed || !strings.Contains(result.Detail, "specscore.yaml") || !strings.Contains(result.Detail, "spec") {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("unconfigured missing root remains non-applicable", func(t *testing.T) {
		t.Parallel()
		repository := t.TempDir()

		report := Verify(context.Background(), "example/no-spec", repository, []Check{CheckSpec})
		if report.Status != StatusPassed || len(report.Results) != 1 {
			t.Fatalf("report = %+v", report)
		}
		if result := report.Results[0]; result.Status != StatusSkipped {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("existing root runs lint", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("test shell helper is POSIX-only")
		}
		repository := t.TempDir()
		writeQualityFile(t, filepath.Join(repository, "specscore.yaml"), "project:\n  slug: example\n")
		if err := os.MkdirAll(filepath.Join(repository, "spec"), 0o755); err != nil {
			t.Fatal(err)
		}
		bin := filepath.Join(t.TempDir(), "bin")
		if err := os.MkdirAll(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		log := filepath.Join(repository, "commands.log")
		writeQualityFile(t, filepath.Join(bin, "specscore"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \""+log+"\"\n")
		if err := os.Chmod(filepath.Join(bin, "specscore"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

		report := Verify(context.Background(), "example/spec", repository, []Check{CheckSpec})
		if report.Status != StatusPassed || len(report.Results) != 1 || report.Results[0].Status != StatusPassed {
			t.Fatalf("report = %+v", report)
		}
		contents, err := os.ReadFile(log)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := strings.TrimSpace(string(contents)), "spec lint"; got != want {
			t.Fatalf("command = %q, want %q", got, want)
		}
	})

	// The remaining subtests cover the external SpecScore Plans store shape
	// (sneat-co/workbench is the canonical example): SpecScore routes other
	// projects' Plans there under spec/plans/{host}/{owner}/{repo}/... (see
	// specscore/specscore spec/features/repo-config "Plan repository routing"
	// and spec/features/plan REQ:external-source-namespace). SpecScore lint
	// does not apply to that layout: with no specscore.yaml it cannot run, and
	// with one it reports structural violations. TestVerifyExternalPlansStore
	// pins the boundaries of the carve-out.

	t.Run("external plans store without config is skipped", func(t *testing.T) {
		t.Parallel()
		repository := t.TempDir()
		writeExternalPlansStore(t, repository)

		report := Verify(context.Background(), "sneat-co/workbench", repository, []Check{CheckSpec})
		if report.Status != StatusPassed || len(report.Results) != 1 {
			t.Fatalf("report = %+v", report)
		}
		result := report.Results[0]
		if result.Status != StatusSkipped {
			t.Fatalf("result = %+v", result)
		}
		for _, want := range []string{"external SpecScore Plans store", "specscore.yaml", "does not apply", "wb does not validate"} {
			if !strings.Contains(result.Detail, want) {
				t.Fatalf("detail = %q, want it to contain %q", result.Detail, want)
			}
		}
		if strings.Contains(result.Detail, "validated from") {
			t.Fatalf("detail = %q claims the Plans are validated elsewhere", result.Detail)
		}
	})

	t.Run("spec tree without config still runs lint", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("test shell helper is POSIX-only")
		}
		repository := t.TempDir()
		writeQualityFile(t, filepath.Join(repository, ".gitignore"), externalStoreLifecycleLockRule+"\n")
		writeQualityFile(t, filepath.Join(repository, "spec", "features", "example", "README.md"), "# Example\n")
		log := stubSpecscoreBinary(t, repository)

		assertSpecLintRan(t, Verify(context.Background(), "example/features-only", repository, []Check{CheckSpec}), log)
	})

	t.Run("external plans mixed with another spec subtree runs lint", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("test shell helper is POSIX-only")
		}
		repository := t.TempDir()
		writeExternalPlansStore(t, repository)
		writeQualityFile(t, filepath.Join(repository, "spec", "features", "example", "README.md"), "# Example\n")
		log := stubSpecscoreBinary(t, repository)

		assertSpecLintRan(t, Verify(context.Background(), "example/mixed-spec", repository, []Check{CheckSpec}), log)
	})

	t.Run("flat plan file without config runs lint", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("test shell helper is POSIX-only")
		}
		repository := t.TempDir()
		writeQualityFile(t, filepath.Join(repository, ".gitignore"), externalStoreLifecycleLockRule+"\n")
		writeQualityFile(t, filepath.Join(repository, "spec", "plans", "user-auth.md"), "# User auth\n")
		log := stubSpecscoreBinary(t, repository)

		assertSpecLintRan(t, Verify(context.Background(), "example/flat-plan", repository, []Check{CheckSpec}), log)
	})
}

// TestVerifyExternalPlansStore runs the spec check against repository trees
// on either side of the external Plans-store carve-out. Every tree that is
// not exactly an external store must run specscore spec lint (fail closed);
// a stub specscore records whether it ran.
func TestVerifyExternalPlansStore(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test shell helper and symlinks are POSIX-only")
	}
	symlink := func(t *testing.T, target, link string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	mkdir := func(t *testing.T, dir string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name     string
		setup    func(t *testing.T, repository string)
		wantLint bool
	}{
		{
			name:  "external store is skipped",
			setup: writeExternalPlansStore,
		},
		{
			name: "namespace index alone is skipped",
			setup: func(t *testing.T, repository string) {
				writeQualityFile(t, filepath.Join(repository, ".gitignore"), "node_modules/\n"+externalStoreLifecycleLockRule+"\n")
				writeQualityFile(t, filepath.Join(repository, "spec", "plans", "github.com", "o", "r", "README.md"), "# r\n")
			},
		},
		{
			name: "CRLF gitignore line is accepted as git reads it",
			setup: func(t *testing.T, repository string) {
				writeExternalPlansStore(t, repository)
				writeQualityFile(t, filepath.Join(repository, ".gitignore"), "# Plans store\r\n"+externalStoreLifecycleLockRule+"  \r\n")
			},
		},
		{
			name: "config present with external layout runs lint",
			setup: func(t *testing.T, repository string) {
				writeExternalPlansStore(t, repository)
				writeQualityFile(t, filepath.Join(repository, "specscore.yaml"), "project:\n  slug: example\n")
			},
			wantLint: true,
		},
		{
			name: "symlinked specscore.yaml runs lint",
			setup: func(t *testing.T, repository string) {
				writeExternalPlansStore(t, repository)
				writeQualityFile(t, filepath.Join(repository, "real.yaml"), "project:\n  slug: example\n")
				symlink(t, "real.yaml", filepath.Join(repository, "specscore.yaml"))
			},
			wantLint: true,
		},
		{
			name: "dangling specscore.yaml symlink runs lint",
			setup: func(t *testing.T, repository string) {
				writeExternalPlansStore(t, repository)
				symlink(t, "missing.yaml", filepath.Join(repository, "specscore.yaml"))
			},
			wantLint: true,
		},
		{
			name: "symlinked spec runs lint",
			setup: func(t *testing.T, repository string) {
				source := t.TempDir()
				writeExternalPlansStore(t, source)
				writeQualityFile(t, filepath.Join(repository, ".gitignore"), externalStoreLifecycleLockRule+"\n")
				symlink(t, filepath.Join(source, "spec"), filepath.Join(repository, "spec"))
			},
			wantLint: true,
		},
		{
			name: "symlinked spec/plans runs lint",
			setup: func(t *testing.T, repository string) {
				source := t.TempDir()
				writeExternalPlansStore(t, source)
				writeQualityFile(t, filepath.Join(repository, ".gitignore"), externalStoreLifecycleLockRule+"\n")
				symlink(t, filepath.Join(source, "spec", "plans"), filepath.Join(repository, "spec", "plans"))
			},
			wantLint: true,
		},
		{
			name: "symlinked namespace index runs lint",
			setup: func(t *testing.T, repository string) {
				writeQualityFile(t, filepath.Join(repository, ".gitignore"), externalStoreLifecycleLockRule+"\n")
				writeQualityFile(t, filepath.Join(repository, "outside.md"), "# outside\n")
				symlink(t, filepath.Join(repository, "outside.md"), filepath.Join(repository, "spec", "plans", "github.com", "o", "r", "README.md"))
			},
			wantLint: true,
		},
		{
			name: "symlink beneath a plan directory runs lint",
			setup: func(t *testing.T, repository string) {
				writeExternalPlansStore(t, repository)
				symlink(t, "/etc/hosts", filepath.Join(repository, "spec", "plans", "github.com", "datatug", "datatug", "phase-1", "escape.md"))
			},
			wantLint: true,
		},
		{
			name: "host-level file runs lint",
			setup: func(t *testing.T, repository string) {
				writeExternalPlansStore(t, repository)
				writeQualityFile(t, filepath.Join(repository, "spec", "plans", "github.com", "README.md"), "# host\n")
			},
			wantLint: true,
		},
		{
			name: "owner-level file runs lint",
			setup: func(t *testing.T, repository string) {
				writeExternalPlansStore(t, repository)
				writeQualityFile(t, filepath.Join(repository, "spec", "plans", "github.com", "datatug", "README.md"), "# owner\n")
			},
			wantLint: true,
		},
		{
			name: "namespace-level non-README file runs lint",
			setup: func(t *testing.T, repository string) {
				writeExternalPlansStore(t, repository)
				writeQualityFile(t, filepath.Join(repository, "spec", "plans", "github.com", "datatug", "datatug", "notes.md"), "# notes\n")
			},
			wantLint: true,
		},
		{
			name: "empty spec runs lint",
			setup: func(t *testing.T, repository string) {
				writeQualityFile(t, filepath.Join(repository, ".gitignore"), externalStoreLifecycleLockRule+"\n")
				mkdir(t, filepath.Join(repository, "spec"))
			},
			wantLint: true,
		},
		{
			name: "directories only runs lint",
			setup: func(t *testing.T, repository string) {
				writeQualityFile(t, filepath.Join(repository, ".gitignore"), externalStoreLifecycleLockRule+"\n")
				mkdir(t, filepath.Join(repository, "spec", "plans", "github.com", "datatug", "datatug", "phase-1"))
			},
			wantLint: true,
		},
		{
			name: "aggregate index alone runs lint",
			setup: func(t *testing.T, repository string) {
				writeQualityFile(t, filepath.Join(repository, ".gitignore"), externalStoreLifecycleLockRule+"\n")
				writeQualityFile(t, filepath.Join(repository, "spec", "plans", "README.md"), "# Plans\n")
			},
			wantLint: true,
		},
		{
			name: "same-repository nested plan runs lint",
			setup: func(t *testing.T, repository string) {
				writeQualityFile(t, filepath.Join(repository, ".gitignore"), externalStoreLifecycleLockRule+"\n")
				writeQualityFile(t, filepath.Join(repository, "spec", "plans", "phase-1", "core", "loop", "task", "README.md"), "# Task\n")
			},
			wantLint: true,
		},
		{
			name: "missing gitignore runs lint",
			setup: func(t *testing.T, repository string) {
				writeExternalPlansStore(t, repository)
				if err := os.Remove(filepath.Join(repository, ".gitignore")); err != nil {
					t.Fatal(err)
				}
			},
			wantLint: true,
		},
		{
			name: "gitignore without the lifecycle-lock line runs lint",
			setup: func(t *testing.T, repository string) {
				writeExternalPlansStore(t, repository)
				writeQualityFile(t, filepath.Join(repository, ".gitignore"), "node_modules/\n")
			},
			wantLint: true,
		},
		{
			name: "unanchored lifecycle-lock line runs lint",
			setup: func(t *testing.T, repository string) {
				writeExternalPlansStore(t, repository)
				writeQualityFile(t, filepath.Join(repository, ".gitignore"), ".specscore-lifecycle.lock\n")
			},
			wantLint: true,
		},
		{
			name: "symlinked gitignore runs lint",
			setup: func(t *testing.T, repository string) {
				writeExternalPlansStore(t, repository)
				if err := os.Rename(filepath.Join(repository, ".gitignore"), filepath.Join(repository, "ignore-rules")); err != nil {
					t.Fatal(err)
				}
				symlink(t, "ignore-rules", filepath.Join(repository, ".gitignore"))
			},
			wantLint: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repository := t.TempDir()
			tc.setup(t, repository)
			log := stubSpecscoreBinary(t, repository)

			report := Verify(context.Background(), "example/plans-store", repository, []Check{CheckSpec})
			if tc.wantLint {
				assertSpecLintRan(t, report, log)
				return
			}
			if report.Status != StatusPassed || len(report.Results) != 1 || report.Results[0].Status != StatusSkipped {
				t.Fatalf("report = %+v, want the spec check skipped", report)
			}
			if _, err := os.Stat(log); !os.IsNotExist(err) {
				t.Fatalf("specscore ran for a skipped external store (log stat err = %v)", err)
			}
		})
	}
}

// TestExternalPlansStorePath pins the path rule for every non-directory
// entry under spec/ in an external Plans store.
func TestExternalPlansStorePath(t *testing.T) {
	t.Parallel()
	for rel, want := range map[string]bool{
		// Aggregate index, and what a symlinked spec/ or spec/plans looks like.
		"plans/README.md":         true,
		"plans/notes.md":          false,
		"plans":                   false,
		".":                       false,
		"README.md":               false,
		"features/auth/README.md": false,

		// Depth: files at host or owner level never fit.
		"plans/github.com/README.md":   false,
		"plans/github.com/o/README.md": false,
		"plans/github.com/o/r":         false,

		// Namespace level: exactly README.md.
		"plans/github.com/o/r/README.md": true,
		"plans/github.com/o/r/notes.md":  false,
		"plans/github.com/o/r/readme.md": false,
		"plans/github.com/o/r/.DS_Store": false,

		// Plan-id depth >= 1 below the repo segment: anything beneath it.
		"plans/github.com/o/r/p1/README.md":       true,
		"plans/github.com/o/r/p1/child/README.md": true,
		"plans/github.com/o/r/p1/notes.txt":       true,

		// Hostname rules.
		"plans/gitlab.example.co.uk/o/r/p/README.md": true,
		"plans/git-hub.com/o/r/p/README.md":          true,
		"plans/127.0.0.1/o/r/p/README.md":            true,
		"plans/localhost/o/r/p/README.md":            false,
		"plans/GitHub.com/o/r/p/README.md":           false,
		"plans/git_hub.com/o/r/p/README.md":          false,
		"plans/github.com:443/o/r/p/README.md":       false,
		"plans/.github.com/o/r/p/README.md":          false,
		"plans/github.com./o/r/p/README.md":          false,
		"plans/-github.com/o/r/p/README.md":          false,
		"plans/github.com-/o/r/p/README.md":          false,
		"plans/github-.com/o/r/p/README.md":          false,
		"plans/github..com/o/r/p/README.md":          false,
		"plans/.git/o/r/p/README.md":                 false,
		"plans/../o/r/p/README.md":                   false,
		"plans/.../..../r/p/README.md":               false,
		"plans/not a host/o/r/p/notes.txt":           false,

		// Owner and repo: non-empty, no leading dot.
		"plans/github.com/.o/r/p/README.md":    false,
		"plans/github.com/o/.git/p/README.md":  false,
		"plans/github.com/../r/p/README.md":    false,
		"plans/github.com/o/../p/README.md":    false,
		"plans/github.com//r/p/README.md":      false,
		"plans/github.com/o//p/README.md":      false,
		"plans/github.com/o.x/r.y/p/README.md": true,

		// A same-repository nested plan has no dot in its first segment.
		"plans/phase-1/core/loop/task/README.md": false,
		"plans/phase-1/core/loop/README.md":      false,
		"plans/x/y/z/w/features/auth/README.md":  false,
	} {
		if got := externalPlansStorePath(rel); got != want {
			t.Errorf("externalPlansStorePath(%q) = %v, want %v", rel, got, want)
		}
	}
}

// writeExternalPlansStore writes a minimal external SpecScore Plans store at
// repository: the lifecycle-lock ignore rule, the aggregate index, a
// namespace index and a nested plan.
func writeExternalPlansStore(t *testing.T, repository string) {
	t.Helper()
	namespace := filepath.Join(repository, "spec", "plans", "github.com", "datatug", "datatug")
	writeQualityFile(t, filepath.Join(repository, ".gitignore"), externalStoreLifecycleLockRule+"\n")
	writeQualityFile(t, filepath.Join(repository, "spec", "plans", "README.md"), "# Plans\n")
	writeQualityFile(t, filepath.Join(namespace, "README.md"), "# datatug Plans\n")
	writeQualityFile(t, filepath.Join(namespace, "phase-1", "README.md"), "# Phase 1\n")
	writeQualityFile(t, filepath.Join(namespace, "phase-1", "database-setup", "README.md"), "# Database setup\n")
}

// assertSpecLintRan fails unless the spec check ran the stub specscore
// exactly once as "spec lint" and passed.
func assertSpecLintRan(t *testing.T, report VerificationReport, log string) {
	t.Helper()
	if report.Status != StatusPassed || len(report.Results) != 1 || report.Results[0].Status != StatusPassed {
		t.Fatalf("report = %+v, want spec lint to run and pass", report)
	}
	contents, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("specscore did not run: %v", err)
	}
	if got, want := strings.TrimSpace(string(contents)), "spec lint"; got != want {
		t.Fatalf("command = %q, want %q (lint must run, not skip)", got, want)
	}
}

// stubSpecscoreBinary installs a "specscore" executable on PATH that appends
// its arguments to a log file in repository and exits 0. It returns the log
// path so a test can assert the exact command wb invoked.
func stubSpecscoreBinary(t *testing.T, repository string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(repository, "commands.log")
	writeQualityFile(t, filepath.Join(bin, "specscore"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \""+log+"\"\n")
	if err := os.Chmod(filepath.Join(bin, "specscore"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func TestParseChecks(t *testing.T) {
	t.Parallel()
	checks, err := ParseChecks("test,lint,test")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(checkStrings(checks), ","), "test,lint"; got != want {
		t.Fatalf("checks = %s, want %s", got, want)
	}
	if _, err := ParseChecks("format"); err == nil {
		t.Fatal("unknown check should fail")
	}
}

func TestCommandErrorRetainsFailureTailWhenOutputIsLong(t *testing.T) {
	t.Parallel()
	prefix := "setup context\n"
	middle := strings.Repeat("passing package output\n", 100)
	failure := "--- FAIL: TestImportantJourney (15.14s)\n    journey_test.go:42: exact failure\nFAIL"

	detail := commandError("go test ./...", prefix+middle+failure, context.DeadlineExceeded)

	if !strings.Contains(detail, prefix) {
		t.Fatalf("detail lost initial command context: %q", detail)
	}
	if !strings.Contains(detail, failure) {
		t.Fatalf("detail lost terminal failure: %q", detail)
	}
	if !strings.Contains(detail, "truncated") {
		t.Fatalf("detail does not disclose truncation: %q", detail)
	}
}

// TestTruncateCommandDetailToKeepsMiddleFailAndPanicBlocksAHeadTailBoundWouldDrop
// pins sneat-dev/wb#582: `go test` output is sorted by package, and the
// overwhelming majority pass, so the failing package's own "--- FAIL"
// block, and any "panic:" and its stack, sit wherever the alphabet puts
// them — almost never at either end. A head+tail-only bound (the
// behaviour before this fix) reliably kept only passing "ok" lines here;
// this test builds exactly that shape and shows the evidence survives,
// strictly within the byte budget (review finding B1).
func TestTruncateCommandDetailToKeepsMiddleFailAndPanicBlocksAHeadTailBoundWouldDrop(t *testing.T) {
	t.Parallel()
	head := strings.Repeat("ok  \tgithub.com/acme/aaa\t0.01s\tcoverage: 100.0% of statements\n", 20)
	failBlock := "--- FAIL: TestDaemonFileBridgeRetryRecoversSubmitAcrossTokenAndGenerationRotation (1.81s)\n" +
		"    daemon_file_bridge_test.go:149: old daemon operation count = 0, <nil>\n"
	panicBlock := "panic: runtime error: index out of range [3] with length 3\n" +
		"\tgoroutine 7 [running]:\n" +
		"\tgithub.com/acme/pkg.doWork(...)\n" +
		"\t\t/src/pkg/work.go:42\n"
	middle := strings.Repeat("ok  \tgithub.com/acme/mmm\t0.02s\tcoverage: 98.0% of statements\n", 200)
	// A bare "FAIL" line — the whole-command summary a shard emits without
	// its own "--- FAIL" block, e.g. a compile failure — must survive on
	// its own too, not only as part of a "--- FAIL" block or a panic's
	// stack.
	bareFail := "FAIL\tgithub.com/acme/broken\t[build failed]\n"
	tail := strings.Repeat("ok  \tgithub.com/acme/zzz\t0.03s\tcoverage: 100.0% of statements\n", 20) + "FAIL\nexit status 1"

	detail := head + failBlock + middle + panicBlock + middle + bareFail + middle + tail

	// A degenerate head+tail bound over this shape would keep only the
	// leading and trailing "ok" lines and the terminal bare "FAIL" — the
	// exact defect #582 reported. Confirm the geometry actually exercises
	// that: both blocks sit well inside the region a 1000-byte bound
	// drops.
	const max = 1000
	headBytes := max / 4
	if headBytes > 250 {
		headBytes = 250
	}
	if strings.Index(detail, failBlock) < headBytes {
		t.Fatalf("test fixture invalid: failBlock is not past the head window")
	}
	if strings.Index(detail, panicBlock)+len(panicBlock) > len(detail)-200 {
		t.Fatalf("test fixture invalid: panicBlock is not clear of the tail window")
	}

	got := truncateCommandDetailTo(detail, max)

	if len(got) > max {
		t.Fatalf("truncated detail (%d bytes) exceeds the budget (%d bytes): %q", len(got), max, got)
	}
	if !strings.Contains(got, "--- FAIL: TestDaemonFileBridgeRetryRecoversSubmitAcrossTokenAndGenerationRotation") {
		t.Fatalf("truncated detail dropped the middle FAIL block: %q", got)
	}
	if !strings.Contains(got, "daemon_file_bridge_test.go:149: old daemon operation count = 0, <nil>") {
		t.Fatalf("truncated detail dropped the FAIL block's assertion message: %q", got)
	}
	if !strings.Contains(got, "panic: runtime error: index out of range [3] with length 3") {
		t.Fatalf("truncated detail dropped the middle panic line: %q", got)
	}
	if !strings.Contains(got, "/src/pkg/work.go:42") {
		t.Fatalf("truncated detail dropped the panic's stack: %q", got)
	}
	if !strings.Contains(got, bareFail) {
		t.Fatalf("truncated detail dropped the standalone middle FAIL line: %q", got)
	}
	if !strings.Contains(got, "truncated") {
		t.Fatalf("truncated detail does not disclose truncation: %q", got)
	}
	if len(got) >= len(detail) {
		t.Fatalf("truncated detail (%d bytes) is not smaller than the input (%d bytes)", len(got), len(detail))
	}
}

// TestTruncateCommandDetailToKeepsARealPanicStacksGoroutineHeaderAndFrames
// pins review finding B (round 2 of #582) with a real captured `go test
// -run TestBoom` failure (testdata/real_panic_stack.txt: a genuine runtime
// panic from an out-of-range index), not a synthetic one. The Go runtime
// always prints exactly one blank line right after the "panic: ..." line
// and before "goroutine N [running]:"; the earlier fix treated that first
// blank line itself as the end of the stack, so the goroutine header and
// every frame under it — including the test's own frame — were dropped
// even though the budget had room to spare.
func TestTruncateCommandDetailToKeepsARealPanicStacksGoroutineHeaderAndFrames(t *testing.T) {
	t.Parallel()
	fixture, err := os.ReadFile(filepath.Join("testdata", "real_panic_stack.txt"))
	if err != nil {
		t.Fatal(err)
	}
	head := strings.Repeat("ok  \tgithub.com/acme/aaa\t0.01s\tcoverage: 100.0% of statements\n", 40)
	tail := strings.Repeat("ok  \tgithub.com/acme/zzz\t0.03s\tcoverage: 100.0% of statements\n", 40)
	detail := head + string(fixture) + tail

	// A generous budget: this test's point is that the goroutine header and
	// its frames are kept at all once there is room, not that the whole
	// fixture must survive an unrelated, unrealistically tight budget.
	const max = 2000
	got := truncateCommandDetailTo(detail, max)
	if len(got) > max {
		t.Fatalf("truncated detail (%d bytes) exceeds the budget (%d bytes): %q", len(got), max, got)
	}
	for _, want := range []string{
		"--- FAIL: TestBoom (0.00s)",
		"panic: runtime error: index out of range [3] with length 0",
		"goroutine 6 [running]:",
		"panicdemo.TestBoom.func1()",
		"/tmp/tmp.QqaMXOYIOO/p_test.go:8",
		"panicdemo.TestBoom(0x38af9667a248?)",
		"/tmp/tmp.QqaMXOYIOO/p_test.go:12",
		"FAIL\tpanicdemo\t0.006s",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("truncated detail dropped %q from a real panic stack: %q", want, got)
		}
	}
}

// TestTruncateCommandDetailToWalksBackToStartWhenTheLastLineHasNoTrailingNewline
// exercises the scanStart walk-back's own edge case directly (review
// finding N3's fix): the line reached while backing up over a run of
// indented continuation lines can itself be the very last line of detail,
// with no trailing newline after it to find.
func TestTruncateCommandDetailToWalksBackToStartWhenTheLastLineHasNoTrailingNewline(t *testing.T) {
	t.Parallel()
	detail := "H\n    indented continuation with no trailing newline at all"
	const max = 10
	got := truncateCommandDetailTo(detail, max)
	// Neither "H" nor the indented line matches any evidence trigger, so
	// this must fall back to the legacy shape byte-for-byte — including
	// its own long-standing imprecision at a budget this tiny (the same
	// parity every other no-evidence input relies on).
	want := legacyTruncateCommandDetailTo(detail, max)
	if got != want {
		t.Fatalf("truncateCommandDetailTo = %q, want the legacy shape %q", got, want)
	}
}

// TestFitEvidenceBlockStopsWithoutAttemptingAPartialLineWhenNoRoomRemainsAtAll
// exercises fitEvidenceBlock's own boundary directly: once a kept line has
// used the entire budget, the next line's separator alone leaves no room
// at all (not even for a rune-safe partial, review finding N5's own
// limit) — the loop must simply stop, not attempt to append anything more.
func TestFitEvidenceBlockStopsWithoutAttemptingAPartialLineWhenNoRoomRemainsAtAll(t *testing.T) {
	t.Parallel()
	const budget = 20
	firstLine := strings.Repeat("A", budget)
	secondLine := strings.Repeat("B", 100)
	available := budget + len(evidenceHeader) + len(evidenceTruncatedNotice)

	got := fitEvidenceBlock([]string{firstLine, secondLine}, available)
	if len(got) > available {
		t.Fatalf("fitEvidenceBlock (%d bytes) exceeds available (%d bytes): %q", len(got), available, got)
	}
	if !strings.Contains(got, firstLine) {
		t.Fatalf("fitEvidenceBlock dropped the line that exactly fit the budget: %q", got)
	}
	if strings.Contains(got, "B") {
		t.Fatalf("fitEvidenceBlock kept part of a line it had no room for at all: %q", got)
	}
}

// TestTruncateCommandDetailToKeepsAFailBlockNearTheEndAfterEvidenceShrinksTheTail
// pins review finding B3: a "--- FAIL" block that sits within what the
// historical head+tail bound would have kept as tail must still survive
// once evidence found earlier shrinks that tail to make room for itself —
// scanning must never stop at the old, larger tail boundary.
func TestTruncateCommandDetailToKeepsAFailBlockNearTheEndAfterEvidenceShrinksTheTail(t *testing.T) {
	t.Parallel()
	head := strings.Repeat("ok  \tgithub.com/acme/aaa\t0.01s\tcoverage: 100.0% of statements\n", 20)
	middleFail := "--- FAIL: TestMiddle (0.01s)\n    fixture_test.go:1: middle failure\n"
	filler := strings.Repeat("ok  \tgithub.com/acme/mmm\t0.02s\tcoverage: 98.0% of statements\n", 150)
	nearTailFail := "--- FAIL: TestNearTail (0.02s)\n    fixture_test.go:2: near-tail failure\n"
	tail := strings.Repeat("ok  \tgithub.com/acme/zzz\t0.03s\tcoverage: 100.0% of statements\n", 5)

	detail := head + middleFail + filler + nearTailFail + tail

	const max = 1000
	// Confirm nearTailFail actually sits inside the legacy (no-evidence)
	// tail window, so this test exercises the shrink, not a fixture that
	// happened to already scan it as "middle".
	headBytes := max / 4
	legacyTailBytes, _ := sizeTailAndMarker(max - headBytes)
	if strings.Index(detail, nearTailFail) < len(detail)-legacyTailBytes {
		t.Fatalf("test fixture invalid: nearTailFail is not inside the legacy tail window")
	}

	got := truncateCommandDetailTo(detail, max)
	if len(got) > max {
		t.Fatalf("truncated detail (%d bytes) exceeds the budget (%d bytes)", len(got), max)
	}
	if !strings.Contains(got, "--- FAIL: TestMiddle") {
		t.Fatalf("truncated detail dropped the middle FAIL block: %q", got)
	}
	if !strings.Contains(got, "--- FAIL: TestNearTail") {
		t.Fatalf("truncated detail dropped the near-tail FAIL block once the tail shrank: %q", got)
	}
}

// TestTruncateCommandDetailToKeepsANestedSubtestBlockWhoseParentIsInTheHead
// pins review finding N7: an indented "--- FAIL" sub-test line, and its
// assertion message, must survive even when the parent "--- FAIL" line
// that introduces the block falls inside the head window itself.
func TestTruncateCommandDetailToKeepsANestedSubtestBlockWhoseParentIsInTheHead(t *testing.T) {
	t.Parallel()
	const max = 1000
	headBytes := max / 4
	// Pad so the parent "--- FAIL" line's own start falls before headBytes
	// but its line, and the indented sub-test block under it, extend past
	// it.
	pad := strings.Repeat("x", headBytes-10)
	parentBlock := "--- FAIL: TestParent (0.05s)\n" +
		"    --- FAIL: TestParent/sub (0.01s)\n" +
		"        fixture_test.go:9: nested assertion failed\n"
	filler := strings.Repeat("ok  \tgithub.com/acme/mmm\t0.02s\tcoverage: 98.0% of statements\n", 150)
	tail := strings.Repeat("ok  \tgithub.com/acme/zzz\t0.03s\tcoverage: 100.0% of statements\n", 5)
	detail := pad + "\n" + parentBlock + filler + tail

	if strings.Index(detail, parentBlock) >= headBytes {
		t.Fatalf("test fixture invalid: parentBlock does not straddle the head boundary")
	}

	got := truncateCommandDetailTo(detail, max)
	if len(got) > max {
		t.Fatalf("truncated detail (%d bytes) exceeds the budget (%d bytes)", len(got), max)
	}
	if !strings.Contains(got, "--- FAIL: TestParent/sub") {
		t.Fatalf("truncated detail dropped the nested sub-test line whose parent is in the head: %q", got)
	}
	if !strings.Contains(got, "nested assertion failed") {
		t.Fatalf("truncated detail dropped the nested sub-test's assertion: %q", got)
	}
}

// TestTruncateCommandDetailToUnchangedWhenNothingWorthKeepingIsDropped pins
// the historical head+tail-only shape for the common case this fix must
// not disturb: when nothing worth keeping is found, truncateCommandDetailTo
// returns byte-for-byte what the pre-#582 algorithm returned (review
// finding N5 — an exact comparison, not Contains/HasPrefix).
func TestTruncateCommandDetailToUnchangedWhenNothingWorthKeepingIsDropped(t *testing.T) {
	t.Parallel()
	detail := strings.Repeat("head", 1000) + "TAIL"
	const max = 2000
	got := truncateCommandDetailTo(detail, max)
	want := legacyTruncateCommandDetailTo(detail, max)
	if got != want {
		t.Fatalf("truncateCommandDetailTo(no evidence) = %q, want byte-identical to the legacy shape %q", got, want)
	}
	if strings.Contains(got, "kept failure evidence") {
		t.Fatalf("truncated detail claims to keep evidence that was never present: %q", got)
	}
}

// TestTruncateCommandDetailToMatchesLegacyOnRandomInputsWithNoRealEvidence
// is the byte-exact differential test review finding B4 asked for: many
// deterministically generated inputs, containing only near-miss look-alike
// text ("FAILED", "panicked", indented "panic:", "x FAIL y") that must
// never be mistaken for real evidence once matching is restricted to whole
// lines, across a range of budgets. Every one must match the legacy
// head+tail-only algorithm byte-for-byte.
func TestTruncateCommandDetailToMatchesLegacyOnRandomInputsWithNoRealEvidence(t *testing.T) {
	t.Parallel()
	random := rand.New(rand.NewSource(20260924))
	lookAlikes := []string{
		"ok  \tgithub.com/acme/pkg%d\t0.0%ds\tcoverage: 9%d.0%% of statements",
		"this package FAILED to look like a real failure",
		"a panicked goroutine is not a panic: line",
		"    panic: this panic: line is indented, so it never starts a line",
		"x FAIL y sits mid-line, never at column zero",
		"=== RUN   TestSomething",
		"--- PASS: TestSomething (0.00s)",
	}
	maxValues := []int{50, 100, 250, 500, 1000, 2000}

	for iteration := 0; iteration < 500; iteration++ {
		lineCount := 5 + random.Intn(60)
		lines := make([]string, 0, lineCount)
		for i := 0; i < lineCount; i++ {
			template := lookAlikes[random.Intn(len(lookAlikes))]
			// Review finding N6: only the one template with %d verbs takes
			// the random substitutions — passing them to every template
			// regardless made fmt.Sprintf append "%!(EXTRA ...)" noise to
			// every line without a verb to consume them.
			line := template
			if strings.Contains(template, "%d") {
				line = fmt.Sprintf(template, random.Intn(100), random.Intn(9), random.Intn(9))
			}
			lines = append(lines, line)
		}
		detail := strings.Join(lines, "\n")
		for _, max := range maxValues {
			got := truncateCommandDetailTo(detail, max)
			want := legacyTruncateCommandDetailTo(detail, max)
			if got != want {
				t.Fatalf("iteration %d, max %d: truncateCommandDetailTo diverged from the legacy shape on a no-evidence input\ndetail=%q\ngot =%q\nwant=%q", iteration, max, detail, got, want)
			}
		}
	}
}

// legacyTruncateCommandDetailTo is a frozen copy of the pre-#582
// algorithm (byte-position head+tail, no evidence recovery), kept only so
// tests can differential-test the new truncateCommandDetailTo against it
// for inputs with nothing worth keeping in the dropped middle, where
// behaviour must be identical.
func legacyTruncateCommandDetailTo(detail string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(detail) <= max {
		return detail
	}
	headBytes := max / 4
	if headBytes > 250 {
		headBytes = 250
	}
	tailBytes := max - headBytes - 64
	if tailBytes < 0 {
		tailBytes = 0
	}
	marker := fmt.Sprintf("\n… output truncated; final %d bytes:\n", tailBytes)
	tailBytes = max - headBytes - len(marker)
	if tailBytes < 0 {
		tailBytes = 0
	}
	marker = fmt.Sprintf("\n… output truncated; final %d bytes:\n", tailBytes)
	return detail[:headBytes] + marker + detail[len(detail)-tailBytes:]
}

// TestTruncateCommandDetailToCapsEvidenceWhenItWouldOverflowTheBudget pins
// the defensive cap on kept evidence: a pathological run with dozens of
// "--- FAIL" blocks in its dropped middle must not make the returned
// string exceed the budget (review finding B1) — every kept line is
// either whole or, only for the very last one, a rune-safe prefix (review
// findings B2 and N5) — and discloses the cut, and the tail correctly
// collapses to nothing once the evidence alone exceeds the remainder.
func TestTruncateCommandDetailToCapsEvidenceWhenItWouldOverflowTheBudget(t *testing.T) {
	t.Parallel()
	head := strings.Repeat("ok  \tgithub.com/acme/aaa\t0.01s\tcoverage: 100.0% of statements\n", 20)
	var blocks strings.Builder
	for i := 0; i < 60; i++ {
		blocks.WriteString("--- FAIL: TestRepeatedFailure (0.01s)\n    fixture_test.go:1: repeated failure body\n")
	}
	tail := strings.Repeat("ok  \tgithub.com/acme/zzz\t0.03s\tcoverage: 100.0% of statements\n", 20)
	detail := head + blocks.String() + tail

	const max = 1000
	got := truncateCommandDetailTo(detail, max)

	if len(got) > max {
		t.Fatalf("truncated detail (%d bytes) exceeds the budget (%d bytes): %q", len(got), max, got)
	}
	if !strings.Contains(got, "--- FAIL: TestRepeatedFailure") {
		t.Fatalf("truncated detail dropped all evidence: %q", got)
	}
	if !strings.Contains(got, "further failure evidence omitted") {
		t.Fatalf("truncated detail cut evidence without disclosing it: %q", got)
	}
	// Every kept evidence line before the omission notice reproduces one
	// of the repeated blocks either in full, or — only for the very last
	// line, when it does not fit whole — as its own rune-safe prefix
	// (review finding N5: an over-long line's start is kept rather than
	// dropping the whole line).
	beforeNotice, _, _ := strings.Cut(got, evidenceTruncatedNotice)
	_, keptEvidence, found := strings.Cut(beforeNotice, evidenceHeader)
	if !found {
		t.Fatalf("truncated detail has no evidence header: %q", got)
	}
	if keptEvidence != "" {
		lines := strings.Split(keptEvidence, "\n")
		for i, line := range lines {
			if line == "--- FAIL: TestRepeatedFailure (0.01s)" || line == "    fixture_test.go:1: repeated failure body" {
				continue
			}
			isLast := i == len(lines)-1
			isPrefix := strings.HasPrefix("--- FAIL: TestRepeatedFailure (0.01s)", line) ||
				strings.HasPrefix("    fixture_test.go:1: repeated failure body", line)
			if !isLast || !isPrefix {
				t.Fatalf("truncated detail cut evidence mid-line in an unexpected place: %q", line)
			}
		}
	}
}

// TestTruncateCommandDetailToNeverSplitsAMultibyteRuneEvenAtATinyBudget
// pins review finding B2's rune-safety requirement in the extreme case
// where even the evidence header does not fit: the returned string must
// still be valid UTF-8 and within budget.
func TestTruncateCommandDetailToNeverSplitsAMultibyteRuneEvenAtATinyBudget(t *testing.T) {
	t.Parallel()
	detail := "AAAAAAAAAAAAAAAAAAAA\n" +
		"--- FAIL: TestTiny (0.00s)\n    fixture_test.go:1: tiny failure\n" +
		strings.Repeat("ok  \tgithub.com/acme/zzz\t0.03s\tcoverage: 100.0% of statements\n", 5)
	const max = 22
	got := truncateCommandDetailTo(detail, max)
	if len(got) > max {
		t.Fatalf("truncated detail (%d bytes) exceeds the budget (%d bytes): %q", len(got), max, got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("truncated detail split a multibyte rune: %q", got)
	}
}

// TestTruncateCommandDetailToTailMarkerStatesTheExactByteCount pins review
// finding B2: the "final N bytes" notice must be rendered from the tail's
// FINAL, actual size, not a provisional one computed before the evidence
// block's own length was known.
func TestTruncateCommandDetailToTailMarkerStatesTheExactByteCount(t *testing.T) {
	t.Parallel()
	head := strings.Repeat("ok  \tgithub.com/acme/aaa\t0.01s\tcoverage: 100.0% of statements\n", 20)
	failBlock := "--- FAIL: TestMiddle (0.01s)\n    fixture_test.go:1: middle failure\n"
	middle := strings.Repeat("ok  \tgithub.com/acme/mmm\t0.02s\tcoverage: 98.0% of statements\n", 200)
	tail := strings.Repeat("ok  \tgithub.com/acme/zzz\t0.03s\tcoverage: 100.0% of statements\n", 20)
	detail := head + failBlock + middle + tail

	got := truncateCommandDetailTo(detail, 1000)
	const markerPrefix = "\n… output truncated; final "
	idx := strings.Index(got, markerPrefix)
	if idx < 0 {
		t.Fatalf("truncated detail has no tail marker: %q", got)
	}
	rest := got[idx+len(markerPrefix):]
	spaceIdx := strings.Index(rest, " bytes:\n")
	if spaceIdx < 0 {
		t.Fatalf("tail marker is not in the expected shape: %q", got)
	}
	claimedCount, err := strconv.Atoi(rest[:spaceIdx])
	if err != nil {
		t.Fatalf("tail marker byte count is not a number: %q: %v", rest[:spaceIdx], err)
	}
	actualTail := rest[spaceIdx+len(" bytes:\n"):]
	if claimedCount != len(actualTail) {
		t.Fatalf("tail marker claims %d bytes, but %d bytes actually follow it: %q", claimedCount, len(actualTail), actualTail)
	}
}

// TestTruncateRuneSafeNeverSplitsAMultibyteRune pins truncateRuneSafe's own
// contract directly (used only in fitEvidenceBlock's own extreme
// last-resort branch, where it is never called with max <= 0 — this test
// still exercises that guard, plus the len(s) <= max no-op case and the
// case that actually requires walking back over a split multibyte rune).
func TestTruncateRuneSafeNeverSplitsAMultibyteRune(t *testing.T) {
	t.Parallel()
	multibyte := "a…b" // "…" is U+2026, 3 bytes: 0xE2 0x80 0xA6
	cases := []struct {
		name string
		s    string
		max  int
		want string
	}{
		{"non-positive max returns empty", multibyte, 0, ""},
		{"negative max returns empty", multibyte, -1, ""},
		{"max at or above len(s) returns s unchanged", multibyte, len(multibyte), multibyte},
		{"cut lands mid-rune, walks back to the rune start", multibyte, 2, "a"},
		{"cut lands mid-rune one byte later, still walks back", multibyte, 3, "a"},
		{"cut lands exactly on a rune boundary", multibyte, 1, "a"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got := truncateRuneSafe(testCase.s, testCase.max)
			if got != testCase.want {
				t.Fatalf("truncateRuneSafe(%q, %d) = %q, want %q", testCase.s, testCase.max, got, testCase.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("truncateRuneSafe(%q, %d) = %q is not valid UTF-8", testCase.s, testCase.max, got)
			}
		})
	}
}

// TestIsEndOfPanicStackRecognizesEveryGoTestStatusLineShape pins review
// finding N6: a panic's stack trace ends not only at a blank line but at
// any of the status-line shapes `go test`'s own output can resume with,
// even with no blank line in between — regardless of sawGoroutineHeader,
// which only changes how a bare blank line is treated (round-2 finding B).
func TestIsEndOfPanicStackRecognizesEveryGoTestStatusLineShape(t *testing.T) {
	t.Parallel()
	for _, line := range []string{
		"--- FAIL: TestSomething (0.00s)",
		"FAIL",
		"FAIL\tgithub.com/acme/pkg\t0.01s",
		"ok  \tgithub.com/acme/pkg\t0.01s",
		"?   \tgithub.com/acme/pkg\t[no test files]",
		"PASS",
		"--- PASS: TestSomething (0.00s)",
		"=== RUN   TestSomething",
		"=== NAME  TestSomething",
		"coverage: 91.2% of statements",
		"[unsharded packages]",
		"[example.test/serial shard 2/2]",
	} {
		for _, sawHeader := range []bool{false, true} {
			if !isEndOfPanicStack(line, sawHeader) {
				t.Errorf("isEndOfPanicStack(%q, %v) = false, want true", line, sawHeader)
			}
		}
	}
	for _, line := range []string{
		"\tgoroutine 7 [running]:",
		"\tgithub.com/acme/pkg.doWork(...)",
		"\t\t/src/pkg/work.go:42",
		"a plain stack-trace-looking line",
	} {
		for _, sawHeader := range []bool{false, true} {
			if isEndOfPanicStack(line, sawHeader) {
				t.Errorf("isEndOfPanicStack(%q, %v) = true, want false", line, sawHeader)
			}
		}
	}
}

// TestIsEndOfPanicStackTreatsABlankLineAsTheEndOnlyAfterTheGoroutineHeader
// pins review finding B (round 2 of #582) directly: the Go runtime always
// prints exactly one blank line right after "panic: ..." and before
// "goroutine N [running]:", so a blank line seen before that header must
// NOT end the stack — only a blank line seen after it (or one of the
// go-test status-line shapes, regardless of the header) does.
func TestIsEndOfPanicStackTreatsABlankLineAsTheEndOnlyAfterTheGoroutineHeader(t *testing.T) {
	t.Parallel()
	if isEndOfPanicStack("", false) {
		t.Error(`isEndOfPanicStack("", false) = true, want false: the blank line before the goroutine header must not end the stack`)
	}
	if !isEndOfPanicStack("", true) {
		t.Error(`isEndOfPanicStack("", true) = false, want true: a blank line after the goroutine header ends the stack`)
	}
	if isEndOfPanicStack("    ", false) {
		t.Error(`isEndOfPanicStack("    ", false) = true, want false`)
	}
	if !isEndOfPanicStack("    ", true) {
		t.Error(`isEndOfPanicStack("    ", true) = false, want true`)
	}
}

func TestShardedCoverageFailureIndexPrecedesRawOutputAndSurvivesTruncation(t *testing.T) {
	t.Parallel()
	jobs := []goCoverageJob{
		{label: "example.test/serial shard 2/2"},
		{label: "unsharded packages"},
	}
	results := []goCoverageJobResult{
		{output: "--- FAIL: TestJourney (0.01s)\n    --- FAIL: TestJourney/remote_resume (0.00s)\nFAIL", err: errors.New("exit status 1")},
		{output: "package compile error", err: errors.New("exit status 1")},
	}
	summary := summarizeCoverageFailures(jobs, results)
	for _, expected := range []string{
		"[example.test/serial shard 2/2] TestJourney",
		"[example.test/serial shard 2/2] TestJourney/remote_resume",
		"[unsharded packages] command failed without a named Go test",
	} {
		if !strings.Contains(summary, expected) {
			t.Fatalf("failure summary missing %q: %q", expected, summary)
		}
	}

	full := summary + coverageRawOutputHeader + strings.Repeat("uninteresting passing output\n", 200) + "terminal raw failure"
	detail := commandError("go test", full, errors.New("exit status 1"))
	for _, expected := range []string{
		"[example.test/serial shard 2/2] TestJourney",
		"[example.test/serial shard 2/2] TestJourney/remote_resume",
		"[unsharded packages] command failed without a named Go test",
		coverageRawOutputHeader,
		"terminal raw failure",
	} {
		if !strings.Contains(detail, expected) {
			t.Fatalf("bounded detail missing %q: %q", expected, detail)
		}
	}
	if strings.Index(detail, "TestJourney/remote_resume") > strings.Index(detail, coverageRawOutputHeader) {
		t.Fatalf("failure index followed raw output: %q", detail)
	}
}

func TestBoundedCoverageParallelismLeavesOneEffectiveCPUForOtherAgents(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                       string
		requested, jobs, cpu, want int
	}{
		{name: "four core host", requested: 8, jobs: 17, cpu: 4, want: 3},
		{name: "single core host", requested: 8, jobs: 17, cpu: 1, want: 1},
		{name: "request below cpu limit", requested: 2, jobs: 17, cpu: 8, want: 2},
		{name: "jobs below cpu limit", requested: 8, jobs: 2, cpu: 8, want: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := boundedCoverageParallelism(tc.requested, tc.jobs, tc.cpu); got != tc.want {
				t.Fatalf("boundedCoverageParallelism(%d, %d, %d) = %d, want %d", tc.requested, tc.jobs, tc.cpu, got, tc.want)
			}
		})
	}
}

func TestCoverWithOptionsDurablyStoresOversizedShardedOutput(t *testing.T) {
	t.Parallel()
	module := t.TempDir()
	writeQualityFile(t, filepath.Join(module, "go.mod"), "module example.test/durable\n\ngo 1.26\n")
	writeQualityFile(t, filepath.Join(module, "serial", "serial.go"), "package serial\n\nfunc Value() int { return 1 }\n")
	writeQualityFile(t, filepath.Join(module, "serial", "serial_test.go"), `package serial

import (
	"strings"
	"testing"
)

func TestAlphaPasses(t *testing.T) { if Value() != 1 { t.Fatal("value") } }
func TestBetaFails(t *testing.T) {
	t.Log(strings.Repeat("oversized shard output ", 3000))
	t.Fatal("exact-failing-shard-test")
}
`)
	diagnosticsDir := filepath.Join(t.TempDir(), "reports")
	report := CoverWithOptions(context.Background(), "example/durable", module, RunOptions{
		GoTestShards:           2,
		GoShardPackages:        []string{"./serial"},
		CoverageDiagnosticsDir: diagnosticsDir,
	})
	if report.Status != StatusFailed {
		t.Fatalf("coverage status = %s, want failed", report.Status)
	}
	if len(report.Error) >= 1100 {
		t.Fatalf("bounded report error length = %d, want below the command detail cap", len(report.Error))
	}
	if report.Diagnostic == nil {
		t.Fatal("failed coverage did not reference durable diagnostics")
	}
	manifestRaw, err := os.ReadFile(report.Diagnostic.Manifest)
	if err != nil {
		t.Fatalf("read diagnostic manifest: %v", err)
	}
	var manifest CoverageDiagnosticManifest
	if err := yaml.Unmarshal(manifestRaw, &manifest); err != nil {
		t.Fatalf("parse diagnostic manifest: %v", err)
	}
	manifestDigest := sha256.Sum256(manifestRaw)
	if report.Diagnostic.SHA256 != fmt.Sprintf("%x", manifestDigest) {
		t.Fatalf("manifest digest = %q, want %x", report.Diagnostic.SHA256, manifestDigest)
	}
	if relative, err := filepath.Rel(diagnosticsDir, report.Diagnostic.Manifest); err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		t.Fatalf("manifest path escapes diagnostics directory: %q (err=%v)", report.Diagnostic.Manifest, err)
	}
	if len(manifest.Files) != 1 {
		t.Fatalf("diagnostic manifest files = %d, want one failed-shard artifact", len(manifest.Files))
	}
	artifact, err := os.ReadFile(manifest.Files[0].Path)
	if err != nil {
		t.Fatalf("read diagnostic artifact: %v", err)
	}
	if len(artifact) <= 1000 || !strings.Contains(string(artifact), "exact-failing-shard-test") || !strings.Contains(string(artifact), strings.Repeat("oversized shard output ", 3000)) {
		t.Fatalf("diagnostic artifact lost oversized shard output (bytes=%d)", len(artifact))
	}
	if relative, err := filepath.Rel(diagnosticsDir, manifest.Files[0].Path); err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		t.Fatalf("artifact path escapes diagnostics directory: %q (err=%v)", manifest.Files[0].Path, err)
	}
	digest := sha256.Sum256(artifact)
	if manifest.Files[0].SHA256 != fmt.Sprintf("%x", digest) {
		t.Fatalf("diagnostic digest = %q, want %x", manifest.Files[0].SHA256, digest)
	}
}

func TestRunWithOptionsRetriesAndTimesOut(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("test shell helper is POSIX-only")
	}
	dir := t.TempDir()
	countPath := filepath.Join(dir, "attempts")
	retryTool := filepath.Join(dir, "retry-tool")
	writeQualityFile(t, retryTool, "#!/bin/sh\ncount=0\nif [ -f \""+countPath+"\" ]; then count=$(cat \""+countPath+"\"); fi\ncount=$((count + 1))\nprintf '%s' \"$count\" > \""+countPath+"\"\nif [ \"$count\" -lt 2 ]; then exit 1; fi\n")
	if err := os.Chmod(retryTool, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, attempts, err := runWithOptions(context.Background(), RunOptions{Retry: 1}, dir, retryTool); err != nil || attempts != 2 {
		t.Fatalf("retry result = err %v, attempts %d", err, attempts)
	}
	timeoutTool := filepath.Join(dir, "timeout-tool")
	writeQualityFile(t, timeoutTool, "#!/bin/sh\nsleep 1\n")
	if err := os.Chmod(timeoutTool, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, attempts, err := runWithOptions(context.Background(), RunOptions{Timeout: 10 * time.Millisecond}, dir, timeoutTool); err == nil || attempts != 1 || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout result = err %v, attempts %d", err, attempts)
	}
}

func TestGoTestCommandCarriesTheWBTimeout(t *testing.T) {
	t.Parallel()
	if got := strings.Join(goCommand(CheckTest, false, 20*time.Minute), " "); got != "go test -timeout 20m0s ./..." {
		t.Fatalf("go test command=%q", got)
	}
	if got := strings.Join(goCommand(CheckTest, true, 0), " "); got != "go test -timeout 0 -p 1 ./..." {
		t.Fatalf("unbounded single-worker go test command=%q", got)
	}
}

func TestRunVerificationCheckTimeoutBoundsAllAttempts(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("test shell helper is POSIX-only")
	}
	dir := t.TempDir()
	tool := filepath.Join(dir, "slow-check")
	writeQualityFile(t, tool, "#!/bin/sh\nsleep 1\n")
	if err := os.Chmod(tool, 0o755); err != nil {
		t.Fatal(err)
	}
	entry := runVerification(context.Background(), RunOptions{Timeout: time.Second, Retry: 1, CheckTimeout: 10 * time.Millisecond}, "test", ".", CheckTest, dir, tool)
	if entry.Status != StatusFailed || entry.Attempts != 1 || !strings.Contains(entry.Detail, "check timed out after 10ms") {
		t.Fatalf("check deadline result = %+v", entry)
	}
}

func TestRunVerificationParentDeadlineWinsOverCheckDeadline(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("test shell helper is POSIX-only")
	}
	dir := t.TempDir()
	tool := filepath.Join(dir, "slow-check")
	writeQualityFile(t, tool, "#!/bin/sh\nsleep 1\n")
	if err := os.Chmod(tool, 0o755); err != nil {
		t.Fatal(err)
	}
	parent, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	t.Cleanup(func() { cancel() })
	entry := runVerification(parent, RunOptions{CheckTimeout: time.Second}, "test", ".", CheckTest, dir, tool)
	if entry.Status != StatusFailed || strings.Contains(entry.Detail, "check timed out") {
		t.Fatalf("parent deadline did not win: %+v", entry)
	}
}

func TestRunWithOptionsCancellationTerminatesForkedProcessTree(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("WB process-tree cancellation is supported on Darwin and Linux")
	}
	for _, test := range []struct {
		name, startupDelay string
	}{
		{name: "immediate", startupDelay: ""},
		// This exceeds the former one-second PID polling deadline. It proves
		// readiness, rather than a race with the attempt deadline, owns start.
		{name: "delayed-start", startupDelay: "sleep 1.2\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			pidsPath := filepath.Join(dir, "pids")
			tool := filepath.Join(dir, "forking-cancellation-tool")
			writeQualityFile(t, tool, "#!/bin/sh\n"+test.startupDelay+"sleep 30 &\nchild=$!\nprintf '%s %s' \"$$\" \"$child\" > \""+pidsPath+"\"\nwhile :; do sleep 1; done\n")
			if err := os.Chmod(tool, 0o755); err != nil {
				t.Fatal(err)
			}

			type result struct {
				output   string
				attempts int
				err      error
			}
			resultCh := make(chan result, 1)
			done := make(chan struct{})
			ctx, cancel := context.WithCancel(context.Background())
			var recordedPIDs []int
			parentGroupID := 0
			t.Cleanup(func() {
				cancel()
				drained := false
				select {
				case <-done:
					drained = true
				case <-time.After(time.Second):
				}
				// The assertions below normally prove these PIDs are already gone.
				// If a mutation regresses group cancellation and an assertion aborts
				// first, kill only the group whose recorded parent was proved to own
				// it, so this test never leaves its child sleep running for 30s.
				if parentGroupID != 0 && qualityProcessesAlive(recordedPIDs) {
					if err := syscall.Kill(-parentGroupID, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
						t.Errorf("kill recorded process group %d: %v", parentGroupID, err)
					}
					if drained {
						t.Error("recorded process survived cancellation; test cleanup killed its group")
					}
				}
				if !drained {
					select {
					case <-done:
					case <-time.After(time.Second):
						t.Error("forking process did not drain after test cleanup cancellation")
					}
				}
			})
			go func() {
				// The 30-second attempt deadline is only a safety net. The separate
				// real-deadline test above owns timeout-to-error mapping; this test
				// owns readiness and whole-process-tree cancellation.
				output, attempts, err := runWithOptions(ctx, RunOptions{Timeout: 30 * time.Second}, dir, tool)
				resultCh <- result{output: output, attempts: attempts, err: err}
				close(done)
			}()

			// The script owns readiness by writing both PIDs. Its bounded watchdog
			// detects a startup failure and reports an early command exit instead
			// of competing with the cancellation behavior being asserted.
			readinessDeadline := time.NewTimer(10 * time.Second)
			defer readinessDeadline.Stop()
			var pids []string
			for len(pids) == 0 {
				select {
				case outcome := <-resultCh:
					t.Fatalf("forking process exited before readiness: err %v, attempts %d, output %q", outcome.err, outcome.attempts, outcome.output)
				case <-readinessDeadline.C:
					t.Fatal("timed out waiting for forked process readiness")
				default:
				}
				raw, err := os.ReadFile(pidsPath)
				if err == nil && strings.TrimSpace(string(raw)) != "" {
					pids = strings.Fields(string(raw))
					break
				}
				if err != nil && !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("read %s: %v", pidsPath, err)
				}
				time.Sleep(10 * time.Millisecond)
			}
			if len(pids) != 2 {
				t.Fatalf("recorded PIDs = %q, want parent and child", pids)
			}
			for _, rawPID := range pids {
				pid, parseErr := strconv.Atoi(rawPID)
				if parseErr != nil || pid <= 0 {
					t.Fatalf("recorded PID %q: %v", rawPID, parseErr)
				}
				recordedPIDs = append(recordedPIDs, pid)
				assertQualityProcessAlive(t, pid)
			}
			parentGroupID = qualityOwnedProcessGroup(t, recordedPIDs[0])
			cancel()
			var outcome result
			select {
			case outcome = <-resultCh:
			case <-time.After(5 * time.Second):
				t.Fatal("forking process did not return within five seconds of cancellation")
			}
			if ctx.Err() != context.Canceled || outcome.err == nil || strings.Contains(outcome.err.Error(), "timed out after") || outcome.attempts != 1 {
				t.Fatalf("cancellation result = context %v, err %v, attempts %d, output %q", ctx.Err(), outcome.err, outcome.attempts, outcome.output)
			}
			for _, rawPID := range pids {
				pid, parseErr := strconv.Atoi(rawPID)
				if parseErr != nil || pid <= 0 {
					t.Fatalf("recorded PID %q: %v", rawPID, parseErr)
				}
				assertQualityProcessGone(t, pid)
			}
		})
	}
}

func assertQualityProcessAlive(t *testing.T, pid int) {
	t.Helper()
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("probe ready PID %d: %v", pid, err)
	}
}

func qualityOwnedProcessGroup(t *testing.T, pid int) int {
	t.Helper()
	output, err := exec.Command("ps", "-o", "pgid=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		t.Fatalf("read process group for %d: %v", pid, err)
	}
	groupID, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil || groupID != pid {
		t.Fatalf("process group for recorded parent %d = %q; want its own group", pid, output)
	}
	return groupID
}

func qualityProcessesAlive(pids []int) bool {
	for _, pid := range pids {
		if syscall.Kill(pid, 0) == nil {
			return true
		}
	}
	return false
}

func assertQualityProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if err != nil {
			t.Fatalf("probe PID %d: %v", pid, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("forked process PID %d survived cancellation", pid)
}

func checkStrings(checks []Check) []string {
	values := make([]string, len(checks))
	for index, check := range checks {
		values[index] = string(check)
	}
	return values
}

func writeQualityFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
