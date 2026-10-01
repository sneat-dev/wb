package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/runner"
)

// fakeProvider is a code-index provider the test controls. It counts what it
// is asked, by checkout, so a test can prove the snapshotter asked it once and
// no request ever did.
type fakeProvider struct {
	mu      sync.Mutex
	indexer string
	asks    map[string]int
	answers map[string]ProviderStatistics
	err     error
	panics  bool
	block   bool
}

func newFakeProvider(indexer string) *fakeProvider {
	return &fakeProvider{indexer: indexer, asks: map[string]int{}, answers: map[string]ProviderStatistics{}}
}

func (p *fakeProvider) Name() string { return "fake" }

func (p *fakeProvider) Indexer() string { return p.indexer }

func (p *fakeProvider) Statistics(ctx context.Context, checkout string) (ProviderStatistics, error) {
	p.mu.Lock()
	p.asks[checkout]++
	answer, err, panics, block := p.answers[checkout], p.err, p.panics, p.block
	p.mu.Unlock()
	if panics {
		panic("a provider panicked at " + checkout)
	}
	if block {
		<-ctx.Done()
		return ProviderStatistics{}, ctx.Err()
	}
	return answer, err
}

func (p *fakeProvider) set(edit func(*fakeProvider)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	edit(p)
}

func (p *fakeProvider) total() (total int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, asks := range p.asks {
		total += asks
	}
	return total
}

func (p *fakeProvider) asked(checkout string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.asks[checkout]
}

// receiptClock is the time of the indexer receipt the fake code-index source
// reports, which a test moves to write a new receipt.
type receiptClock struct {
	mu  sync.Mutex
	at  time.Time
	key string
}

func (r *receiptClock) rekey(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.key = key
}

func (r *receiptClock) current() (time.Time, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.at, r.key
}

func (r *receiptClock) write() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.at = r.at.Add(time.Minute)
}

// providerFleet is a snapshotter over one repository with two worktrees, a
// code-index source reporting one receipt per checkout, and a provider.
type providerFleet struct {
	snapshotter *Snapshotter
	sources     *fakeSources
	provider    *fakeProvider
	receipt     *receiptClock
	index       *fakeCodeIndex
	fingerprint *constFingerprint
	server      *cockpitServer
}

func newProviderFleet(t *testing.T, provider *fakeProvider, change ...func(*Options)) *providerFleet {
	t.Helper()
	sources := oneRepoSources(realTempDir(t))
	receipt := &receiptClock{at: time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)}
	codeIndex := &fakeCodeIndex{key: "k", states: func(_ string, checkouts []string) map[string][]CodeIndex {
		states := map[string][]CodeIndex{}
		for _, checkout := range checkouts {
			at, key := receipt.current()
			states[checkout] = []CodeIndex{{Indexer: "index", State: CodeIndexFresh, ReceiptAt: at, receiptKey: key}}
		}
		return states
	}}
	collectors := sources.collectors()
	collectors.CodeIndex = codeIndex
	if provider != nil {
		collectors.CodeIndexProvider = provider
	}
	fingerprint := newConstFingerprint("one")
	snapshotter, _ := newSnapshotter(collectors, func(options *Options) {
		options.Fingerprint = fingerprint.get
		for _, edit := range change {
			edit(options)
		}
	})
	return &providerFleet{snapshotter: snapshotter, sources: sources, provider: provider, receipt: receipt, index: codeIndex, fingerprint: fingerprint, server: newCockpitServer(t, snapshotter)}
}

func (f *providerFleet) statisticsOf(t *testing.T, path string) *CodeStatistics {
	t.Helper()
	document := f.server.fleet()
	if path == f.sources.repos[0].Path {
		for _, repository := range document.Repositories {
			if repository.Route == RouteLocal {
				return statisticsIn(t, repository.CodeIndex)
			}
		}
	}
	for _, worktree := range document.Worktrees {
		if worktree.Route == RouteLocal && worktree.Task == map[string]string{"/wt/task-a": "task-a", "/wt/task-b": "task-b"}[path] {
			return statisticsIn(t, worktree.CodeIndex)
		}
	}
	t.Fatalf("no entry for %s", path)
	return nil
}

func statisticsIn(t *testing.T, states []CodeIndex) *CodeStatistics {
	t.Helper()
	if len(states) != 1 {
		t.Fatalf("code index = %+v, want one indexer", states)
	}
	return states[0].Statistics
}

