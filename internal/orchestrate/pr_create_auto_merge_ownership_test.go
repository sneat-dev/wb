package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/landinglane"
	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestCreateAutoMergeStageRefusalsReleaseTheObservedOwnedLane(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"initial read", "lane storage", "head pin", "changed files", "link preflight", "commits", "arming", "default method"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			const repository, number, head = "acme/app", "7", "pushed-head"
			const endpoint = "repos/acme/app/pulls/7"
			sentinel := errors.New("owned " + stage + " refusal")
			owner := landinglane.Owner{WBSessionID: "wbs-owned-auto-merge", PID: os.Getpid(), Command: "wb pr create --auto-merge"}
			options := PullRequestCreateOptions{ProjectsRoot: root, AllowUnfenced: true, Lane: LaneGuardRequest{Owner: owner}}
			input := PullRequestCreateResult{Outcome: CreateSuccess, Repository: repository, URL: "https://github.com/acme/app/pull/7", BaseRef: "main", HeadSHA: head, Evidence: map[string]string{"original": "preserved"}}
			if stage == "lane storage" {
				if err := os.WriteFile(filepath.Join(root, ".wb"), []byte("occupied"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var observations []string
			mutationCalls, heldObservations, pullReads := 0, 0, 0
			assertHeld := func() {
				t.Helper()
				home, err := wbhome.Root(root)
				if err != nil {
					t.Fatal(err)
				}
				record, found, err := landinglane.Read(home, repository, "main")
				if err != nil || !found || record.Owner.WBSessionID != owner.WBSessionID || record.Repository != repository || record.Target != "main" {
					t.Fatalf("actual held lane=%+v found=%t err=%v", record, found, err)
				}
				heldObservations++
			}
			if stage == "link preflight" {
				options.LinkPreflight = func(repo string) error {
					if repo != repository {
						t.Fatalf("link preflight repository=%q", repo)
					}
					assertHeld()
					return sentinel
				}
			}
			ctx := githubobserver.WithReader(context.Background(), githubobserver.Reader{
				Get: func(_ context.Context, request githubobserver.GetRequest) (githubobserver.Response, error) {
					if request.Repository != repository || request.Dir != "" {
						t.Fatalf("read identity=%+v", request)
					}
					observations = append(observations, request.Endpoint)
					switch request.Endpoint {
					case endpoint:
						pullReads++
						if stage == "initial read" {
							return githubobserver.Response{}, sentinel
						}
						if stage == "head pin" && pullReads > 1 {
							assertHeld()
							return githubobserver.Response{}, sentinel
						}
						if mutationCalls == 0 && len(observations) > 1 && observations[len(observations)-2] == endpoint+"/commits?per_page=100" {
							assertHeld()
							return githubobserver.Response{Body: []byte(`{"node_id":"PR_owned"}`)}, nil
						}
						observedHead := head
						if stage == "head pin" {
							observedHead = "previous-head"
						}
						return githubobserver.Response{Body: []byte(fmt.Sprintf(`{"number":7,"state":"open","title":"Owned change","head":{"ref":"feature","sha":%q,"repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":"base","repo":{"full_name":"acme/app"}}}`, observedHead))}, nil
					case endpoint + "/files?per_page=100":
						assertHeld()
						if stage == "changed files" {
							return githubobserver.Response{}, sentinel
						}
						return githubobserver.Response{Body: []byte(`[{"filename":"go.sum","status":"modified","patch":"@@ -1 +1 @@\n-old h1:x=\n+new h1:y=\n"}]`)}, nil
					case endpoint + "/commits?per_page=100":
						assertHeld()
						if stage == "commits" {
							return githubobserver.Response{}, sentinel
						}
						return githubobserver.Response{Body: []byte(`[{"sha":"pushed-head","commit":{"message":"Owned commit"}}]`)}, nil
					default:
						t.Fatalf("unexpected hosted read=%+v", request)
						return githubobserver.Response{}, errors.New("unexpected read")
					}
				},
				Read: func(context.Context, string, ...string) ([]byte, error) {
					t.Fatal("unexpected untyped hosted read")
					return nil, sentinel
				},
				Execute: func(_ context.Context, dir string, args ...string) githubobserver.CommandResponse {
					mutationCalls++
					assertHeld()
					if dir != "" || len(args) < 2 || args[0] != "api" || args[1] != "graphql" {
						t.Fatalf("mutation cwd=%q argv=%q", dir, args)
					}
					values := strings.Join(args, "\x00")
					for _, exact := range []string{"-f\x00id=PR_owned", "-f\x00method=MERGE", "-f\x00head=" + head, "-f\x00subject=Owned change (#7)"} {
						if !strings.Contains(values, exact+"\x00") {
							t.Fatalf("mutation missing exact %q: %q", exact, args)
						}
					}
					if !strings.Contains(args[3], "expectedHeadOid:$head") {
						t.Fatalf("arming did not bind expected head: %q", args)
					}
					if stage == "arming" {
						return githubobserver.CommandResponse{ExitCode: 1, Err: sentinel, Stderr: []byte(sentinel.Error())}
					}
					return githubobserver.CommandResponse{ExitCode: 0}
				},
			})
			got, err := createPullRequestAutoMerge(ctx, options, input, repository, number, head)
			expected := []string{endpoint}
			switch stage {
			case "head pin":
				expected = append(expected, endpoint)
			case "changed files", "link preflight":
				expected = append(expected, endpoint+"/files?per_page=100")
			case "commits":
				expected = append(expected, endpoint+"/files?per_page=100", endpoint+"/commits?per_page=100")
			case "arming", "default method":
				expected = append(expected, endpoint+"/files?per_page=100", endpoint+"/commits?per_page=100", endpoint)
			}
			if !reflect.DeepEqual(observations, expected) {
				t.Fatalf("stage observations=%q want=%q", observations, expected)
			}
			if stage == "default method" {
				if err != nil || !got.AutoMergeArmed || !got.Mechanical || got.NextCommand != "wb wait pr acme/app#7 --until closed" {
					t.Fatalf("default method result=%+v, %v", got, err)
				}
			} else {
				if err == nil || got.AutoMergeArmed || got.URL != input.URL || got.Evidence["original"] != "preserved" {
					t.Fatalf("refusal result=%+v, %v", got, err)
				}
				if stage != "lane storage" && stage != "arming" && !errors.Is(err, sentinel) {
					t.Fatalf("lost original read error: %v", err)
				}
				if stage == "arming" && !strings.Contains(err.Error(), sentinel.Error()) {
					t.Fatalf("lost mutation diagnostic: %v", err)
				}
			}
			wantMutations := 0
			if stage == "arming" || stage == "default method" {
				wantMutations = 1
			}
			if mutationCalls != wantMutations {
				t.Fatalf("mutation calls=%d want=%d", mutationCalls, wantMutations)
			}
			if stage != "initial read" && stage != "lane storage" {
				if heldObservations == 0 {
					t.Fatal("lane acquisition was not observed before stage refusal")
				}
				home, homeErr := wbhome.Root(root)
				if homeErr != nil {
					t.Fatal(homeErr)
				}
				record, found, readErr := landinglane.Read(home, repository, "main")
				if readErr != nil || found {
					t.Fatalf("deferred release retained lane=%+v found=%t err=%v", record, found, readErr)
				}
			}
		})
	}
}
