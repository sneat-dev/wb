package wbconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- isLoopbackPeersHost ---

func TestIsLoopbackPeersHostAcceptsLocalhostCaseInsensitively(t *testing.T) {
	t.Parallel()
	for _, host := range []string{"localhost", "LOCALHOST", "LocalHost"} {
		if !isLoopbackPeersHost(host) {
			t.Errorf("isLoopbackPeersHost(%q) = false, want true", host)
		}
	}
}

// --- parsePeersBody ---

func TestParsePeersBodySkipsBlankLinesBetweenChildren(t *testing.T) {
	t.Parallel()
	lines := []string{"peers:\n", "\n", "  other: 1\n"}
	body, err := parsePeersBody(lines, 0, len(lines))
	if err != nil {
		t.Fatalf("parsePeersBody with a blank line = %v, want nil error", err)
	}
	if body.hasUpstream {
		t.Fatalf("parsePeersBody found an upstream child that was never there: %+v", body)
	}
}

func TestParsePeersBodyRejectsATabInABodyLine(t *testing.T) {
	t.Parallel()
	lines := []string{"peers:\n", "\tother: 1\n"}
	if _, err := parsePeersBody(lines, 0, len(lines)); err != errUnsupportedPeersBody {
		t.Fatalf("parsePeersBody with a tab-indented line = %v, want errUnsupportedPeersBody", err)
	}
}

// --- parseFlatPeersScalars ---

func TestParseFlatPeersScalarsRejectsATab(t *testing.T) {
	t.Parallel()
	lines := []string{"\tfoo: 1\n"}
	if _, err := parseFlatPeersScalars(lines, 0, 1); err != errUnsupportedPeersBody {
		t.Fatalf("parseFlatPeersScalars with a tab = %v, want errUnsupportedPeersBody", err)
	}
}

func TestParseFlatPeersScalarsRejectsAnInconsistentIndent(t *testing.T) {
	t.Parallel()
	lines := []string{"  a: 1\n", "    b: 2\n"}
	if _, err := parseFlatPeersScalars(lines, 0, 2); err != errUnsupportedPeersBody {
		t.Fatalf("parseFlatPeersScalars with a changing indent = %v, want errUnsupportedPeersBody", err)
	}
}

// --- parseSimplePeersLine ---

func TestParseSimplePeersLineRejectsAValueWithNoSpaceAfterTheColon(t *testing.T) {
	t.Parallel()
	if _, _, _, ok := parseSimplePeersLine("key:x"); ok {
		t.Fatal("parseSimplePeersLine(\"key:x\") = ok, want a refusal (no space after colon)")
	}
}

func TestParseSimplePeersLineTreatsATrailingSpaceAsBare(t *testing.T) {
	t.Parallel()
	key, value, bare, ok := parseSimplePeersLine("key: ")
	if !ok || !bare || value != "" || key != "key" {
		t.Fatalf("parseSimplePeersLine(\"key: \") = (%q, %q, %t, %t), want (\"key\", \"\", true, true)", key, value, bare, ok)
	}
}

// --- isSimplePeersKeyName ---

func TestIsSimplePeersKeyNameRejectsAnEmptyKey(t *testing.T) {
	t.Parallel()
	if isSimplePeersKeyName("") {
		t.Fatal("isSimplePeersKeyName(\"\") = true, want false")
	}
}

func TestIsSimplePeersKeyNameAcceptsADigitAfterTheFirstCharacter(t *testing.T) {
	t.Parallel()
	if !isSimplePeersKeyName("a1") {
		t.Fatal("isSimplePeersKeyName(\"a1\") = false, want true")
	}
}

func TestIsSimplePeersKeyNameRejectsAnUnsupportedCharacter(t *testing.T) {
	t.Parallel()
	if isSimplePeersKeyName("a$") {
		t.Fatal("isSimplePeersKeyName(\"a$\") = true, want false")
	}
}

// --- splitLinesKeepEnds ---