// TestCodeIndexStatisticsAreAskedOncePerReceiptByTheSnapshotterAndNeverByARequest
// is the read-model half of cockpit#ac:code-index-panel: a provider that
// reports 12 files, 40 symbols of two kinds and 90 edges for one checkout and
// no index for another. The document is read twice and the provider was asked
// once by the snapshotter, none of the reads asked it; after a new receipt it is
// asked once more and the new totals arrive.
func TestCodeIndexStatisticsAreAskedOncePerReceiptByTheSnapshotterAndNeverByARequest(t *testing.T) {
	t.Parallel()
	provider := newFakeProvider("index")
	fleet := newProviderFleet(t, provider)
	repoPath := fleet.sources.repos[0].Path
	provider.set(func(p *fakeProvider) {
		p.answers[repoPath] = ProviderStatistics{Indexed: true, Files: 12, Symbols: 40, Edges: 90, Kinds: map[string]int{"function": 30, "struct": 10}}
	})
	refreshAndSettle(t, fleet.snapshotter)
	asksAfterRefresh := provider.total()
	if asksAfterRefresh != 3 {
		t.Fatalf("the snapshotter asked %d times for three checkouts, want 3", asksAfterRefresh)
	}

	want := &CodeStatistics{Indexed: true, Files: 12, Symbols: 40, Edges: 90, Kinds: []KindCount{{Kind: "function", Count: 30}, {Kind: "struct", Count: 10}}}
	first, second := fleet.statisticsOf(t, repoPath), fleet.statisticsOf(t, repoPath)
	for _, got := range []*CodeStatistics{first, second} {
		if got == nil || jsonOf(t, got) != jsonOf(t, want) {
			t.Fatalf("statistics = %+v, want %+v", got, want)
		}
	}
	if provider.total() != asksAfterRefresh {
		t.Fatalf("a request asked the provider: %d asks after the refresh, %d after two reads", asksAfterRefresh, provider.total())
	}
	if other := fleet.statisticsOf(t, "/wt/task-a"); other == nil || other.Indexed || other.Error != "" || other.Files+other.Symbols+other.Edges != 0 || other.Kinds == nil || len(other.Kinds) != 0 {
		t.Fatalf("the checkout with no index reports %+v, want not indexed with an empty list", other)
	}
	if body, _ := fleet.snapshotter.Body(); !strings.Contains(string(body), `"kinds":[]`) || strings.Contains(string(body), `"kinds":null`) {
		t.Fatalf("a collection marshals as null: %s", body)
	}
	if document := fleet.server.fleet(); document.CodeIndexProvider != "fake" {
		t.Fatalf("the document names provider %q, want fake", document.CodeIndexProvider)
	}

	// Another pass with nothing moved asks nothing.
	refreshAndSettle(t, fleet.snapshotter)
	if provider.total() != asksAfterRefresh {
		t.Fatalf("a pass with the same receipt asked the provider again: %d", provider.total())
	}

	// A new receipt, with the provider now reporting 13 files, asks once more
	// per checkout and the page's numbers follow.
	provider.set(func(p *fakeProvider) {
		answer := p.answers[repoPath]
		answer.Files = 13
		p.answers[repoPath] = answer
	})
	fleet.receipt.write()
	fleet.index.setKey("k2")
	refreshAndSettle(t, fleet.snapshotter)
	if provider.asked(repoPath) != 2 {
		t.Fatalf("after a new receipt the repository was asked %d times, want 2", provider.asked(repoPath))
	}
	if got := fleet.statisticsOf(t, repoPath); got == nil || got.Files != 13 || got.Symbols != 40 {
		t.Fatalf("after the new receipt statistics = %+v, want 13 files", got)
	}
}

func jsonOf(t *testing.T, value any) string {
	t.Helper()
	text, err := jsonString(value)
	if err != nil {
		t.Fatal(err)
	}
	return text
}

// TestCodeIndexStatisticsSurviveGitStateMovingWhileTheReceiptDoesNot proves the
// answer is keyed on the receipt, not on time or on HEAD: Git state moving
// re-reads the repository's states but the provider is not asked again.
func TestCodeIndexStatisticsSurviveGitStateMovingWhileTheReceiptDoesNot(t *testing.T) {
	t.Parallel()
	provider := newFakeProvider("index")
	sources := oneRepoSources(realTempDir(t))
	receipt := &receiptClock{at: time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)}
	codeIndex := &fakeCodeIndex{key: "k", states: func(_ string, checkouts []string) map[string][]CodeIndex {
		states := map[string][]CodeIndex{}
		for _, checkout := range checkouts {
			states[checkout] = []CodeIndex{{Indexer: "index", State: CodeIndexStale, Behind: 1, ReceiptAt: receipt.at}}
		}
		return states
	}}
	collectors := sources.collectors()
	collectors.CodeIndex, collectors.CodeIndexProvider = codeIndex, provider
	fingerprint := newConstFingerprint("one")
	snapshotter, _ := newSnapshotter(collectors, func(options *Options) { options.Fingerprint = fingerprint.get })
	refreshAndSettle(t, snapshotter)
	asked := provider.total()
	fingerprint.value.Store("two")
	refreshAndSettle(t, snapshotter)
	if _, states := codeIndex.counts(); states < 2 {
		t.Fatalf("the states were not read again after Git state moved (%d reads)", states)
	}
	if provider.total() != asked {
		t.Fatalf("a moved fingerprint with the same receipt asked the provider again: %d, was %d", provider.total(), asked)
	}
}

