package hooks

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	unix "github.com/sneat-dev/wb/internal/unixcompat"
	"gopkg.in/yaml.v3"
)

// DefaultGolangciLintVersion is the linter a repository gets when its own CI
// pins none WB can read offline. It tracks the fleet's reusable Go workflow
// (strongo/cicd .github/workflows/workflow.yml), which pins it explicitly
// because an unpinned "latest" once turned every Go repository red at once.
const DefaultGolangciLintVersion = "v2.15.0"

// golangciPin is the linter version a repository's CI runs and where it was
// read from, so a finding can be traced to the same pin CI uses.
type golangciPin struct {
	Version string
	Source  string
}

type workflowDocument struct {
	Jobs map[string]workflowJob `yaml:"jobs"`
}

type workflowJob struct {
	Uses  string         `yaml:"uses"`
	Steps []workflowStep `yaml:"steps"`
}

type workflowStep struct {
	Uses string         `yaml:"uses"`
	With map[string]any `yaml:"with"`
}

// resolveGolangciPin reads the golangci-lint version the repository's CI
// runs, without the network: a golangci-lint-action `version` input in one of
// its workflows, or the same input inside a reusable workflow it calls,
// read from that workflow repository's local clone at the exact pinned ref.
// Anything unreadable falls back to DefaultGolangciLintVersion.
func resolveGolangciPin(repoRoot, projectsRoot string, capture func(string, []string, string, ...string) (string, error)) golangciPin {
	workflows, _ := filepath.Glob(filepath.Join(repoRoot, ".github", "workflows", "*.y*ml"))
	sort.Strings(workflows)
	for _, workflow := range workflows {
		content, err := os.ReadFile(workflow)
		if err != nil {
			continue
		}
		rel := filepath.ToSlash(strings.TrimPrefix(workflow, repoRoot+string(filepath.Separator)))
		if pin, ok := golangciPinFromWorkflow(content, rel, projectsRoot, capture, true); ok {
			return pin
		}
	}
	return golangciPin{Version: DefaultGolangciLintVersion, Source: "WB default, matching the fleet's reusable Go workflow"}
}

func golangciPinFromWorkflow(content []byte, source, projectsRoot string, capture func(string, []string, string, ...string) (string, error), followReusable bool) (golangciPin, bool) {
	var document workflowDocument
	if err := yaml.Unmarshal(content, &document); err != nil {
		return golangciPin{}, false
	}
	names := make([]string, 0, len(document.Jobs))
	for name := range document.Jobs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		job := document.Jobs[name]
		for _, step := range job.Steps {
			if step.Uses != "golangci/golangci-lint-action" && !strings.HasPrefix(step.Uses, "golangci/golangci-lint-action@") {
				continue
			}
			raw, ok := step.With["version"].(string)
			if !ok || ignoredGolangciVersion(raw) {
				continue
			}
			if version, exact := exactGolangciVersion(raw); exact {
				return golangciPin{Version: version, Source: source}, true
			}
			// A shorthand ("v2", "v2.1"), a branch name, or anything stranger
			// cannot be built reproducibly (and must never become a cache path
			// or a `go install` argument), so the WB default is used instead.
			return golangciPin{
				Version: DefaultGolangciLintVersion,
				Source:  fmt.Sprintf("WB default: %s pins %q, which is not an exact version", source, strings.TrimSpace(raw)),
			}, true
		}
		if !followReusable {
			continue
		}
		if pin, ok := golangciPinFromReusable(job.Uses, projectsRoot, capture); ok {
			return pin, true
		}
	}
	return golangciPin{}, false
}

