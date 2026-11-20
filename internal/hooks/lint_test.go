package hooks

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
)

// lintHarness records every governed command `wb hooks lint` would run and
// answers the plain helpers it captures, so a test pins exactly what runs,
// from which directory, without building or running a real linter.
type lintHarness struct {
	t        *testing.T
	repo     string
	mu       sync.Mutex
	calls    []string
	cache    string
	stdout   bytes.Buffer
	stderr   bytes.Buffer
	exit     func(args []string) (int, error)
	output   func(args []string) string
	goEnv    func(dir string) (string, error)
	tier     Classification
	classErr error
	missing  map[string]bool
	terminal bool
}

func newLintHarness(t *testing.T, repo string) *lintHarness {
	t.Helper()
	return &lintHarness{
		t: t, repo: repo, cache: t.TempDir(),
		tier:    Classification{Tier: TierLint, Reason: "feature branch"},
		missing: map[string]bool{},
		goEnv:   func(string) (string, error) { return "go1.27.4\n", nil },
	}
}

func (h *lintHarness) options(hook, language string) LintOptions {
	return LintOptions{
		RepoRoot: h.repo, Hook: hook, Language: language,
		Stdin: strings.NewReader(""), Stdout: &h.stdout, Stderr: &h.stderr,
		Tools: LintTools{
			Capture: func(dir string, env []string, name string, args ...string) (string, error) {
				if name == "go" {
					return h.goEnv(dir)
				}
				return captureCommand(dir, env, name, args...)
			},
			Governed: func(dir string, env []string, stdout, stderr io.Writer, args ...string) (int, error) {
				rel, _ := filepath.Rel(h.repo, dir)
				h.mu.Lock()
				h.calls = append(h.calls, filepath.ToSlash(rel)+"|"+strings.Join(args, " "))
				h.mu.Unlock()
				if h.output != nil {
					_, _ = io.WriteString(stdout, h.output(args))
				}
				if h.exit != nil {
					return h.exit(args)
				}
				return 0, nil
			},
			Classify: func(io.Reader, string) (Classification, error) { return h.tier, h.classErr },
			LookPath: func(name string) (string, error) {
				if h.missing[name] {
					return "", errors.New("not found")
				}
				return "/usr/bin/" + name, nil
			},
			CacheRoot:  func() (string, error) { return h.cache, nil },
			IsTerminal: func(any) bool { return h.terminal },
		},
	}
}

func (h *lintHarness) run(hook, language string) int {
	return Lint(h.options(hook, language))
}

func (h *lintHarness) runWithStdin(hook, language, stdin string) int {
	options := h.options(hook, language)
	options.Stdin = strings.NewReader(stdin)
	return Lint(options)
}

// plantLinter puts an already-built pinned linter where golangciBinary looks.
func (h *lintHarness) plantLinter(goVersion, version string) string {
	h.t.Helper()
	path := filepath.Join(h.cache, "wb", "golangci-lint", goVersion, version, "golangci-lint")
	mustMkdirAll(h.t, filepath.Dir(path))
	mustWriteExecutable(h.t, path, "#!/bin/sh\nexit 0\n")
	return path
}

// lintRepo is a repository with a pushed main on a local bare origin, so
// "commits not on any remote" is exactly the commits a test adds after it.
func lintRepo(t *testing.T) string {
	t.Helper()
	repo := initRepo(t)
	bare := filepath.Join(t.TempDir(), "origin.git")
	testenv.InitBareRemoteForTest(t, bare)
	git(t, repo, "remote", "add", "origin", bare)
	git(t, repo, "push", "-q", "origin", "main")
	return repo
}

func commitFiles(t *testing.T, repo string, files map[string]string) string {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(repo, filepath.FromSlash(name))
		mustMkdirAll(t, filepath.Dir(path))
		mustWrite(t, path, content)
	}
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-q", "-m", "change")
	return git(t, repo, "rev-parse", "HEAD")
}

func pushLine(branch, sha string) string {
	return "refs/heads/" + branch + " " + sha + " refs/heads/" + branch + " " + zeroOID40 + "\n"
}

func TestLintRejectsAnUnsupportedHookAndLanguage(t *testing.T) {
	t.Parallel()
	h := newLintHarness(t, initRepo(t))
	if code := h.run("pre-commit", "node"); code != 2 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(h.stderr.String(), "unsupported lint target") {
		t.Fatalf("stderr %q", h.stderr.String())
	}
}

