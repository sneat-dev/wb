// Package nodeidentity resolves the stable node ID every WB daemon carries,
// per spec/features/peer-connectivity#req:node-identity.
//
// The ID is 128 random bits, hex-encoded, generated once on first start (or
// on first `wb peers join`) and stored under the private WB state directory
// at <state dir>/state/node-id, mode 0600. It is a display and matching
// identifier, not a secret: renaming the host changes nothing, and a copied
// state directory yields two nodes sharing one ID, which
// single-live-session detects on the hub side.
package nodeidentity

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sneat-dev/wb/internal/wbhome"
)

// byteLength is 128 bits.
const byteLength = 16

// relativePath is where the node ID lives inside the private state directory
// resolved by internal/wbhome, matching the feature's exact path.
const relativePath = "state/node-id"

// Path returns the node ID file's location for a given projects root, without
// creating anything.
func Path(projectsRoot string) (string, error) {
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		return "", err
	}
	return PathFromHome(home), nil
}

// PathFromHome returns the node ID file's location for a WB home directory
// that is already resolved (internal/wbhome.Root's return value), for a
// caller that resolved it once already and must not resolve the same
// projects root a second time through a second, independent call — see
// cmd/wb's daemon serve, which reuses its own already-resolved home this way
// rather than calling Load(projectsRoot, nil) directly.
func PathFromHome(home string) string {
	return filepath.Join(home, filepath.FromSlash(relativePath))
}

// Load resolves the node ID for a projects root, creating it (and its
// directory) on first use. random defaults to crypto/rand.Reader; a test may
// inject a deterministic or fault-injecting reader.
func Load(projectsRoot string, random io.Reader) (string, error) {
	path, err := Path(projectsRoot)
	if err != nil {
		return "", err
	}
	return LoadFile(path, random)
}

// LoadFile is Load with the file path already resolved, so a caller that
// already knows the WB home (or a test fixture) does not need a second
// resolution.
func LoadFile(path string, random io.Reader) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("node identity path is empty")
	}
	if existing, err := readNodeID(path); err == nil {
		return existing, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if random == nil {
		random = rand.Reader
	}
	raw := make([]byte, byteLength)
	if _, err := io.ReadFull(random, raw); err != nil {
		return "", fmt.Errorf("generate node ID: %w", err)
	}
	id := hex.EncodeToString(raw)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create node identity directory: %w", err)
	}
	tempPath, err := writeNodeIDTempFile(dir, id)
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(tempPath) }()
	// Publish atomically and exclusively with a hard link: two racing
	// first-starts (a daemon and a concurrent `wb peers join`) must not
	// overwrite one another's generated ID, and a crash between opening and
	// writing the destination file must never leave a partially written
	// node-id in place (an O_CREATE|O_EXCL write directly to path could).
	// link(2) fails with EEXIST if path already exists, whether that is a
	// completed publish from a winning racer or a stale plain file, so
	// exactly one writer's ID is ever published and every loser reads the
	// winner's file back instead.
	if err := os.Link(tempPath, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return readNodeID(path)
		}
		return "", fmt.Errorf("publish node identity file: %w", err)
	}
	return id, nil
}

// writeNodeIDTempFile writes id to a private, fsynced temporary file in dir
// (the node-id file's own directory, so the later os.Link stays on one
// filesystem) and returns its path. The caller publishes it with os.Link and
// always removes the temp name afterward, whether or not the link won the
// race.
func writeNodeIDTempFile(dir, id string) (string, error) {
	file, err := os.CreateTemp(dir, ".node-id-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create node identity temp file: %w", err)
	}
	path := file.Name()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("protect node identity temp file: %w", err)
	}
	if _, err := io.WriteString(file, id+"\n"); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("write node identity temp file: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("sync node identity temp file: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close node identity temp file: %w", err)
	}
	return path, nil
}

func readNodeID(path string) (string, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // private, WB-owned state path.
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(raw))
	if id == "" {
		return "", fmt.Errorf("node identity file %s is empty; delete it to generate a new one", path)
	}
	if len(id) != byteLength*2 {
		return "", fmt.Errorf("node identity file %s does not hold a %d-byte hex ID; delete it to generate a new one", path, byteLength)
	}
	if _, err := hex.DecodeString(id); err != nil {
		return "", fmt.Errorf("node identity file %s is not valid hex; delete it to generate a new one: %w", path, err)
	}
	return id, nil
}
