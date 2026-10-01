//go:build e2e

package worktrees

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func newRetiredStageClaimFixture(t *testing.T, names ...string) (string, *os.File) {
	t.Helper()
	root := t.TempDir()
	for _, name := range names {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	parent, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.Close() })
	return root, parent
}

func assertRetiredStageDescriptorClosed(t *testing.T, descriptor *os.File) {
	t.Helper()
	if descriptor == nil {
		t.Fatal("retired-stage operation did not expose its held descriptor")
	}
	if _, err := descriptor.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("retired-stage descriptor remains usable: %v", err)
	}
}

func TestE2ERetiredStageClaimUsesHeldNoFollowDirectoryAndPreservesOccupants(t *testing.T) {
	t.Parallel()
	root, parent := newRetiredStageClaimFixture(t, ".wb-retired-stage-aa-occupied")
	occupied := filepath.Join(root, ".wb-retired-stage-aa-occupied")
	if err := os.WriteFile(filepath.Join(occupied, "payload"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, ".wb-retired-stage-bb-link")); err != nil {
		t.Fatal(err)
	}
	if name, claimed, err := claimRetiredStageDirectory(parent, ".wb-stage-", ".wb-retired-stage-"); err != nil || claimed || name != "" {
		t.Fatalf("occupied and symlinked retirements were claimed: %q, %t, %v", name, claimed, err)
	}
	if link, err := os.Readlink(filepath.Join(root, ".wb-retired-stage-bb-link")); err != nil || link != outside {
		t.Fatalf("refused symlinked retirement changed: %q, %v", link, err)
	}
	if info, err := os.Stat(outside); err != nil || !info.IsDir() {
		t.Fatalf("outside directory changed after refusal: %v, %v", info, err)
	}
	if err := os.Mkdir(filepath.Join(root, ".wb-retired-stage-zz-empty"), 0o700); err != nil {
		t.Fatal(err)
	}
	name, claimed, err := claimRetiredStageDirectory(parent, ".wb-stage-", ".wb-retired-stage-")
	if err != nil || !claimed || !strings.HasPrefix(name, ".wb-stage-") {
		t.Fatalf("claim empty retirement = %q, %t, %v", name, claimed, err)
	}
	if _, err := os.Lstat(filepath.Join(root, ".wb-retired-stage-zz-empty")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("original empty retirement remains: %v", err)
	}
	if info, err := os.Stat(filepath.Join(root, name)); err != nil || !info.IsDir() {
		t.Fatalf("claimed stage = %v, %v", info, err)
	}
	if contents, err := os.ReadFile(filepath.Join(occupied, "payload")); err != nil || string(contents) != "keep" {
		t.Fatalf("occupied retirement changed: %q, %v", contents, err)
	}
	if link, err := os.Readlink(filepath.Join(root, ".wb-retired-stage-bb-link")); err != nil || link != outside {
		t.Fatalf("symlinked retirement changed: %q, %v", link, err)
	}
	if info, err := os.Stat(outside); err != nil || !info.IsDir() {
		t.Fatalf("outside directory changed: %v, %v", info, err)
	}
}

func TestE2ERetiredStageClaimClosesHandleOnInspectionFailure(t *testing.T) {
	t.Parallel()
	const retiredName = ".wb-retired-stage-inspection"
	root, parent := newRetiredStageClaimFixture(t, retiredName)
	var held *os.File
	want := errors.New("test: read retired directory failed")
	_, claimed, err := claimRetiredStageDirectoryWith(parent, ".wb-stage-", ".wb-retired-stage-", retiredStageClaimOps{
		empty: func(directory *os.File) (bool, error) { held = directory; return false, want },
		claim: quarantineDirectoryEntryNamed,
	})
	if claimed || !errors.Is(err, want) || !strings.Contains(err.Error(), "inspect retired staging directory "+retiredName) {
		t.Fatalf("inspection refusal = %t, %v", claimed, err)
	}
	assertRetiredStageDescriptorClosed(t, held)
	if info, err := os.Stat(filepath.Join(root, retiredName)); err != nil || !info.IsDir() {
		t.Fatalf("retired stage changed after failed inspection: %v, %v", info, err)
	}
}

