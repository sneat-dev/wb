package sessionlaunch

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/runner/runnertest"
	"github.com/sneat-dev/wb/internal/sessionauthority"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionpark"
	unix "github.com/sneat-dev/wb/internal/unixcompat"
)

// slCovGit runs git hermetically against one repository.
func slCovGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_AUTHOR_NAME=sl", "GIT_AUTHOR_EMAIL=sl@example.com", "GIT_COMMITTER_NAME=sl", "GIT_COMMITTER_EMAIL=sl@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// slCovRepo creates one committed repository on its WB session branch.
func slCovRepo(t *testing.T, branch string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	slCovGit(t, dir, "init", "-q", "-b", "main")
	slCovWrite(t, filepath.Join(dir, "tracked.txt"), 0o644, "tracked\n")
	slCovGit(t, dir, "add", "tracked.txt")
	slCovGit(t, dir, "commit", "-q", "-m", "initial")
	head := slCovGit(t, dir, "rev-parse", "HEAD")
	slCovGit(t, dir, "checkout", "-q", "-b", branch)
	return dir, head
}

func TestSlCovVerifyPinnedWorktreeParkedNeutral(t *testing.T) {
	t.Parallel()
	storeRoot := t.TempDir()
	handoffID := "handoff-123"
	neutral := filepath.Join(storeRoot, handoffID, sessionpark.LocalNeutralDirName)
	if err := os.MkdirAll(neutral, 0o700); err != nil {
		t.Fatal(err)
	}
	plan := slCovPlan(handoffID)
	plan.StoreRoot, plan.WorktreeDir = storeRoot, neutral
	plan.RootMode = string(sessionauthority.LaunchRootParkedNeutral)
	if err := verifyPinnedWorktree(context.Background(), plan); err != nil {
		t.Fatalf("exact parked-neutral root = %v", err)
	}
	t.Run("wrong worktree", func(t *testing.T) {
		t.Parallel()
		broken := plan
		broken.WorktreeDir = filepath.Join(storeRoot, handoffID, "other")
		if err := verifyPinnedWorktree(context.Background(), broken); err == nil {
			t.Fatal("accepted a foreign parked-neutral root")
		}
	})
	t.Run("wrong permissions", func(t *testing.T) {
		t.Parallel()
		public := filepath.Join(storeRoot, "handoff-public", sessionpark.LocalNeutralDirName)
		if err := os.MkdirAll(public, 0o755); err != nil {
			t.Fatal(err)
		}
		broken := plan
		broken.HandoffID, broken.WorktreeDir = "handoff-public", public
		if err := verifyPinnedWorktree(context.Background(), broken); err == nil {
			t.Fatal("accepted a public parked-neutral root")
		}
	})
	t.Run("missing root", func(t *testing.T) {
		t.Parallel()
		broken := plan
		broken.WorktreeDir = filepath.Join(storeRoot, handoffID, sessionpark.LocalNeutralDirName, "missing")
		if err := verifyPinnedWorktree(context.Background(), broken); err == nil {
			t.Fatal("accepted a missing parked-neutral root")
		}
	})
}

