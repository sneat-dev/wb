package cmddeps

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/deps"
	"github.com/sneat-dev/wb/internal/depsrun"
	"github.com/sneat-dev/wb/internal/npmrelease"
)

func TestNpmPublishOutputOmitsTokenLookingValueUnderSafeInputKey(t *testing.T) {
	t.Parallel()
	const value = "token-looking-value-must-not-be-printed"
	release := npmrelease.Release{
		Repository: "sneat-co/eventius", Workflow: "release-frontend.yml",
		Package: "@sneat/extension-eventius", Version: "0.0.1", Ref: "main",
		Inputs: map[string]string{"package": value},
	}
	releases, err := npmrelease.Normalize([]npmrelease.Release{release}, "main")
	if err != nil {
		t.Fatalf("safe workflow-input key was heuristically rejected: %v", err)
	}
	publication, err := npmrelease.Run(t.Context(), releases, npmrelease.Options{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"json", "yaml", "markdown"} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			command := cwDepsNewOutCommand(&output)
			command.SetOut(&output)
			if err := writeNpmPublishOutput(command.OutOrStdout(), depsrun.PublicationOutput{Publication: publication}, format); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(output.String(), value) {
				t.Fatalf("%s stdout leaked a workflow input value: %s", format, output.String())
			}
		})
	}
}
func TestCwDepsWriteNpmPublishOutputFormats(t *testing.T) {
	t.Parallel()
	output := depsrun.PublicationOutput{Publication: npmrelease.Report{
		SchemaVersion: npmrelease.SchemaVersion, Operation: "deps-npm-publish-cwfixture", Status: npmrelease.StatusPlanned, Ref: "main",
		Releases: []npmrelease.Receipt{{
			Release: cwDepsReleaseFixture(),
			Status:  npmrelease.StatusPlanned,
			Reason:  "plan|only",
			RunID:   "42",
			RunURL:  "https://example.test/run/42",
			HeadSHA: "abcdef",
		}},
	}}
	for _, format := range []string{"markdown", "yaml", "json"} {
		var out bytes.Buffer
		if err := writeNpmPublishOutput(&out, output, format); err != nil {
			t.Fatalf("format %s: %v", format, err)
		}
		if strings.TrimSpace(out.String()) == "" {
			t.Errorf("format %s wrote nothing", format)
		}
		if format == "json" && !json.Valid(out.Bytes()) {
			t.Errorf("json output is not JSON: %s", out.String())
		}
	}
	var markdown bytes.Buffer
	if err := writeNpmPublishMarkdown(&markdown, output); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# WB npm publication and dependency waves", "@acme/lib", "plan\\|only", "[42](https://example.test/run/42)"} {
		if !strings.Contains(markdown.String(), want) {
			t.Errorf("markdown missing %q:\n%s", want, markdown.String())
		}
	}
	// A propagation section is appended when a bump report is attached.
	output.Propagation = &deps.BumpReport{SchemaVersion: 1, Operation: "deps-bump-cwfixture", Status: "completed", Parallel: 1}
	markdown.Reset()
	if err := writeNpmPublishMarkdown(&markdown, output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(markdown.String(), "deps-bump-cwfixture") {
		t.Errorf("propagation report missing from markdown:\n%s", markdown.String())
	}
	if err := writeNpmPublishOutput(&bytes.Buffer{}, output, "toml"); err == nil ||
		!strings.Contains(err.Error(), `unknown --format "toml"`) {
		t.Fatalf("unknown format = %v", err)
	}
}
func TestCwDepsPreflightNpmPublishCommandWiring(t *testing.T) {
	t.Parallel()
	var captured depsrun.PublicationRequest
	family := NewPublish(testRuntime(), PublicationDependencies{Run: func(_ context.Context, r depsrun.PublicationRequest, _ depsrun.PublicationCallbacks) error {
		captured = r
		return nil
	}})
	family.SetArgs([]string{"npm", "--repo", "acme/app", "--workflow", "release.yml", "--package", "@acme/lib", "--version", "1.2.3", "--fleet", "--workflow-input", "0:package=runtime", "--parallel", "3"})
	if err := family.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(captured.Repositories) != 1 || captured.Workflows[0] != "release.yml" || !captured.Selection.Fleet || captured.WorkflowInputs[0] != "0:package=runtime" || captured.Lifecycle.Parallel != 3 || !captured.Lifecycle.ParallelExplicit {
		t.Fatalf("captured=%+v", captured)
	}
}
func TestNpmPublicationFormatContract(t *testing.T) {
	t.Parallel()
	for _, format := range []string{"markdown", "yaml", "json"} {
		if err := validateNpmPublishFormat(format); err != nil {
			t.Errorf("format %q: %v", format, err)
		}
	}
	if err := validateNpmPublishFormat("toml"); err == nil || !strings.Contains(err.Error(), `unknown --format "toml"`) {
		t.Fatalf("unknown format = %v", err)
	}
}

func cwDepsReleaseFixture() npmrelease.Release {
	return npmrelease.Release{Repository: "acme/app", Workflow: "release.yml", Package: "@acme/lib", Version: "1.2.3", Ref: "main"}
}
