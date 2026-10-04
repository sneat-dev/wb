package worktreebranches

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/sneat-dev/wb/internal/worktreeproof"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/semver"
	"gopkg.in/yaml.v3"
)

// SupersessionEntry captures only the live identities needed to verify a
// reviewed replacement against an exact source and fetched target.
type SupersessionEntry struct {
	Task, Repository, Branch, Base, HeadSHA, RemoteTargetSHA string
	CanonicalDir, WorktreeDir                                string
	OpenPullRequest                                          *PullRequest
}

// SupersessionPorts supplies read-only observations. The facade retains the
// worktree, claim, and deletion transactions that consume a verified receipt.
type SupersessionPorts struct {
	Git                worktreeproof.GitQuery
	ReadGitFileBytes   func(context.Context, string, string, string) ([]byte, error)
	IsAncestor         func(context.Context, string, string, string) (bool, error)
	ReadReceipt        func(string) ([]byte, error)
	ReadCampaignMarker func(string) (bool, error)
}

type SupersessionService struct{ Ports SupersessionPorts }

// Shared receipt contracts are neutral proof data; branch policy verifies them.
type SupersessionReceipt = worktreeproof.SupersessionReceipt
type SupersessionDependencyDelta = worktreeproof.SupersessionDependencyDelta
type SupersessionReplacement = worktreeproof.SupersessionReplacement
type SupersessionResidual = worktreeproof.SupersessionResidual
type SupersessionApproval = worktreeproof.SupersessionApproval
type SupersessionWorkflowAdoption = worktreeproof.SupersessionWorkflowAdoption

func sortedDependencyDeltas(deltas []SupersessionDependencyDelta) []SupersessionDependencyDelta {
	return worktreeproof.SortedDependencyDeltas(deltas)
}

var supersessionClassifications = map[string]bool{
	"replaced":   true,
	"obsolete":   true,
	"regressive": true,
	"cosmetic":   true,
}

// SupersessionReceiptForEntry loads and independently verifies one receipt
// against the current exact source and fetched target identities.
func (service SupersessionService) SupersessionReceiptForEntry(ctx context.Context, path string, entry SupersessionEntry) (*SupersessionReceipt, string) {
	contents, err := service.Ports.ReadReceipt(path)
	if err != nil {
		return nil, fmt.Sprintf("read supersession receipt %s: %v", path, err)
	}
	var receipt SupersessionReceipt
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return nil, fmt.Sprintf("decode supersession receipt %s: %v", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Sprintf("supersession receipt %s contains trailing JSON", path)
	}
	if rejection := service.ValidateSupersessionReceipt(ctx, receipt, entry); rejection != "" {
		return nil, rejection
	}
	return &receipt, ""
}

