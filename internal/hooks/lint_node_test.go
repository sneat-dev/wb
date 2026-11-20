package hooks

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func nodeLintRepo(t *testing.T, files map[string]string) (repo, head string) {
	t.Helper()
	repo = lintRepo(t)
	return repo, commitFiles(t, repo, files)
}

func TestNodePrePushRunsNxAffectedLintAgainstThePushBase(t *testing.T) {
	t.Parallel()
	repo := lintRepo(t)
	base := git(t, repo, "rev-parse", "HEAD")
	head := commitFiles(t, repo, map[string]string{"nx.json": "{}\n", "pnpm-lock.yaml": "x\n", "libs/a/src/x.ts": "export {}\n"})
	mustMkdirAll(t, filepath.Join(repo, "node_modules"))
	h := newLintHarness(t, repo)
	stdin := pushLine("feature", head) + "refs/heads/feature " + head + " refs/heads/other " + zeroOID40 + "\n"
	if code := h.runWithStdin("pre-push", "node", stdin); code != 0 {
		t.Fatalf("exit %d: %s", code, h.stderr.String())
	}
	want := []string{".|pnpm exec nx affected -t lint --base=" + base + " --head=" + head}
	if !reflect.DeepEqual(h.calls, want) {
		t.Fatalf("calls\n got %q\nwant %q", h.calls, want)
	}

	h.calls = nil
	h.exit = func([]string) (int, error) { return 1, nil }
	if code := h.runWithStdin("pre-push", "node", pushLine("feature", head)); code != 1 || !strings.Contains(h.stderr.String(), "nx lint failed") {
		t.Fatalf("failing nx: exit %d stderr %q", code, h.stderr.String())
	}
}

func TestNodePrePushLintsEveryNxProjectWithoutALocalBase(t *testing.T) {
	t.Parallel()
	repo := initRepo(t)
	commitFiles(t, repo, map[string]string{"nx.json": "{}\n", "a.ts": "export {}\n"})
	mustMkdirAll(t, filepath.Join(repo, "node_modules"))
	h := newLintHarness(t, repo)
	h.terminal = true
	if code := h.run("pre-push", "node"); code != 0 || !reflect.DeepEqual(h.calls, []string{".|npx --no-install nx run-many -t lint"}) {
		t.Fatalf("exit %d calls %q", code, h.calls)
	}
}

func TestNodePrePushNxNeedsItsToolsAndDependencies(t *testing.T) {
	t.Parallel()
	repo, head := nodeLintRepo(t, map[string]string{"nx.json": "{}\n", "a.ts": "export {}\n"})
	h := newLintHarness(t, repo)
	if code := h.runWithStdin("pre-push", "node", pushLine("f", head)); code != 0 || len(h.calls) != 0 || !strings.Contains(h.stderr.String(), "no node_modules") {
		t.Fatalf("no dependencies: exit %d calls %q stderr %q", code, h.calls, h.stderr.String())
	}
	h.missing["npm"] = true
	if code := h.runWithStdin("pre-push", "node", pushLine("f", head)); code != 1 || !strings.Contains(h.stderr.String(), "not found on PATH: npm") {
		t.Fatalf("no package manager: exit %d stderr %q", code, h.stderr.String())
	}
}

func TestNodePrePushRunsEachTouchedPackagesOwnLintScript(t *testing.T) {
	t.Parallel()
	repo, head := nodeLintRepo(t, map[string]string{
		"web/package.json":            `{"scripts":{"lint":"eslint ."}}`,
		"web/yarn.lock":               "x\n",
		"web/src/a.tsx":               "export {}\n",
		"web/src/b.ts":                "export {}\n",
		"tools/package.json":          `{"scripts":{"test":"x"}}`,
		"tools/c.mjs":                 "export {}\n",
		"api/package.json":            `{"scripts":{"lint":"x"}}`,
		"api/d.js":                    "x\n",
		"docs/readme.md":              "x\n",
		"web/node_modules/dep/i.js":   "x\n",
		"web/node_modules/dep/p.json": "{}\n",
	})
	mustMkdirAll(t, filepath.Join(repo, "web", "node_modules"))
	h := newLintHarness(t, repo)
	if code := h.runWithStdin("pre-push", "node", pushLine("f", head)); code != 0 {
		t.Fatalf("exit %d: %s", code, h.stderr.String())
	}
	if want := []string{"web|yarn run lint"}; !reflect.DeepEqual(h.calls, want) {
		t.Fatalf("calls\n got %q\nwant %q", h.calls, want)
	}
	if !strings.Contains(h.stderr.String(), "dependencies are not installed for api") {
		t.Fatalf("stderr %q", h.stderr.String())
	}

	h.exit = func([]string) (int, error) { return 2, nil }
	if code := h.runWithStdin("pre-push", "node", pushLine("f", head)); code != 1 || !strings.Contains(h.stderr.String(), "lint failed in package web") {
		t.Fatalf("failing lint: exit %d stderr %q", code, h.stderr.String())
	}
	h.missing["node"] = true
	if code := h.runWithStdin("pre-push", "node", pushLine("f", head)); code != 1 || !strings.Contains(h.stderr.String(), "not found on PATH: node") {
		t.Fatalf("no node: exit %d stderr %q", code, h.stderr.String())
	}
}

