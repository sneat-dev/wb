package defaultbranch

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func TestDefaultBranchWorkflowInspectionRefusesUnverifiedBlobs(t *testing.T) {
	service := New()

	for _, test := range []struct {
		name, listing, blob, want string
		listErr, blobErr          error
	}{
		{name: "absent workflow directory", listErr: errors.New("HTTP 404")},
		{name: "listing unavailable", listErr: errors.New("HTTP 503"), want: "list workflows at source branch"},
		{name: "malformed listing", listing: "{", want: "decode workflow listing"},
		{name: "non-workflow entry skipped", listing: `[{"path":"README.md","sha":"blob","type":"file"}]`},
		{name: "blob unavailable", blobErr: errors.New("HTTP 503"), want: "read workflow"},
		{name: "malformed blob", blob: "{", want: "decode workflow"},
		{name: "invalid encoded bytes", blob: `{"encoding":"base64","content":"%%%"}`, want: "decode workflow .github/workflows/ci.yml content"},
	} {
		t.Run(test.name, func(t *testing.T) {
			oldRead := service.deps.Read
			t.Cleanup(func() { service.deps.Read = oldRead })
			blobCalls := 0
			service.deps.Read = func(_ context.Context, endpoint string) ([]byte, error) {
				switch endpoint {
				case "repos/acme/app/contents/.github/workflows?ref=master":
					listing := test.listing
					if listing == "" {
						listing = `[{"path":".github/workflows/ci.yml","sha":"blob","type":"file"}]`
					}
					return []byte(listing), test.listErr
				case "repos/acme/app/git/blobs/blob":
					blobCalls++
					blob := test.blob
					if blob == "" {
						blob = `{"encoding":"base64","content":"` + base64.StdEncoding.EncodeToString([]byte("on:\n  push:\n    branches: [master]\n")) + `"}`
					}
					return []byte(blob), test.blobErr
				default:
					t.Fatalf("unexpected workflow endpoint %q", endpoint)
					return nil, nil
				}
			}
			repo := Repository{Repository: "acme/app"}
			err := service.inspectDefaultBranchWorkflows(context.Background(), &repo, "master", "main")
			if test.want == "" {
				if err != nil || len(repo.WorkflowFiles) != 0 || blobCalls != 0 {
					t.Fatalf("skipped workflow = %+v err=%v blobReads=%d", repo, err, blobCalls)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.want) || len(repo.WorkflowFiles) != 0 {
				t.Fatalf("unverified workflow = %+v err=%v, want %q", repo, err, test.want)
			}
		})
	}
}

func TestDefaultBranchWorkflowPostReadRequiresExactReplacement(t *testing.T) {
	service := New()

	for _, test := range []struct {
		name, listing, blob, contents, want string
		listErr, blobErr                    error
	}{
		{name: "listing unavailable", listErr: errors.New("HTTP 503"), want: "list workflows after commit"},
		{name: "malformed listing", listing: "{", want: "decode workflows after commit"},
		{name: "planned workflow absent", listing: `[]`, want: "is missing after commit"},
		{name: "blob unavailable", blobErr: errors.New("HTTP 503"), want: "read workflow .github/workflows/ci.yml after commit"},
		{name: "malformed blob", blob: "{", want: "decode workflow .github/workflows/ci.yml after commit"},
		{name: "invalid encoded bytes", blob: `{"encoding":"base64","content":"%%%"}`, want: "decode workflow .github/workflows/ci.yml bytes after commit"},
		{name: "old branch still referenced", contents: "with:\n  ref: \"master\"\n", want: "still references"},
	} {
		t.Run(test.name, func(t *testing.T) {
			oldRead := service.deps.Read
			t.Cleanup(func() { service.deps.Read = oldRead })
			contents := "on:\n  push:\n    branches: [main]\n"
			if test.contents != "" {
				contents = test.contents
			}
			service.deps.Read = func(_ context.Context, endpoint string) ([]byte, error) {
				switch endpoint {
				case "repos/acme/app/contents/.github/workflows?ref=master":
					listing := test.listing
					if listing == "" {
						listing = `[{"path":".github/workflows/ci.yml","sha":"blob","type":"file"}]`
					}
					return []byte(listing), test.listErr
				case "repos/acme/app/git/blobs/blob":
					blob := test.blob
					if blob == "" {
						blob = `{"encoding":"base64","content":"` + base64.StdEncoding.EncodeToString([]byte(contents)) + `"}`
					}
					return []byte(blob), test.blobErr
				default:
					t.Fatalf("unexpected workflow endpoint %q", endpoint)
					return nil, nil
				}
			}
			planned := []Workflow{{Path: ".github/workflows/ci.yml", SHA256After: defaultBranchDigest([]byte(contents))}}
			err := service.verifyDefaultBranchWorkflowBytes(context.Background(), "acme/app", "master", "master", planned)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("post-read = %v, want %q", err, test.want)
			}
		})
	}
}

