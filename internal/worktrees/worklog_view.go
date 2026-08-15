package worktrees

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
)

// PromptRecord is one recorded instruction plus its private body. Bodies are
// local-only data: only this agent-facing dump hands them to a caller.
type PromptRecord struct {
	Name     string    `json:"name"`
	Seq      int       `json:"seq"`
	At       time.Time `json:"at"`
	SHA256   string    `json:"sha256"`
	Source   string    `json:"source"`
	Runtime  string    `json:"runtime,omitempty"`
	Model    string    `json:"model,omitempty"`
	CLI      string    `json:"cli,omitempty"`
	Provider string    `json:"provider,omitempty"`
	Body     string    `json:"body,omitempty"`
}

// WorkLogClaimView is the public-enough claim identity an agent needs to
// continue work. It never includes the archived prompt body; that lives in
// OriginalPrompt / Prompts.
type WorkLogClaimView struct {
	EffortID        string    `json:"effort_id"`
	RunID           string    `json:"run_id"`
	ClaimID         string    `json:"claim_id"`
	Task            string    `json:"task,omitempty"`
	Repository      string    `json:"repository"`
	Worktree        string    `json:"worktree"`
	Branch          string    `json:"branch"`
	Base            string    `json:"base"`
	BaseSHA         string    `json:"base_sha"`
	Lifecycle       string    `json:"lifecycle"`
	RecordedAt      time.Time `json:"recorded_at"`
	Initiator       string    `json:"initiator,omitempty"`
	AgentID         string    `json:"agent_id,omitempty"`
	AgentRuntime    string    `json:"agent_runtime,omitempty"`
	Model           string    `json:"model,omitempty"`
	ModelProvenance string    `json:"model_provenance,omitempty"`
	CLI             string    `json:"cli,omitempty"`
	Provider        string    `json:"provider,omitempty"`
	PromptDigest    string    `json:"prompt_sha256,omitempty"`
	PromptArchive   string    `json:"prompt_archive,omitempty"`
	ClaimPath       string    `json:"claim_path,omitempty"`
}

// WorkLogGitEvidence is live checkout state observed when the dump is taken.
type WorkLogGitEvidence struct {
	Branch string `json:"branch,omitempty"`
	Head   string `json:"head,omitempty"`
	Dirty  bool   `json:"dirty"`
	Status string `json:"status_short,omitempty"`
}

