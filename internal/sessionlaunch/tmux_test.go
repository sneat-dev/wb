package sessionlaunch

import (
	"context"
	"fmt"
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
	for index, diagnostic := range []string{
		"can't find session: wb-session-x",
		"can't find window: wb-session-x",
	} {
		if _, exists, err := (osTmux{executable: write(fmt.Sprintf("missing-%d", index), diagnostic)}).PanePID(context.Background(), "wb-session-x"); err != nil || exists {
			t.Fatalf("missing session for %q = exists %t err %v", diagnostic, exists, err)
		}
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
printf '4242\n'
`
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	pid, exists, err := (osTmux{executable: path}).PanePID(context.Background(), "wb-session-x")
	if err != nil || !exists || pid != 4242 {
		t.Fatalf("PanePID = pid %d exists %t error %v", pid, exists, err)
	}
}