func TestLintRefusesToRunOutsideAGitCheckout(t *testing.T) {
	t.Parallel()
	h := newLintHarness(t, t.TempDir())
	if code := h.run("pre-push", "go"); code != 2 || !strings.Contains(h.stderr.String(), "inside a Git checkout") {
		t.Fatalf("exit %d, stderr %q", code, h.stderr.String())
	}
}

func TestLintDefaultsEveryProcessSeam(t *testing.T) {
	t.Parallel()
	options := withLintDefaults(LintOptions{})
	tools := options.Tools
	if options.Stdin == nil || options.Stdout == nil || options.Stderr == nil || tools.Capture == nil ||
		tools.Governed == nil || tools.Classify == nil || tools.LookPath == nil || tools.CacheRoot == nil || tools.IsTerminal == nil {
		t.Fatalf("a default is missing: %#v", options)
	}
	if firstNonEmpty("", "") != "" {
		t.Fatal("firstNonEmpty invented a value")
	}
}

func TestDefaultGovernedSeamRoutesThroughTheWBCommandGateway(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	executable := filepath.Join(dir, "wb")
	mustWriteExecutable(t, executable, "#!/bin/sh\nprintf '%s|%s|%s\\n' \"$(pwd -P)\" \"$*\" \"$EXTRA\"\nexit 3\n")
	var stdout, stderr bytes.Buffer
	options := withLintDefaults(LintOptions{WBExecutable: executable, ProjectsRoot: "/projects"})
	code, err := options.Tools.Governed(dir, []string{"EXTRA=yes"}, &stdout, &stderr, "go", "vet", "./...")
	resolved, _ := filepath.EvalSymlinks(dir)
	if err != nil || code != 3 || stdout.String() != resolved+"|--projects-root /projects run --quiet -- go vet ./...|yes\n" {
		t.Fatalf("code %d err %v stdout %q", code, err, stdout.String())
	}

	mustWriteExecutable(t, executable, "#!/bin/sh\nprintf '%s\\n' \"$*\"\n")
	stdout.Reset()
	code, err = governedCommand(executable, "", &stdout, &stderr, dir, nil, "true")
	if err != nil || code != 0 || stdout.String() != "run --quiet -- true\n" {
		t.Fatalf("code %d err %v stdout %q", code, err, stdout.String())
	}
	if _, err := governedCommand(filepath.Join(dir, "absent"), "", &stdout, &stderr, dir, nil, "true"); err == nil {
		t.Fatal("a missing wb executable started")
	}
}

func TestCaptureCommandReturnsStdoutAndExplainsFailure(t *testing.T) {
	t.Parallel()
	out, err := captureCommand(t.TempDir(), []string{"WB_LINT_PROBE=1"}, "/bin/sh", "-c", "printf '%s' \"$WB_LINT_PROBE\"")
	if err != nil || out != "1" {
		t.Fatalf("out %q err %v", out, err)
	}
	if _, err := captureCommand(t.TempDir(), nil, "/bin/sh", "-c", "echo boom >&2; exit 4"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err %v", err)
	}
}

func TestGoPrePushVetsAndLintsEveryModuleThePushTouches(t *testing.T) {
	t.Parallel()
	repo := lintRepo(t)
	head := commitFiles(t, repo, map[string]string{
		"backend/go.mod":            "module example.invalid/backend\n",
		"backend/facade/a.go":       "package facade\n",
		"emulatortest/go.mod":       "module example.invalid/emulatortest\n",
		"emulatortest/e2e/b.go":     "package e2e\n",
		"docs/readme.md":            "not go\n",
		"backend/testdata/x/go.mod": "module fixture\n",
		"backend/testdata/x/x.go":   "package x\n",
	})
	h := newLintHarness(t, repo)
	linter := h.plantLinter("go1.27.4", DefaultGolangciLintVersion)
	if code := h.runWithStdin("pre-push", "go", pushLine("feature", head)); code != 0 {
		t.Fatalf("exit %d: %s", code, h.stderr.String())
	}
	want := []string{
		"backend|go vet ./...", "backend|" + linter + " run ./...",
		"emulatortest|go vet ./...", "emulatortest|" + linter + " run ./...",
	}
	if !reflect.DeepEqual(h.calls, want) {
		t.Fatalf("calls\n got %q\nwant %q", h.calls, want)
	}
	if !strings.Contains(h.stdout.String(), "WB hook: tier 1 — feature branch") {
		t.Fatalf("stdout %q", h.stdout.String())
	}
}