// TestNoProviderConfiguredIsReportedAndStatisticsAreAbsent covers the second
// daemon of cockpit#ac:code-index-panel: nothing is asked and the document
// names no provider.
func TestNoProviderConfiguredIsReportedAndStatisticsAreAbsent(t *testing.T) {
	t.Parallel()
	fleet := newProviderFleet(t, nil)
	refreshAndSettle(t, fleet.snapshotter)
	document := fleet.server.fleet()
	if document.CodeIndexProvider != "" {
		t.Fatalf("a provider is named with none configured: %q", document.CodeIndexProvider)
	}
	if body, _ := fleet.snapshotter.Body(); strings.Contains(string(body), "code_index_provider") || strings.Contains(string(body), "statistics") {
		t.Fatalf("the document mentions a provider or statistics: %s", body)
	}
	if got := fleet.statisticsOf(t, fleet.sources.repos[0].Path); got != nil {
		t.Fatalf("statistics with no provider = %+v", got)
	}
}

// TestProviderIsAskedOnlyForTheIndexerItFollows covers a provider following an
// indexer no checkout has: nothing is asked and no statistics appear.
func TestProviderIsAskedOnlyForTheIndexerItFollows(t *testing.T) {
	t.Parallel()
	provider := newFakeProvider("another")
	fleet := newProviderFleet(t, provider)
	refreshAndSettle(t, fleet.snapshotter)
	if provider.total() != 0 {
		t.Fatalf("the provider was asked %d times for an indexer it does not follow", provider.total())
	}
	if got := fleet.statisticsOf(t, fleet.sources.repos[0].Path); got != nil {
		t.Fatalf("statistics for an unfollowed indexer = %+v", got)
	}
}

// TestProviderFailureIsACodeNeverTextOrAPath feeds a provider that fails with
// a message carrying a path and a sentinel, and one that panics and one that
// never answers: each reaches the document as a short code only.
func TestProviderFailureIsACodeNeverTextOrAPath(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		edit func(*fakeProvider)
		code string
	}{
		"an error":   {func(p *fakeProvider) { p.err = errors.New(sentinel + "stderr at /Users/someone/private") }, ErrorProviderFailed},
		"a panic":    {func(p *fakeProvider) { p.panics = true }, ErrorProviderFailed},
		"no command": {func(p *fakeProvider) { p.err = errProviderUnavailable }, ErrorProviderUnavailable},
		"bad output": {func(p *fakeProvider) { p.err = errProviderOutput }, ErrorProviderOutput},
	} {
		provider := newFakeProvider("index")
		tc.edit(provider)
		fleet := newProviderFleet(t, provider)
		refreshAndSettle(t, fleet.snapshotter)
		got := fleet.statisticsOf(t, fleet.sources.repos[0].Path)
		if got == nil || got.Error != tc.code || got.Indexed || got.Kinds == nil {
			t.Errorf("%s: statistics = %+v, want error %q", name, got, tc.code)
		}
		if body, _ := fleet.snapshotter.Body(); strings.Contains(string(body), sentinel) || strings.Contains(string(body), "/Users/") || strings.Contains(string(body), "panicked") {
			t.Errorf("%s: the document carries provider text: %s", name, body)
		}
	}
}

// TestAProviderThatNeverAnswersTimesOutWithACode bounds the ask: the ask's own
// deadline passes, with the parent live, and the answer is the timeout code.
func TestAProviderThatNeverAnswersTimesOutWithACode(t *testing.T) {
	t.Parallel()
	provider := newFakeProvider("index")
	provider.block = true
	got, answered := askProvider(t.Context(), provider, "/checkout", 5*time.Millisecond)
	if !answered || got.Error != ErrorProviderTimeout || got.Indexed || got.Kinds == nil {
		t.Fatalf("a provider that never answers = %+v (answered %v), want %q", got, answered, ErrorProviderTimeout)
	}
}

