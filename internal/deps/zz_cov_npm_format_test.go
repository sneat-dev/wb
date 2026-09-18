package deps

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// This file drives the npm lockfile indexers, the package.json dependency
// scanner/rewriter, and the pnpm-workspace.yaml parser/renderer through the
// branches the pre-existing tests never reached: multi-importer pnpm locks,
// package-lock nested node_modules install paths, unindexable lockfiles,
// CRLF-preserving rewrites, escaped YAML quotes, and malformed mapping lines.
// Every helper and fixture name introduced here is prefixed `depsCov` so it
// cannot collide with the other coverage files landing in this package.

func TestDepsCovNpmFormatLockedVersionResolution(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		values   []string
		version  string
		conflict string
	}{
		{name: "no values", values: nil, version: "", conflict: ""},
		{name: "single value", values: []string{"1.2.3"}, version: "1.2.3", conflict: ""},
		{
			name: "two values", values: []string{"1.2.3", "1.2.4"},
			version: "", conflict: "lockfile importers pin conflicting versions: 1.2.3, 1.2.4",
		},
		{
			name: "three values", values: []string{"0.30.1", "0.30.4", "0.31.0"},
			version: "", conflict: "lockfile importers pin conflicting versions: 0.30.1, 0.30.4, 0.31.0",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			locked := npmLockedVersion{Values: test.values, Source: "pnpm-lock.yaml"}
			if got := locked.Version(); got != test.version {
				t.Fatalf("Version() = %q, want %q", got, test.version)
			}
			if got := locked.Conflict(); got != test.conflict {
				t.Fatalf("Conflict() = %q, want %q", got, test.conflict)
			}
		})
	}
}

func TestDepsCovNpmFormatMergeSortedUnique(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		existing []string
		addition []string
		want     []string
	}{
		{name: "both empty", existing: nil, addition: nil, want: []string{}},
		{name: "sorts and dedupes across inputs", existing: []string{"b", "a"}, addition: []string{"c", "a"}, want: []string{"a", "b", "c"}},
		{name: "skips empty strings", existing: []string{"", "a"}, addition: []string{"", "b"}, want: []string{"a", "b"}},
		{name: "dedupes inside one input", existing: nil, addition: []string{"1.0.0", "1.0.0", "1.0.1"}, want: []string{"1.0.0", "1.0.1"}},
		{name: "lexicographic order", existing: []string{"1.10.0"}, addition: []string{"1.2.0"}, want: []string{"1.10.0", "1.2.0"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := mergeSortedUnique(test.existing, test.addition)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("mergeSortedUnique(%v, %v) = %v, want %v", test.existing, test.addition, got, test.want)
			}
		})
	}
}

func TestDepsCovNpmFormatCleanPnpmLockVersion(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "strips peer suffix", in: "0.24.3(@angular/core@20.0.0)", want: "0.24.3"},
		{name: "trims surrounding space", in: "  v1.2.3\t", want: "v1.2.3"},
		{name: "keeps bare semver", in: "1.2.3", want: "1.2.3"},
		{name: "keeps v prefixed semver verbatim", in: "v1.2.3", want: "v1.2.3"},
		{name: "keeps minor-only semver", in: "1.2", want: "1.2"},
		{name: "empty stays empty", in: "", want: ""},
		{name: "rejects protocol specifier", in: "link:../local", want: ""},
		{name: "rejects relative path", in: "./local", want: ""},
		{name: "rejects absolute path", in: "/abs/local", want: ""},
		{name: "rejects range", in: "^1.2.3", want: ""},
		{name: "rejects workspace protocol", in: "workspace:*", want: ""},
		{name: "rejects non-version word", in: "not-a-version", want: ""},
		{name: "rejects pure peer suffix", in: "(peer@1.0.0)", want: ""},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := cleanPnpmLockVersion(test.in); got != test.want {
				t.Fatalf("cleanPnpmLockVersion(%q) = %q, want %q", test.in, got, test.want)
			}
		})
	}
}

