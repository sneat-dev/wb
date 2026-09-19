package wbconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// remoteLFFixture is a realistic, unrelated remote: block every case below
// either precedes or follows the peers: block with, so a test can assert it
// survived byte-for-byte.
const remoteLFFixture = "remote:\n    provider: git # c\n    repo: acme/state\n"

// TestSetPeersUpstreamSupportsShapeA is round 5's shape A: no top-level
// "peers:" key exists yet, so a fresh block is appended.
func TestSetPeersUpstreamSupportsShapeA(t *testing.T) {
	cases := map[string]string{
		"empty file":              "",
		"no peers, LF":            remoteLFFixture,
		"no peers, CRLF":          strings.ReplaceAll(remoteLFFixture, "\n", "\r\n"),
		"no trailing newline":     strings.TrimSuffix(remoteLFFixture, "\n"),
		"before a doc-end marker": remoteLFFixture + "...\n",
		"a leading doc-start marker is not a second document": "---\n" + remoteLFFixture,
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "wb.yaml")
			if input != "" {
				if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := SetPeersUpstream(path, "https://b.example", "/abs/tok"); err != nil {
				t.Fatalf("SetPeersUpstream = %v, want success", err)
			}
			assertSinglePeersKeyAndUpstream(t, path, input, "https://b.example", "/abs/tok")
		})
	}

	t.Run("before a doc-end marker keeps the marker after the new content", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "wb.yaml")
		if err := os.WriteFile(path, []byte(remoteLFFixture+"...\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := SetPeersUpstream(path, "https://b.example", "/abs/tok"); err != nil {
			t.Fatal(err)
		}
		out, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(out)
		if !strings.HasSuffix(strings.TrimRight(text, "\n"), "...") {
			t.Fatalf("the trailing ... document-end marker was lost or moved:\n%q", text)
		}
		if strings.Index(text, "peers:") > strings.Index(text, "...") {
			t.Fatalf("peers: was written after the ... marker:\n%q", text)
		}
	})
}

