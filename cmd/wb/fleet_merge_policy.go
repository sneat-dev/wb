package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/runqueue"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/spf13/cobra"
)

const mergePolicySchemaVersion = 1

type mergePolicyOptions struct {
	apply        bool
	includeUser  bool
	owners       []string
	parallel     int
	json         bool
	repositories []string
	reportDir    string
	resume       bool
}

type mergePolicyReport struct {
	SchemaVersion int                        `json:"schema_version"`
	Mode          string                     `json:"mode"`
	Desired       mergePolicyDesired         `json:"desired"`
	Repositories  []mergePolicyRepository    `json:"repositories"`
	Rulesets      []mergePolicyRulesetChange `json:"rulesets,omitempty"`
	Summary       mergePolicySummary         `json:"summary"`
	ReportPath    string                     `json:"report_path,omitempty"`
}

type mergePolicyDesired struct {
	AllowMergeCommit bool   `json:"allow_merge_commit"`
	AllowSquashMerge bool   `json:"allow_squash_merge"`
	AllowRebaseMerge bool   `json:"allow_rebase_merge"`
	MergeCommitTitle string `json:"merge_commit_title"`
	MergeCommitBody  string `json:"merge_commit_message"`
}

var desiredMergePolicy = mergePolicyDesired{true, false, false, "PR_TITLE", "PR_BODY"}

type mergePolicyRepository struct {
	Repository     string               `json:"repository"`
	DefaultBranch  string               `json:"default_branch,omitempty"`
	Disposition    string               `json:"disposition"`
	Drift          []string             `json:"drift,omitempty"`
	Conflicts      []string             `json:"conflicts,omitempty"`
	Rulesets       []mergePolicyRuleRef `json:"rulesets,omitempty"`
	ObservedSHA    string               `json:"observed_sha,omitempty"`
	ProtectionSHA  string               `json:"protection_sha,omitempty"`
	ClassicLinear  bool                 `json:"classic_required_linear_history,omitempty"`
	AppliedActions []string             `json:"applied_actions,omitempty"`
	Error          string               `json:"error,omitempty"`
}

type mergePolicyRuleRef struct {
	Type       string   `json:"type"`
	SourceType string   `json:"source_type"`
	Source     string   `json:"source"`
	ID         int64    `json:"id"`
	Methods    []string `json:"allowed_merge_methods,omitempty"`
}

type mergePolicyRulesetChange struct {
	SourceType   string   `json:"source_type"`
	Source       string   `json:"source"`
	ID           int64    `json:"id"`
	Repositories []string `json:"selected_repositories"`
	Disposition  string   `json:"disposition"`
	ObservedSHA  string   `json:"observed_sha,omitempty"`
	Error        string   `json:"error,omitempty"`
}

type mergePolicySummary struct {
	Inspected int `json:"inspected"`
	Compliant int `json:"compliant"`
	Drift     int `json:"drift"`
	Blocked   int `json:"blocked"`
	Errors    int `json:"errors"`
	Applied   int `json:"applied"`
}

type githubRepositoryPolicy struct {
	DefaultBranch      string `json:"default_branch"`
	AllowMergeCommit   bool   `json:"allow_merge_commit"`
	AllowSquashMerge   bool   `json:"allow_squash_merge"`
	AllowRebaseMerge   bool   `json:"allow_rebase_merge"`
	MergeCommitTitle   string `json:"merge_commit_title"`
	MergeCommitMessage string `json:"merge_commit_message"`
}

type githubEffectiveRule struct {
	Type              string          `json:"type"`
	RulesetSourceType string          `json:"ruleset_source_type"`
	RulesetSource     string          `json:"ruleset_source"`
	RulesetID         int64           `json:"ruleset_id"`
	Parameters        json.RawMessage `json:"parameters"`
}

var (
	mergePolicyAuthUser   = discover.AuthUser
	mergePolicyMemberOrgs = discover.MemberOrgs
	mergePolicyListRemote = discover.ListRemote
	mergePolicyDiscover   = func(root, filter string, owners, repositories []string, includeUser bool) ([]discover.Repo, error) {
		return discoverRemoteMergePolicyFleet(filter, owners, repositories, includeUser)
	}
	mergePolicyRead = func(ctx context.Context, endpoint string) ([]byte, error) {
		return githubobserver.Read(ctx, "", "api", endpoint)
	}
	mergePolicyExecute = func(ctx context.Context, args ...string) githubobserver.CommandResponse {
		return githubobserver.Execute(ctx, "", args...)
	}
	mergePolicyPersist = persistMergePolicyReport
)

