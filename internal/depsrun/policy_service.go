package depsrun

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/sneat-dev/wb/internal/deps"
	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/policy"
)

type PolicyRequest struct {
	Directory, ProjectsRoot, Policy, DeclaredType string
	Strict                                        bool
}
type PolicyFleetRequest struct {
	Selection Selection
	Policy    string
}
type PolicyValidation struct {
	GroupCount, TypeCount int
	Diagnostics           []policy.Diagnostic
}
type PolicyInitNotice struct{ Path, DetectedType string }
type PolicyDriftRow struct {
	Repository string `json:"repository"`
	Module     string `json:"module"`
	Policy     string `json:"policy,omitempty"`
	Declared   string `json:"declaredType,omitempty"`
	Detected   string `json:"detectedType,omitempty"`
	Issue      string `json:"issue,omitempty"`
}
type PolicyDriftResult struct {
	Rows   []PolicyDriftRow
	Issues int
}
type PolicyChange struct {
	Repository string `json:"repository"`
	Module     string `json:"module"`
	Before     int    `json:"before"`
	After      int    `json:"after"`
}
type PolicyImpactResult struct {
	Candidate                  string
	NewlyFailing, NewlyPassing []PolicyChange
	Unchanged                  int
}
type PolicyDependencies struct {
	Select     func(context.Context, Selection) ([]deps.Repository, error)
	Abs        func(string) (string, error)
	Stat       func(string) (os.FileInfo, error)
	WriteFile  func(string, []byte, os.FileMode) error
	LoadConfig func(string) (policy.RepoConfig, error)
	Load       func(string) (policy.Policy, error)
	Scan       func(string) (policy.Module, error)
	Fetch      func(string) (string, error)
	Check      func(policy.Policy, policy.Module, string) (policy.Result, error)
	Explain    func(policy.Policy, string, string, string) (policy.Explanation, error)
	Describe   func(policy.Policy, string, string, string, bool) (policy.Effective, error)
}
type PolicyService struct {
	deps  PolicyDependencies
	usage func(string) error
}

func NewPolicy(deps PolicyDependencies, usage func(string) error) *PolicyService {
	return &PolicyService{deps: deps, usage: usage}
}
func DefaultPolicyDependencies(diagnostics io.Writer) PolicyDependencies {
	return PolicyDependencies{Select: New(DefaultDependencies(diagnostics)).Select, Abs: filepath.Abs, Stat: os.Stat, WriteFile: os.WriteFile, LoadConfig: policy.LoadRepoConfig, Load: policy.Load, Scan: policy.ScanModule, Fetch: fetchPolicy, Check: policy.Check, Explain: policy.Explain, Describe: policy.Describe}
}

type resolved struct {
	moduleDir string
	module    policy.Module
	config    policy.RepoConfig
	loaded    policy.Policy
}

// declaredType is the type the repository named, if any.
func (r resolved) declaredType() string { return r.config.Type }

func (s *PolicyService) resolvePolicy(projectsRoot, dir, policyFlag string) (resolved, error) {
	absolute, err := s.deps.Abs(dir)
	if err != nil {
		return resolved{}, s.usage(err.Error())
	}
	moduleDir, err := findModuleDir(absolute)
	if err != nil {
		return resolved{}, s.usage(err.Error())
	}
	config, err := s.deps.LoadConfig(moduleDir)
	if err != nil {
		return resolved{}, s.usage(err.Error())
	}
	reference := policyFlag
	if reference == "" {
		reference = config.Policy
	}
	if reference == "" {
		return resolved{}, s.usage(fmt.Sprintf(
			"no policy selected: create %s in %s with a \"policy:\" line, or pass --policy",
			policy.ConfigFileName, moduleDir))
	}
	source, err := policy.ParseSource(reference)
	if err != nil {
		return resolved{}, s.usage(err.Error())
	}
	path := ""
	switch source.Kind {
	case policy.SourceURL:
		path, err = s.deps.Fetch(source.URL)
	default:
		path, err = source.Locate(moduleDir, policySearchRoots(projectsRoot))
	}
	if err != nil {
		return resolved{}, s.usage(err.Error())
	}
	loaded, err := s.deps.Load(path)
	if err != nil {
		return resolved{}, s.usage(err.Error())
	}
	loaded.Source = reference
	module, err := s.deps.Scan(moduleDir)
	if err != nil {
		return resolved{}, s.usage(err.Error())
	}
	return resolved{moduleDir: moduleDir, module: module, config: config, loaded: loaded}, nil
}
func policySearchRoots(projectsRoot string) []string {
	if projectsRoot == "" {
		return nil
	}
	return []string{projectsRoot}
}
func findModuleDir(dir string) (string, error) {
	current := dir
	for {
		if _, err := os.Stat(filepath.Join(current, "go.mod")); err == nil {
			return current, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("no go.mod found at or above %s", dir)
		}
		current = parent
	}
}
func fetchPolicy(url string) (string, error) {
	return fetchPolicyInjected(url, nil)
}
func fetchPolicyInjected(url string, inj *filewrite.Injector) (string, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	response, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("fetch policy %s: %w", url, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch policy %s: %s", url, response.Status)
	}
	file, err := filewrite.CreateTemp("", "wb-policy-*.yaml", inj)
	if err != nil {
		return "", err
	}
	name := file.Name()
	defer func() { _ = filewrite.Close(file, name, inj) }()
	if _, err := io.Copy(filewrite.Writer(file, name, inj), io.LimitReader(response.Body, 1<<20)); err != nil {
		return "", err
	}
	return name, nil
}
