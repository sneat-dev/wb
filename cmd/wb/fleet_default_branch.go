package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/runqueue"
	"github.com/sneat-dev/wb/internal/wbconfig"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

const defaultBranchSchemaVersion = 1

type defaultBranchOptions struct {
	apply, json, includeUser, allOrgs bool
	branch, reportDir, reconcileFrom  string
	owners, repositories              []string
	parallel                          int
}
type defaultBranchConfig struct {
	Fleet struct {
		DefaultBranch string `yaml:"default_branch"`
		Organizations map[string]struct {
			DefaultBranch string `yaml:"default_branch"`
		} `yaml:"organizations"`
	} `yaml:"fleet"`
}
type defaultBranchReport struct {
	SchemaVersion int                       `json:"schema_version"`
	Mode          string                    `json:"mode"`
	Desired       string                    `json:"desired"`
	Repositories  []defaultBranchRepository `json:"repositories"`
	Summary       defaultBranchSummary      `json:"summary"`
	ReportPath    string                    `json:"report_path,omitempty"`
}
type defaultBranchSummary struct {
	Inspected        int `json:"inspected"`
	Compliant        int `json:"compliant"`
	Drift            int `json:"drift"`
	Blocked          int `json:"blocked"`
	Errors           int `json:"errors"`
	Applied          int `json:"applied"`
	CanonicalBlocked int `json:"canonical_blocked"`
	CanonicalErrors  int `json:"canonical_errors"`
}
type defaultBranchRepository struct {
	Repository      string                   `json:"repository"`
	ObservedDefault string                   `json:"observed_default,omitempty"`
	VerifiedDefault string                   `json:"verified_default,omitempty"`
	Desired         string                   `json:"desired"`
	OldHead         string                   `json:"old_head,omitempty"`
	NewHead         string                   `json:"new_head,omitempty"`
	Disposition     string                   `json:"disposition"`
	Error           string                   `json:"error,omitempty"`
	Archived        bool                     `json:"archived,omitempty"`
	Fork            bool                     `json:"fork,omitempty"`
	TargetExists    bool                     `json:"target_exists,omitempty"`
	Impacts         []string                 `json:"impacts,omitempty"`
	Actions         []string                 `json:"actions,omitempty"`
	CanonicalClones []defaultBranchCanonical `json:"canonical_clones,omitempty"`
}
type defaultBranchCanonical struct {
	Path           string   `json:"path"`
	ObservedBranch string   `json:"observed_branch,omitempty"`
	RemoteHead     string   `json:"remote_head,omitempty"`
	Disposition    string   `json:"disposition"`
	Error          string   `json:"error,omitempty"`
	Actions        []string `json:"actions,omitempty"`
}
type defaultBranchRepoMetadata struct {
	DefaultBranch string `json:"default_branch"`
	Archived      bool   `json:"archived"`
	Fork          bool   `json:"fork"`
	Parent        *struct {
		FullName string `json:"full_name"`
	} `json:"parent"`
}
type defaultBranchRef struct {
	Commit struct {
		SHA string `json:"sha"`
	} `json:"commit"`
}

var (
	defaultBranchAuthUser   = discover.AuthUser
	defaultBranchMemberOrgs = discover.MemberOrgs
	defaultBranchListRemote = discover.ListRemote
	defaultBranchRead       = func(ctx context.Context, endpoint string) ([]byte, error) {
		return githubobserver.Read(ctx, "", "api", endpoint)
	}
	defaultBranchExecute = func(ctx context.Context, args ...string) githubobserver.CommandResponse {
		return githubobserver.Execute(ctx, "", args...)
	}
	defaultBranchConfigPath = wbconfig.DefaultPath
	defaultBranchGit        = func(ctx context.Context, dir string, args ...string) (string, error) {
		command := exec.CommandContext(ctx, "git", args...)
		command.Dir = dir
		output, err := command.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
		}
		return strings.TrimSpace(string(output)), nil
	}
)

