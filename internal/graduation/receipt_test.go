package graduation

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestComposeBindsClosedProducerEvidenceToOneCleanRevision(t *testing.T) {
	inputs, now := validInputs()
	receipt, err := Compose(inputs, now)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Repository != "sneat-dev/wb" || receipt.Revision != strings.Repeat("a", 40) ||
		receipt.LocalCI.LocalCheck.SHA256 != inputs.LocalCheckSHA256 ||
		receipt.RemoteTarget.Evidence.TargetRef != "refs/heads/main" ||
		!receipt.TerminalCleanup.Evidence.Results[0].WorktreeGone || !receipt.CreatedAt.Equal(now) {
		t.Fatalf("receipt=%#v", receipt)
	}
}

func TestComposeAcceptsRemoteBranchAlreadyAbsentAtCleanup(t *testing.T) {
	inputs, now := validInputs()
	result := &inputs.TerminalCleanup.Results[0]
	result.RemoteHeadSHA = ""
	result.RemoteDeleted = false
	if _, err := Compose(inputs, now); err != nil {
		t.Fatalf("already-absent source branch is terminal: %v", err)
	}
}

func TestComposeRefusesDivergentIncompleteOrSelfAttestedEvidence(t *testing.T) {
	tests := map[string]struct {
		change func(*Inputs, time.Time) time.Time
		want   string
	}{
		"dirty local workspace": {
			change: func(inputs *Inputs, now time.Time) time.Time {
				inputs.LocalCheck.Repositories[0].WorkspaceClean = false
				return now
			},
			want: "exact clean Git revision",
		},
		"local revision differs": {
			change: func(inputs *Inputs, now time.Time) time.Time {
				inputs.LocalCheck.Repositories[0].Revision = strings.Repeat("b", 40)
				return now
			},
			want: "conflicts with CI head",
		},
		"failed local mechanism": {
			change: func(inputs *Inputs, now time.Time) time.Time {
				inputs.LocalCheck.Repositories[0].Results[1].Status = quality.StatusFailed
				return now
			},
			want: "failed",
		},
		"CI has no observed checks": {
			change: func(inputs *Inputs, now time.Time) time.Time { inputs.CIWait.Checks = nil; return now },
			want:   "exact observed head",
		},
		"PR CI is not final target CI": {
			change: func(inputs *Inputs, now time.Time) time.Time { inputs.CIWait.PullRequest = "42"; return now },
			want:   "exact observed head",
		},
		"CI target head differs": {
			change: func(inputs *Inputs, now time.Time) time.Time {
				inputs.CIWait.ObservedTargetHead = strings.Repeat("b", 40)
				return now
			},
			want: "exact observed head",
		},
		"CI did not bind candidate to target": {
			change: func(inputs *Inputs, now time.Time) time.Time {
				inputs.CIWait.CandidateContainsTarget = false
				return now
			},
			want: "exact observed head",
		},
		"failed CI bucket contradicts pass": {
			change: func(inputs *Inputs, now time.Time) time.Time { inputs.CIWait.Checks[0].Bucket = "fail"; return now },
			want:   "failed check",
		},
		"remote target differs from CI target": {
			change: func(inputs *Inputs, now time.Time) time.Time {
				inputs.RemoteTarget.TargetRef = "refs/heads/release"
				inputs.RemoteTarget.ObservedOutput = inputs.RemoteTarget.Revision + "\trefs/heads/release\n"
				inputs.RemoteTarget.ObservedOutputSHA256 = Digest([]byte(inputs.RemoteTarget.ObservedOutput))
				return now
			},
			want: "fixed-shape",
		},
		"remote URL uses helper scheme": {
			change: func(inputs *Inputs, now time.Time) time.Time {
				inputs.RemoteTarget.RemoteURL = "file://github.com/sneat-dev/wb"
				return now
			},
			want: "canonical GitHub HTTPS or SSH",
		},
		"deployment payload does not carry revision": {
			change: func(inputs *Inputs, now time.Time) time.Time {
				inputs.DeployedRevision.RevisionJSONPointer = "/deployment/missing"
				return now
			},
			want: "revision pointer",
		},
		"deployment repository differs": {
			change: func(inputs *Inputs, now time.Time) time.Time {
				inputs.DeployedRevision.Repository = "sneat-dev/other"
				return now
			},
			want: "closed GitHub Actions deployment payload",
		},
		"deployment digest conflicts": {
			change: func(inputs *Inputs, now time.Time) time.Time {
				inputs.DeployedRevision.PayloadSHA256 = Digest([]byte("different"))
				return now
			},
			want: "closed GitHub Actions deployment payload",
		},
		"deployment URL contains mutable query": {
			change: func(inputs *Inputs, now time.Time) time.Time {
				inputs.DeployedRevision.RunURL += "?token=secret"
				return now
			},
			want: "canonical immutable GitHub Actions run URL",
		},
		"deployment payload run ID differs": {
			change: func(inputs *Inputs, now time.Time) time.Time {
				inputs.DeployedRevision.PayloadJSON = strings.Replace(inputs.DeployedRevision.PayloadJSON, `"run_id":42`, `"run_id":43`, 1)
				inputs.DeployedRevision.PayloadSHA256 = Digest([]byte(inputs.DeployedRevision.PayloadJSON))
				return now
			},
			want: "run ID pointer",
		},
		"deployment pointer has invalid escape": {
			change: func(inputs *Inputs, now time.Time) time.Time {
				inputs.DeployedRevision.RevisionJSONPointer = "/deployment~2revision"
				return now
			},
			want: "revision pointer",
		},
		"canonical target checkout deletion claimed": {
			change: func(inputs *Inputs, now time.Time) time.Time {
				inputs.TerminalCleanup.Results[0].WorktreeDir = inputs.TerminalCleanup.Results[0].CanonicalDir
				return now
			},
			want: "terminal WB cleanup",
		},
		"cleanup does not report all merged": {
			change: func(inputs *Inputs, now time.Time) time.Time {
				inputs.TerminalCleanup.AllMerged = false
				return now
			},
			want: "applied wb worktree cleanup",
		},
		"cleanup deletes final target branch": {
			change: func(inputs *Inputs, now time.Time) time.Time {
				inputs.TerminalCleanup.Results[0].Branch = "main"
				return now
			},
			want: "terminal WB cleanup",
		},
		"remote source branch remains": {
			change: func(inputs *Inputs, now time.Time) time.Time {
				inputs.TerminalCleanup.Results[0].RemoteDeleted = false
				return now
			},
			want: "terminal WB cleanup",
		},
		"cleanup target differs": {
			change: func(inputs *Inputs, now time.Time) time.Time {
				inputs.TerminalCleanup.Results[0].RemoteTargetSHA = strings.Repeat("b", 40)
				return now
			},
			want: "exact remote target revision",
		},
		"malformed source digest": {
			change: func(inputs *Inputs, now time.Time) time.Time {
				inputs.RemoteTargetSHA256 = "sha256:not-a-digest"
				return now
			},
			want: "source digest",
		},
		"future producer observation": {
			change: func(inputs *Inputs, now time.Time) time.Time {
				return inputs.TerminalCleanup.GeneratedAt.Add(-time.Second)
			},
			want: "future",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			inputs, now := validInputs()
			now = test.change(&inputs, now)
			if _, err := Compose(inputs, now); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Compose error=%v, want %q", err, test.want)
			}
		})
	}
}

