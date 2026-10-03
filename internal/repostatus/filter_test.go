package repostatus

import "testing"

func TestHideCleanRepositoriesKeepsTheWorklist(t *testing.T) {
	t.Parallel()
	report := HideClean(Index{SchemaVersion: 1, Repositories: []Row{
		{Repository: "acme/clean", Status: "clean"},
		{Repository: "acme/dirty", Status: "attention", Summary: "1 modified file"},
		{Repository: "acme/broken", Status: "error", Error: "not a git repository"},
		{Repository: "acme/also-clean", Status: "clean"},
	}})
	if report.HiddenClean != 2 {
		t.Errorf("HiddenClean = %d, want 2", report.HiddenClean)
	}
	var kept []string
	for _, repository := range report.Repositories {
		kept = append(kept, repository.Repository)
	}
	if len(kept) != 2 || kept[0] != "acme/dirty" || kept[1] != "acme/broken" {
		t.Errorf("kept repositories = %v, want [acme/dirty acme/broken]", kept)
	}
	if !Failed(report) {
		t.Error("an error row was filtered away; the run would exit 0 despite an uninspectable repository")
	}
}

func TestStatusIndexHelpers(t *testing.T) {
	t.Parallel()
	report := Index{SchemaVersion: 1, Repositories: []Row{
		{Repository: "acme/clean", Status: "clean"},
		{Repository: "acme/dirty", Status: "attention", Summary: "1 modified file"},
		{Repository: "acme/broken", Status: "error", Error: "not a repository"},
	}}
	if !Failed(report) {
		t.Fatal("an error row must fail the report")
	}
	hidden := HideClean(report)
	if hidden.HiddenClean != 1 || len(hidden.Repositories) != 2 {
		t.Fatalf("hidden = %+v, want the clean row dropped and counted", hidden)
	}
	for _, repository := range hidden.Repositories {
		if repository.Repository == "acme/clean" {
			t.Fatal("the clean row survived filtering")
		}
	}
	if hidden.HiddenClean+len(hidden.Repositories) != len(report.Repositories) {
		t.Fatal("filtering lost rows")
	}
	if Failed(Index{Repositories: []Row{{Status: "clean"}, {Status: "attention"}}}) {
		t.Fatal("a report without error rows must not fail")
	}
}
