package worktrees

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/hooks"
)

// CheckpointRefPrefix is the dedicated namespace WB pushes remote checkpoints
// to. It is never refs/heads/* and never refs/tags/*: it must not be listed
// as a branch, must never be picked up for review, and -- confirmed against
// this repository's own GitHub Actions workflows, all of which filter push
// triggers to branches or tags -- a push confined to this namespace does not
// trigger CI.
//
// A ref under this prefix is a durability aid, never a landing receipt: it
// proves a commit reached the remote, never that it merged anywhere. The
// founder's Definition of Done is unchanged by this feature -- work is landed
// only when it is merged and pushed to its target branch on origin.
//
// This is a re-export of hooks.CheckpointRefPrefix, not an independent
// definition: the pre-push tiering classifier in internal/hooks and the
// checkpoint push/fetch here must never drift onto two different prefixes.
const CheckpointRefPrefix = hooks.CheckpointRefPrefix

// NotALandingReceiptNotice is the fixed disclaimer every remote-checkpoint
// push and fetch result carries, in both text and JSON output, so a
// checkpoint can never be mistaken for a landing receipt by a reader who only
// looked at one field.
const NotALandingReceiptNotice = "NOT a landing receipt: work is landed only when merged and pushed to its target branch on origin."

// remoteCheckpointTimeout bounds the one network call a checkpoint push or
// fetch makes. It is a var, not a const, so a test can shorten it.
var remoteCheckpointTimeout = 90 * time.Second

// CheckpointRemoteRef returns the refs/wb/checkpoints/<task> ref name for
// task, validating task is a safe, single Git ref-path segment.
func CheckpointRemoteRef(task string) (string, error) {
	task = strings.TrimSpace(task)
	if task == "" {
		return "", fmt.Errorf("a remote checkpoint requires a non-empty task identity")
	}
	if !validSafeSegment(task) {
		return "", fmt.Errorf("task %q is not a safe Git ref segment", task)
	}
	return CheckpointRefPrefix + task, nil
}

// RemoteCheckpointResult is the outcome of one checkpoint push. Notice always
// carries NotALandingReceiptNotice verbatim: this struct is serialized to
// JSON for --format json callers, and the disclaimer must survive that path
// exactly as it does in text output.
type RemoteCheckpointResult struct {
	Ref    string `json:"ref"`
	SHA    string `json:"sha"`
	Pushed bool   `json:"pushed"`
	Notice string `json:"notice"`
}

// PushRemoteCheckpointOptions configures PushRemoteCheckpoint.
type PushRemoteCheckpointOptions struct {
	// Root is the repository worktree to push from.
	Root string
	// Task names the checkpoint ref: refs/wb/checkpoints/<Task>.
	Task string
	// HeadSHA is the exact commit to publish. The caller resolves it before
	// calling so the pushed object is never ambiguous.
	HeadSHA string
}

// PushRemoteCheckpoint force-updates origin's refs/wb/checkpoints/<task> to
// point at the exact given commit. The force is expressed as a single
// "+<sha>:<ref>" refspec, so it is scoped to exactly that one destination ref
// -- this command never names refs/heads/* and never passes a wildcard or
// --force, so it cannot touch any branch.
func PushRemoteCheckpoint(ctx context.Context, options PushRemoteCheckpointOptions) (RemoteCheckpointResult, error) {
	ref, err := CheckpointRemoteRef(options.Task)
	if err != nil {
		return RemoteCheckpointResult{}, err
	}
	if !isGitObjectID(options.HeadSHA) {
		return RemoteCheckpointResult{}, fmt.Errorf("a remote checkpoint requires one resolved exact commit, got %q", options.HeadSHA)
	}
	pushCtx, cancel := context.WithTimeout(ctx, remoteCheckpointTimeout)
	defer cancel()
	refspec := "+" + options.HeadSHA + ":" + ref
	if _, err := git(pushCtx, options.Root, "push", "--porcelain", "--", "origin", refspec); err != nil {
		return RemoteCheckpointResult{}, fmt.Errorf("push remote checkpoint %s: %w", ref, err)
	}
	return RemoteCheckpointResult{Ref: ref, SHA: options.HeadSHA, Pushed: true, Notice: NotALandingReceiptNotice}, nil
}

// FetchRemoteCheckpointOptions configures FetchRemoteCheckpoint.
type FetchRemoteCheckpointOptions struct {
	// Root is the repository to fetch into. It need not have any active WB
	// Work Log claim: retrieving another machine's checkpoint is exactly the
	// cross-machine case a claim would not yet exist for.
	Root string
	// Task names the checkpoint ref: refs/wb/checkpoints/<Task>.
	Task string
}

// RemoteCheckpointFetchResult is the outcome of one checkpoint fetch.
type RemoteCheckpointFetchResult struct {
	Ref      string `json:"ref"`
	SHA      string `json:"sha"`
	LocalRef string `json:"local_ref"`
	Notice   string `json:"notice"`
}

// FetchRemoteCheckpoint retrieves origin's refs/wb/checkpoints/<task> into the
// SAME-NAMED local ref (never refs/heads/*), so it is inspectable with plain
// Git evidence commands but never appears as a local branch, is never
// checked out implicitly, and is never mistaken for one. Turning it into a
// branch or a worktree is left to the caller, who decides that deliberately.
func FetchRemoteCheckpoint(ctx context.Context, options FetchRemoteCheckpointOptions) (RemoteCheckpointFetchResult, error) {
	ref, err := CheckpointRemoteRef(options.Task)
	if err != nil {
		return RemoteCheckpointFetchResult{}, err
	}
	fetchCtx, cancel := context.WithTimeout(ctx, remoteCheckpointTimeout)
	defer cancel()
	if _, err := git(fetchCtx, options.Root, "fetch", "--no-tags", "--", "origin", ref+":"+ref); err != nil {
		return RemoteCheckpointFetchResult{}, fmt.Errorf("fetch remote checkpoint %s: %w", ref, err)
	}
	sha, err := git(ctx, options.Root, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil || !isGitObjectID(sha) {
		return RemoteCheckpointFetchResult{}, fmt.Errorf("resolve fetched checkpoint %s: %w", ref, err)
	}
	return RemoteCheckpointFetchResult{Ref: ref, SHA: sha, LocalRef: ref, Notice: NotALandingReceiptNotice}, nil
}