// TestSetPeersUpstreamSupportsShapeB is round 5's shape B: an existing
// block-style "peers:" key at column 0, whose body is nothing but blank
// lines and single-line "key: value" pairs (with at most one nested
// "upstream:" sub-block, itself the same shape one level deeper). Every one
// of these must succeed, and each preserves whatever else was already
// there.
func TestSetPeersUpstreamSupportsShapeB(t *testing.T) {
	cases := map[string]string{
		"existing upstream, LF":          remoteLFFixture + "peers:\n  upstream:\n    url: https://old.example\n    token_file: /old/tok\n",
		"existing upstream, CRLF":        strings.ReplaceAll(remoteLFFixture+"peers:\n  upstream:\n    url: https://old.example\n    token_file: /old/tok\n", "\n", "\r\n"),
		"bare/null upstream":             remoteLFFixture + "peers:\n  upstream:\n",
		"upstream on one inline line":    remoteLFFixture + "peers:\n  upstream: null\n",
		"no upstream yet, a sibling key": remoteLFFixture + "peers:\n  other: 1\n",
		"peers is entirely bare/null":    remoteLFFixture + "peers:\n",
		"peers before remote":            "peers:\n  upstream:\n    url: https://old.example\n    token_file: /old/tok\n" + remoteLFFixture,
		"no trailing newline":            strings.TrimSuffix(remoteLFFixture+"peers:\n  upstream:\n    url: https://old.example\n    token_file: /old/tok\n", "\n"),
		"4-space indent":                 remoteLFFixture + "peers:\n    upstream:\n        url: https://old.example\n        token_file: /old/tok\n",
		"a quoted value already there":   remoteLFFixture + "peers:\n  upstream:\n    url: \"https://old.example\"\n    token_file: '/old/tok'\n",
		// Round 5's own two "over-count" repro fixes: the boundary used to
		// run to the next top-level key or EOF, deleting anything past the
		// peers value's own subtree — a second document, or a trailing
		// comment block. The new plain line scan never looks past the
		// body's own last line at all, so both now succeed with the at-risk
		// content preserved (asserted in dedicated sub-tests below).
		"second document after peers":                  remoteLFFixture + "peers:\n  upstream:\n    url: https://old.example\n    token_file: /old/tok\n---\nother_doc: 1\n",
		"trailing comment after peers":                 remoteLFFixture + "peers:\n  upstream:\n    url: https://old.example\n    token_file: /old/tok\n\n# a trailing comment about peers\n# another comment line\n",
		"comment above the next key (not above peers)": "peers:\n  upstream:\n    url: https://old.example\n    token_file: /old/tok\n# a comment right above remote\n" + remoteLFFixture,
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "wb.yaml")
			if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := SetPeersUpstream(path, "https://b.example", "/abs/tok"); err != nil {
				t.Fatalf("SetPeersUpstream = %v, want success", err)
			}
			assertSinglePeersKeyAndUpstream(t, path, input, "https://b.example", "/abs/tok")
		})
	}

	t.Run("no upstream yet, a sibling key preserves the sibling", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "wb.yaml")
		input := remoteLFFixture + "peers:\n  other: 1\n"
		if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := SetPeersUpstream(path, "https://b.example", "/abs/tok"); err != nil {
			t.Fatal(err)
		}
		out, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out), "other: 1") {
			t.Fatalf("sibling key \"other: 1\" was dropped:\n%q", string(out))
		}
	})

	t.Run("second document after peers keeps the second document", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "wb.yaml")
		input := remoteLFFixture + "peers:\n  upstream:\n    url: https://old.example\n    token_file: /old/tok\n---\nother_doc: 1\n"
		if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := SetPeersUpstream(path, "https://b.example", "/abs/tok"); err != nil {
			t.Fatal(err)
		}
		out, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out), "---\nother_doc: 1\n") {
			t.Fatalf("CORRUPT: the second YAML document was lost:\n%q", string(out))
		}
	})

	t.Run("trailing comment after peers keeps the comment block", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "wb.yaml")
		input := remoteLFFixture + "peers:\n  upstream:\n    url: https://old.example\n    token_file: /old/tok\n\n# a trailing comment about peers\n# another comment line\n"
		if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := SetPeersUpstream(path, "https://b.example", "/abs/tok"); err != nil {
			t.Fatal(err)
		}
		out, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out), "# a trailing comment about peers\n# another comment line") {
			t.Fatalf("CORRUPT: the trailing comment block was lost:\n%q", string(out))
		}
	})

	t.Run("comment above the next key keeps the comment", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "wb.yaml")
		input := "peers:\n  upstream:\n    url: https://old.example\n    token_file: /old/tok\n# a comment right above remote\n" + remoteLFFixture
		if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := SetPeersUpstream(path, "https://b.example", "/abs/tok"); err != nil {
			t.Fatal(err)
		}
		out, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out), "# a comment right above remote\nremote:") {
			t.Fatalf("CORRUPT: the comment directly above remote: was lost:\n%q", string(out))
		}
	})
}

// assertSinglePeersKeyAndUpstream is the common post-condition every
// success case above must satisfy: the result parses, has exactly one line
// starting a peers key, LoadPeersUpstream reads back the value just
// written, and — when input itself contained a "remote:" block — that
// block survived byte for byte.
func assertSinglePeersKeyAndUpstream(t *testing.T, path, input, wantURL, wantTokenFile string) {
	t.Helper()
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatalf("CORRUPT: result does not parse as YAML: %v\n%q", err, string(out))
	}
	peersLines := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimRight(strings.TrimSpace(line), "\r") == "peers:" {
			peersLines++
		}
	}
	if peersLines != 1 {
		t.Fatalf("CORRUPT: result has %d lines starting a peers key, want exactly 1:\n%q", peersLines, string(out))
	}
	up, found, err := LoadPeersUpstream(path)
	if err != nil || !found || up.URL != wantURL || up.TokenFile != wantTokenFile {
		t.Fatalf("CORRUPT: LoadPeersUpstream = %+v, %t, %v", up, found, err)
	}
	idx := strings.Index(input, "remote:")
	if idx >= 0 {
		end := strings.Index(input[idx:], "repo: acme/state")
		chunk := input[idx : idx+end+len("repo: acme/state")]
		if !strings.Contains(string(out), chunk) {
			t.Fatalf("remote: block changed:\nwant substring: %q\ngot:            %q", chunk, string(out))
		}
	}
}

