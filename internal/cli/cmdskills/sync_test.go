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
	t.Parallel()
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
	t.Parallel()
	var out bytes.Buffer
	err := writeSkillsSyncText(&out, skillscmd.TargetResult{
		Dir: "/tmp/codex/skills",
		Err: errors.New("invalid legacy wb_version"),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, unwanted := range []string{"nothing to do", "already up to date", "unchanged"} {
		if strings.Contains(out.String(), unwanted) {
			t.Errorf("failed target output = %q, must not claim it is current", out.String())
		}
	}
	for _, want := range []string{"wb skills sync failed", "invalid legacy wb_version"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("failed target output = %q, missing %q", out.String(), want)
		}
	}
}

func TestNewSkillsSyncCmdRejectsDirAndHarnessTogether(t *testing.T) {
	t.Parallel()
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

func quietSkillsTarget(harness, dir string, dryRun bool, names ...string) skillscmd.TargetResult {
	changes := make([]skillsync.Change, len(names))
	for i, name := range names {
		changes[i] = skillsync.Change{Name: name, Action: skillsync.Unchanged}
	}
	return skillscmd.TargetResult{
		Harness: harness,
		Dir:     dir,
		Report: skillsync.Report{
			Dir:     dir,
			DryRun:  dryRun,
			Changes: changes,
		},
	}
}

func TestWriteSkillsSyncReportsSummarizesUnchangedHarnesses(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	err := writeSkillsSyncReports(&out, []skillscmd.TargetResult{
		quietSkillsTarget("claude", "/home/user/.claude/skills", false, "move", "wb"),
		quietSkillsTarget("cursor", "/home/user/.cursor/skills", false, "move", "wb"),
		quietSkillsTarget("copilot", "/home/user/.copilot/skills", false, "move", "wb"),
	}, "text")
	if err != nil {
		t.Fatal(err)
	}
	want := "wb skills synced for 3 harnesses: claude, cursor, copilot\n  2 skills unchanged; already up to date\n"
	if out.String() != want {
		t.Fatalf("output = %q, want %q", out.String(), want)
	}
}

func TestWriteSkillsSyncReportsKeepsChangedTargetsAndSplitsUnequalQuietGroups(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	changed := skillscmd.TargetResult{
		Harness: "codex",
		Dir:     "/home/user/.codex/skills",
		Report: skillsync.Report{
			Dir: "/home/user/.codex/skills",
			Changes: []skillsync.Change{
				{Name: "wb", Action: skillsync.Updated},
				{Name: "move", Action: skillsync.Unchanged},
				{Name: "park", Action: skillsync.Unchanged},
			},
		},
	}
	err := writeSkillsSyncReports(&out, []skillscmd.TargetResult{
		quietSkillsTarget("claude", "/home/user/.claude/skills", false, "move", "wb"),
		changed,
		quietSkillsTarget("cursor", "/home/user/.cursor/skills", false, "move"),
		quietSkillsTarget("copilot", "/home/user/.copilot/skills", false, "park"),
	}, "text")
	if err != nil {
		t.Fatal(err)
	}
	// claude is alone in its count group, so it keeps the directory line.
	// cursor and copilot share a count and collapse after the changed target.
	want := strings.Join([]string{
		"wb skills synced: /home/user/.claude/skills",
		"  2 skills unchanged; already up to date",
		"wb skills synced: /home/user/.codex/skills",
		"  updated: wb",
		"  2 skills unchanged",
		"wb skills synced for 2 harnesses: cursor, copilot",
		"  1 skill unchanged; already up to date",
		"",
	}, "\n")
	if out.String() != want {
		t.Fatalf("output = %q, want %q", out.String(), want)
	}
	if strings.Contains(out.String(), "move") || strings.Contains(out.String(), "park") {
		t.Fatalf("output listed unchanged skill names: %q", out.String())
	}
}

func TestWriteSkillsSyncReportsDryRunUsesWouldSync(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	err := writeSkillsSyncReports(&out, []skillscmd.TargetResult{
		quietSkillsTarget("claude", "/home/user/.claude/skills", true, "wb"),
		quietSkillsTarget("cursor", "/home/user/.cursor/skills", true, "wb"),
	}, "text")
	if err != nil {
		t.Fatal(err)
	}
	want := "wb skills would sync for 2 harnesses: claude, cursor\n  1 skill unchanged; already up to date\n"
	if out.String() != want {
		t.Fatalf("output = %q, want %q", out.String(), want)
	}
}

func TestWriteSkillsSyncTextCountsUnchangedBesideNamedChanges(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	err := writeSkillsSyncText(&out, skillscmd.TargetResult{
		Dir: "/tmp/claude/skills",
		Report: skillsync.Report{
			Dir: "/tmp/claude/skills",
			Changes: []skillsync.Change{
				{Name: "wb-hooks", Action: skillsync.Conflict},
				{Name: "move", Action: skillsync.Unchanged},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "wb skills synced: /tmp/claude/skills\n  conflicts (left untouched): wb-hooks\n  1 skill unchanged\n"
	if out.String() != want {
		t.Fatalf("output = %q, want %q", out.String(), want)
	}
}

func TestWriteSkillsSyncTextOmitsTheUnchangedLineWhenEverySkillChanged(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	err := writeSkillsSyncText(&out, skillscmd.TargetResult{
		Dir: "/tmp/claude/skills",
		Report: skillsync.Report{
			Dir: "/tmp/claude/skills",
			Changes: []skillsync.Change{
				{Name: "wb", Action: skillsync.Added},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "wb skills synced: /tmp/claude/skills\n  added: wb\n"
	if out.String() != want {
		t.Fatalf("output = %q, want %q", out.String(), want)
	}
}

func TestWriteSkillsSyncTextKeepsTheEmptyCurrentMessage(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	err := writeSkillsSyncText(&out, skillscmd.TargetResult{
		Dir:    "/tmp/claude/skills",
		Report: skillsync.Report{Dir: "/tmp/claude/skills"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "wb skills synced: /tmp/claude/skills\n  nothing to do; skills already match this wb build\n"
	if out.String() != want {
		t.Fatalf("output = %q, want %q", out.String(), want)
	}
}

func TestSkillsSyncQuietGroupingRejectsFailuresAndMismatches(t *testing.T) {
	t.Parallel()
	if skillsSyncQuiet(skillscmd.TargetResult{Err: errors.New("legacy marker")}) {
		t.Fatal("a failed target is not a quiet unchanged sync")
	}
	conflict := skillscmd.TargetResult{Report: skillsync.Report{Changes: []skillsync.Change{
		{Name: "wb", Action: skillsync.Conflict},
	}}}
	if skillsSyncQuiet(conflict) {
		t.Fatal("a conflict is not a quiet unchanged sync")
	}
	group := []skillscmd.TargetResult{quietSkillsTarget("claude", "/claude", false, "a", "b")}
	if skillsSyncQuietCompatible(group, quietSkillsTarget("cursor", "/cursor", false, "a")) {
		t.Fatal("different unchanged counts must not share a summary")
	}
	if skillsSyncQuietCompatible(group, quietSkillsTarget("cursor", "/cursor", true, "a", "b")) {
		t.Fatal("a dry-run must not share a summary with an applied sync")
	}
	if !skillsSyncQuietCompatible(nil, quietSkillsTarget("cursor", "/cursor", false, "a")) {
		t.Fatal("an empty group accepts the first quiet target")
	}
	if !skillsSyncQuietCompatible(group, quietSkillsTarget("cursor", "/cursor", false, "a", "b")) {
		t.Fatal("matching dry-run and unchanged count share a summary")
	}
}

func TestSkillsSyncTargetLabelFallsBackToTheDirectory(t *testing.T) {
	t.Parallel()
	if got := skillsSyncTargetLabel(skillscmd.TargetResult{Dir: "/tmp/skills"}); got != "/tmp/skills" {
		t.Fatalf("label = %q, want the directory", got)
	}
	if got := skillsSyncTargetLabel(skillscmd.TargetResult{Harness: "claude", Dir: "/tmp/skills"}); got != "claude" {
		t.Fatalf("label = %q, want the harness id", got)
	}
}

func TestWriteSkillsSyncJSONKeepsUnchangedNames(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	err := writeSkillsSyncReports(&out, []skillscmd.TargetResult{
		quietSkillsTarget("claude", "/home/user/.claude/skills", false, "move", "wb"),
	}, "json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"unchanged": [`) || !strings.Contains(out.String(), `"move"`) || !strings.Contains(out.String(), `"wb"`) {
		t.Fatalf("json output dropped unchanged names: %s", out.String())
	}
}
