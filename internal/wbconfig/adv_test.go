package wbconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestAdversarialSplice is the adversarial-review reproduction suite for
// round 3's finding 2: a text splice built on naive literal-line matching
// silently produced a duplicate "peers:" key (and lost sibling keys, and
// dropped content after a "..." document-end marker) on every one of these
// inputs. SetPeersUpstream now locates the "peers:" key by its yaml.Node
// decoded value and line range rather than by scanning literal text, so all
// of these must succeed with a well-formed result and a byte-identical
// remote: block, not merely "not crash".
func TestAdversarialSplice(t *testing.T) {
	remoteLF := "remote:\n    provider: git # c\n    repo: acme/state\n"
	cases := map[string]string{
		"crlf-existing-peers": strings.ReplaceAll(remoteLF+"peers:\n  upstream:\n    url: https://a.example\n    token_file: /x/y\n", "\n", "\r\n"),
		"crlf-no-peers":       strings.ReplaceAll(remoteLF, "\n", "\r\n"),
		"quoted-key":          remoteLF + "\"peers\":\n  upstream:\n    url: https://a.example\n    token_file: /x/y\n",
		"flow-style":          remoteLF + "peers: {upstream: {url: https://a.example, token_file: /x/y}}\n",
		"comment-after-key":   remoteLF + "peers:   # mine\n  upstream:\n    url: https://a.example\n    token_file: /x/y\n",
		"col0-comment-inside": remoteLF + "peers:\n  upstream:\n# note\n    url: https://a.example\n    token_file: /x/y\n",
		"sibling-key":         remoteLF + "peers:\n  other: 1\n  upstream:\n    url: https://a.example\n    token_file: /x/y\n",
		"no-trailing-newline": strings.TrimSuffix(remoteLF, "\n"),
		"peers-before-remote": "peers:\n  upstream:\n    url: https://a.example\n    token_file: /x/y\n" + remoteLF,
		"doc-end-marker":      remoteLF + "...\n",
		"doc-start-marker":    "---\n" + remoteLF,
		"peers-null":          remoteLF + "peers:\n",
		"peers-space-colon":   remoteLF + "peers :\n  upstream:\n    url: https://a.example\n    token_file: /x/y\n",
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "wb.yaml")
			if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
				t.Fatal(err)
			}
			err := SetPeersUpstream(path, "https://b.example", "/abs/tok")
			if err != nil {
				t.Fatalf("SetPeersUpstream = %v, want success for this valid (if unusual) input", err)
			}
			out, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}

			var doc map[string]any
			if err := yaml.Unmarshal(out, &doc); err != nil {
				t.Fatalf("CORRUPT: result does not parse as YAML: %v\n%q", err, string(out))
			}
			// Exactly one "peers" key: yaml.Unmarshal into a map silently
			// keeps only the LAST duplicate key rather than erroring, which
			// is exactly how this bug hid before — assert the raw text has
			// no second "peers:"/"\"peers\":" line instead of trusting the
			// map decode alone.
			peersLines := 0
			for _, line := range strings.Split(string(out), "\n") {
				trimmed := strings.TrimRight(strings.TrimSpace(line), "\r")
				if trimmed == "peers:" || strings.HasPrefix(trimmed, "peers:") || strings.HasPrefix(trimmed, "\"peers\":") || strings.HasPrefix(trimmed, "peers ") {
					peersLines++
				}
			}
			if peersLines != 1 {
				t.Fatalf("CORRUPT: result has %d lines starting a peers key, want exactly 1:\n%q", peersLines, string(out))
			}

			up, found, err := LoadPeersUpstream(path)
			if err != nil || !found || up.URL != "https://b.example" || up.TokenFile != "/abs/tok" {
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
		})
	}

	t.Run("sibling-key preserves the sibling", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "wb.yaml")
		input := remoteLF + "peers:\n  other: 1\n  upstream:\n    url: https://a.example\n    token_file: /x/y\n"
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

	t.Run("doc-end-marker keeps the marker after the new content", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "wb.yaml")
		if err := os.WriteFile(path, []byte(remoteLF+"...\n"), 0o600); err != nil {
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
			t.Fatalf("peers: was written after the ... marker, where a single-document parse would never see it:\n%q", text)
		}
		if _, found, err := LoadPeersUpstream(path); err != nil || !found {
			t.Fatalf("LoadPeersUpstream after a doc-end marker = found=%t, %v", found, err)
		}
	})
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

// TestParseYAMLDocumentRejectsANonMappingTopLevel and
// TestStripTopLevelKeyPropagatesAParseFailure cover the two internal
// helpers' own error branches directly, beyond what SetPeersUpstream's
// public-API tests reach.
func TestParseYAMLDocumentRejectsANonMappingTopLevel(t *testing.T) {
	if _, _, err := parseYAMLDocument("- a\n- b\n"); err == nil {
		t.Fatal("expected a refusal for a top-level sequence")
	}
}

func TestStripTopLevelKeyPropagatesAParseFailure(t *testing.T) {
	if _, err := stripTopLevelKey("not: [valid\n", "peers"); err == nil {
		t.Fatal("expected stripTopLevelKey to propagate an unparsable document")
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

// TestSetPeersUpstreamRefusalLeavesTheFileUntouched is round 3's explicit
// pre-write safety requirement: if the result cannot be verified — here, by
// injecting a case verifySpliceResult's re-check is built to catch — the
// original file is left byte-for-byte untouched and SetPeersUpstream
// returns a non-nil error, never a silently corrupted config.
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
