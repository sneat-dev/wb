package lifecyclehooks

import (
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestHkCovLoadMissingConfigIsNotConfigured(t *testing.T) {
	t.Parallel()
	_, found, err := Load(filepath.Join(t.TempDir(), "absent", "wb.yaml"))
	if err != nil || found {
		t.Fatalf("found=%t err=%v, want not configured without error", found, err)
	}
}

func TestHkCovLoadRejectsMalformedYAML(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"tab indentation":   "hooks:\n\tversion: 1\n",
		"unterminated flow": "hooks: {version: 1",
		"bad scalar":        "hooks: ][\n",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := hkCovWriteFile(t, filepath.Join(t.TempDir(), "wb.yaml"), raw, 0o600)
			_, _, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), "parse lifecycle hooks config") {
				t.Fatalf("error=%v, want parse failure", err)
			}
		})
	}
}

func TestHkCovLoadRejectsSecondDocumentAndMultipleDocuments(t *testing.T) {
	t.Parallel()
	single := "hooks:\n  version: 1\n  executors:\n    index:\n      run: /nonexistent/indexer\n      cwd: repository\n      mode: coalesced\n      failure: warn\n  bindings:\n    - on: [checkout-updated]\n      match:\n        repositories:\n          include: [github.com/*/*]\n      execute: [index]\n"
	cases := map[string]string{
		"second document malformed": single + "---\nfoo: [1, 2\n",
		"multiple documents":        single + "---\nversion: 1\n",
		"trailing document":         single + "---\nnull\n",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := hkCovWriteFile(t, filepath.Join(t.TempDir(), "wb.yaml"), raw, 0o600)
			_, _, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), "parse lifecycle hooks config") {
				t.Fatalf("error=%v, want parse failure", err)
			}
		})
	}
}

func TestHkCovLoadRejectsNonMappingTopLevel(t *testing.T) {
	t.Parallel()
	path := hkCovWriteFile(t, filepath.Join(t.TempDir(), "wb.yaml"), "- hooks\n- version\n", 0o600)
	_, _, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "top level must be a mapping") {
		t.Fatalf("error=%v, want mapping error", err)
	}
}

func TestHkCovLoadRejectsDuplicateHooksSection(t *testing.T) {
	t.Parallel()
	raw := "hooks:\n  version: 1\nhooks:\n  version: 1\n"
	path := hkCovWriteFile(t, filepath.Join(t.TempDir(), "wb.yaml"), raw, 0o600)
	_, _, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), `duplicate top-level "hooks" section`) {
		t.Fatalf("error=%v, want duplicate section error", err)
	}
}

func TestHkCovMappingValueHandlesEmptyDocument(t *testing.T) {
	t.Parallel()
	var document yaml.Node
	if err := yaml.Unmarshal([]byte("# only a comment\n"), &document); err != nil {
		t.Fatal(err)
	}
	node, found, err := mappingValue(document, "hooks")
	if err != nil || found || node.Kind != 0 {
		t.Fatalf("node=%+v found=%t err=%v, want no content and no error", node, found, err)
	}
}

func TestHkCovLoadRejectsInvalidHooksConfigurations(t *testing.T) {
	t.Parallel()
	base := "hooks:\n  version: 1\n  executors:\n    index:\n      run: /usr/bin/true\n      cwd: repository\n      mode: coalesced\n      failure: warn\n  bindings:\n    - on: [checkout-updated]\n      match:\n        repositories:\n          include: [github.com/*/*]\n      execute: [index]\n"
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"wrong version", strings.Replace(base, "version: 1", "version: 2", 1), "supported version is 1"},
		{"no executors", "hooks:\n  version: 1\n  executors: {}\n  bindings:\n    - on: [checkout-updated]\n      match:\n        repositories:\n          include: [github.com/*/*]\n      execute: [index]\n", "hooks.executors must not be empty"},
		{"invalid executor name", strings.Replace(base, "    index:", "    Bad_Name:", 1), "invalid executor name"},
		{"missing run", strings.Replace(base, "      run: /usr/bin/true\n", "", 1), `executor "index" requires run`},
		{"wrong cwd", strings.Replace(base, "cwd: repository", "cwd: elsewhere", 1), `executor "index" cwd must be repository`},
		{"wrong mode", strings.Replace(base, "mode: coalesced", "mode: batch", 1), `executor "index" mode must be coalesced`},
		{"wrong failure", strings.Replace(base, "failure: warn", "failure: fail", 1), `executor "index" failure must be warn`},
		{"unparseable timeout", strings.Replace(base, "failure: warn", "failure: warn\n      timeout: soon", 1), `must be a positive duration`},
		{"non-positive timeout", strings.Replace(base, "failure: warn", "failure: warn\n      timeout: -1s", 1), `must be a positive duration`},
		{"no bindings", "hooks:\n  version: 1\n  executors:\n    index:\n      run: /usr/bin/true\n      cwd: repository\n      mode: coalesced\n      failure: warn\n  bindings: []\n", "hooks.bindings must not be empty"},
		{"binding without on", "hooks:\n  version: 1\n  executors:\n    index:\n      run: /usr/bin/true\n      cwd: repository\n      mode: coalesced\n      failure: warn\n  bindings:\n    - match:\n        repositories:\n          include: [github.com/*/*]\n      execute: [index]\n", "binding 1 requires on"},
		{"unsupported event", strings.Replace(base, "on: [checkout-updated]", "on: [checkout-created]", 1), "unsupported event"},
		{"no include", strings.Replace(base, "          include: [github.com/*/*]\n", "", 1), "requires match.repositories.include"},
		{"bad glob", strings.Replace(base, "include: [github.com/*/*]", `include: ["["]`, 1), "invalid repository glob"},
		{"no execute", strings.Replace(base, "      execute: [index]", "      execute: []", 1), "binding 1 requires execute"},
		{"unknown executor", strings.Replace(base, "      execute: [index]", "      execute: [missing]", 1), `references unknown executor "missing"`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			path := hkCovWriteFile(t, filepath.Join(t.TempDir(), "wb.yaml"), testCase.raw, 0o600)
			_, _, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error=%v, want %q", err, testCase.want)
			}
		})
	}
}

func TestHkCovBindingMatchesRequiresConfiguredEvent(t *testing.T) {
	t.Parallel()
	binding := Binding{On: []string{EventCheckoutUpdated}, Match: Match{Repositories: RepositoryMatch{Include: []string{"github.com/*/*"}}}}
	if binding.matches(Event{Name: "some-other-event", Repository: "github.com/acme/app"}) {
		t.Fatal("binding without the matching event must not match")
	}
	if contains([]string{"a", "b"}, "c") {
		t.Fatal("contains must report a missing value as false")
	}
	if !contains([]string{"a", "b"}, "b") {
		t.Fatal("contains must find an existing value")
	}
}
