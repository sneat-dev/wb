// Package graduation composes independently produced WB and deployment
// evidence into one strict, reviewable graduation receipt. It never turns a
// hand-written status field into a green release decision.
package graduation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/worktrees"
)

const SchemaVersion = 1

const (
	RemoteTargetProducer = "wb.verify.receipt.remote-target.v1"
	DeploymentProducer   = "github-actions.deployment-receipt.v1"
)

var (
	repositoryName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*$`)
	gitRevision    = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	providerRunID  = regexp.MustCompile(`^[1-9][0-9]*$`)
	githubSCPURL   = regexp.MustCompile(`^git@github\.com:([A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*)(?:\.git)?$`)
)

// VerificationIndex is exactly the JSON envelope emitted by `wb check
// --profile ci --format json`, copied here because the command package owns
// its renderer. It deliberately preserves the public quality report types.
type VerificationIndex struct {
	SchemaVersion int                          `json:"schema_version"`
	GeneratedAt   time.Time                    `json:"generated_at"`
	Profile       string                       `json:"profile,omitempty"`
	Checks        []quality.Check              `json:"checks"`
	Repositories  []quality.VerificationReport `json:"repositories"`
}

// CIWaitReceipt is exactly the JSON envelope emitted by `wb ci wait --json`.
type CIWaitReceipt struct {
	SchemaVersion int       `json:"schema_version"`
	ObservedAt    time.Time `json:"observed_at"`
	orchestrate.PullRequestWaitResult
}

// RemoteTargetEvidence can only be emitted by `wb verify receipt
// remote-target`. The captured git-ls-remote payload and digest make the
// observed remote ref independently inspectable rather than an assertion.
type RemoteTargetEvidence struct {
	SchemaVersion        int       `json:"schema_version"`
	Producer             string    `json:"producer"`
	Repository           string    `json:"repository"`
	Remote               string    `json:"remote"`
	RemoteURL            string    `json:"remote_url"`
	TargetRef            string    `json:"target_ref"`
	Revision             string    `json:"revision"`
	ObservedAt           time.Time `json:"observed_at"`
	ObservedOutput       string    `json:"observed_output"`
	ObservedOutputSHA256 string    `json:"observed_output_sha256"`
}

// DeployedRevisionEvidence is the closed GitHub Actions deployment receipt.
// It binds both revision and immutable numeric Actions run identity through
// declared pointers into the exact retained provider payload.
type DeployedRevisionEvidence struct {
	SchemaVersion       int       `json:"schema_version"`
	Producer            string    `json:"producer"`
	Provider            string    `json:"provider"`
	Repository          string    `json:"repository"`
	RunURL              string    `json:"run_url"`
	ProviderRunID       string    `json:"provider_run_id"`
	RunIDJSONPointer    string    `json:"run_id_json_pointer"`
	Revision            string    `json:"revision"`
	RevisionJSONPointer string    `json:"revision_json_pointer"`
	ObservedAt          time.Time `json:"observed_at"`
	PayloadJSON         string    `json:"payload_json"`
	PayloadSHA256       string    `json:"payload_sha256"`
}

// TerminalCleanupEvidence is the persistent JSON report written by `wb
// worktree cleanup <task> --apply --remote`. It names only feature/integration
// worktrees; a canonical target checkout is intentionally not deleted.
type TerminalCleanupEvidence struct {
	GeneratedAt  time.Time                          `json:"generated_at"`
	Phase        string                             `json:"phase"`
	Task         string                             `json:"task,omitempty"`
	Filter       string                             `json:"filter,omitempty"`
	AllMerged    bool                               `json:"all_merged"`
	Apply        bool                               `json:"apply"`
	DeleteRemote bool                               `json:"delete_remote"`
	OlderThan    string                             `json:"older_than"`
	Results      []worktrees.CleanupResult          `json:"results"`
	Diagnostics  []worktrees.ListDiagnostic         `json:"diagnostics,omitempty"`
	Artifacts    []worktrees.LifecycleArtifact      `json:"artifacts,omitempty"`
	Recovery     *worktrees.InterruptedLockRecovery `json:"recovery,omitempty"`
}

type Component[T any] struct {
	SHA256     string    `json:"sha256"`
	ObservedAt time.Time `json:"observed_at"`
	Evidence   T         `json:"evidence"`
}

type LocalCIComponent struct {
	LocalCheck Component[VerificationIndex] `json:"local_check"`
	CIWait     Component[CIWaitReceipt]     `json:"ci_wait"`
}

// Receipt retains the immutable source digest and observation time for every
// supplied producer document, so review can independently retrieve and hash
// each component.
type Receipt struct {
	SchemaVersion    int                                 `json:"schema_version"`
	Repository       string                              `json:"repository"`
	Revision         string                              `json:"revision"`
	CreatedAt        time.Time                           `json:"created_at"`
	LocalCI          LocalCIComponent                    `json:"local_ci"`
	RemoteTarget     Component[RemoteTargetEvidence]     `json:"remote_target"`
	DeployedRevision Component[DeployedRevisionEvidence] `json:"deployed_revision"`
	TerminalCleanup  Component[TerminalCleanupEvidence]  `json:"terminal_cleanup"`
}

type Inputs struct {
	LocalCheck             VerificationIndex
	LocalCheckSHA256       string
	LocalCheckObservedAt   time.Time
	CIWait                 CIWaitReceipt
	CIWaitSHA256           string
	CIWaitObservedAt       time.Time
	RemoteTarget           RemoteTargetEvidence
	RemoteTargetSHA256     string
	RemoteTargetObservedAt time.Time
	DeployedRevision       DeployedRevisionEvidence
	DeployedSHA256         string
	DeployedObservedAt     time.Time
	TerminalCleanup        TerminalCleanupEvidence
	CleanupSHA256          string
	CleanupObservedAt      time.Time
}

func DecodeVerificationIndex(raw []byte) (VerificationIndex, error) {
	var value VerificationIndex
	return value, decode(raw, &value)
}

func DecodeCIWaitReceipt(raw []byte) (CIWaitReceipt, error) {
	var value CIWaitReceipt
	return value, decode(raw, &value)
}

func DecodeRemoteTarget(raw []byte) (RemoteTargetEvidence, error) {
	var value RemoteTargetEvidence
	return value, decode(raw, &value)
}

func DecodeDeployedRevision(raw []byte) (DeployedRevisionEvidence, error) {
	var value DeployedRevisionEvidence
	return value, decode(raw, &value)
}

func DecodeTerminalCleanup(raw []byte) (TerminalCleanupEvidence, error) {
	var value TerminalCleanupEvidence
	return value, decode(raw, &value)
}

func decode(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("unexpected trailing JSON value")
		}
		return err
	}
	return nil
}

func Digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func Compose(inputs Inputs, now time.Time) (Receipt, error) {
	if now.IsZero() {
		return Receipt{}, fmt.Errorf("receipt creation time is required")
	}
	if err := validateLocalCheck(inputs.LocalCheck); err != nil {
		return Receipt{}, fmt.Errorf("local WB check evidence: %w", err)
	}
	repository := localCheckRepository(inputs.LocalCheck)
	if err := validateCIWait(inputs.CIWait, repository); err != nil {
		return Receipt{}, fmt.Errorf("WB CI wait evidence: %w", err)
	}
	revision := inputs.CIWait.Head
	if inputs.LocalCheck.Repositories[0].Revision != revision {
		return Receipt{}, fmt.Errorf("local WB check evidence: clean checked revision %q conflicts with CI head %q", inputs.LocalCheck.Repositories[0].Revision, revision)
	}
	if err := validateRemoteTarget(inputs.RemoteTarget, repository, inputs.CIWait.Target, revision); err != nil {
		return Receipt{}, fmt.Errorf("remote-target evidence: %w", err)
	}
	if err := validateDeployedRevision(inputs.DeployedRevision, repository, revision); err != nil {
		return Receipt{}, fmt.Errorf("deployed-revision evidence: %w", err)
	}
	if err := validateTerminalCleanup(inputs.TerminalCleanup, repository, inputs.CIWait.Target, revision); err != nil {
		return Receipt{}, fmt.Errorf("terminal-cleanup evidence: %w", err)
	}
	for name, digest := range map[string]string{
		"local WB check": inputs.LocalCheckSHA256, "WB CI wait": inputs.CIWaitSHA256,
		"remote-target": inputs.RemoteTargetSHA256, "deployed-revision": inputs.DeployedSHA256,
		"terminal-cleanup": inputs.CleanupSHA256,
	} {
		if !validDigest(digest) {
			return Receipt{}, fmt.Errorf("%s source digest is not a sha256 digest", name)
		}
	}
	if err := validateTimes(now, inputs); err != nil {
		return Receipt{}, err
	}
	return Receipt{
		SchemaVersion: SchemaVersion, Repository: repository, Revision: revision, CreatedAt: now.UTC(),
		LocalCI: LocalCIComponent{
			LocalCheck: Component[VerificationIndex]{SHA256: inputs.LocalCheckSHA256, ObservedAt: inputs.LocalCheckObservedAt.UTC(), Evidence: inputs.LocalCheck},
			CIWait:     Component[CIWaitReceipt]{SHA256: inputs.CIWaitSHA256, ObservedAt: inputs.CIWaitObservedAt.UTC(), Evidence: inputs.CIWait},
		},
		RemoteTarget:     Component[RemoteTargetEvidence]{SHA256: inputs.RemoteTargetSHA256, ObservedAt: inputs.RemoteTargetObservedAt.UTC(), Evidence: inputs.RemoteTarget},
		DeployedRevision: Component[DeployedRevisionEvidence]{SHA256: inputs.DeployedSHA256, ObservedAt: inputs.DeployedObservedAt.UTC(), Evidence: inputs.DeployedRevision},
		TerminalCleanup:  Component[TerminalCleanupEvidence]{SHA256: inputs.CleanupSHA256, ObservedAt: inputs.CleanupObservedAt.UTC(), Evidence: inputs.TerminalCleanup},
	}, nil
}

