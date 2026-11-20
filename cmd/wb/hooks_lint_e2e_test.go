//go:build e2e

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/hooks"
	"github.com/sneat-dev/wb/internal/testenv"
)

// errcheckLikeLinter stands in for the pinned golangci-lint: it reports an
// unchecked os.Remove the way errcheck does and exits 1, and is otherwise
// clean. A real linter build needs the network, which an e2e test must not.
const errcheckLikeLinter = `#!/bin/sh
printf '%s\n' "$*" >> "$(dirname "$0")/calls.log"
findings="$(grep -rn --include='*.go' '^[[:space:]]*os.Remove(' . || true)"
if [ -n "$findings" ]; then
    printf '%s\n' "$findings" | while IFS=: read -r file line _; do
        printf '%s:%s:2: Error return value of ` + "`os.Remove`" + ` is not checked (errcheck)\n' "${file#./}" "$line"
    done
    exit 1
fi
echo "0 issues."
`

// TestE2EPrePushLintBlocksAnErrcheckFindingInANestedModule is the 2026-11-20
// askstage incident end to end: a repository whose Go code lives only in a
// nested module (backend/go.mod, no root go.mod) pushed an unchecked error
// that no hook caught, and main went red on golangci's errcheck. With WB's
// managed hooks installed and the go profile included, the real `git push`
// now runs go vet and the pinned linter in that module and is refused.
//
//nolint:paralleltest // builds and runs the real wb binary against real Git hooks.
func TestE2EPrePushLintBlocksAnErrcheckFindingInANestedModule(t *testing.T) {
	binary := buildWB(t)
	root := t.TempDir()
	home := filepath.Join(root, "home")
	projects := filepath.Join(root, "projects")
	remote := filepath.Join(root, "remote.git")
	canonical := filepath.Join(projects, "acme", "app")
	cache := filepath.Join(root, "cache")
	env := append(wbUpgradeEnv(home), "XDG_CACHE_HOME="+cache)
	hookEnv := append(append([]string(nil), env...), "WB_EXECUTABLE="+binary)
	mustUpgradeMkdir(t, home)
	testenv.InitBareRemoteForTest(t, remote)
	mustUpgradeMkdir(t, filepath.Dir(canonical))
	upgradeGit(t, root, nil, "clone", remote, canonical)
	configureUpgradeGitUser(t, canonical)
	mustUpgradeWrite(t, filepath.Join(canonical, ".wb", "hooks.yaml"),
		"version: 1\nprofiles:\n  include: [go]\n  exclude: [worktree]\nmetrics:\n  enabled: false\n")
	mustUpgradeWrite(t, filepath.Join(canonical, "backend", "go.mod"), "module example.invalid/backend\n\ngo 1.21\n")
	mustUpgradeWrite(t, filepath.Join(canonical, "backend", "store", "store.go"),
		"package store\n\nimport \"os\"\n\nfunc Clean(path string) error { return os.Remove(path) }\n")
	upgradeGit(t, canonical, nil, "add", "-A")
	upgradeGit(t, canonical, nil, "commit", "-m", "initial")
	upgradeGit(t, canonical, nil, "push", "-u", "origin", "main")
	if installed := runWBUpgrade(t, binary, env, "--projects-root", projects, "hooks", "install", canonical); installed.exitCode != exitOK {
		t.Fatalf("hooks install: exit=%d stdout=%s stderr=%s", installed.exitCode, installed.stdout, installed.stderr)
	}

	upgradeGit(t, canonical, hookEnv, "checkout", "-b", "feature")
	mustUpgradeWrite(t, filepath.Join(canonical, "backend", "store", "reset.go"),
		"package store\n\nimport \"os\"\n\nfunc Reset(path string) {\n\tos.Remove(path)\n}\n")
	upgradeGit(t, canonical, hookEnv, "add", "-A")
	upgradeGit(t, canonical, hookEnv, "commit", "-m", "reset ignores its error")

	// The pinned linter is built only by a pre-push, so the commit above was
	// linted by go vet alone (an unchecked error is vet-clean). The pre-push
	// finds it built for the module's own toolchain.
	goVersion, err := exec.Command("go", "-C", filepath.Join(canonical, "backend"), "env", "GOVERSION").Output()
	if err != nil {
		t.Fatalf("go env GOVERSION: %v", err)
	}
	cacheRoot := cache
	if runtime.GOOS == "darwin" {
		cacheRoot = filepath.Join(home, "Library", "Caches")
	}
	linter := filepath.Join(cacheRoot, "wb", "golangci-lint", strings.TrimSpace(string(goVersion)), hooks.DefaultGolangciLintVersion, "golangci-lint")
	mustUpgradeMkdir(t, filepath.Dir(linter))
	if err := testenv.WriteExecutableFile(linter, []byte(errcheckLikeLinter), 0o755); err != nil {
		t.Fatal(err)
	}

	err = upgradeGitRun(t, canonical, hookEnv, "push", "origin", "feature")
	if err == nil {
		t.Fatal("a push with an errcheck finding in a nested module was accepted")
	}
	for _, want := range []string{
		"store/reset.go:6:2: Error return value of `os.Remove` is not checked (errcheck)",
		"golangci-lint v2.15.0 failed in module backend (exit 1)",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("push refusal lacks %q:\n%v", want, err)
		}
	}
	if output, _ := runUpgradeGit(remote, nil, "rev-parse", "--verify", "--quiet", "refs/heads/feature"); strings.TrimSpace(output) != "" {
		t.Fatalf("the refused branch reached the remote: %s", output)
	}

	mustUpgradeWrite(t, filepath.Join(canonical, "backend", "store", "reset.go"),
		"package store\n\nimport \"os\"\n\nfunc Reset(path string) error {\n\treturn os.Remove(path)\n}\n")
	upgradeGit(t, canonical, hookEnv, "commit", "-am", "reset returns its error")
	if err := upgradeGitRun(t, canonical, hookEnv, "push", "origin", "feature"); err != nil {
		t.Fatalf("the fixed push was refused: %v", err)
	}

	// With the linter built, the commit hook lints the staged package's
	// changed lines too, so the same mistake no longer reaches a commit.
	mustUpgradeWrite(t, filepath.Join(canonical, "backend", "store", "purge.go"),
		"package store\n\nimport \"os\"\n\nfunc Purge(path string) {\n\tos.Remove(path)\n}\n")
	upgradeGit(t, canonical, hookEnv, "add", "-A")
	err = upgradeGitRun(t, canonical, hookEnv, "commit", "-m", "purge ignores its error")
	if err == nil || !strings.Contains(err.Error(), "store/purge.go:6:2: Error return value of `os.Remove` is not checked (errcheck)") {
		t.Fatalf("the commit hook let an errcheck finding through: %v", err)
	}

	// Pre-push linted whole modules; pre-commit only the staged package's
	// changed lines.
	calls, err := os.ReadFile(filepath.Join(filepath.Dir(linter), "calls.log"))
	if err != nil {
		t.Fatal(err)
	}
	// `commit -am` lints its staged package too (wb#993).
	if want := "run ./...\nrun --new-from-rev=HEAD ./store\nrun ./...\nrun --new-from-rev=HEAD ./store\n"; string(calls) != want {
		t.Fatalf("linter invocations\n got %q\nwant %q", calls, want)
	}
}

