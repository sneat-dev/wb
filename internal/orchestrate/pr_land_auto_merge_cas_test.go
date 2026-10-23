package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/githubobserver"
)

const mergeAdoptionHead = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type mergeAdoptionObservation struct {
	name, write, readHead                                       string
	armed, alreadyMerged, merged, readFails, nilEvidence, adopt bool
}

// This fixture exercises the lower REST adapter protocol, not hosted GitHub
// or native worktree custody authority. Every request must match exactly.
func observeMergeAdoption(t *testing.T, row mergeAdoptionObservation) (string, *landRefusal, map[string]string, error) {
	t.Helper()
	mutations, reads, hooks := 0, 0, 0
	ctx := githubobserver.WithReader(context.Background(), githubobserver.Reader{
		Execute: func(_ context.Context, dir string, args ...string) githubobserver.CommandResponse {
			mutations++
			want := []string{"api", "--method", "PUT", "repos/acme/app/pulls/7/merge", "-f", "merge_method=merge", "-f", "sha=" + mergeAdoptionHead}
			if row.alreadyMerged || dir != "" || !reflect.DeepEqual(args, want) {
				t.Fatalf("unexpected mutation dir=%q argv=%q", dir, args)
			}
			switch row.write {
			case "success":
				return githubobserver.CommandResponse{Stdout: []byte(`{"merged":true,"sha":"landed"}`)}
			case "unknown":
				return githubobserver.CommandResponse{Err: errors.New("owned transport error"), Stderr: []byte("gh: Bad Gateway (HTTP 502)")}
			default:
				return githubobserver.CommandResponse{Stdout: []byte(`{"merged":false,"message":"head branch was modified"}`)}
			}
		},
		Get: func(_ context.Context, request githubobserver.GetRequest) (githubobserver.Response, error) {
			reads++
			if mutations != 1 || request.Endpoint != "repos/acme/app/pulls/7" || request.Repository != "acme/app" {
				t.Fatalf("unexpected reread order/request: %d %+v", mutations, request)
			}
			if row.readFails {
				return githubobserver.Response{}, errors.New("owned reread failure")
			}
			body, err := json.Marshal(map[string]any{"number": 7, "merged": row.merged, "head": map[string]string{"sha": row.readHead}})
			if err != nil {
				t.Fatal(err)
			}
			return githubobserver.Response{Body: body}, nil
		},
	})
	evidence := map[string]string{"original": "unchanged"}
	if row.nilEvidence {
		evidence = nil
	}
	sha, refusal, err := mergeOrAdoptAutoMerge(ctx, PullRequestLandOptions{Repository: "acme/app", beforeMerge: func() { hooks++ }}, "7", mergeAdoptionHead, "merge", "subject", "body", row.armed, row.alreadyMerged, evidence)
	wantMutations, wantReads := 1, 1
	if row.alreadyMerged {
		wantMutations, wantReads = 0, 0
	}
	if row.write == "success" {
		wantReads = 0
	}
	if mutations != wantMutations || reads != wantReads || hooks != 1 {
		t.Fatalf("mutations=%d reads=%d hooks=%d want=%d/%d/1", mutations, reads, hooks, wantMutations, wantReads)
	}
	return sha, refusal, evidence, err
}

func TestMergeRefusalCannotAdoptAnotherPullRequestHead(t *testing.T) {
	t.Parallel()
	sha, refusal, evidence, err := observeMergeAdoption(t, mergeAdoptionObservation{write: "refused", readHead: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", armed: true, merged: true})
	if err != nil || sha != "" || refusal == nil || refusal.code != LandRefusalHeadMoved || refusal.reason != "the branch moved after its checks were observed: head branch was modified" || refusal.command != "wb pr land acme/app#7" {
		t.Fatalf("foreign-head adoption: sha=%q refusal=%+v err=%v evidence=%v", sha, refusal, err, evidence)
	}
	if !reflect.DeepEqual(evidence, map[string]string{"original": "unchanged"}) {
		t.Fatalf("adoption evidence changed: %v", evidence)
	}
}

func TestMergeOrAdoptPreservesExactOutcomeAndMutationCount(t *testing.T) {
	t.Parallel()
	for _, row := range []mergeAdoptionObservation{
		{name: "same head refusal", write: "refused", readHead: mergeAdoptionHead, armed: true, merged: true, adopt: true},
		{name: "same head refusal nil evidence", write: "refused", readHead: mergeAdoptionHead, armed: true, merged: true, nilEvidence: true, adopt: true},
		{name: "unarmed refusal", write: "refused", readHead: mergeAdoptionHead, merged: true},
		{name: "refusal reread fails", write: "refused", armed: true, readFails: true},
		{name: "refusal not merged", write: "refused", armed: true, readHead: mergeAdoptionHead},
		{name: "ordinary successful merge", write: "success"},
		{name: "unknown outcome same head", write: "unknown", readHead: mergeAdoptionHead, merged: true, adopt: true},
		{name: "unknown outcome different head", write: "unknown", readHead: "other", merged: true},
		{name: "unknown outcome not merged", write: "unknown", readHead: mergeAdoptionHead},
		{name: "unknown outcome reread fails", write: "unknown", readFails: true},
		{name: "already observed GitHub merge", alreadyMerged: true, adopt: true},
		{name: "already observed GitHub merge nil evidence", alreadyMerged: true, nilEvidence: true, adopt: true},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			sha, refusal, evidence, err := observeMergeAdoption(t, row)
			switch {
			case row.adopt:
				if sha != "" || refusal != nil || err != nil {
					t.Fatalf("adoption: sha=%q refusal=%+v err=%v", sha, refusal, err)
				}
			case row.write == "success":
				if sha != "landed" || refusal != nil || err != nil {
					t.Fatalf("merge: sha=%q refusal=%+v err=%v", sha, refusal, err)
				}
			case row.write == "unknown":
				if sha != "" || refusal != nil || !errors.Is(err, githubobserver.ErrTransientMutationOutcomeUnknown) {
					t.Fatalf("unknown: sha=%q refusal=%+v err=%v", sha, refusal, err)
				}
				if row.readFails && !strings.Contains(err.Error(), "verify merge outcome: read pull request acme/app#7: owned reread failure") {
					t.Fatalf("reread diagnostic=%v", err)
				}
			default:
				if sha != "" || err != nil || refusal == nil || refusal.code != LandRefusalHeadMoved || refusal.reason != "the branch moved after its checks were observed: head branch was modified" || refusal.command != "wb pr land acme/app#7" {
					t.Fatalf("refusal: sha=%q refusal=%+v err=%v", sha, refusal, err)
				}
			}
			if !row.nilEvidence {
				want := map[string]string{"original": "unchanged"}
				if row.adopt && row.write != "unknown" {
					want["merged_by"] = "github auto-merge"
				}
				if !reflect.DeepEqual(evidence, want) {
					t.Fatalf("evidence=%v want=%v", evidence, want)
				}
			}
		})
	}
}
