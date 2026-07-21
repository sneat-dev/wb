package migrate

import (
	"strings"
	"testing"
)

func TestAuditGoDependencyDecisionsExplainsPreservedVersion(t *testing.T) {
	goMod := []byte("module github.com/acme/consumer\n\ngo 1.24\n\nrequire github.com/acme/stable v1.2.3\n")
	decisions, err := auditGoDependencyDecisions(t.TempDir(), "local_verification", goMod, goMod, nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 1 {
		t.Fatalf("decisions = %+v", decisions)
	}
	decision := decisions[0]
	if decision.Path != "github.com/acme/stable" ||
		decision.VersionAtCheck != "v1.2.3" ||
		decision.VersionAfter != "v1.2.3" ||
		decision.VersionAction != "unchanged" ||
		!strings.Contains(decision.Reason, "no target version configured") {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestAuditGoDependencyDecisionsExplainsPublishedReplacement(t *testing.T) {
	before := []byte("module github.com/acme/consumer\n\ngo 1.24\n\nrequire github.com/acme/provider v0.4.0\n\nreplace github.com/acme/provider => ../provider\n")
	after := []byte("module github.com/acme/consumer\n\ngo 1.24\n\nrequire github.com/acme/provider v0.5.0\n")
	decisions, err := auditGoDependencyDecisions(
		t.TempDir(),
		"publishable",
		before,
		after,
		map[string]string{"github.com/acme/provider": "v0.5.0"},
		map[string]string{"github.com/acme/provider": "/tmp/provider"},
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 1 {
		t.Fatalf("decisions = %+v", decisions)
	}
	decision := decisions[0]
	if decision.VersionAtCheck != "v0.4.0" ||
		decision.TargetVersion != "v0.5.0" ||
		decision.VersionAfter != "v0.5.0" ||
		decision.VersionAction != "updated" ||
		decision.ReplacementAction != "removed" ||
		!strings.Contains(decision.Reason, "removed for publication") {
		t.Fatalf("decision = %+v", decision)
	}
}
