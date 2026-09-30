package gitcli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os/exec"
)

// SHA256Object streams exact Git object bytes through a SHA-256 hash. Reading
// finishes before Wait closes StdoutPipe, and a pipe failure never starts Git.
func SHA256Object(ctx context.Context, directory, object string) (string, error) {
	command := exec.CommandContext(ctx, "git", "-C", directory, "show", object)
	return sha256ObjectWithCommand(command)
}

type objectCommand interface {
	StdoutPipe() (io.ReadCloser, error)
	Start() error
	Wait() error
}

func sha256ObjectWithCommand(command objectCommand) (string, error) {
	pipe, err := command.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := command.Start(); err != nil {
		return "", err
	}
	return hashObjectAndWait(pipe, command.Wait)
}

func hashObjectAndWait(reader io.Reader, wait func() error) (string, error) {
	digest, copyErr := hashObject(reader)
	waitErr := wait()
	if copyErr != nil {
		return "", copyErr
	}
	if waitErr != nil {
		return "", waitErr
	}
	return digest, nil
}

func hashObject(reader io.Reader) (string, error) {
	hash := sha256.New()
	if _, err := io.Copy(hash, reader); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// ReadObjectBytes preserves the command's raw stdout, including NUL bytes.
func ReadObjectBytes(ctx context.Context, directory string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", directory}, args...)...)
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("read archive Git object: %w", err)
	}
	return output, nil
}
