package orchestrate

import (
	"context"
	"github.com/sneat-dev/wb/internal/githubchecks"
	"github.com/sneat-dev/wb/internal/locallink"
	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/worktrees"
	"path/filepath"
	"strings"
)

// preflightLandingCleanup refuses a landing whose tidy-up would fail, before
// the merge makes the landing irreversible.
//
// linksOnly is set when the caller passed --keep: the dirty-worktree check is
// about retiring a checkout and does not apply, while the live-link check is
// about what is being landed and always does.
func preflightLandingCleanup(ctx context.Context, options PullRequestLandOptions, view githubchecks.PullRequestView, number string, linksOnly bool) *landRefusal {
	if view.Head.Repo == nil || strings.TrimSpace(view.Head.Repo.FullName) == "" || strings.TrimSpace(view.Head.Ref) == "" {
		return &landRefusal{
			code:    "cleanup-unverifiable",
			reason:  "the pull request head repository and branch could not be established, so its source worktrees cannot be checked",
			command: "inspect the pull request head, then retry wb pr land " + options.Repository + "#" + number,
		}
	}
	sourceRepository := view.Head.Repo.FullName
	listed, err := worktrees.ListWithDiagnostics(ctx, worktrees.ListOptions{
		ProjectsRoot: options.ProjectsRoot,
		Base:         view.Base.Ref,
		Filter:       sourceRepository,
	})
	if err != nil {
		// The inventory is unreadable, which is not the same as clean. Refuse
		// rather than merge into an unknown tidy-up.
		return &landRefusal{
			code:    "cleanup-unverifiable",
			reason:  "the worktree inventory could not be read, so this landing's cleanup cannot be pre-flighted: " + err.Error(),
			command: "inspect and repair the source worktree inventory, then retry wb pr land " + options.Repository + "#" + number,
		}
	}
	// Use a validated inventory path for diagnostics too. API repository casing
	// must never be used to invent a differently spelled physical clone path.
	sourceCanonical := ""
	for _, entry := range listed.Results {
		if strings.EqualFold(entry.Repository, sourceRepository) && entry.CanonicalDir != "" {
			sourceCanonical = entry.CanonicalDir
			break
		}
	}
	for _, diagnostic := range listed.Diagnostics {
		if diagnostic.NonBlocking || landingDiagnosticIsUnrelated(ctx, options, view, diagnostic, sourceCanonical) {
			continue
		}
		return &landRefusal{
			code:    "cleanup-unverifiable",
			reason:  "the source worktree inventory is incomplete at " + diagnostic.Path + ": " + diagnostic.Message,
			command: "inspect and repair the source worktree inventory, then retry wb pr land " + options.Repository + "#" + number,
		}
	}
	for _, entry := range listed.Results {
		if !strings.EqualFold(entry.Repository, sourceRepository) || entry.Branch != view.Head.Ref {
			continue
		}
		if !entry.Clean && !linksOnly && strings.EqualFold(sourceRepository, options.Repository) {
			return &landRefusal{
				code: "cleanup-blocked-dirty",
				reason: "the worktree for task " + entry.Task + " has uncommitted changes, so landing now would " +
					"merge the work and then be unable to retire the checkout that produced it",
				command: "wb worktree end " + entry.Task + ", or land with --keep",
			}
		}
		if refusal := refuseLinkedWorktree(options.ProjectsRoot, entry); refusal != nil {
			return refusal
		}
	}
	return nil
}

// Inventory filters are substrings. A diagnostic for acme/app-extra must not
// block acme/app, but its path or stale creation manifest cannot establish
// that distinction. Only readable Git identity may exclude a candidate;
// incomplete evidence keeps the fail-closed refusal above.
func landingDiagnosticIsUnrelated(ctx context.Context, options PullRequestLandOptions, view githubchecks.PullRequestView, diagnostic worktrees.ListDiagnostic, sourceCanonical string) bool {
	identity, _, err := runCommand(ctx, options.resolveRunner(), 0, 0, diagnostic.Path, "git", "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-common-dir", "--abbrev-ref", "HEAD")
	lines := strings.Split(strings.TrimSpace(identity), "\n")
	if err != nil || len(lines) != 3 || filepath.Clean(lines[0]) != filepath.Clean(diagnostic.Path) || lines[2] == "" || lines[2] == "HEAD" {
		return false
	}
	if lines[2] != view.Head.Ref {
		return true
	}
	if sourceCanonical == "" {
		sourceCanonical, err = worktrees.CanonicalRepositoryPath(options.ProjectsRoot, view.Head.Repo.FullName)
		if err != nil {
			return false
		}
	}
	common, _, err := runCommand(ctx, options.resolveRunner(), 0, 0, sourceCanonical, "git", "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || strings.TrimSpace(common) == "" {
		return false
	}
	candidateCommon, candidateErr := filepath.EvalSymlinks(lines[1])
	sourceCommon, sourceErr := filepath.EvalSymlinks(strings.TrimSpace(common))
	return candidateErr == nil && sourceErr == nil && candidateCommon != sourceCommon
}

// refuseLinkedWorktree refuses a checkout that still holds a live local
// dependency link, through the one implementation WB has of that question.
//
// It consults both signals — recorded stream links and a `go.work` nobody
// recorded — because either alone misses the other, and it reuses
// locallink.HasLiveLink rather than asking half the question a second way.
func refuseLinkedWorktree(projectsRoot string, entry worktrees.ListResult) *landRefusal {
	store, err := streams.Open(projectsRoot)
	if err != nil {
		return &landRefusal{
			code:    "cleanup-unverifiable",
			reason:  "local dependency link storage for " + entry.WorktreeDir + " could not be resolved: " + err.Error(),
			command: "inspect and repair the WB home, then retry landing",
		}
	}
	links, err := locallink.HasLiveLink(store, entry.WorktreeDir)
	if err != nil {
		return &landRefusal{
			code:    "cleanup-unverifiable",
			reason:  "local dependency link evidence for " + entry.WorktreeDir + " could not be read, so this landing's cleanup cannot be pre-flighted: " + err.Error(),
			command: "inspect and repair the stream records and " + entry.WorktreeDir + "/go.work, then retry landing",
		}
	}
	if len(links) == 0 {
		return nil
	}
	return &landRefusal{
		code:    "cleanup-blocked-live-link",
		reason:  locallink.RefusalMessage(entry.WorktreeDir, links),
		command: "wb deps propagate local --to " + entry.WorktreeDir + " --undo, then verify with released dependencies and retry landing",
	}
}
