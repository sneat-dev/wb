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
	return filepath.Join(home, filepath.FromSlash(relativePath)), nil
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
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create node identity directory: %w", err)
	}
	// Create-exclusive: two racing first-starts (a daemon and a concurrent
	// `wb peers join`) must not overwrite one another's generated ID. The
	// loser reads back the winner's file instead.
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return readNodeID(path)
		}
		return "", fmt.Errorf("create node identity file: %w", err)
	}
	if _, err := io.WriteString(file, id+"\n"); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("write node identity file: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("sync node identity file: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close node identity file: %w", err)
	}
	return id, nil
}

func readNodeID(path string) (string, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // private, WB-owned state path.
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(raw))
	if len(id) != byteLength*2 {
		return "", fmt.Errorf("node identity file %s does not hold a %d-byte hex ID", path, byteLength)
	}
	if _, err := hex.DecodeString(id); err != nil {
		return "", fmt.Errorf("node identity file %s is not valid hex: %w", path, err)
	}
	return id, nil
}
