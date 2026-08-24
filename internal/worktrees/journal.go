package worktrees

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	"gopkg.in/yaml.v3"
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
	journalRootDirectory  = ".wb"
	journalLocalDirectory = "local"
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

var errManifestNotFound = errors.New("worktree manifest not found")

var promptFileName = regexp.MustCompile(`^([0-9]{4})-[A-Za-z0-9][A-Za-z0-9._-]*\.md$`)

// Manifest is written once, when the worktree is created, and never rewritten.
// A later correction is appended to the journal rather than edited in place.
type Manifest struct {
	Version      int       `yaml:"version"`
	EffortID     string    `yaml:"effort_id"`
	ParentEffort string    `yaml:"parent_effort,omitempty"`
	EffortKind   string    `yaml:"effort_kind"`
	Repository   string    `yaml:"repository"`
	Worktree     string    `yaml:"worktree"`
	Branch       string    `yaml:"branch"`
	Base         string    `yaml:"base"`
	BaseSHA      string    `yaml:"base_sha"`
	CreatedAt    time.Time `yaml:"created_at"`
	Initiator    string    `yaml:"initiator,omitempty"`
	AgentID      string    `yaml:"agent_id,omitempty"`
	AgentRuntime string    `yaml:"agent_runtime,omitempty"`
	Model        string    `yaml:"model,omitempty"`
	CLI          string    `yaml:"cli,omitempty"`
	Provider     string    `yaml:"provider,omitempty"`
	RunID        string    `yaml:"run_id,omitempty"`
	ClaimID      string    `yaml:"claim_id,omitempty"`
	Provenance   string    `yaml:"provenance"`

	// InferredFields and Evidence are populated only for a reconstructed
	// manifest, so a reader can see exactly which values were guessed and from
	// what. They stay empty for provenance: created.
	InferredFields []string `yaml:"inferred_fields,omitempty"`
	Evidence       []string `yaml:"evidence,omitempty"`
}

// PromptHeader is the frontmatter of one recorded instruction. The body is held
// separately because it is private local data that must never reach public
// output, reports, hook metrics, or a sync envelope.
type PromptHeader struct {
	Seq      int       `yaml:"seq"`
	At       time.Time `yaml:"at"`
	SHA256   string    `yaml:"sha256"`
	Source   string    `yaml:"source"`
	Runtime  string    `yaml:"runtime,omitempty"`
	Model    string    `yaml:"model,omitempty"`
	CLI      string    `yaml:"cli,omitempty"`
	Provider string    `yaml:"provider,omitempty"`
	Slug     string    `yaml:"-"`
}

// ValidEffortPath accepts a dot-separated effort path of unbounded depth. Dots
// carry parentage, so an empty component, a leading or trailing dot, and an
// over-long path are all rejected rather than normalized: a silently repaired
// identity is worse than a refused one.
func ValidEffortPath(value string) bool {
	if value == "" || len(value) > 200 {
		return false
	}
	if strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") {
		return false
	}
	for _, segment := range strings.Split(value, ".") {
		if segment == "" || !validSafeSegment(segment) {
			return false
		}
	}
	return true
}

// ParentEffort returns the lexical parent of an effort path, or "" for a root
// effort. Parentage is derivable without reading any manifest so an orphan
// family can be grouped even when every manifest is missing.
func ParentEffort(value string) string {
	index := strings.LastIndex(value, ".")
	if index <= 0 {
		return ""
	}
	return value[:index]
}

// EffortKindFor reports whether an effort path names a feature or a task. A
// nested path is a task effort owned by the feature effort at its root.
func EffortKindFor(value string) string {
	if ParentEffort(value) == "" {
		return EffortKindFeature
	}
	return EffortKindTask
}

// IsAncestorEffort reports whether ancestor is a proper prefix segment of
// descendant, so cleanup can refuse a parent while any child is still live.
func IsAncestorEffort(ancestor, descendant string) bool {
	return ancestor != "" && strings.HasPrefix(descendant, ancestor+".")
}

// RepositoryRootFor resolves the working-tree root that owns a path, so a
// caller standing anywhere inside a checkout records against that checkout's
// journal rather than creating a stray one in a subdirectory.
func RepositoryRootFor(ctx context.Context, path string) (string, error) {
	root, err := git(ctx, path, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("resolve worktree root for %s: %w", path, err)
	}
	return filepath.Clean(root), nil
}

