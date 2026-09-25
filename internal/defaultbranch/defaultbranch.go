package defaultbranch

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/gitremote"
	"github.com/sneat-dev/wb/internal/wbconfig"
	"github.com/sneat-dev/wb/internal/wbhome"
	"gopkg.in/yaml.v3"
)

const defaultBranchSchemaVersion = 1

type Options struct {
	Apply, JSON, IncludeUser, AllOrgs, TemporarilyUnarchive, MigratePagesSource, RewriteWorkflowTriggers bool
	Branch, ReportDir, ReconcileFrom, RestoreArchiveFrom                                                 string
	ReconcileSHA256, RestoreArchiveSHA256                                                                string
	Owners, Repositories                                                                                 []string
	Parallel                                                                                             int
}
type defaultBranchConfig struct {
	Fleet struct {
		DefaultBranch string `yaml:"default_branch"`
		Organizations map[string]struct {
			DefaultBranch string `yaml:"default_branch"`
		} `yaml:"organizations"`
	} `yaml:"fleet"`
}
type Report struct {
	SchemaVersion int          `json:"schema_version"`
	Mode          string       `json:"mode"`
	Desired       string       `json:"desired"`
	Repositories  []Repository `json:"repositories"`
	Summary       Summary      `json:"summary"`
	ReportPath    string       `json:"report_path,omitempty"`
}
type Summary struct {
	Inspected        int `json:"inspected"`
	Compliant        int `json:"compliant"`
	Drift            int `json:"drift"`
	Blocked          int `json:"blocked"`
	Errors           int `json:"errors"`
	Applied          int `json:"applied"`
	CanonicalBlocked int `json:"canonical_blocked"`
	CanonicalErrors  int `json:"canonical_errors"`
}
type Repository struct {
	Repository      string       `json:"repository"`
	RepositoryID    int64        `json:"repository_id,omitempty"`
	ObservedDefault string       `json:"observed_default,omitempty"`
	VerifiedDefault string       `json:"verified_default,omitempty"`
	Desired         string       `json:"desired"`
	OldHead         string       `json:"old_head,omitempty"`
	NewHead         string       `json:"new_head,omitempty"`
	Disposition     string       `json:"disposition"`
	Error           string       `json:"error,omitempty"`
	Archived        bool         `json:"archived,omitempty"`
	Fork            bool         `json:"fork,omitempty"`
	TargetExists    bool         `json:"target_exists,omitempty"`
	RenameAccepted  bool         `json:"rename_accepted,omitempty"`
	RecoveredFrom   string       `json:"recovered_from,omitempty"`
	RecoveredSHA256 string       `json:"recovered_sha256,omitempty"`
	Archive         *Archive     `json:"archive_transition,omitempty"`
	PagesBefore     *PagesSource `json:"pages_before,omitempty"`
	PagesAfter      *PagesSource `json:"pages_after,omitempty"`
	PagesPhase      string       `json:"pages_phase,omitempty"`
	PagesAccepted   bool         `json:"pages_mutation_accepted,omitempty"`
	WorkflowFiles   []Workflow   `json:"workflow_files,omitempty"`
	WorkflowPhase   string       `json:"workflow_phase,omitempty"`
	WorkflowCommit  string       `json:"workflow_commit_oid,omitempty"`
	Impacts         []string     `json:"impacts,omitempty"`
	Actions         []string     `json:"actions,omitempty"`
	CanonicalClones []Canonical  `json:"canonical_clones,omitempty"`
}

// Workflow records a byte-preserving proposed replacement.  The
// contents themselves never enter a report: the before/after SHA-256 values
// bind the report to the exact bytes that were read and verified.
type Workflow struct {
	Path         string `json:"path"`
	BlobBefore   string `json:"blob_before"`
	SHA256Before string `json:"sha256_before"`
	SHA256After  string `json:"sha256_after"`
	contents     string
	rewritten    string
}
type PagesSource struct {
	BuildType string `json:"build_type"`
	Branch    string `json:"branch"`
	Path      string `json:"path"`
}
type Archive struct {
	RepositoryID      int64  `json:"repository_id"`
	OriginalArchived  bool   `json:"original_archived"`
	InitialDefault    string `json:"initial_default"`
	DesiredDefault    string `json:"desired_default"`
	InitialHead       string `json:"initial_head"`
	FinalHead         string `json:"final_head,omitempty"`
	Phase             string `json:"phase"`
	UnarchiveAccepted bool   `json:"unarchive_accepted,omitempty"`
	RestoreAccepted   bool   `json:"restore_accepted,omitempty"`
	RecoveryRequired  bool   `json:"recovery_required,omitempty"`
	RestoreError      string `json:"restore_error,omitempty"`
	RecoveredFrom     string `json:"recovered_from,omitempty"`
	RecoveredSHA256   string `json:"recovered_sha256,omitempty"`
}
type Canonical struct {
	Path           string   `json:"path"`
	ObservedBranch string   `json:"observed_branch,omitempty"`
	RemoteHead     string   `json:"remote_head,omitempty"`
	Disposition    string   `json:"disposition"`
	Error          string   `json:"error,omitempty"`
	Actions        []string `json:"actions,omitempty"`
}
type defaultBranchRepoMetadata struct {
	ID            int64  `json:"id"`
	DefaultBranch string `json:"default_branch"`
	Archived      bool   `json:"archived"`
	Fork          bool   `json:"fork"`
	Size          int    `json:"size"`
	Parent        *struct {
		FullName string `json:"full_name"`
	} `json:"parent"`
}
type defaultBranchRef struct {
	Commit struct {
		SHA string `json:"sha"`
	} `json:"commit"`
}

// ConfigPath locates wb.yaml. A test overrides it to point at a fixture.
var ConfigPath = wbconfig.DefaultPath

// Run audits or applies the fleet's default branch using production
// ports: real git over internal/gitcli and internal/runner, real gh
// through internal/githubobserver, real fleet discovery, and a real
// clock. A unit test builds an *Engine directly from fakes_test.go's
// fakes instead of calling Run.
func Run(ctx context.Context, projectsRoot, filter string, options Options, progress io.Writer) (Report, error) {
	return newDefaultBranchEngine().run(ctx, projectsRoot, filter, options, progress)
}

func (e *Engine) run(ctx context.Context, projectsRoot, filter string, options Options, progress io.Writer) (Report, error) {
	if options.RestoreArchiveFrom != "" {
		return e.runDefaultBranchArchiveRestore(projectsRoot, ctx, options, progress)
	}
	config, err := loadDefaultBranchConfig(ConfigPath())
	if err != nil {
		return Report{}, err
	}
	prior, err := readDefaultBranchReport(options.ReconcileFrom, options.ReconcileSHA256)
	if err != nil {
		return Report{}, err
	}
	repos, discoveryFailures, err := e.discoverDefaultBranchFleet(filter, options.Owners, options.Repositories, options.IncludeUser, options.AllOrgs)
	if err != nil {
		return Report{}, err
	}
	locals, err := e.defaultBranchLocalClones(projectsRoot, filter)
	if err != nil {
		return Report{}, err
	}
	report := Report{SchemaVersion: defaultBranchSchemaVersion, Mode: "audit", Desired: strings.TrimSpace(options.Branch)}
	if options.Apply {
		report.Mode = "apply"
	}
	report.Repositories = make([]Repository, len(repos)+len(discoveryFailures))
	copy(report.Repositories, discoveryFailures)
	jobs := make(chan int)
	var wg sync.WaitGroup
	for worker := 0; worker < options.Parallel; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				desired := effectiveDefaultBranch(options.Branch, config, repos[i].Org)
				report.Repositories[len(discoveryFailures)+i] = e.inspectDefaultBranchWithOptions(ctx, repos[i], desired, options.TemporarilyUnarchive, options.MigratePagesSource, options.RewriteWorkflowTriggers)
			}
		}()
	}
	for i := range repos {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	attachDefaultBranchLocalBlockers(&report, locals.Blocked)
	if options.Apply {
		path, err := defaultBranchReportPath(projectsRoot, options.ReportDir)
		if err != nil {
			return report, err
		}
		report.ReportPath = path
		summarizeDefaultBranch(&report)
		if err := persistDefaultBranchReport(report); err != nil {
			return report, err
		}
		for i := range report.Repositories {
			sourceDefault, sourceHead := "", ""
			if report.Repositories[i].Disposition == "drift" {
				if prior != nil {
					report.Repositories[i].Disposition = "blocked"
					report.Repositories[i].Error = "--reconcile-from is remote read-only, but the refreshed remote rename is not compliant; WB will not resend the mutation"
				} else if report.Repositories[i].Archive != nil && report.Repositories[i].Archive.OriginalArchived {
					report.Repositories[i] = e.applyArchivedDefaultBranch(ctx, report.Repositories[i], func(updated Repository) error {
						report.Repositories[i] = updated
						summarizeDefaultBranch(&report)
						return persistDefaultBranchReport(report)
					})
					summarizeDefaultBranch(&report)
					if err := persistDefaultBranchReport(report); err != nil {
						return report, err
					}
					if report.Repositories[i].Disposition == "compliant" && report.Repositories[i].Archive != nil && report.Repositories[i].Archive.Phase == "restored" {
						sourceDefault, sourceHead = report.Repositories[i].ObservedDefault, report.Repositories[i].OldHead
					}
				} else if defaultBranchPagesOnlyRepair(report.Repositories[i]) {
					report.Repositories[i] = e.applyDefaultBranchPagesWithCheckpoint(ctx, report.Repositories[i], func(updated Repository) error {
						report.Repositories[i] = updated
						summarizeDefaultBranch(&report)
						return persistDefaultBranchReport(report)
					})
					summarizeDefaultBranch(&report)
					if err := persistDefaultBranchReport(report); err != nil {
						return report, err
					}
					if report.Repositories[i].Disposition == "drift" {
						if _, err := fmt.Fprintf(progress, "default-branch: applied %s\n", report.Repositories[i].Repository); err != nil {
							return report, err
						}
					}
				} else if report.Repositories[i].ObservedDefault == report.Repositories[i].Desired && report.Repositories[i].PagesBefore != nil && report.Repositories[i].PagesPhase == "unfinished" {
					report.Repositories[i].Disposition = "blocked"
					report.Repositories[i].Error = "unfinished Pages source is outside the supported legacy master root or /docs repair; WB will not infer or overwrite it"
					if err := persistDefaultBranchReport(report); err != nil {
						return report, err
					}
				} else if len(report.Repositories[i].WorkflowFiles) > 0 {
					report.Repositories[i] = e.applyDefaultBranchWorkflowTriggers(ctx, report.Repositories[i], func(updated Repository) error {
						report.Repositories[i] = updated
						summarizeDefaultBranch(&report)
						return persistDefaultBranchReport(report)
					})
					if report.Repositories[i].Disposition == "drift" {
						report.Repositories[i] = e.applyDefaultBranchWithCheckpoint(ctx, report.Repositories[i], func(updated Repository) error {
							report.Repositories[i] = updated
							summarizeDefaultBranch(&report)
							return persistDefaultBranchReport(report)
						})
					}
					summarizeDefaultBranch(&report)
					if err := persistDefaultBranchReport(report); err != nil {
						return report, err
					}
				} else {
					report.Repositories[i] = e.applyDefaultBranchWithCheckpoint(ctx, report.Repositories[i], func(updated Repository) error {
						report.Repositories[i] = updated
						summarizeDefaultBranch(&report)
						return persistDefaultBranchReport(report)
					})
					summarizeDefaultBranch(&report)
					if err := persistDefaultBranchReport(report); err != nil {
						return report, err
					}
					if report.Repositories[i].Disposition == "compliant" && report.Repositories[i].PagesBefore != nil {
						report.Repositories[i] = e.applyDefaultBranchPagesWithCheckpoint(ctx, report.Repositories[i], func(updated Repository) error {
							report.Repositories[i] = updated
							summarizeDefaultBranch(&report)
							return persistDefaultBranchReport(report)
						})
						summarizeDefaultBranch(&report)
						if err := persistDefaultBranchReport(report); err != nil {
							return report, err
						}
					}
					if len(report.Repositories[i].Actions) > 0 && report.Repositories[i].Disposition == "compliant" && defaultBranchPagesTerminal(report.Repositories[i]) {
						if _, err := fmt.Fprintf(progress, "default-branch: applied %s\n", report.Repositories[i].Repository); err != nil {
							return report, err
						}
						sourceDefault, sourceHead = report.Repositories[i].ObservedDefault, report.Repositories[i].OldHead
					}
				}
			}
			resumeReason := ""
			if sourceDefault == "" && report.Repositories[i].Disposition == "compliant" && prior != nil {
				sourceDefault, sourceHead, resumeReason = defaultBranchResumeSource(prior, report.Repositories[i])
				if sourceDefault == "" {
					legacySource, legacyHead, legacyReason := e.defaultBranchPagesAutomaticResume(ctx, prior, report.Repositories[i])
					if legacySource == "" {
						legacySource, legacyHead, legacyReason = e.defaultBranchLegacyRenameResume(ctx, prior, report.Repositories[i])
					}
					if legacySource != "" {
						sourceDefault, sourceHead, resumeReason = legacySource, legacyHead, legacyReason
						recordDefaultBranchLegacyRenameProof(&report.Repositories[i], legacySource, legacyHead, options.ReconcileFrom, options.ReconcileSHA256)
					} else {
						resumeReason = legacyReason
					}
				}
			}
			if sourceDefault != "" {
				e.reconcileDefaultBranchCanonicals(ctx, &report.Repositories[i], locals.Eligible[strings.ToLower(report.Repositories[i].Repository)], sourceDefault, sourceHead, func() error {
					summarizeDefaultBranch(&report)
					return persistDefaultBranchReport(report)
				})
			} else if resumeReason != "" {
				for _, clone := range locals.Eligible[strings.ToLower(report.Repositories[i].Repository)] {
					report.Repositories[i].CanonicalClones = append(report.Repositories[i].CanonicalClones, Canonical{Path: clone.Path, Disposition: "blocked", Error: resumeReason})
				}
			}
		}
	}
	summarizeDefaultBranch(&report)
	if options.Apply && report.ReportPath != "" {
		if err := persistDefaultBranchReport(report); err != nil {
			return report, err
		}
	}
	if !options.Apply && options.ReportDir != "" {
		path, err := defaultBranchReportPath(projectsRoot, options.ReportDir)
		if err != nil {
			return report, err
		}
		report.ReportPath = path
		if err := persistDefaultBranchReport(report); err != nil {
			return report, err
		}
	}
	return report, nil
}

