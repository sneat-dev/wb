//go:build e2e && !windows

package worktrees

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestE2EActiveClaimWalkKeepsHeldDirectoriesDuringPathReplacement(t *testing.T) {
	t.Parallel()
	for _, stage := range []int{1, 2, 3} {
		t.Run(map[int]string{1: "worklogs", 2: "runs", 3: "claims"}[stage], func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			publication := publishClaimForEvidenceWalk(t, home, "task", "run")
			if err := os.WriteFile(filepath.Join(home, "worklogs", "task", "runs", "non-directory-run"), []byte("ignore"), 0o600); err != nil {
				t.Fatal(err)
			}
			paths := map[int]string{
				1: filepath.Join(home, "worklogs"),
				2: filepath.Join(home, "worklogs", "task", "runs"),
				3: filepath.Join(home, "worklogs", "task", "runs", "run", "claims"),
			}
			reads, visits := 0, 0
			err := walkActiveWorkLogClaimsWithReader(home, func(claims *os.File, claimID string, claim workLogClaim) {
				visits++
				if claimID != publication.ClaimID || claim.ClaimID != publication.ClaimID {
					t.Errorf("wrong held claim: id=%s claim=%+v", claimID, claim)
				}
				var reread workLogClaim
				if err := readJSONAt(claims, claimID+".json", &reread); err != nil || reread.ClaimID != claimID {
					t.Errorf("held claim could not be reread: %+v %v", reread, err)
				}
			}, func(directory *os.File) ([]os.DirEntry, error) {
				reads++
				if reads == stage {
					path := paths[stage]
					if err := os.Rename(path, path+"-retained"); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(path, 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(path, "replacement-marker"), []byte("preserve replacement"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				return readActiveDirectoryEntries(directory)
			})
			if err != nil || visits != 1 || reads != 3 {
				t.Fatalf("held-directory walk: visits=%d reads=%d err=%v", visits, reads, err)
			}
			marker, err := os.ReadFile(filepath.Join(paths[stage], "replacement-marker"))
			if err != nil || string(marker) != "preserve replacement" {
				t.Fatalf("replacement tree was changed: %q %v", marker, err)
			}
		})
	}
}

func TestE2EActiveClaimWalkReadFailuresCloseHeldDirectories(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		stage int
		cause error
		want  error
	}{
		{"root vanished", 1, os.ErrNotExist, nil},
		{"root unreadable", 1, os.ErrPermission, os.ErrPermission},
		{"runs vanished", 2, os.ErrNotExist, nil},
		{"runs unreadable", 2, os.ErrPermission, os.ErrPermission},
		{"claims unreadable", 3, os.ErrPermission, os.ErrPermission},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			_ = publishClaimForEvidenceWalk(t, home, "task", "run")
			var held []*os.File
			visits := 0
			err := walkActiveWorkLogClaimsWithReader(home, func(*os.File, string, workLogClaim) { visits++ }, func(directory *os.File) ([]os.DirEntry, error) {
				held = append(held, directory)
				if len(held) == test.stage {
					return nil, test.cause
				}
				return readActiveDirectoryEntries(directory)
			})
			if !errors.Is(err, test.want) || visits != 0 || len(held) != test.stage {
				t.Fatalf("failure: visits=%d reads=%d error=%v want=%v", visits, len(held), err, test.want)
			}
			for _, directory := range held {
				if _, err := directory.Stat(); !errors.Is(err, os.ErrClosed) {
					t.Fatalf("held %s remains open: %v", directory.Name(), err)
				}
			}
		})
	}
}

func TestE2EActiveClaimDirectoryReaderSortsAndPreservesReadFailure(t *testing.T) {
	t.Parallel()
	path := t.TempDir()
	for _, name := range []string{"z", "a"} {
		if err := os.Mkdir(filepath.Join(path, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	directory, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := readActiveDirectoryEntries(directory)
	if err != nil || len(entries) != 2 || entries[0].Name() != "a" || entries[1].Name() != "z" {
		t.Fatalf("directory order: %v %v", entries, err)
	}
	if err := directory.Close(); err != nil {
		t.Fatal(err)
	}
	if entries, err := readActiveDirectoryEntries(directory); entries != nil || err == nil {
		t.Fatalf("closed directory: %v %v", entries, err)
	}
}

func TestE2EActiveClaimWalkFailsClosedOnHeldDirectoryMetadataPermissionLoss(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory search permission checks")
	}
	home := t.TempDir()
	_ = publishClaimForEvidenceWalk(t, home, "task", "run")
	path := filepath.Join(home, "worklogs")
	t.Cleanup(func() { _ = os.Chmod(path, 0o700) })
	var held *os.File
	visits := 0
	err := walkActiveWorkLogClaimsWithReader(home, func(*os.File, string, workLogClaim) { visits++ }, func(directory *os.File) ([]os.DirEntry, error) {
		held = directory
		if err := os.Chmod(path, 0); err != nil {
			t.Fatal(err)
		}
		return readActiveDirectoryEntries(directory)
	})
	var pathErr *os.PathError
	if visits != 0 || !errors.Is(err, os.ErrPermission) || !errors.As(err, &pathErr) || pathErr.Op != "fstatat" {
		t.Fatalf("metadata permission loss must abort inventory: visits=%d err=%v", visits, err)
	}
	if held == nil {
		t.Fatal("directory was not opened before permission loss")
	}
	if _, err := held.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("failed metadata walk retained directory handle: %v", err)
	}
}