func (service SupersessionService) ValidateSupersessionReceipt(ctx context.Context, receipt SupersessionReceipt, entry SupersessionEntry) string {
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
	if rejection := service.ValidateDependencyDeltasReason(ctx, receipt, entry); rejection != "" {
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
		if kind == "commit" && !worktreeproof.IsGitObjectID(replacement.SHA) {
			return fmt.Sprintf("replacement %d commit has no valid landed SHA", index+1)
		}
		if !worktreeproof.IsGitObjectID(replacement.SHA) {
			return fmt.Sprintf("replacement %d has no valid landed commit SHA", index+1)
		}
		landed, err := service.Ports.IsAncestor(ctx, entry.CanonicalDir, replacement.SHA, entry.RemoteTargetSHA)
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

	commitsOutput, err := service.Ports.Git(ctx, entry.CanonicalDir, "rev-list", "--reverse", "--end-of-options", entry.RemoteTargetSHA+".."+entry.HeadSHA)
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
		if !worktreeproof.IsGitObjectID(residual.Commit) {
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

// ValidateDependencyDeltasReason is called while source and target identities are
// still the exact ones used for cleanup. Generic worktree receipts retain
// their existing schema; dependency PR receipts opt into this fail-closed
// proof boundary.
func (service SupersessionService) ValidateDependencyDeltasReason(ctx context.Context, receipt SupersessionReceipt, entry SupersessionEntry) string {
	if strings.TrimSpace(receipt.OriginalPR) == "" {
		if strings.HasPrefix(entry.Task, "deps-") || strings.HasPrefix(entry.Branch, "wb/deps/") {
			return "dependency campaign supersession requires original_pr and exact dependency delta evidence"
		}
		if entry.WorktreeDir != "" && service.Ports.ReadCampaignMarker != nil {
			if campaign, err := service.Ports.ReadCampaignMarker(entry.WorktreeDir); err == nil && campaign {
				return "dependency campaign supersession requires original_pr and exact dependency delta evidence"
			}
		}
		changes, rejection := service.dependencyChanges(ctx, entry)
		if rejection != "" {
			return "dependency campaign supersession requires original_pr and exact dependency delta evidence: " + rejection
		}
		if receipt.DependencyDeltasComplete || len(receipt.DependencyDeltas) > 0 {
			return "dependency delta evidence requires original_pr"
		}
		if len(changes) == 0 {
			if len(receipt.WorkflowAdoptions) > 0 {
				return "workflow adoption evidence has no newly introduced dependency workflow"
			}
			return ""
		}
		return service.validateWorkflowAdoptions(ctx, receipt, entry, changes)
	}
	if len(receipt.WorkflowAdoptions) > 0 {
		return "workflow adoption evidence cannot replace exact dependency PR evidence"
	}
	if rejection := ValidateAuthoritativeSourcePullRequest(receipt, entry); rejection != "" {
		return rejection
	}
	deltas, rejection := DependencyDeltasForValidation(receipt, entry)
	if rejection != "" {
		return rejection
	}
	for index, delta := range deltas {
		prefix := fmt.Sprintf("dependency delta %d", index+1)
		manifest, err := service.Ports.Git(ctx, entry.CanonicalDir, "show", entry.RemoteTargetSHA+":"+delta.Manifest)
		if err != nil {
			return fmt.Sprintf("%s cannot read exact target manifest %q: %v", prefix, delta.Manifest, err)
		}
		candidateValues, rejection, _ := directDependencyEvidence(delta, []byte(manifest))
		if rejection != "" {
			return prefix + " " + rejection
		}
		if rejection := validateObservedDependencyVersions(delta, candidateValues, delta.RequestedAfter, false); rejection != "" {
			return prefix + " " + rejection
		}
		observedCandidate := candidateValues[0]
		if observedCandidate != delta.CandidateAfter {
			return fmt.Sprintf("%s candidate manifest value %q does not match recorded candidate %q", prefix, observedCandidate, delta.CandidateAfter)
		}
		baseHead, err := service.Ports.Git(ctx, entry.CanonicalDir, "merge-base", delta.SourceHead, receipt.TargetHead)
		if err != nil {
			return fmt.Sprintf("%s cannot derive source PR base for before-version proof: %v", prefix, err)
		}
		beforeManifest, err := service.Ports.Git(ctx, entry.CanonicalDir, "show", strings.TrimSpace(baseHead)+":"+delta.Manifest)
		if err != nil {
			return fmt.Sprintf("%s cannot read source PR base manifest %q at %s: %v", prefix, delta.Manifest, strings.TrimSpace(baseHead), err)
		}
		if rejection := ValidateDependencyManifest(delta, []byte(beforeManifest), delta.Before, true); rejection != "" {
			return prefix + " source PR before-version proof: " + rejection
		}
		sourceManifest, err := service.Ports.Git(ctx, entry.CanonicalDir, "show", delta.SourceHead+":"+delta.Manifest)
		if err != nil {
			return fmt.Sprintf("%s cannot read exact source PR manifest %q at %s: %v", prefix, delta.Manifest, delta.SourceHead, err)
		}
		if rejection := ValidateDependencyManifest(delta, []byte(sourceManifest), delta.RequestedAfter, true); rejection != "" {
			return prefix + " source PR requested-after proof: " + rejection
		}
		applicableLockfile, hasLockfile, err := service.DependencyLockfile(ctx, entry.CanonicalDir, entry.RemoteTargetSHA, delta)
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
			lockfile, err := service.Ports.Git(ctx, entry.CanonicalDir, "show", entry.RemoteTargetSHA+":"+delta.Lockfile)
			if err != nil {
				return fmt.Sprintf("%s cannot read exact target lockfile %q: %v", prefix, delta.Lockfile, err)
			}
			if delta.LockfileVersion != delta.RequestedAfter {
				return fmt.Sprintf("%s lockfile version %q does not satisfy requested %q", prefix, delta.LockfileVersion, delta.RequestedAfter)
			}
			if path.Base(delta.Lockfile) == "yarn.lock" {
				return fmt.Sprintf("%s lockfile format yarn.lock is unsupported; terminal supersession is refused", prefix)
			}
			if !SelectorNamesExactPackage(delta.LockfileSelector, delta.Package) || !LockfileEntryContainsVersion(delta.Ecosystem, delta.Lockfile, lockfile, delta.LockfileSelector, delta.LockfileVersion) {
				return fmt.Sprintf("%s lockfile does not prove exact selector %q at %q", prefix, delta.LockfileSelector, delta.LockfileVersion)
			}
		}
	}
	return ""
}

type dependencyChange struct {
	path  string
	added bool
}

// dependencyChanges returns only source changes whose dependency-bearing
// content changed. A missing or unreadable Git object is never treated as an
// unchanged file.
func (service SupersessionService) dependencyChanges(ctx context.Context, entry SupersessionEntry) ([]dependencyChange, string) {
	if entry.CanonicalDir == "" || entry.HeadSHA == "" || entry.RemoteTargetSHA == "" {
		return nil, "exact source and target identities are required to inspect dependency changes"
	}
	base, err := service.Ports.Git(ctx, entry.CanonicalDir, "merge-base", entry.HeadSHA, entry.RemoteTargetSHA)
	if err != nil || strings.TrimSpace(base) == "" {
		return nil, "cannot derive exact source/target merge base; dependency campaign supersession is refused"
	}
	changedFiles, err := service.Ports.Git(ctx, entry.CanonicalDir, "diff", "--name-only", "-z", "--no-renames", "--diff-filter=ACMRD", strings.TrimSpace(base), entry.HeadSHA)
	if err != nil {
		return nil, "cannot inspect exact source dependency diff; dependency campaign supersession is refused"
	}
	changes := make([]dependencyChange, 0)
	for _, file := range strings.Split(changedFiles, "\x00") {
		if file == "" || !IsDependencyManifestOrImporter(file) {
			continue
		}
		baseContents, baseErr := service.readGitFile(ctx, entry.CanonicalDir, strings.TrimSpace(base), file)
		headContents, headErr := service.readGitFile(ctx, entry.CanonicalDir, entry.HeadSHA, file)
		if headErr != nil {
			return nil, fmt.Sprintf("cannot read exact source dependency file %q; dependency campaign supersession is refused", file)
		}
		if baseErr != nil {
			if !isWorkflowPath(file) {
				return nil, fmt.Sprintf("cannot read exact base dependency file %q; dependency campaign supersession is refused", file)
			}
			// Prove absence separately. A failed show can also mean an unreadable
			// object/database, so it cannot by itself establish an added file.
			basePath, err := service.Ports.Git(ctx, entry.CanonicalDir, "ls-tree", "-r", "--name-only", strings.TrimSpace(base), "--", file)
			if err != nil || strings.TrimSpace(basePath) != "" {
				return nil, fmt.Sprintf("cannot prove workflow %q was newly added at the exact source base", file)
			}
			changes = append(changes, dependencyChange{path: file, added: true})
			continue
		}
		changed, err := dependencyContentChanged(file, baseContents, headContents)
		if err != nil {
			return nil, fmt.Sprintf("cannot compare dependency-bearing content in %q: %v", file, err)
		}
		if changed {
			changes = append(changes, dependencyChange{path: file})
		}
	}
	return changes, ""
}

func isWorkflowPath(file string) bool {
	file = path.Clean(strings.TrimSpace(file))
	return strings.HasPrefix(file, ".github/workflows/") && (strings.HasSuffix(file, ".yml") || strings.HasSuffix(file, ".yaml"))
}

func (service SupersessionService) validateWorkflowAdoptions(ctx context.Context, receipt SupersessionReceipt, entry SupersessionEntry, changes []dependencyChange) string {
	if !receipt.Approval.Trusted || strings.ToLower(strings.TrimSpace(receipt.Approval.Decision)) != "approved" || strings.TrimSpace(receipt.Approval.Actor) == "" || strings.TrimSpace(receipt.Approval.ReceiptID) == "" || receipt.Approval.ApprovedAt.IsZero() {
		return "workflow adoption requires a complete trusted-reviewer approval"
	}
	if len(receipt.WorkflowAdoptions) != len(changes) {
		return "workflow adoption evidence must cover every dependency-bearing source change and no others"
	}
	if service.Ports.IsAncestor == nil {
		return "workflow adoption cannot verify replacement ancestry in the exact target"
	}
	byPath := make(map[string]SupersessionWorkflowAdoption, len(receipt.WorkflowAdoptions))
	for _, adoption := range receipt.WorkflowAdoptions {
		if adoption.Path == "" || path.Clean(adoption.Path) != adoption.Path || !isWorkflowPath(adoption.Path) {
			return fmt.Sprintf("workflow adoption has an invalid added-workflow path %q", adoption.Path)
		}
		if _, exists := byPath[adoption.Path]; exists {
			return fmt.Sprintf("workflow adoption repeats path %q", adoption.Path)
		}
		if !worktreeproof.IsGitObjectID(adoption.ReplacementSHA) || len(adoption.SourceSHA256) != 64 {
			return fmt.Sprintf("workflow adoption for %q is missing exact source hash or replacement SHA", adoption.Path)
		}
		if _, err := hex.DecodeString(adoption.SourceSHA256); err != nil {
			return fmt.Sprintf("workflow adoption for %q has an invalid source SHA-256", adoption.Path)
		}
		if !adoption.Reviewed {
			return fmt.Sprintf("workflow adoption for %q is not reviewed", adoption.Path)
		}
		byPath[adoption.Path] = adoption
	}
	for _, change := range changes {
		if !change.added || !isWorkflowPath(change.path) {
			return fmt.Sprintf("dependency-bearing source change %q is not an added workflow eligible for adoption evidence", change.path)
		}
		adoption, ok := byPath[change.path]
		if !ok {
			return fmt.Sprintf("newly added workflow %q has no adoption evidence", change.path)
		}
		source, err := service.readGitFileBytes(ctx, entry.CanonicalDir, entry.HeadSHA, change.path)
		if err != nil {
			return fmt.Sprintf("cannot read exact source workflow %q: %v", change.path, err)
		}
		digest := sha256.Sum256(source)
		if hex.EncodeToString(digest[:]) != strings.ToLower(adoption.SourceSHA256) {
			return fmt.Sprintf("workflow adoption source hash does not match exact source file %q", change.path)
		}
		listed := false
		for _, replacement := range receipt.Replacements {
			if replacement.SHA == adoption.ReplacementSHA {
				listed = true
				break
			}
		}
		if !listed {
			return fmt.Sprintf("workflow adoption for %q names a commit absent from the replacement inventory", change.path)
		}
		landed, err := service.Ports.IsAncestor(ctx, entry.CanonicalDir, adoption.ReplacementSHA, entry.RemoteTargetSHA)
		if err != nil || !landed {
			return fmt.Sprintf("workflow adoption replacement %s is not verified in exact target", adoption.ReplacementSHA)
		}
		target, err := service.readGitFileBytes(ctx, entry.CanonicalDir, entry.RemoteTargetSHA, change.path)
		if err != nil {
			return fmt.Sprintf("workflow adoption cannot read exact target file %q: %v", change.path, err)
		}
		replacement, err := service.readGitFileBytes(ctx, entry.CanonicalDir, adoption.ReplacementSHA, change.path)
		if err != nil {
			return fmt.Sprintf("workflow adoption cannot read replacement file %q at %s: %v", change.path, adoption.ReplacementSHA, err)
		}
		if !bytes.Equal(source, replacement) || !bytes.Equal(source, target) {
			return fmt.Sprintf("workflow adoption file %q in replacement or exact target differs from the reviewed source bytes", change.path)
		}
	}
	return ""
}

func DependencyDeltasForValidation(receipt SupersessionReceipt, entry SupersessionEntry) ([]SupersessionDependencyDelta, string) {
	if !receipt.DependencyDeltasComplete {
		return nil, "dependency delta evidence is incomplete; terminal supersession is refused"
	}
	if len(receipt.DependencyDeltas) == 0 {
		return nil, "dependency PR supersession has no exact manifest/importer delta"
	}
	deltas := sortedDependencyDeltas(receipt.DependencyDeltas)
	for index, delta := range deltas {
		prefix := fmt.Sprintf("dependency delta %d", index+1)
		if delta.SourcePR != receipt.OriginalPR {
			return nil, fmt.Sprintf("%s source PR %q does not match original PR %q", prefix, delta.SourcePR, receipt.OriginalPR)
		}
		if delta.SourceHead != receipt.OriginalHead || delta.SourceHead != entry.HeadSHA {
			return nil, fmt.Sprintf("%s source head %q does not match exact original head %q (source PR may have been force-updated)", prefix, delta.SourceHead, entry.HeadSHA)
		}
		if delta.Consumer != entry.Repository {
			return nil, fmt.Sprintf("%s consumer %q does not match exact repository %q", prefix, delta.Consumer, entry.Repository)
		}
		for _, field := range []struct {
			name  string
			value string
		}{
			{name: "ecosystem", value: delta.Ecosystem},
			{name: "package", value: delta.Package},
			{name: "manifest", value: delta.Manifest},
			{name: "selector", value: delta.Selector},
			{name: "before", value: delta.Before},
			{name: "requested_after", value: delta.RequestedAfter},
			{name: "candidate_after", value: delta.CandidateAfter},
		} {
			if strings.TrimSpace(field.value) == "" {
				return nil, fmt.Sprintf("%s is missing %s proof", prefix, field.name)
			}
		}
		if !delta.Reviewed {
			return nil, fmt.Sprintf("%s is unreviewed", prefix)
		}
		if !DependencyVersionSatisfies(delta.Ecosystem, delta.CandidateAfter, delta.RequestedAfter) {
			return nil, fmt.Sprintf("%s candidate version %q does not satisfy requested %q", prefix, delta.CandidateAfter, delta.RequestedAfter)
		}
	}
	return deltas, ""
}

// ValidateDependencyDeltas exposes the same fail-closed dependency proof used
// by supersession cleanup to campaign/report integrations and their tests.
// Callers must provide the live SupersessionEntry, including authoritative PR data.
func (service SupersessionService) ValidateDependencyDeltas(ctx context.Context, receipt SupersessionReceipt, entry SupersessionEntry) error {
	if rejection := service.ValidateDependencyDeltasReason(ctx, receipt, entry); rejection != "" {
		return fmt.Errorf("%s", rejection)
	}
	return nil
}

func ValidateAuthoritativeSourcePullRequest(receipt SupersessionReceipt, entry SupersessionEntry) string {
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

func (service SupersessionService) DependencyCampaignWorktree(ctx context.Context, entry SupersessionEntry) bool {
	if strings.HasPrefix(entry.Task, "deps-") || strings.HasPrefix(entry.Branch, "wb/deps/") {
		return true
	}
	if entry.WorktreeDir == "" {
		return false
	}
	campaign, err := service.Ports.ReadCampaignMarker(entry.WorktreeDir)
	if err == nil && campaign {
		return true
	}
	changes, rejection := service.dependencyChanges(ctx, entry)
	return rejection != "" || len(changes) > 0
}

func (service SupersessionService) readGitFile(ctx context.Context, repository, revision, file string) ([]byte, error) {
	contents, err := service.Ports.Git(ctx, repository, "show", revision+":"+file)
	if err != nil {
		return nil, err
	}
	return []byte(contents), nil
}

func (service SupersessionService) readGitFileBytes(ctx context.Context, repository, revision, file string) ([]byte, error) {
	if service.Ports.ReadGitFileBytes == nil {
		return nil, fmt.Errorf("byte-preserving Git blob reader is unavailable")
	}
	return service.Ports.ReadGitFileBytes(ctx, repository, revision, file)
}

func dependencyContentChanged(file string, base, head []byte) (bool, error) {
	baseName := path.Base(file)
	switch {
	case baseName == "package.json":
		before, err := npmDependencySections(base)
		if err != nil {
			return false, err
		}
		after, err := npmDependencySections(head)
		if err != nil {
			return false, err
		}
		return !stringMapsEqual(before, after), nil
	case baseName == "go.mod":
		before, err := goDependencySections(base)
		if err != nil {
			return false, err
		}
		after, err := goDependencySections(head)
		if err != nil {
			return false, err
		}
		return !stringMapsEqual(before, after), nil
	case baseName == "go.work":
		before, err := goWorkspaceSections(base)
		if err != nil {
			return false, err
		}
		after, err := goWorkspaceSections(head)
		if err != nil {
			return false, err
		}
		return !stringMapsEqual(before, after), nil
	case baseName == "go.sum" || baseName == "go.work.sum" || baseName == "package-lock.json" || baseName == "npm-shrinkwrap.json" || baseName == "pnpm-lock.yaml" || baseName == "yarn.lock":
		// Lockfiles are resolved dependency evidence. Any source-side edit is
		// kept behind the dependency receipt proof, even if its format changes.
		return !bytes.Equal(base, head), nil
	case baseName == "pnpm-workspace.yaml" || baseName == "pnpm-workspace.yml":
		// Workspace membership and catalog declarations affect package
		// resolution, so source-side edits require the same evidence.
		return !bytes.Equal(base, head), nil
	case strings.HasPrefix(file, ".github/workflows/"):
		before, err := workflowActionReferences(base)
		if err != nil {
			return false, err
		}
		after, err := workflowActionReferences(head)
		if err != nil {
			return false, err
		}
		return !stringSlicesEqual(before, after), nil
	default:
		return false, nil
	}
}

func npmDependencySections(contents []byte) (map[string]string, error) {
	var manifest map[string]json.RawMessage
	if err := json.Unmarshal(contents, &manifest); err != nil {
		return nil, err
	}
	result := make(map[string]string)
	for _, section := range []string{"dependencies", "devDependencies", "peerDependencies", "optionalDependencies"} {
		var dependencies map[string]string
		if raw := manifest[section]; len(raw) > 0 {
			if err := json.Unmarshal(raw, &dependencies); err != nil {
				return nil, fmt.Errorf("parse %s: %w", section, err)
			}
		}
		for name, version := range dependencies {
			result[section+":"+name] = version
		}
	}
	for _, section := range []string{"overrides", "resolutions", "peerDependenciesMeta", "bundledDependencies", "bundleDependencies",
		"dependenciesMeta", "workspaces", "packageManager", "engines", "os", "cpu", "libc", "installConfig", "catalogs"} {
		raw := manifest[section]
		if len(raw) == 0 {
			continue
		}
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, fmt.Errorf("parse %s: %w", section, err)
		}
		canonical, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("canonicalize %s: %w", section, err)
		}
		result[section] = string(canonical)
	}
	return result, nil
}

func goWorkspaceSections(contents []byte) (map[string]string, error) {
	parsed, err := modfile.ParseWork("go.work", contents, nil)
	if err != nil {
		return nil, err
	}
	result := make(map[string]string)
	if parsed.Go != nil {
		result["go"] = parsed.Go.Version
	}
	if parsed.Toolchain != nil {
		result["toolchain"] = parsed.Toolchain.Name
	}
	for _, use := range parsed.Use {
		result["use:"+use.Path] = use.ModulePath
	}
	for _, replacement := range parsed.Replace {
		old := replacement.Old.Path + "@" + replacement.Old.Version
		newVersion := replacement.New.Path + "@" + replacement.New.Version
		result["replace:"+old] = newVersion
	}
	return result, nil
}

func goDependencySections(contents []byte) (map[string]string, error) {
	parsed, err := modfile.Parse("go.mod", contents, nil)
	if err != nil {
		return nil, err
	}
	result := make(map[string]string)
	for _, requirement := range parsed.Require {
		result["require:"+requirement.Mod.Path] = requirement.Mod.Version + "|indirect=" + fmt.Sprint(requirement.Indirect)
	}
	for _, replacement := range parsed.Replace {
		old := replacement.Old.Path + "@" + replacement.Old.Version
		newVersion := replacement.New.Path + "@" + replacement.New.Version
		result["replace:"+old] = newVersion
	}
	for _, exclusion := range parsed.Exclude {
		result["exclude:"+exclusion.Mod.Path] = exclusion.Mod.Version
	}
	if parsed.Go != nil {
		result["go"] = parsed.Go.Version
	}
	if parsed.Toolchain != nil {
		result["toolchain"] = parsed.Toolchain.Name
	}
	return result, nil
}

func workflowActionReferences(contents []byte) ([]string, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(contents, &root); err != nil {
		return nil, err
	}
	var references []string
	var visit func(*yaml.Node) error
	visit = func(node *yaml.Node) error {
		if node.Kind == yaml.MappingNode {
			var uses *yaml.Node
			var with *yaml.Node
			for index := 0; index+1 < len(node.Content); index += 2 {
				key, value := node.Content[index], node.Content[index+1]
				if key.Kind == yaml.ScalarNode {
					switch key.Value {
					case "uses":
						if uses != nil {
							return fmt.Errorf("workflow action step has duplicate uses keys")
						}
						uses = value
					case "with":
						with = value
					}
				}
				if err := visit(value); err != nil {
					return err
				}
			}
			if uses != nil {
				if uses.Kind != yaml.ScalarNode || strings.TrimSpace(uses.Value) == "" {
					return fmt.Errorf("workflow action reference is not a non-empty scalar")
				}
				action := map[string]any{"uses": strings.TrimSpace(uses.Value)}
				if with != nil {
					canonicalWith, err := canonicalYAMLNode(with)
					if err != nil {
						return fmt.Errorf("parse workflow action inputs: %w", err)
					}
					action["with"] = canonicalWith
				}
				encoded, err := json.Marshal(action)
				if err != nil {
					return fmt.Errorf("canonicalize workflow action inputs: %w", err)
				}
				references = append(references, string(encoded))
			}
			return nil
		}
		for _, child := range node.Content {
			if err := visit(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(&root); err != nil {
		return nil, err
	}
	sort.Strings(references)
	return references, nil
}

func canonicalYAMLNode(node *yaml.Node) (any, error) {
	switch node.Kind {
	case yaml.DocumentNode:
		if len(node.Content) != 1 {
			return nil, fmt.Errorf("document does not contain exactly one value")
		}
		return canonicalYAMLNode(node.Content[0])
	case yaml.MappingNode:
		if len(node.Content)%2 != 0 {
			return nil, fmt.Errorf("mapping has an incomplete key/value pair")
		}
		result := make(map[string]any, len(node.Content)/2)
		for index := 0; index+1 < len(node.Content); index += 2 {
			key := node.Content[index]
			if key.Kind != yaml.ScalarNode {
				return nil, fmt.Errorf("mapping key is not scalar")
			}
			if _, duplicate := result[key.Value]; duplicate {
				return nil, fmt.Errorf("mapping has duplicate key %q", key.Value)
			}
			value, err := canonicalYAMLNode(node.Content[index+1])
			if err != nil {
				return nil, err
			}
			result[key.Value] = value
		}
		return result, nil
	case yaml.SequenceNode:
		result := make([]any, 0, len(node.Content))
		for _, child := range node.Content {
			value, err := canonicalYAMLNode(child)
			if err != nil {
				return nil, err
			}
			result = append(result, value)
		}
		return result, nil
	case yaml.ScalarNode:
		return map[string]string{"tag": node.Tag, "value": node.Value}, nil
	default:
		return nil, fmt.Errorf("unsupported YAML node kind %d", node.Kind)
	}
}

func stringMapsEqual(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if other, ok := right[key]; !ok || other != value {
			return false
		}
	}
	return true
}

func stringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func IsDependencyManifestOrImporter(file string) bool {
	file = path.Clean(strings.TrimSpace(file))
	base := path.Base(file)
	switch base {
	case "package.json", "package-lock.json", "npm-shrinkwrap.json", "pnpm-lock.yaml", "pnpm-workspace.yaml", "pnpm-workspace.yml", "yarn.lock", "go.mod", "go.work", "go.sum", "go.work.sum":
		return true
	}
	return strings.HasPrefix(file, ".github/workflows/") && (strings.HasSuffix(file, ".yml") || strings.HasSuffix(file, ".yaml"))
}

func (service SupersessionService) DependencyLockfile(ctx context.Context, canonical, target string, delta SupersessionDependencyDelta) (string, bool, error) {
	contents, err := service.Ports.Git(ctx, canonical, "ls-tree", "-r", "--name-only", target)
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
		if manifestDir != dir && dir != "" && !strings.HasPrefix(manifestDir, dir+"/") {
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

func SelectorNamesExactPackage(selector, packageName string) bool {
	if selector == packageName {
		return packageName != ""
	}
	for _, lockfile := range []string{"package-lock.json", "pnpm-lock.yaml"} {
		if _, ok := ParseLockfileSelector("npm", lockfile, selector, packageName); ok {
			return true
		}
	}
	return false
}

func LockfileEntryContainsVersion(ecosystem, lockfilePath, contents, selector, version string) bool {
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
	packageName := SelectorPackageFromLockfileSelector(selector)
	segments, ok := ParseLockfileSelector(ecosystem, lockfilePath, selector, packageName)
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

func SelectorPackageFromLockfileSelector(selector string) string {
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

// ParseLockfileSelector accepts only the selectors emitted by the dependency
// campaign report. Package identity is parsed as a token, never searched as a
// substring, so nx cannot be proven by nxfoo or @nx/js.
func ParseLockfileSelector(ecosystem, lockfilePath, selector, packageName string) ([]string, bool) {
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

// DependencyVersionSatisfies applies the version semantics of the supported
// ecosystem instead of treating a requested range as a literal string. npm
// ranges commonly arrive as ^ or ~ constraints; Go module requirements remain
// exact versions here. Unknown syntax fails closed.
func DependencyVersionSatisfies(ecosystem, candidate, requested string) bool {
	candidate = NormalizeDependencyVersion(candidate)
	requested = strings.TrimSpace(requested)
	if requested == "" || candidate == "" {
		return false
	}
	if strings.EqualFold(ecosystem, "go") {
		return semver.IsValid(candidate) && semver.IsValid(NormalizeDependencyVersion(requested)) && candidate == NormalizeDependencyVersion(requested)
	}
	if !strings.EqualFold(ecosystem, "npm") {
		return candidate == NormalizeDependencyVersion(requested)
	}
	for _, alternative := range strings.Split(requested, "||") {
		if NpmRangeAlternativeSatisfies(candidate, strings.TrimSpace(alternative)) {
			return true
		}
	}
	return false
}

func NormalizeDependencyVersion(value string) string {
	value = strings.TrimSpace(value)
	if value != "" && value[0] != 'v' {
		return "v" + value
	}
	return value
}

func NpmRangeAlternativeSatisfies(candidate, requested string) bool {
	if semver.IsValid(candidate) && semver.IsValid(NormalizeDependencyVersion(requested)) {
		return semver.Compare(candidate, NormalizeDependencyVersion(requested)) == 0
	}
	if strings.HasPrefix(requested, "^") || strings.HasPrefix(requested, "~") {
		operator, base := requested[:1], NormalizeDependencyVersion(requested[1:])
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
				upper = "v" + fmt.Sprintf("%d.0.0", MustAtoi(parts[0])+1)
			case parts[1] != "0":
				upper = "v0." + fmt.Sprintf("%d.0", MustAtoi(parts[1])+1)
			default:
				upper = "v0.0." + fmt.Sprintf("%d", MustAtoi(parts[2])+1)
			}
		} else {
			upper = "v" + parts[0] + "." + fmt.Sprintf("%d.0", MustAtoi(parts[1])+1)
		}
		return semver.Compare(candidate, upper) < 0
	}
	constraints := strings.Fields(requested)
	if len(constraints) > 1 {
		for _, constraint := range constraints {
			if !NpmComparatorSatisfies(candidate, constraint) {
				return false
			}
		}
		return true
	}
	return NpmComparatorSatisfies(candidate, requested)
}

func NpmComparatorSatisfies(candidate, constraint string) bool {
	operator := "="
	for _, candidateOperator := range []string{"<=", ">=", "<", ">", "="} {
		if strings.HasPrefix(constraint, candidateOperator) {
			operator = candidateOperator
			constraint = strings.TrimSpace(strings.TrimPrefix(constraint, candidateOperator))
			break
		}
	}
	if strings.ContainsAny(constraint, "xX*") {
		parts := strings.Split(strings.TrimPrefix(NormalizeDependencyVersion(constraint), "v"), ".")
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
	version := NormalizeDependencyVersion(constraint)
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

func MustAtoi(value string) int {
	var result int
	for _, digit := range value {
		result = result*10 + int(digit-'0')
	}
	return result
}

type npmDependencyManifest struct {
	Dependencies         map[string]string `json:"dependencies"`
	DevDependencies      map[string]string `json:"devDependencies"`
	PeerDependencies     map[string]string `json:"peerDependencies"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
}

func (manifest npmDependencyManifest) directDependencies(field string) (map[string]string, bool) {
	switch field {
	case "dependencies":
		return manifest.Dependencies, true
	case "devDependencies":
		return manifest.DevDependencies, true
	case "peerDependencies":
		return manifest.PeerDependencies, true
	case "optionalDependencies":
		return manifest.OptionalDependencies, true
	default:
		return nil, false
	}
}

// directDependencyEvidence parses a manifest once and retains every Go
// requirement for the named module. The validator checks every value, while
// the reporting accessor preserves its historical first-value result.
func directDependencyEvidence(delta SupersessionDependencyDelta, contents []byte) (values []string, rejection string, parseErr error) {
	switch strings.ToLower(strings.TrimSpace(delta.Ecosystem)) {
	case "npm":
		var manifest npmDependencyManifest
		if err := json.Unmarshal(contents, &manifest); err != nil {
			return nil, fmt.Sprintf("cannot parse npm manifest %q: %v", delta.Manifest, err), err
		}
		parts := strings.Split(delta.Selector, ".")
		if len(parts) != 2 || parts[0] == "" || parts[1] != delta.Package {
			return nil, fmt.Sprintf("npm selector %q is not the exact direct package selector for %q", delta.Selector, delta.Package), nil
		}
		dependencies, direct := manifest.directDependencies(parts[0])
		if !direct {
			return nil, fmt.Sprintf("npm selector %q is not a direct dependency field", delta.Selector), nil
		}
		value, ok := dependencies[delta.Package]
		if !ok {
			return nil, fmt.Sprintf("npm direct package %q is absent from selector %q", delta.Package, delta.Selector), nil
		}
		return []string{value}, "", nil
	case "go":
		parsed, err := modfile.Parse(delta.Manifest, contents, nil)
		if err != nil {
			return nil, fmt.Sprintf("cannot parse Go manifest %q: %v", delta.Manifest, err), err
		}
		if delta.Selector != "require:"+delta.Package {
			return nil, fmt.Sprintf("Go selector %q is not the exact direct require selector for %q", delta.Selector, delta.Package), nil
		}
		for _, requirement := range parsed.Require {
			if requirement.Mod.Path == delta.Package {
				values = append(values, requirement.Mod.Version)
			}
		}
		if len(values) == 0 {
			return nil, fmt.Sprintf("Go direct module %q is absent", delta.Package), nil
		}
		return values, "", nil
	default:
		err := fmt.Errorf("unsupported dependency ecosystem %q", delta.Ecosystem)
		return nil, err.Error(), err
	}
}

func validateObservedDependencyVersions(delta SupersessionDependencyDelta, values []string, expectedVersion string, exact bool) string {
	for _, value := range values {
		if exact && value == expectedVersion || !exact && DependencyVersionSatisfies(delta.Ecosystem, value, expectedVersion) {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(delta.Ecosystem), "npm") {
			return fmt.Sprintf("npm direct package %q at %q is %q, want %q", delta.Package, delta.Selector, value, expectedVersion)
		}
		return fmt.Sprintf("Go direct module %q is %q, want %q", delta.Package, value, expectedVersion)
	}
	return ""
}

func ValidateDependencyManifest(delta SupersessionDependencyDelta, contents []byte, expectedVersion string, exact bool) string {
	values, rejection, _ := directDependencyEvidence(delta, contents)
	if rejection != "" {
		return rejection
	}
	return validateObservedDependencyVersions(delta, values, expectedVersion, exact)
}

func DependencyManifestValue(delta SupersessionDependencyDelta, contents []byte) (string, bool, error) {
	values, _, err := directDependencyEvidence(delta, contents)
	if err != nil || len(values) == 0 {
		return "", false, err
	}
	return values[0], true, nil
}