func defaultBranchPagesOnlyRepair(repository Repository) bool {
	if repository.ObservedDefault != repository.Desired || repository.PagesBefore == nil || repository.PagesPhase != "unfinished" {
		return false
	}
	pages := *repository.PagesBefore
	return pages.BuildType == "legacy" && pages.Branch == "master" && validDefaultBranchPagesPath(pages.Path)
}

func readDefaultBranchReport(path, expectedDigest string) (*Report, error) {
	if path == "" {
		return nil, nil
	}
	if !ValidDigest(expectedDigest) {
		return nil, errors.New("--reconcile-from requires --reconcile-sha256 with the exact 64-character SHA-256 of that report")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read --reconcile-from report: %w", err)
	}
	if actual := defaultBranchDigest(raw); !strings.EqualFold(actual, expectedDigest) {
		return nil, fmt.Errorf("--reconcile-from SHA-256 mismatch: got %s", actual)
	}
	var report Report
	if err := json.Unmarshal(raw, &report); err != nil {
		return nil, fmt.Errorf("decode --reconcile-from report: %w", err)
	}
	if report.SchemaVersion != defaultBranchSchemaVersion || report.Mode != "apply" {
		return nil, errors.New("--reconcile-from must be a default-branch apply report from this WB schema")
	}
	return &report, nil
}

// runDefaultBranchArchiveRestore is deliberately separate from normal apply:
// it has one repository, reads one caller-bound receipt, and can only restore
// the archived bit.  It never discovers a fleet or scans/reconciles a clone.
func (e *Engine) runDefaultBranchArchiveRestore(projectsRoot string, ctx context.Context, options Options, progress io.Writer) (Report, error) {
	prior, err := readDefaultBranchArchiveRestoreReport(options.RestoreArchiveFrom, options.RestoreArchiveSHA256)
	if err != nil {
		return Report{}, err
	}
	repository, transition, err := defaultBranchArchiveRestoreTarget(*prior, options.Repositories[0])
	if err != nil {
		return Report{}, err
	}
	path, err := defaultBranchReportPath(projectsRoot, options.ReportDir)
	if err != nil {
		return Report{}, err
	}
	repository.Archive = transition
	repository.RecoveredFrom = options.RestoreArchiveFrom
	repository.RecoveredSHA256 = options.RestoreArchiveSHA256
	repository.Archive.RecoveredFrom = options.RestoreArchiveFrom
	repository.Archive.RecoveredSHA256 = options.RestoreArchiveSHA256
	report := Report{SchemaVersion: defaultBranchSchemaVersion, Mode: "restore-archive", Desired: repository.Desired, Repositories: []Repository{repository}, ReportPath: path}
	checkpoint := func(updated Repository) error {
		report.Repositories[0] = updated
		summarizeDefaultBranch(&report)
		return persistDefaultBranchReport(report)
	}
	if err := checkpoint(repository); err != nil {
		return report, err
	}

	metadata, err := e.readDefaultBranchMetadata(ctx, repository.Repository)
	if err != nil {
		return failDefaultBranchArchiveRestore(&report, repository, "read restore target: "+err.Error())
	}
	if err := e.validateDefaultBranchArchiveRestoreTarget(ctx, metadata, repository.Repository, transition); err != nil {
		return failDefaultBranchArchiveRestore(&report, repository, err.Error())
	}
	if metadata.Archived {
		repository.Archived = true
		repository.Disposition = "compliant"
		repository.Error = ""
		repository.Archive.Phase = "restored"
		repository.Archive.RecoveryRequired = false
		repository.Actions = []string{"verified archived state from restore receipt"}
		if err := checkpoint(repository); err != nil {
			return report, err
		}
		_, _ = fmt.Fprintf(progress, "default-branch: verified archived %s\n", repository.Repository)
		return report, nil
	}

	repository.Archive.Phase = "restore_pending"
	repository.Disposition = "pending"
	repository.Error = "archive restoration pending"
	if err := checkpoint(repository); err != nil {
		return report, err
	}
	response := e.GitHub.Execute(ctx, "api", "--method", "PATCH", "repos/"+repository.Repository, "-f", "archived=true")
	repository.Archive.RestoreAccepted = response.Err == nil
	metadata, err = e.readDefaultBranchMetadata(ctx, repository.Repository)
	if err == nil {
		err = e.validateDefaultBranchArchiveRestoreTarget(ctx, metadata, repository.Repository, transition)
	}
	// A transport error is ambiguous. A fresh exact proof of the archived
	// state is enough to record success; sending the PATCH again would weaken
	// the one-mutation recovery contract.
	if err != nil || !metadata.Archived {
		repository.Archive.Phase = "failed"
		repository.Archive.RecoveryRequired = true
		if err != nil {
			repository.Archive.RestoreError = "verify archive restoration: " + err.Error()
		} else {
			repository.Archive.RestoreError = "restore archival: " + githubCommandMessage(response)
		}
		repository.Disposition = "error"
		repository.Error = repository.Archive.RestoreError
		if checkpointErr := checkpoint(repository); checkpointErr != nil {
			return report, checkpointErr
		}
		return report, errors.New(repository.Error)
	}
	repository.Archived = true
	repository.Disposition = "compliant"
	repository.Error = ""
	repository.Archive.Phase = "restored"
	repository.Archive.RecoveryRequired = false
	repository.Archive.RestoreError = ""
	repository.Actions = []string{"restored archived state from restore receipt"}
	if err := checkpoint(repository); err != nil {
		return report, err
	}
	_, _ = fmt.Fprintf(progress, "default-branch: restored archived %s\n", repository.Repository)
	return report, nil
}

func failDefaultBranchArchiveRestore(report *Report, repository Repository, message string) (Report, error) {
	repository.Disposition = "error"
	repository.Error = message
	repository.Archive.Phase = "failed"
	repository.Archive.RecoveryRequired = true
	repository.Archive.RestoreError = message
	report.Repositories[0] = repository
	summarizeDefaultBranch(report)
	if err := persistDefaultBranchReport(*report); err != nil {
		return *report, err
	}
	return *report, errors.New(message)
}

func readDefaultBranchArchiveRestoreReport(path, expectedDigest string) (*Report, error) {
	if !ValidDigest(expectedDigest) {
		return nil, errors.New("--restore-archive-from requires --restore-archive-sha256 with the exact 64-character SHA-256 of that report")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read --restore-archive-from report: %w", err)
	}
	if actual := defaultBranchDigest(raw); !strings.EqualFold(actual, expectedDigest) {
		return nil, fmt.Errorf("--restore-archive-from SHA-256 mismatch: got %s", actual)
	}
	var report Report
	if err := json.Unmarshal(raw, &report); err != nil {
		return nil, fmt.Errorf("decode --restore-archive-from report: %w", err)
	}
	if report.SchemaVersion != defaultBranchSchemaVersion || report.Mode != "apply" {
		return nil, errors.New("--restore-archive-from must be a default-branch apply report from this WB schema")
	}
	return &report, nil
}

func defaultBranchArchiveRestoreTarget(report Report, slug string) (Repository, *Archive, error) {
	for _, repository := range report.Repositories {
		if !strings.EqualFold(repository.Repository, slug) || repository.Archive == nil {
			continue
		}
		transition := *repository.Archive
		if repository.RepositoryID <= 0 || transition.RepositoryID != repository.RepositoryID || !transition.OriginalArchived || !validDefaultBranch(transition.InitialDefault) || !validDefaultBranch(transition.DesiredDefault) || !validDefaultBranchCommit(transition.InitialHead) || (transition.FinalHead != "" && !validDefaultBranchCommit(transition.FinalHead)) {
			return Repository{}, nil, errors.New("--restore-archive-from does not contain a stable archived transition for the selected repository")
		}
		if repository.Desired != "" && repository.Desired != transition.DesiredDefault {
			return Repository{}, nil, errors.New("--restore-archive-from has conflicting desired branch evidence")
		}
		repository.Desired = transition.DesiredDefault
		repository.RepositoryID = transition.RepositoryID
		return repository, &transition, nil
	}
	return Repository{}, nil, errors.New("--restore-archive-from has no archived transition for the selected --repo")
}

func (e *Engine) validateDefaultBranchArchiveRestoreTarget(ctx context.Context, metadata defaultBranchRepoMetadata, slug string, transition *Archive) error {
	if metadata.ID != transition.RepositoryID {
		return errors.New("restore target repository ID differs from the receipt")
	}
	head, err := e.readDefaultBranchRef(ctx, slug, metadata.DefaultBranch)
	if err != nil {
		return fmt.Errorf("read restore target default head: %w", err)
	}
	if metadata.DefaultBranch == transition.InitialDefault && head == transition.InitialHead {
		return nil
	}
	if transition.FinalHead != "" && metadata.DefaultBranch == transition.DesiredDefault && head == transition.FinalHead {
		return nil
	}
	return errors.New("restore target default branch/head pair differs from the receipt")
}

func defaultBranchResumeSource(prior *Report, current Repository) (string, string, string) {
	if prior == nil || current.Desired == "" || current.ObservedDefault != current.Desired {
		return "", "", ""
	}
	for _, previous := range prior.Repositories {
		if !strings.EqualFold(previous.Repository, current.Repository) {
			continue
		}
		if previous.Disposition != "compliant" || previous.ObservedDefault == "" || previous.ObservedDefault == current.Desired || !validDefaultBranch(previous.ObservedDefault) || !validDefaultBranch(previous.Desired) || previous.Desired != current.Desired || previous.VerifiedDefault != current.Desired || previous.OldHead == "" || previous.NewHead != previous.OldHead || !defaultBranchVerifiedMigrationReceipt(previous, current) {
			return "", "", "--reconcile-from does not contain a verified successful migration record for this repository"
		}
		if previous.OldHead != current.OldHead {
			return "", "", "--reconcile-from source SHA no longer matches the refreshed remote default; rerun the audit before reconciling this clone"
		}
		return previous.ObservedDefault, previous.OldHead, ""
	}
	return "", "", "--reconcile-from has no applied migration record for this repository"
}

const (
	defaultBranchLegacyRenamePostProofError = "rename response succeeded but post-read default/head proof did not converge: "
	defaultBranchRenameResponsePendingError = "default-branch mutation response received; awaiting visibility convergence"
)

// defaultBranchLegacyRenameResume accepts only the v1 receipt shape produced
// after GitHub accepted a rename but had not yet made it visible to the
// immediate post-read. The caller has already bound prior to exact report
// bytes through --reconcile-sha256.
func (e *Engine) defaultBranchLegacyRenameResume(ctx context.Context, prior *Report, current Repository) (string, string, string) {
	if prior == nil || prior.SchemaVersion != 1 || prior.Mode != "apply" || current.Disposition != "compliant" || current.ObservedDefault != current.Desired || current.OldHead == "" {
		return "", "", "--reconcile-from does not contain a verified successful migration record for this repository"
	}
	for _, previous := range prior.Repositories {
		if !strings.EqualFold(previous.Repository, current.Repository) {
			continue
		}
		if !defaultBranchLegacyRenamePendingRecord(previous) || previous.Desired != current.Desired || previous.OldHead != current.OldHead {
			return "", "", "--reconcile-from does not contain the exact failed post-rename proof record for this repository"
		}
		_, err := e.GitHub.Read(ctx, "repos/"+current.Repository+"/git/ref/heads/"+url.PathEscape(previous.ObservedDefault))
		if err == nil {
			return "", "", "--reconcile-from old source ref still exists; do not infer a completed rename"
		}
		if !isDefaultBranchNotFound(err) {
			return "", "", "--reconcile-from could not prove the old source ref is absent: " + err.Error()
		}
		return previous.ObservedDefault, previous.OldHead, ""
	}
	return "", "", "--reconcile-from has no applied migration record for this repository"
}

