package sessionrun

import (
	"context"
	"fmt"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/waitregistry"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
	"sort"
	"strings"
)

type ListRequest struct {
	ProjectsRoot string
	OnlyLive     bool
}
type ListDependencies struct {
	Directory func(string) (string, error)
	Records   func(string) ([]session.View, error)
	Worktrees func(context.Context, worktrees.ListOptions) ([]worktrees.ListResult, error)
	Home      func(string) (string, error)
	Waits     func(string, waitregistry.Options) ([]waitregistry.Record, error)
}

func DefaultListDependencies() ListDependencies {
	return ListDependencies{Directory: DirForRead, Records: session.List, Worktrees: worktrees.List, Home: wbhome.Root, Waits: waitregistry.List}
}

type ListService struct{ deps ListDependencies }

func NewList(deps ListDependencies) *ListService { return &ListService{deps: deps} }

// List intentionally detaches the attribution scan from caller cancellation,
// matching the existing observational listing contract.
func (s *ListService) List(_ context.Context, request ListRequest, warn func(string)) ([]Row, error) {
	directory, err := s.deps.Directory(request.ProjectsRoot)
	if err != nil {
		return nil, err
	}
	views, err := s.deps.Records(directory)
	if err != nil {
		return nil, err
	}
	if request.OnlyLive {
		live := make([]session.View, 0, len(views))
		for _, view := range views {
			if view.State == session.StateLive {
				live = append(live, view)
			}
		}
		views = live
	}
	if len(views) == 0 {
		return []Row{}, nil
	}
	results, err := s.deps.Worktrees(context.Background(), worktrees.ListOptions{ProjectsRoot: request.ProjectsRoot})
	if err != nil {
		if warn != nil {
			warn(fmt.Sprintf("derive worktree attribution: %v\n", err))
		}
		results = nil
	}
	rows := attributeSessions(views, results)
	s.attributeWaits(rows, request.ProjectsRoot, warn)
	return rows, nil
}

type Row struct {
	session.View
	Efforts   []string `json:"efforts"`
	Worktrees []string `json:"worktrees"`
	Branches  []string `json:"branches"`
	// Waiting is what this session has delegated to WB and is still blocked
	// on. Without it a session that correctly delegated its waiting is
	// indistinguishable from one that stopped.
	Waiting []string `json:"waiting,omitempty"`
}

func attributeSessions(views []session.View, results []worktrees.ListResult) []Row {
	rows := make([]Row, 0, len(views))
	for _, view := range views {
		efforts, worktreeDirs, branches := map[string]bool{}, map[string]bool{}, map[string]bool{}
		for _, result := range results {
			matched := false
			for _, owner := range result.Owners {
				if owner.PID != view.PID || owner.At.Before(view.StartedAt) {
					continue
				}
				matched = true
				if owner.Effort != "" {
					efforts[owner.Effort] = true
				}
			}
			if matched {
				if result.WorktreeDir != "" {
					worktreeDirs[result.WorktreeDir] = true
				}
				if result.Branch != "" {
					branches[result.Branch] = true
				}
			}
		}
		rows = append(rows, Row{
			View:      view,
			Efforts:   sortedKeys(efforts),
			Worktrees: sortedKeys(worktreeDirs),
			Branches:  sortedKeys(branches),
		})
	}
	return rows
}
func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
func (s *ListService) attributeWaits(rows []Row, projectsRoot string, warn func(string)) {
	home, err := s.deps.Home(projectsRoot)
	if err != nil {
		return
	}
	records, err := s.deps.Waits(home, waitregistry.Options{})
	if err != nil {
		if warn != nil {
			warn(fmt.Sprintf("derive outstanding waits: %v\n", err))
		}
		return
	}
	waiting := map[string][]string{}
	for _, record := range records {
		label := record.Kind + " " + strings.Join(record.Targets, ",")
		if record.Stale {
			label += " (stale)"
		}
		waiting[record.WBSessionID] = append(waiting[record.WBSessionID], label)
	}
	for index := range rows {
		rows[index].Waiting = waiting[rows[index].WBSessionID]
	}
}
