// Package goenv resolves Go's configuration the way cmd/go does, without
// running go: even go env can write telemetry counters, and the callers
// (wb disk and wb disk prune's plan) must not write anything.
package goenv

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Env is the subset of Go's configuration prune depends on, resolved
// without running go: even `go env` can write telemetry counters, and the
// plan must not write anything.
type Env struct {
	GOCACHE     string
	GOMODCACHE  string
	GOPROXY     string
	GOSUMDB     string
	GOPRIVATE   string
	GONOPROXY   string
	GONOSUMDB   string
	GOFLAGS     string
	GOCACHEPROG string
}

// goEnvSources are the inputs the resolution reads, as functions so a test
// can describe any machine.
type goEnvSources struct {
	getenv        func(string) string
	readFile      func(string) ([]byte, error)
	userConfigDir func() (string, error)
	userCacheDir  func() (string, error)
	userHomeDir   func() (string, error)
}

func realGoEnvSources() goEnvSources {
	return goEnvSources{
		getenv: os.Getenv, readFile: readRegular,
		userConfigDir: os.UserConfigDir, userCacheDir: os.UserCacheDir, userHomeDir: os.UserHomeDir,
	}
}

// Resolve resolves Go's settings the way cmd/go does, in order: the
// process environment, Go's environment file, then Go's documented default.
func Resolve() (Env, error) { return resolveGoEnv(realGoEnvSources()) }

func resolveGoEnv(src goEnvSources) (Env, error) {
	file, err := readGoEnvFile(src)
	if err != nil {
		return Env{}, err
	}
	lookup := func(key string) string {
		if value := src.getenv(key); value != "" {
			return value
		}
		return file[key]
	}
	env := Env{
		GOCACHE: lookup("GOCACHE"), GOMODCACHE: lookup("GOMODCACHE"),
		GOPROXY: lookup("GOPROXY"), GOSUMDB: lookup("GOSUMDB"),
		GOPRIVATE: lookup("GOPRIVATE"), GONOPROXY: lookup("GONOPROXY"), GONOSUMDB: lookup("GONOSUMDB"),
		GOFLAGS: lookup("GOFLAGS"), GOCACHEPROG: lookup("GOCACHEPROG"),
	}
	if env.GOCACHE == "" {
		cache, err := src.userCacheDir()
		if err != nil {
			return Env{}, err
		}
		env.GOCACHE = filepath.Join(cache, "go-build")
	}
	if env.GOMODCACHE == "" {
		gopath := lookup("GOPATH")
		if gopath == "" {
			home, err := src.userHomeDir()
			if err != nil {
				return Env{}, err
			}
			gopath = filepath.Join(home, "go")
		}
		env.GOMODCACHE = filepath.Join(filepath.SplitList(gopath)[0], "pkg", "mod")
	}
	if env.GOPROXY == "" {
		env.GOPROXY = "https://proxy.golang.org,direct"
	}
	if env.GOSUMDB == "" {
		env.GOSUMDB = "sum.golang.org"
	}
	if env.GONOPROXY == "" {
		env.GONOPROXY = env.GOPRIVATE
	}
	if env.GONOSUMDB == "" {
		env.GONOSUMDB = env.GOPRIVATE
	}
	return env, nil
}

// readGoEnvFile parses Go's environment file read-only. GOENV=off disables it;
// a missing file is the common case and means no overrides.
func readGoEnvFile(src goEnvSources) (map[string]string, error) {
	path := src.getenv("GOENV")
	if path == "off" {
		return map[string]string{}, nil
	}
	if path == "" {
		dir, err := src.userConfigDir()
		if err != nil {
			return map[string]string{}, nil
		}
		path = filepath.Join(dir, "go", "env")
	}
	raw, err := src.readFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	values := map[string]string{}
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		// Exactly cmd/go's reading of its env file: a line counts only when
		// it starts with an upper-case letter and has an "="; neither side
		// is trimmed. A " GOCACHE=..." line Go ignores is ignored here too.
		line := scanner.Text()
		key, value, ok := strings.Cut(line, "=")
		if !ok || line[0] < 'A' || line[0] > 'Z' {
			continue
		}
		values[key] = value
	}
	// A line too long to read could hide a later GOCACHEPROG or GOPRIVATE;
	// defaults in their place would enable deletion Go itself would not.
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

// readRegular reads path only when it is a regular file: a FIFO under the
// name would block the daemon that reads it.
func readRegular(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	return os.ReadFile(path)
}
