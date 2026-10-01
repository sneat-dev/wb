package fleet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/runner"
)

// A code-index provider reports the statistics of one checkout's index
// (cockpit#req:code-index-summary). Only the snapshotter asks, once per
// checkout per indexer receipt, inside the fingerprint-gated read; no request
// ever reaches a provider. What a provider returns is untrusted: it crosses
// into the document only through sanitizeStatistics, which keeps totals and
// per-kind counts and nothing else.

// CodeIndexProvider reports code-index statistics for a checkout. Name is the
// provider's name, which the document carries; Indexer is the configured name
// of the indexer whose receipts the provider follows, so its statistics are
// attached to that indexer's code-index entry; Statistics reads one checkout.
// A provider that runs a process must bound it (the snapshotter hands it a
// context with a timeout) and cap its output.
type CodeIndexProvider interface {
	Name() string
	Indexer() string
	Statistics(ctx context.Context, checkout string) (ProviderStatistics, error)
}

// ProviderStatistics is what a provider reports for one checkout: whether an
// index exists, its totals, and the symbols by kind.
type ProviderStatistics struct {
	Indexed bool
	Files   int
	Symbols int
	Edges   int
	Kinds   map[string]int
}

// The short codes a statistics Error can hold. They name the kind of failure
// and never carry a path or the text a command printed.
const (
	ErrorProviderUnavailable = "provider_unavailable"
	ErrorProviderTimeout     = "provider_timeout"
	ErrorProviderFailed      = "provider_failed"
	ErrorProviderOutput      = "provider_output_invalid"
)

// maxKinds bounds the number of symbol kinds one checkout's statistics carry,
// and kindPattern what a kind name may be: a short lower-case word. Anything
// else is dropped, so a path or free text cannot ride in a kind name.
const maxKinds = 32

var kindPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// The budgets of provider asks: one ask, and all the asks of one repository's
// read. They come from the pass's context, not the repository's Git budget, so
// a slow ask neither starves nor is starved by the Git reads.
const (
	defaultProviderTimeout = 20 * time.Second
	defaultProviderBudget  = 2 * time.Minute
	// maxProviderAttempts caps how often a failed answer is asked again for one
	// receipt; after it the failure code stays until the next receipt.
	maxProviderAttempts = 3
)

// errProviderUnavailable says the provider's command is not installed, and
// errProviderOutput that what it printed is not statistics.
var (
	errProviderUnavailable = errors.New("provider command is not installed")
	errProviderOutput      = errors.New("provider output is not statistics")
)

// providerFailureCode is the short code for a failed ask: timeout when the
// ask's own deadline passed, else by the error's kind.
func providerFailureCode(ctx context.Context, err error) string {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return ErrorProviderTimeout
	case errors.Is(err, errProviderUnavailable):
		return ErrorProviderUnavailable
	case errors.Is(err, errProviderOutput), errors.Is(err, errGitOutputTooLarge):
		return ErrorProviderOutput
	}
	return ErrorProviderFailed
}

// sanitizeStatistics is what the document takes of what a provider reported: a
// negative total makes the whole answer invalid, kinds whose name does not
// match kindPattern or whose count is not positive are dropped, and at most
// maxKinds are kept, the most numerous first and ties by name.
func sanitizeStatistics(raw ProviderStatistics) (CodeStatistics, bool) {
	stats := CodeStatistics{Indexed: raw.Indexed, Kinds: []KindCount{}}
	if !raw.Indexed {
		return stats, true
	}
	if raw.Files < 0 || raw.Symbols < 0 || raw.Edges < 0 {
		return CodeStatistics{}, false
	}
	stats.Files, stats.Symbols, stats.Edges = raw.Files, raw.Symbols, raw.Edges
	for kind, count := range raw.Kinds {
		if count > 0 && kindPattern.MatchString(kind) {
			stats.Kinds = append(stats.Kinds, KindCount{Kind: kind, Count: count})
		}
	}
	sort.Slice(stats.Kinds, func(i, j int) bool {
		if stats.Kinds[i].Count != stats.Kinds[j].Count {
			return stats.Kinds[i].Count > stats.Kinds[j].Count
		}
		return stats.Kinds[i].Kind < stats.Kinds[j].Kind
	})
	if len(stats.Kinds) > maxKinds {
		stats.Kinds = stats.Kinds[:maxKinds]
	}
	return stats, true
}

// failedStatistics is the answer for an ask that failed: no counts, a code.
func failedStatistics(code string) CodeStatistics {
	return CodeStatistics{Kinds: []KindCount{}, Error: code}
}

