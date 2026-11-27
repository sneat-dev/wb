//go:build linux

package diskprune

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/mod/module"
	"golang.org/x/mod/sumdb/dirhash"
)

type moduleFixture struct {
	t       *testing.T
	tw      *testWorld
	cache   string
	proxy   map[string]int
	modBody map[string]string
	fetched []string
}

// add writes one module version the way Go does: download files, a lock, and
// optionally a read-only extracted tree whose ziphash matches it.
func (f *moduleFixture) add(path, version string, zip, extract bool) string {
	f.t.Helper()
	escPath, _ := module.EscapePath(path)
	escVersion, _ := module.EscapeVersion(version)
	dl := filepath.Join(f.cache, "cache", "download", filepath.FromSlash(escPath), "@v")
	mod := "module " + path + "\n"
	mustWrite(f.t, filepath.Join(dl, escVersion+".mod"), mod, 0o444)
	mustWrite(f.t, filepath.Join(dl, escVersion+".info"), `{"Version":"`+version+`"}`, 0o444)
	mustWrite(f.t, filepath.Join(dl, "list"), version+"\n", 0o644)
	f.modBody["/"+escPath+"/@v/"+escVersion+".mod"] = mod
	extracted := filepath.Join(f.cache, filepath.FromSlash(escPath)+"@"+escVersion)
	if zip {
		mustWrite(f.t, filepath.Join(dl, escVersion+".zip"), "PK zip of "+path, 0o444)
		mustWrite(f.t, filepath.Join(dl, escVersion+".lock"), "", 0o644)
	}
	if extract {
		mustWrite(f.t, filepath.Join(extracted, "go.mod"), mod, 0o444)
		mustWrite(f.t, filepath.Join(extracted, "pkg", "x.go"), "package pkg\n", 0o444)
		hash, err := dirhash.HashDir(extracted, path+"@"+version, dirhash.Hash1)
		if err != nil {
			f.t.Fatal(err)
		}
		mustWrite(f.t, filepath.Join(dl, escVersion+".ziphash"), hash, 0o444)
		for _, dir := range []string{filepath.Join(extracted, "pkg"), extracted} {
			if err := os.Chmod(dir, 0o555); err != nil {
				f.t.Fatal(err)
			}
		}
	}
	return extracted
}

func (f *moduleFixture) http(_ context.Context, method, url string) (int, []byte, error) {
	rel := strings.TrimPrefix(url, "https://proxy.example")
	f.fetched = append(f.fetched, method+" "+rel)
	if status, ok := f.proxy[rel]; ok {
		return status, nil, nil
	}
	if body, ok := f.modBody[rel]; ok && method == "GET" {
		return 200, []byte(body), nil
	}
	if method == "HEAD" && strings.HasSuffix(rel, ".zip") {
		return 200, nil, nil
	}
	return 404, nil, nil
}

func newModuleFixture(t *testing.T) *moduleFixture {
	t.Helper()
	tw := newTestWorld(t)
	cache := filepath.Join(t.TempDir(), "mod")
	chmodTreeWritable(t, cache)
	f := &moduleFixture{t: t, tw: tw, cache: cache, proxy: map[string]int{}, modBody: map[string]string{}}
	tw.Go = GoEnv{GOMODCACHE: cache, GOPROXY: "https://proxy.example,direct", GOSUMDB: "sum.golang.org", GOPRIVATE: "corp.example", GONOPROXY: "corp.example", GONOSUMDB: "corp.example"}
	tw.HTTP = f.http
	tw.LockModuleVersion = LockModuleVersion
	tw.World.Now = func() time.Time { return time.Now().Add(30 * 24 * time.Hour) }
	tw.categories = []category{moduleCategory{}}
	return f
}

func (f *moduleFixture) checkout(files map[string]string) string {
	f.t.Helper()
	root := f.t.TempDir()
	for rel, content := range files {
		mustWrite(f.t, filepath.Join(root, rel), content, 0o644)
	}
	f.tw.Checkouts = append(f.tw.Checkouts, root)
	return root
}

const h = " h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

