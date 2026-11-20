package worktreeretire

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	fixtureGit := ports.Git
	pushed := false
	ports.Git = func(ctx context.Context, dir string, args ...string) (string, error) {
		if args[0] == "fetch" {
			return "", failure
		}
		if args[0] == "push" {
			pushed = true
		}
		return fixtureGit(ctx, dir, args...)
	}
	if err := PublishArchive(context.Background(), home, "remote", &result, ports); !errors.Is(err, failure) {
		t.Fatalf("backup failure did not stop archive publication: %v", err)
	}
	if pushed || result.ArchiveSHA != "" {
		t.Fatal("failed history backup produced a publication receipt")
	}
}
