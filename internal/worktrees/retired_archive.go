package worktrees

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/githubobserver"
)

// DefaultRetiredArchiveRepository is the per-organization archive repository
// basename when user configuration has no override.
const DefaultRetiredArchiveRepository = "backstage-retired"

// RetiredArchiveTarget identifies the private repository that a remote
// retirement flow must use for one organization. The organization is always
// prepended; configuration never permits a source repository to choose another
// organization's archive.
type RetiredArchiveTarget struct {
	Organization string `json:"organization"`
	Repository   string `json:"repository"`
}

// ResolveRetiredArchiveTarget reads only the machine-local worktrees policy.
// Repository-tracked policy is rejected by the existing exact-base policy
// reader, so it cannot redirect an archive destination.
func ResolveRetiredArchiveTarget(organization string) (RetiredArchiveTarget, error) {
	organization = strings.TrimSpace(organization)
	if !validSafeSegment(organization) {
		return RetiredArchiveTarget{}, fmt.Errorf("invalid retired archive organization %q", organization)
	}
	config, found, path, err := configuredUserWorktreesConfig()
	if err != nil {
		return RetiredArchiveTarget{}, err
	}
	repository := DefaultRetiredArchiveRepository
	if found && config.Retirement.ArchiveRepository != nil {
		repository = *config.Retirement.ArchiveRepository
	}
	if found {
		if override, ok := config.Retirement.Organizations[organization]; ok && override.ArchiveRepository != nil {
			repository = *override.ArchiveRepository
		}
	}
	if err := validateRetiredArchiveRepositoryName(repository); err != nil {
		return RetiredArchiveTarget{}, fmt.Errorf("worktrees config %s retired archive: %w", path, err)
	}
	return RetiredArchiveTarget{Organization: organization, Repository: organization + "/" + repository}, nil
}

func validateRetiredArchiveRepositoryName(repository string) error {
	if strings.TrimSpace(repository) != repository || repository == "" || !validRepositorySegment(repository) {
		return fmt.Errorf("must be a non-empty repository basename")
	}
	return nil
}

// RetiredArchiveInspection is the minimum authoritative remote observation.
// Callers must not infer privacy from a local clone or configuration: a missing,
// public, or unobservable archive repository is a refusal.
type RetiredArchiveInspection struct {
	Exists     bool
	Private    bool
	Repository string
}

type RetiredArchiveInspector func(context.Context, string) (RetiredArchiveInspection, error)

// RetiredArchivePlan is a read-only archive-target preflight. It has no ref or
// filesystem operation fields that can be applied by this plan itself.
type RetiredArchivePlan struct {
	GeneratedAt       time.Time `json:"generated_at"`
	SourceRepository  string    `json:"source_repository"`
	ArchiveRepository string    `json:"archive_repository"`
	Outcome           string    `json:"outcome"`
	Refusal           string    `json:"refusal,omitempty"`
	LocalQuarantine   string    `json:"local_quarantine"`
	RemoteRefRename   bool      `json:"remote_ref_rename"`
	WorktreeDeletion  bool      `json:"worktree_deletion"`
	WorkLogExport     string    `json:"work_log_export"`
}

// PlanRetiredArchivePreflight resolves and verifies an archive destination.
// It never writes remote refs, removes a worktree, exports a Work Log, or
// creates a report directory. Inspection failures are deliberately rendered as
// a stable refusal without copying a transport error into a plan or log.
func PlanRetiredArchivePreflight(ctx context.Context, sourceRepository string, inspect RetiredArchiveInspector) (RetiredArchivePlan, error) {
	organization, sourceName, ok := strings.Cut(strings.TrimSpace(sourceRepository), "/")
	if !ok || !validSafeSegment(organization) || !validRepositorySegment(sourceName) {
		return RetiredArchivePlan{}, fmt.Errorf("invalid source repository %q; use organization/repository", sourceRepository)
	}
	target, err := ResolveRetiredArchiveTarget(organization)
	if err != nil {
		return RetiredArchivePlan{}, err
	}
	plan := RetiredArchivePlan{
		GeneratedAt:       time.Now().UTC(),
		SourceRepository:  sourceRepository,
		ArchiveRepository: target.Repository,
		Outcome:           "refused",
		LocalQuarantine:   "preserved",
		RemoteRefRename:   false,
		WorktreeDeletion:  false,
		WorkLogExport:     "not_started",
	}
	if inspect == nil {
		inspect = inspectRetiredArchiveRepository
	}
	observed, inspectErr := inspect(ctx, target.Repository)
	if inspectErr != nil {
		if retiredArchiveMissing(inspectErr) {
			plan.Refusal = "archive repository is missing"
		} else {
			plan.Refusal = "archive repository is unavailable"
		}
		return plan, nil
	}
	if !observed.Exists {
		plan.Refusal = "archive repository is missing"
		return plan, nil
	}
	if observed.Repository != target.Repository {
		plan.Refusal = "archive repository is unavailable"
		return plan, nil
	}
	if !observed.Private {
		plan.Refusal = "archive repository is public"
		return plan, nil
	}
	plan.Outcome = "planned"
	plan.WorkLogExport = "plain private Work Log capture is available through wb worktree retire --apply"
	return plan, nil
}

func retiredArchiveMissing(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "404") || strings.Contains(message, "not found")
}

func inspectRetiredArchiveRepository(ctx context.Context, repository string) (RetiredArchiveInspection, error) {
	body, err := githubobserver.Read(ctx, "", "api", "repos/"+repository)
	if err != nil {
		return RetiredArchiveInspection{}, err
	}
	var response struct {
		FullName string `json:"full_name"`
		Private  bool   `json:"private"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return RetiredArchiveInspection{}, err
	}
	return RetiredArchiveInspection{Exists: true, Private: response.Private, Repository: response.FullName}, nil
}
