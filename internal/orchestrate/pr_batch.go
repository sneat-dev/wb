package orchestrate

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Integration-branch batches (spec/features/integration-branch-batches): one
// branch off an integration target, one --no-ff merge commit per pull
// request pinned at its independently reviewed head, and one pull request
// whose body carries the batch state.

const (
	// BatchRefusalPinInvalid: a pin is not <number>@<sha>=<review-comment-url>.
	BatchRefusalPinInvalid = "batch-pin-invalid"
	// BatchRefusalSourceWrongBase: the source PR does not target the batch target.
	BatchRefusalSourceWrongBase = "batch-source-wrong-base"
	// BatchRefusalSourceNotOpen: the source PR is closed, merged or a draft.
	BatchRefusalSourceNotOpen = "batch-source-not-open"
	// BatchRefusalSourceHeadMoved: the source PR head is not the pinned head.
	BatchRefusalSourceHeadMoved = "batch-source-head-moved"
	// BatchRefusalSourceAlreadyLanded: the pinned head is already in the target.
	BatchRefusalSourceAlreadyLanded = "batch-source-already-landed"
	// BatchRefusalSourceAlreadyIncluded: the pinned head is already in the
	// batch or under another pin, so it would get no merge commit of its own.
	BatchRefusalSourceAlreadyIncluded = "batch-source-already-included"
	// BatchRefusalSourceUnreviewed: the named review does not bind the pin.
	BatchRefusalSourceUnreviewed = "batch-source-unreviewed"
	// BatchRefusalSourceSelfReview: the review identity is incomplete or is
	// the lane author's or the operator's own session.
	BatchRefusalSourceSelfReview = "batch-source-self-review"
	// BatchRefusalMergeCommitsDisabled: the repository disallows merge commits.
	BatchRefusalMergeCommitsDisabled = "batch-merge-commits-disabled"
	// BatchRefusalTargetMergeQueue: the target merges through a merge queue.
	BatchRefusalTargetMergeQueue = "batch-target-merge-queue"
	// BatchRefusalWorktree: the batch worktree is missing, foreign, dirty, or
	// not at the expected commit.
	BatchRefusalWorktree = "batch-worktree-invalid"
	// BatchRefusalConflict: a merge stopped on conflicts; resolve, commit, resume.
	BatchRefusalConflict = "batch-conflict"
	// BatchRefusalResolutionInvalid: the committed resolution is not a merge
	// of exactly the previous batch head and the pinned head.
	BatchRefusalResolutionInvalid = "batch-resolution-invalid"
	// BatchRefusalMoved: origin's batch branch moved; nothing is force-pushed.
	BatchRefusalMoved = "batch-moved"
	// BatchRefusalClosed: the batch pull request is merged or closed.
	BatchRefusalClosed = "batch-closed"
	// BatchRefusalBaseInvalid: the recorded base is not on the target history.
	BatchRefusalBaseInvalid = "batch-base-invalid"
	// BatchRefusalUnrecordedMerge: a merge commit WB did not record, or whose
	// review no longer resolves, is on the batch branch.
	BatchRefusalUnrecordedMerge = "batch-unrecorded-merge"
	// BatchRefusalNotFound: no batch state exists for the named batch.
	BatchRefusalNotFound = "batch-not-found"
)

// batchMergeOptions are the fixed merge settings every batch merge and every
// delta merge-tree run with, so a machine's own Git config never changes
// either result.
var batchMergeOptions = []string{"merge.renames=true", "merge.conflictStyle=merge", "rerere.enabled=false"}

// BatchPin is one source as the operator names it.
type BatchPin struct {
	Number          int
	SHA             string
	ReviewURL       string
	ReviewCommentID int64
}

var batchPinPattern = regexp.MustCompile(`^#?(\d+)@([0-9a-fA-F]{7,40})=(\S+)$`)