func TestNodePrePushLintsEveryPackageWhenThePushCannotBeRead(t *testing.T) {
	t.Parallel()
	repo := lintRepo(t)
	commitFiles(t, repo, map[string]string{"package.json": `{"scripts":{"lint":"x"}}`, "a.js": "x\n", "node_modules/m/package.json": "{}\n"})
	h := newLintHarness(t, repo)
	if code := h.runWithStdin("pre-push", "node", pushLine("f", strings.Repeat("e", 40))); code != 0 {
		t.Fatalf("exit %d: %s", code, h.stderr.String())
	}
	if want := []string{".|npm run lint"}; !reflect.DeepEqual(h.calls, want) {
		t.Fatalf("calls %q", h.calls)
	}
}

func TestNodePrePushSkipsAPushWithoutScriptsOrAtTierZero(t *testing.T) {
	t.Parallel()
	repo, head := nodeLintRepo(t, map[string]string{"package.json": `{"scripts":{"lint":"x"}}`, "README.md": "x\n"})
	h := newLintHarness(t, repo)
	if code := h.runWithStdin("pre-push", "node", pushLine("f", head)); code != 0 || len(h.calls) != 0 || !strings.Contains(h.stderr.String(), "changes no JS/TS source") {
		t.Fatalf("exit %d calls %q", code, h.calls)
	}
	h.tier = Classification{Tier: TierSkip, Reason: "checkpoint"}
	if code := h.runWithStdin("pre-push", "node", pushLine("f", head)); code != 0 || len(h.calls) != 0 {
		t.Fatalf("tier 0: exit %d calls %q", code, h.calls)
	}
}

func TestNodeHelpersPickToolsFromTheRepository(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	for _, test := range []struct{ lockfile, manager string }{
		{"", "npm"}, {"pnpm-lock.yaml", "pnpm"}, {"yarn.lock", "yarn"}, {"bun.lock", "bun"}, {"bun.lockb", "bun"}, {"package-lock.json", "npm"},
	} {
		dir := filepath.Join(repo, "case-"+test.manager+test.lockfile)
		mustMkdirAll(t, dir)
		if test.lockfile != "" {
			mustWrite(t, filepath.Join(dir, test.lockfile), "x\n")
		}
		if got := packageManager(repo, filepath.Base(dir)); got != test.manager {
			t.Errorf("%q: manager %q", test.lockfile, got)
		}
	}
	for manager, want := range map[string]string{
		"pnpm": "pnpm exec nx a", "yarn": "yarn nx a", "bun": "bun x nx a", "npm": "npx --no-install nx a",
	} {
		if got := strings.Join(execTool(manager, "nx", "a"), " "); got != want {
			t.Errorf("%s: %q", manager, got)
		}
	}
	mustWrite(t, filepath.Join(repo, "bad.json"), "{")
	if hasPackageScript(filepath.Join(repo, "absent.json"), "lint") || hasPackageScript(filepath.Join(repo, "bad.json"), "lint") {
		t.Fatal("a missing or invalid manifest declared a script")
	}
	if !isLintableScript("a/b.cts") || isLintableScript("a/b.json") || !ignoredNodeDir("x/node_modules/y") || ignoredNodeDir("src/app") {
		t.Fatal("script classification")
	}
	if hasInstalledDependencies(repo, "deep/inside") {
		t.Fatal("found node_modules that does not exist")
	}
}

func TestNodeToolsPresentReportsTheMissingTool(t *testing.T) {
	t.Parallel()
	h := newLintHarness(t, t.TempDir())
	options := withLintDefaults(h.options("pre-push", "node"))
	options.Tools.LookPath = func(string) (string, error) { return "", errors.New("absent") }
	runner := lintRunner{options: options}
	if ok, blocked := runner.nodeToolsPresent("pnpm", "."); ok || !blocked {
		t.Fatalf("ok %v blocked %v", ok, blocked)
	}
}
