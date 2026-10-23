package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestPRCreateWorktreeResolutionDirectoryContracts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, argument := range []string{dir, dir + string(filepath.Separator) + ".", relative} {
		t.Run(argument, func(t *testing.T) {
			t.Parallel()
			expected, err := filepath.Abs(argument)
			if err != nil {
				t.Fatal(err)
			}
			got, err := ResolvePullRequestCreateWorktree(context.Background(), t.TempDir(), argument)
			if err != nil || got != expected {
				t.Fatalf("argument=%q got=%q want=%q err=%v", argument, got, expected, err)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(dir, ".wb", "local", "manifest.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("directory fixture unexpectedly carries custody evidence: %v", err)
	}
}

func TestPRCreateWorktreeResolutionNormalizationRefusal(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, dir)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(relative)
	if err != nil || !info.IsDir() {
		t.Fatalf("native Stat precondition info=%v err=%v", info, err)
	}
	sentinel := errors.New("absolute normalization refused")
	calls := 0
	got, err := resolvePullRequestCreateWorktreeWithPathResolver(context.Background(), t.TempDir(), relative, func(argument string) (string, error) {
		calls++
		if argument != relative {
			t.Fatalf("argument=%q want=%q", argument, relative)
		}
		return "", sentinel
	})
	if got != "" || !errors.Is(err, sentinel) || err.Error() != "resolve "+relative+" to an absolute path: "+sentinel.Error() || calls != 1 {
		t.Fatalf("got=%q err=%v calls=%d", got, err, calls)
	}
}

func TestPRCreateWorktreeResolutionNativeFilesystemRefusals(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"projects regular ancestor", "existing regular argument"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			file := filepath.Join(dir, "regular")
			if err := os.WriteFile(file, []byte("owned regular file"), 0600); err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(file, "child")
			argument := "task"
			if name == "existing regular argument" {
				root = dir
				argument = file
				info, err := os.Stat(argument)
				if err != nil || info.IsDir() {
					t.Fatalf("native non-directory info=%v err=%v", info, err)
				}
			}
			got, err := ResolvePullRequestCreateWorktree(context.Background(), root, argument)
			if got != "" || err == nil || !strings.HasPrefix(err.Error(), "resolve task ") {
				t.Fatalf("got=%q err=%v", got, err)
			}
			if name == "projects regular ancestor" {
				_, nativeErr := worktrees.List(context.Background(), worktrees.ListOptions{ProjectsRoot: root, Task: argument})
				if nativeErr == nil || err.Error() != fmt.Sprintf("resolve task %q: %v", argument, nativeErr) {
					t.Fatalf("native inventory refusal=%v resolver=%v", nativeErr, err)
				}
			} else if !strings.Contains(err.Error(), "safe path segment") {
				t.Fatalf("expected task validation after native Stat(non-directory): %v", err)
			}
		})
	}
}
