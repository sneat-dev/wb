package hooks

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestRuntimeSegmentsKeepSafeSymbolsAndReplaceUntrustedCharacters(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ input, want string }{{"", "unknown"}, {"a/b: café", "a_b__caf_"}, {"._-AZ09", "._-AZ09"}} {
		if got := sanitizeRuntimeSegment(tc.input); got != tc.want {
			t.Fatalf("segment %q=%q want %q", tc.input, got, tc.want)
		}
	}
}

func TestPendingMetricsRefusesUnencodableTimeWithoutPublishing(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path, err := persistPendingMetricsReceipt(root, "retained", []Event{{Hook: "pre-push"}}, errors.New("native append unavailable"), time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC))
	if path != "" || err == nil || !strings.Contains(err.Error(), "encode pending hook metrics") {
		t.Fatalf("path=%q error=%v", path, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("published entries=%v error=%v", entries, err)
	}
}

func TestRuntimeDirectoryCreationRetainsOwnedBlockingFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	block := filepath.Join(root, "block")
	if err := os.WriteFile(block, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	err := ensureExecutionLayout(ExecutionLayout{Root: filepath.Join(block, "child")})
	if err == nil || !strings.Contains(err.Error(), "create hook runtime path") {
		t.Fatalf("error=%v", err)
	}
	bytes, err := os.ReadFile(block)
	if err != nil || string(bytes) != "retained" {
		t.Fatalf("blocking file=%q error=%v", bytes, err)
	}
}

func TestEnsureExecutionLayoutCreatesPrivateRuntimeDirectories(t *testing.T) {
	isolateEnvironment(t)
	root := os.Getenv(wbhome.EnvOverride)
	repo := initRepo(t)
	layout, err := ResolveExecutionLayout(repo, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureExecutionLayout(layout); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{layout.Root, layout.ReportRoot, layout.PendingMetricsRoot} {
		info, statErr := os.Stat(path)
		if statErr != nil || !info.IsDir() {
			t.Fatalf("runtime path %s was not created: %v", path, statErr)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("runtime path %s mode = %v, want 0700", path, info.Mode().Perm())
		}
	}
}

func TestEnsureExecutionLayoutRefusesARuntimeRootOccupiedByAFile(t *testing.T) {
	isolateEnvironment(t)
	root := os.Getenv(wbhome.EnvOverride)
	repo := initRepo(t)
	layout, err := ResolveExecutionLayout(repo, root)
	if err != nil {
		t.Fatal(err)
	}
	mustMkdirAll(t, filepath.Dir(layout.Root))
	mustWrite(t, layout.Root, "occupied\n")
	if err := ensureExecutionLayout(layout); err == nil || !strings.Contains(err.Error(), "create hook runtime path") {
		t.Fatalf("ensureExecutionLayout(occupied root) error = %v", err)
	}
}
