package peersrun

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/wbhome"
)

type peerUpstreamState struct {
	Blocked bool `json:"blocked"`
}

func refuseExistingTokenFile(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("token file %s already exists; refusing to overwrite it", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("check token file %s: %w", path, err)
	}
	return nil
}
func writeOneTimeToken(path, token string) error {
	return writeOneTimeTokenInjected(path, token, nil)
}
func writeOneTimeTokenInjected(path, token string, inj *filewrite.Injector) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create token directory: %w", err)
	}
	file, err := filewrite.CreateExclusivePath(path, 0o600, inj)
	if err != nil {
		return fmt.Errorf("create token file: %w", err)
	}
	if err := filewrite.Write(file, []byte(token+"\n"), path, inj); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return fmt.Errorf("write token file: %w", err)
	}
	if err := filewrite.Close(file, path, inj); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("close token file: %w", err)
	}
	return nil
}
func peerUpstreamStatePath(projectsRoot string) (string, error) {
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "state", "peer-upstream.json"), nil
}
func loadPeerUpstreamState(path string) (peerUpstreamState, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // private, WB-owned state path.
	if errors.Is(err, os.ErrNotExist) {
		return peerUpstreamState{}, nil
	}
	if err != nil {
		return peerUpstreamState{}, fmt.Errorf("read upstream peer state: %w", err)
	}
	var state peerUpstreamState
	if err := json.Unmarshal(raw, &state); err != nil {
		return peerUpstreamState{}, fmt.Errorf("parse upstream peer state: %w", err)
	}
	return state, nil
}
func savePeerUpstreamState(path string, state peerUpstreamState) error {
	return savePeerUpstreamStateInjected(path, state, nil)
}
func savePeerUpstreamStateInjected(path string, state peerUpstreamState, inj *filewrite.Injector) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create upstream peer state directory: %w", err)
	}
	// This concrete bool-only state has no custom marshal methods and cannot fail JSON encoding.
	raw, _ := json.MarshalIndent(state, "", "  ")
	temporary, err := filewrite.CreateTemp(dir, ".peer-upstream-*.json.tmp", inj)
	if err != nil {
		return fmt.Errorf("stage upstream peer state: %w", err)
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if err := filewrite.ChmodFile(temporary, 0o600, temporaryName, inj); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("protect staged upstream peer state: %w", err)
	}
	if err := filewrite.Write(temporary, raw, temporaryName, inj); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write upstream peer state: %w", err)
	}
	if err := filewrite.Sync(temporary, temporaryName, inj); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync upstream peer state: %w", err)
	}
	if err := filewrite.Close(temporary, temporaryName, inj); err != nil {
		return fmt.Errorf("close upstream peer state: %w", err)
	}
	if err := filewrite.Rename(temporaryName, path, inj); err != nil {
		return fmt.Errorf("replace upstream peer state: %w", err)
	}
	return nil
}
func upstreamDisplayName(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || strings.TrimSpace(parsed.Hostname()) == "" {
		return rawURL
	}
	return parsed.Hostname()
}