func TestGoPrePushBlocksOnAnErrcheckFindingInANestedModule(t *testing.T) {
	t.Parallel()
	repo := lintRepo(t)
	head := commitFiles(t, repo, map[string]string{"backend/go.mod": "module b\n", "backend/a_test.go": "package b\n"})
	h := newLintHarness(t, repo)
	h.plantLinter("go1.27.4", DefaultGolangciLintVersion)
	h.exit = func(args []string) (int, error) {
		if strings.HasSuffix(args[0], "golangci-lint") {
			return 1, nil
		}
		return 0, nil
	}
	h.output = func(args []string) string {
		return "a_test.go:3:2: Error return value of `f.enqueue` is not checked (errcheck)\n"
	}
	if code := h.runWithStdin("pre-push", "go", pushLine("feature", head)); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(h.stderr.String(), "found issues in module backend") {
		t.Fatalf("stderr %q", h.stderr.String())
	}
}

func TestGoPrePushReportsAVetFailureAndKeepsLinting(t *testing.T) {
	t.Parallel()
	repo := lintRepo(t)
	head := commitFiles(t, repo, map[string]string{"go.mod": "module r\n", "a.go": "package r\n"})
	h := newLintHarness(t, repo)
	h.exit = func(args []string) (int, error) {
		if args[0] == "go" && args[1] == "vet" {
			return 1, nil
		}
		return 1, nil // building the linter fails: offline
	}
	if code := h.runWithStdin("pre-push", "go", pushLine("feature", head)); code != 1 {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{"go vet failed in the root module", "could not build golangci-lint"} {
		if !strings.Contains(h.stderr.String(), want) {
			t.Fatalf("stderr lacks %q: %s", want, h.stderr.String())
		}
	}
}

func TestGoPrePushSkipsWhenNothingNeedsChecking(t *testing.T) {
	t.Parallel()
	t.Run("tier 0", func(t *testing.T) {
		t.Parallel()
		h := newLintHarness(t, lintRepo(t))
		h.tier = Classification{Tier: TierSkip, Reason: "deletion"}
		if code := h.run("pre-push", "go"); code != 0 || len(h.calls) != 0 {
			t.Fatalf("exit %d calls %q", code, h.calls)
		}
	})
	t.Run("no Go module changed", func(t *testing.T) {
		t.Parallel()
		repo := lintRepo(t)
		head := commitFiles(t, repo, map[string]string{"go.mod": "module r\n", "README.md": "changed\n"})
		git(t, repo, "push", "-q", "origin", "main")
		next := commitFiles(t, repo, map[string]string{"README.md": "again\n"})
		h := newLintHarness(t, repo)
		h.classErr = errors.New("bad input")
		code := h.runWithStdin("pre-push", "go", "refs/heads/main "+next+" refs/heads/main "+head+"\n")
		if code != 0 || len(h.calls) != 0 || !strings.Contains(h.stderr.String(), "changes no Go module") {
			t.Fatalf("exit %d calls %q stderr %q", code, h.calls, h.stderr.String())
		}
		if !strings.Contains(h.stdout.String(), "classification failed (bad input)") {
			t.Fatalf("stdout %q", h.stdout.String())
		}
	})
	t.Run("go is not installed", func(t *testing.T) {
		t.Parallel()
		repo := lintRepo(t)
		commitFiles(t, repo, map[string]string{"go.mod": "module r\n"})
		h := newLintHarness(t, repo)
		h.terminal = true
		h.missing["go"] = true
		if code := h.run("pre-push", "go"); code != 0 || len(h.calls) != 0 || !strings.Contains(h.stderr.String(), "go is not on PATH") {
			t.Fatalf("exit %d calls %q", code, h.calls)
		}
	})
}

func TestPushedFilesFallsBackToEverythingWhenHistoryIsUnreadable(t *testing.T) {
	t.Parallel()
	repo := lintRepo(t)
	commitFiles(t, repo, map[string]string{"a/go.mod": "module a\n", "b/go.mod": "module b\n", ".hidden/go.mod": "module h\n"})
	h := newLintHarness(t, repo)
	runner := lintRunner{options: withLintDefaults(h.options("pre-push", "go"))}
	files, all := runner.pushedFiles([]RefUpdate{{LocalSHA: strings.Repeat("e", 40), RemoteSHA: zeroOID40}})
	if !all || files != nil {
		t.Fatalf("files %q all %v", files, all)
	}
	if got := goModulesFor(repo, nil, true); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("modules %q", got)
	}
}

