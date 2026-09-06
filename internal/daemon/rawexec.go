package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
)

const RawExecutionPolicyVersion = 1

type rawExecutionPolicy struct {
	Version                 int  `json:"version"`
	AllowRawDaemonExecution bool `json:"allow_raw_daemon_execution"`
}

func RawExecutionPolicyPath() (string, error) {
	account, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("resolve current OS account for daemon policy: %w", err)
	}
	if account.HomeDir == "" {
		return "", errors.New("current OS account has no home directory for daemon policy")
	}
	return filepath.Join(account.HomeDir, ".config", "wb", "daemon-raw-exec.json"), nil
}

// LoadRawExecutionPolicy enables raw subprocesses only through a protected,
// explicit administrator opt-in outside the agent-writable projects tree.
func LoadRawExecutionPolicy(path, projectsRoot string) (bool, error) {
	if path == "" {
		var err error
		path, err = RawExecutionPolicyPath()
		if err != nil {
			return false, err
		}
	}
	resolvedDirectory, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("resolve daemon raw-execution policy directory: %w", err)
	}
	resolvedPath := filepath.Join(resolvedDirectory, filepath.Base(path))
	inside, err := pathWithin(projectsRoot, resolvedPath)
	if err != nil {
		return false, err
	}
	if inside {
		return false, fmt.Errorf("daemon raw-execution policy must be outside projects root %s", projectsRoot)
	}
	info, err := os.Lstat(resolvedPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect daemon raw-execution policy: %w", err)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("daemon raw-execution policy must be a regular file: %s", resolvedPath)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		return false, fmt.Errorf("daemon raw-execution policy permissions are %o, want 600: %s", info.Mode().Perm(), resolvedPath)
	}
	file, err := os.Open(resolvedPath)
	if err != nil {
		return false, fmt.Errorf("open daemon raw-execution policy: %w", err)
	}
	defer func() { _ = file.Close() }()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var policy rawExecutionPolicy
	if err := decoder.Decode(&policy); err != nil {
		return false, fmt.Errorf("decode daemon raw-execution policy: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return false, errors.New("decode daemon raw-execution policy: multiple JSON values")
		}
		return false, fmt.Errorf("decode daemon raw-execution policy trailing content: %w", err)
	}
	if policy.Version != RawExecutionPolicyVersion {
		return false, fmt.Errorf("unsupported daemon raw-execution policy version %d", policy.Version)
	}
	return policy.AllowRawDaemonExecution, nil
}

// RequireRawExecutionPolicy re-reads and validates the protected host policy.
// Callers must invoke it at every execution boundary so revocation is immediate.
func RequireRawExecutionPolicy(path, projectsRoot string) error {
	allowed, err := LoadRawExecutionPolicy(path, projectsRoot)
	if err != nil {
		return err
	}
	if !allowed {
		return errors.New("raw daemon execution is disabled; administrator opt-in is required")
	}
	return nil
}

func pathWithin(root, candidate string) (bool, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return false, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return false, err
	}
	candidate, err = filepath.Abs(candidate)
	if err != nil {
		return false, err
	}
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false, err
	}
	return relative == "." || (relative != ".." && !filepath.IsAbs(relative) && !strings.HasPrefix(relative, ".."+string(filepath.Separator))), nil
}
