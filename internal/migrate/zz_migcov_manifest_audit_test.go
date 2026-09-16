package migrate

import (
	"strings"
	"testing"
)

func TestMigCovGoManifestDependenciesRejectsBrokenManifest(t *testing.T) {
	if _, err := goManifestDependencies("go.mod", []byte("this is not a go.mod\n")); err == nil {
		t.Fatal("goManifestDependencies() accepted an unparseable manifest")
	}
}

func TestMigCovGoManifestDependenciesRecordsVersionsAndReplacements(t *testing.T) {
	contents := []byte(`module example.com/app

go 1.24

require (
	example.com/direct v1.2.3
	example.com/versioned v1.0.0
)

replace example.com/direct => ../direct

replace example.com/versioned => example.com/versioned v1.5.0

replace example.com/unrequired => ../unrequired
`)
	dependencies, err := goManifestDependencies("go.mod", contents)
	if err != nil {
		t.Fatalf("goManifestDependencies() error = %v", err)
	}
	if got := dependencies["example.com/direct"]; got.version != "v1.2.3" || got.replacement != "../direct" {
		t.Errorf("direct dependency = %+v", got)
	}
	if got := dependencies["example.com/versioned"]; got.replacement != "example.com/versioned@v1.5.0" {
		t.Errorf("versioned replacement = %+v", got)
	}
	if _, ok := dependencies["example.com/unrequired"]; ok {
		t.Errorf("replacement without a requirement was recorded: %+v", dependencies)
	}
}