func TestDepsCovNpmFormatParsePnpmLockVersions(t *testing.T) {
	t.Parallel()
	const contents = `lockfileVersion: '9.0'
importers:
  .:
    dependencies:
      '@sneat/core':
        specifier: ^1.0.0
        version: 1.0.0
      '@sneat/peer':
        specifier: ^4.0.0
        version: 4.0.0(@sneat/core@1.0.0)
      '@sneat/linked':
        specifier: link:../local
        version: 'link:../local'
      '@sneat/empty':
        specifier: '*'
        version: ''
    devDependencies:
      '@sneat/core':
        specifier: ^1.0.1
        version: 1.0.1
    optionalDependencies:
      '@sneat/optional':
        specifier: ^3.0.0
        version: 3.0.0
    peerDependencies:
      '@sneat/requirement':
        specifier: ^5.0.0
        version: 5.0.0
  packages/app:
    dependencies:
      '@sneat/core':
        specifier: ^1.0.1
        version: 1.0.1
`
	versions, err := parsePnpmLockVersions([]byte(contents))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"@sneat/core":        {"1.0.0", "1.0.1"},
		"@sneat/peer":        {"4.0.0"},
		"@sneat/optional":    {"3.0.0"},
		"@sneat/requirement": {"5.0.0"},
	}
	if !reflect.DeepEqual(versions, want) {
		t.Fatalf("parsePnpmLockVersions() = %#v, want %#v", versions, want)
	}

	t.Run("rejects a lockfile with no importers", func(t *testing.T) {
		t.Parallel()
		versions, err := parsePnpmLockVersions([]byte("lockfileVersion: '9.0'\n"))
		if err == nil || err.Error() != "no `importers` section; WB indexes pnpm lockfile versions 6 and 9" {
			t.Fatalf("error = %v", err)
		}
		if versions != nil {
			t.Fatalf("versions = %#v, want nil on error", versions)
		}
	})

	t.Run("rejects malformed yaml", func(t *testing.T) {
		t.Parallel()
		versions, err := parsePnpmLockVersions([]byte("lockfileVersion: '9.0'\nimporters: [\n"))
		if err == nil || !strings.Contains(err.Error(), "yaml:") {
			t.Fatalf("error = %v, want a yaml parse error", err)
		}
		if versions != nil {
			t.Fatalf("versions = %#v, want nil on error", versions)
		}
	})
}

func TestDepsCovNpmFormatParsePackageLockVersions(t *testing.T) {
	t.Parallel()
	const contents = `{
  "lockfileVersion": 3,
  "packages": {
    "": {"name": "app"},
    "node_modules/@sneat/core": {"version": "0.30.2"},
    "packages/app/node_modules/@sneat/core": {"version": "0.30.2"},
    "node_modules/@sneat/core/node_modules/lodash": {"version": "4.17.21"},
    "packages/app/node_modules/local-tool": {"version": "2.0.0"},
    "node_modules/ranged": {"version": "^1.0.0"},
    "node_modules/empty": {}
  }
}
`
	versions, err := parsePackageLockVersions([]byte(contents))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"@sneat/core": {"0.30.2"},
		"lodash":      {"4.17.21"},
		"local-tool":  {"2.0.0"},
	}
	if !reflect.DeepEqual(versions, want) {
		t.Fatalf("parsePackageLockVersions() = %#v, want %#v", versions, want)
	}

	t.Run("rejects a lockfile with no packages", func(t *testing.T) {
		t.Parallel()
		versions, err := parsePackageLockVersions([]byte(`{"lockfileVersion":3}`))
		if err == nil || err.Error() != "no `packages` section; WB indexes package-lock.json versions 2 and 3" {
			t.Fatalf("error = %v", err)
		}
		if versions != nil {
			t.Fatalf("versions = %#v, want nil on error", versions)
		}
	})

	t.Run("rejects malformed json", func(t *testing.T) {
		t.Parallel()
		versions, err := parsePackageLockVersions([]byte("{"))
		if err == nil || err.Error() != "unexpected end of JSON input" {
			t.Fatalf("error = %v", err)
		}
		if versions != nil {
			t.Fatalf("versions = %#v, want nil on error", versions)
		}
	})
}

