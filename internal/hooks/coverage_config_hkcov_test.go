package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hkCovWriteGlobalYAML writes a raw global wb.yaml under the isolated
// XDG_CONFIG_HOME and returns its path.
func hkCovWriteGlobalYAML(t *testing.T, content string) string {
	t.Helper()
	dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "wb")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "wb.yaml")
	mustWrite(t, path, content)
	return path
}

// hkCovRepoConfig writes .wb/hooks.yaml in repo and returns its path.
func hkCovRepoConfig(t *testing.T, repo, content string) string {
	t.Helper()
	dir := filepath.Join(repo, ".wb")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "hooks.yaml")
	mustWrite(t, path, content)
	return path
}

func TestHkCovLoadPolicyRejectsANonRepositoryPath(t *testing.T) {
	isolateEnvironment(t)
	if _, err := LoadPolicy(t.TempDir(), ""); err == nil || !strings.Contains(err.Error(), "not a Git worktree") {
		t.Fatalf("LoadPolicy(non-repo) error = %v, want a not-a-worktree error", err)
	}
}

func TestHkCovLoadPolicyRejectsMalformedExplicitConfigs(t *testing.T) {
	repo := initRepo(t)
	isolateEnvironment(t)
	dir := t.TempDir()
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{"invalid yaml", "version: [\n", "parse hooks config"},
		{"version mismatch", "version: 2\n", "supported version is 1"},
		{"multiple documents", "version: 1\n---\nversion: 1\n", "multiple YAML documents"},
		{"invalid hook name", "version: 1\nhooks:\n  Bad_Name:\n    template: x.sh\n", "invalid hook name"},
		{"missing template", "version: 1\nhooks:\n  pre-commit:\n    disabled: false\n", "requires template or disabled"},
		{"invalid include profile", "version: 1\nprofiles:\n  include: [Bad_Name]\n", "invalid profile name"},
		{"invalid exclude profile", "version: 1\nprofiles:\n  exclude: [Bad_Name]\n", "invalid profile name"},
		{"invalid definition name", "version: 1\nprofiles:\n  definitions:\n    Bad_Name: {}\n", "invalid profile name"},
		{"invalid metrics label", "version: 1\nmetrics:\n  labels:\n    Bad_Label: x\n", "invalid metrics label"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(dir, "hooks.yaml")
			mustWrite(t, path, test.content)
			_, err := LoadPolicy(repo, path)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("LoadPolicy error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestHkCovLoadPolicyReportsUnreadableExplicitConfig(t *testing.T) {
	repo := initRepo(t)
	isolateEnvironment(t)
	parent := filepath.Join(t.TempDir(), "regular-file")
	mustWrite(t, parent, "not a directory\n")
	if _, err := LoadPolicy(repo, filepath.Join(parent, "hooks.yaml")); err == nil || !strings.Contains(err.Error(), "read hooks config") {
		t.Fatalf("LoadPolicy error = %v, want a read error", err)
	}

	directory := t.TempDir()
	if _, err := LoadPolicy(repo, directory); err == nil || !strings.Contains(err.Error(), "parse hooks config") {
		t.Fatalf("LoadPolicy(directory config) error = %v, want a parse error", err)
	}
}

func TestHkCovLoadPolicyResolvesBuiltinAndAbsoluteTemplates(t *testing.T) {
	repo := initRepo(t)
	isolateEnvironment(t)
	absolute := filepath.Join(t.TempDir(), "pre-commit.sh")
	mustWrite(t, absolute, "#!/bin/sh\nexit 0\n")
	config := filepath.Join(t.TempDir(), "hooks.yaml")
	mustWrite(t, config, "version: 1\nhooks:\n  pre-commit:\n    template: builtin:pre-commit\n  pre-push:\n    template: "+absolute+"\n")
	policy, err := LoadPolicy(repo, config)
	if err != nil {
		t.Fatal(err)
	}
	if got := policy.Hooks["pre-commit"].Template; got != BuiltinPreCommit || !policy.Hooks["pre-commit"].Builtin {
		t.Fatalf("pre-commit = %#v, want the builtin template kept verbatim", policy.Hooks["pre-commit"])
	}
	if got := policy.Hooks["pre-push"].Template; got != absolute || policy.Hooks["pre-push"].Builtin {
		t.Fatalf("pre-push = %#v, want the absolute path", policy.Hooks["pre-push"])
	}
}

func TestHkCovLoadPolicyRejectsInvalidResolvedHooks(t *testing.T) {
	repo := initRepo(t)
	isolateEnvironment(t)
	dir := t.TempDir()
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{"unknown builtin", "version: 1\nhooks:\n  pre-commit:\n    template: builtin:nope\n", "unknown template"},
		{"profile unknown builtin", "version: 1\nprofiles:\n  definitions:\n    custom:\n      hooks:\n        pre-commit:\n          template: builtin:nope\n", "unknown template"},
		{"profile hook invalid name", "version: 1\nprofiles:\n  definitions:\n    custom:\n      hooks:\n        Bad_Name:\n          template: x.sh\n", "invalid hook name"},
		{"profile hook missing template", "version: 1\nprofiles:\n  definitions:\n    custom:\n      hooks:\n        pre-commit:\n          disabled: false\n", "requires template or disabled"},
		{"profile detection escapes root", "version: 1\nprofiles:\n  definitions:\n    custom:\n      detect:\n        any_files: ['../escape']\n", "must be a non-empty repository-relative path"},
		{"profile detection bad glob", "version: 1\nprofiles:\n  definitions:\n    custom:\n      detect:\n        any_files: ['[']\n", "invalid detection pattern"},
		{"profile detection through a file", "version: 1\nprofiles:\n  definitions:\n    custom:\n      detect:\n        all_files: ['go.mod/child']\n", "not a directory"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			// Every case except the glob one needs a real go.mod for the
			// through-a-file case to produce ENOTDIR rather than ENOENT.
			mustWrite(t, filepath.Join(repo, "go.mod"), "module example.invalid/x\n")
			path := filepath.Join(dir, test.name+".yaml")
			mustWrite(t, path, test.content)
			_, err := LoadPolicy(repo, path)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("LoadPolicy error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestHkCovLoadPolicyRejectsDirectoryTemplateNotRegular(t *testing.T) {
	repo := initRepo(t)
	isolateEnvironment(t)
	mustMkdirAll(t, filepath.Join(repo, "templates", "pre-commit.sh"))
	path := filepath.Join(repo, ".wb", "hooks.yaml")
	hkCovRepoConfig(t, repo, "version: 1\nhooks:\n  pre-commit:\n    template: ../templates/pre-commit.sh\n")
	_, err := LoadPolicy(repo, "")
	if err == nil || !strings.Contains(err.Error(), "is not a regular file") {
		t.Fatalf("LoadPolicy error = %v, want a not-a-regular-file error", err)
	}
	_ = path
}

// TestHkCovLoadWBConfigGitHooksBranches drives each decode branch inside the
// global wb.yaml git_hooks reader.
func TestHkCovLoadWBConfigGitHooksBranches(t *testing.T) {
	repo := initRepo(t)
	cases := []struct {
		name       string
		content    string
		wantErr    string
		wantConfig bool
	}{
		{"no git_hooks section", "version: 1\n", "", false},
		{"null git_hooks section", "git_hooks:\n", "", false},
		{"unknown sibling key first", "other: 1\ngit_hooks:\n  version: 1\n", "", true},
		{"top level sequence", "- one\n- two\n", "top level must be a mapping", false},
		{"duplicate git_hooks", "git_hooks:\n  version: 1\ngit_hooks:\n  version: 1\n", "duplicate git_hooks section", false},
		{"malformed document", "git_hooks: [\n", "parse hooks config", false},
		{"multiple documents", "git_hooks:\n  version: 1\n---\nother: 1\n", "multiple YAML documents", false},
		{"malformed second document", "git_hooks:\n  version: 1\n---\n[\n", "parse hooks config", false},
		{"unknown field", "git_hooks:\n  version: 1\n  bogus: true\n", "field bogus not found", false},
		{"version mismatch", "git_hooks:\n  version: 2\n", "supported version is 1", false},
		{"valid section", "git_hooks:\n  version: 1\n  metrics:\n    enabled: false\n", "", true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			isolateEnvironment(t)
			hkCovWriteGlobalYAML(t, test.content)
			policy, err := LoadPolicy(repo, "")
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("LoadPolicy error = %v, want success", err)
				}
				if got := len(policy.ConfigPaths) > 0; got != test.wantConfig {
					t.Fatalf("ConfigPaths = %v, wantConfig %v", policy.ConfigPaths, test.wantConfig)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("LoadPolicy error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestHkCovLoadWBConfigGitHooksReportsOpenFailure(t *testing.T) {
	repo := initRepo(t)
	isolateEnvironment(t)
	// A non-directory at $XDG_CONFIG_HOME/wb makes os.Open fail with ENOTDIR
	// rather than ENOENT, which must be reported instead of treated as absent.
	configHome := os.Getenv("XDG_CONFIG_HOME")
	mustWrite(t, filepath.Join(configHome, "wb"), "not a directory\n")
	if _, err := LoadPolicy(repo, ""); err == nil || !strings.Contains(err.Error(), "read hooks config") {
		t.Fatalf("LoadPolicy error = %v, want a read error", err)
	}
}

func TestHkCovLoadPolicyReportsRepositoryConfigErrors(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(t *testing.T, repo string)
		wantErr string
	}{
		{
			name: "unreadable repository config",
			setup: func(t *testing.T, repo string) {
				parent := filepath.Join(repo, ".wb")
				mustWrite(t, parent, "not a directory\n")
			},
			wantErr: "read hooks config",
		},
		{
			name: "repository config is a directory",
			setup: func(t *testing.T, repo string) {
				mustMkdirAll(t, filepath.Join(repo, ".wb", "hooks.yaml"))
			},
			wantErr: "parse hooks config",
		},
		{
			name: "repository config version mismatch",
			setup: func(t *testing.T, repo string) {
				hkCovRepoConfig(t, repo, "version: 2\n")
			},
			wantErr: "supported version is 1",
		},
		{
			name: "repository config multiple documents",
			setup: func(t *testing.T, repo string) {
				hkCovRepoConfig(t, repo, "version: 1\n---\nversion: 1\n")
			},
			wantErr: "multiple YAML documents",
		},
		{
			name: "repository config malformed second document",
			setup: func(t *testing.T, repo string) {
				hkCovRepoConfig(t, repo, "version: 1\n---\n[\n")
			},
			wantErr: "parse hooks config",
		},
		{
			name: "repository config invalid hook name",
			setup: func(t *testing.T, repo string) {
				hkCovRepoConfig(t, repo, "version: 1\nhooks:\n  Bad_Name:\n    template: x.sh\n")
			},
			wantErr: "invalid hook name",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			repo := initRepo(t)
			isolateEnvironment(t)
			test.setup(t, repo)
			if _, err := LoadPolicy(repo, ""); err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("LoadPolicy error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

// TestHkCovLoadPolicyReportsGlobalApplyFailure covers the global layer passing
// through the shared applyFile validation.
func TestHkCovLoadPolicyReportsGlobalApplyFailure(t *testing.T) {
	repo := initRepo(t)
	isolateEnvironment(t)
	hkCovWriteGlobalYAML(t, "git_hooks:\n  version: 1\n  hooks:\n    Bad_Name:\n      template: x.sh\n")
	if _, err := LoadPolicy(repo, ""); err == nil || !strings.Contains(err.Error(), "invalid hook name") {
		t.Fatalf("LoadPolicy error = %v, want an invalid-hook-name error", err)
	}

	isolateEnvironment(t)
	hkCovWriteGlobalYAML(t, "git_hooks:\n  version: 1\n  metrics:\n    labels:\n      Bad_Label: x\n")
	if _, err := LoadPolicy(repo, ""); err == nil || !strings.Contains(err.Error(), "invalid metrics label") {
		t.Fatalf("LoadPolicy error = %v, want an invalid-metrics-label error", err)
	}
}

func TestHkCovDefaultMetricsPathHonoursEnvironment(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	if got, want := defaultMetricsPath(), filepath.Join(state, "wb", "hook-events.jsonl"); got != want {
		t.Fatalf("defaultMetricsPath() = %q, want %q", got, want)
	}

	t.Setenv("XDG_STATE_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got, want := defaultMetricsPath(), filepath.Join(home, ".local", "state", "wb", "hook-events.jsonl"); got != want {
		t.Fatalf("defaultMetricsPath() = %q, want %q", got, want)
	}

	t.Setenv("HOME", "")
	if got, want := defaultMetricsPath(), filepath.Join(".wb", "hook-events.jsonl"); got != want {
		t.Fatalf("defaultMetricsPath() without a home = %q, want %q", got, want)
	}
}

func TestHkCovExpandPathHandlesHomeAndErrors(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got, want := expandPath("~/hooks/x.sh"), filepath.Join(home, "hooks", "x.sh"); got != want {
		t.Fatalf("expandPath(~/hooks/x.sh) = %q, want %q", got, want)
	}
	if got := expandPath("/absolute/x.sh"); got != "/absolute/x.sh" {
		t.Fatalf("expandPath(absolute) = %q, want it unchanged", got)
	}
	// A bare ~ is not expanded by this helper; only a "~/" prefix is.
	if got := expandPath("~x"); got != "~x" {
		t.Fatalf("expandPath(~x) = %q, want it unchanged", got)
	}

	t.Setenv("HOME", "")
	if got := expandPath("~/hooks/x.sh"); got != "~/hooks/x.sh" {
		t.Fatalf("expandPath without a home = %q, want the path unchanged", got)
	}
}

func TestHkCovBuiltinTemplateRejectsUnknownNames(t *testing.T) {
	t.Parallel()
	if content, ok := builtinTemplate("builtin:does-not-exist"); ok || content != "" {
		t.Fatalf("builtinTemplate(unknown) = %q, %v; want empty and false", content, ok)
	}
	if _, ok := builtinTemplate(BuiltinPreCommit); !ok {
		t.Fatal("builtinTemplate(builtin:pre-commit) should be known")
	}
}

// TestHkCovApplyProfilesDirectBranches exercises applyProfiles paths that
// LoadPolicy cannot reach because its own layering always initialises Hooks.
func TestHkCovApplyProfilesDirectBranches(t *testing.T) {
	t.Parallel()
	repo := initRepo(t)
	policy := defaultPolicy(repo)
	policy.ProfileDefinitions["ghost"] = ProfileDefinition{Name: "ghost"}
	config := filepath.Join(t.TempDir(), "hooks.yaml")
	order := 7
	err := applyProfiles(&policy, config, ProfilesConfig{
		Auto:        hkCovBool(true),
		Include:     []string{"ghost"},
		Definitions: map[string]ProfileDefinitionConfig{"ghost": {Order: &order, Detect: &ProfileDetection{AnyFiles: []string{"go.mod"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	definition := policy.ProfileDefinitions["ghost"]
	if definition.Hooks == nil {
		t.Fatal("applyProfiles left a nil Hooks map; later merges would panic")
	}
	if !policy.ProfilesAuto || definition.Order != 7 {
		t.Fatalf("profiles auto = %v, ghost order = %d; want true and 7", policy.ProfilesAuto, definition.Order)
	}
	if !policy.ProfileSelections["ghost"] {
		t.Fatalf("ghost selection = %v, want true", policy.ProfileSelections)
	}
}

func TestHkCovResolveProfilesAutoDetectionBranches(t *testing.T) {
	repo := initRepo(t)
	isolateEnvironment(t)
	mustWrite(t, filepath.Join(repo, "go.mod"), "module example.invalid/x\n")

	// An empty detection block never matches.
	hkCovRepoConfig(t, repo, "version: 1\nprofiles:\n  auto: true\n  definitions:\n    blank:\n      detect: {}\n")
	policy, err := LoadPolicy(repo, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, active := range policy.ActiveProfiles {
		if active.Name == "blank" {
			t.Fatalf("blank profile activated with no detection rule: %#v", policy.ActiveProfiles)
		}
	}

	// Any-files that do not match leaves the profile inactive.
	isolateEnvironment(t)
	hkCovRepoConfig(t, repo, "version: 1\nprofiles:\n  auto: true\n  definitions:\n    absent:\n      detect:\n        any_files: ['nope.txt']\n")
	policy, err = LoadPolicy(repo, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, active := range policy.ActiveProfiles {
		if active.Name == "absent" {
			t.Fatalf("absent profile activated: %#v", policy.ActiveProfiles)
		}
	}

	// An any-files pattern that escapes the repository root fails detection.
	isolateEnvironment(t)
	hkCovRepoConfig(t, repo, "version: 1\nprofiles:\n  auto: true\n  definitions:\n    escape:\n      detect:\n        any_files: ['../escape']\n")
	if _, err := LoadPolicy(repo, ""); err == nil || !strings.Contains(err.Error(), "escape") {
		t.Fatalf("LoadPolicy error = %v, want a detection failure for the escaping pattern", err)
	}
}

// TestHkCovMatchProfileAndGlobBranches exercises matchRepositoryPath directly so
// the glob and through-a-file branches are asserted on their returned values.
func TestHkCovMatchProfileAndGlobBranches(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	mustWrite(t, filepath.Join(repo, "go.mod"), "module example.invalid/x\n")
	mustWrite(t, filepath.Join(repo, "a_test.go"), "package a\n")

	matched, matches, err := matchProfile(repo, ProfileDetection{})
	if err != nil || matched || matches != nil {
		t.Fatalf("matchProfile(empty) = %v, %v, %v; want false, nil, nil", matched, matches, err)
	}
	matched, matchedPath, err := matchRepositoryPath(repo, "*.go")
	if err != nil || !matched || len(matchedPath) == 0 {
		t.Fatalf("matchRepositoryPath glob = %v, %q, %v; want a match", matched, matchedPath, err)
	}
	matched, matchedPath, err = matchRepositoryPath(repo, "zzz*")
	if err != nil || matched || matchedPath != "" {
		t.Fatalf("matchRepositoryPath unmatched glob = %v, %q, %v; want no match", matched, matchedPath, err)
	}
	if _, _, err := matchRepositoryPath(repo, "["); err == nil || !strings.Contains(err.Error(), "invalid detection pattern") {
		t.Fatalf("matchRepositoryPath bad glob error = %v", err)
	}
	if _, _, err := matchRepositoryPath(repo, "../outside"); err == nil || !strings.Contains(err.Error(), "repository-relative") {
		t.Fatalf("matchRepositoryPath escaping error = %v", err)
	}
	if _, _, err := matchRepositoryPath(repo, "go.mod/child"); err == nil {
		t.Fatal("matchRepositoryPath through a regular file should fail with ENOTDIR")
	}
	matched, matchedPath, err = matchRepositoryPath(repo, "go.mod")
	if err != nil || !matched || matchedPath != "go.mod" {
		t.Fatalf("matchRepositoryPath literal = %v, %q, %v", matched, matchedPath, err)
	}
	matched, _, err = matchProfile(repo, ProfileDetection{AllFiles: []string{"go.mod"}, AnyFiles: []string{"absent", "a_test.go"}})
	if err != nil || !matched {
		t.Fatalf("matchProfile with one matching any-file = %v, %v; want true", matched, err)
	}
	matched, _, err = matchProfile(repo, ProfileDetection{AnyFiles: []string{"absent", "also-absent"}})
	if err != nil || matched {
		t.Fatalf("matchProfile with no matching any-file = %v, %v; want false", matched, err)
	}
}

// TestHkCovHookBlocksSkipsEmptyProfileHooks asserts a profile entry whose hook
// is disabled or template-less contributes no block.
func TestHkCovHookBlocksSkipsEmptyProfileHooks(t *testing.T) {
	t.Parallel()
	repo := initRepo(t)
	policy := defaultPolicy(repo)
	policy.ProfileDefinitions["custom"] = ProfileDefinition{
		Name:      "custom",
		Order:     50,
		Detection: ProfileDetection{},
		Hooks: map[string]ResolvedHook{
			"pre-commit": {Name: "pre-commit", Disabled: true},
			"pre-push":   {Name: "pre-push", Template: ""},
		},
	}
	policy.ActiveProfiles = []ActiveProfile{{Name: "custom", Order: 50, Reason: "test"}}
	blocks := hookBlocks(policy, "pre-commit")
	for _, block := range blocks {
		if block.Profile == "custom" {
			t.Fatalf("disabled profile hook produced a block: %#v", block)
		}
	}
	if got := hookBlocks(policy, "commit-msg"); len(got) != 0 {
		t.Fatalf("hookBlocks(commit-msg) = %#v, want no blocks for an undefined hook", got)
	}
}

func hkCovBool(value bool) *bool { return &value }
