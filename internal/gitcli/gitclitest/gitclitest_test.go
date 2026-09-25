package gitclitest

import (
	"context"
	"errors"
	"testing"
)

func TestFakeCurrentBranchReturnsScriptedResult(t *testing.T) {
	t.Parallel()
	fake := &Fake{CurrentBranchByDir: map[string]Result{"/repo": {Value: "main"}}}
	branch, err := fake.CurrentBranch(context.Background(), "/repo")
	if err != nil || branch != "main" {
		t.Fatalf("CurrentBranch() = (%q, %v)", branch, err)
	}
}

func TestFakeCurrentBranchPanicsWhenUnscripted(t *testing.T) {
	t.Parallel()
	t.Cleanup(func() {
		if recover() == nil {
			t.Fatal("want a panic for an unscripted dir")
		}
	})
	(&Fake{}).CurrentBranch(context.Background(), "/repo") //nolint:errcheck
}

func TestFakeRevParseReturnsScriptedResult(t *testing.T) {
	t.Parallel()
	fake := &Fake{RevParseByDirAndRev: map[string]Result{key("/repo", "HEAD"): {Value: "abc123"}}}
	sha, err := fake.RevParse(context.Background(), "/repo", "HEAD")
	if err != nil || sha != "abc123" {
		t.Fatalf("RevParse() = (%q, %v)", sha, err)
	}
}

func TestFakeRevParsePanicsWhenUnscripted(t *testing.T) {
	t.Parallel()
	t.Cleanup(func() {
		if recover() == nil {
			t.Fatal("want a panic for an unscripted rev")
		}
	})
	(&Fake{}).RevParse(context.Background(), "/repo", "HEAD") //nolint:errcheck
}

func TestFakeIsAncestorReturnsScriptedResult(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("boom")
	fake := &Fake{IsAncestorByCase: map[string]BoolResult{key("/repo", "base", "head"): {Value: true, Err: wantErr}}}
	ok, err := fake.IsAncestor(context.Background(), "/repo", "base", "head")
	if !ok || err != wantErr {
		t.Fatalf("IsAncestor() = (%v, %v)", ok, err)
	}
}

func TestFakeIsAncestorPanicsWhenUnscripted(t *testing.T) {
	t.Parallel()
	t.Cleanup(func() {
		if recover() == nil {
			t.Fatal("want a panic for an unscripted case")
		}
	})
	(&Fake{}).IsAncestor(context.Background(), "/repo", "base", "head") //nolint:errcheck
}

func TestFakeFetchReturnsScriptedError(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("network unreachable")
	fake := &Fake{FetchErrByDirAndRemote: map[string]error{key("/repo", "origin"): wantErr}}
	if err := fake.Fetch(context.Background(), "/repo", "origin"); err != wantErr {
		t.Fatalf("Fetch() = %v, want %v", err, wantErr)
	}
}

func TestFakeFetchPanicsWhenUnscripted(t *testing.T) {
	t.Parallel()
	t.Cleanup(func() {
		if recover() == nil {
			t.Fatal("want a panic for an unscripted remote")
		}
	})
	(&Fake{}).Fetch(context.Background(), "/repo", "origin") //nolint:errcheck
}

// The tests below cover the methods gitclitest.go adds for
// spec/plans/coverage-to-100 task-17 (internal/orchestrate's Git port): one
// scripted-success case and one unscripted-panic case per method, the same
// shape every method above already follows.

func TestFakeWorktreeRemoveForceReturnsScriptedError(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("boom")
	fake := &Fake{WorktreeRemoveForceErrByCase: map[string]error{key("/repo", "/scratch"): wantErr}}
	if err := fake.WorktreeRemoveForce(context.Background(), "/repo", "/scratch"); err != wantErr {
		t.Fatalf("WorktreeRemoveForce() = %v, want %v", err, wantErr)
	}
}

func TestFakeWorktreeRemoveForcePanicsWhenUnscripted(t *testing.T) {
	t.Parallel()
	t.Cleanup(func() {
		if recover() == nil {
			t.Fatal("want a panic for an unscripted case")
		}
	})
	(&Fake{}).WorktreeRemoveForce(context.Background(), "/repo", "/scratch") //nolint:errcheck
}

