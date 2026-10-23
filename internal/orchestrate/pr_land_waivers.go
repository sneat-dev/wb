package orchestrate

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/sneat-dev/wb/internal/githubchecks"
)

// Waiver repairs for `wb pr land` (spec/features/pr-land-waivers).
//
// Every read below happens only on a landing that carries a --waive-* flag,
// and the baseline walk only when the target tip has no terminal run of a
// waived check, so an ordinary landing pays nothing for this file.

const (
	// LandRefusalWaivedCheckPending reports that the wait ended while the only
	// checks still running were waived ones: they are judged once they
	// conclude, never while running (REQ waiver-judged-only-on-terminal-wait).
	LandRefusalWaivedCheckPending = "waived-check-pending"
	// LandRefusalWaivedCheckHung reports that a waived check's run on the
	// target tip exists but has not concluded.
	LandRefusalWaivedCheckHung = "waived-check-hung-on-target"
	// LandRefusalWaivedCheckBaselineInvalid reports a --waive-baseline that is
	// not a first-parent ancestor of the target tip within the bound, has no
	// matching terminal failure, or is older than a green run of the check.
	LandRefusalWaivedCheckBaselineInvalid = "waived-check-baseline-invalid"
	// LandRefusalWaivedAggregateUnproven reports a --waive-aggregate whose
	// inputs, read from its workflow job's needs:, do not prove it waivable.
	LandRefusalWaivedAggregateUnproven = "waived-aggregate-unproven"

	// maxWaiverBaselineDepth bounds the first-parent walk from the target
	// tip, for both an operator-supplied and a suggested baseline.
	maxWaiverBaselineDepth = 20

	waiverAuthorityServerPolicy  = "server-required-policy"
	waiverAuthorityAllowUnfenced = "allow-unfenced"
	waivedByAggregation          = "aggregation"

	// LandRefusalWaivedCheckDuplicate reports a check named more than once
	// across --waive-check and --waive-aggregate.
	LandRefusalWaivedCheckDuplicate = "waived-check-duplicate"
)

// DuplicateWaiver returns the first check named more than once across the
// waived checks and aggregates, or "".
func DuplicateWaiver(checks, aggregates []string) string {
	seen := map[string]bool{}
	for _, name := range append(append([]string(nil), checks...), aggregates...) {
		normalized := githubchecks.NormalizeCheckName(name)
		if seen[normalized] {
			return normalized
		}
		seen[normalized] = true
	}
	return ""
}

// hasWaivers reports whether the landing carries any check waiver.
func (options PullRequestLandOptions) hasWaivers() bool {
	return len(options.WaiveChecks) > 0 || len(options.WaiveAggregates) > 0
}

// waitWaivedChecks is the name list the check waiter must not fail on: the
// waived checks and the waived aggregators alike.
func (options PullRequestLandOptions) waitWaivedChecks() []string {
	if len(options.WaiveAggregates) == 0 {
		return options.WaiveChecks
	}
	return append(append([]string(nil), options.WaiveChecks...), options.WaiveAggregates...)
}

func (options PullRequestLandOptions) resolveCommitParents() func(context.Context, string, string) ([]string, error) {
	if options.commitParents != nil {
		return options.commitParents
	}
	return pullRequestCommitParents
}

func (options PullRequestLandOptions) resolveAggregateNeeds() func(context.Context, string, string, githubchecks.RemoteCheck) (string, []string, error) {
	if options.aggregateNeeds != nil {
		return options.aggregateNeeds
	}
	return githubAggregateNeeds
}

func (options PullRequestLandOptions) resolveReadPullRequest() func(context.Context, string, string) (githubchecks.PullRequestView, error) {
	if options.readPullRequest != nil {
		return options.readPullRequest
	}
	return githubchecks.ReadPullRequest
}

