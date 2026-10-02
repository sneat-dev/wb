package worktrees

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
)

// ClosedPullRequestEvidence is the audited reason a checkout whose pull request
// was closed unmerged (a duplicate, a superseded attempt) may be discarded.
// It is not deletion authority by itself: the discard still requires a clean
// checkout, an unmoved remote branch, and the sealed Work Log every discard
// writes. What it proves is that the checkout's exact head is the closed pull
// request's exact head, so no commit of the checkout is unique to it and the
// content lives wherever the operator says the duplicate's twin landed.
type ClosedPullRequestEvidence struct {
	Repository string `json:"repository"`
	Number     int    `json:"number"`
	URL        string `json:"url"`
	HeadRef    string `json:"head_ref"`
	HeadSHA    string `json:"head_sha"`
	BaseRef    string `json:"base_ref"`
	// Reason is the operator's stated justification, recorded verbatim.
	Reason string `json:"reason"`
	// AuditPath is the immutable-name audit record written before removal.
	AuditPath string `json:"audit_path,omitempty"`
}

// closedPullRequestAudit is the record persisted for one discarded checkout.
type closedPullRequestAudit struct {
	Task        string                    `json:"task"`
	Repository  string                    `json:"repository"`
	Branch      string                    `json:"branch"`
	WorktreeDir string                    `json:"worktree_dir"`
	HeadSHA     string                    `json:"head_sha"`
	Evidence    ClosedPullRequestEvidence `json:"evidence"`
	RecordedAt  time.Time                 `json:"recorded_at"`
}

var closedPullRequestAuditNamePattern = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// verifyClosedPullRequest re-proves, against GitHub's current answer, that the
// named pull request was closed unmerged, belongs to this repository, and has
// exactly this checkout's branch name and head commit. An empty first return
// value with a non-empty rejection is an ineligible checkout, not a failure.
func verifyClosedPullRequest(
	ctx context.Context,
	resolve func(context.Context, string, string, string, string) (*githubPullRequest, string, error),
	entry ListResult,
	pointer, reason string,
) (*ClosedPullRequestEvidence, string, error) {
	pull, rejection, err := resolve(ctx, entry.WorktreeDir, entry.Repository, entry.Base, pointer)
	if err != nil || rejection != "" {
		return nil, rejection, err
	}
	if !entry.Clean {
		return nil, "--closed-pr requires a clean worktree: uncommitted bytes are not in the closed pull request", nil
	}
	if pull.Head.SHA != entry.HeadSHA {
		return nil, fmt.Sprintf("--closed-pr pull request %s#%d head %s does not equal exact checkout head %s: the checkout has commits the pull request never carried",
			entry.Repository, pull.Number, pull.Head.SHA, entry.HeadSHA), nil
	}
	if entry.Branch != "" && pull.Head.Ref != entry.Branch {
		return nil, fmt.Sprintf("--closed-pr pull request %s#%d head branch %q is not this checkout's branch %q",
			entry.Repository, pull.Number, pull.Head.Ref, entry.Branch), nil
	}
	return &ClosedPullRequestEvidence{
		Repository: entry.Repository, Number: pull.Number, URL: pull.URL,
		HeadRef: pull.Head.Ref, HeadSHA: pull.Head.SHA, BaseRef: pull.Base.Ref, Reason: reason,
	}, "", nil
}

func productionClosedPullRequestResolver(ctx context.Context, worktree, slug, base, pointer string) (*githubPullRequest, string, error) {
	return landingReceiptService().ResolveClosedPullRequest(ctx, worktree, slug, base, pointer)
}

// recordClosedPullRequestDiscard writes the audit record that outlives the
// checkout. The name carries task, repository, and exact head, so two
// discards never overwrite each other and a re-run of the same one rewrites
// identical bytes.
func recordClosedPullRequestDiscard(home, task string, result *AbortResult) error {
	if result.ClosedPullRequest == nil {
		return nil
	}
	directory := filepath.Join(home, "closed-pr-discards")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create closed pull request audit directory: %w", err)
	}
	name := closedPullRequestAuditNamePattern.ReplaceAllString(task+"-"+strings.ReplaceAll(result.Repository, "/", "-")+"-"+result.HeadSHA, "_") + ".json"
	path := filepath.Join(directory, name)
	record := closedPullRequestAudit{
		Task: task, Repository: result.Repository, Branch: result.Branch, WorktreeDir: result.WorktreeDir,
		HeadSHA: result.HeadSHA, Evidence: *result.ClosedPullRequest, RecordedAt: time.Now().UTC(),
	}
	if err := filewrite.WriteJSONAtomic(path, record, 0o600); err != nil {
		return fmt.Errorf("record closed pull request discard for %s: %w", result.Repository, err)
	}
	result.ClosedPullRequest.AuditPath = path
	return nil
}