func TestPushedFilesExcludesWhatTheRemoteAlreadyHas(t *testing.T) {
	t.Parallel()
	repo := lintRepo(t)
	first := commitFiles(t, repo, map[string]string{"one.go": "package one\n"})
	second := commitFiles(t, repo, map[string]string{"two.go": "package two\n", "three.go": "package three\n"})
	h := newLintHarness(t, repo)
	runner := lintRunner{options: withLintDefaults(h.options("pre-push", "go"))}
	files, all := runner.pushedFiles([]RefUpdate{
		{LocalSHA: second, RemoteSHA: first},
		{LocalSHA: second, RemoteSHA: strings.Repeat("d", 40)},
	})
	if all || !reflect.DeepEqual(files, []string{"one.go", "three.go", "two.go"}) {
		t.Fatalf("files %q all %v", files, all)
	}
	files, _ = runner.pushedFiles([]RefUpdate{{LocalSHA: second, RemoteSHA: first}})
	if !reflect.DeepEqual(files, []string{"three.go", "two.go"}) {
		t.Fatalf("files %q", files)
	}
}

func TestPushScopeDropsRefsThatCarryNoCheck(t *testing.T) {
	t.Parallel()
	h := newLintHarness(t, initRepo(t))
	runner := lintRunner{options: withLintDefaults(h.options("pre-push", "go"))}
	runner.options.Stdin = strings.NewReader(
		"(delete) " + zeroOID40 + " refs/heads/old " + strings.Repeat("a", 40) + "\n" +
			"refs/heads/t " + strings.Repeat("b", 40) + " refs/wb/checkpoints/t " + zeroOID40 + "\n" +
			"refs/heads/stream/x " + strings.Repeat("c", 40) + " refs/heads/stream/x " + zeroOID40 + "\n" +
			"refs/heads/f " + strings.Repeat("d", 40) + " refs/heads/f " + zeroOID40 + "\n")
	updates, skip := runner.pushScope()
	if skip || len(updates) != 1 || updates[0].LocalSHA != strings.Repeat("d", 40) {
		t.Fatalf("updates %#v skip %v", updates, skip)
	}
}

func TestPushBaseUsesTheRemoteTipOrTheDefaultBranchMergeBase(t *testing.T) {
	t.Parallel()
	repo := lintRepo(t)
	mainTip := git(t, repo, "rev-parse", "HEAD")
	head := commitFiles(t, repo, map[string]string{"a.ts": "x\n"})
	h := newLintHarness(t, repo)
	runner := lintRunner{options: withLintDefaults(h.options("pre-push", "node"))}
	runner.options.RepoRoot = repo
	if base, ok := runner.pushBase(RefUpdate{LocalSHA: head, RemoteSHA: mainTip}); !ok || base != mainTip {
		t.Fatalf("base %q ok %v", base, ok)
	}
	if base, ok := runner.pushBase(RefUpdate{LocalSHA: head, RemoteSHA: strings.Repeat("f", 40)}); !ok || base != mainTip {
		t.Fatalf("merge base %q ok %v", base, ok)
	}
	if _, ok := runner.pushBase(RefUpdate{LocalSHA: strings.Repeat("e", 40), RemoteSHA: zeroOID40}); ok {
		t.Fatal("an unknown tip has a merge base")
	}
	runner.options.RepoRoot = initRepo(t)
	if _, ok := runner.pushBase(RefUpdate{LocalSHA: "HEAD", RemoteSHA: zeroOID40}); ok {
		t.Fatal("a repository without a default branch has a base")
	}
}

