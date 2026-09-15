package agents

import (
	"fmt"
	"os"
	"strings"
)

// Credential is a resolved provider credential: the environment variable name
// the harness must read it from, and the value WB injects under that name. The
// value never leaves the child's environment.
type Credential struct {
	EnvName string
	Value   string
}

// ResolveCredential reads the credential a provider names, from whichever
// source that provider configured. It fails closed with a message naming the
// exact source, because a missing credential is the single most likely reason a
// dispatch cannot start.
func ResolveCredential(provider Provider) (Credential, error) {
	if name := strings.TrimSpace(provider.CredentialEnv); name != "" {
		value, ok := os.LookupEnv(name)
		if !ok || strings.TrimSpace(value) == "" {
			return Credential{}, fmt.Errorf("provider credential %s is not set in the environment", name)
		}
		return Credential{EnvName: name, Value: value}, nil
	}
	path := strings.TrimSpace(provider.CredentialFile)
	if path == "" {
		return Credential{}, fmt.Errorf("provider names no credential source")
	}
	value, err := ReadCredentialFile(path)
	if err != nil {
		return Credential{}, err
	}
	// The harness still needs an environment variable name to read, so a
	// file-sourced credential is injected under one fixed name rather than
	// depending on how the machine happens to export anything.
	return Credential{EnvName: ProviderCredentialEnv, Value: value}, nil
}

// ReadCredentialFile reads a credential file, refusing anything that is not a
// private regular file WB itself would have written.
//
// The permission check is deliberate: a credential readable by another account
// on the machine is not a credential, and discovering that at dispatch time is
// far better than discovering it in a log.
func ReadCredentialFile(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("provider credential file %s does not exist", path)
		}
		return "", fmt.Errorf("inspect provider credential file %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		// Fail closed rather than follow: a symlink can be repointed after the
		// check, which is the same reason WB refuses a symlinked home.
		return "", fmt.Errorf("provider credential file %s is a symlink; refusing to follow it", path)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("provider credential file %s is not a regular file", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("provider credential file %s is readable by group or others (mode %04o); run: chmod 600 %s", path, info.Mode().Perm(), path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read provider credential file %s: %w", path, err)
	}
	value := strings.TrimSpace(string(raw))
	if value == "" {
		return "", fmt.Errorf("provider credential file %s is empty", path)
	}
	return value, nil
}