// AdmissionMode selects whether a missing journal refuses a commit or is only
// reported. Warn exists so a fleet with unattended sessions can adopt
// enforcement without a flag day: a rollout that depends on stopping agents
// cannot be verified to have stopped them.
type AdmissionMode string

const (
	AdmissionOff     AdmissionMode = "off"
	AdmissionWarn    AdmissionMode = "warn"
	AdmissionEnforce AdmissionMode = "enforce"
)

// Admission reports whether a worktree may accept a commit and why not.
type Admission struct {
	Mode     AdmissionMode `json:"mode"`
	Admitted bool          `json:"admitted"`
	Reason   string        `json:"reason,omitempty"`
	Remedy   string        `json:"remedy,omitempty"`
}

// CheckAdmission decides whether a WB-managed worktree carries the record a
// commit requires: a valid manifest and at least one recorded instruction.
//
// It binds on the worktree's location alone and never inspects environment
// markers to tell an agent from a human. A marker that can be absent — a
// subshell, a wrapper, a script — fails open exactly when it matters, which
// would make the gate an illusion rather than a control.
func CheckAdmission(worktree string, mode AdmissionMode) Admission {
	admission := Admission{Mode: mode, Admitted: true}
	if mode == AdmissionOff {
		return admission
	}
	remedy := fmt.Sprintf("record what you were asked to do: wb worktree set --prompt=\"...\" %s", worktree)

	manifest, err := ReadManifest(worktree)
	switch {
	case errors.Is(err, errManifestNotFound):
		admission.Reason = "this worktree has no WB manifest, so nothing records what it is or who asked for it"
		admission.Remedy = remedy
	case err != nil:
		admission.Reason = fmt.Sprintf("this worktree's manifest cannot be read: %v", err)
		admission.Remedy = remedy
	default:
		prompts, promptErr := ListPrompts(worktree)
		switch {
		case promptErr != nil:
			admission.Reason = fmt.Sprintf("this worktree's prompt sequence cannot be read: %v", promptErr)
			admission.Remedy = remedy
		case len(prompts) == 0:
			admission.Reason = fmt.Sprintf(
				"effort %q has no recorded instruction, so this commit would have no record of who directed it",
				manifest.EffortID,
			)
			admission.Remedy = remedy
		}
	}
	if admission.Reason != "" && mode == AdmissionEnforce {
		admission.Admitted = false
	}
	return admission
}

// openJournalDirectory resolves <worktree>/.wb/local one component at a time
// with O_NOFOLLOW at every level, so neither .wb nor local can be swapped for a
// symlink pointing outside the worktree between checks.
func openJournalDirectory(worktree string, create bool) (*os.File, error) {
	root, err := openAbsoluteDirectoryNoFollow(worktree, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()

	wbFD, err := openJournalComponent(int(root.Fd()), journalRootDirectory, create)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(wbFD) }()

	localFD, err := openJournalComponent(wbFD, journalLocalDirectory, create)
	if err != nil {
		return nil, err
	}
	// Only the WB-owned directory is tightened. The repository's own .wb holds
	// tracked policy files whose mode belongs to the repository, not to WB.
	if err := unix.Fchmod(localFD, 0o700); err != nil {
		_ = unix.Close(localFD)
		return nil, err
	}
	directory := os.NewFile(uintptr(localFD), "wb-journal")
	if directory == nil {
		_ = unix.Close(localFD)
		return nil, fmt.Errorf("wrap work-log journal directory")
	}
	path := filepath.Join(worktree, journalRootDirectory, journalLocalDirectory)
	if !directoryStillMatches(path, directory) {
		_ = directory.Close()
		return nil, fmt.Errorf("work-log journal directory path changed: %s", path)
	}
	return directory, nil
}

func openJournalComponent(parentFD int, name string, create bool) (int, error) {
	var fd int
	var err error
	if create {
		fd, err = openOrCreateNoFollowDirectory(parentFD, name)
	} else {
		fd, err = unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	}
	if errors.Is(err, unix.ENOENT) {
		return 0, os.ErrNotExist
	}
	if err != nil {
		return 0, fmt.Errorf("open work-log journal component %s: %w", name, err)
	}
	return fd, nil
}