func TestGoModuleDiscoveryFollowsTheGoCommandsOwnRules(t *testing.T) {
	t.Parallel()
	for rel, want := range map[string]bool{".": false, "backend": false, "a/testdata": true, "vendor/x": true, "x/.cache": true, "_tools": true, "web/node_modules/m": true} {
		if got := ignoredGoDir(rel); got != want {
			t.Errorf("ignoredGoDir(%q) = %v", rel, got)
		}
	}
	for file, want := range map[string]bool{"a.go": true, "x/go.mod": true, "go.sum": true, "x/.golangci.yml": true, ".golangci.yaml": true, ".golangci.toml": true, ".golangci.json": true, "README.md": false} {
		if got := isGoModuleInput(file); got != want {
			t.Errorf("isGoModuleInput(%q) = %v", file, got)
		}
	}
	repo := t.TempDir()
	for _, module := range []string{".", "svc", "svc/testdata/fixture"} {
		mustMkdirAll(t, filepath.Join(repo, module))
		mustWrite(t, filepath.Join(repo, module, "go.mod"), "module m\n")
	}
	got := goModulesFor(repo, []string{"svc/a.go", "svc/b.go", "root.go", "svc/testdata/fixture/f.go", "docs/x.md"}, false)
	if !reflect.DeepEqual(got, []string{".", "svc"}) {
		t.Fatalf("modules %q", got)
	}
	if got := goModulesFor(t.TempDir(), []string{"loose.go"}, false); len(got) != 0 {
		t.Fatalf("a file under no module mapped to %q", got)
	}
	if moduleLabel(".") != "the root module" || moduleLabel("svc") != "module svc" {
		t.Fatal("module labels")
	}
}

func TestAllMarkerDirsKeepsWalkingPastAnUnreadableDirectory(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission bits are not enforced here")
	}
	repo := t.TempDir()
	mustMkdirAll(t, filepath.Join(repo, "ok"))
	mustWrite(t, filepath.Join(repo, "ok", "go.mod"), "module ok\n")
	locked := filepath.Join(repo, "locked")
	mustMkdirAll(t, locked)
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	if got := allMarkerDirs(repo, "go.mod", ignoredGoDir); !reflect.DeepEqual(got, []string{"ok"}) {
		t.Fatalf("dirs %q", got)
	}
}

func TestRunGolangciTellsFindingsFromABrokenTool(t *testing.T) {
	t.Parallel()
	pin := golangciPin{Version: "v2.15.0", Source: "ci.yml"}
	for _, test := range []struct {
		name    string
		code    int
		err     error
		output  string
		blocks  bool
		message string
	}{
		{name: "issues found", code: 1, blocks: true, message: "found issues in module m"},
		{name: "export data from a newer toolchain", code: 1, output: "could not load export data: export data version 5", message: "cannot read this Go toolchain's export data"},
		{name: "clean", code: 0},
		{name: "no Go files", code: 5},
		{name: "crash", code: 3, message: "exited 3 in module m without reporting issues"},
		{name: "cannot start", err: errors.New("exec failed"), message: "could not start in module m"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			h := newLintHarness(t, t.TempDir())
			h.exit = func([]string) (int, error) { return test.code, test.err }
			h.output = func([]string) string { return test.output }
			runner := lintRunner{options: withLintDefaults(h.options("pre-push", "go"))}
			if got := runner.runGolangci("/bin/golangci-lint", pin, "m", h.repo, "run"); got != test.blocks {
				t.Fatalf("blocks = %v", got)
			}
			if test.message != "" && !strings.Contains(h.stderr.String(), test.message) {
				t.Fatalf("stderr %q", h.stderr.String())
			}
		})
	}
}

func TestGoPreCommitLintsOnlyChangedLinesOfTheStagedPackages(t *testing.T) {
	t.Parallel()
	repo := initRepo(t)
	for name, content := range map[string]string{
		"go.mod": "module r\n", "a.go": "package r\n", "svc/b.go": "package svc\n", "svc/c.go": "package svc\n",
		"backend/go.mod": "module b\n", "backend/x.go": "package b\n", "backend/api/y.go": "package api\n",
		"tools/testdata/go.mod": "module f\n", "tools/testdata/f.go": "package f\n",
		"gone/z.go": "package gone\n",
	} {
		mustMkdirAll(t, filepath.Dir(filepath.Join(repo, name)))
		mustWrite(t, filepath.Join(repo, name), content)
	}
	git(t, repo, "add", "-A")
	if err := os.RemoveAll(filepath.Join(repo, "gone")); err != nil {
		t.Fatal(err)
	}
	h := newLintHarness(t, repo)
	linter := h.plantLinter("go1.27.4", DefaultGolangciLintVersion)
	if code := h.run("pre-commit", "go"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.stderr.String())
	}
	want := []string{
		".|" + linter + " run --new-from-rev=HEAD . ./svc",
		"backend|" + linter + " run --new-from-rev=HEAD . ./api",
	}
	if !reflect.DeepEqual(h.calls, want) {
		t.Fatalf("calls\n got %q\nwant %q", h.calls, want)
	}

	h.exit = func([]string) (int, error) { return 1, nil }
	if code := h.run("pre-commit", "go"); code != 1 {
		t.Fatalf("findings exit %d", code)
	}
}