// TestE2ECommitAllAndCommitPathsRunTheStagedChecks is wb#993 end to end.
// `git commit -a`, `git commit <path>` and `git commit --only <path>` stage
// the commit in a temporary index Git hands the hook in GIT_INDEX_FILE. The
// staged checks used to read the real index, find nothing staged and pass;
// each form must now be refused for a gofmt, a go vet and a lint finding.
//
//nolint:paralleltest // builds and runs the real wb binary against real Git hooks.
func TestE2ECommitAllAndCommitPathsRunTheStagedChecks(t *testing.T) {
	binary := buildWB(t)
	root := t.TempDir()
	home := filepath.Join(root, "home")
	projects := filepath.Join(root, "projects")
	remote := filepath.Join(root, "remote.git")
	canonical := filepath.Join(projects, "acme", "app")
	cache := filepath.Join(root, "cache")
	env := append(wbUpgradeEnv(home), "XDG_CACHE_HOME="+cache)
	hookEnv := append(append([]string(nil), env...), "WB_EXECUTABLE="+binary)
	mustUpgradeMkdir(t, home)
	testenv.InitBareRemoteForTest(t, remote)
	mustUpgradeMkdir(t, filepath.Dir(canonical))
	upgradeGit(t, root, nil, "clone", remote, canonical)
	configureUpgradeGitUser(t, canonical)
	mustUpgradeWrite(t, filepath.Join(canonical, ".wb", "hooks.yaml"),
		"version: 1\nprofiles:\n  include: [go]\n  exclude: [worktree]\nmetrics:\n  enabled: false\n")
	mustUpgradeWrite(t, filepath.Join(canonical, "go.mod"), "module example.invalid/app\n\ngo 1.21\n")
	clean := "package store\n\nfunc Clean() int { return 1 }\n"
	for _, name := range []string{"format", "vet", "lint"} {
		mustUpgradeWrite(t, filepath.Join(canonical, name, name+".go"), strings.Replace(clean, "store", name, 1))
	}
	upgradeGit(t, canonical, nil, "add", "-A")
	upgradeGit(t, canonical, nil, "commit", "-m", "initial")
	if installed := runWBUpgrade(t, binary, env, "--projects-root", projects, "hooks", "install", canonical); installed.exitCode != exitOK {
		t.Fatalf("hooks install: exit=%d stdout=%s stderr=%s", installed.exitCode, installed.stdout, installed.stderr)
	}
	goVersion, err := exec.Command("go", "-C", canonical, "env", "GOVERSION").Output()
	if err != nil {
		t.Fatalf("go env GOVERSION: %v", err)
	}
	cacheRoot := cache
	if runtime.GOOS == "darwin" {
		cacheRoot = filepath.Join(home, "Library", "Caches")
	}
	linter := filepath.Join(cacheRoot, "wb", "golangci-lint", strings.TrimSpace(string(goVersion)), hooks.DefaultGolangciLintVersion, "golangci-lint")
	mustUpgradeMkdir(t, filepath.Dir(linter))
	if err := testenv.WriteExecutableFile(linter, []byte(errcheckLikeLinter), 0o755); err != nil {
		t.Fatal(err)
	}

	headOf := func() string {
		out, err := runUpgradeGit(canonical, nil, "rev-parse", "HEAD")
		if err != nil {
			t.Fatalf("rev-parse HEAD: %v", err)
		}
		return strings.TrimSpace(out)
	}
	head := headOf()
	for _, tc := range []struct {
		name, file, body, want string
		commit                 []string
	}{
		{"commit -a with a gofmt violation", "format/format.go",
			"package format\n\nfunc Clean() int {return 2}\n", "Go files need gofmt", []string{"commit", "-am", "unformatted"}},
		{"commit <path> with a vet violation", "vet/vet.go",
			"package vet\n\nimport \"fmt\"\n\nfunc Clean() string { return fmt.Sprintf(\"%d\", \"x\") }\n",
			"WB hook: go vet failed on the packages in this commit.", []string{"commit", "-m", "vet", "vet/vet.go"}},
		{"commit --only <path> with a lint finding", "lint/lint.go",
			"package lint\n\nimport \"os\"\n\nfunc Clean(path string) {\n\tos.Remove(path)\n}\n",
			"lint/lint.go:6:2: Error return value of `os.Remove` is not checked (errcheck)", []string{"commit", "--only", "-m", "lint", "lint/lint.go"}},
	} {
		original, err := os.ReadFile(filepath.Join(canonical, tc.file))
		if err != nil {
			t.Fatal(err)
		}
		mustUpgradeWrite(t, filepath.Join(canonical, tc.file), tc.body)
		err = upgradeGitRun(t, canonical, hookEnv, tc.commit...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: commit was not refused with %q: %v", tc.name, tc.want, err)
		}
		if got := headOf(); got != head {
			t.Fatalf("%s: the refused commit landed as %s", tc.name, got)
		}
		mustUpgradeWrite(t, filepath.Join(canonical, tc.file), string(original))
	}

	// The checks read the commit's index, not the real one: a gofmt
	// violation staged in the real index does not block `commit --only` of a
	// clean path, and is not committed by it.
	mustUpgradeWrite(t, filepath.Join(canonical, "format", "format.go"), "package format\n\nfunc Clean() int {return 2}\n")
	upgradeGit(t, canonical, nil, "add", "format/format.go")
	mustUpgradeWrite(t, filepath.Join(canonical, "vet", "vet.go"), "package vet\n\nfunc Clean() int { return 4 }\n")
	upgradeGit(t, canonical, hookEnv, "commit", "--only", "-m", "vet only", "vet/vet.go")
	if out, err := runUpgradeGit(canonical, nil, "show", "--name-only", "--format=", "HEAD"); err != nil || strings.TrimSpace(out) != "vet/vet.go" {
		t.Fatalf("commit --only committed %q (%v), want only vet/vet.go", out, err)
	}
	upgradeGit(t, canonical, nil, "checkout", "HEAD", "--", "format/format.go")

	// The vet commit is linted as well; the gofmt one stops before lint. The
	// fixed commit -a passes, linted on its staged package.
	mustUpgradeWrite(t, filepath.Join(canonical, "lint", "lint.go"), "package lint\n\nfunc Clean() int { return 3 }\n")
	upgradeGit(t, canonical, hookEnv, "commit", "-am", "clean")
	calls, err := os.ReadFile(filepath.Join(filepath.Dir(linter), "calls.log"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "run --new-from-rev=HEAD ./vet\nrun --new-from-rev=HEAD ./lint\nrun --new-from-rev=HEAD ./vet\nrun --new-from-rev=HEAD ./lint\n"; string(calls) != want {
		t.Fatalf("linter invocations\n got %q\nwant %q", calls, want)
	}
}
