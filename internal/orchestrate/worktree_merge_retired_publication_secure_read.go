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

// secureEvidenceReadOps are one bounded evidence read's durable boundaries,
// scoped so every refusal branch is testable without package globals.
type secureEvidenceReadOps struct {
	openParent func(string) (*os.File, error)
	openFile   func(*os.File, string) (*os.File, error)
	stat       func(*os.File) (os.FileInfo, error)
	read       func(io.Reader) ([]byte, error)
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
		stat: (*os.File).Stat,
		read: io.ReadAll,
	}
}

// readSecureBoundedRegularFile reads one regular file through its held parent
// directory and a no-follow descriptor. Symlinks, non-regular replacements,
// oversize files and parent-directory swaps refuse instead of being followed.
func readSecureBoundedRegularFile(path string, max int64) ([]byte, error) {
	return readSecureBoundedRegularFileWith(path, max, productionSecureEvidenceReadOps())
}

func readSecureBoundedRegularFileWith(path string, max int64, ops secureEvidenceReadOps) ([]byte, error) {
	parent, err := ops.openParent(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer func() { _ = parent.Close() }()
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
