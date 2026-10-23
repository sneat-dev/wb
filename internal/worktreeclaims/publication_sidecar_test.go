package worktreeclaims

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/worktreelayout"
)

type sidecarPublicationFixture struct {
	home, runPath string
	run           *os.File
	first, next   Claim
	ports         PublicationPorts
	bindingPath   string
}

func newSidecarPublicationFixture(t *testing.T) sidecarPublicationFixture {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	history := terminalHistoryTestPorts()
	run, runPath, err := history.OpenRun(home, "task", "run", true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = run.Close() })
	claim := Claim{Version: 1, EffortID: "task", RunID: "run", Task: "task", Repository: "acme/app", Worktree: filepath.Join(home, "first"), Branch: "first", Base: "main", BaseSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Lifecycle: "active", RecordedAt: time.Unix(100, 0).UTC()}
	claim.ClaimID = WorkLogClaimID(claim.EffortID, CreationResult{Repository: claim.Repository, WorktreeDir: claim.Worktree, Branch: claim.Branch, Base: claim.Base, BaseSHA: claim.BaseSHA})
	next := claim
	next.Worktree, next.Branch = filepath.Join(home, "second"), "second"
	next.ClaimID = WorkLogClaimID(next.EffortID, CreationResult{Repository: next.Repository, WorktreeDir: next.Worktree, Branch: next.Branch, Base: next.Base, BaseSHA: next.BaseSHA})
	ports := PublicationPorts{
		OpenPrivateChild: history.OpenChild,
		ReadJSONAt:       filewrite.ReadJSONAt,
		ReadClaimAt: func(run *os.File, id string) (Claim, error) {
			return ReadWorkLogClaimAt[Claim](run, id, worktreelayout.ValidSafeSegment)
		},
		ReadClaimNames:      func(claims *os.File) ([]string, error) { return claims.Readdirnames(-1) },
		CorroborateExisting: func(Claim) error { return nil },
		WriteJSONImmutableAt: func(dir *os.File, name string, value any, idempotent bool) error {
			return filewrite.WriteJSONImmutableAt(dir, name, value, idempotent, nil)
		},
		EnsureRunIndex:       func(*os.File, string, string) error { return nil },
		WriteProjection:      func(string, Projection) error { return nil },
		WriteCreationJournal: func(Claim) error { return nil },
		OpenOutbox:           history.OpenOutbox,
	}
	if _, err = ports.PublishClaim(home, run, runPath, claim, PublicationHooks{}); err != nil {
		t.Fatal(err)
	}
	bindingPorts := BindingPorts{Root: func(string) (string, error) { return home, nil }, Active: func(string, string) (ActiveClaim, error) {
		return ActiveClaim{Task: claim.Task, ClaimID: claim.ClaimID, Path: filepath.Join(runPath, "claims", claim.ClaimID+".json")}, nil
	}}
	if _, _, err = bindingPorts.RecordClaimPullRequestBinding(home, claim.Worktree, ClaimPullRequestBinding{Repository: claim.Repository, PullRequest: 42, URL: "https://github.com/acme/app/pull/42", RecordedAt: time.Unix(101, 0).UTC()}); err != nil {
		t.Fatal(err)
	}
	return sidecarPublicationFixture{home: home, runPath: runPath, run: run, first: claim, next: next, ports: ports, bindingPath: filepath.Join(runPath, "claims", claim.ClaimID+PullRequestBindingSuffix)}
}

