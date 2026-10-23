package credentialfile

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sneat-dev/wb/internal/filewrite"
)

func ReadToken(in io.Reader) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(in, 16<<10+1))
	if err != nil {
		return "", fmt.Errorf("read machine credential from stdin: %w", err)
	}
	if len(raw) > 16<<10 {
		return "", errors.New("machine credential from stdin exceeds 16384 bytes")
	}
	token := strings.TrimSpace(string(raw))
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return "", errors.New("machine credential from stdin must contain one non-empty token")
	}
	return token, nil
}
func WritePrivate(path, token string) (bool, error) {
	return writePrivateInjected(path, token, nil)
}
func writePrivateInjected(path, token string, inj *filewrite.Injector) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, fmt.Errorf("create credential directory: %w", err)
	}
	if existing, err := os.ReadFile(path); err == nil {
		if strings.TrimSpace(string(existing)) != token {
			return false, fmt.Errorf("credential file %s already exists with different contents; choose a new --token-file", path)
		}
		if err := filewrite.ChmodPath(path, 0o600, inj); err != nil {
			return false, fmt.Errorf("protect credential file: %w", err)
		}
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("inspect credential file: %w", err)
	}
	file, err := filewrite.CreateExclusivePath(path, 0o600, inj)
	if err != nil {
		return false, fmt.Errorf("create credential file: %w", err)
	}
	if err := filewrite.Write(file, []byte(token+"\n"), path, inj); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return false, fmt.Errorf("write credential file: %w", err)
	}
	if err := filewrite.Sync(file, path, inj); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return false, fmt.Errorf("sync credential file: %w", err)
	}
	if err := filewrite.Close(file, path, inj); err != nil {
		_ = os.Remove(path)
		return false, fmt.Errorf("close credential file: %w", err)
	}
	return true, nil
}
