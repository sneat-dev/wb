package orchestrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/sneat-dev/wb/internal/quality"
)

const legacyRecordPrefix = "WB legacy merge provenance (not review approval)\n"
const legacyInventoryPrefix = "<!-- wb-legacy-provenance:"

var legacyRepositoryPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`)
var legacySHA = regexp.MustCompile(`^[0-9a-f]{40}$`)
var legacyAuditAtom = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/@:+-]*$`)
var legacyRawDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)
var legacyDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// LegacyMerge records original parent identity, never a fabricated lane review.
type LegacyMerge struct {
	SHA          string `json:"sha"`
	FirstParent  string `json:"first_parent"`
	SecondParent string `json:"second_parent"`
}

// LegacyProvenance is a safe portable projection. ReceiptDigest is audit
// identity of unsigned local bytes, NOT proof of authorship or review.
type LegacyProvenance struct {
	Version          int                             `json:"v"`
	Repository       string                          `json:"repository"`
	Integration      string                          `json:"integration"`
	To               string                          `json:"to"`
	ReceiptID        string                          `json:"receipt_id"`
	ReceiptDigest    string                          `json:"receipt_digest"`
	TargetSHA        string                          `json:"target_sha"`
	CandidateSHA     string                          `json:"candidate_sha"`
	Sources          []string                        `json:"sources"`
	Merges           []LegacyMerge                   `json:"merges"`
	Validation       WorktreeMergeValidationIdentity `json:"validation"`
	PassedChecks     []string                        `json:"passed_checks"`
	ValidationDigest string                          `json:"validation_digest"`
	WB               string                          `json:"wb"`
	Adopter          ReviewerIdentity                `json:"adopter"`
	OriginalMerger   string                          `json:"original_merger,omitempty"`
}

// LegacyBinding pins a portable record's exact comment text.
type LegacyBinding struct {
	URL    string `json:"url"`
	Digest string `json:"digest"`
}

type legacyArchive interface {
	commitCommentReader
	LegacyComments(context.Context, string, string) ([]LegacyBinding, error)
	CreateLegacyComment(context.Context, string, string, string) (LegacyBinding, error)
}

// LegacyAdoptOptions describes an explicit dry-run/apply provenance adoption.
type LegacyAdoptOptions struct {
	PromoteOptions
	Receipt string
	Apply   bool
	archive legacyArchive
	actor   ReviewerIdentity
}

type LegacyAdoptResult struct {
	Record  LegacyProvenance `json:"record"`
	Binding *LegacyBinding   `json:"binding,omitempty"`
	Applied bool             `json:"applied"`
}

func legacyHash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}
func legacyJSON(value any) string { raw, _ := json.Marshal(value); return string(raw) }
func legacyInventoryDigest(records []LegacyBinding) string {
	return legacyHash([]byte(legacyJSON(records)))
}

func validateLegacyRecord(record LegacyProvenance) error {
	if record.Version != 1 || !legacyRepositoryPattern.MatchString(record.Repository) || !validBranchName(record.Integration) || !validBranchName(record.To) || record.Integration == record.To || record.ReceiptID == "" || !legacyDigest.MatchString(record.ReceiptDigest) || !legacyDigest.MatchString(record.ValidationDigest) || record.WB == "" || !identityComplete(record.Adopter) || !legacySHA.MatchString(record.TargetSHA) || !legacySHA.MatchString(record.CandidateSHA) || len(record.Sources) == 0 || len(record.Sources) != len(record.Merges) {
		return fmt.Errorf("invalid legacy provenance identity")
	}
	if record.Validation.CandidateSHA != record.CandidateSHA || record.Validation.TargetSHA != record.TargetSHA || legacyJSON(record.Validation.SourceSHAs) != legacyJSON(record.Sources) || record.Validation.WBBuild != record.WB || !legacyAuditAtom.MatchString(record.WB) || !legacyAuditAtom.MatchString(record.ReceiptID) || !legacyRawDigest.MatchString(record.Validation.QualityPolicySHA) || !legacyRawDigest.MatchString(record.Validation.WBExecutableSHA) || record.ValidationDigest != legacyHash([]byte(legacyJSON(record.Validation))) || len(record.PassedChecks) == 0 {
		return fmt.Errorf("invalid legacy validation evidence")
	}
	for name, digest := range record.Validation.Validators {
		if !legacyAuditAtom.MatchString(name) || !legacyRawDigest.MatchString(digest) {
			return fmt.Errorf("invalid legacy validator identity")
		}
	}
	for _, check := range record.PassedChecks {
		if !legacyAuditAtom.MatchString(check) {
			return fmt.Errorf("invalid legacy passed check")
		}
	}
	previous := record.TargetSHA
	seen := map[string]bool{}
	for i, m := range record.Merges {
		if !legacySHA.MatchString(m.SHA) || !legacySHA.MatchString(m.SecondParent) || m.FirstParent != previous || m.SecondParent != record.Sources[i] || seen[m.SecondParent] || seen[m.SHA] {
			return fmt.Errorf("invalid legacy merge chain")
		}
		seen[m.SecondParent] = true
		seen[m.SHA] = true
		previous = m.SHA
	}
	if previous != record.CandidateSHA {
		return fmt.Errorf("legacy candidate does not end the exact chain")
	}
	return nil
}

