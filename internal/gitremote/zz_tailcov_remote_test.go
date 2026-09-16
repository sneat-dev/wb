package gitremote

import (
	"strings"
	"testing"
)

// TestTailCovParseRejectsEveryMalformedRemoteShape walks each rejection branch
// the original suite left unexecuted. Every spelling below is a distinct way a
// remote can be malformed or unsafe, and each must be refused with the
// diagnostic for that specific reason — not merely any error — without ever
// echoing the rejected spelling back into the diagnostic.
func TestTailCovParseRejectsEveryMalformedRemoteShape(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		reason string
	}{
		{"url without a host name", "https://:8080/acme/app.git", "missing a valid host or path"},
		{"file remote naming a host", "file://otherhost/projects/acme/app", "file repository remote must be a local absolute path"},
		{"unsupported scheme", "ftp://github.com/acme/app.git", "unsupported scheme"},
		{"scp remote with an empty host", ":acme/app.git", "not a strict scp-style remote"},
		{"scp remote with a non-git user", "other@github.com:acme/app.git", "unsupported user information"},
		{"scp remote with an invalid host", "git@git_hub.com:acme/app.git", "host is invalid"},
		{"scp remote without an owner", "github.com:app.git", "exactly owner/repository"},
		{"absolute path that is not clean", "/projects/../projects/acme/app", "absolute clean path"},
		{"absolute path without an owner", "/app", "owner/repository identity"},
		{"absolute owner that is not leadable", "/tmp/-bad/app", "owner/repository is invalid"},
		{"owner with an unsafe character", "github.com:a!me/app.git", "owner/repository is invalid"},
		{"scp remote with a dot owner", "github.com:./app.git", "owner/repository is invalid"},
		{"host label with a trailing dash", "https://git-.example/acme/app.git", "missing a valid host or path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			remote, err := Parse(tc.raw)
			if err == nil {
				t.Fatalf("Parse(%q) = %#v, want rejection", tc.raw, remote)
			}
			if remote.Identity != (Identity{}) {
				t.Fatalf("Parse(%q) claimed an identity for a rejected remote: %#v", tc.raw, remote.Identity)
			}
			if !strings.Contains(err.Error(), tc.reason) {
				t.Errorf("Parse(%q) error = %q, want it to mention %q", tc.raw, err, tc.reason)
			}
			if strings.Contains(err.Error(), tc.raw) {
				t.Errorf("Parse(%q) disclosed the rejected spelling: %q", tc.raw, err)
			}
		})
	}
}