// askProvider asks provider for one checkout, under its own timeout, and
// returns the statistics the document may carry. A panic in the provider, an
// error or an answer that is not statistics becomes a failure code, and a
// timeout is reported only when the ask's own deadline expired. The second
// result is false when parent ended during the ask (the pass was cancelled or
// the repository's provider budget ran out): then nothing is known and nothing
// should be recorded.
func askProvider(parent context.Context, provider CodeIndexProvider, checkout string, timeout time.Duration) (CodeStatistics, bool) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	var raw ProviderStatistics
	err := catch(func() (err error) {
		raw, err = provider.Statistics(ctx, checkout)
		return err
	})
	if err != nil {
		if parent.Err() != nil {
			return CodeStatistics{}, false
		}
		return failedStatistics(providerFailureCode(ctx, err)), true
	}
	stats, ok := sanitizeStatistics(raw)
	if !ok {
		return failedStatistics(ErrorProviderOutput), true
	}
	return stats, true
}

// maxProviderOutput caps what a provider command may print: statistics are a
// few hundred bytes.
const maxProviderOutput = 1 << 20

// CodeGrapherProvider reads CodeGrapher's statistics with its own command:
// `codegrapher status --json --path <checkout>`, which prints one JSON object
// (initialized, projectPath, fileCount, nodeCount, edgeCount, nodesByKind).
// WB never opens CodeGrapher's database. The command runs in its own process
// group with a sanitised environment and capped output, and nothing it prints
// to standard error is kept.
//
// Symbols are CodeGrapher's nodes other than its file nodes (files are counted
// on their own) and kinds its node kinds. CodeGrapher
// resolves a path to the nearest initialised ancestor; an answer for any
// directory but the checkout asked about is treated as "not indexed", so a
// checkout never borrows another's statistics.
type CodeGrapherProvider struct {
	// Binary is the command; empty means "codegrapher", looked up on PATH.
	Binary string
	// IndexerName is the hooks executor whose receipts this follows; empty
	// means "codegrapher".
	IndexerName string
	// Runner runs the command; nil means the real runner.
	Runner runner.Runner
}

// DefaultCodeGrapherIndexer is the executor name CodeGrapher is followed under
// unless cockpit.code_index_indexer says otherwise.
const DefaultCodeGrapherIndexer = "codegrapher"

// Name is the provider's name.
func (CodeGrapherProvider) Name() string { return "codegrapher" }

// Indexer is the executor name whose receipts this provider follows.
func (p CodeGrapherProvider) Indexer() string {
	return firstNonEmpty(p.IndexerName, DefaultCodeGrapherIndexer)
}

// providerEnvironment is the parent's PATH, HOME, TMPDIR and locale only.
func providerEnvironment(parent []string) []string {
	// Never nil: the runner reads a nil environment as "inherit the daemon's".
	env := []string{}
	for _, variable := range parent {
		for _, prefix := range []string{"PATH=", "HOME=", "TMPDIR=", "LANG=", "LC_"} {
			if strings.HasPrefix(variable, prefix) {
				env = append(env, variable)
				break
			}
		}
	}
	return env
}

// codeGrapherStatus is the part of `codegrapher status --json` that is read.
type codeGrapherStatus struct {
	Initialized bool           `json:"initialized"`
	ProjectPath string         `json:"projectPath"`
	FileCount   int            `json:"fileCount"`
	NodeCount   int            `json:"nodeCount"`
	EdgeCount   int            `json:"edgeCount"`
	NodesByKind map[string]int `json:"nodesByKind"`
}

// Statistics runs the command for checkout.
func (p CodeGrapherProvider) Statistics(ctx context.Context, checkout string) (ProviderStatistics, error) {
	out, err := runCapped(ctx, p.Runner, firstNonEmpty(p.Binary, "codegrapher"), providerEnvironment(os.Environ()), maxProviderOutput, []string{"status", "--json", "--path", checkout})
	if errors.Is(err, errCommandMissing) {
		return ProviderStatistics{}, errProviderUnavailable
	}
	if err != nil {
		return ProviderStatistics{}, err
	}
	return parseCodeGrapherStatus(out, checkout)
}

// parseCodeGrapherStatus reads the command's output for checkout.
func parseCodeGrapherStatus(out []byte, checkout string) (ProviderStatistics, error) {
	var status codeGrapherStatus
	if err := json.NewDecoder(bytes.NewReader(out)).Decode(&status); err != nil {
		return ProviderStatistics{}, errProviderOutput
	}
	if !status.Initialized || !sameDirectory(status.ProjectPath, checkout) {
		return ProviderStatistics{}, nil
	}
	// A file is a node of its own kind; it is counted as a file, not a symbol.
	symbols := status.NodeCount - status.NodesByKind[codeGrapherFileKind]
	delete(status.NodesByKind, codeGrapherFileKind)
	return ProviderStatistics{
		Indexed: true, Files: status.FileCount, Symbols: max(symbols, 0), Edges: status.EdgeCount, Kinds: status.NodesByKind,
	}, nil
}

// codeGrapherFileKind is the node kind CodeGrapher gives a file.
const codeGrapherFileKind = "file"

// sameDirectory reports whether two paths name one directory, symbolic links
// resolved when they can be.
func sameDirectory(left, right string) bool {
	resolve := func(path string) string {
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			return resolved
		}
		return filepath.Clean(path)
	}
	return left != "" && resolve(left) == resolve(right)
}
