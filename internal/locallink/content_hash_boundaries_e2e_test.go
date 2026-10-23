//go:build e2e

package locallink

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestE2EContentHashRetainsNativeTemporaryIndexFailures(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"scratch removal", "empty tree", "write tree"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			repo := initRepository(t)
			git := ExecGit{Timeout: 30 * time.Second}
			realIndex := filepath.Join(repo, ".git", "index")
			before, err := os.ReadFile(realIndex)
			if err != nil {
				t.Fatal(err)
			}
			run := git.run
			remove := os.Remove
			if phase == "scratch removal" {
				remove = func(path string) error {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
					return os.Remove(path)
				}
			} else {
				run = func(ctx context.Context, dir string, env []string, args ...string) (string, error) {
					if phase == "empty tree" && len(args) == 2 && args[0] == "read-tree" {
						if args[1] == "HEAD" {
							return git.run(ctx, dir, env, "read-tree", "missing-ref")
						}
						if args[1] == "--empty" {
							if err := os.Rename(filepath.Join(dir, ".git"), filepath.Join(dir, ".git-retained")); err != nil {
								t.Fatal(err)
							}
							return git.run(ctx, dir, env, args...)
						}
					}
					if phase == "write tree" && len(args) == 1 && args[0] == "write-tree" {
						for _, variable := range env {
							if strings.HasPrefix(variable, "GIT_INDEX_FILE=") {
								if err := os.WriteFile(strings.TrimPrefix(variable, "GIT_INDEX_FILE="), []byte("invalid index"), 0600); err != nil {
									t.Fatal(err)
								}
							}
						}
					}
					return git.run(ctx, dir, env, args...)
				}
			}
			_, _, err = git.contentHashWithIO(context.Background(), repo, nil, run, remove)
			if err == nil {
				t.Fatal("failed temporary index accepted")
			}
			if phase == "scratch removal" && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("remove error=%v", err)
			}
			if phase == "empty tree" {
				realIndex = filepath.Join(repo, ".git-retained", "index")
			}
			after, err := os.ReadFile(realIndex)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("real index changed: %v", err)
			}
		})
	}
}
