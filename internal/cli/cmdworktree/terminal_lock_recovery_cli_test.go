package cmdworktree

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestRetireTerminalLockModeRequiresOneBoundReceipt(t *testing.T) {
	t.Parallel()
	root := "private-projects"
	for _, args := range [][]string{
		{"operation", "--recover-terminal-lock"},
		{"operation", "other", "--recover-terminal-lock", "--merge-receipt=receipt.json"},
		{"operation", "--recover-terminal-lock", "--merge-receipt=receipt.json", "--remote"},
	} {
		deps := retireDeps()
		deps.RecoverTerminalLock = func(context.Context, worktrees.TerminalLockRecoveryOptions) (worktrees.TerminalLockRecoveryOutcome, error) {
			t.Fatal("invalid mode reached recovery")
			return worktrees.TerminalLockRecoveryOutcome{}, nil
		}
		if err := retirementExecute(NewRetire(retirementRuntime(&root), deps), args, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "--recover-terminal-lock requires") {
			t.Fatalf("args %q: error = %v", args, err)
		}
	}
}

func TestRetireTerminalLockModePreservesDryRunApplyAndOutput(t *testing.T) {
	t.Parallel()
	root := "private-projects"
	sentinel := errors.New("recovery refused")
	for _, test := range []struct {
		name, format string
		apply, fail  bool
	}{
		{"dry run text", "text", false, false},
		{"applied text", "text", true, false},
		{"dry run JSON", "json", false, false},
		{"failed recovery", "text", false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			deps := retireDeps()
			deps.RecoverTerminalLock = func(_ context.Context, options worktrees.TerminalLockRecoveryOptions) (worktrees.TerminalLockRecoveryOutcome, error) {
				if options.ProjectsRoot != root || options.Task != "operation" || options.ReceiptPath != "receipt.json" || options.Apply != test.apply {
					t.Fatalf("recovery options = %#v", options)
				}
				if test.fail {
					return worktrees.TerminalLockRecoveryOutcome{}, sentinel
				}
				return worktrees.TerminalLockRecoveryOutcome{
					LockPath: "private.lock", LandingSHA: strings.Repeat("a", 40), Target: "main", TargetSHA: strings.Repeat("b", 40),
					AuditPath: "audit.json", Applied: test.apply,
				}, nil
			}
			args := []string{"operation", "--recover-terminal-lock", "--merge-receipt=receipt.json", "--format=" + test.format}
			if test.apply {
				args = append(args, "--apply")
			}
			var out bytes.Buffer
			err := retirementExecute(NewRetire(retirementRuntime(&root), deps), args, &out, io.Discard)
			if test.fail {
				if !errors.Is(err, sentinel) || out.Len() != 0 {
					t.Fatalf("failed recovery returned %q, %v", out.String(), err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := "private.lock"
			if test.format == "text" {
				want = map[bool]string{false: "would retire inert operation lock", true: "retired inert operation lock"}[test.apply]
			}
			if !strings.Contains(out.String(), want) {
				t.Fatalf("output %q lacks %q", out.String(), want)
			}
		})
	}
}
