package worktrees

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
	"golang.org/x/sys/unix"
)

const (
	workLogProjectionDirectory  = ".wb-worklog"
	workLogProjectionName       = "recovery.json"
	workLogProjectionExclude    = "/.wb-worklog/"
	worktreeInstructionsName    = ".worktree.md"
	worktreeInstructionsExclude = "/.worktree.md"
	legacyWorkLogProjectionName = ".wb-worklog.json"
)

const worktreeInstructions = `<!-- wb-managed-worktree -->
# WB managed worktree

Keep this checkout on its WB feature branch. Do not switch the canonical clone
or use raw Git worktree cleanup commands.

For an agent-driven task, register the live harness before the first mutation:

` + "```sh" + `
wb session register --pid $PPID --runtime codex --model <exact-model>
` + "```" + `

Use $PPID from the harness tool-call shell, never $$ (an intermediate
shell). Intentional human CLI work must use --mode manual --initiator <human>.

When the work is clean, validated, and ready to merge without a conflict or
behavioral judgment, use the normal completion command:

` + "```sh" + `
wb worktree merge . --route auto --cleanup --format json
` + "```" + `

Pass multiple worktree paths to land a compatible batch. Use ` + "`merge prepare`" + `
when other agents need the immutable candidate SHA before remote checks finish,
then use the receipt's exact ` + "`merge land`" + ` or ` + "`merge resume`" + ` command. Auto
landing rebases an unpublished candidate over clean target drift and stops on a
conflict. A landed failure can use ` + "`merge revert`" + ` to create a forward inverse
candidate; never reset or force-push shared history. For a forward fix after
post-target CI fails, commit the additive repair on this preserved source and
rerun ` + "`merge prepare`" + `; WB records the failed landing and retains the lane.
`

var errWorkLogProjectionNotFound = errors.New("work-log projection not found")

var executionIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/+-]{0,127}$`)

const (
	modelProvenanceRuntimeObserved = "runtime_observed"
	modelProvenanceCallerDeclared  = "caller_declared"
	modelProvenanceUnknown         = "unknown"
)

// WorkLogOptions is transport-neutral. The exact prompt is private local data;
// only opaque IDs and bounded Git evidence enter the projection/outbox.
type WorkLogOptions struct {
	EffortID     string
	RunID        string
	Initiator    string
	AgentID      string
	AgentRuntime string
	Model        string
	CLI          string
	Provider     string
	// WBSessionID links this claim to the live registered session that created
	// it. Normal callers leave it empty and the current resolver supplies it.
	WBSessionID           string
	OriginalPrompt        string // readable local file, copied to the private archive
	RequireOriginalPrompt bool   // public create/recycle commands require exact local recovery input
	// AcquiredVia records how this claim came to exist when it is not an
	// ordinary `wb worktree create`. "adopted" marks a claim written for a
	// pre-WB worktree by `wb worktree adopt`, so the claim itself — not just
	// the manifest beside it — says the identity was reconstructed rather than
	// created. Empty for a normal create.
	AcquiredVia string

	// originalPromptContents is an immutable preflight snapshot. Keeping it in
	// the options passed through one create/recycle call closes the usual
	// stat/read/use race: recordWorkLog never reopens a path whose bytes may
	// have changed after preflight.
	originalPromptContents []byte
	originalPromptDigest   string
}

// ClaimExecutionIdentity is the creator-supplied identity for one new claim.
// Model is mandatory when the claim is published. CLI and Provider are
// independent optional route identifiers and never carry credentials.
type ClaimExecutionIdentity struct {
	Model    string
	CLI      string
	Provider string
}

// workLogProjection is an untrusted pointer. It contains no path, prompt,
// repository, branch, or model data and is never used without loading and
// corroborating the immutable private claim.
type workLogProjection struct {
	Version   int    `json:"version"`
	EffortID  string `json:"effort_id"`
	RunID     string `json:"run_id"`
	ClaimID   string `json:"claim_id"`
	Lifecycle string `json:"lifecycle"`
}

type workLogClaim struct {
	Version         int                             `json:"version"`
	EffortID        string                          `json:"effort_id"`
	RunID           string                          `json:"run_id"`
	ClaimID         string                          `json:"claim_id"`
	Task            string                          `json:"task"`
	Repository      string                          `json:"repository"`
	Worktree        string                          `json:"worktree"`
	Branch          string                          `json:"branch"`
	Base            string                          `json:"base"`
	BaseSHA         string                          `json:"base_sha"`
	Lifecycle       string                          `json:"lifecycle"`
	RecordedAt      time.Time                       `json:"recorded_at"`
	Initiator       string                          `json:"initiator,omitempty"`
	AgentID         string                          `json:"agent_id,omitempty"`
	AgentRuntime    string                          `json:"agent_runtime,omitempty"`
	Model           string                          `json:"model,omitempty"`
	ModelProvenance string                          `json:"model_provenance,omitempty"`
	ModelDeclaredBy string                          `json:"model_declared_by,omitempty"`
	CLI             string                          `json:"cli,omitempty"`
	Provider        string                          `json:"provider,omitempty"`
	WBSessionID     string                          `json:"wb_session_id,omitempty"`
	PromptArchive   string                          `json:"prompt_archive,omitempty"` // run-relative
	PromptDigest    string                          `json:"prompt_sha256,omitempty"`
	ParentClaimID   string                          `json:"parent_claim_id,omitempty"`
	AcquiredVia     string                          `json:"acquired_via,omitempty"`
	ExternalHandoff *workLogExternalHandoffEvidence `json:"external_handoff,omitempty"`
}

// workLogIdentityCorrection is immutable evidence. Field presence, rather
// than an empty value convention, makes clearing optional fields auditable.
type workLogIdentityCorrection struct {
	Version       int       `json:"version"`
	Type          string    `json:"type"`
	CorrectionID  string    `json:"correction_id"`
	ClaimID       string    `json:"claim_id"`
	Sequence      int       `json:"sequence"`
	PredecessorID string    `json:"predecessor_id,omitempty"`
	At            time.Time `json:"at"`
	Actor         string    `json:"actor"`
	Reason        string    `json:"reason"`
	Model         *string   `json:"model,omitempty"`
	CLI           *string   `json:"cli,omitempty"`
	Provider      *string   `json:"provider,omitempty"`
}

// ExecutionIdentity is the current, projected view of immutable claim and
// correction history. CLI/provider are deliberately independent and never
// inferred from model or one another.
type ExecutionIdentity struct {
	Model           string   `json:"model"`
	ModelProvenance string   `json:"model_provenance"`
	ModelDeclaredBy string   `json:"model_declared_by,omitempty"`
	CLI             string   `json:"cli,omitempty"`
	Provider        string   `json:"provider,omitempty"`
	CorrectionIDs   []string `json:"correction_ids,omitempty"`
}

// CorrectExecutionIdentityOptions changes only explicitly selected fields.
// Nil means leave unchanged; a pointer to "" clears CLI/provider. Model cannot
// be cleared: use the explicit value "unknown" instead.
type CorrectExecutionIdentityOptions struct {
	ProjectsRoot string
	EffortID     string
	RunID        string
	ClaimID      string
	EventID      string
	Actor        string
	Reason       string
	Model        *string
	CLI          *string
	Provider     *string
}

type ExecutionIdentityCorrectionResult struct {
	ClaimID      string            `json:"claim_id"`
	CorrectionID string            `json:"correction_id"`
	Identity     ExecutionIdentity `json:"identity"`
	OutboxPath   string            `json:"outbox_path"`
}

type workLogPromptMetadata struct {
	Version         int       `json:"version"`
	SHA256          string    `json:"sha256"`
	SourceReference string    `json:"source_reference"`
	CapturedAt      time.Time `json:"captured_at"`
}

type workLogTerminalRecord struct {
	workLogClaim
	FinalCommit      string                          `json:"final_commit"`
	Disposition      string                          `json:"worktree_disposition"`
	SealedAt         time.Time                       `json:"sealed_at"`
	SuccessorClaimID string                          `json:"successor_claim_id,omitempty"`
	SuccessorAgentID string                          `json:"successor_agent_id,omitempty"`
	ExternalHandoff  *workLogExternalHandoffEvidence `json:"external_handoff_completion,omitempty"`
	Orphaned         *workLogOrphanedEvidence        `json:"orphaned_evidence,omitempty"`
	DirtyCapture     *DirtyWorktreeEvidence          `json:"dirty_capture,omitempty"`
	Supersession     *SupersessionReceipt            `json:"supersession,omitempty"`
}

type workLogPublicEvent struct {
	Version         int                             `json:"version"`
	Type            string                          `json:"type"`
	At              time.Time                       `json:"at"`
	EffortID        string                          `json:"effort_id"`
	RunID           string                          `json:"run_id"`
	ClaimID         string                          `json:"claim_id"`
	Repository      string                          `json:"repository"`
	Branch          string                          `json:"branch"`
	Base            string                          `json:"base"`
	BaseSHA         string                          `json:"base_sha"`
	FinalCommit     string                          `json:"final_commit,omitempty"`
	Lifecycle       string                          `json:"lifecycle"`
	Disposition     string                          `json:"disposition,omitempty"`
	CorrectionID    string                          `json:"correction_id,omitempty"`
	ExternalHandoff *workLogExternalHandoffEvidence `json:"external_handoff,omitempty"`
	DirtyCapture    *DirtyWorktreeEvidence          `json:"dirty_capture,omitempty"`
	Supersession    *SupersessionReceipt            `json:"supersession,omitempty"`
}

// WorkLogPublicationOutcome is the typed receipt for the monotonic Work Log
// publication sequence. A caller can distinguish a failure before any claim
// from one after an immutable claim or local projection became durable and can
// therefore roll back or expose the exact recovery evidence deterministically.
type WorkLogPublicationOutcome struct {
	ClaimPath         string `json:"claim_path,omitempty"`
	EffortID          string `json:"effort_id,omitempty"`
	RunID             string `json:"run_id,omitempty"`
	ClaimID           string `json:"claim_id,omitempty"`
	ClaimWritten      bool   `json:"claim_written"`
	ProjectionWritten bool   `json:"projection_written"`
	OutboxWritten     bool   `json:"outbox_written"`
	claim             workLogClaim
}

type workLogPublicationHooks struct {
	afterClaim      func() error
	afterProjection func() error
}

type legacyWorkLogClaim struct {
	Version       int       `json:"version"`
	EffortID      string    `json:"effort_id"`
	RunID         string    `json:"run_id"`
	Task          string    `json:"task"`
	Repository    string    `json:"repository"`
	Worktree      string    `json:"worktree"`
	Branch        string    `json:"branch"`
	Base          string    `json:"base"`
	BaseSHA       string    `json:"base_sha"`
	Lifecycle     string    `json:"lifecycle"`
	RecordedAt    time.Time `json:"recorded_at"`
	Initiator     string    `json:"initiator,omitempty"`
	AgentID       string    `json:"agent_id,omitempty"`
	AgentRuntime  string    `json:"agent_runtime,omitempty"`
	Model         string    `json:"model,omitempty"`
	PromptArchive string    `json:"prompt_archive,omitempty"`
}

type legacyClaimMigration struct {
	Version             int  `json:"version"`
	RecoveredClaims     int  `json:"recovered_claims"`
	ObservedProjections int  `json:"observed_worktree_projections"`
	LostCardinality     bool `json:"lost_cardinality"`
}

// PrepareWorkLogOptions validates every identifier and snapshots the exact
// private prompt before a caller mutates Git, hooks, or a worktree. It also
// corroborates an existing run's immutable prompt archive so reusing a Run ID
// with different bytes is rejected before worktree creation.
func PrepareWorkLogOptions(projectsRoot, task string, options WorkLogOptions) (WorkLogOptions, error) {
	now := time.Now().UTC()
	effort, run, err := normalizeWorkLogOptions(task, options, now)
	if err != nil {
		return WorkLogOptions{}, err
	}
	options.EffortID = effort
	options.RunID = run
	if err := snapshotOriginalPrompt(&options); err != nil {
		return WorkLogOptions{}, err
	}
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		return WorkLogOptions{}, err
	}
	if err := corroborateExistingRunPrompt(home, effort, run, options); err != nil {
		return WorkLogOptions{}, err
	}
	return options, nil
}

// PreflightWorkLogOptions remains the pure, path-independent validation used
// by callers that have not resolved a projects root yet. Mutation paths use
// PrepareWorkLogOptions so an existing run is corroborated as well.
func PreflightWorkLogOptions(task string, options WorkLogOptions) error {
	effort, run, err := normalizeWorkLogOptions(task, options, time.Now().UTC())
	if err != nil {
		return err
	}
	options.EffortID, options.RunID = effort, run
	return snapshotOriginalPrompt(&options)
}

// originalPromptStdinMarker is recorded as this option's SourceReference when
// the exact prompt bytes were captured in memory (from stdin) rather than
// opened from an external file, so the private archive metadata never claims
// a path that does not exist.
const originalPromptStdinMarker = "(stdin)"

// WithOriginalPromptFromStdin captures prompt bytes the caller already holds
// in memory — read once from stdin, never staged to any file — as this
// option's immutable original prompt. It fails closed on empty or
// whitespace-only input, exactly like an empty --original-prompt-file.
// Because the bytes are captured directly instead of reopened from a path,
// there is no shared staging file and no read-after-write window for a
// concurrent caller to corrupt: the private archive WB writes later is
// byte-for-byte these exact contents.
func (options WorkLogOptions) WithOriginalPromptFromStdin(content []byte) (WorkLogOptions, error) {
	if len(bytes.TrimSpace(content)) == 0 {
		return WorkLogOptions{}, fmt.Errorf("--original-prompt-file - requires non-empty stdin so the private Work Log can retain the exact originating request")
	}
	digest := sha256.Sum256(content)
	options.OriginalPrompt = originalPromptStdinMarker
	options.originalPromptContents = append([]byte(nil), content...)
	options.originalPromptDigest = hex.EncodeToString(digest[:])
	return options, nil
}

func snapshotOriginalPrompt(options *WorkLogOptions) error {
	if len(options.originalPromptContents) != 0 {
		digest := sha256.Sum256(options.originalPromptContents)
		if options.originalPromptDigest != hex.EncodeToString(digest[:]) || strings.TrimSpace(options.OriginalPrompt) == "" {
			return fmt.Errorf("prepared original prompt snapshot is internally inconsistent")
		}
		return nil
	}
	prompt := strings.TrimSpace(options.OriginalPrompt)
	if prompt == "" {
		if options.RequireOriginalPrompt {
			return fmt.Errorf("--original-prompt-file is required so the private Work Log can retain the exact originating request")
		}
		return nil
	}
	absolute, err := filepath.Abs(prompt)
	if err != nil {
		return fmt.Errorf("resolve original prompt %s before mutation: %w", prompt, err)
	}
	file, err := os.Open(absolute)
	if err != nil {
		return fmt.Errorf("open original prompt %s before mutation: %w", prompt, err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("inspect original prompt %s: %w", prompt, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("original prompt %s must be a regular file", prompt)
	}
	contents, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("read original prompt %s before mutation: %w", prompt, err)
	}
	if len(bytes.TrimSpace(contents)) == 0 {
		return fmt.Errorf("original prompt %s must not be empty", prompt)
	}
	digest := sha256.Sum256(contents)
	options.OriginalPrompt = absolute
	options.originalPromptContents = append([]byte(nil), contents...)
	options.originalPromptDigest = hex.EncodeToString(digest[:])
	return nil
}

func corroborateExistingRunPrompt(home, effort, run string, options WorkLogOptions) error {
	runDir, _, err := openWorkLogRun(home, effort, run, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect existing work-log run before mutation: %w", err)
	}
	defer func() { _ = runDir.Close() }()
	archived, promptErr := readBytesAt(runDir, "original-prompt.txt")
	var metadata workLogPromptMetadata
	metadataErr := readJSONAt(runDir, "original-prompt.json", &metadata)
	if promptErr == nil {
		if len(options.originalPromptContents) == 0 {
			return fmt.Errorf("work-log run %s/%s already has an original prompt; provide the same --original-prompt-file", effort, run)
		}
		if !bytes.Equal(archived, options.originalPromptContents) {
			return fmt.Errorf("work-log run %s/%s is already bound to different original prompt bytes", effort, run)
		}
		digest := sha256.Sum256(archived)
		want := hex.EncodeToString(digest[:])
		if metadataErr == nil && (metadata.Version != 1 || metadata.SHA256 != want) {
			return fmt.Errorf("work-log run %s/%s prompt metadata does not match its immutable archive", effort, run)
		}
		if metadataErr != nil && !errors.Is(metadataErr, os.ErrNotExist) {
			return fmt.Errorf("inspect existing prompt metadata: %w", metadataErr)
		}
		return nil
	}
	if !errors.Is(promptErr, os.ErrNotExist) {
		return fmt.Errorf("inspect existing original prompt: %w", promptErr)
	}
	if metadataErr == nil || !errors.Is(metadataErr, os.ErrNotExist) {
		if metadataErr != nil {
			return fmt.Errorf("inspect existing prompt metadata: %w", metadataErr)
		}
		return fmt.Errorf("work-log run %s/%s has prompt metadata without its immutable archive", effort, run)
	}
	// Once a run index or claim exists, an absent prompt is evidence from an
	// older/partial writer. Never guess that a newly supplied file was the
	// original request and silently rewrite history.
	if _, err := readBytesAt(runDir, "run.json"); err == nil {
		return fmt.Errorf("work-log run %s/%s already exists without an immutable original prompt", effort, run)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	claims, err := openPrivateChild(runDir, "claims", false)
	if err == nil {
		defer func() { _ = claims.Close() }()
		if names, readErr := claims.Readdirnames(1); readErr == nil && len(names) != 0 {
			return fmt.Errorf("work-log run %s/%s already has a claim without an immutable original prompt", effort, run)
		} else if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func normalizeWorkLogOptions(task string, options WorkLogOptions, now time.Time) (effort, run string, err error) {
	if err := validateNewExecutionIdentity(ClaimExecutionIdentity{Model: options.Model, CLI: options.CLI, Provider: options.Provider}); err != nil {
		return "", "", err
	}
	effort = strings.TrimSpace(options.EffortID)
	if effort == "" {
		effort = task
	}
	run = strings.TrimSpace(options.RunID)
	if run == "" {
		run = "wb-" + now.Format("20060102T150405.000000000Z")
	}
	if !validSafeSegment(effort) {
		return "", "", fmt.Errorf("work-log effort id %q must be one safe path segment", effort)
	}
	if !validSafeSegment(run) {
		return "", "", fmt.Errorf("work-log run id %q must be one safe path segment", run)
	}
	return effort, run, nil
}

func validateNewExecutionIdentity(identity ClaimExecutionIdentity) error {
	model := strings.TrimSpace(identity.Model)
	if model == "" {
		return fmt.Errorf("--model is required for every new Work Log claim; pass the exact child model or the explicit value unknown")
	}
	if !validExecutionIdentifier(model, true) {
		return fmt.Errorf("model %q must be a non-secret execution identifier or explicit unknown", model)
	}
	for _, field := range []struct{ name, value string }{{"cli", identity.CLI}, {"provider", identity.Provider}} {
		value := strings.TrimSpace(field.value)
		if value != "" && !validExecutionIdentifier(value, false) {
			return fmt.Errorf("%s %q must be a non-secret execution identifier", field.name, value)
		}
	}
	return nil
}

func validateCorrectionIdentity(options CorrectExecutionIdentityOptions) error {
	if !validSafeSegment(options.EffortID) || !validSafeSegment(options.RunID) || !validClaimID(options.ClaimID) || !validSafeSegment(options.EventID) {
		return fmt.Errorf("effort, run, claim, and correction event ID must be valid exact Work Log identifiers")
	}
	if strings.TrimSpace(options.Actor) == "" || strings.TrimSpace(options.Reason) == "" {
		return fmt.Errorf("--actor and --reason are required for an execution-identity correction")
	}
	if options.Model == nil && options.CLI == nil && options.Provider == nil {
		return fmt.Errorf("select at least one of --model, --cli, or --provider to correct")
	}
	if options.Model != nil {
		model := strings.TrimSpace(*options.Model)
		if model == "" || !validExecutionIdentifier(model, true) {
			return fmt.Errorf("corrected model must be an exact non-secret identifier or explicit unknown")
		}
	}
	for _, field := range []struct {
		name  string
		value *string
	}{{"cli", options.CLI}, {"provider", options.Provider}} {
		if field.value != nil && strings.TrimSpace(*field.value) != "" && !validExecutionIdentifier(strings.TrimSpace(*field.value), false) {
			return fmt.Errorf("corrected %s must be a bounded non-secret execution identifier, or an explicit empty value to clear it", field.name)
		}
	}
	return nil
}

func validExecutionIdentifier(value string, allowUnknown bool) bool {
	if value == "unknown" {
		return allowUnknown
	}
	if !executionIdentifier.MatchString(value) {
		return false
	}
	lower := strings.ToLower(value)
	// Credentials are never execution-route metadata. This is deliberately a
	// bounded defense against well-known credential shapes, not a claim that
	// arbitrary secrets can be detected. Label syntax and caller guidance remain
	// the primary boundary; obvious token and URL user-info forms are refused.
	return !strings.ContainsAny(value, "@=?#") && !strings.Contains(lower, "token") &&
		!strings.Contains(lower, "secret") && !strings.Contains(lower, "password") &&
		!hasCredentialPrefix(lower)
}

func hasCredentialPrefix(lower string) bool {
	for _, prefix := range []string{
		"sk-", "sk_", "rk_live_", "bearer", "ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_",
		"glpat-", "xoxa-", "xoxb-", "xoxp-", "xoxr-", "npm_", "pypi-", "hf_", "ops_", "akia", "aiza", "eyj",
	} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func declaredBy(options WorkLogOptions) string {
	if value := strings.TrimSpace(options.Initiator); value != "" {
		return value
	}
	if value := strings.TrimSpace(options.AgentID); value != "" {
		return value
	}
	return "unknown"
}

func identityFromClaim(claim workLogClaim) ExecutionIdentity {
	model := strings.TrimSpace(claim.Model)
	provenance := strings.TrimSpace(claim.ModelProvenance)
	if model == "" { // legacy records are readable but never guessed.
		model, provenance = "unknown", modelProvenanceUnknown
	}
	if provenance == "" {
		if model == "unknown" {
			provenance = modelProvenanceUnknown
		} else {
			// v1 records predate caller-declaration evidence; preserve the fact
			// that a runtime supplied it rather than manufacturing a caller.
			provenance = modelProvenanceRuntimeObserved
		}
	}
	return ExecutionIdentity{Model: model, ModelProvenance: provenance,
		ModelDeclaredBy: claim.ModelDeclaredBy, CLI: claim.CLI, Provider: claim.Provider}
}

func workLogClaimID(effort string, result CreateResult) string {
	hash := sha256.New()
	// Claim identity is portable: run IDs and machine-local worktree paths are
	// deliberately absent. The immutable private claim still records and
	// corroborates the absolute live path.
	for _, value := range []string{effort, result.Repository, result.Branch, result.Base, result.BaseSHA} {
		_, _ = io.WriteString(hash, fmt.Sprintf("%d:", len(value)))
		_, _ = io.WriteString(hash, value)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func successorWorkLogClaimID(parentClaimID, successor, disposition string) string {
	hash := sha256.New()
	for _, value := range []string{"successor", parentClaimID, successor, disposition} {
		_, _ = io.WriteString(hash, fmt.Sprintf("%d:", len(value)))
		_, _ = io.WriteString(hash, value)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// declaredSuccessorWorkLogClaimID binds a creator's normalized execution
// identity to the deterministic successor ID. The terminal record stores that
// ID before the successor is published, so a crash/retry cannot silently
// substitute a different model or route beneath an already-sealed handoff.
func declaredSuccessorWorkLogClaimID(parentClaimID, successor, disposition string, identity ClaimExecutionIdentity) string {
	hash := sha256.New()
	for _, value := range []string{
		"successor-execution-identity-v2", parentClaimID, successor, disposition,
		strings.TrimSpace(identity.Model), strings.TrimSpace(identity.CLI), strings.TrimSpace(identity.Provider),
	} {
		_, _ = io.WriteString(hash, fmt.Sprintf("%d:", len(value)))
		_, _ = io.WriteString(hash, value)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func validClaimID(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// recordWorkLog writes one immutable private claim per worktree, a redacted
// pointer projection, and a collision-free per-claim outbox event.
func recordWorkLog(home, task string, result CreateResult, options WorkLogOptions) (string, error) {
	outcome, err := recordWorkLogWithHooks(home, task, result, options, workLogPublicationHooks{})
	return outcome.ClaimPath, err
}

func recordWorkLogWithHooks(home, task string, result CreateResult, options WorkLogOptions, hooks workLogPublicationHooks) (WorkLogPublicationOutcome, error) {
	var outcome WorkLogPublicationOutcome
	now := time.Now().UTC()
	effort, run, err := normalizeWorkLogOptions(task, options, now)
	if err != nil {
		return outcome, err
	}
	outcome.EffortID, outcome.RunID = effort, run
	claimID := workLogClaimID(effort, result)
	outcome.ClaimID = claimID
	runDir, runPath, err := openWorkLogRun(home, effort, run, true)
	if err != nil {
		return outcome, err
	}
	defer func() { _ = runDir.Close() }()
	if err := migrateLegacySingletonClaim(runDir, runPath, home, effort, run); err != nil {
		return outcome, fmt.Errorf("migrate legacy singleton claim: %w", err)
	}

	promptArchive, promptDigest, err := ensureOriginalPromptArchive(runDir, options, now)
	if err != nil {
		return outcome, err
	}
	model := strings.TrimSpace(options.Model)
	provenance := modelProvenanceCallerDeclared
	if model == "unknown" {
		provenance = modelProvenanceUnknown
	}
	sessionID := strings.TrimSpace(options.WBSessionID)
	// A live resolver is authoritative whenever present; callers cannot make
	// an admitted agent claim point at a different session by supplying a
	// stale or forged WorkLogOptions value.
	if identity, ok := RegisteredIdentity(); ok {
		sessionID = strings.TrimSpace(identity.WBSessionID)
	}
	claim := workLogClaim{Version: 2, EffortID: effort, RunID: run, ClaimID: claimID, Task: task,
		Repository: result.Repository, Worktree: result.WorktreeDir, Branch: result.Branch,
		Base: result.Base, BaseSHA: result.BaseSHA, Lifecycle: "active", RecordedAt: now,
		Initiator: strings.TrimSpace(options.Initiator), AgentID: strings.TrimSpace(options.AgentID),
		AgentRuntime: strings.TrimSpace(options.AgentRuntime), Model: model,
		ModelProvenance: provenance, ModelDeclaredBy: declaredBy(options),
		CLI: strings.TrimSpace(options.CLI), Provider: strings.TrimSpace(options.Provider),
		WBSessionID:   sessionID,
		PromptArchive: promptArchive, PromptDigest: promptDigest,
		AcquiredVia: strings.TrimSpace(options.AcquiredVia)}
	claims, err := openPrivateChild(runDir, "claims", true)
	if err != nil {
		return outcome, err
	}
	defer func() { _ = claims.Close() }()
	claimName := claimID + ".json"
	if err := writeJSONImmutableAt(claims, claimName, claim, true); err != nil {
		return outcome, fmt.Errorf("write immutable work-log claim: %w", err)
	}
	outcome.ClaimPath = filepath.Join(runPath, "claims", claimName)
	outcome.ClaimWritten = true
	outcome.claim = claim
	if hooks.afterClaim != nil {
		if err := hooks.afterClaim(); err != nil {
			return outcome, fmt.Errorf("after immutable work-log claim publication: %w", err)
		}
	}
	if err := ensureWorkLogRunIndex(runDir, effort, run); err != nil {
		return outcome, err
	}
	projection := workLogProjection{Version: 1, EffortID: effort, RunID: run, ClaimID: claimID, Lifecycle: "active"}
	if err := writeWorkLogProjection(result.WorktreeDir, projection); err != nil {
		return outcome, err
	}
	outcome.ProjectionWritten = true
	if err := writeCreationJournal(effort, run, claimID, result, options, now); err != nil {
		return outcome, err
	}
	if hooks.afterProjection != nil {
		if err := hooks.afterProjection(); err != nil {
			return outcome, fmt.Errorf("after work-log recovery projection publication: %w", err)
		}
	}
	outbox, err := openWorkLogOutbox(home, effort, true)
	if err != nil {
		return outcome, err
	}
	defer func() { _ = outbox.Close() }()
	event := workLogPublicEvent{Version: 1, Type: "worktree.claimed", At: now, EffortID: effort,
		RunID: run, ClaimID: claimID, Repository: claim.Repository, Branch: claim.Branch,
		Base: claim.Base, BaseSHA: claim.BaseSHA, Lifecycle: "active"}
	if err := writeJSONImmutableAt(outbox, run+"-"+claimID+"-claimed.json", event, true); err != nil {
		return outcome, err
	}
	outcome.OutboxWritten = true
	return outcome, nil
}

// activeWorkLogClaim resolves the worktree's untrusted projection through its
// immutable private claim and live Git identity. A missing projection denotes
// a legacy pre-Work-Log checkout; every other mismatch is a hard resume error.
func activeWorkLogClaim(home, worktree string) (workLogClaim, workLogProjection, string, error) {
	projection, err := readWorkLogProjectionForClaim(home, worktree)
	if err != nil {
		return workLogClaim{}, workLogProjection{}, "", err
	}
	if projection.Lifecycle != "active" {
		return workLogClaim{}, projection, "", fmt.Errorf("work-log projection is %s, not active", projection.Lifecycle)
	}
	if err := corroborateProjectionWithPrivateClaim(home, worktree, projection); err != nil {
		return workLogClaim{}, projection, "", fmt.Errorf("corroborate active work-log claim: %w", err)
	}
	runDir, runPath, err := openWorkLogRun(home, projection.EffortID, projection.RunID, false)
	if err != nil {
		return workLogClaim{}, projection, "", err
	}
	defer func() { _ = runDir.Close() }()
	claims, err := openPrivateChild(runDir, "claims", false)
	if err != nil {
		return workLogClaim{}, projection, "", err
	}
	defer func() { _ = claims.Close() }()
	var claim workLogClaim
	if err := readJSONAt(claims, projection.ClaimID+".json", &claim); err != nil {
		return workLogClaim{}, projection, "", err
	}
	return claim, projection, filepath.Join(runPath, "claims", projection.ClaimID+".json"), nil
}

// CorrectExecutionIdentity appends exactly one correction to an immutable
// claim, so it remains usable after the worktree was terminalized or removed.
// The caller supplies a stable event ID; retrying it is idempotent, including
// recovery from a crash after the correction and before its outbox receipt.
func CorrectExecutionIdentity(options CorrectExecutionIdentityOptions) (ExecutionIdentityCorrectionResult, error) {
	var result ExecutionIdentityCorrectionResult
	if err := validateCorrectionIdentity(options); err != nil {
		return result, err
	}
	home, err := wbhome.Root(options.ProjectsRoot)
	if err != nil {
		return result, err
	}
	runDir, _, err := openWorkLogRun(home, options.EffortID, options.RunID, false)
	if err != nil {
		return result, fmt.Errorf("open correction Work Log run: %w", err)
	}
	defer func() { _ = runDir.Close() }()
	unlock, err := lockClaim(runDir, options.ClaimID)
	if err != nil {
		return result, fmt.Errorf("lock correction claim: %w", err)
	}
	defer unlock()
	claims, err := openPrivateChild(runDir, "claims", false)
	if err != nil {
		return result, fmt.Errorf("open correction claims: %w", err)
	}
	var claim workLogClaim
	err = readJSONAt(claims, options.ClaimID+".json", &claim)
	_ = claims.Close()
	if err != nil {
		return result, fmt.Errorf("read immutable claim %s: %w", options.ClaimID, err)
	}
	if claim.ClaimID != options.ClaimID || claim.EffortID != options.EffortID || claim.RunID != options.RunID || (claim.Version != 1 && claim.Version != 2) {
		return result, fmt.Errorf("claim does not match the supplied Work Log identity")
	}
	identity, corrections, err := projectExecutionIdentity(runDir, claim)
	if err != nil {
		return result, fmt.Errorf("project identity before correction: %w", err)
	}
	for _, correction := range corrections {
		if correction.CorrectionID == options.EventID {
			if !sameCorrectionRequest(correction, options) {
				return result, fmt.Errorf("correction event ID %q already denotes different immutable evidence", options.EventID)
			}
			return writeCorrectionOutbox(home, claim, correction, identity)
		}
	}
	previous := ""
	if len(corrections) != 0 {
		previous = corrections[len(corrections)-1].CorrectionID
	}
	event := workLogIdentityCorrection{Version: 1, Type: "worktree.execution_identity_corrected", CorrectionID: options.EventID,
		ClaimID: claim.ClaimID, Sequence: len(corrections) + 1, PredecessorID: previous, At: time.Now().UTC(),
		Actor: strings.TrimSpace(options.Actor), Reason: strings.TrimSpace(options.Reason), Model: normalizedPointer(options.Model), CLI: normalizedPointer(options.CLI), Provider: normalizedPointer(options.Provider)}
	correctionsDir, err := openWorkLogCorrections(runDir, claim.ClaimID, true)
	if err != nil {
		return result, fmt.Errorf("open correction history: %w", err)
	}
	defer func() { _ = correctionsDir.Close() }()
	name := event.CorrectionID + ".json"
	if _, readErr := readIdentityCorrection(correctionsDir, name); readErr == nil {
		return result, fmt.Errorf("correction event ID %q appeared concurrently; retry the exact command", event.CorrectionID)
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return result, readErr
	} else if err := writeJSONImmutableAt(correctionsDir, name, event, false); err != nil {
		return result, fmt.Errorf("append immutable execution-identity correction: %w", err)
	}
	identity, _, err = projectExecutionIdentity(runDir, claim)
	if err != nil {
		return result, fmt.Errorf("project identity after correction: %w", err)
	}
	return writeCorrectionOutbox(home, claim, event, identity)
}

func writeCorrectionOutbox(home string, claim workLogClaim, event workLogIdentityCorrection, identity ExecutionIdentity) (ExecutionIdentityCorrectionResult, error) {
	var result ExecutionIdentityCorrectionResult
	outbox, err := openWorkLogOutbox(home, claim.EffortID, true)
	if err != nil {
		return result, fmt.Errorf("open correction outbox: %w", err)
	}
	defer func() { _ = outbox.Close() }()
	public := workLogPublicEvent{Version: 1, Type: event.Type, At: event.At, EffortID: claim.EffortID,
		RunID: claim.RunID, ClaimID: claim.ClaimID, Repository: claim.Repository, Branch: claim.Branch,
		Base: claim.Base, BaseSHA: claim.BaseSHA, Lifecycle: claim.Lifecycle, CorrectionID: event.CorrectionID}
	outboxName := claim.RunID + "-" + claim.ClaimID + "-identity-" + event.CorrectionID + ".json"
	if err := writeJSONImmutableAt(outbox, outboxName, public, true); err != nil {
		return result, fmt.Errorf("write execution-identity correction outbox receipt: %w", err)
	}
	return ExecutionIdentityCorrectionResult{ClaimID: claim.ClaimID, CorrectionID: event.CorrectionID,
		Identity: identity, OutboxPath: filepath.Join(home, "worklogs", claim.EffortID, "outbox", outboxName)}, nil
}

func normalizedPointer(value *string) *string {
	if value == nil {
		return nil
	}
	copy := strings.TrimSpace(*value)
	return &copy
}

func sameCorrectionRequest(correction workLogIdentityCorrection, options CorrectExecutionIdentityOptions) bool {
	return correction.Actor == strings.TrimSpace(options.Actor) && correction.Reason == strings.TrimSpace(options.Reason) &&
		sameStringPointer(correction.Model, normalizedPointer(options.Model)) && sameStringPointer(correction.CLI, normalizedPointer(options.CLI)) && sameStringPointer(correction.Provider, normalizedPointer(options.Provider))
}
func sameStringPointer(left, right *string) bool {
	return (left == nil && right == nil) || (left != nil && right != nil && *left == *right)
}

func openWorkLogCorrections(runDir *os.File, claimID string, create bool) (*os.File, error) {
	if !validClaimID(claimID) {
		return nil, fmt.Errorf("invalid correction claim ID")
	}
	root, err := openPrivateChild(runDir, "corrections", create)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return openPrivateChild(root, claimID, create)
}

func readIdentityCorrection(directory *os.File, name string) (workLogIdentityCorrection, error) {
	var correction workLogIdentityCorrection
	err := readJSONAt(directory, name, &correction)
	return correction, err
}

// projectExecutionIdentity proves there is one linear, complete correction
// chain. It deliberately uses sequence/predecessor, not timestamp ordering.
func projectExecutionIdentity(runDir *os.File, claim workLogClaim) (ExecutionIdentity, []workLogIdentityCorrection, error) {
	identity := identityFromClaim(claim)
	directory, err := openWorkLogCorrections(runDir, claim.ClaimID, false)
	if errors.Is(err, os.ErrNotExist) {
		return identity, nil, nil
	}
	if err != nil {
		return ExecutionIdentity{}, nil, err
	}
	defer func() { _ = directory.Close() }()
	names, err := directory.Readdirnames(-1)
	if err != nil {
		return ExecutionIdentity{}, nil, err
	}
	sort.Strings(names)
	corrections := make([]workLogIdentityCorrection, 0, len(names))
	for _, name := range names {
		if !strings.HasSuffix(name, ".json") || !validSafeSegment(strings.TrimSuffix(name, ".json")) {
			return ExecutionIdentity{}, nil, fmt.Errorf("malformed execution-identity correction filename %q", name)
		}
		correction, err := readIdentityCorrection(directory, name)
		if err != nil {
			return ExecutionIdentity{}, nil, err
		}
		if correction.Version != 1 || correction.Type != "worktree.execution_identity_corrected" || correction.ClaimID != claim.ClaimID ||
			correction.CorrectionID != strings.TrimSuffix(name, ".json") || correction.Sequence < 1 || correction.At.IsZero() ||
			strings.TrimSpace(correction.Actor) == "" || strings.TrimSpace(correction.Reason) == "" {
			return ExecutionIdentity{}, nil, fmt.Errorf("malformed execution-identity correction %q", name)
		}
		if correction.Model == nil && correction.CLI == nil && correction.Provider == nil {
			return ExecutionIdentity{}, nil, fmt.Errorf("execution-identity correction %q changes no field", name)
		}
		if correction.Model != nil && (strings.TrimSpace(*correction.Model) == "" || !validExecutionIdentifier(*correction.Model, true)) {
			return ExecutionIdentity{}, nil, fmt.Errorf("execution-identity correction %q has invalid model", name)
		}
		for _, value := range []*string{correction.CLI, correction.Provider} {
			if value != nil && *value != "" && !validExecutionIdentifier(*value, false) {
				return ExecutionIdentity{}, nil, fmt.Errorf("execution-identity correction %q has invalid route", name)
			}
		}
		corrections = append(corrections, correction)
	}
	sort.Slice(corrections, func(i, j int) bool { return corrections[i].Sequence < corrections[j].Sequence })
	previous := ""
	for index, correction := range corrections {
		if correction.Sequence != index+1 || correction.PredecessorID != previous {
			return ExecutionIdentity{}, nil, fmt.Errorf("execution-identity correction chain is forked, incomplete, or cyclic")
		}
		if correction.Model != nil {
			identity.Model = *correction.Model
			identity.ModelDeclaredBy = correction.Actor
			if identity.Model == "unknown" {
				identity.ModelProvenance = modelProvenanceUnknown
			} else {
				identity.ModelProvenance = modelProvenanceCallerDeclared
			}
		}
		if correction.CLI != nil {
			identity.CLI = *correction.CLI
		}
		if correction.Provider != nil {
			identity.Provider = *correction.Provider
		}
		identity.CorrectionIDs = append(identity.CorrectionIDs, correction.CorrectionID)
		previous = correction.CorrectionID
	}
	return identity, corrections, nil
}

func validateResumeWorkLogRequest(home string, requested WorkLogOptions, claim workLogClaim) error {
	identity, err := currentExecutionIdentity(home, claim)
	if err != nil {
		return fmt.Errorf("project current execution identity: %w", err)
	}
	for _, identity := range []struct {
		name      string
		requested string
		existing  string
	}{
		{name: "effort", requested: requested.EffortID, existing: claim.EffortID},
		{name: "run", requested: requested.RunID, existing: claim.RunID},
		{name: "initiator", requested: requested.Initiator, existing: claim.Initiator},
		{name: "agent", requested: requested.AgentID, existing: claim.AgentID},
		{name: "agent runtime", requested: requested.AgentRuntime, existing: claim.AgentRuntime},
		{name: "model", requested: requested.Model, existing: identity.Model},
		{name: "cli", requested: requested.CLI, existing: identity.CLI},
		{name: "provider", requested: requested.Provider, existing: identity.Provider},
	} {
		if value := strings.TrimSpace(identity.requested); value != "" && value != identity.existing {
			return fmt.Errorf("cannot resume active work-log claim with different %s %q (existing %q); use an audited handoff instead", identity.name, value, identity.existing)
		}
	}
	prepared := requested
	prepared.EffortID, prepared.RunID = claim.EffortID, claim.RunID
	if err := snapshotOriginalPrompt(&prepared); err != nil {
		return err
	}
	if len(prepared.originalPromptContents) == 0 {
		return nil
	}
	prepared.RequireOriginalPrompt = false
	if err := corroborateExistingRunPrompt(home, claim.EffortID, claim.RunID, prepared); err != nil {
		return fmt.Errorf("corroborate resumed original prompt: %w", err)
	}
	return nil
}

func currentExecutionIdentity(home string, claim workLogClaim) (ExecutionIdentity, error) {
	runDir, _, err := openWorkLogRun(home, claim.EffortID, claim.RunID, false)
	if err != nil {
		return ExecutionIdentity{}, err
	}
	defer func() { _ = runDir.Close() }()
	identity, _, err := projectExecutionIdentity(runDir, claim)
	return identity, err
}

// workLogOptionsForClaimExtension reuses one existing coordinated run when a
// resume invocation adds a legacy or previously absent repository. It never
// invents new provenance: an explicitly different caller must use handoff.
func workLogOptionsForClaimExtension(home string, requested WorkLogOptions, claim workLogClaim) (WorkLogOptions, error) {
	if err := validateResumeWorkLogRequest(home, requested, claim); err != nil {
		return WorkLogOptions{}, err
	}
	requested.EffortID, requested.RunID = claim.EffortID, claim.RunID
	requested.Initiator, requested.AgentID = claim.Initiator, claim.AgentID
	requested.AgentRuntime = claim.AgentRuntime
	if len(requested.originalPromptContents) != 0 || strings.TrimSpace(requested.OriginalPrompt) != "" {
		requested.RequireOriginalPrompt = false
		if err := snapshotOriginalPrompt(&requested); err != nil {
			return WorkLogOptions{}, err
		}
		return requested, nil
	}
	runDir, runPath, err := openWorkLogRun(home, claim.EffortID, claim.RunID, false)
	if err != nil {
		return WorkLogOptions{}, err
	}
	defer func() { _ = runDir.Close() }()
	contents, err := readBytesAt(runDir, "original-prompt.txt")
	if errors.Is(err, os.ErrNotExist) && !requested.RequireOriginalPrompt {
		return requested, nil
	}
	if err != nil {
		return WorkLogOptions{}, fmt.Errorf("reuse existing original prompt archive: %w", err)
	}
	digest := sha256.Sum256(contents)
	requested.OriginalPrompt = filepath.Join(runPath, "original-prompt.txt")
	requested.originalPromptContents = contents
	requested.originalPromptDigest = hex.EncodeToString(digest[:])
	return requested, nil
}

// reserveOriginalPromptArchive binds a coordinated run to its exact private
// prompt before any worktree is created. The reservation is idempotent for the
// same bytes and rejects a conflicting writer through immutable no-replace
// publication.
func reserveOriginalPromptArchive(home, task string, options WorkLogOptions) error {
	if len(options.originalPromptContents) == 0 && strings.TrimSpace(options.OriginalPrompt) == "" && !options.RequireOriginalPrompt {
		return nil
	}
	effort, run, err := normalizeWorkLogOptions(task, options, time.Now().UTC())
	if err != nil {
		return err
	}
	runDir, _, err := openWorkLogRun(home, effort, run, true)
	if err != nil {
		return err
	}
	defer func() { _ = runDir.Close() }()
	_, _, err = ensureOriginalPromptArchive(runDir, options, time.Now().UTC())
	return err
}

func ensureOriginalPromptArchive(runDir *os.File, options WorkLogOptions, now time.Time) (archive, digest string, err error) {
	if len(options.originalPromptContents) == 0 {
		if err := snapshotOriginalPrompt(&options); err != nil {
			return "", "", err
		}
	}
	if len(options.originalPromptContents) == 0 {
		return "", "", nil
	}
	archive = "original-prompt.txt"
	digest = options.originalPromptDigest
	if err := writeBytesImmutableAt(runDir, archive, options.originalPromptContents, 0o600, true); err != nil {
		return "", "", fmt.Errorf("archive original prompt: %w", err)
	}
	var existing workLogPromptMetadata
	if err := readJSONAt(runDir, "original-prompt.json", &existing); err == nil {
		if existing.Version != 1 || existing.SHA256 != digest {
			return "", "", fmt.Errorf("existing original prompt metadata does not match immutable prompt archive")
		}
		return archive, digest, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", "", fmt.Errorf("inspect original prompt metadata: %w", err)
	}
	metadata := workLogPromptMetadata{Version: 1, SHA256: digest,
		SourceReference: options.OriginalPrompt, CapturedAt: now}
	if err := writeJSONImmutableAt(runDir, "original-prompt.json", metadata, true); err != nil {
		// Concurrent same-run writers may race to publish metadata after both
		// observed the same immutable prompt bytes. Accept the winner only when
		// its digest corroborates those exact bytes.
		if readErr := readJSONAt(runDir, "original-prompt.json", &existing); readErr == nil && existing.Version == 1 && existing.SHA256 == digest {
			return archive, digest, nil
		}
		return "", "", fmt.Errorf("archive original prompt metadata: %w", err)
	}
	return archive, digest, nil
}

func ensureWorkLogRunIndex(runDir *os.File, effort, run string) error {
	var index struct {
		Version int    `json:"version"`
		Effort  string `json:"effort_id"`
		Run     string `json:"run_id"`
	}
	if err := readJSONAt(runDir, "run.json", &index); err == nil {
		if index.Version != 1 || index.Effort != effort || index.Run != run {
			return fmt.Errorf("existing run index identity mismatch")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	index.Version, index.Effort, index.Run = 1, effort, run
	return writeJSONImmutableAt(runDir, "run.json", index, true)
}

// migrateLegacySingletonClaim recovers the one surviving claim from the
// historical run-level claim.json layout. It also writes a deterministic
// machine-readable cardinality warning when live projections prove that the
// singleton had overwritten sibling repositories. The legacy bytes remain in
// place as evidence; migration is immutable and idempotent.
func migrateLegacySingletonClaim(runDir *os.File, runPath, home, effort, run string) error {
	var legacy legacyWorkLogClaim
	if err := readJSONAt(runDir, "claim.json", &legacy); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if legacy.Version != 1 || legacy.EffortID != effort || legacy.RunID != run || legacy.Repository == "" || legacy.Worktree == "" || legacy.Branch == "" || legacy.Base == "" {
		return fmt.Errorf("legacy claim identity is incomplete or mismatched")
	}
	legacyResult := CreateResult{Repository: legacy.Repository, WorktreeDir: legacy.Worktree,
		Branch: legacy.Branch, Base: legacy.Base, BaseSHA: legacy.BaseSHA}
	claimID := workLogClaimID(effort, legacyResult)
	promptArchive := ""
	if legacy.PromptArchive != "" {
		cleanPrompt := filepath.Clean(legacy.PromptArchive)
		if filepath.Dir(cleanPrompt) == filepath.Clean(runPath) {
			promptArchive = filepath.Base(cleanPrompt)
		}
	}
	claim := workLogClaim{Version: 1, EffortID: effort, RunID: run, ClaimID: claimID,
		Task: legacy.Task, Repository: legacy.Repository, Worktree: legacy.Worktree,
		Branch: legacy.Branch, Base: legacy.Base, BaseSHA: legacy.BaseSHA,
		Lifecycle: legacy.Lifecycle, RecordedAt: legacy.RecordedAt, Initiator: legacy.Initiator,
		AgentID: legacy.AgentID, AgentRuntime: legacy.AgentRuntime, Model: legacy.Model,
		PromptArchive: promptArchive}
	claims, err := openPrivateChild(runDir, "claims", true)
	if err != nil {
		return err
	}
	if err := writeJSONImmutableAt(claims, claimID+".json", claim, true); err != nil {
		_ = claims.Close()
		return err
	}
	var existingMigration legacyClaimMigration
	if err := readJSONAt(runDir, "legacy-claim-migration.json", &existingMigration); err == nil {
		_ = claims.Close()
		if existingMigration.Version != 1 {
			return fmt.Errorf("legacy claim migration version mismatch")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		_ = claims.Close()
		return err
	}
	claimEntries, err := claims.Readdirnames(-1)
	_ = claims.Close()
	if err != nil {
		return err
	}
	observed, err := countLegacyWorkLogProjections(filepath.Join(home, "worktrees"), effort, run)
	if err != nil {
		return err
	}
	migration := legacyClaimMigration{Version: 1, RecoveredClaims: len(claimEntries),
		ObservedProjections: observed, LostCardinality: observed > len(claimEntries)}
	return writeJSONImmutableAt(runDir, "legacy-claim-migration.json", migration, true)
}

func countLegacyWorkLogProjections(root, effort, run string) (int, error) {
	count := 0
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		isLegacyProjection := !entry.IsDir() && entry.Name() == legacyWorkLogProjectionName
		isCurrentProjection := !entry.IsDir() && entry.Name() == workLogProjectionName && filepath.Base(filepath.Dir(path)) == workLogProjectionDirectory
		if !isLegacyProjection && !isCurrentProjection {
			return nil
		}
		var identity struct {
			EffortID string `json:"effort_id"`
			RunID    string `json:"run_id"`
		}
		contents, err := os.ReadFile(path)
		if err != nil || json.Unmarshal(contents, &identity) != nil {
			return nil
		}
		if identity.EffortID == effort && identity.RunID == run {
			count++
		}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	return count, err
}

func writeWorkLogProjection(worktree string, projection workLogProjection) error {
	if err := validateProjection(projection); err != nil {
		return err
	}
	if err := ensureWorkLogProjectionExclude(worktree); err != nil {
		return err
	}
	directory, err := openWorkLogProjectionDirectory(worktree, true)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	if err := writeJSONAtomicAt(directory, workLogProjectionName, projection, 0o600); err != nil {
		return err
	}
	return writeManagedWorktreeInstructions(worktree)
}

func ensureWorkLogProjectionExclude(worktree string) error {
	gitPath, err := git(context.Background(), worktree, "rev-parse", "--git-path", "info/exclude")
	if err != nil {
		return fmt.Errorf("resolve per-worktree exclude: %w", err)
	}
	if !filepath.IsAbs(gitPath) {
		gitPath = filepath.Join(worktree, gitPath)
	}
	if err := os.MkdirAll(filepath.Dir(gitPath), 0o700); err != nil {
		return fmt.Errorf("create per-worktree git info: %w", err)
	}
	exclude, err := os.ReadFile(gitPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read per-worktree exclude: %w", err)
	}
	updated := append([]byte(nil), exclude...)
	for _, rule := range []string{workLogProjectionExclude, worktreeInstructionsExclude} {
		if strings.Contains("\n"+string(updated)+"\n", "\n"+rule+"\n") {
			continue
		}
		updated = append(updated, []byte("\n"+rule+"\n")...)
	}
	if !bytes.Equal(updated, exclude) {
		if err := writeBytesAtomic(filepath.Dir(gitPath), filepath.Base(gitPath), updated, 0o600); err != nil {
			return fmt.Errorf("exclude work-log projection: %w", err)
		}
	}
	return nil
}

func writeManagedWorktreeInstructions(worktree string) error {
	root, err := openAbsoluteDirectoryNoFollow(worktree, false)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	fd, openErr := unix.Openat(int(root.Fd()), worktreeInstructionsName, unix.O_RDONLY|unix.O_NOFOLLOW, 0)
	if openErr == nil {
		existingFile := os.NewFile(uintptr(fd), worktreeInstructionsName)
		existing, readErr := io.ReadAll(existingFile)
		_ = existingFile.Close()
		if readErr != nil || !bytes.HasPrefix(existing, []byte("<!-- wb-managed-worktree -->\n")) {
			// A repository- or user-owned file always wins; WB never overwrites it.
			return nil
		}
	} else if !errors.Is(openErr, unix.ENOENT) {
		// Symlinks, directories, and other non-regular occupants are preserved.
		return nil
	}
	return writeBytesAtomicAt(root, worktreeInstructionsName, []byte(worktreeInstructions), 0o600)
}

func openWorkLogProjectionDirectory(worktree string, create bool) (*os.File, error) {
	root, err := openAbsoluteDirectoryNoFollow(worktree, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	var fd int
	if create {
		fd, err = openOrCreateNoFollowDirectory(int(root.Fd()), workLogProjectionDirectory)
	} else {
		fd, err = unix.Openat(int(root.Fd()), workLogProjectionDirectory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	}
	if errors.Is(err, unix.ENOENT) {
		return nil, os.ErrNotExist
	}
	if err != nil {
		return nil, fmt.Errorf("open work-log projection directory: %w", err)
	}
	if err := unix.Fchmod(fd, 0o700); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	directory := os.NewFile(uintptr(fd), "wb-worklog-projection")
	if directory == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("wrap work-log projection directory")
	}
	path := filepath.Join(worktree, workLogProjectionDirectory)
	if !directoryStillMatches(path, directory) {
		_ = directory.Close()
		return nil, fmt.Errorf("work-log projection directory path changed: %s", path)
	}
	return directory, nil
}

func validateProjection(projection workLogProjection) error {
	if projection.Version != 1 || !validSafeSegment(projection.EffortID) || !validSafeSegment(projection.RunID) || !validClaimID(projection.ClaimID) {
		return fmt.Errorf("invalid work-log projection identity")
	}
	if projection.Lifecycle != "active" && projection.Lifecycle != "terminal" {
		return fmt.Errorf("invalid work-log projection lifecycle %q", projection.Lifecycle)
	}
	return nil
}

// sealWorkLogForRecycle treats the projection as an untrusted pointer, loads
// the immutable private claim, corroborates it with live Git, then writes one
// immutable terminal and outbox entry per claim before making the projection
// terminal. Retrying the exact transition is idempotent.
func sealWorkLogForRecycle(home, worktree, finalCommit, disposition string) error {
	return sealWorkLogForRecycleWithSupersession(home, worktree, finalCommit, disposition, nil)
}

func sealWorkLogForRecycleWithDirtyCapture(home, worktree, finalCommit, disposition string, dirty *DirtyWorktreeEvidence) error {
	return sealWorkLogForRecycleWithEvidence(home, worktree, finalCommit, disposition, dirty, nil)
}

func sealWorkLogForSupersession(home, worktree, finalCommit string, receipt *SupersessionReceipt) error {
	return sealWorkLogForRecycleWithEvidence(home, worktree, finalCommit, "superseded", nil, receipt)
}

func sealWorkLogForRecycleWithSupersession(home, worktree, finalCommit, disposition string, supersession *SupersessionReceipt) error {
	return sealWorkLogForRecycleWithEvidence(home, worktree, finalCommit, disposition, nil, supersession)
}

func sealWorkLogForRecycleWithEvidence(home, worktree, finalCommit, disposition string, dirty *DirtyWorktreeEvidence, supersession *SupersessionReceipt) error {
	projection, err := readWorkLogProjectionForClaim(home, worktree)
	if errors.Is(err, errWorkLogProjectionNotFound) {
		return nil // legacy pre-work-log checkout
	}
	if err != nil {
		return err
	}
	runDir, _, err := openWorkLogRun(home, projection.EffortID, projection.RunID, false)
	if err != nil {
		return fmt.Errorf("open private work-log run: %w", err)
	}
	defer func() { _ = runDir.Close() }()
	claimLock, err := lockClaim(runDir, projection.ClaimID)
	if err != nil {
		return err
	}
	defer claimLock()
	currentProjection, err := readWorkLogProjection(worktree)
	if err != nil || currentProjection != projection {
		return fmt.Errorf("work-log projection changed while waiting for claim fence")
	}
	claims, err := openPrivateChild(runDir, "claims", false)
	if err != nil {
		return err
	}
	defer func() { _ = claims.Close() }()
	var claim workLogClaim
	if err := readJSONAt(claims, projection.ClaimID+".json", &claim); err != nil {
		return fmt.Errorf("read immutable work-log claim: %w", err)
	}
	if err := corroborateClaim(worktree, finalCommit, projection, claim); err != nil {
		return err
	}
	var sealedAt time.Time
	if dirty != nil && supersession == nil {
		sealedAt, err = writeWorkLogTerminalWithDirtyCapture(home, runDir, claim, finalCommit, disposition, "", "", nil, dirty)
	} else if dirty == nil && supersession != nil {
		sealedAt, err = writeWorkLogTerminalWithSupersession(home, runDir, claim, finalCommit, disposition, "", "", nil, supersession)
	} else {
		sealedAt, err = writeWorkLogTerminalWithEvidence(home, runDir, claim, finalCommit, disposition, "", "", nil, nil, dirty, supersession)
	}
	if err != nil {
		return err
	}
	_ = sealedAt
	projection.Lifecycle = "terminal"
	return writeWorkLogProjection(worktree, projection)
}

// transferWorkLogClaim seals exactly one old claim and atomically rebinds the
// projection to one deterministic active successor. Dirty tracked state is
// intentionally untouched: handoff/not_landed is a control-plane transition,
// not discard. A crash before the final projection write is retryable because
// terminal and successor identities are immutable and deterministic.
func transferWorkLogClaim(home, worktree, finalCommit, disposition, successor string, identity ClaimExecutionIdentity) error {
	successor = strings.TrimSpace(successor)
	if successor == "" || len(successor) > 200 || strings.ContainsAny(successor, "\x00\r\n") {
		return fmt.Errorf("one successor agent/session ID is required for %s", disposition)
	}
	if err := validateNewExecutionIdentity(identity); err != nil {
		return err
	}
	projection, err := readWorkLogProjectionForClaim(home, worktree)
	if err != nil {
		return err
	}
	runDir, _, err := openWorkLogRun(home, projection.EffortID, projection.RunID, false)
	if err != nil {
		return err
	}
	defer func() { _ = runDir.Close() }()
	claimLock, err := lockClaim(runDir, projection.ClaimID)
	if err != nil {
		return err
	}
	defer claimLock()
	currentProjection, err := readWorkLogProjection(worktree)
	if err != nil || currentProjection != projection {
		return fmt.Errorf("work-log projection changed while waiting for claim fence")
	}
	claims, err := openPrivateChild(runDir, "claims", false)
	if err != nil {
		return err
	}
	defer func() { _ = claims.Close() }()
	var claim workLogClaim
	if err := readJSONAt(claims, projection.ClaimID+".json", &claim); err != nil {
		return err
	}
	if err := corroborateClaim(worktree, finalCommit, projection, claim); err != nil {
		return err
	}
	successorClaimID := declaredSuccessorWorkLogClaimID(claim.ClaimID, successor, disposition, identity)
	sealedAt, err := writeWorkLogTerminal(home, runDir, claim, finalCommit, disposition, successorClaimID, successor, nil)
	if err != nil {
		return err
	}
	successorClaim := claim
	successorClaim.Version = 2
	successorClaim.ClaimID = successorClaimID
	successorClaim.Lifecycle = "active"
	successorClaim.RecordedAt = sealedAt
	successorClaim.Initiator = successor
	successorClaim.AgentID = successor
	successorClaim.AgentRuntime = ""
	successorClaim.Model = strings.TrimSpace(identity.Model)
	successorClaim.ModelProvenance = modelProvenanceCallerDeclared
	if successorClaim.Model == "unknown" {
		successorClaim.ModelProvenance = modelProvenanceUnknown
	}
	successorClaim.ModelDeclaredBy = successor
	successorClaim.CLI = strings.TrimSpace(identity.CLI)
	successorClaim.Provider = strings.TrimSpace(identity.Provider)
	successorClaim.ParentClaimID = claim.ClaimID
	successorClaim.AcquiredVia = disposition
	if err := writeJSONImmutableAt(claims, successorClaimID+".json", successorClaim, true); err != nil {
		return fmt.Errorf("write immutable successor claim: %w", err)
	}
	outbox, err := openWorkLogOutbox(home, claim.EffortID, true)
	if err != nil {
		return err
	}
	defer func() { _ = outbox.Close() }()
	event := workLogPublicEvent{Version: 1, Type: "worktree.claimed", At: sealedAt,
		EffortID: claim.EffortID, RunID: claim.RunID, ClaimID: successorClaimID,
		Repository: claim.Repository, Branch: claim.Branch, Base: claim.Base,
		BaseSHA: claim.BaseSHA, Lifecycle: "active", Disposition: disposition}
	if err := writeJSONImmutableAt(outbox, claim.RunID+"-"+successorClaimID+"-claimed.json", event, true); err != nil {
		return fmt.Errorf("write successor outbox: %w", err)
	}
	projection.ClaimID = successorClaimID
	projection.Lifecycle = "active"
	return writeWorkLogProjection(worktree, projection)
}

// recoverFailedRecycleClaim gives a checkout moved back to its original path
// one new active recovery identity after the prior claim was durably sealed as
// recycled. It never revives the terminal claim. The deterministic successor
// makes a repeated rollback idempotent and keeps the old identity out of a new
// task path.
func recoverFailedRecycleClaim(home, worktree, finalCommit string, prior workLogProjection) error {
	runDir, _, err := openWorkLogRun(home, prior.EffortID, prior.RunID, false)
	if err != nil {
		return err
	}
	defer func() { _ = runDir.Close() }()
	claimLock, err := lockClaim(runDir, prior.ClaimID)
	if err != nil {
		return err
	}
	defer claimLock()
	claims, err := openPrivateChild(runDir, "claims", false)
	if err != nil {
		return err
	}
	defer func() { _ = claims.Close() }()
	var claim workLogClaim
	if err := readJSONAt(claims, prior.ClaimID+".json", &claim); err != nil {
		return err
	}
	if err := corroborateClaim(worktree, finalCommit, prior, claim); err != nil {
		return err
	}
	terminals, err := openPrivateChild(runDir, "terminals", false)
	if err != nil {
		return err
	}
	var terminal workLogTerminalRecord
	if err := readJSONAt(terminals, prior.ClaimID+".json", &terminal); err != nil {
		_ = terminals.Close()
		return fmt.Errorf("read failed-recycle terminal: %w", err)
	}
	_ = terminals.Close()
	if terminal.Disposition != "recycled" || terminal.FinalCommit != finalCommit || terminal.Lifecycle != "terminal" {
		return fmt.Errorf("prior claim is not a matching recycled terminal")
	}
	const recoveryAgent = "wb-recycle-recovery"
	const recoveryVia = "recycle_failed"
	recoveryID := successorWorkLogClaimID(prior.ClaimID, recoveryAgent, recoveryVia)
	recovery := claim
	recovery.Version = 2
	recovery.ClaimID = recoveryID
	recovery.Lifecycle = "active"
	recovery.RecordedAt = terminal.SealedAt
	recovery.Initiator = recoveryAgent
	recovery.AgentID = recoveryAgent
	recovery.AgentRuntime = ""
	recovery.Model = "unknown"
	recovery.ModelProvenance = modelProvenanceUnknown
	recovery.ModelDeclaredBy = recoveryAgent
	recovery.CLI = ""
	recovery.Provider = ""
	recovery.ParentClaimID = prior.ClaimID
	recovery.AcquiredVia = recoveryVia
	if err := writeJSONImmutableAt(claims, recoveryID+".json", recovery, true); err != nil {
		return err
	}
	outbox, err := openWorkLogOutbox(home, claim.EffortID, true)
	if err != nil {
		return err
	}
	defer func() { _ = outbox.Close() }()
	event := workLogPublicEvent{Version: 1, Type: "worktree.claimed", At: recovery.RecordedAt,
		EffortID: recovery.EffortID, RunID: recovery.RunID, ClaimID: recoveryID,
		Repository: recovery.Repository, Branch: recovery.Branch, Base: recovery.Base,
		BaseSHA: recovery.BaseSHA, Lifecycle: "active", Disposition: recoveryVia}
	if err := writeJSONImmutableAt(outbox, recovery.RunID+"-"+recoveryID+"-claimed.json", event, true); err != nil {
		return err
	}
	return writeWorkLogProjection(worktree, workLogProjection{Version: 1, EffortID: prior.EffortID, RunID: prior.RunID, ClaimID: recoveryID, Lifecycle: "active"})
}

func writeWorkLogTerminal(home string, runDir *os.File, claim workLogClaim, finalCommit, disposition, successorClaimID, successorAgentID string, external *workLogExternalHandoffEvidence) (time.Time, error) {
	return writeWorkLogTerminalWithEvidence(home, runDir, claim, finalCommit, disposition, successorClaimID, successorAgentID, external, nil, nil, nil)
}

func writeOrphanedWorkLogTerminal(home string, runDir *os.File, claim workLogClaim, evidence *workLogOrphanedEvidence) (time.Time, error) {
	if evidence == nil || evidence.Version != 1 || evidence.Actor == "" || evidence.Reason == "" ||
		!evidence.WorktreeAbsent || !evidence.RegistrationAbsent || !evidence.LocalBranchAbsent ||
		!evidence.RemoteBranchAbsent || !evidence.TerminalAbsent {
		return time.Time{}, fmt.Errorf("orphaned terminal requires complete negative authority evidence")
	}
	return writeWorkLogTerminalWithEvidence(home, runDir, claim, "", string(AbortOrphaned), "", "", nil, evidence, nil, nil)
}

func writeWorkLogTerminalWithDirtyCapture(home string, runDir *os.File, claim workLogClaim, finalCommit, disposition, successorClaimID, successorAgentID string, external *workLogExternalHandoffEvidence, dirty *DirtyWorktreeEvidence) (time.Time, error) {
	return writeWorkLogTerminalWithEvidence(home, runDir, claim, finalCommit, disposition, successorClaimID, successorAgentID, external, nil, dirty, nil)
}

func writeWorkLogTerminalWithSupersession(home string, runDir *os.File, claim workLogClaim, finalCommit, disposition, successorClaimID, successorAgentID string, external *workLogExternalHandoffEvidence, supersession *SupersessionReceipt) (time.Time, error) {
	return writeWorkLogTerminalWithEvidence(home, runDir, claim, finalCommit, disposition, successorClaimID, successorAgentID, external, nil, nil, supersession)
}

func writeWorkLogTerminalWithEvidence(home string, runDir *os.File, claim workLogClaim, finalCommit, disposition, successorClaimID, successorAgentID string, external *workLogExternalHandoffEvidence, orphaned *workLogOrphanedEvidence, dirty *DirtyWorktreeEvidence, supersession *SupersessionReceipt) (time.Time, error) {
	sealedAt := time.Now().UTC()
	claim.Lifecycle = "terminal"
	terminal := workLogTerminalRecord{workLogClaim: claim, FinalCommit: finalCommit,
		Disposition: disposition, SealedAt: sealedAt, SuccessorClaimID: successorClaimID, SuccessorAgentID: successorAgentID,
		ExternalHandoff: external, Orphaned: orphaned, DirtyCapture: dirty, Supersession: supersession}
	terminals, err := openPrivateChild(runDir, "terminals", true)
	if err != nil {
		return time.Time{}, err
	}
	defer func() { _ = terminals.Close() }()
	terminalName := claim.ClaimID + ".json"
	var existing workLogTerminalRecord
	if err := readJSONAt(terminals, terminalName, &existing); err == nil {
		if existing.ClaimID != claim.ClaimID || existing.FinalCommit != finalCommit || existing.Disposition != disposition || existing.Lifecycle != "terminal" || existing.SuccessorClaimID != successorClaimID || existing.SuccessorAgentID != successorAgentID ||
			!sameExternalHandoffEvidence(existing.ExternalHandoff, external) || !sameOrphanedEvidence(existing.Orphaned, orphaned) || !sameDirtyWorktreeEvidence(existing.DirtyCapture, dirty) || !sameSupersessionReceipt(existing.Supersession, supersession) {
			return time.Time{}, fmt.Errorf("immutable terminal conflicts with requested transition")
		}
		sealedAt = existing.SealedAt
	} else if !errors.Is(err, os.ErrNotExist) {
		return time.Time{}, fmt.Errorf("inspect immutable terminal: %w", err)
	} else if err := writeJSONImmutableAt(terminals, terminalName, terminal, false); err != nil {
		return time.Time{}, fmt.Errorf("write immutable terminal: %w", err)
	}
	outbox, err := openWorkLogOutbox(home, claim.EffortID, true)
	if err != nil {
		return time.Time{}, err
	}
	defer func() { _ = outbox.Close() }()
	event := workLogPublicEvent{Version: 1, Type: "worktree.sealed", At: sealedAt, EffortID: claim.EffortID,
		RunID: claim.RunID, ClaimID: claim.ClaimID, Repository: claim.Repository, Branch: claim.Branch,
		Base: claim.Base, BaseSHA: claim.BaseSHA, FinalCommit: finalCommit, Lifecycle: "terminal", Disposition: disposition,
		ExternalHandoff: external, DirtyCapture: dirty, Supersession: supersession}
	if err := writeJSONImmutableAt(outbox, claim.RunID+"-"+claim.ClaimID+"-sealed.json", event, true); err != nil {
		return time.Time{}, fmt.Errorf("write immutable terminal outbox: %w", err)
	}
	return sealedAt, nil
}

func sameOrphanedEvidence(left, right *workLogOrphanedEvidence) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func sameDirtyWorktreeEvidence(left, right *DirtyWorktreeEvidence) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// preflightWorkLogSeal resolves and corroborates the projection/claim/live-Git
// chain without writing. Coordinated operations run this for every repository
// before terminalizing the first claim.
func preflightWorkLogSeal(home, worktree, finalCommit string) error {
	projection, err := readWorkLogProjectionForClaim(home, worktree)
	if errors.Is(err, errWorkLogProjectionNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return corroborateWorkLogProjection(home, worktree, finalCommit, projection)
}

// preflightWorkLogClaimReadOnly corroborates a Work Log claim without
// migrating a legacy projection. Cleanup planning must remain read-only; apply
// repeats preflightWorkLogSeal while holding the task lock before terminalizing
// the claim or deleting Git state.
func preflightWorkLogClaimReadOnly(home, worktree, finalCommit string) error {
	projection, err := readWorkLogProjectionForReadOnlyClaim(worktree)
	if errors.Is(err, errWorkLogProjectionNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return corroborateWorkLogProjection(home, worktree, finalCommit, projection)
}

func corroborateWorkLogProjection(home, worktree, finalCommit string, projection workLogProjection) error {
	runDir, _, err := openWorkLogRun(home, projection.EffortID, projection.RunID, false)
	if err != nil {
		return err
	}
	defer func() { _ = runDir.Close() }()
	claims, err := openPrivateChild(runDir, "claims", false)
	if err != nil {
		return err
	}
	defer func() { _ = claims.Close() }()
	var claim workLogClaim
	if err := readJSONAt(claims, projection.ClaimID+".json", &claim); err != nil {
		return err
	}
	return corroborateClaim(worktree, finalCommit, projection, claim)
}

func readWorkLogProjection(worktree string) (workLogProjection, error) {
	var projection workLogProjection
	directory, err := openWorkLogProjectionDirectory(worktree, false)
	if err != nil {
		return projection, err
	}
	defer func() { _ = directory.Close() }()
	if err := readJSONAt(directory, workLogProjectionName, &projection); err != nil {
		return projection, fmt.Errorf("decode work-log projection: %w", err)
	}
	if err := validateProjection(projection); err != nil {
		return projection, err
	}
	return projection, nil
}

// readWorkLogProjectionForClaim performs the one-way migration from the
// short-lived .wb-worklog.json pointer used by the first Hybrid Work Log
// implementation. The editable legacy pointer is never trusted: WB first
// validates its opaque IDs, loads the immutable private claim, and
// corroborates that claim against live Git. Only then does it write the
// approved .wb-worklog/recovery.json projection and unlink the old pointer.
func readWorkLogProjectionForClaim(home, worktree string) (workLogProjection, error) {
	projection, currentErr := readWorkLogProjection(worktree)
	legacy, legacyErr := readLegacyWorkLogProjection(worktree)
	switch {
	case currentErr == nil:
		if legacyErr == nil {
			if legacy != projection {
				return workLogProjection{}, fmt.Errorf("legacy and current work-log projections disagree")
			}
			if err := corroborateProjectionWithPrivateClaim(home, worktree, projection); err != nil {
				return workLogProjection{}, err
			}
			if err := removeLegacyWorkLogProjection(worktree); err != nil {
				return workLogProjection{}, err
			}
		} else if !errors.Is(legacyErr, os.ErrNotExist) {
			return workLogProjection{}, legacyErr
		}
		return projection, nil
	case !errors.Is(currentErr, os.ErrNotExist):
		return workLogProjection{}, currentErr
	case errors.Is(legacyErr, os.ErrNotExist):
		return workLogProjection{}, errWorkLogProjectionNotFound
	case legacyErr != nil:
		return workLogProjection{}, legacyErr
	}
	if err := corroborateProjectionWithPrivateClaim(home, worktree, legacy); err != nil {
		return workLogProjection{}, fmt.Errorf("corroborate legacy work-log projection: %v", err)
	}
	if err := writeWorkLogProjection(worktree, legacy); err != nil {
		return workLogProjection{}, fmt.Errorf("migrate legacy work-log projection: %w", err)
	}
	if err := removeLegacyWorkLogProjection(worktree); err != nil {
		return workLogProjection{}, err
	}
	return legacy, nil
}

// readWorkLogProjectionForReadOnlyClaim selects the same current or legacy
// projection that apply can corroborate, but never writes the current
// projection or removes the legacy pointer.
func readWorkLogProjectionForReadOnlyClaim(worktree string) (workLogProjection, error) {
	projection, currentErr := readWorkLogProjection(worktree)
	legacy, legacyErr := readLegacyWorkLogProjection(worktree)
	switch {
	case currentErr == nil:
		if legacyErr == nil && legacy != projection {
			return workLogProjection{}, fmt.Errorf("legacy and current work-log projections disagree")
		}
		if legacyErr != nil && !errors.Is(legacyErr, os.ErrNotExist) {
			return workLogProjection{}, legacyErr
		}
		return projection, nil
	case !errors.Is(currentErr, os.ErrNotExist):
		return workLogProjection{}, currentErr
	case errors.Is(legacyErr, os.ErrNotExist):
		return workLogProjection{}, errWorkLogProjectionNotFound
	case legacyErr != nil:
		return workLogProjection{}, legacyErr
	default:
		return legacy, nil
	}
}

func readLegacyWorkLogProjection(worktree string) (workLogProjection, error) {
	var projection workLogProjection
	root, err := openAbsoluteDirectoryNoFollow(worktree, false)
	if err != nil {
		return projection, err
	}
	defer func() { _ = root.Close() }()
	if err := readJSONAt(root, legacyWorkLogProjectionName, &projection); err != nil {
		return projection, err
	}
	if err := validateProjection(projection); err != nil {
		return projection, err
	}
	return projection, nil
}

func corroborateProjectionWithPrivateClaim(home, worktree string, projection workLogProjection) error {
	runDir, _, err := openWorkLogRun(home, projection.EffortID, projection.RunID, false)
	if err != nil {
		return err
	}
	defer func() { _ = runDir.Close() }()
	claims, err := openPrivateChild(runDir, "claims", false)
	if err != nil {
		return err
	}
	defer func() { _ = claims.Close() }()
	var claim workLogClaim
	if err := readJSONAt(claims, projection.ClaimID+".json", &claim); err != nil {
		return err
	}
	head, err := git(context.Background(), worktree, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	return corroborateClaim(worktree, head, projection, claim)
}

func corroborateClaim(worktree, finalCommit string, projection workLogProjection, claim workLogClaim) error {
	if (claim.Version != 1 && claim.Version != 2) || claim.EffortID != projection.EffortID || claim.RunID != projection.RunID || claim.ClaimID != projection.ClaimID || claim.Lifecycle != "active" {
		return fmt.Errorf("work-log projection does not match immutable active claim")
	}
	if !validSafeSegment(claim.EffortID) || !validSafeSegment(claim.RunID) || !validClaimID(claim.ClaimID) || filepath.Clean(claim.Worktree) != filepath.Clean(worktree) {
		return fmt.Errorf("private work-log claim identity/path mismatch")
	}
	wantID := workLogClaimID(claim.EffortID, CreateResult{Repository: claim.Repository, WorktreeDir: claim.Worktree, Branch: claim.Branch, Base: claim.Base, BaseSHA: claim.BaseSHA})
	if claim.ParentClaimID != "" {
		if !validClaimID(claim.ParentClaimID) || claim.AgentID == "" || (claim.AcquiredVia != "handoff" && claim.AcquiredVia != "not_landed" && claim.AcquiredVia != "recycle_failed" && claim.AcquiredVia != "external_handoff" && claim.AcquiredVia != "parked_session_resume") {
			return fmt.Errorf("private successor claim metadata is invalid")
		}
		if claim.AcquiredVia == "external_handoff" {
			var externalErr error
			wantID, externalErr = expectedExternalClaimID(claim)
			if externalErr != nil {
				return externalErr
			}
		} else if claim.AcquiredVia == "parked_session_resume" {
			var parkedErr error
			wantID, parkedErr = expectedParkedSessionClaimID(claim)
			if parkedErr != nil {
				return parkedErr
			}
		} else if claim.Version == 2 && claim.AcquiredVia != "recycle_failed" {
			wantID = declaredSuccessorWorkLogClaimID(claim.ParentClaimID, claim.AgentID, claim.AcquiredVia,
				ClaimExecutionIdentity{Model: claim.Model, CLI: claim.CLI, Provider: claim.Provider})
		} else {
			wantID = successorWorkLogClaimID(claim.ParentClaimID, claim.AgentID, claim.AcquiredVia)
		}
	}
	if claim.AcquiredVia != "external_handoff" && claim.AcquiredVia != "parked_session_resume" && claim.ExternalHandoff != nil {
		return fmt.Errorf("ordinary private claim carries unexpected external handoff evidence")
	}
	if wantID != claim.ClaimID {
		return fmt.Errorf("private work-log claim digest mismatch")
	}
	branch, err := git(context.Background(), worktree, "branch", "--show-current")
	if err != nil || branch != claim.Branch {
		// #183: the proven recovery is renaming the live branch back to the
		// claim name. Landing evidence is commit-based (see corroborateClaim's
		// own HEAD/base checks below and Cleanup's PR-containment proof), so a
		// PR already opened from the renamed branch still proves out once the
		// name matches again — this is a pure message change, not a relaxed
		// check.
		return fmt.Errorf("live branch %q does not match private claim %q; recovery: rename the live branch back to the claim name (git branch -m %s) — landing evidence is commit-based, so a PR already opened from the renamed branch still proves out once the name matches again", branch, claim.Branch, claim.Branch)
	}
	head, err := git(context.Background(), worktree, "rev-parse", "HEAD")
	if err != nil || head != finalCommit {
		return fmt.Errorf("live HEAD %q does not match terminal commit %q", head, finalCommit)
	}
	if !isGitObjectID(claim.BaseSHA) {
		return fmt.Errorf("private claim has invalid base SHA")
	}
	if _, err := git(context.Background(), worktree, "merge-base", "--is-ancestor", claim.BaseSHA, head); err != nil {
		return fmt.Errorf("live HEAD is not descended from claimed base %s: %w", claim.BaseSHA, err)
	}
	if claim.Version == 2 {
		identity := identityFromClaim(claim)
		if !validExecutionIdentifier(identity.Model, true) ||
			(identity.CLI != "" && !validExecutionIdentifier(identity.CLI, false)) ||
			(identity.Provider != "" && !validExecutionIdentifier(identity.Provider, false)) ||
			(identity.ModelProvenance != modelProvenanceCallerDeclared && identity.ModelProvenance != modelProvenanceRuntimeObserved && identity.ModelProvenance != modelProvenanceUnknown) {
			return fmt.Errorf("private claim has invalid execution identity metadata")
		}
	}
	return nil
}

func removeWorkLogProjection(worktree string) error {
	directory, err := openWorkLogProjectionDirectory(worktree, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := unix.Unlinkat(int(directory.Fd()), workLogProjectionName, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		_ = directory.Close()
		return fmt.Errorf("reset old work-log projection: %w", err)
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		return err
	}
	_ = directory.Close()
	root, err := openAbsoluteDirectoryNoFollow(worktree, false)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err := unix.Unlinkat(int(root.Fd()), workLogProjectionDirectory, unix.AT_REMOVEDIR); err != nil && !errors.Is(err, unix.ENOENT) && !errors.Is(err, unix.ENOTEMPTY) {
		return fmt.Errorf("remove empty work-log projection directory: %w", err)
	}
	return nil
}

func removeLegacyWorkLogProjection(worktree string) error {
	root, err := openAbsoluteDirectoryNoFollow(worktree, false)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err := unix.Unlinkat(int(root.Fd()), legacyWorkLogProjectionName, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("remove legacy work-log projection: %w", err)
	}
	return root.Sync()
}

func openWorkLogRun(home, effort, run string, create bool) (*os.File, string, error) {
	if !validSafeSegment(effort) || !validSafeSegment(run) {
		return nil, "", fmt.Errorf("invalid work-log effort/run identity")
	}
	homeDir, err := openAbsoluteDirectoryNoFollow(home, create)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = homeDir.Close() }()
	current := homeDir
	for _, segment := range []string{"worklogs", effort, "runs", run} {
		next, openErr := openPrivateChild(current, segment, create)
		if current != homeDir {
			_ = current.Close()
		}
		if openErr != nil {
			return nil, "", openErr
		}
		current = next
	}
	return current, filepath.Join(home, "worklogs", effort, "runs", run), nil
}

func openWorkLogOutbox(home, effort string, create bool) (*os.File, error) {
	if !validSafeSegment(effort) {
		return nil, fmt.Errorf("invalid work-log effort identity")
	}
	homeDir, err := openAbsoluteDirectoryNoFollow(home, create)
	if err != nil {
		return nil, err
	}
	defer func() { _ = homeDir.Close() }()
	worklogs, err := openPrivateChild(homeDir, "worklogs", create)
	if err != nil {
		return nil, err
	}
	defer func() { _ = worklogs.Close() }()
	effortDir, err := openPrivateChild(worklogs, effort, create)
	if err != nil {
		return nil, err
	}
	defer func() { _ = effortDir.Close() }()
	return openPrivateChild(effortDir, "outbox", create)
}

func openPrivateChild(parent *os.File, name string, create bool) (*os.File, error) {
	if !validSafeSegment(name) {
		return nil, fmt.Errorf("unsafe private directory segment %q", name)
	}
	var fd int
	var err error
	if create {
		fd, err = openOrCreateNoFollowDirectory(int(parent.Fd()), name)
	} else {
		fd, err = unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	}
	if err != nil {
		return nil, err
	}
	if err := unix.Fchmod(fd, 0o700); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "wb-worklog-"+name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("wrap private directory %s", name)
	}
	return file, nil
}

func lockClaim(runDir *os.File, claimID string) (func(), error) {
	locks, err := openPrivateChild(runDir, "locks", true)
	if err != nil {
		return nil, fmt.Errorf("open claim-lock directory: %w", err)
	}
	fd, err := unix.Openat(int(locks.Fd()), claimID+".lock", unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW, 0o600)
	if errors.Is(err, unix.EEXIST) {
		// Two first-time claimers can race the lock-file publication. Separate
		// create from open so the loser deterministically opens the winner's
		// no-follow regular entry instead of Darwin returning a transient ENOENT
		// from concurrent O_CREAT|O_NOFOLLOW calls.
		fd, err = unix.Openat(int(locks.Fd()), claimID+".lock", unix.O_RDWR|unix.O_NOFOLLOW, 0)
	}
	_ = locks.Close()
	if err != nil {
		return nil, fmt.Errorf("open claim-lock file: %w", err)
	}
	if err := unix.Flock(fd, unix.LOCK_EX); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	return func() { _ = unix.Flock(fd, unix.LOCK_UN); _ = unix.Close(fd) }, nil
}

func writeJSONImmutableAt(directory *os.File, name string, value any, idempotent bool) error {
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	return writeBytesImmutableAt(directory, name, content, 0o600, idempotent)
}

func writeBytesImmutableAt(directory *os.File, name string, content []byte, mode os.FileMode, idempotent bool) error {
	if strings.Contains(name, "/") || name == "" || name == "." || name == ".." {
		return fmt.Errorf("unsafe immutable filename %q", name)
	}
	if existing, err := readBytesAt(directory, name); err == nil {
		if idempotent && bytes.Equal(existing, content) {
			return nil
		}
		return fmt.Errorf("immutable file already exists: %s", name)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return err
	}
	temporary := "." + name + ".tmp-" + hex.EncodeToString(random)
	fd, err := unix.Openat(int(directory.Fd()), temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW, uint32(mode.Perm()))
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), temporary)
	cleanup := true
	defer func() {
		_ = file.Close()
		if cleanup {
			_ = unix.Unlinkat(int(directory.Fd()), temporary, 0)
		}
	}()
	if _, err := file.Write(content); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := renameNoReplace(int(directory.Fd()), temporary, int(directory.Fd()), name); err != nil {
		if existing, readErr := readBytesAt(directory, name); idempotent && readErr == nil && bytes.Equal(existing, content) {
			return nil
		}
		return err
	}
	cleanup = false
	return directory.Sync()
}

func readBytesAt(directory *os.File, name string) ([]byte, error) {
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, os.ErrNotExist
		}
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer func() { _ = file.Close() }()
	return io.ReadAll(file)
}

func readJSONAt(directory *os.File, name string, target any) error {
	content, err := readBytesAt(directory, name)
	if err != nil {
		return err
	}
	return json.Unmarshal(content, target)
}

func writeJSONAtomic(path string, value any, mode os.FileMode) error {
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	return writeBytesAtomic(filepath.Dir(path), filepath.Base(path), append(content, '\n'), mode)
}

func writeJSONAtomicAt(directory *os.File, name string, value any, mode os.FileMode) error {
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeBytesAtomicAt(directory, name, append(content, '\n'), mode)
}

func writeBytesAtomicAt(directory *os.File, name string, content []byte, mode os.FileMode) error {
	if directory == nil || strings.Contains(name, "/") || name == "" || name == "." || name == ".." {
		return fmt.Errorf("unsafe atomic filename %q", name)
	}
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return err
	}
	temporary := "." + name + ".tmp-" + hex.EncodeToString(random)
	fd, err := unix.Openat(int(directory.Fd()), temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW, uint32(mode.Perm()))
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), temporary)
	cleanup := true
	defer func() {
		_ = file.Close()
		if cleanup {
			_ = unix.Unlinkat(int(directory.Fd()), temporary, 0)
		}
	}()
	if _, err := file.Write(content); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := unix.Renameat(int(directory.Fd()), temporary, int(directory.Fd()), name); err != nil {
		return err
	}
	cleanup = false
	return directory.Sync()
}

func writeBytesAtomic(directory, name string, content []byte, mode os.FileMode) error {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, "."+name+".tmp-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
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
	if err := os.Rename(temporaryName, filepath.Join(directory, name)); err != nil {
		return err
	}
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}
