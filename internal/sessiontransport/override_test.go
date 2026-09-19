package sessiontransport

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "wb.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write fixture config: %v", err)
	}
	return path
}

func TestLoadOverrideAbsentFileIsNotAnError(t *testing.T) {
	t.Parallel()
	kind, ok, err := LoadOverride(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err != nil {
		t.Fatalf("LoadOverride(missing file) error = %v, want nil", err)
	}
	if ok {
		t.Fatalf("LoadOverride(missing file) ok = true, want false")
	}
	if kind != "" {
		t.Fatalf("LoadOverride(missing file) kind = %q, want empty", kind)
	}
}

func TestLoadOverrideNoSessionSectionIsNotAnError(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, "session_move:\n  targets: {}\n")
	kind, ok, err := LoadOverride(path)
	if err != nil {
		t.Fatalf("LoadOverride(no session section) error = %v, want nil", err)
	}
	if ok {
		t.Fatalf("LoadOverride(no session section) ok = true, want false")
	}
	if kind != "" {
		t.Fatalf("LoadOverride(no session section) kind = %q, want empty", kind)
	}
}

func TestLoadOverrideNoTransportKeyIsNotAnError(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, "session: {}\n")
	_, ok, err := LoadOverride(path)
	if err != nil {
		t.Fatalf("LoadOverride(empty session section) error = %v, want nil", err)
	}
	if ok {
		t.Fatalf("LoadOverride(empty session section) ok = true, want false")
	}
}

func TestLoadOverrideReadsConfiguredTransport(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, "session:\n  transport: tmux\n")
	kind, ok, err := LoadOverride(path)
	if err != nil {
		t.Fatalf("LoadOverride error = %v, want nil", err)
	}
	if !ok {
		t.Fatalf("LoadOverride ok = false, want true")
	}
	if kind != KindTmux {
		t.Fatalf("LoadOverride kind = %q, want %q", kind, KindTmux)
	}
}

func TestLoadOverrideToleratesOtherTopLevelSections(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, "session_move:\n  targets:\n    mac:\n      default_courier: ssh\n      ssh:\n        host: mac\nsession:\n  transport: herdr\n")
	kind, ok, err := LoadOverride(path)
	if err != nil {
		t.Fatalf("LoadOverride error = %v, want nil", err)
	}
	if !ok || kind != KindHerdr {
		t.Fatalf("LoadOverride = (%q, %v), want (%q, true)", kind, ok, KindHerdr)
	}
}

func TestLoadOverrideUnreadableFileIsAnError(t *testing.T) {
	t.Parallel()
	// A directory can never be read as a config file: os.ReadFile fails
	// with something other than os.ErrNotExist, exercising the distinct
	// "the file exists but could not be read" branch.
	_, _, err := LoadOverride(t.TempDir())
	if err == nil {
		t.Fatal("LoadOverride(a directory) error = nil, want an error")
	}
}

func TestLoadOverrideMalformedYAMLIsAnError(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, "session: [this is not a mapping\n")
	_, _, err := LoadOverride(path)
	if err == nil {
		t.Fatal("LoadOverride(malformed YAML) error = nil, want an error")
	}
}

// TestLoadOverrideUnknownFieldFailsClosed proves S3: a typo in the session
// section, such as "transprot", fails closed with an error rather than
// silently decoding as "no override configured" (KnownFields(true),
// following internal/lifecyclehooks/config.go's pattern).
func TestLoadOverrideUnknownFieldFailsClosed(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, "session:\n  transprot: tmux\n")
	kind, ok, err := LoadOverride(path)
	if err == nil {
		t.Fatalf("LoadOverride(typo'd field) = (%q, %v, nil), want a strict-decode error", kind, ok)
	}
}

