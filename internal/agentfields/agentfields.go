// Package agentfields is the one set of rules for the strings of an agent that
// leaves a machine or comes from another: the patterns, closed sets and bounds
// the cockpit's export decoder enforces, shared by the publisher that builds the
// optional `agents` of a remote snapshot, the hub's snapshot model that validates
// it, and the fleet mapping that reads it back. Everything here is fail-closed: a
// value that does not match is blanked (an optional field) or drops the agent (an
// identifying one), so a path, an environment value or prose in a field never
// travels or renders.
package agentfields

import (
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxTextBytes bounds a free-text field (the export decoder's maxFieldBytes).
const MaxTextBytes = 256

// The two kinds of agent.
const (
	KindSession = "session"
	KindRun     = "run"
)

// The patterns. A token is a run or session identifier or a runtime name; a
// model is a token that may also hold the characters a model name uses, and
// never starts with a separator (so not an absolute path); a task is a task name
// as WB creates it (letters, digits, dots, underscores, dashes and slashes, not
// starting with a separator); a repository is owner/name.
var (
	Token      = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)
	Model      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+\[\]-]{0,63}$`)
	TaskName   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,255}$`)
	Repository = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}/[A-Za-z0-9._-]{1,100}$`)
)

// Activities are the values of an agent's activity.
var Activities = []string{"working", "blocked", "idle", "done", "unknown"}

var states = map[string][]string{
	KindSession: {"live", "parked"},
	KindRun:     {"running", "completed", "failed", "timeout", "abandoned"},
}

// UnsafeRune reports whether character is a control or format character or a
// line or paragraph separator, which no published text may hold.
func UnsafeRune(character rune) bool {
	return unicode.IsControl(character) || unicode.In(character, unicode.Cf, unicode.Zl, unicode.Zp)
}

// UnsafeText reports whether character may not appear in a name: UnsafeRune, the
// replacement character, a private-use character, a space other than the ASCII
// one, and the characters that draw as blanks.
func UnsafeText(character rune) bool {
	switch character {
	case utf8.RuneError, 0x2800, 0x115F, 0x1160, 0x3164, 0xFFA0:
		return true
	}
	return UnsafeRune(character) || unicode.Is(unicode.Co, character) || (unicode.Is(unicode.Zs, character) && character != ' ')
}

// IsText is the export decoder's text rule: bounded and with no unsafe character.
func IsText(value string) bool {
	return len(value) <= MaxTextBytes && !strings.ContainsFunc(value, UnsafeText)
}

// IsTaskName is the stricter rule the publisher and the hub apply to a task.
func IsTaskName(value string) bool { return TaskName.MatchString(value) }

// Agent is the strings of one agent.
type Agent struct {
	Kind, SessionID, RunID, Runtime, Model, State, Activity, Task, Repository string
}

// Clean applies the rules to an agent. It reports false when an identifying field
// fails (the kind, the state, the session or run identifier), and then the agent
// must be dropped; otherwise every other failing field is blanked. task is the
// rule for the task field: IsTaskName on the publish, hub and read sides. An empty value always passes: the field is simply absent.
func Clean(agent Agent, task func(string) bool) (Agent, bool) {
	if !slices.Contains(states[agent.Kind], agent.State) {
		return Agent{}, false
	}
	if (agent.SessionID != "" && !Token.MatchString(agent.SessionID)) || (agent.RunID != "" && !Token.MatchString(agent.RunID)) {
		return Agent{}, false
	}
	if agent.Runtime != "" && !Token.MatchString(agent.Runtime) {
		agent.Runtime = ""
	}
	if agent.Model != "" && !Model.MatchString(agent.Model) {
		agent.Model = ""
	}
	if agent.Activity != "" && !slices.Contains(Activities, agent.Activity) {
		agent.Activity = ""
	}
	if agent.Task != "" && !task(agent.Task) {
		agent.Task = ""
	}
	if agent.Repository != "" && !Repository.MatchString(agent.Repository) {
		agent.Repository = ""
	}
	return agent, true
}

// Valid reports whether every field of agent already passes (nothing would be
// blanked and the agent would be kept): the hub's check, which refuses rather
// than repairs.
func Valid(agent Agent, task func(string) bool) bool {
	cleaned, ok := Clean(agent, task)
	return ok && cleaned == agent
}