func TestDepsCovNpmFormatPackageLockEntryName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		key  string
		want string
		ok   bool
	}{
		{name: "root workspace key", key: "", want: "", ok: false},
		{name: "workspace member path", key: "packages/app", want: "", ok: false},
		{name: "no trailing slash", key: "packages/app/node_modules", want: "", ok: false},
		{name: "bare install", key: "node_modules/foo", want: "foo", ok: true},
		{name: "scoped install", key: "node_modules/@scope/name", want: "@scope/name", ok: true},
		{name: "nested workspace install", key: "packages/app/node_modules/@scope/name", want: "@scope/name", ok: true},
		{name: "empty name after marker", key: "node_modules/", want: "", ok: false},
		{name: "uses the last marker", key: "node_modules/a/node_modules/b", want: "b", ok: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			name, ok := packageLockEntryName(test.key)
			if name != test.want || ok != test.ok {
				t.Fatalf("packageLockEntryName(%q) = (%q, %v), want (%q, %v)", test.key, name, ok, test.want, test.ok)
			}
		})
	}
}

// depsCovNpmFormatRootLockfile indexes every lockfile-shaped branch of
// readNpmLockScopes: two lockfile kinds in one directory whose resolutions
// merge into one scope, a yarn.lock that is reported rather than silently
// dropped, and an unreadable lockfile whose reason is recorded.
func TestDepsCovNpmFormatReadNpmLockScopes(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "package-lock.json"), `{
  "lockfileVersion": 3,
  "packages": {
    "": {"name": "root"},
    "node_modules/@sneat/core": {"version": "1.0.0"},
    "node_modules/lodash": {"version": "4.17.21"},
    "packages/app/node_modules/local-tool": {"version": "2.0.0"}
  }
}
`)
	writeTestFile(t, filepath.Join(root, "pnpm-lock.yaml"), `lockfileVersion: '9.0'
importers:
  .:
    dependencies:
      '@sneat/core':
        specifier: ^1.0.1
        version: 1.0.1
      lodash:
        specifier: ^4.17.21
        version: 4.17.21
      '@sneat/local':
        specifier: link:../local
        version: 'link:../local'
  packages/app:
    dependencies:
      '@sneat/core':
        specifier: ^1.0.1
        version: 1.0.1
`)
	writeTestFile(t, filepath.Join(root, "landings", "yarn.lock"), "# yarn lockfile v1\n")
	writeTestFile(t, filepath.Join(root, "landings", "pnpm-lock.yaml"), "lockfileVersion: '9.0'\nimporters: 42\n")
	if err := os.MkdirAll(filepath.Join(root, "broken"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "missing-target.json"), filepath.Join(root, "broken", "package-lock.json")); err != nil {
		t.Fatal(err)
	}

	scopes, err := readNpmLockScopes(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(scopes) != 3 {
		t.Fatalf("scopes = %#v, want the root, landings, and broken directories", scopes)
	}

	rootScope, ok := scopes[""]
	if !ok || rootScope.Directory != "" || rootScope.Reason != "" {
		t.Fatalf("root scope = %+v (ok=%v)", rootScope, ok)
	}
	if len(rootScope.Versions) != 3 {
		t.Fatalf("root versions = %#v, want @sneat/core, lodash, and local-tool", rootScope.Versions)
	}
	core := rootScope.Versions["@sneat/core"]
	if !reflect.DeepEqual(core.Values, []string{"1.0.0", "1.0.1"}) {
		t.Fatalf("@sneat/core values = %v, want both lockfile resolutions", core.Values)
	}
	if core.Source != "package-lock.json, pnpm-lock.yaml" {
		t.Fatalf("@sneat/core source = %q, want both lockfiles named", core.Source)
	}
	if core.Version() != "" {
		t.Fatalf("@sneat/core Version() = %q, want empty for disagreeing importers", core.Version())
	}
	if core.Conflict() != "lockfile importers pin conflicting versions: 1.0.0, 1.0.1" {
		t.Fatalf("@sneat/core Conflict() = %q", core.Conflict())
	}
	lodash := rootScope.Versions["lodash"]
	if !reflect.DeepEqual(lodash.Values, []string{"4.17.21"}) || lodash.Source != "package-lock.json, pnpm-lock.yaml" || lodash.Version() != "4.17.21" {
		t.Fatalf("lodash = %+v, want the single shared resolution sourced from both lockfiles", lodash)
	}
	if localTool := rootScope.Versions["local-tool"]; !reflect.DeepEqual(localTool.Values, []string{"2.0.0"}) || localTool.Source != "package-lock.json" {
		t.Fatalf("local-tool = %+v", localTool)
	}
	if _, ok := rootScope.Versions["@sneat/local"]; ok {
		t.Fatalf("link: importer entry must not be indexed: %#v", rootScope.Versions)
	}

	landings, ok := scopes["landings"]
	if !ok {
		t.Fatalf("no landings scope: %#v", scopes)
	}
	if !strings.HasPrefix(landings.Reason, "landings/pnpm-lock.yaml: ") {
		t.Fatalf("landings reason = %q, want the unindexable pnpm lockfile named first", landings.Reason)
	}
	if !strings.Contains(landings.Reason, "cannot unmarshal") {
		t.Fatalf("landings reason = %q, want the yaml type error surfaced", landings.Reason)
	}
	if !strings.HasSuffix(landings.Reason, "landings/yarn.lock: yarn.lock is not indexed by WB") {
		t.Fatalf("landings reason = %q, want the yarn.lock note appended", landings.Reason)
	}
	if parts := strings.Split(landings.Reason, "; "); len(parts) != 2 {
		t.Fatalf("landings reason = %q, want exactly two reasons joined by \"; \"", landings.Reason)
	}
	if len(landings.Versions) != 0 {
		t.Fatalf("landings versions = %#v, want none", landings.Versions)
	}

	broken, ok := scopes["broken"]
	if !ok {
		t.Fatalf("no broken scope: %#v", scopes)
	}
	if !strings.HasPrefix(broken.Reason, "broken/package-lock.json: ") || !strings.Contains(broken.Reason, "no such file or directory") {
		t.Fatalf("broken reason = %q, want the unreadable lockfile reported honestly", broken.Reason)
	}
	if len(broken.Versions) != 0 {
		t.Fatalf("broken versions = %#v, want none", broken.Versions)
	}
}

