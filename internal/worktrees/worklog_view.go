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
	EffortID string `json:"effort_id"`
	RunID    string `json:"run_id"`
	ClaimID  string `json:"claim_id"`
	Task     string `json:"task,omitempty"`
	// Repository and Worktree are the current identity resolved through
	// append-only relocation receipts. ClaimPath still points to the unchanged
	// immutable claim that anchors that history.
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
	TaskSummary     string    `json:"task_summary,omitempty"`
	PromptDigest    string    `json:"prompt_sha256,omitempty"`
	PromptArchive   string    `json:"prompt_archive,omitempty"`
	ClaimPath       string    `json:"claim_path,omitempty"`
}

// WorkLogTerminalView is the redacted terminal record for a claim `wb
// worktree log finalize` has sealed. TerminalResult/TerminalMessage/
// ReportPath are populated only when the terminal was sealed by finalize
// (never for a recycled, removed, superseded, orphaned, or handoff
// disposition); ReportPath names the private copy of a --report body under
// WB_HOME but never carries the body itself -- that stays private local data,
// exposed only through WorkLogView.FinalizeReportBody in the bare agent dump.
type WorkLogTerminalView struct {
	Disposition     string    `json:"disposition"`
	FinalCommit     string    `json:"final_commit,omitempty"`
	SealedAt        time.Time `json:"sealed_at"`
	TerminalResult  string    `json:"terminal_result,omitempty"`
	TerminalMessage string    `json:"terminal_message,omitempty"`
	ReportPath      string    `json:"report_path,omitempty"`
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
	Worktree       string               `json:"worktree"`
	Manifest       *Manifest            `json:"manifest,omitempty"`
	Prompts        []PromptRecord       `json:"prompts"`
	OriginalPrompt *OriginalPromptView  `json:"original_prompt,omitempty"`
	Claim          *WorkLogClaimView    `json:"claim,omitempty"`
	Terminal       *WorkLogTerminalView `json:"terminal,omitempty"`
	Owners         []OwnerView          `json:"owners,omitempty"`
	Git            WorkLogGitEvidence   `json:"git"`
	Notes          []string             `json:"notes,omitempty"`
	// FinalizeReportBody is the exact `wb worktree log finalize
	// --report/--report-stdin` body, populated only when
	// LoadWorkLogOptions.IncludePromptBodies is set and Terminal carries a
	// ReportPath -- the same redaction rule as OriginalPrompt/Prompts bodies.
	FinalizeReportBody string `json:"finalize_report_body,omitempty"`
}

// LoadWorkLogOptions selects which private records to include. Agents need
// prompt bodies; a future redacted show path can leave IncludePromptBodies
// false without a second code path for identity.
type LoadWorkLogOptions struct {
	ProjectsRoot        string
	Worktree            string
	IncludePromptBodies bool
}

// workLogViewPorts scopes repository and private-read dependencies to one view.
type workLogViewPorts struct {
	repositoryRoot  func(context.Context, string) (string, error)
	homeRoot        func(string) (string, error)
	lifecycleOwners func(string, string) ([]OwnerView, error)
	owners          func(string) ([]OwnerView, error)
	manifest        func(string) (Manifest, error)
	prompts         func(string, bool) ([]PromptRecord, error)
	activeClaim     func(string, string) (workLogClaim, workLogProjection, string, error)
	relocation      func(string, workLogClaim, string) (workLogRelocationResolution, error)
	originalPrompt  func(string, workLogClaim, []PromptRecord) (*OriginalPromptView, error)
	terminal        func(string, string) (*workLogTerminalRecord, error)
	reportBody      func(string) (string, error)
	git             func(context.Context, string) WorkLogGitEvidence
}

func defaultWorkLogViewPorts() workLogViewPorts {
	return workLogViewPorts{
		repositoryRoot: RepositoryRootFor, homeRoot: wbhome.Root,
		lifecycleOwners: lifecycleOwnerViews, owners: ownerViews,
		manifest: ReadManifest, prompts: listPromptRecords,
		activeClaim: activeWorkLogClaim, relocation: latestRelocationResolution,
		originalPrompt: loadOriginalPrompt, terminal: readWorkLogTerminalRecord,
		reportBody: readWorkLogFinalizeReportBody, git: observeWorkLogGit,
	}
}

