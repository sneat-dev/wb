package policy

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type document struct {
	Groups []struct {
		Name  string   `yaml:"name"`
		Match []string `yaml:"match"`
	} `yaml:"groups"`
	Types []struct {
		Name   string   `yaml:"name"`
		Detect []string `yaml:"detect"`
		Scopes map[string]struct {
			Allow []string `yaml:"allow"`
		} `yaml:"scopes"`
	} `yaml:"types"`
	Layers struct {
		Mode        string              `yaml:"mode"`
		UnknownRole string              `yaml:"unknown-role"`
		Roles       map[string][]string `yaml:"roles"`
		Order       [][]string          `yaml:"order"`
		Forbid      []struct {
			From   string `yaml:"from"`
			To     string `yaml:"to"`
			Reason string `yaml:"reason"`
		} `yaml:"forbid"`
	} `yaml:"layers"`
	Expect []struct {
		Import string `yaml:"import"`
		Module string `yaml:"module"`
		Group  string `yaml:"group"`
		Type   string `yaml:"type"`
	} `yaml:"expect"`
}

// Load reads and compiles a policy document.
//
// It fails on anything that would make the policy unusable or silently wrong:
// a malformed pattern, a duplicate name, an allow list naming a group that
// does not exist, a type with nothing to detect it by. Softer quality
// findings — a pattern another pattern already shadows, a role nobody placed
// in the layer order — are reported by Validate instead, so that they can be
// surfaced without refusing to run.
func Load(path string) (Policy, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return Policy{}, fmt.Errorf("read policy: %w", err)
	}
	return Parse(contents, path)
}

