package orchestrate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	unix "github.com/sneat-dev/wb/internal/unixcompat"
	"github.com/sneat-dev/wb/internal/worktreesecure"
)

// maxRetiredPublicationEvidenceBytes bounds every receipt and acknowledgement
// byte snapshot this flow decodes. The limit is enforced on the actual read,
// not only on a stat that a writer can invalidate before the read.
const maxRetiredPublicationEvidenceBytes = 1 << 20

// publicationNamespace binds one evidence path's lexical parent to the exact
// directory descriptor admitted for it. The descriptor is the only authority
// for staging, publication, directory syncs and collision rereads; every one
// of those boundaries re-corroborates that the lexical path still names the
// held directory, so a parent or ancestor swap can never turn a detached
// directory into a successful acknowledgement.
type publicationNamespace struct {
	path      string
	directory *os.File
}

// openPublicationNamespace opens path's parent without following repository
// symlinks and admits it only while the lexical parent still resolves to that
// same descriptor.
func openPublicationNamespace(path string, create bool) (*publicationNamespace, error) {
	directoryPath := filepath.Dir(path)
	if create {
		if err := os.MkdirAll(directoryPath, 0o700); err != nil {
			return nil, err
		}
	}
	directory, err := worktreesecure.OpenAbsoluteDirectoryNoFollow(directoryPath, false)
	if err != nil {
		return nil, err
	}
	namespace := &publicationNamespace{path: directoryPath, directory: directory}
	if err := namespace.verify(); err != nil {
		_ = directory.Close()
		return nil, err
	}
	return namespace, nil
}

// verify refuses a namespace whose bound path no longer names the held
// directory. It is checked before and after every durable boundary.
func (namespace *publicationNamespace) verify() error {
	if namespace == nil || namespace.directory == nil {
		return fmt.Errorf("evidence parent namespace is unavailable")
	}
	if !secureLexicalDirectoryStillMatches(namespace.path, namespace.directory) {
		return fmt.Errorf("evidence parent namespace %s no longer holds the admitted directory", namespace.path)
	}
	return nil
}

// secureLexicalDirectoryStillMatches re-corroborates one lexical directory
// path against a held descriptor through the same checked no-follow component
// walk that admitted it. Comparing directory identity alone is not enough:
// os.Lstat traverses intermediate path components, so an ancestor that became
// a symlink still resolves to the same final inode and the bound evidence
// would be undiscoverable through the supported no-follow reader. Re-walking
// every component without following any of them refuses such an ancestor even
// when the final directory is byte-for-byte the same held inode.
//
// The observation is validation only. It never becomes the authority for a
// mutation, a sync or a read: the caller keeps publishing and reading through
// the originally admitted descriptor.
func secureLexicalDirectoryStillMatches(path string, directory *os.File) bool {
	if directory == nil {
		return false
	}
	observed, err := worktreesecure.OpenAbsoluteDirectoryNoFollow(path, false)
	if err != nil {
		return false
	}
	defer func() { _ = observed.Close() }()
	held, heldErr := directory.Stat()
	actual, actualErr := observed.Stat()
	return heldErr == nil && actualErr == nil && os.SameFile(held, actual)
}

// Close releases the held namespace descriptor.
func (namespace *publicationNamespace) Close() error {
	if namespace == nil || namespace.directory == nil {
		return nil
	}
	return namespace.directory.Close()
}

// secureEvidenceReadOps are one bounded evidence read's durable boundaries,
// scoped so every refusal branch is testable without package globals.
type secureEvidenceReadOps struct {
	openParent   func(string) (*os.File, error)
	openFile     func(*os.File, string) (*os.File, error)
	stat         func(*os.File) (os.FileInfo, error)
	read         func(io.Reader) ([]byte, error)
	stillMatches func(string, *os.File) bool
}

func productionSecureEvidenceReadOps() secureEvidenceReadOps {
	return secureEvidenceReadOps{
		openParent: func(path string) (*os.File, error) {
			return worktreesecure.OpenAbsoluteDirectoryNoFollow(path, false)
		},
		openFile: func(parent *os.File, name string) (*os.File, error) {
			fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW, 0)
			if err != nil {
				return nil, err
			}
			return os.NewFile(uintptr(fd), name), nil
		},
		stat:         (*os.File).Stat,
		read:         io.ReadAll,
		stillMatches: secureLexicalDirectoryStillMatches,
	}
}

// readSecureBoundedRegularFile reads one regular file through its held parent
// directory and a no-follow descriptor. Symlinks, non-regular replacements,
// oversize files and parent-directory swaps refuse instead of being followed:
// the parent pathname is corroborated both after admission and after the read
// by re-walking every one of its components without following any of them, so
// returned bytes can never describe a detached namespace and an intermediate
// ancestor that became a symlink refuses even when the final directory is
// unchanged.
func readSecureBoundedRegularFile(path string, max int64) ([]byte, error) {
	return readSecureBoundedRegularFileWith(path, max, productionSecureEvidenceReadOps())
}

func readSecureBoundedRegularFileWith(path string, max int64, ops secureEvidenceReadOps) ([]byte, error) {
	parent, err := ops.openParent(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer func() { _ = parent.Close() }()
	if err := verifyReadNamespace(path, parent, ops); err != nil {
		return nil, err
	}
	body, err := readBoundedRegularFileAt(path, parent, max, ops)
	if err != nil {
		return nil, err
	}
	if err := verifyReadNamespace(path, parent, ops); err != nil {
		return nil, err
	}
	return body, nil
}

func verifyReadNamespace(path string, parent *os.File, ops secureEvidenceReadOps) error {
	if ops.stillMatches != nil && !ops.stillMatches(filepath.Dir(path), parent) {
		return fmt.Errorf("%s parent namespace changed while reading evidence", path)
	}
	return nil
}

// readSecureBoundedRegularFileInNamespace reads one evidence file through the
// exact namespace admitted for the acknowledgement, refusing when that
// namespace no longer matches its bound path before or after the read.
func readSecureBoundedRegularFileInNamespace(path string, max int64, namespace *publicationNamespace) ([]byte, error) {
	return readSecureBoundedRegularFileInNamespaceWith(path, max, namespace, productionSecureEvidenceReadOps())
}

func readSecureBoundedRegularFileInNamespaceWith(path string, max int64, namespace *publicationNamespace, ops secureEvidenceReadOps) ([]byte, error) {
	if err := namespace.verify(); err != nil {
		return nil, err
	}
	body, err := readBoundedRegularFileAt(path, namespace.directory, max, ops)
	if err != nil {
		return nil, err
	}
	if err := namespace.verify(); err != nil {
		return nil, err
	}
	return body, nil
}

func readBoundedRegularFileAt(path string, parent *os.File, max int64, ops secureEvidenceReadOps) ([]byte, error) {
	file, err := ops.openFile(parent, filepath.Base(path))
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := ops.stat(file)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > max {
		return nil, fmt.Errorf("%s is not one bounded regular evidence file", path)
	}
	body, err := ops.read(io.LimitReader(file, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > max {
		return nil, fmt.Errorf("%s exceeds the bounded evidence size", path)
	}
	return body, nil
}

func secureEvidenceSHA256(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}