func TestE2ERetiredStageClaimSkipsExhaustedCollisionAndReclaimsSibling(t *testing.T) {
	t.Parallel()
	root, parent := newRetiredStageClaimFixture(t, ".wb-retired-stage-aa-colliding", ".wb-retired-stage-zz-reusable")
	var exhausted *os.File
	var exhaustedName, reclaimedName string
	attempts := 0
	claim := func(directory *os.File, name string, held *os.File, prefix string) (*os.File, string, error) {
		if exhausted != nil {
			reclaimedName = name
			return quarantineDirectoryEntryNamed(directory, name, held, prefix)
		}
		exhausted = held
		exhaustedName = name
		return quarantineDirectoryEntryNamedWith(directory, name, held, prefix,
			func(int) string { return "fixed-collision" },
			func(*os.File, string, *os.File, string, *os.File, func()) (*os.File, error) {
				attempts++
				return nil, unix.EEXIST
			})
	}
	name, claimed, err := claimRetiredStageDirectoryWith(parent, ".wb-stage-", ".wb-retired-stage-", retiredStageClaimOps{empty: directoryEmpty, claim: claim})
	if err != nil || !claimed || attempts != 16 || !strings.HasPrefix(name, ".wb-stage-") || exhaustedName == reclaimedName || reclaimedName == "" {
		t.Fatalf("claim sibling after 16 collisions = %q, %t, %v; attempts=%d exhausted=%q reclaimed=%q", name, claimed, err, attempts, exhaustedName, reclaimedName)
	}
	assertRetiredStageDescriptorClosed(t, exhausted)
	if info, err := os.Stat(filepath.Join(root, exhaustedName)); err != nil || !info.IsDir() {
		t.Fatalf("collision-exhausted retirement changed: %v, %v", info, err)
	}
	if _, err := os.Lstat(filepath.Join(root, reclaimedName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reusable sibling remains retired: %v", err)
	}
}

func TestE2ERetiredStageClaimPreservesPartialMoveAndReportsIdentityChange(t *testing.T) {
	t.Parallel()
	const retiredName = ".wb-retired-stage-identity"
	root, parent := newRetiredStageClaimFixture(t, retiredName)
	var held, moved *os.File
	var published string
	_, claimed, err := claimRetiredStageDirectoryWith(parent, ".wb-stage-", ".wb-retired-stage-", retiredStageClaimOps{
		empty: directoryEmpty,
		claim: func(directory *os.File, name string, retired *os.File, prefix string) (*os.File, string, error) {
			held = retired
			var moveErr error
			moved, published, moveErr = quarantineDirectoryEntryNamed(directory, name, retired, prefix)
			if moveErr != nil {
				return moved, published, moveErr
			}
			return moved, published, errDirectoryMoveIdentityChanged
		},
	})
	if claimed || !errors.Is(err, errDirectoryMoveIdentityChanged) || !strings.Contains(err.Error(), "reclaim retired staging directory "+retiredName) {
		t.Fatalf("partial-move identity refusal = %t, %v", claimed, err)
	}
	assertRetiredStageDescriptorClosed(t, held)
	assertRetiredStageDescriptorClosed(t, moved)
	if _, err := os.Lstat(filepath.Join(root, retiredName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("already-moved source returned: %v", err)
	}
	if info, err := os.Stat(filepath.Join(root, published)); err != nil || !info.IsDir() {
		t.Fatalf("retained partial publication lost: %v, %v", info, err)
	}
}

func TestE2ERetiredStageClaimRejectsUnavailableParent(t *testing.T) {
	t.Parallel()
	root, closed := newRetiredStageClaimFixture(t, ".wb-retired-stage-closed")
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := claimRetiredStageDirectory(closed, ".wb-stage-", ".wb-retired-stage-"); claimed || err == nil || !strings.Contains(err.Error(), "rewind secure staging parent") {
		t.Fatalf("closed parent refusal = %t, %v", claimed, err)
	}
	regularPath := filepath.Join(root, "regular")
	if err := os.WriteFile(regularPath, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	regular, err := os.Open(regularPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = regular.Close() })
	if _, claimed, err := claimRetiredStageDirectory(regular, ".wb-stage-", ".wb-retired-stage-"); claimed || err == nil || !strings.Contains(err.Error(), "read secure staging parent") {
		t.Fatalf("non-directory parent refusal = %t, %v", claimed, err)
	}
}

func TestE2EStageQuarantinePreservesUnmatchedAndRedirectedEntries(t *testing.T) {
	t.Parallel()
	root, parent := newRetiredStageClaimFixture(t, ".wb-stage-held", ".wb-stage-sibling")
	held, err := os.Open(filepath.Join(root, ".wb-stage-held"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })
	heldInfo, err := held.Stat()
	if err != nil {
		t.Fatal(err)
	}
	sibling := filepath.Join(root, ".wb-stage-sibling")
	if err := os.WriteFile(filepath.Join(sibling, "marker"), []byte("sibling"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sibling, filepath.Join(root, ".wb-stage-link")); err != nil {
		t.Fatal(err)
	}
	wrong, err := secureDirectoryIdentityAt(int(parent.Fd()), ".wb-stage-sibling")
	if err != nil {
		t.Fatal(err)
	}
	if err := quarantineStageDirectoryAt(parent, ".wb-stage-held", wrong); !errors.Is(err, errDirectoryMoveIdentityChanged) {
		t.Fatalf("wrong held-stage identity was accepted: %v", err)
	}
	if err := quarantineStageDirectoryAt(parent, ".wb-stage-link", wrong); err == nil {
		t.Fatal("symlinked stage was accepted")
	}
	if err := quarantineMatchingStageDirectoryAt(parent, held); err != nil {
		t.Fatalf("matching held-stage retirement: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, ".wb-stage-held")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("matched active stage remains: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	retiredMatches := 0
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".wb-retired-stage-") {
			continue
		}
		retiredInfo, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		if os.SameFile(heldInfo, retiredInfo) {
			retiredMatches++
		}
	}
	if retiredMatches != 1 {
		t.Fatalf("retired directory matched held stage %d times, want exactly once", retiredMatches)
	}
	if got, err := os.ReadFile(filepath.Join(sibling, "marker")); err != nil || string(got) != "sibling" {
		t.Fatalf("unmatched sibling changed: %q, %v", got, err)
	}
	if got, err := os.Readlink(filepath.Join(root, ".wb-stage-link")); err != nil || got != sibling {
		t.Fatalf("redirecting entry changed: %q, %v", got, err)
	}
}

func TestE2EStageQuarantineRejectsUnavailableParent(t *testing.T) {
	t.Parallel()
	root, parent := newRetiredStageClaimFixture(t, ".wb-stage-held")
	held, err := os.Open(filepath.Join(root, ".wb-stage-held"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })
	identity, err := secureDirectoryIdentityAt(int(parent.Fd()), ".wb-stage-held")
	if err != nil {
		t.Fatal(err)
	}
	if err := parent.Close(); err != nil {
		t.Fatal(err)
	}
	if err := quarantineStageDirectoryByIdentityAt(parent, identity); err == nil || !strings.Contains(err.Error(), "rewind secure staging parent") {
		t.Fatalf("identity quarantine with closed parent = %v", err)
	}
	if err := quarantineMatchingStageDirectoryAt(parent, held); err == nil || !strings.Contains(err.Error(), "rewind secure staging parent") {
		t.Fatalf("matching quarantine with closed parent = %v", err)
	}
	regularPath := filepath.Join(root, "regular")
	if err := os.WriteFile(regularPath, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	regular, err := os.Open(regularPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = regular.Close() })
	if err := quarantineStageDirectoryByIdentityAt(regular, identity); err == nil || !strings.Contains(err.Error(), "read secure staging parent") {
		t.Fatalf("identity quarantine with non-directory parent = %v", err)
	}
	if err := quarantineMatchingStageDirectoryAt(regular, held); err == nil || !strings.Contains(err.Error(), "read secure staging parent") {
		t.Fatalf("matching quarantine with non-directory parent = %v", err)
	}
	if info, err := os.Stat(filepath.Join(root, ".wb-stage-held")); err != nil || !info.IsDir() {
		t.Fatalf("refused stage changed: %v, %v", info, err)
	}
}
