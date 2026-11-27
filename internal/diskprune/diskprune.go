// Package diskprune is wb disk prune (spec/features/disk-prune): it removes
// this user's old scratch from the temporary directory, and clears the Go
// build cache while no Go process runs. Nothing else is ever deleted.
package diskprune

import (
	"context"
	"strings"
	"time"
)

// Category names, as --category takes them.
const (
	CategoryScratch = "scratch"
	CategoryBuild   = "go-build-cache"
)

// Categories are the names --category accepts, in pruning order.
var Categories = []string{CategoryScratch, CategoryBuild}

// DefaultScratchAge is how long nothing in a scratch directory may have
// changed before it is removed: comfortably past any running build or test.
const DefaultScratchAge = 72 * time.Hour

// Statuses of items and categories.
const (
	StatusDelete  = "delete"
	StatusDeleted = "deleted"
	StatusKeep    = "keep"
	StatusSkipped = "skipped"
	StatusFailed  = "failed"
)

// Outcomes of a run.
const (
	OutcomeDone    = "done"
	OutcomePlanned = "planned"
	OutcomeSkipped = "skipped"
	OutcomeFailed  = "failed"
)

// Options is one invocation's selection.
type Options struct {
	Apply bool
	// Categories restricts the run; empty means every category.
	Categories []string
	// ScratchAge is the minimum quiet time of a scratch directory; zero
	// means DefaultScratchAge.
	ScratchAge time.Duration
}

func (o Options) selected(name string) bool {
	if len(o.Categories) == 0 {
		return true
	}
	for _, category := range o.Categories {
		if category == name {
			return true
		}
	}
	return false
}

// Report is a run's result; --format json writes it as is.
type Report struct {
	SchemaVersion int              `json:"schema_version"`
	Mode          string           `json:"mode"`
	Outcome       string           `json:"outcome"`
	Categories    []CategoryReport `json:"categories"`
}

// CategoryReport is one category's plan or result.
type CategoryReport struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	// Bytes is what the plan would free, or what apply freed.
	Bytes int64        `json:"bytes"`
	Items []ItemReport `json:"items,omitempty"`
	// Kept counts the directories left alone, by reason.
	Kept map[string]int `json:"kept,omitempty"`
}

// ItemReport is one directory the category acts on.
type ItemReport struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// Process is a running Go tool, for the report.
type Process struct {
	PID     int
	Command string
}

// Env is the machine prune works on, as fields so a test can describe any.
type Env struct {
	// TempDirs are the resolved temporary directories (TMPDIR, GOTMPDIR).
	TempDirs []string
	UID      int
	Now      func() time.Time
	// GoCache is the resolved GOCACHE; GoCacheProblem says why there is
	// none prune may clear (GOCACHEPROG set, GOCACHE off or missing).
	GoCache        string
	GoCacheProblem string
	// GoProcesses lists running Go tools, or fails when that cannot be told.
	GoProcesses func() ([]Process, error)
	// GoClean runs go clean -cache.
	GoClean func(ctx context.Context) error
	// Measure is the bytes removing path would free.
	Measure func(ctx context.Context, path string) int64
	// RemoveAll removes a directory tree without following symlinks.
	RemoveAll func(path string) error
}

// Run plans, and with o.Apply prunes, every selected category.
func Run(ctx context.Context, env Env, o Options) Report {
	if o.ScratchAge == 0 {
		o.ScratchAge = DefaultScratchAge
	}
	report := Report{SchemaVersion: 1, Mode: "plan"}
	if o.Apply {
		report.Mode = "apply"
	}
	if o.selected(CategoryScratch) {
		report.Categories = append(report.Categories, pruneScratch(ctx, env, o))
	}
	if o.selected(CategoryBuild) {
		report.Categories = append(report.Categories, pruneBuildCache(ctx, env, o))
	}
	report.Outcome = outcome(report)
	return report
}

// outcome: failed beats skipped beats planned; done otherwise. A category
// skipped only because the configuration has nothing prune may clear (an
// unsupported cache) is not a finding.
func outcome(report Report) string {
	result := OutcomeDone
	for _, category := range report.Categories {
		switch {
		case category.Status == StatusFailed:
			return OutcomeFailed
		case category.Status == StatusSkipped && category.Reason != "" && !informational(category.Reason):
			result = OutcomeSkipped
		case category.Status == StatusDelete && result == OutcomeDone:
			result = OutcomePlanned
		}
	}
	return result
}

func informational(reason string) bool {
	return strings.HasPrefix(reason, "unsupported")
}