// LoadWorkLogView assembles the local recovery record an agent needs to resume
// work. It is read-only with respect to Git state and prompt archives. The
// only mutation it may perform is the existing one-way legacy projection
// migration that activeWorkLogClaim already performs when corroborating a
// claim.
func LoadWorkLogView(ctx context.Context, options LoadWorkLogOptions) (WorkLogView, error) {
	return defaultWorkLogViewPorts().loadWorkLogView(ctx, options)
}

func (p workLogViewPorts) loadWorkLogView(ctx context.Context, options LoadWorkLogOptions) (WorkLogView, error) {
	worktree := strings.TrimSpace(options.Worktree)
	if worktree == "" {
		worktree = "."
	}
	root, err := p.repositoryRoot(ctx, worktree)
	if err != nil {
		return WorkLogView{}, err
	}
	view := WorkLogView{Worktree: root, Prompts: []PromptRecord{}}
	home, homeErr := p.homeRoot(options.ProjectsRoot)
	if homeErr == nil {
		owners, ownersErr := p.lifecycleOwners(home, root)
		if ownersErr != nil {
			return WorkLogView{}, ownersErr
		}
		view.Owners = owners
	} else {
		owners, ownersErr := p.owners(root)
		if ownersErr != nil {
			return WorkLogView{}, ownersErr
		}
		view.Owners = owners
	}

	if manifest, manifestErr := p.manifest(root); manifestErr == nil {
		copy := manifest
		view.Manifest = &copy
	} else if !errors.Is(manifestErr, errManifestNotFound) {
		return WorkLogView{}, manifestErr
	} else {
		view.Notes = append(view.Notes, "no .wb/local/manifest.yaml; record one with wb worktree set or recreate under a valid effort path")
	}

	prompts, err := p.prompts(root, options.IncludePromptBodies)
	if err != nil {
		return WorkLogView{}, err
	}
	view.Prompts = prompts
	if len(prompts) == 0 {
		view.Notes = append(view.Notes, "no recorded prompts under .wb/local/prompts/; supply one with wb worktree set --prompt")
	}

	if homeErr != nil {
		view.Notes = append(view.Notes, fmt.Sprintf("could not resolve WB home: %v", homeErr))
	} else if claim, projection, claimPath, claimErr := p.activeClaim(home, root); claimErr == nil {
		resolvedRepository, resolvedWorktree := claim.Repository, claim.Worktree
		if filepath.Clean(root) != filepath.Clean(claim.Worktree) {
			if resolution, resolutionErr := p.relocation(home, claim, root); resolutionErr == nil && resolution.receipt != nil {
				resolvedRepository, resolvedWorktree = resolution.repository, resolution.worktree
			}
		}
		view.Claim = newWorkLogClaimView(claim, resolvedRepository, resolvedWorktree, claimPath)
		if options.IncludePromptBodies {
			if original, originalErr := p.originalPrompt(home, claim, prompts); originalErr == nil {
				view.OriginalPrompt = original
			} else if originalErr != nil {
				view.Notes = append(view.Notes, fmt.Sprintf("original prompt unavailable: %v", originalErr))
			}
		}
	} else if errors.Is(claimErr, errWorkLogProjectionNotFound) {
		view.Notes = append(view.Notes, "no active work-log projection; this checkout may predate Hybrid Work Log create")
	} else if projection.Lifecycle == "terminal" {
		if terminal, terminalErr := p.terminal(home, root); terminalErr == nil && terminal != nil {
			claim := terminal.Claim
			view.Claim = newWorkLogClaimView(claim, claim.Repository, claim.Worktree, "")
			view.Terminal = &WorkLogTerminalView{
				Disposition: terminal.Disposition, FinalCommit: terminal.FinalCommit, SealedAt: terminal.SealedAt,
			}
			if terminal.FinalizeReport != nil {
				view.Terminal.TerminalResult = terminal.FinalizeReport.Result
				view.Terminal.TerminalMessage = terminal.FinalizeReport.Message
				view.Terminal.ReportPath = terminal.FinalizeReport.ReportPath
				if options.IncludePromptBodies && terminal.FinalizeReport.ReportPath != "" {
					if body, bodyErr := p.reportBody(terminal.FinalizeReport.ReportPath); bodyErr == nil {
						view.FinalizeReportBody = body
					} else {
						view.Notes = append(view.Notes, fmt.Sprintf("finalize report unavailable: %v", bodyErr))
					}
				}
			}
			if options.IncludePromptBodies {
				if original, originalErr := p.originalPrompt(home, claim, prompts); originalErr == nil {
					view.OriginalPrompt = original
				} else if originalErr != nil {
					view.Notes = append(view.Notes, fmt.Sprintf("original prompt unavailable: %v", originalErr))
				}
			}
		} else if terminalErr != nil {
			view.Notes = append(view.Notes, fmt.Sprintf("terminal work-log claim not usable: %v", terminalErr))
		} else {
			view.Notes = append(view.Notes, "work-log projection is terminal but no terminal record was found")
		}
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

	view.Git = p.git(ctx, root)
	return view, nil
}

func newWorkLogClaimView(claim workLogClaim, repository, worktree, claimPath string) *WorkLogClaimView {
	return &WorkLogClaimView{
		EffortID: claim.EffortID, RunID: claim.RunID, ClaimID: claim.ClaimID,
		Task: claim.Task, Repository: repository, Worktree: worktree,
		Branch: claim.Branch, Base: claim.Base, BaseSHA: claim.BaseSHA,
		Lifecycle: claim.Lifecycle, RecordedAt: claim.RecordedAt,
		Initiator: claim.Initiator, AgentID: claim.AgentID, AgentRuntime: claim.AgentRuntime,
		Model: claim.Model, ModelProvenance: claim.ModelProvenance,
		CLI: claim.CLI, Provider: claim.Provider, TaskSummary: claim.TaskSummary,
		PromptDigest: claim.PromptDigest, PromptArchive: claim.PromptArchive,
		ClaimPath: claimPath,
	}
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
	local := observeLocalGit(ctx, worktree)
	return WorkLogGitEvidence{
		Branch: local.Branch, Head: local.Head, Dirty: local.Dirty, Status: local.Status,
	}
}

// FormatWorkLogViewText renders the agent bootstrap dump. Private prompt bodies
// are included when present in the view; callers that omit bodies get headers
// only.
func FormatWorkLogViewText(view WorkLogView) string {
	var b strings.Builder
	b.WriteString("# WB work log\n\n")
	writeWorkLogIdentitySections(&b, view)

	if view.Terminal != nil {
		writeWorkLogTerminalHeader(&b, view.Terminal)
		b.WriteString("\n")
		if view.FinalizeReportBody != "" {
			b.WriteString("### Finalize report\n")
			b.WriteString(view.FinalizeReportBody)
			if !strings.HasSuffix(view.FinalizeReportBody, "\n") {
				b.WriteString("\n")
			}
			b.WriteString("\n")
		}
	}

	b.WriteString("## Owners\n")
	if len(view.Owners) == 0 {
		b.WriteString("(none recorded; treated as orphaned)\n\n")
	} else {
		for _, owner := range view.Owners {
			fmt.Fprintf(&b, "- agent=%s model=%s effort=%s pid=%d status=%s at=%s\n", owner.Agent, owner.Model, owner.Effort, owner.PID, owner.PIDStatus, owner.At.Format(time.RFC3339))
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

	writeWorkLogStatusSections(&b, view)
	return b.String()
}

// FormatWorktreeInfoText renders the redacted single-worktree summary. Prompt
// bodies are never included; only ordinals, digests, and identity. Use
// FormatWorkLogViewText / wb worktree log when an agent needs the private
// instruction text.
func FormatWorktreeInfoText(view WorkLogView) string {
	var b strings.Builder
	b.WriteString("# WB worktree info\n\n")
	writeWorkLogIdentitySections(&b, view)

	if view.Terminal != nil {
		writeWorkLogTerminalHeader(&b, view.Terminal)
		b.WriteString("Report body is omitted. Use bare 'wb worktree log' for the private agent dump.\n\n")
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

	writeWorkLogStatusSections(&b, view)
	return b.String()
}

func writeWorkLogTerminalHeader(b *strings.Builder, terminal *WorkLogTerminalView) {
	b.WriteString("## Terminal\n")
	fmt.Fprintf(b, "disposition: %s\n", terminal.Disposition)
	fmt.Fprintf(b, "sealed_at: %s\n", terminal.SealedAt.UTC().Format(time.RFC3339))
	if terminal.TerminalResult != "" {
		fmt.Fprintf(b, "terminal_result: %s\n", terminal.TerminalResult)
	}
	if terminal.TerminalMessage != "" {
		fmt.Fprintf(b, "terminal_message: %s\n", terminal.TerminalMessage)
	}
	if terminal.ReportPath != "" {
		fmt.Fprintf(b, "report_path: %s\n", terminal.ReportPath)
	}
}

func writeWorkLogIdentitySections(b *strings.Builder, view WorkLogView) {
	b.WriteString("## Worktree\n")
	b.WriteString(view.Worktree)
	b.WriteString("\n\n")

	if view.Manifest != nil {
		b.WriteString("## Manifest\n")
		fmt.Fprintf(b, "effort_id: %s\n", view.Manifest.EffortID)
		if view.Manifest.ParentEffort != "" {
			fmt.Fprintf(b, "parent_effort: %s\n", view.Manifest.ParentEffort)
		}
		fmt.Fprintf(b, "effort_kind: %s\n", view.Manifest.EffortKind)
		fmt.Fprintf(b, "repository: %s\n", view.Manifest.Repository)
		fmt.Fprintf(b, "branch: %s\n", view.Manifest.Branch)
		fmt.Fprintf(b, "base: %s\n", view.Manifest.Base)
		fmt.Fprintf(b, "base_sha: %s\n", view.Manifest.BaseSHA)
		fmt.Fprintf(b, "provenance: %s\n", view.Manifest.Provenance)
		if view.Manifest.RunID != "" {
			fmt.Fprintf(b, "run_id: %s\n", view.Manifest.RunID)
		}
		if view.Manifest.ClaimID != "" {
			fmt.Fprintf(b, "claim_id: %s\n", view.Manifest.ClaimID)
		}
		if view.Manifest.Model != "" {
			fmt.Fprintf(b, "model: %s\n", view.Manifest.Model)
		}
		b.WriteString("\n")
	}

	if view.Claim != nil {
		b.WriteString("## Claim\n")
		fmt.Fprintf(b, "effort_id: %s\n", view.Claim.EffortID)
		fmt.Fprintf(b, "run_id: %s\n", view.Claim.RunID)
		fmt.Fprintf(b, "claim_id: %s\n", view.Claim.ClaimID)
		fmt.Fprintf(b, "lifecycle: %s\n", view.Claim.Lifecycle)
		fmt.Fprintf(b, "repository: %s\n", view.Claim.Repository)
		fmt.Fprintf(b, "branch: %s\n", view.Claim.Branch)
		if view.Claim.Model != "" {
			fmt.Fprintf(b, "model: %s\n", view.Claim.Model)
		}
		if view.Claim.PromptDigest != "" {
			fmt.Fprintf(b, "prompt_sha256: %s\n", view.Claim.PromptDigest)
		}
		b.WriteString("\n")
	}
}

func writeWorkLogStatusSections(b *strings.Builder, view WorkLogView) {
	b.WriteString("## Git\n")
	if view.Git.Branch != "" {
		fmt.Fprintf(b, "branch: %s\n", view.Git.Branch)
	}
	if view.Git.Head != "" {
		fmt.Fprintf(b, "head: %s\n", view.Git.Head)
	}
	fmt.Fprintf(b, "dirty: %t\n", view.Git.Dirty)
	if view.Git.Status != "" {
		b.WriteString("status:\n")
		b.WriteString(view.Git.Status)
		b.WriteString("\n")
	}
	b.WriteString("\n")

	if len(view.Notes) > 0 {
		b.WriteString("## Notes\n")
		for _, note := range view.Notes {
			fmt.Fprintf(b, "- %s\n", note)
		}
	}
}
