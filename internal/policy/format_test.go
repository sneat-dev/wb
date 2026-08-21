package policy

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func violatingResult(t *testing.T) Result {
	t.Helper()
	return checkFixture(t, map[string]string{
		"go.mod":               "module github.com/acme/cal/backend\n\ngo 1.26\n",
		"facade4cal/facade.go": "package facade4cal\n\nimport \"github.com/acme/other/backend/dbo4other\"\n",
		"dal4cal/repo.go":      "package dal4cal\n\nimport \"github.com/acme/cal/backend/api4cal\"\n",
	})
}

func render(t *testing.T, result Result, format Format) string {
	t.Helper()
	var buffer bytes.Buffer
	if err := WriteResult(&buffer, result, format); err != nil {
		t.Fatal(err)
	}
	return buffer.String()
}

func TestWriteTextNamesFileLineAndFix(t *testing.T) {
	output := render(t, violatingResult(t), FormatText)
	for _, want := range []string{
		"github.com/acme/cal/backend",
		"facade4cal/facade.go:3",
		"github.com/acme/other/backend/dbo4other",
		"group: extension-implementation",
		"dal -> api",
		"2 blocking",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("text output missing %q:\n%s", want, output)
		}
	}
}

func TestWriteTextOnACleanModule(t *testing.T) {
	result := checkFixture(t, map[string]string{
		"go.mod": "module github.com/acme/cal/backend\n\ngo 1.26\n",
	})
	if output := render(t, result, FormatText); !strings.Contains(output, "no violations") {
		t.Fatalf("expected a clean report, got:\n%s", output)
	}
}

func TestWriteJSONIsMachineReadable(t *testing.T) {
	var payload resultJSON
	if err := json.Unmarshal([]byte(render(t, violatingResult(t), FormatJSON)), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Module != "github.com/acme/cal/backend" {
		t.Fatalf("module = %q", payload.Module)
	}
	if payload.Blocking != 2 || len(payload.Findings) != 2 {
		t.Fatalf("blocking = %d, findings = %d", payload.Blocking, len(payload.Findings))
	}
	if payload.Findings[0].File == "" || payload.Findings[0].Message == "" {
		t.Fatalf("finding is missing fields: %+v", payload.Findings[0])
	}
}

func TestWriteGitHubEmitsAnnotations(t *testing.T) {
	output := render(t, violatingResult(t), FormatGitHub)
	if strings.Count(output, "::error file=") != 2 {
		t.Fatalf("expected two error annotations:\n%s", output)
	}
	if !strings.Contains(output, "line=3") {
		t.Fatalf("annotation should carry the line:\n%s", output)
	}
}

func TestWriteGitHubUsesNoticeForReportMode(t *testing.T) {
	result := violatingResult(t)
	for index := range result.Findings {
		result.Findings[index].Mode = ModeReport
	}
	output := render(t, result, FormatGitHub)
	if strings.Contains(output, "::error") {
		t.Fatalf("report-mode findings must not be errors:\n%s", output)
	}
	if strings.Count(output, "::notice file=") != 2 {
		t.Fatalf("expected two notices:\n%s", output)
	}
}

// A finding's text is partly attacker-influenced — it can quote an import path
// from the scanned repository — so it must not be able to forge or truncate a
// workflow command.
func TestWriteGitHubEscapesWorkflowDelimiters(t *testing.T) {
	result := violatingResult(t)
	result.Findings = result.Findings[:1]
	result.Findings[0].Message = "line one\nline two ::error file=evil::pwned"
	output := render(t, result, FormatGitHub)
	if strings.Count(output, "::error file=") != 1 {
		t.Fatalf("a forged annotation got through:\n%s", output)
	}
	if strings.Contains(output, "evil") && !strings.Contains(output, "%3A%3A") {
		t.Fatalf("delimiters were not escaped:\n%s", output)
	}
	if strings.Count(output, "\n") != 1 {
		t.Fatalf("newline was not escaped:\n%s", output)
	}
}

func TestParseFormat(t *testing.T) {
	for _, name := range []string{"text", "json", "github"} {
		if _, err := ParseFormat(name); err != nil {
			t.Fatalf("ParseFormat(%q): %v", name, err)
		}
	}
	if _, err := ParseFormat("xml"); err == nil {
		t.Fatal("ParseFormat should reject an unknown format")
	}
}

func TestApplyStrictPromotesReportFindings(t *testing.T) {
	result := violatingResult(t)
	for index := range result.Findings {
		result.Findings[index].Mode = ModeReport
	}
	if result.Blocking() != 0 {
		t.Fatal("precondition: nothing should block")
	}
	result.ApplyStrict()
	if result.Blocking() != len(result.Findings) {
		t.Fatalf("strict should promote every finding, blocking = %d", result.Blocking())
	}
}

func TestSummaryCountsByRule(t *testing.T) {
	if got := violatingResult(t).Summary(); got != "1 import, 1 layer" {
		t.Fatalf("summary = %q", got)
	}
}
