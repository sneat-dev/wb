package worktrees

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/semver"
	"gopkg.in/yaml.v3"
)

// SupersessionReceipt is the trusted-reviewer evidence required to retire a
// clean branch whose intent was split across replacement changes. It is an
// operator-supplied receipt, never an inference from CI, a closed PR, or
// patch/tree similarity.
type SupersessionReceipt struct {
	Version           int                       `json:"version"`
	Repository        string                    `json:"repository"`
	Task              string                    `json:"task"`
	Branch            string                    `json:"branch"`
	OriginalHead      string                    `json:"original_head"`
	Target            string                    `json:"target"`
	TargetHead        string                    `json:"target_head"`
	Replacements      []SupersessionReplacement `json:"replacements"`
	Residuals         []SupersessionResidual    `json:"residuals"`
	ResidualsComplete bool                      `json:"residuals_complete"`
	Approval          SupersessionApproval      `json:"approval"`
	// OriginalPR identifies the source pull request when this is a dependency
	// consolidation. A PR-scoped receipt opts into exact dependency proof;
	// generic worktree supersessions leave it empty.
	OriginalPR               string                        `json:"original_pr,omitempty"`
	OriginalPRNumber         int                           `json:"original_pr_number,omitempty"`
	OriginalPRRepository     string                        `json:"original_pr_repository,omitempty"`
	OriginalPRHead           string                        `json:"original_pr_head,omitempty"`
	DependencyDeltasComplete bool                          `json:"dependency_deltas_complete,omitempty"`
	DependencyDeltas         []SupersessionDependencyDelta `json:"dependency_deltas,omitempty"`
}

// SupersessionDependencyDelta is immutable, per-source-PR evidence used before
// an old dependency PR may be called superseded. The selector is deliberately
// explicit: nx and @nx/* are different direct package identities.
type SupersessionDependencyDelta struct {
	SourcePR         string `json:"source_pr"`
	SourceHead       string `json:"source_head"`
	Consumer         string `json:"consumer"`
	Ecosystem        string `json:"ecosystem"`
	Package          string `json:"package"`
	Manifest         string `json:"manifest"`
	Selector         string `json:"selector"`
	Before           string `json:"before"`
	RequestedAfter   string `json:"requested_after"`
	CandidateAfter   string `json:"candidate_after"`
	Lockfile         string `json:"lockfile,omitempty"`
	LockfileSelector string `json:"lockfile_selector,omitempty"`
	LockfileVersion  string `json:"lockfile_version,omitempty"`
	Reviewed         bool   `json:"reviewed"`
}

// DependencyAuditJSON and DependencyAuditMarkdown are deterministic per-PR
// renderings. They sort a copy so receipt bytes remain immutable.
func (receipt SupersessionReceipt) DependencyAuditJSON() ([]byte, error) {
	deltas := sortedDependencyDeltas(receipt.DependencyDeltas)
	payload := struct {
		OriginalPR       string                        `json:"original_pr"`
		OriginalPRNumber int                           `json:"original_pr_number"`
		OriginalPRRepo   string                        `json:"original_pr_repository"`
		OriginalPRHead   string                        `json:"original_pr_head"`
		OriginalHead     string                        `json:"original_head"`
		TargetHead       string                        `json:"target_head"`
		Complete         bool                          `json:"dependency_deltas_complete"`
		DependencyDeltas []SupersessionDependencyDelta `json:"dependency_deltas"`
	}{receipt.OriginalPR, receipt.OriginalPRNumber, receipt.OriginalPRRepository, receipt.OriginalPRHead, receipt.OriginalHead, receipt.TargetHead, receipt.DependencyDeltasComplete, deltas}
	return json.MarshalIndent(payload, "", "  ")
}

func (receipt SupersessionReceipt) DependencyAuditMarkdown() string {
	deltas := sortedDependencyDeltas(receipt.DependencyDeltas)
	var output strings.Builder
	fmt.Fprintf(&output, "# Dependency supersession audit: %s\n\n", receipt.OriginalPR)
	fmt.Fprintf(&output, "- Source PR: `%s` (#%d, `%s`)\n- Source head: `%s`\n- Candidate target: `%s`\n- Complete: `%t`\n\n", receipt.OriginalPR, receipt.OriginalPRNumber, receipt.OriginalPRRepository, receipt.OriginalHead, receipt.TargetHead, receipt.DependencyDeltasComplete)
	output.WriteString("| Consumer | Package | Manifest selector | Before | Requested after | Candidate after | Lockfile | Lockfile version | Source head | Reviewed |\n")
	output.WriteString("|---|---|---|---|---|---|---|---|---|---|\n")
	for _, delta := range deltas {
		fmt.Fprintf(&output, "| `%s` | `%s` | `%s` | `%s` | `%s` | `%s` | `%s` | `%s` | `%s` | %t |\n",
			delta.Consumer, delta.Package, delta.Manifest+":"+delta.Selector, delta.Before, delta.RequestedAfter, delta.CandidateAfter,
			delta.Lockfile, delta.LockfileVersion, delta.SourceHead, delta.Reviewed)
	}
	return output.String()
}

