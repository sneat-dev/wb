package worktrees

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// CanonicalFreshnessStatus is the outcome of a point-of-read comparison of a
// canonical checkout with its freshly fetched remote target.
const (
	CanonicalFreshnessCurrent    = "current"
	CanonicalFreshnessAhead      = "ahead"
	CanonicalFreshnessStale      = "stale"
	CanonicalFreshnessDiverged   = "diverged"
	CanonicalFreshnessOffline    = "offline"
	CanonicalFreshnessFetchError = "fetch_failed"
	CanonicalFreshnessMissing    = "target_missing"
	CanonicalFreshnessDrifted    = "target_drift"
)

// CanonicalFreshness is an exact, read-only receipt for a canonical clone's
// relation to origin/<target>. The target ref is fetched before the counts are
// measured; the working tree, index, and local branch are never changed.
type CanonicalFreshness struct {
	Target      string    `json:"target"`
	RemoteRef   string    `json:"remote_ref"`
	LocalSHA    string    `json:"local_sha,omitempty"`
	RemoteSHA   string    `json:"remote_sha,omitempty"`
	Ahead       int       `json:"ahead,omitempty"`
	Behind      int       `json:"behind,omitempty"`
	Status      string    `json:"status"`
	Fetched     bool      `json:"fetched"`
	TargetDrift bool      `json:"target_drift,omitempty"`
	Error       string    `json:"error,omitempty"`
	ObservedAt  time.Time `json:"observed_at"`
}

// inspectCanonicalFreshness performs the discriminating query required before
// diagnosing from a canonical clone: fetch the configured target, compare the
// exact local HEAD to origin/<target>, and probe the hosted ref once more so a
// target that moved during the read is not presented as a stable receipt.
func inspectCanonicalFreshness(ctx context.Context, root, target string) *CanonicalFreshness {
	return inspectCanonicalFreshnessWith(ctx, root, target, git)
}

type canonicalFreshnessGit func(context.Context, string, ...string) (string, error)

func inspectCanonicalFreshnessWith(ctx context.Context, root, target string, run canonicalFreshnessGit) *CanonicalFreshness {
	result := &CanonicalFreshness{
		Target:     target,
		RemoteRef:  "origin/" + target,
		Status:     CanonicalFreshnessFetchError,
		ObservedAt: time.Now().UTC(),
	}
	local, localErr := run(ctx, root, "rev-parse", "HEAD")
	result.LocalSHA = strings.TrimSpace(local)
	if localErr != nil {
		result.Error = localErr.Error()
		return result
	}

	refspec := "+refs/heads/" + target + ":refs/remotes/origin/" + target
	_, fetchErr := run(ctx, root, "fetch", "--no-tags", "origin", refspec)
	if fetchErr != nil {
		probe, probeErr := run(ctx, root, "ls-remote", "origin", "refs/heads/"+target)
		if probeErr != nil {
			result.Status = CanonicalFreshnessOffline
			result.Error = fmt.Sprintf("fetch origin/%s failed: %v; remote probe failed: %v", target, fetchErr, probeErr)
		} else if fields := strings.Fields(probe); len(fields) != 2 || fields[0] == "" {
			result.Status = CanonicalFreshnessMissing
			result.Error = fmt.Sprintf("origin/%s is not advertised by the remote; fetch failed: %v", target, fetchErr)
		} else {
			result.Error = fmt.Sprintf("fetch origin/%s failed: %v", target, fetchErr)
		}
		return result
	}
	result.Fetched = true

	remote, err := run(ctx, root, "rev-parse", result.RemoteRef)
	if err != nil {
		result.Status = CanonicalFreshnessMissing
		result.Error = fmt.Sprintf("fetched target %s is unavailable locally: %v", result.RemoteRef, err)
		return result
	}
	result.RemoteSHA = strings.TrimSpace(remote)

	counts, err := run(ctx, root, "rev-list", "--left-right", "--count", "HEAD..."+result.RemoteRef)
	if err != nil {
		result.Status = CanonicalFreshnessFetchError
		result.Error = fmt.Sprintf("compare HEAD to %s failed: %v", result.RemoteRef, err)
		return result
	}
	fields := strings.Fields(counts)
	if len(fields) != 2 {
		result.Status = CanonicalFreshnessFetchError
		result.Error = fmt.Sprintf("compare HEAD to %s returned %q", result.RemoteRef, strings.TrimSpace(counts))
		return result
	}
	result.Ahead, err = strconv.Atoi(fields[0])
	if err != nil {
		result.Status = CanonicalFreshnessFetchError
		result.Error = fmt.Sprintf("parse ahead count %q: %v", fields[0], err)
		return result
	}
	result.Behind, err = strconv.Atoi(fields[1])
	if err != nil {
		result.Status = CanonicalFreshnessFetchError
		result.Error = fmt.Sprintf("parse behind count %q: %v", fields[1], err)
		return result
	}

	// Fetch gives us a coherent local snapshot. This second remote observation
	// detects the important race where the hosted target advances immediately
	// after that snapshot was fetched.
	advertised, probeErr := run(ctx, root, "ls-remote", "origin", "refs/heads/"+target)
	if probeErr != nil {
		result.Status = CanonicalFreshnessFetchError
		result.Error = fmt.Sprintf("verify origin/%s after fetch failed: %v", target, probeErr)
		return result
	}
	advertisedFields := strings.Fields(advertised)
	if len(advertisedFields) != 2 || advertisedFields[0] != result.RemoteSHA {
		result.Status = CanonicalFreshnessDrifted
		result.TargetDrift = true
		if len(advertisedFields) == 0 {
			result.Error = fmt.Sprintf("origin/%s disappeared after fetch", target)
		} else {
			result.Error = fmt.Sprintf("origin/%s moved from %s to %s during freshness check", target, result.RemoteSHA, advertisedFields[0])
		}
		return result
	}

	switch {
	case result.Ahead == 0 && result.Behind == 0:
		result.Status = CanonicalFreshnessCurrent
	case result.Ahead > 0 && result.Behind == 0:
		result.Status = CanonicalFreshnessAhead
	case result.Ahead == 0 && result.Behind > 0:
		result.Status = CanonicalFreshnessStale
	default:
		result.Status = CanonicalFreshnessDiverged
	}
	return result
}