func (e *Engine) defaultBranchPagesAutomaticResume(ctx context.Context, prior *Report, current Repository) (string, string, string) {
	if prior == nil || prior.SchemaVersion != 1 || prior.Mode != "apply" || current.Disposition != "compliant" || current.Desired != "main" || current.ObservedDefault != current.Desired {
		return "", "", "--reconcile-from does not contain a verified automatic Pages transition record"
	}
	for _, previous := range prior.Repositories {
		if !strings.EqualFold(previous.Repository, current.Repository) {
			continue
		}
		p := previous.PagesBefore
		if previous.Disposition != "error" || previous.Error != "Pages source changed after planning; WB will not overwrite it" || !previous.RenameAccepted || previous.TargetExists || previous.VerifiedDefault != current.Desired || previous.NewHead != previous.OldHead || !validDefaultBranchCommit(previous.OldHead) || previous.OldHead != current.OldHead || previous.ObservedDefault != "master" || previous.Desired != current.Desired || previous.RepositoryID == 0 || previous.RepositoryID != current.RepositoryID || p == nil || p.BuildType != "legacy" || p.Branch != "master" || !validDefaultBranchPagesPath(p.Path) || previous.PagesPhase != "prepared" || previous.PagesAccepted || previous.PagesAfter != nil || !slicesEqual(previous.Actions, []string{"renamed master to " + current.Desired, "verified default branch and head"}) {
			return "", "", "--reconcile-from does not contain the exact automatic Pages transition record"
		}
		_, err := e.GitHub.Read(ctx, "repos/"+current.Repository+"/git/ref/heads/master")
		if err == nil {
			return "", "", "--reconcile-from old source ref still exists; do not infer a completed rename"
		}
		if !isDefaultBranchNotFound(err) {
			return "", "", "--reconcile-from could not prove the old source ref is absent: " + err.Error()
		}
		body, err := e.GitHub.Read(ctx, "repos/"+current.Repository+"/pages")
		if err != nil {
			return "", "", "--reconcile-from could not read Pages source: " + err.Error()
		}
		pages, err := decodeDefaultBranchPages(body)
		if err != nil || pages.BuildType != "legacy" || pages.Branch != current.Desired || pages.Path != p.Path {
			return "", "", "--reconcile-from Pages source does not prove the automatic transition"
		}
		return previous.ObservedDefault, previous.OldHead, ""
	}
	return "", "", "--reconcile-from has no automatic Pages transition record for this repository"
}

func defaultBranchLegacyRenamePendingRecord(previous Repository) bool {
	postProofFailure := previous.Disposition == "error" && previous.Error == defaultBranchLegacyRenamePostProofError
	postResponsePending := previous.RenameAccepted && (previous.Disposition == "pending" || previous.Disposition == "error")
	return (postProofFailure || postResponsePending) && !previous.TargetExists && previous.NewHead == "" && previous.VerifiedDefault == "" && len(previous.Actions) == 0 && previous.ObservedDefault != previous.Desired && validDefaultBranch(previous.ObservedDefault) && validDefaultBranch(previous.Desired) && validDefaultBranchCommit(previous.OldHead)
}

func recordDefaultBranchLegacyRenameProof(repository *Repository, sourceDefault, sourceHead, receiptPath, receiptSHA256 string) {
	repository.ObservedDefault = sourceDefault
	repository.VerifiedDefault = repository.Desired
	repository.OldHead = sourceHead
	repository.NewHead = sourceHead
	repository.Disposition = "compliant"
	repository.Error = ""
	repository.RecoveredFrom = receiptPath
	repository.RecoveredSHA256 = receiptSHA256
	repository.Actions = []string{"verified prior rename " + sourceDefault + " to " + repository.Desired, "verified current default branch and head"}
}

func validDefaultBranchCommit(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') && (character < 'A' || character > 'F') {
			return false
		}
	}
	return true
}

func defaultBranchMigrationActionsVerified(repository Repository) bool {
	verified := []string{"verified default branch and head"}
	rename := append([]string{"renamed " + repository.ObservedDefault + " to " + repository.Desired}, verified...)
	switchDefault := append([]string{"set default branch to existing same-SHA " + repository.Desired}, verified...)
	prior := []string{"verified prior rename " + repository.ObservedDefault + " to " + repository.Desired, "verified current default branch and head"}
	return slicesEqual(repository.Actions, rename) || slicesEqual(repository.Actions, switchDefault) || (repository.RecoveredFrom != "" && ValidDigest(repository.RecoveredSHA256) && slicesEqual(repository.Actions, prior))
}

// defaultBranchVerifiedMigrationReceipt accepts the normal completed migration
// record, or the strictly terminal form emitted by --temporarily-unarchive.
// The latter needs additional identity and archive-state evidence because its
// action list ends with the restored-archive checkpoint.
func defaultBranchVerifiedMigrationReceipt(previous, current Repository) bool {
	if previous.Archive == nil {
		if previous.Archived || current.Archived {
			return false
		}
		if previous.PagesBefore != nil {
			return defaultBranchVerifiedPagesMigrationReceipt(previous, current)
		}
		return defaultBranchMigrationActionsVerified(previous)
	}
	transition := previous.Archive
	if previous.RepositoryID <= 0 || current.RepositoryID != previous.RepositoryID || previous.Archived != current.Archived || !previous.Archived || previous.Fork != current.Fork ||
		transition.RepositoryID != previous.RepositoryID || !transition.OriginalArchived || transition.InitialDefault != previous.ObservedDefault || transition.DesiredDefault != current.Desired || transition.InitialHead != previous.OldHead || transition.FinalHead != previous.NewHead || transition.Phase != "restored" || !transition.UnarchiveAccepted || !transition.RestoreAccepted || transition.RecoveryRequired || transition.RestoreError != "" || !validDefaultBranchCommit(previous.OldHead) || previous.NewHead != previous.OldHead {
		return false
	}
	if len(previous.Actions) < 2 || previous.Actions[len(previous.Actions)-1] != "restored archived state" {
		return false
	}
	base := previous
	base.Actions = append([]string(nil), previous.Actions[:len(previous.Actions)-1]...)
	return defaultBranchMigrationActionsVerified(base)
}

func defaultBranchVerifiedPagesMigrationReceipt(previous, current Repository) bool {
	before, after, observed := previous.PagesBefore, previous.PagesAfter, current.PagesAfter
	if previous.RepositoryID <= 0 || previous.RepositoryID != current.RepositoryID || previous.PagesPhase != "verified" || before == nil || after == nil || observed == nil || *after != *observed || before.BuildType != "legacy" || before.Branch != previous.ObservedDefault || !validDefaultBranchPagesPath(before.Path) || after.BuildType != "legacy" || after.Branch != previous.Desired || after.Path != before.Path {
		return false
	}
	base := []string{"renamed " + previous.ObservedDefault + " to " + previous.Desired, "verified default branch and head"}
	if previous.TargetExists {
		base[0] = "set default branch to existing same-SHA " + previous.Desired
	}
	if previous.PagesAccepted {
		return slicesEqual(previous.Actions, append(base, "migrated Pages source to "+previous.Desired+" with path "+before.Path, "verified Pages source"))
	}
	return previous.ObservedDefault == "master" && previous.Desired == "main" && slicesEqual(previous.Actions, append(base, "verified GitHub automatic Pages source transition to main with path "+before.Path))
}

func slicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func ValidDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func defaultBranchDigest(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

// githubCommandMessage mirrors cmd/wb's helper of the same name (kept in
// cmd/wb/fleet_merge_policy.go, a different task-22 file this lane does not
// touch). It is duplicated here rather than exported from cmd/wb, since
// cmd/wb may not be imported back into internal/.
func githubCommandMessage(response githubobserver.CommandResponse) string {
	value := strings.TrimSpace(string(response.Stderr))
	if value == "" {
		value = strings.TrimSpace(string(response.Stdout))
	}
	if value == "" && response.Err != nil {
		value = response.Err.Error()
	}
	return value
}

type defaultBranchLocalCloneSet struct {
	Eligible map[string][]discover.Repo
	Blocked  map[string][]Canonical
}

func attachDefaultBranchLocalBlockers(report *Report, blockers map[string][]Canonical) {
	for i := range report.Repositories {
		key := strings.ToLower(report.Repositories[i].Repository)
		report.Repositories[i].CanonicalClones = append(report.Repositories[i].CanonicalClones, blockers[key]...)
	}
}

func (e *Engine) defaultBranchLocalClones(projectsRoot, filter string) (defaultBranchLocalCloneSet, error) {
	result := defaultBranchLocalCloneSet{Eligible: map[string][]discover.Repo{}, Blocked: map[string][]Canonical{}}
	if strings.TrimSpace(projectsRoot) == "" {
		return result, nil
	}
	local, err := discover.ScanLocal(projectsRoot)
	if err != nil {
		return result, fmt.Errorf("scan local canonical clones: %w", err)
	}
	for _, clone := range local {
		if filter != "" && !strings.Contains(clone.Slug(), filter) {
			continue
		}
		if clone.Host != "" && !strings.EqualFold(clone.Host, "github.com") {
			continue
		}
		origin, err := e.Git.Run(context.Background(), clone.Path, "remote", "get-url", "origin")
		if err != nil {
			if strings.EqualFold(clone.Host, "github.com") {
				key := strings.ToLower(clone.Slug())
				result.Blocked[key] = append(result.Blocked[key], Canonical{Path: clone.Path, Disposition: "error", Error: "read canonical origin: " + err.Error()})
			}
			continue
		}
		remote, err := gitremote.Parse(origin)
		if err != nil || remote.Identity.Host() != "github.com" || !strings.EqualFold(remote.Identity.Repository, clone.Slug()) {
			if strings.EqualFold(clone.Host, "github.com") {
				key := strings.ToLower(clone.Slug())
				message := "canonical origin does not prove github.com/" + clone.Slug()
				if err != nil {
					message += ": " + err.Error()
				}
				result.Blocked[key] = append(result.Blocked[key], Canonical{Path: clone.Path, Disposition: "blocked", Error: message})
			}
			continue
		}
		key := strings.ToLower(clone.Slug())
		result.Eligible[key] = append(result.Eligible[key], clone)
	}
	for key := range result.Eligible {
		sort.Slice(result.Eligible[key], func(i, j int) bool { return result.Eligible[key][i].Path < result.Eligible[key][j].Path })
	}
	return result, nil
}

func loadDefaultBranchConfig(path string) (defaultBranchConfig, error) {
	var cfg defaultBranchConfig
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("read WB config: %w", err)
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("parse WB config: %w", err)
	}
	return cfg, nil
}
func effectiveDefaultBranch(explicit string, cfg defaultBranchConfig, org string) string {
	if value := strings.TrimSpace(explicit); value != "" {
		return value
	}
	for configuredOwner, configured := range cfg.Fleet.Organizations {
		if strings.EqualFold(strings.TrimSpace(configuredOwner), strings.TrimSpace(org)) {
			if value := strings.TrimSpace(configured.DefaultBranch); value != "" {
				return value
			}
		}
	}
	return strings.TrimSpace(cfg.Fleet.DefaultBranch)
}
func (e *Engine) discoverDefaultBranchFleet(filter string, owners, exact []string, includeUser, allOrgs bool) ([]discover.Repo, []Repository, error) {
	if len(exact) > 0 {
		repos := make([]discover.Repo, 0, len(exact))
		seen := map[string]bool{}
		for _, slug := range exact {
			owner, name, ok := strings.Cut(strings.TrimSpace(slug), "/")
			if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
				return nil, nil, fmt.Errorf("invalid --repo %q; use owner/repository", slug)
			}
			key := strings.ToLower(owner + "/" + name)
			if seen[key] || (filter != "" && !strings.Contains(owner+"/"+name, filter)) {
				continue
			}
			seen[key] = true
			repos = append(repos, discover.Repo{Org: owner, Name: name, Remote: true})
		}
		return repos, nil, nil
	}
	selected := map[string]bool{}
	if len(owners) == 0 && !includeUser {
		if !allOrgs {
			user, err := e.Discovery.AuthUser()
			if err != nil {
				return nil, nil, err
			}
			selected[user] = true
		}
		orgs, err := e.Discovery.MemberOrgs()
		if err != nil {
			return nil, nil, err
		}
		for _, org := range orgs {
			selected[org] = true
		}
	}
	if includeUser {
		user, err := e.Discovery.AuthUser()
		if err != nil {
			return nil, nil, err
		}
		selected[user] = true
	}
	for _, org := range owners {
		if org = strings.TrimSpace(org); org != "" {
			selected[org] = true
		}
	}
	names := make([]string, 0, len(selected))
	for owner := range selected {
		names = append(names, owner)
	}
	sort.Strings(names)
	var repos []discover.Repo
	var failures []Repository
	seen := map[string]bool{}
	for _, owner := range names {
		listed, err := e.Discovery.ListRemote(owner)
		if err != nil {
			failures = append(failures, Repository{Repository: owner + "/*", Disposition: "error", Error: "list GitHub repositories: " + err.Error()})
			continue
		}
		if len(listed) >= 1000 {
			failures = append(failures, Repository{Repository: owner + "/*", Disposition: "error", Error: "GitHub repository listing reached 1000 entries and may be incomplete; WB will not audit or apply a partial owner scope"})
			continue
		}
		for _, repo := range listed {
			key := strings.ToLower(repo.Slug())
			if seen[key] || (filter != "" && !strings.Contains(repo.Slug(), filter)) {
				continue
			}
			seen[key] = true
			repo.Remote = true
			repos = append(repos, repo)
		}
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].Slug() < repos[j].Slug() })
	sort.Slice(failures, func(i, j int) bool { return failures[i].Repository < failures[j].Repository })
	return repos, failures, nil
}
func (e *Engine) inspectDefaultBranchWithOptions(ctx context.Context, repo discover.Repo, desired string, temporarilyUnarchive, migratePagesSource bool, rewriteWorkflowTriggers ...bool) Repository {
	rewriteWorkflows := len(rewriteWorkflowTriggers) > 0 && rewriteWorkflowTriggers[0]
	result := Repository{Repository: repo.Slug(), Desired: desired}
	if !validDefaultBranch(desired) {
		result.Disposition = "blocked"
		result.Error = "invalid desired branch: set fleet.default_branch, fleet.organizations.<owner>.default_branch, or --branch to a Git ref name"
		return result
	}
	body, err := e.GitHub.Read(ctx, "repos/"+repo.Slug())
	if err != nil {
		result.Disposition = "error"
		result.Error = err.Error()
		return result
	}
	var meta defaultBranchRepoMetadata
	if err := json.Unmarshal(body, &meta); err != nil {
		result.Disposition = "error"
		result.Error = "decode repository metadata: " + err.Error()
		return result
	}
	result.RepositoryID, result.ObservedDefault, result.Archived, result.Fork = meta.ID, meta.DefaultBranch, meta.Archived, meta.Fork
	if strings.TrimSpace(meta.DefaultBranch) == "" {
		result.Disposition = "blocked"
		result.Error = "empty repository has no default branch; create and push the desired branch first"
		return result
	}
	if !validDefaultBranch(meta.DefaultBranch) {
		result.Disposition = "error"
		result.Error = "GitHub returned an invalid default branch ref"
		return result
	}
	oldRef, err := e.readDefaultBranchRef(ctx, repo.Slug(), meta.DefaultBranch)
	if err != nil {
		if meta.Size == 0 && isDefaultBranchNotFound(err) {
			result.Disposition = "blocked"
			result.Error = "empty repository has no initial commit on its advertised default branch; create and push the desired branch first"
			return result
		}
		result.Disposition = "error"
		result.Error = err.Error()
		return result
	}
	result.OldHead = oldRef
	if meta.DefaultBranch == desired {
		result.NewHead = oldRef
		if migratePagesSource {
			e.inspectDefaultBranchPagesAtDesired(ctx, &result, meta)
			return result
		}
		result.Disposition = "compliant"
		return result
	}
	if meta.Archived && !temporarilyUnarchive {
		result.Disposition = "blocked"
		result.Error = "archived repository: GitHub makes archived repositories read-only; WB does not temporarily unarchive it"
		return result
	}
	if meta.Archived {
		if meta.ID <= 0 {
			result.Disposition = "blocked"
			result.Error = "archived repository did not provide a stable numeric ID; WB will not temporarily unarchive it"
			return result
		}
		result.Archive = &Archive{RepositoryID: meta.ID, OriginalArchived: true, InitialDefault: meta.DefaultBranch, DesiredDefault: desired, InitialHead: oldRef, Phase: "prepared"}
	}
	newRef, err := e.readDefaultBranchRef(ctx, repo.Slug(), desired)
	if err == nil {
		result.TargetExists = true
		result.NewHead = newRef
		if newRef != oldRef {
			result.Disposition = "blocked"
			result.Error = "target branch exists at a different SHA; no overwrite or promotion is safe"
			return result
		}
		result.Impacts = append(result.Impacts, "target exists at same SHA; default switch would require protection/ruleset equivalence")
	} else if !strings.Contains(strings.ToLower(err.Error()), "http 404") {
		result.Disposition = "error"
		result.Error = err.Error()
		return result
	}
	if err := e.defaultBranchSafetyWithOptions(ctx, &result, meta, meta.DefaultBranch, migratePagesSource, rewriteWorkflows); err != nil {
		result.Disposition = "blocked"
		result.Error = err.Error()
		return result
	}
	result.Disposition = "drift"
	return result
}
func (e *Engine) readDefaultBranchRef(ctx context.Context, slug, branch string) (string, error) {
	body, err := e.GitHub.Read(ctx, "repos/"+slug+"/branches/"+url.PathEscape(branch))
	if err != nil {
		return "", err
	}
	var ref defaultBranchRef
	if err := json.Unmarshal(body, &ref); err != nil || ref.Commit.SHA == "" {
		return "", fmt.Errorf("decode branch %s: %w", branch, err)
	}
	return ref.Commit.SHA, nil
}
func (e *Engine) defaultBranchSafetyWithOptions(ctx context.Context, result *Repository, meta defaultBranchRepoMetadata, old string, migratePagesSource bool, rewriteWorkflowTriggers ...bool) error {
	rewriteWorkflows := len(rewriteWorkflowTriggers) > 0 && rewriteWorkflowTriggers[0]
	slug := result.Repository
	headOwner := strings.Split(slug, "/")[0]
	querySlugs := []string{slug}
	if meta.Fork {
		if meta.Parent == nil || !validDefaultBranchRepository(meta.Parent.FullName) {
			return errors.New("fork parent metadata is missing; WB cannot inventory outgoing pull requests")
		}
		querySlugs = append(querySlugs, meta.Parent.FullName)
		result.Impacts = append(result.Impacts, "fork and parent outbound pull-request inventories checked")
	}
	for _, querySlug := range querySlugs {
		pulls, err := e.GitHub.Read(ctx, "repos/"+querySlug+"/pulls?state=open&head="+url.QueryEscape(headOwner+":"+old))
		if err != nil {
			return fmt.Errorf("list open source-default pull requests in %s: %w", querySlug, err)
		}
		var open []json.RawMessage
		if err := json.Unmarshal(pulls, &open); err != nil {
			return fmt.Errorf("decode open source-default pull requests in %s: %w", querySlug, err)
		}
		if len(open) > 0 {
			return fmt.Errorf("open pull request in %s uses %q as head; GitHub closes it when that branch is renamed", querySlug, old)
		}
	}
	if err := e.inspectDefaultBranchWorkflows(ctx, result, old, result.Desired); err != nil {
		return err
	}
	if len(result.WorkflowFiles) > 0 && !rewriteWorkflows {
		return fmt.Errorf("workflow trigger references %q; pass --rewrite-workflow-triggers only after reviewing the proposed byte-preserving replacements", old)
	}
	if err := e.inspectDefaultBranchPages(ctx, result, old, migratePagesSource); err != nil {
		return err
	}
	for _, endpoint := range []string{"repos/" + slug + "/branches/" + url.PathEscape(old) + "/protection"} {
		body, err := e.GitHub.Read(ctx, endpoint)
		if err == nil && len(strings.TrimSpace(string(body))) > 0 {
			result.Impacts = append(result.Impacts, "inspect before apply: "+endpoint)
			return fmt.Errorf("pages, classic protection, or effective rules require an explicit migration; WB will not weaken or assume renamed coverage")
		}
		if err != nil && !isDefaultBranchNotFound(err) {
			return fmt.Errorf("inspect branch impact: %w", err)
		}
	}
	rulesEndpoint := "repos/" + slug + "/rules/branches/" + url.PathEscape(old) + "?per_page=100"
	rules, err := e.GitHub.Read(ctx, rulesEndpoint)
	if err != nil {
		if !isDefaultBranchNotFound(err) {
			return fmt.Errorf("inspect branch impact: %w", err)
		}
		return nil
	}
	var effectiveRules []json.RawMessage
	if err := json.Unmarshal(rules, &effectiveRules); err != nil {
		return fmt.Errorf("decode effective branch rules: %w", err)
	}
	if len(effectiveRules) > 0 {
		result.Impacts = append(result.Impacts, "inspect before apply: "+rulesEndpoint)
		return errors.New("pages, classic protection, or effective rules require an explicit migration; WB will not weaken or assume renamed coverage")
	}
	return nil
}

