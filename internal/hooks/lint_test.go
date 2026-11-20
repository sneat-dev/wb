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
	"syscall"
	"testing"
	"time"

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
			Governed: func(dir string, env []string, _ time.Duration, stdout, stderr io.Writer, args ...string) (int, error) {
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
	mustWriteExecutable(t, executable, "#!/bin/sh\nprintf '%s|%s|%s\\n' \"$(pwd -P)\" \"$*\" \"$EXTRA\"\necho 'error: go exited with status 3' >&2\nexit 3\n")
	var stdout, stderr bytes.Buffer
	options := withLintDefaults(LintOptions{WBExecutable: executable, ProjectsRoot: "/projects"})
	code, err := options.Tools.Governed(dir, []string{"EXTRA=yes"}, time.Minute, &stdout, &stderr, "go", "vet", "./...")
	resolved, _ := filepath.EvalSymlinks(dir)
	if err != nil || code != 3 || stdout.String() != resolved+"|--projects-root /projects run --quiet --timeout 1m0s -- go vet ./...|yes\n" {
		t.Fatalf("code %d err %v stdout %q", code, err, stdout.String())
	}

	mustWriteExecutable(t, executable, "#!/bin/sh\nprintf '%s\\n' \"$*\"\n")
	stdout.Reset()
	code, err = governedCommand(executable, "", time.Minute, &stdout, &stderr, dir, nil, "true")
	if err != nil || code != 0 || stdout.String() != "run --quiet --timeout 1m0s -- true\n" {
		t.Fatalf("code %d err %v stdout %q", code, err, stdout.String())
	}
	if _, err := governedCommand(filepath.Join(dir, "absent"), "", time.Minute, &stdout, &stderr, dir, nil, "true"); err == nil {
		t.Fatal("a missing wb executable started")
	}
	if _, err := governedCommand(executable, "", time.Minute, &stdout, &stderr, dir, nil); err == nil {
		t.Fatal("an empty command ran")
	}
}