func TestSplitLinesKeepEndsReturnsNilForEmptyText(t *testing.T) {
	t.Parallel()
	if lines := splitLinesKeepEnds(""); lines != nil {
		t.Fatalf("splitLinesKeepEnds(\"\") = %#v, want nil", lines)
	}
}

// --- decodeAllPeersDocuments ---

func TestDecodeAllPeersDocumentsSurfacesAParseFailure(t *testing.T) {
	t.Parallel()
	if _, err := decodeAllPeersDocuments("peers: [\n"); err == nil {
		t.Fatal("decodeAllPeersDocuments with unterminated flow syntax = nil error, want one")
	}
}

func TestDecodeAllPeersDocumentsTreatsACommentOnlyStreamAsOneEmptyDocument(t *testing.T) {
	t.Parallel()
	docs, err := decodeAllPeersDocuments("# just a comment, no content\n")
	if err != nil {
		t.Fatalf("decodeAllPeersDocuments(comment-only) = %v, want nil error", err)
	}
	if len(docs) != 1 {
		t.Fatalf("decodeAllPeersDocuments(comment-only) = %d docs, want exactly 1 implicit empty document", len(docs))
	}
}

// --- firstPeersDocumentMap ---

func TestFirstPeersDocumentMapReturnsEmptyForNoDocuments(t *testing.T) {
	t.Parallel()
	m := firstPeersDocumentMap(nil)
	if m == nil || len(m) != 0 {
		t.Fatalf("firstPeersDocumentMap(nil) = %#v, want an empty (non-nil) map", m)
	}
}

func TestFirstPeersDocumentMapReturnsEmptyWhenTheFirstDocumentIsNotAMapping(t *testing.T) {
	t.Parallel()
	m := firstPeersDocumentMap([]any{"not a mapping"})
	if m == nil || len(m) != 0 {
		t.Fatalf("firstPeersDocumentMap([\"not a mapping\"]) = %#v, want an empty (non-nil) map", m)
	}
}

// --- setPeersUpstreamInjected ---

func TestSetPeersUpstreamSurfacesANonNotExistReadFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// A directory where SetPeersUpstream expects a regular config file: the
	// read fails, but not with os.ErrNotExist, so it must be reported
	// rather than treated as "no existing config".
	path := filepath.Join(dir, "wb.yaml")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	err := SetPeersUpstream(path, "https://x.example", "/tok")
	if err == nil || !strings.Contains(err.Error(), "read config") {
		t.Fatalf("SetPeersUpstream(path is a directory) = %v, want a \"read config\" error", err)
	}
}

// --- verifyPeersUpstreamEdit ---
//
// verifyPeersUpstreamEdit is SetPeersUpstream's pre-write safety net. Each
// check below isolates exactly one of its independent verifications by
// crafting original/updated text pairs that pass every earlier check and
// fail only the one under test.

const validUpstreamDoc = "peers:\n  upstream:\n    url: https://x.example\n    token_file: /tok\n"

func TestVerifyPeersUpstreamEditRejectsAResultThatDoesNotMatchTheRequestedValue(t *testing.T) {
	t.Parallel()
	updated := "peers:\n  upstream:\n    url: https://wrong.example\n    token_file: /tok\n"
	err := verifyPeersUpstreamEdit("", updated, "https://x.example", "/tok")
	if err == nil || !strings.Contains(err.Error(), "result peers.upstream") {
		t.Fatalf("verifyPeersUpstreamEdit(mismatched result) = %v, want a \"result peers.upstream\" error", err)
	}
}

func TestVerifyPeersUpstreamEditSurfacesAnUnparsableOriginal(t *testing.T) {
	t.Parallel()
	original := "peers: [\n"
	err := verifyPeersUpstreamEdit(original, validUpstreamDoc, "https://x.example", "/tok")
	if err == nil || !strings.Contains(err.Error(), "re-check original") {
		t.Fatalf("verifyPeersUpstreamEdit(unparsable original) = %v, want a \"re-check original\" error", err)
	}
}

