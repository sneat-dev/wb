package worktreebranches

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestDependencyChangesFailClosedAtEveryGitBoundary(t *testing.T) {
	const workflow = ".github/workflows/check.yml"
	const manifest = "package.json"
	boom := errors.New("unreadable git object")
	for _, tc := range []struct {
		name, file, stage, want string
	}{
		{name: "missing identities", stage: "identity", want: "exact source and target identities"},
		{name: "merge base error", stage: "merge-base-error", want: "cannot derive exact source/target merge base"},
		{name: "empty merge base", stage: "merge-base-empty", want: "cannot derive exact source/target merge base"},
		{name: "diff error", stage: "diff-error", want: "cannot inspect exact source dependency diff"},
		{name: "source object error", file: manifest, stage: "source-error", want: "cannot read exact source dependency file"},
		{name: "base package object error", file: manifest, stage: "base-error", want: "cannot read exact base dependency file"},
		{name: "new workflow absence unavailable", file: workflow, stage: "tree-error", want: "cannot prove workflow"},
		{name: "new workflow already in base", file: workflow, stage: "tree-present", want: "cannot prove workflow"},
		{name: "malformed package json", file: manifest, stage: "bad-package", want: "cannot compare dependency-bearing content"},
		{name: "new workflow is classified", file: workflow, stage: "new-workflow", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry := SupersessionEntry{CanonicalDir: "/repo", HeadSHA: "source", RemoteTargetSHA: "target"}
			if tc.stage == "identity" {
				entry.CanonicalDir = ""
			}
			service := SupersessionService{Ports: SupersessionPorts{Git: func(_ context.Context, _ string, args ...string) (string, error) {
				switch args[0] {
				case "merge-base":
					if tc.stage == "merge-base-error" {
						return "", boom
					}
					if tc.stage == "merge-base-empty" {
						return "", nil
					}
					return "base", nil
				case "diff":
					if tc.stage == "diff-error" {
						return "", boom
					}
					return tc.file + "\x00", nil
				case "show":
					object := args[1]
					if strings.HasPrefix(object, "base:") {
						if tc.stage == "base-error" || tc.stage == "new-workflow" || tc.stage == "tree-error" || tc.stage == "tree-present" {
							return "", boom
						}
						if tc.stage == "bad-package" {
							return `{`, nil
						}
						return `{"dependencies":{"demo":"1.0.0"}}`, nil
					}
					if tc.stage == "source-error" {
						return "", boom
					}
					if tc.stage == "bad-package" {
						return `{`, nil
					}
					return `{"dependencies":{"demo":"2.0.0"}}`, nil
				case "ls-tree":
					if tc.stage == "tree-error" {
						return "", boom
					}
					if tc.stage == "tree-present" {
						return workflow, nil
					}
					return "", nil
				default:
					t.Fatalf("unexpected git operation: %q", args)
					return "", nil
				}
			}}}
			changes, rejection := service.dependencyChanges(context.Background(), entry)
			if tc.want != "" && !strings.Contains(rejection, tc.want) {
				t.Fatalf("rejection = %q, want %q", rejection, tc.want)
			}
			if tc.want == "" && (rejection != "" || len(changes) != 1 || !changes[0].added || changes[0].path != workflow) {
				t.Fatalf("new workflow result = %#v, %q", changes, rejection)
			}
		})
	}
}

