package sessionlaunch

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/session"
)

//nolint:paralleltest // slCovPrivateFixture changes the process-wide working directory.
func TestPrivateLauncherRejectsRedirectedReadyArtifactAfterRegistration(t *testing.T) {
	fx, id, deps := slCovPrivateFixture(t)
	pid := deps.pid()
	foreign := filepath.Join(t.TempDir(), "foreign-ready")
	if err := os.WriteFile(foreign, []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}
	redirect := filepath.Join(slCovAttemptDir(fx.store.Root, id), readyDirectoryName, strconv.Itoa(pid)+".json")
	deps.register = func(directory string, record session.Record) (session.Record, error) {
		registered, err := session.Register(directory, record)
		if err != nil {
			return registered, err
		}
		return registered, os.Symlink(foreign, redirect)
	}
	err := runPrivateLauncher(fx.privateArgs(id), deps)
	if err == nil {
		t.Fatal("launcher accepted a redirected ready artifact")
	}
	if !errors.Is(err, os.ErrPermission) && !strings.Contains(err.Error(), "symbolic link") && !strings.Contains(err.Error(), "too many levels") {
		t.Fatalf("unexpected ready artifact rejection: %v", err)
	}
	content, err := os.ReadFile(foreign)
	if err != nil || string(content) != "foreign" {
		t.Fatalf("foreign ready artifact changed: %q, %v", content, err)
	}
}