// applyWaivers is REQ waiver-judged-only-on-terminal-wait at the landing's
// call site: a waiver is judged only on a passed wait, never on one still
// running or failed for a non-waived check, and not at all once GitHub
// merged. stop reports that result is final.
func applyWaivers(ctx context.Context, options PullRequestLandOptions, number string, view githubchecks.PullRequestView, waited githubchecks.PullRequestWaitResult, mergedByGitHub bool, result PullRequestLandResult) (PullRequestLandResult, bool, error) {
	switch waiverStageFor(options, waited, mergedByGitHub) {
	case waiverStageMoot:
		result.Evidence["waivers"] = waiversMoot
	case waiverStagePending:
		return pendingWaiverResult(result, options, number, waited), true, nil
	case waiverStageJudge:
		waivedChecks, refusal, err := verifyWaivedChecks(ctx, options, number, view.Base.Ref, view.Head.SHA, waited.Checks)
		if err != nil {
			return result, true, err
		}
		if refusal != nil {
			// Armed auto-merge may have merged the green head while the
			// waivers were judged; a refusal then would refuse a landing.
			if result.AutoMergeArmed {
				if merged, readErr := options.resolveReadPullRequest()(ctx, options.Repository, number); readErr == nil && merged.Merged {
					result.Evidence["waivers"] = waiversMoot
					return result, false, nil
				}
			}
			return mergeRefusal(result, *refusal), true, nil
		}
		result.WaivedChecks = waivedChecks
	}
	return result, false, nil
}

const waiversMoot = "moot (merged by GitHub)"

// waiverStage is what the call site does with the waivers after the wait.
type waiverStage int

const (
	// waiverStageSkip: the wait is not terminal for a non-waived reason, or a
	// non-waived check failed; the ordinary pending/failed result stands and
	// no waiver verdict is recorded.
	waiverStageSkip waiverStage = iota
	// waiverStageJudge: the wait passed; judge every waiver.
	waiverStageJudge
	// waiverStageMoot: GitHub already merged the pull request.
	waiverStageMoot
	// waiverStagePending: only waived checks are still running.
	waiverStagePending
)

func waiverStageFor(options PullRequestLandOptions, waited githubchecks.PullRequestWaitResult, mergedByGitHub bool) waiverStage {
	switch {
	case mergedByGitHub:
		return waiverStageMoot
	case waited.Status == githubchecks.PullRequestWaitPassed:
		return waiverStageJudge
	case waited.Status == githubchecks.PullRequestWaitPending && len(runningWaivedChecks(waited, options.waitWaivedChecks())) > 0:
		return waiverStagePending
	default:
		return waiverStageSkip
	}
}

// runningWaivedChecks returns the waived checks still running when every
// running check is waived and every required check has registered; otherwise
// nil, because the wait is then pending for a reason a waiver cannot answer.
func runningWaivedChecks(waited githubchecks.PullRequestWaitResult, waived []string) []string {
	var running []string
	for _, check := range waited.Checks {
		switch check.Bucket {
		case "pass", "skipping", "fail", "cancel":
			continue
		}
		if !checkNameIn(check.Name, waived) {
			return nil
		}
		running = append(running, githubchecks.NormalizeCheckName(check.Name))
	}
	for _, required := range waited.RequiredChecks {
		if findCheck(waited.Checks, required.Name) == nil {
			return nil
		}
	}
	return running
}

func pendingWaiverResult(result PullRequestLandResult, options PullRequestLandOptions, number string, waited githubchecks.PullRequestWaitResult) PullRequestLandResult {
	running := runningWaivedChecks(waited, options.waitWaivedChecks())
	result.Outcome = LandFindings
	result.RefusalCode = LandRefusalWaivedCheckPending
	result.Reason = "waived checks are judged only once they conclude; still running: " + strings.Join(running, ", ")
	result.SanctionedCommand = pullRequestLandResumeCommand(options, number, prLandResumeTimeoutFlag(options.Slice))
	return withSavings(result)
}

func checkNameIn(name string, names []string) bool {
	normalized := githubchecks.NormalizeCheckName(name)
	for _, candidate := range names {
		if githubchecks.NormalizeCheckName(candidate) == normalized {
			return true
		}
	}
	return false
}