// openJournalSubdirectory opens prompts/ or worklog/ below the journal root.
func openJournalSubdirectory(worktree, name string, create bool) (*os.File, error) {
	journal, err := openJournalDirectory(worktree, create)
	if err != nil {
		return nil, err
	}
	defer func() { _ = journal.Close() }()

	fd, err := openJournalComponent(int(journal.Fd()), name, create)
	if err != nil {
		return nil, err
	}
	if err := unix.Fchmod(fd, 0o700); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	directory := os.NewFile(uintptr(fd), "wb-journal-"+name)
	if directory == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("wrap work-log journal %s directory", name)
	}
	return directory, nil
}

// ensureJournalExclude adds the single `/.wb/local/` rule to the repository's
// local exclude mechanism. It never edits the shared .gitignore, which belongs
// to the team rather than to this machine.
func ensureJournalExclude(worktree string) error {
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
	if strings.Contains("\n"+string(exclude)+"\n", "\n"+journalExcludeRule+"\n") {
		return nil
	}
	updated := append(exclude, []byte("\n"+journalExcludeRule+"\n")...)
	if err := writeBytesAtomic(filepath.Dir(gitPath), filepath.Base(gitPath), updated, 0o600); err != nil {
		return fmt.Errorf("exclude work-log journal: %w", err)
	}
	return nil
}

// WriteManifest creates the immutable creation record. It refuses to replace an
// existing manifest: a second write would destroy the very evidence the file
// exists to preserve.
func WriteManifest(worktree string, manifest Manifest) error {
	if err := validateManifest(manifest); err != nil {
		return err
	}
	if err := ensureJournalExclude(worktree); err != nil {
		return err
	}
	directory, err := openJournalDirectory(worktree, true)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()

	if _, err := readBytesAt(directory, manifestName); err == nil {
		return fmt.Errorf("worktree manifest already exists at %s; a manifest is immutable", worktree)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	encoded, err := yaml.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("encode worktree manifest: %w", err)
	}
	return writeBytesAtomicAt(directory, manifestName, encoded, 0o600)
}

