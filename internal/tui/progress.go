// Package tui holds the bubbletea models for `wb sync`'s progress display
// and its post-run interactive results browser.
package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/progress"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/trakhimenok/workbench/wb/internal/fleetsync"
)

var (
	headerStyle = lipgloss.NewStyle().Bold(true)
	dimStyle    = lipgloss.NewStyle().Faint(true)
)

// RepoStarted signals a worker began processing org/name.
type RepoStarted struct{ Org, Name string }

// RepoDone signals a worker finished processing a repo.
type RepoDone struct{ Result fleetsync.Result }

// SyncDone signals every repo has been processed; the program should quit.
type SyncDone struct{}

type inFlight struct{ org, name string }

// ProgressModel renders an overall bar, one bar per org, and a bounded live
// tail of in-flight repos while a sync runs.
type ProgressModel struct {
	overall     progress.Model
	orgBars     map[string]progress.Model
	orgOrder    []string
	orgTotal    map[string]int
	orgDone     map[string]int
	total, done int
	inFlight    []inFlight
	maxInFlight int
	Results     []fleetsync.Result
	quitting    bool
}

// NewProgressModel builds a ProgressModel for the given per-org repo counts
// (orgTotal) and the worker count (maxInFlight, the live-tail row limit).
func NewProgressModel(orgTotal map[string]int, maxInFlight int) ProgressModel {
	orgs := make([]string, 0, len(orgTotal))
	total := 0
	for org, n := range orgTotal {
		orgs = append(orgs, org)
		total += n
	}
	sort.Strings(orgs)
	bars := make(map[string]progress.Model, len(orgs))
	for _, org := range orgs {
		bars[org] = progress.New(progress.WithDefaultGradient())
	}
	return ProgressModel{
		overall:     progress.New(progress.WithDefaultGradient()),
		orgBars:     bars,
		orgOrder:    orgs,
		orgTotal:    orgTotal,
		orgDone:     map[string]int{},
		total:       total,
		maxInFlight: maxInFlight,
	}
}

func (m ProgressModel) Init() tea.Cmd { return nil }

func (m ProgressModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case RepoStarted:
		m.inFlight = append(m.inFlight, inFlight{msg.Org, msg.Name})
		return m, nil
	case RepoDone:
		m.done++
		org := msg.Result.Repo.Org
		m.orgDone[org]++
		m.Results = append(m.Results, msg.Result)
		for i, f := range m.inFlight {
			if f.org == org && f.name == msg.Result.Repo.Name {
				m.inFlight = append(m.inFlight[:i], m.inFlight[i+1:]...)
				break
			}
		}
		return m, nil
	case SyncDone:
		m.quitting = true
		return m, tea.Quit
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m ProgressModel) View() string {
	if m.quitting {
		return ""
	}
	var b strings.Builder
	pct := 0.0
	if m.total > 0 {
		pct = float64(m.done) / float64(m.total)
	}
	fmt.Fprintf(&b, "%s %s %d/%d\n\n", headerStyle.Render("Overall"), m.overall.ViewAs(pct), m.done, m.total)
	for _, org := range m.orgOrder {
		p := 0.0
		if m.orgTotal[org] > 0 {
			p = float64(m.orgDone[org]) / float64(m.orgTotal[org])
		}
		bar := m.orgBars[org]
		fmt.Fprintf(&b, "%-20s %s %d/%d\n", org, bar.ViewAs(p), m.orgDone[org], m.orgTotal[org])
	}
	b.WriteString("\n")
	shown := m.inFlight
	if len(shown) > m.maxInFlight {
		shown = shown[len(shown)-m.maxInFlight:]
	}
	for _, f := range shown {
		fmt.Fprintf(&b, "%s\n", dimStyle.Render("  … "+f.org+"/"+f.name))
	}
	return b.String()
}