func TestGoPreCommitLintsWholePackagesBeforeTheFirstCommit(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(repo); err == nil {
		repo = resolved
	}
	git(t, repo, "init", "-q", "-b", "main")
	mustWrite(t, filepath.Join(repo, "go.mod"), "module r\n")
	mustWrite(t, filepath.Join(repo, "a.go"), "package r\n")
	git(t, repo, "add", "-A")
	h := newLintHarness(t, repo)
	linter := h.plantLinter("go1.27.4", DefaultGolangciLintVersion)
	if code := h.run("pre-commit", "go"); code != 0 || !reflect.DeepEqual(h.calls, []string{".|" + linter + " run ."}) {
		t.Fatalf("exit %d calls %q", code, h.calls)
	}
}

func TestGoPreCommitSkipsWithoutStagedGoOrAnUnbuiltLinter(t *testing.T) {
	t.Parallel()
	repo := initRepo(t)
	h := newLintHarness(t, repo)
	if code := h.run("pre-commit", "go"); code != 0 || len(h.calls) != 0 {
		t.Fatalf("nothing staged: exit %d calls %q", code, h.calls)
	}
	mustWrite(t, filepath.Join(repo, "go.mod"), "module r\n")
	mustWrite(t, filepath.Join(repo, "a.go"), "package r\n")
	git(t, repo, "add", "-A")
	if code := h.run("pre-commit", "go"); code != 0 || len(h.calls) != 0 || !strings.Contains(h.stderr.String(), "the next pre-push builds it") {
		t.Fatalf("unbuilt linter: exit %d calls %q stderr %q", code, h.calls, h.stderr.String())
	}

	broken := newLintHarness(t, repo)
	options := broken.options("pre-commit", "go")
	options.Tools.Capture = func(string, []string, string, ...string) (string, error) { return "", errors.New("git broke") }
	if code := Lint(options); code != 0 || !strings.Contains(broken.stderr.String(), "could not list the staged Go files") {
		t.Fatalf("git failure: exit %d stderr %q", code, broken.stderr.String())
	}
}

func TestResolveGolangciPinReadsTheRepositorysOwnCI(t *testing.T) {
	t.Parallel()
	noGit := func(string, []string, string, ...string) (string, error) { return "", errors.New("no clone") }
	repo := t.TempDir()
	workflows := filepath.Join(repo, ".github", "workflows")
	mustMkdirAll(t, workflows)
	if pin := resolveGolangciPin(repo, "", noGit); pin.Version != DefaultGolangciLintVersion || !strings.Contains(pin.Source, "WB default") {
		t.Fatalf("no workflows: %#v", pin)
	}
	mustMkdirAll(t, filepath.Join(workflows, "a-dir.yml"))
	mustWrite(t, filepath.Join(workflows, "b-broken.yml"), "jobs: [\n")
	mustWrite(t, filepath.Join(workflows, "c-latest.yml"), `jobs:
  lint:
    steps:
      - uses: actions/checkout@v6
      - uses: golangci/golangci-lint-action@v9
        with:
          version: latest
      - uses: golangci/golangci-lint-action@v9
        with:
          version: ${{ inputs.version }}
`)
	mustWrite(t, filepath.Join(workflows, "d-go.yaml"), `jobs:
  lint:
    steps:
      - uses: golangci/golangci-lint-action@v9.3.0
        with:
          version: 1b907273167eec8b3d84cae636b7798ced4433d5
`)
	if pin := resolveGolangciPin(repo, "", noGit); pin.Version != "1b907273167eec8b3d84cae636b7798ced4433d5" || pin.Source != ".github/workflows/d-go.yaml" {
		t.Fatalf("direct pin: %#v", pin)
	}
}

