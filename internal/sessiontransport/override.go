package sessiontransport

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"

	"gopkg.in/yaml.v3"

	"github.com/sneat-dev/wb/internal/herdr"
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

// LoadOverride reads the optional `session.transport` override from
// configPath. An absent file, or a file with no `session` section at all,
// is not an error: it reports ("", false, nil) so a caller falls through to
// automatic selection (Task 5), distinguishing "no override configured"
// from "an override that failed to validate."
//
// The `session` section itself is decoded strictly, with
// yaml.Decoder.KnownFields(true) — following internal/lifecyclehooks/config.go's
// pattern of parsing the whole file generically first, then re-decoding
// only the one section of interest under strict field checking — so a typo
// such as `transprot` fails closed with an error rather than silently
// decoding as "no override configured". Every other top-level section
// (`session_move`, `remote`, ...) is still tolerated untouched, because
// only the `session` node is ever extracted and re-decoded.
func LoadOverride(configPath string) (Kind, bool, error) {
	raw, err := os.ReadFile(configPath)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read config %s: %w", configPath, err)
	}
	var document yaml.Node
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return "", false, fmt.Errorf("parse config %s: %w", configPath, err)
	}
	sessionNode, found, err := mappingValue(document, "session")
	if err != nil {
		return "", false, fmt.Errorf("parse config %s: %w", configPath, err)
	}
	if !found || sessionNode.Kind == 0 || sessionNode.Tag == "!!null" {
		return "", false, nil
	}
	sub, err := yaml.Marshal(&sessionNode)
	if err != nil {
		return "", false, fmt.Errorf("parse config %s: %w", configPath, err)
	}
	strict := yaml.NewDecoder(bytes.NewReader(sub))
	strict.KnownFields(true)
	var section OverrideConfig
	if err := strict.Decode(&section); err != nil {
		return "", false, fmt.Errorf("parse config %s: session section: %w", configPath, err)
	}
	if section.Transport == "" {
		return "", false, nil
	}
	return section.Transport, true, nil
}

// mappingValue extracts the value node for key from document's top-level
// mapping, mirroring internal/lifecyclehooks/config.go's helper of the same
// shape. found is false, with no error, when the top level has no such key;
// an error means the document's top level is not a mapping at all.
func mappingValue(document yaml.Node, key string) (yaml.Node, bool, error) {
	if len(document.Content) == 0 {
		return yaml.Node{}, false, nil
	}
	root := document.Content[0]
	if root.Kind != yaml.MappingNode {
		return yaml.Node{}, false, errors.New("top level must be a mapping")
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != key {
			continue
		}
		return *root.Content[i+1], true, nil
	}
	return yaml.Node{}, false, nil
}

// PrerequisiteCheck reports whether kind's runtime prerequisite (a tmux
// binary on PATH, a resolvable herdr binary) is usable right now. It
// returns a descriptive error rather than a bool so [ResolveOverride]'s
// refusal is actionable, per REQ:explicit-transport-override. A check that
// has nothing to say about kind (a herdr check asked about tmux) MUST
// return nil, not an unrelated error.
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

// CheckHerdrBinary is a [PrerequisiteCheck] for [KindHerdr]: it fails
// closed when the herdr binary cannot be resolved via HERDR_BIN_PATH or
// PATH (internal/herdr.ResolveBinary, Task 1's landed adapter — this
// package goes through it rather than calling exec.LookPath("herdr")
// itself, matching Task 1's "every other task that needs herdr goes
// through this package" rule). It does not check herdr socket
// reachability: Task 10 owns herdr's remaining distinct failure modes
// (missing command, socket unreachable, version drift). It reports nil for
// every other Kind. A nil lookup defaults to herdr.OSLookupEnv.
func CheckHerdrBinary(lookup herdr.EnvLookup) PrerequisiteCheck {
	return func(kind Kind) error {
		if kind != KindHerdr {
			return nil
		}
		resolve := lookup
		if resolve == nil {
			resolve = herdr.OSLookupEnv
		}
		if _, err := herdr.ResolveBinary(resolve); err != nil {
			return fmt.Errorf("%w: %w", ErrOverridePrerequisiteUnavailable, err)
		}
		return nil
	}
}

// defaultPrerequisiteCheck is what [ResolveOverride] applies when its check
// argument is nil, so nil never means "skip validation" — a nil check must
// still enforce tmux's binary-on-PATH check and herdr's binary-resolution
// check, both against the real process environment. It is a package var,
// not a direct call, only so a test can substitute a fake without touching
// the real PATH or environment.
var defaultPrerequisiteCheck PrerequisiteCheck = ComposeChecks(CheckTmuxBinary(nil), CheckHerdrBinary(nil))

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
// back to herdr or none"). A nil check applies [defaultPrerequisiteCheck]
// rather than skipping validation entirely.
func ResolveOverride(requested Kind, check PrerequisiteCheck) (Kind, error) {
	if !requested.Valid() {
		return "", fmt.Errorf("%w: %q (must be one of %v)", ErrOverrideInvalid, string(requested), Kinds())
	}
	if check == nil {
		check = defaultPrerequisiteCheck
	}
	if err := check(requested); err != nil {
		return "", fmt.Errorf("session.transport %q is explicitly configured but not usable: %w", requested, err)
	}
	return requested, nil
}
