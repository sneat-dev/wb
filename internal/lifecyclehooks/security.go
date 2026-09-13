package lifecyclehooks

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sneat-dev/wb/internal/gitops"
)

func validateTrustedConfig(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("lifecycle hook configuration must be a regular non-symlink file: %s", path)
	}
	if err := validateTrustedControlFile(path, info, "lifecycle hook configuration"); err != nil {
		return nil, fmt.Errorf("lifecycle hook configuration %s: %w", path, err)
	}
	return info, nil
}

func (dispatcher Dispatcher) validateControlPaths(events []Event) error {
	controls := []struct {
		name string
		path string
	}{
		{name: "configuration", path: dispatcher.ConfigPath},
		{name: "state directory", path: dispatcher.StateDir},
		{name: "receipt", path: dispatcher.ReceiptPath},
	}
	for _, event := range events {
		checkout, err := filepath.Abs(event.Checkout)
		if err != nil {
			return fmt.Errorf("resolve checkout %s: %w", event.Checkout, err)
		}
		physicalCheckout, err := dispatcher.EvalSymlinks(checkout)
		if err != nil {
			return fmt.Errorf("resolve checkout %s: %w", event.Checkout, err)
		}
		for _, control := range controls {
			candidate, err := filepath.Abs(control.path)
			if err != nil {
				return fmt.Errorf("resolve lifecycle hook %s: %w", control.name, err)
			}
			physical, resolveErr := resolveWithMissingTail(candidate, dispatcher.EvalSymlinks)
			if resolveErr != nil {
				return fmt.Errorf("resolve lifecycle hook %s: %w", control.name, resolveErr)
			}
			if pathWithin(physicalCheckout, physical) {
				return fmt.Errorf("lifecycle hook %s must resolve outside repository checkout %s", control.name, event.Checkout)
			}
		}
	}
	return nil
}

// resolveWithMissingTail resolves symlinks in the longest existing ancestor,
// so a not-yet-created XDG state path cannot hide inside a checkout through a
// symlinked parent.
func resolveWithMissingTail(path string, eval func(string) (string, error)) (string, error) {
	candidate := filepath.Clean(path)
	var tail []string
	for {
		resolved, err := eval(candidate)
		if err == nil {
			parts := append([]string{resolved}, tail...)
			return filepath.Join(parts...), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			return path, nil
		}
		tail = append([]string{filepath.Base(candidate)}, tail...)
		candidate = parent
	}
}

func verifyCheckout(event Event) (string, os.FileInfo, error) {
	checkout, err := filepath.Abs(event.Checkout)
	if err != nil {
		return "", nil, fmt.Errorf("resolve checkout: %w", err)
	}
	physical, err := filepath.EvalSymlinks(checkout)
	if err != nil {
		return "", nil, fmt.Errorf("resolve checkout: %w", err)
	}
	info, err := os.Stat(physical)
	if err != nil {
		return "", nil, fmt.Errorf("inspect checkout: %w", err)
	}
	if !info.IsDir() {
		return "", nil, errors.New("checkout must resolve to a directory")
	}
	repository, err := RepositoryIdentity(physical)
	if err != nil {
		return "", nil, fmt.Errorf("identify checkout repository: %w", err)
	}
	if repository != strings.ToLower(strings.TrimSpace(event.Repository)) {
		return "", nil, fmt.Errorf("checkout repository changed from %s to %s", event.Repository, repository)
	}
	head, err := gitops.HeadSHA(physical)
	if err != nil {
		return "", nil, fmt.Errorf("read checkout HEAD: %w", err)
	}
	if !strings.EqualFold(head, event.NewSHA) {
		return "", nil, fmt.Errorf("checkout HEAD changed from queued %s to %s; enqueue the current revision instead", event.NewSHA, head)
	}
	return physical, info, nil
}

func validatePrivateDirectory(path, purpose string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%s must be a non-symlink directory", purpose)
	}
	return validateTrustedControlFile(path, info, purpose)
}

func ensureTrustedParent(path, purpose string) error {
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	return validatePrivateDirectory(parent, purpose)
}

func validateTrustedDataFile(path, purpose string) (os.FileInfo, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("%s must be a regular non-symlink file", purpose)
	}
	if err := validateTrustedControlFile(path, info, purpose); err != nil {
		return nil, false, err
	}
	return info, true, nil
}
