package daemon

import (
	"encoding/hex"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

func TestRawPolicyPathRefusesUnavailableAccountIdentity(t *testing.T) {
	t.Parallel()
	failure := errors.New("account lookup failed")
	for _, scenario := range []string{"lookup", "missing home"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			path, err := rawExecutionPolicyPathWithCurrent(func() (*user.User, error) {
				if scenario == "lookup" {
					return nil, failure
				}
				return &user.User{}, nil
			})
			if path != "" || err == nil {
				t.Fatalf("policy path=%q,%v", path, err)
			}
			if scenario == "lookup" && !errors.Is(err, failure) {
				t.Fatalf("lookup cause lost: %v", err)
			}
			if scenario == "missing home" && !strings.Contains(err.Error(), "no home directory") {
				t.Fatalf("home error=%v", err)
			}
		})
	}
}

func TestRawPolicyLoaderRefusesDefaultPathFailure(t *testing.T) {
	t.Parallel()
	failure := errors.New("account policy path unavailable")
	allowed, err := loadRawExecutionPolicyWithIO("", t.TempDir(), func() (string, error) { return "", failure }, os.Open)
	if allowed || !errors.Is(err, failure) {
		t.Fatalf("policy=%t,%v", allowed, err)
	}
}

func TestRawPolicyLoaderRefusesPolicyRemovedAfterInspection(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "policy.json")
	writeRawExecutionPolicy(t, path, []byte(enabledRawExecutionPolicy), 0o600)
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	allowed, err := loadRawExecutionPolicyWithIO(path, t.TempDir(), RawExecutionPolicyPath, func(resolved string) (*os.File, error) {
		calls++
		if resolved != resolvedPath {
			t.Fatalf("opened unexpected policy %q", resolved)
		}
		if err := os.Remove(resolved); err != nil {
			t.Fatal(err)
		}
		return os.Open(resolved)
	})
	if allowed || !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), "open daemon raw-execution policy") || calls != 1 {
		t.Fatalf("policy=%t,%v calls=%d", allowed, err, calls)
	}
}

func TestPolicyContainmentPropagatesPathResolutionFailures(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"root absolute", "candidate absolute", "relative"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			failure := errors.New("path resolution failed")
			calls, injected := 0, 0
			absolute := func(path string) (string, error) {
				calls++
				if phase == "root absolute" && calls == 1 || phase == "candidate absolute" && calls == 2 {
					injected++
					return "", failure
				}
				return filepath.Abs(path)
			}
			relative := func(root, candidate string) (string, error) {
				if phase == "relative" {
					injected++
					return "", failure
				}
				return filepath.Rel(root, candidate)
			}
			inside, err := pathWithinWithPaths(root, filepath.Join(root, "candidate"), absolute, relative)
			if inside || !errors.Is(err, failure) || injected != 1 {
				t.Fatalf("containment=%t,%v injections=%d", inside, err, injected)
			}
		})
	}
}

func TestRuntimePathsPropagateInvalidProjectsRoot(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "invalid\x00root")
	for name, resolve := range map[string]func(string) (string, error){"state": StatePath, "operations": OperationsDir, "socket": SocketPath} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if path, err := resolve(root); err == nil || path != "" {
				t.Fatalf("runtime path=%q,%v", path, err)
			}
		})
	}
}

func TestDaemonIDsRetainPrefixAndSecureHexShape(t *testing.T) {
	t.Parallel()
	for _, prefix := range []string{"wbg-", "wbo-", "wbwg-", "wbwl-"} {
		id := randomID(prefix)
		bytes, err := hex.DecodeString(strings.TrimPrefix(id, prefix))
		if !strings.HasPrefix(id, prefix) || len(bytes) != 16 || err != nil {
			t.Fatalf("ID=%q decoded=%d,%v", id, len(bytes), err)
		}
	}
}
