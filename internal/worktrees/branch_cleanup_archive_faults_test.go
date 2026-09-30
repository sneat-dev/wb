package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReviewedBranchArchiveStopsAtEveryFailedIOBoundary(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{
		"mkdir recovery", "sync recovery ancestors", "mkdir archive", "chmod archive", "sync recovery after mkdir",
		"create bundle", "chmod bundle", "sync bundle", "hash bundle", "copy receipt", "mkdir verification",
		"init verification", "fetch bundle", "resolve restored head", "encode manifest", "write manifest", "sync archive", "sync recovery final",
	} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			receipt := filepath.Join(t.TempDir(), "receipt.json")
			if err := os.WriteFile(receipt, []byte("{}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			digest, err := supersessionFileSHA256(receipt)
			if err != nil {
				t.Fatal(err)
			}
			result := BranchCleanupResult{BranchEntry: BranchEntry{
				Repository: "acme/app", Branch: "feature/reviewed", Scope: BranchScopeRemote,
				SHA: retirementHead, TargetSHA: retirementTarget, SupersessionReceipt: receipt, SupersessionSHA256: digest,
			}}
			var stages []string
			fail := func(name string) error {
				stages = append(stages, name)
				if name == failure {
					return errors.New("injected " + name)
				}
				return nil
			}
			syncCount := 0
			io := reviewedBranchArchiveIO{
				mkdirAll: func(path string, mode os.FileMode) error {
					if err := fail("mkdir recovery"); err != nil {
						return err
					}
					return os.MkdirAll(path, mode)
				},
				syncAncestors: func(path string) error {
					if err := fail("sync recovery ancestors"); err != nil {
						return err
					}
					return syncDirectoryAndAncestors(path)
				},
				mkdirTemp: func(path, pattern string) (string, error) {
					name := "mkdir archive"
					if path == "" {
						name = "mkdir verification"
					}
					if err := fail(name); err != nil {
						return "", err
					}
					return os.MkdirTemp(path, pattern)
				},
				chmod: func(path string, mode os.FileMode) error {
					name := "chmod archive"
					if filepath.Base(path) == "source.bundle" {
						name = "chmod bundle"
					}
					if err := fail(name); err != nil {
						return err
					}
					return os.Chmod(path, mode)
				},
				syncDirectory: func(path string) error {
					syncCount++
					name := []string{"sync recovery after mkdir", "sync archive", "sync recovery final"}[syncCount-1]
					if err := fail(name); err != nil {
						return err
					}
					return syncDirectory(path)
				},
				syncFile: func(path string) error {
					if err := fail("sync bundle"); err != nil {
						return err
					}
					return syncFile(path)
				},
				fileSHA256: func(path string) (string, error) {
					if err := fail("hash bundle"); err != nil {
						return "", err
					}
					return fileSHA256(path)
				},
				copyFileSHA256: func(source, target string) (string, error) {
					if err := fail("copy receipt"); err != nil {
						return "", err
					}
					return copyFileSHA256(source, target)
				},
				writeDurableFile: func(path string, body []byte, mode os.FileMode) error {
					if err := fail("write manifest"); err != nil {
						return err
					}
					return writeDurableFile(path, body, mode)
				},
				now: func() time.Time {
					stages = append(stages, "encode manifest")
					if failure == "encode manifest" {
						return time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)
					}
					return time.Now()
				},
				git: func(_ context.Context, _ string, args ...string) (string, error) {
					name := map[string]string{"bundle": "create bundle", "init": "init verification", "fetch": "fetch bundle", "rev-parse": "resolve restored head"}[args[0]]
					if err := fail(name); err != nil {
						return "", err
					}
					if args[0] == "bundle" {
						if args[3] != "refs/remotes/origin/feature/reviewed" {
							t.Fatalf("remote bundle source = %q", args[3])
						}
						return "", os.WriteFile(args[2], []byte("bundle bytes\n"), 0o600)
					}
					if args[0] == "fetch" && args[2] != "refs/remotes/origin/feature/reviewed:refs/heads/recovery" {
						t.Fatalf("remote bundle restore ref = %q", args[2])
					}
					if args[0] == "rev-parse" {
						return retirementHead + "\n", nil
					}
					return "", nil
				},
			}
			path, err := archiveReviewedBranchWithIO(context.Background(), t.TempDir(), "", result, io)
			wantError := "injected " + failure
			if failure == "encode manifest" {
				wantError = "year outside of range"
			}
			if err == nil || path != "" || !strings.Contains(err.Error(), wantError) {
				t.Fatalf("archive fault %q: path=%q err=%v", failure, path, err)
			}
			if len(stages) == 0 || stages[len(stages)-1] != failure {
				t.Fatalf("archive continued after %q: stages=%q", failure, stages)
			}
		})
	}
}