func newFleetMergePolicyCmd() *cobra.Command {
	defaultParallel := min(runqueue.Budget(), 16)
	options := mergePolicyOptions{parallel: defaultParallel}
	command := &cobra.Command{
		Use:   "merge-policy",
		Short: "Audit or apply merge-commit policy across GitHub repositories",
		Long: `Audit GitHub repository merge settings and effective default-branch rules.

The desired policy enables merge commits only and asks GitHub to use the pull
request title and body for the merge commit. Audit is the default and is
read-only. --apply is explicit: WB first inventories the exact selected scope,
writes the durable plan, then changes only merge settings.

Audit without selectors may inventory the authenticated user and member
organizations. Apply fails closed unless --org/-o, --repo, or --user selects
the mutation scope explicitly; --org restricts rather than enlarges that scope.

Repository-owned required-linear-history policy is desired drift: apply removes
the dedicated classic setting and the corresponding repository ruleset rule
while preserving every other protection; WB never weakens unrelated protection.
Existing merge-queue rules remain preserved blockers.
Existing organization and enterprise rulesets are higher-level authorities and remain
audit-only in this slice; repository fallback is refused when either conflicts.

Exit codes: 0 compliant/applied, 1 drift, conflicts, or inspection errors,
2 invalid usage.`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			options.owners = requestedMergePolicyOwners(command, options.owners)
			if len(options.repositories) > 0 && (len(options.owners) > 0 || options.includeUser) {
				return usageError("--repo cannot be combined with --org or --user")
			}
			if options.parallel < 1 || options.parallel > 16 {
				return usageError("--parallel must be between 1 and 16")
			}
			if options.resume && (!options.apply || strings.TrimSpace(options.reportDir) == "") {
				return usageError("--resume requires --apply and --report-dir")
			}
			if options.apply && len(options.owners) == 0 && len(options.repositories) == 0 && !options.includeUser {
				return usageError("--apply requires explicit --org, --repo, or --user scope")
			}
			report, err := runMergePolicy(command.Context(), options, command.ErrOrStderr())
			if err != nil {
				return err
			}
			if options.json {
				if err := writeJSONTo(command.OutOrStdout(), report); err != nil {
					return err
				}
			} else if err := printMergePolicyReport(command.OutOrStdout(), report); err != nil {
				return err
			}
			if report.Summary.Drift+report.Summary.Blocked+report.Summary.Errors > 0 {
				return &exitError{code: exitFindings, message: "merge-policy findings remain; see the report above"}
			}
			return nil
		},
	}
	command.Flags().BoolVar(&options.apply, "apply", false, "apply the exact planned merge policy; audit is the default")
	command.Flags().StringArrayVarP(&options.owners, "org", "o", nil, "only inspect or apply this GitHub organization (repeatable)")
	command.Flags().StringArrayVar(&options.repositories, "repo", nil, "only inspect or apply this exact owner/repository (repeatable)")
	command.Flags().BoolVar(&options.includeUser, "user", false, "include the authenticated user's own repositories in explicit scope")
	command.Flags().IntVar(&options.parallel, "parallel", defaultParallel, "maximum GitHub repositories to inspect or apply concurrently (1-16; default: WB CPU budget)")
	command.Flags().StringVar(&options.reportDir, "report-dir", "", "durable report directory (apply defaults below <wb-home>/reports/merge-policy)")
	command.Flags().BoolVar(&options.resume, "resume", false, "resume into an existing --report-dir after interruption; all decisions are re-observed")
	addJSONFormatFlags(command, &options.json)
	return command
}