// AC modules-referenced-or-unrefetchable-kept.
func TestModulesKeepReferencedUnrefetchableAndEdited(t *testing.T) {
	t.Parallel()
	f := newModuleFixture(t)
	gone := f.add("example.com/a", "v1.0.0", true, true)
	f.add("example.com/a", "v1.1.0", true, true)
	f.add("example.com/B", "v2.0.0", true, true)
	f.add("example.com/tools", "v1.0.0", true, false)
	f.add("example.com/w", "v1.0.0", true, false)
	f.add("corp.example/private", "v1.0.0", true, true)
	edited := f.add("example.com/edited", "v1.0.0", true, true)
	if err := os.Chmod(edited, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(edited, "local.go"), "package edited // a local change", 0o644)
	f.add("example.com/gone", "v1.0.0", true, true)
	f.proxy["/example.com/gone/@v/v1.0.0.mod"] = 404
	f.add("example.com/nozip", "v1.0.0", true, true)
	f.proxy["/example.com/nozip/@v/v1.0.0.zip"] = 404
	f.add("example.com/auth", "v1.0.0", false, false)
	f.proxy["/example.com/auth/@v/v1.0.0.mod"] = 401
	f.add("golang.org/toolchain", "v0.0.1-go1.27.4.linux-amd64", true, true)
	f.add("golang.org/fips140", "v1.0.0", true, false)
	young := f.add("example.com/c", "v1.0.0", true, false)
	future := time.Now().Add(29 * 24 * time.Hour)
	youngMod := filepath.Join(f.cache, "cache", "download", "example.com", "c", "@v", "v1.0.0.mod")
	if err := os.Chtimes(youngMod, future, future); err != nil {
		t.Fatal(err)
	}
	f.add("example.com/modonly", "v1.0.0", false, false)
	f.checkout(map[string]string{"go.sum": "example.com/a v1.1.0" + h + "\n\n"})
	f.checkout(map[string]string{
		"nested/go.sum":         "example.com/B v2.0.0/go.mod" + h + "\n",
		"tools/tools.mod":       "module tools\n",
		"tools/tools.sum":       "example.com/tools v1.0.0" + h + "\n",
		"go.work.sum":           "example.com/w v1.0.0" + h + "\n",
		"notes.sum":             "not a sum file: no notes.mod beside it",
		"node_modules/x/go.sum": "garbage that must not be read",
	})

	report := f.tw.run(Options{Apply: true, Categories: []string{CategoryModules}, Floor: 0.01})
	items := map[string]ItemReport{}
	for _, it := range categoryByName(t, report, CategoryModules).Items {
		items[strings.TrimPrefix(it.Path, f.cache+"/")] = it
	}
	if it := items["example.com/a@v1.0.0"]; it.Status != StatusDeleted || exists(gone) {
		t.Errorf("a@v1.0.0 = %#v (exists %t)", it, exists(gone))
	}
	if it := items["example.com/modonly@v1.0.0"]; it.Status != StatusDeleted {
		t.Errorf("modonly = %#v", it)
	}
	for key, reason := range map[string]string{
		"corp.example/private@v1.0.0": "not-refetchable: private", "example.com/edited@v1.0.0": "locally-modified",
		"example.com/gone@v1.0.0": "not-refetchable: missing-at-proxy", "example.com/nozip@v1.0.0": "not-refetchable: missing-at-proxy",
		"example.com/auth@v1.0.0": "not-refetchable: proxy-auth",
	} {
		if it := items[key]; !strings.HasPrefix(it.Reason, reason) {
			t.Errorf("%s = %#v, want %s", key, it, reason)
		}
	}
	for _, kept := range []string{"example.com/a@v1.1.0", "example.com/!b@v2.0.0", "example.com/tools@v1.0.0", "example.com/w@v1.0.0", "golang.org/toolchain@v0.0.1-go1.27.4.linux-amd64", "example.com/c@v1.0.0"} {
		if _, listed := items[kept]; listed {
			t.Errorf("%s was a candidate", kept)
		}
	}
	dl := filepath.Join(f.cache, "cache", "download", "example.com", "a", "@v")
	for _, survivor := range []string{"v1.0.0.lock", "list", "v1.1.0.zip"} {
		if !exists(filepath.Join(dl, survivor)) {
			t.Errorf("%s was removed", survivor)
		}
	}
	for _, removed := range []string{"v1.0.0.zip", "v1.0.0.ziphash", "v1.0.0.mod", "v1.0.0.info", "v1.0.0.partial"} {
		if exists(filepath.Join(dl, removed)) {
			t.Errorf("%s survived", removed)
		}
	}
	if !exists(young) && exists(youngMod) == false {
		t.Error("the young version was removed")
	}
	if cat := categoryByName(t, report, CategoryModules); !cat.EstimateIsUpperBound || cat.EffectiveFloor != "168h0m0s" {
		t.Errorf("category = %#v", cat)
	}
}