func TestVerifyPeersUpstreamEditSurfacesAnUnparsableResult(t *testing.T) {
	t.Parallel()
	// The first document (what parsePeersUpstream reads) is valid and
	// matches; the second document in the stream is malformed. Only
	// decodeAllPeersDocuments walks every document, so this isolates its
	// failure from the earlier, first-document-only check.
	updated := validUpstreamDoc + "---\nbroken: [\n"
	err := verifyPeersUpstreamEdit("", updated, "https://x.example", "/tok")
	if err == nil || !strings.Contains(err.Error(), "re-check result") {
		t.Fatalf("verifyPeersUpstreamEdit(unparsable second document) = %v, want a \"re-check result\" error", err)
	}
}

func TestVerifyPeersUpstreamEditRejectsAChangedDocumentCount(t *testing.T) {
	t.Parallel()
	original := "unrelated: 1\n"
	updated := validUpstreamDoc + "---\nunrelated: 1\n"
	err := verifyPeersUpstreamEdit(original, updated, "https://x.example", "/tok")
	if err == nil || !strings.Contains(err.Error(), "number of YAML documents changed") {
		t.Fatalf("verifyPeersUpstreamEdit(document count changed) = %v, want a document-count error", err)
	}
}

func TestVerifyPeersUpstreamEditRejectsAChangedNonFirstDocument(t *testing.T) {
	t.Parallel()
	original := validUpstreamDoc + "---\nunrelated: 1\n"
	updated := validUpstreamDoc + "---\nunrelated: 2\n"
	err := verifyPeersUpstreamEdit(original, updated, "https://x.example", "/tok")
	if err == nil || !strings.Contains(err.Error(), "document 2 changed") {
		t.Fatalf("verifyPeersUpstreamEdit(second document changed) = %v, want a \"document 2 changed\" error", err)
	}
}

func TestVerifyPeersUpstreamEditRejectsAChangedSiblingKey(t *testing.T) {
	t.Parallel()
	original := "foo: 1\n" + validUpstreamDoc
	updated := "foo: 2\n" + validUpstreamDoc
	err := verifyPeersUpstreamEdit(original, updated, "https://x.example", "/tok")
	if err == nil || !strings.Contains(err.Error(), "a key other than peers changed") {
		t.Fatalf("verifyPeersUpstreamEdit(sibling key changed) = %v, want a \"key other than peers\" error", err)
	}
}

func TestVerifyPeersUpstreamEditRejectsAChangedPeersSibling(t *testing.T) {
	t.Parallel()
	original := "peers:\n  other: 1\n  upstream:\n    url: https://x.example\n    token_file: /tok\n"
	updated := "peers:\n  other: 2\n  upstream:\n    url: https://x.example\n    token_file: /tok\n"
	err := verifyPeersUpstreamEdit(original, updated, "https://x.example", "/tok")
	if err == nil || !strings.Contains(err.Error(), "a peers: child other than upstream changed") {
		t.Fatalf("verifyPeersUpstreamEdit(peers sibling changed) = %v, want a \"peers: child\" error", err)
	}
}

func TestVerifyPeersUpstreamEditRejectsChangedTextOutsideThePeersBlock(t *testing.T) {
	t.Parallel()
	// Only a comment differs, so the decoded structural maps are identical
	// (comments carry no value) and every earlier check passes; this
	// isolates the final byte-for-byte textual check.
	original := "# note one\nfoo: 1\n" + validUpstreamDoc
	updated := "# note two\nfoo: 1\n" + validUpstreamDoc
	err := verifyPeersUpstreamEdit(original, updated, "https://x.example", "/tok")
	if err == nil || !strings.Contains(err.Error(), "text outside peers: changed") {
		t.Fatalf("verifyPeersUpstreamEdit(comment changed outside peers:) = %v, want a \"text outside peers:\" error", err)
	}
}
