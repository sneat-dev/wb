package deps

// Coverage tests for the go, github-actions, npm, and npm-range adapters.
//
// Everything here is hermetic: repositories are seeded into t.TempDir(), lock
// and module tooling is replaced by a fake executable at the front of PATH
// whose every invocation is logged, and no test reaches the network or real
// GitHub. A test that must launch a fake executable is skipped on Windows
// (there is no /bin/sh); the file itself still compiles there.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/testenv"
)

// ---------------------------------------------------------------------------
// Fake-tool machinery
// ---------------------------------------------------------------------------

// depsCovPOSIXShimSkip marks a test that launches a fake executable written as
// a POSIX shell script. Windows has no /bin/sh, so the case is skipped there —
// the same guard writeFakePnpm already uses.
func depsCovPOSIXShimSkip(t *testing.T, tool string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake " + tool + " shim requires a POSIX shell")
	}
}

func depsCovShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// depsCovInstallShim writes a fake `tool` into a fresh directory, puts that
// directory at the front of PATH, and returns the path of a log file the shim
// appends one line to per invocation. The test then proves both that the fake
// (not a real binary that could touch the network) handled each call and that
// it was handed the arguments the adapter is supposed to construct.
func depsCovInstallShim(t *testing.T, tool, body string) string {
	t.Helper()
	depsCovPOSIXShimSkip(t, tool)
	dir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), tool+".log")
	script := "#!/bin/sh\n" +
		"printf 'args=%s gowork=%s\\n' \"$*\" \"$GOWORK\" >> " + depsCovShellQuote(logPath) + "\n" +
		body
	path := filepath.Join(dir, tool)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	resolved, err := exec.LookPath(tool)
	if err != nil {
		t.Fatalf("LookPath(%s): %v", tool, err)
	}
	if resolved != path {
		t.Fatalf("PATH override did not take effect for %s: resolved %q, want %q", tool, resolved, path)
	}
	return logPath
}

func depsCovShimLog(t *testing.T, logPath string) string {
	t.Helper()
	contents, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading shim log %s: %v", logPath, err)
	}
	return string(contents)
}

func depsCovShimNotRun(t *testing.T, logPath string) {
	t.Helper()
	if _, err := os.Stat(logPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("shim log %s exists (stat err = %v); the fake tool ran when it must not have", logPath, err)
	}
}

// depsCovGoShim scripts a fake `go` binary. Each verb reports its own exit code
// and optional stderr, so one generator covers every apply() failure branch.
type depsCovGoShim struct {
	getExit  int
	tidyExit int
	listExit int
	getErr   string
	tidyErr  string
	listErr  string
	listOut  string
}

func depsCovInstallGoShim(t *testing.T, config depsCovGoShim) string {
	t.Helper()
	var body strings.Builder
	body.WriteString("case \"$1\" in\n")
	body.WriteString("  get)\n")
	if config.getErr != "" {
		body.WriteString("    printf '%s\\n' " + depsCovShellQuote(config.getErr) + " >&2\n")
	}
	body.WriteString("    exit " + strconv.Itoa(config.getExit) + " ;;\n")
	body.WriteString("  mod)\n")
	if config.tidyErr != "" {
		body.WriteString("    printf '%s\\n' " + depsCovShellQuote(config.tidyErr) + " >&2\n")
	}
	body.WriteString("    exit " + strconv.Itoa(config.tidyExit) + " ;;\n")
	body.WriteString("  list)\n")
	if config.listOut != "" {
		body.WriteString("    printf '%s\\n' " + depsCovShellQuote(config.listOut) + "\n")
	}
	if config.listErr != "" {
		body.WriteString("    printf '%s\\n' " + depsCovShellQuote(config.listErr) + " >&2\n")
	}
	body.WriteString("    exit " + strconv.Itoa(config.listExit) + " ;;\n")
	body.WriteString("esac\nexit 1\n")
	return depsCovInstallShim(t, "go", body.String())
}

func depsCovInstallNpmShim(t *testing.T, failMessage string) string {
	t.Helper()
	body := "exit 0\n"
	if failMessage != "" {
		body = "printf '%s\\n' " + depsCovShellQuote(failMessage) + " >&2\nexit 1\n"
	}
	return depsCovInstallShim(t, "npm", body)
}

// depsCovFakeGitScript renders a fake `git` that prints lines and exits 0.
func depsCovFakeGitScript(lines ...string) string {
	var body strings.Builder
	for _, line := range lines {
		body.WriteString("printf '%s\\n' " + depsCovShellQuote(line) + "\n")
	}
	body.WriteString("exit 0\n")
	return body.String()
}

// ---------------------------------------------------------------------------
// Fixture machinery
// ---------------------------------------------------------------------------

// depsCovWriteGoMod writes a minimal but valid go.mod. An empty dependency
// produces a module that declares no requirement at all.
func depsCovWriteGoMod(t *testing.T, dir, modulePath, dependency, version string) {
	t.Helper()
	body := "module " + modulePath + "\n\ngo 1.24\n"
	if dependency != "" {
		body += "\nrequire " + dependency + " " + version + "\n"
	}
	writeTestFile(t, filepath.Join(dir, "go.mod"), body)
}

// depsCovPhantomCommit returns the id of a commit whose tree lists name as a
// gitlink to an object that does not exist. `git ls-tree -r --name-only` still
// prints the path, but `git show <commit>:<name>` fails, which is the only
// deterministic way to reach the adapters' failed blob-read branches without
// racing a concurrent filesystem change (a gitlink may legitimately point at an
// absent object; write-tree and commit-tree accept it).
func depsCovPhantomCommit(t *testing.T, dir, name string) string {
	t.Helper()
	const absent = "2222222222222222222222222222222222222222"
	runTestGit(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+absent+","+name)
	tree := strings.TrimSpace(runTestGit(t, dir, "write-tree"))
	return strings.TrimSpace(runTestGit(t, dir, "commit-tree", tree, "-m", "phantom "+name))
}

func depsCovGoTarget(version string) Target {
	return Target{Ecosystem: EcosystemGo, Dependency: "example.com/model", Version: version}
}

func depsCovNpmTarget(dependency, version string) Target {
	return Target{Ecosystem: EcosystemNPM, Dependency: dependency, Version: version}
}

// ---------------------------------------------------------------------------
// go.go
// ---------------------------------------------------------------------------

