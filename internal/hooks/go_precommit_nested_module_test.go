package hooks

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	var stderr bytes.Buffer
	result, err := Run(RunOptions{RepoPath: repo, Hook: "pre-commit", Stdout: &bytes.Buffer{}, Stderr: &stderr})
	if err == nil && result.ExitCode != 0 {
		t.Fatalf("exit code %d without an error", result.ExitCode)
	}
	return result.ExitCode, stderr.String()
}

// installFakeGo puts a `go` on PATH that appends "<cwd>|<args>" to the
// returned log instead of compiling anything, so a test can pin exactly what
// the hook ran and from which directory.
func installFakeGo(t *testing.T) string {
	t.Helper()
	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "go-calls.log")
	script := "#!/bin/sh\nprintf '%s|%s\\n' \"$(pwd -P)\" \"$*\" >> '" + logPath + "'\n"
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

// TestBuiltInGoPreCommitVetsNestedModuleFromItsOwnDirectory pins WB-831: a
// nested module (own go.mod, no go.work) is not part of the root module, so
// vetting its packages from the repository root fails with "main module does
// not contain package". Each staged package is vetted from its own module.
func TestBuiltInGoPreCommitVetsNestedModuleFromItsOwnDirectory(t *testing.T) {
	repo := goPreCommitFixture(t)
	writeGoModule(t, repo, "example.invalid/root")
	writeGoModule(t, filepath.Join(repo, "end2end"), "example.invalid/root/end2end")
	writeGoFile(t, filepath.Join(repo, "end2end", "sqlite", "a.go"), cleanGoBody)
	git(t, repo, "add", "end2end")

	if code, stderr := runGoPreCommit(t, repo); code != 0 {
		t.Fatalf("clean nested module refused with exit %d: %s", code, stderr)
	}
}

func TestBuiltInGoPreCommitFailsNamingTheNestedModuleWhenItsVetFails(t *testing.T) {
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

func TestBuiltInGoPreCommitVetsEveryModuleTheCommitTouches(t *testing.T) {
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

func TestBuiltInGoPreCommitRunsVetPerModuleFromThatModuleDirectory(t *testing.T) {
	repo := goPreCommitFixture(t)
	writeGoModule(t, repo, "example.invalid/root")
	writeGoModule(t, filepath.Join(repo, "tools", "gen"), "example.invalid/root/tools/gen")
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
		filepath.Join(repo, "tools", "gen") + "|vet . ./inner",
	}
	got := readFakeGoLog(t, logPath)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("go invocations\n got: %q\nwant: %q", got, want)
	}
}

// TestBuiltInGoPreCommitSingleModuleRunsTheSameVetAsBefore pins that a
// repository with one module still gets one `go vet` from the repository
// root over exactly the staged packages, as before WB-831.
func TestBuiltInGoPreCommitSingleModuleRunsTheSameVetAsBefore(t *testing.T) {
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

func TestBuiltInGoPreCommitSkipsVetForGoFilesUnderNoModule(t *testing.T) {
	repo := goPreCommitFixture(t)
	writeGoFile(t, filepath.Join(repo, "scripts", "a.go"), cleanGoBody)
	git(t, repo, "add", "scripts")
	logPath := installFakeGo(t)

	if code, stderr := runGoPreCommit(t, repo); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if got := readFakeGoLog(t, logPath); len(got) != 0 {
		t.Fatalf("go ran for files under no go.mod: %q", got)
	}
}

// Auto-detection of the go profile needs a root go.mod, so a repository with
// only nested modules reaches this block through profiles.include, which
// forces the profile on.
func TestBuiltInGoPreCommitVetsNestedModuleOfRepositoryWithoutRootModule(t *testing.T) {
	repo := goPreCommitFixture(t)
	mustWrite(t, filepath.Join(repo, ".wb", "hooks.yaml"), "version: 1\nprofiles:\n  include: [go]\nmetrics:\n  enabled: false\n")
	writeGoModule(t, filepath.Join(repo, "svc"), "example.invalid/svc")
	writeGoFile(t, filepath.Join(repo, "svc", "a.go"), cleanGoBody)
	writeGoFile(t, filepath.Join(repo, "loose.go"), cleanGoBody)
	git(t, repo, "add", ".")
	logPath := installFakeGo(t)

	if code, stderr := runGoPreCommit(t, repo); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	got := readFakeGoLog(t, logPath)
	want := []string{filepath.Join(repo, "svc") + "|vet ."}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("go invocations\n got: %q\nwant: %q", got, want)
	}
}

func TestBuiltInGoPreCommitSkipsPackagesWhoseDirectoryIsGone(t *testing.T) {
	repo := goPreCommitFixture(t)
	writeGoModule(t, repo, "example.invalid/root")
	writeGoFile(t, filepath.Join(repo, "svc", "a.go"), cleanGoBody)
	git(t, repo, "add", ".")
	if err := os.RemoveAll(filepath.Join(repo, "svc")); err != nil {
		t.Fatal(err)
	}
	logPath := installFakeGo(t)

	if code, stderr := runGoPreCommit(t, repo); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if got := readFakeGoLog(t, logPath); len(got) != 0 {
		t.Fatalf("go ran for a package that no longer exists: %q", got)
	}
}
