package fleetsync

import (
	"reflect"
	"testing"

	"github.com/sneat-dev/wb/internal/discover"
)

func TestSummaryIsOrderedSharedAccountingModel(t *testing.T) {
	results := []Result{
		{Repo: discover.Repo{Org: "z", Name: "current"}, Status: Pulled, PullAttempted: true, PullSucceeded: true},
		{Repo: discover.Repo{Org: "a", Name: "updated"}, Status: Pulled, PullAttempted: true, PullSucceeded: true, Updated: true},
		{Repo: discover.Repo{Org: "b", Name: "unpublished"}, Status: Unpushed, PullAttempted: true, PullSucceeded: true},
		{Repo: discover.Repo{Org: "c", Name: "broken"}, Status: Failed, PullAttempted: true},
	}
	groups := Summary(results)
	if len(groups) != 16 {
		t.Fatalf("groups = %d, want 16", len(groups))
	}
	wantLabels := []string{
		"Not owned/fork", "Cloned", "Pulled", "Skipped (dirty)", "Skipped (ignored)",
		"Empty remote", "Archived removed", "Archived kept", "Archived absent",
		"Pull planned", "Pull attempted", "Pull succeeded", "Updated from remote", "Already current",
		"Needs attention", "Errors",
	}
	gotLabels := make([]string, len(groups))
	for index, group := range groups {
		gotLabels[index] = group.Label
	}
	if !reflect.DeepEqual(gotLabels, wantLabels) {
		t.Fatalf("labels = %v, want %v", gotLabels, wantLabels)
	}
	for label, want := range map[string]int{
		"Pulled": 2, "Pull attempted": 4, "Pull succeeded": 3,
		"Updated from remote": 1, "Already current": 2, "Needs attention": 1, "Errors": 1,
	} {
		group, ok := SummaryGroupByLabel(groups, label)
		if !ok || len(group.Results) != want {
			t.Errorf("%s = %d (found=%t), want %d", label, len(group.Results), ok, want)
		}
	}
	pulled, _ := SummaryGroupByLabel(groups, "Pulled")
	if got := []string{pulled.Results[0].Repo.Slug(), pulled.Results[1].Repo.Slug()}; !reflect.DeepEqual(got, []string{"a/updated", "z/current"}) {
		t.Fatalf("Pulled repository order = %v", got)
	}
}