func sortedDependencyDeltas(deltas []SupersessionDependencyDelta) []SupersessionDependencyDelta {
	sorted := append([]SupersessionDependencyDelta(nil), deltas...)
	sort.Slice(sorted, func(i, j int) bool {
		left, right := sorted[i], sorted[j]
		for _, pair := range [][2]string{{left.SourcePR, right.SourcePR}, {left.Consumer, right.Consumer}, {left.Manifest, right.Manifest}, {left.Selector, right.Selector}, {left.Package, right.Package}, {left.Before, right.Before}} {
			if pair[0] != pair[1] {
				return pair[0] < pair[1]
			}
		}
		return left.RequestedAfter < right.RequestedAfter
	})
	return sorted
}

// SupersessionReplacement identifies the reviewed change that replaced an
// intended slice. A replacement may be a merged PR or an exact commit.
type SupersessionReplacement struct {
	Kind string `json:"kind"` // pr or commit
	Ref  string `json:"ref"`
	SHA  string `json:"sha,omitempty"`
}

// SupersessionResidual classifies one commit reachable from the original
// branch and outside the exact target. Every such commit must appear exactly
// once, including commits whose intended slice was replaced elsewhere.
type SupersessionResidual struct {
	Commit         string   `json:"commit"`
	Classification string   `json:"classification"` // replaced, obsolete, regressive, or cosmetic
	Reason         string   `json:"reason"`
	ReplacementRef string   `json:"replacement_ref,omitempty"`
	Paths          []string `json:"paths,omitempty"`
	Reviewed       bool     `json:"reviewed"`
}

// SupersessionApproval is the explicit trusted-reviewer boundary. Trusted is
// intentionally a field in the immutable receipt: WB never guesses who is
// trusted from a PR, commit author, CI result, or local identity.
type SupersessionApproval struct {
	Actor      string    `json:"actor"`
	Trusted    bool      `json:"trusted"`
	Decision   string    `json:"decision"`
	ReceiptID  string    `json:"receipt_id"`
	ApprovedAt time.Time `json:"approved_at"`
}

var supersessionClassifications = map[string]bool{
	"replaced":   true,
	"obsolete":   true,
	"regressive": true,
	"cosmetic":   true,
}

// supersessionReceiptForEntry loads and independently verifies one receipt
// against the current exact source and fetched target identities.
func supersessionReceiptForEntry(ctx context.Context, path string, entry ListResult) (*SupersessionReceipt, string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Sprintf("read supersession receipt %s: %v", path, err), nil
	}
	var receipt SupersessionReceipt
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return nil, fmt.Sprintf("decode supersession receipt %s: %v", path, err), nil
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Sprintf("supersession receipt %s contains trailing JSON", path), nil
	}
	if rejection := validateSupersessionReceipt(ctx, receipt, entry); rejection != "" {
		return nil, rejection, nil
	}
	return &receipt, "", nil
}

func applySupersessionReceipt(ctx context.Context, path string, entry *ListResult) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	receipt, rejection, err := supersessionReceiptForEntry(ctx, path, *entry)
	if err != nil {
		return err
	}
	if rejection != "" {
		entry.SupersessionRejection = rejection
		entry.SupersededAtOrigin = false
		return nil
	}
	entry.SupersededAtOrigin = true
	entry.SupersessionReceipt = path
	entry.SupersessionReviewer = receipt.Approval.Actor
	entry.SupersessionReceiptID = receipt.Approval.ReceiptID
	entry.SupersessionRejection = ""
	entry.supersessionReceipt = receipt
	return nil
}

