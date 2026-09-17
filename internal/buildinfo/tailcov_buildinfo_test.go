package buildinfo

import (
	"testing"

	strongobuildinfo "github.com/strongo/buildinfo"
)

// TestTailCovRevisionStripsTheDirtySuffix pins the split between Revision and
// Modified: the bare SHA an agent records must not carry the "+dirty"
// decoration, and the decoration must still be observable on its own.
func TestTailCovRevisionStripsTheDirtySuffix(t *testing.T) {
	original := resolved
	t.Cleanup(func() { resolved = original })

	resolved = strongobuildinfo.Info{
		Name:    "wb",
		Version: "1.2.3",
		Commit:  "0123456789abcdef0123456789abcdef01234567" + dirtySuffix,
		Date:    "2026-09-15T10:11:12Z",
	}

	if got, want := Revision(), "0123456789abcdef0123456789abcdef01234567"; got != want {
		t.Errorf("Revision() = %q, want the bare SHA %q", got, want)
	}
	if !Modified() {
		t.Error("Modified() = false for a +dirty build tree")
	}
	if got, want := Date(), "2026-09-15T10:11:12Z"; got != want {
		t.Errorf("Date() = %q, want %q", got, want)
	}
}

// TestTailCovRevisionAndModifiedReportACleanBuild covers the other half of the
// suffix check: a clean tree reports the SHA unchanged and Modified false.
func TestTailCovRevisionAndModifiedReportACleanBuild(t *testing.T) {
	original := resolved
	t.Cleanup(func() { resolved = original })

	resolved = strongobuildinfo.Info{Commit: "abcdef", Date: ""}

	if got := Revision(); got != "abcdef" {
		t.Errorf("Revision() = %q, want the clean SHA unchanged", got)
	}
	if Modified() {
		t.Error("Modified() = true for a commit without the +dirty suffix")
	}
	if got := Date(); got != "" {
		t.Errorf("Date() = %q, want the empty unknown date passed through", got)
	}
}

// TestTailCovRevisionAndModifiedReportAnUnknownBuild is the ad-hoc `go build`
// case: nothing stamped, nothing embedded. Callers record these unconditionally,
// so every accessor must answer rather than panic.
func TestTailCovRevisionAndModifiedReportAnUnknownBuild(t *testing.T) {
	original := resolved
	t.Cleanup(func() { resolved = original })

	resolved = strongobuildinfo.Info{}

	if got := Revision(); got != "" {
		t.Errorf("Revision() = %q for an unknown build, want empty", got)
	}
	if Modified() {
		t.Errorf("Modified() = true for an unknown build, want false")
	}
	if got := Date(); got != "" {
		t.Errorf("Date() = %q for an unknown build, want empty", got)
	}
}
