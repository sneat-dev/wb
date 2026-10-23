package cmdsession

import (
	"errors"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCwWtReadSessionMessageBody(t *testing.T) {
	t.Parallel()
	command := NewSend(shared.Runtime{}, Dependencies{})
	command.SetIn(strings.NewReader("from stdin\n"))

	body, err := readSessionMessageBody(command, "inline", "", true)
	if err != nil || body != "inline" {
		t.Fatalf("direct body = (%q, %v)", body, err)
	}
	body, err = readSessionMessageBody(command, "", "-", false)
	if err != nil || body != "from stdin\n" {
		t.Fatalf("stdin body = (%q, %v)", body, err)
	}

	file := filepath.Join(t.TempDir(), "body.txt")
	if err := os.WriteFile(file, []byte("from a file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body, err = readSessionMessageBody(command, "", file, false)
	if err != nil || body != "from a file\n" {
		t.Fatalf("file body = (%q, %v)", body, err)
	}

	// Invalid UTF-8 is refused.
	badUTF8 := filepath.Join(t.TempDir(), "bad.txt")
	if err := os.WriteFile(badUTF8, []byte{0xff, 0xfe, 0xfd}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSessionMessageBody(command, "", badUTF8, false); err == nil || !strings.Contains(err.Error(), "valid UTF-8") {
		t.Fatalf("invalid UTF-8 = %v", err)
	}

	// An oversized direct body is refused.
	if _, err := readSessionMessageBody(command, strings.Repeat("x", sessionmove.MaxMessageBodyBytes+1), "", true); err == nil {
		t.Fatal("oversized direct body must fail")
	}
	// A read error from stdin is reported rather than ignored.
	failing := NewSend(shared.Runtime{}, Dependencies{})
	failing.SetIn(cwWtErrorReader{})
	if _, err := readSessionMessageBody(failing, "", "-", false); err == nil {
		t.Fatal("a failing stdin reader must be reported")
	}
}

type cwWtErrorReader struct{}

func (cwWtErrorReader) Read([]byte) (int, error) { return 0, errors.New("cwWt: injected read failure") }
