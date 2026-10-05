//go:build e2e

package hooks

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests run the real built-in hook script under /bin/sh against temporary
// repositories (real git, a fake `go` on PATH), so they are real-process e2e
// tests and run in the e2e CI job (go test -tags e2e -run ^TestE2E).
//
// goPreCommitFixture is a temporary repository with the built-in Go
// pre-commit profile active and no other policy.
func goPreCommitFixture(t *testing.T) string {
	t.Helper()
	repo := initRepo(t)
	isolateConfig(t)
	configDir := filepath.Join(repo, ".wb")
	mustMkdirAll(t, configDir)
	mustWrite(t, filepath.Join(configDir, "hooks.yaml"), "version: 1\nprofiles:\n  auto: true\nmetrics:\n  enabled: false\n")
	return repo
}

func writeGoModule(t *testing.T, dir, module string) {
	t.Helper()
	mustMkdirAll(t, dir)
	mustWrite(t, filepath.Join(dir, "go.mod"), "module "+module+"\n\ngo 1.26\n")
}

func writeGoFile(t *testing.T, path, body string) {
	t.Helper()
	mustMkdirAll(t, filepath.Dir(path))
	mustWrite(t, path, "package p\n\n"+body)
}

const (
	cleanGoBody = "func Clean() int { return 1 }\n"
	// gofmt-clean but rejected by go vet (printf verb/argument mismatch).
	vetFailingGoBody = "import \"fmt\"\n\nfunc Broken() string { return fmt.Sprintf(\"%d\", \"x\") }\n"
)

func runGoPreCommit(t *testing.T, repo string) (int, string) {
	t.Helper()
	result, stderr := runGoPreCommitResult(t, repo)
	return result.ExitCode, stderr
}

// runGoPreCommitResult is runGoPreCommit that also returns the RunResult, so a
// test can tell "the go/pre-commit block ran and skipped" from "the block never
// ran".
func runGoPreCommitResult(t *testing.T, repo string) (RunResult, string) {
	t.Helper()
	var stderr bytes.Buffer
	result, err := Run(RunOptions{RepoPath: repo, Hook: "pre-commit", Stdout: &bytes.Buffer{}, Stderr: &stderr})
	if err == nil && result.ExitCode != 0 {
		t.Fatalf("exit code %d without an error", result.ExitCode)
	}
	return result, stderr.String()
}

// requireGoPreCommitBlockRan fails unless the go/pre-commit block executed, so
// a "go was never called" assertion cannot hold merely because the profile was
// inactive.
func requireGoPreCommitBlockRan(t *testing.T, result RunResult) {
	t.Helper()
	for _, block := range result.Blocks {
		if block.ID == "go/pre-commit" {
			return
		}
	}
	t.Fatalf("the go/pre-commit block did not run: %+v", result.Blocks)
}

// installFakeGo puts a `go` on PATH that appends "<cwd>|<args>" to the
// returned log instead of compiling anything, so a test can pin exactly what
// the hook ran and from which directory.
func installFakeGo(t *testing.T) string {
	t.Helper()
	return installFakeGoScript(t, "")
}

// installFakeGoScript is installFakeGo with extra shell run before the call is
// logged, for a `go` wrapper that behaves differently (for example one that
// reads standard input).
func installFakeGoScript(t *testing.T, prelude string) string {
	t.Helper()
	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "go-calls.log")
	script := "#!/bin/sh\n" + prelude + "printf '%s|%s\\n' \"$(pwd -P)\" \"$*\" >> '" + logPath + "'\n"
	mustWriteExecutable(t, filepath.Join(binDir, "go"), script)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func readFakeGoLog(t *testing.T, logPath string) []string {
	t.Helper()
	content, err := os.ReadFile(logPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(content)), "\n")
}