func runMergePolicy(ctx context.Context, options mergePolicyOptions, progress io.Writer) (mergePolicyReport, error) {
	var inspected, total atomic.Int64
	resumedActions := map[string][]string{}
	heartbeatDone := make(chan struct{})
	defer close(heartbeatDone)
	go func() {
		ticker := time.NewTicker(9 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_, _ = fmt.Fprintf(progress, "merge-policy: working; inspected %d/%d repositories\n", inspected.Load(), total.Load())
			case <-heartbeatDone:
				return
			}
		}
	}()
	if options.resume {
		payload, err := os.ReadFile(filepath.Join(options.reportDir, "merge-policy.json"))
		if err != nil {
			return mergePolicyReport{}, fmt.Errorf("resume merge-policy report: %w", err)
		}
		var previous mergePolicyReport
		if err := json.Unmarshal(payload, &previous); err != nil {
			return mergePolicyReport{}, fmt.Errorf("decode resume merge-policy report: %w", err)
		}
		for _, repo := range previous.Repositories {
			resumedActions[repo.Repository] = append([]string(nil), repo.AppliedActions...)
		}
	}
	repos, err := mergePolicyDiscover(projectsRoot, filterFlag, options.owners, options.repositories, options.includeUser)
	if err != nil {
		return mergePolicyReport{}, err
	}
	selected := make([]discover.Repo, 0, len(repos))
	for _, repo := range repos {
		if repo.Remote && !repo.Archived {
			selected = append(selected, repo)
		}
	}
	total.Store(int64(len(selected)))
	report := mergePolicyReport{SchemaVersion: mergePolicySchemaVersion, Mode: "audit", Desired: desiredMergePolicy}
	if options.apply {
		report.Mode = "apply"
	}
	report.Repositories = make([]mergePolicyRepository, len(selected))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for worker := 0; worker < options.parallel; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				report.Repositories[index] = inspectMergePolicyRepository(ctx, selected[index].Slug())
				inspected.Add(1)
			}
		}()
	}
	for index := range selected {
		jobs <- index
	}
	close(jobs)
	wg.Wait()
	for index := range report.Repositories {
		for _, action := range resumedActions[report.Repositories[index].Repository] {
			report.Repositories[index].AppliedActions = appendUnique(report.Repositories[index].AppliedActions, action)
		}
	}
	buildMergePolicyRulesetPlan(ctx, &report)
	summarizeMergePolicy(&report)
	if options.apply {
		path, err := mergePolicyReportPath(options.reportDir)
		if err != nil {
			return report, err
		}
		report.ReportPath = path
		if err := persistMergePolicyReport(report); err != nil {
			return report, err
		}
		if _, err := fmt.Fprintf(progress, "merge-policy: planned %d repositories and %d shared rulesets; report %s\n", len(report.Repositories), len(report.Rulesets), path); err != nil {
			return report, fmt.Errorf("write merge-policy plan progress: %w", err)
		}
		if err := applyMergePolicy(ctx, &report, options.parallel, progress); err != nil {
			return report, err
		}
		summarizeMergePolicy(&report)
		if err := persistMergePolicyReport(report); err != nil {
			return report, err
		}
	} else if strings.TrimSpace(options.reportDir) != "" {
		path, err := mergePolicyReportPath(options.reportDir)
		if err != nil {
			return report, err
		}
		report.ReportPath = path
		if err := persistMergePolicyReport(report); err != nil {
			return report, err
		}
	}
	return report, nil
}

func discoverRemoteMergePolicyFleet(filter string, explicitOwners, exactRepositories []string, includeUser bool) ([]discover.Repo, error) {
	if len(exactRepositories) > 0 {
		var repos []discover.Repo
		seen := map[string]bool{}
		for _, slug := range exactRepositories {
			owner, name, ok := strings.Cut(strings.TrimSpace(slug), "/")
			if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
				return nil, fmt.Errorf("invalid --repo %q; use owner/repository", slug)
			}
			normalized := owner + "/" + name
			if seen[normalized] || (filter != "" && !strings.Contains(normalized, filter)) {
				continue
			}
			seen[normalized] = true
			repos = append(repos, discover.Repo{Org: owner, Name: name, Remote: true})
		}
		sort.Slice(repos, func(i, j int) bool { return repos[i].Slug() < repos[j].Slug() })
		return repos, nil
	}
	owners := map[string]bool{}
	if len(explicitOwners) == 0 && !includeUser {
		user, err := mergePolicyAuthUser()
		if err != nil {
			return nil, fmt.Errorf("resolve authenticated GitHub owner: %w", err)
		}
		owners[user] = true
		orgs, err := mergePolicyMemberOrgs()
		if err != nil {
			return nil, fmt.Errorf("list authenticated GitHub organizations: %w", err)
		}
		for _, org := range orgs {
			owners[org] = true
		}
	} else if includeUser {
		user, err := mergePolicyAuthUser()
		if err != nil {
			return nil, fmt.Errorf("resolve authenticated GitHub owner: %w", err)
		}
		owners[user] = true
	}
	for _, owner := range explicitOwners {
		if owner = strings.TrimSpace(owner); owner != "" {
			owners[owner] = true
		}
	}
	names := make([]string, 0, len(owners))
	for owner := range owners {
		names = append(names, owner)
	}
	sort.Strings(names)
	var repos []discover.Repo
	for _, owner := range names {
		listed, err := mergePolicyListRemote(owner)
		if err != nil {
			return nil, fmt.Errorf("list GitHub repositories for %s: %w", owner, err)
		}
		for _, repo := range listed {
			if filter == "" || strings.Contains(repo.Slug(), filter) {
				repo.Remote = true
				repos = append(repos, repo)
			}
		}
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].Slug() < repos[j].Slug() })
	return repos, nil
}

