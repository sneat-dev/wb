package githubapp

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
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

type installationTokenRoundTripper func(*http.Request) (*http.Response, error)

func (roundTrip installationTokenRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestInstallationTokenSourceExchangesSignedAppJWT(t *testing.T) {
	now := time.Date(2026, time.September, 6, 5, 0, 0, 0, time.UTC)
	privateKey := testRSAPrivateKey(t)
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
	var request *http.Request
	source := InstallationTokenSource{
		AppID:         12345,
		PrivateKeyPEM: privateKeyPEM,
		Transport: installationTokenRoundTripper(func(received *http.Request) (*http.Response, error) {
			request = received
			return installationTokenResponse(http.StatusCreated, `{"token":"installation-token","expires_at":"2026-09-06T06:00:00Z"}`), nil
		}),
		APIBase: "https://github.example/api/v3/",
		Now:     func() time.Time { return now },
	}

	token, err := source.Token(context.Background(), WebhookDelivery{Payload: []byte(`{"installation":{"id":987654321}}`)})
	if err != nil {
		t.Fatal(err)
	}
	if token != "installation-token" {
		t.Fatalf("token = %q", token)
	}
	if request == nil || request.Method != http.MethodPost || request.URL.String() != "https://github.example/api/v3/app/installations/987654321/access_tokens" {
		t.Fatalf("request = %#v", request)
	}
	if request.Header.Get("Accept") != "application/vnd.github+json" || request.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("headers = %#v", request.Header)
	}
	if body, readErr := io.ReadAll(request.Body); readErr != nil || string(body) != "{}" {
		t.Fatalf("body = %q, err=%v", body, readErr)
	}
	verifyAppJWT(t, strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer "), privateKey, now, 12345)
}

func TestInstallationTokenSourceAcceptsPKCS8AndDefaultGitHubAPI(t *testing.T) {
	now := time.Date(2026, time.September, 6, 5, 0, 0, 0, time.UTC)
	privateKey := testRSAPrivateKey(t)
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	source := InstallationTokenSource{
		AppID:         1,
		PrivateKeyPEM: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}),
		Transport: installationTokenRoundTripper(func(request *http.Request) (*http.Response, error) {
			if request.URL.String() != "https://api.github.com/app/installations/2/access_tokens" {
				t.Fatalf("URL = %q", request.URL)
			}
			return installationTokenResponse(http.StatusCreated, `{"token":"token","expires_at":"2026-09-06T05:00:01Z"}`), nil
		}),
		Now: func() time.Time { return now },
	}
	if token, err := source.Token(context.Background(), WebhookDelivery{Payload: []byte(`{"installation":{"id":2}}`)}); err != nil || token != "token" {
		t.Fatalf("token = %q, err=%v", token, err)
	}
}