// ReadManifest loads the creation record from the worktree alone.
func ReadManifest(worktree string) (Manifest, error) {
	directory, err := openJournalDirectory(worktree, false)
	if errors.Is(err, os.ErrNotExist) {
		return Manifest{}, errManifestNotFound
	}
	if err != nil {
		return Manifest{}, err
	}
	defer func() { _ = directory.Close() }()

	content, err := readBytesAt(directory, manifestName)
	if errors.Is(err, os.ErrNotExist) {
		return Manifest{}, errManifestNotFound
	}
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := yaml.Unmarshal(content, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("parse worktree manifest at %s: %w", worktree, err)
	}
	if err := validateManifest(manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func validateManifest(manifest Manifest) error {
	if manifest.Version != 1 {
		return fmt.Errorf("unsupported worktree manifest version %d", manifest.Version)
	}
	if !ValidEffortPath(manifest.EffortID) {
		return fmt.Errorf("invalid manifest effort id %q", manifest.EffortID)
	}
	if manifest.ParentEffort != "" && manifest.ParentEffort != ParentEffort(manifest.EffortID) {
		return fmt.Errorf(
			"manifest parent effort %q contradicts effort path %q; the path is authoritative",
			manifest.ParentEffort, manifest.EffortID,
		)
	}
	switch manifest.EffortKind {
	case EffortKindFeature, EffortKindTask:
	default:
		return fmt.Errorf("invalid manifest effort kind %q", manifest.EffortKind)
	}
	switch manifest.Provenance {
	case ProvenanceCreated:
		if len(manifest.InferredFields) != 0 {
			return fmt.Errorf("a created manifest cannot record inferred fields")
		}
	case ProvenanceReconstructed:
		if len(manifest.InferredFields) == 0 {
			return fmt.Errorf("a reconstructed manifest must record which fields were inferred")
		}
	default:
		return fmt.Errorf("invalid manifest provenance %q", manifest.Provenance)
	}
	if strings.TrimSpace(manifest.Branch) == "" || strings.TrimSpace(manifest.Repository) == "" {
		return fmt.Errorf("manifest must record repository and branch")
	}
	return nil
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
	if !ValidEffortPath(effort) {
		return nil
	}
	manifest := Manifest{
		Version:      1,
		EffortID:     effort,
		ParentEffort: ParentEffort(effort),
		EffortKind:   EffortKindFor(effort),
		Repository:   result.Repository,
		Worktree:     result.WorktreeDir,
		Branch:       result.Branch,
		Base:         result.Base,
		BaseSHA:      result.BaseSHA,
		CreatedAt:    now,
		Initiator:    strings.TrimSpace(options.Initiator),
		AgentID:      strings.TrimSpace(options.AgentID),
		AgentRuntime: strings.TrimSpace(options.AgentRuntime),
		Model:        strings.TrimSpace(options.Model),
		CLI:          strings.TrimSpace(options.CLI),
		Provider:     strings.TrimSpace(options.Provider),
		RunID:        run,
		ClaimID:      claimID,
		Provenance:   ProvenanceCreated,
	}
	if err := WriteManifest(result.WorktreeDir, manifest); err != nil {
		// A worktree resumed onto an existing journal already carries its
		// creation record, and that record is immutable by design.
		if !strings.Contains(err.Error(), "immutable") {
			return err
		}
	}
	if _, err := recordOwner(result.WorktreeDir, effort, ownerAgent(options.AgentRuntime, options.AgentID), strings.TrimSpace(options.Model), CurrentIdentity().PID); err != nil {
		return err
	}
	if len(options.originalPromptContents) == 0 {
		return nil
	}
	existing, err := ListPrompts(result.WorktreeDir)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil
	}
	header := PromptHeader{
		At:       now,
		Source:   PromptSourceAgent,
		Runtime:  strings.TrimSpace(options.AgentRuntime),
		Model:    strings.TrimSpace(options.Model),
		CLI:      strings.TrimSpace(options.CLI),
		Provider: strings.TrimSpace(options.Provider),
		Slug:     "initial",
	}
	if strings.TrimSpace(options.Initiator) != "" {
		header.Source = PromptSourceHuman
	}
	if _, err = AppendPrompt(result.WorktreeDir, header, options.originalPromptContents); err != nil {
		return err
	}
	return nil
}

func ownerAgent(runtime, agentID string) string {
	if runtime = strings.TrimSpace(runtime); runtime != "" {
		return runtime
	}
	return strings.TrimSpace(agentID)
}

// ReconstructManifest derives a manifest for a worktree that predates the
// journal, using Git evidence alone, and records exactly which fields were
// inferred and from what.
//
// It never fabricates a prompt. A worktree whose instructions were never
// recorded genuinely has none, and inventing one would put a lie in the only
// record a successor can trust. The admission gate's remedy is how such a
// worktree acquires its first real instruction.
func ReconstructManifest(ctx context.Context, worktree string) (Manifest, error) {
	if existing, err := ReadManifest(worktree); err == nil {
		return existing, nil
	} else if !errors.Is(err, errManifestNotFound) {
		return Manifest{}, err
	}
	branch, err := git(ctx, worktree, "branch", "--show-current")
	if err != nil {
		return Manifest{}, fmt.Errorf("reconstruct manifest: %w", err)
	}
	if strings.TrimSpace(branch) == "" {
		return Manifest{}, fmt.Errorf("cannot reconstruct a manifest for a detached HEAD at %s", worktree)
	}
	inferred := []string{"effort_id", "effort_kind", "created_at"}
	evidence := []string{
		"effort_id from the managed worktree path, else the branch name",
		"created_at from the branch's earliest reflog entry, else its oldest commit",
	}

	// A worktree whose origin was removed or was never set is exactly the kind
	// of damaged checkout triage exists for. Falling back to the path keeps it
	// explicable instead of refusing to describe it at all.
	repository, err := OriginSlug(ctx, worktree)
	if err != nil || strings.TrimSpace(repository) == "" {
		repository = repositoryFromWorktreePath(worktree)
		if repository == "" {
			return Manifest{}, fmt.Errorf(
				"cannot identify the repository for %s: it has no origin remote and its path does not carry owner/repository", worktree,
			)
		}
		inferred = append(inferred, "repository")
		evidence = append(evidence, "repository from the worktree path; the checkout has no usable origin remote")
	}

	effort := effortFromWorktreePath(worktree)
	if effort == "" {
		effort = strings.TrimPrefix(branch, "feature/")
		effort = strings.ReplaceAll(effort, "/", ".")
	}
	if !ValidEffortPath(effort) {
		return Manifest{}, fmt.Errorf(
			"cannot derive a valid effort path for %s from path or branch %q", worktree, branch,
		)
	}

	manifest := Manifest{
		Version:      1,
		EffortID:     effort,
		ParentEffort: ParentEffort(effort),
		EffortKind:   EffortKindFor(effort),
		Repository:   repository,
		Worktree:     worktree,
		Branch:       branch,
		CreatedAt:    reconstructCreationTime(ctx, worktree, branch),
		Provenance:   ProvenanceReconstructed,
	}
	if base, sha, ok := reconstructBase(ctx, worktree, branch); ok {
		manifest.Base, manifest.BaseSHA = base, sha
		inferred = append(inferred, "base", "base_sha")
		evidence = append(evidence, "base from the merge-base with the default remote target")
	}
	manifest.InferredFields = inferred
	manifest.Evidence = evidence
	if err := WriteManifest(worktree, manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// effortFromWorktreePath recovers the effort segment of a managed worktree path
// laid out as <worktrees-root>/<effort>/<owner>/<repository>.
func effortFromWorktreePath(worktree string) string {
	parts := strings.Split(filepath.ToSlash(filepath.Clean(worktree)), "/")
	if len(parts) < 3 {
		return ""
	}
	candidate := parts[len(parts)-3]
	if !ValidEffortPath(candidate) {
		return ""
	}
	return candidate
}

// repositoryFromWorktreePath reads owner/repository from a managed layout of
// <worktrees-root>/<effort>/<owner>/<repository>, falling back to the checkout's
// own directory name when the path carries no owner.
func repositoryFromWorktreePath(worktree string) string {
	parts := strings.Split(filepath.ToSlash(filepath.Clean(worktree)), "/")
	if len(parts) >= 2 {
		owner, name := parts[len(parts)-2], parts[len(parts)-1]
		if validSafeSegment(owner) && validRepositorySegment(name) {
			return owner + "/" + name
		}
	}
	if len(parts) >= 1 && validRepositorySegment(parts[len(parts)-1]) {
		return "unknown/" + parts[len(parts)-1]
	}
	return ""
}

func reconstructCreationTime(ctx context.Context, worktree, branch string) time.Time {
	if out, err := git(ctx, worktree, "reflog", "show", "--date=iso-strict", "--format=%gd %gs %cI", branch); err == nil {
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if last := strings.TrimSpace(lines[len(lines)-1]); last != "" {
			fields := strings.Fields(last)
			if parsed, err := time.Parse(time.RFC3339, fields[len(fields)-1]); err == nil {
				return parsed.UTC()
			}
		}
	}
	if out, err := git(ctx, worktree, "log", "--reverse", "--format=%cI", "-1", branch); err == nil {
		if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(out)); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

func reconstructBase(ctx context.Context, worktree, branch string) (string, string, bool) {
	for _, candidate := range []string{"origin/main", "origin/master"} {
		sha, err := git(ctx, worktree, "merge-base", candidate, branch)
		if err != nil || strings.TrimSpace(sha) == "" {
			continue
		}
		return strings.TrimPrefix(candidate, "origin/"), strings.TrimSpace(sha), true
	}
	return "", "", false
}

// AppendPrompt records one instruction at the next ordinal. Body bytes are
// stored exactly; only the digest and ordinal may ever enter public state.
func AppendPrompt(worktree string, header PromptHeader, body []byte) (string, error) {
	if len(body) == 0 {
		return "", fmt.Errorf("a prompt must record the exact instruction and cannot be empty")
	}
	switch header.Source {
	case PromptSourceHarness, PromptSourceAgent, PromptSourceHuman:
	default:
		return "", fmt.Errorf(
			"prompt source must be one of %s, %s, %s; it is recorded, never inferred",
			PromptSourceHarness, PromptSourceAgent, PromptSourceHuman,
		)
	}
	if err := ensureJournalExclude(worktree); err != nil {
		return "", err
	}
	directory, err := openJournalSubdirectory(worktree, promptsDirectory, true)
	if err != nil {
		return "", err
	}
	defer func() { _ = directory.Close() }()

	existing, err := listPromptsIn(directory)
	if err != nil {
		return "", err
	}
	header.Seq = len(existing)
	if header.At.IsZero() {
		header.At = time.Now().UTC()
	}
	digest := sha256.Sum256(body)
	header.SHA256 = hex.EncodeToString(digest[:])

	slug := promptSlug(header.Slug, body)
	name := fmt.Sprintf(promptOrdinalFmt+"-%s.md", header.Seq, slug)
	if !promptFileName.MatchString(name) {
		return "", fmt.Errorf("derived prompt file name %q is not valid", name)
	}
	frontmatter, err := yaml.Marshal(header)
	if err != nil {
		return "", fmt.Errorf("encode prompt frontmatter: %w", err)
	}
	content := append([]byte("---\n"), frontmatter...)
	content = append(content, []byte("---\n\n")...)
	content = append(content, body...)
	if content[len(content)-1] != '\n' {
		content = append(content, '\n')
	}
	if err := writeBytesAtomicAt(directory, name, content, 0o600); err != nil {
		return "", err
	}
	return name, nil
}

// ListPrompts returns the recorded instruction headers in ordinal order. Bodies
// are deliberately not returned: callers that render status must not be handed
// private prompt text by default.
func ListPrompts(worktree string) ([]PromptHeader, error) {
	directory, err := openJournalSubdirectory(worktree, promptsDirectory, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = directory.Close() }()
	return listPromptsIn(directory)
}

func listPromptsIn(directory *os.File) ([]PromptHeader, error) {
	names, err := directory.Readdirnames(-1)
	if err != nil {
		return nil, fmt.Errorf("read prompt sequence: %w", err)
	}
	if _, err := directory.Seek(0, 0); err != nil {
		return nil, fmt.Errorf("rewind prompt sequence: %w", err)
	}
	sort.Strings(names)

	headers := make([]PromptHeader, 0, len(names))
	for _, name := range names {
		match := promptFileName.FindStringSubmatch(name)
		if match == nil {
			continue
		}
		ordinal, err := strconv.Atoi(match[1])
		if err != nil {
			continue
		}
		content, err := readBytesAt(directory, name)
		if err != nil {
			return nil, err
		}
		header, err := parsePromptHeader(content)
		if err != nil {
			return nil, fmt.Errorf("prompt %s: %w", name, err)
		}
		if header.Seq != ordinal {
			return nil, fmt.Errorf(
				"prompt %s records seq %d but its ordinal is %d", name, header.Seq, ordinal,
			)
		}
		headers = append(headers, header)
	}
	sort.Slice(headers, func(i, j int) bool { return headers[i].Seq < headers[j].Seq })
	for index, header := range headers {
		if header.Seq != index {
			return nil, fmt.Errorf(
				"prompt sequence is not contiguous: expected ordinal %d, found %d", index, header.Seq,
			)
		}
	}
	return headers, nil
}

func parsePromptHeader(content []byte) (PromptHeader, error) {
	text := string(content)
	if !strings.HasPrefix(text, "---\n") {
		return PromptHeader{}, fmt.Errorf("missing YAML frontmatter")
	}
	end := strings.Index(text[4:], "\n---\n")
	if end < 0 {
		return PromptHeader{}, fmt.Errorf("unterminated YAML frontmatter")
	}
	var header PromptHeader
	if err := yaml.Unmarshal([]byte(text[4:4+end+1]), &header); err != nil {
		return PromptHeader{}, fmt.Errorf("parse frontmatter: %w", err)
	}
	switch header.Source {
	case PromptSourceHarness, PromptSourceAgent, PromptSourceHuman:
	default:
		return PromptHeader{}, fmt.Errorf("invalid prompt source %q", header.Source)
	}
	return header, nil
}

// promptSlug derives a short, filesystem-safe hint from the instruction so a
// directory listing is readable without opening files. It is a convenience, not
// an identifier: the ordinal orders the sequence and the digest identifies it.
func promptSlug(explicit string, body []byte) string {
	candidate := explicit
	if candidate == "" {
		line := strings.TrimSpace(string(body))
		if index := strings.IndexAny(line, "\r\n"); index >= 0 {
			line = line[:index]
		}
		candidate = line
	}
	var builder strings.Builder
	previousDash := false
	for _, r := range strings.ToLower(candidate) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			builder.WriteRune(r)
			previousDash = false
		default:
			if !previousDash && builder.Len() > 0 {
				builder.WriteByte('-')
				previousDash = true
			}
		}
		if builder.Len() >= 40 {
			break
		}
	}
	slug := strings.Trim(builder.String(), "-")
	if slug == "" {
		return "prompt"
	}
	return slug
}