func TestGovernedCommandOnlyCallsItATimeoutWhenTheDeadlinePassed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	executable := filepath.Join(dir, "wb")
	// A quick non-zero exit is reported as is, whoever printed it: a wb
	// refusal blocks like a finding does.
	mustWriteExecutable(t, executable, "#!/bin/sh\necho 'error: host is saturated' >&2\nexit 1\n")
	if code, err := governedCommand(executable, "", time.Minute, io.Discard, io.Discard, dir, nil, "golangci-lint", "run"); err != nil || code != 1 {
		t.Fatalf("quick exit: code %d err %v", code, err)
	}
	// wb run --timeout stopping the check exits 1 at the deadline.
	mustWriteExecutable(t, executable, "#!/bin/sh\nsleep 0.3\nexit 1\n")
	if _, err := governedCommand(executable, "", 100*time.Millisecond, io.Discard, io.Discard, dir, nil, "golangci-lint", "run"); !errors.Is(err, errCheckTimedOut) {
		t.Fatalf("deadline exit: %v", err)
	}
	if _, err := governedCommand(executable, "", 0, io.Discard, io.Discard, dir, nil, "golangci-lint"); err == nil || errors.Is(err, errCheckTimedOut) {
		t.Fatalf("zero deadline: %v", err)
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
		{name: "issues found", code: 1, output: "a.go:3:2: Error return value is not checked (errcheck)\n", blocks: true, message: "found issues in module m"},
		{name: "a column-less finding", code: 1, output: "a.go:3: File is not properly formatted (gofmt)\n", blocks: true, message: "found issues in module m"},
		{name: "a finding in a format WB cannot parse", code: 1, output: "::error file=a.go,line=3::unused (unused)\r\n", blocks: true, message: "found issues in module m"},
		{name: "no output at all", code: 1, blocks: true, message: "found issues in module m"},
		{name: "export data from a newer toolchain", code: 1,
			output:  "cache.go:7:2: could not import io (could not load export data: export data version 5 is greater than maximum supported version 4) (typecheck)\n1 issues:\n",
			message: "cannot read this Go toolchain's export data"},
		{name: "export data with CRLF and colour", code: 1,
			output:  "\x1b[1mcache.go:7:2\x1b[0m: could not import io (could not load export data: x) (\x1b[34mtypecheck\x1b[0m)\r\n",
			message: "cannot read this Go toolchain's export data"},
		{name: "export data plus a real finding", code: 1,
			output: "cache.go:7:2: could not import io (could not load export data: x) (typecheck)\na.go:3:2: Error return value is not checked (errcheck)\n",
			blocks: true, message: "found issues in module m"},
		{name: "export data in a format WB does not parse", code: 1,
			output: "cache.go:7\ttypecheck\tcould not import io (could not load export data: x)\n", blocks: true},
		{name: "export data reported by another linter", code: 1,
			output: "typecheck.go:7:2: could not load export data quoted (revive)\n", blocks: true},
		// A finding whose printed source line merely quotes the phrase is
		// still a finding: only an issue line counts.
		{name: "a finding on a line quoting the export-data message", code: 1,
			output: "lint.go:414:2: Error return value is not checked (errcheck)\n\tif strings.Contains(out, \"could not load export data\") { // typecheck\n",
			blocks: true, message: "found issues in module m"},
		{name: "clean", code: 0},
		{name: "any other exit", code: 3, blocks: true, message: "failed in module m (exit 3)"},
		{name: "the hook's own deadline", err: fmt.Errorf("golangci-lint %w (2m0s)", errCheckTimedOut), message: "did not finish in module m"},
		{name: "wb could not run it", err: errors.New("exec: wb not found"), blocks: true, message: "This is a WB problem, not a lint result"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			h := newLintHarness(t, t.TempDir())
			h.exit = func([]string) (int, error) { return test.code, test.err }
			h.output = func([]string) string { return test.output }
			runner := lintRunner{options: withLintDefaults(h.options("pre-push", "go"))}
			if got := runner.runGolangci("/bin/golangci-lint", pin, "m", h.repo, time.Minute, "run"); got != test.blocks {
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
	h.output = func([]string) string { return "svc/b.go:1:1: unused variable (unused)\n" }
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
	options.Tools.Governed = func(dir string, env []string, timeout time.Duration, stdout, stderr io.Writer, args ...string) (int, error) {
		for _, entry := range env {
			if gobin, ok := strings.CutPrefix(entry, "GOBIN="); ok {
				mustWriteExecutable(t, filepath.Join(gobin, "golangci-lint"), "#!/bin/sh\n")
			}
		}
		return governed(dir, env, timeout, stdout, stderr, args...)
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

// buildingOptions makes the harness's `go install` leave a linter in GOBIN,
// as a real build does.
func buildingOptions(t *testing.T, h *lintHarness) LintOptions {
	t.Helper()
	options := withLintDefaults(h.options("pre-push", "go"))
	governed := options.Tools.Governed
	options.Tools.Governed = func(dir string, env []string, timeout time.Duration, stdout, stderr io.Writer, args ...string) (int, error) {
		for _, entry := range env {
			if gobin, ok := strings.CutPrefix(entry, "GOBIN="); ok {
				for _, goos := range []string{"linux", "windows"} {
					mustWriteExecutable(t, filepath.Join(gobin, golangciExecutableName(goos)), "#!/bin/sh\n")
				}
			}
		}
		return governed(dir, env, timeout, stdout, stderr, args...)
	}
	return options
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
	t.Run("empty toolchain version", func(t *testing.T) {
		t.Parallel()
		h := newLintHarness(t, t.TempDir())
		h.goEnv = func(string) (string, error) { return "\n", nil }
		check(t, h, h.options("pre-push", "go"), "printed nothing")
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
	readOnlyParent := func(t *testing.T, h *lintHarness, withLock bool) {
		t.Helper()
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("permission bits are not enforced here")
		}
		parent := filepath.Join(h.cache, "wb", "golangci-lint", "go1.27.4")
		mustMkdirAll(t, parent)
		if withLock {
			mustWrite(t, filepath.Join(parent, "v2.15.0.lock"), "")
		}
		if err := os.Chmod(parent, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })
	}
	t.Run("lock cannot be created", func(t *testing.T) {
		t.Parallel()
		h := newLintHarness(t, t.TempDir())
		readOnlyParent(t, h, false)
		check(t, h, h.options("pre-push", "go"), "could not lock the golangci-lint build")
	})
	t.Run("staging cannot be created", func(t *testing.T) {
		t.Parallel()
		h := newLintHarness(t, t.TempDir())
		readOnlyParent(t, h, true)
		check(t, h, h.options("pre-push", "go"), "could not prepare")
	})
	t.Run("build fails", func(t *testing.T) {
		t.Parallel()
		h := newLintHarness(t, t.TempDir())
		h.exit = func([]string) (int, error) { return 1, nil }
		check(t, h, h.options("pre-push", "go"), "could not build golangci-lint v2.15.0")
	})
	t.Run("build succeeds without producing the linter", func(t *testing.T) {
		t.Parallel()
		h := newLintHarness(t, t.TempDir())
		check(t, h, h.options("pre-push", "go"), "produced no golangci-lint")
		if entries, _ := os.ReadDir(filepath.Join(h.cache, "wb", "golangci-lint", "go1.27.4")); len(entries) != 1 {
			t.Fatalf("an empty build was installed: %v", entries)
		}
	})
	t.Run("install target is occupied", func(t *testing.T) {
		t.Parallel()
		h := newLintHarness(t, t.TempDir())
		target := filepath.Join(h.cache, "wb", "golangci-lint", "go1.27.4", "v2.15.0")
		mustMkdirAll(t, filepath.Dir(target))
		mustWrite(t, target, "not a directory\n")
		check(t, h, buildingOptions(t, h), "could not install the built golangci-lint")
	})
}

func TestGolangciBinaryNamesTheWindowsExecutable(t *testing.T) {
	t.Parallel()
	h := newLintHarness(t, t.TempDir())
	runner := lintRunner{options: buildingOptions(t, h)}
	runner.options.Tools.GOOS = "windows"
	binary, ok := runner.golangciBinary(golangciPin{Version: "v2.15.0"}, h.repo, true)
	if !ok || filepath.Base(binary) != "golangci-lint.exe" {
		t.Fatalf("binary %q ok %v: %s", binary, ok, h.stderr.String())
	}
	if runner.goos() != "windows" || (lintRunner{options: withLintDefaults(LintOptions{})}).goos() != runtime.GOOS {
		t.Fatal("goos override")
	}
}

func TestGolangciBinaryWaitsForABuildAnotherHookHolds(t *testing.T) {
	t.Parallel()
	h := newLintHarness(t, t.TempDir())
	target := filepath.Join(h.cache, "wb", "golangci-lint", "go1.27.4", "v2.15.0")
	mustMkdirAll(t, filepath.Dir(target))
	unlock, err := lockFile(target + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		binary string
		ok     bool
	}
	done := make(chan result, 1)
	go func() {
		runner := lintRunner{options: withLintDefaults(h.options("pre-push", "go"))}
		binary, ok := runner.golangciBinary(golangciPin{Version: "v2.15.0"}, h.repo, true)
		done <- result{binary, ok}
	}()
	time.Sleep(50 * time.Millisecond)
	mustMkdirAll(t, target)
	mustWriteExecutable(t, filepath.Join(target, "golangci-lint"), "#!/bin/sh\n")
	unlock()
	got := <-done
	if !got.ok || got.binary != filepath.Join(target, "golangci-lint") || len(h.calls) != 0 {
		t.Fatalf("result %#v calls %q", got, h.calls)
	}
}

func TestPruneStaleBuildsRemovesOnlyAbandonedBuildsOfThisVersion(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	for _, name := range []string{".build-v2.15.0-old", ".build-v2.15.0-new", ".build-v2.14.0-old", "v2.15.0"} {
		mustMkdirAll(t, filepath.Join(parent, name))
	}
	mustWrite(t, filepath.Join(parent, ".build-v2.15.0-file"), "x")
	old := time.Now().Add(-2 * staleBuildAge)
	for _, name := range []string{".build-v2.15.0-old", ".build-v2.14.0-old", ".build-v2.15.0-file"} {
		if err := os.Chtimes(filepath.Join(parent, name), old, old); err != nil {
			t.Fatal(err)
		}
	}
	pruneStaleBuilds(parent, ".build-v2.15.0-", time.Now())
	var left []string
	entries, _ := os.ReadDir(parent)
	for _, entry := range entries {
		left = append(left, entry.Name())
	}
	if want := []string{".build-v2.14.0-old", ".build-v2.15.0-file", ".build-v2.15.0-new", "v2.15.0"}; !reflect.DeepEqual(left, want) {
		t.Fatalf("left %q", left)
	}
	if _, err := lockFile(filepath.Join(parent, "absent", "x.lock")); err == nil {
		t.Fatal("locked a file in a missing directory")
	}
}

func TestPreCommitToolchainProbeNeverDownloads(t *testing.T) {
	t.Parallel()
	h := newLintHarness(t, t.TempDir())
	var probeEnv []string
	options := withLintDefaults(h.options("pre-commit", "go"))
	options.Tools.Capture = func(_ string, env []string, _ string, _ ...string) (string, error) {
		probeEnv = env
		return "go1.27.4", nil
	}
	runner := lintRunner{options: options}
	if _, ok := runner.golangciBinary(golangciPin{Version: "v2.15.0"}, h.repo, false); ok || !reflect.DeepEqual(probeEnv, []string{"GOPROXY=off"}) {
		t.Fatalf("probe env %q", probeEnv)
	}
}

func TestSyncBufferAcceptsConcurrentWriters(t *testing.T) {
	t.Parallel()
	var buffer syncBuffer
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for j := 0; j < 100; j++ {
				_, _ = buffer.Write([]byte("x"))
			}
		}()
	}
	group.Wait()
	if len(buffer.String()) != 800 {
		t.Fatalf("lost writes: %d", len(buffer.String()))
	}
}

// TestGovernedCommandStopsAHungCheckAndItsDescendants is the real topology:
// wb's child spawns a grandchild that keeps the output pipe open, and wb does
// not stop it (an older wb, or one that hung). The hook must still return,
// and must stop the whole tree rather than orphan the grandchild.
func TestGovernedCommandStopsAHungCheckAndItsDescendants(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("process groups are a Darwin/Linux feature")
	}
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "grandchild.pid")
	executable := filepath.Join(dir, "wb")
	mustWriteExecutable(t, executable, "#!/bin/sh\n/bin/sh -c 'sleep 30 & echo $! > \""+pidFile+"\"; wait' &\nsleep 30\n")
	var stdout syncBuffer
	started := time.Now()
	_, err := governedCommandWithGrace(executable, "", 500*time.Millisecond, 100*time.Millisecond, &stdout, &stdout, dir, nil, "golangci-lint", "run")
	elapsed := time.Since(started)
	if err == nil || !errors.Is(err, errCheckTimedOut) || elapsed > 5*time.Second {
		t.Fatalf("err %v after %s", err, elapsed)
	}
	content, readErr := os.ReadFile(pidFile)
	if readErr != nil {
		t.Fatal(readErr)
	}
	var pid int
	if _, err := fmt.Sscan(string(content), &pid); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		process, _ := os.FindProcess(pid)
		if process.Signal(syscall.Signal(0)) != nil {
			return
		}
		if time.Now().After(deadline) {
			_ = process.Kill()
			t.Fatalf("grandchild %d survived the timeout", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestPushedFilesSeeMergesRenamesForcePushesAndStreamCommits(t *testing.T) {
	t.Parallel()
	repo := lintRepo(t)
	mainTip := commitFiles(t, repo, map[string]string{"backend/go.mod": "module b\n", "backend/a.go": "package b\n", "other/go.mod": "module o\n"})
	git(t, repo, "push", "-q", "origin", "main")
	h := newLintHarness(t, repo)
	runner := lintRunner{options: withLintDefaults(h.options("pre-push", "go"))}
	files := func(update RefUpdate) []string {
		t.Helper()
		got, all := runner.pushedFiles([]RefUpdate{update})
		if all {
			t.Fatalf("no base for %#v", update)
		}
		return got
	}

	// A rename reports the path it left, so the old package is checked too.
	git(t, repo, "mv", "backend/a.go", "other/a.go")
	git(t, repo, "commit", "-q", "-m", "move")
	moved := git(t, repo, "rev-parse", "HEAD")
	if got := files(RefUpdate{LocalSHA: moved, RemoteSHA: mainTip}); !reflect.DeepEqual(got, []string{"backend/a.go", "other/a.go"}) {
		t.Fatalf("rename: %q", got)
	}

	// A commit first pushed to a stream branch (Tier 0, unlinted) and then
	// merged into main still counts when main is pushed.
	git(t, repo, "checkout", "-q", "-b", "stream/x", mainTip)
	streamTip := commitFiles(t, repo, map[string]string{"backend/s.go": "package b\n"})
	git(t, repo, "push", "-q", "origin", "stream/x")
	git(t, repo, "checkout", "-q", "main")
	git(t, repo, "reset", "-q", "--hard", mainTip)
	git(t, repo, "merge", "-q", "--no-ff", "-m", "merge stream", streamTip)
	merged := git(t, repo, "rev-parse", "HEAD")
	if got := files(RefUpdate{LocalSHA: merged, RemoteSHA: mainTip}); !reflect.DeepEqual(got, []string{"backend/s.go"}) {
		t.Fatalf("merge of a stream commit: %q", got)
	}

	// A force push compares trees, so the files of dropped commits count.
	git(t, repo, "push", "-q", "origin", merged+":refs/heads/main")
	git(t, repo, "reset", "-q", "--hard", mainTip)
	rewritten := commitFiles(t, repo, map[string]string{"other/o.go": "package o\n"})
	if got := files(RefUpdate{LocalSHA: rewritten, RemoteSHA: merged}); !reflect.DeepEqual(got, []string{"backend/s.go", "other/o.go"}) {
		t.Fatalf("force push: %q", got)
	}
}

func TestGoModuleDiscoverySkipsIgnoredPackagesAndFollowsLinterConfig(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	for _, module := range []string{".", "backend", "emulatortest", "tools/gen"} {
		mustMkdirAll(t, filepath.Join(repo, module))
		mustWrite(t, filepath.Join(repo, module, "go.mod"), "module m\n")
	}
	if got := goModulesFor(repo, []string{"pkg/testdata/fixture.go", "vendor/x/y.go", "_scratch/z.go"}, false); len(got) != 0 {
		t.Fatalf("ignored package dirs touched %q", got)
	}
	if got := goModulesFor(repo, []string{".golangci.yml"}, false); !reflect.DeepEqual(got, []string{".", "backend", "emulatortest", "tools/gen"}) {
		t.Fatalf("root config: %q", got)
	}
	if got := goModulesFor(repo, []string{"tools/.golangci.yaml"}, false); !reflect.DeepEqual(got, []string{".", "tools/gen"}) {
		t.Fatalf("nested config: %q", got)
	}
	if moduleRelative("backend", "backend") != "." || moduleRelative(".", "a/b") != "a/b" || moduleRelative("backend", "backend/api") != "api" {
		t.Fatal("moduleRelative")
	}
	if got := splitNUL(" lead.go\x00trail.go \x00\x00x.go\n"); !reflect.DeepEqual(got, []string{" lead.go", "trail.go ", "x.go"}) {
		t.Fatalf("splitNUL %q", got)
	}
}

func TestGoPreCommitNeverLintsTestdataOrVendoredPackages(t *testing.T) {
	t.Parallel()
	repo := initRepo(t)
	for name, content := range map[string]string{"go.mod": "module r\n", "pkg/testdata/f.go": "package f\n", "vendor/v/v.go": "package v\n"} {
		mustMkdirAll(t, filepath.Dir(filepath.Join(repo, name)))
		mustWrite(t, filepath.Join(repo, name), content)
	}
	git(t, repo, "add", "-A")
	h := newLintHarness(t, repo)
	h.plantLinter("go1.27.4", DefaultGolangciLintVersion)
	if code := h.run("pre-commit", "go"); code != 0 || len(h.calls) != 0 {
		t.Fatalf("exit %d calls %q", code, h.calls)
	}
}

func TestGolangciPinRejectsUnsafeOrInexactWorkflowValues(t *testing.T) {
	t.Parallel()
	projects := t.TempDir()
	var gitCalls [][]string
	capture := func(dir string, _ []string, name string, args ...string) (string, error) {
		gitCalls = append(gitCalls, append([]string{dir}, args...))
		return "", errors.New("no clone")
	}
	for _, test := range []struct{ version, want, source string }{
		{"v2.15.0", "v2.15.0", ".github/workflows/ci.yml"},
		{"2.15.0", "v2.15.0", ".github/workflows/ci.yml"},
		{"v2.6.0-rc.1", "v2.6.0-rc.1", ".github/workflows/ci.yml"},
		{"1b907273167eec8b3d84cae636b7798ced4433d5", "1b907273167eec8b3d84cae636b7798ced4433d5", ".github/workflows/ci.yml"},
		{"1B907273", "1B907273", ".github/workflows/ci.yml"},
		{"v2", DefaultGolangciLintVersion, `pins "v2", which is not an exact version`},
		{"v2.1", DefaultGolangciLintVersion, `pins "v2.1"`},
		{"main", DefaultGolangciLintVersion, `pins "main"`},
		{"..", DefaultGolangciLintVersion, `pins ".."`},
		{"../../../etc", DefaultGolangciLintVersion, `not an exact version`},
		{"-O/tmp/x", DefaultGolangciLintVersion, `not an exact version`},
	} {
		repo := t.TempDir()
		mustMkdirAll(t, filepath.Join(repo, ".github", "workflows"))
		mustWrite(t, filepath.Join(repo, ".github", "workflows", "ci.yml"),
			"jobs:\n  lint:\n    steps:\n      - uses: golangci/golangci-lint-action-fork@v1\n        with:\n          version: v9.9.9\n      - uses: golangci/golangci-lint-action@v9\n        with:\n          version: '"+test.version+"'\n")
		pin := resolveGolangciPin(repo, projects, capture)
		if pin.Version != test.want || !strings.Contains(pin.Source, test.source) {
			t.Errorf("version %q: pin %#v", test.version, pin)
		}
	}
	for _, uses := range []string{
		"../../etc/.github/workflows/w.yml@v1",
		"owner/../.github/workflows/w.yml@v1",
		"owner/repo/.github/workflows/../../x.yml@v1",
		"owner/repo/.github/workflows/w.yml@-Ofile",
		"owner/repo/.github/workflows/w.yml@a..b",
		"owner/repo/.github/workflows/w.txt@v1",
		"owner/repo/.github/workflows/w.yml",
		"owner/.github/workflows/w.yml@v1",
		".hidden/repo/.github/workflows/w.yml@v1",
		"owner/./.github/workflows/w.yml@v1",
		"owner/-repo/.github/workflows/w.yml@v1",
		"owner/.-x/.github/workflows/w.yml@v1",
	} {
		if _, ok := golangciPinFromReusable(uses, projects, capture); ok {
			t.Errorf("%q resolved", uses)
		}
	}
	if len(gitCalls) != 0 {
		t.Fatalf("an unsafe reference reached git: %q", gitCalls)
	}
	for _, uses := range []string{"owner/repo/.github/workflows/w.yml@v1.2.3", "sneat-dev/.github/.github/workflows/w.yml@v1.2.3"} {
		gitCalls = nil
		if _, ok := golangciPinFromReusable(uses, projects, capture); ok || len(gitCalls) != 2 {
			t.Fatalf("%s was not looked up: %q", uses, gitCalls)
		}
		for _, call := range gitCalls {
			if call[1] != "show" || call[2] != "--end-of-options" || call[3] != "v1.2.3:.github/workflows/w.yml" {
				t.Fatalf("git call %q", call)
			}
		}
	}
}

func TestLockFileReportsALockTheFilesystemRefuses(t *testing.T) {
	t.Parallel()
	refuse := func(int, int) error { return errors.New("locks unsupported") }
	if _, err := lockFileWith(filepath.Join(t.TempDir(), "x.lock"), refuse); err == nil || err.Error() != "locks unsupported" {
		t.Fatalf("err %v", err)
	}
}

func TestPushedFilesFallsBackWhenThePushedTipIsUnknown(t *testing.T) {
	t.Parallel()
	repo := lintRepo(t)
	h := newLintHarness(t, repo)
	runner := lintRunner{options: withLintDefaults(h.options("pre-push", "go"))}
	if files, all := runner.pushedFiles([]RefUpdate{{LocalSHA: strings.Repeat("e", 40), RemoteSHA: git(t, repo, "rev-parse", "HEAD")}}); !all || files != nil {
		t.Fatalf("files %q all %v", files, all)
	}
}

func TestGoPrePushLeavesAVetThatCannotRunToCI(t *testing.T) {
	t.Parallel()
	repo := lintRepo(t)
	head := commitFiles(t, repo, map[string]string{"go.mod": "module r\n", "a.go": "package r\n"})
	h := newLintHarness(t, repo)
	h.plantLinter("go1.27.4", DefaultGolangciLintVersion)
	h.exit = func(args []string) (int, error) {
		if args[0] == "go" {
			return 0, fmt.Errorf("go %w (10m0s)", errCheckTimedOut)
		}
		return 0, nil
	}
	if code := h.runWithStdin("pre-push", "go", pushLine("f", head)); code != 0 || !strings.Contains(h.stderr.String(), "go vet did not finish in the root module") {
		t.Fatalf("exit %d stderr %q", code, h.stderr.String())
	}
}

// TestGovernedCommandAlwaysRunsWBInItsOwnProcessGroup pins that a hook check
// is never attached to the terminal's process group, whatever the hook's
// output is: there is no interactive branch to fall into.
func TestGovernedCommandAlwaysRunsWBInItsOwnProcessGroup(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("process groups are a Darwin/Linux feature")
	}
	dir := t.TempDir()
	executable := filepath.Join(dir, "wb")
	mustWriteExecutable(t, executable, "#!/bin/sh\nprintf '%s %s' \"$$\" \"$(ps -o pgid= -p $$ | tr -d ' ')\"\n")
	var stdout bytes.Buffer
	if _, err := governedCommand(executable, "", time.Minute, &stdout, os.Stderr, dir, nil, "true"); err != nil {
		t.Fatal(err)
	}
	var pid, pgid int
	if _, err := fmt.Sscan(stdout.String(), &pid, &pgid); err != nil || pid != pgid {
		t.Fatalf("wb pid %d runs in process group %d (%q)", pid, pgid, stdout.String())
	}
}
