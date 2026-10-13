package orchestrate

import (
	"context"
	"encoding/hex"
	"fmt"
	"github.com/sneat-dev/wb/internal/githubchecks"
	"github.com/sneat-dev/wb/internal/locallink"
	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/sneat-dev/wb/internal/worktreesecure"
	"os"
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
		if diagnostic.NonBlocking || landingRetiredArtifactIsUnrelated(ctx, options, view, diagnostic, listed.Artifacts, sourceCanonical) || landingDiagnosticIsUnrelated(ctx, options, view, diagnostic, sourceCanonical) {
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

// A verified retired sibling stage is recovery state, not a task producer.
// Its cleanup diagnostic can be excluded only when the source Git registry
// independently proves no matching or unidentified producer inside it. The
// artifact remains untouched and ordinary cleanup retains its own admission.
func landingRetiredArtifactIsUnrelated(ctx context.Context, options PullRequestLandOptions, view githubchecks.PullRequestView, diagnostic worktrees.ListDiagnostic, artifacts []worktrees.LifecycleArtifact, sourceCanonical string) bool {
	if sourceCanonical == "" {
		return false
	}
	for _, artifact := range artifacts {
		if !artifact.InspectionVerified || artifact.Kind != "secure_worktree_stage" || artifact.State != "quarantined" || artifact.Task != "" || artifact.Disposition != "unscoped_local_stage" ||
			artifact.Path != diagnostic.Path || artifact.WorktreesRoot != diagnostic.WorktreesRoot || filepath.Dir(artifact.Path) != artifact.WorktreesRoot {
			continue
		}
		directory, err := worktreesecure.OpenAbsoluteDirectoryNoFollow(artifact.Path, false)
		if err != nil {
			return false
		}
		defer func() { _ = directory.Close() }()
		artifactIdentity, err := directory.Stat()
		if err != nil {
			return false
		}
		canonicalIdentity, err := os.Stat(sourceCanonical)
		if err != nil || !canonicalIdentity.IsDir() {
			return false
		}
		registry, _, err := runCommand(ctx, options.resolveRunner(), 0, 0, sourceCanonical, "git", "worktree", "list", "--porcelain", "-z")
		if err != nil || !strings.HasSuffix(registry, "\x00\x00") {
			return false
		}
		canonicalFound := false
		for _, record := range strings.Split(strings.TrimSuffix(registry, "\x00\x00"), "\x00\x00") {
			fields := strings.Split(record, "\x00")
			path, known := strings.CutPrefix(fields[0], "worktree ")
			if !known || !filepath.IsAbs(path) {
				return false
			}
			registeredIdentity, inside, err := landingRegisteredDirectoryRelation(artifactIdentity, path)
			if err != nil {
				return false
			}
			canonicalFound = canonicalFound || os.SameFile(canonicalIdentity, registeredIdentity)
			branch := ""
			for _, field := range fields[1:] {
				if value, found := strings.CutPrefix(field, "branch "); found {
					branch = value
				}
			}
			if inside && (!strings.HasPrefix(branch, "refs/heads/") || branch == "refs/heads/" || branch == "refs/heads/"+view.Head.Ref) {
				return false
			}
		}
		return canonicalFound && worktreesecure.DirectoryStillMatches(artifact.Path, directory)
	}
	return false
}

// Resolve symlinks before a no-follow open, then compare physical parent
// descriptors. Cleaned path strings (including EvalSymlinks output) cannot
// prove ancestry on a filesystem where alternate casing names the same inode.
func landingRegisteredDirectoryRelation(artifactIdentity os.FileInfo, path string) (os.FileInfo, bool, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, false, err
	}
	current, err := worktreesecure.OpenAbsoluteDirectoryNoFollow(resolved, false)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = current.Close() }()
	registeredIdentity, err := current.Stat()
	if err != nil {
		return nil, false, err
	}
	for {
		identity, err := current.Stat()
		if err != nil {
			return nil, false, err
		}
		if os.SameFile(artifactIdentity, identity) {
			return registeredIdentity, true, nil
		}
		parent, err := worktreesecure.OpenDirectoryAtNoFollow(int(current.Fd()), "..", "wb-landing-parent", "inspect physical registered worktree parent")
		if err != nil {
			return nil, false, err
		}
		parentIdentity, err := parent.Stat()
		if err != nil {
			_ = parent.Close()
			return nil, false, fmt.Errorf("inspect physical parent identity: %w", err)
		}
		_ = current.Close()
		current = parent
		if os.SameFile(identity, parentIdentity) {
			return registeredIdentity, false, nil
		}
	}
}

