package deps

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// npmPackageJSONWithDependency renders a minimal but Prettier-shaped
// package.json (one key per line, 2-space indent). scanNpmPackageJSONRefs
// tracks object nesting through indentation the way every package.json in
// this fleet is actually formatted, so a compact single-line fixture would
// not exercise it realistically; every fixture in this file goes through
// this helper instead of writing raw single-line JSON.
func npmPackageJSONWithDependency(name, dependencyKey, dependencyVersion string) string {
	return fmt.Sprintf("{\n  \"name\": %q,\n  \"dependencies\": {\n    %q: %q\n  }\n}\n", name, dependencyKey, dependencyVersion)
}

func seedNpmInspectRepository(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for path, body := range files {
		writeTestFile(t, filepath.Join(dir, path), body)
	}
	runTestGit(t, dir, "init", "-b", "main")
	runTestGit(t, dir, "config", "user.name", "WB Test")
	runTestGit(t, dir, "config", "user.email", "wb@example.test")
	runTestGit(t, dir, "add", "-A")
	runTestGit(t, dir, "commit", "-m", "initial")
	return dir
}

func TestNpmAdapterInspectFindsExistingReferenceAcrossManifestTypes(t *testing.T) {
	t.Parallel()
	dir := seedNpmInspectRepository(t, map[string]string{
		"package.json":        npmPackageJSONWithDependency("@sneat/app", "@sneat/core", "1.2.3"),
		"pnpm-workspace.yaml": "packages:\n  - \"packages/*\"\n\noverrides:\n  \"@sneat/core\": \"1.2.3\"\n",
	})
	target := Target{Ecosystem: EcosystemNPM, Dependency: "@sneat/core", Version: "1.3.0"}
	decisions, err := (npmAdapter{}).inspect(context.Background(), dir, "HEAD", target, Options{Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 2 {
		t.Fatalf("decisions = %+v, want one for package.json and one for pnpm-workspace.yaml", decisions)
	}
	byFile := map[string]Decision{}
	for _, decision := range decisions {
		byFile[decision.File] = decision
	}
	if decision := byFile["package.json"]; decision.Action != "planned" || decision.BeforeVersion != "1.2.3" || decision.AfterVersion != "1.3.0" {
		t.Fatalf("package.json decision = %+v", decision)
	}
	if decision := byFile["pnpm-workspace.yaml"]; decision.Action != "planned" || decision.BeforeVersion != "1.2.3" {
		t.Fatalf("pnpm-workspace.yaml decision = %+v", decision)
	}
}

func TestNpmAdapterInspectBlocksDowngrade(t *testing.T) {
	t.Parallel()
	dir := seedNpmInspectRepository(t, map[string]string{
		"package.json": npmPackageJSONWithDependency("@sneat/app", "@sneat/core", "2.0.0"),
	})
	target := Target{Ecosystem: EcosystemNPM, Dependency: "@sneat/core", Version: "1.9.0"}
	decisions, err := (npmAdapter{}).inspect(context.Background(), dir, "HEAD", target, Options{Timeout: time.Minute})
	if err == nil || !strings.Contains(err.Error(), "lower than observed version") {
		t.Fatalf("error = %v, want a blocked downgrade", err)
	}
	if len(decisions) != 1 || decisions[0].Action != "blocked_downgrade" {
		t.Fatalf("decisions = %+v", decisions)
	}
}

func TestNpmAdapterInspectReturnsNothingWhenDependencyAbsent(t *testing.T) {
	t.Parallel()
	dir := seedNpmInspectRepository(t, map[string]string{
		"package.json": npmPackageJSONWithDependency("@sneat/app", "lodash", "^4.17.21"),
	})
	target := Target{Ecosystem: EcosystemNPM, Dependency: "@sneat/core", Version: "1.3.0"}
	decisions, err := (npmAdapter{}).inspect(context.Background(), dir, "HEAD", target, Options{Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 0 {
		t.Fatalf("decisions = %+v, want none", decisions)
	}
}

// writeFakePnpm installs a fake `pnpm` executable at the front of PATH for
// this test only. Real lockfile regeneration talks to the registry, and this
// package's tests must stay hermetic and network-free, so every apply() test
// below scripts pnpm's observable behavior (accept --lockfile-only, verify
// --frozen-lockfile) instead of requiring pnpm to be installed. Because it
// mutates PATH with t.Setenv, the caller must not run in parallel with
// anything else that also mutates the environment.
func writeFakePnpm(t *testing.T, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake pnpm shim requires a POSIX shell")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "pnpm")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// fakePnpmRegenerateAndVerify behaves like a real `pnpm install`: an
// unqualified `--lockfile-only` call regenerates (marks) the lockfile in the
// current directory, and a `--frozen-lockfile --lockfile-only` call only
// succeeds if that regeneration already happened — modeling exactly the
// ERR_PNPM_LOCKFILE_CONFIG_MISMATCH failure a skipped regeneration produces.
const fakePnpmRegenerateAndVerify = `
if [ "$1" != "install" ]; then
  exit 1
fi
shift
frozen=0
for arg in "$@"; do
  if [ "$arg" = "--frozen-lockfile" ]; then frozen=1; fi
done
if [ "$frozen" = "1" ]; then
  grep -q REGENERATED pnpm-lock.yaml 2>/dev/null && exit 0
  echo "ERR_PNPM_LOCKFILE_CONFIG_MISMATCH" >&2
  exit 1
fi
echo "REGENERATED" >> pnpm-lock.yaml
exit 0
`

func TestNpmAdapterApplyUpdatesManifestsAndRegeneratesLockfile(t *testing.T) {
	writeFakePnpm(t, fakePnpmRegenerateAndVerify)
	worktree := t.TempDir()
	writeTestFile(t, filepath.Join(worktree, "package.json"), `{
  "name": "@sneat/app",
  "dependencies": {
    "@sneat/core": "1.2.3"
  }
}
`)
	writeTestFile(t, filepath.Join(worktree, "pnpm-lock.yaml"), "lockfileVersion: '9.0'\n")

	target := Target{Ecosystem: EcosystemNPM, Dependency: "@sneat/core", Version: "1.3.0"}
	decisions, err := (npmAdapter{}).apply(context.Background(), worktree, target, Options{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("apply: %v (decisions=%+v)", err, decisions)
	}

	packageJSON, err := os.ReadFile(filepath.Join(worktree, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(packageJSON), `"@sneat/core": "1.3.0"`) {
		t.Fatalf("package.json was not updated:\n%s", packageJSON)
	}
	lockfile, err := os.ReadFile(filepath.Join(worktree, "pnpm-lock.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(lockfile), "REGENERATED") {
		t.Fatalf("lockfile was not regenerated:\n%s", lockfile)
	}

	var manifestDecision, lockfileDecision *Decision
	for index := range decisions {
		switch decisions[index].File {
		case "package.json":
			manifestDecision = &decisions[index]
		case "pnpm-lock.yaml":
			lockfileDecision = &decisions[index]
		}
	}
	if manifestDecision == nil || manifestDecision.Action != "updated" {
		t.Fatalf("manifest decision = %+v", manifestDecision)
	}
	if lockfileDecision == nil || lockfileDecision.Action != "lockfile_regenerated" {
		t.Fatalf("lockfile decision = %+v", lockfileDecision)
	}
}

func TestNpmAdapterApplyUpdatesPnpmWorkspaceOverrides(t *testing.T) {
	writeFakePnpm(t, fakePnpmRegenerateAndVerify)
	worktree := t.TempDir()
	// package.json itself does not reference @sneat/core at all — this fleet
	// pins cross-package versions in pnpm-workspace.yaml overrides, which
	// pnpm 11 reads instead of the legacy package.json `pnpm.overrides`
	// field, so the override file is the only place a mismatch can silently
	// slip through.
	writeTestFile(t, filepath.Join(worktree, "package.json"), `{
  "name": "@sneat/app",
  "dependencies": {
    "lodash": "^4.17.21"
  }
}
`)
	writeTestFile(t, filepath.Join(worktree, "pnpm-workspace.yaml"), `packages:
  - "packages/*"

overrides:
  "@sneat/core": "1.2.3"
`)
	writeTestFile(t, filepath.Join(worktree, "pnpm-lock.yaml"), "lockfileVersion: '9.0'\n")

	target := Target{Ecosystem: EcosystemNPM, Dependency: "@sneat/core", Version: "1.3.0"}
	decisions, err := (npmAdapter{}).apply(context.Background(), worktree, target, Options{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("apply: %v (decisions=%+v)", err, decisions)
	}

	workspace, err := os.ReadFile(filepath.Join(worktree, "pnpm-workspace.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(workspace), `"@sneat/core": "1.3.0"`) {
		t.Fatalf("pnpm-workspace.yaml override was not updated:\n%s", workspace)
	}
	packageJSON, err := os.ReadFile(filepath.Join(worktree, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(packageJSON), "@sneat/core") {
		t.Fatalf("package.json must stay untouched when it never referenced the dependency:\n%s", packageJSON)
	}

	var workspaceDecision, lockfileDecision *Decision
	for index := range decisions {
		switch decisions[index].File {
		case "pnpm-workspace.yaml":
			workspaceDecision = &decisions[index]
		case "pnpm-lock.yaml":
			lockfileDecision = &decisions[index]
		}
	}
	if workspaceDecision == nil || workspaceDecision.Action != "updated" {
		t.Fatalf("pnpm-workspace.yaml decision = %+v", workspaceDecision)
	}
	if lockfileDecision == nil || lockfileDecision.Action != "lockfile_regenerated" {
		t.Fatalf("lockfile decision = %+v; the overrides-only change must still trigger lockfile regeneration", lockfileDecision)
	}
}

// TestNpmAdapterApplyRegeneratesEachIndependentLockfileScope is the
// multi-lockfile-repo case the task explicitly asked for: a repository like
// sneat-apps that owns more than one pnpm workspace (a root workspace and a
// landings/ subtree with its own pnpm-workspace.yaml and pnpm-lock.yaml).
// Both lockfiles reference the changed dependency and must each be
// regenerated and verified independently, in their own directory.
func TestNpmAdapterApplyRegeneratesEachIndependentLockfileScope(t *testing.T) {
	writeFakePnpm(t, fakePnpmRegenerateAndVerify)
	worktree := t.TempDir()
	writeTestFile(t, filepath.Join(worktree, "package.json"), `{
  "name": "@sneat/apps-root",
  "dependencies": {
    "@sneat/core": "1.2.3"
  }
}
`)
	writeTestFile(t, filepath.Join(worktree, "pnpm-lock.yaml"), "lockfileVersion: '9.0'\n")
	writeTestFile(t, filepath.Join(worktree, "landings", "package.json"), `{
  "name": "@sneat/apps-landings",
  "dependencies": {
    "@sneat/core": "1.2.3"
  }
}
`)
	writeTestFile(t, filepath.Join(worktree, "landings", "pnpm-lock.yaml"), "lockfileVersion: '9.0'\n")

	target := Target{Ecosystem: EcosystemNPM, Dependency: "@sneat/core", Version: "1.3.0"}
	decisions, err := (npmAdapter{}).apply(context.Background(), worktree, target, Options{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("apply: %v (decisions=%+v)", err, decisions)
	}

	rootLockfile, err := os.ReadFile(filepath.Join(worktree, "pnpm-lock.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rootLockfile), "REGENERATED") {
		t.Fatalf("root pnpm-lock.yaml was not regenerated:\n%s", rootLockfile)
	}
	landingsLockfile, err := os.ReadFile(filepath.Join(worktree, "landings", "pnpm-lock.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(landingsLockfile), "REGENERATED") {
		t.Fatalf("landings/pnpm-lock.yaml was not regenerated independently:\n%s", landingsLockfile)
	}

	lockfileDecisionsByFile := map[string]Decision{}
	for _, decision := range decisions {
		if decision.Action == "lockfile_regenerated" {
			lockfileDecisionsByFile[decision.File] = decision
		}
	}
	if _, ok := lockfileDecisionsByFile["pnpm-lock.yaml"]; !ok {
		t.Fatalf("no lockfile_regenerated decision for the root lockfile: %+v", decisions)
	}
	if _, ok := lockfileDecisionsByFile[filepath.ToSlash(filepath.Join("landings", "pnpm-lock.yaml"))]; !ok {
		t.Fatalf("no lockfile_regenerated decision for landings/pnpm-lock.yaml: %+v", decisions)
	}
}

func TestNpmAdapterApplyFailsWhenLockfileRegenerationFails(t *testing.T) {
	writeFakePnpm(t, `
if [ "$1" = "install" ]; then
  echo "pnpm ERR_FAKE_REGISTRY_UNREACHABLE" >&2
  exit 1
fi
exit 1
`)
	worktree := t.TempDir()
	writeTestFile(t, filepath.Join(worktree, "package.json"), npmPackageJSONWithDependency("@sneat/app", "@sneat/core", "1.2.3"))
	writeTestFile(t, filepath.Join(worktree, "pnpm-lock.yaml"), "lockfileVersion: '9.0'\n")

	target := Target{Ecosystem: EcosystemNPM, Dependency: "@sneat/core", Version: "1.3.0"}
	decisions, err := (npmAdapter{}).apply(context.Background(), worktree, target, Options{Timeout: 10 * time.Second})
	if err == nil || !strings.Contains(err.Error(), "ERR_FAKE_REGISTRY_UNREACHABLE") {
		t.Fatalf("error = %v, want the lockfile regeneration failure surfaced", err)
	}
	var lockfileDecision *Decision
	for index := range decisions {
		if decisions[index].File == "pnpm-lock.yaml" {
			lockfileDecision = &decisions[index]
		}
	}
	if lockfileDecision == nil || lockfileDecision.Action != "lockfile_regeneration_failed" {
		t.Fatalf("lockfile decision = %+v, want lockfile_regeneration_failed", lockfileDecision)
	}
	// The manifest write itself must have gone through: apply() must not
	// silently drop a completed manifest edit just because the lockfile step
	// failed afterward — the caller sees a failed operation either way, but
	// the reported decisions must be honest about what actually happened.
	packageJSON, err := os.ReadFile(filepath.Join(worktree, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(packageJSON), `"1.3.0"`) {
		t.Fatalf("package.json was not written before the lockfile step failed:\n%s", packageJSON)
	}
}

func TestNpmAdapterApplyFailsWhenFrozenProbeIsInconsistent(t *testing.T) {
	writeFakePnpm(t, `
if [ "$1" != "install" ]; then
  exit 1
fi
shift
for arg in "$@"; do
  if [ "$arg" = "--frozen-lockfile" ]; then
    echo "ERR_PNPM_LOCKFILE_CONFIG_MISMATCH" >&2
    exit 1
  fi
done
exit 0
`)
	worktree := t.TempDir()
	writeTestFile(t, filepath.Join(worktree, "package.json"), npmPackageJSONWithDependency("@sneat/app", "@sneat/core", "1.2.3"))
	writeTestFile(t, filepath.Join(worktree, "pnpm-lock.yaml"), "lockfileVersion: '9.0'\n")

	target := Target{Ecosystem: EcosystemNPM, Dependency: "@sneat/core", Version: "1.3.0"}
	decisions, err := (npmAdapter{}).apply(context.Background(), worktree, target, Options{Timeout: 10 * time.Second})
	if err == nil || !strings.Contains(err.Error(), "ERR_PNPM_LOCKFILE_CONFIG_MISMATCH") {
		t.Fatalf("error = %v, want the frozen-lockfile mismatch surfaced", err)
	}
	var lockfileDecision *Decision
	for index := range decisions {
		if decisions[index].File == "pnpm-lock.yaml" {
			lockfileDecision = &decisions[index]
		}
	}
	if lockfileDecision == nil || lockfileDecision.Action != "lockfile_verification_failed" {
		t.Fatalf("lockfile decision = %+v, want lockfile_verification_failed", lockfileDecision)
	}
}

func TestNpmAdapterApplyBlocksDowngradeBeforeWritingAnything(t *testing.T) {
	worktree := t.TempDir()
	writeTestFile(t, filepath.Join(worktree, "package.json"), npmPackageJSONWithDependency("@sneat/app", "@sneat/core", "2.0.0"))
	target := Target{Ecosystem: EcosystemNPM, Dependency: "@sneat/core", Version: "1.9.0"}
	decisions, err := (npmAdapter{}).apply(context.Background(), worktree, target, Options{Timeout: time.Minute})
	if err == nil || !strings.Contains(err.Error(), "lower than observed version") {
		t.Fatalf("error = %v, want a blocked downgrade", err)
	}
	if len(decisions) != 1 || decisions[0].Action != "blocked_downgrade" {
		t.Fatalf("decisions = %+v", decisions)
	}
	packageJSON, err := os.ReadFile(filepath.Join(worktree, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(packageJSON), "2.0.0") {
		t.Fatalf("a blocked downgrade must not write anything:\n%s", packageJSON)
	}
}

func TestNpmAdapterApplyReturnsNothingWhenDependencyAbsentFromRepository(t *testing.T) {
	worktree := t.TempDir()
	writeTestFile(t, filepath.Join(worktree, "package.json"), npmPackageJSONWithDependency("@sneat/app", "lodash", "^4.17.21"))
	target := Target{Ecosystem: EcosystemNPM, Dependency: "@sneat/core", Version: "1.3.0"}
	decisions, err := (npmAdapter{}).apply(context.Background(), worktree, target, Options{Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 0 {
		t.Fatalf("decisions = %+v, want none", decisions)
	}
}

func TestNpmAdapterApplyDoesNotTouchLockfilesWhenRepositoryHasNone(t *testing.T) {
	worktree := t.TempDir()
	writeTestFile(t, filepath.Join(worktree, "package.json"), npmPackageJSONWithDependency("@sneat/app", "@sneat/core", "1.2.3"))
	target := Target{Ecosystem: EcosystemNPM, Dependency: "@sneat/core", Version: "1.3.0"}
	// No fake pnpm on PATH at all: if apply() tried to regenerate anything
	// without a lockfile present, this would fail with "executable file not
	// found in $PATH" instead of succeeding quietly.
	decisions, err := (npmAdapter{}).apply(context.Background(), worktree, target, Options{Timeout: time.Minute})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	for _, decision := range decisions {
		if strings.HasPrefix(decision.Action, "lockfile_") {
			t.Fatalf("unexpected lockfile decision without any lockfile present: %+v", decision)
		}
	}
}

func TestValidateNpmPackageNameAcceptsScopedAndPlainNames(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"@sneat/core", "lodash", "@sneat/deps-set-cli", "a"} {
		if err := validateNpmPackageName(name); err != nil {
			t.Errorf("validateNpmPackageName(%q) = %v, want valid", name, err)
		}
	}
	for _, name := range []string{"", "@Sneat/Core", "UPPER", "@no-scope-name/", "@/missing-scope", strings.Repeat("a", 215)} {
		if err := validateNpmPackageName(name); err == nil {
			t.Errorf("validateNpmPackageName(%q) = nil, want an error", name)
		}
	}
}

func TestUniversalSemverAcceptsBothGoAndNpmVersionStyles(t *testing.T) {
	t.Parallel()
	if !universalSemverValid("v1.2.3") || !universalSemverValid("1.2.3") {
		t.Fatal("both v-prefixed and bare exact versions must be valid")
	}
	if universalSemverValid("^1.2.3") || universalSemverValid("workspace:*") || universalSemverValid("latest") {
		t.Fatal("ranges and protocol specifiers must not be treated as exact versions")
	}
	if universalSemverCompare("1.3.0", "v1.2.0") <= 0 {
		t.Fatal("comparison must work across the two prefix styles")
	}
	if !comparableNpmDowngrade("2.0.0", "1.9.0") {
		t.Fatal("an exact npm downgrade must be detected")
	}
	if comparableNpmDowngrade("^2.0.0", "1.9.0") {
		t.Fatal("a range must never be treated as a comparable downgrade")
	}
}