func newFleetDefaultBranchCmd() *cobra.Command {
	parallel := min(runqueue.Budget(), 4)
	options := defaultBranchOptions{parallel: parallel}
	command := &cobra.Command{Use: "default-branch", Short: "Audit or safely rename GitHub default branches", Long: `Audit the configured GitHub default branch across the accessible fleet. Audit is read-only.

The desired branch comes from wb.yaml fleet.default_branch, overridden by fleet.organizations.<owner>.default_branch; --branch has highest precedence. Apply requires explicit --org, --repo, or --user scope and writes a durable report before each mutation.

When the desired branch is absent, WB uses GitHub's branch-rename endpoint only after fresh repository and branch reads. If it already exists at the same SHA, WB may change the repository default only after confirming protection/ruleset coverage. A different target SHA, an archived repository, an open source-default PR (including fork-to-parent PRs), Pages source, or a protection/ruleset impact is a refusal. Workflow references are reported only; WB never rewrites branch strings blindly.`, Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			options.owners = requestedDefaultBranchOwners(cmd, options.owners)
			if options.parallel < 1 || options.parallel > 16 {
				return usageError("--parallel must be between 1 and 16")
			}
			if len(options.repositories) > 0 && (len(options.owners) > 0 || options.includeUser || options.allOrgs) {
				return usageError("--repo cannot be combined with --org, --all-orgs, or --user")
			}
			if options.allOrgs && (len(options.owners) > 0 || options.includeUser) {
				return usageError("--all-orgs cannot be combined with --org or --user")
			}
			if options.apply && len(options.owners) == 0 && len(options.repositories) == 0 && !options.includeUser && !options.allOrgs {
				return usageError("--apply requires explicit --org, --all-orgs, --repo, or --user scope")
			}
			if options.reconcileFrom != "" && !options.apply {
				return usageError("--reconcile-from requires --apply")
			}
			report, err := runDefaultBranch(cmd.Context(), options, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			if options.json {
				err = writeJSONTo(cmd.OutOrStdout(), report)
			} else {
				err = printDefaultBranchReport(cmd.OutOrStdout(), report)
			}
			if err != nil {
				return err
			}
			if defaultBranchHasFindings(report) {
				return &exitError{code: exitFindings, message: "default-branch findings remain; see the report above"}
			}
			return nil
		}}
	command.Flags().BoolVar(&options.apply, "apply", false, "apply only reviewed, safe branch renames")
	command.Flags().StringVar(&options.branch, "branch", "", "desired default branch; overrides organization and fleet policy")
	command.Flags().StringArrayVarP(&options.owners, "org", "o", nil, "only inspect or apply this GitHub organization (repeatable)")
	command.Flags().StringArrayVar(&options.repositories, "repo", nil, "only inspect or apply this exact owner/repository (repeatable)")
	command.Flags().BoolVar(&options.includeUser, "user", false, "include the authenticated user's repositories in explicit scope")
	command.Flags().BoolVar(&options.allOrgs, "all-orgs", false, "include every currently accessible organization, excluding personal repositories")
	command.Flags().IntVar(&options.parallel, "parallel", parallel, "maximum GitHub repositories to inspect or apply concurrently (1-16)")
	command.Flags().StringVar(&options.reportDir, "report-dir", "", "durable report directory (apply defaults below <wb-home>/reports/default-branch)")
	command.Flags().StringVar(&options.reconcileFrom, "reconcile-from", "", "resume canonical-clone reconciliation from an earlier default-branch apply report")
	addJSONFormatFlags(command, &options.json)
	return command
}

