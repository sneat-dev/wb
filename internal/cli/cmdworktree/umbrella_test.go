package cmdworktree

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/umbrella"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktreeclaims"
	"github.com/sneat-dev/wb/internal/worktreerun"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

func seedCmdWorktreeUmbrellaClaim(t *testing.T, projectsRoot, task, repo, umbrellaRef string, landed bool) {
	t.Helper()
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	wtDir := filepath.Join(projectsRoot, ".worktrees", task, repo)
	baseSHA := strings.Repeat("a", 40)
	claimID := worktreeclaims.WorkLogClaimID(task, worktreeclaims.CreationResult{
		Repository: repo, WorktreeDir: wtDir, Branch: task, Base: "main", BaseSHA: baseSHA,
	})
	claim := worktreeclaims.Claim{
		Version: 2, EffortID: task, RunID: "run-1", ClaimID: claimID, Task: task,
		Repository: repo, Worktree: wtDir, Branch: task, Base: "main", BaseSHA: baseSHA,
		Lifecycle: "active", RecordedAt: time.Now().UTC(), Umbrella: umbrellaRef,
	}
	claimsDir := filepath.Join(home, "worklogs", task, "runs", "run-1", "claims")
	if err := os.MkdirAll(claimsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(claim)
	if err := os.WriteFile(filepath.Join(claimsDir, claimID+".json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if landed {
		termDir := filepath.Join(home, "worklogs", task, "runs", "run-1", "terminals")
		if err := os.MkdirAll(termDir, 0o700); err != nil {
			t.Fatal(err)
		}
		term := worktreeclaims.TerminalRecord{
			Claim: claim, FinalCommit: strings.Repeat("b", 40), Disposition: "landed", SealedAt: time.Now().UTC(),
			Landed: &worktreeclaims.LandedEvidence{
				Target: "main", LandedSHA: strings.Repeat("b", 40), Proof: worktreeclaims.LandedProofContained, PullRequest: 846,
			},
		}
		term.Lifecycle = "terminal"
		termRaw, _ := json.Marshal(term)
		if err := os.WriteFile(filepath.Join(termDir, claimID+".json"), termRaw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWorktreeCreateUmbrellaFlagAndMultiRepoWarning(t *testing.T) {
	t.Parallel()
	projectsRoot := t.TempDir()
	runtime := shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: projectsRoot} }}

	// 1. Multi-repo create without --umbrella warns once on stderr and does not block.
	var stdout, stderr bytes.Buffer
	var capturedUmbrella string
	cmdWarn := NewCreate(runtime, CreateDependencies{
		PrepareLog: func(_ string, _ string, opts worktrees.WorkLogOptions) (worktrees.WorkLogOptions, error) {
			capturedUmbrella = opts.Umbrella
			return opts, nil
		},
		RegisteredIdentity: func() (worktrees.AgentIdentity, bool) { return worktrees.AgentIdentity{}, true },
		BeforeCreate:       func(string, []string) error { return nil },
		Claim: func(*cobra.Command, bool, string, string) worktreerun.RemoteClaimOutcome {
			return worktreerun.RemoteClaimOutcome{}
		},
		AfterCreate: func(*cobra.Command, string, []worktrees.CreateResult) {},
		Run: func(_ context.Context, repos []string, opts worktrees.CreateOptions) ([]worktrees.CreateResult, error) {
			results := make([]worktrees.CreateResult, 0, len(repos))
			for _, r := range repos {
				results = append(results, worktrees.CreateResult{Repository: r, Branch: opts.Operation, WorktreeDir: filepath.Join(projectsRoot, r)})
			}
			return results, nil
		},
	})
	cmdWarn.SetOut(&stdout)
	cmdWarn.SetErr(&stderr)
	cmdWarn.SetArgs([]string{"cross-task", "sneat-co/backstage", "trakhimenok/workbench", "--mode", "manual", "--initiator", "alex", "--original-prompt-file", "p.txt"})
	if err := cmdWarn.Execute(); err != nil {
		t.Fatalf("multi-repo create without umbrella must not block: %v", err)
	}
	if capturedUmbrella != "" {
		t.Fatalf("unexpected umbrella %q", capturedUmbrella)
	}
	if !strings.Contains(stderr.String(), "spans 2 repositories and has no umbrella") ||
		!strings.Contains(stderr.String(), "wb worktree set cross-task --umbrella") {
		t.Fatalf("expected multi-repo warning on stderr, got: %q", stderr.String())
	}

	// 2. Multi-repo create WITH --umbrella records canonical ref and does not warn.
	stdout.Reset()
	stderr.Reset()
	cmdWithUmbrella := NewCreate(runtime, CreateDependencies{
		ResolveUmbrella: func(_ context.Context, _ string, _ []string, raw string) (umbrella.Ref, error) {
			return umbrella.Parse(raw, "")
		},
		PrepareLog: func(_ string, _ string, opts worktrees.WorkLogOptions) (worktrees.WorkLogOptions, error) {
			capturedUmbrella = opts.Umbrella
			return opts, nil
		},
		RegisteredIdentity: func() (worktrees.AgentIdentity, bool) { return worktrees.AgentIdentity{}, true },
		BeforeCreate:       func(string, []string) error { return nil },
		Claim: func(*cobra.Command, bool, string, string) worktreerun.RemoteClaimOutcome {
			return worktreerun.RemoteClaimOutcome{}
		},
		AfterCreate: func(*cobra.Command, string, []worktrees.CreateResult) {},
		Run: func(_ context.Context, repos []string, opts worktrees.CreateOptions) ([]worktrees.CreateResult, error) {
			return []worktrees.CreateResult{{Repository: repos[0], Branch: opts.Operation}}, nil
		},
	})
	cmdWithUmbrella.SetOut(&stdout)
	cmdWithUmbrella.SetErr(&stderr)
	cmdWithUmbrella.SetArgs([]string{"cross-task", "sneat-co/backstage", "trakhimenok/workbench", "--umbrella", "sneat-co/backstage#845", "--mode", "manual", "--initiator", "alex", "--original-prompt-file", "p.txt"})
	if err := cmdWithUmbrella.Execute(); err != nil {
		t.Fatalf("create with --umbrella failed: %v", err)
	}
	if capturedUmbrella != "github:sneat-co/backstage#845" {
		t.Fatalf("captured umbrella = %q", capturedUmbrella)
	}
	if strings.Contains(stderr.String(), "has no umbrella") {
		t.Fatalf("unexpected warning when --umbrella provided: %q", stderr.String())
	}

	// 3. Unknown registry on create --umbrella refuses before creating worktrees.
	cmdBadReg := NewCreate(runtime, CreateDependencies{
		BeforeCreate: func(string, []string) error { t.Fatal("BeforeCreate should not run on invalid umbrella"); return nil },
	})
	cmdBadReg.SetArgs([]string{"cross-task", "sneat-co/backstage", "--umbrella", "synchestra:eff-1", "--mode", "manual", "--initiator", "alex", "--original-prompt-file", "p.txt"})
	if err := cmdBadReg.Execute(); err == nil || !strings.Contains(err.Error(), "synchestra") {
		t.Fatalf("create --umbrella synchestra error = %v", err)
	}
}

func TestWorktreeSetUmbrellaAndSummaryAndLifecycleStatus(t *testing.T) {
	t.Parallel()
	projectsRoot := t.TempDir()
	runtime := shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: projectsRoot} }}
	seedCmdWorktreeUmbrellaClaim(t, projectsRoot, "cross-task", "sneat-co/backstage", "", true)
	seedCmdWorktreeUmbrellaClaim(t, projectsRoot, "cross-task", "trakhimenok/workbench", "", true)

	// 1. wb worktree set <task> --umbrella attaches to all claims (text + json).
	var out bytes.Buffer
	cmdSet := NewSet(runtime, SetDependencies{
		ResolveUmbrella: func(_ context.Context, _ string, _ []string, raw string) (umbrella.Ref, error) {
			return umbrella.Parse(raw, "")
		},
	})
	cmdSet.SetOut(&out)
	cmdSet.SetArgs([]string{"cross-task", "--umbrella", "sneat-co/backstage#845"})
	if err := cmdSet.Execute(); err != nil {
		t.Fatalf("worktree set --umbrella text: %v", err)
	}
	if !strings.Contains(out.String(), "recorded umbrella github:sneat-co/backstage#845 for task cross-task (2 claim(s), 2 repository/repositories)") {
		t.Fatalf("unexpected set output: %q", out.String())
	}

	out.Reset()
	cmdSetJSON := NewSet(runtime, SetDependencies{
		ResolveUmbrella: func(_ context.Context, _ string, _ []string, raw string) (umbrella.Ref, error) {
			return umbrella.Parse(raw, "")
		},
	})
	cmdSetJSON.SetOut(&out)
	cmdSetJSON.SetArgs([]string{"cross-task", "--umbrella", "sneat-co/backstage#845", "--format", "json"})
	if err := cmdSetJSON.Execute(); err != nil {
		t.Fatalf("worktree set --umbrella json: %v", err)
	}
	if !strings.Contains(out.String(), `"github:sneat-co/backstage#845"`) {
		t.Fatalf("unexpected set json output: %q", out.String())
	}

	// Refusals: unknown registry, setter error, invalid format.
	cmdSetBad := NewSet(runtime, SetDependencies{})
	cmdSetBad.SetArgs([]string{"cross-task", "--umbrella", "synchestra:eff-1"})
	if err := cmdSetBad.Execute(); err == nil || !strings.Contains(err.Error(), "synchestra") {
		t.Fatalf("set --umbrella synchestra error = %v", err)
	}
	cmdSetErr := NewSet(runtime, SetDependencies{
		ResolveUmbrella: func(_ context.Context, _ string, _ []string, raw string) (umbrella.Ref, error) {
			return umbrella.Parse(raw, "")
		},
		SetUmbrella: func(context.Context, string, string, umbrella.Ref) (worktrees.SetUmbrellaResult, error) {
			return worktrees.SetUmbrellaResult{}, errors.New("set boom")
		},
	})
	cmdSetErr.SetArgs([]string{"cross-task", "--umbrella", "sneat-co/backstage#845"})
	if err := cmdSetErr.Execute(); err == nil || !strings.Contains(err.Error(), "set boom") {
		t.Fatalf("set error = %v", err)
	}
	cmdSetFmt := NewSet(runtime, SetDependencies{})
	cmdSetFmt.SetArgs([]string{"cross-task", "--format", "xml"})
	if err := cmdSetFmt.Execute(); err == nil {
		t.Fatal("set accepted invalid format")
	}

	// 2. wb worktree summary <task> shows umbrella, recorded members, and close offer.
	out.Reset()
	cmdSummary := NewSummary(runtime, func(context.Context, worktrees.ListOptions) (worktrees.ListOutcome, error) {
		return worktrees.ListOutcome{
			Results: []worktrees.ListResult{
				{Task: "cross-task", Repository: "sneat-co/backstage", Branch: "cross-task", Base: "main", HeadSHA: strings.Repeat("b", 40), Clean: true},
			},
		}, nil
	})
	cmdSummary.SetOut(&out)
	cmdSummary.SetArgs([]string{"cross-task"})
	if err := cmdSummary.Execute(); err != nil {
		t.Fatalf("summary: %v", err)
	}
	for _, want := range []string{
		"umbrella: github:sneat-co/backstage#845",
		"## sneat-co/backstage",
		"## trakhimenok/workbench",
		"all umbrella members finished; close with: wb umbrella close sneat-co/backstage#845 --apply",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("summary output missing %q:\n%s", want, out.String())
		}
	}

	// 3. postUmbrellaStatusAndOfferClose posts status and prints close offer (and warns on error).
	var posted []string
	providers := umbrella.DefaultProviders(func(_ context.Context, args ...string) umbrella.CommandResult {
		posted = append(posted, strings.Join(args, " "))
		return umbrella.CommandResult{Stdout: []byte(`{"id":1}`)}
	})
	var statusOut, statusErr bytes.Buffer
	dummy := &cobra.Command{}
	dummy.SetOut(&statusOut)
	dummy.SetErr(&statusErr)
	postUmbrellaStatusAndOfferClose(context.Background(), dummy, projectsRoot, providers, "text", umbrella.StatusUpdate{
		Task:       "cross-task",
		Repository: "sneat-co/backstage",
		Outcome:    "retired",
		LandedSHA:  strings.Repeat("b", 40),
	}, "")
	if len(posted) != 1 || !strings.Contains(statusOut.String(), "wb umbrella close sneat-co/backstage#845 --apply") {
		t.Fatalf("posted=%v stdout=%q", posted, statusOut.String())
	}

	// Error path writes warning to stderr.
	failProviders := umbrella.DefaultProviders(func(context.Context, ...string) umbrella.CommandResult {
		return umbrella.CommandResult{Stderr: []byte("boom"), Err: errors.New("exit 1")}
	})
	postUmbrellaStatusAndOfferClose(context.Background(), dummy, projectsRoot, failProviders, "text", umbrella.StatusUpdate{
		Task:       "cross-task",
		Repository: "sneat-co/backstage",
		Outcome:    "archived",
		ArchiveRef: "retired/ref",
	}, "github:sneat-co/backstage#845")
	if !strings.Contains(statusErr.String(), "warning: failed to post status to umbrella") {
		t.Fatalf("expected warning on stderr, got %q", statusErr.String())
	}

	// writeWorktreeMergeReceipt displays umbrella and close offer.
	var receiptOut bytes.Buffer
	err := writeWorktreeMergeReceipt(&receiptOut, "text", orchestrate.WorktreeMergeReceipt{
		Status:               orchestrate.WorktreeMergeComplete,
		Repository:           "sneat-co/backstage",
		Target:               "main",
		Umbrella:             "github:sneat-co/backstage#845",
		UmbrellaCloseCommand: "wb umbrella close sneat-co/backstage#845 --apply",
	})
	if err != nil {
		t.Fatalf("writeWorktreeMergeReceipt: %v", err)
	}
	if !strings.Contains(receiptOut.String(), "umbrella: github:sneat-co/backstage#845") ||
		!strings.Contains(receiptOut.String(), "wb umbrella close sneat-co/backstage#845 --apply") {
		t.Fatalf("receipt output = %q", receiptOut.String())
	}

	// Retire --preserve and abort --apply post status lines to umbrella.
	var retirePosted []string
	retireProviders := umbrella.DefaultProviders(func(_ context.Context, args ...string) umbrella.CommandResult {
		retirePosted = append(retirePosted, strings.Join(args, " "))
		return umbrella.CommandResult{Stdout: []byte(`{"id":1}`)}
	})
	cmdPreserve := NewRetire(runtime, RetireDependencies{
		Preserve: PreserveDependencies{
			Admit: func(*cobra.Command, bool) (worktrees.AgentIdentity, func(), error) {
				return worktrees.AgentIdentity{}, func() {}, nil
			},
			CheckOwnership:      func(context.Context, string, string) error { return nil },
			ReleaseWhenComplete: func(*cobra.Command, string, string) bool { return false },
			UmbrellaProviders:   retireProviders,
			Run: func(context.Context, worktrees.RetireOptions) (worktrees.RetireResult, error) {
				return worktrees.RetireResult{
					Task: "cross-task", Repository: "sneat-co/backstage", Branch: "cross-task",
					ArchiveRepository: "sneat-co/.retired", ArchiveRef: "retired/backstage/x",
					RetiredRef: "retired/x", Phase: "complete",
				}, nil
			},
		},
	})
	cmdPreserve.SetOut(&bytes.Buffer{})
	cmdPreserve.SetArgs([]string{"cross-task", "--preserve", "tag", "--apply"})
	if err := cmdPreserve.Execute(); err != nil {
		t.Fatalf("retire --preserve --apply: %v", err)
	}
	if len(retirePosted) != 1 || !strings.Contains(retirePosted[0], "archived") {
		t.Fatalf("retire --preserve posted = %v", retirePosted)
	}

	var abortPosted []string
	abortProviders := umbrella.DefaultProviders(func(_ context.Context, args ...string) umbrella.CommandResult {
		abortPosted = append(abortPosted, strings.Join(args, " "))
		return umbrella.CommandResult{Stdout: []byte(`{"id":1}`)}
	})
	cmdAbort := NewAbort(runtime, AbortDependencies{
		UmbrellaProviders: abortProviders,
		Release:           func(*cobra.Command, string, string) bool { return false },
		SkipRelease:       func(*cobra.Command, string) {},
		Run: func(context.Context, worktrees.AbortOptions) ([]worktrees.AbortResult, error) {
			return []worktrees.AbortResult{{
				ListResult:   worktrees.ListResult{Task: "cross-task", Repository: "sneat-co/backstage", Umbrella: "github:sneat-co/backstage#845"},
				Disposition:  worktrees.AbortDiscarded,
				Applied:      true,
				DirtyCapture: &worktrees.DirtyWorktreeEvidence{SHA256: "abc123"},
			}}, nil
		},
	})
	cmdAbort.SetOut(&bytes.Buffer{})
	cmdAbort.SetArgs([]string{"cross-task", "--disposition", "discarded", "--apply"})
	if err := cmdAbort.Execute(); err != nil {
		t.Fatalf("abort --apply: %v", err)
	}
	if len(abortPosted) != 1 || !strings.Contains(abortPosted[0], "aborted (discarded)") {
		t.Fatalf("abort posted = %v", abortPosted)
	}
}

