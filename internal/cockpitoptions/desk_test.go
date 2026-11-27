package cockpitoptions

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/deskpublish"
	"github.com/sneat-dev/wb/internal/deskrun"
)

const deskMachineID = "0123456789abcdef0123456789abcdef"

func deskConfig(t *testing.T, detail string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "wb.yaml")
	body := "desk:\n  url: https://desk.example\n  machine_id: " + deskMachineID + "\n  credential_file: " + filepath.Join(dir, "desk.token") + "\n  detail: " + detail + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTheDaemonPublishesToDeskOnlyWhenConnected(t *testing.T) {
	t.Parallel()
	var logs []string
	logf := func(format string, args ...any) { logs = append(logs, format) }
	random := func() *strings.Reader { return strings.NewReader(strings.Repeat("k", deskpublish.TaskKeyBytes)) }
	if got := deskPublisher(filepath.Join(t.TempDir(), "none.yaml"), "1", random(), logf, newDeskClient); got != nil {
		t.Fatal("no configuration, no publisher")
	}
	notConnected := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(notConnected, []byte("desk:\n  detail: full\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := deskPublisher(notConnected, "1", random(), logf, newDeskClient); got != nil {
		t.Fatal("an unconnected desk section, no publisher")
	}
	if len(logs) != 0 {
		t.Fatalf("an unconnected machine logs nothing: %v", logs)
	}
	malformed := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(malformed, []byte("desk:\n  detail: everything\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := deskPublisher(malformed, "1", random(), logf, newDeskClient); got != nil || len(logs) != 1 {
		t.Fatalf("a malformed section is logged and publishes nothing: %v", logs)
	}
	full := deskConfig(t, "full")
	if got := deskPublisher(full, "1", random(), logf, newDeskClient); got == nil {
		t.Fatal("a connected machine at full detail publishes")
	}
	if _, err := os.Stat(deskrun.TaskKeyPath(full)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("full detail needs no task key and creates none")
	}
	repositories := deskConfig(t, "repositories")
	if got := deskPublisher(repositories, "1", random(), logf, newDeskClient); got == nil {
		t.Fatal("a connected machine at repositories detail publishes")
	}
	if info, err := os.Stat(deskrun.TaskKeyPath(repositories)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("repositories detail creates a private task key: %v", err)
	}
	if err := os.Chmod(deskrun.TaskKeyPath(repositories), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := deskPublisher(repositories, "1", random(), logf, newDeskClient); got != nil {
		t.Fatal("a readable task key is refused")
	}
	refuse := func(string, string) (deskpublish.Client, error) { return nil, errors.New("no client") }
	if got := deskPublisher(full, "1", random(), logf, refuse); got != nil {
		t.Fatal("a client that cannot be built publishes nothing")
	}
}
