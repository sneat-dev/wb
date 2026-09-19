package worktrees

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/unixcompat"
	"github.com/sneat-dev/wb/internal/wbhome"
)

// ActiveClaimSummary is the small, non-sensitive part of an unsealed Work Log
// claim needed to detect concurrent agent work. It deliberately omits the
// checkout path, original prompt, commit IDs, and model/provider details.
type ActiveClaimSummary struct {
	Task        string
	TaskSummary string
	Repository  string
	Branch      string
	Owner       string
	WBSessionID string
	RecordedAt  time.Time
}

// ListActiveClaimSummaries reads immutable Work Log claims without inspecting
// Git repositories. Path enumeration keeps this read-only command compatible
// with agent sandboxes; symlinked parent entries are rejected, then claims and
// terminals are read through no-follow directory descriptors.
//
// It reads every home wbhome.Resolve reports for projectsRoot, not only the
// write home: a claim recorded under a retired legacy home (for example
// $HOME/.wb before the projects-root layout existed) still names a live task,
// and a caller deciding whether a repository is safe to touch must see it.
func ListActiveClaimSummaries(projectsRoot, filter string) ([]ActiveClaimSummary, error) {
	resolution, err := wbhome.Resolve(projectsRoot)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(resolution.Read))
	result := make([]ActiveClaimSummary, 0)
	for _, layout := range resolution.Read {
		home := filepath.Clean(layout.Home)
		if home == "" || seen[home] {
			continue
		}
		seen[home] = true
		claims, err := listActiveClaimSummariesInHome(home, filter)
		if err != nil {
			return nil, err
		}
		result = append(result, claims...)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Repository != result[j].Repository {
			return result[i].Repository < result[j].Repository
		}
		if result[i].Task != result[j].Task {
			return result[i].Task < result[j].Task
		}
		return result[i].Branch < result[j].Branch
	})
	return result, nil
}

// listActiveClaimSummariesInHome is ListActiveClaimSummaries for exactly one
// resolved home.
func listActiveClaimSummariesInHome(home, filter string) ([]ActiveClaimSummary, error) {
	result := make([]ActiveClaimSummary, 0)
	err := walkActiveWorkLogClaims(home, func(_ *os.File, _ string, claim workLogClaim) {
		if !filterMatches(filter, claim.Repository) {
			return
		}
		result = append(result, ActiveClaimSummary{
			Task: claim.Task, TaskSummary: claim.TaskSummary, Repository: claim.Repository,
			Branch: claim.Branch, Owner: claim.AgentID, WBSessionID: claim.WBSessionID,
			RecordedAt: claim.RecordedAt,
		})
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// walkActiveWorkLogClaims is the one directory walk behind every reader of
// the worklogs/<task>/runs/<run>/claims layout that only cares about a claim
// still in its active (non-terminal) Work Log life —
// ListActiveClaimSummaries and ListRegisteredPullRequestBindings, as of this
// writing. Path enumeration, symlink rejection, static claim validation, and
// the terminal-skip rule live here exactly once, so both callers' tests
// exercise the same error branches instead of two independent, drifting
// copies of them (herdr-session-transport Plan Task 6 review, M4).
//
// visit is called once per active claim, with the open, no-follow "claims"
// directory descriptor the claim itself was read from — open so visit can
// read a sidecar file beside it, as ListRegisteredPullRequestBindings does —
// the claim's ID, and the decoded claim. visit performs no filesystem I/O
// that can itself fail in a way the walk should abort for; a caller that
// needs to skip a claim (an unmatched filter, a missing sidecar) simply
// returns without recording it. A top-level filesystem error — reading a
// directory, opening a private child — still aborts the whole walk and is
// returned to ListActiveClaimSummaries/ListRegisteredPullRequestBindings'
// own caller, exactly as it did before this extraction.
func walkActiveWorkLogClaims(home string, visit func(claims *os.File, claimID string, claim workLogClaim)) error {
	worklogsRoot := filepath.Join(home, "worklogs")
	efforts, err := os.ReadDir(worklogsRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	worklogs, err := openDirectDirectoryNoFollow(worklogsRoot)
	if err != nil {
		return err
	}
	defer func() { _ = worklogs.Close() }()
	for _, effort := range efforts {
		if !safeActiveDirectoryEntry(effort) || !validSafeSegment(effort.Name()) {
			continue
		}
		effortDir, openErr := openPrivateChild(worklogs, effort.Name(), false)
		if openErr != nil {
			continue
		}
		runsDir, openErr := openPrivateChild(effortDir, "runs", false)
		_ = effortDir.Close()
		if errors.Is(openErr, os.ErrNotExist) {
			continue
		}
		if openErr != nil {
			return openErr
		}
		runsRoot := filepath.Join(worklogsRoot, effort.Name(), "runs")
		runs, readErr := os.ReadDir(runsRoot)
		if errors.Is(readErr, os.ErrNotExist) {
			_ = runsDir.Close()
			continue
		}
		if readErr != nil {
			_ = runsDir.Close()
			return readErr
		}
		for _, run := range runs {
			if !safeActiveDirectoryEntry(run) || !validSafeSegment(run.Name()) {
				continue
			}
			runRoot := filepath.Join(runsRoot, run.Name())
			runDir, runErr := openPrivateChild(runsDir, run.Name(), false)
			if runErr != nil {
				continue
			}
			claimsRoot := filepath.Join(runRoot, "claims")
			claims, claimsErr := openPrivateChild(runDir, "claims", false)
			if errors.Is(claimsErr, os.ErrNotExist) {
				_ = runDir.Close()
				continue
			}
			if claimsErr != nil {
				_ = runDir.Close()
				_ = runsDir.Close()
				return claimsErr
			}
			claimEntries, entriesErr := os.ReadDir(claimsRoot)
			if entriesErr != nil {
				_ = claims.Close()
				_ = runDir.Close()
				_ = runsDir.Close()
				return entriesErr
			}
			terminals, terminalErr := openPrivateChild(runDir, "terminals", false)
			if terminalErr != nil && !errors.Is(terminalErr, os.ErrNotExist) {
				_ = claims.Close()
				_ = runDir.Close()
				_ = runsDir.Close()
				return terminalErr
			}
			for _, entry := range claimEntries {
				claimID := strings.TrimSuffix(entry.Name(), ".json")
				if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || entry.Name() != claimID+".json" || !validClaimID(claimID) {
					continue
				}
				var claim workLogClaim
				if readJSONAt(claims, entry.Name(), &claim) != nil || validateStaticWorkLogClaim(claim, effort.Name(), run.Name()) != nil || claim.Task != effort.Name() {
					continue
				}
				if terminals != nil {
					var terminal workLogTerminalRecord
					if terminalReadErr := readJSONAt(terminals, entry.Name(), &terminal); terminalReadErr == nil {
						continue
					} else if !errors.Is(terminalReadErr, os.ErrNotExist) {
						continue
					}
				}
				visit(claims, claimID, claim)
			}
			if terminals != nil {
				_ = terminals.Close()
			}
			_ = claims.Close()
			_ = runDir.Close()
		}
		_ = runsDir.Close()
	}
	return nil
}

func safeActiveDirectoryEntry(entry os.DirEntry) bool {
	if entry.Type()&os.ModeSymlink != 0 {
		return false
	}
	info, err := entry.Info()
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0
}

func openDirectDirectoryNoFollow(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "wb-active-private-directory")
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("wrap active Work Log directory")
	}
	return file, nil
}
