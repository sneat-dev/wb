package sessionlaunch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTmuxPanePIDDistinguishesMissingSessionFromOperationalFailure(t *testing.T) {
	write := func(name, diagnostic string) string {
		path := filepath.Join(t.TempDir(), name)
		body := "#!/bin/sh\nprintf '%s\\n' \"" + diagnostic + "\" >&2\nexit 1\n"
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	if _, exists, err := (osTmux{executable: write("missing", "can't find session: wb-session-x")}).PanePID(context.Background(), "wb-session-x"); err != nil || exists {
		t.Fatalf("missing session = exists %t err %v", exists, err)
	}
	if _, _, err := (osTmux{executable: write("broken", "permission denied reading tmux socket")}).PanePID(context.Background(), "wb-session-x"); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("operational error = %v", err)
	}
}

func TestTmuxPanePIDScopesAllPanesInExactSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tmux")
	body := `#!/bin/sh
if [ "$1" != "list-panes" ] || [ "$2" != "-s" ] || [ "$3" != "-t" ] || [ "$4" != "=wb-session-x" ]; then
  printf 'unexpected argv: %s\n' "$*" >&2
  exit 2
fi
printf '4242\t0\n'
`
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	pid, exists, err := (osTmux{executable: path}).PanePID(context.Background(), "wb-session-x")
	if err != nil || !exists || pid != 4242 {
		t.Fatalf("PanePID = pid %d exists %t error %v", pid, exists, err)
	}
}

func TestTmuxPaneFailureRetainsExitStatusAndBoundedDiagnostic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tmux")
	body := `#!/bin/sh
case "$1" in
  list-panes) printf '1\t17\n' ;;
  capture-pane) printf 'fatal startup configuration\n' ;;
  *) printf 'unexpected argv: %s\n' "$*" >&2; exit 2 ;;
esac
`
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	failure, found, err := (osTmux{executable: path}).PaneFailure(context.Background(), "wb-session-x")
	if err != nil || !found || failure.ExitStatus != 17 || failure.Diagnostic != "fatal startup configuration" {
		t.Fatalf("PaneFailure = %#v found %t error %v", failure, found, err)
	}
}

func TestTmuxPaneFailureAcceptsLivePaneWithEmptyExitStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tmux")
	body := `#!/bin/sh
case "$1" in
  list-panes) printf '0\t\n' ;;
  *) printf 'unexpected argv: %s\n' "$*" >&2; exit 2 ;;
esac
`
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	failure, found, err := (osTmux{executable: path}).PaneFailure(context.Background(), "wb-session-x")
	if err != nil || found || failure != (tmuxFailure{}) {
		t.Fatalf("PaneFailure = %#v found %t error %v", failure, found, err)
	}
}

func TestParseTmuxPaneFailureOutputRequiresStatusOnlyForDeadPane(t *testing.T) {
	tests := []struct {
		name   string
		output string
		dead   int
		status int
		valid  bool
	}{
		{name: "live", output: "0\t\n", valid: true},
		{name: "dead", output: "1\t17\n", dead: 1, status: 17, valid: true},
		{name: "live status", output: "0\t17\n"},
		{name: "missing newline", output: "0\t"},
		{name: "extra field", output: "1\t17\textra\n"},
		{name: "invalid dead state", output: "2\t17\n"},
		{name: "missing dead status", output: "1\t\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dead, status, err := parseTmuxPaneFailureOutput([]byte(test.output))
			if test.valid {
				if err != nil || dead != test.dead || status != test.status {
					t.Fatalf("parse = dead %d status %d error %v", dead, status, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("parse unexpectedly accepted dead %d status %d", dead, status)
			}
		})
	}
}