func validateSupersessionReceipt(ctx context.Context, receipt SupersessionReceipt, entry ListResult) string {
	if receipt.Version != 1 {
		return fmt.Sprintf("supersession receipt version %d is unsupported", receipt.Version)
	}
	if receipt.Repository != entry.Repository || receipt.Task != entry.Task || receipt.Branch != entry.Branch {
		return "supersession receipt source identity does not match the live worktree"
	}
	if receipt.OriginalHead != entry.HeadSHA {
		return fmt.Sprintf("supersession receipt original head %s does not match live head %s", receipt.OriginalHead, entry.HeadSHA)
	}
	if receipt.Target != entry.Base {
		return fmt.Sprintf("supersession receipt target %q does not match requested target %q", receipt.Target, entry.Base)
	}
	if receipt.TargetHead == "" || receipt.TargetHead != entry.RemoteTargetSHA {
		return fmt.Sprintf("supersession receipt target head %s does not match exact fetched origin/%s head %s", receipt.TargetHead, entry.Base, entry.RemoteTargetSHA)
	}
	if len(receipt.Replacements) == 0 {
		return "supersession receipt has no replacement PRs or commits"
	}
	if rejection := validateDependencyDeltas(ctx, receipt, entry); rejection != "" {
		return rejection
	}
	for index, replacement := range receipt.Replacements {
		kind := strings.ToLower(strings.TrimSpace(replacement.Kind))
		if kind != "pr" && kind != "commit" {
			return fmt.Sprintf("replacement %d has unsupported kind %q", index+1, replacement.Kind)
		}
		if strings.TrimSpace(replacement.Ref) == "" && strings.TrimSpace(replacement.SHA) == "" {
			return fmt.Sprintf("replacement %d has no PR or commit reference", index+1)
		}
		if kind == "commit" && !isGitObjectID(replacement.SHA) {
			return fmt.Sprintf("replacement %d commit has no valid landed SHA", index+1)
		}
		if !isGitObjectID(replacement.SHA) {
			return fmt.Sprintf("replacement %d has no valid landed commit SHA", index+1)
		}
		landed, err := isAncestor(ctx, entry.CanonicalDir, replacement.SHA, entry.RemoteTargetSHA)
		if err != nil {
			return fmt.Sprintf("verify replacement %d landed in target: %v", index+1, err)
		}
		if !landed {
			return fmt.Sprintf("replacement %d commit %s is not contained in the exact target", index+1, replacement.SHA)
		}
	}
	if !receipt.ResidualsComplete {
		return "supersession receipt does not declare a complete residual inventory"
	}
	if len(receipt.Residuals) == 0 {
		return "supersession receipt has no classified residuals"
	}
	if receipt.Approval.Actor == "" || !receipt.Approval.Trusted || strings.ToLower(receipt.Approval.Decision) != "approved" || receipt.Approval.ReceiptID == "" || receipt.Approval.ApprovedAt.IsZero() {
		return "supersession receipt has no complete trusted-reviewer approval"
	}

	commitsOutput, err := git(ctx, entry.CanonicalDir, "rev-list", "--reverse", "--end-of-options", entry.RemoteTargetSHA+".."+entry.HeadSHA)
	if err != nil {
		return fmt.Sprintf("enumerate original branch commits: %v", err)
	}
	original := strings.Fields(commitsOutput)
	if len(original) == 0 {
		return "supersession receipt names a branch with no residual commits outside the target"
	}
	originalSet := make(map[string]bool, len(original))
	for _, commit := range original {
		originalSet[commit] = true
	}
	seen := make(map[string]bool, len(receipt.Residuals))
	for index, residual := range receipt.Residuals {
		if !isGitObjectID(residual.Commit) {
			return fmt.Sprintf("residual %d has invalid commit %q", index+1, residual.Commit)
		}
		if !originalSet[residual.Commit] {
			return fmt.Sprintf("residual %s is not a commit in the original branch", residual.Commit)
		}
		if seen[residual.Commit] {
			return fmt.Sprintf("residual %s is classified more than once", residual.Commit)
		}
		seen[residual.Commit] = true
		classification := strings.ToLower(strings.TrimSpace(residual.Classification))
		if !supersessionClassifications[classification] {
			return fmt.Sprintf("residual %s is unclassified (classification %q)", residual.Commit, residual.Classification)
		}
		if strings.TrimSpace(residual.Reason) == "" {
			return fmt.Sprintf("residual %s has no reviewer reason", residual.Commit)
		}
		if !residual.Reviewed {
			return fmt.Sprintf("residual %s is unreviewed", residual.Commit)
		}
		if classification == "replaced" && strings.TrimSpace(residual.ReplacementRef) == "" {
			return fmt.Sprintf("replaced residual %s has no replacement reference", residual.Commit)
		}
	}
	missing := make([]string, 0)
	for _, commit := range original {
		if !seen[commit] {
			missing = append(missing, commit)
		}
	}
	if len(missing) != 0 {
		sort.Strings(missing)
		return fmt.Sprintf("supersession receipt has unclassified residual commit(s): %s", strings.Join(missing, ", "))
	}
	return ""
}