func isDefaultBranchNotFound(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "404")
}

func (e *Engine) inspectDefaultBranchPages(ctx context.Context, result *Repository, old string, migrate bool) error {
	body, err := e.GitHub.Read(ctx, "repos/"+result.Repository+"/pages")
	if isDefaultBranchNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect Pages source: %w", err)
	}
	pages, err := decodeDefaultBranchPages(body)
	if err != nil {
		return err
	}
	result.Impacts = append(result.Impacts, "inspect before apply: repos/"+result.Repository+"/pages")
	if !migrate {
		return errors.New("pages, classic protection, or effective rules require an explicit migration; WB will not weaken or assume renamed coverage")
	}
	if pages.BuildType != "legacy" || pages.Branch != old || !validDefaultBranchPagesPath(pages.Path) {
		return errors.New("pages source is not a complete legacy source on the observed default branch; WB will not change it")
	}
	result.PagesBefore, result.PagesPhase = &pages, "prepared"
	return nil
}

func (e *Engine) inspectDefaultBranchPagesAtDesired(ctx context.Context, result *Repository, meta defaultBranchRepoMetadata) {
	body, err := e.GitHub.Read(ctx, "repos/"+result.Repository+"/pages")
	if isDefaultBranchNotFound(err) {
		result.Disposition = "compliant"
		return
	}
	if err != nil {
		result.Disposition, result.Error = "error", "inspect Pages source: "+err.Error()
		return
	}
	pages, err := decodeDefaultBranchPages(body)
	if err != nil {
		result.Disposition, result.Error = "blocked", err.Error()
		return
	}
	if pages.BuildType == "legacy" && pages.Branch == meta.DefaultBranch && validDefaultBranchPagesPath(pages.Path) {
		result.PagesAfter = &pages
		result.Disposition = "compliant"
		return
	}
	result.PagesBefore, result.PagesPhase = &pages, "unfinished"
	result.Disposition = "drift"
	result.Error = "default branch already matches desired branch, but Pages source migration remains unfinished; WB will not infer the prior default branch"
}

func decodeDefaultBranchPages(body []byte) (PagesSource, error) {
	var value struct {
		BuildType string `json:"build_type"`
		Source    struct {
			Branch string `json:"branch"`
			Path   string `json:"path"`
		} `json:"source"`
	}
	if err := json.Unmarshal(body, &value); err != nil {
		return PagesSource{}, fmt.Errorf("decode Pages source: %w", err)
	}
	return PagesSource{BuildType: value.BuildType, Branch: value.Source.Branch, Path: value.Source.Path}, nil
}

func validDefaultBranchPagesPath(path string) bool { return path == "/" || path == "/docs" }

func defaultBranchPagesTerminal(repository Repository) bool {
	return repository.PagesBefore == nil || (repository.PagesPhase == "verified" && repository.PagesAfter != nil)
}

func validDefaultBranchRepository(slug string) bool {
	owner, name, ok := strings.Cut(slug, "/")
	return ok && owner != "" && name != "" && !strings.Contains(name, "/")
}

func workflowReferencesDefaultBranch(contents, branch string) bool {
	escaped := regexp.QuoteMeta(branch)
	// A standalone branch token catches quoted and multiline YAML, raw ref
	// URLs, and actions refs. It intentionally also catches comments and other
	// free-form occurrences: a false positive is reviewable, while a missed
	// reference could be broken by the branch rename.
	return regexp.MustCompile(`(?mi)(^|[^[:alnum:]_.-])` + escaped + `($|[^[:alnum:]_.-])`).MatchString(contents)
}

// workflowReferencesRepositoryDefaultBranch retains the conservative reference
// check, except for an exact ref segment in a raw.githubusercontent.com URL
// belonging to another repository. That URL cannot be changed by renaming this
// repository's default branch and is deliberately left byte-for-byte intact.
func workflowReferencesRepositoryDefaultBranch(contents, branch, repository string) bool {
	masked := []byte(contents)
	for _, match := range rawGitHubContentURL.FindAllStringSubmatchIndex(contents, -1) {
		owner, name, ref := contents[match[2]:match[3]], contents[match[4]:match[5]], contents[match[6]:match[7]]
		if ref != branch || strings.EqualFold(owner+"/"+name, repository) {
			continue
		}
		for i := match[6]; i < match[7]; i++ {
			masked[i] = '_'
		}
	}
	return workflowReferencesDefaultBranch(string(masked), branch)
}

var rawGitHubContentURL = regexp.MustCompile(`https://raw\.githubusercontent\.com/([[:alnum:]_.-]+)/([[:alnum:]_.-]+)/([[:alnum:]_.-]+)/`)

// rewriteWorkflowBranchTriggers accepts only plain scalar list members under
// on.<push|pull_request>.branches. It deliberately refuses every other old
// branch token, including comments, URLs, action refs, expressions, anchors,
// aliases. It also accepts a fully plain flow-style list under the same two
// trigger nodes. Splitting with After preserves the original
// line terminators and every byte outside the scalar token.
func rewriteWorkflowBranchTriggers(contents, old, desired string) (string, bool) {
	lines := strings.SplitAfter(contents, "\n")
	onIndent, eventIndent, eventChildIndent, branchesIndent := -1, -1, -1, -1
	changed := false
	for i, raw := range lines {
		line, ending := raw, ""
		if strings.HasSuffix(line, "\n") {
			line, ending = strings.TrimSuffix(line, "\n"), "\n"
		}
		if strings.HasSuffix(line, "\r") {
			line, ending = strings.TrimSuffix(line, "\r"), "\r"+ending
		}
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		trim := strings.TrimSpace(line)
		if indent == 0 && trim == "on:" {
			onIndent, eventIndent, eventChildIndent, branchesIndent = indent, -1, -1, -1
			continue
		}
		if onIndent < 0 {
			continue
		}
		if indent <= onIndent && trim != "" {
			onIndent, eventIndent, eventChildIndent, branchesIndent = -1, -1, -1, -1
			continue
		}
		if eventIndent >= 0 && indent <= eventIndent && trim != "" {
			eventIndent, eventChildIndent, branchesIndent = -1, -1, -1
		}
		if eventIndent < 0 && indent > onIndent && (trim == "push:" || trim == "pull_request:") {
			eventIndent, eventChildIndent = indent, -1
			continue
		}
		if eventIndent < 0 {
			continue
		}
		if branchesIndent >= 0 {
			if indent <= branchesIndent && trim != "" {
				branchesIndent = -1
			} else if indent > branchesIndent {
				prefix := line[:indent]
				if strings.TrimSpace(line) == "- "+old || strings.TrimSpace(line) == "-"+old {
					middle := strings.TrimLeft(line[indent:], " \t")
					if strings.HasPrefix(middle, "- ") {
						lines[i] = prefix + "- " + desired + ending
						changed = true
					}
				}
				continue
			}
		}
		if eventChildIndent < 0 && indent > eventIndent && trim != "" {
			eventChildIndent = indent
		}
		if indent != eventChildIndent {
			continue
		}
		if branchesIndent < 0 {
			if trim == "branches:" {
				branchesIndent = indent
				continue
			}
			if rewritten, replaced := rewriteWorkflowFlowBranchList(line, indent, old, desired); replaced {
				lines[i] = rewritten + ending
				changed = true
				continue
			}
		}
	}
	return strings.Join(lines, ""), changed
}

