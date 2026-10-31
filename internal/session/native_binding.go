package session

import (
	"crypto/sha256"
	"github.com/sneat-dev/wb/internal/sessionauthority"
	"io"
	"os"
	"path/filepath"
	"reflect"
)

// ModelEnrichmentCompatible permits observational metadata to become known
// only when the launcher left the model unspecified. A requested known model
// remains exact, and current-chat registry transitions retain full snapshots.
func ModelEnrichmentCompatible(expected, current string) bool {
	return sessionauthority.ModelEnrichmentCompatible(expected, current)
}

// ExecutableIdentityMatches permits aliases and byte-identical official copies,
// never a version-label assertion. Hashes stream through stable regular-file
// descriptors, with a fixed size cap; the common same-inode path does no hash.
func ExecutableIdentityMatches(expected, current string) bool {
	if !filepath.IsAbs(expected) || filepath.Clean(expected) != expected || !filepath.IsAbs(current) || filepath.Clean(current) != current {
		return false
	}
	first, err := os.Open(expected)
	if err != nil {
		return false
	}
	defer func() { _ = first.Close() }()
	second, err := os.Open(current)
	if err != nil {
		return false
	}
	defer func() { _ = second.Close() }()
	a, err := first.Stat()
	if err != nil {
		return false
	}
	b, err := second.Stat()
	if err != nil {
		return false
	}
	const limit = 128 << 20
	if !a.Mode().IsRegular() || !b.Mode().IsRegular() || a.Mode().Perm()&0o111 == 0 || b.Mode().Perm()&0o111 == 0 || a.Size() <= 0 || a.Size() > limit || a.Size() != b.Size() {
		return false
	}
	if os.SameFile(a, b) {
		return true
	}
	digest := func(file *os.File, before os.FileInfo) ([32]byte, bool) {
		hash := sha256.New()
		count, err := io.Copy(hash, io.LimitReader(file, limit+1))
		if err != nil || count != before.Size() {
			return [32]byte{}, false
		}
		after, err := file.Stat()
		if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
			return [32]byte{}, false
		}
		var value [32]byte
		copy(value[:], hash.Sum(nil))
		return value, true
	}
	ah, ok := digest(first, a)
	if !ok {
		return false
	}
	bh, ok := digest(second, b)
	return ok && ah == bh
}

func nativeBindingCompatible(expected, current Record, native string) bool {
	oldNative, err := effectiveRecordNative(expected)
	if err != nil {
		return false
	}
	currentNative, err := effectiveRecordNative(current)
	if err != nil {
		return false
	}
	if oldNative != "" && oldNative != native || currentNative != "" && currentNative != native {
		return false
	}
	if !ModelEnrichmentCompatible(expected.Model, current.Model) {
		return false
	}
	if expected.WBPath != current.WBPath && !ExecutableIdentityMatches(expected.WBPath, current.WBPath) {
		return false
	}
	normalized := expected
	normalized.Model = current.Model
	normalized.NativeHarnessID = current.NativeHarnessID
	normalized.AgentID = current.AgentID
	normalized.WBPath = current.WBPath
	normalized.WBVersion = current.WBVersion
	return reflect.DeepEqual(normalized, current)
}
