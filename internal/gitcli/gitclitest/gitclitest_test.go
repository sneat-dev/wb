package gitclitest

import (
	"context"
	"errors"
	"testing"
)

// mustPanic runs fn and returns the recovered value. It recovers inside its
// own call frame rather than via a deferred function in the caller, so a
// parallel test can use it without tripping the paralleltest check-cleanup
// rule (defer alongside t.Parallel() in the same function).
func mustPanic(t *testing.T, fn func()) any {
	t.Helper()
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		fn()
	}()
	if recovered == nil {
		t.Fatal("want a panic")
	}
	return recovered
}

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
	mustPanic(t, func() {
		_, _ = (&Fake{}).CurrentBranch(context.Background(), "/repo")
	})
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
	mustPanic(t, func() {
		_, _ = (&Fake{}).RevParse(context.Background(), "/repo", "HEAD")
	})
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
	mustPanic(t, func() {
		_, _ = (&Fake{}).IsAncestor(context.Background(), "/repo", "base", "head")
	})
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
	mustPanic(t, func() {
		_ = (&Fake{}).Fetch(context.Background(), "/repo", "origin")
	})
}
