package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

func TestReceiptScanCallerErrorPolicies(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home := t.TempDir()
	if proof, path, err := findAbsorbedConflictCleanupProof(ctx, "", "", "", "", "", "", "sha"); proof != nil || path != "" || err != nil {
		t.Fatalf("empty absorbed home = %+v, %q, %v", proof, path, err)
	}
	if proof, err := findRetiredPrepareCandidateAcknowledgement(ctx, home, "", "", "", "", "", "", "sha"); proof != nil || err != nil {
		t.Fatalf("empty retired default = %+v, %v", proof, err)
	}
	if proof, path, err := findAbsorbedConflictCleanupProof(ctx, home, "", "", "", "", "", "sha"); proof != nil || path != "" || err != nil {
		t.Fatalf("missing absorbed reports = %+v, %q, %v", proof, path, err)
	}
	if proof, err := findRetiredPrepareCandidateAcknowledgement(ctx, home, "", "", "", "", "", "main", "sha"); proof != nil || err != nil {
		t.Fatalf("missing retired reports = %+v, %v", proof, err)
	}
	if err := os.MkdirAll(filepath.Join(home, "reports"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "reports", "worktree-merge"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if proof, path, err := findAbsorbedConflictCleanupProof(ctx, home, "", "", "", "", "", "sha"); proof != nil || path != "" || !errors.Is(err, syscall.ENOTDIR) || !strings.Contains(err.Error(), filepath.Join(home, "reports", "worktree-merge")) {
		t.Fatalf("absorbed ENOTDIR = %+v, %q, %v", proof, path, err)
	}
	if proof, err := findRetiredPrepareCandidateAcknowledgement(ctx, home, "", "", "", "", "", "main", "sha"); proof != nil || !errors.Is(err, syscall.ENOTDIR) || !strings.HasPrefix(err.Error(), "read worktree-merge reports:") {
		t.Fatalf("retired ENOTDIR = %+v, %v", proof, err)
	}
}

func TestForEachWorktreeMergeReceiptFiltersAndStopsInSortedOrder(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	dir := filepath.Join(home, "reports", "worktree-merge")
	if err := os.MkdirAll(filepath.Join(dir, "00-directory.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string]string{
		"01-other.txt": "ignored", "02-first.json": "first", "03-first.ack.json": "ack",
		"04-second.json": "second", "05-third.json": "third",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(dir, "missing"), filepath.Join(dir, "01-dangling.json")); err != nil {
		t.Fatal(err)
	}
	var visited []string
	err := forEachWorktreeMergeReceipt(home, func(path string, bytes []byte) bool {
		visited = append(visited, filepath.Base(path)+":"+string(bytes))
		return strings.HasSuffix(path, "04-second.json")
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"02-first.json:first", "04-second.json:second"}
	if !reflect.DeepEqual(visited, want) {
		t.Fatalf("visited %q, want %q", visited, want)
	}
}

func TestForEachWorktreeMergeReceiptReturnsReadDirError(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "reports"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "reports", "worktree-merge"), []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := forEachWorktreeMergeReceipt(home, func(string, []byte) bool { t.Fatal("visited file"); return false })
	if !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("ReadDir error = %v, want ENOTDIR", err)
	}
}

func TestReceiptScanCallersSkipMalformedAndUnrelatedReceipts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	projectsRoot := t.TempDir()
	home := filepath.Join(projectsRoot, ".wb")
	dir := filepath.Join(home, "reports", "worktree-merge")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string]string{
		"00-malformed.json": "{broken", "01-unrelated.json": "{}",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if proof, path, err := findAbsorbedConflictCleanupProof(ctx, home, "", "task", "/fixture/worktree", "branch", "sha", "target"); proof != nil || path != "" || err != nil {
		t.Fatalf("absorbed malformed/unrelated = %+v, %q, %v", proof, path, err)
	}
	if proof, err := findRetiredPrepareCandidateAcknowledgement(ctx, home, "", "task", "/fixture/worktree", "branch", "sha", "main", "target"); proof != nil || err != nil {
		t.Fatalf("retired malformed/unrelated = %+v, %v", proof, err)
	}
	if hasRetiredPrepareCandidateAcknowledgement(projectsRoot, "task", "/fixture/worktree", "branch", "sha") {
		t.Fatal("adoption accepted malformed/unrelated receipt")
	}
	if hasRetiredPrepareCandidateAcknowledgement("\x00", "task", "/fixture/worktree", "branch", "sha") {
		t.Fatal("adoption resolved an invalid projects root")
	}
}
