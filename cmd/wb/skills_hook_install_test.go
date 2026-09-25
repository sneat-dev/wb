package main

import (
	"bytes"
	"strings"
	"testing"
)

// AC: cov-rwi-03 unit03 seam list, cmd/wb/skills_hook_install.go RunE.
// With no --settings flag, the command must derive the path itself from the
// home directory rather than defaulting to the empty string -- exercised
// here with an isolated HOME so it never touches the real one, and with
// --dry-run so nothing is written to it either.
func TestSkillsHookInstallWithoutSettingsFlagDerivesPathFromHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	command := newSkillsHookInstallCmd()
	command.SetArgs([]string{"--dry-run"})
	var out bytes.Buffer
	command.SetOut(&out)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "SessionStart") {
		t.Fatalf("dry-run document = %q, want the merged SessionStart hook", out.String())
	}
}

// AC: cov-rwi-03 unit03 seam list, cmd/wb/skills_hook_install.go
// mergeSkillsHookSettings. A read failure that is not "file does not exist"
// -- a settings path that is itself a directory -- must be reported as a
// read error, not silently treated as an empty/absent settings file.
func TestMergeSkillsHookSettingsReportsANonNotExistReadError(t *testing.T) {
	t.Parallel()
	dirAsPath := t.TempDir()
	_, _, err := mergeSkillsHookSettings(dirAsPath, "wb skills hook run")
	if err == nil {
		t.Fatal("expected an error reading a directory as the settings file")
	}
	if !strings.Contains(err.Error(), "read "+dirAsPath) {
		t.Errorf("err = %v, want it to name the read failure on %s", err, dirAsPath)
	}
}