func validateLocalCheck(evidence VerificationIndex) error {
	if evidence.SchemaVersion != SchemaVersion || evidence.GeneratedAt.IsZero() || evidence.Profile != "ci" || len(evidence.Repositories) != 1 {
		return fmt.Errorf("must be a one-repository schema v1 wb check --profile ci JSON report")
	}
	for _, report := range evidence.Repositories {
		if !repositoryName.MatchString(report.Repository) || report.Status != quality.StatusPassed || !report.WorkspaceClean || !gitRevision.MatchString(report.Revision) {
			return fmt.Errorf("WB check repository must have owner/repository identity, passed status, and one exact clean Git revision")
		}
		passed := map[quality.Check]bool{}
		for _, result := range report.Results {
			if result.Status == quality.StatusFailed {
				return fmt.Errorf("WB check result %q failed", result.Check)
			}
			if result.Status == quality.StatusPassed {
				passed[result.Check] = true
			}
		}
		for _, required := range []quality.Check{quality.CheckLint, quality.CheckTest, quality.CheckBuild} {
			if !passed[required] {
				return fmt.Errorf("WB check lacks passed %s mechanism", required)
			}
		}
	}
	return nil
}

func localCheckRepository(evidence VerificationIndex) string {
	return evidence.Repositories[0].Repository
}

