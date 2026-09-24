package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/fleetsync"
	"github.com/sneat-dev/wb/internal/gitops"
)

// tailCovSizedModel builds a results model and drives it through the window
// size it will be rendered at.
func tailCovSizedModel(t *testing.T, results []fleetsync.Result, width, height int) ResultsModel {
	t.Helper()
	m := NewResultsModel(results)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return updated.(ResultsModel)
}

// TestTailCovProgressModelInitAndUnhandledMessage pins that the model starts no
// command of its own (the sync drives every message) and passes a message it
// does not own through without mutating state.
func TestTailCovProgressModelInitAndUnhandledMessage(t *testing.T) {
	t.Parallel()
	m := NewProgressModel(map[string]int{"acme": 1}, 1)
	if cmd := m.Init(); cmd != nil {
		t.Fatalf("Init() = %v, want nil while the sync drives its own messages", cmd)
	}

	updated, cmd := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if cmd != nil {
		t.Fatalf("unhandled message produced command %v, want nil", cmd)
	}
	got := updated.(ProgressModel)
	if got.done != 0 || len(got.inFlight) != 0 || len(got.Results) != 0 || got.quitting {
		t.Fatalf("unhandled message mutated progress state: %#v", got)
	}
}

// TestTailCovProgressModelQuittingRendersEmptyView asserts a quit renders
// nothing at all, so the alternate-screen restore is not fighting stale rows.
func TestTailCovProgressModelQuittingRendersEmptyView(t *testing.T) {
	t.Parallel()
	m := NewProgressModel(map[string]int{"acme": 2}, 2)
	if m.View().Content == "" {
		t.Fatal("running progress view is empty, so the quit assertion proves nothing")
	}
	updated, _ := m.Update(SyncDone{})
	m = updated.(ProgressModel)
	if !m.quitting {
		t.Fatal("SyncDone did not mark the model quitting")
	}
	if got := m.View().Content; got != "" {
		t.Fatalf("quitting view = %q, want empty", got)
	}
}

// TestTailCovSummaryItemAccessors pins the row text the summary list renders
// and the filter text the fuzzy matcher searches.
func TestTailCovSummaryItemAccessors(t *testing.T) {
	t.Parallel()
	sections := map[fleetsync.SummarySection]string{
		fleetsync.SummaryFinalOutcomes:       "Outcome",
		fleetsync.SummaryPullActions:         "Pull",
		fleetsync.SummaryAttention:           "Attention",
		fleetsync.SummaryErrors:              "Error",
		fleetsync.SummarySection("Unmapped"): "",
	}
	for section, want := range sections {
		if got := summarySectionLabel(section); got != want {
			t.Errorf("summarySectionLabel(%q) = %q, want %q", section, got, want)
		}
	}

	item := summaryItem{fleetsync.SummaryGroup{
		Label:   "Pulled",
		Section: fleetsync.SummaryFinalOutcomes,
		Results: []fleetsync.Result{{Repo: discover.Repo{Org: "a", Name: "one"}}},
	}}
	if got := item.FilterValue(); got != "Pulled Final outcomes" {
		t.Errorf("FilterValue() = %q, want %q", got, "Pulled Final outcomes")
	}
	if got := item.Title(); !strings.HasPrefix(got, "Outcome") || !strings.Contains(got, "Pulled") || !strings.HasSuffix(got, "1") {
		t.Errorf("Title() = %q, want section, label and count", got)
	}
	if got := item.Description(); got != "1 repository" {
		t.Errorf("Description() = %q, want %q", got, "1 repository")
	}
}

