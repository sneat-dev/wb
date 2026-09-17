package worktrees

import (
	"strings"
	"testing"
	"time"
)

// wtLogCovFullView returns a view with every optional section populated so the
// renderers must emit each conditional block.
func wtLogCovFullView() WorkLogView {
	return WorkLogView{
		Worktree: "/tmp/wt",
		Manifest: &Manifest{
			Version: 1, EffortID: "effort-1", ParentEffort: "parent-1", EffortKind: "feature",
			Repository: "acme/app", Branch: "wb/feature", Base: "main", BaseSHA: "abc123",
			Provenance: "created", RunID: "run-1", ClaimID: "claim-1", Model: "claude-sonnet",
		},
		Claim: &WorkLogClaimView{
			EffortID: "effort-1", RunID: "run-1", ClaimID: "claim-1", Lifecycle: "active",
			Repository: "acme/app", Branch: "wb/feature", Model: "claude-sonnet", PromptDigest: "digest-1",
		},
		Terminal: &WorkLogTerminalView{
			Disposition: "landed", SealedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
			TerminalResult: "success", TerminalMessage: "merged", ReportPath: "/tmp/report.md",
		},
		FinalizeReportBody: "report body without newline",
		Owners: []OwnerView{
			{OwnerRegistration: OwnerRegistration{Agent: "agent-1", Model: "claude-sonnet", Effort: "effort-1", PID: 4242, At: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}, PIDStatus: "alive"},
		},
		OriginalPrompt: &OriginalPromptView{
			Source: "journal", Name: "0000-original-prompt.md", SHA256: "sha-1", Body: "prompt body without newline",
		},
		Prompts: []PromptRecord{
			{Name: "0000-original-prompt.md", Seq: 0, Source: "human", SHA256: "sha-1", At: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Body: "first body"},
			{Name: "0001-steering.md", Seq: 1, Source: "human", SHA256: "sha-2", Body: "second body\n"},
		},
		Git:   WorkLogGitEvidence{Branch: "wb/feature", Head: "deadbeef", Dirty: true, Status: " M file.go"},
		Notes: []string{"first note", "second note"},
	}
}

func TestWtLogCovFormatWorkLogViewRendersEverySection(t *testing.T) {
	text := FormatWorkLogViewText(wtLogCovFullView())
	for _, want := range []string{
		"# WB work log",
		"## Worktree\n/tmp/wt",
		"## Manifest",
		"parent_effort: parent-1",
		"run_id: run-1",
		"claim_id: claim-1",
		"model: claude-sonnet",
		"## Claim",
		"prompt_sha256: digest-1",
		"## Terminal",
		"terminal_result: success",
		"terminal_message: merged",
		"report_path: /tmp/report.md",
		"### Finalize report",
		"report body without newline",
		"## Owners",
		"- agent=agent-1 model=claude-sonnet effort=effort-1 pid=4242 status=alive at=2026-01-01T00:00:00Z",
		"## Original prompt",
		"source: journal",
		"name: 0000-original-prompt.md",
		"sha256: sha-1",
		"prompt body without newline",
		"## Prompt sequence",
		"### 0000-original-prompt.md",
		"seq: 0",
		"at: 2026-01-01T00:00:00Z",
		"first body",
		"### 0001-steering.md",
		"## Git",
		"branch: wb/feature",
		"head: deadbeef",
		"dirty: true",
		"status:\n M file.go",
		"## Notes",
		"- first note",
		"- second note",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("work log text missing %q:\n%s", want, text)
		}
	}
}

func TestWtLogCovFormatWorkLogViewRendersEmptySections(t *testing.T) {
	view := WorkLogView{Worktree: "/tmp/empty", Prompts: []PromptRecord{}}
	text := FormatWorkLogViewText(view)
	for _, want := range []string{
		"# WB work log",
		"## Worktree\n/tmp/empty",
		"## Owners\n(none recorded; treated as orphaned)",
		"## Prompt sequence\n(none)",
		"dirty: false",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("empty work log text missing %q:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{"## Manifest", "## Claim", "## Terminal", "## Original prompt", "## Notes", "status:"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("empty work log text unexpectedly contains %q:\n%s", unwanted, text)
		}
	}
}