func TestDepsCovNpmFormatReadNpmLockScopesWalkFailure(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "absent")
	scopes, err := readNpmLockScopes(missing)
	if err == nil {
		t.Fatal("readNpmLockScopes on a missing root must fail")
	}
	if scopes != nil {
		t.Fatalf("scopes = %#v, want nil", scopes)
	}
	if !strings.Contains(err.Error(), "absent") {
		t.Fatalf("error = %v, want the walked path named", err)
	}
}

// TestDepsCovNpmFormatScanPackageJSONRefsUnclosedDependencyBlock pins the
// scanner's documented indentation-only recovery: when a dependency block is
// missing its closing brace, the next sibling dependency field at or above
// the block's indent still opens cleanly instead of swallowing the rest of
// the manifest.
func TestDepsCovNpmFormatScanPackageJSONRefsUnclosedDependencyBlock(t *testing.T) {
	t.Parallel()
	contents := []byte("{\n  \"dependencies\": {\n    \"lodash\": \"4.17.21\"\n  \"devDependencies\": {\n    \"typescript\": \"5.4.2\"\n  }\n}\n")
	refs := scanNpmPackageJSONRefs(contents)
	want := []npmPackageJSONRef{
		{Line: 2, Field: "dependencies", Key: "lodash", Value: "4.17.21"},
		{Line: 4, Field: "devDependencies", Key: "typescript", Value: "5.4.2"},
	}
	if !reflect.DeepEqual(refs, want) {
		t.Fatalf("refs = %+v, want %+v", refs, want)
	}

	// A well-formed manifest with a non-dependency block before and after the
	// dependency block must scan only the dependency entries and reset the
	// active field at the closing brace.
	wellFormed := scanNpmPackageJSONRefs([]byte("{\n  \"name\": \"app\",\n  \"dependencies\": {\n    \"lodash\": \"4.17.21\"\n  },\n  \"version\": \"1.0.0\"\n}\n"))
	if !reflect.DeepEqual(wellFormed, []npmPackageJSONRef{{Line: 3, Field: "dependencies", Key: "lodash", Value: "4.17.21"}}) {
		t.Fatalf("wellFormed refs = %+v", wellFormed)
	}
}

