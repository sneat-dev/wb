package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/filewrite"
)

func TestRemoteEnrollKeepsCredentialOutOfOutputAndConfig(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config", "wb.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("parallel: 3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	const token = "one-time-opaque-token"
	verified := false
	restarted := false
	deps := remoteEnrollDeps{
		configPath: func() string { return configPath },
		verify: func(_ context.Context, hubURL, machine, got string) error {
			verified = hubURL == defaultRemoteHubURL && machine == "studio-mac" && got == token
			return nil
		},
		restart: func(context.Context, string) error { restarted = true; return nil },
	}
	var stdout bytes.Buffer
	if err := runRemoteEnroll(context.Background(), deps, root, "studio-mac", defaultRemoteHubURL, "", true, true, true, strings.NewReader(token+"\n"), &stdout); err != nil {
		t.Fatal(err)
	}
	if !verified || !restarted {
		t.Fatalf("verified=%t restarted=%t", verified, restarted)
	}
	if strings.Contains(stdout.String(), token) {
		t.Fatal("credential leaked to stdout")
	}
	var result remoteEnrollResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	credential, err := os.ReadFile(result.TokenFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(credential)) != token {
		t.Fatal("private credential file does not contain the supplied token")
	}
	config, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(config), token) || !strings.Contains(string(config), "parallel: 3") {
		t.Fatalf("config leaked token or lost unrelated settings:\n%s", config)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(result.TokenFile)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("credential mode = %o, want 600", info.Mode().Perm())
		}
	}
}

func TestRemoteEnrollRequiresExplicitStdinAndDoesNotPersistUnverifiedToken(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "wb.yaml")
	deps := remoteEnrollDeps{
		configPath: func() string { return configPath },
		verify:     func(context.Context, string, string, string) error { return context.Canceled },
		restart:    func(context.Context, string) error { t.Fatal("restart called"); return nil },
	}
	if err := runRemoteEnroll(context.Background(), deps, root, "vm", defaultRemoteHubURL, "", false, false, false, strings.NewReader("secret"), ioDiscard{}); err == nil || !strings.Contains(err.Error(), "--token-stdin") {
		t.Fatalf("missing explicit stdin error = %v", err)
	}
	if err := runRemoteEnroll(context.Background(), deps, root, "vm", defaultRemoteHubURL, "", true, false, false, strings.NewReader("secret"), ioDiscard{}); err == nil || !strings.Contains(err.Error(), "verify hub credential") {
		t.Fatalf("verification error = %v", err)
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("config exists after failed verification: %v", err)
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(payload []byte) (int, error) { return len(payload), nil }

// The following tests exercise writePrivateCredentialInjected's
// filewrite.Injector-reachable error branches (task-9 PR-2): the create
// path's happy case is already covered above, but reaching a create,
// write, sync, close, or the pre-existing-file chmod failure
// deterministically needs the injector.

func TestWritePrivateCredentialInjectedHonoursAnInjectedWriteFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential")
	inj := &filewrite.Injector{Step: filewrite.StepWrite, Err: errBoomForCmdWB}
	if _, err := writePrivateCredentialInjected(path, "secret", inj); !errors.Is(err, errBoomForCmdWB) {
		t.Fatalf("writePrivateCredentialInjected error = %v", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("credential file not cleaned up after injected write failure: %v", err)
	}
}

func TestWritePrivateCredentialInjectedHonoursAnInjectedSyncFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential")
	inj := &filewrite.Injector{Step: filewrite.StepSync, Err: errBoomForCmdWB}
	if _, err := writePrivateCredentialInjected(path, "secret", inj); !errors.Is(err, errBoomForCmdWB) {
		t.Fatalf("writePrivateCredentialInjected error = %v", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("credential file not cleaned up after injected sync failure: %v", err)
	}
}

func TestWritePrivateCredentialInjectedHonoursAnInjectedCloseFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential")
	inj := &filewrite.Injector{Step: filewrite.StepClose, Err: errBoomForCmdWB}
	if _, err := writePrivateCredentialInjected(path, "secret", inj); !errors.Is(err, errBoomForCmdWB) {
		t.Fatalf("writePrivateCredentialInjected error = %v", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("credential file not cleaned up after injected close failure: %v", err)
	}
}

func TestWritePrivateCredentialInjectedHonoursAnInjectedChmodFailureOnAnExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential")
	if err := os.WriteFile(path, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inj := &filewrite.Injector{Step: filewrite.StepChmod, Err: errBoomForCmdWB}
	if _, err := writePrivateCredentialInjected(path, "secret", inj); !errors.Is(err, errBoomForCmdWB) {
		t.Fatalf("writePrivateCredentialInjected error = %v", err)
	}
}