// TestLoadOverrideTopLevelSessionNotAMappingIsAnError exercises
// mappingValue's "top level must be a mapping" branch directly through
// LoadOverride: a document whose top level is a bare scalar or sequence,
// not a mapping, cannot contain a `session:` key at all.
func TestLoadOverrideTopLevelSessionNotAMappingIsAnError(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, "- just\n- a\n- sequence\n")
	_, _, err := LoadOverride(path)
	if err == nil {
		t.Fatal("LoadOverride(non-mapping top level) error = nil, want an error")
	}
}

// TestLoadOverrideEmptyFileIsNotAnError exercises the "document has no
// content at all" branch: an empty file parses to a Node with no Content,
// which is not an error — there is simply nothing to find.
func TestLoadOverrideEmptyFileIsNotAnError(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, "")
	kind, ok, err := LoadOverride(path)
	if err != nil {
		t.Fatalf("LoadOverride(empty file) error = %v, want nil", err)
	}
	if ok || kind != "" {
		t.Fatalf("LoadOverride(empty file) = (%q, %v), want (\"\", false)", kind, ok)
	}
}

// TestLoadOverrideExplicitNullSessionIsNotAnError exercises the
// "sessionNode.Tag == !!null" branch: `session:` with nothing after it
// decodes to an explicit YAML null, which is "no override configured", not
// a decode error.
func TestLoadOverrideExplicitNullSessionIsNotAnError(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, "session:\n")
	kind, ok, err := LoadOverride(path)
	if err != nil {
		t.Fatalf("LoadOverride(null session) error = %v, want nil", err)
	}
	if ok || kind != "" {
		t.Fatalf("LoadOverride(null session) = (%q, %v), want (\"\", false)", kind, ok)
	}
}

// TestResolveOverrideExplicitTmuxOverrideFailsClosedWithNoTmuxBinary proves
// AC:explicit-override-fails-closed directly: `wb.yaml` names
// session.transport: tmux, no tmux binary is on PATH, and resolving the
// transport refuses rather than silently falling back to herdr or none.
func TestResolveOverrideExplicitTmuxOverrideFailsClosedWithNoTmuxBinary(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, "session:\n  transport: tmux\n")
	requested, ok, err := LoadOverride(path)
	if err != nil || !ok {
		t.Fatalf("LoadOverride = (%q, %v, %v), want (tmux, true, nil)", requested, ok, err)
	}

	noTmuxOnPath := func(string) (string, error) {
		return "", errors.New("exec: \"tmux\": executable file not found in $PATH")
	}

	resolved, err := ResolveOverride(requested, CheckTmuxBinary(noTmuxOnPath))
	if err == nil {
		t.Fatalf("ResolveOverride succeeded with resolved = %q, want a fail-closed error", resolved)
	}
	if !errors.Is(err, ErrOverridePrerequisiteUnavailable) {
		t.Fatalf("ResolveOverride error = %v, want ErrOverridePrerequisiteUnavailable", err)
	}
	if resolved == KindHerdr || resolved == KindNone {
		t.Fatalf("ResolveOverride silently fell back to %q instead of refusing", resolved)
	}
	if resolved != "" {
		t.Fatalf("ResolveOverride returned non-empty Kind %q alongside an error", resolved)
	}
}

func TestResolveOverrideExplicitTmuxOverrideSucceedsWhenTmuxIsAvailable(t *testing.T) {
	t.Parallel()
	tmuxOnPath := func(file string) (string, error) {
		if file != "tmux" {
			t.Fatalf("CheckTmuxBinary looked up %q, want \"tmux\"", file)
		}
		return "/usr/local/bin/tmux", nil
	}
	resolved, err := ResolveOverride(KindTmux, CheckTmuxBinary(tmuxOnPath))
	if err != nil {
		t.Fatalf("ResolveOverride error = %v, want nil", err)
	}
	if resolved != KindTmux {
		t.Fatalf("ResolveOverride = %q, want %q", resolved, KindTmux)
	}
}

