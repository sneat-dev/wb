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

// Lines are split at LF only, as Go splits them: a CRLF value keeps its CR
// (so it names a different directory, as it does for Go), and a very long
// line does not hide a later setting.
func TestGoEnvFileIsSplitAsGoSplitsIt(t *testing.T) {
	t.Parallel()
	long := "GOFLAGS=" + strings.Repeat("x", 2<<20) + "\nGOCACHEPROG=/bin/cacher\nGOMODCACHE=/mods\r\n"
	env, err := resolveGoEnv(fakeGoEnvSources(map[string]string{"GOENV": "/custom/env"}, map[string]string{"/custom/env": long}))
	if err != nil || env.GOCACHEPROG != "/bin/cacher" || env.GOMODCACHE != "/mods\r" {
		t.Fatalf("env = %#v %v", env.GOCACHEPROG, err)
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

// The env file is read only when it is a regular file; a directory (or a
// FIFO that would block) is an error, a missing file is not read.
func TestGoEnvFileMustBeRegular(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := readRegular(dir); err == nil {
		t.Fatal("a directory was read as the env file")
	}
	if _, err := readRegular(filepath.Join(dir, "absent")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a missing env file: %v", err)
	}
	file := filepath.Join(dir, "env")
	if err := os.WriteFile(file, []byte("GOFLAGS=-x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if raw, err := readRegular(file); err != nil || string(raw) != "GOFLAGS=-x\n" {
		t.Fatalf("env file = %q, %v", raw, err)
	}
}

// $GOROOT/go.env is the toolchain's layer below the user's env file: a
// GOCACHEPROG set there is seen; the user's file and the environment
// override it.
func TestGorootEnvIsTheLowestLayer(t *testing.T) {
	t.Parallel()
	src := fakeGoEnvSources(map[string]string{"GOENV": "/user/env"}, map[string]string{
		"/goroot/go.env": "GOCACHEPROG=/bin/cacher\nGOPROXY=https://toolchain.example\n",
		"/user/env":      "GOPROXY=https://user.example\n",
	})
	src.goroot = func() string { return "/goroot" }
	env, err := resolveGoEnv(src)
	if err != nil || env.GOCACHEPROG != "/bin/cacher" || env.GOPROXY != "https://user.example" {
		t.Fatalf("env = %#v %v", env, err)
	}
	src.goroot = func() string { return "/none" }
	if env, err := resolveGoEnv(src); err != nil || env.GOCACHEPROG != "" {
		t.Fatalf("a toolchain without go.env = %#v %v", env, err)
	}
	src.goroot = func() string { return "" }
	if _, err := resolveGoEnv(src); err != nil {
		t.Fatalf("no toolchain: %v", err)
	}
	broken := fakeGoEnvSources(nil, nil)
	broken.goroot = func() string { return "/goroot" }
	broken.readFile = func(path string) ([]byte, error) {
		if path == "/goroot/go.env" {
			return nil, errors.New("EIO")
		}
		return nil, os.ErrNotExist
	}
	if _, err := resolveGoEnv(broken); err == nil {
		t.Fatal("an unreadable go.env was ignored")
	}
}

// GOROOT comes from the environment, else from the go on PATH.
func TestGorootIsFoundWithoutRunningGo(t *testing.T) {
	t.Parallel()
	if got := gorootOf(func(string) string { return "/env/goroot" }, nil); got != "/env/goroot" {
		t.Fatalf("env GOROOT = %q", got)
	}
	none := func(string) string { return "" }
	if got := gorootOf(none, func(string) (string, error) { return "", errors.New("not found") }); got != "" {
		t.Fatalf("no go = %q", got)
	}
	if got := gorootOf(none, func(string) (string, error) { return "/usr/local/go/bin/go", nil }); got != "/usr/local/go" {
		t.Fatalf("go on PATH = %q", got)
	}
	if realSources := realGoEnvSources(); realSources.goroot == nil {
		t.Fatal("the real sources do not find GOROOT")
	} else {
		_ = realSources.goroot()
	}
}