// TestTailCovRepositoryItemAccessors asserts the row title carries the pull
// action, and the filter value carries every searchable field including the
// error text.
func TestTailCovRepositoryItemAccessors(t *testing.T) {
	t.Parallel()
	item := repositoryItem{fleetsync.Result{
		Repo:        discover.Repo{Org: "acme", Name: "widgets"},
		Status:      fleetsync.Pulled,
		PullPlanned: true,
		Detail:      gitops.RepoStatus{Modified: []string{"a.go"}},
		Err:         fmt.Errorf("network unavailable"),
	}}
	title := item.Title()
	if !strings.HasPrefix(title, "acme/widgets — pulled") || !strings.Contains(title, ", pull planned (dry-run)") {
		t.Fatalf("Title() = %q, want the pull action appended to the status", title)
	}
	filter := item.FilterValue()
	for _, want := range []string{"acme/widgets", "pulled", "planned (dry-run)", "1 modified file", "network unavailable"} {
		if !strings.Contains(filter, want) {
			t.Errorf("FilterValue() = %q, want it to contain %q", filter, want)
		}
	}
	if got := item.Description(); got != "1 modified file" {
		t.Errorf("Description() = %q, want %q", got, "1 modified file")
	}

	quiet := repositoryItem{fleetsync.Result{Repo: discover.Repo{Org: "acme", Name: "quiet"}, Status: fleetsync.NoOp}}
	if title := quiet.Title(); strings.Contains(title, "pull") {
		t.Errorf("Title() = %q, want no pull suffix without a pull action", title)
	}
	if filter := quiet.FilterValue(); !strings.HasPrefix(filter, "acme/quiet noop") || strings.Contains(filter, "network unavailable") {
		t.Errorf("FilterValue() = %q, want the plain status with no error text", filter)
	}
}

// TestTailCovResultsModelInitAndQuitKeys drives the two unconditional quit
// paths directly.
func TestTailCovResultsModelInitAndQuitKeys(t *testing.T) {
	t.Parallel()
	base := tailCovSizedModel(t, []fleetsync.Result{{Repo: discover.Repo{Org: "a", Name: "one"}, Status: fleetsync.Failed}}, 100, 24)
	if cmd := base.Init(); cmd != nil {
		t.Fatalf("Init() = %v, want nil", cmd)
	}

	updated, cmd := base.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("ctrl+c produced no tea.Quit command")
	}
	if msg := cmd(); msg != (tea.QuitMsg{}) {
		t.Fatalf("ctrl+c command produced %#v, want tea.QuitMsg", msg)
	}
	if got := updated.(ResultsModel); got.focus != base.focus {
		t.Fatalf("ctrl+c changed focus to %v, want %v", got.focus, base.focus)
	}

	updated, cmd = base.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if cmd == nil {
		t.Fatal("q produced no tea.Quit command")
	}
	if msg := cmd(); msg != (tea.QuitMsg{}) {
		t.Fatalf("q command produced %#v, want tea.QuitMsg", msg)
	}
	if got := updated.(ResultsModel); got.focus != base.focus {
		t.Fatalf("q changed focus to %v, want %v", got.focus, base.focus)
	}
}

// TestTailCovResultsModelFocusKeys pins enter/right to entering the repository
// pane and esc/left to leaving it.
func TestTailCovResultsModelFocusKeys(t *testing.T) {
	t.Parallel()
	results := []fleetsync.Result{{Repo: discover.Repo{Org: "a", Name: "broken"}, Status: fleetsync.Failed}}
	m := tailCovSizedModel(t, results, 100, 24)
	selectResultGroup(t, &m, "Errors")

	// Nothing to focus while the selected category is empty.
	empty := tailCovSizedModel(t, results, 100, 24)
	updated, _ := empty.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := updated.(ResultsModel); got.focus != focusSummary {
		t.Fatalf("enter with an empty repository pane changed focus to %v", got.focus)
	}

	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(ResultsModel)
	if m.focus != focusRepositories {
		t.Fatalf("enter did not focus repositories: %v", m.focus)
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = updated.(ResultsModel)
	if m.focus != focusSummary {
		t.Fatalf("esc did not return focus to the summary: %v", m.focus)
	}

	updated, _ = m.Update(tea.KeyPressMsg{Code: 'l', Text: "l"})
	m = updated.(ResultsModel)
	if m.focus != focusRepositories {
		t.Fatalf("l did not focus repositories: %v", m.focus)
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: 'h', Text: "h"})
	m = updated.(ResultsModel)
	if m.focus != focusSummary {
		t.Fatalf("h did not return focus to the summary: %v", m.focus)
	}
}