func findCheck(checks []githubchecks.RemoteCheck, name string) *githubchecks.RemoteCheck {
	normalized := githubchecks.NormalizeCheckName(name)
	for i := range checks {
		if githubchecks.NormalizeCheckName(checks[i].Name) == normalized {
			return &checks[i]
		}
	}
	return nil
}

func checkTerminal(check *githubchecks.RemoteCheck) bool {
	switch check.Bucket {
	case "pass", "skipping", "fail", "cancel":
		return true
	}
	return false
}

func checkGreen(check *githubchecks.RemoteCheck) bool {
	return check.Bucket == "pass" || check.Bucket == "skipping"
}

func checkConclusion(check *githubchecks.RemoteCheck) string {
	if check.Conclusion != "" {
		return check.Conclusion
	}
	return check.Bucket
}

// waiverHistory judges waived checks against the target tip and, only when
// the tip has no terminal run, against its first-parent history. Commit
// parent and check reads are cached for the invocation.
type waiverHistory struct {
	ctx       context.Context
	options   PullRequestLandOptions
	number    string
	target    string
	tipSHA    string
	tipChecks []githubchecks.RemoteCheck
	path      []string
	walked    bool
	checks    map[string][]githubchecks.RemoteCheck
}

// firstParentPath returns up to maxWaiverBaselineDepth first-parent
// ancestors of the tip, nearest first.
func (history *waiverHistory) firstParentPath() ([]string, error) {
	if history.walked {
		return history.path, nil
	}
	parents := history.options.resolveCommitParents()
	current := history.tipSHA
	for len(history.path) < maxWaiverBaselineDepth {
		list, err := parents(history.ctx, history.options.Repository, current)
		if err != nil {
			return nil, err
		}
		if len(list) == 0 {
			break
		}
		current = list[0]
		history.path = append(history.path, current)
	}
	history.walked = true
	return history.path, nil
}

func (history *waiverHistory) checksAt(sha string) ([]githubchecks.RemoteCheck, error) {
	if cached, ok := history.checks[sha]; ok {
		return cached, nil
	}
	checks, _, reason := history.options.resolveInspector().CommitChecks(history.ctx, githubchecks.PullRequestWaitOptions{
		Repository: history.options.Repository,
		Head:       sha,
	})
	if reason != "" {
		return nil, fmt.Errorf("read checks for %s (%s): %s", history.target, shortMergeRevision(sha), reason)
	}
	history.checks[sha] = checks
	return checks, nil
}

func (history *waiverHistory) refusal(code, reason string) *landRefusal {
	return &landRefusal{code: code, reason: reason, command: "wb pr land " + history.options.Repository + "#" + history.number}
}

// judge returns the commit the waived check was judged against (the tip, or
// a first-parent baseline), or a refusal.
func (history *waiverHistory) judge(name, headConclusion string) (string, *landRefusal, error) {
	tip := shortMergeRevision(history.tipSHA)
	if tipCheck := findCheck(history.tipChecks, name); tipCheck != nil && checkTerminal(tipCheck) {
		if checkGreen(tipCheck) {
			return "", history.refusal(LandRefusalWaivedCheckGreen,
				fmt.Sprintf("waived check %q is green on target branch %s tip (target SHA %s, conclusion %q)", name, history.target, tip, tipCheck.Conclusion)), nil
		}
		if conclusion := checkConclusion(tipCheck); conclusion != headConclusion {
			return "", history.refusal(LandRefusalWaivedCheckMismatch,
				fmt.Sprintf("waived check %q conclusion %q on pull request head does not match conclusion %q on target branch %s tip (target SHA %s)", name, headConclusion, conclusion, history.target, tip)), nil
		}
		return history.tipSHA, nil, nil
	}
	if strings.TrimSpace(history.options.WaiveBaseline) != "" {
		return history.verifyBaseline(name, headConclusion)
	}
	code, state := LandRefusalWaivedCheckAbsent, "was not observed"
	if findCheck(history.tipChecks, name) != nil {
		code, state = LandRefusalWaivedCheckHung, "has not concluded"
	}
	found, err := history.searchBaseline(name, headConclusion)
	if err != nil {
		return "", nil, err
	}
	reason := fmt.Sprintf("waived check %q %s on target branch %s tip (target SHA %s)", name, state, history.target, tip)
	if found == "" {
		refusal := history.refusal(code, reason+fmt.Sprintf("; no baseline within %d first-parent commits", maxWaiverBaselineDepth))
		return "", refusal, nil
	}
	opts := history.options
	opts.WaiveBaseline = found
	refusal := &landRefusal{
		code:    code,
		reason:  reason + fmt.Sprintf("; first-parent ancestor %s has the same %q conclusion", shortMergeRevision(found), headConclusion),
		command: pullRequestLandResumeCommand(opts, history.number, ""),
	}
	return "", refusal, nil
}