// ParseBatchPin parses `<number>@<sha>=<review-comment-url>`; a leading `#`
// is accepted when the operator quoted it.
func ParseBatchPin(repository, raw string) (BatchPin, error) {
	match := batchPinPattern.FindStringSubmatch(strings.TrimSpace(raw))
	if match == nil {
		return BatchPin{}, fmt.Errorf("pin %q is not <number>@<sha>=<review-comment-url>", raw)
	}
	number, _ := strconv.Atoi(match[1])
	comment := issueCommentURLPattern.FindStringSubmatch(match[3])
	if comment == nil {
		return BatchPin{}, fmt.Errorf("pin %q: %q is not a GitHub issue-comment URL", raw, match[3])
	}
	if !strings.EqualFold(comment[1], repository) || comment[2] != match[1] {
		return BatchPin{}, fmt.Errorf("pin %q: review comment belongs to %s#%s, not %s#%d", raw, comment[1], comment[2], repository, number)
	}
	id, _ := strconv.ParseInt(comment[3], 10, 64)
	return BatchPin{Number: number, SHA: strings.ToLower(match[2]), ReviewURL: match[3], ReviewCommentID: id}, nil
}

// Batch step kinds and states.
const (
	BatchStepSource  = "source"
	BatchStepRefresh = "refresh"

	BatchStateMerged     = "merged"
	BatchStateConflicted = "conflicted"
	BatchStateResolved   = "resolved"
)

// BatchStep is one merge commit on the batch branch, in first-parent order:
// a pinned source, or a refresh merge of the target tip.
type BatchStep struct {
	Kind            string   `yaml:"kind" json:"kind"`
	PullRequest     int      `yaml:"pr,omitempty" json:"pr,omitempty"`
	Head            string   `yaml:"head" json:"head"`
	ReviewCommentID int64    `yaml:"review_comment_id,omitempty" json:"review_comment_id,omitempty"`
	ReviewDigest    string   `yaml:"review_digest,omitempty" json:"review_digest,omitempty"`
	Reviewer        string   `yaml:"reviewer,omitempty" json:"reviewer,omitempty"`
	Identity        string   `yaml:"identity,omitempty" json:"identity,omitempty"`
	MergeCommit     string   `yaml:"merge_commit,omitempty" json:"merge_commit,omitempty"`
	State           string   `yaml:"state" json:"state"`
	ConflictPaths   []string `yaml:"conflict_paths,omitempty" json:"conflict_paths,omitempty"`
}

// BatchState is the batch's whole record. Its authoritative copy is the
// fenced block in the batch pull request body; WB's private copy is a cache.
type BatchState struct {
	Version      int         `yaml:"wb_batch" json:"wb_batch"`
	Revision     int         `yaml:"-" json:"revision"`
	Repository   string      `yaml:"repository" json:"repository"`
	Target       string      `yaml:"target" json:"target"`
	Branch       string      `yaml:"branch" json:"branch"`
	Base         string      `yaml:"base" json:"base"`
	MergeOptions []string    `yaml:"merge_options" json:"merge_options"`
	BuiltBy      []string    `yaml:"built_by" json:"built_by"`
	Steps        []BatchStep `yaml:"steps" json:"steps"`
	PullRequest  int         `yaml:"-" json:"pull_request,omitempty"`
}

// sources returns the pinned source steps.
func (state BatchState) sources() []BatchStep {
	var sources []BatchStep
	for _, step := range state.Steps {
		if step.Kind == BatchStepSource {
			sources = append(sources, step)
		}
	}
	return sources
}

// conflicted returns the index of the step stopped on a conflict, or -1.
func (state BatchState) conflicted() int {
	for i, step := range state.Steps {
		if step.State == BatchStateConflicted {
			return i
		}
	}
	return -1
}

// addBuilder records the session of a WB invocation that mutates the batch.
func (state *BatchState) addBuilder(session string) {
	for _, existing := range state.BuiltBy {
		if existing == session {
			return
		}
	}
	state.BuiltBy = append(state.BuiltBy, session)
}

const (
	batchBlockBegin = "<!-- wb-batch:begin -->"
	batchBlockEnd   = "<!-- wb-batch:end -->"
)