func TestResolveGolangciPinFollowsAReusableWorkflowIntoItsLocalClone(t *testing.T) {
	t.Parallel()
	projects := t.TempDir()
	cicd := filepath.Join(projects, "github.com", "strongo", "cicd")
	mustMkdirAll(t, filepath.Join(cicd, ".github", "workflows"))
	git(t, projects, "init", "-q", "-b", "main", cicd)
	git(t, cicd, "config", "user.name", "t")
	git(t, cicd, "config", "user.email", "t@example.invalid")
	mustWrite(t, filepath.Join(cicd, ".github", "workflows", "workflow.yml"), `jobs:
  nested:
    uses: other/repo/.github/workflows/x.yml@v1
  golangci:
    steps:
      - uses: golangci/golangci-lint-action@v9
        with:
          version: v2.15.0
`)
	git(t, cicd, "add", "-A")
	git(t, cicd, "commit", "-q", "-m", "pin")
	ref := git(t, cicd, "rev-parse", "HEAD")
	mustWrite(t, filepath.Join(cicd, ".github", "workflows", "workflow.yml"), "jobs: {}\n")

	repo := t.TempDir()
	mustMkdirAll(t, filepath.Join(repo, ".github", "workflows"))
	mustWrite(t, filepath.Join(repo, ".github", "workflows", "ci.yml"), fmt.Sprintf(`jobs:
  local:
    uses: ./.github/workflows/local.yml
  missing:
    uses: absent/repo/.github/workflows/workflow.yml@v1
  backend:
    uses: strongo/cicd/.github/workflows/workflow.yml@%s
`, ref))
	pin := resolveGolangciPin(repo, projects, captureCommand)
	if pin.Version != "v2.15.0" || pin.Source != "strongo/cicd/.github/workflows/workflow.yml@"+ref {
		t.Fatalf("reusable pin: %#v", pin)
	}
	if pin := resolveGolangciPin(repo, "", captureCommand); pin.Version != DefaultGolangciLintVersion {
		t.Fatalf("no projects root: %#v", pin)
	}
	mustWrite(t, filepath.Join(cicd, ".github", "workflows", "workflow.yml"), "jobs:\n  x:\n    steps: []\n")
	git(t, cicd, "add", "-A")
	git(t, cicd, "commit", "-q", "-m", "drop pin")
	empty := git(t, cicd, "rev-parse", "HEAD")
	mustWrite(t, filepath.Join(repo, ".github", "workflows", "ci.yml"), "jobs:\n  b:\n    uses: strongo/cicd/.github/workflows/workflow.yml@"+empty+"\n")
	if pin := resolveGolangciPin(repo, projects, captureCommand); pin.Version != DefaultGolangciLintVersion {
		t.Fatalf("reusable without a pin: %#v", pin)
	}
}

func TestGolangciPackageMatchesTheMajorVersion(t *testing.T) {
	t.Parallel()
	if golangciPackage("v1.64.8") != "github.com/golangci/golangci-lint/cmd/golangci-lint" ||
		golangciPackage("v2.15.0") != "github.com/golangci/golangci-lint/v2/cmd/golangci-lint" ||
		golangciPackage("1b907273") != "github.com/golangci/golangci-lint/v2/cmd/golangci-lint" {
		t.Fatal("wrong module path")
	}
}

func TestGolangciBinaryBuildsThePinOnceAndReusesIt(t *testing.T) {
	t.Parallel()
	h := newLintHarness(t, t.TempDir())
	pin := golangciPin{Version: "v2.15.0", Source: "ci.yml"}
	h.exit = func(args []string) (int, error) {
		if strings.Join(args[:2], " ") != "go install" || args[2] != "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0" {
			return 9, nil
		}
		return 0, nil
	}
	options := withLintDefaults(h.options("pre-push", "go"))
	governed := options.Tools.Governed
	options.Tools.Governed = func(dir string, env []string, stdout, stderr io.Writer, args ...string) (int, error) {
		for _, entry := range env {
			if gobin, ok := strings.CutPrefix(entry, "GOBIN="); ok {
				mustWriteExecutable(t, filepath.Join(gobin, "golangci-lint"), "#!/bin/sh\n")
			}
		}
		return governed(dir, env, stdout, stderr, args...)
	}
	runner := lintRunner{options: options}
	binary, ok := runner.golangciBinary(pin, h.repo, true)
	want := filepath.Join(h.cache, "wb", "golangci-lint", "go1.27.4", "v2.15.0", "golangci-lint")
	if !ok || binary != want {
		t.Fatalf("binary %q ok %v: %s", binary, ok, h.stderr.String())
	}
	if again, ok := runner.golangciBinary(pin, h.repo, true); !ok || again != want || len(h.calls) != 1 {
		t.Fatalf("rebuilt: %q %v %q", again, ok, h.calls)
	}
}

