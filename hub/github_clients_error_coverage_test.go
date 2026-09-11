package hub

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"
)

type coverageErrorReader struct{}

func (coverageErrorReader) Read([]byte) (int, error) { return 0, errors.New("read") }

func coverageRSAKey(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

func TestCoverageGitHubAppVerifierFailuresAndDefaults(t *testing.T) {
	key := coverageRSAKey(t)
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	base := GitHubAppInstallationVerifier{Client: &http.Client{}, AppID: 1, PrivateKeyPEM: key, APIBaseURL: "https://api.github.test", Now: func() time.Time { return now }}
	for _, verifier := range []GitHubAppInstallationVerifier{{}, {Client: &http.Client{}, AppID: 0}, {Client: &http.Client{}, AppID: 1}} {
		if _, err := verifier.VerifyAppInstallation(context.Background(), 0); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("invalid verifier returned %v", err)
		}
	}
	badKey := base
	badKey.PrivateKeyPEM = nil
	if _, err := badKey.VerifyAppInstallation(context.Background(), 7); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("bad key returned %v", err)
	}
	// A context.Context that is nil at runtime (but not a literal nil at the
	// call site, so staticcheck's SA1012 does not flag it) exercises
	// http.NewRequestWithContext's own nil-context rejection.
	var nilCtx context.Context
	if _, err := base.VerifyAppInstallation(nilCtx, 7); err == nil {
		t.Fatal("nil request context accepted")
	}
	cases := []struct {
		name      string
		transport roundTripFunc
	}{
		{"transport", func(*http.Request) (*http.Response, error) { return nil, errors.New("offline") }},
		{"status", func(*http.Request) (*http.Response, error) { return jsonResponse(http.StatusNotFound, nil), nil }},
		{"transient status", func(*http.Request) (*http.Response, error) { return jsonResponse(http.StatusBadGateway, nil), nil }},
		{"decode", func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("{"))}, nil
		}},
		{"suspended", func(*http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, map[string]any{"id": 7, "account": map[string]string{"login": "acme", "type": "Organization"}, "repository_selection": "selected", "html_url": "https://github.com/settings/installations/7", "suspended_at": now}), nil
		}},
		{"invalid", func(*http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, map[string]any{"id": 0}), nil
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			verifier := base
			verifier.Client = &http.Client{Transport: test.transport}
			if _, err := verifier.VerifyAppInstallation(context.Background(), 7); err == nil {
				t.Fatal("GitHub App failure accepted")
			}
		})
	}
	defaults := base
	defaults.APIBaseURL = ""
	if defaults.apiBaseURL() != "https://api.github.com" {
		t.Fatal("default GitHub API base missing")
	}
	defaults.APIBaseURL = "http://bad"
	if defaults.apiBaseURL() != "" {
		t.Fatal("invalid GitHub API base accepted")
	}

	ecdsaKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(ecdsaKey)
	if err != nil {
		t.Fatal(err)
	}
	base.PrivateKeyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})
	if _, err := base.appJWT(); err == nil {
		t.Fatal("non-RSA PKCS8 key accepted")
	}

	weak := &rsa.PrivateKey{PublicKey: rsa.PublicKey{N: big.NewInt(3233), E: 17}, D: big.NewInt(2753), Primes: []*big.Int{big.NewInt(61), big.NewInt(53)}}
	base.PrivateKeyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(weak)})
	if _, err := base.appJWT(); err == nil {
		t.Fatal("undersized RSA key signed a SHA-256 JWT")
	}
}

