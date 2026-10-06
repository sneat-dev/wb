package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/githubchecks"
	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/landinglane"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/wbhome"
)

const landOwnerHead = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const landOwnerBase = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

// landOwnerProtocol is hosted-adapter protocol evidence only. Native tests
// supply head/ancestry observations from their own real Git DAG and preserve
// that distinction; these JSON responses never claim live GitHub authority.
type landOwnerProtocol struct {
	t                                          *testing.T
	head, target, branch                       string
	files, commits                             []byte
	draft, required, policyUnavailable, merged bool
	failEndpoint                               string
	failure                                    error
	beforeGet                                  func(githubobserver.GetRequest)
	parentBody                                 func(string) []byte
	compareBody                                func(string) []byte
	mutation                                   func([]string) githubobserver.CommandResponse
	calls                                      []string
	comments, merges, arms, checkReads         int
}

func newLandOwnerProtocol(t *testing.T) *landOwnerProtocol {
	t.Helper()
	return &landOwnerProtocol{t: t, head: landOwnerHead, target: landOwnerBase, branch: "feature/source", required: true, failure: errors.New("owned provider failure"), files: []byte(`[{"filename":"go.sum","status":"modified","patch":"@@ -1 +1 @@\n-old h1:x=\n+new h1:y=\n"}]`), commits: []byte(`[{"sha":"` + landOwnerHead + `","commit":{"message":"owned change"}}]`)}
}

func (p *landOwnerProtocol) context() context.Context {
	return githubobserver.WithReader(context.Background(), githubobserver.Reader{
		Get: p.get,
		Read: func(_ context.Context, dir string, args ...string) ([]byte, error) {
			p.t.Fatalf("unexpected hosted Read dir=%q argv=%q", dir, args)
			return nil, p.failure
		},
		Execute: p.execute,
	})
}

func (p *landOwnerProtocol) get(_ context.Context, request githubobserver.GetRequest) (githubobserver.Response, error) {
	if request.Dir != "" || request.Repository != "acme/app" || request.FreshWindow != 0 {
		p.t.Fatalf("hosted read identity %+v", request)
	}
	p.calls = append(p.calls, request.Endpoint)
	if p.beforeGet != nil {
		p.beforeGet(request)
	}
	if request.Endpoint == p.failEndpoint {
		return githubobserver.Response{}, p.failure
	}
	var body []byte
	switch request.Endpoint {
	case "repos/acme/app/pulls/7":
		raw, err := json.Marshal(map[string]any{"number": 7, "node_id": "PR_owned", "state": "open", "draft": p.draft, "mergeable": true, "mergeable_state": "clean", "title": "Owned change", "html_url": "https://github.com/acme/app/pull/7", "merged": p.merged, "merge_commit_sha": p.head, "head": map[string]any{"ref": p.branch, "sha": p.head, "repo": map[string]string{"full_name": "acme/app"}}, "base": map[string]any{"ref": "main", "repo": map[string]string{"full_name": "acme/app"}}})
		if err != nil {
			p.t.Fatal(err)
		}
		body = raw
	case "repos/acme/app/pulls/7/files?per_page=100":
		body = p.files
	case "repos/acme/app/pulls/7/commits?per_page=100":
		body = p.commits
	case "repos/acme/app/branches/main":
		if p.policyUnavailable {
			return githubobserver.Response{}, errors.New("HTTP 403: Resource not accessible by integration")
		}
		if p.required {
			body = []byte(`{"protected":true,"protection":{"required_status_checks":{"checks":[{"context":"CI","app_id":42}]}}}`)
		} else {
			body = []byte(`{"protected":false,"protection":{}}`)
		}
	case "repos/acme/app/branches/main/protection/required_status_checks":
		body = []byte(`{"strict":true,"contexts":[],"checks":[{"context":"CI","app_id":42}]}`)
	case "repos/acme/app/rules/branches/main?per_page=100":
		if p.policyUnavailable {
			return githubobserver.Response{}, errors.New("HTTP 403: Resource not accessible by integration")
		}
		body = []byte(`[]`)
	case "repos/acme/app/git/ref/heads/main":
		body = []byte(fmt.Sprintf(`{"object":{"sha":%q}}`, p.target))
	default:
		switch {
		case strings.HasPrefix(request.Endpoint, "repos/acme/app/compare/"):
			if p.compareBody != nil {
				body = p.compareBody(strings.TrimPrefix(request.Endpoint, "repos/acme/app/compare/"))
			} else {
				body = []byte(fmt.Sprintf(`{"status":"ahead","base_commit":{"sha":%q},"merge_base_commit":{"sha":%q}}`, p.target, p.target))
			}
		case strings.HasSuffix(request.Endpoint, "/check-runs?per_page=100"):
			if request.Head != p.head {
				p.t.Fatalf("check identity %+v, head=%s", request, p.head)
			}
			p.checkReads++
			body = []byte(`{"total_count":1,"check_runs":[{"name":"CI","status":"completed","conclusion":"success","html_url":"https://ci.example/owned/1","app":{"id":42}}]}`)
		case strings.HasSuffix(request.Endpoint, "/status?per_page=100"):
			body = []byte(`{"total_count":0,"statuses":[]}`)
		case strings.Contains(request.Endpoint, "/actions/runs?"):
			body = []byte(`{"total_count":0,"workflow_runs":[]}`)
		case strings.HasPrefix(request.Endpoint, "repos/acme/app/commits/") && p.parentBody != nil:
			body = p.parentBody(strings.TrimPrefix(request.Endpoint, "repos/acme/app/commits/"))
		default:
			p.t.Fatalf("unexpected hosted Get %+v", request)
		}
	}
	return githubobserver.Response{Body: body}, nil
}

