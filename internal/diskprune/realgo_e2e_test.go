//go:build e2e

package diskprune

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// With GOFLAGS=-modcache in Go's env file, the real go clean -cache that
// prune runs leaves the module cache alone and clears the build cache.
//
//nolint:paralleltest // sets GOENV, GOMODCACHE and GOFLAGS
func TestE2ERealGoCleanNeverWidensToTheModuleCache(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not installed")
	}
	dir := t.TempDir()
	envFile := filepath.Join(dir, "env")
	mustWrite(t, envFile, "GOFLAGS=-modcache\n")
	modcache := filepath.Join(dir, "mod")
	sentinel := filepath.Join(modcache, "cache", "download", "sentinel")
	mustWrite(t, sentinel, "keep me")
	cache := filepath.Join(dir, "build")
	mustWrite(t, filepath.Join(cache, "0a", "0a"+strings.Repeat("0", 62)+"-a"), "entry")
	t.Setenv("GOENV", envFile)
	t.Setenv("GOMODCACHE", modcache)
	t.Setenv("GOFLAGS", "")
	if err := goClean(context.Background(), cache); err != nil {
		t.Fatal(err)
	}
	if !exists(sentinel) {
		t.Fatal("go clean -cache removed the module cache")
	}
	if exists(filepath.Join(cache, "0a")) {
		t.Fatal("go clean -cache left the build cache")
	}
}