// TestAnAskCutShortByItsParentIsNotAnAnswer covers a pass that was cancelled
// or a budget that ran out during an ask: no timeout or failure is attributed
// to the provider, and nothing is recorded.
func TestAnAskCutShortByItsParentIsNotAnAnswer(t *testing.T) {
	t.Parallel()
	provider := newFakeProvider("index")
	provider.block = true
	parent, cancel := context.WithTimeout(t.Context(), 5*time.Millisecond)
	t.Cleanup(cancel)
	if got, answered := askProvider(parent, provider, "/checkout", time.Minute); answered {
		t.Fatalf("an ask cut short by its parent answered %+v", got)
	}
}

// TestInvalidProviderStatisticsAreRefused covers a provider that reports a
// negative total.
func TestInvalidProviderStatisticsAreRefused(t *testing.T) {
	t.Parallel()
	provider := newFakeProvider("index")
	provider.answers["/checkout"] = ProviderStatistics{Indexed: true, Files: -1}
	if got, answered := askProvider(t.Context(), provider, "/checkout", time.Minute); !answered || got.Error != ErrorProviderOutput || got.Files != 0 {
		t.Fatalf("a negative total = %+v, want %q", got, ErrorProviderOutput)
	}
}

// TestAFailedAnswerIsAskedAgainUpToTheCapThenStays covers the retry of a
// transient failure: it is not final, the provider is asked again on later
// passes, a success ends it, and a provider that keeps failing is asked
// maxProviderAttempts times per receipt and no more.
func TestAFailedAnswerIsAskedAgainUpToTheCapThenStays(t *testing.T) {
	t.Parallel()
	provider := newFakeProvider("index")
	provider.err = errBoom
	fleet := newProviderFleet(t, provider)
	repoPath := fleet.sources.repos[0].Path
	provider.answers[repoPath] = ProviderStatistics{Indexed: true, Files: 7}
	for pass := 1; pass <= 2; pass++ {
		refreshAndSettle(t, fleet.snapshotter)
		if got := fleet.statisticsOf(t, repoPath); got == nil || got.Error != ErrorProviderFailed {
			t.Fatalf("pass %d: %+v, want the failure code", pass, got)
		}
		if provider.asked(repoPath) != pass {
			t.Fatalf("pass %d asked the repository %d times", pass, provider.asked(repoPath))
		}
	}
	// With no budget left the earlier failed answer stays, and nothing is asked.
	fleet.snapshotter.providerBudget = time.Nanosecond
	refreshAndSettle(t, fleet.snapshotter)
	if got := fleet.statisticsOf(t, repoPath); got == nil || got.Error != ErrorProviderFailed || provider.asked(repoPath) != 2 {
		t.Fatalf("with no budget: %+v after %d asks", got, provider.asked(repoPath))
	}
	fleet.snapshotter.providerBudget = time.Minute
	provider.set(func(p *fakeProvider) { p.err = nil })
	refreshAndSettle(t, fleet.snapshotter)
	if got := fleet.statisticsOf(t, repoPath); got == nil || !got.Indexed || got.Files != 7 {
		t.Fatalf("after the provider recovered: %+v", got)
	}
	refreshAndSettle(t, fleet.snapshotter)
	if provider.asked(repoPath) != 3 {
		t.Fatalf("a final answer was asked again: %d asks", provider.asked(repoPath))
	}

	always := newFakeProvider("index")
	always.err = errBoom
	stuck := newProviderFleet(t, always)
	for range 6 {
		refreshAndSettle(t, stuck.snapshotter)
	}
	if always.asked(stuck.sources.repos[0].Path) != maxProviderAttempts {
		t.Fatalf("a failing provider was asked %d times, want %d", always.asked(stuck.sources.repos[0].Path), maxProviderAttempts)
	}
	if got := stuck.statisticsOf(t, stuck.sources.repos[0].Path); got == nil || got.Error != ErrorProviderFailed {
		t.Fatalf("the code does not stay: %+v", got)
	}
	// The next receipt asks afresh.
	stuck.receipt.write()
	stuck.index.setKey("k2")
	refreshAndSettle(t, stuck.snapshotter)
	if always.asked(stuck.sources.repos[0].Path) != maxProviderAttempts+1 {
		t.Fatalf("a new receipt did not ask again: %d", always.asked(stuck.sources.repos[0].Path))
	}
}

