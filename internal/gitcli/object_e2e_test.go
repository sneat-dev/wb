//go:build e2e

package gitcli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestE2EObjectBytesAndStreamingSHAWithNativeGit(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	if output, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	body := []byte("private\x00archive\n")
	source := filepath.Join(repo, "object")
	if err := os.WriteFile(source, body, 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command("git", "-C", repo, "hash-object", "-w", source).Output()
	if err != nil {
		t.Fatal(err)
	}
	object := strings.TrimSpace(string(output))
	got, err := ReadObjectBytes(context.Background(), repo, "show", object)
	if err != nil || string(got) != string(body) {
		t.Fatalf("object bytes = (%q, %v)", got, err)
	}
	want := sha256.Sum256(body)
	if got, err := SHA256Object(context.Background(), repo, object); err != nil || got != hex.EncodeToString(want[:]) {
		t.Fatalf("object SHA = (%q, %v)", got, err)
	}
	if got, err := ReadObjectBytes(context.Background(), repo, "show", "missing-object"); err == nil || got != nil || !strings.Contains(err.Error(), "read archive Git object") {
		t.Fatalf("missing object bytes = (%q, %v)", got, err)
	}
	if got, err := SHA256Object(context.Background(), repo, "missing-object"); err == nil || got != "" {
		t.Fatalf("missing object SHA = (%q, %v)", got, err)
	}
}
