package githubapp

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	githubAppJWTBackdate              = time.Minute
	githubAppJWTLifetime              = 9 * time.Minute
	maxInstallationTokenResponseBytes = 1 << 20
	defaultGitHubAPIBase              = "https://api.github.com"
)

// InstallationTokenSource exchanges a delivery's exact GitHub App
// installation identity for a short-lived installation access token. Hosts
// supply every credential, the HTTP transport, and the clock.
type InstallationTokenSource struct {
	AppID         int64
	PrivateKeyPEM []byte
	Transport     http.RoundTripper
	APIBase       string
	Now           func() time.Time
}

// Token creates a short-lived RS256 GitHub App JWT and exchanges it for the
// installation token identified by delivery.Payload. Errors never include the
// private key, JWT, installation token, or response body.
func (source InstallationTokenSource) Token(ctx context.Context, delivery WebhookDelivery) (string, error) {
	if source.AppID <= 0 {
		return "", errors.New("github app ID must be positive")
	}
	if len(source.PrivateKeyPEM) == 0 {
		return "", errors.New("github app private key is required")
	}
	if source.Transport == nil {
		return "", errors.New("github app token transport is required")
	}
	if source.Now == nil {
		return "", errors.New("github app token clock is required")
	}

	installationID, err := installationIDFromDelivery(delivery)
	if err != nil {
		return "", err
	}
	privateKey, err := parseGitHubAppPrivateKey(source.PrivateKeyPEM)
	if err != nil {
		return "", err
	}
	now := source.Now().UTC()
	appJWT, err := signGitHubAppJWT(privateKey, source.AppID, now)
	if err != nil {
		return "", err
	}

	apiBase := strings.TrimRight(source.APIBase, "/")
	if apiBase == "" {
		apiBase = defaultGitHubAPIBase
	}
	endpoint := apiBase + "/app/installations/" + strconv.FormatInt(installationID, 10) + "/access_tokens"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader([]byte("{}")))
	if err != nil {
		return "", errors.New("create github installation-token request")
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+appJWT)
	request.Header.Set("Content-Type", "application/json")

	response, err := source.Transport.RoundTrip(request)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return "", contextErr
		}
		return "", errors.New("request github installation token: transport failed")
	}
	if response == nil || response.Body == nil {
		return "", errors.New("request github installation token: empty response")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("request github installation token: status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxInstallationTokenResponseBytes+1))
	if err != nil {
		return "", errors.New("read github installation-token response")
	}
	if len(body) > maxInstallationTokenResponseBytes {
		return "", errors.New("github installation-token response is too large")
	}
	var value struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &value); err != nil {
		return "", errors.New("decode github installation-token response")
	}
	if value.Token == "" || strings.TrimSpace(value.Token) != value.Token {
		return "", errors.New("github installation-token response has an invalid token")
	}
	if !value.ExpiresAt.After(now) {
		return "", errors.New("github installation-token response has no future expiry")
	}
	return value.Token, nil
}

func installationIDFromDelivery(delivery WebhookDelivery) (int64, error) {
	var envelope struct {
		Installation *struct {
			ID int64 `json:"id"`
		} `json:"installation"`
	}
	if err := json.Unmarshal(delivery.Payload, &envelope); err != nil {
		return 0, errors.New("webhook payload has no valid installation ID")
	}
	if envelope.Installation == nil || envelope.Installation.ID <= 0 {
		return 0, errors.New("webhook payload has no valid installation ID")
	}
	return envelope.Installation.ID, nil
}

func parseGitHubAppPrivateKey(privateKeyPEM []byte) (*rsa.PrivateKey, error) {
	block, rest := pem.Decode(privateKeyPEM)
	if block == nil || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("github app private key is not one PEM block")
	}
	switch block.Type {
	case "RSA PRIVATE KEY":
		privateKey, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, errors.New("github app private key is not valid PKCS1 RSA")
		}
		return privateKey, nil
	case "PRIVATE KEY":
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, errors.New("github app private key is not valid PKCS8")
		}
		privateKey, ok := parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("github app private key is not RSA")
		}
		return privateKey, nil
	default:
		return nil, errors.New("github app private key has an unsupported PEM type")
	}
}

func signGitHubAppJWT(privateKey *rsa.PrivateKey, appID int64, now time.Time) (string, error) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims := fmt.Sprintf(`{"iat":%d,"exp":%d,"iss":%d}`, now.Add(-githubAppJWTBackdate).Unix(), now.Add(githubAppJWTLifetime).Unix(), appID)
	payload := base64.RawURLEncoding.EncodeToString([]byte(claims))
	signingInput := header + "." + payload
	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", errors.New("sign github app JWT")
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}