func TestSlCovVerifyPinnedWorktreeAgainstRealGit(t *testing.T) {
	runnertest.AllowRealProcess(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Run("git unavailable", func(t *testing.T) {
			t.Setenv("PATH", t.TempDir())
			if err := verifyPinnedWorktree(context.Background(), slCovPlan("handoff-123")); err == nil || !strings.Contains(err.Error(), "git executable is unavailable") {
				t.Fatalf("missing git = %v", err)
			}
		})
		return
	}
	const handoffID = "handoff-123"
	repo, head := slCovRepo(t, "wb-session/"+handoffID)
	plan := slCovPlan(handoffID)
	plan.StoreRoot, plan.WorktreeDir, plan.PinnedCommit = t.TempDir(), repo, head
	plan.PinnedBranch = "wb-session/" + handoffID
	if err := verifyPinnedWorktree(context.Background(), plan); err != nil {
		t.Fatalf("clean pinned worktree = %v", err)
	}
	t.Run("wrong head", func(t *testing.T) {
		t.Parallel()
		broken := plan
		broken.PinnedCommit = strings.Repeat("a", 40)
		if err := verifyPinnedWorktree(context.Background(), broken); err == nil {
			t.Fatal("accepted a foreign HEAD")
		}
	})
	t.Run("wrong branch", func(t *testing.T) {
		t.Parallel()
		broken := plan
		broken.PinnedBranch = "wb-session/other"
		if err := verifyPinnedWorktree(context.Background(), broken); err == nil {
			t.Fatal("accepted a foreign branch")
		}
	})
	// Left serial (not parallel with its siblings): it writes an untracked
	// file into the shared repo worktree that a sibling ("legacy empty
	// pinned branch falls back") asserts is clean.
	t.Run("dirty pinned worktree", func(t *testing.T) {
		slCovWrite(t, filepath.Join(repo, "untracked.txt"), 0o644, "dirty\n")
		t.Cleanup(func() { _ = os.Remove(filepath.Join(repo, "untracked.txt")) })
		if err := verifyPinnedWorktree(context.Background(), plan); err == nil || !strings.Contains(err.Error(), "dirty") {
			t.Fatalf("dirty pinned worktree = %v", err)
		}
		t.Run("dirty parked-local worktree is allowed", func(t *testing.T) {
			t.Parallel()
			parked := plan
			parked.RootMode = string(sessionauthority.LaunchRootParkedLocal)
			if err := verifyPinnedWorktree(context.Background(), parked); err != nil {
				t.Fatalf("dirty parked-local worktree = %v", err)
			}
		})
	})
	t.Run("legacy empty pinned branch falls back", func(t *testing.T) {
		t.Parallel()
		legacy := plan
		legacy.PinnedBranch = ""
		if err := verifyPinnedWorktree(context.Background(), legacy); err != nil {
			t.Fatalf("legacy pinned branch = %v", err)
		}
		legacy.PinnedBranch = "wb-session/handoff-other"
		if err := verifyPinnedWorktree(context.Background(), legacy); err == nil {
			t.Fatal("accepted a foreign legacy branch")
		}
	})
}

// slCovAuthorityPlan builds one internally consistent private authority, its
// immutable launch plan, and the derived options/plan pair.
func slCovAuthorityPlan(t *testing.T) (sessionauthority.Launch, launchPlan, resolvedAuthority, Options, string) {
	t.Helper()
	worktree := "/target/worktree"
	storeRoot := "/target/store"
	authority := sessionauthority.Launch{
		AggregateID: "handoff-123", AggregateDigest: string(slCovDigest("request")), AggregateFile: "request.json",
		SuccessorWBSessionID: "wbs-successor", PredecessorWBSessionID: "wbs-source",
		TargetMachine: "hetzner-vm1", SourceRuntime: RuntimeCodex, SourceModel: "gpt-5",
		PinnedCommit: strings.Repeat("b", 40), PinnedBranch: "wb-session/handoff-123",
		ContinuationKind:   sessionauthority.ContinuationTracked,
		ContinuationPath:   ".wb/handoffs/handoff-123.md",
		ContinuationDigest: string(slCovDigest("handover\n")),
	}
	spec, err := harnessSpecForAuthority(authority, worktree)
	if err != nil {
		t.Fatal(err)
	}
	plan := launchPlan{
		SchemaVersion: launchSchemaVersion, HandoffID: authority.AggregateID,
		RequestDigest:        sessionmove.Digest(authority.AggregateDigest),
		SuccessorWBSessionID: authority.SuccessorWBSessionID, PredecessorWBSessionID: authority.PredecessorWBSessionID,
		Machine: authority.TargetMachine, TmuxName: "wb-session-" + authority.SuccessorWBSessionID,
		Runtime: spec.Runtime, Model: spec.Model, StoreRoot: storeRoot, WorktreeDir: worktree,
		PinnedCommit: authority.PinnedCommit, PinnedBranch: authority.PinnedBranch,
		RootMode:     string(authority.RootMode),
		HandoverPath: authority.ContinuationPath, AuthorityFile: authority.AggregateFile,
		ContinuationKind: string(authority.ContinuationKind), ContinuationDigest: sessionmove.Digest(authority.ContinuationDigest),
		HarnessExecutable: "/bin/" + spec.Executable, HarnessArgs: spec.Args,
	}
	resolved := resolvedAuthority{launch: authority, storeRoot: storeRoot}
	options := Options{PinnedCommit: authority.PinnedCommit}
	return authority, plan, resolved, options, worktree
}

