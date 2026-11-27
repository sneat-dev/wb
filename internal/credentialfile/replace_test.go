package credentialfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/filewrite"
)

func TestReplacePrivateRotatesAnExistingCredential(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "credentials", "desk.token")
	if err := ReplacePrivate(path, "first"); err != nil {
		t.Fatal(err)
	}
	if err := ReplacePrivate(path, "second"); err != nil {
		t.Fatalf("a different token replaces the old one: %v", err)
	}
	raw, _ := os.ReadFile(path)
	info, _ := os.Stat(path)
	if string(raw) != "second\n" || info.Mode().Perm() != 0o600 {
		t.Fatalf("credential = %q mode %v", raw, info.Mode().Perm())
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("staging files must not remain: %v", entries)
	}
}

func TestReplacePrivateKeepsThePreviousCredentialOnFailure(t *testing.T) {
	t.Parallel()
	boom := errors.New("injected")
	for _, step := range []filewrite.Step{filewrite.StepOpenOrCreate, filewrite.StepChmod, filewrite.StepWrite,
		filewrite.StepSync, filewrite.StepClose, filewrite.StepRename} {
		t.Run(string(step), func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "desk.token")
			if err := ReplacePrivate(path, "old"); err != nil {
				t.Fatal(err)
			}
			err := replacePrivateInjected(path, "new", &filewrite.Injector{Step: step, Err: boom})
			if !errors.Is(err, boom) {
				t.Fatalf("error = %v", err)
			}
			if raw, _ := os.ReadFile(path); string(raw) != "old\n" {
				t.Fatalf("the previous credential must survive, got %q", raw)
			}
		})
	}
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ReplacePrivate(filepath.Join(blocker, "sub", "desk.token"), "x"); err == nil {
		t.Fatal("an uncreatable directory must fail")
	}
}
