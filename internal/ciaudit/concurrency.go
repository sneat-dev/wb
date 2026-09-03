package ciaudit

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Concurrency is one workflow's cancel-in-progress policy.
//
// A stream branch is force-pushed on every rebase, so without a concurrency
// group keyed to the branch a superseded push races its predecessor instead of
// cancelling it: the same commit range is built twice and the fleet pays for
// both. `push-hook-defers-to-ci-on-stream-branches` moves local cost to CI
// and therefore obliges WB to bound CI, which starts with proving the
// cancellation is configured at all.
type Concurrency struct {
	// Workflow is the repository-relative workflow path.
	Workflow string `json:"workflow"`
	// Name is the workflow's declared name, when it has one.
	Name string `json:"name,omitempty"`
	// PullRequest is true when the workflow runs on pull_request events, and
	// therefore runs on a stream branch's draft pull request.
	PullRequest bool `json:"pull_request"`
	// Push is true when the workflow runs on push events.
	Push bool `json:"push"`
	// Group is the concurrency group expression, empty when the workflow
	// declares no concurrency at all.
	Group string `json:"group,omitempty"`
	// CancelInProgress is the declared value; false covers both "declared
	// false" and "not declared", which Declared distinguishes.
	CancelInProgress bool `json:"cancel_in_progress"`
	// Declared is true when the workflow declares a concurrency block.
	Declared bool `json:"declared"`
	// RefKeyed is true when the group expression varies per ref or per pull
	// request, which is what makes cancellation scoped to one stream branch
	// rather than to the whole repository.
	RefKeyed bool `json:"ref_keyed"`
}

// Cancels reports whether this workflow cancels a superseded run for the
// branch it is building.
func (concurrency Concurrency) Cancels() bool {
	return concurrency.Declared && concurrency.CancelInProgress && concurrency.RefKeyed
}

// StreamConcurrency reads every workflow under .github/workflows and reports
// the pull-request workflows a stream branch's draft pull request would
// trigger, with their concurrency policy.
//
// It is deliberately a typed YAML read rather than a regular expression: the
// value being checked (`cancel-in-progress: true` under a ref-keyed group) is
// exactly the kind of nested structure a text match reports as present when it
// is declared for a different job.
func StreamConcurrency(root string) ([]Concurrency, error) {
	directory := filepath.Join(root, ".github", "workflows")
	entries, err := os.ReadDir(directory)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read workflows in %s: %w", directory, err)
	}
	var reports []Concurrency
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		lower := strings.ToLower(name)
		if !strings.HasSuffix(lower, ".yml") && !strings.HasSuffix(lower, ".yaml") {
			continue
		}
		contents, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			return nil, fmt.Errorf("read workflow %s: %w", name, err)
		}
		report, err := parseWorkflowConcurrency(filepath.ToSlash(filepath.Join(".github", "workflows", name)), contents)
		if err != nil {
			return nil, err
		}
		reports = append(reports, report)
	}
	sort.Slice(reports, func(i, j int) bool { return reports[i].Workflow < reports[j].Workflow })
	return reports, nil
}

type workflowDocument struct {
	Name        string    `yaml:"name"`
	On          yaml.Node `yaml:"on"`
	Concurrency yaml.Node `yaml:"concurrency"`
}

func parseWorkflowConcurrency(path string, contents []byte) (Concurrency, error) {
	var document workflowDocument
	if err := yaml.Unmarshal(contents, &document); err != nil {
		return Concurrency{}, fmt.Errorf("parse workflow %s: %w", path, err)
	}
	report := Concurrency{Workflow: path, Name: document.Name}
	for _, event := range workflowEvents(document.On) {
		switch event {
		case "pull_request", "pull_request_target":
			report.PullRequest = true
		case "push":
			report.Push = true
		}
	}
	group, cancel, declared := workflowConcurrency(document.Concurrency)
	report.Group, report.CancelInProgress, report.Declared = group, cancel, declared
	report.RefKeyed = groupIsRefKeyed(group)
	return report, nil
}