func TestCoverageGitHubOAuthClientErrorsPaginationAndBounds(t *testing.T) {
	base := GitHubOAuthVerifier{Client: &http.Client{}, ClientID: "id", ClientSecret: "secret", CallbackURL: "https://example.test/callback", OAuthBaseURL: "https://github.test", APIBaseURL: "https://api.github.test"}
	// A context.Context that is nil at runtime (but not a literal nil at the
	// call site, so staticcheck's SA1012 does not flag it) exercises
	// http.NewRequestWithContext's own nil-context rejection.
	var nilCtx context.Context
	if _, err := base.exchange(nilCtx, "code"); err == nil {
		t.Fatal("nil OAuth request context accepted")
	}
	if _, err := githubGET[map[string]any](nilCtx, base, "token", "/user", nil); err == nil {
		t.Fatal("nil API request context accepted")
	}
	for _, test := range []struct {
		name        string
		transport   roundTripFunc
		unavailable bool
	}{
		{"transport", func(*http.Request) (*http.Response, error) { return nil, errors.New("offline") }, true},
		{"transient status", func(*http.Request) (*http.Response, error) { return jsonResponse(http.StatusBadGateway, nil), nil }, true},
		{"invalid status", func(*http.Request) (*http.Response, error) { return jsonResponse(http.StatusBadRequest, nil), nil }, false},
		{"decode", func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("{"))}, nil
		}, true},
		{"invalid response", func(*http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, map[string]string{"token_type": "bearer"}), nil
		}, false},
	} {
		t.Run("exchange "+test.name, func(t *testing.T) {
			verifier := base
			verifier.Client = &http.Client{Transport: test.transport}
			_, err := verifier.exchange(context.Background(), "code")
			if err == nil || errors.Is(err, ErrUnavailable) != test.unavailable {
				t.Fatalf("exchange error=%v unavailable=%v", err, test.unavailable)
			}
		})
	}

	identityVerifier := base
	identityVerifier.Client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/login/oauth/access_token":
			return jsonResponse(http.StatusOK, map[string]string{"access_token": "token", "token_type": "bearer"}), nil
		case "/user":
			return jsonResponse(http.StatusOK, map[string]any{"id": 1, "login": "user"}), nil
		default:
			return nil, errors.New("installations offline")
		}
	})}
	if _, err := identityVerifier.VerifyGitHubIdentity(context.Background(), "token"); err == nil {
		t.Fatal("installation list failure accepted")
	}
	userUnavailable := base
	userUnavailable.Client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/login/oauth/access_token" {
			return jsonResponse(http.StatusOK, map[string]string{"access_token": "token", "token_type": "bearer"}), nil
		}
		return jsonResponse(http.StatusBadGateway, nil), nil
	})}
	if _, err := userUnavailable.VerifyGitHubIdentity(context.Background(), "token"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("transient user verification=%v", err)
	}
	invalidIdentity := base
	invalidIdentity.Client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/login/oauth/access_token":
			return jsonResponse(http.StatusOK, map[string]string{"access_token": "token", "token_type": "bearer"}), nil
		case "/user":
			return jsonResponse(http.StatusOK, map[string]any{"id": 1, "login": "user"}), nil
		case "/user/installations":
			return jsonResponse(http.StatusOK, map[string]any{"installations": []any{map[string]any{"id": 7, "account": map[string]string{"login": "acme", "type": "Bot"}, "repository_selection": "selected", "html_url": "https://github.com/settings/installations/7"}}}), nil
		default:
			return jsonResponse(http.StatusOK, map[string]any{"repositories": []any{}}), nil
		}
	})}
	if _, err := invalidIdentity.VerifyGitHubIdentity(context.Background(), "token"); err == nil {
		t.Fatal("invalid verified identity accepted")
	}

	repositoryFailure := base
	repositoryFailure.Client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/user/installations" {
			return jsonResponse(http.StatusOK, map[string]any{"installations": []any{map[string]any{"id": 7, "account": map[string]string{"login": "acme", "type": "Organization"}, "repository_selection": "selected", "html_url": "https://github.com/settings/installations/7"}}}), nil
		}
		return nil, errors.New("repositories offline")
	})}
	if _, err := repositoryFailure.installations(context.Background(), "token"); err == nil {
		t.Fatal("repository list failure accepted")
	}

	suspendedAt := time.Now().UTC()
	paginationCalls := 0
	pagination := base
	pagination.Client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if strings.Contains(request.URL.Path, "/repositories") {
			return jsonResponse(http.StatusOK, map[string]any{"repositories": []any{}}), nil
		}
		paginationCalls++
		if paginationCalls == 1 {
			items := make([]map[string]any, 100)
			for i := range items {
				items[i] = map[string]any{"id": 200 - i, "account": map[string]string{"login": "acme", "type": "Organization"}, "repository_selection": "selected", "html_url": "https://github.com/settings/installations/7"}
			}
			items[50]["suspended_at"] = suspendedAt
			return jsonResponse(http.StatusOK, map[string]any{"installations": items}), nil
		}
		return jsonResponse(http.StatusOK, map[string]any{"installations": []any{map[string]any{"id": 1, "account": map[string]string{"login": "acme", "type": "Organization"}, "repository_selection": "selected", "html_url": "https://github.com/settings/installations/1"}}}), nil
	})}
	installations, err := pagination.installations(context.Background(), "token")
	if err != nil || len(installations) != 101 || installations[0].ID != 1 || installations[len(installations)-1].ID != 200 {
		t.Fatalf("paginated installations=%d err=%v", len(installations), err)
	}
	foundSuspended := false
	for _, installation := range installations {
		foundSuspended = foundSuspended || installation.State == "suspended"
	}
	if !foundSuspended {
		t.Fatal("suspended installation state lost")
	}
	equalIDs := base
	equalIDs.Client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if strings.Contains(request.URL.Path, "/repositories") {
			return jsonResponse(http.StatusOK, map[string]any{"repositories": []any{}}), nil
		}
		return jsonResponse(http.StatusOK, map[string]any{"installations": []any{
			map[string]any{"id": 7, "account": map[string]string{"login": "a", "type": "Organization"}, "repository_selection": "selected", "html_url": "https://github.com/settings/installations/7"},
			map[string]any{"id": 7, "account": map[string]string{"login": "b", "type": "Organization"}, "repository_selection": "selected", "html_url": "https://github.com/settings/installations/7"},
		}}), nil
	})}
	if result, err := equalIDs.installations(context.Background(), "token"); err != nil || len(result) != 2 {
		t.Fatalf("equal ID installation sort=%+v err=%v", result, err)
	}

	tooMany := base
	tooMany.Client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if strings.Contains(request.URL.Path, "/repositories") {
			return jsonResponse(http.StatusOK, map[string]any{"repositories": []any{}}), nil
		}
		items := make([]map[string]any, maxVerifiedInstallations+1)
		for i := range items {
			items[i] = map[string]any{"id": i + 1, "account": map[string]string{"login": "acme", "type": "Organization"}, "repository_selection": "selected", "html_url": "https://github.com/settings/installations/1"}
		}
		return jsonResponse(http.StatusOK, map[string]any{"installations": items}), nil
	})}
	if _, err := tooMany.installations(context.Background(), "token"); err == nil {
		t.Fatal("oversized installation result accepted")
	}
}

