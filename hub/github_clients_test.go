package hub

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func jsonResponse(status int, value any) *http.Response {
	payload, _ := json.Marshal(value)
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(string(payload))),
	}
}

func TestGitHubOAuthVerifierUsesExactUserInstallationAccess(t *testing.T) {
	requests := make([]*http.Request, 0)
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests = append(requests, request)
		switch request.URL.Path {
		case "/login/oauth/access_token":
			body, _ := io.ReadAll(request.Body)
			if request.Method != http.MethodPost || !strings.Contains(string(body), "code=temporary-code") {
				t.Fatalf("unexpected token request %s %s", request.Method, body)
			}
			return jsonResponse(http.StatusOK, map[string]string{"access_token": "ephemeral-token", "token_type": "bearer"}), nil
		case "/user":
			return jsonResponse(http.StatusOK, map[string]any{"id": 42, "login": "octocat"}), nil
		case "/user/installations":
			return jsonResponse(http.StatusOK, map[string]any{"installations": []any{map[string]any{
				"id": 7, "account": map[string]string{"login": "acme", "type": "Organization"},
				"repository_selection": "selected", "html_url": "https://github.com/settings/installations/7",
			}}}), nil
		case "/user/installations/7/repositories":
			return jsonResponse(http.StatusOK, map[string]any{"repositories": []any{map[string]any{"id": 99, "full_name": "Acme/App"}}}), nil
		default:
			t.Fatalf("unexpected request path %q", request.URL.Path)
			return nil, nil
		}
	})}
	verifier := GitHubOAuthVerifier{
		Client:       client,
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		CallbackURL:  "https://workbench.example/callback",
		OAuthBaseURL: "https://github.test",
		APIBaseURL:   "https://api.github.test",
	}
	token, err := verifier.ExchangeGitHubOAuthCode(context.Background(), "temporary-code")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := verifier.VerifyGitHubIdentity(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if identity.UserID != 42 || len(identity.Installations) != 1 || identity.Installations[0].Repositories[0].ID != 99 || identity.Installations[0].Repositories[0].Repository != "github.com/acme/app" {
		t.Fatalf("identity=%+v", identity)
	}
	for _, request := range requests[1:] {
		if request.Header.Get("Authorization") != "Bearer ephemeral-token" {
			t.Fatalf("missing transient bearer on %s", request.URL)
		}
	}
}

func TestGitHubOAuthVerifierRejectsFailures(t *testing.T) {
	valid := GitHubOAuthVerifier{Client: &http.Client{}, ClientID: "id", ClientSecret: "secret", CallbackURL: "https://example.test/callback"}
	if valid.Validate() != nil || valid.oauthBaseURL() != "https://github.com" || valid.apiBaseURL() != "https://api.github.com" {
		t.Fatal("valid default verifier rejected")
	}
	invalid := []GitHubOAuthVerifier{
		{},
		{Client: &http.Client{}, ClientID: "id", ClientSecret: "secret", CallbackURL: "http://example.test/callback"},
		{Client: &http.Client{}, ClientID: "id", ClientSecret: "secret", CallbackURL: "https://example.test/callback", OAuthBaseURL: "http://github.test"},
		{Client: &http.Client{}, ClientID: "id", ClientSecret: "secret", CallbackURL: "https://example.test/callback", APIBaseURL: "relative"},
	}
	for _, verifier := range invalid {
		if verifier.Validate() == nil {
			t.Fatalf("invalid verifier accepted: %+v", verifier)
		}
	}
	if _, err := valid.VerifyGitHubIdentity(context.Background(), "bad code"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("invalid code returned %v", err)
	}
	if _, err := valid.ExchangeGitHubOAuthCode(context.Background(), "bad code"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("invalid exchange code returned %v", err)
	}

	cases := []struct {
		name      string
		transport roundTripFunc
	}{
		{"transport", func(*http.Request) (*http.Response, error) { return nil, errors.New("offline") }},
		{"token status", func(*http.Request) (*http.Response, error) { return jsonResponse(http.StatusForbidden, nil), nil }},
		{"bad token", func(*http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, map[string]string{"error": "bad_verification_code"}), nil
		}},
		{"bad user", func(request *http.Request) (*http.Response, error) {
			if request.URL.Path == "/login/oauth/access_token" {
				return jsonResponse(http.StatusOK, map[string]string{"access_token": "token", "token_type": "bearer"}), nil
			}
			return jsonResponse(http.StatusOK, map[string]any{"id": 0, "login": ""}), nil
		}},
		{"installations status", func(request *http.Request) (*http.Response, error) {
			if request.URL.Path == "/login/oauth/access_token" {
				return jsonResponse(http.StatusOK, map[string]string{"access_token": "token", "token_type": "bearer"}), nil
			}
			if request.URL.Path == "/user" {
				return jsonResponse(http.StatusOK, map[string]any{"id": 1, "login": "user"}), nil
			}
			return jsonResponse(http.StatusInternalServerError, nil), nil
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			verifier := valid
			verifier.OAuthBaseURL = "https://github.test"
			verifier.APIBaseURL = "https://api.github.test"
			verifier.Client = &http.Client{Transport: test.transport}
			if _, err := verifier.VerifyGitHubIdentity(context.Background(), "token"); err == nil {
				t.Fatal("failure accepted")
			}
		})
	}
}

func TestGitHubAppVerifierAuthenticatesAndRejectsSuspension(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	pkcs1 := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/app/installations/7" || !strings.HasPrefix(request.Header.Get("Authorization"), "Bearer ey") {
			t.Fatalf("unexpected app request: %s %+v", request.URL, request.Header)
		}
		return jsonResponse(http.StatusOK, map[string]any{
			"id": 7, "account": map[string]string{"login": "acme", "type": "Organization"},
			"repository_selection": "selected", "html_url": "https://github.com/settings/installations/7",
		}), nil
	})}
	verifier := GitHubAppInstallationVerifier{Client: client, AppID: 123, PrivateKeyPEM: pkcs1, APIBaseURL: "https://api.github.test", Now: func() time.Time { return now }}
	installation, err := verifier.VerifyAppInstallation(context.Background(), 7)
	if err != nil || installation.ID != 7 || installation.State != "installed" {
		t.Fatalf("installation=%+v err=%v", installation, err)
	}
	pkcs8Bytes, _ := x509.MarshalPKCS8PrivateKey(key)
	verifier.PrivateKeyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8Bytes})
	if _, err := verifier.appJWT(); err != nil {
		t.Fatalf("PKCS8 rejected: %v", err)
	}

	for _, keyPEM := range [][]byte{nil, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("bad")}), pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: []byte("bad")}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("bad")})} {
		verifier.PrivateKeyPEM = keyPEM
		if _, err := verifier.appJWT(); err == nil {
			t.Fatalf("invalid key accepted: %q", keyPEM)
		}
	}
}