// TestE2EBuiltInGoPreCommitVetsNestedModuleFromItsOwnDirectory pins WB-831: a
// nested module (own go.mod, no go.work) is not part of the root module, so
// vetting its packages from the repository root fails with "main module does
// not contain package". Each staged package is vetted from its own module.
//
//nolint:paralleltest // fixture sets XDG_*, WB home and PATH with t.Setenv (isolateConfig, installFakeGo), which forbids t.Parallel
func TestE2EBuiltInGoPreCommitVetsNestedModuleFromItsOwnDirectory(t *testing.T) {
	repo := goPreCommitFixture(t)
	writeGoModule(t, repo, "example.invalid/root")
	writeGoModule(t, filepath.Join(repo, "end2end"), "example.invalid/root/end2end")
	writeGoFile(t, filepath.Join(repo, "end2end", "sqlite", "a.go"), cleanGoBody)
	git(t, repo, "add", "end2end")

	if code, stderr := runGoPreCommit(t, repo); code != 0 {
		t.Fatalf("clean nested module refused with exit %d: %s", code, stderr)
	}
}

//nolint:paralleltest // fixture sets XDG_*, WB home and PATH with t.Setenv (isolateConfig, installFakeGo), which forbids t.Parallel
func TestE2EBuiltInGoPreCommitFailsNamingTheNestedModuleWhenItsVetFails(t *testing.T) {
	repo := goPreCommitFixture(t)
	writeGoModule(t, repo, "example.invalid/root")
	writeGoModule(t, filepath.Join(repo, "end2end"), "example.invalid/root/end2end")
	writeGoFile(t, filepath.Join(repo, "end2end", "a.go"), vetFailingGoBody)
	git(t, repo, "add", "end2end")

	code, stderr := runGoPreCommit(t, repo)
	if code == 0 {
		t.Fatal("vet failure in a nested module did not fail the commit")
	}
	if !strings.Contains(stderr, "WB hook: go vet failed on the packages in this commit, in module end2end.") {
		t.Fatalf("stderr does not name the nested module: %s", stderr)
	}
	if strings.Contains(stderr, "does not contain package") {
		t.Fatalf("nested package was vetted from the wrong module: %s", stderr)
	}
}

//nolint:paralleltest // fixture sets XDG_*, WB home and PATH with t.Setenv (isolateConfig, installFakeGo), which forbids t.Parallel
func TestE2EBuiltInGoPreCommitVetsEveryModuleTheCommitTouches(t *testing.T) {
	repo := goPreCommitFixture(t)
	writeGoModule(t, repo, "example.invalid/root")
	writeGoModule(t, filepath.Join(repo, "end2end"), "example.invalid/root/end2end")
	writeGoFile(t, filepath.Join(repo, "a.go"), cleanGoBody)
	writeGoFile(t, filepath.Join(repo, "end2end", "b.go"), vetFailingGoBody)
	git(t, repo, "add", "a.go", "end2end")

	code, stderr := runGoPreCommit(t, repo)
	if code == 0 || !strings.Contains(stderr, "in module end2end.") {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}

	// The root module's failure keeps its original, unqualified message and
	// does not stop the nested module from being vetted too.
	writeGoFile(t, filepath.Join(repo, "a.go"), vetFailingGoBody)
	git(t, repo, "add", "a.go")
	code, stderr = runGoPreCommit(t, repo)
	if code == 0 {
		t.Fatal("two failing modules passed")
	}
	if !strings.Contains(stderr, "WB hook: go vet failed on the packages in this commit.\n") ||
		!strings.Contains(stderr, "in module end2end.") {
		t.Fatalf("both modules should be reported: %s", stderr)
	}
}

