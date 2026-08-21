package policy

import (
	"fmt"
	"sort"
	"strings"
)

// Explanation is the full account of one classification decision, which is
// what someone needs when a verdict surprises them.
type Explanation struct {
	Import         string
	Module         string
	RepoType       string
	TypeDetected   bool
	Classification Classification

	// Scopes reports the verdict in each scope, because the same import can
	// be legitimate in a test and forbidden in production code.
	Scopes []ScopeVerdict
}

// ScopeVerdict is the outcome for one scope.
type ScopeVerdict struct {
	Scope   string
	Allowed bool
	Allow   []string
}

// Explain answers "why is this import allowed or forbidden here".
func Explain(policy Policy, modulePath, declaredType, importPath string) (Explanation, error) {
	explanation := Explanation{Import: importPath, Module: modulePath}
	if declaredType != "" {
		if _, ok := policy.Type(declaredType); !ok {
			return Explanation{}, fmt.Errorf("type %q is not declared in %s", declaredType, policy.Source)
		}
		explanation.RepoType = declaredType
	} else {
		detected, err := policy.Detect(modulePath)
		if err != nil {
			return Explanation{}, err
		}
		explanation.RepoType = detected
		explanation.TypeDetected = true
	}
	explanation.Classification = policy.Classify(importPath, modulePath)
	repoType, _ := policy.Type(explanation.RepoType)
	for _, scope := range Scopes() {
		declared := repoType.Scopes[scope]
		explanation.Scopes = append(explanation.Scopes, ScopeVerdict{
			Scope:   scope,
			Allowed: declared.Allows(explanation.Classification.Group),
			Allow:   declared.Allow,
		})
	}
	return explanation, nil
}

// ExpectationResult is the outcome of one policy self-assertion.
type ExpectationResult struct {
	Expectation Expectation
	Subject     string
	Want        string
	Got         string
	Passed      bool
	Err         string
}

// RunExpectations exercises the assertions a policy makes about itself.
//
// Classification is the part of a policy that breaks quietly — reorder two
// group patterns and every verdict downstream changes with nothing to show
// for it — so a policy is expected to carry examples of what it means.
func RunExpectations(policy Policy) []ExpectationResult {
	results := make([]ExpectationResult, 0, len(policy.Expectations))
	for _, expectation := range policy.Expectations {
		result := ExpectationResult{Expectation: expectation}
		switch {
		case expectation.Import != "":
			result.Subject = expectation.Import
			result.Want = expectation.Group
			result.Got = policy.Classify(expectation.Import, "").Group
		default:
			result.Subject = expectation.Module
			result.Want = expectation.Type
			detected, err := policy.Detect(expectation.Module)
			if err != nil {
				result.Err = err.Error()
			}
			result.Got = detected
		}
		result.Passed = result.Err == "" && result.Got == result.Want
		results = append(results, result)
	}
	return results
}

// Effective is the resolved rule set one repository is actually held to.
type Effective struct {
	PolicySource string
	Module       string
	RepoType     string
	TypeDetected bool
	ConfigPath   string
	Strict       bool

	Scopes      []ScopeVerdict
	LayerMode   Mode
	LayerOrder  string
	LayerForbid []ForbidEdge
}

// Describe resolves what a repository is bound by. Central policy is opaque
// unless a repository can print its own rules back.
func Describe(policy Policy, modulePath, declaredType, configPath string, strict bool) (Effective, error) {
	effective := Effective{
		PolicySource: policy.Source,
		Module:       modulePath,
		ConfigPath:   configPath,
		Strict:       strict,
		LayerMode:    policy.Layers.Mode,
		LayerOrder:   policy.Layers.describeOrder(),
		LayerForbid:  policy.Layers.Forbid,
	}
	if strict {
		effective.LayerMode = ModeEnforce
	}
	if declaredType != "" {
		if _, ok := policy.Type(declaredType); !ok {
			return Effective{}, fmt.Errorf("type %q is not declared in %s", declaredType, policy.Source)
		}
		effective.RepoType = declaredType
	} else {
		detected, err := policy.Detect(modulePath)
		if err != nil {
			return Effective{}, err
		}
		effective.RepoType = detected
		effective.TypeDetected = true
	}
	repoType, _ := policy.Type(effective.RepoType)
	for _, scope := range Scopes() {
		allow := append([]string(nil), repoType.Scopes[scope].Allow...)
		sort.Strings(allow)
		effective.Scopes = append(effective.Scopes, ScopeVerdict{Scope: scope, Allowed: true, Allow: allow})
	}
	return effective, nil
}

// ApplyStrict promotes report-mode findings to blocking ones. A repository may
// hold itself to more than the fleet requires; it may never hold itself to less.
func (r *Result) ApplyStrict() {
	for index := range r.Findings {
		r.Findings[index].Mode = ModeEnforce
	}
}

// Summary counts findings by rule for a one-line report.
func (r Result) Summary() string {
	counts := map[string]int{}
	for _, finding := range r.Findings {
		counts[finding.Rule]++
	}
	if len(counts) == 0 {
		return "no violations"
	}
	parts := make([]string, 0, len(counts))
	for _, rule := range sortedKeys(counts) {
		parts = append(parts, fmt.Sprintf("%d %s", counts[rule], rule))
	}
	return strings.Join(parts, ", ")
}