// TestTailCovResultsModelHalfPageKeysScrollDetails proves both spellings of
// half-page-up move the detail viewport back toward the top.
func TestTailCovResultsModelHalfPageKeysScrollDetails(t *testing.T) {
	t.Parallel()
	commits := make([]string, 60)
	for i := range commits {
		commits[i] = fmt.Sprintf("%07x commit %d with a subject long enough to wrap", i, i)
	}
	m := tailCovSizedModel(t, []fleetsync.Result{{
		Repo: discover.Repo{Org: "a", Name: "many"}, Status: fleetsync.Unpushed,
		Detail: gitops.RepoStatus{Unpushed: commits},
	}}, 70, 20)
	selectResultGroup(t, &m, "Needs attention")
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	m = updated.(ResultsModel)
	deep := m.detail.YOffset()
	if deep == 0 {
		t.Fatal("pgdown did not scroll the detail viewport")
	}

	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	m = updated.(ResultsModel)
	if after := m.detail.YOffset(); after >= deep {
		t.Fatalf("pgup offset = %d, want < %d", after, deep)
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	m = updated.(ResultsModel)
	deep = m.detail.YOffset()
	updated, _ = m.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	m = updated.(ResultsModel)
	if after := m.detail.YOffset(); after >= deep {
		t.Fatalf("ctrl+u offset = %d, want < %d", after, deep)
	}
}

// TestTailCovResultsModelRendersSummaryBeforeFirstWindowSize covers the
// pre-size guard: with no terminal geometry the model renders the summary list
// alone rather than dividing by a zero-size layout.
func TestTailCovResultsModelRendersSummaryBeforeFirstWindowSize(t *testing.T) {
	t.Parallel()
	m := NewResultsModel([]fleetsync.Result{{Repo: discover.Repo{Org: "a", Name: "one"}, Status: fleetsync.Failed}})
	unsized := m.summary.View()
	if unsized == "" {
		t.Fatal("summary list rendered nothing, so the guard assertion proves nothing")
	}
	if got := m.View().Content; got != unsized {
		t.Fatalf("unsized view = %q, want the summary list %q", got, unsized)
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: -1, Height: -1})
	m = updated.(ResultsModel)
	if got := m.View().Content; got != m.summary.View() {
		t.Fatalf("negative-size view = %q, want the summary list %q", got, m.summary.View())
	}
}

// TestTailCovResultsRightHeightsDegeneratePanes pins the arithmetic that keeps
// the repository pane and detail pane non-negative on tiny terminals.
func TestTailCovResultsRightHeightsDegeneratePanes(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		height, repositories, detail int
	}{
		{height: 2, repositories: 1, detail: 1},
		{height: 3, repositories: 1, detail: 1},
		{height: 4, repositories: 2, detail: 1},
		{height: 5, repositories: 3, detail: 1},
	} {
		repositories, detail := resultsRightHeights(test.height)
		if repositories != test.repositories || detail != test.detail {
			t.Errorf("resultsRightHeights(%d) = (%d, %d), want (%d, %d)", test.height, repositories, detail, test.repositories, test.detail)
		}
	}
}