func TestSlCovValidatePlanForOptionsRejectsEveryDivergence(t *testing.T) {
	t.Parallel()
	authority, plan, resolved, options, worktree := slCovAuthorityPlan(t)
	if err := validatePlanForOptions(plan, options, resolved, worktree); err != nil {
		t.Fatalf("consistent legacy plan = %v", err)
	}
	withAuthority := options
	withAuthority.Authority = &authority
	if err := validatePlanForOptions(plan, withAuthority, resolved, worktree); err != nil {
		t.Fatalf("consistent parked plan = %v", err)
	}
	tests := map[string]func(*launchPlan, *Options){
		"schema version": func(p *launchPlan, _ *Options) { p.SchemaVersion++ },
		"handoff id":     func(p *launchPlan, _ *Options) { p.HandoffID = "other" },
		"request digest": func(p *launchPlan, _ *Options) { p.RequestDigest = slCovDigest("other") },
		"successor":      func(p *launchPlan, _ *Options) { p.SuccessorWBSessionID = "other" },
		"predecessor":    func(p *launchPlan, _ *Options) { p.PredecessorWBSessionID = "other" },
		"machine":        func(p *launchPlan, _ *Options) { p.Machine = "other" },
		"tmux name":      func(p *launchPlan, _ *Options) { p.TmuxName = "other" },
		"store root":     func(p *launchPlan, _ *Options) { p.StoreRoot = "/other" },
		"worktree":       func(p *launchPlan, _ *Options) { p.WorktreeDir = "/other" },
		"pinned commit":  func(p *launchPlan, _ *Options) { p.PinnedCommit = strings.Repeat("c", 40) },
		"root mode":      func(p *launchPlan, _ *Options) { p.RootMode = string(sessionauthority.LaunchRootParkedNeutral) },
		"handover path":  func(p *launchPlan, _ *Options) { p.HandoverPath = "other.md" },
		"pinned branch legacy": func(p *launchPlan, _ *Options) {
			p.PinnedBranch = "wb-session/other"
		},
		"authority file legacy": func(p *launchPlan, _ *Options) { p.AuthorityFile = "other.json" },
		"continuation kind legacy": func(p *launchPlan, _ *Options) {
			p.ContinuationKind = "other"
		},
		"continuation digest legacy": func(p *launchPlan, _ *Options) {
			p.ContinuationDigest = slCovDigest("other")
		},
		"harness model": func(p *launchPlan, _ *Options) { p.Model = "claude-opus" },
		"harness args":  func(p *launchPlan, _ *Options) { p.HarnessArgs = []string{"other"} },
		"harness runtime": func(p *launchPlan, _ *Options) {
			p.Runtime = RuntimeClaudeCode
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			broken, brokenOptions := plan, options
			mutate(&broken, &brokenOptions)
			if err := validatePlanForOptions(broken, brokenOptions, resolved, worktree); err == nil {
				t.Fatalf("divergence %q was accepted", name)
			}
		})
	}
	t.Run("parked authority exact fields", func(t *testing.T) {
		t.Parallel()
		broken := plan
		broken.PinnedBranch = "wb-session/other"
		brokenOptions := options
		brokenOptions.Authority = &authority
		if err := validatePlanForOptions(broken, brokenOptions, resolved, worktree); err == nil || !strings.Contains(err.Error(), "parked-session authority") {
			t.Fatalf("parked divergence = %v", err)
		}
	})
	t.Run("legacy optional fields may be empty", func(t *testing.T) {
		t.Parallel()
		sparse := plan
		sparse.PinnedBranch, sparse.AuthorityFile, sparse.ContinuationKind, sparse.ContinuationDigest = "", "", "", ""
		if err := validatePlanForOptions(sparse, options, resolved, worktree); err != nil {
			t.Fatalf("sparse legacy plan = %v", err)
		}
	})
}

