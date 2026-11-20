package hooks

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

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
			if !strings.HasPrefix(step.Uses, "golangci/golangci-lint-action") {
				continue
			}
			if version, ok := step.With["version"].(string); ok && usableGolangciVersion(version) {
				return golangciPin{Version: strings.TrimSpace(version), Source: source}, true
			}
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

var reusableWorkflowRef = regexp.MustCompile(`^([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)/(\.github/workflows/[^@]+)@([0-9A-Za-z._/-]+)$`)

// golangciPinFromReusable follows `uses: owner/repo/.github/workflows/x.yml@ref`
// into the workflow repository's local clone, reading the file at exactly
// that ref. It never fetches: an absent clone or ref is simply unknown.
func golangciPinFromReusable(uses, projectsRoot string, capture func(string, []string, string, ...string) (string, error)) (golangciPin, bool) {
	match := reusableWorkflowRef.FindStringSubmatch(strings.TrimSpace(uses))
	if match == nil || projectsRoot == "" {
		return golangciPin{}, false
	}
	owner, repo, file, ref := match[1], match[2], match[3], match[4]
	for _, clone := range []string{
		filepath.Join(projectsRoot, owner, repo),
		filepath.Join(projectsRoot, "github.com", owner, repo),
	} {
		content, err := capture(clone, nil, "git", "show", ref+":"+file)
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

func usableGolangciVersion(version string) bool {
	version = strings.TrimSpace(version)
	return version != "" && version != "latest" && !strings.Contains(version, "${{")
}

func golangciPackage(version string) string {
	if strings.HasPrefix(version, "v1.") {
		return "github.com/golangci/golangci-lint/cmd/golangci-lint"
	}
	return "github.com/golangci/golangci-lint/v2/cmd/golangci-lint"
}

var unsafePathSegment = regexp.MustCompile(`[^0-9A-Za-z._-]`)

// golangciBinary returns the pinned linter built with the Go toolchain the
// module itself selects, so the linter can read that toolchain's export data
// (a linter built by an older Go cannot: "export data version 5 is greater
// than maximum supported version 4"). A missing binary is built once per
// version and toolchain when build is true (pre-push); pre-commit never
// builds one. ok is false when no linter is available, after saying why.
func (r lintRunner) golangciBinary(pin golangciPin, moduleDir string, build bool) (string, bool) {
	tools := r.options.Tools
	goVersion, err := tools.Capture(moduleDir, nil, "go", "env", "GOVERSION")
	if err != nil {
		r.say("WB hook: could not read the Go toolchain version (%v); skipping golangci-lint, CI remains the lint gate.", err)
		return "", false
	}
	cacheRoot, err := tools.CacheRoot()
	if err != nil {
		r.say("WB hook: no user cache directory for the pinned golangci-lint (%v); skipping golangci-lint, CI remains the lint gate.", err)
		return "", false
	}
	dir := filepath.Join(cacheRoot, "wb", "golangci-lint",
		unsafePathSegment.ReplaceAllString(strings.TrimSpace(goVersion), "_"),
		unsafePathSegment.ReplaceAllString(pin.Version, "_"))
	binary := filepath.Join(dir, "golangci-lint")
	if info, err := os.Stat(binary); err == nil && !info.IsDir() {
		return binary, true
	}
	if !build {
		r.say("WB hook: golangci-lint %s is not built for %s on this machine yet; the next pre-push builds it. Skipping changed-line lint for this commit.", pin.Version, strings.TrimSpace(goVersion))
		return "", false
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		r.say("WB hook: could not prepare %s (%v); skipping golangci-lint, CI remains the lint gate.", dir, err)
		return "", false
	}
	staging, err := os.MkdirTemp(filepath.Dir(dir), ".build-*")
	if err != nil {
		r.say("WB hook: could not prepare %s (%v); skipping golangci-lint, CI remains the lint gate.", dir, err)
		return "", false
	}
	defer func() { _ = os.RemoveAll(staging) }()
	r.say("WB hook: building golangci-lint %s (%s) with %s, once per machine and toolchain…", pin.Version, pin.Source, strings.TrimSpace(goVersion))
	code, err := r.governed(moduleDir, []string{"GOBIN=" + staging, "GOFLAGS="}, "go", "install", golangciPackage(pin.Version)+"@"+pin.Version)
	if err != nil || code != 0 {
		r.say("WB hook: could not build golangci-lint %s (offline, or the version is unavailable); this push is checked by go vet only and CI remains the lint gate.", pin.Version)
		return "", false
	}
	if err := os.Rename(staging, dir); err != nil {
		// A concurrent hook may have finished the same build first.
		if info, statErr := os.Stat(binary); statErr == nil && !info.IsDir() {
			return binary, true
		}
		r.say("WB hook: could not install the built golangci-lint into %s (%v); skipping golangci-lint, CI remains the lint gate.", dir, err)
		return "", false
	}
	return binary, true
}
