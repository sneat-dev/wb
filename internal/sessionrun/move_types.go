package sessionrun

import (
	"github.com/sneat-dev/wb/internal/sessionlaunch"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"io"
)

// MoveRequest describes a fresh move or a retry of an immutable handoff.
// Input is only consumed for HandoverFile "-" at the existing input stage.
type MoveRequest struct {
	ProjectsRoot    string
	Worktree        string
	Target          string
	Via             string
	ConfigPath      string
	Harness         string
	Model           string
	Summary         string
	Validation      string
	Remaining       string
	HandoverFile    string
	Input           io.Reader
	OverrideSecrets []string
	ResumeID        string
}

// MoveResult is the existing public move response. Resume selects the existing
// completed-handoff text without adding a field to the public JSON response.
type MoveResult struct {
	Phase        string                        `json:"phase"`
	Courier      sessionmove.Courier           `json:"courier"`
	SourceActive bool                          `json:"source_active"`
	Request      sessionmove.Request           `json:"request"`
	Digest       sessionmove.Digest            `json:"request_digest"`
	Successor    *sessionlaunch.Result         `json:"successor,omitempty"`
	Receipt      *sessionmove.Receipt          `json:"receipt,omitempty"`
	Address      *sessionmove.SuccessorAddress `json:"successor_address,omitempty"`
	Resume       bool                          `json:"-"`
}
