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

// AC: cli-install#req:version-json-contract ("version | ... dev when the
// build cannot determine it") — review M1: JSON()'s own version key MUST
// spell the fleet-wide contract's undetermined placeholder "dev", even
// though Version() and the plain `wb version` text banner keep printing
// Unknown ("unknown"), wb's own longstanding convention.
func TestJSONTranslatesUnknownToDevForContract(t *testing.T) {
	t.Cleanup(func() { Set("") })

	Set(Unknown)
	if got := JSON().Version; got != "dev" {
		t.Fatalf(`JSON().Version = %q, want the cli-install contract's "dev" placeholder for an undetermined build`, got)
	}
	if got := Version(); got != Unknown {
		t.Fatalf("Version() = %q, want %q unchanged — only JSON() translates the placeholder", got, Unknown)
	}
}

// A determined version (whatever resolve() or Set found) MUST pass through
// JSON() unchanged: the "dev" translation applies only to Unknown itself.
func TestJSONPassesThroughADeterminedVersionUnchanged(t *testing.T) {
	t.Cleanup(func() { Set("") })

	Set("1.2.3")
	if got := JSON().Version; got != "1.2.3" {
		t.Fatalf("JSON().Version = %q, want 1.2.3 unchanged", got)
	}
}
