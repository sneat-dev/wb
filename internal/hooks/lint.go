package hooks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/process"
	"github.com/sneat-dev/wb/internal/streambranch"
)

// LintOptions drive `wb hooks lint`, the static-check step the built-in Go
// and Node profiles run at pre-commit and pre-push.
//
// Pre-push lints every Go module (nested go.mod included) and every JS/TS
// package the pushed commits touch, with the linter version the repository's
// own CI pins. Pre-commit runs golangci-lint over the staged packages only,
// reporting issues on new or changed lines, and never builds a linter: a
// commit is a save point measured in seconds.
type LintOptions struct {
	RepoRoot     string
	Hook         string
	Language     string
	ProjectsRoot string
	WBExecutable string
	Stdin        io.Reader
	Stdout       io.Writer
	Stderr       io.Writer
	Tools        LintTools
}

// LintTools are the process seams `wb hooks lint` uses. Zero fields take
// their production behaviour; tests replace them to pin exactly what runs.
type LintTools struct {
	// Capture runs a plain helper (git, go env) and returns its stdout.
	Capture func(dir string, env []string, name string, args ...string) (string, error)
	// Governed runs one check through `wb run --`, streaming its output to
	// stdout and stderr, and returns the checked command's exit code. A
	// check still running after timeout is killed and reported as an error.
	Governed func(dir string, env []string, timeout time.Duration, stdout, stderr io.Writer, args ...string) (int, error)
	// Classify decides the push tier for the pushed-ref list.
	Classify func(stdin io.Reader, repoRoot string) (Classification, error)
	LookPath func(string) (string, error)
	// CacheRoot is where pinned linter binaries are kept.
	CacheRoot func() (string, error)
	// IsTerminal reports whether stdin is an interactive terminal.
	IsTerminal func(any) bool
	// GOOS overrides runtime.GOOS for the linter's executable name.
	GOOS string
}

const (
	lintLanguageGo   = "go"
	lintLanguageNode = "node"
)