// validateDependencyDeltas is called while source and target identities are
// still the exact ones used for cleanup. Generic worktree receipts retain
// their existing schema; dependency PR receipts opt into this fail-closed
// proof boundary.
func validateDependencyDeltas(ctx context.Context, receipt SupersessionReceipt, entry ListResult) string {
	if strings.TrimSpace(receipt.OriginalPR) == "" {
		if dependencyCampaignWorktree(ctx, entry) {
			return "dependency campaign supersession requires original_pr and exact dependency delta evidence"
		}
		if receipt.DependencyDeltasComplete || len(receipt.DependencyDeltas) > 0 {
			return "dependency delta evidence requires original_pr"
		}
		return ""
	}
	if rejection := validateAuthoritativeSourcePullRequest(receipt, entry); rejection != "" {
		return rejection
	}
	if !receipt.DependencyDeltasComplete {
		return "dependency delta evidence is incomplete; terminal supersession is refused"
	}
	if len(receipt.DependencyDeltas) == 0 {
		return "dependency PR supersession has no exact manifest/importer delta"
	}
	deltas := append([]SupersessionDependencyDelta(nil), receipt.DependencyDeltas...)
	sort.Slice(deltas, func(i, j int) bool {
		left, right := deltas[i], deltas[j]
		for _, pair := range [][2]string{{left.SourcePR, right.SourcePR}, {left.Manifest, right.Manifest}, {left.Selector, right.Selector}, {left.Package, right.Package}} {
			if pair[0] != pair[1] {
				return pair[0] < pair[1]
			}
		}
		return left.Before < right.Before
	})
	for index, delta := range deltas {
		prefix := fmt.Sprintf("dependency delta %d", index+1)
		if delta.SourcePR != receipt.OriginalPR {
			return fmt.Sprintf("%s source PR %q does not match original PR %q", prefix, delta.SourcePR, receipt.OriginalPR)
		}
		if delta.SourceHead != receipt.OriginalHead || delta.SourceHead != entry.HeadSHA {
			return fmt.Sprintf("%s source head %q does not match exact original head %q (source PR may have been force-updated)", prefix, delta.SourceHead, entry.HeadSHA)
		}
		if delta.Consumer != entry.Repository {
			return fmt.Sprintf("%s consumer %q does not match exact repository %q", prefix, delta.Consumer, entry.Repository)
		}
		for name, value := range map[string]string{
			"ecosystem": delta.Ecosystem, "package": delta.Package, "manifest": delta.Manifest,
			"selector": delta.Selector, "before": delta.Before, "requested_after": delta.RequestedAfter,
			"candidate_after": delta.CandidateAfter,
		} {
			if strings.TrimSpace(value) == "" {
				return fmt.Sprintf("%s is missing %s proof", prefix, name)
			}
		}
		if !delta.Reviewed {
			return fmt.Sprintf("%s is unreviewed", prefix)
		}
		if !dependencyVersionSatisfies(delta.Ecosystem, delta.CandidateAfter, delta.RequestedAfter) {
			return fmt.Sprintf("%s candidate version %q does not satisfy requested %q", prefix, delta.CandidateAfter, delta.RequestedAfter)
		}
		manifest, err := git(ctx, entry.CanonicalDir, "show", entry.RemoteTargetSHA+":"+delta.Manifest)
		if err != nil {
			return fmt.Sprintf("%s cannot read exact target manifest %q: %v", prefix, delta.Manifest, err)
		}
		if rejection := validateDependencyManifest(delta, []byte(manifest), delta.RequestedAfter, false); rejection != "" {
			return prefix + " " + rejection
		}
		observedCandidate, found, err := dependencyManifestValue(delta, []byte(manifest))
		if err != nil {
			return fmt.Sprintf("%s cannot read candidate direct dependency value: %v", prefix, err)
		}
		if !found || observedCandidate != delta.CandidateAfter {
			return fmt.Sprintf("%s candidate manifest value %q does not match recorded candidate %q", prefix, observedCandidate, delta.CandidateAfter)
		}
		baseHead, err := git(ctx, entry.CanonicalDir, "merge-base", delta.SourceHead, receipt.TargetHead)
		if err != nil {
			return fmt.Sprintf("%s cannot derive source PR base for before-version proof: %v", prefix, err)
		}
		beforeManifest, err := git(ctx, entry.CanonicalDir, "show", strings.TrimSpace(baseHead)+":"+delta.Manifest)
		if err != nil {
			return fmt.Sprintf("%s cannot read source PR base manifest %q at %s: %v", prefix, delta.Manifest, strings.TrimSpace(baseHead), err)
		}
		if rejection := validateDependencyManifest(delta, []byte(beforeManifest), delta.Before, true); rejection != "" {
			return prefix + " source PR before-version proof: " + rejection
		}
		sourceManifest, err := git(ctx, entry.CanonicalDir, "show", delta.SourceHead+":"+delta.Manifest)
		if err != nil {
			return fmt.Sprintf("%s cannot read exact source PR manifest %q at %s: %v", prefix, delta.Manifest, delta.SourceHead, err)
		}
		if rejection := validateDependencyManifest(delta, []byte(sourceManifest), delta.RequestedAfter, true); rejection != "" {
			return prefix + " source PR requested-after proof: " + rejection
		}
		applicableLockfile, hasLockfile, err := dependencyLockfile(ctx, entry.CanonicalDir, entry.RemoteTargetSHA, delta)
		if err != nil {
			return fmt.Sprintf("%s cannot inspect lockfiles for exact manifest %q: %v", prefix, delta.Manifest, err)
		}
		if hasLockfile && delta.Lockfile == "" {
			return fmt.Sprintf("%s is missing resolved lockfile proof for %q", prefix, applicableLockfile)
		}
		if delta.Lockfile != "" && !hasLockfile {
			return fmt.Sprintf("%s names lockfile %q but no applicable lockfile exists for %q", prefix, delta.Lockfile, delta.Manifest)
		}
		if delta.Lockfile != "" {
			if delta.LockfileSelector == "" || delta.LockfileVersion == "" {
				return fmt.Sprintf("%s lockfile proof is incomplete", prefix)
			}
			if delta.Lockfile != applicableLockfile {
				return fmt.Sprintf("%s lockfile %q is not the exact lockfile for manifest %q (want %q)", prefix, delta.Lockfile, delta.Manifest, applicableLockfile)
			}
			lockfile, err := git(ctx, entry.CanonicalDir, "show", entry.RemoteTargetSHA+":"+delta.Lockfile)
			if err != nil {
				return fmt.Sprintf("%s cannot read exact target lockfile %q: %v", prefix, delta.Lockfile, err)
			}
			if delta.LockfileVersion != delta.RequestedAfter {
				return fmt.Sprintf("%s lockfile version %q does not satisfy requested %q", prefix, delta.LockfileVersion, delta.RequestedAfter)
			}
			if path.Base(delta.Lockfile) == "yarn.lock" {
				return fmt.Sprintf("%s lockfile format yarn.lock is unsupported; terminal supersession is refused", prefix)
			}
			if !selectorNamesExactPackage(delta.LockfileSelector, delta.Package) || !lockfileEntryContainsVersion(delta.Ecosystem, delta.Lockfile, lockfile, delta.LockfileSelector, delta.LockfileVersion) {
				return fmt.Sprintf("%s lockfile does not prove exact selector %q at %q", prefix, delta.LockfileSelector, delta.LockfileVersion)
			}
		}
	}
	return ""
}

