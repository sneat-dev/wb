package worktrees

import (
	"context"
	"os"
	"regexp"
	"time"

	"github.com/sneat-dev/wb/internal/worktreeclaims"
	"github.com/sneat-dev/wb/internal/worktreejournal"
)

// The live journal lives inside the worktree it describes so an abandoned
// checkout can be triaged from its own contents, with no canonical clone, WB
// home, or index required. A pointer cannot do that: when a worktree is
// orphaned, the external record is exactly what has gone missing.
//
// The excluded path is deliberately `.wb/local/` and never `.wb/`. A
// repository's own `.wb/hooks.yaml` and `.wb/templates/` are tracked team
// policy; excluding their parent would silently swallow newly added policy
// files, which is the kind of bug that costs an hour to find.
const (
	journalRootDirectory  = worktreejournal.JournalRootDirectory
	journalLocalDirectory = worktreejournal.JournalLocalDirectory
	journalExcludeRule    = "/.wb/local/"

	manifestName      = "manifest.yaml"
	promptsDirectory  = "prompts"
	worklogDirectory  = "worklog"
	promptOrdinalFmt  = "%04d"
	promptOrdinalSize = 4
)

// ManifestProvenance distinguishes a record of creation from an inference made
// later. Triage must never mistake one for the other.
const (
	ProvenanceCreated       = "created"
	ProvenanceReconstructed = "reconstructed"
)

// PromptSource is recorded, never inferred. A prompt captured by a harness hook
// is harness_observed, one an agent reports about itself is agent_declared, and
// one a person supplies at the terminal is human_declared.
const (
	PromptSourceHarness = "harness_observed"
	PromptSourceAgent   = "agent_declared"
	PromptSourceHuman   = "human_declared"
)

// EffortKind separates a durable feature effort from a task effort a sub-agent
// owns below it.
const (
	EffortKindFeature = "feature"
	EffortKindTask    = "task"
)

var errManifestNotFound = worktreeclaims.ErrManifestNotFound

var promptFileName = regexp.MustCompile(`^([0-9]{4})-[A-Za-z0-9][A-Za-z0-9._-]*\.md$`)

// Manifest is written once, when the worktree is created, and never rewritten.
// A later correction is appended to the journal rather than edited in place.
type Manifest = worktreeclaims.Manifest

// PromptHeader is the frontmatter of one recorded instruction. The body is held
// separately because it is private local data that must never reach public
// output, reports, hook metrics, or a sync envelope.
type PromptHeader = worktreeclaims.PromptHeader

// ValidEffortPath accepts a dot-separated effort path of unbounded depth. Dots
// carry parentage, so an empty component, a leading or trailing dot, and an
// over-long path are all rejected rather than normalized: a silently repaired
// identity is worse than a refused one.
func ValidEffortPath(value string) bool { return worktreeclaims.ValidEffortPath(value) }

// ParentEffort returns the lexical parent of an effort path, or "" for a root
// effort. Parentage is derivable without reading any manifest so an orphan
// family can be grouped even when every manifest is missing.
func ParentEffort(value string) string { return worktreeclaims.ParentEffort(value) }

// EffortKindFor reports whether an effort path names a feature or a task. A
// nested path is a task effort owned by the feature effort at its root.
func EffortKindFor(value string) string { return worktreeclaims.EffortKindFor(value) }

// IsAncestorEffort reports whether ancestor is a proper prefix segment of
// descendant, so cleanup can refuse a parent while any child is still live.
func IsAncestorEffort(ancestor, descendant string) bool {
	return worktreeclaims.IsAncestorEffort(ancestor, descendant)
}

// RepositoryRootFor resolves the working-tree root that owns a path, so a
// caller standing anywhere inside a checkout records against that checkout's
// journal rather than creating a stray one in a subdirectory.
func RepositoryRootFor(ctx context.Context, path string) (string, error) {
	return claimJournalPorts().RepositoryRootFor(ctx, path)
}

// AdmissionMode selects whether a missing journal refuses a commit or is only
// reported. Warn exists so a fleet with unattended sessions can adopt
// enforcement without a flag day: a rollout that depends on stopping agents
// cannot be verified to have stopped them.
type AdmissionMode = worktreeclaims.AdmissionMode

const (
	AdmissionOff     = worktreeclaims.AdmissionOff
	AdmissionWarn    = worktreeclaims.AdmissionWarn
	AdmissionEnforce = worktreeclaims.AdmissionEnforce
)

type Admission = worktreeclaims.Admission

// CheckAdmission decides whether a WB-managed worktree carries the record a
// commit requires: a valid manifest and at least one recorded instruction.
//
// It binds on the worktree's location alone and never inspects environment
// markers to tell an agent from a human. A marker that can be absent — a
// subshell, a wrapper, a script — fails open exactly when it matters, which
// would make the gate an illusion rather than a control.
func CheckAdmission(worktree string, mode AdmissionMode) Admission {
	return claimJournalPorts().CheckAdmission(worktree, mode)
}

// openJournalDirectory resolves <worktree>/.wb/local one component at a time
// with O_NOFOLLOW at every level, so neither .wb nor local can be swapped for a
// symlink pointing outside the worktree between checks.
func openJournalDirectory(worktree string, create bool) (*os.File, error) {
	return worktreejournal.OpenJournalDirectory(worktree, create)
}

func openJournalComponent(parentFD int, name string, create bool) (int, error) {
	return worktreejournal.OpenJournalComponent(parentFD, name, create)
}