func TestResolveOverrideRejectsAnUnshippedTransport(t *testing.T) {
	t.Parallel()
	resolved, err := ResolveOverride("docker", nil)
	if err == nil {
		t.Fatalf("ResolveOverride(\"docker\") succeeded with %q, want an error", resolved)
	}
	if !errors.Is(err, ErrOverrideInvalid) {
		t.Fatalf("ResolveOverride error = %v, want ErrOverrideInvalid", err)
	}
	if resolved != "" {
		t.Fatalf("ResolveOverride(\"docker\") resolved = %q, want empty", resolved)
	}
}

// TestResolveOverrideNilCheckAcceptsNoneUnconditionally proves half of M5
// hermetically: none has no runtime prerequisite at all, so
// defaultPrerequisiteCheck(KindNone) always returns nil regardless of the
// host's real tmux/herdr installation state — CheckTmuxBinary and
// CheckHerdrBinary both report nil immediately for any Kind other than
// their own.
func TestResolveOverrideNilCheckAcceptsNoneUnconditionally(t *testing.T) {
	t.Parallel()
	resolved, err := ResolveOverride(KindNone, nil)
	if err != nil {
		t.Fatalf("ResolveOverride(none, nil) error = %v, want nil", err)
	}
	if resolved != KindNone {
		t.Fatalf("ResolveOverride(none, nil) = %q, want %q", resolved, KindNone)
	}
}

// TestResolveOverrideNilCheckAppliesDefaultRatherThanSkipping proves M5's
// actual claim — nil does not mean "skip validation" — by substituting a
// fake default check for the duration of this test. It does not call
// t.Parallel: it mutates the package-level defaultPrerequisiteCheck var,
// which every other test in this package (and any parallel one) could
// otherwise observe mid-mutation.
func TestResolveOverrideNilCheckAppliesDefaultRatherThanSkipping(t *testing.T) {
	original := defaultPrerequisiteCheck
	t.Cleanup(func() { defaultPrerequisiteCheck = original })
	fakeFailure := errors.New("fake default prerequisite failure")
	defaultPrerequisiteCheck = func(Kind) error { return fakeFailure }

	resolved, err := ResolveOverride(KindHerdr, nil)
	if err == nil {
		t.Fatalf("ResolveOverride(herdr, nil) succeeded with %q; want the default check enforced, not skipped", resolved)
	}
	if !errors.Is(err, fakeFailure) {
		t.Fatalf("ResolveOverride(herdr, nil) error = %v, want it to wrap the fake default failure", err)
	}
	if resolved != "" {
		t.Fatalf("ResolveOverride(herdr, nil) resolved = %q alongside an error, want empty", resolved)
	}
}

func TestCheckTmuxBinaryIgnoresOtherKinds(t *testing.T) {
	t.Parallel()
	neverCalled := func(string) (string, error) {
		t.Fatal("CheckTmuxBinary's lookPath must not be called for a non-tmux Kind")
		return "", nil
	}
	if err := CheckTmuxBinary(neverCalled)(KindHerdr); err != nil {
		t.Fatalf("CheckTmuxBinary(...)(KindHerdr) = %v, want nil", err)
	}
	if err := CheckTmuxBinary(neverCalled)(KindNone); err != nil {
		t.Fatalf("CheckTmuxBinary(...)(KindNone) = %v, want nil", err)
	}
}

func TestCheckTmuxBinaryDefaultsToExecLookPath(t *testing.T) {
	t.Parallel()
	// A nil lookPath must not panic; it falls back to exec.LookPath, whose
	// outcome depends on the host and is not asserted here.
	_ = CheckTmuxBinary(nil)(KindTmux)
}

