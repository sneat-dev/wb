package hub

import (
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
	"net/http"
	"strconv"
	"strings"
	"time"
)

type GitHubAppInstallationVerifier struct {
	Client        *http.Client
	AppID         int64
	PrivateKeyPEM []byte
	APIBaseURL    string
	Now           func() time.Time
}

func (verifier GitHubAppInstallationVerifier) VerifyAppInstallation(ctx context.Context, installationID int64) (VerifiedInstallation, error) {
	if verifier.Client == nil || verifier.AppID <= 0 || installationID <= 0 {
		return VerifiedInstallation{}, ErrUnavailable
	}
	token, err := verifier.appJWT()
	if err != nil {
		return VerifiedInstallation{}, ErrUnavailable
	}
	endpoint := verifier.apiBaseURL() + "/app/installations/" + strconv.FormatInt(installationID, 10)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return VerifiedInstallation{}, ErrUnavailable
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err := verifier.Client.Do(request)
	if err != nil {
		return VerifiedInstallation{}, ErrUnavailable
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		if transientGitHubStatus(response.StatusCode) {
			return VerifiedInstallation{}, ErrUnavailable
		}
		return VerifiedInstallation{}, fmt.Errorf("request GitHub App installation: status %d", response.StatusCode)
	}
	var value struct {
		ID      int64 `json:"id"`
		Account struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"account"`
		RepositorySelection string     `json:"repository_selection"`
		HTMLURL             string     `json:"html_url"`
		SuspendedAt         *time.Time `json:"suspended_at"`
	}
	if err := decodeBoundedJSON(response.Body, &value); err != nil {
		return VerifiedInstallation{}, ErrUnavailable
	}
	if value.SuspendedAt != nil {
		return VerifiedInstallation{}, errors.New("decode GitHub App installation")
	}
	installation := VerifiedInstallation{
		ID: value.ID, Account: value.Account.Login, AccountType: value.Account.Type,
		RepositorySelection: value.RepositorySelection, Repositories: []VerifiedRepository{}, State: "installed", ManageURL: value.HTMLURL,
	}
	if err := (VerifiedGitHubIdentity{UserID: 1, Login: "validation", Installations: []VerifiedInstallation{installation}}).Validate(); err != nil {
		return VerifiedInstallation{}, errors.New("invalid GitHub App installation")
	}
	return installation, nil
}

func (verifier GitHubAppInstallationVerifier) appJWT() (string, error) {
	block, _ := pem.Decode(verifier.PrivateKeyPEM)
	if block == nil {
		return "", errors.New("invalid GitHub App private key")
	}
	var key *rsa.PrivateKey
	switch block.Type {
	case "RSA PRIVATE KEY":
		parsed, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return "", errors.New("invalid GitHub App private key")
		}
		key = parsed
	case "PRIVATE KEY":
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return "", errors.New("invalid GitHub App private key")
		}
		var ok bool
		key, ok = parsed.(*rsa.PrivateKey)
		if !ok {
			return "", errors.New("GitHub App private key is not RSA")
		}
	default:
		return "", errors.New("invalid GitHub App private key")
	}
	now := time.Now().UTC()
	if verifier.Now != nil {
		now = verifier.Now().UTC()
	}
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	claims, _ := json.Marshal(map[string]int64{"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": verifier.AppID})
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", errors.New("sign GitHub App JWT")
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func (verifier GitHubAppInstallationVerifier) apiBaseURL() string {
	if verifier.APIBaseURL == "" {
		return "https://api.github.com"
	}
	if !validHTTPSURL(verifier.APIBaseURL) {
		return ""
	}
	return strings.TrimRight(verifier.APIBaseURL, "/")
}
