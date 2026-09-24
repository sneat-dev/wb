package sessionlaunch

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionauthority"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionpark"
)

var slCovParkNow = time.Date(2026, time.August, 25, 12, 0, 0, 0, time.UTC)

// slCovLocalBundle builds one zero-worktree local park bundle.
func slCovLocalBundle() sessionpark.Bundle {
	return sessionpark.Bundle{
		SchemaVersion:   sessionpark.SchemaVersion,
		ParkedSessionID: "park-abc123",
		Source: session.Record{PID: 4242, WBSessionID: "wbs-source", Machine: "source-mac",
			Runtime: RuntimeCodex, Model: "gpt-5", StartedAt: slCovParkNow},
		Continuation: "parked continuation\n",
		ParkedAt:     slCovParkNow,
	}
}

// slCovParkFixture materializes one admitted local park aggregate and the
// immutable launch plan derived from it.
func slCovParkFixture(t *testing.T) (*launchState, launchPlan, sessionpark.Bundle, string) {
	t.Helper()
	bundle := slCovLocalBundle()
	storeRoot := filepath.Join(t.TempDir(), sessionpark.SourceDirName)
	aggregate := filepath.Join(storeRoot, bundle.ParkedSessionID)
	neutral := filepath.Join(aggregate, sessionpark.LocalNeutralDirName)
	if err := os.MkdirAll(neutral, 0o700); err != nil {
		t.Fatal(err)
	}
	continuation := bundle.Continuation + "successor context body\n"
	slCovWrite(t, filepath.Join(aggregate, sessionpark.SuccessorContextFileName), 0o600, continuation)
	raw, err := sessionpark.EncodeBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	slCovWrite(t, filepath.Join(aggregate, sessionpark.BundleFileName), 0o600, string(raw))
	digest := sessionmove.DigestBytes(raw)
	continuationPath := filepath.Join(storeRoot, bundle.ParkedSessionID, sessionpark.SuccessorContextFileName)
	authority, err := sessionpark.LocalLaunchAuthority(bundle, digest, continuationPath, []byte(continuation))
	if err != nil {
		t.Fatal(err)
	}
	spec, err := harnessSpecForAuthority(authority, neutral)
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	plan := launchPlan{
		SchemaVersion: launchSchemaVersion, HandoffID: authority.AggregateID,
		RequestDigest:        digest,
		SuccessorWBSessionID: authority.SuccessorWBSessionID, PredecessorWBSessionID: authority.PredecessorWBSessionID,
		Machine: authority.TargetMachine, TmuxName: "wb-session-" + authority.SuccessorWBSessionID,
		Runtime: spec.Runtime, Model: spec.Model, StoreRoot: storeRoot, WorktreeDir: neutral,
		PinnedCommit: authority.PinnedCommit, PinnedBranch: authority.PinnedBranch,
		RootMode:     string(authority.RootMode),
		HandoverPath: authority.ContinuationPath, AuthorityFile: sessionpark.BundleFileName,
		ContinuationKind: string(authority.ContinuationKind), ContinuationDigest: sessionmove.Digest(authority.ContinuationDigest),
		WBExecutable: slCovExecutable(t, binDir, "wb"), HarnessExecutable: slCovExecutable(t, binDir, spec.Executable),
		HarnessArgs: spec.Args,
	}
	state, err := openLaunchState(storeRoot, bundle.ParkedSessionID, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	if _, _, _, err := state.savePlan(plan); err != nil {
		t.Fatal(err)
	}
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(neutral); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	return state, plan, bundle, continuationPath
}

func TestSlCovValidatePrivateParkPlanLocalBundle(t *testing.T) {
	t.Run("exact bundle", func(t *testing.T) {
		state, plan, _, continuationPath := slCovParkFixture(t)
		got, err := validatePrivateParkPlan(state, plan)
		if err != nil || got != continuationPath {
			t.Fatalf("validatePrivateParkPlan = %q %v", got, err)
		}
	})
	t.Run("not private", func(t *testing.T) {
		state, plan, _, _ := slCovParkFixture(t)
		plan.ContinuationKind = ""
		if _, err := validatePrivateParkPlan(state, plan); err == nil {
			t.Fatal("accepted a non-private plan")
		}
	})
	t.Run("unknown authority file", func(t *testing.T) {
		state, plan, _, _ := slCovParkFixture(t)
		plan.AuthorityFile = "other.json"
		if _, err := validatePrivateParkPlan(state, plan); err == nil {
			t.Fatal("accepted an unknown authority artifact")
		}
	})
	t.Run("missing bundle", func(t *testing.T) {
		state, plan, bundle, _ := slCovParkFixture(t)
		if err := os.Remove(filepath.Join(plan.StoreRoot, bundle.ParkedSessionID, sessionpark.BundleFileName)); err != nil {
			t.Fatal(err)
		}
		if _, err := validatePrivateParkPlan(state, plan); err == nil {
			t.Fatal("accepted a missing bundle")
		}
	})
	t.Run("bundle digest mismatch", func(t *testing.T) {
		state, plan, _, _ := slCovParkFixture(t)
		plan.RequestDigest = slCovDigest("other")
		if _, err := validatePrivateParkPlan(state, plan); err == nil {
			t.Fatal("accepted a divergent bundle digest")
		}
	})
	t.Run("missing successor context", func(t *testing.T) {
		state, plan, bundle, _ := slCovParkFixture(t)
		if err := os.Remove(filepath.Join(plan.StoreRoot, bundle.ParkedSessionID, sessionpark.SuccessorContextFileName)); err != nil {
			t.Fatal(err)
		}
		if _, err := validatePrivateParkPlan(state, plan); err == nil {
			t.Fatal("accepted a missing successor context")
		}
	})
	t.Run("non canonical bundle", func(t *testing.T) {
		state, plan, bundle, _ := slCovParkFixture(t)
		slCovWrite(t, filepath.Join(plan.StoreRoot, bundle.ParkedSessionID, sessionpark.BundleFileName), 0o600, "{}\n")
		plan.RequestDigest = sessionmove.DigestBytes([]byte("{}\n"))
		if _, err := validatePrivateParkPlan(state, plan); err == nil {
			t.Fatal("accepted a non-canonical bundle")
		}
	})
	t.Run("neutral root shape", func(t *testing.T) {
		state, plan, _, _ := slCovParkFixture(t)
		neutral := filepath.Join(plan.StoreRoot, plan.HandoffID, sessionpark.LocalNeutralDirName)
		if err := os.Chmod(neutral, 0o755); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = os.Chmod(neutral, 0o700) }()
		if _, err := validatePrivateParkPlan(state, plan); err == nil {
			t.Fatal("accepted a public neutral root")
		}
	})
	t.Run("field divergence", func(t *testing.T) {
		mutations := map[string]func(*launchPlan){
			"handoff":           func(plan *launchPlan) { plan.HandoffID = "other" },
			"successor":         func(plan *launchPlan) { plan.SuccessorWBSessionID = "other" },
			"predecessor":       func(plan *launchPlan) { plan.PredecessorWBSessionID = "other" },
			"machine":           func(plan *launchPlan) { plan.Machine = "other" },
			"tmux name":         func(plan *launchPlan) { plan.TmuxName = "other" },
			"pinned commit":     func(plan *launchPlan) { plan.PinnedCommit = strings.Repeat("c", 40) },
			"pinned branch":     func(plan *launchPlan) { plan.PinnedBranch = "other" },
			"root mode":         func(plan *launchPlan) { plan.RootMode = string(sessionauthority.LaunchRootPinnedClean) },
			"handover path":     func(plan *launchPlan) { plan.HandoverPath = "other.md" },
			"continuation dig":  func(plan *launchPlan) { plan.ContinuationDigest = slCovDigest("other") },
			"store root":        func(plan *launchPlan) { plan.StoreRoot = "" },
			"harness model":     func(plan *launchPlan) { plan.Model = "claude-opus" },
			"harness args":      func(plan *launchPlan) { plan.HarnessArgs = []string{"other"} },
			"relative wb path":  func(plan *launchPlan) { plan.WBExecutable = "relative/wb" },
			"relative harness":  func(plan *launchPlan) { plan.HarnessExecutable = "relative/codex" },
			"relative worktree": func(plan *launchPlan) { plan.WorktreeDir = "relative/worktree" },
			"invalid wb exec":   func(plan *launchPlan) { plan.WBExecutable = filepath.Join(t.TempDir(), "absent") },
			"invalid harness":   func(plan *launchPlan) { plan.HarnessExecutable = filepath.Join(t.TempDir(), "absent") },
			"source dir broken": func(plan *launchPlan) { plan.StoreRoot = filepath.Join(filepath.Dir(plan.StoreRoot), "elsewhere") },
			"continuation argv": func(plan *launchPlan) { plan.HarnessArgs = append(plan.HarnessArgs, plan.HandoverPath) },
			"schema":            func(plan *launchPlan) { plan.SchemaVersion++ },
		}
		for name, mutate := range mutations {
			t.Run(name, func(t *testing.T) {
				state, plan, _, _ := slCovParkFixture(t)
				mutate(&plan)
				if _, err := validatePrivateParkPlan(state, plan); err == nil {
					t.Fatalf("divergence %q was accepted", name)
				}
			})
		}
	})
	t.Run("wrong cwd", func(t *testing.T) {
		state, plan, _, _ := slCovParkFixture(t)
		previous, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chdir(t.TempDir()); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = os.Chdir(previous) }()
		if _, err := validatePrivateParkPlan(state, plan); err == nil || !strings.Contains(err.Error(), "not rooted in the pinned target worktree") {
			t.Fatalf("wrong cwd = %v", err)
		}
	})
}