// openJournalSubdirectory opens prompts/ or worklog/ below the journal root.
func openJournalSubdirectory(worktree, name string, create bool) (*os.File, error) {
	return worktreejournal.OpenJournalSubdirectory(worktree, name, create)
}

// ensureJournalExclude adds the single `/.wb/local/` rule to the repository's
// local exclude mechanism. It never edits the shared .gitignore, which belongs
// to the team rather than to this machine.
func ensureJournalExclude(worktree string) error {
	return claimJournalPorts().EnsureJournalExclude(worktree)
}

// WriteManifest creates the immutable creation record. It refuses to replace an
// existing manifest: a second write would destroy the very evidence the file
// exists to preserve.
func WriteManifest(worktree string, manifest Manifest) error {
	return claimJournalPorts().WriteManifest(worktree, manifest)
}

// EnsureManifest writes the creation manifest for a worktree that some
// caller other than `wb worktree create` assembled directly — most notably
// an internal orchestration engine (deps bump/set wave processing) that
// creates a worktree with `git worktree add` rather than through wb's own
// CLI. It is idempotent: a worktree that already carries a manifest (most
// commonly a --resume'd operation) is left untouched, since a manifest is
// immutable by design — this only ever fills in a genuinely missing record,
// it never second-guesses one already written.
func EnsureManifest(worktree string, manifest Manifest) error {
	return claimJournalPorts().EnsureManifest(worktree, manifest)
}

// EnsurePrompt records header/body as the worktree's originating instruction
// unless one is already recorded. See EnsureManifest for why this must be
// idempotent rather than erroring on a second call.
func EnsurePrompt(worktree string, header PromptHeader, body []byte) error {
	return claimJournalPorts().EnsurePrompt(worktree, header, body)
}

// ReadManifest loads the creation record from the worktree alone.
func ReadManifest(worktree string) (Manifest, error) {
	return claimJournalPorts().ReadManifest(worktree)
}

// writeCreationJournal publishes the immutable manifest and, when the caller
// supplied the originating instruction, records it as prompt ordinal 0000.
//
// It is deliberately tolerant of an effort ID that predates effort paths: an
// identifier WB cannot express as a path still gets a worktree, it just gets no
// manifest, and the commit gate's remedy is how that worktree acquires one.
// Refusing to create the worktree instead would strand real work over a naming
// rule introduced after the fact.
func writeCreationJournal(effort, run, claimID string, result CreateResult, options WorkLogOptions, now time.Time) error {
	return claimJournalPorts().WriteCreationJournal(effort, run, claimID,
		worktreeclaims.CreationResult{Repository: result.Repository, WorktreeDir: result.WorktreeDir, Branch: result.Branch, Base: result.Base, BaseSHA: result.BaseSHA},
		toClaimOptions(options), now)
}

func ownerAgent(runtime, agentID string) string { return worktreeclaims.OwnerAgent(runtime, agentID) }

// ReconstructManifest derives a manifest for a worktree that predates the
// journal, using Git evidence alone, and records exactly which fields were
// inferred and from what.
//
// It never fabricates a prompt. A worktree whose instructions were never
// recorded genuinely has none, and inventing one would put a lie in the only
// record a successor can trust. The admission gate's remedy is how such a
// worktree acquires its first real instruction.
func ReconstructManifest(ctx context.Context, worktree string) (Manifest, error) {
	return claimJournalPorts().ReconstructManifest(ctx, worktree)
}

// PreviewReconstructedManifest returns exactly what ReconstructManifest would
// return, without ever writing to disk: a worktree that already has a
// manifest gets that manifest back unchanged (there is nothing to preview —
// it is already persisted), and one that doesn't gets the same reconstruction
// held only in memory. `wb worktree adopt`'s dry run uses this so it can
// report an accurate effort/repository/branch preview without the side effect
// a manifest write would be.
func PreviewReconstructedManifest(ctx context.Context, worktree string) (Manifest, error) {
	return claimJournalPorts().PreviewReconstructedManifest(ctx, worktree)
}

// effortFromWorktreePath recovers an effort from either supported physical
// placement: the default <canonical-repository>/.worktrees/<effort>, or a
// shared <worktrees-root>/<effort>/<owner>/<repository> path.
func effortFromWorktreePath(worktree string) string {
	return worktreeclaims.EffortFromWorktreePath(worktree)
}

// AppendPrompt records one instruction at the next ordinal. Body bytes are
// stored exactly; only the digest and ordinal may ever enter public state.
func AppendPrompt(worktree string, header PromptHeader, body []byte) (string, error) {
	return claimJournalPorts().AppendPrompt(worktree, header, body)
}

// ListPrompts returns the recorded instruction headers in ordinal order. Bodies
// are deliberately not returned: callers that render status must not be handed
// private prompt text by default.
func ListPrompts(worktree string) ([]PromptHeader, error) {
	return claimJournalPorts().ListPrompts(worktree)
}

func parsePromptHeader(content []byte) (PromptHeader, error) {
	return worktreeclaims.ParsePromptHeader(content)
}

// claimJournalPorts binds this facade's Git, exclusion, and immutable file I/O to one operation.
func claimJournalPorts() worktreeclaims.Ports {
	return worktreeclaims.Ports{
		Git:                   git,
		OriginSlug:            OriginSlug,
		EnsureExclude:         ensurePerWorktreeGitExclude,
		ReadBytesAt:           readBytesAt,
		WriteBytesImmutableAt: writeBytesImmutableAt,
		RecordOwner: func(worktree, effort, agent, model string, pid int) error {
			_, err := recordOwner(worktree, effort, agent, model, pid)
			return err
		},
		CurrentPID: func() int { return CurrentIdentity().PID },
	}
}