// TestAnExhaustedBudgetSkipsTheAsksAndRecordsNothing covers a repository whose
// provider budget is spent: the provider is not started, no code is cached, and
// a later pass with budget asks.
func TestAnExhaustedBudgetSkipsTheAsksAndRecordsNothing(t *testing.T) {
	t.Parallel()
	provider := newFakeProvider("index")
	fleet := newProviderFleet(t, provider, func(options *Options) { options.ProviderBudget = time.Nanosecond })
	refreshAndSettle(t, fleet.snapshotter)
	if provider.total() != 0 {
		t.Fatalf("the provider was asked %d times with no budget", provider.total())
	}
	if got := fleet.statisticsOf(t, fleet.sources.repos[0].Path); got != nil {
		t.Fatalf("statistics recorded with no budget: %+v", got)
	}
	fleet.snapshotter.providerBudget = time.Minute
	refreshAndSettle(t, fleet.snapshotter)
	if provider.total() != 3 {
		t.Fatalf("a later pass with budget asked %d times, want 3", provider.total())
	}
}

// TestASlowAskNeitherStarvesNorIsStarvedByTheGitBudget gives each ask its own
// deadline: with every ask blocking, each checkout gets its own timeout code
// rather than the later ones failing instantly.
func TestASlowAskNeitherStarvesNorIsStarvedByTheGitBudget(t *testing.T) {
	t.Parallel()
	provider := newFakeProvider("index")
	provider.block = true
	fleet := newProviderFleet(t, provider, func(options *Options) { options.ProviderTimeout = 20 * time.Millisecond })
	refreshAndSettle(t, fleet.snapshotter)
	for _, path := range []string{fleet.sources.repos[0].Path, "/wt/task-a", "/wt/task-b"} {
		if got := fleet.statisticsOf(t, path); got == nil || got.Error != ErrorProviderTimeout {
			t.Fatalf("%s: %+v, want its own timeout", path, got)
		}
		if provider.asked(path) != 1 {
			t.Fatalf("%s was asked %d times", path, provider.asked(path))
		}
	}
}

// TestACheckoutWithNoReceiptNeverStartsTheProvider covers the read-only rule:
// the command opens the index read-write, so a checkout the indexer has no
// receipt for is reported as not indexed and nothing runs for it.
func TestACheckoutWithNoReceiptNeverStartsTheProvider(t *testing.T) {
	t.Parallel()
	provider := newFakeProvider("index")
	sources := oneRepoSources(realTempDir(t))
	codeIndex := &fakeCodeIndex{key: "k", states: func(_ string, checkouts []string) map[string][]CodeIndex {
		states := map[string][]CodeIndex{}
		for _, checkout := range checkouts {
			states[checkout] = []CodeIndex{{Indexer: "index", State: CodeIndexNever}}
		}
		return states
	}}
	collectors := sources.collectors()
	collectors.CodeIndex, collectors.CodeIndexProvider = codeIndex, provider
	snapshotter, _ := newSnapshotter(collectors, nil)
	refreshAndSettle(t, snapshotter)
	refreshAndSettle(t, snapshotter)
	if provider.total() != 0 {
		t.Fatalf("the provider was asked %d times for checkouts with no receipt", provider.total())
	}
	for _, repository := range snapshotter.Document().Repositories {
		if repository.Route != RouteLocal {
			continue
		}
		if got := statisticsIn(t, repository.CodeIndex); got == nil || got.Indexed || got.Error != "" || got.Kinds == nil {
			t.Fatalf("a checkout with no receipt reports %+v, want not indexed", got)
		}
	}
}

// TestTheReceiptsCommitAndStatusKeyTheAnswerNotItsTimeAlone asks again when the
// receipt's commit or status changed under the same time.
func TestTheReceiptsCommitAndStatusKeyTheAnswerNotItsTimeAlone(t *testing.T) {
	t.Parallel()
	provider := newFakeProvider("index")
	fleet := newProviderFleet(t, provider)
	repoPath := fleet.sources.repos[0].Path
	fleet.receipt.rekey("sha-1\x00succeeded")
	refreshAndSettle(t, fleet.snapshotter)
	fleet.index.setKey("k2")
	refreshAndSettle(t, fleet.snapshotter)
	if provider.asked(repoPath) != 1 {
		t.Fatalf("the same receipt was asked %d times", provider.asked(repoPath))
	}
	fleet.receipt.rekey("sha-2\x00succeeded")
	fleet.index.setKey("k3")
	refreshAndSettle(t, fleet.snapshotter)
	if provider.asked(repoPath) != 2 {
		t.Fatalf("a new commit under the same time was asked %d times, want 2", provider.asked(repoPath))
	}
}