func TestDependencyCampaignWorktreeRecognizesLegacyHints(t *testing.T) {
	t.Parallel()
	service := SupersessionService{}
	for _, tc := range []struct {
		name  string
		entry SupersessionEntry
		want  bool
	}{
		{name: "dependency task prefix", entry: SupersessionEntry{Task: "deps-release"}, want: true},
		{name: "dependency branch prefix", entry: SupersessionEntry{Branch: "wb/deps/update"}, want: true},
		{name: "no worktree directory", entry: SupersessionEntry{Task: "sample", Branch: "feature/sample"}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := service.DependencyCampaignWorktree(context.Background(), tc.entry); got != tc.want {
				t.Fatalf("dependency campaign = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestValidateDependencyDeltasReasonRequiresWorkflowAdoptionProof(t *testing.T) {
	for _, tc := range []struct {
		name, want            string
		entry                 SupersessionEntry
		receipt               SupersessionReceipt
		marker                bool
		dependencyChanged     bool
		dependencyReadFailure bool
	}{
		{name: "dependency task marker", entry: SupersessionEntry{Task: "deps-release"}, want: "requires original_pr"},
		{name: "campaign marker", entry: SupersessionEntry{WorktreeDir: "/worktree"}, marker: true, want: "requires original_pr"},
		{name: "orphaned workflow adoption", entry: SupersessionEntry{CanonicalDir: "/repo", HeadSHA: "source", RemoteTargetSHA: "target"}, receipt: SupersessionReceipt{WorkflowAdoptions: []SupersessionWorkflowAdoption{{Path: ".github/workflows/ci.yml"}}}, want: "has no newly introduced dependency workflow"},
		{name: "delta evidence without PR", entry: SupersessionEntry{CanonicalDir: "/repo", HeadSHA: "source", RemoteTargetSHA: "target"}, receipt: SupersessionReceipt{DependencyDeltasComplete: true}, want: "dependency delta evidence requires original_pr"},
		{name: "dependency diff failure refuses generic proof", entry: SupersessionEntry{CanonicalDir: "/repo", HeadSHA: "source", RemoteTargetSHA: "target"}, dependencyReadFailure: true, want: "requires original_pr"},
		{name: "workflow adoption cannot replace dependency PR", receipt: SupersessionReceipt{OriginalPR: "https://example.test/pr/1", WorkflowAdoptions: []SupersessionWorkflowAdoption{{Path: ".github/workflows/ci.yml"}}}, want: "cannot replace exact dependency PR evidence"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := SupersessionService{Ports: SupersessionPorts{
				ReadCampaignMarker: func(string) (bool, error) { return tc.marker, nil },
				Git: func(_ context.Context, _ string, args ...string) (string, error) {
					switch args[0] {
					case "merge-base":
						if tc.dependencyReadFailure {
							return "", errors.New("missing merge base")
						}
						return "base", nil
					case "diff":
						if tc.dependencyChanged {
							return "package.json\x00", nil
						}
						return "", nil
					case "show":
						if strings.HasPrefix(args[1], "base:") {
							return `{"dependencies":{"demo":"1"}}`, nil
						}
						return `{"dependencies":{"demo":"2"}}`, nil
					default:
						return "", errors.New("unexpected Git operation")
					}
				},
			}}
			got := service.ValidateDependencyDeltasReason(context.Background(), tc.receipt, tc.entry)
			if !strings.Contains(got, tc.want) {
				t.Fatalf("rejection = %q, want %q", got, tc.want)
			}
		})
	}
	service := SupersessionService{Ports: SupersessionPorts{Git: func(_ context.Context, _ string, args ...string) (string, error) {
		if args[0] == "merge-base" {
			return "base", nil
		}
		if args[0] == "diff" {
			return "", nil
		}
		return "", errors.New("unexpected Git operation")
	}}}
	if got := service.ValidateDependencyDeltasReason(context.Background(), SupersessionReceipt{}, SupersessionEntry{CanonicalDir: "/repo", HeadSHA: "source", RemoteTargetSHA: "target"}); got != "" {
		t.Fatalf("generic receipt with no dependency changes rejected: %s", got)
	}
}

func TestValidateWorkflowAdoptionsRejectsUnprovenEvidence(t *testing.T) {
	const workflowPath = ".github/workflows/check.yml"
	const source = "source"
	replacement, target := strings.Repeat("c", 40), strings.Repeat("b", 40)
	body := []byte("name: check\njobs: {}\n")
	digest := sha256.Sum256(body)
	validReceipt := func() SupersessionReceipt {
		return SupersessionReceipt{
			Approval:          SupersessionApproval{Actor: "reviewer", Trusted: true, Decision: "approved", ReceiptID: "review-1", ApprovedAt: time.Now()},
			Replacements:      []SupersessionReplacement{{Kind: "commit", Ref: replacement, SHA: replacement}},
			WorkflowAdoptions: []SupersessionWorkflowAdoption{{Path: workflowPath, SourceSHA256: hex.EncodeToString(digest[:]), ReplacementSHA: replacement, Reviewed: true}},
		}
	}
	for _, tc := range []struct {
		name, want string
		changes    int
		change     func(*SupersessionReceipt, *SupersessionEntry, *SupersessionPorts)
	}{
		{name: "ancestor port missing", want: "cannot verify replacement ancestry", change: func(_ *SupersessionReceipt, _ *SupersessionEntry, p *SupersessionPorts) { p.IsAncestor = nil }},
		{name: "unsafe path", want: "invalid added-workflow path", change: func(r *SupersessionReceipt, _ *SupersessionEntry, _ *SupersessionPorts) {
			r.WorkflowAdoptions[0].Path = "../check.yml"
		}},
		{name: "duplicate path", want: "repeats path", changes: 2, change: func(r *SupersessionReceipt, _ *SupersessionEntry, _ *SupersessionPorts) {
			r.WorkflowAdoptions = append(r.WorkflowAdoptions, r.WorkflowAdoptions[0])
		}},
		{name: "missing object ID", want: "missing exact source hash or replacement SHA", change: func(r *SupersessionReceipt, _ *SupersessionEntry, _ *SupersessionPorts) {
			r.WorkflowAdoptions[0].ReplacementSHA = "bad"
		}},
		{name: "invalid digest syntax", want: "invalid source SHA-256", change: func(r *SupersessionReceipt, _ *SupersessionEntry, _ *SupersessionPorts) {
			r.WorkflowAdoptions[0].SourceSHA256 = strings.Repeat("z", 64)
		}},
		{name: "changed source blob", want: "cannot read exact source workflow", change: func(_ *SupersessionReceipt, _ *SupersessionEntry, p *SupersessionPorts) {
			p.ReadGitFileBytes = func(context.Context, string, string, string) ([]byte, error) {
				return nil, errors.New("missing source")
			}
		}},
		{name: "source bytes drift", want: "source hash does not match", change: func(_ *SupersessionReceipt, _ *SupersessionEntry, p *SupersessionPorts) {
			p.ReadGitFileBytes = func(_ context.Context, _, revision, _ string) ([]byte, error) {
				if revision == source {
					return []byte("different"), nil
				}
				return body, nil
			}
		}},
		{name: "byte reader unavailable", want: "byte-preserving Git blob reader is unavailable", change: func(_ *SupersessionReceipt, _ *SupersessionEntry, p *SupersessionPorts) { p.ReadGitFileBytes = nil }},
		{name: "unlisted replacement commit", want: "absent from the replacement inventory", change: func(r *SupersessionReceipt, _ *SupersessionEntry, _ *SupersessionPorts) { r.Replacements = nil }},
		{name: "replacement ancestry error", want: "not verified in exact target", change: func(_ *SupersessionReceipt, _ *SupersessionEntry, p *SupersessionPorts) {
			p.IsAncestor = func(context.Context, string, string, string) (bool, error) { return false, errors.New("git failed") }
		}},
		{name: "replacement is not an ancestor", want: "not verified in exact target", change: func(_ *SupersessionReceipt, _ *SupersessionEntry, p *SupersessionPorts) {
			p.IsAncestor = func(context.Context, string, string, string) (bool, error) { return false, nil }
		}},
		{name: "target blob unreadable", want: "cannot read exact target file", change: func(_ *SupersessionReceipt, _ *SupersessionEntry, p *SupersessionPorts) {
			p.ReadGitFileBytes = func(_ context.Context, _, revision, _ string) ([]byte, error) {
				if revision == target {
					return nil, errors.New("missing target")
				}
				return body, nil
			}
		}},
		{name: "replacement blob unreadable", want: "cannot read replacement file", change: func(_ *SupersessionReceipt, _ *SupersessionEntry, p *SupersessionPorts) {
			p.ReadGitFileBytes = func(_ context.Context, _, revision, _ string) ([]byte, error) {
				if revision == replacement {
					return nil, errors.New("missing replacement")
				}
				return body, nil
			}
		}},
		{name: "target blob drift", want: "differs from the reviewed source bytes", change: func(_ *SupersessionReceipt, _ *SupersessionEntry, p *SupersessionPorts) {
			p.ReadGitFileBytes = func(_ context.Context, _, revision, _ string) ([]byte, error) {
				if revision == target {
					return []byte("different"), nil
				}
				return body, nil
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := validReceipt()
			e := SupersessionEntry{CanonicalDir: "/repo", HeadSHA: source, RemoteTargetSHA: target}
			p := SupersessionPorts{IsAncestor: func(context.Context, string, string, string) (bool, error) { return true, nil }, ReadGitFileBytes: func(_ context.Context, _, _, _ string) ([]byte, error) { return body, nil }}
			if tc.change != nil {
				tc.change(&r, &e, &p)
			}
			changes := []dependencyChange{{path: workflowPath, added: true}}
			if tc.changes == 2 {
				changes = append(changes, dependencyChange{path: ".github/workflows/other.yml", added: true})
			}
			got := (SupersessionService{Ports: p}).validateWorkflowAdoptions(context.Background(), r, e, changes)
			if !strings.Contains(got, tc.want) {
				t.Fatalf("rejection = %q, want %q", got, tc.want)
			}
		})
	}
	for _, tc := range []struct {
		name   string
		change func(*SupersessionReceipt, []dependencyChange)
	}{
		{name: "missing approval", change: func(r *SupersessionReceipt, _ []dependencyChange) { r.Approval.Trusted = false }},
		{name: "adoption count mismatch", change: func(r *SupersessionReceipt, _ []dependencyChange) { r.WorkflowAdoptions = nil }},
		{name: "unreviewed", change: func(r *SupersessionReceipt, _ []dependencyChange) { r.WorkflowAdoptions[0].Reviewed = false }},
		{name: "changed path does not match proof", change: func(_ *SupersessionReceipt, c []dependencyChange) { c[0].path = ".github/workflows/other.yml" }},
		{name: "not an added workflow", change: func(_ *SupersessionReceipt, c []dependencyChange) { c[0].added = false }}} {
		t.Run(tc.name, func(t *testing.T) {
			r := validReceipt()
			changes := []dependencyChange{{path: workflowPath, added: true}}
			tc.change(&r, changes)
			service := SupersessionService{Ports: SupersessionPorts{IsAncestor: func(context.Context, string, string, string) (bool, error) { return true, nil }}}
			got := service.validateWorkflowAdoptions(context.Background(), r, SupersessionEntry{}, changes)
			if got == "" {
				t.Fatal("invalid workflow adoption was accepted")
			}
		})
	}
}

func TestDependencyContentParsersRejectMalformedInputsAndCompareResolutionData(t *testing.T) {
	for _, tc := range []struct {
		name, file             string
		before, after          []byte
		wantChanged, wantError bool
	}{
		{"npm same semantic sections", "package.json", []byte(`{"dependencies":{"x":"1"}}`), []byte("{\n  \"dependencies\": {\"x\": \"1\"}\n}"), false, false},
		{"npm malformed", "package.json", []byte("{"), []byte(`{}`), false, true},
		{"npm head malformed", "package.json", []byte(`{}`), []byte("{"), false, true},
		{"npm wrong dependency shape", "package.json", []byte(`{"dependencies":[]}`), []byte(`{}`), false, true},
		{"go mod malformed", "go.mod", []byte("module"), []byte("module example.test/app\ngo 1.24\n"), false, true},
		{"go workspace malformed", "go.work", []byte("go"), []byte("go 1.24\n"), false, true},
		{"go workspace head malformed", "go.work", []byte("go 1.24\n"), []byte("go"), false, true},
		{"go workspace resolution change", "go.work", []byte("go 1.24\nuse ./a\n"), []byte("go 1.25\nuse ./a\n"), true, false},
		{"go workspace replacement and toolchain", "go.work", []byte("go 1.24\nuse ./a\nreplace example.test/lib v1.0.0 => ./lib\n"), []byte("go 1.24\ntoolchain go1.24.1\nuse ./a\nreplace example.test/lib v1.0.0 => ./lib\n"), true, false},
		{"go mod full sections", "go.mod", []byte("module example.test/app\ngo 1.24\nrequire example.test/lib v1.2.3\n"), []byte("module example.test/app\ngo 1.24\ntoolchain go1.24.1\nrequire example.test/lib v1.2.3\nreplace example.test/lib => ./lib\nexclude example.test/lib v1.0.0\n"), true, false},
		{"lockfile raw change", "go.work.sum", []byte("x h1:a\n"), []byte("x h1:b\n"), true, false},
		{"workspace raw change", "pnpm-workspace.yaml", []byte("packages: [a]\n"), []byte("packages: [b]\n"), true, false},
		{"invalid workflow yaml", ".github/workflows/test.yml", []byte("jobs: [\n"), []byte("jobs: {}\n"), false, true},
		{"workflow with mapping duplicate", ".github/workflows/test.yml", []byte("steps:\n- uses: actions/setup-go@v5\n  with: {go-version: 1.24, go-version: 1.25}\n"), []byte("steps:\n- uses: actions/setup-go@v5\n"), false, true},
		{"changed workflow action input", ".github/workflows/test.yml", []byte("steps:\n- uses: actions/setup-go@v5\n  with: {go-version: '1.24'}\n"), []byte("steps:\n- uses: actions/setup-go@v5\n  with: {go-version: '1.25'}\n"), true, false},
		{"workflow run script ignored", ".github/workflows/test.yml", []byte("steps:\n- run: echo before\n"), []byte("steps:\n- run: echo after\n"), false, false},
		{"workflow head malformed", ".github/workflows/test.yml", []byte("jobs: {}\n"), []byte("jobs: [\n"), false, true},
		{"unrecognized file ignored", "README.md", []byte("a"), []byte("b"), false, false}} {
		t.Run(tc.name, func(t *testing.T) {
			changed, err := dependencyContentChanged(tc.file, tc.before, tc.after)
			if (err != nil) != tc.wantError || changed != tc.wantChanged {
				t.Fatalf("dependencyContentChanged = %v, %v", changed, err)
			}
		})
	}
	if _, err := workflowActionReferences([]byte("jobs:\n  a:\n    steps:\n    - uses: actions/checkout@v4\n      uses: actions/setup-go@v5\n")); err == nil || !strings.Contains(err.Error(), "duplicate uses") {
		t.Fatalf("duplicate workflow action uses error = %v", err)
	}
	if _, err := workflowActionReferences([]byte("jobs:\n  a:\n    steps:\n    - uses: {not: scalar}\n")); err == nil || !strings.Contains(err.Error(), "non-empty scalar") {
		t.Fatalf("invalid workflow action uses error = %v", err)
	}
	if _, err := workflowActionReferences([]byte("jobs:\n  a:\n    steps:\n    - uses: actions/setup-go@v5\n      with: {go-version: 1.24, go-version: 1.25}\n")); err == nil || !strings.Contains(err.Error(), "duplicate key") {
		t.Fatalf("duplicate workflow input error = %v", err)
	}
	refs, err := workflowActionReferences([]byte("jobs:\n  a:\n    steps:\n    - uses: actions/checkout@v4\n      with:\n        fetch-depth: 0\n    - run: echo hi\n"))
	if err != nil || len(refs) != 1 || !strings.Contains(refs[0], "fetch-depth") {
		t.Fatalf("canonical action refs = %#v, %v", refs, err)
	}
	if _, err := npmDependencySections([]byte(`{"overrides":{"demo":1e10000}}`)); err == nil {
		t.Fatal("out-of-range JSON number in a resolution section was accepted")
	}
	if _, err := npmDependencySectionsWithMarshal([]byte(`{"overrides":{"demo":"1"}}`), func(any) ([]byte, error) { return nil, errors.New("marshal failed") }); err == nil || !strings.Contains(err.Error(), "canonicalize overrides") {
		t.Fatalf("canonical npm section error = %v", err)
	}
	if _, err := workflowActionReferencesWithMarshal([]byte("steps:\n- uses: actions/checkout@v4\n"), func(any) ([]byte, error) { return nil, errors.New("marshal failed") }); err == nil || !strings.Contains(err.Error(), "canonicalize workflow action inputs") {
		t.Fatalf("canonical workflow action error = %v", err)
	}
	if _, err := marshalCanonicalJSON(func() {}); err == nil {
		t.Fatal("unsupported canonical JSON value was accepted")
	}
}

func TestCanonicalYAMLNodeRejectsMalformedTreeShapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		node *yaml.Node
		want string
	}{
		{"empty document", &yaml.Node{Kind: yaml.DocumentNode}, "exactly one value"},
		{"odd mapping", &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{{Kind: yaml.ScalarNode, Value: "key"}}}, "incomplete key/value"},
		{"complex key", &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{{Kind: yaml.SequenceNode}, {Kind: yaml.ScalarNode, Value: "value"}}}, "key is not scalar"},
		{"duplicate key", &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{{Kind: yaml.ScalarNode, Value: "key"}, {Kind: yaml.ScalarNode, Value: "one"}, {Kind: yaml.ScalarNode, Value: "key"}, {Kind: yaml.ScalarNode, Value: "two"}}}, "duplicate key"},
		{"nested invalid mapping", &yaml.Node{Kind: yaml.SequenceNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Content: []*yaml.Node{{Kind: yaml.SequenceNode}, {Kind: yaml.ScalarNode}}}}}, "key is not scalar"},
		{"unsupported node", &yaml.Node{Kind: yaml.AliasNode}, "unsupported YAML node kind"}} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := canonicalYAMLNode(tc.node); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("canonicalYAMLNode error = %v, want %q", err, tc.want)
			}
		})
	}
	if value, err := canonicalYAMLNode(&yaml.Node{Kind: yaml.SequenceNode, Content: []*yaml.Node{{Kind: yaml.ScalarNode, Tag: "!!str", Value: "x"}}}); err != nil || len(value.([]any)) != 1 {
		t.Fatalf("canonical sequence = %#v, %v", value, err)
	}
	if _, err := canonicalYAMLNode(&yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.ScalarNode, Tag: "!!str", Value: "x"}}}); err != nil {
		t.Fatalf("canonical document = %v", err)
	}
	if _, err := canonicalYAMLNode(&yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{{Kind: yaml.ScalarNode, Value: "key"}, {Kind: yaml.AliasNode}}}); err == nil {
		t.Fatal("mapping value error was ignored")
	}
}