// ValidateDependencyDeltas exposes the same fail-closed dependency proof used
// by supersession cleanup to campaign/report integrations and their tests.
// Callers must provide the live ListResult, including authoritative PR data.
func ValidateDependencyDeltas(ctx context.Context, receipt SupersessionReceipt, entry ListResult) error {
	if rejection := validateDependencyDeltas(ctx, receipt, entry); rejection != "" {
		return fmt.Errorf("%s", rejection)
	}
	return nil
}

func validateAuthoritativeSourcePullRequest(receipt SupersessionReceipt, entry ListResult) string {
	pr := entry.OpenPullRequest
	if pr == nil {
		return "dependency receipt has no authoritative source pull request in the live inventory"
	}
	if pr.Number <= 0 || strings.TrimSpace(pr.URL) == "" || strings.TrimSpace(pr.Repository) == "" || strings.TrimSpace(pr.HeadSHA) == "" {
		return "dependency receipt authoritative source pull request is missing URL, number, repository, or head"
	}
	if receipt.OriginalPR != pr.URL {
		return fmt.Sprintf("dependency receipt original_pr %q does not match authoritative source pull request URL %q", receipt.OriginalPR, pr.URL)
	}
	if receipt.OriginalPRNumber <= 0 || receipt.OriginalPRNumber != pr.Number {
		return fmt.Sprintf("dependency receipt original PR number %d does not match authoritative source pull request number %d", receipt.OriginalPRNumber, pr.Number)
	}
	if receipt.OriginalPRRepository == "" || receipt.OriginalPRRepository != pr.Repository || pr.Repository != entry.Repository {
		return fmt.Sprintf("dependency receipt original PR repository %q does not match authoritative source repository %q", receipt.OriginalPRRepository, pr.Repository)
	}
	if receipt.OriginalPRHead == "" || receipt.OriginalPRHead != pr.HeadSHA || pr.HeadSHA != receipt.OriginalHead || pr.HeadSHA != entry.HeadSHA {
		return fmt.Sprintf("dependency receipt original PR head %q does not match authoritative source head %q", receipt.OriginalPRHead, pr.HeadSHA)
	}
	return ""
}

