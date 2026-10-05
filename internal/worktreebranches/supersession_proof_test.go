package worktreebranches

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"
)

func supersessionProofFixture() (SupersessionReceipt, SupersessionEntry, string) {
	source, target := strings.Repeat("a", 40), strings.Repeat("b", 40)
	replacement, residual := strings.Repeat("c", 40), strings.Repeat("d", 40)
	entry := SupersessionEntry{Task: "task", Repository: "acme/app", Branch: "feature", Base: "main",
		HeadSHA: source, RemoteTargetSHA: target, CanonicalDir: "/repo"}
	receipt := SupersessionReceipt{Version: 1, Repository: entry.Repository, Task: entry.Task,
		Branch: entry.Branch, OriginalHead: source, Target: entry.Base, TargetHead: target,
		Replacements:      []SupersessionReplacement{{Kind: "commit", Ref: "replacement", SHA: replacement}},
		Residuals:         []SupersessionResidual{{Commit: residual, Classification: "replaced", Reason: "reviewed replacement", ReplacementRef: "replacement", Reviewed: true}},
		ResidualsComplete: true,
		Approval:          SupersessionApproval{Actor: "reviewer", Trusted: true, Decision: "approved", ReceiptID: "review-1", ApprovedAt: time.Now().UTC()},
	}
	return receipt, entry, residual
}

func TestSupersessionAcceptsOnlyExactLandedAddedWorkflowAdoption(t *testing.T) {
	t.Parallel()
	const workflowPath = ".github/workflows/provider-tools.yml"
	const base = "base"
	source, target, replacement := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	workflow := "name: provider tools\njobs:\n  check:\n    steps:\n      - uses: actions/checkout@v4\n"
	digest := sha256.Sum256([]byte(workflow))
	valid := func() (SupersessionReceipt, SupersessionEntry) {
		receipt, entry, _ := supersessionProofFixture()
		entry.HeadSHA, entry.RemoteTargetSHA = source, target
		receipt.OriginalHead, receipt.TargetHead = source, target
		receipt.Replacements = []SupersessionReplacement{{Kind: "commit", Ref: replacement, SHA: replacement}}
		receipt.Approval = SupersessionApproval{Actor: "reviewer", Trusted: true, Decision: "approved", ReceiptID: "audit-1", ApprovedAt: time.Now().UTC()}
		receipt.WorkflowAdoptions = []SupersessionWorkflowAdoption{{Path: workflowPath, SourceSHA256: hex.EncodeToString(digest[:]), ReplacementSHA: replacement, Reviewed: true}}
		return receipt, entry
	}

	for _, tc := range []struct {
		name  string
		paths []string
		setup func(*SupersessionReceipt)
		want  string
	}{
		{name: "exact landed workflow bytes", paths: []string{workflowPath}},
		{name: "different target workflow", paths: []string{workflowPath}, want: "differs from the reviewed source bytes", setup: func(_ *SupersessionReceipt) {}},
		{name: "different replacement workflow", paths: []string{workflowPath}, want: "differs from the reviewed source bytes", setup: func(_ *SupersessionReceipt) {}},
		{name: "wrong source hash", paths: []string{workflowPath}, want: "source hash does not match", setup: func(r *SupersessionReceipt) { r.WorkflowAdoptions[0].SourceSHA256 = strings.Repeat("0", 64) }},
		{name: "unlisted replacement", paths: []string{workflowPath}, want: "absent from the replacement inventory", setup: func(r *SupersessionReceipt) { r.WorkflowAdoptions[0].ReplacementSHA = strings.Repeat("e", 40) }},
		{name: "missing trusted approval", paths: []string{workflowPath}, want: "complete trusted-reviewer approval", setup: func(r *SupersessionReceipt) { r.Approval.Trusted = false }},
		{name: "unreviewed workflow", paths: []string{workflowPath}, want: "is not reviewed", setup: func(r *SupersessionReceipt) { r.WorkflowAdoptions[0].Reviewed = false }},
		{name: "additional package dependency change", paths: []string{workflowPath, "package.json"}, want: "must cover every dependency-bearing", setup: func(_ *SupersessionReceipt) {}},
		{name: "modified existing workflow", paths: []string{workflowPath}, want: "not an added workflow eligible", setup: func(_ *SupersessionReceipt) {}},
		{name: "missing adoption evidence", paths: []string{workflowPath}, want: "must cover every dependency-bearing", setup: func(r *SupersessionReceipt) { r.WorkflowAdoptions = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			receipt, entry := valid()
			if tc.setup != nil {
				tc.setup(&receipt)
			}
			service := SupersessionService{Ports: SupersessionPorts{
				ReadGitFileBytes: func(_ context.Context, _, revision, file string) ([]byte, error) {
					if file != workflowPath {
						return nil, errors.New("unexpected blob path")
					}
					if revision == source {
						return []byte(workflow), nil
					}
					if revision == target && tc.name == "different target workflow" || revision == replacement && tc.name == "different replacement workflow" {
						return []byte(workflow + "# drift\n"), nil
					}
					if revision == target || revision == replacement {
						return []byte(workflow), nil
					}
					return nil, errors.New("unexpected blob revision")
				},
				Git: func(_ context.Context, _ string, args ...string) (string, error) {
					switch args[0] {
					case "merge-base":
						return base, nil
					case "diff":
						return strings.Join(tc.paths, "\x00") + "\x00", nil
					case "show":
						object := args[1]
						switch object {
						case base + ":" + workflowPath:
							if tc.name == "modified existing workflow" {
								return "jobs: {}\n", nil
							}
							return "", errors.New("path does not exist")
						case source + ":" + workflowPath:
							return workflow, nil
						case target + ":" + workflowPath:
							if tc.name == "different target workflow" {
								return workflow + "# drift\n", nil
							}
							return workflow, nil
						case replacement + ":" + workflowPath:
							if tc.name == "different replacement workflow" {
								return workflow + "# drift\n", nil
							}
							return workflow, nil
						case base + ":package.json":
							return `{"dependencies":{"demo":"1.0.0"}}`, nil
						case source + ":package.json":
							return `{"dependencies":{"demo":"2.0.0"}}`, nil
						}
					case "ls-tree":
						if tc.name == "modified existing workflow" {
							return workflowPath, nil
						}
						return "", nil
					}
					t.Fatalf("unexpected Git query: %q", args)
					return "", nil
				},
				IsAncestor: func(_ context.Context, _, ancestor, descendant string) (bool, error) {
					return ancestor == replacement && descendant == target, nil
				},
			}}
			got := service.ValidateDependencyDeltasReason(context.Background(), receipt, entry)
			if tc.want == "" && got != "" {
				t.Fatalf("valid workflow adoption refused: %s", got)
			}
			if tc.want != "" && !strings.Contains(got, tc.want) {
				t.Fatalf("refusal = %q, want substring %q", got, tc.want)
			}
		})
	}
}

