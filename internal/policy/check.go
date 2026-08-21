package policy

import (
	"fmt"
	"sort"
	"strings"
)

// Rule names the family a finding belongs to.
const (
	// RuleImport is a dependency this kind of repository may not have.
	RuleImport = "import"
	// RuleLayer is an import travelling the wrong way inside one repository.
	RuleLayer = "layer"
	// RuleRole is a package whose name matches no declared role, reported
	// only where the policy asks for it.
	RuleRole = "role"
)

// Finding is one violation.
type Finding struct {
	Rule string
	Mode Mode

	File     string
	Line     int
	Package  string
	Scope    string
	Import   string
	Manifest bool

	// Group is set for import findings.
	Group string
	// FromRole and ToRole are set for layer findings.
	FromRole string
	ToRole   string

	Message string
	// Fix names the shape of the remedy where one can be stated honestly.
	Fix string
}

// Result is the outcome of checking one module.
type Result struct {
	Module Module
	Policy Policy
	// Type is the repository type the rules were taken from.
	Type string
	// TypeDetected records whether Type came from detection or was declared.
	TypeDetected bool

	Findings []Finding
}

// Blocking counts findings that must fail the command. Report-mode findings
// are excluded: they are visible and counted, and they do not gate.
func (r Result) Blocking() int {
	count := 0
	for _, finding := range r.Findings {
		if finding.Mode == ModeEnforce {
			count++
		}
	}
	return count
}

// Reported counts findings that are visible but do not gate.
func (r Result) Reported() int { return len(r.Findings) - r.Blocking() }

// Check applies a policy to a scanned module.
//
// declaredType overrides detection. It exists because a module path cannot
// always say what a repository is; it is not an escape hatch, since the rules
// it selects are still the central policy's.
func Check(policy Policy, module Module, declaredType string) (Result, error) {
	result := Result{Module: module, Policy: policy}
	if declaredType != "" {
		if _, ok := policy.Type(declaredType); !ok {
			return Result{}, fmt.Errorf("type %q is not declared in %s (known types: %s)",
				declaredType, policy.Source, strings.Join(policy.TypeNames(), ", "))
		}
		result.Type = declaredType
	} else {
		detected, err := policy.Detect(module.Path)
		if err != nil {
			return Result{}, err
		}
		result.Type = detected
		result.TypeDetected = true
	}
	repoType, _ := policy.Type(result.Type)

	for _, reference := range module.References {
		classification := policy.Classify(reference.Import, module.Path)
		if classification.Group == GroupStdlib {
			continue
		}
		scope := repoType.Scopes[reference.Scope]
		if scope.Allows(classification.Group) {
			continue
		}
		result.Findings = append(result.Findings, Finding{
			Rule:     RuleImport,
			Mode:     ModeEnforce,
			File:     reference.File,
			Line:     reference.Line,
			Package:  reference.Package,
			Scope:    reference.Scope,
			Import:   reference.Import,
			Manifest: reference.Manifest,
			Group:    classification.Group,
			Message: fmt.Sprintf("%s must not import %s in the %s scope",
				result.Type, describeGroup(classification.Group), reference.Scope),
			Fix: suggestFix(policy, repoType, reference.Scope, classification.Group),
		})
	}

	result.Findings = append(result.Findings, policy.layerFindings(module)...)

	sort.SliceStable(result.Findings, func(i, j int) bool {
		left, right := result.Findings[i], result.Findings[j]
		if left.Mode != right.Mode {
			return left.Mode == ModeEnforce
		}
		if left.File != right.File {
			return left.File < right.File
		}
		return left.Line < right.Line
	})
	return result, nil
}

