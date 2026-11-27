package diskprune

import (
	"context"
	"errors"
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
		MountPoints: mountPoints,
		Measure:     measure,
		RemoveAll:   removeAll,
	}
	settings, err := resolveGo()
	if err != nil {
		env.GoCacheError = "Go's settings cannot be read: " + err.Error()
		return env
	}
	for _, cache := range []string{settings.GOCACHE, settings.GOMODCACHE} {
		if filepath.IsAbs(cache) {
			env.GoCaches = append(env.GoCaches, resolvedPath(cache))
		}
	}
	env.GoCache, env.GoCacheProblem, env.GoCacheError = buildCache(settings)
	env.GoClean = func(ctx context.Context) error { return goClean(ctx, env.GoCache) }
	return env
}

// tempDirs are the resolved, distinct temporary directories. One that
// cannot be resolved is left out: cleaning its spelling lexically could name
// another directory (/data/missing/../archive is not /data/archive when
// missing does not exist).
func tempDirs(dirs ...string) []string {
	var out []string
	seen := map[string]bool{}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		real, err := filepath.EvalSymlinks(dir)
		if err != nil {
			continue
		}
		dir = real
		if !seen[dir] {
			seen[dir] = true
			out = append(out, dir)
		}
	}
	return out
}

// buildCache is the resolved GOCACHE when prune may clear it; else why not
// (nothing to clear), or the inspection error (a finding). It is resolved
// once, so the check, the measurement and go clean all name one directory.
func buildCache(settings goenv.Env) (cache, problem, failure string) {
	switch {
	case settings.GOCACHEPROG != "":
		return "", "GOCACHEPROG is set, so the cache is not a directory prune clears", ""
	case settings.GOCACHE == "off" || !filepath.IsAbs(settings.GOCACHE):
		return "", fmt.Sprintf("GOCACHE is %q", settings.GOCACHE), ""
	}
	real, err := filepath.EvalSymlinks(settings.GOCACHE)
	if errors.Is(err, os.ErrNotExist) {
		return "", "no build cache at " + settings.GOCACHE, ""
	}
	if err != nil {
		return "", "", "GOCACHE " + settings.GOCACHE + " cannot be inspected: " + err.Error()
	}
	if info, err := os.Stat(real); err != nil || !info.IsDir() {
		return "", "no build cache at " + settings.GOCACHE, ""
	}
	return real, "", ""
}

// resolvedPath is path with symlinks resolved when it exists.
func resolvedPath(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return filepath.Clean(path)
}

// goClean runs go clean -cache against exactly this cache. GOFLAGS is set
// to a single space, which overrides a GOFLAGS in Go's env files (dropping
// it would let a -modcache there widen the clean) and parses as no flags.
func goClean(ctx context.Context, cache string) error {
	cmd := exec.CommandContext(ctx, "go", "clean", "-cache")
	for _, variable := range os.Environ() {
		if !strings.HasPrefix(variable, "GOFLAGS=") && !strings.HasPrefix(variable, "GOCACHE=") {
			cmd.Env = append(cmd.Env, variable)
		}
	}
	cmd.Env = append(cmd.Env, "GOCACHE="+cache, "GOFLAGS= ")
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