var (
	exactSemver  = regexp.MustCompile(`^v?([0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?)$`)
	commitSHA    = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	safeRepoName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)
	safeRef      = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./-]*$`)
	workflowFile = regexp.MustCompile(`^\.github/workflows/[A-Za-z0-9_][A-Za-z0-9_.-]*\.ya?ml$`)
)

// exactGolangciVersion accepts only a full release version (with or without
// the leading "v", which `go install` requires) or a commit SHA.
func exactGolangciVersion(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if match := exactSemver.FindStringSubmatch(raw); match != nil {
		return "v" + match[1], true
	}
	if commitSHA.MatchString(raw) {
		return raw, true
	}
	return "", false
}

// ignoredGolangciVersion reports values that do not pin anything here: an
// empty input, "latest", or an expression evaluated by the CI runner.
func ignoredGolangciVersion(raw string) bool {
	raw = strings.TrimSpace(raw)
	return raw == "" || raw == "latest" || strings.Contains(raw, "${{")
}

// golangciPinFromReusable follows `uses: owner/repo/.github/workflows/x.yml@ref`
// into the workflow repository's local clone, reading the file at exactly
// that ref. It never fetches: an absent clone or ref is simply unknown. Every
// part of the reference comes from a workflow file and is validated before it
// becomes a path or a Git argument.
func golangciPinFromReusable(uses, projectsRoot string, capture func(string, []string, string, ...string) (string, error)) (golangciPin, bool) {
	target, ref, found := strings.Cut(strings.TrimSpace(uses), "@")
	parts := strings.SplitN(target, "/", 3)
	if !found || projectsRoot == "" || len(parts) != 3 {
		return golangciPin{}, false
	}
	owner, repo, file := parts[0], parts[1], parts[2]
	for _, name := range []string{owner, repo} {
		if !safeRepoName.MatchString(name) || strings.Contains(name, "..") {
			return golangciPin{}, false
		}
	}
	if !workflowFile.MatchString(file) || !safeRef.MatchString(ref) || strings.Contains(ref, "..") {
		return golangciPin{}, false
	}
	for _, clone := range []string{
		filepath.Join(projectsRoot, owner, repo),
		filepath.Join(projectsRoot, "github.com", owner, repo),
	} {
		content, err := capture(clone, nil, "git", "show", "--end-of-options", ref+":"+file)
		if err != nil {
			continue
		}
		source := owner + "/" + repo + "/" + file + "@" + ref
		if pin, ok := golangciPinFromWorkflow([]byte(content), source, projectsRoot, capture, false); ok {
			return pin, true
		}
	}
	return golangciPin{}, false
}

func golangciPackage(version string) string {
	if strings.HasPrefix(version, "v1.") {
		return "github.com/golangci/golangci-lint/cmd/golangci-lint"
	}
	return "github.com/golangci/golangci-lint/v2/cmd/golangci-lint"
}

var unsafePathSegment = regexp.MustCompile(`[^0-9A-Za-z._-]`)

// staleBuildAge is how old an abandoned build directory (a hook killed
// mid-build) must be before a later build removes it.
const staleBuildAge = time.Hour

func golangciExecutableName(goos string) string {
	if goos == "windows" {
		return "golangci-lint.exe"
	}
	return "golangci-lint"
}

func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// golangciBinary returns the pinned linter built with the Go toolchain the
// module itself selects, so the linter can read that toolchain's export data
// (a linter built by an older Go cannot: "export data version 5 is greater
// than maximum supported version 4"). A missing binary is built once per
// version and toolchain when build is true (pre-push), under a lock so
// concurrent pushes build it once; pre-commit never builds one and never lets
// the toolchain probe download a Go release (GOPROXY=off). ok is false when
// no linter is available, after saying why.
func (r lintRunner) golangciBinary(pin golangciPin, moduleDir string, build bool) (string, bool) {
	tools := r.options.Tools
	var probeEnv []string
	if !build {
		probeEnv = []string{"GOPROXY=off"}
	}
	output, err := tools.Capture(moduleDir, probeEnv, "go", "env", "GOVERSION")
	goVersion := strings.TrimSpace(output)
	if err == nil && goVersion == "" {
		err = errors.New("go env GOVERSION printed nothing")
	}
	if err != nil {
		r.say("WB hook: could not read the Go toolchain version (%v); skipping golangci-lint, CI remains the lint gate.", err)
		return "", false
	}
	cacheRoot, err := tools.CacheRoot()
	if err != nil {
		r.say("WB hook: no user cache directory for the pinned golangci-lint (%v); skipping golangci-lint, CI remains the lint gate.", err)
		return "", false
	}
	versionSegment := unsafePathSegment.ReplaceAllString(pin.Version, "_")
	dir := filepath.Join(cacheRoot, "wb", "golangci-lint", unsafePathSegment.ReplaceAllString(goVersion, "_"), versionSegment)
	name := golangciExecutableName(r.goos())
	binary := filepath.Join(dir, name)
	if isRegularFile(binary) {
		return binary, true
	}
	if !build {
		r.say("WB hook: golangci-lint %s is not built for %s on this machine yet; the next pre-push builds it. Skipping changed-line lint for this commit.", pin.Version, goVersion)
		return "", false
	}
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		r.say("WB hook: could not prepare %s (%v); skipping golangci-lint, CI remains the lint gate.", dir, err)
		return "", false
	}
	unlock, err := lockFile(dir + ".lock")
	if err != nil {
		r.say("WB hook: could not lock the golangci-lint build in %s (%v); skipping golangci-lint, CI remains the lint gate.", parent, err)
		return "", false
	}
	defer unlock()
	if isRegularFile(binary) {
		// Another hook built it while this one waited for the lock.
		return binary, true
	}
	pruneStaleBuilds(parent, ".build-"+versionSegment+"-", time.Now())
	staging, err := os.MkdirTemp(parent, ".build-"+versionSegment+"-*")
	if err != nil {
		r.say("WB hook: could not prepare %s (%v); skipping golangci-lint, CI remains the lint gate.", dir, err)
		return "", false
	}
	defer func() { _ = os.RemoveAll(staging) }()
	r.say("WB hook: building golangci-lint %s (%s) with %s, once per machine and toolchain…", pin.Version, pin.Source, goVersion)
	code, err := r.governed(moduleDir, []string{"GOBIN=" + staging, "GOFLAGS="}, buildTimeout, "go", "install", golangciPackage(pin.Version)+"@"+pin.Version)
	if err != nil || code != 0 {
		r.say("WB hook: could not build golangci-lint %s (offline, or the version is unavailable); this push is checked by go vet only and CI remains the lint gate.", pin.Version)
		return "", false
	}
	if !isRegularFile(filepath.Join(staging, name)) {
		r.say("WB hook: building golangci-lint %s produced no %s; skipping golangci-lint, CI remains the lint gate.", pin.Version, name)
		return "", false
	}
	if err := os.Rename(staging, dir); err != nil {
		r.say("WB hook: could not install the built golangci-lint into %s (%v); skipping golangci-lint, CI remains the lint gate.", dir, err)
		return "", false
	}
	return binary, true
}

func (r lintRunner) goos() string {
	if r.options.Tools.GOOS != "" {
		return r.options.Tools.GOOS
	}
	return runtime.GOOS
}

// pruneStaleBuilds removes build directories a killed hook left behind. It
// runs under the target's build lock, so no live build owns them.
func pruneStaleBuilds(parent, prefix string, now time.Time) {
	entries, _ := os.ReadDir(parent)
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		if info, err := entry.Info(); err == nil && now.Sub(info.ModTime()) > staleBuildAge {
			_ = os.RemoveAll(filepath.Join(parent, entry.Name()))
		}
	}
}

// lockFile takes an exclusive advisory lock on path, waiting for any holder
// (whose build is itself bounded by buildTimeout). The file is never
// removed, so two waiters can never lock different inodes for one target.
func lockFile(path string) (func(), error) { return lockFileWith(path, unix.Flock) }

// lockFileWith is lockFile with the flock call as a seam, so the refusal
// branch is testable without a filesystem that refuses locks.
func lockFileWith(path string, flock func(int, int) error) (func(), error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := flock(int(file.Fd()), unix.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, err
	}
	return func() {
		_ = flock(int(file.Fd()), unix.LOCK_UN)
		_ = file.Close()
	}, nil
}