func runDefaultBranch(ctx context.Context, options defaultBranchOptions, progress io.Writer) (defaultBranchReport, error) {
	config, err := loadDefaultBranchConfig(defaultBranchConfigPath())
	if err != nil {
		return defaultBranchReport{}, err
	}
	prior, err := readDefaultBranchReport(options.reconcileFrom)
	if err != nil {
		return defaultBranchReport{}, err
	}
	repos, discoveryFailures, err := discoverDefaultBranchFleet(filterFlag, options.owners, options.repositories, options.includeUser, options.allOrgs)
	if err != nil {
		return defaultBranchReport{}, err
	}
	locals, err := defaultBranchLocalClones(filterFlag)
	if err != nil {
		return defaultBranchReport{}, err
	}
	report := defaultBranchReport{SchemaVersion: defaultBranchSchemaVersion, Mode: "audit", Desired: strings.TrimSpace(options.branch)}
	if options.apply {
		report.Mode = "apply"
	}
	report.Repositories = make([]defaultBranchRepository, len(repos)+len(discoveryFailures))
	copy(report.Repositories, discoveryFailures)
	jobs := make(chan int)
	var wg sync.WaitGroup
	for worker := 0; worker < options.parallel; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				desired := effectiveDefaultBranch(options.branch, config, repos[i].Org)
				report.Repositories[len(discoveryFailures)+i] = inspectDefaultBranch(ctx, repos[i], desired)
			}
		}()
	}
	for i := range repos {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	if options.apply {
		path, err := defaultBranchReportPath(options.reportDir)
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
				report.Repositories[i] = applyDefaultBranch(ctx, report.Repositories[i])
				summarizeDefaultBranch(&report)
				if err := persistDefaultBranchReport(report); err != nil {
					return report, err
				}
				if len(report.Repositories[i].Actions) > 0 {
					if _, err := fmt.Fprintf(progress, "default-branch: applied %s\n", report.Repositories[i].Repository); err != nil {
						return report, err
					}
					sourceDefault, sourceHead = report.Repositories[i].ObservedDefault, report.Repositories[i].OldHead
				}
			}
			resumeReason := ""
			if sourceDefault == "" && report.Repositories[i].Disposition == "compliant" && prior != nil {
				sourceDefault, sourceHead, resumeReason = defaultBranchResumeSource(prior, report.Repositories[i])
			}
			if sourceDefault != "" {
				reconcileDefaultBranchCanonicals(ctx, &report.Repositories[i], locals[strings.ToLower(report.Repositories[i].Repository)], sourceDefault, sourceHead, func() error {
					summarizeDefaultBranch(&report)
					return persistDefaultBranchReport(report)
				})
			} else if resumeReason != "" {
				for _, clone := range locals[strings.ToLower(report.Repositories[i].Repository)] {
					report.Repositories[i].CanonicalClones = append(report.Repositories[i].CanonicalClones, defaultBranchCanonical{Path: clone.Path, Disposition: "blocked", Error: resumeReason})
				}
			}
		}
	}
	summarizeDefaultBranch(&report)
	if options.apply && report.ReportPath != "" {
		if err := persistDefaultBranchReport(report); err != nil {
			return report, err
		}
	}
	if !options.apply && options.reportDir != "" {
		path, err := defaultBranchReportPath(options.reportDir)
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

func readDefaultBranchReport(path string) (*defaultBranchReport, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read --reconcile-from report: %w", err)
	}
	var report defaultBranchReport
	if err := json.Unmarshal(raw, &report); err != nil {
		return nil, fmt.Errorf("decode --reconcile-from report: %w", err)
	}
	if report.SchemaVersion != defaultBranchSchemaVersion || report.Mode != "apply" {
		return nil, errors.New("--reconcile-from must be a default-branch apply report from this WB schema")
	}
	return &report, nil
}

func defaultBranchResumeSource(prior *defaultBranchReport, current defaultBranchRepository) (string, string, string) {
	if prior == nil || current.Desired == "" || current.ObservedDefault != current.Desired {
		return "", "", ""
	}
	for _, previous := range prior.Repositories {
		if !strings.EqualFold(previous.Repository, current.Repository) {
			continue
		}
		if previous.ObservedDefault == "" || previous.ObservedDefault == current.Desired || !validDefaultBranch(previous.ObservedDefault) || previous.Desired != current.Desired || previous.OldHead == "" || len(previous.Actions) == 0 {
			return "", "", "--reconcile-from does not contain a valid applied source/default binding for this repository"
		}
		if previous.OldHead != current.OldHead {
			return "", "", "--reconcile-from source SHA no longer matches the refreshed remote default; rerun the audit before reconciling this clone"
		}
		return previous.ObservedDefault, previous.OldHead, ""
	}
	return "", "", "--reconcile-from has no applied migration record for this repository"
}

