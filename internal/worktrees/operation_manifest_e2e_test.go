//go:build e2e

package worktrees

import (
	"bytes"
	"errors"
	"os"
	"testing"
)

func TestE2EOperationManifestAuthenticatesPhysicalPublicationWinner(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"own publication", "matching collision", "blank legacy collision", "conflicting collision", "corrupt collision", "publication refusal", "reread refusal", "existing corrupt"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			worktree := newJournalWorktree(t)
			expected := newCreatedManifest("task")
			expected.Worktree = worktree
			base := claimJournalPorts()
			ports := base
			cause := errors.New("owned journal refusal")
			published, collisions := false, 0
			var winnerBytes []byte
			if mode == "existing corrupt" {
				dir, err := openJournalDirectory(worktree, true)
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := dir.Close(); err != nil {
						t.Error(err)
					}
				}()
				if err := base.WriteBytesImmutableAt(dir, manifestName, []byte("invalid: ["), 0600, false); err != nil {
					t.Fatal(err)
				}
			}
			ports.WriteBytesImmutableAt = func(dir *os.File, name string, data []byte, modeBits os.FileMode, replace bool) error {
				if name != manifestName {
					return base.WriteBytesImmutableAt(dir, name, data, modeBits, replace)
				}
				if mode == "publication refusal" {
					return cause
				}
				if mode == "matching collision" || mode == "blank legacy collision" || mode == "conflicting collision" || mode == "corrupt collision" {
					winner := expected
					if mode == "blank legacy collision" {
						winner.Worktree, winner.Base, winner.BaseSHA = "", "", ""
					}
					if mode == "conflicting collision" {
						winner.BaseSHA = "conflicting"
					}
					if mode == "corrupt collision" {
						if err := base.WriteBytesImmutableAt(dir, name, []byte("invalid: ["), modeBits, false); err != nil {
							return err
						}
					} else if err := base.WriteManifest(worktree, winner); err != nil {
						return err
					}
					var err error
					winnerBytes, err = base.ReadBytesAt(dir, name)
					if err != nil {
						return err
					}
					collisions++
				}
				err := base.WriteBytesImmutableAt(dir, name, data, modeBits, replace)
				published = true
				return err
			}
			ports.ReadBytesAt = func(dir *os.File, name string) ([]byte, error) {
				if published && mode == "reread refusal" {
					return nil, cause
				}
				return base.ReadBytesAt(dir, name)
			}
			err := ensureOperationManifestWithPorts(worktree, expected, ports)
			good := mode == "own publication" || mode == "matching collision" || mode == "blank legacy collision"
			if good && err != nil {
				t.Fatal(err)
			}
			if !good && err == nil {
				t.Fatalf("accepted %s", mode)
			}
			if (mode == "publication refusal" || mode == "reread refusal") && !errors.Is(err, cause) {
				t.Fatalf("lost actual refusal: %v", err)
			}
			if winnerBytes != nil {
				if collisions != 1 {
					t.Fatalf("actual collision count=%d", collisions)
				}
				dir, openErr := openJournalDirectory(worktree, false)
				if openErr != nil {
					t.Fatal(openErr)
				}
				defer func() {
					if err := dir.Close(); err != nil {
						t.Error(err)
					}
				}()
				after, readErr := base.ReadBytesAt(dir, manifestName)
				if readErr != nil || !bytes.Equal(after, winnerBytes) {
					t.Fatalf("immutable winner replaced: %v", readErr)
				}
			}
		})
	}
}