func dependencyCampaignWorktree(ctx context.Context, entry ListResult) bool {
	if strings.HasPrefix(entry.Task, "deps-") || strings.HasPrefix(entry.Branch, "wb/deps/") {
		return true
	}
	if entry.WorktreeDir == "" {
		return false
	}
	manifest, err := ReadManifest(entry.WorktreeDir)
	if err == nil && manifest.DependencyCampaign {
		return true
	}
	if entry.CanonicalDir == "" || entry.HeadSHA == "" || entry.RemoteTargetSHA == "" {
		return false
	}
	changed, err := git(ctx, entry.CanonicalDir, "diff", "--name-only", "--diff-filter=ACMR", entry.RemoteTargetSHA, entry.HeadSHA)
	if err != nil {
		// With exact source and target identities present, an unreadable diff
		// must fail closed instead of allowing a generic terminalization.
		return true
	}
	for _, file := range strings.Fields(changed) {
		if isDependencyManifestOrImporter(file) {
			return true
		}
	}
	return false
}

func isDependencyManifestOrImporter(file string) bool {
	file = path.Clean(strings.TrimSpace(file))
	base := path.Base(file)
	switch base {
	case "package.json", "package-lock.json", "pnpm-lock.yaml", "pnpm-workspace.yaml", "pnpm-workspace.yml", "yarn.lock", "go.mod", "go.sum":
		return true
	}
	return strings.HasPrefix(file, ".github/workflows/") && (strings.HasSuffix(file, ".yml") || strings.HasSuffix(file, ".yaml"))
}

func dependencyLockfile(ctx context.Context, canonical, target string, delta SupersessionDependencyDelta) (string, bool, error) {
	contents, err := git(ctx, canonical, "ls-tree", "-r", "--name-only", target)
	if err != nil {
		return "", false, err
	}
	manifestDir := path.Dir(delta.Manifest)
	if manifestDir == "." {
		manifestDir = ""
	}
	var names []string
	switch strings.ToLower(strings.TrimSpace(delta.Ecosystem)) {
	case "npm":
		names = []string{"pnpm-lock.yaml", "package-lock.json", "yarn.lock"}
	case "go":
		names = []string{"go.sum"}
	default:
		return "", false, nil
	}
	var best string
	for _, candidate := range strings.Fields(contents) {
		base := path.Base(candidate)
		if !containsString(names, base) {
			continue
		}
		dir := path.Dir(candidate)
		if dir == "." {
			dir = ""
		}
		if manifestDir != dir && !strings.HasPrefix(manifestDir, dir+"/") {
			continue
		}
		if best == "" || len(dir) > len(path.Dir(best)) || (len(dir) == len(path.Dir(best)) && candidate < best) {
			best = candidate
		}
	}
	return best, best != "", nil
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func selectorNamesExactPackage(selector, packageName string) bool {
	if selector == packageName {
		return packageName != ""
	}
	for _, lockfile := range []string{"package-lock.json", "pnpm-lock.yaml"} {
		if _, ok := parseLockfileSelector("npm", lockfile, selector, packageName); ok {
			return true
		}
	}
	return false
}

func lockfileEntryContainsVersion(ecosystem, lockfilePath, contents, selector, version string) bool {
	if strings.EqualFold(ecosystem, "go") {
		for _, line := range strings.Split(contents, "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[0] == selector && fields[1] == version {
				return true
			}
		}
		return false
	}
	if path.Base(lockfilePath) == "yarn.lock" {
		return false
	}
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(contents), &document); err != nil {
		return false
	}
	node := &document
	if len(node.Content) == 1 {
		node = node.Content[0]
	}
	packageName := selectorPackageFromLockfileSelector(selector)
	segments, ok := parseLockfileSelector(ecosystem, lockfilePath, selector, packageName)
	if !ok {
		return false
	}
	for _, segment := range segments {
		if node.Kind != yaml.MappingNode {
			return false
		}
		var next *yaml.Node
		for index := 0; index+1 < len(node.Content); index += 2 {
			if node.Content[index].Value == segment {
				next = node.Content[index+1]
				break
			}
		}
		if next == nil {
			return false
		}
		node = next
	}
	return node.Kind == yaml.ScalarNode && node.Value == version
}

func selectorPackageFromLockfileSelector(selector string) string {
	segments := strings.Split(selector, "|")
	if len(segments) != 3 {
		return ""
	}
	switch segments[0] {
	case "packages":
		return strings.TrimPrefix(segments[1], "node_modules/")
	case "snapshots":
		key := strings.TrimPrefix(segments[1], "/")
		if at := strings.LastIndex(key, "@"); at > 0 {
			return key[:at]
		}
	}
	return ""
}