func TestDepsCovNpmFormatApplyPackageJSONOverrideLineEndings(t *testing.T) {
	t.Parallel()
	t.Run("preserves crlf and trailing comma", func(t *testing.T) {
		t.Parallel()
		contents := []byte("{\r\n  \"dependencies\": {\r\n    \"@sneat/core\": \"1.2.3\",\r\n    \"lodash\": \"^4.17.21\"\r\n  }\r\n}\r\n")
		updated, matched, err := applyNpmPackageJSONOverride(contents, "@sneat/core", "1.3.0")
		if err != nil {
			t.Fatal(err)
		}
		want := "{\r\n  \"dependencies\": {\r\n    \"@sneat/core\": \"1.3.0\",\r\n    \"lodash\": \"^4.17.21\"\r\n  }\r\n}\r\n"
		if string(updated) != want {
			t.Fatalf("updated =\n%q\nwant\n%q", updated, want)
		}
		if !reflect.DeepEqual(matched, []npmPackageJSONRef{{Line: 2, Field: "dependencies", Key: "@sneat/core", Value: "1.2.3"}}) {
			t.Fatalf("matched = %+v", matched)
		}
	})

	t.Run("preserves lf and missing trailing comma", func(t *testing.T) {
		t.Parallel()
		contents := []byte("{\n  \"dependencies\": {\n    \"@sneat/core\": \"1.2.3\"\n  }\n}\n")
		updated, matched, err := applyNpmPackageJSONOverride(contents, "@sneat/core", "1.3.0")
		if err != nil {
			t.Fatal(err)
		}
		want := "{\n  \"dependencies\": {\n    \"@sneat/core\": \"1.3.0\"\n  }\n}\n"
		if string(updated) != want {
			t.Fatalf("updated =\n%q\nwant\n%q", updated, want)
		}
		if len(matched) != 1 || matched[0].Value != "1.2.3" {
			t.Fatalf("matched = %+v", matched)
		}
	})
}

