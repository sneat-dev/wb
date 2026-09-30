package worktrees

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func workLogViewTestPorts() workLogViewPorts {
	return workLogViewPorts{
		repositoryRoot: func(_ context.Context, _ string) (string, error) { return "/checkout", nil },
		homeRoot:       func(string) (string, error) { return "/home", nil },
		lifecycleOwners: func(string, string) ([]OwnerView, error) {
			return []OwnerView{}, nil
		},
		owners:   func(string) ([]OwnerView, error) { return []OwnerView{}, nil },
		manifest: func(string) (Manifest, error) { return Manifest{}, nil },
		prompts:  func(string, bool) ([]PromptRecord, error) { return []PromptRecord{}, nil },
		activeClaim: func(string, string) (workLogClaim, workLogProjection, string, error) {
			return workLogClaim{}, workLogProjection{}, "", errWorkLogProjectionNotFound
		},
		relocation: func(string, workLogClaim, string) (workLogRelocationResolution, error) {
			return workLogRelocationResolution{}, nil
		},
		originalPrompt: func(string, workLogClaim, []PromptRecord) (*OriginalPromptView, error) {
			return &OriginalPromptView{Body: "archived prompt"}, nil
		},
		terminal:   func(string, string) (*workLogTerminalRecord, error) { return nil, nil },
		reportBody: func(string) (string, error) { return "private report", nil },
		git:        func(context.Context, string) WorkLogGitEvidence { return WorkLogGitEvidence{} },
	}
}

func TestWorkLogViewReportsAuthorityAndStorageFailures(t *testing.T) {
	t.Parallel()
	failure := errors.New("injected view failure")
	cases := []struct {
		name   string
		change func(*workLogViewPorts)
	}{
		{"repository root", func(p *workLogViewPorts) {
			p.repositoryRoot = func(context.Context, string) (string, error) { return "", failure }
		}},
		{"lifecycle owners", func(p *workLogViewPorts) {
			p.lifecycleOwners = func(string, string) ([]OwnerView, error) { return nil, failure }
		}},
		{"fallback owners", func(p *workLogViewPorts) {
			p.homeRoot = func(string) (string, error) { return "", failure }
			p.owners = func(string) ([]OwnerView, error) { return nil, failure }
		}},
		{"manifest", func(p *workLogViewPorts) { p.manifest = func(string) (Manifest, error) { return Manifest{}, failure } }},
		{"prompts", func(p *workLogViewPorts) {
			p.prompts = func(string, bool) ([]PromptRecord, error) { return nil, failure }
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ports := workLogViewTestPorts()
			tc.change(&ports)
			if _, err := ports.loadWorkLogView(context.Background(), LoadWorkLogOptions{IncludePromptBodies: true}); !errors.Is(err, failure) {
				t.Fatalf("view error = %v, want injected failure", err)
			}
		})
	}
}

func TestWorkLogViewExplainsMissingAuthoritiesAndUsesJournalFallback(t *testing.T) {
	t.Parallel()
	ports := workLogViewTestPorts()
	missing, err := ports.loadWorkLogView(context.Background(), LoadWorkLogOptions{})
	if err != nil || !strings.Contains(strings.Join(missing.Notes, " "), "no active work-log projection") {
		t.Fatalf("missing claim view = %#v, err = %v", missing, err)
	}
	ports.homeRoot = func(string) (string, error) { return "", errors.New("home unavailable") }
	ports.manifest = func(string) (Manifest, error) { return Manifest{}, errManifestNotFound }
	ports.prompts = func(string, bool) ([]PromptRecord, error) {
		return []PromptRecord{{Name: "first", Body: "journal prompt", SHA256: "digest"}}, nil
	}
	view, err := ports.loadWorkLogView(context.Background(), LoadWorkLogOptions{IncludePromptBodies: true})
	if err != nil || view.OriginalPrompt == nil || view.OriginalPrompt.Source != "journal" || view.OriginalPrompt.Body != "journal prompt" {
		t.Fatalf("journal fallback view = %#v, err = %v", view, err)
	}
	if len(view.Notes) < 2 || !strings.Contains(strings.Join(view.Notes, " "), "home unavailable") {
		t.Fatalf("missing authority notes = %#v", view.Notes)
	}
}