func (p *landOwnerProtocol) execute(_ context.Context, dir string, args ...string) githubobserver.CommandResponse {
	if dir != "" {
		p.t.Fatalf("mutation cwd %q", dir)
	}
	p.calls = append(p.calls, strings.Join(args, " "))
	switch {
	case len(args) > 3 && reflect.DeepEqual(args[:4], []string{"api", "--method", "POST", "repos/acme/app/issues/7/comments"}):
		p.comments++
		if len(args) != 6 || args[4] != "-f" || !strings.HasPrefix(args[5], "body=") || !strings.Contains(args[5], "Reviewed-Head: "+p.head) {
			p.t.Fatalf("review binding argv=%q", args)
		}
	case len(args) > 3 && reflect.DeepEqual(args[:4], []string{"api", "--method", "PUT", "repos/acme/app/pulls/7/merge"}):
		p.merges++
		if !strings.Contains(strings.Join(args, "\x00"), "-f\x00sha="+p.head) {
			p.t.Fatalf("merge lost exact head lease %q", args)
		}
	case len(args) > 1 && args[0] == "api" && args[1] == "graphql":
		p.arms++
	default:
		p.t.Fatalf("unexpected hosted Execute %q", args)
	}
	if p.mutation != nil {
		return p.mutation(args)
	}
	return githubobserver.CommandResponse{Stdout: []byte(`{"merged":false,"message":"head branch was modified"}`)}
}