func TestDepsCovNpmFormatParsePnpmWorkspaceLine(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
		want pnpmWorkspaceLine
	}{
		{name: "blank line", raw: "\n", want: pnpmWorkspaceLine{raw: "\n"}},
		{name: "comment line", raw: "# a comment\n", want: pnpmWorkspaceLine{raw: "# a comment\n"}},
		{name: "list item", raw: "  - \"packages/*\"\n", want: pnpmWorkspaceLine{raw: "  - \"packages/*\"\n", indent: 2}},
		{name: "section header", raw: "overrides:\n", want: pnpmWorkspaceLine{raw: "overrides:\n", isMapping: true, key: "overrides"}},
		{
			name: "bare key with bare value",
			raw:  "  react: ^18.2.0\n",
			want: pnpmWorkspaceLine{raw: "  react: ^18.2.0\n", indent: 2, isMapping: true, key: "react", value: "^18.2.0", valueSet: true},
		},
		{
			name: "single quoted key and value with comment",
			raw:  "'@sneat/core': '1.2.3' # pinned\n",
			want: pnpmWorkspaceLine{
				raw: "'@sneat/core': '1.2.3' # pinned\n", isMapping: true,
				key: "@sneat/core", keyQuote: '\'', value: "1.2.3", valueSet: true, quote: '\'', comment: "# pinned",
			},
		},
		{
			name: "double quoted key and value",
			raw:  "\"@sneat/core\": \"1.2.3\"\n",
			want: pnpmWorkspaceLine{
				raw: "\"@sneat/core\": \"1.2.3\"\n", isMapping: true,
				key: "@sneat/core", keyQuote: '"', value: "1.2.3", valueSet: true, quote: '"',
			},
		},
		{name: "unterminated quoted key", raw: "'broken: value\n", want: pnpmWorkspaceLine{raw: "'broken: value\n"}},
		{name: "no colon at all", raw: "noColonHere\n", want: pnpmWorkspaceLine{raw: "noColonHere\n"}},
		{name: "quoted key then junk", raw: "'key' value\n", want: pnpmWorkspaceLine{raw: "'key' value\n"}},
		{name: "bare key then junk", raw: "  key value\n", want: pnpmWorkspaceLine{raw: "  key value\n", indent: 2}},
		{
			name: "quoted key with only spaces after colon",
			raw:  "'a':   \n",
			want: pnpmWorkspaceLine{raw: "'a':   \n", isMapping: true, key: "a", keyQuote: '\''},
		},
		{
			name: "tab after colon",
			raw:  "a:\t1.0.0\n",
			want: pnpmWorkspaceLine{raw: "a:\t1.0.0\n", isMapping: true, key: "a", value: "1.0.0", valueSet: true},
		},
		{
			name: "doubled single quote in key",
			raw:  "'a''b': 1.0.0\n",
			want: pnpmWorkspaceLine{raw: "'a''b': 1.0.0\n", isMapping: true, key: "a''b", keyQuote: '\'', value: "1.0.0", valueSet: true},
		},
		{
			name: "escaped double quote in key",
			raw:  "\"a\\\"b\": 1.0.0\n",
			want: pnpmWorkspaceLine{raw: "\"a\\\"b\": 1.0.0\n", isMapping: true, key: "a\\\"b", keyQuote: '"', value: "1.0.0", valueSet: true},
		},
		{
			name: "no space after colon",
			raw:  "key:value\n",
			want: pnpmWorkspaceLine{raw: "key:value\n", isMapping: true, key: "key", value: "value", valueSet: true},
		},
		{
			name: "comment only value",
			raw:  "key: # note\n",
			want: pnpmWorkspaceLine{raw: "key: # note\n", isMapping: true, key: "key", valueSet: true, comment: "# note"},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := parsePnpmWorkspaceLine(test.raw)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("parsePnpmWorkspaceLine(%q) = %+v, want %+v", test.raw, got, test.want)
			}
		})
	}
}