// searchBaseline returns the nearest first-parent ancestor whose run of the
// check concluded like the pull request head, stopping at a green run.
func (history *waiverHistory) searchBaseline(name, headConclusion string) (string, error) {
	path, err := history.firstParentPath()
	if err != nil {
		return "", err
	}
	for _, sha := range path {
		checks, err := history.checksAt(sha)
		if err != nil {
			return "", err
		}
		check := findCheck(checks, name)
		if check == nil || !checkTerminal(check) {
			continue
		}
		if checkGreen(check) || checkConclusion(check) != headConclusion {
			return "", nil
		}
		return sha, nil
	}
	return "", nil
}

func (history *waiverHistory) verifyBaseline(name, headConclusion string) (string, *landRefusal, error) {
	baseline := strings.ToLower(strings.TrimSpace(history.options.WaiveBaseline))
	path, err := history.firstParentPath()
	if err != nil {
		return "", nil, err
	}
	for _, sha := range path {
		checks, err := history.checksAt(sha)
		if err != nil {
			return "", nil, err
		}
		check := findCheck(checks, name)
		if len(baseline) >= 7 && strings.HasPrefix(sha, baseline) {
			if check == nil || !checkTerminal(check) || checkGreen(check) || checkConclusion(check) != headConclusion {
				return "", history.refusal(LandRefusalWaivedCheckBaselineInvalid,
					fmt.Sprintf("waived check %q has no terminal %q run on baseline %s", name, headConclusion, shortMergeRevision(sha))), nil
			}
			return sha, nil, nil
		}
		if check != nil && checkTerminal(check) && checkGreen(check) {
			return "", history.refusal(LandRefusalWaivedCheckBaselineInvalid,
				fmt.Sprintf("waived check %q is green on %s, between baseline %s and target branch %s tip", name, shortMergeRevision(sha), baseline, history.target)), nil
		}
	}
	return "", history.refusal(LandRefusalWaivedCheckBaselineInvalid,
		fmt.Sprintf("baseline %s is not a first-parent ancestor of target branch %s tip %s within %d commits", baseline, history.target, shortMergeRevision(history.tipSHA), maxWaiverBaselineDepth)), nil
}