func legacyComment(body string) (LegacyProvenance, error) {
	var record LegacyProvenance
	if !strings.HasPrefix(body, legacyRecordPrefix) {
		return record, fmt.Errorf("not a legacy provenance record")
	}
	decoder := json.NewDecoder(strings.NewReader(strings.TrimPrefix(body, legacyRecordPrefix)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return record, err
	}
	if body != legacyRecordPrefix+legacyJSON(record) {
		return record, fmt.Errorf("noncanonical legacy record")
	}
	return record, validateLegacyRecord(record)
}

func legacyBindingID(repository string, binding LegacyBinding) (string, int64, error) {
	// Validate the entire URL before the first provider read. No arbitrary transport.
	match := commitCommentURLPattern.FindStringSubmatch(binding.URL)
	if match == nil || !legacyRepositoryPattern.MatchString(repository) || !strings.EqualFold(match[1], repository) || !legacySHA.MatchString(match[2]) || !legacyDigest.MatchString(binding.Digest) {
		return "", 0, fmt.Errorf("invalid legacy record binding")
	}
	id, err := strconv.ParseInt(match[3], 10, 64)
	if err != nil || id <= 0 {
		return "", 0, fmt.Errorf("invalid legacy comment ID")
	}
	return match[2], id, nil
}

// AdoptLegacyIntegration reads a completed direct WB receipt and verifies the
// entire remotely published first-parent chain. Dry-run performs no writes.
func AdoptLegacyIntegration(ctx context.Context, options LegacyAdoptOptions) (LegacyAdoptResult, error) {
	var result LegacyAdoptResult
	if !legacyRepositoryPattern.MatchString(options.Repository) {
		return result, fmt.Errorf("invalid repository")
	}
	raw, err := os.ReadFile(options.Receipt)
	if err != nil {
		return result, err
	}
	var receipt WorktreeMergeReceipt
	if err = json.Unmarshal(raw, &receipt); err != nil {
		return result, err
	}
	actor := options.actor
	if !identityComplete(actor) {
		actor = currentSessionIdentity()
	}
	if receipt.SchemaVersion != 1 || receipt.Candidate.Task != receipt.ID || !validBranchName(receipt.Candidate.Branch) || !strings.HasPrefix(receipt.Candidate.Branch, "wb/integration/"+receipt.Target+"/") || receipt.Repository != options.Repository || receipt.Target != options.Integration || receipt.Status != WorktreeMergeComplete || receipt.Phase != WorktreeMergePhaseLand || receipt.Route.Route != WorktreeMergeRouteDirect || receipt.PreviousTargetSHA != receipt.TargetSHA || receipt.Candidate.SHA != receipt.LandingSHA || receipt.ValidationIdentity == nil || receipt.ValidationIdentity.CandidateSHA != receipt.Candidate.SHA || receipt.ValidationIdentity.TargetSHA != receipt.TargetSHA || receipt.Validation.Status != quality.StatusPassed || !anyCheckPassed(receipt.Validation) || receipt.Rebase != nil || receipt.RevertOf != nil || receipt.RebatchOf != "" || len(receipt.TargetRefreshes) > 0 || len(receipt.SourceRefreshes) > 0 || len(receipt.ForwardRepairs) > 0 || receipt.ValidationDeferral != nil {
		return result, fmt.Errorf("unsupported or incomplete legacy WB receipt")
	}
	git := options.checkoutGit()
	refused, _, err := options.open(ctx, git, &PromoteResult{})
	if err != nil {
		return result, err
	}
	if refused != nil {
		return result, fmt.Errorf("%s", refused.Reason)
	}
	record := LegacyProvenance{Version: 1, Repository: options.Repository, Integration: options.Integration, To: options.To, ReceiptID: receipt.ID, ReceiptDigest: legacyHash(raw), TargetSHA: receipt.TargetSHA, CandidateSHA: receipt.Candidate.SHA, ValidationDigest: legacyHash([]byte(legacyJSON(receipt.ValidationIdentity))), WB: receipt.ValidationIdentity.WBBuild, Adopter: actor, Validation: *receipt.ValidationIdentity}
	for _, check := range receipt.Validation.Results {
		if check.Status == quality.StatusPassed {
			record.PassedChecks = append(record.PassedChecks, string(check.Check))
		}
	}
	if receipt.LaneOwner != nil {
		record.OriginalMerger = receipt.LaneOwner.Owner.WBSessionID
	}
	for _, source := range receipt.Sources {
		if !source.Merged {
			return result, fmt.Errorf("unmerged source")
		}
		record.Sources = append(record.Sources, source.SHA)
	}
	if legacyJSON(record.Sources) != legacyJSON(receipt.ValidationIdentity.SourceSHAs) {
		return result, fmt.Errorf("source validation identity mismatch")
	}
	// Exact ancestry and parent proof, not patch equivalence or source branch liveness.
	if !legacySHA.MatchString(record.TargetSHA) || !legacySHA.MatchString(record.CandidateSHA) {
		return result, fmt.Errorf("invalid receipt commit")
	}
	published, err := localInTarget(ctx, git, "refs/remotes/origin/"+options.Integration)(record.CandidateSHA)
	if err != nil {
		return result, err
	}
	if !published {
		return result, fmt.Errorf("legacy candidate not remotely published")
	}
	chain, err := git.out(ctx, "rev-list", "--first-parent", "--reverse", record.TargetSHA+".."+record.CandidateSHA)
	if err != nil {
		return result, err
	}
	for _, sha := range strings.Fields(chain) {
		parents, err := git.out(ctx, "show", "-s", "--format=%P%x00%B", sha)
		if err != nil {
			return result, err
		}
		parentText, message, _ := strings.Cut(parents, "\x00")
		if carriesLaneTrailer(SourceCommit{Body: message}) {
			return result, fmt.Errorf("recorded lane merges must retain their original review gate")
		}
		p := strings.Fields(parentText)
		if len(p) != 2 {
			return result, fmt.Errorf("legacy chain contains an unlisted non-merge or octopus")
		}
		record.Merges = append(record.Merges, LegacyMerge{sha, p[0], p[1]})
	}
	if err := validateLegacyRecord(record); err != nil {
		return result, err
	}
	result.Record = record
	if !options.Apply {
		return result, nil
	}
	archive := options.resolveLegacyArchive()
	body := legacyRecordPrefix + legacyJSON(record)
	bindings, err := archive.LegacyComments(ctx, record.Repository, record.CandidateSHA)
	if err != nil {
		return result, err
	}
	for _, binding := range bindings {
		candidate, id, err := legacyBindingID(record.Repository, binding)
		if err != nil || candidate != record.CandidateSHA {
			return result, fmt.Errorf("foreign existing legacy binding")
		}
		comment, err := archive.CommitComment(ctx, record.Repository, id)
		if err != nil {
			return result, err
		}
		if comment.CommitID != candidate || legacyHash([]byte(comment.Body)) != binding.Digest {
			return result, fmt.Errorf("changed existing legacy binding")
		}
		archived, err := legacyComment(comment.Body)
		if err != nil {
			return result, err
		}
		// The original adopter remains recorded across a different session's retry.
		// Exact receipt and graph identity must match; never rewrite old evidence.
		comparable := record
		comparable.Adopter = archived.Adopter
		if legacyJSON(comparable) == legacyJSON(archived) {
			result.Record = archived
			result.Binding = &binding
			result.Applied = true
			return result, nil
		}
	}
	binding, err := archive.CreateLegacyComment(ctx, record.Repository, record.CandidateSHA, body)
	if err != nil {
		return result, err
	}
	// A malformed write response remains unknown; rerun discovers exact content.
	if sha, _, err := legacyBindingID(record.Repository, binding); err != nil || sha != record.CandidateSHA || binding.Digest != legacyHash([]byte(body)) {
		return result, fmt.Errorf("unconfirmed legacy archive response")
	}
	result.Binding = &binding
	result.Applied = true
	return result, nil
}

func parseLegacyInventory(body string) ([]LegacyBinding, error) {
	var records []LegacyBinding
	count := strings.Count(body, legacyInventoryPrefix)
	if count == 0 {
		return records, nil
	}
	if count != 1 {
		return nil, fmt.Errorf("duplicate legacy inventory")
	}
	tail := strings.SplitN(body, legacyInventoryPrefix, 2)[1]
	text, _, ok := strings.Cut(tail, " -->")
	if !ok {
		return nil, fmt.Errorf("incomplete legacy inventory")
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&records); err != nil {
		return nil, err
	}
	if text != legacyJSON(records) || len(records) == 0 {
		return nil, fmt.Errorf("noncanonical legacy inventory")
	}
	return records, nil
}

func verifyLegacyProvenance(ctx context.Context, reader commitCommentReader, repository, integration, to string, walk []integrationCommit, verdict *provenance, bindings []LegacyBinding) error {
	bySHA := map[string]integrationCommit{}
	for _, c := range walk {
		bySHA[c.SHA] = c
	}
	claimed := map[string]bool{}
	unknown := map[string]bool{}
	for _, sha := range verdict.Unrecorded {
		unknown[sha] = true
	}
	canonical := append([]LegacyBinding(nil), bindings...)
	sort.Slice(canonical, func(i, j int) bool { return canonical[i].URL < canonical[j].URL })
	for _, binding := range canonical {
		candidate, id, err := legacyBindingID(repository, binding)
		if err != nil {
			return err
		}
		comment, err := reader.CommitComment(ctx, repository, id)
		if err != nil {
			return err
		}
		if comment.CommitID != candidate || legacyHash([]byte(comment.Body)) != binding.Digest {
			return fmt.Errorf("legacy archive changed or foreign commit")
		}
		record, err := legacyComment(comment.Body)
		if err != nil {
			return err
		}
		if record.Repository != repository || record.Integration != integration || record.To != to || record.CandidateSHA != candidate {
			return fmt.Errorf("foreign legacy provenance")
		}
		for _, m := range record.Merges {
			c, ok := bySHA[m.SHA]
			if !ok || !unknown[m.SHA] || carriesLaneTrailer(SourceCommit{Body: c.Message}) || len(c.Parents) != 2 || c.Parents[0] != m.FirstParent || c.Parents[1] != m.SecondParent || claimed[m.SHA] {
				return fmt.Errorf("legacy graph mismatch or duplicate claim")
			}
			claimed[m.SHA] = true
		}
		verdict.Legacy = append(verdict.Legacy, record)
		verdict.MergedBy = append(verdict.MergedBy, record.Adopter.Session)
		if record.OriginalMerger != "" {
			verdict.MergedBy = append(verdict.MergedBy, record.OriginalMerger)
		}
	}
	var remainingUnknown []string
	for _, sha := range verdict.Unrecorded {
		if !claimed[sha] {
			remainingUnknown = append(remainingUnknown, sha)
		}
	}
	verdict.Unrecorded = remainingUnknown
	verdict.LegacyBindings = canonical
	return nil
}

func (adapter githubIntegration) LegacyComments(ctx context.Context, repository, candidate string) ([]LegacyBinding, error) {
	if !legacyRepositoryPattern.MatchString(repository) || !legacySHA.MatchString(candidate) {
		return nil, fmt.Errorf("invalid legacy archive endpoint")
	}
	raw, err := adapter.gh(ctx, "api", "--paginate", "--slurp", "repos/"+repository+"/commits/"+candidate+"/comments?per_page=100")
	if err != nil {
		return nil, err
	}
	var pages [][]struct {
		Body     string `json:"body"`
		URL      string `json:"html_url"`
		CommitID string `json:"commit_id"`
	}
	if err := json.Unmarshal(raw, &pages); err != nil {
		return nil, err
	}
	if len(pages) == 0 {
		return nil, fmt.Errorf("incomplete legacy comment inventory")
	}
	var bindings []LegacyBinding
	for _, page := range pages {
		if page == nil {
			return nil, fmt.Errorf("incomplete legacy comment page")
		}
		for _, comment := range page {
			if strings.HasPrefix(comment.Body, legacyRecordPrefix) {
				binding := LegacyBinding{comment.URL, legacyHash([]byte(comment.Body))}
				sha, _, err := legacyBindingID(repository, binding)
				if err != nil || sha != candidate || comment.CommitID != candidate {
					return nil, fmt.Errorf("foreign legacy archive response")
				}
				bindings = append(bindings, binding)
			}
		}
	}
	return bindings, nil
}

func (adapter githubIntegration) CreateLegacyComment(ctx context.Context, repository, candidate, body string) (LegacyBinding, error) {
	var binding LegacyBinding
	if !legacyRepositoryPattern.MatchString(repository) || !legacySHA.MatchString(candidate) {
		return binding, fmt.Errorf("invalid legacy archive endpoint")
	}
	record, err := legacyComment(body)
	if err != nil || record.Repository != repository || record.CandidateSHA != candidate {
		return binding, fmt.Errorf("invalid legacy archive body")
	}
	raw, err := adapter.gh(ctx, "api", "-X", "POST", "repos/"+repository+"/commits/"+candidate+"/comments", "-f", "body="+body)
	if err != nil {
		return binding, err
	}
	var comment struct {
		URL      string `json:"html_url"`
		Body     string `json:"body"`
		CommitID string `json:"commit_id"`
	}
	if err := json.Unmarshal(raw, &comment); err != nil {
		return binding, err
	}
	if comment.CommitID != candidate || comment.Body != body {
		return binding, fmt.Errorf("unconfirmed legacy archive write")
	}
	binding = LegacyBinding{comment.URL, legacyHash([]byte(comment.Body))}
	return binding, nil
}

func legacyRecordBindings(ctx context.Context, options PromoteOptions) ([]LegacyBinding, error) {
	// Explicit inputs override no stored inventory: conflicts are rejected rather
	// than replacing an existing attestation silently.
	_, body, err := options.resolveGitHub().OpenPullRequest(ctx, options.Repository, options.Integration, options.To)
	if err != nil {
		return nil, err
	}
	stored, err := parseLegacyInventory(body)
	if err != nil {
		return nil, err
	}
	bindings := append([]LegacyBinding(nil), stored...)
	for _, raw := range options.LegacyRecords {
		url, digest, ok := strings.Cut(raw, "=sha256:")
		if !ok {
			return nil, fmt.Errorf("legacy record must be <commit-comment-url>=sha256:<digest>")
		}
		binding := LegacyBinding{url, "sha256:" + digest}
		if _, _, err := legacyBindingID(options.Repository, binding); err != nil {
			return nil, err
		}
		found := false
		for _, old := range bindings {
			if old.URL == binding.URL {
				if old.Digest != binding.Digest {
					return nil, fmt.Errorf("legacy record digest conflict")
				}
				found = true
			}
		}
		if !found {
			bindings = append(bindings, binding)
		}
	}
	return bindings, nil
}

func legacyReviewBinds(body, digest string) bool {
	var found string
	count := 0
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "Reviewed-Legacy-Provenance:") {
			count++
			found = strings.TrimSpace(strings.TrimPrefix(line, "Reviewed-Legacy-Provenance:"))
		}
	}
	return count == 1 && found == digest
}

func (options LegacyAdoptOptions) resolveLegacyArchive() legacyArchive {
	if options.archive != nil {
		return options.archive
	}
	return newGitHubIntegration()
}