// A proxy prune cannot prove independent, or verification off, keeps every
// version before any request is made.
func TestModulesRefetchProblems(t *testing.T) {
	t.Parallel()
	for goproxy, want := range map[string]string{
		"file:///home/u/go/pkg/mod/cache/download": "not https",
		"https://127.0.0.1:8443":                   "loopback",
		"https://localhost/":                       "loopback",
		"off":                                      "names no proxy",
		"direct":                                   "names no proxy",
		",,":                                       "names no proxy",
		"http://proxy.example":                     "not https",
	} {
		if got := refetchProblem(GoEnv{GOPROXY: goproxy}, "example.com/a"); !strings.Contains(got, want) {
			t.Errorf("GOPROXY=%q: %q, want %q", goproxy, got, want)
		}
	}
	if got := refetchProblem(GoEnv{GOPROXY: "https://p.example", GOSUMDB: "off"}, "x.example/a"); !strings.Contains(got, "GOSUMDB") {
		t.Errorf("sumdb off: %q", got)
	}
	if got := refetchProblem(GoEnv{GOPROXY: "https://p.example", GOFLAGS: "-mod=vendor"}, "x.example/a"); !strings.Contains(got, "vendor") {
		t.Errorf("vendor: %q", got)
	}
	if got := refetchProblem(GoEnv{GOPROXY: "https://p.example|https://q.example"}, "x.example/a"); got != "" {
		t.Errorf("good proxy: %q", got)
	}
}