func TestCheckHerdrBinaryIgnoresOtherKinds(t *testing.T) {
	t.Parallel()
	neverCalled := func(string) (string, bool) {
		t.Fatal("CheckHerdrBinary's lookup must not be called for a non-herdr Kind")
		return "", false
	}
	if err := CheckHerdrBinary(neverCalled)(KindTmux); err != nil {
		t.Fatalf("CheckHerdrBinary(...)(KindTmux) = %v, want nil", err)
	}
	if err := CheckHerdrBinary(neverCalled)(KindNone); err != nil {
		t.Fatalf("CheckHerdrBinary(...)(KindNone) = %v, want nil", err)
	}
}

// TestCheckHerdrBinaryFailsClosedOnAnInvalidBinPath keeps the test
// hermetic by making the stat on HERDR_BIN_PATH fail with something other
// than "does not exist": internal/herdr.ResolveBinary falls back to a real
// PATH lookup only for a stale (not-exist) HERDR_BIN_PATH, and this repo's
// host may or may not actually have herdr installed. A NUL byte makes
// os.Stat fail with "invalid argument" instead, so ResolveBinary returns
// that error directly without ever touching the real PATH.
func TestCheckHerdrBinaryFailsClosedOnAnInvalidBinPath(t *testing.T) {
	t.Parallel()
	invalidBinPath := func(key string) (string, bool) {
		if key == "HERDR_BIN_PATH" {
			return "/definitely\x00invalid", true
		}
		return "", false
	}
	err := CheckHerdrBinary(invalidBinPath)(KindHerdr)
	if err == nil {
		t.Fatal("CheckHerdrBinary(invalid HERDR_BIN_PATH)(KindHerdr) = nil, want an error")
	}
	if !errors.Is(err, ErrOverridePrerequisiteUnavailable) {
		t.Fatalf("CheckHerdrBinary error = %v, want ErrOverridePrerequisiteUnavailable", err)
	}
}

func TestCheckHerdrBinarySucceedsWhenResolved(t *testing.T) {
	t.Parallel()
	binPath := filepath.Join(t.TempDir(), "herdr")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write fake herdr binary fixture: %v", err)
	}
	fakeEnv := func(key string) (string, bool) {
		if key == "HERDR_BIN_PATH" {
			return binPath, true
		}
		return "", false
	}
	if err := CheckHerdrBinary(fakeEnv)(KindHerdr); err != nil {
		t.Fatalf("CheckHerdrBinary(resolvable)(KindHerdr) = %v, want nil", err)
	}
}

func TestCheckHerdrBinaryDefaultsToOSLookupEnv(t *testing.T) {
	t.Parallel()
	// A nil lookup must not panic; it falls back to herdr.OSLookupEnv,
	// whose outcome depends on the host and is not asserted here.
	_ = CheckHerdrBinary(nil)(KindHerdr)
}

func TestComposeChecksStopsAtFirstFailure(t *testing.T) {
	t.Parallel()
	var secondCalled bool
	first := func(Kind) error { return errors.New("first check failed") }
	second := func(Kind) error { secondCalled = true; return nil }

	err := ComposeChecks(first, second)(KindHerdr)
	if err == nil || err.Error() != "first check failed" {
		t.Fatalf("ComposeChecks error = %v, want %q", err, "first check failed")
	}
	if secondCalled {
		t.Fatal("ComposeChecks called the second check after the first failed")
	}
}

func TestComposeChecksSkipsNilEntries(t *testing.T) {
	t.Parallel()
	err := ComposeChecks(nil, func(Kind) error { return nil }, nil)(KindNone)
	if err != nil {
		t.Fatalf("ComposeChecks with nil entries = %v, want nil", err)
	}
}

func TestComposeChecksAllPass(t *testing.T) {
	t.Parallel()
	calls := 0
	pass := func(Kind) error { calls++; return nil }
	if err := ComposeChecks(pass, pass)(KindTmux); err != nil {
		t.Fatalf("ComposeChecks(pass, pass) = %v, want nil", err)
	}
	if calls != 2 {
		t.Fatalf("ComposeChecks called its checks %d times, want 2", calls)
	}
}