func TestInstallationTokenSourceRejectsInvalidConfigurationAndDelivery(t *testing.T) {
	valid := validInstallationTokenSource(t)
	cases := map[string]InstallationTokenSource{
		"app ID":      func() InstallationTokenSource { source := valid; source.AppID = 0; return source }(),
		"private key": func() InstallationTokenSource { source := valid; source.PrivateKeyPEM = nil; return source }(),
		"transport":   func() InstallationTokenSource { source := valid; source.Transport = nil; return source }(),
		"clock":       func() InstallationTokenSource { source := valid; source.Now = nil; return source }(),
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := source.Token(context.Background(), WebhookDelivery{Payload: []byte(`{"installation":{"id":1}}`)}); err == nil {
				t.Fatal("expected configuration error")
			}
		})
	}
	for name, payload := range map[string]string{
		"malformed":       `{`,
		"missing":         `{}`,
		"zero":            `{"installation":{"id":0}}`,
		"negative":        `{"installation":{"id":-1}}`,
		"string":          `{"installation":{"id":"1"}}`,
		"repository only": `{"repository":{"id":1}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := valid.Token(context.Background(), WebhookDelivery{Payload: []byte(payload)}); err == nil {
				t.Fatal("expected installation identity error")
			}
		})
	}
}

func TestInstallationTokenSourceRejectsInvalidPrivateKeys(t *testing.T) {
	source := validInstallationTokenSource(t)
	ecdsaKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecdsaDER, err := x509.MarshalPKCS8PrivateKey(ecdsaKey)
	if err != nil {
		t.Fatal(err)
	}
	for name, key := range map[string][]byte{
		"not PEM":       []byte("private-material"),
		"wrong block":   pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: []byte("private-material")}),
		"bad PKCS1":     pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: []byte("private-material")}),
		"bad PKCS8":     pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("private-material")}),
		"non-RSA PKCS8": pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: ecdsaDER}),
		"RSA too small": pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(tinyRSAPrivateKey())}),
	} {
		t.Run(name, func(t *testing.T) {
			source.PrivateKeyPEM = key
			_, tokenErr := source.Token(context.Background(), WebhookDelivery{Payload: []byte(`{"installation":{"id":1}}`)})
			if tokenErr == nil {
				t.Fatal("expected private-key error")
			}
			if strings.Contains(tokenErr.Error(), "private-material") {
				t.Fatalf("error exposes private material: %v", tokenErr)
			}
		})
	}
}

func TestInstallationTokenSourceRejectsRequestAndResponseFailures(t *testing.T) {
	now := time.Date(2026, time.September, 6, 5, 0, 0, 0, time.UTC)
	cases := map[string]struct {
		base      string
		transport installationTokenRoundTripper
	}{
		"invalid API base": {base: "://bad", transport: respondingInstallationTokenTransport(http.StatusCreated, `{"token":"token","expires_at":"2026-09-06T06:00:00Z"}`)},
		"transport": {transport: func(*http.Request) (*http.Response, error) {
			return nil, errors.New("transport failed with secret-jwt")
		}},
		"nil response": {transport: func(*http.Request) (*http.Response, error) { return nil, nil }},
		"nil body": {transport: func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusCreated}, nil
		}},
		"wrong status": {transport: respondingInstallationTokenTransport(http.StatusUnauthorized, "secret-response-body")},
		"read": {transport: func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(failingTokenReader{})}, nil
		}},
		"too large":        {transport: respondingInstallationTokenTransport(http.StatusCreated, strings.Repeat("x", maxInstallationTokenResponseBytes+1))},
		"malformed JSON":   {transport: respondingInstallationTokenTransport(http.StatusCreated, `{`)},
		"missing token":    {transport: respondingInstallationTokenTransport(http.StatusCreated, `{"expires_at":"2026-09-06T06:00:00Z"}`)},
		"whitespace token": {transport: respondingInstallationTokenTransport(http.StatusCreated, `{"token":" token ","expires_at":"2026-09-06T06:00:00Z"}`)},
		"missing expiry":   {transport: respondingInstallationTokenTransport(http.StatusCreated, `{"token":"secret-token"}`)},
		"expired":          {transport: respondingInstallationTokenTransport(http.StatusCreated, `{"token":"secret-token","expires_at":"2026-09-06T05:00:00Z"}`)},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			source := validInstallationTokenSource(t)
			source.APIBase = testCase.base
			source.Transport = testCase.transport
			source.Now = func() time.Time { return now }
			_, err := source.Token(context.Background(), WebhookDelivery{Payload: []byte(`{"installation":{"id":1}}`)})
			if err == nil {
				t.Fatal("expected exchange error")
			}
			for _, secret := range []string{"secret-jwt", "secret-response-body", "secret-token"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error exposes %q: %v", secret, err)
				}
			}
		})
	}
}

func TestInstallationTokenSourcePreservesContextCancellationWithoutTransportErrorDetails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	source := validInstallationTokenSource(t)
	source.Transport = installationTokenRoundTripper(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("secret-jwt")
	})
	if _, err := source.Token(ctx, WebhookDelivery{Payload: []byte(`{"installation":{"id":1}}`)}); !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "secret-jwt") {
		t.Fatalf("error = %v", err)
	}
}

func TestSignGitHubAppJWTRejectsInvalidRSAKey(t *testing.T) {
	key := &rsa.PrivateKey{PublicKey: rsa.PublicKey{N: big.NewInt(1), E: 65537}}
	if _, err := signGitHubAppJWT(key, 1, time.Unix(1, 0)); err == nil {
		t.Fatal("expected signing error")
	}
}

func tinyRSAPrivateKey() *rsa.PrivateKey {
	return &rsa.PrivateKey{
		PublicKey: rsa.PublicKey{N: big.NewInt(3233), E: 17},
		D:         big.NewInt(2753),
		Primes:    []*big.Int{big.NewInt(61), big.NewInt(53)},
	}
}

type failingTokenReader struct{}

func (failingTokenReader) Read([]byte) (int, error) { return 0, errors.New("secret-response-body") }

func respondingInstallationTokenTransport(status int, body string) installationTokenRoundTripper {
	return func(*http.Request) (*http.Response, error) { return installationTokenResponse(status, body), nil }
}

func installationTokenResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Body: io.NopCloser(strings.NewReader(body))}
}

func validInstallationTokenSource(t *testing.T) InstallationTokenSource {
	t.Helper()
	privateKey := testRSAPrivateKey(t)
	return InstallationTokenSource{
		AppID:         1,
		PrivateKeyPEM: pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)}),
		Transport:     respondingInstallationTokenTransport(http.StatusCreated, `{"token":"token","expires_at":"2026-09-06T06:00:00Z"}`),
		Now:           func() time.Time { return time.Date(2026, time.September, 6, 5, 0, 0, 0, time.UTC) },
	}
}

func testRSAPrivateKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	return privateKey
}

func verifyAppJWT(t *testing.T, token string, privateKey *rsa.PrivateKey, now time.Time, appID int64) {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("JWT has %d parts", len(parts))
	}
	decode := func(part string) []byte {
		value, err := base64.RawURLEncoding.DecodeString(part)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	var header struct {
		Algorithm string `json:"alg"`
		Type      string `json:"typ"`
	}
	if err := json.Unmarshal(decode(parts[0]), &header); err != nil || header.Algorithm != "RS256" || header.Type != "JWT" {
		t.Fatalf("header = %#v, err=%v", header, err)
	}
	var claims struct {
		IssuedAt  int64 `json:"iat"`
		ExpiresAt int64 `json:"exp"`
		Issuer    int64 `json:"iss"`
	}
	if err := json.Unmarshal(decode(parts[1]), &claims); err != nil {
		t.Fatal(err)
	}
	if claims.IssuedAt != now.Add(-githubAppJWTBackdate).Unix() || claims.ExpiresAt != now.Add(githubAppJWTLifetime).Unix() || claims.Issuer != appID {
		t.Fatalf("claims = %#v", claims)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&privateKey.PublicKey, crypto.SHA256, digest[:], decode(parts[2])); err != nil {
		t.Fatalf("signature: %v", err)
	}
}
