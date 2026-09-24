package sessionmove

import (
	"strings"
	"testing"
)

// NOTE: NewHandoffID/NewMessageID's rand.Read error branch (types.go:58,
// message.go:83) cannot be driven from a test: crypto/rand.Reader is still
// swappable, but on this Go toolchain crypto/rand.Read treats any error
// from the configured Reader as an unrecoverable entropy failure and calls
// runtime fatal (process-crashing), not a returned error. Reaching that
// `if err != nil` branch needs a seam (an injectable ID-generation source)
// -- task 8-13 territory, not test-only.

func TestParseWorkLogReferenceRejectsDotSegments(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"effort is dot":     "worklog:./run-1/" + strings.Repeat("a", 64),
		"effort is dot-dot": "worklog:../run-1/" + strings.Repeat("a", 64),
		"run is dot":        "worklog:effort-1/./" + strings.Repeat("a", 64),
		"run is dot-dot":    "worklog:effort-1/../" + strings.Repeat("a", 64),
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseWorkLogReference(value); err == nil {
				t.Fatalf("ParseWorkLogReference(%q) succeeded, want an unsafe path segment error", value)
			}
		})
	}
}

func TestExpectedTargetWorkLogReferenceRejectsInvalidRequest(t *testing.T) {
	t.Parallel()
	request := validRequest()
	request.HandoffID = ""
	if _, err := ExpectedTargetWorkLogReference(request, DigestBytes([]byte("x"))); err == nil {
		t.Fatal("ExpectedTargetWorkLogReference accepted an invalid request, want error")
	}
}

func TestExpectedTargetWorkLogReferenceRejectsInvalidDigest(t *testing.T) {
	t.Parallel()
	request := validRequest()
	if _, err := ExpectedTargetWorkLogReference(request, Digest("not-a-digest")); err == nil {
		t.Fatal("ExpectedTargetWorkLogReference accepted an invalid digest, want error")
	}
}
