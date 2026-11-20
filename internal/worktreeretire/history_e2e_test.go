//go:build e2e

package worktreeretire

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func historyGit(ctx context.Context, dir string, args ...string) (string, error) {
	body, err := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%v: %s: %w", args, body, err)
	}
	return strings.TrimSpace(string(body)), nil
}
func historyRepository(t *testing.T) (string, string, string) {
	t.Helper()
	root := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		out, err := historyGit(context.Background(), root, args...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	run("init", "--initial-branch=main")
	run("config", "user.name", "WB test")
	run("config", "user.email", "wb-test@localhost")
	if err := os.WriteFile(filepath.Join(root, "base.txt"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "base")
	base := run("rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte(strings.Repeat("source\n", 1000)), 0600); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "source")
	source := run("rev-parse", "HEAD")
	run("update-ref", "refs/heads/preserved-source", source)
	run("update-ref", "refs/heads/preserved-base", base)
	return root, source, base
}
func historyWorking(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := historyGit(context.Background(), dir, "init", "--initial-branch=main"); err != nil {
		t.Fatal(err)
	}
	return dir
}
func TestE2EHistoryBundleMustRestoreIntoAnEmptyObjectDatabase(t *testing.T) {
	t.Parallel()
	root, source, base := historyRepository(t)
	bundle := filepath.Join(t.TempDir(), "history.bundle")
	if _, err := historyGit(context.Background(), root, "bundle", "create", bundle, "refs/heads/preserved-source", "refs/heads/preserved-base"); err != nil {
		t.Fatal(err)
	}
	ports := Ports{Git: historyGit}
	if err := validateSourceHistory(context.Background(), bundle, source, base, ports); err != nil {
		t.Fatal(err)
	}
	if err := validateSourceHistory(context.Background(), bundle, base, base, ports); err == nil {
		t.Fatal("wrong source accepted")
	}
	body, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatal(err)
	}
	truncated := filepath.Join(t.TempDir(), "truncated.bundle")
	if err := os.WriteFile(truncated, body[:len(body)-20], 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateSourceHistory(context.Background(), truncated, source, base, ports); err == nil {
		t.Fatal("truncated pack accepted")
	}
	for _, stage := range []string{"list-heads", "init", "fetch", "fsck"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			p := ports
			failure := errors.New("history proof unavailable")
			p.Git = func(ctx context.Context, dir string, args ...string) (string, error) {
				if args[0] == stage || len(args) > 1 && args[1] == stage {
					return "", failure
				}
				return historyGit(ctx, dir, args...)
			}
			if err := validateSourceHistory(context.Background(), bundle, source, base, p); !errors.Is(err, failure) {
				t.Fatalf("proof failure lost: %v", err)
			}
		})
	}
}
func TestE2EHistoryArchiveRetryReusesPublishedBytes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root, source, base := historyRepository(t)
	ports := Ports{Git: historyGit, GitBytes: GitBytes}
	result := Receipt{SourceSHA: source, Worktree: root, ArchiveRef: "main"}
	claim := Claim{BaseSHA: base}
	hashes := map[string]string{}
	working := historyWorking(t)
	if err := archiveSourceHistory(ctx, working, "", "", result, claim, hashes, ports); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"config", "user.name", "WB test"}, {"config", "user.email", "wb-test@localhost"}, {"add", "history.bundle"}, {"commit", "-m", "published backup"}} {
		if _, err := historyGit(ctx, working, args...); err != nil {
			t.Fatal(err)
		}
	}
	archiveSHA, err := historyGit(ctx, working, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	retry := historyWorking(t)
	if _, err := historyGit(ctx, retry, "config", "pack.compression", "0"); err != nil {
		t.Fatal(err)
	}
	result.Worktree = filepath.Join(t.TempDir(), "source-no-longer-readable")
	retryHashes := map[string]string{}
	if err := archiveSourceHistory(ctx, retry, working, archiveSHA, result, claim, retryHashes, ports); err != nil {
		t.Fatal(err)
	}
	if retryHashes["history.bundle"] != hashes["history.bundle"] {
		t.Fatal("retry regenerated backup bytes")
	}
}
func TestE2EHistoryArchiveRefusesEveryUnavailableBackupStage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root, source, base := historyRepository(t)
	result := Receipt{SourceSHA: source, Worktree: root, ArchiveRef: "main"}
	claim := Claim{BaseSHA: base}
	failure := errors.New("backup unavailable")
	for _, stage := range []string{"fetch", "create", "list-heads", "capture"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			working := historyWorking(t)
			ports := Ports{Git: func(ctx context.Context, dir string, args ...string) (string, error) {
				if args[0] == stage || len(args) > 1 && args[1] == stage {
					return "", failure
				}
				out, err := historyGit(ctx, dir, args...)
				if err == nil && stage == "capture" && args[0] == "fsck" {
					err = os.Remove(filepath.Join(working, ".git", "source-history.bundle"))
				}
				return out, err
			}}
			if err := archiveSourceHistory(ctx, working, "", "", result, claim, map[string]string{}, ports); err == nil {
				t.Fatal("failed backup accepted")
			}
		})
	}
	// A read or write failure while recovering a published bundle cannot fall
	// back to creating a different backup under the immutable archive ref.
	for _, stage := range []string{"fetch", "head", "moved", "body", "write"} {
		t.Run("retry "+stage, func(t *testing.T) {
			t.Parallel()
			working := historyWorking(t)
			if stage == "write" {
				if err := os.Mkdir(filepath.Join(working, ".git", "source-history.bundle"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			ports := Ports{
				Git: func(_ context.Context, _ string, args ...string) (string, error) {
					if args[0] == "fetch" {
						if stage == "fetch" {
							return "", failure
						}
						return "", nil
					}
					if stage == "head" {
						return "", failure
					}
					if stage == "moved" {
						return base, nil
					}
					return source, nil
				},
				GitBytes: func(context.Context, string, ...string) ([]byte, error) {
					if stage == "body" {
						return nil, failure
					}
					return []byte("backup"), nil
				},
			}
			if err := archiveSourceHistory(ctx, working, "remote", source, result, claim, map[string]string{}, ports); err == nil {
				t.Fatal("unverified archived history accepted")
			}
		})
	}
}
