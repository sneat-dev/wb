package sessiontransport

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"gopkg.in/yaml.v3"
)

// OverrideConfig is the `session` section of `wb.yaml`
// ([wbconfig.DefaultPath] by default) that REQ:explicit-transport-override
// reads. An equivalent command flag carries the identical [Kind] value
// directly into [ResolveOverride] without going through this file at all —
// Task 2 ships the validation both routes share, not a CLI flag itself (no
// production call site is rewired yet).
type OverrideConfig struct {
	Transport Kind `yaml:"transport"`
}

// overrideConfigFile tolerates every other top-level `wb.yaml` section
// (session_move, remote, recipes, ...), matching internal/sessionmove's
// LoadConfig convention: only `session` is decoded, everything else is
// ignored rather than rejected.
type overrideConfigFile struct {
	Session *OverrideConfig `yaml:"session"`
}

// LoadOverride reads the optional `session.transport` override from
// configPath. An absent file, or a file with no `session.transport` key, is
// not an error: it reports ("", false, nil) so a caller falls through to
// automatic selection (Task 5), distinguishing "no override configured"
// from "an override that failed to validate" (that is [ResolveOverride]'s
// job, once a caller has a candidate [Kind] in hand).
func LoadOverride(configPath string) (Kind, bool, error) {
	raw, err := os.ReadFile(configPath)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read config %s: %w", configPath, err)
	}
	var file overrideConfigFile
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return "", false, fmt.Errorf("parse config %s: %w", configPath, err)
	}
	if file.Session == nil || file.Session.Transport == "" {
		return "", false, nil
	}
	return file.Session.Transport, true, nil
}

// PrerequisiteCheck reports whether kind's runtime prerequisite (a tmux
// binary on PATH, a reachable herdr socket) is usable right now. It returns
// a descriptive error rather than a bool so [ResolveOverride]'s refusal is
// actionable, per REQ:explicit-transport-override. A check that has nothing
// to say about kind (a herdr check asked about tmux) MUST return nil, not
// an unrelated error.
type PrerequisiteCheck func(kind Kind) error

// ComposeChecks combines several [PrerequisiteCheck] values into one that
// fails closed on the first check that fails. Production wiring (Task 4's
// herdr check, Task 10's distinct failure modes) combines checks this way
// so neither later task needs to reinvent composition. A nil entry is
// skipped.
func ComposeChecks(checks ...PrerequisiteCheck) PrerequisiteCheck {
	return func(kind Kind) error {
		for _, check := range checks {
			if check == nil {
				continue
			}
			if err := check(kind); err != nil {
				return err
			}
		}
		return nil
	}
}

// LookPath matches exec.LookPath's signature. Production code passes
// exec.LookPath (the default when nil is given to [CheckTmuxBinary]); tests
// pass a fake so no test result depends on the host's actual PATH.
type LookPath func(file string) (string, error)

// CheckTmuxBinary is a [PrerequisiteCheck] for [KindTmux]: it fails closed,
// naming the missing binary, when tmux cannot be resolved on PATH. It
// reports nil for every other [Kind]. A nil lookPath defaults to
// exec.LookPath.
func CheckTmuxBinary(lookPath LookPath) PrerequisiteCheck {
	return func(kind Kind) error {
		if kind != KindTmux {
			return nil
		}
		resolve := lookPath
		if resolve == nil {
			resolve = exec.LookPath
		}
		if _, err := resolve("tmux"); err != nil {
			return fmt.Errorf("%w: tmux binary not found on PATH: %w", ErrOverridePrerequisiteUnavailable, err)
		}
		return nil
	}
}

// ErrOverrideInvalid means an explicit override named a value that is not
// one of the transports WB actually ships (REQ:explicit-transport-override).
var ErrOverrideInvalid = errors.New("sessiontransport: explicit transport override does not name a shipped transport")

// ErrOverridePrerequisiteUnavailable means an explicit override named a
// shipped transport whose runtime prerequisite is not reachable right now.
// REQ:explicit-transport-override requires WB to refuse on this error,
// never to silently fall back to automatic selection.
var ErrOverridePrerequisiteUnavailable = errors.New("sessiontransport: explicit transport override's prerequisite is unavailable")

// ResolveOverride validates an explicit transport override (from `wb.yaml`'s
// `session.transport`, or an equivalent command flag) against the
// transports WB ships, and against whatever check reports about its
// runtime prerequisite. It fails closed on both: an unshipped [Kind], or a
// shipped Kind whose prerequisite check fails, is refused outright, and
// the zero Kind is returned alongside the error so a caller cannot
// mistakenly treat a failed resolution as having chosen some other
// transport (AC:explicit-override-fails-closed's "does not silently fall
// back to herdr or none"). check may be nil when the caller has no
// prerequisite to verify.
func ResolveOverride(requested Kind, check PrerequisiteCheck) (Kind, error) {
	if !requested.Valid() {
		return "", fmt.Errorf("%w: %q (must be one of %v)", ErrOverrideInvalid, string(requested), Kinds)
	}
	if check != nil {
		if err := check(requested); err != nil {
			return "", fmt.Errorf("session.transport %q is explicitly configured but not usable: %w", requested, err)
		}
	}
	return requested, nil
}