func TestFakeWorktreeAddDetachedReturnsScriptedError(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("boom")
	fake := &Fake{WorktreeAddDetachedErrByCase: map[string]error{key("/repo", "/scratch", "abc123"): wantErr}}
	if err := fake.WorktreeAddDetached(context.Background(), "/repo", "/scratch", "abc123"); err != wantErr {
		t.Fatalf("WorktreeAddDetached() = %v, want %v", err, wantErr)
	}
}

func TestFakeWorktreeAddDetachedPanicsWhenUnscripted(t *testing.T) {
	t.Parallel()
	t.Cleanup(func() {
		if recover() == nil {
			t.Fatal("want a panic for an unscripted case")
		}
	})
	(&Fake{}).WorktreeAddDetached(context.Background(), "/repo", "/scratch", "abc123") //nolint:errcheck
}

func TestFakeCherryPickNoCommitReturnsScriptedError(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("boom")
	fake := &Fake{CherryPickNoCommitErrByCase: map[string]error{key("/repo", "sha1,sha2"): wantErr}}
	if err := fake.CherryPickNoCommit(context.Background(), "/repo", "sha1", "sha2"); err != wantErr {
		t.Fatalf("CherryPickNoCommit() = %v, want %v", err, wantErr)
	}
}

func TestFakeCherryPickNoCommitPanicsWhenUnscripted(t *testing.T) {
	t.Parallel()
	t.Cleanup(func() {
		if recover() == nil {
			t.Fatal("want a panic for an unscripted case")
		}
	})
	(&Fake{}).CherryPickNoCommit(context.Background(), "/repo", "sha1") //nolint:errcheck
}

func TestFakeCommitNoVerifyReturnsScriptedError(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("boom")
	fake := &Fake{CommitNoVerifyErrByCase: map[string]error{key("/repo", "aggregate"): wantErr}}
	if err := fake.CommitNoVerify(context.Background(), "/repo", "aggregate"); err != wantErr {
		t.Fatalf("CommitNoVerify() = %v, want %v", err, wantErr)
	}
}

func TestFakeCommitNoVerifyPanicsWhenUnscripted(t *testing.T) {
	t.Parallel()
	t.Cleanup(func() {
		if recover() == nil {
			t.Fatal("want a panic for an unscripted case")
		}
	})
	(&Fake{}).CommitNoVerify(context.Background(), "/repo", "aggregate") //nolint:errcheck
}

func TestFakeCherryPickReturnsScriptedError(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("boom")
	fake := &Fake{CherryPickErrByCase: map[string]error{key("/repo", "sha1"): wantErr}}
	if err := fake.CherryPick(context.Background(), "/repo", "sha1"); err != wantErr {
		t.Fatalf("CherryPick() = %v, want %v", err, wantErr)
	}
}

func TestFakeCherryPickPanicsWhenUnscripted(t *testing.T) {
	t.Parallel()
	t.Cleanup(func() {
		if recover() == nil {
			t.Fatal("want a panic for an unscripted case")
		}
	})
	(&Fake{}).CherryPick(context.Background(), "/repo", "sha1") //nolint:errcheck
}

func TestFakePushForceWithLeaseHeadReturnsScriptedError(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("boom")
	fake := &Fake{PushForceWithLeaseHeadErrByCase: map[string]error{key("/repo", "feature", "sha1"): wantErr}}
	if err := fake.PushForceWithLeaseHead(context.Background(), "/repo", "feature", "sha1"); err != wantErr {
		t.Fatalf("PushForceWithLeaseHead() = %v, want %v", err, wantErr)
	}
}

func TestFakePushForceWithLeaseHeadPanicsWhenUnscripted(t *testing.T) {
	t.Parallel()
	t.Cleanup(func() {
		if recover() == nil {
			t.Fatal("want a panic for an unscripted case")
		}
	})
	(&Fake{}).PushForceWithLeaseHead(context.Background(), "/repo", "feature", "sha1") //nolint:errcheck
}

func TestFakeFetchRefsReturnsScriptedError(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("boom")
	fake := &Fake{FetchRefsErrByCase: map[string]error{key("/repo", "origin", "main,feature"): wantErr}}
	if err := fake.FetchRefs(context.Background(), "/repo", "origin", "main", "feature"); err != wantErr {
		t.Fatalf("FetchRefs() = %v, want %v", err, wantErr)
	}
}