func requestedMergePolicyOwners(command *cobra.Command, owners []string) []string {
	selected := append([]string(nil), owners...)
	if rootOrg := command.Root().PersistentFlags().Lookup("org"); rootOrg != nil && rootOrg.Changed {
		selected = append(selected, extraOrgs...)
	}
	return selected
}

func inspectMergePolicyRepository(ctx context.Context, slug string) mergePolicyRepository {
	result := mergePolicyRepository{Repository: slug}
	body, err := mergePolicyRead(ctx, "repos/"+slug)
	if err != nil {
		result.Disposition, result.Error = "error", err.Error()
		return result
	}
	var policy githubRepositoryPolicy
	if err := json.Unmarshal(body, &policy); err != nil {
		result.Disposition, result.Error = "error", "decode repository settings: "+err.Error()
		return result
	}
	result.DefaultBranch = policy.DefaultBranch
	result.ObservedSHA = digestJSON(body)
	if !policy.AllowMergeCommit {
		result.Drift = append(result.Drift, "allow_merge_commit=false")
	}
	if policy.AllowSquashMerge {
		result.Drift = append(result.Drift, "allow_squash_merge=true")
	}
	if policy.AllowRebaseMerge {
		result.Drift = append(result.Drift, "allow_rebase_merge=true")
	}
	if policy.MergeCommitTitle != desiredMergePolicy.MergeCommitTitle {
		result.Drift = append(result.Drift, "merge_commit_title="+policy.MergeCommitTitle)
	}
	if policy.MergeCommitMessage != desiredMergePolicy.MergeCommitBody {
		result.Drift = append(result.Drift, "merge_commit_message="+policy.MergeCommitMessage)
	}
	protectionSHA, classicLinear, protectionConflicts, err := inspectClassicProtection(ctx, slug, policy.DefaultBranch)
	if err != nil {
		result.Disposition, result.Error = "error", err.Error()
		return result
	}
	result.ProtectionSHA = protectionSHA
	result.ClassicLinear = classicLinear
	if classicLinear {
		result.Drift = append(result.Drift, "classic branch protection requires linear history")
	}
	result.Conflicts = append(result.Conflicts, protectionConflicts...)
	rulesBody, err := mergePolicyRead(ctx, "repos/"+slug+"/rules/branches/"+policy.DefaultBranch+"?per_page=100")
	if err != nil {
		result.Disposition, result.Error = "error", "read effective rules: "+err.Error()
		return result
	}
	var rules []githubEffectiveRule
	if err := json.Unmarshal(rulesBody, &rules); err != nil {
		result.Disposition, result.Error = "error", "decode effective rules: "+err.Error()
		return result
	}
	for _, rule := range rules {
		ref := mergePolicyRuleRef{Type: rule.Type, SourceType: rule.RulesetSourceType, Source: rule.RulesetSource, ID: rule.RulesetID}
		switch rule.Type {
		case "required_linear_history":
			if strings.EqualFold(rule.RulesetSourceType, "Repository") {
				result.Drift = append(result.Drift, describeRuleConflict(rule, "requires linear history"))
			} else {
				result.Conflicts = append(result.Conflicts, describeRuleConflict(rule, "requires linear history"))
			}
		case "merge_queue":
			result.Conflicts = append(result.Conflicts, describeRuleConflict(rule, "requires the merge queue"))
		case "pull_request":
			var parameters struct {
				AllowedMergeMethods []string `json:"allowed_merge_methods"`
			}
			_ = json.Unmarshal(rule.Parameters, &parameters)
			ref.Methods = parameters.AllowedMergeMethods
			if len(parameters.AllowedMergeMethods) == 0 {
				result.Conflicts = append(result.Conflicts, describeRuleConflict(rule, "did not report allowed merge methods"))
			} else if !allowsMerge(parameters.AllowedMergeMethods) {
				if strings.EqualFold(rule.RulesetSourceType, "Repository") {
					result.Drift = append(result.Drift, describeRuleConflict(rule, "does not allow merge commits"))
				} else {
					result.Conflicts = append(result.Conflicts, describeRuleConflict(rule, "does not allow merge commits"))
				}
			}
		}
		if rule.RulesetID != 0 {
			result.Rulesets = append(result.Rulesets, ref)
		}
	}
	switch {
	case len(result.Conflicts) > 0:
		result.Disposition = "blocked"
	case len(result.Drift) > 0:
		result.Disposition = "drift"
	default:
		result.Disposition = "compliant"
	}
	return result
}