// rewriteWorkflowFlowBranchList changes one exact plain scalar in
// "branches: [ ... ]". It rejects comments, quotes, YAML decorations, and
// expressions by accepting only a complete list of unquoted branch scalars.
func rewriteWorkflowFlowBranchList(line string, indent int, old, desired string) (string, bool) {
	trim := strings.TrimSpace(line)
	if !strings.HasPrefix(trim, "branches:") {
		return line, false
	}
	value := strings.TrimSpace(strings.TrimPrefix(trim, "branches:"))
	if len(value) < 2 || value[0] != '[' || value[len(value)-1] != ']' {
		return line, false
	}
	items := value[1 : len(value)-1]
	if items == "" || strings.ContainsAny(items, "#'\"&*!${}[]") {
		return line, false
	}
	valueStart := strings.Index(line[indent:], "[")
	if valueStart < 0 {
		return line, false
	}
	valueStart += indent + 1
	type replacement struct{ start, end int }
	var replacements []replacement
	for offset, remaining := 0, items; ; {
		item := remaining
		if comma := strings.IndexByte(remaining, ','); comma >= 0 {
			item = remaining[:comma]
		}
		scalar := strings.TrimSpace(item)
		if !workflowPlainFlowBranchScalar(scalar) {
			return line, false
		}
		if scalar == old {
			itemStart := valueStart + offset
			leading := len(item) - len(strings.TrimLeft(item, " \t"))
			replacements = append(replacements, replacement{start: itemStart + leading, end: itemStart + leading + len(scalar)})
		}
		comma := strings.IndexByte(remaining, ',')
		if comma < 0 {
			break
		}
		offset += comma + 1
		remaining = remaining[comma+1:]
	}
	if len(replacements) == 0 {
		return line, false
	}
	var rewritten strings.Builder
	rewritten.Grow(len(line) + len(replacements)*(len(desired)-len(old)))
	previous := 0
	for _, replacement := range replacements {
		rewritten.WriteString(line[previous:replacement.start])
		rewritten.WriteString(desired)
		previous = replacement.end
	}
	rewritten.WriteString(line[previous:])
	return rewritten.String(), true
}

func workflowPlainFlowBranchScalar(value string) bool {
	if value == "" {
		return false
	}
	lower := strings.ToLower(value)
	switch lower {
	case "true", "false", "null", "~", "yes", "no", "on", "off", ".nan", ".inf", "-.inf", "+.inf":
		return false
	}
	hasLetter := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			hasLetter = true
			continue
		}
		if (r >= '0' && r <= '9') || strings.ContainsRune("._/-", r) {
			continue
		}
		return false
	}
	return hasLetter
}

func (e *Engine) inspectDefaultBranchWorkflows(ctx context.Context, result *Repository, old, desired string) error {
	workflows, err := e.GitHub.Read(ctx, "repos/"+result.Repository+"/contents/.github/workflows?ref="+url.QueryEscape(old))
	if isDefaultBranchNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("list workflows at source branch: %w", err)
	}
	var listing []struct {
		Path string `json:"path"`
		SHA  string `json:"sha"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal(workflows, &listing); err != nil {
		return fmt.Errorf("decode workflow listing: %w", err)
	}
	for _, entry := range listing {
		if entry.Type != "file" || (!strings.HasSuffix(entry.Path, ".yml") && !strings.HasSuffix(entry.Path, ".yaml")) {
			continue
		}
		blob, err := e.GitHub.Read(ctx, "repos/"+result.Repository+"/git/blobs/"+entry.SHA)
		if err != nil {
			return fmt.Errorf("read workflow %s: %w", entry.Path, err)
		}
		var value struct {
			Content  string `json:"content"`
			Encoding string `json:"encoding"`
		}
		if err := json.Unmarshal(blob, &value); err != nil {
			return fmt.Errorf("decode workflow %s: %w", entry.Path, err)
		}
		if value.Encoding != "base64" {
			return fmt.Errorf("workflow %s uses unsupported content encoding %q", entry.Path, value.Encoding)
		}
		contents, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(value.Content, "\n", ""))
		if err != nil {
			return fmt.Errorf("decode workflow %s content: %w", entry.Path, err)
		}
		rewritten, changed := rewriteWorkflowBranchTriggers(string(contents), old, desired)
		if workflowReferencesRepositoryDefaultBranch(rewritten, old, result.Repository) {
			result.Impacts = append(result.Impacts, "workflow old-branch reference: "+entry.Path)
			return fmt.Errorf("workflow %s has an unsupported reference to %q; WB will not rewrite it", entry.Path, old)
		}
		if changed {
			result.WorkflowFiles = append(result.WorkflowFiles, Workflow{Path: entry.Path, BlobBefore: entry.SHA, SHA256Before: defaultBranchDigest(contents), SHA256After: defaultBranchDigest([]byte(rewritten)), contents: string(contents), rewritten: rewritten})
		}
	}
	return nil
}

func (e *Engine) verifyDefaultBranchWorkflowBytes(ctx context.Context, repository, branch, old string, planned []Workflow) error {
	listingBody, err := e.GitHub.Read(ctx, "repos/"+repository+"/contents/.github/workflows?ref="+url.QueryEscape(branch))
	if err != nil {
		return fmt.Errorf("list workflows after commit: %w", err)
	}
	var listing []struct {
		Path string `json:"path"`
		SHA  string `json:"sha"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal(listingBody, &listing); err != nil {
		return fmt.Errorf("decode workflows after commit: %w", err)
	}
	byPath := map[string]string{}
	for _, entry := range listing {
		if entry.Type == "file" {
			byPath[entry.Path] = entry.SHA
		}
	}
	for _, plannedFile := range planned {
		sha := byPath[plannedFile.Path]
		if sha == "" {
			return fmt.Errorf("workflow %s is missing after commit", plannedFile.Path)
		}
		blob, err := e.GitHub.Read(ctx, "repos/"+repository+"/git/blobs/"+sha)
		if err != nil {
			return fmt.Errorf("read workflow %s after commit: %w", plannedFile.Path, err)
		}
		var value struct {
			Content  string `json:"content"`
			Encoding string `json:"encoding"`
		}
		if err := json.Unmarshal(blob, &value); err != nil || value.Encoding != "base64" {
			return fmt.Errorf("decode workflow %s after commit", plannedFile.Path)
		}
		contents, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(value.Content, "\n", ""))
		if err != nil {
			return fmt.Errorf("decode workflow %s bytes after commit: %w", plannedFile.Path, err)
		}
		if defaultBranchDigest(contents) != plannedFile.SHA256After {
			return fmt.Errorf("workflow %s bytes differ from the planned replacement", plannedFile.Path)
		}
		if workflowReferencesRepositoryDefaultBranch(string(contents), old, repository) {
			return fmt.Errorf("workflow %s still references %q after commit", plannedFile.Path, old)
		}
	}
	return nil
}

const defaultBranchWorkflowMutation = `mutation($branch: CommittableBranch!, $expected: GitObjectID!, $message: CommitMessage!, $additions: [FileAddition!]!) { createCommitOnBranch(input: {branch: $branch, expectedHeadOid: $expected, message: $message, fileChanges: {additions: $additions}}) { commit { oid } } }`

// applyDefaultBranchWorkflowTriggers creates one CAS-protected commit before
// the branch rename. It rereads every planned blob and the source head before
// submitting the mutation; a failed or ambiguous response is accepted only
// after the exact new head and all replacement hashes are observed.
func (e *Engine) applyDefaultBranchWorkflowTriggers(ctx context.Context, repo Repository, checkpoint func(Repository) error) Repository {
	owner, name, ok := strings.Cut(repo.Repository, "/")
	if !ok || owner == "" || name == "" {
		repo.Disposition, repo.Error = "error", "invalid repository observation"
		return repo
	}
	fresh := e.inspectDefaultBranchWithOptions(ctx, discover.Repo{Org: owner, Name: name}, repo.Desired, false, false, true)
	if fresh.Disposition != "drift" || fresh.ObservedDefault != repo.ObservedDefault || fresh.OldHead != repo.OldHead || len(fresh.WorkflowFiles) != len(repo.WorkflowFiles) {
		repo.Disposition, repo.Error = "blocked", "workflow source changed after planning; rerun the audit"
		return repo
	}
	for i := range repo.WorkflowFiles {
		if fresh.WorkflowFiles[i].Path != repo.WorkflowFiles[i].Path || fresh.WorkflowFiles[i].BlobBefore != repo.WorkflowFiles[i].BlobBefore || fresh.WorkflowFiles[i].SHA256Before != repo.WorkflowFiles[i].SHA256Before || fresh.WorkflowFiles[i].SHA256After != repo.WorkflowFiles[i].SHA256After {
			repo.Disposition, repo.Error = "blocked", "workflow bytes changed after planning; rerun the audit"
			return repo
		}
	}
	repo.WorkflowPhase, repo.Error = "pending", "workflow commit pending"
	if checkpoint != nil {
		if err := checkpoint(repo); err != nil {
			repo.Disposition, repo.Error = "error", "persist pending workflow commit: "+err.Error()
			return repo
		}
	}
	args := []string{"api", "graphql", "-f", "query=" + defaultBranchWorkflowMutation, "-F", "branch[repositoryNameWithOwner]=" + repo.Repository, "-F", "branch[branchName]=" + repo.ObservedDefault, "-F", "expected=" + repo.OldHead, "-F", "message[headline]=chore: update default-branch workflow triggers"}
	for _, file := range fresh.WorkflowFiles {
		args = append(args, "-F", fmt.Sprintf("additions[][path]=%s", file.Path), "-F", fmt.Sprintf("additions[][contents]=%s", base64.StdEncoding.EncodeToString([]byte(file.rewritten))))
	}
	response := e.GitHub.Execute(ctx, args...)
	var payload struct {
		Data struct {
			CreateCommitOnBranch struct {
				Commit struct {
					OID string `json:"oid"`
				} `json:"commit"`
			} `json:"createCommitOnBranch"`
		} `json:"data"`
	}
	decodeErr := json.Unmarshal(response.Stdout, &payload)
	newHead, readErr := e.readDefaultBranchRef(ctx, repo.Repository, repo.ObservedDefault)
	if readErr != nil || !validDefaultBranchCommit(newHead) || newHead == repo.OldHead {
		repo.Disposition, repo.Error = "error", "workflow commit response did not produce a verified new source head"
		return repo
	}
	if response.Err == nil && (decodeErr != nil || payload.Data.CreateCommitOnBranch.Commit.OID == "" || payload.Data.CreateCommitOnBranch.Commit.OID != newHead) {
		repo.Disposition, repo.Error = "error", "workflow commit response lacks the exact created commit OID"
		return repo
	}
	if err := e.verifyDefaultBranchWorkflowBytes(ctx, repo.Repository, repo.ObservedDefault, repo.ObservedDefault, fresh.WorkflowFiles); err != nil {
		repo.Disposition, repo.Error = "error", "workflow commit post-read did not prove every replacement: "+err.Error()
		return repo
	}
	if err := e.verifyDefaultBranchWorkflowCommitParent(ctx, repo.Repository, newHead, repo.OldHead); err != nil {
		repo.Disposition, repo.Error = "error", "workflow commit post-read did not prove the expected parent: "+err.Error()
		return repo
	}
	repo.OldHead, repo.NewHead, repo.WorkflowCommit, repo.WorkflowPhase, repo.Disposition, repo.Error = newHead, "", newHead, "verified", "drift", ""
	repo.Actions = append(repo.Actions, "rewrote supported workflow triggers in one commit "+newHead)
	if checkpoint != nil {
		if err := checkpoint(repo); err != nil {
			repo.Disposition, repo.Error = "error", "persist verified workflow commit: "+err.Error()
		}
	}
	return repo
}

func (e *Engine) verifyDefaultBranchWorkflowCommitParent(ctx context.Context, repository, commit, expectedParent string) error {
	body, err := e.GitHub.Read(ctx, "repos/"+repository+"/commits/"+commit)
	if err != nil {
		return fmt.Errorf("read new commit: %w", err)
	}
	var value struct {
		Parents []struct {
			SHA string `json:"sha"`
		} `json:"parents"`
	}
	if err := json.Unmarshal(body, &value); err != nil || len(value.Parents) != 1 || value.Parents[0].SHA != expectedParent {
		return errors.New("new workflow commit is not a one-parent child of the planned source head")
	}
	return nil
}