func TestFakeFetchRefsPanicsWhenUnscripted(t *testing.T) {
	t.Parallel()
	t.Cleanup(func() {
		if recover() == nil {
			t.Fatal("want a panic for an unscripted case")
		}
	})
	(&Fake{}).FetchRefs(context.Background(), "/repo", "origin", "main") //nolint:errcheck
}

func TestFakeRevListReverseRangeReturnsScriptedResult(t *testing.T) {
	t.Parallel()
	fake := &Fake{RevListReverseRangeByCase: map[string]Result{key("/repo", "base", "head"): {Value: "sha1\nsha2"}}}
	out, err := fake.RevListReverseRange(context.Background(), "/repo", "base", "head")
	if err != nil || out != "sha1\nsha2" {
		t.Fatalf("RevListReverseRange() = (%q, %v)", out, err)
	}
}

func TestFakeRevListReverseRangePanicsWhenUnscripted(t *testing.T) {
	t.Parallel()
	t.Cleanup(func() {
		if recover() == nil {
			t.Fatal("want a panic for an unscripted case")
		}
	})
	(&Fake{}).RevListReverseRange(context.Background(), "/repo", "base", "head") //nolint:errcheck
}

func TestFakeRemotePushURLReturnsScriptedResult(t *testing.T) {
	t.Parallel()
	fake := &Fake{RemotePushURLByDirAndRemote: map[string]Result{key("/repo", "origin"): {Value: "git@example.test:o/r.git"}}}
	url, err := fake.RemotePushURL(context.Background(), "/repo", "origin")
	if err != nil || url != "git@example.test:o/r.git" {
		t.Fatalf("RemotePushURL() = (%q, %v)", url, err)
	}
}

func TestFakeRemotePushURLPanicsWhenUnscripted(t *testing.T) {
	t.Parallel()
	t.Cleanup(func() {
		if recover() == nil {
			t.Fatal("want a panic for an unscripted remote")
		}
	})
	(&Fake{}).RemotePushURL(context.Background(), "/repo", "origin") //nolint:errcheck
}

func TestFakeStatusPorcelainReturnsScriptedResult(t *testing.T) {
	t.Parallel()
	fake := &Fake{StatusPorcelainByDir: map[string]Result{"/repo": {Value: "M file.go"}}}
	status, err := fake.StatusPorcelain(context.Background(), "/repo")
	if err != nil || status != "M file.go" {
		t.Fatalf("StatusPorcelain() = (%q, %v)", status, err)
	}
}

func TestFakeStatusPorcelainPanicsWhenUnscripted(t *testing.T) {
	t.Parallel()
	t.Cleanup(func() {
		if recover() == nil {
			t.Fatal("want a panic for an unscripted dir")
		}
	})
	(&Fake{}).StatusPorcelain(context.Background(), "/repo") //nolint:errcheck
}

func TestFakeBranchShowCurrentReturnsScriptedResult(t *testing.T) {
	t.Parallel()
	fake := &Fake{BranchShowCurrentByDir: map[string]Result{"/repo": {Value: "feature"}}}
	branch, err := fake.BranchShowCurrent(context.Background(), "/repo")
	if err != nil || branch != "feature" {
		t.Fatalf("BranchShowCurrent() = (%q, %v)", branch, err)
	}
}

func TestFakeBranchShowCurrentPanicsWhenUnscripted(t *testing.T) {
	t.Parallel()
	t.Cleanup(func() {
		if recover() == nil {
			t.Fatal("want a panic for an unscripted dir")
		}
	})
	(&Fake{}).BranchShowCurrent(context.Background(), "/repo") //nolint:errcheck
}

func TestFakeMergeBaseIsAncestorStrictReturnsScriptedError(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("boom")
	fake := &Fake{MergeBaseIsAncestorStrictErrByCase: map[string]error{key("/repo", "base", "head"): wantErr}}
	if err := fake.MergeBaseIsAncestorStrict(context.Background(), "/repo", "base", "head"); err != wantErr {
		t.Fatalf("MergeBaseIsAncestorStrict() = %v, want %v", err, wantErr)
	}
}

func TestFakeMergeBaseIsAncestorStrictPanicsWhenUnscripted(t *testing.T) {
	t.Parallel()
	t.Cleanup(func() {
		if recover() == nil {
			t.Fatal("want a panic for an unscripted case")
		}
	})
	(&Fake{}).MergeBaseIsAncestorStrict(context.Background(), "/repo", "base", "head") //nolint:errcheck
}

