package worktreeclaims

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/worktreejournal"
	"gopkg.in/yaml.v3"
)

// Ports are bound to one facade operation. No Git or publication state is global.
type Ports struct {
	RecordOwner           func(string, string, string, string, int) error
	CurrentPID            func() int
	Git                   func(context.Context, string, ...string) (string, error)
	OriginSlug            func(context.Context, string) (string, error)
	EnsureExclude         func(string, []string, string) error
	ReadBytesAt           func(*os.File, string) ([]byte, error)
	WriteBytesImmutableAt func(*os.File, string, []byte, os.FileMode, bool) error
	EncodeManifest        func(Manifest) ([]byte, error)
	EncodePromptHeader    func(PromptHeader) ([]byte, error)
	ReadNames             func(*os.File) ([]string, error)
	Rewind                func(*os.File) error
}

type AdmissionMode string

const (
	AdmissionOff     AdmissionMode = "off"
	AdmissionWarn    AdmissionMode = "warn"
	AdmissionEnforce AdmissionMode = "enforce"
)

type Admission struct {
	Mode     AdmissionMode `json:"mode"`
	Admitted bool          `json:"admitted"`
	Reason   string        `json:"reason,omitempty"`
	Remedy   string        `json:"remedy,omitempty"`
}

func (p Ports) RepositoryRootFor(ctx context.Context, path string) (string, error) {
	root, err := p.Git(ctx, path, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("resolve worktree root for %s: %w", path, err)
	}
	return filepath.Clean(root), nil
}

