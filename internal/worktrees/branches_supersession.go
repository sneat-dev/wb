package worktrees

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
)

func supersessionFileSHA256(path string) (string, error) {
	return fileSHA256(path)
}

func fileSHA256(path string) (digest string, resultErr error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}