func validateCIWait(evidence CIWaitReceipt, repository string) error {
	result := evidence.PullRequestWaitResult
	if evidence.SchemaVersion != SchemaVersion || evidence.ObservedAt.IsZero() || result.Status != orchestrate.PullRequestWaitPassed || result.Repository != repository || result.PullRequest != "" || !gitRevision.MatchString(result.Head) || result.ObservedHead != result.Head || result.ObservedTargetHead != result.Head || !result.CandidateContainsTarget || result.StableObservations < 2 || !nonBlank(result.RequiredChecksAuthority) || !nonBlank(result.Target) || len(result.Checks) == 0 {
		return fmt.Errorf("must be a schema v1 passed wb ci wait JSON receipt for the exact observed head")
	}
	passed := false
	for _, check := range result.Checks {
		if !nonBlank(check.Name) || (check.Bucket != "pass" && check.Bucket != "skipping") {
			return fmt.Errorf("WB CI wait evidence contains a non-terminal or failed check")
		}
		passed = passed || check.Bucket == "pass"
	}
	if !passed {
		return fmt.Errorf("WB CI wait evidence contains no passed CI mechanism")
	}
	return nil
}

func validateRemoteTarget(evidence RemoteTargetEvidence, repository, target, revision string) error {
	if evidence.SchemaVersion != SchemaVersion || evidence.Producer != RemoteTargetProducer || evidence.Repository != repository || evidence.Revision != revision || !gitRevision.MatchString(evidence.Revision) || evidence.TargetRef != "refs/heads/"+target || !nonBlank(evidence.Remote) || !nonBlank(evidence.RemoteURL) || evidence.ObservedAt.IsZero() {
		return fmt.Errorf("must be a complete fixed-shape WB remote-target receipt for the CI head")
	}
	if evidence.ObservedOutput != evidence.Revision+"\t"+evidence.TargetRef+"\n" || evidence.ObservedOutputSHA256 != Digest([]byte(evidence.ObservedOutput)) {
		return fmt.Errorf("git ls-remote payload or digest conflicts with target identity")
	}
	if err := ValidateGitHubRepositoryURL(repository, evidence.RemoteURL); err != nil {
		return err
	}
	return nil
}

func validateDeployedRevision(evidence DeployedRevisionEvidence, repository, revision string) error {
	payloadBytes := []byte(evidence.PayloadJSON)
	if evidence.SchemaVersion != SchemaVersion || evidence.Producer != DeploymentProducer || evidence.Provider != "github-actions" || evidence.Repository != repository || evidence.Revision != revision || !gitRevision.MatchString(evidence.Revision) || !providerRunID.MatchString(evidence.ProviderRunID) || evidence.ObservedAt.IsZero() || len(payloadBytes) == 0 || evidence.PayloadSHA256 != Digest(payloadBytes) {
		return fmt.Errorf("must retain a closed GitHub Actions deployment payload for the CI head")
	}
	parsed, err := url.Parse(evidence.RunURL)
	wantRunPath := "/" + repository + "/actions/runs/" + evidence.ProviderRunID
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Hostname(), "github.com") || parsed.Port() != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" || parsed.Path != wantRunPath {
		return fmt.Errorf("deployment run_url must be the canonical immutable GitHub Actions run URL for provider_run_id")
	}
	payload, err := decodeProviderPayload(payloadBytes)
	if err != nil || len(payload) == 0 {
		return fmt.Errorf("deployment payload must be one non-empty structured JSON object")
	}
	payloadRevision, err := jsonPointerString(payload, evidence.RevisionJSONPointer)
	if err != nil || payloadRevision != revision {
		return fmt.Errorf("deployment payload revision pointer must resolve to the exact CI head")
	}
	payloadRunID, err := jsonPointerRunID(payload, evidence.RunIDJSONPointer)
	if err != nil || payloadRunID != evidence.ProviderRunID {
		return fmt.Errorf("deployment payload run ID pointer must resolve to the exact immutable GitHub Actions run")
	}
	return nil
}

