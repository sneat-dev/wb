package hooks

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNodePrePushRunsNxAffectedLintAgainstThePushBase(t *testing.T) {
	t.Parallel()
	h := newLintHarness(t, t.TempDir())
	h.files(map[string]string{"nx.json": "{}\n", "pnpm-lock.yaml": "x\n", "node_modules/.keep": ""})
	stdin := h.push(sha('a'), sha('0'), "libs/a/src/x.ts") + "refs/heads/feature " + sha('a') + " refs/heads/other " + zeroOID40 + "\n"
	if code := h.runWithStdin("pre-push", "node", stdin); code != 0 {
		t.Fatalf("exit %d: %s", code, h.stderr.String())
	}
	want := []string{".|pnpm exec nx affected -t lint --base=" + sha('0') + " --head=" + sha('a')}
	if !reflect.DeepEqual(h.calls, want) {
		t.Fatalf("calls\n got %q\nwant %q", h.calls, want)
	}

	h.calls = nil
	h.exit = func([]string) (int, error) { return 1, nil }
	if code := h.runWithStdin("pre-push", "node", stdin); code != 1 || !strings.Contains(h.stderr.String(), "nx lint failed") {
		t.Fatalf("failing nx: exit %d stderr %q", code, h.stderr.String())
	}
}

func TestNodePrePushLintsEveryNxProjectWithoutALocalBase(t *testing.T) {
	t.Parallel()
	h := newLintHarness(t, t.TempDir())
	h.files(map[string]string{"nx.json": "{}\n", "node_modules/.keep": ""})
	h.git.defaultBranch = ""
	h.terminal = true
	if code := h.run("pre-push", "node"); code != 0 || !reflect.DeepEqual(h.calls, []string{".|npm exec --no -- nx run-many -t lint"}) {
		t.Fatalf("exit %d calls %q", code, h.calls)
	}
}

func TestNodePrePushNxNeedsItsToolsAndDependencies(t *testing.T) {
	t.Parallel()
	h := newLintHarness(t, t.TempDir())
	h.files(map[string]string{"nx.json": "{}\n"})
	stdin := h.push(sha('a'), sha('0'), "a.ts")
	if code := h.runWithStdin("pre-push", "node", stdin); code != 0 || len(h.calls) != 0 || !strings.Contains(h.stderr.String(), "no node_modules") {
		t.Fatalf("no dependencies: exit %d calls %q stderr %q", code, h.calls, h.stderr.String())
	}
	h.missing["npm"] = true
	if code := h.runWithStdin("pre-push", "node", stdin); code != 1 || !strings.Contains(h.stderr.String(), "not found on PATH: npm") {
		t.Fatalf("no package manager: exit %d stderr %q", code, h.stderr.String())
	}
}

func TestNodePrePushRunsEachTouchedPackagesOwnLintScript(t *testing.T) {
	t.Parallel()
	h := newLintHarness(t, t.TempDir())
	h.files(map[string]string{
		"web/package.json":       `{"scripts":{"lint":"eslint ."}}`,
		"web/yarn.lock":          "x\n",
		"web/node_modules/.keep": "",
		"tools/package.json":     `{"scripts":{"test":"x"}}`,
		"api/package.json":       `{"scripts":{"lint":"x"}}`,
	})
	stdin := h.push(sha('a'), sha('0'), "web/src/a.tsx", "web/src/b.ts", "tools/c.mjs", "api/d.js", "docs/readme.md", "web/node_modules/dep/i.js")
	if code := h.runWithStdin("pre-push", "node", stdin); code != 0 {
		t.Fatalf("exit %d: %s", code, h.stderr.String())
	}
	if want := []string{"web|yarn run lint"}; !reflect.DeepEqual(h.calls, want) {
		t.Fatalf("calls\n got %q\nwant %q", h.calls, want)
	}
	if !strings.Contains(h.stderr.String(), "dependencies are not installed for api") {
		t.Fatalf("stderr %q", h.stderr.String())
	}

	h.exit = func([]string) (int, error) { return 2, nil }
	if code := h.runWithStdin("pre-push", "node", stdin); code != 1 || !strings.Contains(h.stderr.String(), "lint failed in package web") {
		t.Fatalf("failing lint: exit %d stderr %q", code, h.stderr.String())
	}
	h.missing["node"] = true
	if code := h.runWithStdin("pre-push", "node", stdin); code != 1 || !strings.Contains(h.stderr.String(), "not found on PATH: node") {
		t.Fatalf("no node: exit %d stderr %q", code, h.stderr.String())
	}
}