func TestDepsCovAdaptersGoInspectPlansAndSortsModules(t *testing.T) {
	t.Parallel()
	dir := seedNpmInspectRepository(t, map[string]string{
		"go.mod":                "module example.com/app\n\ngo 1.24\n\nrequire example.com/model v0.2.0\n",
		"nested/a/go.mod":       "module example.com/a\n\ngo 1.24\n\nrequire example.com/model v0.1.0\n",
		"nested/b/go.mod":       "module example.com/b\n\ngo 1.24\n\nrequire example.com/other v1.0.0\n",
		"testdata/go.mod":       "module example.com/fixture\n\ngo 1.24\n\nrequire example.com/model v0.9.9\n",
		"vendor/go.mod":         "module example.com/vendored\n\ngo 1.24\n\nrequire example.com/model v0.8.8\n",
		"README.md":             "not a manifest\n",
		".github/workflows.yml": "unrelated\n",
	})
	decisions, err := (goAdapter{}).inspect(context.Background(), dir, "HEAD", depsCovGoTarget("v0.2.0"), Options{Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 2 {
		t.Fatalf("decisions = %+v, want exactly the two production go.mod files", decisions)
	}
	if decisions[0].File != "go.mod" || decisions[1].File != "nested/a/go.mod" {
		t.Fatalf("decisions are not sorted by file: %+v", decisions)
	}
	unchanged := decisions[0]
	if unchanged.Action != "unchanged" || unchanged.BeforeVersion != "v0.2.0" || unchanged.AfterVersion != "v0.2.0" {
		t.Fatalf("already-exact requirement decision = %+v", unchanged)
	}
	if unchanged.Ecosystem != EcosystemGo || unchanged.Selector != "require:example.com/model" || unchanged.ResolvedRef != "v0.2.0" {
		t.Fatalf("decision identity fields = %+v", unchanged)
	}
	if unchanged.Reason != "requirement already declares the exact target version" {
		t.Fatalf("unchanged reason = %q", unchanged.Reason)
	}
	planned := decisions[1]
	if planned.Action != "planned" || planned.BeforeVersion != "v0.1.0" || planned.AfterVersion != "v0.2.0" || planned.AfterRef != "v0.2.0" {
		t.Fatalf("planned requirement decision = %+v", planned)
	}
	if planned.Reason != "existing Go requirement will be set with official Go tooling" {
		t.Fatalf("planned reason = %q", planned.Reason)
	}
}

func TestDepsCovAdaptersGoInspectPropagatesFailures(t *testing.T) {
	t.Run("ls-tree fails", func(t *testing.T) {
		dir := seedNpmInspectRepository(t, map[string]string{"go.mod": "module example.com/app\n\ngo 1.24\n"})
		decisions, err := (goAdapter{}).inspect(context.Background(), dir, "no-such-base", depsCovGoTarget("v0.2.0"), Options{Timeout: time.Minute})
		if err == nil {
			t.Fatal("inspect succeeded against a nonexistent base ref")
		}
		if len(decisions) != 0 {
			t.Fatalf("decisions = %+v, want none", decisions)
		}
	})

	t.Run("git show fails", func(t *testing.T) {
		dir := seedNpmInspectRepository(t, map[string]string{"seed.txt": "seed\n"})
		commit := depsCovPhantomCommit(t, dir, "go.mod")
		decisions, err := (goAdapter{}).inspect(context.Background(), dir, commit, depsCovGoTarget("v0.2.0"), Options{Timeout: time.Minute})
		if err == nil {
			t.Fatal("inspect succeeded although the listed go.mod blob cannot be read")
		}
		if len(decisions) != 0 {
			t.Fatalf("decisions = %+v, want none", decisions)
		}
	})

	t.Run("unparsable manifest fails", func(t *testing.T) {
		dir := seedNpmInspectRepository(t, map[string]string{"go.mod": "this is not a module file\n"})
		_, err := (goAdapter{}).inspect(context.Background(), dir, "HEAD", depsCovGoTarget("v0.2.0"), Options{Timeout: time.Minute})
		if err == nil || !strings.Contains(err.Error(), "parse go.mod") {
			t.Fatalf("error = %v, want a parse failure naming go.mod", err)
		}
	})

	t.Run("downgrade is blocked", func(t *testing.T) {
		dir := seedNpmInspectRepository(t, map[string]string{"go.mod": "module example.com/app\n\ngo 1.24\n\nrequire example.com/model v1.9.0\n"})
		decisions, err := (goAdapter{}).inspect(context.Background(), dir, "HEAD", depsCovGoTarget("v1.0.0"), Options{Timeout: time.Minute})
		if err == nil || !strings.Contains(err.Error(), "lower than observed version") || !strings.Contains(err.Error(), "--allow-downgrade") {
			t.Fatalf("error = %v, want a blocked-downgrade diagnostic", err)
		}
		if len(decisions) != 1 || decisions[0].Action != "blocked_downgrade" || decisions[0].BeforeVersion != "v1.9.0" {
			t.Fatalf("decisions = %+v", decisions)
		}
	})
}

func TestDepsCovAdaptersGoApplyReportsGoGetFailure(t *testing.T) {
	testenv.Isolate(t)
	logPath := depsCovInstallGoShim(t, depsCovGoShim{getExit: 1, getErr: "GO_GET_FAILED"})
	worktree := t.TempDir()
	depsCovWriteGoMod(t, worktree, "example.com/app", "example.com/model", "v0.1.0")

	decisions, err := (goAdapter{}).apply(context.Background(), worktree, depsCovGoTarget("v0.2.0"), Options{Timeout: time.Minute})
	if err == nil || !strings.Contains(err.Error(), "GO_GET_FAILED") {
		t.Fatalf("error = %v, want the go get failure surfaced", err)
	}
	if !strings.Contains(err.Error(), "go.mod") {
		t.Fatalf("error = %v, want the failing module named", err)
	}
	if len(decisions) != 1 || decisions[0].Action != "failed" || decisions[0].BeforeVersion != "v0.1.0" {
		t.Fatalf("decisions = %+v", decisions)
	}
	if !strings.Contains(decisions[0].Reason, "GO_GET_FAILED") {
		t.Fatalf("decision reason = %q, want the tooling failure", decisions[0].Reason)
	}
	log := depsCovShimLog(t, logPath)
	if !strings.Contains(log, "args=get example.com/model@v0.2.0") {
		t.Fatalf("go get was not invoked as expected:\n%s", log)
	}
	if strings.Contains(log, "mod tidy") {
		t.Fatalf("go mod tidy ran after go get failed:\n%s", log)
	}
	if contents := mustReadFile(t, filepath.Join(worktree, "go.mod")); !strings.Contains(contents, "example.com/model v0.1.0") {
		t.Fatalf("a failed go get must not rewrite go.mod:\n%s", contents)
	}
}

func TestDepsCovAdaptersGoApplyReportsTidyFailure(t *testing.T) {
	testenv.Isolate(t)
	logPath := depsCovInstallGoShim(t, depsCovGoShim{tidyExit: 1, tidyErr: "GO_TIDY_FAILED"})
	worktree := t.TempDir()
	depsCovWriteGoMod(t, worktree, "example.com/app", "example.com/model", "v0.1.0")

	decisions, err := (goAdapter{}).apply(context.Background(), worktree, depsCovGoTarget("v0.2.0"), Options{Timeout: time.Minute})
	if err == nil || !strings.Contains(err.Error(), "GO_TIDY_FAILED") {
		t.Fatalf("error = %v, want the go mod tidy failure surfaced", err)
	}
	if len(decisions) != 1 || decisions[0].Action != "failed" || !strings.Contains(decisions[0].Reason, "GO_TIDY_FAILED") {
		t.Fatalf("decisions = %+v", decisions)
	}
	log := depsCovShimLog(t, logPath)
	if !strings.Contains(log, "args=mod tidy") {
		t.Fatalf("go mod tidy was not invoked:\n%s", log)
	}
	if strings.Contains(log, "args=list") {
		t.Fatalf("go list ran after go mod tidy failed:\n%s", log)
	}
}

func TestDepsCovAdaptersGoApplyReportsListFailure(t *testing.T) {
	testenv.Isolate(t)
	logPath := depsCovInstallGoShim(t, depsCovGoShim{listExit: 1, listErr: "GO_LIST_FAILED"})
	worktree := t.TempDir()
	depsCovWriteGoMod(t, worktree, "example.com/app", "example.com/model", "v0.1.0")

	decisions, err := (goAdapter{}).apply(context.Background(), worktree, depsCovGoTarget("v0.2.0"), Options{Timeout: time.Minute})
	if err == nil || !strings.Contains(err.Error(), "inspect selected version") || !strings.Contains(err.Error(), "GO_LIST_FAILED") {
		t.Fatalf("error = %v, want the go list failure surfaced", err)
	}
	if len(decisions) != 1 || decisions[0].Action != "failed" || !strings.Contains(decisions[0].Reason, "GO_LIST_FAILED") {
		t.Fatalf("decisions = %+v", decisions)
	}
	if !strings.Contains(depsCovShimLog(t, logPath), "args=list -m -f {{.Version}} example.com/model") {
		t.Fatalf("go list was not invoked as expected")
	}
}

func TestDepsCovAdaptersGoApplyRejectsInexactSelection(t *testing.T) {
	testenv.Isolate(t)
	depsCovInstallGoShim(t, depsCovGoShim{listOut: "v9.9.9"})
	worktree := t.TempDir()
	depsCovWriteGoMod(t, worktree, "example.com/app", "example.com/model", "v0.1.0")

	decisions, err := (goAdapter{}).apply(context.Background(), worktree, depsCovGoTarget("v0.2.0"), Options{Timeout: time.Minute})
	if err == nil || !strings.Contains(err.Error(), "produced v9.9.9 instead of exact target v0.2.0") {
		t.Fatalf("error = %v, want an inexact-selection rejection", err)
	}
	if len(decisions) != 1 || decisions[0].Action != "failed" || decisions[0].AfterVersion != "v9.9.9" || decisions[0].AfterRef != "v9.9.9" {
		t.Fatalf("decisions = %+v", decisions)
	}
}

func TestDepsCovAdaptersGoApplyUpdatesModuleWithOfficialTooling(t *testing.T) {
	testenv.Isolate(t)
	// The apply path must hand a poisoned workspace setting to mutating verbs
	// only as GOWORK=off: `go get` and `go mod tidy` resolve the published
	// module graph, while the read-only `go list` keeps the caller's workspace.
	t.Setenv("GOWORK", "/nonexistent/poisoned.work")
	logPath := depsCovInstallGoShim(t, depsCovGoShim{listOut: "v0.2.0"})
	worktree := t.TempDir()
	depsCovWriteGoMod(t, worktree, "example.com/app", "example.com/model", "v0.1.0")

	decisions, err := (goAdapter{}).apply(context.Background(), worktree, depsCovGoTarget("v0.2.0"), Options{Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 1 {
		t.Fatalf("decisions = %+v", decisions)
	}
	decision := decisions[0]
	if decision.Action != "updated" || decision.BeforeVersion != "v0.1.0" || decision.AfterVersion != "v0.2.0" || decision.AfterRef != "v0.2.0" {
		t.Fatalf("decision = %+v", decision)
	}
	if decision.Reason != "official Go tooling selected the exact target and tidied the module" {
		t.Fatalf("reason = %q", decision.Reason)
	}
	log := depsCovShimLog(t, logPath)
	for _, want := range []string{
		"args=get example.com/model@v0.2.0 gowork=off",
		"args=mod tidy gowork=off",
		"args=list -m -f {{.Version}} example.com/model gowork=/nonexistent/poisoned.work",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("shim log is missing %q:\n%s", want, log)
		}
	}
}

func TestDepsCovAdaptersGoApplyAllowsExplicitDowngrade(t *testing.T) {
	testenv.Isolate(t)
	depsCovInstallGoShim(t, depsCovGoShim{listOut: "v1.0.0"})
	worktree := t.TempDir()
	depsCovWriteGoMod(t, worktree, "example.com/app", "example.com/model", "v1.9.0")

	decisions, err := (goAdapter{}).apply(context.Background(), worktree, depsCovGoTarget("v1.0.0"), Options{Timeout: time.Minute, AllowDowngrade: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 1 || decisions[0].Action != "updated" {
		t.Fatalf("decisions = %+v", decisions)
	}
	if decisions[0].Reason != "official Go tooling applied the explicitly allowed downgrade and tidied the module" {
		t.Fatalf("reason = %q", decisions[0].Reason)
	}
	if decisions[0].BeforeVersion != "v1.9.0" || decisions[0].AfterVersion != "v1.0.0" {
		t.Fatalf("decision = %+v", decisions[0])
	}
}

func TestDepsCovAdaptersGoApplyLeavesExactRequirementUntouched(t *testing.T) {
	testenv.Isolate(t)
	logPath := depsCovInstallGoShim(t, depsCovGoShim{getExit: 1, getErr: "MUST_NOT_RUN"})
	worktree := t.TempDir()
	depsCovWriteGoMod(t, worktree, "example.com/app", "example.com/model", "v0.2.0")

	decisions, err := (goAdapter{}).apply(context.Background(), worktree, depsCovGoTarget("v0.2.0"), Options{Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 1 || decisions[0].Action != "unchanged" || decisions[0].AfterVersion != "v0.2.0" || decisions[0].AfterRef != "v0.2.0" {
		t.Fatalf("decisions = %+v", decisions)
	}
	if decisions[0].Reason != "requirement already declares the exact target version" {
		t.Fatalf("reason = %q", decisions[0].Reason)
	}
	depsCovShimNotRun(t, logPath)
}

func TestDepsCovAdaptersGoApplyBlocksDowngradeBeforeTooling(t *testing.T) {
	testenv.Isolate(t)
	logPath := depsCovInstallGoShim(t, depsCovGoShim{getExit: 1, getErr: "MUST_NOT_RUN"})
	worktree := t.TempDir()
	depsCovWriteGoMod(t, worktree, "example.com/app", "example.com/model", "v1.9.0")

	decisions, err := (goAdapter{}).apply(context.Background(), worktree, depsCovGoTarget("v1.0.0"), Options{Timeout: time.Minute})
	if err == nil || !strings.Contains(err.Error(), "lower than observed version") || !strings.Contains(err.Error(), "--allow-downgrade") {
		t.Fatalf("error = %v, want a blocked downgrade", err)
	}
	if len(decisions) != 1 || decisions[0].Action != "blocked_downgrade" || decisions[0].BeforeVersion != "v1.9.0" {
		t.Fatalf("decisions = %+v", decisions)
	}
	depsCovShimNotRun(t, logPath)
}

func TestDepsCovAdaptersGoApplyJoinsPerModuleFailures(t *testing.T) {
	testenv.Isolate(t)
	logPath := depsCovInstallGoShim(t, depsCovGoShim{getExit: 1, getErr: "GO_GET_FAILED"})
	worktree := t.TempDir()
	depsCovWriteGoMod(t, worktree, "example.com/app", "example.com/model", "v0.1.0")
	depsCovWriteGoMod(t, filepath.Join(worktree, "sub"), "example.com/sub", "example.com/model", "v0.1.0")
	depsCovWriteGoMod(t, filepath.Join(worktree, "unrelated"), "example.com/unrelated", "example.com/other", "v1.0.0")
	depsCovWriteGoMod(t, filepath.Join(worktree, "vendor"), "example.com/vendored", "example.com/model", "v0.1.0")

	decisions, err := (goAdapter{}).apply(context.Background(), worktree, depsCovGoTarget("v0.2.0"), Options{Timeout: time.Minute})
	if err == nil {
		t.Fatal("apply succeeded although every go get failed")
	}
	if got := strings.Count(err.Error(), "GO_GET_FAILED"); got != 2 {
		t.Fatalf("error joins %d failures, want 2:\n%v", got, err)
	}
	if len(decisions) != 2 {
		t.Fatalf("decisions = %+v, want one per failing module", decisions)
	}
	if decisions[0].File != "go.mod" || decisions[1].File != "sub/go.mod" {
		t.Fatalf("decisions are not sorted by file: %+v", decisions)
	}
	for _, decision := range decisions {
		if decision.Action != "failed" || decision.BeforeVersion != "v0.1.0" {
			t.Fatalf("decision = %+v", decision)
		}
	}
	log := depsCovShimLog(t, logPath)
	if strings.Contains(log, "vendor") || strings.Contains(log, "unrelated") {
		t.Fatalf("apply ran go in a module it must not touch:\n%s", log)
	}
}

func TestDepsCovAdaptersGoApplyReportsManifestScanErrors(t *testing.T) {
	testenv.Isolate(t)
	logPath := depsCovInstallGoShim(t, depsCovGoShim{getExit: 1, getErr: "MUST_NOT_RUN"})
	worktree := t.TempDir()
	writeTestFile(t, filepath.Join(worktree, "go.mod"), "this is not a module file\n")

	decisions, err := (goAdapter{}).apply(context.Background(), worktree, depsCovGoTarget("v0.2.0"), Options{Timeout: time.Minute})
	if err == nil || !strings.Contains(err.Error(), "parse go.mod") {
		t.Fatalf("error = %v, want the manifest parse failure surfaced", err)
	}
	if len(decisions) != 0 {
		t.Fatalf("decisions = %+v, want none", decisions)
	}
	depsCovShimNotRun(t, logPath)
}

func TestDepsCovAdaptersGoManifestsFindsAndSortsModules(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	depsCovWriteGoMod(t, root, "example.com/app", "example.com/model", "v1.0.0")
	depsCovWriteGoMod(t, filepath.Join(root, "nested"), "example.com/nested", "example.com/model", "v1.2.0")
	depsCovWriteGoMod(t, filepath.Join(root, "nested", "other"), "example.com/other", "example.com/other", "v1.0.0")
	depsCovWriteGoMod(t, filepath.Join(root, "vendor"), "example.com/vendored", "example.com/model", "v9.9.9")
	depsCovWriteGoMod(t, filepath.Join(root, "node_modules"), "example.com/modules", "example.com/model", "v9.9.9")
	depsCovWriteGoMod(t, filepath.Join(root, ".git"), "example.com/git", "example.com/model", "v9.9.9")
	writeTestFile(t, filepath.Join(root, "README.md"), "not a manifest\n")

	manifests, err := goManifests(root, "example.com/model")
	if err != nil {
		t.Fatal(err)
	}
	if len(manifests) != 2 {
		t.Fatalf("manifests = %+v, want only the two production modules", manifests)
	}
	if manifests[0].relative != "go.mod" || manifests[0].version != "v1.0.0" || manifests[0].dir != root {
		t.Fatalf("first manifest = %+v", manifests[0])
	}
	if manifests[1].relative != "nested/go.mod" || manifests[1].version != "v1.2.0" || manifests[1].dir != filepath.Join(root, "nested") {
		t.Fatalf("second manifest = %+v", manifests[1])
	}
}

func TestDepsCovAdaptersGoManifestsScansARootNamedVendor(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "vendor")
	depsCovWriteGoMod(t, root, "example.com/vendored", "example.com/model", "v1.0.0")

	manifests, err := goManifests(root, "example.com/model")
	if err != nil {
		t.Fatal(err)
	}
	// The skip list applies to directories discovered *below* the requested
	// root, not to the root the caller explicitly asked to scan.
	if len(manifests) != 1 || manifests[0].relative != "go.mod" || manifests[0].version != "v1.0.0" {
		t.Fatalf("manifests = %+v, want the requested root scanned", manifests)
	}
}

func TestDepsCovAdaptersGoManifestsReportsWalkAndManifestErrors(t *testing.T) {
	t.Parallel()
	t.Run("missing root", func(t *testing.T) {
		manifests, err := goManifests(filepath.Join(t.TempDir(), "absent"), "example.com/model")
		if err == nil {
			t.Fatal("goManifests succeeded against a nonexistent root")
		}
		if len(manifests) != 0 {
			t.Fatalf("manifests = %+v, want none", manifests)
		}
	})

	t.Run("unreadable go.mod", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Symlink(filepath.Join(root, "missing-target"), filepath.Join(root, "go.mod")); err != nil {
			t.Fatal(err)
		}
		if _, err := goManifests(root, "example.com/model"); err == nil {
			t.Fatal("goManifests succeeded although go.mod cannot be read")
		}
	})

	t.Run("unparsable go.mod", func(t *testing.T) {
		root := t.TempDir()
		writeTestFile(t, filepath.Join(root, "go.mod"), "this is not a module file\n")
		_, err := goManifests(root, "example.com/model")
		if err == nil || !strings.Contains(err.Error(), "parse go.mod") {
			t.Fatalf("error = %v, want a parse failure", err)
		}
	})
}

func TestDepsCovAdaptersRequiredGoVersionParsesRequirements(t *testing.T) {
	t.Parallel()
	version, found, err := requiredGoVersion("go.mod", []byte("module example.com/app\n\ngo 1.24\n\nrequire example.com/model v0.2.0\n"), "example.com/model")
	if err != nil || !found || version != "v0.2.0" {
		t.Fatalf("requiredGoVersion = (%q, %v, %v)", version, found, err)
	}
	version, found, err = requiredGoVersion("go.mod", []byte("module example.com/app\n\ngo 1.24\n\nrequire example.com/other v0.2.0\n"), "example.com/model")
	if err != nil || found || version != "" {
		t.Fatalf("absent requirement = (%q, %v, %v), want (\"\", false, nil)", version, found, err)
	}
	_, _, err = requiredGoVersion("go.mod", []byte("this is not a module file\n"), "example.com/model")
	if err == nil || !strings.Contains(err.Error(), "parse go.mod") {
		t.Fatalf("error = %v, want a parse failure naming go.mod", err)
	}
}

func TestDepsCovAdaptersValidatePublishableGoManifests(t *testing.T) {
	t.Parallel()
	t.Run("sorted local replacements", func(t *testing.T) {
		root := t.TempDir()
		writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/app\n\ngo 1.24\n\nreplace example.com/b => ../b\n")
		writeTestFile(t, filepath.Join(root, "nested", "go.mod"), "module example.com/nested\n\ngo 1.24\n\nreplace example.com/a => ../a\n")
		// A vendored manifest and a non-manifest file are walked but must not
		// contribute: the skip list applies below the root, and only go.mod
		// files are inspected.
		writeTestFile(t, filepath.Join(root, "vendor", "go.mod"), "module example.com/vendored\n\ngo 1.24\n\nreplace example.com/v => ../v\n")
		writeTestFile(t, filepath.Join(root, "README.md"), "not a manifest\n")
		err := validatePublishableGoManifests(root)
		want := "local Go module replacements cannot be committed or published: go.mod: example.com/b => ../b; nested/go.mod: example.com/a => ../a"
		if err == nil || err.Error() != want {
			t.Fatalf("error = %v, want %q", err, want)
		}
	})

	t.Run("versioned replacements are publishable", func(t *testing.T) {
		root := t.TempDir()
		writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/app\n\ngo 1.24\n\nreplace example.com/model => example.com/fork v0.2.1\n")
		if err := validatePublishableGoManifests(root); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("missing root", func(t *testing.T) {
		if err := validatePublishableGoManifests(filepath.Join(t.TempDir(), "absent")); err == nil {
			t.Fatal("validatePublishableGoManifests succeeded against a nonexistent root")
		}
	})

	t.Run("unreadable go.mod", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Symlink(filepath.Join(root, "missing-target"), filepath.Join(root, "go.mod")); err != nil {
			t.Fatal(err)
		}
		if err := validatePublishableGoManifests(root); err == nil {
			t.Fatal("validatePublishableGoManifests succeeded although go.mod cannot be read")
		}
	})

	t.Run("unparsable go.mod", func(t *testing.T) {
		root := t.TempDir()
		writeTestFile(t, filepath.Join(root, "go.mod"), "this is not a module file\n")
		err := validatePublishableGoManifests(root)
		if err == nil || !strings.Contains(err.Error(), "parse go.mod") {
			t.Fatalf("error = %v, want a parse failure", err)
		}
	})

	t.Run("root named vendor is still scanned", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "vendor")
		writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/app\n\ngo 1.24\n\nreplace example.com/model => ../model\n")
		err := validatePublishableGoManifests(root)
		if err == nil || !strings.Contains(err.Error(), "example.com/model => ../model") {
			t.Fatalf("error = %v, want the local replacement reported", err)
		}
	})
}

// ---------------------------------------------------------------------------
// github_actions.go
// ---------------------------------------------------------------------------

func TestDepsCovAdaptersResolveGitHubRefShortCircuitsAndDelegates(t *testing.T) {
	t.Parallel()
	upper := strings.ToUpper(strings.Repeat("ab", 20))
	calls := 0
	resolved, err := resolveGitHubRef(context.Background(), "acme/cicd", upper, Options{
		ResolveGitHubRef: func(context.Context, string, string) (string, error) {
			calls++
			return "delegate-result", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved != strings.ToLower(upper) {
		t.Fatalf("resolved = %q, want the lowercased full SHA %q", resolved, strings.ToLower(upper))
	}
	if calls != 0 {
		t.Fatalf("an exact 40-character SHA must not consult the injected resolver (calls = %d)", calls)
	}

	var gotDependency, gotVersion string
	resolved, err = resolveGitHubRef(context.Background(), "acme/cicd", "v1.10.5", Options{
		ResolveGitHubRef: func(_ context.Context, dependency, version string) (string, error) {
			calls++
			gotDependency, gotVersion = dependency, version
			return strings.Repeat("c", 40), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || resolved != strings.Repeat("c", 40) {
		t.Fatalf("delegated call = %d, resolved = %q", calls, resolved)
	}
	if gotDependency != "acme/cicd" || gotVersion != "v1.10.5" {
		t.Fatalf("resolver received (%q, %q)", gotDependency, gotVersion)
	}

	sentinel := errors.New("resolver exploded")
	_, err = resolveGitHubRef(context.Background(), "acme/cicd", "v1.10.5", Options{
		ResolveGitHubRef: func(context.Context, string, string) (string, error) { return "", sentinel },
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want the injected resolver error", err)
	}
}

func TestDepsCovAdaptersResolveGitHubRefQueriesTagsWithFakeGit(t *testing.T) {
	direct := strings.Repeat("1", 40)
	dereferenced := strings.Repeat("2", 40)
	for _, testCase := range []struct {
		name    string
		script  string
		want    string
		wantErr string
	}{
		{
			name:   "dereferenced tag wins",
			script: depsCovFakeGitScript(direct+"\trefs/tags/v1.2.3", dereferenced+"\trefs/tags/v1.2.3^{}"),
			want:   dereferenced,
		},
		{
			name:   "lightweight tag falls back to the direct ref",
			script: depsCovFakeGitScript(direct + "\trefs/tags/v1.2.3"),
			want:   direct,
		},
		{
			name:   "SHAs are lowercased",
			script: depsCovFakeGitScript(strings.ToUpper(direct)+"\trefs/tags/v1.2.3", strings.ToUpper(dereferenced)+"\trefs/tags/v1.2.3^{}"),
			want:   dereferenced,
		},
		{
			name:    "malformed output is ignored",
			script:  depsCovFakeGitScript("not-a-sha refs/tags/v1.2.3", "short refs/tags/v1.2.3^{}"),
			wantErr: "was not found",
		},
		{
			name:    "empty output is not found",
			script:  depsCovFakeGitScript(),
			wantErr: "was not found",
		},
		{
			name:    "ls-remote failure is wrapped",
			script:  "printf '%s\\n' 'GIT_LS_REMOTE_FAILED' >&2\nexit 1\n",
			wantErr: "GIT_LS_REMOTE_FAILED",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			logPath := depsCovInstallShim(t, "git", testCase.script)
			githubDir := t.TempDir()
			resolved, err := resolveGitHubRef(context.Background(), "acme/cicd", "v1.2.3", Options{GitHubDir: githubDir, Timeout: time.Minute})
			if testCase.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
					t.Fatalf("error = %v, want it to contain %q", err, testCase.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if resolved != testCase.want {
				t.Fatalf("resolved = %q, want %q", resolved, testCase.want)
			}
			log := depsCovShimLog(t, logPath)
			want := "args=ls-remote https://github.com/acme/cicd.git refs/tags/v1.2.3 refs/tags/v1.2.3^{}"
			if !strings.Contains(log, want) {
				t.Fatalf("git was not invoked as expected, missing %q:\n%s", want, log)
			}
		})
	}
}

func TestDepsCovAdaptersGitHubActionsInspectCollectsWorkflowDecisions(t *testing.T) {
	t.Parallel()
	oldSHA := strings.Repeat("1", 40)
	resolved := strings.Repeat("2", 40)
	dir := seedNpmInspectRepository(t, map[string]string{
		".github/workflows/ci.yml":       "jobs:\n  ci:\n    uses: acme/cicd/.github/workflows/go.yml@" + oldSHA + " # v1.0.0\n",
		".github/workflows/release.yaml": "jobs:\n  rel:\n    uses: acme/cicd@" + oldSHA + " # v1.0.0\n",
		".github/workflows/notes.md":     "uses: acme/cicd@" + oldSHA + " # v1.0.0\n",
		"README.md":                      "not a workflow\n",
	})
	target := Target{Ecosystem: EcosystemGitHubActions, Dependency: "acme/cicd", Version: "v1.1.0", Resolved: resolved}
	decisions, err := (githubActionsAdapter{}).inspect(context.Background(), dir, "HEAD", target, Options{Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 2 {
		t.Fatalf("decisions = %+v, want one per workflow file", decisions)
	}
	if decisions[0].File != ".github/workflows/ci.yml" || decisions[1].File != ".github/workflows/release.yaml" {
		t.Fatalf("decisions are not sorted by file: %+v", decisions)
	}
	for _, decision := range decisions {
		if decision.Action != "updated" || decision.BeforeVersion != "v1.0.0" || decision.AfterVersion != "v1.1.0" {
			t.Fatalf("decision = %+v", decision)
		}
		if decision.Ecosystem != EcosystemGitHubActions || decision.Selector != "uses:acme/cicd" || decision.ResolvedRef != resolved || decision.AfterRef != resolved {
			t.Fatalf("decision identity fields = %+v", decision)
		}
		if decision.Reason != "existing reference set to the requested exact version" {
			t.Fatalf("reason = %q", decision.Reason)
		}
	}
}

func TestDepsCovAdaptersGitHubActionsInspectPropagatesFailures(t *testing.T) {
	t.Parallel()
	t.Run("ls-tree fails", func(t *testing.T) {
		dir := seedNpmInspectRepository(t, map[string]string{".github/workflows/ci.yml": "jobs:\n"})
		if _, err := (githubActionsAdapter{}).inspect(context.Background(), dir, "no-such-base", Target{Ecosystem: EcosystemGitHubActions, Dependency: "acme/cicd", Version: "v1.1.0"}, Options{Timeout: time.Minute}); err == nil {
			t.Fatal("inspect succeeded against a nonexistent base ref")
		}
	})

	t.Run("git show fails", func(t *testing.T) {
		dir := seedNpmInspectRepository(t, map[string]string{"seed.txt": "seed\n"})
		commit := depsCovPhantomCommit(t, dir, ".github/workflows/ci.yml")
		if _, err := (githubActionsAdapter{}).inspect(context.Background(), dir, commit, Target{Ecosystem: EcosystemGitHubActions, Dependency: "acme/cicd", Version: "v1.1.0"}, Options{Timeout: time.Minute}); err == nil {
			t.Fatal("inspect succeeded although the listed workflow blob cannot be read")
		}
	})

	t.Run("downgrade is blocked", func(t *testing.T) {
		dir := seedNpmInspectRepository(t, map[string]string{
			".github/workflows/ci.yml": "jobs:\n  ci:\n    uses: acme/cicd@" + strings.Repeat("3", 40) + " # v2.0.0\n",
		})
		target := Target{Ecosystem: EcosystemGitHubActions, Dependency: "acme/cicd", Version: "v1.8.0", Resolved: strings.Repeat("4", 40)}
		_, err := (githubActionsAdapter{}).inspect(context.Background(), dir, "HEAD", target, Options{Timeout: time.Minute})
		if err == nil || !strings.Contains(err.Error(), "lower than observed version") {
			t.Fatalf("error = %v, want a blocked downgrade", err)
		}
	})
}

func TestDepsCovAdaptersGitHubActionsApplyWritesOnlyChangedWorkflows(t *testing.T) {
	t.Parallel()
	oldSHA := strings.Repeat("1", 40)
	resolved := strings.Repeat("2", 40)
	worktree := t.TempDir()
	ciPath := filepath.Join(worktree, ".github", "workflows", "ci.yml")
	pinnedPath := filepath.Join(worktree, ".github", "workflows", "pinned.yaml")
	otherPath := filepath.Join(worktree, ".github", "workflows", "other.yml")
	writeTestFile(t, ciPath, "jobs:\n  ci:\n    uses: acme/cicd/.github/workflows/go.yml@"+oldSHA+" # v1.0.0\n")
	writeTestFile(t, pinnedPath, "jobs:\n  rel:\n    uses: acme/cicd@"+resolved+" # v1.1.0\n")
	writeTestFile(t, otherPath, "jobs:\n  build:\n    runs-on: ubuntu-latest\n")
	if err := os.Chmod(ciPath, 0o640); err != nil {
		t.Fatal(err)
	}
	pinnedBefore, otherBefore := mustReadFile(t, pinnedPath), mustReadFile(t, otherPath)

	target := Target{Ecosystem: EcosystemGitHubActions, Dependency: "acme/cicd", Version: "v1.1.0", Resolved: resolved}
	decisions, err := (githubActionsAdapter{}).apply(context.Background(), worktree, target, Options{Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}

	updated := mustReadFile(t, ciPath)
	if !strings.Contains(updated, "acme/cicd/.github/workflows/go.yml@"+resolved+" # v1.1.0") {
		t.Fatalf("ci.yml was not repinned:\n%s", updated)
	}
	if strings.Contains(updated, oldSHA) {
		t.Fatalf("ci.yml still holds the old ref:\n%s", updated)
	}
	if mustReadFile(t, pinnedPath) != pinnedBefore {
		t.Fatal("an already-pinned workflow was rewritten")
	}
	if mustReadFile(t, otherPath) != otherBefore {
		t.Fatal("a workflow without the dependency was rewritten")
	}
	info, err := os.Stat(ciPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("workflow mode = %v, want 0640 preserved", info.Mode().Perm())
	}
	if len(decisions) != 2 {
		t.Fatalf("decisions = %+v, want one updated and one unchanged", decisions)
	}
	if decisions[0].Action != "updated" || decisions[1].Action != "unchanged" {
		t.Fatalf("decisions = %+v", decisions)
	}
	entries, err := os.ReadDir(filepath.Dir(ciPath))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".wb-deps-") {
			t.Fatalf("temporary file left behind: %s", entry.Name())
		}
	}
}

func TestDepsCovAdaptersGitHubActionsApplyReportsFailures(t *testing.T) {
	t.Parallel()
	t.Run("no workflows directory", func(t *testing.T) {
		worktree := t.TempDir()
		writeTestFile(t, filepath.Join(worktree, "README.md"), "no workflows here\n")
		decisions, err := (githubActionsAdapter{}).apply(context.Background(), worktree, Target{Ecosystem: EcosystemGitHubActions, Dependency: "acme/cicd", Version: "v1.1.0"}, Options{Timeout: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		if len(decisions) != 0 {
			t.Fatalf("decisions = %+v, want none", decisions)
		}
	})

	t.Run("github path is not a directory", func(t *testing.T) {
		worktree := t.TempDir()
		writeTestFile(t, filepath.Join(worktree, ".github"), "a regular file, not a directory\n")
		_, err := (githubActionsAdapter{}).apply(context.Background(), worktree, Target{Ecosystem: EcosystemGitHubActions, Dependency: "acme/cicd", Version: "v1.1.0"}, Options{Timeout: time.Minute})
		if err == nil {
			t.Fatal("apply succeeded although .github/workflows cannot be a directory")
		}
	})

	t.Run("unreadable workflow", func(t *testing.T) {
		worktree := t.TempDir()
		workflows := filepath.Join(worktree, ".github", "workflows")
		if err := os.MkdirAll(workflows, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(workflows, "missing-target"), filepath.Join(workflows, "broken.yml")); err != nil {
			t.Fatal(err)
		}
		_, err := (githubActionsAdapter{}).apply(context.Background(), worktree, Target{Ecosystem: EcosystemGitHubActions, Dependency: "acme/cicd", Version: "v1.1.0"}, Options{Timeout: time.Minute})
		if err == nil || !strings.Contains(err.Error(), "broken.yml") {
			t.Fatalf("error = %v, want the unreadable workflow named", err)
		}
	})

	t.Run("downgrade is blocked before writing", func(t *testing.T) {
		worktree := t.TempDir()
		ciPath := filepath.Join(worktree, ".github", "workflows", "ci.yml")
		body := "jobs:\n  ci:\n    uses: acme/cicd@" + strings.Repeat("3", 40) + " # v2.0.0\n"
		writeTestFile(t, ciPath, body)
		target := Target{Ecosystem: EcosystemGitHubActions, Dependency: "acme/cicd", Version: "v1.0.0", Resolved: strings.Repeat("4", 40)}
		_, err := (githubActionsAdapter{}).apply(context.Background(), worktree, target, Options{Timeout: time.Minute})
		if err == nil || !strings.Contains(err.Error(), "lower than observed version") {
			t.Fatalf("error = %v, want a blocked downgrade", err)
		}
		if mustReadFile(t, ciPath) != body {
			t.Fatal("a blocked downgrade rewrote the workflow")
		}
	})
}

func TestDepsCovAdaptersRewriteGitHubActionsAllowsExplicitDowngrade(t *testing.T) {
	t.Parallel()
	resolved := strings.Repeat("2", 40)
	target := Target{Ecosystem: EcosystemGitHubActions, Dependency: "acme/cicd", Version: "v1.0.0", Resolved: resolved}
	contents := []byte("uses: acme/cicd@" + strings.Repeat("3", 40) + " # v2.0.0\n")
	updated, decisions, err := rewriteGitHubActions(contents, ".github/workflows/ci.yml", target, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 1 || decisions[0].Action != "updated" {
		t.Fatalf("decisions = %+v", decisions)
	}
	if decisions[0].Reason != "explicitly allowed downgrade applied" {
		t.Fatalf("reason = %q", decisions[0].Reason)
	}
	if decisions[0].BeforeVersion != "v2.0.0" || decisions[0].AfterVersion != "v1.0.0" {
		t.Fatalf("decision = %+v", decisions[0])
	}
	if !strings.Contains(string(updated), "acme/cicd@"+resolved+" # v1.0.0") {
		t.Fatalf("workflow was not repinned:\n%s", updated)
	}
}

func TestDepsCovAdaptersWriteAtomicWritesAndCleansUp(t *testing.T) {
	t.Parallel()
	t.Run("writes contents and mode", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "workflow.yml")
		if err := writeAtomic(path, []byte("jobs:\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := mustReadFile(t, path); got != "jobs:\n" {
			t.Fatalf("contents = %q", got)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || entries[0].Name() != "workflow.yml" {
			t.Fatalf("directory entries = %v, want only the written file", entries)
		}
	})

	t.Run("missing directory is reported", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "absent", "workflow.yml")
		if err := writeAtomic(path, []byte("jobs:\n"), 0o644); err == nil {
			t.Fatal("writeAtomic succeeded although its directory does not exist")
		}
		if _, err := os.Stat(filepath.Join(dir, "absent")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("writeAtomic created the missing directory: %v", err)
		}
	})

	t.Run("failed rename removes the temporary file", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "target")
		writeTestFile(t, filepath.Join(target, "keep.txt"), "keep\n")
		if err := writeAtomic(target, []byte("jobs:\n"), 0o644); err == nil {
			t.Fatal("writeAtomic replaced a directory")
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || entries[0].Name() != "target" {
			t.Fatalf("temporary file was left behind: %v", entries)
		}
		if got := mustReadFile(t, filepath.Join(target, "keep.txt")); got != "keep\n" {
			t.Fatalf("existing content changed: %q", got)
		}
	})
}

// ---------------------------------------------------------------------------
// npm.go
// ---------------------------------------------------------------------------

func TestDepsCovAdaptersNpmInspectUsesCatalogSelectorAndSkipsIgnoredTrees(t *testing.T) {
	t.Parallel()
	dir := seedNpmInspectRepository(t, map[string]string{
		"package.json":              npmPackageJSONWithDependency("@sneat/app", "lodash", "^4.17.21"),
		"node_modules/package.json": npmPackageJSONWithDependency("@sneat/ignored", "react", "1.0.0"),
		"testdata/package.json":     npmPackageJSONWithDependency("@sneat/fixture", "react", "1.0.0"),
		"pnpm-workspace.yaml": `packages:
  - "packages/*"

overrides:
  "@sneat/other": "1.0.0"

catalogs:
  react17:
    react: "17.0.2"
`,
	})
	decisions, err := (npmAdapter{}).inspect(context.Background(), dir, "HEAD", depsCovNpmTarget("react", "18.3.0"), Options{Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 1 {
		t.Fatalf("decisions = %+v, want only the pnpm catalogs entry", decisions)
	}
	decision := decisions[0]
	if decision.File != "pnpm-workspace.yaml" || decision.Selector != "catalogs.react17.react" {
		t.Fatalf("decision = %+v, want the catalogs selector", decision)
	}
	if decision.Action != "planned" || decision.BeforeVersion != "17.0.2" || decision.AfterVersion != "18.3.0" {
		t.Fatalf("decision = %+v", decision)
	}
	if decision.Reason != "existing pnpm workspace override will be set to the exact target version" {
		t.Fatalf("reason = %q", decision.Reason)
	}
}

func TestDepsCovAdaptersNpmInspectPropagatesFailures(t *testing.T) {
	t.Parallel()
	t.Run("ls-tree fails", func(t *testing.T) {
		dir := seedNpmInspectRepository(t, map[string]string{"package.json": npmPackageJSONWithDependency("@sneat/app", "@sneat/core", "1.2.3")})
		if _, err := (npmAdapter{}).inspect(context.Background(), dir, "no-such-base", depsCovNpmTarget("@sneat/core", "1.3.0"), Options{Timeout: time.Minute}); err == nil {
			t.Fatal("inspect succeeded against a nonexistent base ref")
		}
	})

	t.Run("package.json cannot be read from the base", func(t *testing.T) {
		dir := seedNpmInspectRepository(t, map[string]string{"seed.txt": "seed\n"})
		commit := depsCovPhantomCommit(t, dir, "package.json")
		if _, err := (npmAdapter{}).inspect(context.Background(), dir, commit, depsCovNpmTarget("@sneat/core", "1.3.0"), Options{Timeout: time.Minute}); err == nil {
			t.Fatal("inspect succeeded although the listed package.json cannot be read")
		}
	})

	t.Run("pnpm-workspace.yaml cannot be read from the base", func(t *testing.T) {
		dir := seedNpmInspectRepository(t, map[string]string{"seed.txt": "seed\n"})
		commit := depsCovPhantomCommit(t, dir, "pnpm-workspace.yaml")
		if _, err := (npmAdapter{}).inspect(context.Background(), dir, commit, depsCovNpmTarget("@sneat/core", "1.3.0"), Options{Timeout: time.Minute}); err == nil {
			t.Fatal("inspect succeeded although the listed pnpm-workspace.yaml cannot be read")
		}
	})

	t.Run("workspace downgrade is blocked", func(t *testing.T) {
		dir := seedNpmInspectRepository(t, map[string]string{
			"package.json": npmPackageJSONWithDependency("@sneat/app", "lodash", "^4.17.21"),
			"pnpm-workspace.yaml": `overrides:
  "@sneat/other": "1.0.0"
  "@sneat/core": "2.0.0"
`,
		})
		decisions, err := (npmAdapter{}).inspect(context.Background(), dir, "HEAD", depsCovNpmTarget("@sneat/core", "1.9.0"), Options{Timeout: time.Minute})
		if err == nil || !strings.Contains(err.Error(), "lower than observed version") {
			t.Fatalf("error = %v, want a blocked downgrade", err)
		}
		if len(decisions) != 1 || decisions[0].Action != "blocked_downgrade" || decisions[0].Selector != "overrides.@sneat/core" {
			t.Fatalf("decisions = %+v", decisions)
		}
	})
}

func TestDepsCovAdaptersNpmApplyReportsMissingTree(t *testing.T) {
	t.Parallel()
	decisions, err := (npmAdapter{}).apply(context.Background(), filepath.Join(t.TempDir(), "absent"), depsCovNpmTarget("@sneat/core", "1.3.0"), Options{Timeout: time.Minute})
	if err == nil {
		t.Fatal("apply succeeded against a nonexistent worktree")
	}
	if len(decisions) != 0 {
		t.Fatalf("decisions = %+v, want none", decisions)
	}
}

func TestDepsCovAdaptersNpmApplyReportsUnreadableManifests(t *testing.T) {
	t.Parallel()
	t.Run("package.json", func(t *testing.T) {
		worktree := t.TempDir()
		if err := os.Symlink(filepath.Join(worktree, "missing-target"), filepath.Join(worktree, "package.json")); err != nil {
			t.Fatal(err)
		}
		if _, err := (npmAdapter{}).apply(context.Background(), worktree, depsCovNpmTarget("@sneat/core", "1.3.0"), Options{Timeout: time.Minute}); err == nil {
			t.Fatal("apply succeeded although package.json cannot be read")
		}
	})

	t.Run("pnpm-workspace.yaml", func(t *testing.T) {
		worktree := t.TempDir()
		writeTestFile(t, filepath.Join(worktree, "package.json"), npmPackageJSONWithDependency("@sneat/app", "lodash", "^4.17.21"))
		if err := os.Symlink(filepath.Join(worktree, "missing-target"), filepath.Join(worktree, "pnpm-workspace.yaml")); err != nil {
			t.Fatal(err)
		}
		if _, err := (npmAdapter{}).apply(context.Background(), worktree, depsCovNpmTarget("@sneat/core", "1.3.0"), Options{Timeout: time.Minute}); err == nil {
			t.Fatal("apply succeeded although pnpm-workspace.yaml cannot be read")
		}
	})
}

func TestDepsCovAdaptersNpmApplyBlocksWorkspaceDowngrade(t *testing.T) {
	t.Parallel()
	worktree := t.TempDir()
	writeTestFile(t, filepath.Join(worktree, "package.json"), npmPackageJSONWithDependency("@sneat/app", "lodash", "^4.17.21"))
	workspace := "overrides:\n  \"@sneat/other\": \"1.0.0\"\n  \"@sneat/core\": \"2.0.0\"\n"
	writeTestFile(t, filepath.Join(worktree, "pnpm-workspace.yaml"), workspace)

	decisions, err := (npmAdapter{}).apply(context.Background(), worktree, depsCovNpmTarget("@sneat/core", "1.9.0"), Options{Timeout: time.Minute})
	if err == nil || !strings.Contains(err.Error(), "lower than observed version") {
		t.Fatalf("error = %v, want a blocked downgrade", err)
	}
	if len(decisions) != 1 || decisions[0].Action != "blocked_downgrade" || decisions[0].Selector != "overrides.@sneat/core" {
		t.Fatalf("decisions = %+v", decisions)
	}
	if got := mustReadFile(t, filepath.Join(worktree, "pnpm-workspace.yaml")); got != workspace {
		t.Fatalf("a blocked downgrade rewrote pnpm-workspace.yaml:\n%s", got)
	}
}

func TestDepsCovAdaptersWorkspaceSelectorRendersSectionPaths(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name string
		ref  pnpmWorkspaceRef
		want string
	}{
		{name: "catalogs includes the catalog name", ref: pnpmWorkspaceRef{Section: "catalogs", CatalogName: "react17", Key: "react"}, want: "catalogs.react17.react"},
		{name: "overrides", ref: pnpmWorkspaceRef{Section: "overrides", Key: "@sneat/core"}, want: "overrides.@sneat/core"},
		{name: "catalog", ref: pnpmWorkspaceRef{Section: "catalog", Key: "react"}, want: "catalog.react"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := workspaceSelector(testCase.ref); got != testCase.want {
				t.Fatalf("workspaceSelector(%+v) = %q, want %q", testCase.ref, got, testCase.want)
			}
		})
	}
}

func TestDepsCovAdaptersNpmManifestFilesScansAndSkipsTrees(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, path := range []string{
		"package.json",
		"sub/package.json",
		"pnpm-workspace.yaml",
		"sub/pnpm-workspace.yaml",
		"node_modules/package.json",
		"node_modules/pnpm-workspace.yaml",
		"vendor/package.json",
		"dist/package.json",
		"testdata/package.json",
		".git/package.json",
		"README.md",
	} {
		writeTestFile(t, filepath.Join(root, path), "{}\n")
	}
	packages, workspaces, err := npmManifestFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	wantPackages := []string{"package.json", "sub/package.json"}
	if strings.Join(packages, ",") != strings.Join(wantPackages, ",") {
		t.Fatalf("package manifests = %v, want %v", packages, wantPackages)
	}
	wantWorkspaces := []string{"pnpm-workspace.yaml", "sub/pnpm-workspace.yaml"}
	if strings.Join(workspaces, ",") != strings.Join(wantWorkspaces, ",") {
		t.Fatalf("workspace manifests = %v, want %v", workspaces, wantWorkspaces)
	}

	if _, _, err := npmManifestFiles(filepath.Join(root, "absent")); err == nil {
		t.Fatal("npmManifestFiles succeeded against a nonexistent root")
	}
}

func TestDepsCovAdaptersNpmLockfileDirectoriesClassifiesLockfiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, path := range []string{
		"pnpm-lock.yaml",
		"sub/package-lock.json",
		"sub/yarn.lock",
		"node_modules/pnpm-lock.yaml",
		"vendor/yarn.lock",
		"README.md",
	} {
		writeTestFile(t, filepath.Join(root, path), "lockfile\n")
	}
	directories, err := npmLockfileDirectories(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(directories) != 2 {
		t.Fatalf("directories = %v, want only the root and sub scopes", directories)
	}
	if got := directories[""]; len(got) != 1 || got[0] != npmLockfilePnpm {
		t.Fatalf("root lockfiles = %v, want [pnpm-lock.yaml]", got)
	}
	if got := directories["sub"]; len(got) != 2 || got[0] != npmLockfileNpm || got[1] != npmLockfileYarn {
		t.Fatalf("sub lockfiles = %v, want [package-lock.json yarn.lock]", got)
	}

	if _, err := npmLockfileDirectories(filepath.Join(root, "absent")); err == nil {
		t.Fatal("npmLockfileDirectories succeeded against a nonexistent root")
	}
}

func TestDepsCovAdaptersRegenerateAffectedLockfilesHandlesEachKind(t *testing.T) {
	target := depsCovNpmTarget("@sneat/core", "1.3.0")

	t.Run("no changed files", func(t *testing.T) {
		worktree := t.TempDir()
		writeTestFile(t, filepath.Join(worktree, "pnpm-lock.yaml"), "lockfileVersion: '9.0'\n")
		decisions, err := regenerateAffectedLockfiles(context.Background(), worktree, map[string]bool{}, target, Options{Timeout: time.Minute})
		if err != nil || len(decisions) != 0 {
			t.Fatalf("decisions = %+v, err = %v; want no work at all", decisions, err)
		}
	})

	t.Run("missing worktree", func(t *testing.T) {
		_, err := regenerateAffectedLockfiles(context.Background(), filepath.Join(t.TempDir(), "absent"), map[string]bool{"package.json": true}, target, Options{Timeout: time.Minute})
		if err == nil {
			t.Fatal("regenerateAffectedLockfiles succeeded against a nonexistent worktree")
		}
	})

	t.Run("yarn lockfile is skipped with instructions", func(t *testing.T) {
		worktree := t.TempDir()
		writeTestFile(t, filepath.Join(worktree, "yarn.lock"), "# yarn lockfile v1\n")
		decisions, err := regenerateAffectedLockfiles(context.Background(), worktree, map[string]bool{"package.json": true}, target, Options{Timeout: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		if len(decisions) != 1 || decisions[0].Action != "lockfile_skipped" || decisions[0].File != "yarn.lock" {
			t.Fatalf("decisions = %+v", decisions)
		}
		if decisions[0].Reason != "yarn.lock is not regenerated automatically; run `yarn install` in yarn.lock before merging" {
			t.Fatalf("reason = %q", decisions[0].Reason)
		}
	})

	t.Run("npm lockfile is regenerated", func(t *testing.T) {
		logPath := depsCovInstallNpmShim(t, "")
		worktree := t.TempDir()
		writeTestFile(t, filepath.Join(worktree, "package-lock.json"), "{}\n")
		decisions, err := regenerateAffectedLockfiles(context.Background(), worktree, map[string]bool{"package.json": true}, target, Options{Timeout: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		if len(decisions) != 1 || decisions[0].Action != "lockfile_regenerated" || decisions[0].File != "package-lock.json" {
			t.Fatalf("decisions = %+v", decisions)
		}
		if decisions[0].Reason != "npm install --package-lock-only regenerated the lockfile" {
			t.Fatalf("reason = %q", decisions[0].Reason)
		}
		if !strings.Contains(depsCovShimLog(t, logPath), "args=install --package-lock-only") {
			t.Fatal("npm was not invoked with --package-lock-only")
		}
	})

	t.Run("npm regeneration failure is reported", func(t *testing.T) {
		depsCovInstallNpmShim(t, "NPM_REGENERATE_FAILED")
		worktree := t.TempDir()
		writeTestFile(t, filepath.Join(worktree, "package-lock.json"), "{}\n")
		decisions, err := regenerateAffectedLockfiles(context.Background(), worktree, map[string]bool{"package.json": true}, target, Options{Timeout: time.Minute})
		if err == nil || !strings.Contains(err.Error(), "regenerate package-lock.json") || !strings.Contains(err.Error(), "NPM_REGENERATE_FAILED") {
			t.Fatalf("error = %v, want the regeneration failure surfaced", err)
		}
		if len(decisions) != 1 || decisions[0].Action != "lockfile_regeneration_failed" || !strings.Contains(decisions[0].Reason, "NPM_REGENERATE_FAILED") {
			t.Fatalf("decisions = %+v", decisions)
		}
	})

	t.Run("changed file outside every lockfile scope", func(t *testing.T) {
		worktree := t.TempDir()
		writeTestFile(t, filepath.Join(worktree, "sub", "pnpm-lock.yaml"), "lockfileVersion: '9.0'\n")
		decisions, err := regenerateAffectedLockfiles(context.Background(), worktree, map[string]bool{"other/package.json": true}, target, Options{Timeout: time.Minute})
		if err != nil || len(decisions) != 0 {
			t.Fatalf("decisions = %+v, err = %v; want no lockfile regeneration", decisions, err)
		}
	})

	t.Run("each kind in one directory is handled", func(t *testing.T) {
		depsCovInstallNpmShim(t, "")
		worktree := t.TempDir()
		writeTestFile(t, filepath.Join(worktree, "package-lock.json"), "{}\n")
		writeTestFile(t, filepath.Join(worktree, "yarn.lock"), "# yarn lockfile v1\n")
		decisions, err := regenerateAffectedLockfiles(context.Background(), worktree, map[string]bool{"package.json": true}, target, Options{Timeout: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		if len(decisions) != 2 {
			t.Fatalf("decisions = %+v, want one per lockfile kind", decisions)
		}
		if decisions[0].File != "package-lock.json" || decisions[0].Action != "lockfile_regenerated" {
			t.Fatalf("first decision = %+v", decisions[0])
		}
		if decisions[1].File != "yarn.lock" || decisions[1].Action != "lockfile_skipped" {
			t.Fatalf("second decision = %+v", decisions[1])
		}
	})
}

// ---------------------------------------------------------------------------
// npm_range.go
// ---------------------------------------------------------------------------

func TestDepsCovAdaptersNpmRangeComparatorAdmitsDirectly(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		specifier string
		version   string
		evaluated bool
		admits    bool
		reason    string
	}{
		{specifier: "", version: "1.0.0", evaluated: true, admits: true},
		{specifier: "*", version: "1.0.0", evaluated: true, admits: true},
		{specifier: "x", version: "1.0.0", evaluated: true, admits: true},
		{specifier: "X", version: "1.0.0", evaluated: true, admits: true},
		{specifier: "latest", version: "1.0.0", evaluated: true, admits: true},
		{specifier: "1.2.3", version: "1.2.3", evaluated: true, admits: true},
		{specifier: "1.2.3", version: "1.2.4", evaluated: true, admits: false},
		{specifier: "=1.2.3", version: "1.2.4", evaluated: true, admits: false},
		{specifier: ">1.2.3", version: "1.2.4", evaluated: true, admits: true},
		{specifier: ">1.2.3", version: "1.2.3", evaluated: true, admits: false},
		{specifier: ">=1.2.3", version: "1.2.3", evaluated: true, admits: true},
		{specifier: "<2.0.0", version: "2.0.0", evaluated: true, admits: false},
		{specifier: "<=1.2.3", version: "1.2.3", evaluated: true, admits: true},
		{specifier: "^1.2.3", version: "1.9.0", evaluated: true, admits: true},
		{specifier: "^1.2.3", version: "2.0.0", evaluated: true, admits: false},
		{specifier: "~1.2.3", version: "1.2.9", evaluated: true, admits: true},
		{specifier: "~1.2.3", version: "1.3.0", evaluated: true, admits: false},
		{specifier: "1.x", version: "1.5.0", reason: "wildcard specifier"},
		{specifier: "1.X", version: "1.5.0", reason: "wildcard specifier"},
		{specifier: "1.*", version: "1.5.0", reason: "wildcard specifier"},
		{specifier: "abc", version: "1.0.0", reason: "does not name an exact semantic version"},
		{specifier: "^1.0.0-beta.1", version: "1.0.0", reason: "pins a prerelease"},
	} {
		t.Run(testCase.specifier+"/"+testCase.version, func(t *testing.T) {
			t.Parallel()
			verdict := npmRangeComparatorAdmits(testCase.specifier, testCase.version)
			if verdict.Evaluated != testCase.evaluated || verdict.Admits != testCase.admits {
				t.Fatalf("npmRangeComparatorAdmits(%q, %q) = %+v, want evaluated=%v admits=%v",
					testCase.specifier, testCase.version, verdict, testCase.evaluated, testCase.admits)
			}
			if testCase.reason == "" {
				if verdict.Reason != "" {
					t.Fatalf("npmRangeComparatorAdmits(%q, %q) reason = %q, want none", testCase.specifier, testCase.version, verdict.Reason)
				}
				return
			}
			if !strings.Contains(verdict.Reason, testCase.reason) {
				t.Fatalf("npmRangeComparatorAdmits(%q, %q) reason = %q, want it to contain %q",
					testCase.specifier, testCase.version, verdict.Reason, testCase.reason)
			}
		})
	}
}

func TestDepsCovAdaptersNpmRangeConjunctionEdgeCases(t *testing.T) {
	t.Parallel()
	admitted := npmRangeConjunctionAdmits(">=22.0.0 <23.0.0", "22.1.4")
	if !admitted.Evaluated || !admitted.Admits {
		t.Fatalf("admitted verdict = %+v", admitted)
	}
	rejected := npmRangeConjunctionAdmits(">=22.0.0 <23.0.0", "24.0.0")
	if !rejected.Evaluated || rejected.Admits {
		t.Fatalf("rejected verdict = %+v", rejected)
	}
	// A readable comparator that rejects settles the conjunction even though a
	// sibling is unreadable.
	settled := npmRangeConjunctionAdmits(">=22.0.0 <23.0.0-weird.x", "21.0.0")
	if !settled.Evaluated || settled.Admits {
		t.Fatalf("settled verdict = %+v", settled)
	}
	// Two unreadable comparators keep the first reason; the joined result is
	// unevaluated rather than a guess.
	both := npmRangeConjunctionAdmits(">=22.0.0-weird.1 <23.0.0-weird.2", "99.0.0")
	if both.Evaluated || !strings.Contains(both.Reason, ">=22.0.0-weird.1") {
		t.Fatalf("both-unreadable verdict = %+v, want the first comparator's reason", both)
	}
	empty := npmRangeConjunctionAdmits("   ", "1.0.0")
	if empty.Evaluated || !strings.Contains(empty.Reason, "names no comparator") {
		t.Fatalf("empty verdict = %+v", empty)
	}
}

func TestDepsCovAdaptersNpmRangeUnionEdgeCases(t *testing.T) {
	t.Parallel()
	// Blank branches are skipped rather than treated as a comparator.
	leading := npmRangeUnionAdmits(" || 1.0.0", "1.0.0")
	if !leading.Evaluated || !leading.Admits {
		t.Fatalf("leading-blank verdict = %+v", leading)
	}
	trailing := npmRangeUnionAdmits("1.0.0 || ", "1.0.0")
	if !trailing.Evaluated || !trailing.Admits {
		t.Fatalf("trailing-blank verdict = %+v", trailing)
	}
	allBlank := npmRangeUnionAdmits("||", "1.0.0")
	if !allBlank.Evaluated || allBlank.Admits {
		t.Fatalf("all-blank verdict = %+v", allBlank)
	}
	// One admitting branch settles the union; one rejecting branch does not.
	admitting := npmRangeUnionAdmits("^1.0.0 || 2.x", "1.5.0")
	if !admitting.Evaluated || !admitting.Admits {
		t.Fatalf("admitting verdict = %+v", admitting)
	}
	rejecting := npmRangeUnionAdmits("^1.0.0 || ^2.0.0", "3.0.0")
	if !rejecting.Evaluated || rejecting.Admits {
		t.Fatalf("rejecting verdict = %+v", rejecting)
	}
	// Two unreadable branches keep the first reason instead of overwriting it.
	unreadable := npmRangeUnionAdmits("2.x || 3.y", "1.0.0")
	if unreadable.Evaluated || !strings.Contains(unreadable.Reason, "2.x") || !strings.Contains(unreadable.Reason, "does not evaluate") {
		t.Fatalf("unreadable verdict = %+v", unreadable)
	}
}

func TestDepsCovAdaptersSplitNpmVersionParts(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		literal             string
		major, minor, patch int
	}{
		{literal: "1.2.3", major: 1, minor: 2, patch: 3},
		{literal: "v1.2.3", major: 1, minor: 2, patch: 3},
		{literal: "1.2.3-beta.1", major: 1, minor: 2, patch: 3},
		{literal: "1.2.3+build.7", major: 1, minor: 2, patch: 3},
		{literal: "not-a-version", major: 0, minor: 0, patch: 0},
		{literal: "", major: 0, minor: 0, patch: 0},
	} {
		t.Run(testCase.literal, func(t *testing.T) {
			t.Parallel()
			major, minor, patch := splitNpmVersionParts(testCase.literal)
			if major != testCase.major || minor != testCase.minor || patch != testCase.patch {
				t.Fatalf("splitNpmVersionParts(%q) = (%d, %d, %d), want (%d, %d, %d)",
					testCase.literal, major, minor, patch, testCase.major, testCase.minor, testCase.patch)
			}
		})
	}
}