// TestStatisticsKeepOnlyTotalsAndSafeBoundedKinds feeds kind names that are
// paths, text, too long or hold uppercase, non-positive counts and more kinds
// than the bound: only well-formed kinds survive, the most numerous first.
func TestStatisticsKeepOnlyTotalsAndSafeBoundedKinds(t *testing.T) {
	t.Parallel()
	kinds := map[string]int{
		"function": 5, "/Users/someone/private": 9, "Function": 9, "a b": 9, sentinel + "kind": 9, "": 9,
		"method": 5, "zero": 0, "negative": -3, strings.Repeat("a", 33): 9, "ok_kind-2": 7,
	}
	got, ok := sanitizeStatistics(ProviderStatistics{Indexed: true, Files: 1, Symbols: 2, Edges: 3, Kinds: kinds})
	want := []KindCount{{Kind: "ok_kind-2", Count: 7}, {Kind: "function", Count: 5}, {Kind: "method", Count: 5}}
	if !ok || fmt.Sprint(got.Kinds) != fmt.Sprint(want) || got.Files != 1 || got.Symbols != 2 || got.Edges != 3 {
		t.Fatalf("sanitised = %+v, want kinds %v", got, want)
	}

	many := map[string]int{}
	for i := range maxKinds + 10 {
		many[fmt.Sprintf("kind%02d", i)] = i + 1
	}
	got, _ = sanitizeStatistics(ProviderStatistics{Indexed: true, Kinds: many})
	if len(got.Kinds) != maxKinds || got.Kinds[0].Count != maxKinds+10 {
		t.Fatalf("kinds = %d (first %+v), want %d, most numerous first", len(got.Kinds), got.Kinds[0], maxKinds)
	}

	// Not indexed reports no counts, whatever the provider put beside it.
	got, ok = sanitizeStatistics(ProviderStatistics{Files: 4, Kinds: map[string]int{"function": 1}})
	if !ok || got.Indexed || got.Files != 0 || got.Kinds == nil || len(got.Kinds) != 0 {
		t.Fatalf("not indexed = %+v", got)
	}
}

// TestStatisticsReachTheDocumentWithNothingBeyondTotalsAndKinds is the
// sentinel for the statistics: hostile kind names and a failure message never
// appear, while the allowed values do.
func TestStatisticsReachTheDocumentWithNothingBeyondTotalsAndKinds(t *testing.T) {
	t.Parallel()
	provider := newFakeProvider("index")
	fleet := newProviderFleet(t, provider)
	repoPath := fleet.sources.repos[0].Path
	provider.answers[repoPath] = ProviderStatistics{Indexed: true, Files: 3, Symbols: 4, Edges: 5, Kinds: map[string]int{"function": 4, sentinel + "path": 1, repoPath: 2}}
	refreshAndSettle(t, fleet.snapshotter)
	body, _ := fleet.snapshotter.Body()
	if strings.Contains(string(body), sentinel) || strings.Contains(string(body), repoPath) {
		t.Fatalf("the document carries provider text or a path: %s", body)
	}
	if !strings.Contains(string(body), `"kinds":[{"kind":"function","count":4}]`) {
		t.Fatalf("the allowed kind did not arrive: %s", body)
	}
}

// TestACheckoutLeavingTheRepositoryDropsItsAnswer covers a worktree that goes
// away: its cached answer is not kept, so a worktree that returns is asked.
func TestACheckoutLeavingTheRepositoryDropsItsAnswer(t *testing.T) {
	t.Parallel()
	provider := newFakeProvider("index")
	fleet := newProviderFleet(t, provider)
	refreshAndSettle(t, fleet.snapshotter)
	fleet.sources.change(func(f *fakeSources) { f.worktrees["acme/widgets"] = f.worktrees["acme/widgets"][1:] })
	fleet.index.setKey("k2")
	fleet.fingerprint.value.Store("two")
	refreshAndSettle(t, fleet.snapshotter)
	removed := provider.asked("/wt/task-a")
	fleet.sources.change(func(f *fakeSources) {
		f.worktrees["acme/widgets"] = []LinkedWorktree{{Path: "/wt/task-a", Branch: "feature/a"}, {Path: "/wt/task-b", Branch: "feature/b"}}
	})
	fleet.index.setKey("k3")
	fleet.fingerprint.value.Store("three")
	refreshAndSettle(t, fleet.snapshotter)
	if provider.asked("/wt/task-a") != removed+1 {
		t.Fatalf("a worktree that left and returned was asked %d times after %d", provider.asked("/wt/task-a"), removed)
	}
}