// applyArchivedDefaultBranch permits one guarded temporary unarchive. Every
// path after GitHub accepts that mutation attempts to restore archival before
// returning, including a failed report checkpoint.
func (e *Engine) applyArchivedDefaultBranch(ctx context.Context, repo Repository, checkpoint func(Repository) error) Repository {
	transition := repo.Archive
	if transition == nil || !transition.OriginalArchived || transition.RepositoryID <= 0 || repo.RepositoryID != transition.RepositoryID {
		repo.Disposition = "blocked"
		repo.Error = "archived repository transition lacks a stable original archive proof"
		return repo
	}
	metadata, err := e.readDefaultBranchMetadata(ctx, repo.Repository)
	if err != nil {
		repo.Disposition = "error"
		repo.Error = "refresh archived repository before temporary unarchive: " + err.Error()
		return repo
	}
	if err := e.validatePlannedArchivedDefaultBranch(ctx, metadata, repo.Repository, transition); err != nil {
		repo.Disposition = "blocked"
		repo.Error = "archived repository changed after planning: " + err.Error()
		return repo
	}
	transition.Phase = "unarchive_pending"
	if err := checkpoint(repo); err != nil {
		repo.Disposition = "error"
		repo.Error = "persist pending archive transition: " + err.Error()
		return repo
	}
	response := e.GitHub.Execute(ctx, "api", "--method", "PATCH", "repos/"+repo.Repository, "-f", "archived=false")
	transition.UnarchiveAccepted = response.Err == nil
	transition.Phase = "unarchived"
	if response.Err != nil {
		repo.Disposition = "error"
		repo.Error = "temporarily unarchive repository: " + githubCommandMessage(response)
		return e.restoreArchivedDefaultBranch(ctx, repo, checkpoint)
	}
	if err := checkpoint(repo); err != nil {
		repo.Disposition = "error"
		repo.Error = "persist accepted unarchive response: " + err.Error()
		return e.restoreArchivedDefaultBranch(ctx, repo, checkpoint)
	}
	owner, name, ok := strings.Cut(repo.Repository, "/")
	if !ok {
		repo.Disposition = "error"
		repo.Error = "invalid repository observation"
		return e.restoreArchivedDefaultBranch(ctx, repo, checkpoint)
	}
	// The initial inspection has already accepted only the narrow trigger
	// grammar. Reinspect it after unarchiving, so a concurrent workflow change
	// cannot be committed or renamed from a stale archive transition.
	fresh := e.inspectDefaultBranchWithOptions(ctx, discover.Repo{Org: owner, Name: name}, repo.Desired, false, repo.PagesBefore != nil, len(repo.WorkflowFiles) > 0)
	if fresh.RepositoryID != transition.RepositoryID || fresh.Archived {
		repo.Disposition = "error"
		repo.Error = "temporary unarchive did not preserve the planned repository identity and active state"
		return e.restoreArchivedDefaultBranch(ctx, repo, checkpoint)
	}
	fresh.Archive = transition
	if fresh.Disposition != "drift" {
		repo = fresh
		return e.restoreArchivedDefaultBranch(ctx, repo, checkpoint)
	}
	if len(fresh.WorkflowFiles) > 0 {
		repo = e.applyDefaultBranchWorkflowTriggers(ctx, fresh, func(updated Repository) error {
			updated.Archive = transition
			repo = updated
			return checkpoint(repo)
		})
		repo.Archive = transition
		if repo.Disposition != "drift" {
			return e.restoreArchivedDefaultBranch(ctx, repo, checkpoint)
		}
		fresh = repo
	}
	repo = e.applyDefaultBranchWithCheckpoint(ctx, fresh, func(updated Repository) error {
		updated.Archive = transition
		repo = updated
		return checkpoint(repo)
	})
	repo.Archive = transition
	if repo.Disposition == "compliant" {
		transition.FinalHead = repo.NewHead
	}
	return e.restoreArchivedDefaultBranch(ctx, repo, checkpoint)
}

func (e *Engine) validatePlannedArchivedDefaultBranch(ctx context.Context, metadata defaultBranchRepoMetadata, slug string, transition *Archive) error {
	if metadata.ID != transition.RepositoryID {
		return errors.New("repository ID differs from the archived transition")
	}
	if !metadata.Archived {
		return errors.New("repository is no longer archived")
	}
	if metadata.DefaultBranch != transition.InitialDefault {
		return errors.New("default branch differs from the archived transition")
	}
	head, err := e.readDefaultBranchRef(ctx, slug, metadata.DefaultBranch)
	if err != nil {
		return fmt.Errorf("read planned default head: %w", err)
	}
	if head != transition.InitialHead {
		return errors.New("default head differs from the archived transition")
	}
	return nil
}

func (e *Engine) restoreArchivedDefaultBranch(ctx context.Context, repo Repository, checkpoint func(Repository) error) Repository {
	transition := repo.Archive
	if transition == nil {
		return repo
	}
	transition.Phase = "restore_pending"
	pendingErr := checkpoint(repo)
	if pendingErr != nil {
		transition.RecoveryRequired = true
		transition.RestoreError = "persist pending archive restoration: " + pendingErr.Error()
		repo.Disposition = "error"
		repo.Error = transition.RestoreError
	}
	beforeRestore, beforeRestoreErr := e.readDefaultBranchMetadata(ctx, repo.Repository)
	if beforeRestoreErr != nil || beforeRestore.ID != transition.RepositoryID {
		transition.Phase, transition.RecoveryRequired = "failed", true
		if beforeRestoreErr != nil {
			transition.RestoreError = "refresh repository before archive restoration: " + beforeRestoreErr.Error()
		} else {
			transition.RestoreError = "repository ID changed before archive restoration"
		}
		repo.Disposition, repo.Error = "error", transition.RestoreError
		if checkpointErr := checkpoint(repo); checkpointErr != nil {
			repo.Error += "; persist failed archive restoration: " + checkpointErr.Error()
		}
		return repo
	}
	response := e.GitHub.Execute(ctx, "api", "--method", "PATCH", "repos/"+repo.Repository, "-f", "archived=true")
	transition.RestoreAccepted = response.Err == nil
	metadata, metadataErr := e.readDefaultBranchMetadata(ctx, repo.Repository)
	expectedDefault, expectedHead := transition.InitialDefault, transition.InitialHead
	if transition.FinalHead != "" {
		expectedDefault, expectedHead = repo.Desired, transition.FinalHead
	} else if repo.WorkflowCommit != "" {
		// A workflow commit may have succeeded before the subsequent branch
		// rename failed. Restore archival at that verified child head on the
		// original default; restore-only never rolls back the workflow commit.
		expectedHead = repo.WorkflowCommit
	}
	if metadataErr != nil || metadata.ID != transition.RepositoryID || !metadata.Archived || metadata.DefaultBranch != expectedDefault {
		transition.Phase, transition.RecoveryRequired = "failed", true
		if metadataErr != nil {
			transition.RestoreError = "verify archive restoration: " + metadataErr.Error()
		} else if response.Err != nil {
			transition.RestoreError = "restore archival: " + githubCommandMessage(response)
		} else {
			transition.RestoreError = "archive restoration did not preserve repository identity, archived state, and desired default"
		}
		repo.Disposition = "error"
		repo.Error = transition.RestoreError
		if checkpointErr := checkpoint(repo); checkpointErr != nil {
			repo.Error += "; persist failed archive restoration: " + checkpointErr.Error()
		}
		return repo
	}
	head, headErr := e.readDefaultBranchRef(ctx, repo.Repository, metadata.DefaultBranch)
	if headErr != nil || head != expectedHead {
		transition.Phase, transition.RecoveryRequired = "failed", true
		if headErr != nil {
			transition.RestoreError = "verify archived default head: " + headErr.Error()
		} else {
			transition.RestoreError = "archive restoration changed the verified default head"
		}
		repo.Disposition, repo.Error = "error", transition.RestoreError
		if checkpointErr := checkpoint(repo); checkpointErr != nil {
			repo.Error += "; persist failed archive restoration: " + checkpointErr.Error()
		}
		return repo
	}
	transition.Phase, transition.RecoveryRequired, transition.RestoreError = "restored", false, ""
	repo.Archived = true
	if repo.Disposition == "compliant" {
		repo.Actions = append(repo.Actions, "restored archived state")
	}
	if err := checkpoint(repo); err != nil {
		repo.Disposition = "error"
		repo.Error = "persist verified archive restoration: " + err.Error()
	}
	return repo
}

func (e *Engine) readDefaultBranchMetadata(ctx context.Context, repository string) (defaultBranchRepoMetadata, error) {
	body, err := e.GitHub.Read(ctx, "repos/"+repository)
	if err != nil {
		return defaultBranchRepoMetadata{}, err
	}
	var metadata defaultBranchRepoMetadata
	if err := json.Unmarshal(body, &metadata); err != nil {
		return defaultBranchRepoMetadata{}, fmt.Errorf("decode repository metadata: %w", err)
	}
	if metadata.ID <= 0 || !validDefaultBranch(metadata.DefaultBranch) {
		return defaultBranchRepoMetadata{}, errors.New("repository metadata lacks a stable ID or valid default branch")
	}
	return metadata, nil
}

func (e *Engine) applyDefaultBranchWithCheckpoint(ctx context.Context, repo Repository, checkpoint func(Repository) error) Repository {
	owner, name, ok := strings.Cut(repo.Repository, "/")
	if !ok || owner == "" || name == "" {
		repo.Disposition = "error"
		repo.Error = "invalid repository observation"
		return repo
	}
	fresh := e.inspectDefaultBranchWithOptions(ctx, discover.Repo{Org: owner, Name: name}, repo.Desired, false, repo.PagesBefore != nil)
	if fresh.Disposition != "drift" {
		repo.Disposition = fresh.Disposition
		repo.Error = fresh.Error
		repo.Impacts = append(repo.Impacts, fresh.Impacts...)
		return repo
	}
	if fresh.ObservedDefault != repo.ObservedDefault || fresh.OldHead != repo.OldHead || fresh.NewHead != repo.NewHead || fresh.TargetExists != repo.TargetExists {
		fresh.Disposition = "blocked"
		fresh.Error = "repository default or branch head changed after planning; rerun the audit"
		return fresh
	}
	args := []string{"api", "--method", "POST", "repos/" + repo.Repository + "/branches/" + url.PathEscape(repo.ObservedDefault) + "/rename", "-f", "new_name=" + repo.Desired}
	if repo.TargetExists {
		args = []string{"api", "--method", "PATCH", "repos/" + repo.Repository, "-f", "default_branch=" + repo.Desired}
	}
	repo.Error = "default-branch mutation pending"
	if checkpoint != nil {
		if err := checkpoint(repo); err != nil {
			repo.Disposition = "error"
			repo.Error = "persist pending default-branch mutation: " + err.Error()
			return repo
		}
	}
	response := e.GitHub.Execute(ctx, args...)
	if response.Err != nil {
		repo.Disposition = "error"
		repo.Error = githubCommandMessage(response)
		return repo
	}
	repo.RenameAccepted = !repo.TargetExists
	repo.Disposition = "pending"
	repo.Error = defaultBranchRenameResponsePendingError
	if checkpoint != nil {
		if err := checkpoint(repo); err != nil {
			repo.Disposition = "error"
			repo.Error = "persist default-branch mutation response: " + err.Error()
			return repo
		}
	}
	var verified Repository
	if repo.TargetExists {
		verified = e.readDefaultBranchRenameVisibility(ctx, repo, e.Clock.Now().Add(30*time.Second))
	} else {
		var waitErr error
		verified, waitErr = e.waitForDefaultBranchRename(ctx, repo)
		if waitErr != nil {
			repo.Disposition = "error"
			repo.Error = "wait for renamed branch visibility: " + waitErr.Error()
			return repo
		}
	}
	if verified.Disposition != "compliant" {
		repo.Disposition = "error"
		repo.Error = "rename response succeeded but post-read default/head proof did not converge: " + verified.Error
		return repo
	}
	if verified.OldHead != repo.OldHead || verified.NewHead != repo.OldHead {
		repo.Disposition = "error"
		repo.Error = "rename response succeeded but post-read branch head differs from the planned source SHA"
		return repo
	}
	repo.Disposition = "compliant"
	repo.Error = ""
	repo.VerifiedDefault = verified.ObservedDefault
	repo.NewHead = verified.NewHead
	if repo.TargetExists {
		repo.Actions = []string{"set default branch to existing same-SHA " + repo.Desired, "verified default branch and head"}
	} else {
		repo.Actions = []string{"renamed " + repo.ObservedDefault + " to " + repo.Desired, "verified default branch and head"}
	}
	return repo
}