func inspectClassicProtection(ctx context.Context, slug, branch string) (string, bool, []string, error) {
	endpoint := "repos/" + slug + "/branches/" + url.PathEscape(branch) + "/protection"
	body, err := mergePolicyRead(ctx, endpoint)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "http 404") {
			return "none", false, nil, nil
		}
		return "", false, nil, fmt.Errorf("read classic branch protection: %w", err)
	}
	var protection struct {
		RequiredLinearHistory *struct {
			Enabled bool `json:"enabled"`
		} `json:"required_linear_history"`
		RequiredMergeQueue *struct {
			Enabled bool `json:"enabled"`
		} `json:"required_merge_queue"`
		MergeQueue *struct {
			Enabled bool `json:"enabled"`
		} `json:"merge_queue"`
	}
	if err := json.Unmarshal(body, &protection); err != nil {
		return "", false, nil, fmt.Errorf("decode classic branch protection: %w", err)
	}
	var conflicts []string
	linearHistory := protection.RequiredLinearHistory != nil && protection.RequiredLinearHistory.Enabled
	if (protection.RequiredMergeQueue != nil && protection.RequiredMergeQueue.Enabled) || (protection.MergeQueue != nil && protection.MergeQueue.Enabled) {
		conflicts = append(conflicts, "classic branch protection requires the merge queue")
	}
	return digestJSON(body), linearHistory, conflicts, nil
}

func describeRuleConflict(rule githubEffectiveRule, detail string) string {
	return fmt.Sprintf("%s ruleset %d (%s): %s", strings.ToLower(rule.RulesetSourceType), rule.RulesetID, rule.RulesetSource, detail)
}

func buildMergePolicyRulesetPlan(ctx context.Context, report *mergePolicyReport) {
	byKey := map[string]*mergePolicyRulesetChange{}
	for _, repo := range report.Repositories {
		for _, ref := range repo.Rulesets {
			if (ref.Type != "pull_request" || allowsMerge(ref.Methods)) && ref.Type != "required_linear_history" {
				continue
			}
			key := fmt.Sprintf("%s/%s/%d", strings.ToLower(ref.SourceType), ref.Source, ref.ID)
			change := byKey[key]
			if change == nil {
				change = &mergePolicyRulesetChange{SourceType: ref.SourceType, Source: ref.Source, ID: ref.ID, Disposition: "planned"}
				byKey[key] = change
			}
			if !containsSorted(change.Repositories, repo.Repository) {
				change.Repositories = append(change.Repositories, repo.Repository)
				sort.Strings(change.Repositories)
			}
		}
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		change := byKey[key]
		sort.Strings(change.Repositories)
		for _, selected := range report.Repositories {
			if !containsSorted(change.Repositories, selected.Repository) {
				continue
			}
			if len(selected.Conflicts) > 0 {
				change.Disposition = "blocked"
				change.Error = "selected repository has required-linear-history or merge-queue conflict; no shared policy was changed"
				break
			}
		}
		if change.Disposition == "blocked" {
			report.Rulesets = append(report.Rulesets, *change)
			continue
		}
		if strings.EqualFold(change.SourceType, "Enterprise") || strings.EqualFold(change.SourceType, "Organization") {
			change.Disposition = "blocked"
			change.Error = strings.ToLower(change.SourceType) + " ruleset is a higher-level authority and is audit-only; no repository fallback or ruleset mutation was attempted"
		}
		if change.Disposition != "blocked" {
			endpoint := fmt.Sprintf("repos/%s/rulesets/%d", change.Source, change.ID)
			if strings.EqualFold(change.SourceType, "Organization") {
				endpoint = fmt.Sprintf("orgs/%s/rulesets/%d", change.Source, change.ID)
			}
			body, err := mergePolicyRead(ctx, endpoint)
			if err != nil {
				change.Disposition, change.Error = "blocked", "snapshot ruleset before apply: "+err.Error()
			} else {
				change.ObservedSHA = digestJSON(body)
			}
		}
		report.Rulesets = append(report.Rulesets, *change)
	}
}

