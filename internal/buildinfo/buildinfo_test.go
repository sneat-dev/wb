package buildinfo

import "testing"

func TestVersionPrefersTheLinkTimeStamp(t *testing.T) {
	t.Cleanup(func() { Set("") })

	Set("v1.2.3")
	if got := Version(); got != "v1.2.3" {
		t.Fatalf("Version() = %q, want the stamped v1.2.3", got)
	}
}

// With no stamp the resolver falls back to embedded build info, and failing
// that to Unknown. Either is acceptable depending on how the test binary was
// produced; what must never happen is an empty string, since callers record
// this value unconditionally.
func TestVersionIsNeverEmpty(t *testing.T) {
	t.Cleanup(func() { Set("") })

	Set("")
	if got := Version(); got == "" {
		t.Fatal("Version() = \"\", want a resolved version or Unknown")
	}
}
