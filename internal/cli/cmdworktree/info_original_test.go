package cmdworktree

import (
	"bytes"
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/worktreecollab"
	"github.com/sneat-dev/wb/internal/worktreerun"
	"strings"
	"testing"
)

// These are the original format and writer clauses of TestCollaborationInfoBoundaries.
// Native authority failures from that compound test belong to worktreerun.
func TestOriginalCollaborationInfoAdapterBoundaries(t *testing.T) {
	t.Parallel()
	inspect := func(context.Context, worktreerun.InfoRequest) (worktreerun.InfoDocument, error) {
		return worktreerun.InfoDocument{}, nil
	}
	runtime := shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: "projects"} }}
	command := NewInfo(runtime, inspect)
	command.SetArgs([]string{"worktree", "--format", "yaml"})
	command.SetErr(&bytes.Buffer{})
	command.SilenceErrors = true
	command.SilenceUsage = true
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "format") {
		t.Fatalf("invalid info format = %v", err)
	}
	command = NewInfo(runtime, inspect)
	command.SetOut(collaborationWriterError{errors.New("output unavailable")})
	command.SetErr(&bytes.Buffer{})
	command.SetArgs([]string{"worktree"})
	command.SilenceUsage, command.SilenceErrors = true, true
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "output unavailable") {
		t.Fatalf("text output failure = %v", err)
	}
}

func TestOriginalCollaborationInfoFormatter(t *testing.T) {
	t.Parallel()
	if formatted := formatCollaborationInfo(worktreecollab.View{Checkout: worktreecollab.Checkout{ID: "test-checkout"}, Owner: "peer", OwnerStatus: "live", Joined: []worktreecollab.Participant{{SessionID: "peer", Live: true}}}); !strings.Contains(formatted, "Current owner: peer") || !strings.Contains(formatted, "Joined session: peer") {
		t.Fatalf("format = %q", formatted)
	}
}