func TestLandOwnedLaneAndReviewStageRefusalsPreserveOrdering(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, code, errorText, failEndpoint string
		nonmechanical                       bool
	}{
		{name: "lane conflict", code: LandRefusalLandingLaneHeld},
		{name: "lane storage", errorText: ".wb"},
		{name: "owned lane files fail", errorText: "owned provider failure", failEndpoint: "repos/acme/app/pulls/7/files?per_page=100"},
		{name: "draft", code: LandRefusalDraft},
		{name: "kept unfenced target", code: LandRefusalUnfencedTarget},
		{name: "files fail", errorText: "owned provider failure", failEndpoint: "repos/acme/app/pulls/7/files?per_page=100"},
		{name: "CI approval", code: LandRefusalUnapprovedPatch, nonmechanical: true},
		{name: "review file unreadable", errorText: "read --review-comment-file", nonmechanical: true},
		{name: "review post fail", errorText: "post review comment on acme/app#7", nonmechanical: true},
		{name: "commits fail", errorText: "owned provider failure", failEndpoint: "repos/acme/app/pulls/7/commits?per_page=100"},
		{name: "prearm unverifiable review", errorText: "owned provider failure", failEndpoint: "repos/acme/app/pulls/7/commits?per_page=100", nonmechanical: true},
	}
	for _, row := range tests {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			options := landOptions(&landFixture{projects: root})
			options.NoAutoMerge = true
			options.NoUpdateBranch = true
			p := newLandOwnerProtocol(t)
			p.failEndpoint = row.failEndpoint
			if row.nonmechanical {
				p.files = []byte(`[{"filename":"app.go","status":"modified","patch":"@@ -1 +1 @@\n-old\n+new\n"}]`)
			}
			own := landinglane.Owner{WBSessionID: "wbs-owned-land", PID: os.Getpid(), Command: "wb pr land"}
			var competing landinglane.Record
			if row.name == "lane conflict" {
				// The private registry authenticates this actually live test process
				// as the competing session. Lane custody compares session IDs;
				// the acquiring session has a distinct ID even with the same PID.
				home, err := wbhome.EnsureRoot(root)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := session.Register(filepath.Join(home, session.DirName), session.Record{PID: os.Getpid(), WBSessionID: "wbs-competing-land", Runtime: "test", Model: "protocol-test", StartedAt: time.Now().UTC()}); err != nil {
					t.Fatal(err)
				}
				competing, err = acquireLandingLane(root, "acme/app", "main", LaneGuardRequest{Owner: landinglane.Owner{WBSessionID: "wbs-competing-land", PID: os.Getpid(), Command: "competing land"}})
				if err != nil {
					t.Fatal(err)
				}
				options.Lane = LaneGuardRequest{Owner: own}
			}
			if row.name == "lane storage" {
				if err := os.WriteFile(filepath.Join(root, ".wb"), []byte("occupied"), 0600); err != nil {
					t.Fatal(err)
				}
				options.Lane = LaneGuardRequest{Owner: own}
			}
			if row.name == "owned lane files fail" {
				options.Lane = LaneGuardRequest{Owner: own}
				p.beforeGet = func(req githubobserver.GetRequest) {
					if req.Endpoint != p.failEndpoint {
						return
					}
					home, err := wbhome.Root(root)
					if err != nil {
						t.Fatal(err)
					}
					record, found, err := landinglane.Read(home, "acme/app", "main")
					if err != nil || !found || record.Owner.WBSessionID != own.WBSessionID {
						t.Fatalf("files boundary did not own real lane %+v %t %v", record, found, err)
					}
				}
			}
			switch row.name {
			case "draft":
				p.draft = true
			case "kept unfenced target":
				p.required = false
				options.KeepCommits = []string{landOwnerHead}
				options.Reason = "keep history"
				options.MergeMethod = "squash"
				options.MergeMethodExplicit = true
			case "CI approval":
				options.ApprovedBy = "ci"
			case "review file unreadable":
				options.ApprovedBy = "owned-model@codex@owned-review"
				options.ReviewCommentFile = filepath.Join(root, "absent-review")
			case "review post fail":
				options.ApprovedBy = "owned-model@codex@owned-review"
				options.ReviewComment = "owned review"
				p.mutation = func([]string) githubobserver.CommandResponse {
					return githubobserver.CommandResponse{ExitCode: 1, Err: p.failure, Stderr: []byte(p.failure.Error())}
				}
			case "prearm unverifiable review":
				review := filepath.Join(root, "review.txt")
				if err := os.WriteFile(review, []byte("Reviewed-Head: "+landOwnerBase+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
				options.ApprovedBy = review
			}
			result, err := landPullRequest(p.context(), options)
			if row.errorText != "" {
				if err == nil || !strings.Contains(err.Error(), row.errorText) {
					t.Fatalf("partial result %+v, error %v", result, err)
				}
			} else if err != nil || result.Outcome != LandRefused || result.RefusalCode != row.code {
				t.Fatalf("refusal %+v, err %v", result, err)
			}
			if result.HeadSHA != landOwnerHead || result.Repository != "acme/app" || result.PullRequest != 7 {
				t.Fatalf("partial identity %+v", result)
			}
			if p.merges != 0 || p.arms != 0 || p.checkReads != 0 {
				t.Fatalf("early refusal crossed publication/wait: %v", p.calls)
			}
			wantComments := 0
			if row.name == "review post fail" {
				wantComments = 1
			}
			if p.comments != wantComments {
				t.Fatalf("comments %d, calls %v", p.comments, p.calls)
			}
			if row.name == "owned lane files fail" {
				home, err := wbhome.Root(root)
				if err != nil {
					t.Fatal(err)
				}
				_, found, err := landinglane.Read(home, "acme/app", "main")
				if err != nil || found || result.LaneOwner == nil || result.LaneOwner.Owner.WBSessionID != own.WBSessionID {
					t.Fatalf("owned lane not released %+v, found=%t err=%v", result.LaneOwner, found, err)
				}
			}
			if row.name == "lane conflict" {
				home, err := wbhome.Root(root)
				if err != nil {
					t.Fatal(err)
				}
				record, found, err := landinglane.Read(home, "acme/app", "main")
				if err != nil || !found || !reflect.DeepEqual(record, competing) || result.SanctionedCommand != "wb session recall wbs-competing-land" {
					t.Fatalf("competitor changed %+v %t %v result %+v", record, found, err, result)
				}
			}
			if row.name == "prearm unverifiable review" {
				if result.ReviewBound == nil || *result.ReviewBound || !strings.Contains(result.Evidence["review"], "review-unverified:") || result.AutoMergeArmed {
					t.Fatalf("prearm review note %+v", result)
				}
			}
			// Hosted reads must stop at the failing stage; each row's last observation
			// names the real first refusal rather than a later fabricated failure.
			if row.failEndpoint != "" && p.calls[len(p.calls)-1] != row.failEndpoint {
				t.Fatalf("read ordering %v", p.calls)
			}
		})
	}
}