// Lint runs the requested hook's static checks and returns the process exit
// code: 0 when the checks pass or have nothing to check, 1 on findings, 2 on
// a usage error.
func Lint(options LintOptions) int {
	options = withLintDefaults(options)
	runner := lintRunner{options: options}
	root, err := RepositoryRoot(firstNonEmpty(options.RepoRoot, "."))
	if err != nil {
		runner.say("WB hook: lint must run inside a Git checkout: %v", err)
		return 2
	}
	runner.options.RepoRoot = root
	switch {
	case options.Hook == "pre-push" && options.Language == lintLanguageGo:
		return runner.prePushGo()
	case options.Hook == "pre-push" && options.Language == lintLanguageNode:
		return runner.prePushNode()
	case options.Hook == "pre-commit" && options.Language == lintLanguageGo:
		return runner.preCommitGo()
	}
	runner.say("WB hook: unsupported lint target --hook %q --language %q (want pre-push with go or node, or pre-commit with go).", options.Hook, options.Language)
	return 2
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func withLintDefaults(options LintOptions) LintOptions {
	if options.Stdin == nil {
		options.Stdin = os.Stdin
	}
	if options.Stdout == nil {
		options.Stdout = os.Stdout
	}
	if options.Stderr == nil {
		options.Stderr = os.Stderr
	}
	tools := &options.Tools
	if tools.Capture == nil {
		tools.Capture = captureCommand
	}
	if tools.Governed == nil {
		executable, projectsRoot := options.WBExecutable, options.ProjectsRoot
		tools.Governed = func(dir string, env []string, timeout time.Duration, stdout, stderr io.Writer, args ...string) (int, error) {
			return governedCommand(executable, projectsRoot, timeout, stdout, stderr, dir, env, args...)
		}
	}
	if tools.Classify == nil {
		tools.Classify = ClassifyPendingPush
	}
	if tools.LookPath == nil {
		tools.LookPath = exec.LookPath
	}
	if tools.CacheRoot == nil {
		tools.CacheRoot = os.UserCacheDir
	}
	if tools.IsTerminal == nil {
		tools.IsTerminal = console.IsTerminal
	}
	return options
}

// Every check a hook runs is bounded, so a stalled network or a hung linter
// daemon can never hold a commit or push open indefinitely — a hook that
// hangs is a hook people learn to bypass.
const (
	captureTimeout      = time.Minute
	buildTimeout        = 10 * time.Minute
	prePushCheckTimeout = 10 * time.Minute
	preCommitTimeout    = 2 * time.Minute
)

func captureCommand(dir string, env []string, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), captureTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// gatewayGrace is how long past a check's own deadline the hook waits for
// `wb run --timeout` to stop the check before stopping wb itself.
const gatewayGrace = 15 * time.Second

// governedCommand routes one check through `wb run --` so it is admitted
// against the shared CPU budget and recorded like every other governed
// command, preserving the checked command's exit code.
//
// The deadline is passed to `wb run --timeout`, which owns the check's
// process group and stops all of it. As a backstop the hook runs wb in its
// own process group and stops that group gatewayGrace later, with a bounded
// wait for any descendant still holding an output pipe.
//
// A non-zero exit is the check's own only when wb says so ("<command> exited
// with status N"); any other failure is wb's (a refusal, a timeout, a broken
// gateway) and is returned as an error, never as a finding.
func governedCommand(executable, projectsRoot string, timeout time.Duration, stdout, stderr io.Writer, dir string, env []string, args ...string) (int, error) {
	return governedCommandWithGrace(executable, projectsRoot, timeout, gatewayGrace, stdout, stderr, dir, env, args...)
}

func governedCommandWithGrace(executable, projectsRoot string, timeout, grace time.Duration, stdout, stderr io.Writer, dir string, env []string, args ...string) (int, error) {
	if len(args) == 0 {
		return 0, errors.New("no command to run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout+grace)
	defer cancel()
	full := []string{}
	if projectsRoot != "" {
		full = append(full, "--projects-root", projectsRoot)
	}
	full = append(append(full, "run", "--quiet", "--timeout", timeout.String(), "--"), args...)
	var gatewayOutput syncBuffer
	cmd := process.CommandContextInteractive(ctx, console.IsTerminal(stdout) || console.IsTerminal(stderr), executable, full...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = stdout
	cmd.Stderr = io.MultiWriter(stderr, &gatewayOutput)
	err := cmd.Run()
	if ctx.Err() != nil {
		return 0, fmt.Errorf("%s did not finish within %s", args[0], timeout)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return 0, err
	}
	code := exitErr.ExitCode()
	childExit := regexp.MustCompile(`(?m)^error: ` + regexp.QuoteMeta(args[0]) + ` exited with status ` + fmt.Sprint(code) + `\r?$`)
	if !childExit.MatchString(gatewayOutput.String()) {
		return 0, fmt.Errorf("wb run did not complete %s (exit %d)", args[0], code)
	}
	return code, nil
}

type lintRunner struct {
	options LintOptions
}

func (r lintRunner) say(format string, args ...any) {
	_, _ = fmt.Fprintf(r.options.Stderr, format+"\n", args...)
}

// governed runs one check through `wb run --` with the hook's own output.
func (r lintRunner) governed(dir string, env []string, timeout time.Duration, args ...string) (int, error) {
	return r.options.Tools.Governed(dir, env, timeout, r.options.Stdout, r.options.Stderr, args...)
}

// syncBuffer is a bytes.Buffer two process-output copiers may write at once:
// exec copies stdout and stderr on separate goroutines when they differ.
type syncBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

// splitNUL splits `git -z` output. Paths are taken exactly as Git wrote
// them: a name may legitimately begin or end with a space.
func splitNUL(out string) []string {
	var parts []string
	for _, part := range strings.Split(out, "\x00") {
		if part = strings.TrimRight(part, "\r\n"); part != "" {
			parts = append(parts, part)
		}
	}
	return parts
}

func (r lintRunner) git(args ...string) (string, error) {
	return r.options.Tools.Capture(r.options.RepoRoot, nil, "git", args...)
}

// pushScope reads the pushed-ref list, prints the tier decision, and returns
// the ref updates whose commits must be checked. skip is true for a Tier 0
// push (deletions, checkpoint refs, stream branches only).
func (r lintRunner) pushScope() (updates []RefUpdate, skip bool) {
	classifyInput := r.options.Stdin
	if !r.options.Tools.IsTerminal(r.options.Stdin) {
		input, _ := io.ReadAll(r.options.Stdin)
		updates, _ = ParseRefUpdates(bytes.NewReader(input))
		classifyInput = bytes.NewReader(input)
	}
	classification, err := r.options.Tools.Classify(classifyInput, r.options.RepoRoot)
	if err != nil {
		classification = Classification{Tier: TierLint, Reason: fmt.Sprintf("classification failed (%v); defaulting to the fast lane, CI is the real gate", err)}
	}
	_, _ = fmt.Fprintf(r.options.Stdout, "WB hook: tier %d — %s\n", classification.ExitCode(), classification.Reason)
	if classification.Tier == TierSkip {
		return nil, true
	}
	checked := updates[:0:0]
	for _, update := range updates {
		if isDeletion(update) || isCheckpointRef(update.RemoteRef) || streambranch.Is(update.RemoteRef) {
			continue
		}
		checked = append(checked, update)
	}
	if len(checked) == 0 {
		// An interactive or unparsable invocation names no refs: check what
		// HEAD would publish.
		checked = []RefUpdate{{LocalRef: "HEAD", LocalSHA: "HEAD", RemoteSHA: zeroOID40}}
	}
	return checked, false
}

// pushedFiles lists the repository-relative paths each pushed ref changes:
// the tree diff from the remote's current tip (or, for a new branch or a
// remote tip this checkout lacks, from the merge base with the default
// branch) to the pushed tip. A tree diff covers merge commits, a force push
// that drops commits, and commits already on some other remote ref (a stream
// branch pushed at Tier 0), none of which a "commits not on any remote" log
// would report. Renames count both paths, since removing a file can break
// the package it left. all is true when no base is known, and the caller
// must then check everything.
func (r lintRunner) pushedFiles(updates []RefUpdate) (files []string, all bool) {
	seen := map[string]bool{}
	for _, update := range updates {
		base, ok := r.pushBase(update)
		if !ok {
			return nil, true
		}
		out, err := r.git("diff", "--name-only", "-z", "--no-renames", base, update.LocalSHA, "--")
		if err != nil {
			return nil, true
		}
		for _, file := range splitNUL(out) {
			if !seen[file] {
				seen[file] = true
				files = append(files, file)
			}
		}
	}
	sort.Strings(files)
	return files, false
}

// pushBase names the commit a pushed tip is compared against: the
// remote's current tip when this checkout has it, otherwise the merge base
// with the default branch's remote-tracking ref. ok is false when neither is
// known locally.
func (r lintRunner) pushBase(update RefUpdate) (string, bool) {
	if !isZeroOID(update.RemoteSHA) {
		if _, err := r.git("cat-file", "-e", update.RemoteSHA+"^{commit}"); err == nil {
			return update.RemoteSHA, true
		}
	}
	defaultBranch := DetectDefaultBranch(r.options.RepoRoot)
	if defaultBranch == "" {
		return "", false
	}
	base, err := r.git("merge-base", update.LocalSHA, "refs/remotes/origin/"+defaultBranch)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(base), true
}

// ignoredGoDir reports whether the go command itself ignores a directory:
// testdata, vendor, and names starting with "." or "_" (plus node_modules,
// which is never Go source).
func ignoredGoDir(rel string) bool {
	if rel == "." {
		return false
	}
	for _, segment := range strings.Split(filepath.ToSlash(rel), "/") {
		if segment == "testdata" || segment == "vendor" || segment == "node_modules" ||
			strings.HasPrefix(segment, ".") || strings.HasPrefix(segment, "_") {
			return true
		}
	}
	return false
}

// nearestMarkerDir walks up from a repository-relative directory to the
// closest directory holding marker, stopping at the repository root.
func nearestMarkerDir(repoRoot, dir, marker string) (string, bool) {
	candidate := path.Clean(filepath.ToSlash(dir))
	for {
		if info, err := os.Stat(filepath.Join(repoRoot, filepath.FromSlash(candidate), marker)); err == nil && !info.IsDir() {
			return candidate, true
		}
		if candidate == "." {
			return "", false
		}
		candidate = path.Dir(candidate)
	}
}

func isGolangciConfig(file string) bool {
	switch path.Base(file) {
	case ".golangci.yml", ".golangci.yaml", ".golangci.toml", ".golangci.json":
		return true
	}
	return false
}

func isGoModuleInput(file string) bool {
	switch path.Base(file) {
	case "go.mod", "go.sum":
		return true
	}
	return isGolangciConfig(file) || strings.HasSuffix(file, ".go")
}

// moduleRelative expresses a repository-relative directory relative to the
// module that contains it.
func moduleRelative(module, dir string) string {
	switch {
	case dir == module:
		return "."
	case module == ".":
		return dir
	}
	return strings.TrimPrefix(dir, module+"/")
}

// goModulesFor maps changed files to the Go modules that own them, each
// module a repository-relative directory. A file in a directory the go
// command ignores (testdata, vendor, _x, .x) touches no module. A golangci
// configuration file touches every module at or beneath its directory, since
// golangci-lint finds it by walking up from each. With all set, every module
// in the repository is returned.
func goModulesFor(repoRoot string, files []string, all bool) []string {
	if all {
		return allMarkerDirs(repoRoot, "go.mod", ignoredGoDir)
	}
	var modulesInRepo []string
	walked := false
	seen := map[string]bool{}
	var modules []string
	add := func(module string) {
		if !seen[module] {
			seen[module] = true
			modules = append(modules, module)
		}
	}
	for _, file := range files {
		if !isGoModuleInput(file) {
			continue
		}
		dir := path.Dir(file)
		if isGolangciConfig(file) {
			if !walked {
				// Walk the repository only when a linter configuration changed.
				modulesInRepo, walked = allMarkerDirs(repoRoot, "go.mod", ignoredGoDir), true
			}
			for _, module := range modulesInRepo {
				if dir == "." || module == dir || strings.HasPrefix(module, dir+"/") {
					add(module)
				}
			}
		}
		module, ok := nearestMarkerDir(repoRoot, dir, "go.mod")
		if !ok || ignoredGoDir(module) || ignoredGoDir(moduleRelative(module, dir)) {
			continue
		}
		add(module)
	}
	sort.Strings(modules)
	return modules
}

// allMarkerDirs lists every repository-relative directory holding marker,
// skipping .git and any directory ignored reports.
func allMarkerDirs(repoRoot, marker string, ignored func(string) bool) []string {
	var dirs []string
	_ = filepath.WalkDir(repoRoot, func(current string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(repoRoot, current)
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			if rel != "." && (entry.Name() == ".git" || ignored(rel)) {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() == marker {
			dirs = append(dirs, path.Dir(rel))
		}
		return nil
	})
	sort.Strings(dirs)
	return dirs
}

func moduleLabel(module string) string {
	if module == "." {
		return "the root module"
	}
	return "module " + module
}

func (r lintRunner) moduleDir(module string) string {
	return filepath.Join(r.options.RepoRoot, filepath.FromSlash(module))
}

// prePushGo vets and lints every Go module the pushed commits touch.
func (r lintRunner) prePushGo() int {
	updates, skip := r.pushScope()
	if skip {
		return 0
	}
	files, all := r.pushedFiles(updates)
	modules := goModulesFor(r.options.RepoRoot, files, all)
	if len(modules) == 0 {
		r.say("WB hook: this push changes no Go module; skipping go vet and golangci-lint.")
		return 0
	}
	if _, err := r.options.Tools.LookPath("go"); err != nil {
		r.say("WB hook: go is not on PATH; skipping go vet and golangci-lint (CI remains the gate).")
		return 0
	}
	pin := resolveGolangciPin(r.options.RepoRoot, r.options.ProjectsRoot, r.options.Tools.Capture)
	failed := false
	for _, module := range modules {
		dir := r.moduleDir(module)
		switch code, err := r.governed(dir, nil, prePushCheckTimeout, "go", "vet", "./..."); {
		case err != nil:
			r.say("WB hook: go vet could not run in %s (%v); CI remains the gate.", moduleLabel(module), err)
		case code != 0:
			r.say("WB hook: go vet failed in %s.", moduleLabel(module))
			failed = true
		}
		linter, ok := r.golangciBinary(pin, dir, true)
		if !ok {
			continue
		}
		if r.runGolangci(linter, pin, module, dir, prePushCheckTimeout, "run", "./...") {
			failed = true
		}
	}
	if failed {
		return 1
	}
	return 0
}

// exportDataIssue matches golangci-lint's report that it cannot read the
// toolchain's export data: an issue line (path:line[:col], any text format)
// from the typecheck linter carrying the importer's own message. Colour
// escapes and CRLF line ends are ignored.
var (
	issuePosition = regexp.MustCompile(`^[^\s:]+:\d+(?::\d+)?[:\s]`)
	ansiEscape    = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)
)

func exportDataIssue(output string) bool {
	for _, line := range strings.Split(output, "\n") {
		if issuePosition.MatchString(line) && strings.Contains(line, "could not load export data") && strings.Contains(line, "typecheck") {
			return true
		}
	}
	return false
}

// runGolangci runs one golangci-lint invocation and reports whether it found
// issues. golangci-lint's exit 1 ("issues found") blocks unless the failure
// is positively identified as the tool's own: a pin too old to read this
// toolchain's export data. Anything that is not golangci-lint's verdict at
// all (a crash exit, a timeout, a wb gateway error) is reported loudly and
// left to CI rather than blocking work on a broken tool.
func (r lintRunner) runGolangci(linter string, pin golangciPin, module, dir string, timeout time.Duration, args ...string) bool {
	var output syncBuffer
	code, err := r.options.Tools.Governed(dir, nil, timeout,
		io.MultiWriter(r.options.Stdout, &output), io.MultiWriter(r.options.Stderr, &output),
		append([]string{linter}, args...)...)
	reported := strings.ReplaceAll(ansiEscape.ReplaceAllString(output.String(), ""), "\r", "")
	switch {
	case err != nil:
		r.say("WB hook: golangci-lint %s did not complete in %s (%v); CI remains the lint gate.", pin.Version, moduleLabel(module), err)
	case code == 1 && exportDataIssue(reported):
		// The pinned linter predates this Go toolchain's export data format,
		// so it reports the standard library itself as a type error. That is
		// a tool/toolchain mismatch, not a finding in this change.
		r.say("WB hook: golangci-lint %s cannot read this Go toolchain's export data; its typecheck errors are not findings. Bump the pin in %s (CI uses it too). Skipping golangci-lint in %s, CI remains the lint gate.", pin.Version, pin.Source, moduleLabel(module))
	case code == 1:
		r.say("WB hook: golangci-lint %s (%s) found issues in %s.", pin.Version, pin.Source, moduleLabel(module))
		return true
	case code == 0, code == 5:
		// 5: no Go files to analyse.
	default:
		r.say("WB hook: golangci-lint %s exited %d in %s, which is not a lint verdict; it could not lint this module, CI remains the lint gate.", pin.Version, code, moduleLabel(module))
	}
	return false
}

// preCommitGo lints the staged Go packages, reporting only issues on new or
// changed lines. It uses an already-built pinned linter only: building one
// is a pre-push job, never a commit's.
func (r lintRunner) preCommitGo() int {
	out, err := r.git("diff", "--cached", "--name-only", "--diff-filter=ACMR", "-z", "--", "*.go")
	if err != nil {
		r.say("WB hook: could not list the staged Go files (%v); skipping golangci-lint for this commit.", err)
		return 0
	}
	packages := map[string][]string{}
	var modules []string
	for _, file := range splitNUL(out) {
		dir := path.Dir(file)
		if info, err := os.Stat(r.moduleDir(dir)); err != nil || !info.IsDir() {
			// A staged package whose directory is gone no longer resolves.
			continue
		}
		module, ok := nearestMarkerDir(r.options.RepoRoot, dir, "go.mod")
		relative := moduleRelative(module, dir)
		if !ok || ignoredGoDir(module) || ignoredGoDir(relative) {
			continue
		}
		pkg := "."
		if relative != "." {
			pkg = "./" + relative
		}
		if _, ok := packages[module]; !ok {
			modules = append(modules, module)
		}
		if !containsExact(packages[module], pkg) {
			packages[module] = append(packages[module], pkg)
		}
	}
	if len(modules) == 0 {
		return 0
	}
	sort.Strings(modules)
	pin := resolveGolangciPin(r.options.RepoRoot, r.options.ProjectsRoot, r.options.Tools.Capture)
	args := []string{"run"}
	if _, err := r.git("rev-parse", "--verify", "--quiet", "HEAD^{commit}"); err == nil {
		args = append(args, "--new-from-rev=HEAD")
	}
	failed := false
	for _, module := range modules {
		dir := r.moduleDir(module)
		linter, ok := r.golangciBinary(pin, dir, false)
		if !ok {
			continue
		}
		targets := packages[module]
		sort.Strings(targets)
		if r.runGolangci(linter, pin, module, dir, preCommitTimeout, append(append([]string(nil), args...), targets...)...) {
			failed = true
		}
	}
	if failed {
		return 1
	}
	return 0
}

func containsExact(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
