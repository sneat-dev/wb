package agentguard

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRewriteHandlesNullAndMalformedToolInput(t *testing.T) {
	t.Parallel()
	for _, original := range []string{"null", "[]", "42", "not JSON", `{"timeout":42,"custom":{"keep":true}}`} {
		t.Run(original, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			wrote, err := WriteDecision(&output, Decision{RewriteCommand: "wb run -- go test"}, json.RawMessage(original))
			if !wrote || err != nil {
				t.Fatalf("rewrite=%t,%v", wrote, err)
			}
			var response struct {
				Hook hookSpecificOutput `json:"hookSpecificOutput"`
			}
			if err := json.Unmarshal(output.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			var fields map[string]any
			if err := json.Unmarshal(response.Hook.UpdatedInput, &fields); err != nil {
				t.Fatal(err)
			}
			if fields["command"] != "wb run -- go test" || response.Hook.PermissionDecision != "" {
				t.Fatalf("response=%+v fields=%v", response, fields)
			}
			if strings.Contains(original, "timeout") && (fields["timeout"] != float64(42) || !reflect.DeepEqual(fields["custom"], map[string]any{"keep": true})) {
				t.Fatalf("unknown fields lost: %v", fields)
			}
		})
	}
}

func TestInspectionFailsOpenAfterUnexpectedParserPanic(t *testing.T) {
	t.Parallel()
	decision := inspectSafely(func() Decision { panic("unexpected parser failure") })
	if decision != (Decision{}) {
		t.Fatalf("panic yielded refusal or rewrite: %+v", decision)
	}
}

func TestBashRefusesNewDenyFindingBeforeRewrite(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	calls := 0
	decision := inspectBashCallWithInspector(ToolCall{CWD: root}, toolInput{Command: "go test"}, Options{ProjectsRoot: root}, func(command, cwd, projects string) *finding {
		calls++
		if command != "go test" || cwd != root || projects != root {
			t.Fatalf("inspection arguments=%q,%q,%q", command, cwd, projects)
		}
		return &finding{Message: "authority changed between inspections"}
	})
	if calls != 1 || !decision.Deny || decision.Reason != "authority changed between inspections" || decision.RewriteCommand != "" {
		t.Fatalf("second-phase deny=%+v calls=%d", decision, calls)
	}
}

func TestSimpleRewriteRejectsAssignmentOnlyCommand(t *testing.T) {
	t.Parallel()
	for _, command := range []string{"KEY=value", "cd /tmp && KEY=value", "cd /tmp &&"} {
		if rewritten, ok := simpleGovernedRewrite(command, ""); ok || rewritten != "" {
			t.Fatalf("rewrote command without a program: %q => %q", command, rewritten)
		}
	}
	if simpleWBInvocation("wb run -- go test > output.log") {
		t.Fatal("stamped a redirected WB invocation")
	}
}

func TestShellKeepsRedirectionTargetsSeparateFromWords(t *testing.T) {
	t.Parallel()
	got := splitSegments("echo first > '#target' second 2>> errors.log # comment\nwb run -- go test")
	want := []segment{{Words: []string{"echo", "first", "second"}, RedirectTargets: []string{"#target", "errors.log"}}, {Words: []string{"wb", "run", "--", "go", "test"}, Separator: "\n"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("segments=%+v, want %+v", got, want)
	}
}

func TestCanonicalClonePathPreservesHostAndTrailingPath(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	clone := filepath.Join(root, "github.com", "acme", "app")
	if err := os.MkdirAll(filepath.Join(clone, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := canonicalClonePath(root, "acme", "app", "config", "settings.json"); got != filepath.Join(clone, "config", "settings.json") {
		t.Fatalf("host-qualified path=%q", got)
	}
}
