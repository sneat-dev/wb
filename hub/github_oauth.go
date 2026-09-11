package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sneat-dev/wb/api/githubapp/repositoryevent"
)

const maxGitHubResponseBytes = 8 << 20

type GitHubOAuthVerifier struct {
	Client       *http.Client
	ClientID     string
	ClientSecret string
	CallbackURL  string
	OAuthBaseURL string
	APIBaseURL   string
}

func (verifier GitHubOAuthVerifier) Validate() error {
	if verifier.Client == nil || strings.TrimSpace(verifier.ClientID) == "" || strings.TrimSpace(verifier.ClientSecret) == "" || !validHTTPSURL(verifier.CallbackURL) {
		return ErrUnavailable
	}
	if verifier.oauthBaseURL() == "" || verifier.apiBaseURL() == "" {
		return ErrUnavailable
	}
	return nil
}

func (verifier GitHubOAuthVerifier) ExchangeGitHubOAuthCode(ctx context.Context, code string) (string, error) {
	if verifier.Validate() != nil || code == "" || len(code) > 2048 || strings.ContainsAny(code, " \t\r\n") {
		return "", ErrUnavailable
	}
	return verifier.exchange(ctx, code)
}

func (verifier GitHubOAuthVerifier) VerifyGitHubIdentity(ctx context.Context, token string) (VerifiedGitHubIdentity, error) {
	if verifier.Validate() != nil || token == "" || len(token) > 2048 || strings.ContainsAny(token, " \t\r\n") {
		return VerifiedGitHubIdentity{}, ErrUnavailable
	}
	user, err := githubGET[struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	}](ctx, verifier, token, "/user", nil)
	if errors.Is(err, ErrUnavailable) {
		return VerifiedGitHubIdentity{}, ErrUnavailable
	}
	if err != nil || user.ID <= 0 || strings.TrimSpace(user.Login) == "" {
		return VerifiedGitHubIdentity{}, errors.New("verify GitHub user")
	}
	installations, err := verifier.installations(ctx, token)
	if err != nil {
		return VerifiedGitHubIdentity{}, err
	}
	identity := VerifiedGitHubIdentity{UserID: user.ID, Login: user.Login, Installations: installations}
	if err := identity.Validate(); err != nil {
		return VerifiedGitHubIdentity{}, err
	}
	return identity, nil
}

func (verifier GitHubOAuthVerifier) exchange(ctx context.Context, code string) (string, error) {
	form := url.Values{"client_id": {verifier.ClientID}, "client_secret": {verifier.ClientSecret}, "code": {code}, "redirect_uri": {verifier.CallbackURL}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, verifier.oauthBaseURL()+"/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", ErrUnavailable
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := verifier.Client.Do(request)
	if err != nil {
		return "", ErrUnavailable
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		if transientGitHubStatus(response.StatusCode) {
			return "", ErrUnavailable
		}
		return "", fmt.Errorf("exchange GitHub OAuth code: status %d", response.StatusCode)
	}
	var value struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		Error       string `json:"error"`
	}
	if err := decodeBoundedJSON(response.Body, &value); err != nil {
		return "", ErrUnavailable
	}
	if value.Error != "" || value.AccessToken == "" || len(value.AccessToken) > 2048 || strings.ContainsAny(value.AccessToken, " \t\r\n") || !strings.EqualFold(value.TokenType, "bearer") {
		return "", errors.New("exchange GitHub OAuth code: invalid response")
	}
	return value.AccessToken, nil
}