//nolint:paralleltest // fixture sets XDG_*, WB home and PATH with t.Setenv (isolateConfig, installFakeGo), which forbids t.Parallel
func TestE2EBuiltInGoPreCommitRunsVetPerModuleFromThatModuleDirectory(t *testing.T) {
	repo := goPreCommitFixture(t)
	writeGoModule(t, repo, "example.invalid/root")
	writeGoModule(t, filepath.Join(repo, "tools", "gen"), "example.invalid/root/tools/gen")
	writeGoModule(t, filepath.Join(repo, "tools", "gen", "inner"), "example.invalid/root/tools/gen/inner")
	writeGoModule(t, filepath.Join(repo, "my mod"), "example.invalid/root/mymod")
	writeGoFile(t, filepath.Join(repo, "a.go"), cleanGoBody)
	writeGoFile(t, filepath.Join(repo, "svc", "b.go"), cleanGoBody)
	writeGoFile(t, filepath.Join(repo, "tools", "gen", "c.go"), cleanGoBody)
	writeGoFile(t, filepath.Join(repo, "tools", "gen", "inner", "d.go"), cleanGoBody)
	writeGoFile(t, filepath.Join(repo, "my mod", "sub dir", "e.go"), cleanGoBody)
	git(t, repo, "add", ".")
	logPath := installFakeGo(t)

	if code, stderr := runGoPreCommit(t, repo); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	want := []string{
		repo + "|vet . ./svc",
		filepath.Join(repo, "my mod") + "|vet ./sub dir",
		filepath.Join(repo, "tools", "gen") + "|vet .",
		filepath.Join(repo, "tools", "gen", "inner") + "|vet .",
	}
	got := readFakeGoLog(t, logPath)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("go invocations\n got: %q\nwant: %q", got, want)
	}
}

// TestE2EBuiltInGoPreCommitSingleModuleRunsTheSameVetAsBefore pins that a
// repository with one module still gets one `go vet` from the repository
// root over exactly the staged packages, as before WB-831.
//
//nolint:paralleltest // fixture sets XDG_*, WB home and PATH with t.Setenv (isolateConfig, installFakeGo), which forbids t.Parallel
func TestE2EBuiltInGoPreCommitVetsEveryModuleEvenWhenGoReadsStandardInput(t *testing.T) {
	repo := goPreCommitFixture(t)
	writeGoModule(t, repo, "example.invalid/root")
	writeGoModule(t, filepath.Join(repo, "a"), "example.invalid/root/a")
	writeGoModule(t, filepath.Join(repo, "b"), "example.invalid/root/b")
	writeGoFile(t, filepath.Join(repo, "r.go"), cleanGoBody)
	writeGoFile(t, filepath.Join(repo, "a", "a.go"), cleanGoBody)
	writeGoFile(t, filepath.Join(repo, "b", "b.go"), cleanGoBody)
	git(t, repo, "add", ".")
	// A wrapper that drains stdin must not swallow the list of modules the hook
	// is still going to vet.
	logPath := installFakeGoScript(t, "cat >/dev/null\n")

	if code, stderr := runGoPreCommit(t, repo); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	want := []string{
		repo + "|vet .",
		filepath.Join(repo, "a") + "|vet .",
		filepath.Join(repo, "b") + "|vet .",
	}
	got := readFakeGoLog(t, logPath)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("go invocations\n got: %q\nwant: %q", got, want)
	}
}

//nolint:paralleltest // fixture sets XDG_*, WB home and PATH with t.Setenv (isolateConfig, installFakeGo), which forbids t.Parallel
func TestE2EBuiltInGoPreCommitSingleModuleRunsTheSameVetAsBefore(t *testing.T) {
	repo := goPreCommitFixture(t)
	writeGoModule(t, repo, "example.invalid/root")
	writeGoFile(t, filepath.Join(repo, "a.go"), cleanGoBody)
	writeGoFile(t, filepath.Join(repo, "x", "y", "b.go"), cleanGoBody)
	writeGoFile(t, filepath.Join(repo, "x", "c.go"), cleanGoBody)
	git(t, repo, "add", ".")
	logPath := installFakeGo(t)

	if code, stderr := runGoPreCommit(t, repo); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	got := readFakeGoLog(t, logPath)
	want := []string{repo + "|vet . ./x ./x/y"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("go invocations\n got: %q\nwant: %q", got, want)
	}
}

