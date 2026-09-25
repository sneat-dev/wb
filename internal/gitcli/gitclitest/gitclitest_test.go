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

//nolint:paralleltest // a recover-based panic assertion must be a direct defer, not t.Parallel-safe t.Cleanup
func TestFakeCurrentBranchPanicsWhenUnscripted(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("want a panic for an unscripted dir")
		}
	}()
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

//nolint:paralleltest // a recover-based panic assertion must be a direct defer, not t.Parallel-safe t.Cleanup
func TestFakeRevParsePanicsWhenUnscripted(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("want a panic for an unscripted rev")
		}
	}()
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

//nolint:paralleltest // a recover-based panic assertion must be a direct defer, not t.Parallel-safe t.Cleanup
func TestFakeIsAncestorPanicsWhenUnscripted(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("want a panic for an unscripted case")
		}
	}()
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

//nolint:paralleltest // a recover-based panic assertion must be a direct defer, not t.Parallel-safe t.Cleanup
func TestFakeFetchPanicsWhenUnscripted(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("want a panic for an unscripted remote")
		}
	}()
	(&Fake{}).Fetch(context.Background(), "/repo", "origin") //nolint:errcheck
}

func TestFakeRunReturnsScriptedResult(t *testing.T) {
	t.Parallel()
	fake := &Fake{RunByDirAndArgv: map[string]Result{key("/repo", "status", "--porcelain"): {Value: "clean"}}}
	out, err := fake.Run(context.Background(), "/repo", "status", "--porcelain")
	if err != nil || out != "clean" {
		t.Fatalf("Run() = (%q, %v)", out, err)
	}
}

//nolint:paralleltest // a recover-based panic assertion must be a direct defer, not t.Parallel-safe t.Cleanup
func TestFakeRunPanicsWhenUnscripted(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("want a panic for an unscripted argv")
		}
	}()
	(&Fake{}).Run(context.Background(), "/repo", "status") //nolint:errcheck
}

func TestFakeAtomicRenameRefsReturnsScriptedError(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("stale expected sha")
	fake := &Fake{AtomicRenameRefsErrByCase: map[string]error{key("/repo", "master", "main", "sha"): wantErr}}
	if err := fake.AtomicRenameRefs(context.Background(), "/repo", "master", "main", "sha"); err != wantErr {
		t.Fatalf("AtomicRenameRefs() = %v, want %v", err, wantErr)
	}
}

//nolint:paralleltest // a recover-based panic assertion must be a direct defer, not t.Parallel-safe t.Cleanup
func TestFakeAtomicRenameRefsPanicsWhenUnscripted(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("want a panic for an unscripted case")
		}
	}()
	(&Fake{}).AtomicRenameRefs(context.Background(), "/repo", "master", "main", "sha") //nolint:errcheck
}

func TestFakeAttachHeadReturnsScriptedError(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("symbolic-ref failed")
	fake := &Fake{AttachHeadErrByDirAndDestination: map[string]error{key("/repo", "main"): wantErr}}
	if err := fake.AttachHead(context.Background(), "/repo", "main"); err != wantErr {
		t.Fatalf("AttachHead() = %v, want %v", err, wantErr)
	}
}

//nolint:paralleltest // a recover-based panic assertion must be a direct defer, not t.Parallel-safe t.Cleanup
func TestFakeAttachHeadPanicsWhenUnscripted(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("want a panic for an unscripted destination")
		}
	}()
	(&Fake{}).AttachHead(context.Background(), "/repo", "main") //nolint:errcheck
}

func TestFakeRefExistsReturnsScriptedResult(t *testing.T) {
	t.Parallel()
	fake := &Fake{RefExistsByDirAndRef: map[string]BoolResult{key("/repo", "refs/heads/main"): {Value: true}}}
	ok, err := fake.RefExists(context.Background(), "/repo", "refs/heads/main")
	if err != nil || !ok {
		t.Fatalf("RefExists() = (%v, %v)", ok, err)
	}
}

//nolint:paralleltest // a recover-based panic assertion must be a direct defer, not t.Parallel-safe t.Cleanup
func TestFakeRefExistsPanicsWhenUnscripted(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("want a panic for an unscripted ref")
		}
	}()
	(&Fake{}).RefExists(context.Background(), "/repo", "refs/heads/main") //nolint:errcheck
}