func TestWorkLogViewResolvesActiveAndRelocatedClaim(t *testing.T) {
	t.Parallel()
	claim := workLogClaim{EffortID: "task", Worktree: "/old", Repository: "acme/app", Lifecycle: "active"}
	ports := workLogViewTestPorts()
	ports.activeClaim = func(string, string) (workLogClaim, workLogProjection, string, error) {
		return claim, workLogProjection{}, "/private/claim.json", nil
	}
	ports.relocation = func(string, workLogClaim, string) (workLogRelocationResolution, error) {
		return workLogRelocationResolution{receipt: &workLogRelocationReceipt{}, repository: "acme/moved", worktree: "/checkout"}, nil
	}
	view, err := ports.loadWorkLogView(context.Background(), LoadWorkLogOptions{IncludePromptBodies: true})
	if err != nil || view.Claim == nil || view.Claim.Repository != "acme/moved" || view.Claim.Worktree != "/checkout" || view.OriginalPrompt == nil {
		t.Fatalf("relocated claim view = %#v, err = %v", view, err)
	}
	ports.relocation = func(string, workLogClaim, string) (workLogRelocationResolution, error) {
		return workLogRelocationResolution{}, errors.New("untrusted relocation")
	}
	ports.originalPrompt = func(string, workLogClaim, []PromptRecord) (*OriginalPromptView, error) {
		return nil, errors.New("archive unreadable")
	}
	view, err = ports.loadWorkLogView(context.Background(), LoadWorkLogOptions{IncludePromptBodies: true})
	if err != nil || view.Claim == nil || view.Claim.Repository != "acme/app" || !strings.Contains(strings.Join(view.Notes, " "), "archive unreadable") {
		t.Fatalf("untrusted relocation view = %#v, err = %v", view, err)
	}
	claim.Worktree = "/checkout"
	view, err = ports.loadWorkLogView(context.Background(), LoadWorkLogOptions{})
	if err != nil || view.Claim == nil || view.Claim.Worktree != "/checkout" {
		t.Fatalf("same checkout view = %#v, err = %v", view, err)
	}
}

func TestWorkLogViewReadsTerminalReportAndExplainsCorruptEvidence(t *testing.T) {
	t.Parallel()
	claim := workLogClaim{EffortID: "task", Worktree: "/checkout", Repository: "acme/app"}
	ports := workLogViewTestPorts()
	ports.activeClaim = func(string, string) (workLogClaim, workLogProjection, string, error) {
		return workLogClaim{}, workLogProjection{Lifecycle: "terminal"}, "", errors.New("terminal projection")
	}
	ports.terminal = func(string, string) (*workLogTerminalRecord, error) {
		return &workLogTerminalRecord{Claim: claim, Disposition: "completed", FinalCommit: "commit", SealedAt: time.Now(),
			FinalizeReport: &workLogFinalizeReport{Result: "success", ReportPath: "/private/report.md"}}, nil
	}
	view, err := ports.loadWorkLogView(context.Background(), LoadWorkLogOptions{IncludePromptBodies: true})
	if err != nil || view.Terminal == nil || view.Terminal.ReportPath != "/private/report.md" || view.FinalizeReportBody != "private report" || view.OriginalPrompt == nil {
		t.Fatalf("terminal report view = %#v, err = %v", view, err)
	}
	ports.reportBody = func(string) (string, error) { return "", errors.New("report unreadable") }
	ports.originalPrompt = func(string, workLogClaim, []PromptRecord) (*OriginalPromptView, error) {
		return nil, errors.New("prompt unreadable")
	}
	view, err = ports.loadWorkLogView(context.Background(), LoadWorkLogOptions{IncludePromptBodies: true})
	if err != nil || !strings.Contains(strings.Join(view.Notes, " "), "report unreadable") || !strings.Contains(strings.Join(view.Notes, " "), "prompt unreadable") {
		t.Fatalf("unreadable report/prompt notes = %#v, err = %v", view.Notes, err)
	}
	ports.terminal = func(string, string) (*workLogTerminalRecord, error) { return nil, errors.New("terminal unreadable") }
	view, err = ports.loadWorkLogView(context.Background(), LoadWorkLogOptions{})
	if err != nil || !strings.Contains(strings.Join(view.Notes, " "), "terminal unreadable") {
		t.Fatalf("unreadable terminal notes = %#v, err = %v", view.Notes, err)
	}
	ports.terminal = func(string, string) (*workLogTerminalRecord, error) { return nil, nil }
	view, err = ports.loadWorkLogView(context.Background(), LoadWorkLogOptions{})
	if err != nil || !strings.Contains(strings.Join(view.Notes, " "), "no terminal record") {
		t.Fatalf("absent terminal notes = %#v, err = %v", view.Notes, err)
	}
	ports.activeClaim = func(string, string) (workLogClaim, workLogProjection, string, error) {
		return workLogClaim{}, workLogProjection{Lifecycle: "active"}, "", errors.New("claim corrupt")
	}
	view, err = ports.loadWorkLogView(context.Background(), LoadWorkLogOptions{})
	if err != nil || !strings.Contains(strings.Join(view.Notes, " "), "claim corrupt") {
		t.Fatalf("corrupt active claim notes = %#v, err = %v", view.Notes, err)
	}
}