// OriginalPromptView is the effort's originating instruction. Prefer the
// journal ordinal 0000 body; fall back to the immutable WB_HOME archive.
type OriginalPromptView struct {
	Source string `json:"source"` // journal | archive
	Name   string `json:"name,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	Body   string `json:"body,omitempty"`
}

// WorkLogView is the agent bootstrap payload for one worktree: identity, the
// exact initial prompt, every later steering instruction, and live Git state.
type WorkLogView struct {
	Worktree       string              `json:"worktree"`
	Manifest       *Manifest           `json:"manifest,omitempty"`
	Prompts        []PromptRecord      `json:"prompts"`
	OriginalPrompt *OriginalPromptView `json:"original_prompt,omitempty"`
	Claim          *WorkLogClaimView   `json:"claim,omitempty"`
	Git            WorkLogGitEvidence  `json:"git"`
	Notes          []string            `json:"notes,omitempty"`
}

// LoadWorkLogOptions selects which private records to include. Agents need
// prompt bodies; a future redacted show path can leave IncludePromptBodies
// false without a second code path for identity.
type LoadWorkLogOptions struct {
	ProjectsRoot        string
	Worktree            string
	IncludePromptBodies bool
}

// LoadWorkLogView assembles the local recovery record an agent needs to resume
// work. It is read-only with respect to Git state and prompt archives. The
// only mutation it may perform is the existing one-way legacy projection
// migration that activeWorkLogClaim already performs when corroborating a
// claim.
func LoadWorkLogView(ctx context.Context, options LoadWorkLogOptions) (WorkLogView, error) {
	worktree := strings.TrimSpace(options.Worktree)
	if worktree == "" {
		worktree = "."
	}
	root, err := RepositoryRootFor(ctx, worktree)
	if err != nil {
		return WorkLogView{}, err
	}
	view := WorkLogView{Worktree: root, Prompts: []PromptRecord{}}

	if manifest, manifestErr := ReadManifest(root); manifestErr == nil {
		copy := manifest
		view.Manifest = &copy
	} else if !errors.Is(manifestErr, errManifestNotFound) {
		return WorkLogView{}, manifestErr
	} else {
		view.Notes = append(view.Notes, "no .wb/local/manifest.yaml; record one with wb worktree set or recreate under a valid effort path")
	}

	prompts, err := listPromptRecords(root, options.IncludePromptBodies)
	if err != nil {
		return WorkLogView{}, err
	}
	view.Prompts = prompts
	if len(prompts) == 0 {
		view.Notes = append(view.Notes, "no recorded prompts under .wb/local/prompts/; supply one with wb worktree set --prompt")
	}

	home, homeErr := wbhome.Root(options.ProjectsRoot)
	if homeErr != nil {
		view.Notes = append(view.Notes, fmt.Sprintf("could not resolve WB home: %v", homeErr))
	} else if claim, _, claimPath, claimErr := activeWorkLogClaim(home, root); claimErr == nil {
		view.Claim = &WorkLogClaimView{
			EffortID: claim.EffortID, RunID: claim.RunID, ClaimID: claim.ClaimID,
			Task: claim.Task, Repository: claim.Repository, Worktree: claim.Worktree,
			Branch: claim.Branch, Base: claim.Base, BaseSHA: claim.BaseSHA,
			Lifecycle: claim.Lifecycle, RecordedAt: claim.RecordedAt,
			Initiator: claim.Initiator, AgentID: claim.AgentID, AgentRuntime: claim.AgentRuntime,
			Model: claim.Model, ModelProvenance: claim.ModelProvenance,
			CLI: claim.CLI, Provider: claim.Provider,
			PromptDigest: claim.PromptDigest, PromptArchive: claim.PromptArchive,
			ClaimPath: claimPath,
		}
		if options.IncludePromptBodies {
			if original, originalErr := loadOriginalPrompt(home, claim, prompts); originalErr == nil {
				view.OriginalPrompt = original
			} else if originalErr != nil {
				view.Notes = append(view.Notes, fmt.Sprintf("original prompt unavailable: %v", originalErr))
			}
		}
	} else if errors.Is(claimErr, errWorkLogProjectionNotFound) {
		view.Notes = append(view.Notes, "no active work-log projection; this checkout may predate Hybrid Work Log create")
	} else {
		view.Notes = append(view.Notes, fmt.Sprintf("active work-log claim not usable: %v", claimErr))
	}

	if view.OriginalPrompt == nil && options.IncludePromptBodies && len(prompts) > 0 {
		first := prompts[0]
		view.OriginalPrompt = &OriginalPromptView{
			Source: "journal",
			Name:   first.Name,
			SHA256: first.SHA256,
			Body:   first.Body,
		}
	}

	view.Git = observeWorkLogGit(ctx, root)
	return view, nil
}

func loadOriginalPrompt(home string, claim workLogClaim, prompts []PromptRecord) (*OriginalPromptView, error) {
	if len(prompts) > 0 && prompts[0].Body != "" {
		return &OriginalPromptView{
			Source: "journal",
			Name:   prompts[0].Name,
			SHA256: prompts[0].SHA256,
			Body:   prompts[0].Body,
		}, nil
	}
	archive := strings.TrimSpace(claim.PromptArchive)
	if archive == "" {
		archive = "original-prompt.txt"
	}
	if strings.Contains(archive, string(filepath.Separator)) || archive == "." || archive == ".." {
		return nil, fmt.Errorf("refusing unsafe prompt archive name %q", archive)
	}
	runDir, _, err := openWorkLogRun(home, claim.EffortID, claim.RunID, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = runDir.Close() }()
	contents, err := readBytesAt(runDir, archive)
	if err != nil {
		return nil, err
	}
	return &OriginalPromptView{
		Source: "archive",
		Name:   archive,
		SHA256: claim.PromptDigest,
		Body:   string(contents),
	}, nil
}

func listPromptRecords(worktree string, includeBodies bool) ([]PromptRecord, error) {
	directory, err := openJournalSubdirectory(worktree, promptsDirectory, false)
	if errors.Is(err, os.ErrNotExist) {
		return []PromptRecord{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = directory.Close() }()

	names, err := directory.Readdirnames(-1)
	if err != nil {
		return nil, fmt.Errorf("read prompt sequence: %w", err)
	}
	if _, err := directory.Seek(0, 0); err != nil {
		return nil, fmt.Errorf("rewind prompt sequence: %w", err)
	}
	sort.Strings(names)

	records := make([]PromptRecord, 0, len(names))
	for _, name := range names {
		match := promptFileName.FindStringSubmatch(name)
		if match == nil {
			continue
		}
		content, err := readBytesAt(directory, name)
		if err != nil {
			return nil, err
		}
		header, body, err := parsePromptFile(content)
		if err != nil {
			return nil, fmt.Errorf("prompt %s: %w", name, err)
		}
		ordinal, err := strconv.Atoi(match[1])
		if err != nil {
			continue
		}
		if header.Seq != ordinal {
			return nil, fmt.Errorf("prompt %s records seq %d but its ordinal is %d", name, header.Seq, ordinal)
		}
		record := PromptRecord{
			Name: name, Seq: header.Seq, At: header.At, SHA256: header.SHA256,
			Source: header.Source, Runtime: header.Runtime, Model: header.Model,
			CLI: header.CLI, Provider: header.Provider,
		}
		if includeBodies {
			record.Body = body
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Seq < records[j].Seq })
	for index, record := range records {
		if record.Seq != index {
			return nil, fmt.Errorf("prompt sequence is not contiguous: expected ordinal %d, found %d", index, record.Seq)
		}
	}
	return records, nil
}

func parsePromptFile(content []byte) (PromptHeader, string, error) {
	header, err := parsePromptHeader(content)
	if err != nil {
		return PromptHeader{}, "", err
	}
	text := string(content)
	end := strings.Index(text[4:], "\n---\n")
	if end < 0 {
		return PromptHeader{}, "", fmt.Errorf("unterminated YAML frontmatter")
	}
	body := strings.TrimPrefix(text[4+end+5:], "\n")
	return header, body, nil
}

func observeWorkLogGit(ctx context.Context, worktree string) WorkLogGitEvidence {
	evidence := WorkLogGitEvidence{}
	if branch, err := git(ctx, worktree, "branch", "--show-current"); err == nil {
		evidence.Branch = strings.TrimSpace(branch)
	}
	if head, err := git(ctx, worktree, "rev-parse", "HEAD"); err == nil {
		evidence.Head = strings.TrimSpace(head)
	}
	if status, err := git(ctx, worktree, "status", "--porcelain"); err == nil {
		trimmed := strings.TrimSpace(status)
		evidence.Status = trimmed
		evidence.Dirty = trimmed != ""
	}
	return evidence
}

// FormatWorkLogViewText renders the agent bootstrap dump. Private prompt bodies
// are included when present in the view; callers that omit bodies get headers
// only.
func FormatWorkLogViewText(view WorkLogView) string {
	var b strings.Builder
	b.WriteString("# WB work log\n\n")
	b.WriteString("## Worktree\n")
	b.WriteString(view.Worktree)
	b.WriteString("\n\n")

	if view.Manifest != nil {
		b.WriteString("## Manifest\n")
		fmt.Fprintf(&b, "effort_id: %s\n", view.Manifest.EffortID)
		if view.Manifest.ParentEffort != "" {
			fmt.Fprintf(&b, "parent_effort: %s\n", view.Manifest.ParentEffort)
		}
		fmt.Fprintf(&b, "effort_kind: %s\n", view.Manifest.EffortKind)
		fmt.Fprintf(&b, "repository: %s\n", view.Manifest.Repository)
		fmt.Fprintf(&b, "branch: %s\n", view.Manifest.Branch)
		fmt.Fprintf(&b, "base: %s\n", view.Manifest.Base)
		fmt.Fprintf(&b, "base_sha: %s\n", view.Manifest.BaseSHA)
		fmt.Fprintf(&b, "provenance: %s\n", view.Manifest.Provenance)
		if view.Manifest.RunID != "" {
			fmt.Fprintf(&b, "run_id: %s\n", view.Manifest.RunID)
		}
		if view.Manifest.ClaimID != "" {
			fmt.Fprintf(&b, "claim_id: %s\n", view.Manifest.ClaimID)
		}
		if view.Manifest.Model != "" {
			fmt.Fprintf(&b, "model: %s\n", view.Manifest.Model)
		}
		b.WriteString("\n")
	}

	if view.Claim != nil {
		b.WriteString("## Claim\n")
		fmt.Fprintf(&b, "effort_id: %s\n", view.Claim.EffortID)
		fmt.Fprintf(&b, "run_id: %s\n", view.Claim.RunID)
		fmt.Fprintf(&b, "claim_id: %s\n", view.Claim.ClaimID)
		fmt.Fprintf(&b, "lifecycle: %s\n", view.Claim.Lifecycle)
		fmt.Fprintf(&b, "repository: %s\n", view.Claim.Repository)
		fmt.Fprintf(&b, "branch: %s\n", view.Claim.Branch)
		if view.Claim.Model != "" {
			fmt.Fprintf(&b, "model: %s\n", view.Claim.Model)
		}
		if view.Claim.PromptDigest != "" {
			fmt.Fprintf(&b, "prompt_sha256: %s\n", view.Claim.PromptDigest)
		}
		b.WriteString("\n")
	}

	if view.OriginalPrompt != nil {
		b.WriteString("## Original prompt\n")
		fmt.Fprintf(&b, "source: %s\n", view.OriginalPrompt.Source)
		if view.OriginalPrompt.Name != "" {
			fmt.Fprintf(&b, "name: %s\n", view.OriginalPrompt.Name)
		}
		if view.OriginalPrompt.SHA256 != "" {
			fmt.Fprintf(&b, "sha256: %s\n", view.OriginalPrompt.SHA256)
		}
		b.WriteString("\n")
		b.WriteString(view.OriginalPrompt.Body)
		if !strings.HasSuffix(view.OriginalPrompt.Body, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	b.WriteString("## Prompt sequence\n")
	if len(view.Prompts) == 0 {
		b.WriteString("(none)\n\n")
	} else {
		for _, prompt := range view.Prompts {
			fmt.Fprintf(&b, "### %s\n", prompt.Name)
			fmt.Fprintf(&b, "seq: %d\n", prompt.Seq)
			fmt.Fprintf(&b, "source: %s\n", prompt.Source)
			fmt.Fprintf(&b, "sha256: %s\n", prompt.SHA256)
			if prompt.At.IsZero() {
				b.WriteString("\n")
			} else {
				fmt.Fprintf(&b, "at: %s\n\n", prompt.At.UTC().Format(time.RFC3339))
			}
			if prompt.Body != "" {
				b.WriteString(prompt.Body)
				if !strings.HasSuffix(prompt.Body, "\n") {
					b.WriteString("\n")
				}
			}
			b.WriteString("\n")
		}
	}

	b.WriteString("## Git\n")
	if view.Git.Branch != "" {
		fmt.Fprintf(&b, "branch: %s\n", view.Git.Branch)
	}
	if view.Git.Head != "" {
		fmt.Fprintf(&b, "head: %s\n", view.Git.Head)
	}
	fmt.Fprintf(&b, "dirty: %t\n", view.Git.Dirty)
	if view.Git.Status != "" {
		b.WriteString("status:\n")
		b.WriteString(view.Git.Status)
		b.WriteString("\n")
	}
	b.WriteString("\n")

	if len(view.Notes) > 0 {
		b.WriteString("## Notes\n")
		for _, note := range view.Notes {
			fmt.Fprintf(&b, "- %s\n", note)
		}
	}
	return b.String()
}

// FormatWorktreeInfoText renders the redacted single-worktree summary. Prompt
// bodies are never included; only ordinals, digests, and identity. Use
// FormatWorkLogViewText / wb worktree log when an agent needs the private
// instruction text.
func FormatWorktreeInfoText(view WorkLogView) string {
	var b strings.Builder
	b.WriteString("# WB worktree info\n\n")
	b.WriteString("## Worktree\n")
	b.WriteString(view.Worktree)
	b.WriteString("\n\n")

	if view.Manifest != nil {
		b.WriteString("## Manifest\n")
		fmt.Fprintf(&b, "effort_id: %s\n", view.Manifest.EffortID)
		if view.Manifest.ParentEffort != "" {
			fmt.Fprintf(&b, "parent_effort: %s\n", view.Manifest.ParentEffort)
		}
		fmt.Fprintf(&b, "effort_kind: %s\n", view.Manifest.EffortKind)
		fmt.Fprintf(&b, "repository: %s\n", view.Manifest.Repository)
		fmt.Fprintf(&b, "branch: %s\n", view.Manifest.Branch)
		fmt.Fprintf(&b, "base: %s\n", view.Manifest.Base)
		fmt.Fprintf(&b, "base_sha: %s\n", view.Manifest.BaseSHA)
		fmt.Fprintf(&b, "provenance: %s\n", view.Manifest.Provenance)
		if view.Manifest.RunID != "" {
			fmt.Fprintf(&b, "run_id: %s\n", view.Manifest.RunID)
		}
		if view.Manifest.ClaimID != "" {
			fmt.Fprintf(&b, "claim_id: %s\n", view.Manifest.ClaimID)
		}
		if view.Manifest.Model != "" {
			fmt.Fprintf(&b, "model: %s\n", view.Manifest.Model)
		}
		b.WriteString("\n")
	}

	if view.Claim != nil {
		b.WriteString("## Claim\n")
		fmt.Fprintf(&b, "effort_id: %s\n", view.Claim.EffortID)
		fmt.Fprintf(&b, "run_id: %s\n", view.Claim.RunID)
		fmt.Fprintf(&b, "claim_id: %s\n", view.Claim.ClaimID)
		fmt.Fprintf(&b, "lifecycle: %s\n", view.Claim.Lifecycle)
		fmt.Fprintf(&b, "repository: %s\n", view.Claim.Repository)
		fmt.Fprintf(&b, "branch: %s\n", view.Claim.Branch)
		if view.Claim.Model != "" {
			fmt.Fprintf(&b, "model: %s\n", view.Claim.Model)
		}
		if view.Claim.PromptDigest != "" {
			fmt.Fprintf(&b, "prompt_sha256: %s\n", view.Claim.PromptDigest)
		}
		b.WriteString("\n")
	}

	b.WriteString("## Prompt sequence\n")
	if len(view.Prompts) == 0 {
		b.WriteString("(none)\n\n")
	} else {
		for _, prompt := range view.Prompts {
			fmt.Fprintf(&b, "- %s seq=%d source=%s sha256=%s\n", prompt.Name, prompt.Seq, prompt.Source, prompt.SHA256)
		}
		b.WriteString("\n")
	}
	b.WriteString("Prompt bodies are omitted. Use `wb worktree log` for the private agent dump.\n\n")

	b.WriteString("## Git\n")
	if view.Git.Branch != "" {
		fmt.Fprintf(&b, "branch: %s\n", view.Git.Branch)
	}
	if view.Git.Head != "" {
		fmt.Fprintf(&b, "head: %s\n", view.Git.Head)
	}
	fmt.Fprintf(&b, "dirty: %t\n", view.Git.Dirty)
	if view.Git.Status != "" {
		b.WriteString("status:\n")
		b.WriteString(view.Git.Status)
		b.WriteString("\n")
	}
	b.WriteString("\n")

	if len(view.Notes) > 0 {
		b.WriteString("## Notes\n")
		for _, note := range view.Notes {
			fmt.Fprintf(&b, "- %s\n", note)
		}
	}
	return b.String()
}
