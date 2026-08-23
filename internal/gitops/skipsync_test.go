package gitops

import (
	"path/filepath"
	"testing"
)

func TestSkipSyncReadsLocalMarker(t *testing.T) {
	cases := []struct {
		name  string
		value string // "" leaves the key unset
		want  bool
	}{
		{name: "absent", value: "", want: false},
		{name: "true", value: "true", want: true},
		{name: "false", value: "false", want: false},
		{name: "yes is a git bool", value: "yes", want: true},
		{name: "1 is a git bool", value: "1", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			git(t, dir, "init", "-q", "-b", "main")
			if tc.value != "" {
				git(t, dir, "config", "--local", SkipSyncKey, tc.value)
			}
			got, err := SkipSync(dir)
			if err != nil {
				t.Fatalf("SkipSync: unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("SkipSync = %v, want %v", got, tc.want)
			}
		})
	}
}

// A malformed value must surface as an error. Reporting it as "not marked"
// would resume pulling a repo the user asked wb to leave alone.
func TestSkipSyncMalformedValueErrors(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "config", "--local", SkipSyncKey, "garbage")

	got, err := SkipSync(dir)
	if err == nil {
		t.Fatalf("SkipSync = %v, nil; want an error for a non-boolean value", got)
	}
	if got {
		t.Fatalf("SkipSync = true on error, want false")
	}
}

func TestSkipSyncNonRepoErrors(t *testing.T) {
	got, err := SkipSync(t.TempDir())
	if err == nil {
		t.Fatalf("SkipSync = %v, nil; want an error outside a git repository", got)
	}
}

// The marker is per-repo. Reading without --local would fall back to the
// user's ~/.gitconfig, where one stray key would disable sync fleet-wide.
func TestSkipSyncIgnoresGlobalConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "config", "--global", SkipSyncKey, "true")

	got, err := SkipSync(dir)
	if err != nil {
		t.Fatalf("SkipSync: unexpected error: %v", err)
	}
	if got {
		t.Fatalf("SkipSync = true; a global %s must not mark an unmarked repo", SkipSyncKey)
	}
}

func TestSetAndUnsetSkipSync(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")

	if err := SetSkipSync(dir); err != nil {
		t.Fatalf("SetSkipSync: %v", err)
	}
	if got, err := SkipSync(dir); err != nil || !got {
		t.Fatalf("after SetSkipSync: SkipSync = %v, %v; want true, nil", got, err)
	}

	if err := UnsetSkipSync(dir); err != nil {
		t.Fatalf("UnsetSkipSync: %v", err)
	}
	if got, err := SkipSync(dir); err != nil || got {
		t.Fatalf("after UnsetSkipSync: SkipSync = %v, %v; want false, nil", got, err)
	}
}

// Unsetting an unmarked repo is a no-op, not a failure.
func TestUnsetSkipSyncIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")

	if err := UnsetSkipSync(dir); err != nil {
		t.Fatalf("UnsetSkipSync on an unmarked repo: %v", err)
	}
	if err := UnsetSkipSync(dir); err != nil {
		t.Fatalf("second UnsetSkipSync: %v", err)
	}
}

// git config --unset exits 5 on a multi-valued key and leaves every value in
// place. Treating that 5 as success would report a repo unmarked while it is
// still marked, so UnsetSkipSync must clear all values.
func TestUnsetSkipSyncClearsDuplicateValues(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "config", "--local", "--add", SkipSyncKey, "true")
	git(t, dir, "config", "--local", "--add", SkipSyncKey, "true")

	if err := UnsetSkipSync(dir); err != nil {
		t.Fatalf("UnsetSkipSync: %v", err)
	}
	got, err := SkipSync(dir)
	if err != nil {
		t.Fatalf("SkipSync after unset: %v", err)
	}
	if got {
		t.Fatalf("SkipSync = true; duplicate values were left behind")
	}
}