// parseLockfileSelector accepts only the selectors emitted by the dependency
// campaign report. Package identity is parsed as a token, never searched as a
// substring, so nx cannot be proven by nxfoo or @nx/js.
func parseLockfileSelector(ecosystem, lockfilePath, selector, packageName string) ([]string, bool) {
	if !strings.EqualFold(ecosystem, "npm") || packageName == "" {
		return nil, false
	}
	segments := strings.Split(selector, "|")
	if len(segments) != 3 || segments[2] != "version" {
		return nil, false
	}
	switch path.Base(lockfilePath) {
	case "package-lock.json":
		if segments[0] != "packages" || segments[1] != "node_modules/"+packageName {
			return nil, false
		}
	case "pnpm-lock.yaml":
		if segments[0] != "snapshots" || !strings.HasPrefix(segments[1], "/"+packageName+"@") {
			return nil, false
		}
		// Reject a package token that only matches a prefix of a scoped or
		// unscoped package key. The required @ delimiter is the grammar bound.
		if strings.TrimPrefix(segments[1], "/"+packageName+"@") == "" {
			return nil, false
		}
	default:
		return nil, false
	}
	return segments, true
}

// dependencyVersionSatisfies applies the version semantics of the supported
// ecosystem instead of treating a requested range as a literal string. npm
// ranges commonly arrive as ^ or ~ constraints; Go module requirements remain
// exact versions here. Unknown syntax fails closed.
func dependencyVersionSatisfies(ecosystem, candidate, requested string) bool {
	candidate = normalizeDependencyVersion(candidate)
	requested = strings.TrimSpace(requested)
	if requested == "" || candidate == "" {
		return false
	}
	if strings.EqualFold(ecosystem, "go") {
		return semver.IsValid(candidate) && semver.IsValid(normalizeDependencyVersion(requested)) && candidate == normalizeDependencyVersion(requested)
	}
	if !strings.EqualFold(ecosystem, "npm") {
		return candidate == normalizeDependencyVersion(requested)
	}
	for _, alternative := range strings.Split(requested, "||") {
		if npmRangeAlternativeSatisfies(candidate, strings.TrimSpace(alternative)) {
			return true
		}
	}
	return false
}

func normalizeDependencyVersion(value string) string {
	value = strings.TrimSpace(value)
	if value != "" && value[0] != 'v' {
		return "v" + value
	}
	return value
}

func npmRangeAlternativeSatisfies(candidate, requested string) bool {
	if semver.IsValid(candidate) && semver.IsValid(normalizeDependencyVersion(requested)) {
		return semver.Compare(candidate, normalizeDependencyVersion(requested)) == 0
	}
	if strings.HasPrefix(requested, "^") || strings.HasPrefix(requested, "~") {
		operator, base := requested[:1], normalizeDependencyVersion(requested[1:])
		if !semver.IsValid(base) || !semver.IsValid(candidate) || semver.Compare(candidate, base) < 0 {
			return false
		}
		parts := strings.Split(strings.TrimPrefix(base, "v"), ".")
		if len(parts) != 3 {
			return false
		}
		var upper string
		if operator == "^" {
			switch {
			case parts[0] != "0":
				upper = "v" + fmt.Sprintf("%d.0.0", mustAtoi(parts[0])+1)
			case parts[1] != "0":
				upper = "v0." + fmt.Sprintf("%d.0", mustAtoi(parts[1])+1)
			default:
				upper = "v0.0." + fmt.Sprintf("%d", mustAtoi(parts[2])+1)
			}
		} else {
			upper = "v" + parts[0] + "." + fmt.Sprintf("%d.0", mustAtoi(parts[1])+1)
		}
		return semver.Compare(candidate, upper) < 0
	}
	constraints := strings.Fields(requested)
	if len(constraints) > 1 {
		for _, constraint := range constraints {
			if !npmComparatorSatisfies(candidate, constraint) {
				return false
			}
		}
		return true
	}
	return npmComparatorSatisfies(candidate, requested)
}

func npmComparatorSatisfies(candidate, constraint string) bool {
	operator := "="
	for _, candidateOperator := range []string{"<=", ">=", "<", ">", "="} {
		if strings.HasPrefix(constraint, candidateOperator) {
			operator = candidateOperator
			constraint = strings.TrimSpace(strings.TrimPrefix(constraint, candidateOperator))
			break
		}
	}
	if strings.ContainsAny(constraint, "xX*") {
		parts := strings.Split(strings.TrimPrefix(normalizeDependencyVersion(constraint), "v"), ".")
		candidateParts := strings.Split(strings.TrimPrefix(candidate, "v"), ".")
		for index, part := range parts {
			if part == "x" || part == "X" || part == "*" {
				break
			}
			if index >= len(candidateParts) || part != candidateParts[index] {
				return false
			}
		}
		return true
	}
	version := normalizeDependencyVersion(constraint)
	if !semver.IsValid(candidate) || !semver.IsValid(version) {
		return false
	}
	comparison := semver.Compare(candidate, version)
	switch operator {
	case "<":
		return comparison < 0
	case "<=":
		return comparison <= 0
	case ">":
		return comparison > 0
	case ">=":
		return comparison >= 0
	default:
		return comparison == 0
	}
}

