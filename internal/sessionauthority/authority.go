// Package sessionauthority defines the transport-neutral authority consumed by
// the shared pinned-worktree and successor-launch primitives. Protocol packages
// adapt their exact admitted aggregates to this narrow capability; a caller-
// supplied path or decoded projection is never sufficient authority.
package sessionauthority

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type ContinuationKind string

const (
	ContinuationEnvironment = "WB_SESSION_CONTINUATION_FILE"
	// ContinuationTracked is a repository-relative file authenticated by an
	// immutable Git commit, as used by the existing session-move protocol.
	ContinuationTracked ContinuationKind = "tracked"
	// ContinuationPrivate is a 0600 regular file outside the target worktree,
	// retained below the admitted aggregate directory.
	ContinuationPrivate ContinuationKind = "private"
)

// Fence is the descriptor-retaining execution authority common to admitted
// session protocols. RetainSessionDir returns a duplicate of the already-
// validated aggregate directory; implementations must not reopen an arbitrary
// caller path and treat a matching digest as equivalent authority.
type Fence interface {
	HeldForSession(expectedRoot, aggregateID, digest string) bool
	RetainSessionDir(expectedRoot, aggregateID, digest string) (*os.File, error)
}

// Launch is the immutable identity used by the one hardened tmux launcher.
// AggregateFile is authenticated inside the retained aggregate directory.
type Launch struct {
	AggregateID            string
	AggregateDigest        string
	AggregateFile          string
	SuccessorWBSessionID   string
	PredecessorWBSessionID string
	TargetMachine          string
	SourceRuntime          string
	SourceModel            string
	RequestedHarness       string
	PinnedCommit           string
	PinnedBranch           string
	ContinuationKind       ContinuationKind
	ContinuationPath       string
	ContinuationDigest     string
}

var safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
var digest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var gitObjectID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

func ValidID(value string) bool {
	return safeID.MatchString(value) && value != "." && value != ".."
}

func ValidateMemberKey(value string) error {
	if !ValidID(value) {
		return fmt.Errorf("member key must be one fixed safe ID")
	}
	return nil
}

func (launch Launch) Validate() error {
	for name, value := range map[string]string{
		"aggregate_id":              launch.AggregateID,
		"successor_wb_session_id":   launch.SuccessorWBSessionID,
		"predecessor_wb_session_id": launch.PredecessorWBSessionID,
		"target_machine":            launch.TargetMachine,
	} {
		if !ValidID(value) {
			return fmt.Errorf("%s is not one fixed safe ID", name)
		}
	}
	if !digest.MatchString(launch.AggregateDigest) {
		return fmt.Errorf("aggregate digest must be sha256:<64 lowercase hex characters>")
	}
	if !digest.MatchString(launch.ContinuationDigest) {
		return fmt.Errorf("continuation digest must be sha256:<64 lowercase hex characters>")
	}
	if !ValidID(launch.AggregateFile) || filepath.Base(launch.AggregateFile) != launch.AggregateFile {
		return fmt.Errorf("aggregate file must be one fixed safe basename")
	}
	if strings.TrimSpace(launch.SourceRuntime) == "" || strings.ContainsAny(launch.SourceRuntime, "\r\n") {
		return fmt.Errorf("source runtime is required and must be single-line")
	}
	if strings.ContainsAny(launch.SourceModel, "\r\n") || strings.ContainsAny(launch.RequestedHarness, "\r\n") {
		return fmt.Errorf("model and requested harness must be single-line")
	}
	if !gitObjectID.MatchString(launch.PinnedCommit) {
		return fmt.Errorf("pinned commit must be one full lowercase Git object ID")
	}
	if strings.TrimSpace(launch.PinnedBranch) == "" || strings.ContainsAny(launch.PinnedBranch, "\r\n") {
		return fmt.Errorf("pinned branch is required and must be single-line")
	}
	switch launch.ContinuationKind {
	case ContinuationTracked:
		clean := filepath.Clean(filepath.FromSlash(launch.ContinuationPath))
		if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("tracked continuation path must be repository-relative")
		}
	case ContinuationPrivate:
		if !filepath.IsAbs(launch.ContinuationPath) || filepath.Clean(launch.ContinuationPath) != launch.ContinuationPath {
			return fmt.Errorf("private continuation path must be clean and absolute")
		}
	default:
		return fmt.Errorf("continuation kind %q is unsupported", launch.ContinuationKind)
	}
	return nil
}