func applyMergePolicy(ctx context.Context, report *mergePolicyReport, parallel int, progress io.Writer) error {
	checkpoint := func() error {
		if report.ReportPath == "" {
			return nil
		}
		summarizeMergePolicy(report)
		return mergePolicyPersist(*report)
	}
	// Validate every repository lease before the first mutation. A shared
	// ruleset may affect several repositories, so discovering repository drift
	// after changing it would leave a partially applied plan.
	for index := range report.Repositories {
		repo := &report.Repositories[index]
		if repo.Disposition != "drift" {
			continue
		}
		fresh, err := mergePolicyRead(ctx, "repos/"+repo.Repository)
		protectionSHA, _, _, protectionErr := inspectClassicProtection(ctx, repo.Repository, repo.DefaultBranch)
		if err != nil || protectionErr != nil || digestJSON(fresh) != repo.ObservedSHA || protectionSHA != repo.ProtectionSHA {
			repo.Disposition = "blocked"
			repo.Conflicts = append(repo.Conflicts, "repository settings or classic protection changed after planning; rerun the audit")
		}
	}
	blockedRulesets := map[string]bool{}
	for index := range report.Rulesets {
		change := &report.Rulesets[index]
		key := fmt.Sprintf("%s/%s/%d", strings.ToLower(change.SourceType), change.Source, change.ID)
		if change.Disposition == "blocked" {
			blockedRulesets[key] = true
			continue
		}
		for _, repository := range change.Repositories {
			for _, repo := range report.Repositories {
				if repo.Repository == repository && (repo.Disposition == "blocked" || repo.Disposition == "error") {
					change.Disposition = "blocked"
					change.Error = "a repository in the shared ruleset scope changed or is blocked; no shared policy was changed"
					blockedRulesets[key] = true
				}
			}
		}
		if change.Disposition == "blocked" {
			continue
		}
		if err := applySharedRuleset(ctx, *change); err != nil {
			change.Disposition, change.Error = "error", err.Error()
			blockedRulesets[key] = true
		} else {
			change.Disposition = "applied"
		}
		if err := checkpoint(); err != nil {
			return err
		}
	}
	if err := checkpoint(); err != nil {
		return err
	}
	if parallel < 1 {
		parallel = 1
	}
	type repositoryResult struct {
		index         int
		repo          mergePolicyRepository
		checkpointErr error
	}
	eligible := make([]int, 0, len(report.Repositories))
	results := make(chan repositoryResult, len(report.Repositories))
	for index := range report.Repositories {
		repo := report.Repositories[index]
		if repo.Disposition == "compliant" || repo.Disposition == "error" || len(repo.Conflicts) > 0 {
			continue
		}
		eligible = append(eligible, index)
	}
	var checkpointMu sync.Mutex
	checkpointRepository := func(index int, repo mergePolicyRepository) error {
		checkpointMu.Lock()
		defer checkpointMu.Unlock()
		report.Repositories[index] = repo
		return checkpoint()
	}
	launch := func(index int) {
		go func() {
			repo, err := applyRepositoryMergePolicy(ctx, report.Repositories[index], blockedRulesets, func(repo mergePolicyRepository) error {
				return checkpointRepository(index, repo)
			})
			results <- repositoryResult{index: index, repo: repo, checkpointErr: err}
		}()
	}
	next, inFlight := 0, 0
	for next < len(eligible) && inFlight < parallel {
		launch(eligible[next])
		next++
		inFlight++
	}
	var firstCheckpointErr error
	for inFlight > 0 {
		result := <-results
		inFlight--
		if result.checkpointErr != nil && firstCheckpointErr == nil {
			firstCheckpointErr = result.checkpointErr
		}
		if result.repo.Disposition == "applied" {
			_, _ = fmt.Fprintf(progress, "merge-policy: applied %s\n", result.repo.Repository)
		}
		if err := checkpointRepository(result.index, result.repo); err != nil && firstCheckpointErr == nil {
			firstCheckpointErr = err
		}
		if firstCheckpointErr == nil && next < len(eligible) {
			launch(eligible[next])
			next++
			inFlight++
		}
	}
	return firstCheckpointErr
}