func defaultBranchLocalClones(filter string) (map[string][]discover.Repo, error) {
	if strings.TrimSpace(projectsRoot) == "" {
		return map[string][]discover.Repo{}, nil
	}
	local, err := discover.ScanLocal(projectsRoot)
	if err != nil {
		return nil, fmt.Errorf("scan local canonical clones: %w", err)
	}
	bySlug := make(map[string][]discover.Repo)
	for _, clone := range local {
		if filter != "" && !strings.Contains(clone.Slug(), filter) {
			continue
		}
		key := strings.ToLower(clone.Slug())
		bySlug[key] = append(bySlug[key], clone)
	}
	for key := range bySlug {
		sort.Slice(bySlug[key], func(i, j int) bool { return bySlug[key][i].Path < bySlug[key][j].Path })
	}
	return bySlug, nil
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
func requestedDefaultBranchOwners(command *cobra.Command, owners []string) []string {
	selected := append([]string(nil), owners...)
	if rootOrg := command.Root().PersistentFlags().Lookup("org"); rootOrg != nil && rootOrg.Changed {
		selected = append(selected, extraOrgs...)
	}
	return selected
}

func discoverDefaultBranchFleet(filter string, owners, exact []string, includeUser, allOrgs bool) ([]discover.Repo, []defaultBranchRepository, error) {
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
			user, err := defaultBranchAuthUser()
			if err != nil {
				return nil, nil, err
			}
			selected[user] = true
		}
		orgs, err := defaultBranchMemberOrgs()
		if err != nil {
			return nil, nil, err
		}
		for _, org := range orgs {
			selected[org] = true
		}
	}
	if includeUser {
		user, err := defaultBranchAuthUser()
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
	var failures []defaultBranchRepository
	seen := map[string]bool{}
	for _, owner := range names {
		listed, err := defaultBranchListRemote(owner)
		if err != nil {
			failures = append(failures, defaultBranchRepository{Repository: owner + "/*", Disposition: "error", Error: "list GitHub repositories: " + err.Error()})
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
func inspectDefaultBranch(ctx context.Context, repo discover.Repo, desired string) defaultBranchRepository {
	result := defaultBranchRepository{Repository: repo.Slug(), Desired: desired}
	if !validDefaultBranch(desired) {
		result.Disposition = "blocked"
		result.Error = "invalid desired branch: set fleet.default_branch, fleet.organizations.<owner>.default_branch, or --branch to a Git ref name"
		return result
	}
	body, err := defaultBranchRead(ctx, "repos/"+repo.Slug())
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
	result.ObservedDefault, result.Archived, result.Fork = meta.DefaultBranch, meta.Archived, meta.Fork
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
	oldRef, err := readDefaultBranchRef(ctx, repo.Slug(), meta.DefaultBranch)
	if err != nil {
		result.Disposition = "error"
		result.Error = err.Error()
		return result
	}
	result.OldHead = oldRef
	if meta.DefaultBranch == desired {
		result.NewHead = oldRef
		result.Disposition = "compliant"
		return result
	}
	if meta.Archived {
		result.Disposition = "blocked"
		result.Error = "archived repository: GitHub makes archived repositories read-only; WB does not temporarily unarchive it"
		return result
	}
	newRef, err := readDefaultBranchRef(ctx, repo.Slug(), desired)
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
	if err := defaultBranchSafety(ctx, &result, meta, meta.DefaultBranch); err != nil {
		result.Disposition = "blocked"
		result.Error = err.Error()
		return result
	}
	result.Disposition = "drift"
	return result
}
func readDefaultBranchRef(ctx context.Context, slug, branch string) (string, error) {
	body, err := defaultBranchRead(ctx, "repos/"+slug+"/branches/"+url.PathEscape(branch))
	if err != nil {
		return "", err
	}
	var ref defaultBranchRef
	if err := json.Unmarshal(body, &ref); err != nil || ref.Commit.SHA == "" {
		return "", fmt.Errorf("decode branch %s: %w", branch, err)
	}
	return ref.Commit.SHA, nil
}
func defaultBranchSafety(ctx context.Context, result *defaultBranchRepository, meta defaultBranchRepoMetadata, old string) error {
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
		pulls, err := defaultBranchRead(ctx, "repos/"+querySlug+"/pulls?state=open&head="+url.QueryEscape(headOwner+":"+old))
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
	// Raw workflow URLs and actions `uses: owner/repo@branch` do not follow a
	// renamed branch. Read each workflow and block only a concrete old-branch
	// reference; workflows without one remain eligible.
	workflows, err := defaultBranchRead(ctx, "repos/"+slug+"/contents/.github/workflows?ref="+url.QueryEscape(old))
	if err != nil {
		if isDefaultBranchNotFound(err) {
			workflows = []byte("[]")
		} else {
			return fmt.Errorf("list workflows at source branch: %w", err)
		}
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
		if entry.Type == "file" && (strings.HasSuffix(entry.Path, ".yml") || strings.HasSuffix(entry.Path, ".yaml")) {
			blob, err := defaultBranchRead(ctx, "repos/"+slug+"/git/blobs/"+entry.SHA)
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
			contents := value.Content
			switch value.Encoding {
			case "base64":
				decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(contents, "\n", ""))
				if err != nil {
					return fmt.Errorf("decode workflow %s content: %w", entry.Path, err)
				}
				contents = string(decoded)
			case "":
				return fmt.Errorf("workflow %s did not declare a content encoding", entry.Path)
			default:
				return fmt.Errorf("workflow %s uses unsupported content encoding %q", entry.Path, value.Encoding)
			}
			if workflowReferencesDefaultBranch(contents, old) {
				result.Impacts = append(result.Impacts, "workflow old-branch reference: "+entry.Path)
				return fmt.Errorf("workflow %s references %q; WB will not blindly rewrite it", entry.Path, old)
			}
		}
	}
	for _, endpoint := range []string{"repos/" + slug + "/pages", "repos/" + slug + "/branches/" + url.PathEscape(old) + "/protection", "repos/" + slug + "/rules/branches/" + url.PathEscape(old) + "?per_page=100"} {
		body, err := defaultBranchRead(ctx, endpoint)
		if err == nil && len(body) > 0 && string(body) != "[]" {
			result.Impacts = append(result.Impacts, "inspect before apply: "+endpoint)
			return fmt.Errorf("pages, classic protection, or effective rules require an explicit migration; WB will not weaken or assume renamed coverage")
		}
		if err != nil && !isDefaultBranchNotFound(err) {
			return fmt.Errorf("inspect branch impact: %w", err)
		}
	}
	return nil
}

func isDefaultBranchNotFound(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "404")
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
func applyDefaultBranch(ctx context.Context, repo defaultBranchRepository) defaultBranchRepository {
	owner, name, ok := strings.Cut(repo.Repository, "/")
	if !ok || owner == "" || name == "" {
		repo.Disposition = "error"
		repo.Error = "invalid repository observation"
		return repo
	}
	fresh := inspectDefaultBranch(ctx, discover.Repo{Org: owner, Name: name}, repo.Desired)
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
	response := defaultBranchExecute(ctx, args...)
	if response.Err != nil {
		repo.Disposition = "error"
		repo.Error = githubCommandMessage(response)
		return repo
	}
	verified := inspectDefaultBranch(ctx, discover.Repo{Org: owner, Name: name}, repo.Desired)
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
	repo.VerifiedDefault = verified.ObservedDefault
	repo.NewHead = verified.NewHead
	if repo.TargetExists {
		repo.Actions = []string{"set default branch to existing same-SHA " + repo.Desired, "verified default branch and head"}
	} else {
		repo.Actions = []string{"renamed " + repo.ObservedDefault + " to " + repo.Desired, "verified default branch and head"}
	}
	return repo
}

// reconcileDefaultBranchCanonicals changes only the old default branch in a
// clean canonical clone that exactly matches the freshly fetched remote head.
// It deliberately leaves every other local state in place and records why a
// clone was preserved. A report checkpoint happens immediately before and
// after the one local branch mutation.
func reconcileDefaultBranchCanonicals(ctx context.Context, repository *defaultBranchRepository, clones []discover.Repo, sourceDefault, sourceHead string, checkpoint func() error) {
	for _, clone := range clones {
		repository.CanonicalClones = append(repository.CanonicalClones, defaultBranchCanonical{Path: clone.Path, Disposition: "blocked"})
		entry := &repository.CanonicalClones[len(repository.CanonicalClones)-1]
		if err := reconcileDefaultBranchCanonical(ctx, repository, entry, sourceDefault, sourceHead, checkpoint); err != nil {
			entry.Error = err.Error()
		}
	}
}

func reconcileDefaultBranchCanonical(ctx context.Context, repository *defaultBranchRepository, entry *defaultBranchCanonical, sourceDefault, sourceHead string, checkpoint func() error) error {
	if _, err := defaultBranchGit(ctx, entry.Path, "fetch", "--prune", "origin"); err != nil {
		return fmt.Errorf("refresh origin before local reconciliation: %w", err)
	}
	if _, err := defaultBranchGit(ctx, entry.Path, "remote", "set-head", "origin", "--auto"); err != nil {
		return fmt.Errorf("refresh origin/HEAD before local reconciliation: %w", err)
	}
	status, err := defaultBranchGit(ctx, entry.Path, "status", "--porcelain")
	if err != nil {
		return fmt.Errorf("inspect local changes: %w", err)
	}
	if status != "" {
		return errors.New("local changes present; preserve the canonical clone and reconcile it after committing or rescuing the work")
	}
	unpushed, err := defaultBranchGit(ctx, entry.Path, "log", "--branches", "--not", "--remotes", "--format=%H")
	if err != nil {
		return fmt.Errorf("inspect local unpublished commits: %w", err)
	}
	if unpushed != "" {
		return errors.New("local unpublished commits present; preserve the canonical clone and reconcile it after pushing or rescuing the work")
	}
	worktrees, err := defaultBranchGit(ctx, entry.Path, "worktree", "list", "--porcelain")
	if err != nil {
		return fmt.Errorf("inspect linked worktrees: %w", err)
	}
	if strings.Count(worktrees, "worktree ") != 1 {
		return errors.New("linked worktree exists; preserve every branch until that worktree is retired or moved")
	}
	current, err := defaultBranchGit(ctx, entry.Path, "branch", "--show-current")
	if err != nil {
		return fmt.Errorf("inspect checked-out branch: %w", err)
	}
	entry.ObservedBranch = current
	remoteHead, err := defaultBranchGit(ctx, entry.Path, "rev-parse", "origin/"+repository.Desired)
	if err != nil {
		return fmt.Errorf("resolve refreshed origin/%s: %w", repository.Desired, err)
	}
	entry.RemoteHead = remoteHead
	if remoteHead != sourceHead {
		return fmt.Errorf("origin/%s is %s, not planned source %s; rerun the audit before local reconciliation", repository.Desired, remoteHead, sourceHead)
	}
	if current == repository.Desired {
		localHead, err := defaultBranchGit(ctx, entry.Path, "rev-parse", repository.Desired)
		if err != nil {
			return fmt.Errorf("resolve local %s: %w", repository.Desired, err)
		}
		if localHead != remoteHead {
			return fmt.Errorf("local %s is %s while origin/%s is %s; run wb sync --filter %s then retry --reconcile-from", repository.Desired, localHead, repository.Desired, remoteHead, repository.Repository)
		}
		upstream, upstreamErr := defaultBranchGit(ctx, entry.Path, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
		if upstreamErr == nil && upstream == "origin/"+repository.Desired {
			entry.Disposition = "compliant"
			entry.Actions = []string{"local " + repository.Desired + " already reconciled"}
			return nil
		}
		entry.Disposition = "drift"
		if err := checkpoint(); err != nil {
			return fmt.Errorf("persist local tracking repair plan: %w", err)
		}
		if _, err := defaultBranchGit(ctx, entry.Path, "branch", "--set-upstream-to=origin/"+repository.Desired, repository.Desired); err != nil {
			return fmt.Errorf("restore local tracking branch: %w", err)
		}
		entry.Disposition = "compliant"
		entry.Actions = []string{"restored upstream origin/" + repository.Desired}
		return checkpoint()
	}
	if current != sourceDefault {
		return fmt.Errorf("canonical checkout is on %q; only the old default %q may be renamed automatically", current, sourceDefault)
	}
	branches, err := defaultBranchGit(ctx, entry.Path, "for-each-ref", "--format=%(refname:strip=2)", "refs/heads")
	if err != nil {
		return fmt.Errorf("inspect local branch names: %w", err)
	}
	for _, branch := range strings.Split(branches, "\n") {
		if branch == repository.Desired {
			return fmt.Errorf("local destination branch %q already exists", repository.Desired)
		}
	}
	localHead, err := defaultBranchGit(ctx, entry.Path, "rev-parse", sourceDefault)
	if err != nil {
		return fmt.Errorf("resolve local %s: %w", sourceDefault, err)
	}
	if localHead != remoteHead {
		return fmt.Errorf("local %s is %s while origin/%s is %s; run wb sync --filter %s then retry --reconcile-from", sourceDefault, localHead, repository.Desired, remoteHead, repository.Repository)
	}
	entry.Disposition = "drift"
	if err := checkpoint(); err != nil {
		return fmt.Errorf("persist local-reconciliation plan: %w", err)
	}
	if _, err := defaultBranchGit(ctx, entry.Path, "branch", "-m", sourceDefault, repository.Desired); err != nil {
		return fmt.Errorf("rename local default branch: %w", err)
	}
	if _, err := defaultBranchGit(ctx, entry.Path, "branch", "--set-upstream-to=origin/"+repository.Desired, repository.Desired); err != nil {
		entry.Disposition = "error"
		entry.Actions = []string{"renamed local " + sourceDefault + " to " + repository.Desired}
		if checkpointErr := checkpoint(); checkpointErr != nil {
			return fmt.Errorf("set local tracking branch: %v; persist partial local reconciliation receipt: %w", err, checkpointErr)
		}
		return fmt.Errorf("set local tracking branch: %w", err)
	}
	entry.Disposition = "compliant"
	entry.Actions = []string{"renamed local " + sourceDefault + " to " + repository.Desired, "set upstream to origin/" + repository.Desired}
	if err := checkpoint(); err != nil {
		return fmt.Errorf("persist local-reconciliation receipt: %w", err)
	}
	return nil
}

func summarizeDefaultBranch(report *defaultBranchReport) {
	report.Summary = defaultBranchSummary{Inspected: len(report.Repositories)}
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

func defaultBranchHasFindings(report defaultBranchReport) bool {
	return report.Summary.Drift+report.Summary.Blocked+report.Summary.Errors+
		report.Summary.CanonicalBlocked+report.Summary.CanonicalErrors > 0
}
func defaultBranchReportPath(dir string) (string, error) {
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
func persistDefaultBranchReport(report defaultBranchReport) error {
	payload, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(report.ReportPath), ".default-branch-*.json")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(payload); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, report.ReportPath); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(report.ReportPath))
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}
func printDefaultBranchReport(out io.Writer, report defaultBranchReport) error {
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