//nolint:paralleltest // fixture sets XDG_*, WB home and PATH with t.Setenv (isolateConfig, installFakeGo), which forbids t.Parallel
func TestE2EBuiltInGoPreCommitSkipsVetForGoFilesUnderNoModule(t *testing.T) {
	repo := goPreCommitFixture(t)
	// A root go.mod activates the profile by detection, so force it on instead:
	// the repository has no go.mod anywhere and the block must still run and
	// find nothing to vet.
	mustWrite(t, filepath.Join(repo, ".wb", "hooks.yaml"), "version: 1\nprofiles:\n  include: [go]\nmetrics:\n  enabled: false\n")
	writeGoFile(t, filepath.Join(repo, "scripts", "a.go"), cleanGoBody)
	git(t, repo, "add", "scripts")
	logPath := installFakeGo(t)

	result, stderr := runGoPreCommitResult(t, repo)
	if result.ExitCode != 0 {
		t.Fatalf("exit %d: %s", result.ExitCode, stderr)
	}
	requireGoPreCommitBlockRan(t, result)
	if got := readFakeGoLog(t, logPath); len(got) != 0 {
		t.Fatalf("go ran for files under no go.mod: %q", got)
	}
}

// profiles.include forces the go profile on in a repository with no root
// go.mod (the founder's fleet policy does), so the block must keep skipping vet
// there exactly as it did before WB-831: nested modules of such a repository
// are not vetted. Widening that is a separate decision.
//
//nolint:paralleltest // fixture sets XDG_*, WB home and PATH with t.Setenv (isolateConfig, installFakeGo), which forbids t.Parallel
func TestE2EBuiltInGoPreCommitSkipsVetWhenRepositoryHasNoRootModule(t *testing.T) {
	repo := goPreCommitFixture(t)
	mustWrite(t, filepath.Join(repo, ".wb", "hooks.yaml"), "version: 1\nprofiles:\n  include: [go]\nmetrics:\n  enabled: false\n")
	writeGoModule(t, filepath.Join(repo, "svc"), "example.invalid/svc")
	writeGoFile(t, filepath.Join(repo, "svc", "a.go"), cleanGoBody)
	writeGoFile(t, filepath.Join(repo, "loose.go"), cleanGoBody)
	git(t, repo, "add", ".")
	logPath := installFakeGo(t)

	result, stderr := runGoPreCommitResult(t, repo)
	if result.ExitCode != 0 {
		t.Fatalf("exit %d: %s", result.ExitCode, stderr)
	}
	requireGoPreCommitBlockRan(t, result)
	if got := readFakeGoLog(t, logPath); len(got) != 0 {
		t.Fatalf("go ran in a repository with no root go.mod: %q", got)
	}
}

//nolint:paralleltest // fixture sets XDG_*, WB home and PATH with t.Setenv (isolateConfig, installFakeGo), which forbids t.Parallel
func TestE2EBuiltInGoPreCommitSkipsPackagesWhoseDirectoryIsGone(t *testing.T) {
	repo := goPreCommitFixture(t)
	writeGoModule(t, repo, "example.invalid/root")
	writeGoFile(t, filepath.Join(repo, "svc", "a.go"), cleanGoBody)
	git(t, repo, "add", ".")
	if err := os.RemoveAll(filepath.Join(repo, "svc")); err != nil {
		t.Fatal(err)
	}
	logPath := installFakeGo(t)

	result, stderr := runGoPreCommitResult(t, repo)
	if result.ExitCode != 0 {
		t.Fatalf("exit %d: %s", result.ExitCode, stderr)
	}
	requireGoPreCommitBlockRan(t, result)
	if got := readFakeGoLog(t, logPath); len(got) != 0 {
		t.Fatalf("go ran for a package that no longer exists: %q", got)
	}
}
