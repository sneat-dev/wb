package worktrees

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
)

// ClaimPullRequestBinding is the durable task -> pull-request fact `wb pr
// create` records once a pull request exists for a worktree's active Work Log
// claim. Nothing recorded at claim time could carry it: the claim is written
// when the worktree is created, before any pull request exists, and staying
// immutable is what lets a triage tool trust it. This binding is therefore a
// sidecar beside the claim, never a field inside it, so the daemon can later
// resolve a pull request back to its task, its claim, and the session that
// owns it, without polling GitHub or trusting a caller's own report.
type ClaimPullRequestBinding struct {
	Repository  string    `json:"repository"`
	PullRequest int       `json:"pull_request,omitempty"`
	URL         string    `json:"url"`
	RecordedAt  time.Time `json:"recorded_at"`
}

// pullRequestBindingSuffix names the sidecar file beside one claim's own
// "<claim-id>.json". It cannot collide with a claim ID: claim IDs are
// validated by validClaimID and never contain a dot followed by this word.
const pullRequestBindingSuffix = ".pull_request.json"

// RecordClaimPullRequestBinding durably records that the active Work Log
// claim for worktree opened or adopted a pull request. It returns the task
// and claim ID the binding was recorded against, so a caller can report
// exactly what was bound. The write is best-effort at the call site: a caller
// that cannot record it still has an opened pull request, and should report
// the write failure rather than lose the pull request over it.
func RecordClaimPullRequestBinding(projectsRoot, worktree string, binding ClaimPullRequestBinding) (task, claimID string, err error) {
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		return "", "", err
	}
	claim, _, claimPath, err := activeWorkLogClaim(home, worktree)
	if err != nil {
		return "", "", fmt.Errorf("resolve active work-log claim for %s: %w", worktree, err)
	}
	if binding.RecordedAt.IsZero() {
		binding.RecordedAt = time.Now().UTC()
	} else {
		binding.RecordedAt = binding.RecordedAt.UTC()
	}
	encoded, err := json.MarshalIndent(binding, "", "  ")
	if err != nil {
		return "", "", fmt.Errorf("encode task-to-pull-request binding: %w", err)
	}
	sidecar := strings.TrimSuffix(claimPath, ".json") + pullRequestBindingSuffix
	if err := os.WriteFile(sidecar, append(encoded, '\n'), 0o600); err != nil {
		return "", "", fmt.Errorf("record task-to-pull-request binding: %w", err)
	}
	return claim.Task, claim.ClaimID, nil
}