// applyDefaultBranchPagesWithCheckpoint updates only the legacy Pages source
// after the default rename has been read back. Pages has no documented CAS
// precondition, so the persisted pre-write observation and immediate exact
// post-read are the recovery boundary.
func (e *Engine) applyDefaultBranchPagesWithCheckpoint(ctx context.Context, repo Repository, checkpoint func(Repository) error) Repository {
	if repo.PagesBefore == nil {
		return repo
	}
	fresh, err := e.readDefaultBranchMetadata(ctx, repo.Repository)
	if err != nil || fresh.DefaultBranch != repo.Desired {
		repo.Disposition = "error"
		if err != nil {
			repo.Error = "verify default branch before Pages migration: " + err.Error()
		} else {
			repo.Error = "default branch changed before Pages migration"
		}
		return repo
	}
	head, err := e.readDefaultBranchRef(ctx, repo.Repository, repo.Desired)
	if err != nil || head != repo.OldHead {
		repo.Disposition = "error"
		if err != nil {
			repo.Error = "verify default head before Pages migration: " + err.Error()
		} else {
			repo.Error = "default branch head changed before Pages migration"
		}
		return repo
	}
	body, err := e.GitHub.Read(ctx, "repos/"+repo.Repository+"/pages")
	if err != nil {
		repo.Disposition, repo.Error = "error", "read Pages source before migration: "+err.Error()
		return repo
	}
	before, err := decodeDefaultBranchPages(body)
	automatic := err == nil && repo.PagesPhase != "unfinished" && repo.ObservedDefault == "master" && repo.PagesBefore.Branch == "master" && repo.Desired == "main" && repo.PagesBefore.BuildType == "legacy" && before.BuildType == "legacy" && validDefaultBranchPagesPath(before.Path) && before.Path == repo.PagesBefore.Path && before.Branch == repo.Desired
	if automatic {
		_, oldRefErr := e.GitHub.Read(ctx, "repos/"+repo.Repository+"/git/ref/heads/master")
		if oldRefErr == nil {
			repo.Disposition, repo.Error = "error", "old master ref still exists after Pages source transition"
			return repo
		}
		if !isDefaultBranchNotFound(oldRefErr) {
			repo.Disposition, repo.Error = "error", "could not prove old master ref is absent after Pages source transition: "+oldRefErr.Error()
			return repo
		}
		repo.PagesAfter, repo.PagesPhase = &before, "verified"
		repo.Disposition, repo.Error = "compliant", ""
		repo.Actions = append(repo.Actions, "verified GitHub automatic Pages source transition to "+repo.Desired+" with path "+before.Path)
		if checkpoint != nil {
			if err := checkpoint(repo); err != nil {
				repo.Disposition, repo.Error = "error", "persist verified automatic Pages transition: "+err.Error()
			}
		}
		return repo
	}
	if err != nil || before != *repo.PagesBefore || before.BuildType != "legacy" || !validDefaultBranchPagesPath(before.Path) || (repo.PagesPhase == "unfinished" && before.Branch != "master") || (repo.PagesPhase != "unfinished" && before.Branch != repo.ObservedDefault) {
		repo.Disposition = "error"
		if err != nil {
			repo.Error = "decode Pages source before migration: " + err.Error()
		} else {
			repo.Error = "Pages source changed after planning; WB will not overwrite it"
		}
		return repo
	}
	repo.PagesPhase = "pending"
	if checkpoint != nil {
		if err := checkpoint(repo); err != nil {
			repo.Disposition, repo.Error = "error", "persist pending Pages migration: "+err.Error()
			return repo
		}
	}
	response := e.GitHub.Execute(ctx, "api", "--method", "PUT", "repos/"+repo.Repository+"/pages", "-f", "source[branch]="+repo.Desired, "-f", "source[path]="+before.Path)
	if response.Err != nil {
		repo.Disposition, repo.Error = "error", "update Pages source: "+githubCommandMessage(response)
		return repo
	}
	repo.PagesAccepted, repo.PagesPhase = true, "response_received"
	if checkpoint != nil {
		if err := checkpoint(repo); err != nil {
			repo.Disposition, repo.Error = "error", "persist Pages migration response: "+err.Error()
			return repo
		}
	}
	body, err = e.GitHub.Read(ctx, "repos/"+repo.Repository+"/pages")
	if err != nil {
		repo.Disposition, repo.Error = "error", "verify Pages source migration: "+err.Error()
		return repo
	}
	after, err := decodeDefaultBranchPages(body)
	if err != nil || after.BuildType != "legacy" || after.Branch != repo.Desired || after.Path != before.Path {
		repo.Disposition = "error"
		if err != nil {
			repo.Error = "decode Pages source after migration: " + err.Error()
		} else {
			repo.Error = "Pages source post-write verification did not preserve legacy build type and source path"
		}
		return repo
	}
	repo.PagesAfter = &after
	repo.PagesPhase = "verified"
	repo.Disposition, repo.Error = "compliant", ""
	repo.Actions = append(repo.Actions, "migrated Pages source to "+repo.Desired+" with path "+before.Path, "verified Pages source")
	if checkpoint != nil {
		if err := checkpoint(repo); err != nil {
			repo.Disposition, repo.Error = "error", "persist verified Pages migration: "+err.Error()
		}
	}
	return repo
}

func (e *Engine) waitForDefaultBranchRename(ctx context.Context, planned Repository) (Repository, error) {
	deadline := e.Clock.Now().Add(30 * time.Second)
	observed := e.readDefaultBranchRenameVisibility(ctx, planned, deadline)
	for defaultBranchRenameStillPending(planned, observed) {
		remaining := deadline.Sub(e.Clock.Now())
		if remaining <= 0 {
			break
		}
		if remaining > 250*time.Millisecond {
			remaining = 250 * time.Millisecond
		}
		if err := e.Clock.Wait(ctx, remaining); err != nil {
			return observed, err
		}
		observed = e.readDefaultBranchRenameVisibility(ctx, planned, deadline)
	}
	return observed, nil
}

func (e *Engine) readDefaultBranchRenameVisibility(ctx context.Context, planned Repository, deadline time.Time) Repository {
	readContext, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	observed := Repository{Repository: planned.Repository, Desired: planned.Desired}
	body, err := e.GitHub.Read(readContext, "repos/"+planned.Repository)
	if err != nil {
		observed.Disposition, observed.Error = "error", err.Error()
		return observed
	}
	var meta defaultBranchRepoMetadata
	if err := json.Unmarshal(body, &meta); err != nil || !validDefaultBranch(meta.DefaultBranch) {
		observed.Disposition, observed.Error = "error", "decode repository default branch"
		return observed
	}
	observed.ObservedDefault = meta.DefaultBranch
	if meta.DefaultBranch != planned.ObservedDefault && meta.DefaultBranch != planned.Desired {
		observed.Disposition, observed.Error = "blocked", "repository default changed while waiting for rename visibility"
		return observed
	}
	head, err := e.readDefaultBranchRef(readContext, planned.Repository, planned.Desired)
	if err != nil {
		if isDefaultBranchNotFound(err) {
			observed.Disposition, observed.Error = "pending", "renamed target branch is not visible yet"
			return observed
		}
		observed.Disposition, observed.Error = "error", err.Error()
		return observed
	}
	observed.OldHead, observed.NewHead = head, head
	if head != planned.OldHead {
		observed.Disposition, observed.Error = "blocked", "repository branch head changed while waiting for rename visibility"
		return observed
	}
	if meta.DefaultBranch == planned.Desired {
		observed.Disposition = "compliant"
		return observed
	}
	if meta.DefaultBranch == planned.ObservedDefault {
		observed.Disposition = "drift"
		return observed
	}
	observed.Disposition, observed.Error = "blocked", "repository default changed while waiting for rename visibility"
	return observed
}

func defaultBranchRenameStillPending(planned, observed Repository) bool {
	return observed.Disposition == "pending" || (observed.Disposition == "drift" && observed.ObservedDefault == planned.ObservedDefault && observed.OldHead == planned.OldHead)
}

// reconcileDefaultBranchCanonicals changes only the old default branch in a
// clean canonical clone whose old default is either equal to or wholly
// contained by the freshly fetched remote head.
// It deliberately leaves every other local state in place and records why a
// clone was preserved. A report checkpoint happens immediately before and
// after the one local branch mutation.
func (e *Engine) reconcileDefaultBranchCanonicals(ctx context.Context, repository *Repository, clones []discover.Repo, sourceDefault, sourceHead string, checkpoint func() error) {
	for _, clone := range clones {
		repository.CanonicalClones = append(repository.CanonicalClones, Canonical{Path: clone.Path, Disposition: "blocked"})
		entry := &repository.CanonicalClones[len(repository.CanonicalClones)-1]
		if err := e.reconcileDefaultBranchCanonical(ctx, repository, entry, sourceDefault, sourceHead, checkpoint); err != nil {
			entry.Error = err.Error()
		}
	}
}

