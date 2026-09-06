package githubapp

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fixedGitHubToken struct{}

func (fixedGitHubToken) Token(context.Context, WebhookDelivery) (string, error) {
	return "installation-token", nil
}

type failingGitHubToken struct{}

func (failingGitHubToken) Token(context.Context, WebhookDelivery) (string, error) {
	return "", errors.New("token unavailable")
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) Do(request *http.Request) (*http.Response, error) { return fn(request) }

func TestWorkbenchRootREADMEProvidesPublicOptIn(t *testing.T) {
	if _, err := VerifyPublicEligibility(
		"github.com/sneat-dev/wb",
		"https://github.com/sneat-dev/wb/blob/0123456789abcdef0123456789abcdef01234567/README.md",
		"## WB\n\nPublic Workbench projections are opt-in from this repository's root README: [Workbench dashboard](https://sneat.work/bench).\n",
		time.Date(2026, 9, 6, 4, 0, 0, 0, time.UTC),
	); err != nil {
		t.Fatalf("root README opt-in rejected: %v", err)
	}
}

func TestGitHubRESTProjectionReaderBuildsAuthoritativeSnapshot(t *testing.T) {
	readme := base64.StdEncoding.EncodeToString([]byte("## WB\n\n[Dashboard](https://sneat.work/bench)\n"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/repos/acme/widgets":
			_, _ = io.WriteString(w, `{"full_name":"acme/widgets","name":"widgets","default_branch":"main","open_issues_count":4,"html_url":"https://github.com/acme/widgets"}`)
		case r.URL.Path == "/repos/acme/widgets/commits":
			_, _ = io.WriteString(w, `[{"sha":"0123456789abcdef0123456789abcdef01234567"}]`)
		case r.URL.Path == "/repos/acme/widgets/contents/README.md":
			_, _ = io.WriteString(w, `{"encoding":"base64","content":"`+readme+`"}`)
		case r.URL.Path == "/search/issues" && strings.Contains(r.URL.Query().Get("q"), "is:open"):
			_, _ = io.WriteString(w, `{"total_count":2}`)
		case r.URL.Path == "/search/issues":
			_, _ = io.WriteString(w, `{"total_count":7}`)
		case r.URL.Path == "/repos/acme/widgets/releases":
			_, _ = io.WriteString(w, `[{"html_url":"https://github.com/acme/widgets/releases/tag/v1"}]`)
		case r.URL.Path == "/repos/acme/widgets/pulls":
			_, _ = io.WriteString(w, `[{"number":9,"merged_at":"2026-09-06T03:00:00Z","html_url":"https://github.com/acme/widgets/pull/9","merge_commit_sha":"abcdef"},{"number":8,"merged_at":null}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	reader := GitHubRESTProjectionReader{HTTP: server.Client(), Tokens: fixedGitHubToken{}, APIBase: server.URL, Now: func() time.Time { return time.Date(2026, 9, 6, 4, 0, 0, 0, time.UTC) }}
	snapshot, err := reader.RefreshAuthoritativeProjection(context.Background(), WebhookDelivery{ID: "delivery", Event: "push", Payload: []byte(`{"repository":{"full_name":"acme/widgets"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Repositories) != 1 || !snapshot.Repositories[0].PublicOptIn || snapshot.Repositories[0].Summary.OpenIssues != 2 || snapshot.Repositories[0].Summary.OpenPulls != 2 || snapshot.Repositories[0].Summary.MergedPulls != 7 || snapshot.Repositories[0].Summary.Releases != 1 {
		t.Fatalf("repositories = %#v", snapshot.Repositories)
	}
	if snapshot.Repositories[0].PublicEligibility == nil || !strings.Contains(snapshot.Repositories[0].PublicEligibility.READMEURL, "/0123456789abcdef0123456789abcdef01234567/") {
		t.Fatalf("eligibility = %#v", snapshot.Repositories[0].PublicEligibility)
	}
	if len(snapshot.Organizations) != 0 {
		t.Fatalf("organizations = %#v", snapshot.Organizations)
	}
	if snapshot.LatestMerges == nil || !snapshot.LatestMerges.PublicOptIn || snapshot.LatestMerges.Repository != "github.com/acme/widgets" || len(snapshot.LatestMerges.Entries) != 1 || snapshot.LatestMerges.Entries[0].PullRequest != 9 {
		t.Fatalf("merges = %#v", snapshot.LatestMerges)
	}
}

func TestGitHubRESTProjectionReaderRequiresIdentityAndDependencies(t *testing.T) {
	reader := GitHubRESTProjectionReader{}
	for name, delivery := range map[string]WebhookDelivery{
		"missing identity": {Payload: []byte(`{"action":"push"}`)},
		"invalid identity": {Repository: "acme/widgets"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := reader.RefreshAuthoritativeProjection(context.Background(), delivery); err == nil {
				t.Fatal("expected identity error")
			}
		})
	}
	if _, err := (GitHubRESTProjectionReader{HTTP: http.DefaultClient}).RefreshAuthoritativeProjection(context.Background(), WebhookDelivery{Repository: "github.com/acme/widgets"}); err == nil {
		t.Fatal("expected missing token source error")
	}
	if _, err := (GitHubRESTProjectionReader{HTTP: http.DefaultClient, Tokens: failingGitHubToken{}}).RefreshAuthoritativeProjection(context.Background(), WebhookDelivery{Repository: "github.com/acme/widgets"}); err == nil {
		t.Fatal("expected token error")
	}
	reader = GitHubRESTProjectionReader{}
	if err := reader.Refresh(context.Background(), WebhookDelivery{}); err == nil {
		t.Fatal("expected Refresh identity error")
	}
	if _, err := reader.RefreshProjection(context.Background(), WebhookDelivery{}); err == nil {
		t.Fatal("expected RefreshProjection identity error")
	}
	if reader.now().IsZero() {
		t.Fatal("default clock returned zero")
	}
}

func TestGitHubRESTProjectionReaderHTTPFailures(t *testing.T) {
	reader := GitHubRESTProjectionReader{HTTP: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("transport down")
	})}
	if err := reader.get(context.Background(), "token", "/x", &struct{}{}); err == nil {
		t.Fatal("expected transport error")
	}
	reader.HTTP = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusBadGateway, Status: "502 Bad Gateway", Body: io.NopCloser(strings.NewReader("upstream"))}, nil
	})
	if err := reader.get(context.Background(), "token", "/x", &struct{}{}); err == nil {
		t.Fatal("expected status error")
	}
	reader.HTTP = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader("{"))}, nil
	})
	if err := reader.get(context.Background(), "token", "/x", &struct{}{}); err == nil {
		t.Fatal("expected decode error")
	}
}