func applyRepositoryMergePolicy(ctx context.Context, repo mergePolicyRepository, blockedRulesets map[string]bool, checkpoint func(mergePolicyRepository) error) (mergePolicyRepository, error) {
	for _, ref := range repo.Rulesets {
		if blockedRulesets[fmt.Sprintf("%s/%s/%d", strings.ToLower(ref.SourceType), ref.Source, ref.ID)] {
			repo.Disposition = "blocked"
			repo.Conflicts = append(repo.Conflicts, "shared ruleset update was not safe or supported")
			return repo, nil
		}
	}
	fresh, err := mergePolicyRead(ctx, "repos/"+repo.Repository)
	protectionSHA, classicLinear, conflicts, protectionErr := inspectClassicProtection(ctx, repo.Repository, repo.DefaultBranch)
	if err != nil || protectionErr != nil || digestJSON(fresh) != repo.ObservedSHA || protectionSHA != repo.ProtectionSHA || len(conflicts) > 0 {
		repo.Disposition = "blocked"
		repo.Conflicts = append(repo.Conflicts, "repository settings or classic protection changed after planning; rerun the audit")
		return repo, nil
	}
	if classicLinear {
		endpoint := "repos/" + repo.Repository + "/branches/" + url.PathEscape(repo.DefaultBranch) + "/protection/required_linear_history"
		response := mergePolicyExecute(ctx, "api", "--method", "DELETE", endpoint)
		if response.Err != nil {
			repo.Disposition = "error"
			repo.Error = "remove classic required linear history: " + githubCommandMessage(response)
			return repo, nil
		}
		repo.ClassicLinear = false
		repo.AppliedActions = appendUnique(repo.AppliedActions, "removed_classic_required_linear_history")
		if err := checkpoint(repo); err != nil {
			return repo, err
		}
	}
	response := mergePolicyExecute(ctx, "api", "--method", "PATCH", "repos/"+repo.Repository,
		"-F", "allow_merge_commit=true", "-F", "allow_squash_merge=false", "-F", "allow_rebase_merge=false",
		"-f", "merge_commit_title=PR_TITLE", "-f", "merge_commit_message=PR_BODY")
	if response.Err != nil {
		repo.Disposition = "error"
		repo.Error = githubCommandMessage(response)
		return repo, nil
	}
	repo.Disposition = "applied"
	repo.Drift = nil
	repo.AppliedActions = appendUnique(repo.AppliedActions, "updated_repository_merge_settings")
	return repo, nil
}