// commandCall is one command a fakeCommandRunner was asked to run.
type commandCall struct {
	Binary string
	Args   []string
	Opts   runner.RunOptions
}

// fakeCommandRunner answers any command with what reply returns for it.
type fakeCommandRunner struct {
	fakeGitRunner
	commands []commandCall
	reply    func(commandCall) gitReply
}

func newFakeCommand(t *testing.T, reply func(commandCall) gitReply) *fakeCommandRunner {
	t.Helper()
	return &fakeCommandRunner{fakeGitRunner: fakeGitRunner{t: t}, reply: reply}
}

func (f *fakeCommandRunner) RunOpts(_ context.Context, workdir string, opts runner.RunOptions, name string, args ...string) (runner.Result, error) {
	pinRunOptions(f.t, workdir, opts)
	call := commandCall{Binary: name, Args: args, Opts: opts}
	f.commands = append(f.commands, call)
	return resultOf(f.reply(call), opts)
}

// statusAnswer is `codegrapher status --json` printing statistics for path.
func statusAnswer(path string) gitReply {
	return gitReply{Out: fmt.Sprintf(`{"initialized":true,"projectPath":%q,"fileCount":12,"nodeCount":52,"edgeCount":90,"nodesByKind":{"file":12,"function":30,"struct":10}}`, path)}
}

// TestCodeGrapherProviderReadsItsStatusCommand checks what the provider asked
// the runner to run and what it took from the answer.
func TestCodeGrapherProviderReadsItsStatusCommand(t *testing.T) {
	t.Parallel()
	checkout := realTempDir(t)
	command := newFakeCommand(t, func(commandCall) gitReply { return statusAnswer(checkout) })
	provider := CodeGrapherProvider{Binary: "/opt/codegrapher", Runner: command}
	got, err := provider.Statistics(t.Context(), checkout)
	if err != nil {
		t.Fatal(err)
	}
	want := ProviderStatistics{Indexed: true, Files: 12, Symbols: 40, Edges: 90, Kinds: map[string]int{"function": 30, "struct": 10}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("statistics = %+v, want %+v", got, want)
	}
	call := command.commands[0]
	if call.Binary != "/opt/codegrapher" || strings.Join(call.Args, " ") != "status --json --path "+checkout {
		t.Fatalf("the command ran as %q %q", call.Binary, call.Args)
	}
	if !slices.Equal(call.Opts.Env, providerEnvironment(os.Environ())) || call.Opts.StdoutLimit != maxProviderOutput || !call.Opts.DiscardStderr {
		t.Errorf("options = %+v, want the sanitised environment, the output cap and no standard error", call.Opts)
	}
	if _, err := (CodeGrapherProvider{Runner: command}).Statistics(t.Context(), checkout); err != nil || command.commands[1].Binary != "codegrapher" {
		t.Errorf("the default command is codegrapher: %q, %v", command.commands[1].Binary, err)
	}
	if provider.Name() != "codegrapher" || provider.Indexer() != "codegrapher" || (CodeGrapherProvider{IndexerName: "x"}).Indexer() != "x" {
		t.Fatalf("name %q, indexer %q", provider.Name(), provider.Indexer())
	}
}

// TestCodeGrapherProviderSaysNotIndexedForAnIndexThatIsNotTheCheckouts covers
// a project that is not initialised and an answer about another directory (the
// nearest ancestor's index), which the checkout must not borrow.
func TestCodeGrapherProviderSaysNotIndexedForAnIndexThatIsNotTheCheckouts(t *testing.T) {
	t.Parallel()
	checkout := realTempDir(t)
	for name, out := range map[string]string{
		"not initialised": fmt.Sprintf(`{"initialized":false,"projectPath":%q}`, checkout),
		"another project": `{"initialized":true,"projectPath":"/somewhere/else","fileCount":9,"nodeCount":9,"edgeCount":9}`,
		"no project":      `{"initialized":true,"fileCount":9}`,
	} {
		command := newFakeCommand(t, func(commandCall) gitReply { return gitReply{Out: out} })
		got, err := CodeGrapherProvider{Runner: command}.Statistics(t.Context(), checkout)
		if err != nil || got.Indexed || got.Files != 0 {
			t.Errorf("%s: %+v, %v; want not indexed", name, got, err)
		}
	}
}

