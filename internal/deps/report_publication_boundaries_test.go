package deps

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestReportYAMLHandlesConcreteScalarsAndExtremeDates(t *testing.T) {
	t.Parallel()
	for _, at := range []time.Time{time.Time{}, time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 1, 0, 0, 0, 0, time.FixedZone("beyond RFC3339", 24*60*60))} {
		for _, text := range []string{"\x00\xff<value>", "!!binary", "a: [\n"} {
			reports := []interface{ YAML() ([]byte, error) }{Report{Status: text}, BumpReport{Status: text, SeedEvents: []ReleaseEvent{{CheckedAt: at}}}, DriftReport{Mode: text, ObservedAt: at}, Graph{BaseRef: text}}
			for _, report := range reports {
				raw, err := report.YAML()
				if err != nil || len(raw) == 0 {
					t.Fatalf("%T YAML=%q err=%v", report, raw, err)
				}
			}
		}
	}
}

func TestDriftPublicationRefusesUnencodableTimestamp(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	report := DriftReport{ObservedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.FixedZone("invalid JSON offset", 24*60*60))}
	err := WriteDriftReports(root, report)
	if err == nil || !strings.Contains(err.Error(), "timezone hour outside of range") {
		t.Fatalf("unencodable timestamp: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "deps-drift.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("JSON published after encoding refusal: %v", err)
	}
	for _, name := range []string{"deps-drift.md", "deps-drift.yaml"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("prior %s missing: %v", name, err)
		}
	}
}

func TestGraphReportScalarEncodingAndViewValidation(t *testing.T) {
	t.Parallel()
	graph := Graph{BaseRef: "\x00\xff<main>", Repositories: []GraphRepository{{Slug: "acme/app", Modules: []string{"example.com/app"}}}}
	encoded, err := graph.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var parsed Graph
	if err := json.Unmarshal(encoded, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.BaseRef != "\x00�<main>" || !strings.Contains(string(encoded), "\\u003cmain\\u003e") {
		t.Fatalf("scalar escaping differs: %s", encoded)
	}
	for _, view := range []GraphView{GraphViewRepositories, GraphViewDependencies, GraphViewSelections} {
		root := t.TempDir()
		paths, err := WriteGraphReports(root, graph, view)
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{paths.Markdown, paths.YAML, paths.JSON, paths.SVG, paths.HTML} {
			if _, err := os.Stat(path); err != nil {
				t.Fatal(err)
			}
		}
	}
	_, err = WriteGraphReports(t.TempDir(), graph, "unknown")
	if err == nil || !strings.Contains(err.Error(), "unknown dependency graph view") {
		t.Fatalf("invalid view accepted: %v", err)
	}
}

func TestReleaseObservationsMergeRequirementsWithoutAliasing(t *testing.T) {
	t.Parallel()
	later := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	incoming := map[string]string{"example.com/sdk": "v1.2.3"}
	got := mergeReleaseObservations([]ReleaseObservation{{Module: "example.com/app"}}, []ReleaseObservation{{Module: "example.com/app", ExpectedRequirements: incoming, CheckedAt: later}})
	if len(got) != 1 || !reflect.DeepEqual(got[0].ExpectedRequirements, incoming) || !got[0].CheckedAt.Equal(later) {
		t.Fatalf("merged=%+v", got)
	}
	got[0].ExpectedRequirements["example.com/sdk"] = "v9.0.0"
	if incoming["example.com/sdk"] != "v1.2.3" {
		t.Fatal("merge aliased incoming requirements")
	}
}