func TestGitHubRESTProjectionReaderRejectsReachableFailures(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	readme := base64.StdEncoding.EncodeToString([]byte("## WB\n\n[Dashboard](https://sneat.work/bench)\n"))
	for _, mode := range []string{"repository", "identity", "commits", "badsha", "readme-error", "encoding", "base64", "content-error", "nooptin", "open", "merged", "releases", "negative", "default", "pulls"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fail := func() { http.Error(w, "boom", http.StatusBadGateway) }
				switch {
				case r.URL.Path == "/repos/acme/widgets":
					if mode == "repository" {
						fail()
						return
					}
					if mode == "identity" {
						_, _ = io.WriteString(w, `{"full_name":"acme/other","name":"widgets","default_branch":"main"}`)
						return
					}
					branch := "main"
					if mode == "default" {
						branch = ""
					}
					openIssues := "4"
					if mode == "negative" {
						openIssues = "1"
					}
					_, _ = io.WriteString(w, `{"full_name":"acme/widgets","name":"widgets","default_branch":"`+branch+`","open_issues_count":`+openIssues+`}`)
				case r.URL.Path == "/repos/acme/widgets/commits":
					if mode == "readme-error" {
						fail()
						return
					}
					if mode == "commits" {
						_, _ = io.WriteString(w, `[]`)
						return
					}
					if mode == "badsha" {
						_, _ = io.WriteString(w, `[{"sha":"short"}]`)
						return
					}
					_, _ = io.WriteString(w, `[{"sha":"`+sha+`"}]`)
				case r.URL.Path == "/repos/acme/widgets/contents/README.md":
					if mode == "content-error" {
						fail()
						return
					}
					if mode == "encoding" {
						_, _ = io.WriteString(w, `{"encoding":"utf-8","content":"x"}`)
						return
					}
					if mode == "base64" {
						_, _ = io.WriteString(w, `{"encoding":"base64","content":"%%%"}`)
						return
					}
					content := readme
					if mode == "nooptin" {
						content = base64.StdEncoding.EncodeToString([]byte("# About\n"))
					}
					_, _ = io.WriteString(w, `{"encoding":"base64","content":"`+content+`"}`)
				case r.URL.Path == "/search/issues" && strings.Contains(r.URL.Query().Get("q"), "is:open"):
					if mode == "open" {
						fail()
						return
					}
					_, _ = io.WriteString(w, `{"total_count":2}`)
				case r.URL.Path == "/search/issues":
					if mode == "merged" {
						fail()
						return
					}
					_, _ = io.WriteString(w, `{"total_count":7}`)
				case r.URL.Path == "/repos/acme/widgets/releases":
					if mode == "releases" {
						fail()
						return
					}
					_, _ = io.WriteString(w, `[]`)
				case r.URL.Path == "/repos/acme/widgets/pulls":
					if mode == "pulls" || mode == "nooptin" {
						fail()
						return
					}
					_, _ = io.WriteString(w, `[]`)
				default:
					fail()
				}
			}))
			defer server.Close()
			reader := GitHubRESTProjectionReader{HTTP: server.Client(), Tokens: fixedGitHubToken{}, APIBase: server.URL, Now: func() time.Time { return time.Unix(1, 0) }}
			snapshot, err := reader.RefreshAuthoritativeProjection(context.Background(), WebhookDelivery{Repository: "github.com/acme/widgets"})
			wantError := mode != "nooptin" && mode != "negative" && mode != "org-empty"
			if wantError && err == nil {
				t.Fatal("expected reader failure")
			}
			if !wantError && err != nil {
				t.Fatalf("reader error = %v", err)
			}
			if mode == "nooptin" && (snapshot.LatestMerges == nil || snapshot.LatestMerges.PublicOptIn || len(snapshot.LatestMerges.Entries) != 0) {
				t.Fatalf("private latest merges = %#v", snapshot.LatestMerges)
			}
		})
	}
	if err := (GitHubRESTProjectionReader{HTTP: http.DefaultClient, APIBase: "://bad", Tokens: fixedGitHubToken{}}).get(context.Background(), "token", "/x", &struct{}{}); err == nil {
		t.Fatal("expected invalid URL error")
	}
}

type combinedProjectionReader struct{ calls int }

func (reader *combinedProjectionReader) RefreshProjection(context.Context, WebhookDelivery) (ProjectionSnapshot, error) {
	return validProjectionSnapshot(), nil
}
func (reader *combinedProjectionReader) RefreshAuthoritativeProjection(context.Context, WebhookDelivery) (ProjectionSnapshot, error) {
	reader.calls++
	return validProjectionSnapshot(), nil
}

func TestProjectionEngineUsesRequestScopedAuthoritativeReaderHandoff(t *testing.T) {
	reader := &combinedProjectionReader{}
	engine, store, writer := newProjector()
	engine.Reader = reader
	delivery := WebhookDelivery{ID: "handoff", Event: "push", Payload: []byte("payload")}
	if _, err := engine.Process(context.Background(), delivery, projectorSignature(engine.WebhookSecret, delivery.Payload)); err != nil {
		t.Fatal(err)
	}
	if reader.calls != 1 || writer.repositories != 1 || !store.committed {
		t.Fatalf("handoff calls=%d writes=%d committed=%v", reader.calls, writer.repositories, store.committed)
	}
}