func TestNodePrePushLintsEveryPackageWhenThePushCannotBeRead(t *testing.T) {
	t.Parallel()
	h := newLintHarness(t, t.TempDir())
	h.files(map[string]string{"package.json": `{"scripts":{"lint":"x"}}`, "node_modules/m/package.json": "{}"})
	if code := h.runWithStdin("pre-push", "node", pushLine("f", sha('e'))); code != 0 {
		t.Fatalf("exit %d: %s", code, h.stderr.String())
	}
	if want := []string{".|npm run lint"}; !reflect.DeepEqual(h.calls, want) {
		t.Fatalf("calls %q", h.calls)
	}
}

func TestNodePrePushSkipsAPushWithoutScriptsOrAtTierZero(t *testing.T) {
	t.Parallel()
	h := newLintHarness(t, t.TempDir())
	h.files(map[string]string{"package.json": `{"scripts":{"lint":"x"}}`})
	stdin := h.push(sha('a'), sha('0'), "README.md")
	if code := h.runWithStdin("pre-push", "node", stdin); code != 0 || len(h.calls) != 0 || !strings.Contains(h.stderr.String(), "changes no JS/TS source") {
		t.Fatalf("exit %d calls %q", code, h.calls)
	}
	h.tier = Classification{Tier: TierSkip, Reason: "checkpoint"}
	if code := h.runWithStdin("pre-push", "node", stdin); code != 0 || len(h.calls) != 0 {
		t.Fatalf("tier 0: exit %d calls %q", code, h.calls)
	}
}

func TestNodePrePushLintsAfterAConfigurationOnlyChange(t *testing.T) {
	t.Parallel()
	for _, config := range []string{"package.json", "tsconfig.base.json", ".eslintrc.json", "eslint.config.mjs", "project.json"} {
		h := newLintHarness(t, t.TempDir())
		h.files(map[string]string{"package.json": `{"scripts":{"lint":"x"}}`, "node_modules/.keep": ""})
		stdin := h.push(sha('a'), sha('0'), config)
		if code := h.runWithStdin("pre-push", "node", stdin); code != 0 || !reflect.DeepEqual(h.calls, []string{".|npm run lint"}) {
			t.Errorf("%s: exit %d calls %q", config, code, h.calls)
		}
	}
}

func TestNodePrePushLeavesALintThatRanPastItsDeadlineToCI(t *testing.T) {
	t.Parallel()
	h := newLintHarness(t, t.TempDir())
	h.files(map[string]string{"package.json": `{"scripts":{"lint":"x"}}`, "node_modules/.keep": ""})
	stdin := h.push(sha('a'), sha('0'), "a.js")
	h.exit = func([]string) (int, error) { return 0, fmt.Errorf("npm %w (10m0s)", errCheckTimedOut) }
	if code := h.runWithStdin("pre-push", "node", stdin); code != 0 || !strings.Contains(h.stderr.String(), "lint did not finish in package .") {
		t.Fatalf("exit %d stderr %q", code, h.stderr.String())
	}
	n := newLintHarness(t, t.TempDir())
	n.files(map[string]string{"nx.json": "{}\n", "node_modules/.keep": ""})
	nxStdin := n.push(sha('a'), sha('0'), "a.ts")
	n.exit = h.exit
	if code := n.runWithStdin("pre-push", "node", nxStdin); code != 0 || !strings.Contains(n.stderr.String(), "nx lint did not finish") {
		t.Fatalf("nx: exit %d stderr %q", code, n.stderr.String())
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
		"pnpm": "pnpm exec nx a", "yarn": "yarn nx a", "bun": "bun x nx a", "npm": "npm exec --no -- nx a",
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