func (p Policy) layerFindings(module Module) []Finding {
	if len(p.Layers.Order) == 0 {
		return nil
	}
	var findings []Finding
	reportedRoleless := map[string]bool{}
	for _, reference := range module.References {
		if reference.Manifest {
			continue
		}
		target, ok := internalPackage(module.Path, reference.Import)
		if !ok {
			continue
		}
		fromRole, fromKnown := p.Layers.roleOf(reference.Package)
		toRole, toKnown := p.Layers.roleOf(target)

		if p.Layers.UnknownRole == "error" {
			for pkg, known := range map[string]bool{reference.Package: fromKnown, target: toKnown} {
				if known || pkg == "" || reportedRoleless[pkg] {
					continue
				}
				reportedRoleless[pkg] = true
				findings = append(findings, Finding{
					Rule: RuleRole, Mode: p.Layers.Mode, File: reference.File, Line: reference.Line,
					Package: pkg, Scope: reference.Scope,
					Message: fmt.Sprintf("package %q matches no declared layer role, so nothing constrains its direction", pkg),
				})
			}
		}
		if !fromKnown || !toKnown || fromRole == toRole {
			continue
		}

		if reason, forbidden := p.Layers.forbidden(fromRole, toRole); forbidden {
			message := fmt.Sprintf("%s must not import %s", fromRole, toRole)
			if reason != "" {
				message += ": " + reason
			}
			findings = append(findings, Finding{
				Rule: RuleLayer, Mode: p.Layers.Mode, File: reference.File, Line: reference.Line,
				Package: reference.Package, Scope: reference.Scope, Import: reference.Import,
				FromRole: fromRole, ToRole: toRole, Message: message,
			})
			continue
		}

		fromDepth, _ := p.Layers.layerIndex(fromRole)
		toDepth, hasTo := p.Layers.layerIndex(toRole)
		if !hasTo || toDepth >= fromDepth {
			continue
		}
		findings = append(findings, Finding{
			Rule: RuleLayer, Mode: p.Layers.Mode, File: reference.File, Line: reference.Line,
			Package: reference.Package, Scope: reference.Scope, Import: reference.Import,
			FromRole: fromRole, ToRole: toRole,
			Message: fmt.Sprintf("%s must not import %s: imports travel down the layer order, never up (%s)",
				fromRole, toRole, p.Layers.describeOrder()),
		})
	}
	return findings
}

// roleOf resolves a package directory to a role. Role patterns are matched
// against the first path segment, so nested packages inherit the role of the
// top-level directory that groups them.
func (l Layers) roleOf(packageDir string) (string, bool) {
	if packageDir == "" {
		return "", false
	}
	first := packageDir
	if index := strings.IndexByte(packageDir, '/'); index >= 0 {
		first = packageDir[:index]
	}
	for _, rule := range l.Roles {
		for _, pattern := range rule.Patterns {
			if pattern.Match(first, "") {
				return rule.Role, true
			}
		}
	}
	return "", false
}

func (l Layers) forbidden(from, to string) (string, bool) {
	for _, edge := range l.Forbid {
		if edge.From == from && edge.To == to {
			return edge.Reason, true
		}
	}
	return "", false
}

// internalPackage returns the package directory an import refers to when the
// import is inside the module being scanned.
func internalPackage(modulePath, importPath string) (string, bool) {
	if importPath == modulePath {
		return "", true
	}
	prefix := modulePath + "/"
	if !strings.HasPrefix(importPath, prefix) {
		return "", false
	}
	return strings.TrimPrefix(importPath, prefix), true
}

func describeGroup(group string) string {
	if group == GroupUnclassified {
		return "a dependency no group in the policy classifies"
	}
	return group
}

// suggestFix names the nearest permitted group, which for the common case —
// an implementation import that should have been a contract import — is the
// whole remedy.
func suggestFix(policy Policy, repoType RepoType, scope, group string) string {
	if group == GroupUnclassified {
		return "classify it in the policy, or remove the dependency"
	}
	allowed := repoType.Scopes[scope].Allow
	if len(allowed) == 0 {
		return ""
	}
	for _, candidate := range allowed {
		if !strings.Contains(candidate, "contract") {
			continue
		}
		for _, declared := range policy.Groups {
			if declared.Name != candidate || len(declared.Patterns) == 0 {
				continue
			}
			return fmt.Sprintf("import %s instead", declared.Patterns[0])
		}
	}
	return "permitted here: " + strings.Join(allowed, ", ")
}