func (p Ports) CheckAdmission(worktree string, mode AdmissionMode) Admission {
	admission := Admission{Mode: mode, Admitted: true}
	if mode == AdmissionOff {
		return admission
	}
	remedy := fmt.Sprintf("record what you were asked to do: wb worktree set --prompt=\"...\" %s", worktree)

	manifest, err := p.ReadManifest(worktree)
	switch {
	case errors.Is(err, ErrManifestNotFound):
		admission.Reason = "this worktree has no WB manifest, so nothing records what it is or who asked for it"
		admission.Remedy = remedy
	case err != nil:
		admission.Reason = fmt.Sprintf("this worktree's manifest cannot be read: %v", err)
		admission.Remedy = remedy
	default:
		prompts, promptErr := p.ListPrompts(worktree)
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

func (p Ports) EnsureJournalExclude(worktree string) error {
	return p.EnsureExclude(worktree, []string{journalExcludeRule}, "exclude work-log journal")
}

func (p Ports) WriteManifest(worktree string, manifest Manifest) error {
	if err := ValidateManifest(manifest); err != nil {
		return err
	}
	if err := p.EnsureJournalExclude(worktree); err != nil {
		return err
	}
	directory, err := worktreejournal.OpenJournalDirectory(worktree, true)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()

	if _, err := p.ReadBytesAt(directory, manifestName); err == nil {
		return fmt.Errorf("worktree manifest already exists at %s; a manifest is immutable", worktree)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	encode := p.EncodeManifest
	if encode == nil {
		encode = func(value Manifest) ([]byte, error) { return yaml.Marshal(value) }
	}
	encoded, err := encode(manifest)
	if err != nil {
		return fmt.Errorf("encode worktree manifest: %w", err)
	}
	// Immutable publication must be no-replace at the filesystem boundary.
	// A check followed by replace-capable rename lets two first writers both
	// succeed and silently destroys one creation authority.
	return p.WriteBytesImmutableAt(directory, manifestName, encoded, 0o600, false)
}

func (p Ports) EnsureManifest(worktree string, manifest Manifest) error {
	if err := p.WriteManifest(worktree, manifest); err != nil {
		if strings.Contains(err.Error(), "immutable") {
			return nil
		}
		return err
	}
	return nil
}

func (p Ports) EnsurePrompt(worktree string, header PromptHeader, body []byte) error {
	existing, err := p.ListPrompts(worktree)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil
	}
	_, err = p.AppendPrompt(worktree, header, body)
	return err
}

func (p Ports) ReadManifest(worktree string) (Manifest, error) {
	directory, err := worktreejournal.OpenJournalDirectory(worktree, false)
	if errors.Is(err, os.ErrNotExist) {
		return Manifest{}, ErrManifestNotFound
	}
	if err != nil {
		return Manifest{}, err
	}
	defer func() { _ = directory.Close() }()

	content, err := p.ReadBytesAt(directory, manifestName)
	if errors.Is(err, os.ErrNotExist) {
		return Manifest{}, ErrManifestNotFound
	}
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := yaml.Unmarshal(content, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("parse worktree manifest at %s: %w", worktree, err)
	}
	if err := ValidateManifest(manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func (p Ports) ReconstructManifest(ctx context.Context, worktree string) (Manifest, error) {
	if existing, err := p.ReadManifest(worktree); err == nil {
		return existing, nil
	} else if !errors.Is(err, ErrManifestNotFound) {
		return Manifest{}, err
	}
	manifest, err := p.ComputeReconstructedManifest(ctx, worktree)
	if err != nil {
		return Manifest{}, err
	}
	if err := p.WriteManifest(worktree, manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func (p Ports) PreviewReconstructedManifest(ctx context.Context, worktree string) (Manifest, error) {
	if existing, err := p.ReadManifest(worktree); err == nil {
		return existing, nil
	} else if !errors.Is(err, ErrManifestNotFound) {
		return Manifest{}, err
	}
	return p.ComputeReconstructedManifest(ctx, worktree)
}

func (p Ports) ComputeReconstructedManifest(ctx context.Context, worktree string) (Manifest, error) {
	branch, err := p.Git(ctx, worktree, "branch", "--show-current")
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
	repository, err := p.OriginSlug(ctx, worktree)
	if err != nil || strings.TrimSpace(repository) == "" {
		repository = RepositoryFromWorktreePath(worktree)
		if repository == "" {
			return Manifest{}, fmt.Errorf(
				"cannot identify the repository for %s: it has no origin remote and its path does not carry owner/repository", worktree,
			)
		}
		inferred = append(inferred, "repository")
		evidence = append(evidence, "repository from the worktree path; the checkout has no usable origin remote")
	}

	effort := EffortFromWorktreePath(worktree)
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
		CreatedAt:    p.ReconstructCreationTime(ctx, worktree, branch),
		Provenance:   ProvenanceReconstructed,
	}
	if base, sha, ok := p.ReconstructBase(ctx, worktree, branch); ok {
		manifest.Base, manifest.BaseSHA = base, sha
		inferred = append(inferred, "base", "base_sha")
		evidence = append(evidence, "base from the merge-base with the default remote target")
	}
	manifest.InferredFields = inferred
	manifest.Evidence = evidence
	return manifest, nil
}

func (p Ports) ReconstructCreationTime(ctx context.Context, worktree, branch string) time.Time {
	if out, err := p.Git(ctx, worktree, "reflog", "show", "--date=iso-strict", "--format=%gd %gs %cI", branch); err == nil {
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if last := strings.TrimSpace(lines[len(lines)-1]); last != "" {
			fields := strings.Fields(last)
			if parsed, err := time.Parse(time.RFC3339, fields[len(fields)-1]); err == nil {
				return parsed.UTC()
			}
		}
	}
	if out, err := p.Git(ctx, worktree, "log", "--reverse", "--format=%cI", "-1", branch); err == nil {
		if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(out)); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

func (p Ports) ReconstructBase(ctx context.Context, worktree, branch string) (string, string, bool) {
	for _, candidate := range []string{"origin/main", "origin/master"} {
		sha, err := p.Git(ctx, worktree, "merge-base", candidate, branch)
		if err != nil || strings.TrimSpace(sha) == "" {
			continue
		}
		return strings.TrimPrefix(candidate, "origin/"), strings.TrimSpace(sha), true
	}
	return "", "", false
}

func (p Ports) AppendPrompt(worktree string, header PromptHeader, body []byte) (string, error) {
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
	if err := p.EnsureJournalExclude(worktree); err != nil {
		return "", err
	}
	directory, err := worktreejournal.OpenJournalSubdirectory(worktree, promptsDirectory, true)
	if err != nil {
		return "", err
	}
	defer func() { _ = directory.Close() }()
	unlock, err := LockJournalSequence(directory, ".prompts.lock")
	if err != nil {
		return "", err
	}
	defer unlock()

	existing, err := p.ListPromptsIn(directory)
	if err != nil {
		return "", err
	}
	header.Seq = len(existing)
	if header.At.IsZero() {
		header.At = time.Now().UTC()
	}
	digest := sha256.Sum256(body)
	header.SHA256 = hex.EncodeToString(digest[:])

	slug := PromptSlug(header.Slug, body)
	name := fmt.Sprintf(promptOrdinalFmt+"-%s.md", header.Seq, slug)
	if !promptFileName.MatchString(name) {
		return "", fmt.Errorf("derived prompt file name %q is not valid", name)
	}
	encode := p.EncodePromptHeader
	if encode == nil {
		encode = func(value PromptHeader) ([]byte, error) { return yaml.Marshal(value) }
	}
	frontmatter, err := encode(header)
	if err != nil {
		return "", fmt.Errorf("encode prompt frontmatter: %w", err)
	}
	content := append([]byte("---\n"), frontmatter...)
	content = append(content, []byte("---\n\n")...)
	content = append(content, body...)
	if content[len(content)-1] != '\n' {
		content = append(content, '\n')
	}
	if err := p.WriteBytesImmutableAt(directory, name, content, 0o600, false); err != nil {
		return "", err
	}
	return name, nil
}

func (p Ports) ListPrompts(worktree string) ([]PromptHeader, error) {
	directory, err := worktreejournal.OpenJournalSubdirectory(worktree, promptsDirectory, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = directory.Close() }()
	return p.ListPromptsIn(directory)
}

func (p Ports) ListPromptsIn(directory *os.File) ([]PromptHeader, error) {
	readNames := p.ReadNames
	if readNames == nil {
		readNames = func(dir *os.File) ([]string, error) { return dir.Readdirnames(-1) }
	}
	names, err := readNames(directory)
	if err != nil {
		return nil, fmt.Errorf("read prompt sequence: %w", err)
	}
	rewind := p.Rewind
	if rewind == nil {
		rewind = func(dir *os.File) error { _, err := dir.Seek(0, 0); return err }
	}
	if err := rewind(directory); err != nil {
		return nil, fmt.Errorf("rewind prompt sequence: %w", err)
	}
	sort.Strings(names)

	headers := make([]PromptHeader, 0, len(names))
	for _, name := range names {
		match := promptFileName.FindStringSubmatch(name)
		if match == nil {
			continue
		}
		ordinal, _ := strconv.Atoi(match[1]) // exactly four ASCII digits
		content, err := p.ReadBytesAt(directory, name)
		if err != nil {
			return nil, err
		}
		header, err := ParsePromptHeader(content)
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

type CreationResult struct {
	Repository, WorktreeDir, Branch, Base, BaseSHA string
}

func (p Ports) WriteCreationJournal(effort, run, claimID string, result CreationResult, options Options, now time.Time) error {
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
	if err := p.WriteManifest(result.WorktreeDir, manifest); err != nil {
		// A worktree resumed onto an existing journal already carries its
		// creation record, and that record is immutable by design.
		if !strings.Contains(err.Error(), "immutable") {
			return err
		}
	}
	if err := p.RecordOwner(result.WorktreeDir, effort, OwnerAgent(options.AgentRuntime, options.AgentID), strings.TrimSpace(options.Model), p.CurrentPID()); err != nil {
		return err
	}
	if len(options.Snapshot.Contents) == 0 {
		return nil
	}
	existing, err := p.ListPrompts(result.WorktreeDir)
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
	if _, err = p.AppendPrompt(result.WorktreeDir, header, options.Snapshot.Contents); err != nil {
		return err
	}
	return nil
}