// TestSetPeersUpstreamRefusesUnsupportedShapes is round 5's ruling in full:
// "stop trying to handle arbitrary YAML. Support two simple shapes and
// refuse everything else." Every one of these must be refused with a
// non-zero error and leave the file completely untouched — never silently
// wrong, and never a panic (TestWriteSkillsSyncCmd... — see the dedicated
// panic-input case below, which is exactly the reviewer's repro for the
// spliceLines index panic this suite also guards against).
func TestSetPeersUpstreamRefusesUnsupportedShapes(t *testing.T) {
	cases := map[string]string{
		"quoted key":                                     remoteLFFixture + "\"peers\":\n  upstream:\n    url: https://a.example\n    token_file: /x/y\n",
		"space before colon":                             remoteLFFixture + "peers :\n  upstream:\n    url: https://a.example\n    token_file: /x/y\n",
		"peers value is flow-style":                      remoteLFFixture + "peers: {upstream: {url: https://a.example, token_file: /x/y}}\n",
		"comment after peers:":                           remoteLFFixture + "peers:   # mine\n  upstream:\n    url: https://a.example\n    token_file: /x/y\n",
		"comment above peers:":                           remoteLFFixture + "# a note about peers\npeers:\n  upstream:\n    url: https://a.example\n    token_file: /x/y\n",
		"column-0 comment inside the body":               remoteLFFixture + "peers:\n  upstream:\n# note\n    url: https://a.example\n    token_file: /x/y\n",
		"root document is flow-style":                    "{remote: {provider: git}, peers: {upstream: {url: https://old.example, token_file: /old/tok}}}\n",
		"top-level block scalar":                         "peers: |\n  something\n" + remoteLFFixture,
		"multi-line flow collection":                     "peers: {upstream: {url: https://x,\n  token_file: /y}}\n" + remoteLFFixture,
		"CRLF with a trailing inline comment on a value": strings.ReplaceAll("peers:\n  upstream:\n    url: https://x\n    token_file: /y   # comment\n"+remoteLFFixture, "\n", "\r\n"),
		"anchor":                 remoteLFFixture + "peers:\n  upstream:\n    url: &anchor https://x\n    token_file: /y\n",
		"alias":                  remoteLFFixture + "peers:\n  other: *anchor\n  upstream:\n    url: https://x\n    token_file: /y\n",
		"explicit tag":           remoteLFFixture + "peers:\n  other: !!str foo\n  upstream:\n    url: https://x\n    token_file: /y\n",
		"a second upstream: key": remoteLFFixture + "peers:\n  upstream:\n    url: https://x\n    token_file: /y\n  upstream:\n    url: https://z\n    token_file: /w\n",
		"a non-upstream key opens a nested block": remoteLFFixture + "peers:\n  other:\n    nested: 1\n  upstream:\n    url: https://x\n    token_file: /y\n",
		"a tab in the body":                       remoteLFFixture + "peers:\n\tupstream:\n\t\turl: https://x\n\t\ttoken_file: /y\n",
		"unparsable YAML":                         "not: [valid\n",
		"peers is a non-mapping scalar":           "peers: not-a-mapping\n",
		"peers is a sequence":                     "peers:\n  - a\n  - b\n",
		// Round 5's own two "over-count" reproductions: a multi-line quoted
		// scalar spanning a "---" second document, and one spanning a
		// trailing comment. Round 4's boundary silently deleted the
		// content past the scalar's real end; this grammar refuses a
		// multi-line quoted scalar outright instead of guessing where it
		// really ends.
		"multi-line quoted scalar before a second document":  remoteLFFixture + "peers:\n  note: \"a\n\nb\"\n---\nother_doc: 1\n",
		"multi-line quoted scalar before a trailing comment": "peers:\n  note: \"a\nb\"\n# precious comment\n" + remoteLFFixture,
		// Round 5's own two "under-count" reproductions: a block scalar and
		// a folded plain scalar, both of which round 4's boundary
		// under-estimated, corrupting a sibling by leaving part of the
		// scalar's own continuation lines outside the replaced range.
		"block scalar sibling":           remoteLFFixture + "peers:\n  upstream:\n    url: https://old.example\n    token_file: /old/tok\n  note: |-\n    a\n    b\n",
		"folded plain multi-line scalar": remoteLFFixture + "peers:\n  note: a\n    b\n    c\n",
		// Round 5's panic reproduction: an unterminated double-quoted
		// scalar followed by several blank lines at EOF used to panic
		// spliceLines with an out-of-bounds index. It must now refuse
		// cleanly instead — see TestSetPeersUpstreamNeverPanics below for
		// the explicit non-panic assertion on this exact input.
		"unterminated quoted scalar at EOF": "peers:\n  note: \"\n\n\n\n",
		// Round 5's "duplicated FootComment" reproduction: a comment
		// indented as if it were still part of the body, after upstream's
		// own nested children.
		"an indented trailing comment inside the body": "peers:\n  upstream:\n    url: https://old.example\n    token_file: /old/tok\n  # trailing indented comment\n" + remoteLFFixture,
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "wb.yaml")
			if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := SetPeersUpstream(path, "https://b.example", "/abs/tok"); err == nil {
				t.Fatalf("SetPeersUpstream = nil, want a refusal for this unsupported shape")
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != input {
				t.Fatalf("a refused Set must leave the file byte-identical:\nbefore: %q\nafter:  %q", input, string(after))
			}
		})
	}
}

