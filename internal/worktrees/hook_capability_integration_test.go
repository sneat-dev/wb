package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/hooks"
	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestSecureCleanupGitHelperRunsRealGoHookWithPrivateCachesAndAuthorizedMetrics(t *testing.T) {
	if err := requireGitFilesystemCapability(); err != nil {
		t.Skipf("secure Git capability unavailable: %v", err)
	}
	fixture := newGitFixture(t)
	wbHome := filepath.Join(t.TempDir(), "wb-home")
	t.Setenv(wbhome.EnvOverride, wbHome)
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("XDG_STATE_HOME", "")
	// The descriptor-capability child must not pass instrumentation or a
	// workspace outside this retained repository to the hook it invokes.
	t.Setenv("GOCOVERDIR", filepath.Join(t.TempDir(), "outside-coverage"))
	t.Setenv("GOWORK", filepath.Join(t.TempDir(), "outside.go.work"))
	forbidden := filepath.Join(t.TempDir(), "must-not-write")
	t.Setenv("WB_TEST_FORBIDDEN", forbidden)
	installSecureHookCapabilityFixture(t, fixture.canonical)
	gitTest(t, fixture.canonical, "add", ".wb", "go.mod", "main.go", "main_test.go")
	gitTest(t, fixture.canonical, "commit", "-m", "configure secure hook capability fixture")

	canonical, err := openCanonicalRepository(fixture.canonical)
	if err != nil {
		t.Fatal(err)
	}
	defer canonical.close()
	if err := runSecureCleanupGitHelper(context.Background(), canonical, nil, nil, "", "", "push", "origin", "main"); err != nil {
		t.Fatal(err)
	}

	layout, err := hooks.ResolveExecutionLayout(fixture.canonical, "")
	if err != nil {
		t.Fatal(err)
	}
	reportDir := filepath.Join(layout.ReportRoot, "secure-go")
	for _, file := range []struct {
		name string
		want string
	}{
		{name: "gopath.txt", want: layout.GoPath},
		{name: "gocache.txt", want: layout.GoCache},
		{name: "gomodcache.txt", want: layout.GoModCache},
		{name: "gotmpdir.txt", want: layout.GoTmpDir},
	} {
		content, readErr := os.ReadFile(filepath.Join(reportDir, file.name))
		if readErr != nil {
			t.Fatalf("read %s: %v", file.name, readErr)
		}
		if got := strings.TrimSpace(string(content)); got != file.want {
			t.Fatalf("%s = %q, want %q", file.name, got, file.want)
		}
		if strings.HasPrefix(file.want, fixture.canonical+string(filepath.Separator)) || file.want == fixture.canonical {
			t.Fatalf("%s unexpectedly points into repository: %s", file.name, file.want)
		}
	}
	policy, err := hooks.LoadPolicy(fixture.canonical, "")
	if err != nil {
		t.Fatal(err)
	}
	events, err := hooks.ReadEvents(policy.Metrics.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		layout, layoutErr := hooks.ResolveExecutionLayout(fixture.canonical, "")
		if layoutErr != nil {
			t.Fatal(layoutErr)
		}
		pending, readErr := os.ReadDir(layout.PendingMetricsRoot)
		if readErr != nil || len(pending) == 0 {
			t.Fatalf("expected durable hook metrics or a replayable pending receipt at %s (pending=%v err=%v)", policy.Metrics.Path, pending, readErr)
		}
	}
	if _, err := os.Lstat(forbidden); !os.IsNotExist(err) {
		t.Fatalf("hook wrote outside declared roots: %v", err)
	}
	if status := gitTestOutput(t, fixture.canonical, "status", "--porcelain"); status != "" {
		t.Fatalf("secure hook dirtied repository: %q", status)
	}
}

func installSecureHookCapabilityFixture(t *testing.T, repo string) {
	t.Helper()
	mustWriteHookCapabilityFile(t, filepath.Join(repo, "go.mod"), "module example.invalid/securehook\n\ngo 1.26\n")
	mustWriteHookCapabilityFile(t, filepath.Join(repo, "main.go"), "package main\n\nfunc main() {}\n")
	mustWriteHookCapabilityFile(t, filepath.Join(repo, "main_test.go"), "package main\n\nimport \"testing\"\n\nfunc TestSecureHook(t *testing.T) {}\n")
	configDir := filepath.Join(repo, ".wb")
	mustMkdirAllHookCapability(t, configDir)
	mustWriteHookCapabilityFile(t, filepath.Join(configDir, "hooks.yaml"), "version: 1\nhooks:\n  pre-push:\n    template: secure-go-pre-push.sh\nprofiles:\n  exclude: [worktree]\nmetrics:\n  enabled: true\n")
	mustWriteHookCapabilityFile(t, filepath.Join(configDir, "secure-go-pre-push.sh"), `#!/bin/sh
set -eu
report_dir="$WB_HOOK_REPORT_ROOT/secure-go"
umask 077
mkdir -p "$report_dir"
if /bin/sh -c ': > "$1"' sh "$WB_TEST_FORBIDDEN" 2>/dev/null; then
    echo "forbidden write unexpectedly succeeded" >&2
    exit 99
fi
if [ -n "${GOCOVERDIR-}" ] || [ "${GOWORK-}" != "off" ]; then
    echo "cleanup helper leaked coverage environment or did not disable caller workspace" >&2
    exit 98
fi
go env GOPATH > "$report_dir/gopath.txt"
go env GOCACHE > "$report_dir/gocache.txt"
go env GOMODCACHE > "$report_dir/gomodcache.txt"
go env GOTMPDIR > "$report_dir/gotmpdir.txt"
go vet ./...
	go test ./...
`)
	hooksDir := filepath.Join(repo, ".git", "hooks")
	mustMkdirAllHookCapability(t, hooksDir)
	mustWriteHookCapabilityExecutable(t, filepath.Join(hooksDir, "pre-push"), "#!/bin/sh\nexec "+shellQuoteHookCapability(os.Args[0])+" "+secureHookRunTestHelperArgument+" pre-push \"$@\"\n")
}

func shellQuoteHookCapability(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func mustMkdirAllHookCapability(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWriteHookCapabilityFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustWriteHookCapabilityExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}
