package quality

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestCoverAggregatesGoStatements(t *testing.T) {
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

func TestVerifySpecScoreConfiguration(t *testing.T) {
	t.Run("configured missing root fails closed", func(t *testing.T) {
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

func TestShardedCoverageFailureIndexPrecedesRawOutputAndSurvivesTruncation(t *testing.T) {
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

func TestRunVerificationCheckTimeoutBoundsAllAttempts(t *testing.T) {
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
	defer cancel()
	entry := runVerification(parent, RunOptions{CheckTimeout: time.Second}, "test", ".", CheckTest, dir, tool)
	if entry.Status != StatusFailed || strings.Contains(entry.Detail, "check timed out") {
		t.Fatalf("parent deadline did not win: %+v", entry)
	}
}

func TestRunWithOptionsCancellationTerminatesForkedProcessTree(t *testing.T) {
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
