package integration

import (
	"bytes"
	"github.com/sneat-dev/wb/internal/cli/cmdbranch"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/worktrees"
	"strings"
	"testing"
)

func TestBranchQuarantineRequiresRepoBranchAndReason(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	command := cmdbranch.New(shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: root} }}, cmdbranch.Dependencies{Quarantine: worktrees.BranchQuarantine})
	var stderr bytes.Buffer
	command.SetErr(&stderr)
	command.SetOut(&stderr)
	command.SetArgs([]string{"quarantine"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "single quarantine requires --repo, --branch, and --reason") {
		t.Fatalf("quarantine with no selector = %v, want a usage refusal", err)
	}
}
