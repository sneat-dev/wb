package githubobserver

import "testing"

func TestPullRequestURLFromOutputIgnoresCLIAdvisories(t *testing.T) {
	t.Parallel()

	const url = "https://github.example.com/acme/widget/pull/42"
	const hint = "- Event subscriptions are in preview and may not be enabled for your account."
	if got := PullRequestURLFromOutput("Creating pull request...\n  " + url + "\n" + hint + "\n"); got != url {
		t.Fatalf("URL = %q, want %q", got, url)
	}
	if got := PullRequestURLFromOutput(hint); got != "" {
		t.Fatalf("output without a PR URL returned %q", got)
	}
}
