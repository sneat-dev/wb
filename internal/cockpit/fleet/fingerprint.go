package fleet

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Fingerprint is a cheap signal that a canonical clone's Git state has not
// changed: a hash over file metadata, never over Git output, so computing it
// runs no Git command and reads no file content. The signals are
//
//   - the size and modification time of .git/HEAD, .git/index,
//     .git/packed-refs, .git/config and .git/reftable/tables.list (the
//     checked-out branch, the staged and tracked state, every packed ref, the
//     remotes and default branch the configuration names, and the reftable
//     backend's table list), and the names in .git/reftable;
//   - every file under .git/refs but refs/wb, by relative name, size and
//     modification time (a loose branch, remote-tracking ref or tag created,
//     moved or deleted). refs/wb is WB's own private namespace, which reading
//     a repository's branches writes to; counting it would make every read
//     change the fingerprint it was keyed on;
//   - the worktree list: each entry of .git/worktrees with the size and
//     modification time of its HEAD and index, and the directory names under
//     <clone>/.worktrees (a worktree created, removed, switched or staged).
//
// Edits that touch no index and no ref do not move it, and neither does WB
// state that lives outside Git (a heartbeat, a manifest); the snapshotter
// reads that state on every pass instead, and uses the fingerprint only to
// skip the Git work.
func Fingerprint(checkout string) (string, error) {
	gitDir := filepath.Join(checkout, ".git")
	info, err := os.Lstat(gitDir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a canonical clone", checkout)
	}
	sum := sha256.New()
	for _, name := range []string{"HEAD", "index", "packed-refs", "config", filepath.Join("reftable", "tables.list")} {
		if err := stamp(sum, gitDir, name); err != nil {
			return "", err
		}
	}
	tables, err := os.ReadDir(filepath.Join(gitDir, "reftable"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	for _, table := range tables {
		_, _ = fmt.Fprintf(sum, "reftable\x00%s\n", table.Name())
	}
	privateRefs := filepath.Join(gitDir, "refs", "wb")
	err = filepath.WalkDir(filepath.Join(gitDir, "refs"), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path == privateRefs {
				return filepath.SkipDir
			}
			return nil
		}
		return stamp(sum, gitDir, strings.TrimPrefix(path, gitDir))
	})
	if err != nil {
		return "", err
	}
	linked, err := os.ReadDir(filepath.Join(gitDir, "worktrees"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	for _, entry := range linked {
		for _, name := range []string{"HEAD", "index"} {
			if err := stamp(sum, gitDir, filepath.Join("worktrees", entry.Name(), name)); err != nil {
				return "", err
			}
		}
	}
	local, err := os.ReadDir(filepath.Join(checkout, ".worktrees"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	for _, entry := range local {
		_, _ = fmt.Fprintf(sum, "local-worktree\x00%s\n", entry.Name())
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// stamp writes one file's size and modification time to sum, or a marker when
// the file is absent, which is itself a state worth telling apart.
func stamp(sum hash.Hash, gitDir, name string) error {
	info, err := os.Lstat(filepath.Join(gitDir, name))
	if errors.Is(err, fs.ErrNotExist) {
		_, _ = fmt.Fprintf(sum, "%s\x00absent\n", name)
		return nil
	}
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(sum, "%s\x00%d\x00%d\n", name, info.Size(), info.ModTime().UnixNano())
	return nil
}