func TestFakeBranchSetUpstreamToReturnsScriptedError(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("boom")
	fake := &Fake{BranchSetUpstreamToErrByCase: map[string]error{key("/repo", "origin/feature", "feature"): wantErr}}
	if err := fake.BranchSetUpstreamTo(context.Background(), "/repo", "origin/feature", "feature"); err != wantErr {
		t.Fatalf("BranchSetUpstreamTo() = %v, want %v", err, wantErr)
	}
}

func TestFakeBranchSetUpstreamToPanicsWhenUnscripted(t *testing.T) {
	t.Parallel()
	t.Cleanup(func() {
		if recover() == nil {
			t.Fatal("want a panic for an unscripted case")
		}
	})
	(&Fake{}).BranchSetUpstreamTo(context.Background(), "/repo", "origin/feature", "feature") //nolint:errcheck
}

func TestFakeMergeTreeWriteTreeReturnsScriptedResult(t *testing.T) {
	t.Parallel()
	fake := &Fake{MergeTreeWriteTreeByCase: map[string]Result{key("/repo", "a", "b"): {Value: "treeid"}}}
	tree, err := fake.MergeTreeWriteTree(context.Background(), "/repo", "a", "b")
	if err != nil || tree != "treeid" {
		t.Fatalf("MergeTreeWriteTree() = (%q, %v)", tree, err)
	}
}

func TestFakeMergeTreeWriteTreePanicsWhenUnscripted(t *testing.T) {
	t.Parallel()
	t.Cleanup(func() {
		if recover() == nil {
			t.Fatal("want a panic for an unscripted case")
		}
	})
	(&Fake{}).MergeTreeWriteTree(context.Background(), "/repo", "a", "b") //nolint:errcheck
}

func TestFakeShowTreeFormatReturnsScriptedResult(t *testing.T) {
	t.Parallel()
	fake := &Fake{ShowTreeFormatByCase: map[string]Result{key("/repo", "head1"): {Value: "treeid"}}}
	tree, err := fake.ShowTreeFormat(context.Background(), "/repo", "head1")
	if err != nil || tree != "treeid" {
		t.Fatalf("ShowTreeFormat() = (%q, %v)", tree, err)
	}
}

func TestFakeShowTreeFormatPanicsWhenUnscripted(t *testing.T) {
	t.Parallel()
	t.Cleanup(func() {
		if recover() == nil {
			t.Fatal("want a panic for an unscripted case")
		}
	})
	(&Fake{}).ShowTreeFormat(context.Background(), "/repo", "head1") //nolint:errcheck
}

func TestFakeCommitObjectExistsReturnsScriptedResult(t *testing.T) {
	t.Parallel()
	fake := &Fake{CommitObjectExistsByCase: map[string]bool{key("/repo", "sha1"): true}}
	if !fake.CommitObjectExists(context.Background(), "/repo", "sha1") {
		t.Fatal("CommitObjectExists() = false, want true")
	}
}

func TestFakeCommitObjectExistsPanicsWhenUnscripted(t *testing.T) {
	t.Parallel()
	t.Cleanup(func() {
		if recover() == nil {
			t.Fatal("want a panic for an unscripted case")
		}
	})
	(&Fake{}).CommitObjectExists(context.Background(), "/repo", "sha1")
}

func TestFakeConfigRegexpMatchesReturnsScriptedResult(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("boom")
	fake := &Fake{ConfigRegexpMatchesByCase: map[string]BoolResult{key("/repo", "^remote\\.x\\."): {Value: true, Err: wantErr}}}
	matched, err := fake.ConfigRegexpMatches(context.Background(), "/repo", "^remote\\.x\\.")
	if !matched || err != wantErr {
		t.Fatalf("ConfigRegexpMatches() = (%v, %v)", matched, err)
	}
}

func TestFakeConfigRegexpMatchesPanicsWhenUnscripted(t *testing.T) {
	t.Parallel()
	t.Cleanup(func() {
		if recover() == nil {
			t.Fatal("want a panic for an unscripted case")
		}
	})
	(&Fake{}).ConfigRegexpMatches(context.Background(), "/repo", "^remote\\.x\\.") //nolint:errcheck
}
