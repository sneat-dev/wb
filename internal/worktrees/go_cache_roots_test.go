package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestGoCacheRootsHonorExplicitAndDefaultAuthority(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	build, modules := filepath.Join(base, "build"), filepath.Join(base, "modules")
	cache, home, first, second := filepath.Join(base, "cache"), filepath.Join(base, "home"), filepath.Join(base, "first"), filepath.Join(base, "second")
	separator := string(os.PathListSeparator)
	for _, tc := range []struct {
		name   string
		inputs goCacheRootInputs
		want   []string
	}{
		{name: "unavailable defaults"},
		{name: "native defaults", inputs: goCacheRootInputs{userCache: cache, home: home}, want: []string{filepath.Join(cache, "go-build"), filepath.Join(home, "go", "pkg", "mod")}},
		{name: "explicit overrides", inputs: goCacheRootInputs{buildCache: build, moduleCache: modules, userCache: cache, home: home}, want: []string{build, modules}},
		{name: "deduplicated authority", inputs: goCacheRootInputs{buildCache: build, moduleCache: build}, want: []string{build}},
		{name: "relative overrides rejected", inputs: goCacheRootInputs{buildCache: "relative", moduleCache: "relative", userCache: cache, home: home}},
		{name: "disabled build cache", inputs: goCacheRootInputs{buildCache: "off", moduleCache: modules, userCache: cache}, want: []string{modules}},
		{name: "first GOPATH only", inputs: goCacheRootInputs{goPath: first + separator + second}, want: []string{filepath.Join(first, "pkg", "mod")}},
		{name: "empty first GOPATH", inputs: goCacheRootInputs{goPath: separator + second}},
		{name: "relative first GOPATH", inputs: goCacheRootInputs{goPath: "relative" + separator + second}},
		{name: "module override wins over GOPATH list", inputs: goCacheRootInputs{goPath: first + separator + second, moduleCache: modules}, want: []string{modules}},
		{name: "relative defaults rejected", inputs: goCacheRootInputs{userCache: "relative", home: "relative"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := resolveGoCacheRoots(tc.inputs); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("cache roots = %v, want %v", got, tc.want)
			}
		})
	}
}

//nolint:paralleltest // Native cache resolution reads process-wide environment; fixtures restore it.
func TestAmbientGoCacheRootsUseNativeEnvironment(t *testing.T) {
	for _, name := range []string{"HOME", "USERPROFILE", "home", "XDG_CACHE_HOME", "LOCALAPPDATA", "GOCACHE", "GOPATH", "GOMODCACHE"} {
		t.Setenv(name, "")
	}
	if roots := ambientGoCacheRoots(); len(roots) != 0 {
		t.Fatalf("unavailable native home/cache = %v", roots)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("home", home)
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "cache"))
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if roots := ambientGoCacheRoots(); !reflect.DeepEqual(roots, []string{filepath.Join(cache, "go-build"), filepath.Join(home, "go", "pkg", "mod")}) {
		t.Fatalf("native default caches = %v", roots)
	}
	t.Setenv("GOCACHE", "relative")
	t.Setenv("GOMODCACHE", "relative")
	if roots := ambientGoCacheRoots(); len(roots) != 0 {
		t.Fatalf("relative caches admitted = %v", roots)
	}
	root := t.TempDir()
	t.Setenv("GOCACHE", root)
	t.Setenv("GOMODCACHE", root)
	if roots := ambientGoCacheRoots(); !reflect.DeepEqual(roots, []string{root}) {
		t.Fatalf("duplicate caches = %v", roots)
	}
	t.Setenv("GOCACHE", "off")
	t.Setenv("GOMODCACHE", "")
	first, second := filepath.Join(root, "first"), filepath.Join(root, "second")
	t.Setenv("GOPATH", first+string(os.PathListSeparator)+second)
	if roots := ambientGoCacheRoots(); !reflect.DeepEqual(roots, []string{filepath.Join(first, "pkg", "mod")}) {
		t.Fatalf("native GOPATH-list module cache = %v", roots)
	}
}

func TestProjectsRootContextPreservesBlankAndTrimsExplicitRoots(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	if withProjectsRoot(ctx, " \t") != ctx {
		t.Fatal("blank projects root changed context")
	}
	root := t.TempDir()
	if got := projectsRootFromContext(withProjectsRoot(ctx, " "+root+" ")); got != root {
		t.Fatalf("context root = %q", got)
	}
	if got := projectsRootFromContext(withProjectsRoot(withProjectsRoot(ctx, root), "\t")); got != root {
		t.Fatalf("blank override discarded existing context root: %q", got)
	}
}