func applySharedRuleset(ctx context.Context, change mergePolicyRulesetChange) error {
	if !strings.EqualFold(change.SourceType, "Repository") {
		return fmt.Errorf("%s ruleset application is unsupported and audit-only", strings.ToLower(change.SourceType))
	}
	endpoint := fmt.Sprintf("repos/%s/rulesets/%d", change.Source, change.ID)
	body, err := mergePolicyRead(ctx, endpoint)
	if err != nil {
		return err
	}
	if change.ObservedSHA != "" && digestJSON(body) != change.ObservedSHA {
		return fmt.Errorf("ruleset changed after planning; rerun the audit")
	}
	var full map[string]any
	if err := json.Unmarshal(body, &full); err != nil {
		return err
	}
	rules, ok := full["rules"].([]any)
	if !ok {
		return fmt.Errorf("ruleset response has no rules")
	}
	changed := false
	updatedRules := make([]any, 0, len(rules))
	for _, value := range rules {
		rule, ok := value.(map[string]any)
		if !ok {
			updatedRules = append(updatedRules, value)
			continue
		}
		if rule["type"] == "required_linear_history" {
			changed = true
			continue
		}
		updatedRules = append(updatedRules, value)
		if rule["type"] != "pull_request" {
			continue
		}
		parameters, ok := rule["parameters"].(map[string]any)
		if !ok {
			parameters = map[string]any{}
			rule["parameters"] = parameters
		}
		parameters["allowed_merge_methods"] = []string{"merge"}
		changed = true
	}
	if !changed {
		return fmt.Errorf("ruleset has no required-linear-history or pull-request policy to change")
	}
	full["rules"] = updatedRules
	// Response-only fields are not accepted by GitHub's update endpoint.
	for _, key := range []string{"id", "node_id", "source", "source_type", "_links", "created_at", "updated_at", "current_user_can_bypass"} {
		delete(full, key)
	}
	payload, err := json.Marshal(full)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp("", "wb-merge-policy-ruleset-*.json")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer func() {
		_ = os.Remove(name)
	}()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(payload); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	response := mergePolicyExecute(ctx, "api", "--method", "PUT", endpoint, "--input", name)
	if response.Err != nil {
		return fmt.Errorf("update %s ruleset %d: %s", strings.ToLower(change.SourceType), change.ID, githubCommandMessage(response))
	}
	return nil
}

func summarizeMergePolicy(report *mergePolicyReport) {
	report.Summary = mergePolicySummary{Inspected: len(report.Repositories)}
	for _, repo := range report.Repositories {
		switch repo.Disposition {
		case "compliant":
			report.Summary.Compliant++
		case "applied":
			report.Summary.Applied++
		case "drift":
			report.Summary.Drift++
		case "blocked":
			report.Summary.Blocked++
		case "error":
			report.Summary.Errors++
		}
	}
}

func mergePolicyReportPath(explicit string) (string, error) {
	dir := strings.TrimSpace(explicit)
	if dir == "" {
		home, err := wbhome.EnsureRoot(projectsRoot)
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, "reports", "merge-policy", time.Now().UTC().Format("20060102T150405.000000000Z"))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, "merge-policy.json"), nil
}

func persistMergePolicyReport(report mergePolicyReport) error {
	payload, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return os.WriteFile(report.ReportPath, payload, 0o600)
}

func printMergePolicyReport(out io.Writer, report mergePolicyReport) error {
	if _, err := fmt.Fprintf(out, "Merge policy: %s\n", report.Mode); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "  %d repositories · %d compliant · %d drift · %d blocked · %d errors · %d applied\n", report.Summary.Inspected, report.Summary.Compliant, report.Summary.Drift, report.Summary.Blocked, report.Summary.Errors, report.Summary.Applied); err != nil {
		return err
	}
	for _, repo := range report.Repositories {
		detail := strings.Join(append(append([]string{}, repo.Drift...), repo.Conflicts...), "; ")
		if repo.Error != "" {
			detail = repo.Error
		}
		if detail == "" {
			detail = "merge commits only; PR title + PR body"
		}
		if _, err := fmt.Fprintf(out, "  %-9s %-42s %s\n", repo.Disposition, repo.Repository, detail); err != nil {
			return err
		}
	}
	for _, rule := range report.Rulesets {
		if _, err := fmt.Fprintf(out, "  %-9s %s ruleset %d (%d selected) %s\n", rule.Disposition, strings.ToLower(rule.SourceType), rule.ID, len(rule.Repositories), rule.Error); err != nil {
			return err
		}
	}
	if report.ReportPath != "" {
		if _, err := fmt.Fprintf(out, "Report: %s\n", report.ReportPath); err != nil {
			return err
		}
	}
	return nil
}

func allowsMerge(values []string) bool {
	for _, value := range values {
		if value == "merge" {
			return true
		}
	}
	return false
}
func appendUnique(values []string, value string) []string {
	for _, current := range values {
		if current == value {
			return values
		}
	}
	return append(values, value)
}
func containsSorted(values []string, want string) bool {
	index := sort.SearchStrings(values, want)
	return index < len(values) && values[index] == want
}
func digestJSON(body []byte) string { sum := sha256.Sum256(body); return hex.EncodeToString(sum[:]) }
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