func TestWtLogCovFormatWorkLogViewOmitsOptionalManifestAndClaimFields(t *testing.T) {
	view := WorkLogView{
		Worktree: "/tmp/minimal",
		Manifest: &Manifest{Version: 1, EffortID: "e", EffortKind: "feature", Repository: "acme/app", Branch: "b", Base: "main", BaseSHA: "sha", Provenance: "created"},
		Claim:    &WorkLogClaimView{EffortID: "e", RunID: "r", ClaimID: "c", Lifecycle: "active", Repository: "acme/app", Branch: "b"},
		Prompts:  []PromptRecord{},
	}
	text := FormatWorkLogViewText(view)
	// Claim always renders its identity trio; the omitted fields are the
	// optional manifest extras and the claim's optional execution metadata.
	for _, unwanted := range []string{"parent_effort:", "model:", "prompt_sha256:"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("minimal work log text unexpectedly contains %q:\n%s", unwanted, text)
		}
	}
	// Terminal absent, but a finalize body present must not render without it.
	if strings.Contains(text, "### Finalize report") {
		t.Fatalf("finalize body rendered without terminal:\n%s", text)
	}
}

func TestWtLogCovFormatWorkLogViewReportBodyWithTrailingNewlineAndZeroPromptTime(t *testing.T) {
	view := WorkLogView{
		Worktree:           "/tmp/wt2",
		Terminal:           &WorkLogTerminalView{Disposition: "landed", SealedAt: time.Unix(0, 0).UTC()},
		FinalizeReportBody: "already newline terminated\n",
		OriginalPrompt:     &OriginalPromptView{Source: "archive", Body: "prompt newline\n"},
		Prompts:            []PromptRecord{{Name: "0000-x.md", Seq: 0, Source: "human", SHA256: "sha"}},
	}
	text := FormatWorkLogViewText(view)
	if !strings.Contains(text, "already newline terminated\n\n") {
		t.Fatalf("report body did not render as its own block:\n%s", text)
	}
	if strings.Contains(text, "terminal_result:") || strings.Contains(text, "report_path:") {
		t.Fatalf("empty terminal fields should be omitted:\n%s", text)
	}
	if strings.Contains(text, "## Original prompt\nsource: archive\nname:") {
		t.Fatalf("empty original-prompt name should be omitted:\n%s", text)
	}
	// A zero At renders a blank line rather than a timestamp.
	if strings.Contains(text, "at: 0001-01-01") {
		t.Fatalf("zero prompt time should not render a timestamp:\n%s", text)
	}
}

func TestWtLogCovFormatWorktreeInfoRendersRedactedSections(t *testing.T) {
	view := wtLogCovFullView()
	text := FormatWorktreeInfoText(view)
	for _, want := range []string{
		"# WB worktree info",
		"## Worktree\n/tmp/wt",
		"## Manifest",
		"parent_effort: parent-1",
		"## Claim",
		"## Terminal",
		"terminal_result: success",
		"terminal_message: merged",
		"report_path: /tmp/report.md",
		"Report body is omitted.",
		"## Prompt sequence",
		"- 0000-original-prompt.md seq=0 source=human sha256=sha-1",
		"Prompt bodies are omitted.",
		"## Git",
		"dirty: true",
		"status:\n M file.go",
		"## Notes",
		"- first note",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("info text missing %q:\n%s", want, text)
		}
	}
	for _, leaked := range []string{"first body", "second body", "prompt body without newline", "report body without newline", "## Original prompt"} {
		if strings.Contains(text, leaked) {
			t.Fatalf("info text leaked private content %q:\n%s", leaked, text)
		}
	}
}

func TestWtLogCovFormatWorktreeInfoRendersEmptySections(t *testing.T) {
	text := FormatWorktreeInfoText(WorkLogView{Worktree: "/tmp/info-empty", Prompts: []PromptRecord{}})
	for _, want := range []string{
		"# WB worktree info",
		"## Prompt sequence\n(none)",
		"Prompt bodies are omitted.",
		"dirty: false",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("empty info text missing %q:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{"## Manifest", "## Claim", "## Terminal", "## Notes", "status:"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("empty info text unexpectedly contains %q:\n%s", unwanted, text)
		}
	}
}