// AC module-reference-set-fails-closed and module-busy-while-any-go-tool-exists.
func TestModulesFailClosedAndBusy(t *testing.T) {
	t.Parallel()
	conflict := newModuleFixture(t)
	conflict.add("example.com/a", "v1.0.0", true, true)
	conflict.checkout(map[string]string{"go.sum": "<<<<<<< HEAD\nexample.com/a v1.0.0" + h + "\n"})
	report := conflict.tw.run(Options{Apply: true, Categories: []string{CategoryModules}, Floor: 0.01})
	if cat := categoryByName(t, report, CategoryModules); !strings.HasPrefix(cat.Reason, "incomplete-reference-set") || report.Outcome != OutcomeFailClosed {
		t.Fatalf("conflict = %#v outcome %q", cat, report.Outcome)
	}
	ignored := newModuleFixture(t)
	ignored.add("example.com/a", "v1.0.0", true, true)
	ignored.checkout(map[string]string{"testdata/broken/go.sum": "<<<<<<< HEAD\n"})
	ignored.tw.IgnoreSumFiles = []string{filepath.Join("testdata", "broken", "go.sum")}
	report = ignored.tw.run(Options{Categories: []string{CategoryModules}})
	if cat := categoryByName(t, report, CategoryModules); cat.Status == StatusSkipped || cat.Items[0].Reason != "ignored-sum-file" {
		t.Fatalf("ignored = %#v", cat)
	}

	unreadable := newModuleFixture(t)
	unreadable.add("example.com/a", "v1.0.0", true, true)
	root := unreadable.checkout(map[string]string{"sub/go.sum": "example.com/a v1.0.0" + h + "\n"})
	if err := os.Chmod(filepath.Join(root, "sub"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(root, "sub"), 0o755) })
	if os.Geteuid() != 0 {
		cat := categoryByName(t, unreadable.tw.run(Options{Categories: []string{CategoryModules}}), CategoryModules)
		if !strings.HasPrefix(cat.Reason, "incomplete-reference-set") {
			t.Fatalf("unreadable = %#v", cat)
		}
	}
	gap := newModuleFixture(t)
	gap.add("example.com/a", "v1.0.0", true, true)
	gap.tw.ProtectedGap = "git worktree list failed"
	if cat := categoryByName(t, gap.tw.run(Options{Categories: []string{CategoryModules}}), CategoryModules); !strings.HasPrefix(cat.Reason, "incomplete-reference-set") {
		t.Fatalf("gap = %#v", cat)
	}

	for name, processes := range map[string][]Process{
		"in fleet": {{PID: 10, Comm: "go", Readable: true, Cwd: "/home/u/projects/acme/app"}},
		"stopped":  {{PID: 11, Comm: "compile", Readable: true}},
		"gopls":    {{PID: 12, Comm: "gopls", Readable: true}},
	} {
		busy := newModuleFixture(t)
		busy.add("example.com/a", "v1.0.0", true, true)
		busy.tw.processes = processes
		cat := categoryByName(t, busy.tw.run(Options{Apply: true, Categories: []string{CategoryModules}, Floor: 0.01}), CategoryModules)
		if cat.Reason != "busy" || len(cat.PIDs) != 1 {
			t.Errorf("%s: %#v", name, cat)
		}
	}
	unverified := newModuleFixture(t)
	unverified.add("example.com/a", "v1.0.0", true, true)
	unverified.tw.processes = []Process{{PID: 13, Comm: "go"}}
	if cat := categoryByName(t, unverified.tw.run(Options{Categories: []string{CategoryModules}}), CategoryModules); cat.Reason != "busy-unverified" {
		t.Errorf("unverified = %#v", cat)
	}

	locked := newModuleFixture(t)
	locked.add("example.com/a", "v1.0.0", true, true)
	hold, err := os.Open(filepath.Join(locked.cache, "cache", "download", "example.com", "a", "@v", "v1.0.0.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Close()
	held, err := LockModuleVersion(hold.Name())
	if err != nil {
		t.Fatal(err)
	}
	report = locked.tw.run(Options{Apply: true, Categories: []string{CategoryModules}, Floor: 0.01})
	held()
	if it := categoryByName(t, report, CategoryModules).Items[0]; !strings.HasPrefix(it.Reason, "busy: Go holds") {
		t.Fatalf("locked version = %#v", it)
	}
}

// AC modules-not-in-the-default-run, plus configuration and apply edge cases.
func TestModulesEdgeCases(t *testing.T) {
	t.Parallel()
	f := newModuleFixture(t)
	f.add("example.com/a", "v1.0.0", true, true)
	f.tw.categories = []category{moduleCategory{}, scratchCategory{}}
	report := f.tw.run(Options{Apply: true, Floor: 0.01})
	if cat := categoryByName(t, report, CategoryModules); cat.Status != StatusRequiresCategory || cat.EstimateBytes == nil || *cat.EstimateBytes <= 0 {
		t.Fatalf("default run = %#v", cat)
	}
	if !exists(filepath.Join(f.cache, "example.com", "a@v1.0.0")) {
		t.Fatal("the default run removed a module")
	}

	for name, mutate := range map[string]func(*moduleFixture){
		"missing cache": func(f *moduleFixture) { f.tw.Go.GOMODCACHE = filepath.Join(f.cache, "absent") },
		"protected":     func(f *moduleFixture) { f.tw.Protected = []string{filepath.Dir(f.cache)} },
		"unknown":       func(f *moduleFixture) { f.tw.evidence = Evidence{Unknown: "container"} },
		"identity": func(f *moduleFixture) {
			f.tw.Identity = func(string) (Identity, error) { return Identity{}, errors.New("statx") }
		},
		"unreadable dl": func(f *moduleFixture) {
			dl := filepath.Join(f.cache, "cache", "download", "example.com")
			_ = os.Chmod(dl, 0o000)
			f.t.Cleanup(func() { _ = os.Chmod(dl, 0o755) })
		},
	} {
		f := newModuleFixture(t)
		f.add("example.com/a", "v1.0.0", true, true)
		mutate(f)
		cat := categoryByName(t, f.tw.run(Options{Categories: []string{CategoryModules}}), CategoryModules)
		if cat.Status != StatusSkipped && !(name == "unreadable dl" && os.Geteuid() == 0) {
			t.Errorf("%s: %#v", name, cat)
		}
	}

	for name, c := range map[string]struct {
		mutate func(*moduleFixture)
		status string
		reason string
	}{
		"busy at apply": {func(f *moduleFixture) {
			f.tw.categories = []category{hookedCategory{inner: moduleCategory{}, after: func() { f.tw.processes = []Process{{PID: 9, Comm: "go", Readable: true}} }}}
		}, StatusSkipped, "busy"},
		"budget spent": {func(f *moduleFixture) {
			f.tw.HTTP = func(context.Context, string, string) (int, []byte, error) {
				time.Sleep(time.Millisecond)
				return 0, nil, errors.New("timeout")
			}
		}, StatusSkipped, "not-refetchable"},
		"zip probe error": {func(f *moduleFixture) {
			f.tw.HTTP = func(_ context.Context, method, url string) (int, []byte, error) {
				if method == "HEAD" {
					return 0, nil, errors.New("reset")
				}
				return f.http(context.Background(), method, url)
			}
		}, StatusSkipped, "not-refetchable: reset"},
		"go.mod differs": {func(f *moduleFixture) {
			f.modBody["/example.com/a/@v/v1.0.0.mod"] = "module evil\n"
		}, StatusSkipped, "not-refetchable: the proxy's go.mod differs"},
		"lock error": {func(f *moduleFixture) {
			f.tw.LockModuleVersion = func(string) (func(), error) { return nil, errors.New("EIO") }
		}, StatusFailed, ""},
		"changed": {func(f *moduleFixture) {
			f.tw.RemoveFile = func(string, string, Identity) (int64, error) { return 0, ErrChanged }
		}, StatusSkipped, "changed"},
		"remove error": {func(f *moduleFixture) {
			f.tw.RemoveFile = func(string, string, Identity) (int64, error) { return 0, errors.New("EIO") }
		}, StatusFailed, ""},
		"tree mount": {func(f *moduleFixture) {
			f.tw.RemoveTree = func(string, string, Identity) (TreeOutcome, error) { return TreeOutcome{}, ErrContainsMount }
		}, StatusSkipped, "contains-mount"},
		"late zip error": {func(f *moduleFixture) {
			calls := 0
			f.tw.RemoveFile = func(dir, name string, id Identity) (int64, error) {
				calls++
				if strings.HasSuffix(name, ".zip") {
					return 0, errors.New("EIO")
				}
				return RemoveFileAt(dir, name, id)
			}
		}, StatusFailed, ""},
		"identity at apply": {func(f *moduleFixture) {
			f.tw.categories = []category{hookedCategory{inner: moduleCategory{}, after: func() {
				f.tw.Identity = func(string) (Identity, error) { return Identity{}, errors.New("statx") }
			}}}
		}, StatusFailed, ""},
		"remounted": {func(f *moduleFixture) {
			f.tw.categories = []category{hookedCategory{inner: moduleCategory{}, after: func() {
				f.tw.Identity = func(path string) (Identity, error) { id, err := StatIdentity(path); id.MountID++; return id, err }
			}}}
		}, StatusSkipped, "contains-mount"},
	} {
		f := newModuleFixture(t)
		f.add("example.com/a", "v1.0.0", true, true)
		c.mutate(f)
		cat := categoryByName(t, f.tw.run(Options{Apply: true, Categories: []string{CategoryModules}, Floor: 0.01}), CategoryModules)
		if len(cat.Items) != 1 || cat.Items[0].Status != c.status || !strings.HasPrefix(cat.Items[0].Reason, c.reason) {
			t.Errorf("%s: %#v", name, cat.Items)
		}
	}
	spent := &probeClock{}
	if got := proveRefetch(context.Background(), f.tw.World, moduleVersion{}, spent); !strings.Contains(got, "probe-budget") {
		t.Fatalf("spent budget = %q", got)
	}
	if got := locallyModified(moduleVersion{downloadDir: t.TempDir(), escVersion: "v1"}); !strings.HasPrefix(got, "partial-scan") {
		t.Fatalf("missing ziphash = %q", got)
	}
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "v1.ziphash"), "h1:x", 0o644)
	if got := locallyModified(moduleVersion{downloadDir: dir, escVersion: "v1", extracted: filepath.Join(dir, "absent")}); !strings.HasPrefix(got, "partial-scan") {
		t.Fatalf("missing tree = %q", got)
	}
}
