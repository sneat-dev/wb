package deps

import (
	"strings"
	"testing"
)

func TestParseTargetRequiresQualifiedGitHubIdentity(t *testing.T) {
	t.Parallel()
	if _, err := ParseTarget("github-actions", "cicd@v1.2.3"); err == nil {
		t.Fatal("ParseTarget accepted an ownerless GitHub Actions dependency")
	}
	target, err := ParseTarget("github-actions", "strongo/cicd@v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if target.Dependency != "strongo/cicd" || target.Version != "v1.2.3" {
		t.Fatalf("target = %+v", target)
	}
}

func TestRewriteGitHubActionsPinsResolvedCommitAndPreservesSubpath(t *testing.T) {
	t.Parallel()
	oldSHA := strings.Repeat("1", 40)
	newSHA := strings.Repeat("2", 40)
	contents := []byte("jobs:\n  ci:\n    uses: strongo/cicd/.github/workflows/go.yml@" + oldSHA + " # v1.9.0\n")
	target := Target{Ecosystem: EcosystemGitHubActions, Dependency: "strongo/cicd", Version: "v1.10.5", Resolved: newSHA}
	updated, decisions, err := rewriteGitHubActions(contents, ".github/workflows/ci.yml", target, true, false)
	if err != nil {
		t.Fatal(err)
	}
	want := "uses: strongo/cicd/.github/workflows/go.yml@" + newSHA + " # v1.10.5"
	if !strings.Contains(string(updated), want) {
		t.Fatalf("updated workflow does not contain %q:\n%s", want, updated)
	}
	if len(decisions) != 1 || decisions[0].BeforeVersion != "v1.9.0" || decisions[0].Action != "updated" {
		t.Fatalf("decisions = %+v", decisions)
	}
}

func TestRewriteGitHubActionsPreservesNonVersionComment(t *testing.T) {
	t.Parallel()
	target := Target{Ecosystem: EcosystemGitHubActions, Dependency: "acme/action", Version: "v2.0.0", Resolved: strings.Repeat("a", 40)}
	contents := []byte("    uses: 'acme/action/run@main' # required for release\r\n")
	updated, decisions, err := rewriteGitHubActions(contents, ".github/workflows/release.yaml", target, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updated), "uses: 'acme/action/run@"+target.Resolved+"' # v2.0.0; required for release\r\n") {
		t.Fatalf("comment or CRLF not preserved:\n%q", updated)
	}
	if len(decisions) != 1 || decisions[0].BeforeVersion != "" {
		t.Fatalf("decisions = %+v", decisions)
	}
}

func TestRewriteGitHubActionsBlocksDowngradeBeforeWriting(t *testing.T) {
	t.Parallel()
	target := Target{Ecosystem: EcosystemGitHubActions, Dependency: "acme/cicd", Version: "v1.8.0", Resolved: strings.Repeat("b", 40)}
	contents := []byte("uses: acme/cicd/action@" + strings.Repeat("c", 40) + " # v1.9.2\n")
	updated, decisions, err := rewriteGitHubActions(contents, ".github/workflows/ci.yml", target, true, false)
	if err == nil {
		t.Fatal("downgrade unexpectedly succeeded")
	}
	if string(updated) != string(contents) {
		t.Fatalf("blocked downgrade changed contents:\n%s", updated)
	}
	if len(decisions) != 1 || decisions[0].Action != "blocked_downgrade" {
		t.Fatalf("decisions = %+v", decisions)
	}
}

func TestRewriteGitHubActionsRecognizesExactPin(t *testing.T) {
	t.Parallel()
	sha := strings.Repeat("d", 40)
	target := Target{Ecosystem: EcosystemGitHubActions, Dependency: "acme/cicd", Version: "v1.9.2", Resolved: sha}
	contents := []byte("uses: acme/cicd@" + sha + " # v1.9.2\n")
	updated, decisions, err := rewriteGitHubActions(contents, ".github/workflows/ci.yml", target, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if string(updated) != string(contents) || len(decisions) != 1 || decisions[0].Action != "unchanged" {
		t.Fatalf("updated=%q decisions=%+v", updated, decisions)
	}
}
