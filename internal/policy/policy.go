package policy

import (
	"fmt"
	"strings"
)

// Scope names the body of code a rule applies to. Test files legitimately
// reach for dependencies production code must not — an in-memory or emulator
// database driver being the usual case — so the two are separate rule sets
// declared centrally, rather than an exception filed per repository.
const (
	ScopeSource = "source"
	ScopeTests  = "tests"
	// ScopeMain is a composition root — a file in package main. Wiring a
	// concrete database driver or bot framework is what a composition root is
	// for, and forbidding it there would either flag every daemon in the fleet
	// or force the wiring somewhere worse.
	ScopeMain = "main"
)

// Scopes lists every scope in a stable order.
func Scopes() []string { return []string{ScopeSource, ScopeTests, ScopeMain} }

// GroupStdlib is assigned to imports of the Go standard library. The standard
// library is never an architecture boundary, so it is always permitted and
// cannot be named in an allow list.
const GroupStdlib = "stdlib"

// GroupUnclassified is assigned to an import no declared group matches. Rules
// are allow lists, so an unclassified import is denied: a policy that gains a
// new kind of dependency fails closed instead of quietly permitting it. A
// policy that wants a catch-all declares one, with the pattern "...".
const GroupUnclassified = "unclassified"

// Mode says whether a rule family blocks or merely reports. It is declared in
// the central policy and cannot be set by a repository, so a new rule can be
// rolled out fleet-wide without any repository being able to opt itself out.
type Mode string

const (
	// ModeEnforce fails the check.
	ModeEnforce Mode = "enforce"
	// ModeReport prints and counts findings without affecting the exit code.
	ModeReport Mode = "report"
)

// Policy is a complete, compiled rule set. It carries no knowledge of any
// particular fleet: every name in it comes from the policy document.
type Policy struct {
	// Source records where the document was loaded from, so `show` can tell a
	// repository what it is actually being held to.
	Source string

	Groups []Group
	Types  []RepoType
	Layers Layers

	Expectations []Expectation
}

// Group classifies import paths. Order is significant and first match wins,
// so a narrow group must be declared above a broad one.
type Group struct {
	Name     string
	Patterns []Pattern
}

// RepoType is a kind of repository. Detect patterns are matched against the
// module path, which is why most repositories need no configuration at all.
type RepoType struct {
	Name   string
	Detect []Pattern
	Scopes map[string]Scope
}

// Scope is the allow list for one body of code. There is deliberately no deny
// list: anything not allowed is forbidden, which leaves nothing to widen.
type Scope struct {
	Allow []string
}

// Allows reports whether this scope permits the named group.
func (s Scope) Allows(group string) bool {
	if group == GroupStdlib {
		return true
	}
	for _, allowed := range s.Allow {
		if allowed == group {
			return true
		}
	}
	return false
}

// Layers describes permitted direction between packages inside one repository.
type Layers struct {
	Mode Mode
	// UnknownRole says what to do with a package whose name matches no role.
	// "ignore" is the sane default while a fleet still has packages that
	// predate the convention.
	UnknownRole string
	Roles       []RoleRule
	// Order runs from the outermost layer to the innermost. A package may
	// import its own layer and any layer below it, never above.
	Order [][]string
	// Forbid names individual role edges that are refused even though the
	// layer order permits them. The depth rule alone cannot express "delivery
	// must go through the facade", because api → dal does travel downward;
	// stating such edges explicitly keeps the exception visible in the policy
	// rather than hidden in the tool.
	Forbid []ForbidEdge
}

// ForbidEdge is one explicitly refused role-to-role import.
type ForbidEdge struct {
	From   string
	To     string
	Reason string
}

// RoleRule maps package directory names onto a role such as "facade".
type RoleRule struct {
	Role     string
	Patterns []Pattern
}

// Expectation is one assertion a policy makes about itself, exercised by
// `wb deps policy test`. Classification is the part of a policy that breaks
// quietly, so a policy is expected to carry examples of its own intent.
type Expectation struct {
	Import string
	Module string
	Group  string
	Type   string
}

// Type returns the named repository type.
func (p Policy) Type(name string) (RepoType, bool) {
	for _, candidate := range p.Types {
		if candidate.Name == name {
			return candidate, true
		}
	}
	return RepoType{}, false
}

// TypeNames lists declared type names in declaration order.
func (p Policy) TypeNames() []string {
	names := make([]string, 0, len(p.Types))
	for _, candidate := range p.Types {
		names = append(names, candidate.Name)
	}
	return names
}

// HasGroup reports whether a group of that name is declared.
func (p Policy) HasGroup(name string) bool {
	for _, group := range p.Groups {
		if group.Name == name {
			return true
		}
	}
	return false
}

// GroupNames lists declared group names in declaration order.
func (p Policy) GroupNames() []string {
	names := make([]string, 0, len(p.Groups))
	for _, group := range p.Groups {
		names = append(names, group.Name)
	}
	return names
}

// layerIndex returns the depth of a role, counting from 0 at the outermost
// layer. A role absent from the order has no depth.
func (l Layers) layerIndex(role string) (int, bool) {
	for depth, layer := range l.Order {
		for _, candidate := range layer {
			if candidate == role {
				return depth, true
			}
		}
	}
	return 0, false
}

// describeOrder renders the layer order the way the policy declares it.
func (l Layers) describeOrder() string {
	rendered := make([]string, 0, len(l.Order))
	for _, layer := range l.Order {
		rendered = append(rendered, strings.Join(layer, "/"))
	}
	return strings.Join(rendered, " → ")
}

// ParseMode validates a mode string.
func ParseMode(raw string) (Mode, error) {
	switch Mode(raw) {
	case ModeEnforce:
		return ModeEnforce, nil
	case ModeReport:
		return ModeReport, nil
	case "":
		return ModeEnforce, nil
	default:
		return "", fmt.Errorf("unknown mode %q: expected %q or %q", raw, ModeEnforce, ModeReport)
	}
}