// Parse compiles a policy from bytes. source is used only for diagnostics.
func Parse(contents []byte, source string) (Policy, error) {
	var raw document
	decoder := yaml.NewDecoder(strings.NewReader(string(contents)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&raw); err != nil {
		return Policy{}, fmt.Errorf("parse %s: %w", source, err)
	}

	compiled := Policy{Source: source}
	seen := map[string]bool{}
	for _, group := range raw.Groups {
		if group.Name == "" {
			return Policy{}, fmt.Errorf("%s: a group has no name", source)
		}
		if group.Name == GroupStdlib || group.Name == GroupUnclassified {
			return Policy{}, fmt.Errorf("%s: %q is a reserved group name", source, group.Name)
		}
		if seen[group.Name] {
			return Policy{}, fmt.Errorf("%s: group %q is declared twice", source, group.Name)
		}
		seen[group.Name] = true
		if len(group.Match) == 0 {
			return Policy{}, fmt.Errorf("%s: group %q has no match patterns", source, group.Name)
		}
		patterns, err := compilePatterns(group.Match)
		if err != nil {
			return Policy{}, fmt.Errorf("%s: group %q: %w", source, group.Name, err)
		}
		compiled.Groups = append(compiled.Groups, Group{Name: group.Name, Patterns: patterns})
	}
	if len(compiled.Groups) == 0 {
		return Policy{}, fmt.Errorf("%s: no groups declared", source)
	}

	typeSeen := map[string]bool{}
	for _, declared := range raw.Types {
		name := declared.Name
		if name == "" {
			return Policy{}, fmt.Errorf("%s: a type has no name", source)
		}
		if typeSeen[name] {
			return Policy{}, fmt.Errorf("%s: type %q is declared twice", source, name)
		}
		typeSeen[name] = true
		if len(declared.Detect) == 0 {
			return Policy{}, fmt.Errorf("%s: type %q has no detect patterns, so no repository can ever be classified as it", source, name)
		}
		detect, err := compilePatterns(declared.Detect)
		if err != nil {
			return Policy{}, fmt.Errorf("%s: type %q: %w", source, name, err)
		}
		scopes := map[string]Scope{}
		for scopeName, scope := range declared.Scopes {
			if scopeName != ScopeSource && scopeName != ScopeTests && scopeName != ScopeMain {
				return Policy{}, fmt.Errorf("%s: type %q declares unknown scope %q: expected one of %s",
					source, name, scopeName, strings.Join(Scopes(), ", "))
			}
			for _, allowed := range scope.Allow {
				if allowed == GroupStdlib {
					return Policy{}, fmt.Errorf("%s: type %q scope %q allows %q, which is always permitted and must not be listed", source, name, scopeName, GroupStdlib)
				}
				if !compiled.HasGroup(allowed) {
					return Policy{}, fmt.Errorf("%s: type %q scope %q allows group %q, which is not declared%s", source, name, scopeName, allowed, suggest(allowed, compiled.GroupNames()))
				}
			}
			scopes[scopeName] = Scope{Allow: append([]string(nil), scope.Allow...)}
		}
		if _, ok := scopes[ScopeSource]; !ok {
			return Policy{}, fmt.Errorf("%s: type %q has no %q scope", source, name, ScopeSource)
		}
		// A type that says nothing about tests is held to its source rules.
		// Silence must never be the more permissive reading.
		for _, inherited := range []string{ScopeTests, ScopeMain} {
			if _, ok := scopes[inherited]; !ok {
				scopes[inherited] = scopes[ScopeSource]
			}
		}
		compiled.Types = append(compiled.Types, RepoType{Name: name, Detect: detect, Scopes: scopes})
	}
	if len(compiled.Types) == 0 {
		return Policy{}, fmt.Errorf("%s: no types declared", source)
	}

	mode, err := ParseMode(raw.Layers.Mode)
	if err != nil {
		return Policy{}, fmt.Errorf("%s: layers: %w", source, err)
	}
	unknownRole := raw.Layers.UnknownRole
	switch unknownRole {
	case "", "ignore":
		unknownRole = "ignore"
	case "error":
	default:
		return Policy{}, fmt.Errorf("%s: layers: unknown-role must be \"ignore\" or \"error\", not %q", source, unknownRole)
	}
	layers := Layers{Mode: mode, UnknownRole: unknownRole, Order: raw.Layers.Order}
	for _, role := range sortedKeys(raw.Layers.Roles) {
		patterns, err := compilePatterns(raw.Layers.Roles[role])
		if err != nil {
			return Policy{}, fmt.Errorf("%s: layer role %q: %w", source, role, err)
		}
		layers.Roles = append(layers.Roles, RoleRule{Role: role, Patterns: patterns})
	}
	for _, layer := range layers.Order {
		for _, role := range layer {
			if !hasRole(layers.Roles, role) {
				return Policy{}, fmt.Errorf("%s: layer order names role %q, which has no patterns", source, role)
			}
		}
	}
	for index, edge := range raw.Layers.Forbid {
		if edge.From == "" || edge.To == "" {
			return Policy{}, fmt.Errorf("%s: layers.forbid[%d] must name both from and to", source, index)
		}
		for _, role := range []string{edge.From, edge.To} {
			if !hasRole(layers.Roles, role) {
				return Policy{}, fmt.Errorf("%s: layers.forbid[%d] names role %q, which has no patterns", source, index, role)
			}
		}
		layers.Forbid = append(layers.Forbid, ForbidEdge(edge))
	}
	compiled.Layers = layers

	for index, expectation := range raw.Expect {
		if (expectation.Import == "") == (expectation.Module == "") {
			return Policy{}, fmt.Errorf("%s: expect[%d] must name exactly one of import or module", source, index)
		}
		if expectation.Import != "" && expectation.Group == "" {
			return Policy{}, fmt.Errorf("%s: expect[%d] on an import must state the expected group", source, index)
		}
		if expectation.Module != "" && expectation.Type == "" {
			return Policy{}, fmt.Errorf("%s: expect[%d] on a module must state the expected type", source, index)
		}
		compiled.Expectations = append(compiled.Expectations, Expectation(expectation))
	}
	return compiled, nil
}

// Diagnostic is a non-fatal finding about a policy document itself.
type Diagnostic struct {
	Message string
}

// Validate reports quality problems in a policy that do not stop it running
// but do stop it meaning what its author thinks it means.
//
// The one that matters most is an unreachable group pattern. Classification is
// first-match-wins, so a broad pattern placed above a narrow one silently
// takes every path the narrow one was written for, and nothing anywhere errors.
func Validate(policy Policy) []Diagnostic {
	var diagnostics []Diagnostic

	for index, group := range policy.Groups {
		for _, pattern := range group.Patterns {
			for earlierIndex := 0; earlierIndex < index; earlierIndex++ {
				earlier := policy.Groups[earlierIndex]
				for _, candidate := range earlier.Patterns {
					if !candidate.Covers(pattern) {
						continue
					}
					diagnostics = append(diagnostics, Diagnostic{Message: fmt.Sprintf(
						"group %q pattern %q is unreachable: every path it matches is already claimed by group %q pattern %q, declared above it",
						group.Name, pattern, earlier.Name, candidate)})
				}
			}
		}
	}

	for index, repoType := range policy.Types {
		for _, pattern := range repoType.Detect {
			for earlierIndex := 0; earlierIndex < index; earlierIndex++ {
				earlier := policy.Types[earlierIndex]
				for _, candidate := range earlier.Detect {
					if !candidate.Covers(pattern) {
						continue
					}
					diagnostics = append(diagnostics, Diagnostic{Message: fmt.Sprintf(
						"type %q detect pattern %q is unreachable: every module it matches is already claimed by type %q pattern %q, declared above it",
						repoType.Name, pattern, earlier.Name, candidate)})
				}
			}
		}
	}

	for _, role := range policy.Layers.Roles {
		if _, ok := policy.Layers.layerIndex(role.Role); !ok {
			diagnostics = append(diagnostics, Diagnostic{Message: fmt.Sprintf(
				"layer role %q has patterns but is never placed in the layer order, so it constrains nothing", role.Role)})
		}
	}

	// There is deliberately no "this group is never allowed" diagnostic. A
	// group nobody allows is how a policy forbids something — naming
	// extension-implementation or bot-framework and then permitting neither is
	// the entire point — and Load already rejects an allow list that names a
	// group which does not exist, which is the typo this would otherwise catch.
	return diagnostics
}

func compilePatterns(raw []string) ([]Pattern, error) {
	patterns := make([]Pattern, 0, len(raw))
	for _, candidate := range raw {
		compiled, err := CompilePattern(candidate)
		if err != nil {
			return nil, err
		}
		patterns = append(patterns, compiled)
	}
	return patterns, nil
}

func hasRole(roles []RoleRule, name string) bool {
	for _, role := range roles {
		if role.Role == name {
			return true
		}
	}
	return false
}

func sortedKeys[V any](in map[string]V) []string {
	keys := make([]string, 0, len(in))
	for key := range in {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// suggest offers a near-miss name, because the realistic failure here is a
// typo in a group name rather than a genuinely missing group.
func suggest(given string, candidates []string) string {
	for _, candidate := range candidates {
		if strings.EqualFold(candidate, given) || editDistanceWithin(candidate, given, 2) {
			return fmt.Sprintf(" (did you mean %q?)", candidate)
		}
	}
	return ""
}

func editDistanceWithin(a, b string, limit int) bool {
	if a == b {
		return true
	}
	if len(a) > len(b)+limit || len(b) > len(a)+limit {
		return false
	}
	previous := make([]int, len(b)+1)
	current := make([]int, len(b)+1)
	for j := range previous {
		previous[j] = j
	}
	for i := 1; i <= len(a); i++ {
		current[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			current[j] = min(min(current[j-1]+1, previous[j]+1), previous[j-1]+cost)
		}
		copy(previous, current)
	}
	return previous[len(b)] <= limit
}