func TestCmdWorktreeUmbrellaRemainingBranches(t *testing.T) {
	t.Parallel()
	projectsRoot := t.TempDir()
	runtime := shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: projectsRoot} }}
	seedCmdWorktreeUmbrellaClaim(t, projectsRoot, "t-active", "o/r", "github:o/r#1", false)

	// resolveUmbrellaWithProviders: valid with fake provider, parse error, default-registry conflict.
	fakeProv := umbrella.DefaultProviders(func(context.Context, ...string) umbrella.CommandResult {
		return umbrella.CommandResult{Stdout: []byte(`{"state":"open"}`)}
	})
	if ref, err := resolveUmbrellaWithProviders(context.Background(), projectsRoot, nil, "o/r#1", fakeProv); err != nil || ref.String() != "github:o/r#1" {
		t.Fatalf("resolveUmbrellaWithProviders valid = (%+v, %v)", ref, err)
	}
	if _, err := resolveUmbrellaWithProviders(context.Background(), projectsRoot, nil, "bad-ref", fakeProv); err == nil {
		t.Fatal("resolveUmbrellaWithProviders accepted bad ref")
	}
	badRepo := filepath.Join(projectsRoot, "o", "badcfg")
	if err := os.MkdirAll(filepath.Join(badRepo, ".wb"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badRepo, ".wb", "worktrees.yaml"), []byte("version: 99\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveUmbrellaWithProviders(context.Background(), projectsRoot, []string{"o/badcfg"}, "o/r#1", fakeProv); err == nil {
		t.Fatal("resolveUmbrellaWithProviders ignored invalid repo config")
	}

	// postUmbrellaStatusAndOfferClose: invalid umbrella ref returns silently; nil providers with canceled ctx warns.
	var errBuf bytes.Buffer
	dummy := &cobra.Command{}
	dummy.SetOut(&bytes.Buffer{})
	dummy.SetErr(&errBuf)
	postUmbrellaStatusAndOfferClose(context.Background(), dummy, projectsRoot, fakeProv, "text", umbrella.StatusUpdate{Task: "t", Repository: "o/r", Outcome: "retired"}, "bad-ref")
	ctxCancel, cancel := context.WithCancel(context.Background())
	cancel()
	postUmbrellaStatusAndOfferClose(ctxCancel, dummy, projectsRoot, nil, "json", umbrella.StatusUpdate{Task: "t", Repository: "o/r", Outcome: "retired"}, "github:o/r#1")
	if !strings.Contains(errBuf.String(), "warning: failed to post status") {
		t.Fatalf("expected stderr warning on canceled default provider, got %q", errBuf.String())
	}

	// writeUmbrellaCloseOffer: invalid ref and unready umbrella both return nil with no output.
	var offerOut bytes.Buffer
	if err := writeUmbrellaCloseOffer(&offerOut, projectsRoot, "bad-ref"); err != nil || offerOut.Len() != 0 {
		t.Fatalf("writeUmbrellaCloseOffer bad-ref = (%v, %q)", err, offerOut.String())
	}
	if err := writeUmbrellaCloseOffer(&offerOut, projectsRoot, "github:o/r#1"); err != nil || offerOut.Len() != 0 {
		t.Fatalf("writeUmbrellaCloseOffer unready = (%v, %q)", err, offerOut.String())
	}

	// NewSet: JSON output with prompt + umbrella, and text writer failure on umbrella line.
	var out bytes.Buffer
	cmdBoth := NewSet(runtime, SetDependencies{
		Steer: func(context.Context, worktrees.LogSteerOptions) (worktrees.LogVerbResult, error) {
			return worktrees.LogVerbResult{Prompt: "0001-human.md"}, nil
		},
		ResolveUmbrella: func(_ context.Context, _ string, _ []string, raw string) (umbrella.Ref, error) {
			return umbrella.Parse(raw, "")
		},
		SetUmbrella: func(context.Context, string, string, umbrella.Ref) (worktrees.SetUmbrellaResult, error) {
			return worktrees.SetUmbrellaResult{Task: "t", Umbrella: "github:o/r#1", Claims: 1, Repositories: []string{"o/r"}}, nil
		},
	})
	cmdBoth.SetOut(&out)
	cmdBoth.SetArgs([]string{"t", "--prompt", "note", "--umbrella", "o/r#1", "--format", "json"})
	if err := cmdBoth.Execute(); err != nil {
		t.Fatalf("set prompt+umbrella json: %v", err)
	}
	if !strings.Contains(out.String(), `"prompt"`) || !strings.Contains(out.String(), `"umbrella"`) {
		t.Fatalf("unexpected set json: %s", out.String())
	}
	cmdWriteFail := NewSet(runtime, SetDependencies{
		ResolveUmbrella: func(_ context.Context, _ string, _ []string, raw string) (umbrella.Ref, error) {
			return umbrella.Parse(raw, "")
		},
		SetUmbrella: func(context.Context, string, string, umbrella.Ref) (worktrees.SetUmbrellaResult, error) {
			return worktrees.SetUmbrellaResult{Task: "t", Umbrella: "github:o/r#1", Claims: 1, Repositories: []string{"o/r"}}, nil
		},
	})
	cmdWriteFail.SetOut(&activeLimitedWriter{Allow: 0})
	cmdWriteFail.SetArgs([]string{"t", "--umbrella", "o/r#1"})
	if err := cmdWriteFail.Execute(); err == nil {
		t.Fatal("set umbrella write failure ignored")
	}
	cmdEmptyPrompt := NewSet(runtime, SetDependencies{})
	cmdEmptyPrompt.SetArgs([]string{"t", "--prompt", "   ", "--umbrella", "o/r#1"})
	if err := cmdEmptyPrompt.Execute(); err == nil {
		t.Fatal("set with whitespace --prompt accepted")
	}

	// NewSummary: umbrella from ListResult fallback when no claim exists in projectsRoot; write errors on writeInventorySummary.
	out.Reset()
	cmdSum := NewSummary(runtime, func(context.Context, worktrees.ListOptions) (worktrees.ListOutcome, error) {
		return worktrees.ListOutcome{
			Results: []worktrees.ListResult{{Task: "live-only", Repository: "o/r", Branch: "b", Base: "main", HeadSHA: "1234", Clean: true, Umbrella: "github:o/r#9"}},
		}, nil
	})
	cmdSum.SetOut(&out)
	cmdSum.SetArgs([]string{"live-only"})
	if err := cmdSum.Execute(); err != nil || !strings.Contains(out.String(), "umbrella: github:o/r#9") {
		t.Fatalf("summary fallback = (%v, %q)", err, out.String())
	}
	recorded := []umbrella.Member{
		{Task: "t", Repository: "o/r2", State: "archived", PullRequest: 5, LandedSHA: strings.Repeat("f", 40), RestorePointer: "o/.retired:ref"},
	}
	live := []worktrees.ListResult{{Task: "t", Repository: "o/r1", Branch: "b", Base: "main", HeadSHA: "abc", Clean: true}}
	activeSweepWrites(t, 25, func(w *activeLimitedWriter) error {
		return writeInventorySummary(w, "t", live, false, "github:o/r#9", recorded)
	})

	// writeWorktreeMergeReceipt writer errors on umbrella and close-offer lines.
	receipt := orchestrate.WorktreeMergeReceipt{
		Status:               orchestrate.WorktreeMergeComplete,
		Repository:           "o/r",
		Target:               "main",
		Umbrella:             "github:o/r#9",
		UmbrellaCloseCommand: "wb umbrella close o/r#9 --apply",
	}
	activeSweepWrites(t, 3, func(w *activeLimitedWriter) error {
		return writeWorktreeMergeReceipt(w, "text", receipt)
	})

	// NewRetire landed cleanup --apply posts retired status with MergedPullRequest + HeadSHA fallback; preserve with RetiredRef fallback.
	var cleanupPosted []string
	cleanupProv := umbrella.DefaultProviders(func(_ context.Context, args ...string) umbrella.CommandResult {
		cleanupPosted = append(cleanupPosted, strings.Join(args, " "))
		return umbrella.CommandResult{Stdout: []byte(`{"id":1}`)}
	})
	cmdRetire := NewRetire(runtime, RetireDependencies{
		UmbrellaProviders: cleanupProv,
		Progress:          func(*cobra.Command, bool) InventoryProgress { return InventoryProgress{Finish: func() {}} },
		Release:           func(*cobra.Command, string, string) bool { return false },
		Run: func(context.Context, worktrees.CleanupOptions) (worktrees.CleanupOutcome, error) {
			return worktrees.CleanupOutcome{
				Results: []worktrees.CleanupResult{
					{ListResult: worktrees.ListResult{Task: "t-ret", Repository: "o/r", HeadSHA: strings.Repeat("c", 40), Umbrella: "github:o/r#1", MergedPullRequest: &worktrees.PullRequest{Number: 7}}, Applied: true},
					{ListResult: worktrees.ListResult{Task: "t-ret", Repository: "o/r2"}, Applied: false},
				},
			}, nil
		},
	})
	cmdRetire.SetOut(&bytes.Buffer{})
	cmdRetire.SetArgs([]string{"t-ret", "--apply"})
	if err := cmdRetire.Execute(); err != nil {
		t.Fatalf("retire --apply: %v", err)
	}
	if len(cleanupPosted) != 1 || !strings.Contains(cleanupPosted[0], "PR o/r#7") {
		t.Fatalf("retire cleanup posted = %v", cleanupPosted)
	}

	// Preserve fallback to RetiredRef when ArchiveRef is empty.
	cmdPreserveFallback := NewRetire(runtime, RetireDependencies{
		Preserve: PreserveDependencies{
			Admit: func(*cobra.Command, bool) (worktrees.AgentIdentity, func(), error) {
				return worktrees.AgentIdentity{}, func() {}, nil
			},
			CheckOwnership:      func(context.Context, string, string) error { return nil },
			ReleaseWhenComplete: func(*cobra.Command, string, string) bool { return false },
			UmbrellaProviders:   cleanupProv,
			Run: func(context.Context, worktrees.RetireOptions) (worktrees.RetireResult, error) {
				return worktrees.RetireResult{Task: "t-active", Repository: "o/r", Branch: "b", RetiredRef: "retired/only", Phase: "complete"}, nil
			},
		},
	})
	cmdPreserveFallback.SetOut(&bytes.Buffer{})
	cmdPreserveFallback.SetArgs([]string{"t-active", "--preserve", "branch", "--apply"})
	if err := cmdPreserveFallback.Execute(); err != nil {
		t.Fatalf("retire preserve fallback: %v", err)
	}

	// NewAbort PR label variants (ClosedPullRequest, OpenPullRequest, MergedPullRequest, skipped).
	cmdAbortAll := NewAbort(runtime, AbortDependencies{
		UmbrellaProviders: cleanupProv,
		Release:           func(*cobra.Command, string, string) bool { return false },
		SkipRelease:       func(*cobra.Command, string) {},
		Run: func(context.Context, worktrees.AbortOptions) ([]worktrees.AbortResult, error) {
			return []worktrees.AbortResult{
				{ListResult: worktrees.ListResult{Task: "t-active", Repository: "o/r1", Umbrella: "github:o/r#1"}, ClosedPullRequest: &worktrees.ClosedPullRequestEvidence{Number: 1}, Disposition: worktrees.AbortDiscarded, Applied: true},
				{ListResult: worktrees.ListResult{Task: "t-active", Repository: "o/r2", Umbrella: "github:o/r#1", OpenPullRequest: &worktrees.PullRequest{Number: 2}}, Disposition: worktrees.AbortDiscarded, Applied: true},
				{ListResult: worktrees.ListResult{Task: "t-active", Repository: "o/r3", Umbrella: "github:o/r#1", MergedPullRequest: &worktrees.PullRequest{Number: 3}}, Disposition: worktrees.AbortDiscarded, Applied: true},
				{ListResult: worktrees.ListResult{Task: "t-active", Repository: "o/r4"}, Excluded: true, Applied: false},
			}, nil
		},
	})
	cmdAbortAll.SetOut(&bytes.Buffer{})
	cmdAbortAll.SetArgs([]string{"t-active", "--disposition", "discarded", "--apply"})
	if err := cmdAbortAll.Execute(); err != nil {
		t.Fatalf("abort all PR variants: %v", err)
	}
}