func validateTerminalCleanup(evidence TerminalCleanupEvidence, repository, target, revision string) error {
	if evidence.Phase != "applied" || !evidence.AllMerged || !evidence.Apply || !evidence.DeleteRemote || !nonBlank(evidence.Task) || evidence.GeneratedAt.IsZero() || len(evidence.Results) == 0 || len(evidence.Diagnostics) != 0 {
		return fmt.Errorf("must be an applied wb worktree cleanup --remote report")
	}
	found := false
	seen := map[string]bool{}
	for _, result := range evidence.Results {
		if !repositoryName.MatchString(result.Repository) || seen[result.Repository] || result.Task != evidence.Task {
			return fmt.Errorf("cleanup report has an invalid, duplicate, or cross-task repository result")
		}
		seen[result.Repository] = true
		remoteAbsent := result.RemoteDeleted || result.RemoteHeadSHA == ""
		if !gitRevision.MatchString(result.HeadSHA) || !result.Eligible || !result.Clean || !result.Applied || !result.WorktreeGone || !result.BranchDeleted || !remoteAbsent || result.WorktreeDir == result.CanonicalDir || result.Branch == target {
			return fmt.Errorf("campaign worktree %s lacks terminal WB cleanup evidence", result.Repository)
		}
		if result.Repository == repository {
			found = true
			if !result.IntegratedAtOrigin || result.RemoteTargetSHA != revision {
				return fmt.Errorf("cleanup report does not bind %s to the exact remote target revision", repository)
			}
		}
	}
	if !found {
		return fmt.Errorf("cleanup report lacks repository %s", repository)
	}
	return nil
}

func decodeProviderPayload(raw []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("unexpected trailing JSON value")
		}
		return nil, err
	}
	return payload, nil
}

func jsonPointerValue(root any, pointer string) (any, error) {
	if pointer == "" || pointer[0] != '/' {
		return nil, fmt.Errorf("JSON pointer must be a non-root RFC 6901 pointer")
	}
	current := root
	for _, raw := range strings.Split(pointer[1:], "/") {
		for index := 0; index < len(raw); index++ {
			if raw[index] == '~' && (index+1 >= len(raw) || (raw[index+1] != '0' && raw[index+1] != '1')) {
				return nil, fmt.Errorf("JSON pointer contains an invalid escape")
			}
			if raw[index] == '~' {
				index++
			}
		}
		token := strings.ReplaceAll(strings.ReplaceAll(raw, "~1", "/"), "~0", "~")
		object, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("JSON pointer crosses a non-object")
		}
		current, ok = object[token]
		if !ok {
			return nil, fmt.Errorf("JSON pointer token %q is absent", token)
		}
	}
	return current, nil
}

func jsonPointerString(root any, pointer string) (string, error) {
	current, err := jsonPointerValue(root, pointer)
	if err != nil {
		return "", err
	}
	value, ok := current.(string)
	if !ok || !gitRevision.MatchString(value) {
		return "", fmt.Errorf("deployment revision pointer does not resolve to a Git revision string")
	}
	return value, nil
}

func jsonPointerRunID(root any, pointer string) (string, error) {
	current, err := jsonPointerValue(root, pointer)
	if err != nil {
		return "", err
	}
	var value string
	switch typed := current.(type) {
	case string:
		value = typed
	case json.Number:
		value = typed.String()
	default:
		return "", fmt.Errorf("deployment run ID pointer does not resolve to a decimal string or number")
	}
	if !providerRunID.MatchString(value) {
		return "", fmt.Errorf("deployment run ID pointer does not resolve to a canonical positive decimal run ID")
	}
	return value, nil
}

