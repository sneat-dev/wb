package wbconfig

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestIsLoopbackPeersHostEqualFold(t *testing.T) {
	t.Parallel()

	if !isLoopbackPeersHost("LocalHost") {
		t.Fatalf("isLoopbackPeersHost(LocalHost) = false, want true (case-insensitive match)")
	}
	if !isLoopbackPeersHost("localhost") {
		t.Fatalf("isLoopbackPeersHost(localhost) = false, want true")
	}
}

func TestSetPeersUpstreamInjectedReadDirError(t *testing.T) {
	t.Parallel()

	// path is a directory, not a file: os.ReadFile fails with an error that
	// is not ErrNotExist, reaching the default (propagated error) branch.
	dir := t.TempDir()
	err := setPeersUpstreamInjected(dir, "https://hub.example.com", filepath.Join(dir, "token"), nil)
	if err == nil {
		t.Fatalf("setPeersUpstreamInjected(dir as path) = nil error, want an error")
	}
	if !strings.Contains(err.Error(), "read config") {
		t.Fatalf("error = %q, want mention of 'read config'", err.Error())
	}
}

func TestParsePeersBodyRejectsTab(t *testing.T) {
	t.Parallel()

	lines := []string{"peers:\n", "  upstream:\turl\n"}
	_, err := parsePeersBody(lines, 0, len(lines))
	if err != errUnsupportedPeersBody {
		t.Fatalf("parsePeersBody(tab body) error = %v, want errUnsupportedPeersBody", err)
	}
}

func TestParseFlatPeersScalarsRejectsTabAndIndentMismatch(t *testing.T) {
	t.Parallel()

	tabLines := []string{"    a: 1\tb\n"}
	if _, err := parseFlatPeersScalars(tabLines, 0, len(tabLines)); err != errUnsupportedPeersBody {
		t.Fatalf("parseFlatPeersScalars(tab) error = %v, want errUnsupportedPeersBody", err)
	}

	mismatchLines := []string{"  a: 1\n", "    b: 2\n"}
	if _, err := parseFlatPeersScalars(mismatchLines, 0, len(mismatchLines)); err != errUnsupportedPeersBody {
		t.Fatalf("parseFlatPeersScalars(indent mismatch) error = %v, want errUnsupportedPeersBody", err)
	}
}

func TestParseSimplePeersLineNoSpaceAfterColon(t *testing.T) {
	t.Parallel()

	_, _, _, ok := parseSimplePeersLine("key:value")
	if ok {
		t.Fatalf("parseSimplePeersLine(key:value) ok = true, want false (missing space after colon)")
	}
}

func TestParseSimplePeersLineBlankValueAfterTrim(t *testing.T) {
	t.Parallel()

	key, value, bare, ok := parseSimplePeersLine("key:   ")
	if !ok || !bare || key != "key" || value != "" {
		t.Fatalf("parseSimplePeersLine(key:   ) = %q, %q, %v, %v; want key, \"\", true, true", key, value, bare, ok)
	}
}

func TestIsSimplePeersKeyName(t *testing.T) {
	t.Parallel()

	if isSimplePeersKeyName("") {
		t.Fatalf("isSimplePeersKeyName(\"\") = true, want false")
	}
	if isSimplePeersKeyName("1abc") {
		t.Fatalf("isSimplePeersKeyName(1abc) = true, want false (digit-first key)")
	}
	if !isSimplePeersKeyName("url") {
		t.Fatalf("isSimplePeersKeyName(url) = false, want true")
	}
}

func TestSplitLinesKeepEndsEmpty(t *testing.T) {
	t.Parallel()

	if got := splitLinesKeepEnds(""); got != nil {
		t.Fatalf("splitLinesKeepEnds(\"\") = %#v, want nil", got)
	}
}