func mustAtoi(value string) int {
	var result int
	for _, digit := range value {
		result = result*10 + int(digit-'0')
	}
	return result
}

func validateDependencyManifest(delta SupersessionDependencyDelta, contents []byte, expectedVersion string, exact bool) string {
	switch strings.ToLower(strings.TrimSpace(delta.Ecosystem)) {
	case "npm":
		var manifest struct {
			Dependencies         map[string]string `json:"dependencies"`
			DevDependencies      map[string]string `json:"devDependencies"`
			PeerDependencies     map[string]string `json:"peerDependencies"`
			OptionalDependencies map[string]string `json:"optionalDependencies"`
		}
		if err := json.Unmarshal(contents, &manifest); err != nil {
			return fmt.Sprintf("cannot parse npm manifest %q: %v", delta.Manifest, err)
		}
		parts := strings.Split(delta.Selector, ".")
		if len(parts) != 2 || parts[0] == "" || parts[1] != delta.Package {
			return fmt.Sprintf("npm selector %q is not the exact direct package selector for %q", delta.Selector, delta.Package)
		}
		var dependencies map[string]string
		switch parts[0] {
		case "dependencies":
			dependencies = manifest.Dependencies
		case "devDependencies":
			dependencies = manifest.DevDependencies
		case "peerDependencies":
			dependencies = manifest.PeerDependencies
		case "optionalDependencies":
			dependencies = manifest.OptionalDependencies
		default:
			return fmt.Sprintf("npm selector %q is not a direct dependency field", delta.Selector)
		}
		value, ok := dependencies[delta.Package]
		if !ok {
			return fmt.Sprintf("npm direct package %q is absent from selector %q", delta.Package, delta.Selector)
		}
		if exact && value != expectedVersion || !exact && !dependencyVersionSatisfies(delta.Ecosystem, value, expectedVersion) {
			return fmt.Sprintf("npm direct package %q at %q is %q, want %q", delta.Package, delta.Selector, value, expectedVersion)
		}
	case "go":
		parsed, err := modfile.Parse(delta.Manifest, contents, nil)
		if err != nil {
			return fmt.Sprintf("cannot parse Go manifest %q: %v", delta.Manifest, err)
		}
		if delta.Selector != "require:"+delta.Package {
			return fmt.Sprintf("Go selector %q is not the exact direct require selector for %q", delta.Selector, delta.Package)
		}
		found := false
		for _, requirement := range parsed.Require {
			if requirement.Mod.Path == delta.Package {
				found = true
				if exact && requirement.Mod.Version != expectedVersion || !exact && !dependencyVersionSatisfies(delta.Ecosystem, requirement.Mod.Version, expectedVersion) {
					return fmt.Sprintf("Go direct module %q is %q, want %q", delta.Package, requirement.Mod.Version, expectedVersion)
				}
			}
		}
		if !found {
			return fmt.Sprintf("Go direct module %q is absent", delta.Package)
		}
	default:
		return fmt.Sprintf("unsupported dependency ecosystem %q", delta.Ecosystem)
	}
	return ""
}

func dependencyManifestValue(delta SupersessionDependencyDelta, contents []byte) (string, bool, error) {
	switch strings.ToLower(strings.TrimSpace(delta.Ecosystem)) {
	case "npm":
		var manifest struct {
			Dependencies         map[string]string `json:"dependencies"`
			DevDependencies      map[string]string `json:"devDependencies"`
			PeerDependencies     map[string]string `json:"peerDependencies"`
			OptionalDependencies map[string]string `json:"optionalDependencies"`
		}
		if err := json.Unmarshal(contents, &manifest); err != nil {
			return "", false, err
		}
		parts := strings.Split(delta.Selector, ".")
		if len(parts) != 2 || parts[1] != delta.Package {
			return "", false, nil
		}
		var dependencies map[string]string
		switch parts[0] {
		case "dependencies":
			dependencies = manifest.Dependencies
		case "devDependencies":
			dependencies = manifest.DevDependencies
		case "peerDependencies":
			dependencies = manifest.PeerDependencies
		case "optionalDependencies":
			dependencies = manifest.OptionalDependencies
		default:
			return "", false, nil
		}
		value, found := dependencies[delta.Package]
		return value, found, nil
	case "go":
		parsed, err := modfile.Parse(delta.Manifest, contents, nil)
		if err != nil {
			return "", false, err
		}
		if delta.Selector != "require:"+delta.Package {
			return "", false, nil
		}
		for _, requirement := range parsed.Require {
			if requirement.Mod.Path == delta.Package {
				return requirement.Mod.Version, true, nil
			}
		}
		return "", false, nil
	default:
		return "", false, fmt.Errorf("unsupported dependency ecosystem %q", delta.Ecosystem)
	}
}

func sameSupersessionReceipt(left, right *SupersessionReceipt) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}
