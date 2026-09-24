package worktrees

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

// RegisteredPullRequestBinding pairs one durably recorded task-to-pull-request
// binding with the claim identity it was recorded against. It is what
// ListRegisteredPullRequestBindings returns: the exact, and only, set of pull
// requests the daemon's watcher (herdr-session-transport, Plan Task 6) may
// evaluate.
type RegisteredPullRequestBinding struct {
	Task        string
	ClaimID     string
	Repository  string
	PullRequest int
	URL         string
	RecordedAt  time.Time
}

// ListRegisteredPullRequestBindings is the first reader of the binding
// RecordClaimPullRequestBinding writes (sneat-dev/wb#601 recorded it with no
// reader before herdr-session-transport). It returns exactly the pull
// requests with a durably recorded binding beside a still-active Work Log
// claim, across every home wbhome.Resolve reports for projectsRoot. It reads
// only local disk state RecordClaimPullRequestBinding already wrote: no
// GitHub call and no fleet-wide pull-request scan happens here.
//
// A claim that has since reached a terminal state is skipped: nothing should
// watch on behalf of a claim whose Work Log life is already over, matching
// ListActiveClaimSummaries' own terminal-skip rule.
func ListRegisteredPullRequestBindings(projectsRoot string) ([]RegisteredPullRequestBinding, error) {
	resolution, err := wbhome.Resolve(projectsRoot)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(resolution.Read))
	result := make([]RegisteredPullRequestBinding, 0)
	for _, layout := range resolution.Read {
		home := filepath.Clean(layout.Home)
		if home == "" || seen[home] {
			continue
		}
		seen[home] = true
		bindings, err := listRegisteredPullRequestBindingsInHome(home)
		if err != nil {
			return nil, err
		}
		result = append(result, bindings...)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Repository != result[j].Repository {
			return result[i].Repository < result[j].Repository
		}
		if result[i].Task != result[j].Task {
			return result[i].Task < result[j].Task
		}
		return result[i].ClaimID < result[j].ClaimID
	})
	return result, nil
}

// listRegisteredPullRequestBindingsInHome is
// ListRegisteredPullRequestBindings for exactly one resolved home. It shares
// walkActiveWorkLogClaims (internal/worktrees/active_claims.go) with
// ListActiveClaimSummaries — the same enumeration, symlink rejection, and
// terminal-skip rule — and reads each active claim's ".pull_request.json"
// sidecar instead of the claim itself, reporting nothing for a claim that has
// no such sidecar.
func listRegisteredPullRequestBindingsInHome(home string) ([]RegisteredPullRequestBinding, error) {
	result := make([]RegisteredPullRequestBinding, 0)
	err := walkActiveWorkLogClaims(home, func(claims *os.File, claimID string, claim workLogClaim) {
		var binding ClaimPullRequestBinding
		if readJSONAt(claims, claimID+pullRequestBindingSuffix, &binding) != nil {
			return
		}
		result = append(result, RegisteredPullRequestBinding{
			Task: claim.Task, ClaimID: claimID, Repository: binding.Repository,
			PullRequest: binding.PullRequest, URL: binding.URL, RecordedAt: binding.RecordedAt,
		})
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