func TestVerifyPeersUpstreamEditMismatch(t *testing.T) {
	t.Parallel()

	updatedWrong := "peers:\n  upstream:\n    url: https://wrong.example.com\n    token_file: /etc/wb/token\n"
	err := verifyPeersUpstreamEdit("", updatedWrong, "https://hub.example.com", "/etc/wb/token")
	if err == nil {
		t.Fatalf("verifyPeersUpstreamEdit(mismatched upstream) = nil error, want an error")
	}
	if !strings.Contains(err.Error(), "result peers.upstream") {
		t.Fatalf("error = %q, want mention of result peers.upstream", err.Error())
	}
}

func TestVerifyPeersUpstreamEditDocumentCountChanged(t *testing.T) {
	t.Parallel()

	hubURL, tokenFile := "https://hub.example.com", "/etc/wb/token"
	original := "peers:\n  upstream:\n    url: https://hub.example.com\n    token_file: /etc/wb/token\n"
	updated := original + "---\nextra: 1\n"

	err := verifyPeersUpstreamEdit(original, updated, hubURL, tokenFile)
	if err == nil {
		t.Fatalf("verifyPeersUpstreamEdit(extra document) = nil error, want an error")
	}
	if !strings.Contains(err.Error(), "number of YAML documents changed") {
		t.Fatalf("error = %q, want mention of document count change", err.Error())
	}
}

func TestVerifyPeersUpstreamEditTextOutsideChanged(t *testing.T) {
	t.Parallel()

	hubURL, tokenFile := "https://hub.example.com", "/etc/wb/token"
	peersBlock := "peers:\n  upstream:\n    url: https://hub.example.com\n    token_file: /etc/wb/token\n"
	original := "# note A\n" + peersBlock
	updated := "# note B\n" + peersBlock

	err := verifyPeersUpstreamEdit(original, updated, hubURL, tokenFile)
	if err == nil {
		t.Fatalf("verifyPeersUpstreamEdit(text outside changed) = nil error, want an error")
	}
	if !strings.Contains(err.Error(), "text outside peers: changed") {
		t.Fatalf("error = %q, want mention of text outside peers: changed", err.Error())
	}
}

func TestVerifyPeersUpstreamEditPasses(t *testing.T) {
	t.Parallel()

	hubURL, tokenFile := "https://hub.example.com", "/etc/wb/token"
	peersBlock := "peers:\n  upstream:\n    url: https://hub.example.com\n    token_file: /etc/wb/token\n"
	if err := verifyPeersUpstreamEdit(peersBlock, peersBlock, hubURL, tokenFile); err != nil {
		t.Fatalf("verifyPeersUpstreamEdit(identical) = %v, want nil", err)
	}
}

func TestFirstPeersDocumentMap(t *testing.T) {
	t.Parallel()

	if got := firstPeersDocumentMap(nil); len(got) != 0 {
		t.Fatalf("firstPeersDocumentMap(nil) = %#v, want empty map", got)
	}
	want := map[string]any{"a": 1}
	if got := firstPeersDocumentMap([]any{want}); len(got) != 1 || got["a"] != 1 {
		t.Fatalf("firstPeersDocumentMap(map doc) = %#v, want %#v", got, want)
	}
	// docs[0] not a map[string]any (e.g. a scalar top-level document): the
	// type assertion fails and the fallback empty map is returned.
	if got := firstPeersDocumentMap([]any{"not a map"}); len(got) != 0 {
		t.Fatalf("firstPeersDocumentMap(non-map doc) = %#v, want empty map", got)
	}
}

func TestDecodeAllPeersDocumentsCommentOnly(t *testing.T) {
	t.Parallel()

	// A comment-only, non-blank document decodes to zero YAML documents;
	// the function must still return exactly one (empty map) document.
	docs, err := decodeAllPeersDocuments("# just a comment\n")
	if err != nil {
		t.Fatalf("decodeAllPeersDocuments: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("decodeAllPeersDocuments(comment only) = %d docs, want 1", len(docs))
	}
}
