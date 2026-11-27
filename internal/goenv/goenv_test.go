package goenv

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeGoEnvSources(env map[string]string, files map[string]string) goEnvSources {
	return goEnvSources{
		getenv: func(key string) string { return env[key] },
		readFile: func(path string) ([]byte, error) {
			if content, ok := files[path]; ok {
				return []byte(content), nil
			}
			return nil, os.ErrNotExist
		},
		userConfigDir: func() (string, error) { return "/home/u/.config", nil },
		userCacheDir:  func() (string, error) { return "/home/u/.cache", nil },
		userHomeDir:   func() (string, error) { return "/home/u", nil },
	}
}

// With nothing configured, prune must arrive at the same paths and proxy
// defaults Go itself uses, without running go.
func TestGoEnvDefaultsMatchGo(t *testing.T) {
	t.Parallel()
	env, err := resolveGoEnv(fakeGoEnvSources(nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	want := Env{
		GOCACHE: "/home/u/.cache/go-build", GOMODCACHE: "/home/u/go/pkg/mod",
		GOPROXY: "https://proxy.golang.org,direct", GOSUMDB: "sum.golang.org",
	}
	if env != want {
		t.Fatalf("env = %#v, want %#v", env, want)
	}
}

// The environment wins over Go's env file, which wins over defaults; GOPATH's
// first entry decides the module cache; GOPRIVATE seeds the two derived lists.
func TestGoEnvPrecedence(t *testing.T) {
	t.Parallel()
	file := "# comment\nGOCACHE=/file/cache\nGOPROXY=https://file.example\nGOPATH=/gp1" + string(filepath.ListSeparator) + "/gp2\nGOPRIVATE=corp.example\nnot a pair\n"
	env, err := resolveGoEnv(fakeGoEnvSources(map[string]string{"GOCACHE": "/env/cache", "GOFLAGS": "-mod=mod"},
		map[string]string{"/home/u/.config/go/env": file}))
	if err != nil {
		t.Fatal(err)
	}
	if env.GOCACHE != "/env/cache" || env.GOPROXY != "https://file.example" || env.GOMODCACHE != "/gp1/pkg/mod" {
		t.Fatalf("env = %#v", env)
	}
	if env.GONOPROXY != "corp.example" || env.GONOSUMDB != "corp.example" || env.GOFLAGS != "-mod=mod" {
		t.Fatalf("derived = %#v", env)
	}
}

// GOENV names the file, GOENV=off disables it, and an unreadable config dir
// means no file rather than an error.
func TestGoEnvFileSelection(t *testing.T) {
	t.Parallel()
	custom, err := resolveGoEnv(fakeGoEnvSources(map[string]string{"GOENV": "/custom/env"},
		map[string]string{"/custom/env": "GOSUMDB=off\n"}))
	if err != nil || custom.GOSUMDB != "off" {
		t.Fatalf("custom = %#v %v", custom, err)
	}
	off, err := resolveGoEnv(fakeGoEnvSources(map[string]string{"GOENV": "off"},
		map[string]string{"/home/u/.config/go/env": "GOSUMDB=off\n"}))
	if err != nil || off.GOSUMDB != "sum.golang.org" {
		t.Fatalf("GOENV=off = %#v %v", off, err)
	}
	src := fakeGoEnvSources(nil, nil)
	src.userConfigDir = func() (string, error) { return "", errors.New("no config dir") }
	if _, err := resolveGoEnv(src); err != nil {
		t.Fatalf("missing config dir: %v", err)
	}
}

// Failures that would leave a path unknown are errors, not guesses.
func TestGoEnvFailures(t *testing.T) {
	t.Parallel()
	failure := errors.New("boom")
	src := fakeGoEnvSources(nil, nil)
	src.readFile = func(string) ([]byte, error) { return nil, failure }
	if _, err := resolveGoEnv(src); !errors.Is(err, failure) {
		t.Fatalf("unreadable env file: %v", err)
	}
	src = fakeGoEnvSources(nil, nil)
	src.userCacheDir = func() (string, error) { return "", failure }
	if _, err := resolveGoEnv(src); !errors.Is(err, failure) {
		t.Fatalf("no cache dir: %v", err)
	}
	src = fakeGoEnvSources(nil, nil)
	src.userHomeDir = func() (string, error) { return "", failure }
	if _, err := resolveGoEnv(src); !errors.Is(err, failure) {
		t.Fatalf("no home: %v", err)
	}
	if _, err := Resolve(); err != nil {
		t.Fatalf("real resolution: %v", err)
	}
}

// A go env line too long to read fails closed: a GOCACHEPROG or GOPRIVATE
// after it must not silently fall back to defaults.
func TestGoEnvFileWithAnUnreadableLineFailsClosed(t *testing.T) {
	t.Parallel()
	long := "GOFLAGS=" + strings.Repeat("x", 2<<20) + "\nGOCACHEPROG=/bin/cacher\n"
	if _, err := resolveGoEnv(fakeGoEnvSources(map[string]string{"GOENV": "/custom/env"}, map[string]string{"/custom/env": long})); err == nil {
		t.Fatal("an over-long go env line was skipped")
	}
}

// Lines Go ignores, an indented key or a comment, are ignored; values are
// taken verbatim, as Go takes them.
func TestGoEnvFileIsReadAsGoReadsIt(t *testing.T) {
	t.Parallel()
	env, err := resolveGoEnv(fakeGoEnvSources(map[string]string{"GOENV": "/custom/env"}, map[string]string{
		"/custom/env": " GOCACHE=/backup/old-cache\n#GOSUMDB=off\ngoflags=-x\n=x\n\nGOMODCACHE=/mods\n",
	}))
	if err != nil || env.GOMODCACHE != "/mods" || env.GOSUMDB != "sum.golang.org" || strings.Contains(env.GOCACHE, "backup") {
		t.Fatalf("env = %#v %v", env, err)
	}
}