// TestCodeGrapherProviderFollowsASymbolicLinkedCheckout covers a checkout
// reached through a symbolic link: the command answers with the real path.
func TestCodeGrapherProviderFollowsASymbolicLinkedCheckout(t *testing.T) {
	t.Parallel()
	real := realTempDir(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	command := newFakeCommand(t, func(commandCall) gitReply { return statusAnswer(real) })
	if got, err := (CodeGrapherProvider{Runner: command}).Statistics(t.Context(), link); err != nil || !got.Indexed {
		t.Fatalf("through a link: %+v, %v", got, err)
	}
}

// TestCodeGrapherProviderFailsWithoutLeakingWhatTheCommandPrints covers a
// missing command, a failing one, output that is not JSON, and output past the
// cap. A failure carries no output: the runner is told to discard standard
// error and the error is a fixed one.
func TestCodeGrapherProviderFailsWithoutLeakingWhatTheCommandPrints(t *testing.T) {
	t.Parallel()
	checkout := realTempDir(t)
	ctx := t.Context()
	statistics := func(reply gitReply) error {
		_, err := CodeGrapherProvider{Runner: newFakeCommand(t, func(commandCall) gitReply { return reply })}.Statistics(ctx, checkout)
		return err
	}
	if err := statistics(gitReply{Err: &exec.Error{Name: "codegrapher", Err: exec.ErrNotFound}}); !errors.Is(err, errProviderUnavailable) {
		t.Errorf("a missing command: %v, want errProviderUnavailable", err)
	}
	if err := statistics(gitReply{Out: "SECRET-OUT /private/path", Exit: 3}); err == nil || strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "/private") {
		t.Errorf("a failing command: %v, want an error without its output", err)
	}
	if err := statistics(gitReply{Out: "not json"}); !errors.Is(err, errProviderOutput) {
		t.Errorf("output that is not JSON: %v, want errProviderOutput", err)
	}
	err := statistics(gitReply{Out: strings.Repeat("a", maxProviderOutput+4096)})
	if code := providerFailureCode(ctx, err); code != ErrorProviderOutput {
		t.Errorf("output past the cap: %v, code %q, want %q", err, code, ErrorProviderOutput)
	}
}

// TestProviderEnvironmentKeepsOnlyTheAllowList covers the filter directly.
func TestProviderEnvironmentKeepsOnlyTheAllowList(t *testing.T) {
	t.Parallel()
	got := providerEnvironment([]string{"PATH=/bin", "HOME=/h", "TMPDIR=/t", "LANG=C", "LC_ALL=C", "GIT_DIR=/x", "TOKEN=y", "PATHX=z"})
	if fmt.Sprint(got) != "[PATH=/bin HOME=/h TMPDIR=/t LANG=C LC_ALL=C]" {
		t.Fatalf("environment = %v", got)
	}
	if none := providerEnvironment([]string{"TOKEN=y"}); none == nil || len(none) != 0 {
		t.Fatalf("an environment with nothing allowed = %#v, want an empty slice that is not nil", none)
	}
}

// TestFailureCodesOfTheProviderAreFixed covers each mapping of an error to its
// code, and that a document's statistics type has exactly the closed fields.
func TestFailureCodesOfTheProviderAreFixed(t *testing.T) {
	t.Parallel()
	for err, code := range map[error]string{
		errProviderUnavailable: ErrorProviderUnavailable, errProviderOutput: ErrorProviderOutput,
		errGitOutputTooLarge: ErrorProviderOutput, errBoom: ErrorProviderFailed,
	} {
		if got := providerFailureCode(t.Context(), err); got != code {
			t.Errorf("%v is %q, want %q", err, got, code)
		}
	}
	data, err := json.Marshal(failedStatistics(ErrorProviderFailed))
	if err != nil || string(data) != `{"indexed":false,"files":0,"symbols":0,"edges":0,"kinds":[],"error":"provider_failed"}` {
		t.Fatalf("a failure marshals as %s, %v", data, err)
	}
}

// TestAnEmptyProviderEnvironmentReachesTheRunnerAsEmptyNotNil: a nil
// environment is read by the runner as "inherit the daemon's".
func TestAnEmptyProviderEnvironmentReachesTheRunnerAsEmptyNotNil(t *testing.T) {
	t.Parallel()
	command := newFakeCommand(t, func(commandCall) gitReply { return gitReply{Out: "{}"} })
	if _, err := runCapped(t.Context(), command, "codegrapher", providerEnvironment([]string{"TOKEN=y"}), 10, []string{"status"}); err != nil {
		t.Fatal(err)
	}
	if env := command.commands[0].Opts.Env; env == nil || len(env) != 0 {
		t.Fatalf("environment = %#v, want an empty slice that is not nil", env)
	}
}
