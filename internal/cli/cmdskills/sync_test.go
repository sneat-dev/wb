package cmdskills

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/strongo/cli-helpers/skillsync"
	skillscmd "github.com/strongo/cli-helpers/skillsync/cobracmd"
)

func TestWriteSkillsSyncJSONIncludesTargetError(t *testing.T) {
	var out bytes.Buffer
	err := writeSkillsSyncJSON(&out, []skillscmd.TargetResult{{
		Harness: "codex",
		Dir:     "/tmp/codex/skills",
		Report: skillsync.Report{
			Dir:        "/tmp/codex/skills",
			CLIVersion: "0.96.6",
		},
		Err: errors.New("legacy marker content differs"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	var payload skillsSyncJSON
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out.String())
	}
	if payload.Status != "failed" {
		t.Errorf("status = %q, want failed", payload.Status)
	}
	if payload.Error != "legacy marker content differs" {
		t.Errorf("error = %q", payload.Error)
	}
	if payload.Harness != "codex" || payload.Dir != "/tmp/codex/skills" {
		t.Errorf("target = %+v", payload)
	}
}

func TestWriteSkillsSyncTextDoesNotDescribeAFailedTargetAsCurrent(t *testing.T) {
	var out bytes.Buffer
	err := writeSkillsSyncText(&out, skillscmd.TargetResult{
		Dir: "/tmp/codex/skills",
		Err: errors.New("invalid legacy wb_version"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "nothing to do") {
		t.Errorf("failed target output = %q, must not claim it is current", out.String())
	}
	for _, want := range []string{"wb skills sync failed", "invalid legacy wb_version"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("failed target output = %q, missing %q", out.String(), want)
		}
	}
}

func TestNewSkillsSyncCmdRejectsDirAndHarnessTogether(t *testing.T) {
	command := newSkillsSyncCmd(testRuntime(), testSyncDependencies())
	command.SetArgs([]string{"--dir", "/explicit/skills", "--harness", "cursor"})
	err := command.Execute()
	if err == nil {
		t.Fatal("expected --dir and --harness together to fail")
	}
	var coded *exitError
	if !errors.As(err, &coded) || coded.code != exitUsage {
		t.Fatalf("err = %v, want exitUsage", err)
	}
}