// workflowEvents flattens the three shapes GitHub accepts for `on`: a scalar,
// a sequence, and a mapping.
//
// YAML 1.1 readers fold a bare `on:` key to the boolean true, which is why the
// key is matched case-insensitively against both spellings rather than assumed
// to arrive as the string "on".
func workflowEvents(node yaml.Node) []string {
	switch node.Kind {
	case yaml.ScalarNode:
		return []string{node.Value}
	case yaml.SequenceNode:
		events := make([]string, 0, len(node.Content))
		for _, child := range node.Content {
			events = append(events, child.Value)
		}
		return events
	case yaml.MappingNode:
		events := make([]string, 0, len(node.Content)/2)
		for index := 0; index+1 < len(node.Content); index += 2 {
			events = append(events, node.Content[index].Value)
		}
		return events
	}
	return nil
}

func workflowConcurrency(node yaml.Node) (group string, cancel bool, declared bool) {
	switch node.Kind {
	case yaml.ScalarNode:
		if strings.TrimSpace(node.Value) == "" {
			return "", false, false
		}
		// A bare string concurrency declares a group with no cancellation.
		return node.Value, false, true
	case yaml.MappingNode:
		for index := 0; index+1 < len(node.Content); index += 2 {
			key := node.Content[index].Value
			value := node.Content[index+1]
			switch key {
			case "group":
				group = value.Value
			case "cancel-in-progress":
				cancel = strings.EqualFold(strings.TrimSpace(value.Value), "true")
			}
		}
		return group, cancel, true
	}
	return "", false, false
}

// groupIsRefKeyed reports whether the group expression varies per branch or
// pull request. A group that names only the workflow serializes the whole
// repository, which is a different — and for a stream, wrong — policy: it
// would queue an unrelated branch behind the stream instead of cancelling the
// stream's own superseded run.
func groupIsRefKeyed(group string) bool {
	lower := strings.ToLower(group)
	for _, marker := range []string{
		"github.ref",
		"github.head_ref",
		"github.event.pull_request.number",
		"github.event.number",
		"github.sha",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// WorkflowMechanisms reports which verification mechanisms one workflow
// actually runs.
//
// `batch-verification-runs-what-ci-runs` allows a local run to name a mechanism
// as skipped only after proving CI carries it. That proof has to be a read of
// the workflow, not an assumption: an unverified "CI owns it" is the
// 17-occurrence lesson reintroduced as a false assurance, which is worse than
// no gate at all.
//
// The mechanism names match what a verification run reports as skipped.
func WorkflowMechanisms(root, workflow string) (map[string]bool, error) {
	contents, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(workflow)))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read workflow %s: %w", workflow, err)
	}
	// Comments are stripped first. A workflow saying "we should add -race one
	// day" is the opposite of evidence that CI runs it, and matching the word
	// rather than the invocation is exactly how a false "CI owns it" gets
	// printed.
	text := stripYAMLComments(string(contents))
	mechanisms := map[string]bool{}
	for mechanism, pattern := range workflowMechanismPatterns {
		if pattern.MatchString(text) {
			mechanisms[mechanism] = true
		}
	}
	return mechanisms, nil
}

// workflowMechanismPatterns maps a mechanism to the evidence that it runs.
//
// Each pattern matches the invocation rather than the word: `-race` appearing
// in a comment or a job name is not evidence that the race detector runs.
var workflowMechanismPatterns = map[string]*regexp.Regexp{
	"-race":    regexp.MustCompile(`(?m)\bgo\s+test\b[^\n]*\s-race\b`),
	"go vet":   regexp.MustCompile(`(?m)\bgo\s+vet\b`),
	"-count=1": regexp.MustCompile(`(?m)\bgo\s+test\b[^\n]*-count=1\b`),
	"CI=1":     regexp.MustCompile(`(?mi)^\s*CI\s*:\s*["']?(?:1|true)`),
}

// stripYAMLComments removes each line's comment. It only treats a `#` at the
// start of a line or after whitespace as a comment opener, so a `#` inside a
// value (a colour, a fragment) is left alone.
func stripYAMLComments(text string) string {
	lines := strings.Split(text, "\n")
	for index, line := range lines {
		inSingle, inDouble := false, false
		for position, character := range line {
			switch character {
			case '\'':
				if !inDouble {
					inSingle = !inSingle
				}
			case '"':
				if !inSingle {
					inDouble = !inDouble
				}
			case '#':
				if inSingle || inDouble {
					continue
				}
				if position == 0 || line[position-1] == ' ' || line[position-1] == '\t' {
					lines[index] = line[:position]
				}
			}
			if len(lines[index]) <= position {
				break
			}
		}
	}
	return strings.Join(lines, "\n")
}
