package remotestate

import "testing"

func TestSameOriginNormalizesSchemeHostPortAndTrailingDot(t *testing.T) {
	for _, pair := range [][2]string{
		{"https://VM1.sneat.dev", "https://vm1.sneat.dev"},
		{"https://vm1.sneat.dev.", "https://vm1.sneat.dev"},
		{"https://vm1.sneat.dev:443", "https://vm1.sneat.dev"},
		{"http://vm1.sneat.dev:80", "http://vm1.sneat.dev"},
	} {
		if !SameOrigin(pair[0], pair[1]) {
			t.Fatalf("SameOrigin(%q, %q) = false, want true", pair[0], pair[1])
		}
	}
	if SameOrigin("https://vm1.sneat.dev:8443", "https://vm1.sneat.dev") {
		t.Fatal("sameOrigin must not ignore a non-default port")
	}
	if SameOrigin("https://vm1.sneat.dev", "https://vm2.sneat.dev") {
		t.Fatal("sameOrigin must not equate two different hosts")
	}
}

func TestSameOriginRejectsMalformedInputs(t *testing.T) {
	t.Parallel()
	for _, pair := range [][2]string{{"%zz", "https://hub.example.test"}, {"https://hub.example.test", "%zz"}} {
		if SameOrigin(pair[0], pair[1]) {
			t.Fatalf("malformed origin accepted: %q", pair)
		}
	}
}
