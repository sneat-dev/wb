package diskprune

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/diskusage"
	"github.com/sneat-dev/wb/internal/goenv"
)

// NewEnv describes this machine. It runs nothing: Go's settings are read the
// way cmd/go reads them, without running go.
func NewEnv() Env {
	env := Env{
		TempDirs:    tempDirs(os.TempDir(), os.Getenv("GOTMPDIR")),
		UID:         os.Getuid(),
		Now:         time.Now,
		GoProcesses: goProcesses,
		Measure:     measure,
		RemoveAll:   removeAll,
	}
	settings, err := resolveGo()
	if err != nil {
		env.GoCacheProblem = "Go's settings cannot be read: " + err.Error()
		return env
	}
	env.GoCache, env.GoCacheProblem = buildCache(settings)
	env.GoClean = func(ctx context.Context) error { return goClean(ctx, env.GoCache) }
	return env
}

// tempDirs are the resolved, distinct temporary directories.
func tempDirs(dirs ...string) []string {
	var out []string
	seen := map[string]bool{}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			dir = real
		}
		dir = filepath.Clean(dir)
		if !seen[dir] {
			seen[dir] = true
			out = append(out, dir)
		}
	}
	return out
}

// buildCache is GOCACHE when prune may clear it, else why not.
func buildCache(settings goenv.Env) (string, string) {
	switch {
	case settings.GOCACHEPROG != "":
		return "", "GOCACHEPROG is set, so the cache is not a directory prune clears"
	case settings.GOCACHE == "off" || !filepath.IsAbs(settings.GOCACHE):
		return "", fmt.Sprintf("GOCACHE is %q", settings.GOCACHE)
	}
	if info, err := os.Stat(settings.GOCACHE); err != nil || !info.IsDir() {
		return "", "no build cache at " + settings.GOCACHE
	}
	return settings.GOCACHE, ""
}

// goClean runs go clean -cache against exactly this cache. GOFLAGS is
// dropped so that a -modcache there cannot widen the clean.
func goClean(ctx context.Context, cache string) error {
	cmd := exec.CommandContext(ctx, "go", "clean", "-cache")
	for _, variable := range os.Environ() {
		if !strings.HasPrefix(variable, "GOFLAGS=") && !strings.HasPrefix(variable, "GOCACHE=") {
			cmd.Env = append(cmd.Env, variable)
		}
	}
	cmd.Env = append(cmd.Env, "GOCACHE="+cache)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go clean -cache: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func measure(ctx context.Context, path string) int64 {
	usage, err := diskusage.NewWalk().Measure(ctx, path)
	if err != nil {
		return 0
	}
	return usage.UnsharedBytes
}

// resolveGo reads Go's settings; a seam for a machine whose settings cannot
// be read.
var resolveGo = goenv.Resolve
