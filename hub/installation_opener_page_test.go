package hub

import (
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// The opener page is on the same origin as the daemon's other pages when a
// daemon hosts the hub. Its one script runs by a nonce minted for the response,
// so nothing else inline could run on it, and the values it embeds are JSON,
// which cannot close the script element.
func TestInstallationOpenerPageRunsOnlyItsOwnScriptByAFreshNonce(t *testing.T) {
	t.Parallel()
	const hostile = `</script><script>alert(1)</script>"';`
	page := func(inert bool) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		writeInstallationOpenerPage(recorder, hostile, hostile, inert)
		return recorder
	}
	nonceOf := regexp.MustCompile(`^default-src 'none'; script-src 'nonce-([A-Z2-7]{26})'; base-uri 'none'; frame-ancestors 'none'$`)

	first, second := page(false), page(false)
	policy := first.Header().Get("Content-Security-Policy")
	match := nonceOf.FindStringSubmatch(policy)
	if match == nil || strings.Contains(policy, "unsafe-inline") {
		t.Fatalf("policy = %q, want one script nonce and no unsafe-inline", policy)
	}
	body := first.Body.String()
	if strings.Count(body, "<script") != 1 || !strings.Contains(body, `<script nonce="`+match[1]+`">`) {
		t.Errorf("the page has scripts other than the one with its nonce: %s", body)
	}
	if strings.Contains(body, "</script><script>alert(1)") || !strings.Contains(body, `</script>`) {
		t.Errorf("a value closed the script element: %s", body)
	}
	if other := nonceOf.FindStringSubmatch(second.Header().Get("Content-Security-Policy")); other == nil || other[1] == match[1] {
		t.Errorf("two responses carry the same nonce: %q", second.Header().Get("Content-Security-Policy"))
	}
	// The inert page has no script at all.
	if inert := page(true); strings.Contains(inert.Body.String(), "<script") || nonceOf.FindString(inert.Header().Get("Content-Security-Policy")) == "" {
		t.Errorf("the inert page = %q with %q", inert.Body.String(), inert.Header().Get("Content-Security-Policy"))
	}
}
