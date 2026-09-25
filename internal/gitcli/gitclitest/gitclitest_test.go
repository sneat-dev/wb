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

func TestFakeRevParsePanicsWhenUnscripted(t *testing.T) {
	t.Parallel()
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

func TestFakeIsAncestorPanicsWhenUnscripted(t *testing.T) {
	t.Parallel()
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

func TestFakeFetchPanicsWhenUnscripted(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Fatal("want a panic for an unscripted remote")
		}
	}()
	(&Fake{}).Fetch(context.Background(), "/repo", "origin") //nolint:errcheck
}