// slCovFence is a fully injectable sessionauthority.Fence.
type slCovFence struct {
	held      bool
	retainErr error
	handoff   *os.File
}

func (fence *slCovFence) HeldForSession(string, string, string) bool { return fence.held }

func (fence *slCovFence) RetainSessionDir(string, string, string) (*os.File, error) {
	if fence.retainErr != nil {
		return nil, fence.retainErr
	}
	fd, err := unix.Dup(int(fence.handoff.Fd()))
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "sl-cov-retained-handoff"), nil
}

// slCovAuthorityFixture is a hermetic parked-neutral launch environment whose
// aggregate directory is reachable only through the retained fence descriptor.
type slCovAuthorityFixture struct {
	root      string
	handoffID string
	worktree  string
	sessions  string
	authority sessionauthority.Launch
	plan      launchPlan
	state     *launchState
	fence     *slCovFence
	deps      dependencies
	tmux      *slCovFlexTmux
}

func slCovNewAuthorityFixture(t *testing.T) *slCovAuthorityFixture {
	t.Helper()
	root := filepath.Join(t.TempDir(), "parked-sessions")
	handoffID := "handoff-123"
	handoffDir := filepath.Join(root, handoffID)
	worktree := filepath.Join(handoffDir, sessionpark.LocalNeutralDirName)
	if err := os.MkdirAll(worktree, 0o700); err != nil {
		t.Fatal(err)
	}
	authority := sessionauthority.Launch{
		AggregateID: handoffID, AggregateDigest: string(slCovDigest("request")), AggregateFile: "request.json",
		SuccessorWBSessionID: "wbs-successor", PredecessorWBSessionID: "wbs-source",
		TargetMachine: "hetzner-vm1", SourceRuntime: RuntimeCodex, SourceModel: "gpt-5",
		ContinuationKind:   sessionauthority.ContinuationPrivate,
		ContinuationPath:   filepath.Join(handoffDir, "successor-context.md"),
		ContinuationDigest: string(slCovDigest("private")),
		RootMode:           sessionauthority.LaunchRootParkedNeutral,
	}
	if err := authority.Validate(); err != nil {
		t.Fatal(err)
	}
	spec, err := harnessSpecForAuthority(authority, worktree)
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	plan := launchPlan{
		SchemaVersion: launchSchemaVersion, HandoffID: authority.AggregateID,
		RequestDigest:        sessionmove.Digest(authority.AggregateDigest),
		SuccessorWBSessionID: authority.SuccessorWBSessionID, PredecessorWBSessionID: authority.PredecessorWBSessionID,
		Machine: authority.TargetMachine, TmuxName: "wb-session-" + authority.SuccessorWBSessionID,
		Runtime: spec.Runtime, Model: spec.Model, StoreRoot: root, WorktreeDir: worktree,
		PinnedCommit: authority.PinnedCommit, PinnedBranch: authority.PinnedBranch,
		RootMode:     string(authority.RootMode),
		HandoverPath: authority.ContinuationPath, AuthorityFile: authority.AggregateFile,
		ContinuationKind: string(authority.ContinuationKind), ContinuationDigest: sessionmove.Digest(authority.ContinuationDigest),
		WBExecutable: slCovExecutable(t, binDir, "wb"), HarnessExecutable: slCovExecutable(t, binDir, spec.Executable),
		HarnessArgs: spec.Args,
	}
	handoff, err := os.Open(handoffDir)
	if err != nil {
		t.Fatal(err)
	}
	state, err := openLaunchStateFromHandoff(handoffID, handoff, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := state.savePlan(plan); err != nil {
		t.Fatal(err)
	}
	fenceHandoff, err := os.Open(handoffDir)
	if err != nil {
		t.Fatal(err)
	}
	tmux := &slCovFlexTmux{}
	sessions := filepath.Join(t.TempDir(), "sessions")
	deps := dependencies{
		tmux:         tmux,
		lookPath:     func(name string) (string, error) { return filepath.Join(binDir, name), nil },
		wbExecutable: func() (string, error) { return plan.WBExecutable, nil },
		sessionDir:   func(string) (string, error) { return sessions, nil },
		now:          func() time.Time { return slCovParkNow },
		pollInterval: time.Millisecond, startTimeout: 50 * time.Millisecond,
		verifyPinned:  func(context.Context, launchPlan) error { return nil },
		processStatus: func(int) error { return syscall.ESRCH },
	}
	slCovExecutable(t, binDir, "codex")
	slCovExecutable(t, binDir, "wb")
	fixture := &slCovAuthorityFixture{
		root: root, handoffID: handoffID, worktree: worktree, sessions: sessions, authority: authority,
		plan: plan, state: state, fence: &slCovFence{held: true, handoff: fenceHandoff}, deps: deps, tmux: tmux,
	}
	t.Cleanup(func() {
		_ = state.Close()
		_ = fenceHandoff.Close()
	})
	return fixture
}

func (fixture *slCovAuthorityFixture) options() Options {
	authority := fixture.authority
	return Options{Authority: &authority, StoreRoot: fixture.root, Fence: fixture.fence,
		WorktreeDir: fixture.worktree, PinnedCommit: authority.PinnedCommit, ProjectsRoot: fixture.root}
}

func TestSlCovStartWithAuthorityRejectsEveryGate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("missing fence", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		options := fixture.options()
		options.Fence = nil
		if _, err := startWithDependencies(ctx, options, fixture.deps); err == nil || !strings.Contains(err.Error(), "requires the exact held handoff execution lock") {
			t.Fatalf("missing fence = %v", err)
		}
	})
	t.Run("unheld fence", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		fixture.fence.held = false
		if _, err := startWithDependencies(ctx, fixture.options(), fixture.deps); err == nil {
			t.Fatal("start accepted an unheld fence")
		}
	})
	t.Run("pinned commit mismatch", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		options := fixture.options()
		options.PinnedCommit = strings.Repeat("d", 40)
		if _, err := startWithDependencies(ctx, options, fixture.deps); err == nil || !strings.Contains(err.Error(), "does not match admitted commit") {
			t.Fatalf("pinned commit mismatch = %v", err)
		}
	})
	t.Run("relative worktree", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		options := fixture.options()
		options.WorktreeDir = "relative"
		if _, err := startWithDependencies(ctx, options, fixture.deps); err == nil {
			t.Fatal("start accepted a relative worktree")
		}
	})
	t.Run("unclean store root", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		options := fixture.options()
		options.StoreRoot = fixture.root + "/"
		if _, err := startWithDependencies(ctx, options, fixture.deps); err == nil {
			t.Fatal("start accepted an unclean store root")
		}
	})
	t.Run("retain error", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		fixture.fence.retainErr = errors.New("retain failed")
		if _, err := startWithDependencies(ctx, fixture.options(), fixture.deps); err == nil || !strings.Contains(err.Error(), "retain failed") {
			t.Fatalf("retain error = %v", err)
		}
	})
	t.Run("unopenable aggregate", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		if err := os.MkdirAll(filepath.Join(fixture.root, "collide"), 0o700); err != nil {
			t.Fatal(err)
		}
		slCovWrite(t, filepath.Join(fixture.root, "collide", launchDirectoryName), 0o600, "not a directory")
		collide := fixture.authority
		collide.AggregateID = "collide"
		collide.ContinuationPath = filepath.Join(fixture.root, "collide", "successor-context.md")
		options := Options{Authority: &collide, StoreRoot: fixture.root, Fence: fixture.fence,
			WorktreeDir: fixture.worktree, ProjectsRoot: fixture.root}
		if _, err := startWithDependencies(ctx, options, fixture.deps); err == nil {
			t.Fatal("start accepted an unopenable aggregate")
		}
	})
	t.Run("corrupt plan", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		slCovWrite(t, filepath.Join(fixture.root, fixture.handoffID, launchDirectoryName, "plan.json"), 0o600, "{}\n")
		if _, err := startWithDependencies(ctx, fixture.options(), fixture.deps); err == nil {
			t.Fatal("start accepted a corrupt plan")
		}
	})
	t.Run("unsupported harness", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		if err := os.Remove(filepath.Join(fixture.root, fixture.handoffID, launchDirectoryName, "plan.json")); err != nil {
			t.Fatal(err)
		}
		authority := fixture.authority
		authority.RequestedHarness = "bogus"
		options := fixture.options()
		options.Authority = &authority
		if _, err := startWithDependencies(ctx, options, fixture.deps); err == nil || !strings.Contains(err.Error(), "unsupported") {
			t.Fatalf("unsupported harness = %v", err)
		}
	})
	t.Run("harness lookup error", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		if err := os.Remove(filepath.Join(fixture.root, fixture.handoffID, launchDirectoryName, "plan.json")); err != nil {
			t.Fatal(err)
		}
		fixture.deps.lookPath = func(string) (string, error) { return "", errors.New("no harness") }
		if _, err := startWithDependencies(ctx, fixture.options(), fixture.deps); err == nil || !strings.Contains(err.Error(), "no harness") {
			t.Fatalf("harness lookup error = %v", err)
		}
	})
	t.Run("harness not executable", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		if err := os.Remove(filepath.Join(fixture.root, fixture.handoffID, launchDirectoryName, "plan.json")); err != nil {
			t.Fatal(err)
		}
		plain := filepath.Join(t.TempDir(), "harness")
		slCovWrite(t, plain, 0o644, "not executable")
		fixture.deps.lookPath = func(string) (string, error) { return plain, nil }
		if _, err := startWithDependencies(ctx, fixture.options(), fixture.deps); err == nil {
			t.Fatal("start accepted a non-executable harness")
		}
	})
	t.Run("wb executable lookup error", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		if err := os.Remove(filepath.Join(fixture.root, fixture.handoffID, launchDirectoryName, "plan.json")); err != nil {
			t.Fatal(err)
		}
		fixture.deps.wbExecutable = func() (string, error) { return "", errors.New("no wb") }
		if _, err := startWithDependencies(ctx, fixture.options(), fixture.deps); err == nil || !strings.Contains(err.Error(), "no wb") {
			t.Fatalf("wb lookup error = %v", err)
		}
	})
	t.Run("wb executable not executable", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		if err := os.Remove(filepath.Join(fixture.root, fixture.handoffID, launchDirectoryName, "plan.json")); err != nil {
			t.Fatal(err)
		}
		plain := filepath.Join(t.TempDir(), "wb")
		slCovWrite(t, plain, 0o644, "not executable")
		fixture.deps.wbExecutable = func() (string, error) { return plain, nil }
		if _, err := startWithDependencies(ctx, fixture.options(), fixture.deps); err == nil {
			t.Fatal("start accepted a non-executable WB binary")
		}
	})
	t.Run("plan publication failure", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		if err := os.Remove(filepath.Join(fixture.root, fixture.handoffID, launchDirectoryName, "plan.json")); err != nil {
			t.Fatal(err)
		}
		slCovReadOnly(t, filepath.Join(fixture.root, fixture.handoffID, launchDirectoryName))
		if _, err := startWithDependencies(ctx, fixture.options(), fixture.deps); err == nil {
			t.Fatal("start published a plan into a read-only launch directory")
		}
	})
	t.Run("invalid planned executables", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		broken := fixture.plan
		broken.WBExecutable = filepath.Join(t.TempDir(), "absent-wb")
		raw, err := encodeLaunchJSON(broken)
		if err != nil {
			t.Fatal(err)
		}
		planPath := filepath.Join(fixture.root, fixture.handoffID, launchDirectoryName, "plan.json")
		if err := os.Remove(planPath); err != nil {
			t.Fatal(err)
		}
		if created, err := fixture.state.publish("", "plan.json", raw); err != nil || !created {
			t.Fatalf("publish broken plan = %t %v", created, err)
		}
		if _, err := startWithDependencies(ctx, fixture.options(), fixture.deps); err == nil || !strings.Contains(err.Error(), "invalid WB executable") {
			t.Fatalf("invalid planned executables = %v", err)
		}
	})
	t.Run("pane probe error", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		fixture.tmux.panePIDErr = errors.New("pane probe failed")
		if _, err := startWithDependencies(ctx, fixture.options(), fixture.deps); err == nil || !strings.Contains(err.Error(), "pane probe failed") {
			t.Fatalf("pane probe error = %v", err)
		}
	})
	t.Run("corrupt abandonment", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		attempt, err := fixture.state.createAttempt()
		if err != nil {
			t.Fatal(err)
		}
		if created, err := attempt.publish("", "abandoned.json", []byte("{")); err != nil || !created {
			t.Fatalf("inject abandonment = %t %v", created, err)
		}
		_ = attempt.Close()
		if _, err := startWithDependencies(ctx, fixture.options(), fixture.deps); err == nil {
			t.Fatal("start accepted a corrupt abandonment artifact")
		}
	})
	t.Run("abandonment conflicts with live tmux", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		slCovSealAbandonment(t, fixture, 6301)
		fixture.tmux.pid, fixture.tmux.exists = 6301, true
		if _, err := startWithDependencies(ctx, fixture.options(), fixture.deps); err == nil || !strings.Contains(err.Error(), "conflicts with live tmux session") {
			t.Fatalf("live tmux abandonment = %v", err)
		}
	})
	t.Run("unbound abandonment", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		slCovSealAbandonment(t, fixture, 6302)
		attempt, err := latestAttempt(fixture.state)
		if err != nil {
			t.Fatal(err)
		}
		abandonment, err := attempt.loadAbandonment()
		if err != nil {
			t.Fatal(err)
		}
		_ = attempt.Close()
		abandonment.HandoffID = "other"
		raw, err := encodeLaunchJSON(abandonment)
		if err != nil {
			t.Fatal(err)
		}
		slCovWrite(t, filepath.Join(slCovAttemptDir(fixture.root, abandonment.AttemptID), "abandoned.json"), 0o600, string(raw))
		if _, err := startWithDependencies(ctx, fixture.options(), fixture.deps); err == nil {
			t.Fatal("start accepted an unbound abandonment")
		}
	})
	t.Run("verify pinned failure after abandonment", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		slCovSealAbandonment(t, fixture, 6303)
		fixture.deps.verifyPinned = func(context.Context, launchPlan) error { return errors.New("pinned changed") }
		if _, err := startWithDependencies(ctx, fixture.options(), fixture.deps); err == nil || !strings.Contains(err.Error(), "after launcher abandonment") {
			t.Fatalf("verify pinned failure = %v", err)
		}
	})
	t.Run("tmux start failure without adoption", func(t *testing.T) {
		t.Parallel()
		fixture := slCovNewAuthorityFixture(t)
		fixture.tmux.startErr = errors.New("duplicate session")
		if _, err := startWithDependencies(ctx, fixture.options(), fixture.deps); err == nil || !strings.Contains(err.Error(), "duplicate session") {
			t.Fatalf("start failure = %v", err)
		}
	})
}