// ValidateGitHubRepositoryURL accepts only the canonical GitHub transport
// forms WB can independently bind to an owner/repository identity. It rejects
// aliases, ports, credentials, mutable URL components, and helper schemes.
func ValidateGitHubRepositoryURL(repository, raw string) error {
	if !repositoryName.MatchString(repository) || strings.TrimSpace(raw) != raw || raw == "" || strings.ContainsAny(raw, "\r\n") {
		return fmt.Errorf("remote URL or expected repository is invalid")
	}
	if match := githubSCPURL.FindStringSubmatch(raw); match != nil {
		if strings.TrimSuffix(match[1], ".git") != repository {
			return fmt.Errorf("remote URL does not identify expected repository %s", repository)
		}
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Port() != "" || !strings.EqualFold(parsed.Hostname(), "github.com") {
		return fmt.Errorf("remote URL must be one canonical GitHub HTTPS or SSH repository URL")
	}
	switch parsed.Scheme {
	case "https":
		if parsed.User != nil {
			return fmt.Errorf("remote URL must not embed credentials")
		}
	case "ssh":
		if parsed.User == nil || parsed.User.Username() != "git" {
			return fmt.Errorf("GitHub SSH remote URL must use the git user")
		}
		if _, hasPassword := parsed.User.Password(); hasPassword {
			return fmt.Errorf("remote URL must not embed credentials")
		}
	default:
		return fmt.Errorf("remote URL must use canonical GitHub HTTPS or SSH transport")
	}
	path := strings.TrimSuffix(parsed.Path, ".git")
	if path != "/"+repository {
		return fmt.Errorf("remote URL does not identify expected repository %s", repository)
	}
	return nil
}

func validateTimes(now time.Time, inputs Inputs) error {
	if !inputs.LocalCheckObservedAt.Equal(inputs.LocalCheck.GeneratedAt) ||
		!inputs.CIWaitObservedAt.Equal(inputs.CIWait.ObservedAt) ||
		!inputs.RemoteTargetObservedAt.Equal(inputs.RemoteTarget.ObservedAt) ||
		!inputs.DeployedObservedAt.Equal(inputs.DeployedRevision.ObservedAt) ||
		!inputs.CleanupObservedAt.Equal(inputs.TerminalCleanup.GeneratedAt) {
		return fmt.Errorf("component observation times must come from their closed producer envelopes")
	}
	values := []struct {
		name string
		at   time.Time
	}{
		{"local WB check file", inputs.LocalCheckObservedAt}, {"WB CI wait file", inputs.CIWaitObservedAt},
		{"remote-target file", inputs.RemoteTargetObservedAt}, {"remote-target observation", inputs.RemoteTarget.ObservedAt},
		{"deployment file", inputs.DeployedObservedAt}, {"deployment observation", inputs.DeployedRevision.ObservedAt},
		{"cleanup file", inputs.CleanupObservedAt}, {"cleanup report", inputs.TerminalCleanup.GeneratedAt},
	}
	for _, value := range values {
		if value.at.IsZero() || value.at.After(now) {
			return fmt.Errorf("%s timestamp must be non-zero and not in the future", value.name)
		}
	}
	latestLocalCI := inputs.LocalCheck.GeneratedAt
	if inputs.CIWaitObservedAt.After(latestLocalCI) {
		latestLocalCI = inputs.CIWaitObservedAt
	}
	if inputs.RemoteTarget.ObservedAt.Before(latestLocalCI) || inputs.DeployedRevision.ObservedAt.Before(inputs.RemoteTarget.ObservedAt) || inputs.TerminalCleanup.GeneratedAt.Before(inputs.DeployedRevision.ObservedAt) {
		return fmt.Errorf("graduation evidence timestamps must order local/CI, remote-target, deployed revision, then terminal cleanup")
	}
	return nil
}

func validDigest(value string) bool {
	const prefix = "sha256:"
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(value, prefix))
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == strings.TrimPrefix(value, prefix)
}

func nonBlank(value string) bool {
	return strings.TrimSpace(value) != "" && !strings.ContainsAny(value, "\r\n")
}
