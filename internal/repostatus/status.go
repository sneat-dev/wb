// Package repostatus collects local Git attention without CLI dependencies.
package repostatus

import (
	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/sneat-dev/wb/internal/reposelection"
)

// Observer receives joined inspection callbacks; Complete may run concurrently.
type Observer struct {
	Start    func(int)
	Complete func(reposelection.Target, Row)
}

// Collect selects once, then inspects the selected targets in deterministic order.
func Collect(request reposelection.Request, observer Observer) (Index, error) {
	return collectWith(request, observer, reposelection.Select, gitops.Status)
}

func collectWith(request reposelection.Request, observer Observer,
	selectTargets func(reposelection.Request) ([]reposelection.Target, error),
	status func(string) (gitops.RepoStatus, error),
) (Index, error) {
	targets, err := selectTargets(request)
	if err != nil {
		return Index{}, err
	}
	if observer.Start != nil {
		observer.Start(len(targets))
	}
	return Index{SchemaVersion: 1, Repositories: inspectTargetsWith(targets, request.Parallel, observer.Complete, status)}, nil
}

// InspectTargets reuses an already-selected fleet without scanning again.
func InspectTargets(targets []reposelection.Target, parallel int,
	complete func(reposelection.Target, Row),
) []Row {
	return inspectTargetsWith(targets, parallel, complete, gitops.Status)
}

type Index struct {
	SchemaVersion int `yaml:"schema_version" json:"schema_version"`
	// HiddenClean counts the clean repositories left out of Repositories, so
	// every consumer of this index can tell a filtered report from a fleet
	// where nothing was inspected.
	HiddenClean  int   `yaml:"hidden_clean,omitempty" json:"hidden_clean,omitempty"`
	Repositories []Row `yaml:"repositories" json:"repositories"`
}

type Row struct {
	Repository       string                  `yaml:"repository" json:"repository"`
	Path             string                  `yaml:"path" json:"path"`
	Status           string                  `yaml:"status" json:"status"`
	Summary          string                  `yaml:"summary,omitempty" json:"summary,omitempty"`
	Modified         []string                `yaml:"modified,omitempty" json:"modified,omitempty"`
	Untracked        []string                `yaml:"untracked,omitempty" json:"untracked,omitempty"`
	Conflicted       []string                `yaml:"conflicted,omitempty" json:"conflicted,omitempty"`
	Unpushed         []string                `yaml:"unpushed,omitempty" json:"unpushed,omitempty"`
	UnpushedBranches []gitops.UnpushedBranch `yaml:"unpushed_branches,omitempty" json:"unpushed_branches,omitempty"`
	Stashed          []string                `yaml:"stashed,omitempty" json:"stashed,omitempty"`
	Error            string                  `yaml:"error,omitempty" json:"error,omitempty"`
}

func inspectTargetsWith(
	targets []reposelection.Target,
	parallel int,
	complete func(reposelection.Target, Row),
	status func(string) (gitops.RepoStatus, error),
) []Row {
	reports := make([]Row, len(targets))
	reposelection.ForEach(len(targets), parallel, func(index int) {
		target := targets[index]
		state, err := status(target.Path)
		if err != nil {
			reports[index] = Row{Repository: target.Repository, Path: target.Path, Status: "error", Error: err.Error()}
			if complete != nil {
				complete(target, reports[index])
			}
			return
		}
		status := "clean"
		if state.Dirty() {
			status = "attention"
		}
		reports[index] = Row{
			Repository:       target.Repository,
			Path:             target.Path,
			Status:           status,
			Summary:          state.Summary(),
			Modified:         state.Modified,
			Untracked:        state.Untracked,
			Conflicted:       state.Conflicted,
			Unpushed:         state.Unpushed,
			UnpushedBranches: state.UnpushedBranches,
			Stashed:          state.Stashed,
		}
		if complete != nil {
			complete(target, reports[index])
		}
	})
	return reports
}

// HideClean drops the clean rows and records how many were
// dropped. Errors and attention rows survive, so the exit code and the
// worklist stay the same whether or not the report was filtered.
func HideClean(report Index) Index {
	kept := make([]Row, 0, len(report.Repositories))
	for _, repository := range report.Repositories {
		if repository.Status == "clean" {
			continue
		}
		kept = append(kept, repository)
	}
	report.HiddenClean = len(report.Repositories) - len(kept)
	report.Repositories = kept
	return report
}

func Failed(report Index) bool {
	for _, repository := range report.Repositories {
		if repository.Status == "error" {
			return true
		}
	}
	return false
}