// slCovSealAbandonment durably seals one dead pre-release wrapper attempt.
func slCovSealAbandonment(t *testing.T, fixture *slCovAuthorityFixture, pid int) {
	t.Helper()
	attempt, err := fixture.state.createAttempt()
	if err != nil {
		t.Fatal(err)
	}
	fence, err := attempt.acquireExecFence(pid)
	if err != nil {
		t.Fatal(err)
	}
	if err := fence.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := attempt.saveAbandonment(fixture.plan, mustPlanDigest(t, fixture), pid, fixture.deps.now()); err != nil {
		t.Fatal(err)
	}
	_ = attempt.Close()
}

func mustPlanDigest(t *testing.T, fixture *slCovAuthorityFixture) sessionmove.Digest {
	t.Helper()
	state, err := openLaunchState(fixture.root, fixture.handoffID, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	_, digest, err := state.loadPlan()
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func TestSlCovAuthorityFixtureHarnessSpecMatches(t *testing.T) {
	t.Parallel()
	fixture := slCovNewAuthorityFixture(t)
	spec, err := harnessSpecForAuthority(fixture.authority, fixture.worktree)
	if err != nil {
		t.Fatal(err)
	}
	if fixture.plan.Runtime != spec.Runtime || fixture.plan.Model != spec.Model ||
		filepath.Base(fixture.plan.HarnessExecutable) != spec.Executable {
		t.Fatalf("fixture plan does not match harness spec: %#v vs %#v", fixture.plan, spec)
	}
}