// RenderBatchBody renders the batch PR body: a human table, then the state.
func RenderBatchBody(state BatchState) string {
	var body strings.Builder
	fmt.Fprintf(&body, "WB batch `%s` into `%s` from base `%s`. Each source below merged with a merge commit at its pinned, independently reviewed head.\n\n", state.Branch, state.Target, shortMergeRevision(state.Base))
	body.WriteString("| Step | Source | Pinned head | Reviewer | Merge commit | State |\n|---|---|---|---|---|---|\n")
	for i, step := range state.Steps {
		source := "refresh `" + state.Target + "`"
		if step.Kind == BatchStepSource {
			source = "#" + strconv.Itoa(step.PullRequest)
		}
		fmt.Fprintf(&body, "| %d | %s | `%s` | %s | `%s` | %s |\n", i+1, source, shortMergeRevision(step.Head), dashIfEmpty(step.Reviewer), shortMergeRevision(dashIfEmpty(step.MergeCommit)), step.State)
	}
	body.WriteString("\nLanding needs an independent review of the integration delta (`wb pr batch delta " + state.Branch + "`) ending `Reviewed-Head: <batch head>`.\n\n")
	block, _ := yaml.Marshal(state)
	body.WriteString(batchBlockBegin + "\n```yaml\n" + string(block) + "```\n" + batchBlockEnd + "\n")
	return body.String()
}

func dashIfEmpty(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

// ParseBatchBody extracts the batch state from a PR body; ok is false when
// the body carries no batch block.
func ParseBatchBody(body string) (BatchState, bool, error) {
	start := strings.Index(body, batchBlockBegin)
	end := strings.Index(body, batchBlockEnd)
	if start < 0 || end < start {
		return BatchState{}, false, nil
	}
	block := strings.TrimSpace(body[start+len(batchBlockBegin) : end])
	block = strings.TrimSuffix(strings.TrimPrefix(block, "```yaml"), "```")
	var state BatchState
	if err := yaml.Unmarshal([]byte(block), &state); err != nil {
		return BatchState{}, true, fmt.Errorf("parse batch block: %w", err)
	}
	if state.Version != 1 {
		return BatchState{}, true, fmt.Errorf("batch block version %d is not supported", state.Version)
	}
	return state, true, nil
}

// BatchResult is the envelope every batch verb returns.
type BatchResult struct {
	SchemaVersion int           `json:"v"`
	Verb          string        `json:"verb"`
	Outcome       LandOutcome   `json:"outcome"`
	RefusalCode   string        `json:"refusal_code,omitempty"`
	Reason        string        `json:"reason,omitempty"`
	Refusals      []BatchIssue  `json:"refusals,omitempty"`
	Command       string        `json:"sanctioned_command,omitempty"`
	Worktree      string        `json:"worktree,omitempty"`
	State         *BatchState   `json:"state,omitempty"`
	Head          string        `json:"head,omitempty"`
	PullRequest   string        `json:"pull_request,omitempty"`
	Delta         []BatchDelta  `json:"delta,omitempty"`
	Attribution   []BatchFailed `json:"attribution,omitempty"`
	Changed       bool          `json:"changed"`
}

// BatchIssue is one refusal among several reported together.
type BatchIssue struct {
	Code   string `json:"code"`
	Source string `json:"source,omitempty"`
	Reason string `json:"reason"`
}

// ExitCode maps the batch outcome onto WB's exit contract.
func (result BatchResult) ExitCode() int {
	switch result.Outcome {
	case LandSuccess:
		return 0
	case LandRefused:
		return 2
	default:
		return 1
	}
}

func (result BatchResult) refuse(code, reason, command string) BatchResult {
	result.Outcome, result.RefusalCode, result.Reason, result.Command = LandRefused, code, reason, command
	return result
}

func (result BatchResult) finding(code, reason, command string) BatchResult {
	result.Outcome, result.RefusalCode, result.Reason, result.Command = LandFindings, code, reason, command
	return result
}

// MarshalBatchResult renders the JSON envelope.
func MarshalBatchResult(result BatchResult) ([]byte, error) {
	return json.MarshalIndent(result, "", "  ")
}