func TestCoverageGitHubRepositoryClientErrorsDeduplicationAndBounds(t *testing.T) {
	base := GitHubOAuthVerifier{Client: &http.Client{}, ClientID: "id", ClientSecret: "secret", CallbackURL: "https://example.test/callback", APIBaseURL: "https://api.github.test"}
	apiFailure := base
	apiFailure.Client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("offline") })}
	if _, err := apiFailure.repositories(context.Background(), "token", 7); err == nil {
		t.Fatal("repository API failure accepted")
	}
	invalid := base
	invalid.Client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, map[string]any{"repositories": []any{map[string]any{"id": 0, "full_name": "bad"}}}), nil
	})}
	if _, err := invalid.repositories(context.Background(), "token", 7); err == nil {
		t.Fatal("invalid repository accepted")
	}

	deduplicate := base
	deduplicate.Client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, map[string]any{"repositories": []any{
			map[string]any{"id": 2, "full_name": "Acme/B"},
			map[string]any{"id": 1, "full_name": "Acme/A"},
			map[string]any{"id": 1, "full_name": "Acme/A"},
		}}), nil
	})}
	repositories, err := deduplicate.repositories(context.Background(), "token", 7)
	if err != nil || len(repositories) != 2 || repositories[0].Repository != "github.com/acme/a" {
		t.Fatalf("deduplicated repositories=%+v err=%v", repositories, err)
	}

	tooMany := base
	tooMany.Client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		items := make([]map[string]any, maxInstallationRepos+1)
		for i := range items {
			items[i] = map[string]any{"id": i + 1, "full_name": "acme/repo" + big.NewInt(int64(i)).String()}
		}
		return jsonResponse(http.StatusOK, map[string]any{"repositories": items}), nil
	})}
	if _, err := tooMany.repositories(context.Background(), "token", 7); err == nil {
		t.Fatal("oversized repository result accepted")
	}
}

func TestCoverageGitHubGETAndBoundedJSONFailures(t *testing.T) {
	base := GitHubOAuthVerifier{Client: &http.Client{}, APIBaseURL: "https://api.github.test"}
	for _, test := range []struct {
		name      string
		transport roundTripFunc
	}{
		{"transport", func(*http.Request) (*http.Response, error) { return nil, errors.New("offline") }},
		{"status", func(*http.Request) (*http.Response, error) { return jsonResponse(http.StatusBadGateway, nil), nil }},
		{"non-transient status", func(*http.Request) (*http.Response, error) { return jsonResponse(http.StatusNotFound, nil), nil }},
		{"decode", func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("{"))}, nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			verifier := base
			verifier.Client = &http.Client{Transport: test.transport}
			if _, err := githubGET[map[string]any](context.Background(), verifier, "token", "/user", nil); err == nil {
				t.Fatal("GitHub API failure accepted")
			}
		})
	}
	if err := decodeBoundedJSON(coverageErrorReader{}, &map[string]any{}); err == nil {
		t.Fatal("reader failure accepted")
	}
	large := strings.NewReader(strings.Repeat("x", maxGitHubResponseBytes+1))
	if err := decodeBoundedJSON(large, &map[string]any{}); err == nil {
		t.Fatal("oversized GitHub response accepted")
	}
	var value map[string]any
	if err := decodeBoundedJSON(strings.NewReader(`{"ok":true}`), &value); err != nil || value["ok"] != true {
		t.Fatalf("valid bounded JSON=%+v err=%v", value, err)
	}

	body, _ := json.Marshal(map[string]bool{"ok": true})
	if len(body) == 0 {
		t.Fatal("unexpected empty fixture")
	}
}
