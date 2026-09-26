package quality

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func deadcodeFailureOutput(identities ...string) string {
	var output strings.Builder
	fmt.Fprintf(&output, "New unreachable functions (%d):\n", len(identities))
	for index, identity := range identities {
		fmt.Fprintf(&output, "  internal/example/file.go:%d: %s\n", index+1, identity)
	}
	fmt.Fprintf(&output, "error: %d function(s) are unreachable from main and are not in %s; wire them up, delete them, or record them with --update-baseline\nexit status 1\n", len(identities), DefaultDeadcodeBaseline)
	return output.String()
}

func TestDeadcodeFailureEvidenceRetainsCompleteFindingsAcrossBoundedDetailAndJSON(t *testing.T) {
	var identities []string
	for index := 0; index < 100; index++ {
		identities = append(identities, fmt.Sprintf("example.test/pkg.Function%d", index))
	}
	output := deadcodeFailureOutput(identities...)
	entry := VerificationEntry{
		Language: "go", Check: CheckLint, Command: "go run ./cmd/wb deadcode", Status: StatusFailed,
		Detail:   commandError("go run ./cmd/wb deadcode", output, fmt.Errorf("exit status 1")),
		Deadcode: parseDeadcodeFailureEvidence(output),
	}
	if len(entry.Detail) > 1000 || !entry.Deadcode.Valid() || entry.Deadcode.Count != len(identities) {
		t.Fatalf("bounded detail / evidence = %d bytes / %+v", len(entry.Detail), entry.Deadcode)
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	var restored VerificationEntry
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if !restored.Deadcode.Valid() || restored.Deadcode.Identities[len(identities)-1] != identities[len(identities)-1] {
		t.Fatalf("receipt lost complete deadcode evidence: %+v", restored.Deadcode)
	}
}

func TestDeadcodeFailureEvidenceRejectsPartialAndNonFindingOutput(t *testing.T) {
	good := deadcodeFailureOutput("example.test/pkg.A", "example.test/pkg.B")
	for _, output := range []string{
		strings.Replace(good, "functions (2)", "functions (3)", 1),
		strings.Replace(good, "example.test/pkg.B", "example.test/pkg.A", 1),
		strings.Replace(good, "error: 2 function(s)", "error: 1 function(s)", 1),
		strings.Replace(good, "exit status 1\n", "", 1),
		truncateCommandDetailTo(good+strings.Repeat("more output\n", 100), 1000),
		"go: module download failed\nexit status 1\n",
	} {
		if evidence := parseDeadcodeFailureEvidence(output); evidence.Valid() {
			t.Fatalf("accepted incomplete or non-finding output: %q", output)
		}
	}
}

func TestDeadcodeFailureEvidenceAcceptsCompleteFixedSection(t *testing.T) {
	output := deadcodeFailureOutput("example.test/pkg.A")
	output = strings.Replace(output, "error: 1 function(s)", "\nBaseline entries now reachable or gone (1) — rerun with --update-baseline to drop them:\n  example.test/pkg.Gone\nerror: 1 function(s)", 1)
	if evidence := parseDeadcodeFailureEvidence(output); !evidence.Valid() || evidence.Identities[0] != "example.test/pkg.A" {
		t.Fatalf("complete report with fixed section rejected: %+v", evidence)
	}
}
