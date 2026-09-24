package wbconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSetPeersUpstreamLeavesRemoteByteIdentical encodes invite-and-join's
// "It MUST NOT change remote:" requirement, and AC:identity-and-admission's
// "writes peers.upstream with remote: byte-identical".
func TestSetPeersUpstreamLeavesRemoteByteIdentical(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "wb.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	original := "parallel: 3\nremote:\n  provider: git\n  repo: acme/state\n  publish:\n    unpushed: counts\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	remoteBlockBefore := extractBlock(t, original, "remote:")

	tokenFile := filepath.Join(filepath.Dir(path), "credentials", "peer-vm1.token")
	if err := SetPeersUpstream(path, "https://vm1.sneat.dev", tokenFile); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	remoteBlockAfter := extractBlock(t, text, "remote:")
	if strings.TrimRight(remoteBlockBefore, "\n") != strings.TrimRight(remoteBlockAfter, "\n") {
		t.Fatalf("remote: block changed:\nbefore:\n%s\nafter:\n%s", remoteBlockBefore, remoteBlockAfter)
	}
	for _, want := range []string{"parallel: 3", "peers:", "upstream:", "url: https://vm1.sneat.dev", "token_file: " + tokenFile} {
		if !strings.Contains(text, want) {
			t.Errorf("updated config lacks %q:\n%s", want, text)
		}
	}

	upstream, found, err := LoadPeersUpstream(path)
	if err != nil || !found || upstream.URL != "https://vm1.sneat.dev" || upstream.TokenFile != tokenFile {
		t.Fatalf("LoadPeersUpstream = %+v, %t, %v", upstream, found, err)
	}
}

// TestSetPeersUpstreamPreservesNonCanonicalIndentationAndComments is M3: a
// text-splice implementation must leave remote: byte-identical for ANY
// valid YAML indentation, not just the 2-space canonical form a full
// yaml.Encoder round trip would have silently reformatted it to.
func TestSetPeersUpstreamPreservesNonCanonicalIndentationAndComments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wb.yaml")
	original := "parallel: 3\n" +
		"# a hand-written comment above remote\n" +
		"remote:\n" +
		"    provider: git # inline comment\n" +
		"    repo: acme/state\n" +
		"    publish:\n" +
		"        unpushed: counts\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := SetPeersUpstream(path, "https://vm1.sneat.dev", tokenFile); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, original) {
		t.Fatalf("4-space-indented remote: with comments was not preserved verbatim:\nwant substring:\n%s\ngot:\n%s", original, text)
	}
	if !strings.Contains(text, "peers:\n  upstream:\n") {
		t.Fatalf("peers.upstream block missing or mis-shaped:\n%s", text)
	}
}

func TestSetPeersUpstreamCreatesConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "wb.yaml")
	if err := SetPeersUpstream(path, "https://vm1.sneat.dev", filepath.Join(t.TempDir(), "token")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestSetPeersUpstreamIsIdempotentAndUpdatesInPlace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wb.yaml")
	if err := SetPeersUpstream(path, "https://vm1.sneat.dev", "/abs/one.token"); err != nil {
		t.Fatal(err)
	}
	if err := SetPeersUpstream(path, "https://vm2.sneat.dev", "/abs/two.token"); err != nil {
		t.Fatal(err)
	}
	upstream, found, err := LoadPeersUpstream(path)
	if err != nil || !found || upstream.URL != "https://vm2.sneat.dev" || upstream.TokenFile != "/abs/two.token" {
		t.Fatalf("LoadPeersUpstream after re-join = %+v, %t, %v", upstream, found, err)
	}
}

func TestLoadPeersUpstreamReportsAbsence(t *testing.T) {
	dir := t.TempDir()
	if upstream, found, err := LoadPeersUpstream(filepath.Join(dir, "absent.yaml")); err != nil || found || upstream != (PeersUpstreamConfig{}) {
		t.Fatalf("LoadPeersUpstream(missing file) = %+v, %t, %v", upstream, found, err)
	}
	path := filepath.Join(dir, "wb.yaml")
	if err := os.WriteFile(path, []byte("remote:\n  provider: git\n  repo: acme/state\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if upstream, found, err := LoadPeersUpstream(path); err != nil || found || upstream != (PeersUpstreamConfig{}) {
		t.Fatalf("LoadPeersUpstream(no peers section) = %+v, %t, %v", upstream, found, err)
	}
}

func TestSetPeersUpstreamRejectsAMalformedPeersSection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(path, []byte("peers: not-a-mapping\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetPeersUpstream(path, "https://vm1.sneat.dev", "/abs/token"); err == nil {
		t.Fatal("expected an error for a non-mapping peers section")
	}
}

// TestSetPeersUpstreamValidatesURLAndTokenFile is M5: Set refuses a
// non-https/non-loopback URL and a relative token_file, before writing
// anything.
func TestSetPeersUpstreamValidatesURLAndTokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wb.yaml")
	if err := SetPeersUpstream(path, "http://vm1.sneat.dev", "/abs/token"); err == nil {
		t.Fatal("expected a refusal for a non-loopback http:// URL")
	}
	if err := SetPeersUpstream(path, "https://vm1.sneat.dev", "relative/token"); err == nil {
		t.Fatal("expected a refusal for a relative token_file")
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("a refused Set must never create the config file")
	}
	// The loopback exception still works, matching join's own same rule.
	if err := SetPeersUpstream(path, "http://127.0.0.1:9999", "/abs/token"); err != nil {
		t.Fatalf("loopback http:// must be accepted: %v", err)
	}
}

// TestLoadPeersUpstreamValidatesURLAndTokenFile is M5's Load-side half: a
// hand-edited or corrupted peers.upstream section is refused clearly rather
// than handed to a caller as if it were well-formed.
func TestLoadPeersUpstreamValidatesURLAndTokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(path, []byte("peers:\n  upstream:\n    url: ftp://vm1.sneat.dev\n    token_file: /abs/token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, found, err := LoadPeersUpstream(path); err == nil || found {
		t.Fatalf("expected LoadPeersUpstream to refuse an ftp:// URL, got found=%t err=%v", found, err)
	}

	if err := os.WriteFile(path, []byte("peers:\n  upstream:\n    url: https://vm1.sneat.dev\n    token_file: relative/token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, found, err := LoadPeersUpstream(path); err == nil || found {
		t.Fatalf("expected LoadPeersUpstream to refuse a relative token_file, got found=%t err=%v", found, err)
	}
}

func TestSetPeersUpstreamRejectsUnparsableYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(path, []byte("not: [valid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetPeersUpstream(path, "https://vm1.sneat.dev", "/abs/token"); err == nil {
		t.Fatal("expected an error for unparsable YAML")
	}
	if _, _, err := LoadPeersUpstream(path); err == nil {
		t.Fatal("expected LoadPeersUpstream to reject unparsable YAML")
	}
}

// extractBlock returns the top-level YAML block starting at key (e.g.
// "remote:") up to (not including) the next top-level key or EOF, so a test
// can compare it byte-for-byte before and after an unrelated edit.
func extractBlock(t *testing.T, text, key string) string {
	t.Helper()
	lines := strings.Split(text, "\n")
	start := -1
	for i, line := range lines {
		if line == key {
			start = i
			break
		}
	}
	if start == -1 {
		t.Fatalf("block %q not found in:\n%s", key, text)
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if len(lines[i]) > 0 && lines[i][0] != ' ' && lines[i][0] != '\t' {
			end = i
			break
		}
	}
	return strings.Join(lines[start:end], "\n")
}