func (e *Engine) reconcileDefaultBranchCanonical(ctx context.Context, repository *Repository, entry *Canonical, sourceDefault, sourceHead string, checkpoint func() error) error {
	if _, err := e.Git.Run(ctx, entry.Path, "fetch", "--prune", "origin"); err != nil {
		return fmt.Errorf("refresh origin before local reconciliation: %w", err)
	}
	if _, err := e.Git.Run(ctx, entry.Path, "remote", "set-head", "origin", "--auto"); err != nil {
		return fmt.Errorf("refresh origin/HEAD before local reconciliation: %w", err)
	}
	status, err := e.Git.Run(ctx, entry.Path, "status", "--porcelain")
	if err != nil {
		return fmt.Errorf("inspect local changes: %w", err)
	}
	if status != "" {
		return errors.New("local changes present; preserve the canonical clone and reconcile it after committing or rescuing the work")
	}
	worktrees, err := e.Git.Run(ctx, entry.Path, "worktree", "list", "--porcelain")
	if err != nil {
		return fmt.Errorf("inspect linked worktrees: %w", err)
	}
	if strings.Count(worktrees, "worktree ") != 1 {
		return errors.New("linked worktree exists; preserve every branch until that worktree is retired or moved")
	}
	current, err := e.Git.Run(ctx, entry.Path, "branch", "--show-current")
	if err != nil {
		return fmt.Errorf("inspect checked-out branch: %w", err)
	}
	entry.ObservedBranch = current
	remoteHead, err := e.Git.Run(ctx, entry.Path, "rev-parse", "origin/"+repository.Desired)
	if err != nil {
		return fmt.Errorf("resolve refreshed origin/%s: %w", repository.Desired, err)
	}
	entry.RemoteHead = remoteHead
	if remoteHead != sourceHead {
		return fmt.Errorf("origin/%s is %s, not planned source %s; rerun the audit before local reconciliation", repository.Desired, remoteHead, sourceHead)
	}
	if current == repository.Desired {
		localHead, err := e.Git.Run(ctx, entry.Path, "rev-parse", repository.Desired)
		if err != nil {
			return fmt.Errorf("resolve local %s: %w", repository.Desired, err)
		}
		if localHead != remoteHead {
			return fmt.Errorf("local %s is %s while origin/%s is %s; run wb sync --filter %s then retry --reconcile-from", repository.Desired, localHead, repository.Desired, remoteHead, repository.Repository)
		}
		upstream, upstreamErr := e.Git.Run(ctx, entry.Path, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
		if upstreamErr == nil && upstream == "origin/"+repository.Desired {
			entry.Disposition = "compliant"
			entry.Actions = []string{"local " + repository.Desired + " already reconciled"}
			return nil
		}
		entry.Disposition = "drift"
		if err := checkpoint(); err != nil {
			return fmt.Errorf("persist local tracking repair plan: %w", err)
		}
		if _, err := e.Git.Run(ctx, entry.Path, "branch", "--set-upstream-to=origin/"+repository.Desired, repository.Desired); err != nil {
			return fmt.Errorf("restore local tracking branch: %w", err)
		}
		entry.Disposition = "compliant"
		entry.Actions = []string{"restored upstream origin/" + repository.Desired}
		return checkpoint()
	}
	if current == "" {
		head, err := e.Git.Run(ctx, entry.Path, "rev-parse", "HEAD")
		if err != nil {
			return fmt.Errorf("resolve detached HEAD: %w", err)
		}
		mainExists, err := e.Git.RefExists(ctx, entry.Path, "refs/heads/"+repository.Desired)
		if err != nil {
			return fmt.Errorf("inspect detached local %s: %w", repository.Desired, err)
		}
		sourceExists, err := e.Git.RefExists(ctx, entry.Path, "refs/heads/"+sourceDefault)
		if err != nil {
			return fmt.Errorf("inspect detached old local %s: %w", sourceDefault, err)
		}
		if !mainExists && sourceExists {
			sourceHead, err := e.Git.Run(ctx, entry.Path, "rev-parse", sourceDefault)
			if err != nil {
				return fmt.Errorf("resolve detached local %s: %w", sourceDefault, err)
			}
			if head != remoteHead || sourceHead != remoteHead {
				return fmt.Errorf("detached HEAD is not the failed atomic rename state for %s; preserve it for explicit recovery", sourceDefault)
			}
			entry.Disposition = "drift"
			entry.Actions = []string{"recovered detached HEAD before atomically renaming local " + sourceDefault + " to " + repository.Desired}
			if err := checkpoint(); err != nil {
				return fmt.Errorf("persist detached atomic rename recovery plan: %w", err)
			}
			if err := e.Git.AttachHead(ctx, entry.Path, sourceDefault); err != nil {
				return fmt.Errorf("reattach detached HEAD to local %s: %w", sourceDefault, err)
			}
			if err := e.verifyDefaultBranchAttachment(ctx, entry.Path, sourceDefault, repository.Desired, remoteHead); err != nil {
				entry.Disposition = "blocked"
				entry.Actions = append(entry.Actions, "attachment verification failed")
				if checkpointErr := checkpoint(); checkpointErr != nil {
					return fmt.Errorf("verify restored local %s attachment: %v; persist blocked detached recovery receipt: %w", sourceDefault, err, checkpointErr)
				}
				return fmt.Errorf("verify restored local %s attachment: %w", sourceDefault, err)
			}
			entry.Actions = []string{"restored HEAD to local " + sourceDefault + " after failed atomic rename"}
			if err := checkpoint(); err != nil {
				return fmt.Errorf("persist detached atomic rename recovery receipt: %w", err)
			}
			return nil
		}
		mainHead, err := e.Git.Run(ctx, entry.Path, "rev-parse", repository.Desired)
		if err != nil {
			return fmt.Errorf("resolve detached local %s: %w", repository.Desired, err)
		}
		if head != remoteHead || !mainExists || mainHead != remoteHead || sourceExists {
			return fmt.Errorf("detached HEAD is not the recorded atomic rename state for %s; preserve it for explicit recovery", sourceDefault)
		}
		entry.Disposition = "drift"
		entry.Actions = []string{"recovered detached HEAD after atomically renaming local " + sourceDefault + " to " + repository.Desired}
		if err := checkpoint(); err != nil {
			return fmt.Errorf("persist detached local reconciliation recovery plan: %w", err)
		}
		if err := e.Git.AttachHead(ctx, entry.Path, repository.Desired); err != nil {
			return fmt.Errorf("attach detached HEAD to local %s: %w", repository.Desired, err)
		}
		if err := e.verifyDefaultBranchAttachment(ctx, entry.Path, repository.Desired, repository.Desired, remoteHead); err != nil {
			entry.Disposition = "blocked"
			entry.Actions = append(entry.Actions, "attachment verification failed")
			if checkpointErr := checkpoint(); checkpointErr != nil {
				return fmt.Errorf("verify recovered local %s attachment: %v; persist blocked detached recovery receipt: %w", repository.Desired, err, checkpointErr)
			}
			return fmt.Errorf("verify recovered local %s attachment: %w", repository.Desired, err)
		}
		if _, err := e.Git.Run(ctx, entry.Path, "branch", "--set-upstream-to=origin/"+repository.Desired, repository.Desired); err != nil {
			return fmt.Errorf("set local tracking branch after detached recovery: %w", err)
		}
		entry.Disposition = "compliant"
		entry.Actions = append(entry.Actions, "set upstream to origin/"+repository.Desired)
		if err := checkpoint(); err != nil {
			return fmt.Errorf("persist detached local reconciliation receipt: %w", err)
		}
		return nil
	}
	if current != sourceDefault {
		return fmt.Errorf("canonical checkout is on %q; only the old default %q may be renamed automatically", current, sourceDefault)
	}
	branches, err := e.Git.Run(ctx, entry.Path, "for-each-ref", "--format=%(refname:strip=2)", "refs/heads")
	if err != nil {
		return fmt.Errorf("inspect local branch names: %w", err)
	}
	for _, branch := range strings.Split(branches, "\n") {
		if branch == repository.Desired {
			return fmt.Errorf("local destination branch %q already exists", repository.Desired)
		}
	}
	localHead, err := e.Git.Run(ctx, entry.Path, "rev-parse", sourceDefault)
	if err != nil {
		return fmt.Errorf("resolve local %s: %w", sourceDefault, err)
	}
	if localHead != remoteHead {
		ancestor, err := e.Git.IsAncestor(ctx, entry.Path, sourceDefault, "origin/"+repository.Desired)
		if err != nil {
			return fmt.Errorf("classify local %s against origin/%s: %w", sourceDefault, repository.Desired, err)
		}
		if !ancestor {
			unpublished, err := e.Git.Run(ctx, entry.Path, "log", "origin/"+repository.Desired+".."+sourceDefault, "--not", "--remotes", "--format=%H")
			if err != nil {
				return fmt.Errorf("inspect local %s unpublished commits: %w", sourceDefault, err)
			}
			if unpublished != "" {
				return fmt.Errorf("local %s has unpublished commits beyond origin/%s; preserve the canonical clone and reconcile it after pushing or rescuing the work", sourceDefault, repository.Desired)
			}
			return fmt.Errorf("local %s is not contained in origin/%s but has no unpublished commits; preserve it for explicit divergence recovery", sourceDefault, repository.Desired)
		}

		entry.Disposition = "drift"
		entry.Actions = []string{"planned fast-forward local " + sourceDefault + " (" + localHead + ") to origin/" + repository.Desired + " (" + remoteHead + ")"}
		if err := checkpoint(); err != nil {
			return fmt.Errorf("persist local fast-forward plan: %w", err)
		}
		checkpointedHead := remoteHead
		if _, err := e.Git.Run(ctx, entry.Path, "merge", "--ff-only", "origin/"+repository.Desired); err != nil {
			return fmt.Errorf("fast-forward local %s to origin/%s: %w", sourceDefault, repository.Desired, err)
		}
		localHead, err = e.Git.Run(ctx, entry.Path, "rev-parse", sourceDefault)
		if err != nil {
			return fmt.Errorf("resolve local %s after fast-forward: %w", sourceDefault, err)
		}
		remoteHead, err = e.Git.Run(ctx, entry.Path, "rev-parse", "origin/"+repository.Desired)
		if err != nil {
			return fmt.Errorf("resolve origin/%s after fast-forward: %w", repository.Desired, err)
		}
		entry.RemoteHead = remoteHead
		if localHead != checkpointedHead || remoteHead != checkpointedHead {
			return fmt.Errorf("local %s is %s while origin/%s is %s after fast-forward checkpoint %s; preserve it for explicit recovery", sourceDefault, localHead, repository.Desired, remoteHead, checkpointedHead)
		}
		entry.Actions = []string{"fast-forwarded local " + sourceDefault + " to origin/" + repository.Desired}
		if err := checkpoint(); err != nil {
			return fmt.Errorf("persist local fast-forward receipt: %w", err)
		}
	}
	entry.Disposition = "drift"
	if len(entry.Actions) == 0 {
		entry.Actions = []string{"planned rename local " + sourceDefault + " to " + repository.Desired}
	} else {
		entry.Actions = append(entry.Actions, "planned rename local "+sourceDefault+" to "+repository.Desired)
	}
	if err := checkpoint(); err != nil {
		return fmt.Errorf("persist local-reconciliation plan: %w", err)
	}
	checkpointedHead := remoteHead
	localHead, err = e.Git.Run(ctx, entry.Path, "rev-parse", sourceDefault)
	if err != nil {
		return fmt.Errorf("resolve local %s before rename: %w", sourceDefault, err)
	}
	remoteHead, err = e.Git.Run(ctx, entry.Path, "rev-parse", "origin/"+repository.Desired)
	if err != nil {
		return fmt.Errorf("resolve origin/%s before rename: %w", repository.Desired, err)
	}
	entry.RemoteHead = remoteHead
	if localHead != checkpointedHead || remoteHead != checkpointedHead {
		return fmt.Errorf("local %s is %s while origin/%s is %s before rename checkpoint %s; preserve it for explicit recovery", sourceDefault, localHead, repository.Desired, remoteHead, checkpointedHead)
	}
	if err := e.Git.AtomicRenameRefs(ctx, entry.Path, sourceDefault, repository.Desired, checkpointedHead); err != nil {
		return fmt.Errorf("rename local default branch: %w", err)
	}
	atomicAction := "renamed refs local " + sourceDefault + " to " + repository.Desired + "; HEAD detached; tracking incomplete"
	if len(entry.Actions) > 0 && strings.HasPrefix(entry.Actions[0], "fast-forwarded local ") {
		entry.Actions = []string{entry.Actions[0], atomicAction}
	} else {
		entry.Actions = []string{atomicAction}
	}
	if err := checkpoint(); err != nil {
		return fmt.Errorf("persist atomic local rename receipt: %w", err)
	}
	if err := e.Git.AttachHead(ctx, entry.Path, repository.Desired); err != nil {
		entry.Disposition = "error"
		if checkpointErr := checkpoint(); checkpointErr != nil {
			return fmt.Errorf("attach HEAD to local %s: %v; persist detached local rename receipt: %w", repository.Desired, err, checkpointErr)
		}
		return fmt.Errorf("attach HEAD to local %s: %w", repository.Desired, err)
	}
	if err := e.verifyDefaultBranchAttachment(ctx, entry.Path, repository.Desired, repository.Desired, checkpointedHead); err != nil {
		entry.Disposition = "blocked"
		entry.Actions = append(entry.Actions, "attachment verification failed")
		if checkpointErr := checkpoint(); checkpointErr != nil {
			return fmt.Errorf("verify renamed local %s attachment: %v; persist blocked local rename receipt: %w", repository.Desired, err, checkpointErr)
		}
		return fmt.Errorf("verify renamed local %s attachment: %w", repository.Desired, err)
	}
	renamedActions := []string{"renamed local " + sourceDefault + " to " + repository.Desired}
	if len(entry.Actions) > 0 && strings.HasPrefix(entry.Actions[0], "fast-forwarded local ") {
		renamedActions = append([]string{entry.Actions[0]}, renamedActions...)
	}
	if _, err := e.Git.Run(ctx, entry.Path, "branch", "--set-upstream-to=origin/"+repository.Desired, repository.Desired); err != nil {
		entry.Disposition = "error"
		entry.Actions = renamedActions
		if checkpointErr := checkpoint(); checkpointErr != nil {
			return fmt.Errorf("set local tracking branch: %v; persist partial local reconciliation receipt: %w", err, checkpointErr)
		}
		return fmt.Errorf("set local tracking branch: %w", err)
	}
	entry.Disposition = "compliant"
	entry.Actions = append(renamedActions, "set upstream to origin/"+repository.Desired)
	if err := checkpoint(); err != nil {
		return fmt.Errorf("persist local-reconciliation receipt: %w", err)
	}
	return nil
}

func (e *Engine) verifyDefaultBranchAttachment(ctx context.Context, path, branch, desired, expected string) error {
	head, err := e.Git.Run(ctx, path, "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("resolve HEAD: %w", err)
	}
	branchHead, err := e.Git.Run(ctx, path, "rev-parse", branch)
	if err != nil {
		return fmt.Errorf("resolve local %s: %w", branch, err)
	}
	remoteHead, err := e.Git.Run(ctx, path, "rev-parse", "origin/"+desired)
	if err != nil {
		return fmt.Errorf("resolve origin/%s: %w", desired, err)
	}
	status, err := e.Git.Run(ctx, path, "status", "--porcelain")
	if err != nil {
		return fmt.Errorf("inspect local changes: %w", err)
	}
	if head != expected || branchHead != expected || remoteHead != expected || status != "" {
		return fmt.Errorf("HEAD %s, local %s %s, origin/%s %s, status %q do not match expected clean %s", head, branch, branchHead, desired, remoteHead, status, expected)
	}
	return nil
}

func summarizeDefaultBranch(report *Report) {
	report.Summary = Summary{Inspected: len(report.Repositories)}
	for _, repo := range report.Repositories {
		switch repo.Disposition {
		case "compliant":
			report.Summary.Compliant++
		case "drift":
			report.Summary.Drift++
		case "blocked":
			report.Summary.Blocked++
		case "error":
			report.Summary.Errors++
		}
		if len(repo.Actions) > 0 {
			report.Summary.Applied++
		}
		for _, clone := range repo.CanonicalClones {
			switch clone.Disposition {
			case "blocked", "drift":
				report.Summary.CanonicalBlocked++
			case "error":
				report.Summary.CanonicalErrors++
			}
		}
	}
}

func HasFindings(report Report) bool {
	return report.Summary.Drift+report.Summary.Blocked+report.Summary.Errors+
		report.Summary.CanonicalBlocked+report.Summary.CanonicalErrors > 0
}
func defaultBranchReportPath(projectsRoot, dir string) (string, error) {
	if dir == "" {
		home, err := wbhome.Root(projectsRoot)
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, "reports", "default-branch")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	reserved, err := os.CreateTemp(dir, "default-branch-"+time.Now().UTC().Format("20060102T150405.000000000Z")+"-*.json")
	if err != nil {
		return "", err
	}
	path := reserved.Name()
	if err := reserved.Close(); err != nil {
		return "", err
	}
	if err := os.Remove(path); err != nil {
		return "", err
	}
	return path, nil
}
func persistDefaultBranchReport(report Report) error {
	return persistDefaultBranchReportInjected(report, nil)
}

// persistDefaultBranchReportInjected is persistDefaultBranchReport's test
// seam (task-9 PR-2): every production call site reaches it only through
// persistDefaultBranchReport, which always passes a nil *filewrite.Injector,
// so production behaviour is unchanged; a test passes its own Injector
// directly to reach a create/chmod/write/sync/close/rename/dir-sync
// failure branch deterministically.
func persistDefaultBranchReportInjected(report Report, inj *filewrite.Injector) error {
	payload, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := filewrite.CreateTemp(filepath.Dir(report.ReportPath), ".default-branch-*.json", inj)
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if err := filewrite.ChmodFile(temporary, 0o600, temporaryName, inj); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := filewrite.Write(temporary, payload, temporaryName, inj); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := filewrite.Sync(temporary, temporaryName, inj); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := filewrite.Close(temporary, temporaryName, inj); err != nil {
		return err
	}
	if err := filewrite.Rename(temporaryName, report.ReportPath, inj); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(report.ReportPath))
	if err != nil {
		return err
	}
	syncErr := filewrite.SyncDir(directory, inj)
	closeErr := filewrite.Close(directory, directory.Name(), inj)
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}
func Print(out io.Writer, report Report) error {
	if _, err := fmt.Fprintf(out, "Default branch %s (%s)\\n", report.Desired, report.Mode); err != nil {
		return err
	}
	for _, repo := range report.Repositories {
		if _, err := fmt.Fprintf(out, "- %s: %s", repo.Repository, repo.Disposition); err != nil {
			return err
		}
		if repo.Error != "" {
			if _, err := fmt.Fprintf(out, " — %s", repo.Error); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(out); err != nil {
			return err
		}
		for _, clone := range repo.CanonicalClones {
			if _, err := fmt.Fprintf(out, "  - %s: %s", clone.Path, clone.Disposition); err != nil {
				return err
			}
			if clone.Error != "" {
				if _, err := fmt.Fprintf(out, " — %s", clone.Error); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintln(out); err != nil {
				return err
			}
		}
	}
	return nil
}

func validDefaultBranch(branch string) bool {
	branch = strings.TrimSpace(branch)
	if branch == "" || branch == "@" || strings.HasPrefix(branch, "-") || strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") || strings.HasSuffix(branch, ".") || strings.Contains(branch, "..") || strings.Contains(branch, "@{") || strings.ContainsAny(branch, " ~^:?*[]\\") {
		return false
	}
	for _, character := range branch {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	for _, part := range strings.Split(branch, "/") {
		if part == "" || part == "." || part == ".." || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}