func TestDepsCovNpmFormatIndexUnescapedQuote(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		value string
		quote byte
		want  int
	}{
		{name: "no quote present", value: "abc", quote: '\'', want: -1},
		{name: "single quote found", value: "a'b", quote: '\'', want: 1},
		{name: "doubled single quote is escaped", value: "a''b'", quote: '\'', want: 4},
		{name: "double quote found", value: "abc", quote: '"', want: -1},
		{name: "backslash escapes double quote", value: `a\"b"`, quote: '"', want: 4},
		{name: "trailing backslash consumes end", value: `a\`, quote: '"', want: -1},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := indexUnescapedQuote(test.value, test.quote); got != test.want {
				t.Fatalf("indexUnescapedQuote(%q, %q) = %d, want %d", test.value, test.quote, got, test.want)
			}
		})
	}
}

func TestDepsCovNpmFormatParsePnpmWorkspaceValue(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		rest        string
		wantValue   string
		wantQuote   byte
		wantComment string
	}{
		{name: "single quoted with comment", rest: "'1.2.3' # pin", wantValue: "1.2.3", wantQuote: '\'', wantComment: "# pin"},
		{name: "double quoted without comment", rest: "\"1.2.3\"", wantValue: "1.2.3", wantQuote: '"'},
		{name: "single quoted with trailing junk", rest: "'1.2.3' trailing", wantValue: "1.2.3", wantQuote: '\''},
		{name: "unterminated quote falls through", rest: "'unterminated", wantValue: "'unterminated"},
		{name: "bare value with comment", rest: "^1.2.3 # comment", wantValue: "^1.2.3", wantComment: "# comment"},
		{name: "comment only", rest: "# just a comment", wantValue: "", wantComment: "# just a comment"},
		{name: "bare value", rest: "1.2.3", wantValue: "1.2.3"},
		{name: "bare value with tabs", rest: "\t1.2.3\t", wantValue: "1.2.3"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			value, quote, comment := parsePnpmWorkspaceValue(test.rest)
			if value != test.wantValue || quote != test.wantQuote || comment != test.wantComment {
				t.Fatalf("parsePnpmWorkspaceValue(%q) = (%q, %q, %q), want (%q, %q, %q)",
					test.rest, value, quote, comment, test.wantValue, test.wantQuote, test.wantComment)
			}
		})
	}
}

func TestDepsCovNpmFormatRenderPnpmWorkspaceValue(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		value   string
		quote   byte
		comment string
		want    string
	}{
		{name: "bare", value: "1.2.3", want: "1.2.3"},
		{name: "single quoted", value: "1.2.3", quote: '\'', want: "'1.2.3'"},
		{name: "double quoted", value: "1.2.3", quote: '"', want: "\"1.2.3\""},
		{name: "single quoted with comment", value: "1.2.3", quote: '\'', comment: "# pin", want: "'1.2.3' # pin"},
		{name: "escapes single quote", value: "it's", quote: '\'', want: "'it''s'"},
		{name: "escapes double quote", value: "say \"hi\"", quote: '"', want: "\"say \\\"hi\\\"\""},
		{name: "double quoted with comment", value: "1.2.3", quote: '"', comment: "# pin", want: "\"1.2.3\" # pin"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := renderPnpmWorkspaceValue(test.value, test.quote, test.comment); got != test.want {
				t.Fatalf("renderPnpmWorkspaceValue(%q, %q, %q) = %q, want %q", test.value, test.quote, test.comment, got, test.want)
			}
		})
	}
}

func TestDepsCovNpmFormatRenderPnpmWorkspaceKey(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		key   string
		quote byte
		want  string
	}{
		{name: "bare", key: "react", want: "react"},
		{name: "single quoted scope", key: "@sneat/core", quote: '\'', want: "'@sneat/core'"},
		{name: "double quoted scope", key: "@sneat/core", quote: '"', want: "\"@sneat/core\""},
		{name: "escapes single quote", key: "it's", quote: '\'', want: "'it''s'"},
		{name: "escapes double quote", key: "a\"b", quote: '"', want: "\"a\\\"b\""},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := renderPnpmWorkspaceKey(test.key, test.quote); got != test.want {
				t.Fatalf("renderPnpmWorkspaceKey(%q, %q) = %q, want %q", test.key, test.quote, got, test.want)
			}
		})
	}
}

// TestDepsCovNpmFormatScanPnpmWorkspaceRefsSections pins the indentation-only
// section tracking: a top-level mapping closes the active section, a new
// catalog name closes the previous catalog, and mapping lines that are not
// leaves (a bare "partial:" key) or that sit outside every recognized section
// are never reported as references.
func TestDepsCovNpmFormatScanPnpmWorkspaceRefsSections(t *testing.T) {
	t.Parallel()
	const contents = `packages:
  - "packages/*"

overrides:
  "@sneat/core": "1.2.3" # pinned
  lodash: ^4.17.21
  partial:

catalog:
  react: ^18.2.0
  "@sneat/models": ^1.0.0

catalogs:
  react17:
    react: ^17.0.2
  inline: ^9.9.9
  react18:
    react: ^18.2.0

peerDependencyRules:
  allowedVersions:
    react: 18
`
	refs := scanPnpmWorkspaceRefs([]byte(contents))
	want := []pnpmWorkspaceRef{
		{Line: 4, Section: "overrides", Key: "@sneat/core", Value: "1.2.3", quote: '"', comment: "# pinned"},
		{Line: 5, Section: "overrides", Key: "lodash", Value: "^4.17.21"},
		{Line: 9, Section: "catalog", Key: "react", Value: "^18.2.0"},
		{Line: 10, Section: "catalog", Key: "@sneat/models", Value: "^1.0.0"},
		{Line: 14, Section: "catalogs", CatalogName: "react17", Key: "react", Value: "^17.0.2"},
		{Line: 17, Section: "catalogs", CatalogName: "react18", Key: "react", Value: "^18.2.0"},
	}
	if !reflect.DeepEqual(refs, want) {
		t.Fatalf("refs = %#v, want %#v", refs, want)
	}
}

func TestDepsCovNpmFormatApplyPnpmWorkspaceOverrideQuoteStyles(t *testing.T) {
	t.Parallel()
	const contents = `packages:
  - "packages/*"
overrides:
  '@sneat/core': '1.2.3' # pinned by hand
  "lodash": "4.17.21"
  react: ^18.2.0
`
	cases := []struct {
		name       string
		dependency string
		version    string
		want       string
		wantMatch  pnpmWorkspaceRef
	}{
		{
			name: "single quoted value with comment", dependency: "@sneat/core", version: "1.3.0",
			want: "packages:\n  - \"packages/*\"\noverrides:\n  '@sneat/core': '1.3.0' # pinned by hand\n  \"lodash\": \"4.17.21\"\n  react: ^18.2.0\n",
			wantMatch: pnpmWorkspaceRef{
				Line: 3, Section: "overrides", Key: "@sneat/core", Value: "1.2.3", quote: '\'', comment: "# pinned by hand",
			},
		},
		{
			name: "double quoted value", dependency: "lodash", version: "4.18.0",
			want:      "packages:\n  - \"packages/*\"\noverrides:\n  '@sneat/core': '1.2.3' # pinned by hand\n  \"lodash\": \"4.18.0\"\n  react: ^18.2.0\n",
			wantMatch: pnpmWorkspaceRef{Line: 4, Section: "overrides", Key: "lodash", Value: "4.17.21", quote: '"'},
		},
		{
			name: "bare value", dependency: "react", version: "18.3.0",
			want:      "packages:\n  - \"packages/*\"\noverrides:\n  '@sneat/core': '1.2.3' # pinned by hand\n  \"lodash\": \"4.17.21\"\n  react: 18.3.0\n",
			wantMatch: pnpmWorkspaceRef{Line: 5, Section: "overrides", Key: "react", Value: "^18.2.0"},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			updated, matched, err := applyPnpmWorkspaceOverride([]byte(contents), test.dependency, test.version)
			if err != nil {
				t.Fatal(err)
			}
			if string(updated) != test.want {
				t.Fatalf("updated =\n%q\nwant\n%q", updated, test.want)
			}
			if !reflect.DeepEqual(matched, []pnpmWorkspaceRef{test.wantMatch}) {
				t.Fatalf("matched = %#v, want %#v", matched, []pnpmWorkspaceRef{test.wantMatch})
			}
		})
	}
}

func TestDepsCovNpmFormatSplitPreservingLineEndings(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		text string
		want []string
	}{
		{name: "empty text", text: "", want: nil},
		{name: "lf terminated lines", text: "a\nb\n", want: []string{"a\n", "b\n"}},
		{name: "crlf with unterminated tail", text: "a\r\nb", want: []string{"a\r\n", "b"}},
		{name: "no newline at all", text: "single", want: []string{"single"}},
		{name: "only a newline", text: "\n", want: []string{"\n"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := splitPreservingLineEndings(test.text); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("splitPreservingLineEndings(%q) = %#v, want %#v", test.text, got, test.want)
			}
		})
	}
}

func TestDepsCovNpmFormatLineEndingOf(t *testing.T) {
	t.Parallel()
	cases := []struct {
		line string
		want string
	}{
		{line: "", want: ""},
		{line: "a", want: ""},
		{line: "a\n", want: "\n"},
		{line: "a\r\n", want: "\r\n"},
		{line: "a\r", want: ""},
	}
	for _, test := range cases {
		t.Run(strings.ReplaceAll(test.line, "\n", "LF"), func(t *testing.T) {
			t.Parallel()
			if got := lineEndingOf(test.line); got != test.want {
				t.Fatalf("lineEndingOf(%q) = %q, want %q", test.line, got, test.want)
			}
		})
	}
}