func TestGolangciBinaryExplainsEveryWayItCanBeUnavailable(t *testing.T) {
	t.Parallel()
	pin := golangciPin{Version: "v2.15.0", Source: "ci.yml"}
	check := func(t *testing.T, h *lintHarness, options LintOptions, want string) {
		t.Helper()
		runner := lintRunner{options: withLintDefaults(options)}
		if binary, ok := runner.golangciBinary(pin, h.repo, true); ok {
			t.Fatalf("unexpected linter %q", binary)
		}
		if !strings.Contains(h.stderr.String(), want) {
			t.Fatalf("stderr %q lacks %q", h.stderr.String(), want)
		}
	}
	t.Run("no toolchain version", func(t *testing.T) {
		t.Parallel()
		h := newLintHarness(t, t.TempDir())
		h.goEnv = func(string) (string, error) { return "", errors.New("no go") }
		check(t, h, h.options("pre-push", "go"), "could not read the Go toolchain version")
	})
	t.Run("no cache directory", func(t *testing.T) {
		t.Parallel()
		h := newLintHarness(t, t.TempDir())
		options := h.options("pre-push", "go")
		options.Tools.CacheRoot = func() (string, error) { return "", errors.New("no home") }
		check(t, h, options, "no user cache directory")
	})
	t.Run("cache is not a directory", func(t *testing.T) {
		t.Parallel()
		h := newLintHarness(t, t.TempDir())
		mustWrite(t, filepath.Join(h.cache, "wb"), "file\n")
		check(t, h, h.options("pre-push", "go"), "could not prepare")
	})
	t.Run("cache is read-only", func(t *testing.T) {
		t.Parallel()
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("permission bits are not enforced here")
		}
		h := newLintHarness(t, t.TempDir())
		parent := filepath.Join(h.cache, "wb", "golangci-lint", "go1.27.4")
		mustMkdirAll(t, parent)
		if err := os.Chmod(parent, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })
		check(t, h, h.options("pre-push", "go"), "could not prepare")
	})
	t.Run("build fails", func(t *testing.T) {
		t.Parallel()
		h := newLintHarness(t, t.TempDir())
		h.exit = func([]string) (int, error) { return 1, nil }
		check(t, h, h.options("pre-push", "go"), "could not build golangci-lint v2.15.0")
	})
	t.Run("install target is occupied", func(t *testing.T) {
		t.Parallel()
		h := newLintHarness(t, t.TempDir())
		target := filepath.Join(h.cache, "wb", "golangci-lint", "go1.27.4", "v2.15.0")
		mustMkdirAll(t, filepath.Dir(target))
		mustWrite(t, target, "not a directory\n")
		check(t, h, h.options("pre-push", "go"), "could not install the built golangci-lint")
	})
}

func TestGolangciBinaryUsesTheBuildAConcurrentHookFinishedFirst(t *testing.T) {
	t.Parallel()
	h := newLintHarness(t, t.TempDir())
	target := filepath.Join(h.cache, "wb", "golangci-lint", "go1.27.4", "v2.15.0")
	h.exit = func([]string) (int, error) {
		mustMkdirAll(t, target)
		mustWriteExecutable(t, filepath.Join(target, "golangci-lint"), "#!/bin/sh\n")
		return 0, nil
	}
	options := withLintDefaults(h.options("pre-push", "go"))
	governed := options.Tools.Governed
	options.Tools.Governed = func(dir string, env []string, stdout, stderr io.Writer, args ...string) (int, error) {
		for _, entry := range env {
			if gobin, ok := strings.CutPrefix(entry, "GOBIN="); ok {
				mustWriteExecutable(t, filepath.Join(gobin, "golangci-lint"), "#!/bin/sh\n")
			}
		}
		return governed(dir, env, stdout, stderr, args...)
	}
	runner := lintRunner{options: options}
	if binary, ok := runner.golangciBinary(golangciPin{Version: "v2.15.0"}, h.repo, true); !ok || binary != filepath.Join(target, "golangci-lint") {
		t.Fatalf("binary %q ok %v: %s", binary, ok, h.stderr.String())
	}
}
