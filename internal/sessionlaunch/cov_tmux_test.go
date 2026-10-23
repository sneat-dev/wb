package sessionlaunch

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestSlCovStartDetachedRunsFixedArgvForAbsentSession(t *testing.T) {
	t.Parallel()
	log := filepath.Join(t.TempDir(), "new-session.log")
	path := slCovScript(t, `case "$1" in
  list-panes) printf '0\t\n' ;;
  new-session) printf '%s' "$*" > `+log+` ;;
esac
`)
	if err := (osTmux{executable: path}).StartDetached(context.Background(), "wb-session-x", "/target/worktree", "/bin/codex", []string{"-C", "/target/worktree", "prompt"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	want := "new-session -d -s wb-session-x -c /target/worktree /bin/codex -C /target/worktree prompt ; set-option -t =wb-session-x remain-on-exit on"
	if string(raw) != want {
		t.Fatalf("new-session argv = %q, want %q", raw, want)
	}
}

func TestSlCovStartDetachedRemovesTerminalSessionBeforeRecreating(t *testing.T) {
	t.Parallel()
	killLog := filepath.Join(t.TempDir(), "kill.log")
	newLog := filepath.Join(t.TempDir(), "new.log")
	path := slCovScript(t, `case "$1" in
  list-panes) printf '1\t9\n' ;;
  capture-pane) printf 'previous crash\n' ;;
  kill-session) printf '%s' "$*" > `+killLog+` ;;
  new-session) printf 'started' > `+newLog+` ;;
esac
`)
	if err := (osTmux{executable: path}).StartDetached(context.Background(), "wb-session-x", "/target/worktree", "/bin/codex", nil); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(killLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "kill-session -t =wb-session-x") {
		t.Fatalf("kill-session argv = %q", raw)
	}
	if _, err := os.Stat(newLog); err != nil {
		t.Fatalf("new-session was not reached: %v", err)
	}
}

func TestSlCovStartDetachedSurfacesKillAndStartFailures(t *testing.T) {
	t.Parallel()
	t.Run("kill failure", func(t *testing.T) {
		t.Parallel()
		path := slCovScript(t, `case "$1" in
  list-panes) printf '1\t9\n' ;;
  capture-pane) printf 'previous crash\n' ;;
  kill-session) printf 'lock held\n'; exit 3 ;;
  new-session) exit 0 ;;
esac
`)
		err := (osTmux{executable: path}).StartDetached(context.Background(), "wb-session-x", "/w", "/bin/codex", nil)
		if err == nil || !strings.Contains(err.Error(), "remove terminal tmux successor") {
			t.Fatalf("kill failure = %v", err)
		}
	})
	t.Run("start failure", func(t *testing.T) {
		t.Parallel()
		path := slCovScript(t, `case "$1" in
  list-panes) printf '0\t\n' ;;
  new-session) printf 'duplicate session\n'; exit 4 ;;
esac
`)
		err := (osTmux{executable: path}).StartDetached(context.Background(), "wb-session-x", "/w", "/bin/codex", nil)
		if err == nil || !strings.Contains(err.Error(), "start detached tmux successor") {
			t.Fatalf("start failure = %v", err)
		}
	})
	t.Run("failure inspection failure", func(t *testing.T) {
		t.Parallel()
		path := slCovScript(t, `case "$1" in
  list-panes) printf 'permission denied\n'; exit 2 ;;
esac
`)
		err := (osTmux{executable: path}).StartDetached(context.Background(), "wb-session-x", "/w", "/bin/codex", nil)
		if err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Fatalf("inspection failure = %v", err)
		}
	})
}

func TestSlCovPanePIDRejectsMalformedFieldShapes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		output string
		code   int
		pid    int
		exists bool
		valid  bool
	}{
		{name: "live", output: "4242\t0\n", pid: 4242, exists: true, valid: true},
		{name: "dead", output: "4242\t1\n", valid: true},
		{name: "single field", output: "4242\n"},
		{name: "invalid dead state", output: "4242\t2\n"},
		{name: "invalid pid", output: "notapid\t0\n"},
		{name: "non positive pid", output: "0\t0\n"},
		{name: "no server", output: "no server running on /private/tmp/tmux-501/default\n", code: 1, valid: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := slCovScript(t, "printf '"+strings.ReplaceAll(test.output, "\n", "\\n")+"'\nexit "+strconv.Itoa(test.code)+"\n")
			pid, exists, err := (osTmux{executable: path}).PanePID(context.Background(), "wb-session-x")
			if !test.valid {
				if err == nil {
					t.Fatalf("PanePID accepted %q", test.output)
				}
				return
			}
			if err != nil || exists != test.exists || pid != test.pid {
				t.Fatalf("PanePID = pid %d exists %t error %v", pid, exists, err)
			}
		})
	}
}

func TestSlCovPaneFailureSurfacesCaptureAndParseFailures(t *testing.T) {
	t.Parallel()
	t.Run("capture failure", func(t *testing.T) {
		t.Parallel()
		path := slCovScript(t, `case "$1" in
  list-panes) printf '1\t5\n' ;;
  capture-pane) printf 'pane vanished\n'; exit 2 ;;
esac
`)
		_, found, err := (osTmux{executable: path}).PaneFailure(context.Background(), "wb-session-x")
		if err == nil || found || !strings.Contains(err.Error(), "capture terminal tmux successor") {
			t.Fatalf("capture failure = found %t error %v", found, err)
		}
	})
	t.Run("malformed fields", func(t *testing.T) {
		t.Parallel()
		path := slCovScript(t, `printf '1\t5'`)
		_, _, err := (osTmux{executable: path}).PaneFailure(context.Background(), "wb-session-x")
		if err == nil || !strings.Contains(err.Error(), "malformed failure fields") {
			t.Fatalf("malformed fields = %v", err)
		}
	})
	t.Run("unexpected exit status", func(t *testing.T) {
		t.Parallel()
		path := slCovScript(t, `printf 'socket error\n'; exit 2`)
		_, _, err := (osTmux{executable: path}).PaneFailure(context.Background(), "wb-session-x")
		if err == nil || !strings.Contains(err.Error(), "socket error") {
			t.Fatalf("unexpected exit status = %v", err)
		}
	})
}