func TestMigCovDependencyVersionActionCoversEveryTransition(t *testing.T) {
	tests := []struct {
		name                        string
		before, after               bool
		versionBefore, versionAfter string
		want                        string
	}{
		{name: "neither", want: "not_required"},
		{name: "added", after: true, want: "added"},
		{name: "removed", before: true, want: "removed"},
		{name: "updated", before: true, after: true, versionBefore: "v1.0.0", versionAfter: "v1.1.0", want: "updated"},
		{name: "unchanged", before: true, after: true, versionBefore: "v1.0.0", versionAfter: "v1.0.0", want: "unchanged"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := dependencyVersionAction(test.before, test.after, test.versionBefore, test.versionAfter); got != test.want {
				t.Fatalf("dependencyVersionAction() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestMigCovDependencyDecisionReasonExplainsEveryOutcome(t *testing.T) {
	tests := []struct {
		name       string
		decision   GoDependencyDecision
		campaign   bool
		wantReason string
		wantAbsent string
	}{
		{
			name:       "not required anywhere",
			decision:   GoDependencyDecision{VersionAction: "not_required"},
			wantReason: "dependency was not required before or after normalization",
		},
		{
			name:       "configured requirement unused",
			decision:   GoDependencyDecision{VersionAction: "not_required", TargetVersion: "v1.0.0"},
			wantReason: "go mod tidy found no source use for the configured migration requirement",
		},
		{
			name:       "configured dependency added",
			decision:   GoDependencyDecision{VersionAction: "added", TargetVersion: "v1.0.0"},
			wantReason: "migration configured this dependency",
		},
		{
			name:       "source dependency added",
			decision:   GoDependencyDecision{VersionAction: "added"},
			wantReason: "go mod tidy added the dependency required by source code",
		},
		{
			name:       "dependency removed",
			decision:   GoDependencyDecision{VersionAction: "removed"},
			wantReason: "go mod tidy removed the unused dependency",
		},
		{
			name:       "selection changed without a target",
			decision:   GoDependencyDecision{VersionAction: "updated"},
			wantReason: "Go module selection changed the dependency during normalization",
		},
		{
			name:       "target applied",
			decision:   GoDependencyDecision{VersionAction: "updated", TargetVersion: "v2.0.0", VersionAfter: "v2.0.0"},
			wantReason: "configured target version applied",
		},
		{
			name:       "target resolved elsewhere",
			decision:   GoDependencyDecision{VersionAction: "updated", TargetVersion: "v2.0.0", VersionAfter: "v2.1.0"},
			wantReason: "Go module selection resolved target v2.0.0 to v2.1.0",
		},
		{
			name:       "unchanged without a target",
			decision:   GoDependencyDecision{VersionAction: "unchanged"},
			wantReason: "no target version configured; WB preserved the selected version",
		},
		{
			name:       "already at target",
			decision:   GoDependencyDecision{VersionAction: "unchanged", TargetVersion: "v3.0.0", VersionAfter: "v3.0.0"},
			wantReason: "already at the configured target version",
		},
		{
			name:       "selection kept another version",
			decision:   GoDependencyDecision{VersionAction: "unchanged", TargetVersion: "v3.0.0", VersionAfter: "v2.9.0"},
			wantReason: "Go module selection kept v2.9.0 instead of configured target v3.0.0",
		},
		{
			name:       "campaign replacement added",
			decision:   GoDependencyDecision{VersionAction: "unchanged", ReplacementAction: "added"},
			campaign:   true,
			wantReason: "campaign worktree selected for local verification",
		},
		{
			name:       "tooling replacement added",
			decision:   GoDependencyDecision{VersionAction: "unchanged", ReplacementAction: "added"},
			wantReason: "replacement added by Go tooling",
		},
		{
			name:       "campaign replacement removed for publication",
			decision:   GoDependencyDecision{VersionAction: "unchanged", ReplacementAction: "removed", Phase: "publishable"},
			campaign:   true,
			wantReason: "temporary campaign worktree replacement removed for publication",
		},
		{
			name:       "unused campaign replacement removed",
			decision:   GoDependencyDecision{VersionAction: "unchanged", ReplacementAction: "removed", Phase: "local_verification"},
			campaign:   true,
			wantReason: "unused campaign worktree replacement removed",
		},
		{
			name:       "foreign replacement removed",
			decision:   GoDependencyDecision{VersionAction: "unchanged", ReplacementAction: "removed"},
			wantReason: "replacement removed during normalization",
		},
		{
			name:       "replacement retargeted",
			decision:   GoDependencyDecision{VersionAction: "unchanged", ReplacementAction: "updated"},
			wantReason: "replacement target changed during normalization",
		},
		{
			name:       "campaign replacement preserved",
			decision:   GoDependencyDecision{VersionAction: "unchanged", ReplacementAction: "unchanged", ReplacementAfter: "../campaign"},
			campaign:   true,
			wantReason: "campaign worktree replacement preserved",
		},
		{
			name:       "existing replacement preserved",
			decision:   GoDependencyDecision{VersionAction: "unchanged", ReplacementAction: "unchanged", ReplacementAfter: "../local"},
			wantReason: "existing replacement preserved",
		},
		{
			name:       "no replacement reason when none applies",
			decision:   GoDependencyDecision{VersionAction: "removed", ReplacementAction: "unchanged"},
			wantReason: "go mod tidy removed the unused dependency",
			wantAbsent: "replacement",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := dependencyDecisionReason(test.decision, test.campaign)
			if test.wantReason == "" {
				if got != "" {
					t.Fatalf("dependencyDecisionReason() = %q, want empty", got)
				}
				return
			}
			if !strings.Contains(got, test.wantReason) {
				t.Fatalf("dependencyDecisionReason() = %q, want it to contain %q", got, test.wantReason)
			}
			if test.wantAbsent != "" && strings.Contains(got, test.wantAbsent) {
				t.Fatalf("dependencyDecisionReason() = %q, want no %q", got, test.wantAbsent)
			}
		})
	}
}

func TestMigCovAuditGoDependencyDecisionsRejectsBrokenBeforeAndAfter(t *testing.T) {
	valid := []byte("module example.com/app\n\ngo 1.24\n\nrequire example.com/dep v1.0.0\n")
	if _, err := auditGoDependencyDecisions(t.TempDir(), "local_verification", []byte("nonsense\n"), valid, nil, nil, true); err == nil {
		t.Fatal("auditGoDependencyDecisions() accepted an unparseable before manifest")
	}
	if _, err := auditGoDependencyDecisions(t.TempDir(), "local_verification", valid, []byte("nonsense\n"), nil, nil, true); err == nil {
		t.Fatal("auditGoDependencyDecisions() accepted an unparseable after manifest")
	}
}

func TestMigCovAuditGoDependencyDecisionsCoversEveryCandidateSource(t *testing.T) {
	before := []byte("module example.com/app\n\ngo 1.24\n\nrequire example.com/kept v1.0.0\n")
	after := []byte("module example.com/app\n\ngo 1.24\n\nrequire example.com/kept v1.0.0\n\nrequire example.com/new v0.1.0\n\nreplace example.com/kept => ../kept\n")
	decisions, err := auditGoDependencyDecisions(
		t.TempDir(), "local_verification", before, after,
		map[string]string{"example.com/configured": "v9.9.9"},
		map[string]string{"example.com/kept": "/tmp/kept"},
		true,
	)
	if err != nil {
		t.Fatalf("auditGoDependencyDecisions() error = %v", err)
	}
	byPath := map[string]GoDependencyDecision{}
	for _, decision := range decisions {
		byPath[decision.Path] = decision
	}
	for _, path := range []string{"example.com/kept", "example.com/new", "example.com/configured"} {
		if _, ok := byPath[path]; !ok {
			t.Errorf("missing decision for %s: %+v", path, decisions)
		}
	}
	if decision := byPath["example.com/new"]; decision.VersionAction != "added" {
		t.Errorf("added dependency decision = %+v", decision)
	}
	if decision := byPath["example.com/kept"]; decision.ReplacementAction != "added" || decision.VersionAction != "unchanged" {
		t.Errorf("kept dependency decision = %+v", decision)
	}
	if decision := byPath["example.com/configured"]; decision.VersionAction != "not_required" || decision.TargetVersion != "v9.9.9" {
		t.Errorf("configured dependency decision = %+v", decision)
	}
}