func TestPublishClaimAcceptsRegisteredPullRequestSidecarAndResumes(t *testing.T) {
	t.Parallel()
	f := newSidecarPublicationFixture(t)
	before, err := os.ReadFile(f.bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	interrupted := errors.New("synthetic after-claim interruption")
	receipt, err := f.ports.PublishClaim(f.home, f.run, f.runPath, f.next, PublicationHooks{AfterClaim: func() error { return interrupted }})
	if !errors.Is(err, interrupted) || !receipt.ClaimWritten || receipt.ProjectionWritten || receipt.OutboxWritten {
		t.Fatalf("new distinct claim interrupted at wrong boundary: receipt=%+v err=%v", receipt, err)
	}
	for range 2 {
		receipt, err = f.ports.PublishClaim(f.home, f.run, f.runPath, f.next, PublicationHooks{})
		if err != nil || !receipt.ClaimWritten || !receipt.ProjectionWritten || !receipt.OutboxWritten || receipt.Claim.ClaimID != f.next.ClaimID {
			t.Fatalf("publication/retry: receipt=%+v err=%v", receipt, err)
		}
	}
	after, err := os.ReadFile(f.bindingPath)
	if err != nil || string(after) != string(before) {
		t.Fatalf("registered binding changed: %v", err)
	}
	existing, err := f.ports.ReadClaimAt(f.run, f.first.ClaimID)
	if err != nil || existing != f.first {
		t.Fatalf("existing claim changed: %+v %v", existing, err)
	}
}

func TestPublishClaimRefusesUnsafePullRequestSidecarsBeforeWriting(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		mutate func(*testing.T, *sidecarPublicationFixture)
	}{
		{"invalid binding id", func(t *testing.T, f *sidecarPublicationFixture) {
			mustSidecarWrite(t, filepath.Join(filepath.Dir(f.bindingPath), "invalid"+PullRequestBindingSuffix), []byte(`{}`))
		}},
		{"orphan binding", func(t *testing.T, f *sidecarPublicationFixture) {
			if err := os.Remove(filepath.Join(filepath.Dir(f.bindingPath), f.first.ClaimID+".json")); err != nil {
				t.Fatal(err)
			}
		}},
		{"corrupt binding", func(t *testing.T, f *sidecarPublicationFixture) { mustSidecarWrite(t, f.bindingPath, []byte(`{`)) }},
		{"corrupt sibling", func(t *testing.T, f *sidecarPublicationFixture) {
			mustSidecarWrite(t, filepath.Join(filepath.Dir(f.bindingPath), f.first.ClaimID+".json"), []byte(`{`))
		}},
		{"mismatched sibling identity", func(t *testing.T, f *sidecarPublicationFixture) {
			claim := f.first
			claim.ClaimID = f.next.ClaimID
			data, err := json.Marshal(claim)
			if err != nil {
				t.Fatal(err)
			}
			mustSidecarWrite(t, filepath.Join(filepath.Dir(f.bindingPath), f.first.ClaimID+".json"), data)
		}},
		{"missing descriptor reader", func(_ *testing.T, f *sidecarPublicationFixture) { f.ports.ReadJSONAt = nil }},
		{"unknown visible entry", func(t *testing.T, f *sidecarPublicationFixture) {
			mustSidecarWrite(t, filepath.Join(filepath.Dir(f.bindingPath), "unknown.txt"), []byte("unknown"))
		}},
		{"same worktree conflicting identity", func(_ *testing.T, f *sidecarPublicationFixture) { f.next.Worktree = f.first.Worktree }},
	}
	for _, field := range []string{"repository", "blank repository", "pull request", "url", "recorded at"} {
		cases = append(cases, struct {
			name   string
			mutate func(*testing.T, *sidecarPublicationFixture)
		}{field, func(t *testing.T, f *sidecarPublicationFixture) {
			binding := ClaimPullRequestBinding{Repository: f.first.Repository, PullRequest: 42, URL: "https://github.com/acme/app/pull/42", RecordedAt: time.Unix(101, 0).UTC()}
			switch field {
			case "repository":
				binding.Repository = "other/repository"
			case "blank repository":
				binding.Repository = " "
			case "pull request":
				binding.PullRequest = 0
			case "url":
				binding.URL = " "
			case "recorded at":
				binding.RecordedAt = time.Time{}
			}
			data, err := json.Marshal(binding)
			if err != nil {
				t.Fatal(err)
			}
			mustSidecarWrite(t, f.bindingPath, data)
		}})
	}
	for _, sibling := range []bool{false, true} {
		name := "binding symlink"
		if sibling {
			name = "sibling symlink"
		}
		cases = append(cases, struct {
			name   string
			mutate func(*testing.T, *sidecarPublicationFixture)
		}{name, func(t *testing.T, f *sidecarPublicationFixture) {
			path := f.bindingPath
			if sibling {
				path = filepath.Join(filepath.Dir(path), f.first.ClaimID+".json")
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(f.home, "outside.json")
			mustSidecarWrite(t, target, data)
			if err = os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err = os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newSidecarPublicationFixture(t)
			tc.mutate(t, &f)
			receipt, err := f.ports.PublishClaim(f.home, f.run, f.runPath, f.next, PublicationHooks{})
			if err == nil || receipt.ClaimWritten || receipt.ProjectionWritten || receipt.OutboxWritten {
				t.Fatalf("unsafe metadata accepted: receipt=%+v err=%v", receipt, err)
			}
			if _, err = os.Stat(filepath.Join(f.runPath, "claims", f.next.ClaimID+".json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("new authority exists: %v", err)
			}
		})
	}
}

func mustSidecarWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPublishClaimPreservesDescriptorReadErrors(t *testing.T) {
	t.Parallel()
	for _, suffix := range []string{PullRequestBindingSuffix, ".json"} {
		t.Run(suffix, func(t *testing.T) {
			t.Parallel()
			f := newSidecarPublicationFixture(t)
			sentinel := errors.New("synthetic descriptor read failure")
			f.ports.ReadJSONAt = func(dir *os.File, name string, value any) error {
				if name == f.first.ClaimID+suffix {
					return sentinel
				}
				return filewrite.ReadJSONAt(dir, name, value)
			}
			receipt, err := f.ports.PublishClaim(f.home, f.run, f.runPath, f.next, PublicationHooks{})
			if !errors.Is(err, sentinel) || receipt.ClaimWritten {
				t.Fatalf("descriptor error lost: %+v %v", receipt, err)
			}
		})
	}
}

func TestPublishClaimIgnoresAtomicScratchBesideRegisteredSidecar(t *testing.T) {
	t.Parallel()
	f := newSidecarPublicationFixture(t)
	scratch := filepath.Join(f.runPath, "claims", ".atomic-incomplete")
	mustSidecarWrite(t, scratch, []byte("incomplete"))
	receipt, err := f.ports.PublishClaim(f.home, f.run, f.runPath, f.next, PublicationHooks{})
	if err != nil || !receipt.OutboxWritten {
		t.Fatalf("scratch prevented publication: %+v %v", receipt, err)
	}
	if data, err := os.ReadFile(scratch); err != nil || string(data) != "incomplete" {
		t.Fatalf("scratch changed: %q %v", data, err)
	}
}