// judgeAggregates applies REQ waiver-aggregator-from-needs to every
// --waive-aggregate, after the ordinary waivers in waived were accepted.
func judgeAggregates(ctx context.Context, options PullRequestLandOptions, history *waiverHistory, headSHA string, headChecks []githubchecks.RemoteCheck, required []githubchecks.RequiredRemoteCheck, waived []WaivedCheck, authority string) ([]WaivedCheck, *landRefusal) {
	waivedNames := make([]string, 0, len(waived))
	for _, check := range waived {
		waivedNames = append(waivedNames, check.Name)
	}
	unproven := func(aggregate, reason string) *landRefusal {
		return history.refusal(LandRefusalWaivedAggregateUnproven, fmt.Sprintf("aggregate %q: %s", aggregate, reason))
	}
	for _, aggregate := range options.WaiveAggregates {
		name := githubchecks.NormalizeCheckName(aggregate)
		headCheck := findCheck(headChecks, name)
		if headCheck == nil || (headCheck.Bucket != "fail" && headCheck.Bucket != "cancel") {
			status := "absent"
			if headCheck != nil {
				status = checkConclusion(headCheck)
			}
			return nil, history.refusal(LandRefusalWaivedCheckNotFailed,
				fmt.Sprintf("waived aggregate %q did not fail or cancel on pull request head (status: %s)", name, status))
		}
		if isCheckRequiredByPolicy(name, headCheck.AppID, required) {
			return nil, history.refusal(LandRefusalWaivedCheckRequired,
				fmt.Sprintf("waived aggregate %q is required by target branch %s policy", name, history.target))
		}
		workflow, inputs, err := options.resolveAggregateNeeds()(ctx, options.Repository, headSHA, *headCheck)
		if err != nil {
			return nil, unproven(name, err.Error())
		}
		waivedInput := false
		for _, input := range inputs {
			matches := inputChecks(headChecks, input, headCheck.WorkflowRunID)
			if len(matches) == 0 {
				return nil, unproven(name, fmt.Sprintf("input %q has no check run on the pull request head", input))
			}
			for _, match := range matches {
				if isCheckRequiredByPolicy(githubchecks.NormalizeCheckName(match.Name), match.AppID, required) {
					return nil, history.refusal(LandRefusalWaivedCheckRequired,
						fmt.Sprintf("input %q of waived aggregate %q is required by target branch %s policy", match.Name, name, history.target))
				}
				switch {
				case checkGreen(&match):
				case (match.Bucket == "fail" || match.Bucket == "cancel") && checkNameIn(match.Name, waivedNames):
					waivedInput = true
				case match.Bucket == "fail" || match.Bucket == "cancel":
					return nil, unproven(name, fmt.Sprintf("input %q failed and is not waived", match.Name))
				default:
					return nil, unproven(name, fmt.Sprintf("input %q has not concluded", match.Name))
				}
			}
		}
		if !waivedInput {
			return nil, unproven(name, "none of its inputs is waived")
		}
		waived = append(waived, WaivedCheck{
			Name:          name,
			Conclusion:    checkConclusion(headCheck),
			TargetSHA:     history.tipSHA,
			Reason:        options.WaiveReason,
			WaivedBy:      waivedByAggregation,
			Inputs:        inputs,
			Workflow:      workflow,
			Authority:     authority,
			BaselineSHA:   history.tipSHA,
			BaselineIsTip: true,
		})
	}
	return waived, nil
}

// inputChecks returns the head checks for one needs: input: the job's own
// check, or every matrix leg named "<input> (...)", from the aggregator's own
// workflow run when it has one, so a same-named job elsewhere never counts.
func inputChecks(checks []githubchecks.RemoteCheck, input string, workflowRunID int64) []githubchecks.RemoteCheck {
	var matches []githubchecks.RemoteCheck
	for _, check := range checks {
		if workflowRunID != 0 && check.WorkflowRunID != workflowRunID {
			continue
		}
		name := githubchecks.NormalizeCheckName(check.Name)
		if name == input || strings.HasPrefix(name, input+" (") {
			matches = append(matches, check)
		}
	}
	return matches
}

// githubReader is the GitHub GET shape githubAggregateNeedsWith reads through.
type githubReader func(ctx context.Context, worktree, repository, target, head, endpoint string) ([]byte, error)

// githubAggregateNeeds reads the aggregator's workflow file at its run's head
// and returns the check names of its job's needs:.
func githubAggregateNeeds(ctx context.Context, repository, headSHA string, check githubchecks.RemoteCheck) (string, []string, error) {
	return githubAggregateNeedsWith(ctx, githubGet, repository, headSHA, check)
}