// TestTailCovResultsModelToggleFocusLeavesEmptyRepositoryPane pins both
// branches of focus toggling, including a focused-but-empty repository pane.
func TestTailCovResultsModelToggleFocusLeavesEmptyRepositoryPane(t *testing.T) {
	t.Parallel()
	m := NewResultsModel([]fleetsync.Result{{Repo: discover.Repo{Org: "a", Name: "broken"}, Status: fleetsync.Failed}})
	if len(m.repositories.Items()) != 0 {
		t.Fatalf("default category should be empty, got %d repositories", len(m.repositories.Items()))
	}
	m.toggleFocus()
	if m.focus != focusSummary {
		t.Fatalf("toggleFocus from an empty repository pane changed focus to %v", m.focus)
	}

	selectResultGroup(t, &m, "Errors")
	m.toggleFocus()
	if m.focus != focusRepositories {
		t.Fatalf("toggleFocus with a populated repository pane left focus at %v", m.focus)
	}
	m.toggleFocus()
	if m.focus != focusSummary {
		t.Fatalf("second toggleFocus left focus at %v", m.focus)
	}
}

// TestTailCovResultsModelRendersPullAndTrackingDetail asserts the detail panel
// exposes the pull action and, for diverged repositories, the tracking line
// that makes the report actionable.
func TestTailCovResultsModelRendersPullAndTrackingDetail(t *testing.T) {
	t.Parallel()
	results := []fleetsync.Result{
		{Repo: discover.Repo{Org: "a", Name: "current"}, Status: fleetsync.Pulled, PullSucceeded: true},
		{
			Repo: discover.Repo{Org: "a", Name: "diverged"}, Status: fleetsync.Diverged,
			Tracking: gitops.TrackingState{Branch: "main", Upstream: "origin/main", Ahead: 1, Behind: 2, Configured: true},
		},
	}
	m := tailCovSizedModel(t, results, 120, 30)

	selectResultGroup(t, &m, "Pulled")
	if view := m.View().Content; !strings.Contains(view, "Pull: already current") {
		t.Fatalf("detail panel missing the pull action: %q", view)
	}

	selectResultGroup(t, &m, "Needs attention")
	if view := m.View().Content; !strings.Contains(view, "Tracking: main is 1 ahead, 2 behind origin/main") {
		t.Fatalf("detail panel missing the tracking line: %q", view)
	}
}

// TestTailCovResultsModelDetailForRepositoryWithNoIdentity pins the guard that
// renders a placeholder instead of an empty " — " heading when the summary
// counts a result whose repository was never resolved.
func TestTailCovResultsModelDetailForRepositoryWithNoIdentity(t *testing.T) {
	t.Parallel()
	m := tailCovSizedModel(t, []fleetsync.Result{{Status: fleetsync.Failed}}, 120, 30)
	selectResultGroup(t, &m, "Errors")
	if view := m.View().Content; !strings.Contains(view, "No repositories need review.") {
		t.Fatalf("detail panel = %q, want the no-repository placeholder", view)
	}
}

// TestTailCovResultsModelDetailWhenSummaryHasNoCategory pins the fallback used
// when the summary offers no selectable category: the detail pane must say so
// rather than keep showing the previously selected repository.
func TestTailCovResultsModelDetailWhenSummaryHasNoCategory(t *testing.T) {
	t.Parallel()
	m := tailCovSizedModel(t, []fleetsync.Result{{Repo: discover.Repo{Org: "a", Name: "one"}, Status: fleetsync.Pulled}}, 120, 30)
	selectResultGroup(t, &m, "Pulled")
	if got := m.selectedSummaryGroup().Label; got != "Pulled" {
		t.Fatalf("selected summary group = %q, want Pulled", got)
	}

	m.summary.SetItems(nil)
	m.syncGroup(true)

	if got := m.selectedSummaryGroup().Label; got != "" {
		t.Fatalf("categoryless summary still selected %q", got)
	}
	if got := len(m.repositories.Items()); got != 0 {
		t.Fatalf("repositories after losing every category = %d, want 0", got)
	}
	if detail := m.detail.View(); !strings.Contains(detail, "No summary category matches the filter.") {
		t.Fatalf("detail pane = %q, want the no-category message", detail)
	}
}