// slCovParkEnvelopeFixture materializes one admitted remote park aggregate.
func slCovParkEnvelopeFixture(t *testing.T, continuation string, requestContinuation string) (*launchState, launchPlan, string) {
	t.Helper()
	storeRoot := filepath.Join(t.TempDir(), sessionpark.TargetDirName)
	resumeID := "park-resume-1"
	aggregate := filepath.Join(storeRoot, resumeID)
	worktree := filepath.Join(t.TempDir(), "worktree")
	if err := os.MkdirAll(aggregate, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	request := sessionpark.RemoteRequest{
		SchemaVersion: sessionpark.RequestSchemaVersion, ResumeID: resumeID, ParkedSessionID: "park-abc123",
		SuccessorWBSessionID: "wbs-successor", PredecessorWBSessionID: "wbs-source",
		SourceMachine: "source-mac", TargetMachine: "hetzner-vm1",
		SourceRuntime: RuntimeCodex, SourceModel: "gpt-5",
		Continuation: requestContinuation,
		Members: []sessionpark.RemoteMember{{
			MemberID: "app", Repository: "acme/app", RepositoryRemote: "https://github.com/acme/app.git",
			Branch: "main", Commit: strings.Repeat("a", 40),
			SourceWorkLogReference: "worklog:session-move/session-move-run/" + strings.Repeat("b", 64),
		}},
		CreatedAt: slCovParkNow,
	}
	raw, err := sessionpark.EncodeEnvelope(sessionpark.Envelope{
		SchemaVersion: sessionpark.EnvelopeSchemaVersion, Kind: sessionpark.EnvelopeKind, Request: request,
	})
	if err != nil {
		t.Fatal(err)
	}
	slCovWrite(t, filepath.Join(aggregate, sessionpark.EnvelopeFileName), 0o600, string(raw))
	slCovWrite(t, filepath.Join(aggregate, sessionpark.SuccessorContextFileName), 0o600, continuation)
	digest := sessionmove.DigestBytes(raw)
	continuationPath := filepath.Join(storeRoot, resumeID, sessionpark.SuccessorContextFileName)
	authority, err := sessionpark.LaunchAuthority(request, digest, continuationPath, []byte(continuation))
	if err != nil {
		t.Fatal(err)
	}
	spec, err := harnessSpecForAuthority(authority, worktree)
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	plan := launchPlan{
		SchemaVersion: launchSchemaVersion, HandoffID: authority.AggregateID,
		RequestDigest:        digest,
		SuccessorWBSessionID: authority.SuccessorWBSessionID, PredecessorWBSessionID: authority.PredecessorWBSessionID,
		Machine: authority.TargetMachine, TmuxName: "wb-session-" + authority.SuccessorWBSessionID,
		Runtime: spec.Runtime, Model: spec.Model, StoreRoot: storeRoot, WorktreeDir: worktree,
		PinnedCommit: authority.PinnedCommit, PinnedBranch: authority.PinnedBranch,
		RootMode:     string(authority.RootMode),
		HandoverPath: authority.ContinuationPath, AuthorityFile: sessionpark.EnvelopeFileName,
		ContinuationKind: string(authority.ContinuationKind), ContinuationDigest: sessionmove.Digest(authority.ContinuationDigest),
		WBExecutable: slCovExecutable(t, binDir, "wb"), HarnessExecutable: slCovExecutable(t, binDir, spec.Executable),
		HarnessArgs: spec.Args,
	}
	state, err := openLaunchState(storeRoot, resumeID, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(worktree); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	return state, plan, continuationPath
}

func TestSlCovValidatePrivateParkPlanRemoteEnvelope(t *testing.T) {
	t.Run("exact envelope", func(t *testing.T) {
		state, plan, continuationPath := slCovParkEnvelopeFixture(t, "envelope continuation\n", "envelope continuation\n")
		got, err := validatePrivateParkPlan(state, plan)
		if err != nil || got != continuationPath {
			t.Fatalf("validatePrivateParkPlan = %q %v", got, err)
		}
	})
	t.Run("successor context does not extend the request continuation", func(t *testing.T) {
		state, plan, _ := slCovParkEnvelopeFixture(t, "envelope continuation\n", "different prefix\n")
		if _, err := validatePrivateParkPlan(state, plan); err == nil || !strings.Contains(err.Error(), "conflicts with admitted envelope") {
			t.Fatalf("continuation prefix conflict = %v", err)
		}
	})
	t.Run("non canonical envelope", func(t *testing.T) {
		state, plan, _ := slCovParkEnvelopeFixture(t, "envelope continuation\n", "envelope continuation\n")
		raw := "{}\n"
		slCovWrite(t, filepath.Join(plan.StoreRoot, plan.HandoffID, sessionpark.EnvelopeFileName), 0o600, raw)
		plan.RequestDigest = sessionmove.DigestBytes([]byte(raw))
		if _, err := validatePrivateParkPlan(state, plan); err == nil {
			t.Fatal("accepted a non-canonical envelope")
		}
	})
	t.Run("missing envelope", func(t *testing.T) {
		state, plan, _ := slCovParkEnvelopeFixture(t, "envelope continuation\n", "envelope continuation\n")
		if err := os.Remove(filepath.Join(plan.StoreRoot, plan.HandoffID, sessionpark.EnvelopeFileName)); err != nil {
			t.Fatal(err)
		}
		if _, err := validatePrivateParkPlan(state, plan); err == nil {
			t.Fatal("accepted a missing envelope")
		}
	})
}

func TestSlCovVerifyPrivateLocalRootLocalMembers(t *testing.T) {
	_, gitErr := exec.LookPath("git")
	storeRoot := filepath.Join(t.TempDir(), sessionpark.SourceDirName)
	bundle := slCovLocalBundle()
	bundle.ParkedSessionID = "park-local1"
	branch := "wb-session/" + bundle.ParkedSessionID
	var repo, head string
	if gitErr == nil {
		repo, head = slCovRepo(t, branch)
	} else {
		repo = filepath.Join(t.TempDir(), "worktree")
		if err := os.MkdirAll(repo, 0o755); err != nil {
			t.Fatal(err)
		}
		head = strings.Repeat("a", 40)
	}
	bundle.Worktrees = []sessionpark.Worktree{{Repository: "acme/app", WorktreeDir: repo, Branch: branch, Head: head}}
	aggregate := filepath.Join(storeRoot, bundle.ParkedSessionID)
	if err := os.MkdirAll(aggregate, 0o700); err != nil {
		t.Fatal(err)
	}
	continuation := bundle.Continuation + "local successor context\n"
	slCovWrite(t, filepath.Join(aggregate, sessionpark.SuccessorContextFileName), 0o600, continuation)
	raw, err := sessionpark.EncodeBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	slCovWrite(t, filepath.Join(aggregate, sessionpark.BundleFileName), 0o600, string(raw))
	digest := sessionmove.DigestBytes(raw)
	continuationPath := filepath.Join(storeRoot, bundle.ParkedSessionID, sessionpark.SuccessorContextFileName)
	authority, err := sessionpark.LocalLaunchAuthority(bundle, digest, continuationPath, []byte(continuation))
	if err != nil {
		t.Fatal(err)
	}
	if authority.RootMode != sessionauthority.LaunchRootParkedLocal {
		t.Fatalf("authority root mode = %q", authority.RootMode)
	}
	spec, err := harnessSpecForAuthority(authority, repo)
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	plan := launchPlan{
		SchemaVersion: launchSchemaVersion, HandoffID: authority.AggregateID, RequestDigest: digest,
		SuccessorWBSessionID: authority.SuccessorWBSessionID, PredecessorWBSessionID: authority.PredecessorWBSessionID,
		Machine: authority.TargetMachine, TmuxName: "wb-session-" + authority.SuccessorWBSessionID,
		Runtime: spec.Runtime, Model: spec.Model, StoreRoot: storeRoot, WorktreeDir: repo,
		PinnedCommit: authority.PinnedCommit, PinnedBranch: authority.PinnedBranch, RootMode: string(authority.RootMode),
		HandoverPath: authority.ContinuationPath, AuthorityFile: sessionpark.BundleFileName,
		ContinuationKind: string(authority.ContinuationKind), ContinuationDigest: sessionmove.Digest(authority.ContinuationDigest),
		WBExecutable: slCovExecutable(t, binDir, "wb"), HarnessExecutable: slCovExecutable(t, binDir, spec.Executable),
		HarnessArgs: spec.Args,
	}
	state, err := openLaunchState(storeRoot, bundle.ParkedSessionID, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	if gitErr != nil {
		t.Setenv("PATH", t.TempDir())
		if _, err := validatePrivateParkPlan(state, plan); err == nil || !strings.Contains(err.Error(), "git executable is unavailable") {
			t.Fatalf("parked-local plan without git = %v", err)
		}
		return
	}
	if _, err := validatePrivateParkPlan(state, plan); err != nil {
		t.Fatalf("exact parked-local plan = %v", err)
	}
	t.Run("member HEAD changed", func(t *testing.T) {
		t.Parallel()
		slCovWrite(t, filepath.Join(repo, "extra.txt"), 0o644, "extra\n")
		slCovGit(t, repo, "add", "extra.txt")
		slCovGit(t, repo, "commit", "-q", "-m", "extra")
		if _, err := validatePrivateParkPlan(state, plan); err == nil {
			t.Fatal("accepted a changed parked-local member HEAD")
		}
	})
}