func githubAggregateNeedsWith(ctx context.Context, get githubReader, repository, headSHA string, check githubchecks.RemoteCheck) (string, []string, error) {
	if check.WorkflowRunID == 0 {
		return "", nil, errors.New("it is not a GitHub Actions job, so its inputs cannot be read")
	}
	runBody, err := get(ctx, "", repository, "", headSHA, "repos/"+repository+"/actions/runs/"+strconv.FormatInt(check.WorkflowRunID, 10))
	if err != nil {
		return "", nil, fmt.Errorf("read workflow run: %w", err)
	}
	var run struct {
		Path    string `json:"path"`
		HeadSHA string `json:"head_sha"`
	}
	if err := json.Unmarshal(runBody, &run); err != nil || run.Path == "" || run.HeadSHA == "" {
		return "", nil, errors.New("workflow run names no workflow file and head")
	}
	// The run's own head, as GitHub reports it, is the workflow revision that
	// actually ran; a pull_request run may differ from the PR head.
	contentBody, err := get(ctx, "", repository, "", headSHA, "repos/"+repository+"/contents/"+run.Path+"?ref="+url.QueryEscape(run.HeadSHA))
	if err != nil {
		return "", nil, fmt.Errorf("read %s at %s: %w", run.Path, shortMergeRevision(run.HeadSHA), err)
	}
	var content struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(contentBody, &content); err != nil {
		return "", nil, fmt.Errorf("decode %s: %w", run.Path, err)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(content.Content, "\n", ""))
	if err != nil {
		return "", nil, fmt.Errorf("decode %s: %w", run.Path, err)
	}
	inputs, err := parseWorkflowNeeds(raw, githubchecks.NormalizeCheckName(check.Name))
	if err != nil {
		return "", nil, fmt.Errorf("%s: %w", run.Path, err)
	}
	return run.Path, inputs, nil
}

type workflowJob struct {
	Name  string    `yaml:"name"`
	Uses  string    `yaml:"uses"`
	Needs yaml.Node `yaml:"needs"`
}

// parseWorkflowNeeds finds the one job whose check name is checkName and
// returns the check names of the jobs it needs.
func parseWorkflowNeeds(raw []byte, checkName string) ([]string, error) {
	var workflow struct {
		Jobs map[string]workflowJob `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		return nil, fmt.Errorf("parse workflow: %w", err)
	}
	jobCheckName := func(key string, job workflowJob) string {
		if job.Name == "" || strings.Contains(job.Name, "${{") {
			return key
		}
		return job.Name
	}
	var found []workflowJob
	for key, job := range workflow.Jobs {
		if name := jobCheckName(key, job); name == checkName || strings.HasPrefix(checkName, name+" (") {
			found = append(found, job)
		}
	}
	if len(found) != 1 {
		return nil, fmt.Errorf("%d jobs match check %q, want exactly 1", len(found), checkName)
	}
	if found[0].Uses != "" {
		return nil, fmt.Errorf("check %q is a reusable-workflow call, whose inputs cannot be proved", checkName)
	}
	var needs []string
	switch found[0].Needs.Kind {
	case yaml.ScalarNode:
		needs = []string{found[0].Needs.Value}
	case yaml.SequenceNode:
		if err := found[0].Needs.Decode(&needs); err != nil {
			return nil, fmt.Errorf("decode needs of %q: %w", checkName, err)
		}
	}
	if len(needs) == 0 {
		return nil, fmt.Errorf("job %q declares no needs", checkName)
	}
	inputs := make([]string, 0, len(needs))
	for _, need := range needs {
		job, ok := workflow.Jobs[need]
		if !ok {
			return nil, fmt.Errorf("job %q needs unknown job %q", checkName, need)
		}
		if job.Uses != "" {
			return nil, fmt.Errorf("input %q of %q is a reusable-workflow call, whose checks cannot be proved", need, checkName)
		}
		inputs = append(inputs, jobCheckName(need, job))
	}
	return inputs, nil
}
