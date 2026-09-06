package githubapp

import (
	"context"
	"encoding/base64"
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

func TestGitHubRESTProjectionReaderBuildsAuthoritativeSnapshot(t *testing.T) {
	readme := base64.StdEncoding.EncodeToString([]byte("## WB\n\n[Dashboard](https://sneat.work/bench)\n"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/repos/acme/widgets":
			io.WriteString(w, `{"full_name":"acme/widgets","name":"widgets","default_branch":"main","open_issues_count":4,"html_url":"https://github.com/acme/widgets"}`)
		case r.URL.Path == "/repos/acme/widgets/commits":
			io.WriteString(w, `[{"sha":"0123456789abcdef0123456789abcdef01234567"}]`)
		case r.URL.Path == "/repos/acme/widgets/contents/README.md":
			io.WriteString(w, `{"encoding":"base64","content":"`+readme+`"}`)
		case r.URL.Path == "/search/issues" && strings.Contains(r.URL.Query().Get("q"), "is:open"):
			io.WriteString(w, `{"total_count":2}`)
		case r.URL.Path == "/search/issues":
			io.WriteString(w, `{"total_count":7}`)
		case r.URL.Path == "/repos/acme/widgets/releases":
			io.WriteString(w, `[{"html_url":"https://github.com/acme/widgets/releases/tag/v1"}]`)
		case r.URL.Path == "/orgs/acme":
			io.WriteString(w, `{"login":"acme","public_repos":12,"html_url":"https://github.com/acme"}`)
		case r.URL.Path == "/repos/acme/widgets/pulls":
			io.WriteString(w, `[{"number":9,"merged_at":"2026-09-06T03:00:00Z","html_url":"https://github.com/acme/widgets/pull/9","merge_commit_sha":"abcdef"},{"number":8,"merged_at":null}]`)
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
	if len(snapshot.Organizations) != 1 || snapshot.Organizations[0].ID != "github.com/acme" || snapshot.Organizations[0].Summary.Repositories != 12 {
		t.Fatalf("organizations = %#v", snapshot.Organizations)
	}
	if len(snapshot.LatestMerges) != 1 || snapshot.LatestMerges[0].PullRequest != 9 {
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