func TestLandPassedChecksKeepPolicyAndReviewEvidenceOnLeasedMergeRefusal(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"exact merge lease", "unverifiable review both stages", "unavailable policy"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			options := landOptions(&landFixture{projects: root})
			options.NoAutoMerge = true
			options.NoUpdateBranch = true
			p := newLandOwnerProtocol(t)
			if kind == "unavailable policy" {
				p.policyUnavailable = true
				options.AllowUnfenced = true
			}
			if kind == "unverifiable review both stages" {
				p.files = []byte(`[{"filename":"app.go","patch":"@@ -1 +1 @@\n-old\n+new\n"}]`)
				review := filepath.Join(root, "review.txt")
				if err := os.WriteFile(review, []byte("Reviewed-Head: "+landOwnerBase+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
				options.ApprovedBy = review
			}
			result, err := landPullRequest(p.context(), options)
			if err != nil || result.Outcome != LandRefused || result.RefusalCode != LandRefusalHeadMoved || result.HeadSHA != p.head || p.merges != 1 || p.checkReads < 2 || p.comments != 0 || p.arms != 0 || result.BranchDeleted || len(result.CleanedTasks) > 0 {
				t.Fatalf("leased refusal %+v err %v calls %v", result, err, p.calls)
			}
			if result.Checks == nil || result.Checks.Status != githubchecks.PullRequestWaitPassed {
				t.Fatalf("lost actual waiter protocol receipt %+v", result)
			}
			if kind == "unavailable policy" && (!strings.Contains(result.Evidence["required_check_policy"], "HTTP 403") || result.Evidence["allow_unfenced"] != "true") {
				t.Fatalf("authority gap hidden %+v", result)
			}
			if kind == "unverifiable review both stages" && (result.ReviewBound == nil || *result.ReviewBound || !strings.Contains(result.Evidence["review"], "no local checkout")) {
				t.Fatalf("postwait unverified binding %+v", result)
			}
		})
	}
}

func TestLandReviewBindingLeavesProvenSameHeadEvidenceUntouched(t *testing.T) {
	t.Parallel()
	result := PullRequestLandResult{ReviewBound: boolPtr(true), Evidence: map[string]string{"review": "already bound", "other": "retained"}}
	options := PullRequestLandOptions{Repository: "acme/app", ProjectsRoot: t.TempDir()}
	before := map[string]string{"review": "already bound", "other": "retained"}
	if refusal := applyPullRequestLandReviewBinding(context.Background(), options, githubchecks.PullRequestView{}, landOwnerHead, landOwnerHead, false, "7", &result); refusal != nil || result.ReviewBound == nil || !*result.ReviewBound || !reflect.DeepEqual(result.Evidence, before) {
		t.Fatalf("same-head binding mutated %+v refusal %+v", result, refusal)
	}
}

func TestLandInitialPullRequestReadFailurePreservesUnobservedReceipt(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	options := landOptions(&landFixture{projects: root})
	p := newLandOwnerProtocol(t)
	p.failEndpoint = "repos/acme/app/pulls/7"
	result, err := landPullRequest(p.context(), options)
	if !errors.Is(err, p.failure) {
		t.Fatalf("initial read lost original cause: %v", err)
	}
	if result.SchemaVersion != 1 || result.Verb != "pr land" || result.Repository != "acme/app" || !result.Kept || len(result.ManualEquivalent) != 8 || result.ManualEquivalent[0] != "gh pr view 7 --repo acme/app" || result.ManualEquivalent[1] != "gh api repos/acme/app/pulls/7/files" {
		t.Fatalf("initial selector receipt lost: %+v", result)
	}
	// The selector remains in manual commands; no successful view has yet
	// supplied an authoritative number, URL, head or merge identity.
	if result.PullRequest != 0 || result.URL != "" || result.Title != "" || result.HeadRef != "" || result.HeadSHA != "" || result.BaseRef != "" || result.MergeSHA != "" || len(result.Evidence) != 0 {
		t.Fatalf("initial read fabricated observed identity: %+v", result)
	}
	if !reflect.DeepEqual(p.calls, []string{"repos/acme/app/pulls/7"}) || p.comments != 0 || p.arms != 0 || p.checkReads != 0 || p.merges != 0 || result.AutoMergeArmed || result.Checks != nil || result.LaneOwner != nil || result.BranchDeleted || len(result.CleanedTasks) != 0 {
		t.Fatalf("initial refusal crossed authority boundary: %+v calls %v", result, p.calls)
	}
	entries, readErr := os.ReadDir(root)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("initial read created authority state: %v err %v", entries, readErr)
	}
}