func (verifier GitHubOAuthVerifier) installations(ctx context.Context, token string) ([]VerifiedInstallation, error) {
	type installation struct {
		ID                  int64                        `json:"id"`
		Account             struct{ Login, Type string } `json:"account"`
		RepositorySelection string                       `json:"repository_selection"`
		HTMLURL             string                       `json:"html_url"`
		SuspendedAt         *time.Time                   `json:"suspended_at"`
	}
	result := make([]VerifiedInstallation, 0)
	for page := 1; ; page++ {
		value, err := githubGET[struct {
			Installations []installation `json:"installations"`
		}](ctx, verifier, token, "/user/installations", url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}})
		if err != nil {
			return nil, err
		}
		for _, item := range value.Installations {
			repositories, err := verifier.repositories(ctx, token, item.ID)
			if err != nil {
				return nil, err
			}
			state := "installed"
			if item.SuspendedAt != nil {
				state = "suspended"
			}
			result = append(result, VerifiedInstallation{ID: item.ID, Account: item.Account.Login, AccountType: item.Account.Type, RepositorySelection: item.RepositorySelection, Repositories: repositories, State: state, ManageURL: item.HTMLURL})
			if len(result) > maxVerifiedInstallations {
				return nil, errors.New("GitHub installation result exceeds bound")
			}
		}
		if len(value.Installations) < 100 {
			break
		}
	}
	slices.SortFunc(result, func(a, b VerifiedInstallation) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	return result, nil
}

func (verifier GitHubOAuthVerifier) repositories(ctx context.Context, token string, installationID int64) ([]VerifiedRepository, error) {
	type repository struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
	}
	result := make([]VerifiedRepository, 0)
	path := "/user/installations/" + strconv.FormatInt(installationID, 10) + "/repositories"
	for page := 1; ; page++ {
		value, err := githubGET[struct {
			Repositories []repository `json:"repositories"`
		}](ctx, verifier, token, path, url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}})
		if err != nil {
			return nil, err
		}
		for _, item := range value.Repositories {
			canonical := "github.com/" + strings.ToLower(strings.TrimSpace(item.FullName))
			if item.ID <= 0 || repositoryevent.ValidateRepository(canonical) != nil {
				return nil, errors.New("GitHub returned invalid repository identity")
			}
			result = append(result, VerifiedRepository{ID: item.ID, Repository: canonical})
			if len(result) > maxInstallationRepos {
				return nil, errors.New("GitHub repository result exceeds bound")
			}
		}
		if len(value.Repositories) < 100 {
			break
		}
	}
	slices.SortFunc(result, func(a, b VerifiedRepository) int { return strings.Compare(a.Repository, b.Repository) })
	result = slices.CompactFunc(result, func(a, b VerifiedRepository) bool { return a.ID == b.ID || a.Repository == b.Repository })
	return result, nil
}

func githubGET[T any](ctx context.Context, verifier GitHubOAuthVerifier, token, path string, query url.Values) (T, error) {
	var zero T
	endpoint := verifier.apiBaseURL() + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return zero, errors.New("create GitHub API request")
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err := verifier.Client.Do(request)
	if err != nil {
		return zero, ErrUnavailable
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		if transientGitHubStatus(response.StatusCode) {
			return zero, ErrUnavailable
		}
		return zero, fmt.Errorf("request GitHub API: status %d", response.StatusCode)
	}
	if err := decodeBoundedJSON(response.Body, &zero); err != nil {
		return zero, ErrUnavailable
	}
	return zero, nil
}

func transientGitHubStatus(status int) bool {
	return status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

func decodeBoundedJSON(reader io.Reader, out any) error {
	limited := io.LimitReader(reader, maxGitHubResponseBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil || len(data) > maxGitHubResponseBytes {
		return errors.New("response exceeds bound")
	}
	return json.Unmarshal(data, out)
}

func (verifier GitHubOAuthVerifier) oauthBaseURL() string {
	if verifier.OAuthBaseURL == "" {
		return "https://github.com"
	}
	if !validHTTPSURL(verifier.OAuthBaseURL) {
		return ""
	}
	return strings.TrimRight(verifier.OAuthBaseURL, "/")
}

func (verifier GitHubOAuthVerifier) apiBaseURL() string {
	if verifier.APIBaseURL == "" {
		return "https://api.github.com"
	}
	if !validHTTPSURL(verifier.APIBaseURL) {
		return ""
	}
	return strings.TrimRight(verifier.APIBaseURL, "/")
}
