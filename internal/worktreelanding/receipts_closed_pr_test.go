package worktreelanding

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestResolveClosedPullRequestRefusesWhatIsNotAClosedUnmergedPullRequest(t *testing.T) {
	t.Parallel()
	sha := strings.Repeat("c", 40)
	service := func(body string, err error, requested *string) ReceiptService {
		return ReceiptService{Ports: ReceiptPorts{GitHubGet: func(_ context.Context, request GitHubGetRequest) ([]byte, error) {
			if requested != nil {
				*requested = request.Endpoint
			}
			return []byte(body), err
		}}}
	}
	closed := `{"number":6,"html_url":"https://github.com/acme/app/pull/6","state":"closed","merged_at":null,"head":{"ref":"dup","sha":"` + sha + `"},"base":{"ref":"main"}}`
	for _, test := range []struct {
		name, pointer, body, rejection string
		getErr                         error
		wantErr                        string
	}{
		{name: "bare number", pointer: "6", body: closed},
		{name: "hash number", pointer: "#6", body: closed},
		{name: "web url with a suffix", pointer: "https://github.com/Acme/App/pull/6/files", body: closed},
		{name: "another repository", pointer: "https://github.com/other/app/pull/6", body: closed, rejection: `names repository "other/app"`},
		{name: "not a pull request pointer", pointer: "deadbeef", body: closed, rejection: "not a pull request number"},
		{name: "zero", pointer: "0", body: closed, rejection: "not a pull request number"},
		{name: "open", pointer: "6", body: strings.Replace(closed, `"closed"`, `"open"`, 1), rejection: "is not closed"},
		{name: "merged", pointer: "6", body: strings.Replace(closed, `"merged_at":null`, `"merged_at":"2026-10-01T10:00:00Z"`, 1), rejection: "was merged"},
		{name: "no head commit", pointer: "6", body: strings.Replace(closed, sha, "nope", 1), rejection: "invalid head commit"},
		{name: "unreadable", pointer: "6", getErr: errors.New("boom"), wantErr: "boom"},
		{name: "undecodable", pointer: "6", body: "{", wantErr: "decode acme/app pull request 6"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var endpoint string
			pull, rejection, err := service(test.body, test.getErr, &endpoint).ResolveClosedPullRequest(context.Background(), "/w", "acme/app", "main", test.pointer)
			switch {
			case test.wantErr != "":
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("error = %v, want %q", err, test.wantErr)
				}
			case test.rejection != "":
				if err != nil || pull != nil || !strings.Contains(rejection, test.rejection) {
					t.Fatalf("pull=%v rejection=%q err=%v, want rejection %q", pull, rejection, err, test.rejection)
				}
			default:
				if err != nil || rejection != "" || pull == nil || pull.Number != 6 || pull.Head.SHA != sha || endpoint != "repos/acme/app/pulls/6" {
					t.Fatalf("pull=%v rejection=%q err=%v endpoint=%q", pull, rejection, err, endpoint)
				}
			}
		})
	}
}

func TestResolveClosedPullRequestTrustsTheRequestedNumberOverThePayload(t *testing.T) {
	t.Parallel()
	sha := strings.Repeat("d", 40)
	body := `{"number":99,"state":"closed","merged_at":null,"head":{"ref":"dup","sha":"` + sha + `"}}`
	service := ReceiptService{Ports: ReceiptPorts{GitHubGet: func(context.Context, GitHubGetRequest) ([]byte, error) { return []byte(body), nil }}}
	pull, _, err := service.ResolveClosedPullRequest(context.Background(), "/w", "acme/app", "main", "6")
	if err != nil || pull == nil || pull.Number != 6 {
		t.Fatalf("pull=%v err=%v", pull, err)
	}
}
