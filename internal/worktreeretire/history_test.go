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
func TestHistoryBundleMustRestoreIntoAnEmptyObjectDatabase(t *testing.T) {
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
func TestHistoryArchiveRetryReusesPublishedBytes(t *testing.T) {
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
func TestHistoryArchiveRefusesEveryUnavailableBackupStage(t *testing.T) {
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

//nolint:paralleltest // TMPDIR failure is scoped to this test by t.Setenv.
func TestHistoryProofRefusesUnavailableRestoreDirectory(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	if err := validateSourceHistory(context.Background(), "bundle", "source", "base", Ports{}); err == nil {
		t.Fatal("missing restore directory accepted")
	}
}
func TestLegacyHistoryArchiveChecksPinnedManifest(t *testing.T) {
	t.Parallel()
	failure := errors.New("manifest unavailable")
	for _, body := range []string{`{"files":{}}`, `{"base_sha":"base","files":{"history.bundle":"hash"}}`, `invalid`} {
		ports := Ports{Git: func(context.Context, string, ...string) (string, error) { return "head", nil }, GitBytes: func(context.Context, string, ...string) ([]byte, error) { return []byte(body), nil }}
		legacy, err := legacyHistoryArchive(context.Background(), "work", "remote", "ref", "head", ports)
		if body == `invalid` {
			if err == nil {
				t.Fatal("invalid manifest accepted")
			}
		} else if err != nil || legacy != (body == `{"files":{}}`) {
			t.Fatalf("legacy=%t err=%v", legacy, err)
		}
	}
	ports := Ports{Git: func(context.Context, string, ...string) (string, error) { return "head", nil }, GitBytes: func(context.Context, string, ...string) ([]byte, error) { return nil, failure }}
	if _, err := legacyHistoryArchive(context.Background(), "work", "remote", "ref", "head", ports); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	ports.Git = func(context.Context, string, ...string) (string, error) { return "", failure }
	if _, err := legacyHistoryArchive(context.Background(), "work", "remote", "ref", "head", ports); !errors.Is(err, failure) {
		t.Fatal(err)
	}
}

func TestPublishArchiveResumesLegacyHistoryOnlyWithValidAncestry(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"legacy valid", "manifest failed", "ancestry failed"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			home, result, ports := archiveFixture(t)
			oldRead := ports.ReadClaim
			ports.ReadClaim = func(home, worktree string) (Claim, error) {
				claim, err := oldRead(home, worktree)
				claim.BaseSHA = strings.Repeat("a", 40)
				return claim, err
			}
			sha := strings.Repeat("b", 40)
			ports.RemoteSHA = func(context.Context, string, string, string) (string, error) { return sha, nil }
			oldGit := ports.Git
			failure := errors.New("legacy proof unavailable")
			ports.Git = func(ctx context.Context, dir string, args ...string) (string, error) {
				if args[0] == "merge-base" {
					if stage == "ancestry failed" {
						return "", failure
					}
					return "", nil
				}
				return oldGit(ctx, dir, args...)
			}
			oldBytes := ports.GitBytes
			ports.GitBytes = func(ctx context.Context, dir string, args ...string) ([]byte, error) {
				if _, err := os.Stat(filepath.Join(dir, "retirement.json")); os.IsNotExist(err) {
					if stage == "manifest failed" {
						return nil, failure
					}
					return []byte(`{"files":{}}`), nil
				}
				return oldBytes(ctx, dir, args...)
			}
			err := PublishArchive(context.Background(), home, "remote", &result, ports)
			if stage == "legacy valid" {
				if err != nil {
					t.Fatal(err)
				}
				if result.ArchiveSHA != sha {
					t.Fatal("legacy archive replaced")
				}
			} else if !errors.Is(err, failure) {
				t.Fatalf("proof failure lost: %v", err)
			}
		})
	}
}
func TestArchiveManifestBindsOriginalBase(t *testing.T) {
	t.Parallel()
	expected := Manifest{Version: 1, BaseSHA: "original"}
	actual := expected
	actual.BaseSHA = "replacement"
	if _, err := ValidateArchiveManifest(expected, actual); err == nil {
		t.Fatal("changed original base accepted")
	}
}

func TestPublishArchiveStopsBeforePublicationWhenHistoryBackupFails(t *testing.T) {
	t.Parallel()
	home, result, ports := archiveFixture(t)
	readClaim := ports.ReadClaim
	ports.ReadClaim = func(home, worktree string) (Claim, error) {
		claim, err := readClaim(home, worktree)
		claim.BaseSHA = strings.Repeat("a", 40)
		return claim, err
	}
	failure := errors.New("source history backup unavailable")
	git := ports.Git
	pushed := false
	ports.Git = func(ctx context.Context, dir string, args ...string) (string, error) {
		if args[0] == "fetch" {
			return "", failure
		}
		if args[0] == "push" {
			pushed = true
		}
		return git(ctx, dir, args...)
	}
	if err := PublishArchive(context.Background(), home, "remote", &result, ports); !errors.Is(err, failure) {
		t.Fatalf("backup failure did not stop archive publication: %v", err)
	}
	if pushed || result.ArchiveSHA != "" {
		t.Fatal("failed history backup produced a publication receipt")
	}
}