// TestSetPeersUpstreamNeverPanics is the reviewer's exact round 5 panic
// reproduction: an unterminated double-quoted scalar followed by several
// blank lines at EOF used to drive spliceLines past the end of the file's
// own line count. recover() turns any regression back into a normal test
// failure instead of crashing the whole test binary, so this stays a
// reliable regression guard rather than something that just happens not to
// panic today.
func TestSetPeersUpstreamNeverPanics(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("SetPeersUpstream panicked: %v", r)
		}
	}()
	path := filepath.Join(t.TempDir(), "wb.yaml")
	input := "peers:\n  note: \"\n\n\n\n"
	if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetPeersUpstream(path, "https://b.example", "/abs/tok"); err == nil {
		t.Fatal("expected a refusal, not success, for an unterminated quoted scalar")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != input {
		t.Fatalf("a refused Set must leave the file byte-identical:\nbefore: %q\nafter:  %q", input, string(after))
	}
}

// TestSetPeersUpstreamReportsUncreatableDirectory mirrors
// TestTailCovSetRemoteHubReportsUncreatableDirectory for the new splice
// path's own os.MkdirAll call.
func TestSetPeersUpstreamReportsUncreatableDirectory(t *testing.T) {
	root := t.TempDir()
	dangling := filepath.Join(root, "dangling")
	if err := os.Symlink(filepath.Join(root, "no-such-target"), dangling); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	path := filepath.Join(dangling, "wb.yaml")
	if err := SetPeersUpstream(path, "https://b.example", "/abs/tok"); err == nil || !strings.Contains(err.Error(), "create config directory") {
		t.Fatalf("SetPeersUpstream under an uncreatable directory = %v, want a directory creation failure", err)
	}
}

// TestSetPeersUpstreamReportsUnstageableConfig mirrors
// TestTailCovSetRemoteHubReportsUnstageableConfig for the new splice path's
// own os.CreateTemp call.
func TestSetPeersUpstreamReportsUnstageableConfig(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permission bits, so the staging write cannot fail")
	}
	root := t.TempDir()
	directory := filepath.Join(root, "read-only")
	if err := os.Mkdir(directory, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(directory, 0o700) })
	path := filepath.Join(directory, "wb.yaml")
	err := SetPeersUpstream(path, "https://b.example", "/abs/tok")
	if err == nil || !strings.Contains(err.Error(), "stage config") {
		t.Fatalf("SetPeersUpstream against an unwritable directory = %v, want a staging failure", err)
	}
	if _, statErr := os.Stat(path); statErr == nil {
		t.Fatal("SetPeersUpstream must not create the config file on a staging failure")
	}
}

// TestLoadPeersUpstreamReportsAReadFailure covers LoadPeersUpstream's
// generic-read-error branch (distinct from the ordinary "file does not
// exist" case), using an unreadable file.
func TestLoadPeersUpstreamReportsAReadFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permission bits, so the read cannot fail")
	}
	path := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(path, []byte("peers:\n  upstream:\n    url: https://a.example\n    token_file: /x\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	if _, _, err := LoadPeersUpstream(path); err == nil {
		t.Fatal("expected LoadPeersUpstream to report an unreadable file")
	}
}

// TestSetPeersUpstreamRefusalLeavesTheFileUntouched is the pre-write safety
// requirement for a refusal that happens before rewritePeersUpstream is
// even reached (an invalid URL): the original file is left byte-for-byte
// untouched and SetPeersUpstream returns a non-nil error, never a silently
// corrupted config.
func TestSetPeersUpstreamRefusalLeavesTheFileUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wb.yaml")
	original := "remote:\n  provider: git\n  repo: acme/state\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetPeersUpstream(path, "not-a-valid-url", "/abs/tok"); err == nil {
		t.Fatal("expected a refusal for an invalid URL")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != original {
		t.Fatalf("a refused Set must leave the file byte-identical:\nbefore: %q\nafter:  %q", original, string(after))
	}
}