func TestSupersessionReceiptRefusesIncompleteOrDriftedProof(t *testing.T) {
	t.Parallel()
	boom := errors.New("unreadable Git evidence")
	for _, tc := range []struct {
		name, stage, want string
		change            func(*SupersessionReceipt, *SupersessionEntry)
	}{
		{"source identity", "", "source identity", func(r *SupersessionReceipt, _ *SupersessionEntry) { r.Repository = "other/repo" }},
		{"target identity", "", "target", func(r *SupersessionReceipt, _ *SupersessionEntry) { r.Target = "release" }},
		{"missing replacement", "", "no replacement", func(r *SupersessionReceipt, _ *SupersessionEntry) { r.Replacements = nil }},
		{"dependency evidence without PR", "", "requires original_pr", func(r *SupersessionReceipt, _ *SupersessionEntry) { r.DependencyDeltasComplete = true }},
		{"unsupported replacement", "", "unsupported kind", func(r *SupersessionReceipt, _ *SupersessionEntry) { r.Replacements[0].Kind = "tag" }},
		{"unnamed replacement", "", "no PR or commit reference", func(r *SupersessionReceipt, _ *SupersessionEntry) {
			r.Replacements[0].Ref = ""
			r.Replacements[0].SHA = ""
		}},
		{"invalid commit replacement", "", "no valid landed SHA", func(r *SupersessionReceipt, _ *SupersessionEntry) { r.Replacements[0].SHA = "invalid" }},
		{"invalid PR replacement", "", "no valid landed commit SHA", func(r *SupersessionReceipt, _ *SupersessionEntry) {
			r.Replacements[0].Kind = "pr"
			r.Replacements[0].SHA = "invalid"
		}},
		{"unreadable replacement ancestry", "ancestor error", "verify replacement", nil},
		{"replacement outside target", "ancestor absent", "not contained", nil},
		{"incomplete residual inventory", "", "complete residual inventory", func(r *SupersessionReceipt, _ *SupersessionEntry) { r.ResidualsComplete = false }},
		{"missing residual", "", "no classified residuals", func(r *SupersessionReceipt, _ *SupersessionEntry) { r.Residuals = nil }},
		{"incomplete approval", "", "trusted-reviewer", func(r *SupersessionReceipt, _ *SupersessionEntry) { r.Approval.Trusted = false }},
		{"unreadable residual history", "rev-list error", "enumerate original branch", nil},
		{"no residual history", "rev-list empty", "no residual commits", nil},
		{"invalid residual ID", "", "invalid commit", func(r *SupersessionReceipt, _ *SupersessionEntry) { r.Residuals[0].Commit = "invalid" }},
		{"foreign residual", "", "not a commit in the original", func(r *SupersessionReceipt, _ *SupersessionEntry) { r.Residuals[0].Commit = strings.Repeat("e", 40) }},
		{"duplicate residual", "", "classified more than once", func(r *SupersessionReceipt, _ *SupersessionEntry) { r.Residuals = append(r.Residuals, r.Residuals[0]) }},
		{"unknown classification", "", "unclassified", func(r *SupersessionReceipt, _ *SupersessionEntry) { r.Residuals[0].Classification = "unknown" }},
		{"missing reviewer reason", "", "no reviewer reason", func(r *SupersessionReceipt, _ *SupersessionEntry) { r.Residuals[0].Reason = "" }},
		{"replaced without reference", "", "no replacement reference", func(r *SupersessionReceipt, _ *SupersessionEntry) { r.Residuals[0].ReplacementRef = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			receipt, entry, residual := supersessionProofFixture()
			if tc.change != nil {
				tc.change(&receipt, &entry)
			}
			service := SupersessionService{Ports: SupersessionPorts{
				Git: func(_ context.Context, _ string, args ...string) (string, error) {
					if args[0] == "merge-base" {
						return "base", nil
					}
					if args[0] == "diff" {
						return "", nil
					}
					if args[0] != "rev-list" {
						t.Fatalf("unexpected Git query: %q", args)
					}
					if tc.stage == "rev-list error" {
						return "", boom
					}
					if tc.stage == "rev-list empty" {
						return "", nil
					}
					return residual, nil
				},
				IsAncestor: func(context.Context, string, string, string) (bool, error) {
					if tc.stage == "ancestor error" {
						return false, boom
					}
					return tc.stage != "ancestor absent", nil
				},
			}}
			if got := service.ValidateSupersessionReceipt(context.Background(), receipt, entry); !strings.Contains(got, tc.want) {
				t.Fatalf("refusal = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSupersessionDependencyProofRefusesChangedManifestAndLockfile(t *testing.T) {
	t.Parallel()
	boom := errors.New("unreadable Git evidence")
	for _, tc := range []struct {
		name, stage, want string
		change            func(*SupersessionDependencyDelta)
	}{
		{"target unreadable", "target error", "cannot read exact target manifest", nil},
		{"target malformed", "target malformed", "cannot parse npm manifest", nil},
		{"target mismatch", "target mismatch", "want", nil},
		{"recorded candidate mismatch", "", "does not match recorded candidate", func(d *SupersessionDependencyDelta) { d.RequestedAfter = "^22.7.0"; d.CandidateAfter = "22.7.8" }},
		{"base unreadable", "merge-base error", "cannot derive source PR base", nil},
		{"before unreadable", "before error", "cannot read source PR base manifest", nil},
		{"before mismatch", "before mismatch", "before-version proof", nil},
		{"source unreadable", "source error", "cannot read exact source PR manifest", nil},
		{"source mismatch", "source mismatch", "requested-after proof", nil},
		{"lockfile inventory unreadable", "ls-tree error", "cannot inspect lockfiles", nil},
		{"lockfile absent", "no lockfile", "no applicable lockfile", nil},
		{"lockfile proof incomplete", "", "lockfile proof is incomplete", func(d *SupersessionDependencyDelta) { d.LockfileSelector = "" }},
		{"wrong lockfile", "", "not the exact lockfile", func(d *SupersessionDependencyDelta) { d.Lockfile = "other-lock.json" }},
		{"lockfile unreadable", "lockfile error", "cannot read exact target lockfile", nil},
		{"lockfile version mismatch", "", "does not satisfy requested", func(d *SupersessionDependencyDelta) { d.LockfileVersion = "22.7.8" }},
		{"unsupported yarn", "yarn", "yarn.lock is unsupported", func(d *SupersessionDependencyDelta) { d.Lockfile = "yarn.lock" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			receipt, entry, _ := supersessionProofFixture()
			prURL := "https://github.com/acme/app/pull/17"
			entry.OpenPullRequest = &PullRequest{Number: 17, URL: prURL, Repository: entry.Repository, HeadSHA: entry.HeadSHA}
			receipt.OriginalPR, receipt.OriginalPRNumber, receipt.OriginalPRRepository, receipt.OriginalPRHead = prURL, 17, entry.Repository, entry.HeadSHA
			receipt.DependencyDeltasComplete = true
			delta := SupersessionDependencyDelta{SourcePR: prURL, SourceHead: entry.HeadSHA, Consumer: entry.Repository,
				Ecosystem: "npm", Package: "nx", Manifest: "package.json", Selector: "dependencies.nx",
				Before: "22.6.4", RequestedAfter: "22.7.7", CandidateAfter: "22.7.7",
				Lockfile: "package-lock.json", LockfileSelector: "packages|node_modules/nx|version", LockfileVersion: "22.7.7", Reviewed: true}
			if tc.change != nil {
				tc.change(&delta)
			}
			receipt.DependencyDeltas = []SupersessionDependencyDelta{delta}
			base := strings.Repeat("e", 40)
			service := SupersessionService{Ports: SupersessionPorts{Git: func(_ context.Context, _ string, args ...string) (string, error) {
				switch args[0] {
				case "merge-base":
					if tc.stage == "merge-base error" {
						return "", boom
					}
					return base, nil
				case "ls-tree":
					if tc.stage == "ls-tree error" {
						return "", boom
					}
					if tc.stage == "no lockfile" {
						return "", nil
					}
					if tc.stage == "yarn" {
						return "yarn.lock", nil
					}
					return "package-lock.json", nil
				case "show":
					object := args[1]
					switch object {
					case entry.RemoteTargetSHA + ":package.json":
						if tc.stage == "target error" {
							return "", boom
						}
						if tc.stage == "target malformed" {
							return "{", nil
						}
						if tc.stage == "target mismatch" {
							return `{"dependencies":{"nx":"20.0.0"}}`, nil
						}
						return `{"dependencies":{"nx":"22.7.7"}}`, nil
					case base + ":package.json":
						if tc.stage == "before error" {
							return "", boom
						}
						if tc.stage == "before mismatch" {
							return `{"dependencies":{"nx":"20.0.0"}}`, nil
						}
						return `{"dependencies":{"nx":"22.6.4"}}`, nil
					case entry.HeadSHA + ":package.json":
						if tc.stage == "source error" {
							return "", boom
						}
						if tc.stage == "source mismatch" {
							return `{"dependencies":{"nx":"20.0.0"}}`, nil
						}
						return `{"dependencies":{"nx":"22.7.7"}}`, nil
					case entry.RemoteTargetSHA + ":package-lock.json", entry.RemoteTargetSHA + ":yarn.lock":
						if tc.stage == "lockfile error" {
							return "", boom
						}
						return `{"packages":{"node_modules/nx":{"version":"22.7.7"}}}`, nil
					}
				}
				t.Fatalf("unexpected Git query: %q", args)
				return "", nil
			}}}
			if got := service.ValidateDependencyDeltasReason(context.Background(), receipt, entry); !strings.Contains(got, tc.want) {
				t.Fatalf("refusal = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSupersessionPureProofEdges(t *testing.T) {
	t.Parallel()
	deltas := []SupersessionDependencyDelta{{RequestedAfter: "2"}, {RequestedAfter: "1"}}
	if sorted := sortedDependencyDeltas(deltas); sorted[0].RequestedAfter != "1" || deltas[0].RequestedAfter != "2" {
		t.Fatalf("audit order changed receipt bytes: sorted=%#v, original=%#v", sorted, deltas)
	}
	_, entry, _ := supersessionProofFixture()
	entry.WorktreeDir = "/worktree"
	service := SupersessionService{Ports: SupersessionPorts{
		ReadCampaignMarker: func(string) (bool, error) { return false, nil },
		Git: func(_ context.Context, _ string, args ...string) (string, error) {
			if args[0] == "merge-base" {
				return "base", nil
			}
			return "", errors.New("unreadable diff")
		},
	}}
	if !service.DependencyCampaignWorktree(context.Background(), entry) {
		t.Fatal("unreadable exact diff permitted generic supersession")
	}
	if LockfileEntryContainsVersion("npm", "package-lock.json", "{}", "wrong selector", "22.7.7") {
		t.Fatal("unparseable lockfile selector proved a version")
	}
	if NpmRangeAlternativeSatisfies("v0.0.1", "^0.0.1-rc.1") {
		t.Fatal("ambiguous prerelease range proved a version")
	}
	if !NpmRangeAlternativeSatisfies("v1.2.3", ">=1.0.0") {
		t.Fatal("valid single comparator was refused")
	}
}

func TestDependencyCampaignDetectionUsesSourceDependencyContent(t *testing.T) {
	t.Parallel()
	const base, head = "base", "head"
	for _, tc := range []struct {
		name  string
		paths []string
		base  map[string]string
		head  map[string]string
		want  bool
	}{
		{
			name:  "package scripts and workflow run steps are generic changes",
			paths: []string{"package.json", ".github/workflows/ci.yml"},
			base: map[string]string{
				"package.json":             `{"dependencies":{"nx":"1.2.3"},"scripts":{"test":"go test ./..."}}`,
				".github/workflows/ci.yml": "name: ci\njobs:\n  test:\n    steps:\n      - uses: actions/checkout@v4\n      - run: go test ./...\n",
			},
			head: map[string]string{
				"package.json":             `{"dependencies":{"nx":"1.2.3"},"scripts":{"pretest":"go generate ./...","test":"go test ./..."}}`,
				".github/workflows/ci.yml": "name: ci\njobs:\n  test:\n    steps:\n      - uses: actions/checkout@v4\n      - run: go generate ./...\n      - run: go test ./...\n",
			},
			want: false,
		},
		{
			name:  "direct npm dependency version change",
			paths: []string{"package.json"},
			base:  map[string]string{"package.json": `{"dependencies":{"nx":"1.2.3"}}`},
			head:  map[string]string{"package.json": `{"dependencies":{"nx":"2.0.0"}}`},
			want:  true,
		},
		{
			name:  "Go toolchain directive change is dependency relevant",
			paths: []string{"go.mod"},
			base:  map[string]string{"go.mod": "module example.com/app\n\ngo 1.24\nrequire example.com/lib v1.2.3\n"},
			head:  map[string]string{"go.mod": "module example.com/app\n\ngo 1.25\nrequire example.com/lib v1.2.3\n"},
			want:  true,
		},
		{
			name:  "Go module version change",
			paths: []string{"go.mod"},
			base:  map[string]string{"go.mod": "module example.com/app\n\nrequire example.com/lib v1.2.3\n"},
			head:  map[string]string{"go.mod": "module example.com/app\n\nrequire example.com/lib v2.0.0\n"},
			want:  true,
		},
		{
			name:  "npm override change",
			paths: []string{"package.json"},
			base:  map[string]string{"package.json": `{"dependencies":{"nx":"1.2.3"},"overrides":{"vulnerable":"1.0.0"}}`},
			head:  map[string]string{"package.json": `{"dependencies":{"nx":"1.2.3"},"overrides":{"vulnerable":"1.0.1"}}`},
			want:  true,
		},
		{
			name:  "npm workspace membership change",
			paths: []string{"package.json"},
			base:  map[string]string{"package.json": `{"workspaces":["packages/*"]}`},
			head:  map[string]string{"package.json": `{"workspaces":["packages/*","tools/*"]}`},
			want:  true,
		},
		{
			name:  "Go workspace module membership change",
			paths: []string{"go.work"},
			base:  map[string]string{"go.work": "go 1.24\n\nuse ./app\n"},
			head:  map[string]string{"go.work": "go 1.24\n\nuse (\n ./app\n ./tools\n)\n"},
			want:  true,
		},
		{
			name:  "Yarn resolution change",
			paths: []string{"package.json"},
			base:  map[string]string{"package.json": `{"resolutions":{"vulnerable":"1.0.0"}}`},
			head:  map[string]string{"package.json": `{"resolutions":{"vulnerable":"1.0.1"}}`},
			want:  true,
		},
		{
			name:  "bundled dependency change",
			paths: []string{"package.json"},
			base:  map[string]string{"package.json": `{"bundledDependencies":["one"]}`},
			head:  map[string]string{"package.json": `{"bundledDependencies":["one","two"]}`},
			want:  true,
		},
		{
			name:  "workflow action version change",
			paths: []string{".github/workflows/ci.yml"},
			base:  map[string]string{".github/workflows/ci.yml": "jobs:\n  test:\n    steps:\n      - uses: actions/checkout@v4\n"},
			head:  map[string]string{".github/workflows/ci.yml": "jobs:\n  test:\n    steps:\n      - uses: actions/checkout@v5\n"},
			want:  true,
		},
		{
			name:  "workflow action toolchain input change",
			paths: []string{".github/workflows/ci.yml"},
			base:  map[string]string{".github/workflows/ci.yml": "jobs:\n  test:\n    steps:\n      - uses: actions/setup-go@v5\n        with:\n          go-version: '1.24'\n"},
			head:  map[string]string{".github/workflows/ci.yml": "jobs:\n  test:\n    steps:\n      - uses: actions/setup-go@v5\n        with:\n          go-version: '1.25'\n"},
			want:  true,
		},
		{
			name:  "lockfile change",
			paths: []string{"package-lock.json"},
			base:  map[string]string{"package-lock.json": "before\n"},
			head:  map[string]string{"package-lock.json": "after\n"},
			want:  true,
		},
		{
			name:  "npm shrinkwrap change",
			paths: []string{"npm-shrinkwrap.json"},
			base:  map[string]string{"npm-shrinkwrap.json": "before\n"},
			head:  map[string]string{"npm-shrinkwrap.json": "after\n"},
			want:  true,
		},
		{
			name:  "Go workspace sum change",
			paths: []string{"go.work.sum"},
			base:  map[string]string{"go.work.sum": "example.com/lib v1.2.3 h1:before\n"},
			head:  map[string]string{"go.work.sum": "example.com/lib v1.2.4 h1:after\n"},
			want:  true,
		},
		{
			name:  "malformed dependency manifest fails closed",
			paths: []string{"package.json"},
			base:  map[string]string{"package.json": "{\n"},
			head:  map[string]string{"package.json": "{}\n"},
			want:  true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			contents := map[string]string{}
			for file, value := range tc.base {
				contents[base+":"+file] = value
			}
			for file, value := range tc.head {
				contents[head+":"+file] = value
			}
			service := SupersessionService{Ports: SupersessionPorts{
				ReadCampaignMarker: func(string) (bool, error) { return false, nil },
				Git: func(_ context.Context, _ string, args ...string) (string, error) {
					switch args[0] {
					case "merge-base":
						return base, nil
					case "diff":
						if args[len(args)-2] != base || args[len(args)-1] != head {
							t.Fatalf("diff did not compare source commits from the exact merge base: %q", args)
						}
						return strings.Join(tc.paths, "\x00") + "\x00", nil
					case "show":
						value, ok := contents[args[1]]
						if !ok {
							t.Fatalf("unexpected Git show query: %q", args)
						}
						return value, nil
					default:
						t.Fatalf("unexpected Git query: %q", args)
						return "", nil
					}
				},
			}}
			entry := SupersessionEntry{Task: "sample", Branch: "feature/sample", CanonicalDir: "/repo", WorktreeDir: "/worktree", HeadSHA: head, RemoteTargetSHA: "target"}
			if got := service.DependencyCampaignWorktree(context.Background(), entry); got != tc.want {
				t.Fatalf("dependency campaign = %t, want %t", got, tc.want)
			}
		})
	}
}