func TestExplicitTypedUmbrellaBypassesConflictingRepositoryDefaults(t *testing.T) {
	t.Parallel()
	projectsRoot := t.TempDir()
	repo1 := filepath.Join(projectsRoot, "acme", "one")
	repo2 := filepath.Join(projectsRoot, "acme", "two")
	for _, pair := range []struct{ dir, reg string }{
		{repo1, "synchestra"},
		{repo2, "github"},
	} {
		if err := os.MkdirAll(filepath.Join(pair.dir, ".wb"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pair.dir, ".wb", "worktrees.yaml"),
			[]byte("version: 1\numbrella:\n  default_registry: "+pair.reg+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fakeProv := umbrella.DefaultProviders(func(context.Context, ...string) umbrella.CommandResult {
		return umbrella.CommandResult{Stdout: []byte(`{"state":"open"}`)}
	})
	// Bare reference across conflicting repos still refuses.
	if _, err := resolveUmbrellaWithProviders(context.Background(), projectsRoot, []string{"acme/one", "acme/two"}, "effort-1", fakeProv); err == nil || !strings.Contains(err.Error(), "disagree on umbrella.default_registry") {
		t.Fatalf("bare ref across conflicting repos error = %v", err)
	}
	// Explicit typed reference bypasses default resolution and succeeds!
	ref, err := resolveUmbrellaWithProviders(context.Background(), projectsRoot, []string{"acme/one", "acme/two"}, "github:acme/one#42", fakeProv)
	if err != nil || ref.String() != "github:acme/one#42" {
		t.Fatalf("explicit ref across conflicting repos = (%+v, %v), want github:acme/one#42", ref, err)
	}
}