func TestDefaultBranchSafetyReportsUnverifiedPullsWorkflowsAndRules(t *testing.T) {
	service := New()

	for _, test := range []struct {
		name, endpoint, body, want string
		readErr                    error
	}{
		{name: "pull request inventory unavailable", endpoint: "pulls", readErr: errors.New("HTTP 503"), want: "list open source-default pull requests"},
		{name: "pull request inventory malformed", endpoint: "pulls", body: "{", want: "decode open source-default pull requests"},
		{name: "workflow inventory unavailable", endpoint: "workflows", readErr: errors.New("HTTP 503"), want: "list workflows at source branch"},
		{name: "branch protection unavailable", endpoint: "protection", readErr: errors.New("HTTP 503"), want: "inspect branch impact"},
		{name: "effective rules unavailable", endpoint: "rules", readErr: errors.New("HTTP 503"), want: "inspect branch impact"},
		{name: "effective rules absent", endpoint: "rules", readErr: errors.New("HTTP 404")},
		{name: "effective rules malformed", endpoint: "rules", body: "{", want: "decode effective branch rules"},
	} {
		t.Run(test.name, func(t *testing.T) {
			oldRead := service.deps.Read
			t.Cleanup(func() { service.deps.Read = oldRead })
			var queried []string
			service.deps.Read = func(_ context.Context, endpoint string) ([]byte, error) {
				kind := ""
				switch endpoint {
				case "repos/acme/app/pulls?state=open&head=acme%3Amaster":
					kind = "pulls"
				case "repos/acme/app/contents/.github/workflows?ref=master":
					kind = "workflows"
				case "repos/acme/app/pages":
					kind = "pages"
				case "repos/acme/app/branches/master/protection":
					kind = "protection"
				case "repos/acme/app/rules/branches/master?per_page=100":
					kind = "rules"
				default:
					t.Fatalf("unexpected safety endpoint %q", endpoint)
				}
				queried = append(queried, kind)
				if kind == test.endpoint {
					if test.readErr != nil {
						return nil, test.readErr
					}
					return []byte(test.body), nil
				}
				switch kind {
				case "pulls", "rules":
					return []byte(`[]`), nil
				default:
					return nil, errors.New("HTTP 404")
				}
			}
			result := Repository{Repository: "acme/app", Desired: "main"}
			err := service.defaultBranchSafetyWithOptions(context.Background(), &result, RepoMetadata{ID: 1}, "master", false)
			if test.want == "" {
				if err != nil {
					t.Fatalf("safe absence = %v, queries=%v", err, queried)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("unverified safety = %v, want %q, queries=%v", err, test.want, queried)
			}
			if len(queried) == 0 || queried[len(queried)-1] != test.endpoint {
				t.Fatalf("scenario %q did not stop at failing endpoint: %v", test.name, queried)
			}
		})
	}
}
