package cmdworktree

import (
	"context"
	"fmt"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/hooks"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
	"io"
)

// NewGuard constructs the checkout policy inspection command.
func NewGuard(environment shared.Runtime, inspect func(context.Context, string, worktrees.GuardOptions) (worktrees.GuardResult, error), isTerminal func(any) bool) *cobra.Command {
	var base, format, admission string
	var quiet, published, prePushStdin bool
	command := &cobra.Command{
		Use:   "guard [repository-path]",
		Short: "Reject unsafe canonical clones and misplaced worktrees",
		Long: `Validate the checkout policy used by agents and WB Git hooks.

A canonical clone is valid when it is clean, whatever branch it has checked
out: a different checked-out branch is a normal state, and no WB verb needs the
clone on the base branch. Uncommitted work in it is what the guard protects. A
linked checkout is valid only when it uses a non-base branch and either lives
in the central store at
<root>/.worktrees/<task>/<host>/<owner>/<repository> (or the legacy
<root>/.worktrees/<task>/<owner>/<repository>), lives in
<canonical>/.worktrees/<task> under the repository-local store mode, or carries
an active Work
Log claim
from 'wb worktree adopt --apply' — adoption's whole point is never relocating
the checkout, so that one case is resolved from its claim instead of its path.

--admission additionally requires a managed worktree to carry its own record: a
manifest and at least one recorded instruction. It binds on the worktree's
location and never tries to tell an agent from a human by environment markers,
which can be absent exactly when they matter. Use warn while a fleet adopts the
journal and enforce once it has.

When the guarded checkout is canonical, this command also fetches the selected
origin target and includes an exact freshness receipt. A stale, ahead, or
diverged clone is reported as a warning with left/right commit counts. If the
remote cannot be reached, or the target moves while it is being checked, the
warning says so explicitly; the checkout is never fast-forwarded or otherwise
changed by guard.

--published is the post-push verification Git itself cannot give you. Git runs
no post-push hook, and it runs pre-push only when it has refs to update — so
the most dangerous push is the one that does nothing. A detached HEAD, or a
branch other than the one HEAD is on, makes "git push" print "Everything
up-to-date" while the commit sits on the remote nowhere at all.

--published fetches this worktree's own branch and compares it to HEAD, exiting
1 with the exact remedy unless HEAD is provably at origin/<branch>. Anything WB
could not observe — offline, a failed fetch, a ref that moved mid-check — is
unverified, never assumed published. Run it after every push.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			if err := shared.RequireOutputFormat(admission, "off", "warn", "enforce"); err != nil {
				return fmt.Errorf("unsupported admission mode %q; use off, warn, or enforce", admission)
			}
			path := "."
			if len(args) == 1 {
				path = args[0]
			}
			if prePushStdin && pushOnlyDeletesRemoteRefs(command.InOrStdin(), isTerminal) {
				// Nothing is sent from this checkout, so nothing about it can
				// make the push unsafe (sneat-dev/wb#824).
				return nil
			}
			result, err := inspect(command.Context(), path, worktrees.GuardOptions{
				ProjectsRoot:     environment.Flags().ProjectsRoot,
				Base:             base,
				Admission:        worktrees.AdmissionMode(admission),
				CheckFreshness:   true,
				CheckPublication: published,
			})
			if err != nil {
				return err
			}
			return renderGuard(environment, command, result, format, quiet, published)
		},
	}
	command.Flags().StringVar(&base, "base", "", "protected canonical base branch (default: origin/HEAD, then main)")
	command.Flags().BoolVar(&quiet, "quiet", false, "write nothing when the checkout is valid")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().StringVar(&admission, "admission", "off", "require a worktree record before committing: off, warn, or enforce (managed hooks default to enforce)")
	command.Flags().BoolVar(&published, "published", false, "verify after a push that HEAD is exactly origin/<this worktree's branch>; exit 1 with the remedy otherwise")
	command.Flags().BoolVar(&prePushStdin, "pre-push-stdin", false, "run as a pre-push guard: read Git's pushed-ref list on stdin and pass without inspecting the checkout when the push only deletes remote refs")
	_ = command.Flags().MarkHidden("pre-push-stdin")
	return command
}

func pushOnlyDeletesRemoteRefs(stdin io.Reader, isTerminal func(any) bool) bool {
	if isTerminal(stdin) {
		return false
	}
	only, err := hooks.OnlyRemoteRefDeletions(stdin)
	return err == nil && only
}