// Inventory filters are substrings. A diagnostic for acme/app-extra must not
// block acme/app, but its path or stale creation manifest cannot establish
// that distinction. Only readable Git identity may exclude a candidate;
// incomplete evidence keeps the fail-closed refusal above.
func landingDiagnosticIsUnrelated(ctx context.Context, options PullRequestLandOptions, view githubchecks.PullRequestView, diagnostic worktrees.ListDiagnostic, sourceCanonical string) bool {
	identity, _, err := runCommand(ctx, options.resolveRunner(), 0, 0, diagnostic.Path, "git", "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-common-dir", "--abbrev-ref", "HEAD")
	lines := strings.Split(strings.TrimSpace(identity), "\n")
	if err != nil || len(lines) != 3 || lines[2] == "" {
		return false
	}
	rootIdentity, rootErr := os.Stat(lines[0])
	pathIdentity, pathErr := os.Stat(diagnostic.Path)
	if rootErr != nil || pathErr != nil || !rootIdentity.IsDir() || !os.SameFile(rootIdentity, pathIdentity) {
		return false
	}
	if lines[2] != "HEAD" && lines[2] != view.Head.Ref {
		return true
	}
	if lines[2] == "HEAD" && sourceCanonical == "" {
		return false
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
	candidateCommon, candidateErr := os.Stat(lines[1])
	sourceCommon, sourceErr := os.Stat(strings.TrimSpace(common))
	if candidateErr != nil || sourceErr != nil || !candidateCommon.IsDir() || !sourceCommon.IsDir() {
		return false
	}
	if lines[2] == "HEAD" {
		return os.SameFile(candidateCommon, sourceCommon) && landingDetachedDiagnosticIsUnrelated(ctx, options, view, diagnostic.Path, sourceCanonical, rootIdentity, candidateCommon)
	}
	return !os.SameFile(candidateCommon, sourceCommon)
}

// A clean detached checkout at another exact commit cannot be the reviewed
// PR producer. Missing claims alone prove nothing: bind readable local Git
// identity and status to the canonical registry, then re-observe the checkout.
func landingDetachedDiagnosticIsUnrelated(ctx context.Context, options PullRequestLandOptions, view githubchecks.PullRequestView, path, canonical string, rootIdentity, commonIdentity os.FileInfo) bool {
	if len(view.Head.SHA) != 40 {
		return false
	}
	if _, err := hex.DecodeString(view.Head.SHA); err != nil {
		return false
	}
	args := []string{"git", "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-common-dir", "HEAD", "--abbrev-ref", "HEAD"}
	identity, _, err := runCommand(ctx, options.resolveRunner(), 0, 0, path, args[0], args[1:]...)
	fields := strings.Split(strings.TrimSpace(identity), "\n")
	if err != nil || len(fields) != 4 || fields[3] != "HEAD" || len(fields[2]) != 40 || strings.EqualFold(fields[2], view.Head.SHA) {
		return false
	}
	if _, err := hex.DecodeString(fields[2]); err != nil {
		return false
	}
	root, rootErr := os.Stat(fields[0])
	common, commonErr := os.Stat(fields[1])
	canonicalIdentity, canonicalErr := os.Stat(canonical)
	if rootErr != nil || commonErr != nil || canonicalErr != nil || !os.SameFile(rootIdentity, root) || !os.SameFile(commonIdentity, common) {
		return false
	}
	status, _, err := runCommand(ctx, options.resolveRunner(), 0, 0, path, "git", "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil || status != "" {
		return false
	}
	registry, _, err := runCommand(ctx, options.resolveRunner(), 0, 0, canonical, "git", "worktree", "list", "--porcelain", "-z")
	if err != nil || !strings.HasSuffix(registry, "\x00\x00") {
		return false
	}
	canonicalFound, detachedFound := false, false
	for _, record := range strings.Split(strings.TrimSuffix(registry, "\x00\x00"), "\x00\x00") {
		recordFields := strings.Split(record, "\x00")
		registeredPath, known := strings.CutPrefix(recordFields[0], "worktree ")
		registeredIdentity, statErr := os.Stat(registeredPath)
		if !known || !filepath.IsAbs(registeredPath) || statErr != nil || !registeredIdentity.IsDir() {
			return false
		}
		canonicalFound = canonicalFound || os.SameFile(canonicalIdentity, registeredIdentity)
		if !os.SameFile(rootIdentity, registeredIdentity) {
			continue
		}
		detachedCount, headCount := 0, 0
		for _, field := range recordFields[1:] {
			if strings.HasPrefix(field, "branch ") {
				return false
			}
			if field == "detached" {
				detachedCount++
			}
			if head, known := strings.CutPrefix(field, "HEAD "); known {
				headCount++
				if head != fields[2] {
					return false
				}
			}
		}
		if detachedFound || detachedCount != 1 || headCount != 1 {
			return false
		}
		detachedFound = true
	}
	if !canonicalFound || !detachedFound {
		return false
	}
	after, _, err := runCommand(ctx, options.resolveRunner(), 0, 0, path, args[0], args[1:]...)
	if err != nil || after != identity {
		return false
	}
	status, _, err = runCommand(ctx, options.resolveRunner(), 0, 0, path, "git", "status", "--porcelain=v1", "--untracked-files=all")
	currentRoot, currentRootErr := os.Stat(path)
	currentCommon, currentCommonErr := os.Stat(fields[1])
	return err == nil && status == "" && currentRootErr == nil && currentCommonErr == nil && os.SameFile(rootIdentity, currentRoot) && os.SameFile(commonIdentity, currentCommon)
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