func TestDecodeEvidenceRejectsUnknownFieldsAndProse(t *testing.T) {
	if _, err := DecodeVerificationIndex([]byte("this is not machine-readable evidence")); err == nil {
		t.Fatal("prose decoded as local-check evidence")
	}
	inputs, _ := validInputs()
	raw, err := json.Marshal(inputs.LocalCheck)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw[:len(raw)-1], []byte(`,"status":"passed"}`)...)
	if _, err := DecodeVerificationIndex(raw); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("hand-authored status field was accepted: %v", err)
	}
}

func validInputs() (Inputs, time.Time) {
	const repository = "sneat-dev/wb"
	revision := strings.Repeat("a", 40)
	localAt := time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC)
	ciAt := localAt.Add(time.Minute)
	remoteAt := ciAt.Add(time.Minute)
	deployedAt := remoteAt.Add(time.Minute)
	cleanupAt := deployedAt.Add(time.Minute)
	payload := `{"deployment":{"revision":"` + revision + `","run_id":42}}`
	localCheck := VerificationIndex{SchemaVersion: SchemaVersion, GeneratedAt: localAt, Profile: "ci", Checks: []quality.Check{quality.CheckLint, quality.CheckTest, quality.CheckBuild}, Repositories: []quality.VerificationReport{{
		Repository: repository, Path: "/projects/sneat-dev/wb", Revision: revision, WorkspaceClean: true, Status: quality.StatusPassed,
		Results: []quality.VerificationEntry{
			{Check: quality.CheckLint, Command: "go vet ./...", Status: quality.StatusPassed},
			{Check: quality.CheckTest, Command: "go test ./...", Status: quality.StatusPassed},
			{Check: quality.CheckBuild, Command: "go build ./...", Status: quality.StatusPassed},
		},
	}}}
	ciWait := CIWaitReceipt{SchemaVersion: SchemaVersion, ObservedAt: ciAt, PullRequestWaitResult: orchestrate.PullRequestWaitResult{
		Status: orchestrate.PullRequestWaitPassed, Repository: repository, Target: "main", Head: revision, ObservedHead: revision, ObservedTargetHead: revision, CandidateContainsTarget: true,
		Checks:                  []orchestrate.RemoteCheck{{Name: "test", Bucket: "pass", Link: "https://github.test/runs/42"}},
		RequiredChecksAuthority: "github-rulesets", StableObservations: 2,
	}}
	remoteOutput := revision + "\trefs/heads/main\n"
	remoteTarget := RemoteTargetEvidence{SchemaVersion: SchemaVersion, Producer: RemoteTargetProducer, Repository: repository, Remote: "origin", RemoteURL: "git@github.com:sneat-dev/wb.git", TargetRef: "refs/heads/main", Revision: revision, ObservedAt: remoteAt, ObservedOutput: remoteOutput, ObservedOutputSHA256: Digest([]byte(remoteOutput))}
	deployed := DeployedRevisionEvidence{SchemaVersion: SchemaVersion, Producer: DeploymentProducer, Provider: "github-actions", Repository: repository, RunURL: "https://github.com/sneat-dev/wb/actions/runs/42", ProviderRunID: "42", RunIDJSONPointer: "/deployment/run_id", Revision: revision, RevisionJSONPointer: "/deployment/revision", ObservedAt: deployedAt, PayloadJSON: payload, PayloadSHA256: Digest([]byte(payload))}
	cleanup := TerminalCleanupEvidence{GeneratedAt: cleanupAt, Phase: "applied", Task: "graduation", AllMerged: true, Apply: true, DeleteRemote: true, OlderThan: "24h0m0s", Results: []worktrees.CleanupResult{{
		ListResult: worktrees.ListResult{Task: "graduation", Repository: repository, CanonicalDir: "/projects/sneat-dev/wb", WorktreeDir: "/worktrees/graduation/sneat-dev/wb", Branch: "feature/graduation", Base: "main", HeadSHA: revision, RemoteHeadSHA: revision, RemoteTargetSHA: revision, IntegratedAtOrigin: true, Clean: true},
		Eligible:   true, Applied: true, RemoteDeleted: true, WorktreeGone: true, BranchDeleted: true,
	}}}
	inputs := Inputs{
		LocalCheck: localCheck, LocalCheckSHA256: Digest([]byte("local")), LocalCheckObservedAt: localAt,
		CIWait: ciWait, CIWaitSHA256: Digest([]byte("ci")), CIWaitObservedAt: ciAt,
		RemoteTarget: remoteTarget, RemoteTargetSHA256: Digest([]byte("remote")), RemoteTargetObservedAt: remoteAt,
		DeployedRevision: deployed, DeployedSHA256: Digest([]byte("deployed")), DeployedObservedAt: deployedAt,
		TerminalCleanup: cleanup, CleanupSHA256: Digest([]byte("cleanup")), CleanupObservedAt: cleanupAt,
	}
	return inputs, cleanupAt.Add(time.Minute)
}
